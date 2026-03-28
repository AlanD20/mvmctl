"""Image download and conversion utilities."""

import logging
import shutil
import subprocess
import tempfile
import urllib.request
from collections.abc import Callable
from pathlib import Path
from urllib.error import URLError

from mvmctl.constants import (
    CONST_MEBIBYTE_BYTES,
    CONST_SECTOR_SIZE_BYTES,
    DEFAULT_FC_KERNEL_ARCH,
    DEFAULT_FIRECRACKER_CI_VERSION,
    DEFAULT_IMAGE_IMPORT_SIZE_MIB,
    FIRECRACKER_CI_IMAGE_LIST_URL,
    FIRECRACKER_CI_KERNEL_S3_BASE,
    HTTP_TIMEOUT_SHA256_FETCH_S,
    HTTP_USER_AGENT,
)
from mvmctl.exceptions import ConfigError, ImageError
from mvmctl.models.image import ImageImportSpec, ImageSpec
from mvmctl.utils.http import download_file as _download_file
from mvmctl.utils.process import privileged_cmd
from mvmctl.utils.template import render_optional_template, render_template

logger = logging.getLogger(__name__)

_SECTOR_SIZE = CONST_SECTOR_SIZE_BYTES

download_file = _download_file


def _get_int(value: object, default: int = 0) -> int:
    """Safely extract an integer from a partition dict value."""
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    if isinstance(value, str):
        try:
            return int(value)
        except ValueError:
            return default
    return default


class _NoPartitionTable:
    """Sentinel: raw image has no partition table and should be used as-is."""


_NO_PARTITION_TABLE = _NoPartitionTable()


def convert_qcow2_to_raw(
    qcow2_path: Path,
    raw_path: Path,
) -> bool:
    """Convert qcow2 to raw using qemu-img.

    Args:
        qcow2_path: Source qcow2 file
        raw_path: Destination raw file

    Returns:
        True if successful

    Raises:
        ImageError: On conversion failure or missing qemu-img
    """
    try:
        logger.info("Converting %s to raw...", qcow2_path.name)

        subprocess.run(
            [
                "qemu-img",
                "convert",
                "-m",
                "16",
                "-f",
                "qcow2",
                "-O",
                "raw",
                str(qcow2_path),
                str(raw_path),
            ],
            capture_output=True,
            text=True,
            check=True,
        )

        logger.info("Converted to %s", raw_path.name)
        return True

    except subprocess.CalledProcessError as e:
        detail = e.stderr.strip() if e.stderr else "no details"
        raise ImageError(f"qemu-img conversion failed: {detail}") from e
    except FileNotFoundError as e:
        raise ImageError("qemu-img not found. Install qemu-utils.") from e


def _parse_partitions_sfdisk(
    raw_path: Path,
    partition: int | None,
) -> tuple[list[dict[str, object]], int | None] | _NoPartitionTable | None:
    """Parse partition table using sfdisk.

    Returns:
        ``(partitions, requested_partition)`` on success where partitions is a list
        of partition dicts with 'start', 'size', 'type' keys.
        ``_NO_PARTITION_TABLE`` sentinel if image has no partition table,
        or ``None`` if sfdisk is unavailable or fails.

    Raises:
        ImageError: On extraction failure (propagated from outer handler).
    """
    import json as json_mod

    try:
        sfdisk_result = subprocess.run(
            ["sfdisk", "--json", str(raw_path)],
            capture_output=True,
            text=True,
            check=True,
        )
        table = json_mod.loads(sfdisk_result.stdout)
        partitions_raw = table.get("partitiontable", {}).get("partitions", [])

        if not partitions_raw:
            return _NO_PARTITION_TABLE

        # Convert to standard partition dicts, validating types
        partitions: list[dict[str, object]] = []
        for p in partitions_raw:
            start = p.get("start")
            size = p.get("size")
            if not isinstance(start, (int, float)) or not isinstance(size, (int, float)):
                raise ImageError("Failed to parse partition table")
            partitions.append(
                {
                    "start": int(start),
                    "size": int(size),
                    "type": p.get("type", ""),
                    "node": p.get("node", ""),
                }
            )

        return partitions, partition

    except (
        FileNotFoundError,
        subprocess.CalledProcessError,
        json_mod.JSONDecodeError,
        KeyError,
    ):
        return None


def _parse_partitions_fdisk(
    raw_path: Path,
    partition: int | None,
) -> tuple[list[dict[str, object]], int | None] | _NoPartitionTable:
    """Parse partition table using fdisk (fallback when sfdisk unavailable).

    Returns:
        ``(partitions, requested_partition)`` on success where partitions is a list
        of partition dicts with 'start', 'size', 'type' keys.
        ``_NO_PARTITION_TABLE`` sentinel if image has no partition table.

    Raises:
        ImageError: If fdisk output cannot be parsed.
    """
    import re

    result = subprocess.run(
        ["fdisk", "-l", str(raw_path)],
        capture_output=True,
        text=True,
        check=False,
    )

    partition_lines = [
        line
        for line in result.stdout.split("\n")
        if re.match(rf"^{re.escape(str(raw_path))}p?\d", line)
    ]

    if not partition_lines:
        return _NO_PARTITION_TABLE

    # Parse fdisk output into partition dicts
    # Format: Device Boot Start End Sectors Size Id Type
    partitions: list[dict[str, object]] = []
    for line in partition_lines:
        parts = line.split()
        if len(parts) >= 6:
            try:
                start = int(parts[3])
                size = int(parts[4])
                part_type = parts[5] if len(parts) > 5 else ""
                partitions.append(
                    {
                        "start": start,
                        "size": size,
                        "type": part_type,
                    }
                )
            except (ValueError, IndexError):
                # Found a line that looks like a partition but can't be parsed
                raise ImageError("Failed to parse fdisk output for partition sectors")

    if not partitions:
        return _NO_PARTITION_TABLE

    return partitions, partition


def _detect_and_rename_fs(output_path: Path) -> Path:
    """Detect filesystem type via blkid and rename output file accordingly.

    Args:
        output_path: Path to the extracted partition image.

    Returns:
        The (possibly renamed) output path.
    """
    try:
        blkid_result = subprocess.run(
            ["blkid", "-o", "value", "-s", "TYPE", str(output_path)],
            capture_output=True,
            text=True,
            check=False,
        )
        fs_type = blkid_result.stdout.strip()
        if fs_type:
            ext_map = {"ext4": ".ext4", "btrfs": ".btrfs", "xfs": ".xfs"}
            ext = ext_map.get(fs_type, ".img")
            final_path = output_path.with_suffix(ext)
            output_path.rename(final_path)
            output_path = final_path
            logger.info("Detected filesystem: %s", fs_type)
    except FileNotFoundError:
        pass
    return output_path


def get_filesystem_uuid(image_path: Path) -> str | None:
    try:
        blkid_result = subprocess.run(
            ["blkid", "-p", "-s", "UUID", "-o", "value", str(image_path)],
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError:
        return None

    fs_uuid = blkid_result.stdout.strip()
    return fs_uuid if fs_uuid else None


_COPY_CHUNK_SIZE = CONST_MEBIBYTE_BYTES  # 1 MiB


def _copy_bytes(
    src: Path,
    dst: Path,
    offset: int,
    count: int | None,
) -> None:
    """Copy bytes from *src* starting at *offset* into *dst*.

    Args:
        src: Source file path.
        dst: Destination file path (created/overwritten).
        offset: Byte offset to start reading from in *src*.
        count: Number of bytes to copy, or ``None`` to copy to EOF.
    """
    with open(src, "rb") as fin, open(dst, "wb") as fout:
        fin.seek(offset)
        remaining = count
        while True:
            chunk_size = _COPY_CHUNK_SIZE
            if remaining is not None:
                chunk_size = min(chunk_size, remaining)
            data = fin.read(chunk_size)
            if not data:
                break
            fout.write(data)
            if remaining is not None:
                remaining -= len(data)
                if remaining <= 0:
                    break


def extract_partition_from_raw(
    raw_path: Path,
    output_path: Path,
    partition: int | None = None,
    disabled_detectors: list[str] | None = None,
) -> Path:
    """Extract root partition from raw disk image.

    Uses fdisk to find partitions and dd to extract.

    Args:
        raw_path: Raw disk image
        output_path: Output filesystem image
        partition: Partition number (auto-detect if None)
        disabled_detectors: List of detector names to disable for auto-detection

    Returns:
        Path to extracted partition image

    Raises:
        ImageError: On extraction failure
    """
    from mvmctl.core.partition_detection import RootPartitionDetector

    try:
        parsed = _parse_partitions_sfdisk(raw_path, partition)
        if parsed is None:
            parsed = _parse_partitions_fdisk(raw_path, partition)

        if isinstance(parsed, _NoPartitionTable):
            logger.info("No partition table found, using image as-is")
            raw_path.rename(output_path)
            return output_path

        if not isinstance(parsed, tuple):
            raise ImageError(f"Unexpected parse result type: {type(parsed).__name__}")

        partitions, requested_partition = parsed

        if len(partitions) == 0:
            logger.info("No partitions found, using image as-is")
            raw_path.rename(output_path)
            return output_path

        # Determine which partition to extract
        if len(partitions) > 1 and requested_partition is None:
            logger.info("Found %d partitions:", len(partitions))
            for i, p in enumerate(partitions, 1):
                logger.debug(
                    "  %d: start=%s size=%s type=%s",
                    i,
                    p.get("start"),
                    p.get("size"),
                    p.get("type", "?"),
                )
            detector = RootPartitionDetector(disabled_detectors=disabled_detectors)
            chosen_idx = detector.detect(partitions)
            logger.info("Detector selected partition %d as root", chosen_idx)
            chosen = partitions[chosen_idx - 1]
            partition_num = chosen_idx
        elif requested_partition is not None:
            if requested_partition < 1 or requested_partition > len(partitions):
                raise ImageError(
                    f"Partition {requested_partition} out of range (1-{len(partitions)})"
                )
            logger.info("Found %d partitions:", len(partitions))
            logger.info("Using partition %d as root", requested_partition)
            chosen = partitions[requested_partition - 1]
            partition_num = requested_partition
        else:
            # Single partition - use it directly
            chosen = partitions[0]
            partition_num = 1

        start_sector = _get_int(chosen.get("start"), 0)
        size_val = chosen.get("size")
        sector_count: int | None = _get_int(size_val, 0) if size_val else None

        logger.info("Extracting partition %d (start=%d)...", partition_num, start_sector)

        skip_bytes = start_sector * _SECTOR_SIZE
        count_bytes = sector_count * _SECTOR_SIZE if sector_count else None
        _copy_bytes(raw_path, output_path, skip_bytes, count_bytes)

        output_path = _detect_and_rename_fs(output_path)

        logger.info("Extracted to %s", output_path.name)
        return output_path

    except OSError as e:
        # Sanitize: don't expose file paths in error message
        raise ImageError("Extraction failed") from e
    except (IndexError, ValueError) as e:
        raise ImageError("Failed to parse partition table") from e


def create_ext4_from_tar(
    tar_path: Path,
    output_path: Path,
    size: str,
) -> bool:
    """Create ext4 image from tar archive.

    Args:
        tar_path: Source tar archive
        output_path: Destination ext4 image
        size: Image size (e.g., "2G")

    Returns:
        True if successful

    Raises:
        ImageError: On failure to create image or missing tools
    """

    try:
        logger.info("Creating ext4 image from %s...", tar_path.name)

        # Create empty image
        subprocess.run(
            ["truncate", "-s", size, str(output_path)],
            capture_output=True,
            check=True,
        )

        # Format as ext4
        subprocess.run(
            ["mkfs.ext4", str(output_path)],
            capture_output=True,
            check=True,
        )

        # Mount and extract
        with tempfile.TemporaryDirectory() as mnt:
            subprocess.run(
                privileged_cmd(["mount", "-o", "loop", str(output_path), mnt]),
                check=True,
                capture_output=True,
            )
            try:
                subprocess.run(
                    ["tar", "-xf", str(tar_path), "-C", mnt],
                    capture_output=True,
                    check=True,
                )
            finally:
                subprocess.run(privileged_cmd(["umount", mnt]), check=False, capture_output=True)

        logger.info("Created %s", output_path.name)
        return True

    except subprocess.CalledProcessError as e:
        # Sanitize: don't expose command details in error message
        raise ImageError("Failed to create image") from e
    except FileNotFoundError as e:
        # Sanitize: don't expose tool path in error message
        raise ImageError("Required tool not found") from e


def _handle_qcow2(
    download_path: Path,
    final_path: Path,
    size_mib: int,
    partition: int | None = None,
    disabled_detectors: list[str] | None = None,
) -> Path:
    raw_path = download_path.with_suffix(".raw")
    convert_qcow2_to_raw(download_path, raw_path)
    actual_path = extract_partition_from_raw(
        raw_path,
        final_path.with_suffix(".img"),
        partition=partition,
        disabled_detectors=disabled_detectors,
    )
    raw_path.unlink(missing_ok=True)
    return actual_path


def _handle_tar_rootfs(
    download_path: Path,
    final_path: Path,
    size_mib: int,
    partition: int | None = None,
    disabled_detectors: list[str] | None = None,
) -> Path:
    create_ext4_from_tar(download_path, final_path, size=f"{size_mib}M")
    return final_path


def _handle_raw(
    download_path: Path,
    final_path: Path,
    size_mib: int,
    partition: int | None = None,
    disabled_detectors: list[str] | None = None,
) -> Path:
    return extract_partition_from_raw(
        download_path,
        final_path.with_suffix(".img"),
        partition=partition,
        disabled_detectors=disabled_detectors,
    )


def _get_template_variables(spec: ImageSpec) -> dict[str, str]:
    import platform

    try:
        from mvmctl.core.metadata import get_default_binary_entry
        from mvmctl.utils.fs import get_cache_dir

        default_binary = get_default_binary_entry(get_cache_dir())
        ci_version = ""
        if default_binary is not None:
            _version, binary_meta = default_binary
            raw_ci_version = binary_meta.get("ci_version")
            if isinstance(raw_ci_version, str):
                ci_version = raw_ci_version
    except Exception:
        ci_version = ""

    if not ci_version:
        ci_version = DEFAULT_FIRECRACKER_CI_VERSION

    arch = platform.machine() or DEFAULT_FC_KERNEL_ARCH
    variables = {
        "ci_version": ci_version,
        "arch": arch,
        "image_type": spec.image_type,
        "version": spec.version,
        "image_version": spec.version,
        "ubuntu_version": spec.version,
    }
    return {k: str(v) for k, v in variables.items()}


def _resolve_source_template(spec: ImageSpec) -> str:
    import re

    template_vars = _get_template_variables(spec)
    list_url = render_template(FIRECRACKER_CI_IMAGE_LIST_URL, template_vars)

    try:
        req = urllib.request.Request(list_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT_SHA256_FETCH_S) as resp:
            xml_content = resp.read().decode("utf-8")
    except Exception as e:
        logger.debug("Failed to list Firecracker CI ubuntu images from %s", list_url, exc_info=True)
        raise ImageError("Failed to list Firecracker CI ubuntu images") from e

    ci_version = template_vars["ci_version"]
    arch = template_vars["arch"]
    pattern = (
        rf"<Key>(firecracker-ci/{re.escape(ci_version)}/{re.escape(arch)}/"
        rf"ubuntu-[0-9.]+\.squashfs)</Key>"
    )
    keys = re.findall(pattern, xml_content)
    if not keys:
        raise ImageError(f"No ubuntu squashfs found for CI version {ci_version} / arch {arch}")

    keys.sort()
    chosen_key = keys[-1]
    return f"{FIRECRACKER_CI_KERNEL_S3_BASE}/{chosen_key}"


def _fetch_sha256_from_url(sha256_url: str, source_filename: str | None = None) -> str | None:
    try:
        req = urllib.request.Request(sha256_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT_SHA256_FETCH_S) as resp:
            content = resp.read().decode().strip()
    except (URLError, OSError):
        return None

    if source_filename is None:
        # Backward compatible: return first token for single-entry checksum files
        parts: list[str] = content.split()
        if not parts:
            return None
        return parts[0].lower()

    # Multi-entry checksum file: find the line matching source_filename
    source_basename = Path(source_filename).name
    for line in content.splitlines():
        line = line.strip()
        if not line:
            continue
        line_parts: list[str] = line.split()
        if len(line_parts) < 2:
            continue
        # Handle BSD format: "SHA256 (filename) = hash" or "hash *filename"
        # The filename is always the last token (after stripping leading *)
        filename_in_line = line_parts[-1].lstrip("*")
        # Compare: exact match, basename match, or path basename match
        filename_in_line_basename = Path(filename_in_line).name
        if (
            filename_in_line == source_filename
            or filename_in_line == source_basename
            or filename_in_line_basename == source_filename
            or filename_in_line_basename == source_basename
        ):
            return line_parts[0].lower()
    return None


def _handle_squashfs(
    download_path: Path,
    final_path: Path,
    size_mib: int,
    partition: int | None = None,
    disabled_detectors: list[str] | None = None,
) -> Path:
    with tempfile.TemporaryDirectory() as tmpdir:
        tmpdir_path = Path(tmpdir)
        extract_dir = tmpdir_path / "squashfs-root"

        try:
            subprocess.run(
                ["unsquashfs", "-d", str(extract_dir), str(download_path)],
                capture_output=True,
                check=True,
            )
        except subprocess.CalledProcessError as e:
            raise ImageError("unsquashfs failed") from e
        except FileNotFoundError as e:
            raise ImageError("unsquashfs not found. Install squashfs-tools.") from e

        try:
            subprocess.run(
                ["truncate", "-s", f"{size_mib}M", str(final_path)],
                capture_output=True,
                check=True,
            )
            subprocess.run(
                ["mkfs.ext4", "-d", str(extract_dir), "-F", str(final_path)],
                capture_output=True,
                check=True,
            )
        except subprocess.CalledProcessError as e:
            raise ImageError("Failed to create ext4 from squashfs") from e

    logger.info("Created ext4 from squashfs: %s", final_path)
    return final_path


_FORMAT_HANDLERS: dict[str, Callable[[Path, Path, int, int | None, list[str] | None], Path]] = {
    "qcow2": _handle_qcow2,
    "tar-rootfs": _handle_tar_rootfs,
    "raw": _handle_raw,
    "squashfs": _handle_squashfs,
}


def fetch_image(
    spec: ImageSpec,
    output_dir: Path,
    force: bool = False,
    partition: int | None = None,
) -> Path:
    """Fetch and convert an image.

    Args:
        spec: Image specification
        output_dir: Directory to store images
        force: Re-download even if exists
        partition: Specific partition number to extract (1-indexed), or None for auto-detect

    Returns:
        Path to final image

    Raises:
        ImageError: On failure to fetch or convert image
    """
    output_dir.mkdir(parents=True, exist_ok=True)

    # Determine final output path
    final_path = output_dir / f"{spec.id}.{spec.convert_to}"

    if final_path.exists() and not force:
        logger.info("Image already exists: %s", final_path)
        return final_path

    template_vars = _get_template_variables(spec)
    source = spec.source
    if "{" in spec.source:
        source = _resolve_source_template(spec)

    intentional_no_checksum = spec.sha256 is None and spec.sha256_url is None

    resolved_sha256 = spec.sha256.lower() if spec.sha256 is not None else None
    sha256_url = render_optional_template(spec.sha256_url, template_vars)
    if resolved_sha256 is None and sha256_url is not None:
        source_basename = source.rsplit("/", 1)[-1] if source else None
        resolved_sha256 = _fetch_sha256_from_url(sha256_url, source_filename=source_basename)

    no_checksum = resolved_sha256 is None

    download_path = output_dir / f"{spec.id}.download"
    download_file(
        source,
        download_path,
        expected_sha256=resolved_sha256,
        allow_missing_checksum=no_checksum,
        silent_missing_checksum=intentional_no_checksum,
    )

    handler = _FORMAT_HANDLERS.get(spec.format)
    if handler is None:
        download_path.unlink(missing_ok=True)
        raise ImageError(f"Unknown format: {spec.format}")
    actual_path = handler(download_path, final_path, spec.size_mib, partition, None)

    # Cleanup download
    download_path.unlink(missing_ok=True)

    return actual_path


def load_images_config(config_path: Path) -> list[ImageSpec]:
    """Load images from YAML config.

    Args:
        config_path: Path to images.yaml

    Returns:
        List of image specifications

    Raises:
        ConfigError: If config file not found
    """
    import yaml

    if not config_path.exists():
        raise ConfigError("Config not found")

    with open(config_path) as f:
        data = yaml.safe_load(f)

    images = []
    for img in data.get("images", []):
        image_id = img["id"]
        images.append(
            ImageSpec(
                id=image_id,
                image_type=img.get("type", image_id),
                version=str(img.get("version", image_id)),
                name=img.get("name", image_id),
                source=img["source"],
                format=img["format"],
                convert_to=img["convert_to"],
                size_mib=img.get("size_mib", DEFAULT_IMAGE_IMPORT_SIZE_MIB),
                sha256=img.get("sha256"),
                sha256_url=img.get("sha256_url"),
            )
        )

    return images


def import_image(
    spec: ImageImportSpec,
    output_dir: Path,
    force: bool = False,
    partition: int | None = None,
) -> Path:
    """Import a local image file into the image cache.

    Args:
        spec: Import specification (id, name, source_path, format)
        output_dir: Directory to store the imported image
        force: Overwrite existing image if present
        partition: Specific partition number to extract (1-indexed), or None for auto-detect

    Returns:
        Path to the imported image

    Raises:
        ImageError: If the image already exists (and not force), source missing,
            or conversion fails
    """
    output_dir.mkdir(parents=True, exist_ok=True)

    final_path = output_dir / f"{spec.id}.{spec.convert_to}"

    if final_path.exists() and not force:
        raise ImageError(f"Image '{spec.id}' already exists. Use --force to overwrite.")

    if not spec.source_path.exists():
        raise ImageError("Source file not found")

    logger.info(
        "Importing %s as '%s' (format: %s)...",
        spec.source_path.name,
        spec.id,
        spec.format,
    )

    if spec.format == "qcow2":
        import tempfile

        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir_path = Path(tmpdir)
            raw_path = tmpdir_path / f"{spec.id}.raw"
            extracted_path = tmpdir_path / f"{spec.id}.img"
            convert_qcow2_to_raw(spec.source_path, raw_path)
            actual_path = extract_partition_from_raw(
                raw_path,
                extracted_path,
                partition=partition,
                disabled_detectors=spec.disabled_detectors,
            )

            destination_path = output_dir / f"{spec.id}{actual_path.suffix}"
            if destination_path.exists() and not force:
                raise ImageError(f"Image '{spec.id}' already exists. Use --force to overwrite.")
            if destination_path.exists():
                destination_path.unlink()

            shutil.move(str(actual_path), destination_path)
            return destination_path

    elif spec.format == "raw":
        shutil.copy2(spec.source_path, final_path)
        return final_path

    elif spec.format == "tar-rootfs":
        size_str = f"{spec.size_mib}M"
        create_ext4_from_tar(spec.source_path, final_path, size=size_str)
        return final_path

    else:
        raise ImageError(f"Unsupported import format: {spec.format}")
