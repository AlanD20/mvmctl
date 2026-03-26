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
from mvmctl.utils.template import render_optional_template, render_template

logger = logging.getLogger(__name__)

_SECTOR_SIZE = CONST_SECTOR_SIZE_BYTES

download_file = _download_file


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
) -> tuple[int, int | None, int] | _NoPartitionTable | None:
    """Parse partition table using sfdisk.

    Returns:
        ``(start_sector, sector_count, partition_number)`` on success,
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
        partitions = table.get("partitiontable", {}).get("partitions", [])

        if not partitions:
            return _NO_PARTITION_TABLE

        if len(partitions) > 1 and partition is None:
            logger.info("Found %d partitions:", len(partitions))
            for i, p in enumerate(partitions, 1):
                logger.debug(
                    "  %d: start=%s size=%s type=%s",
                    i,
                    p.get("start"),
                    p.get("size"),
                    p.get("type", "?"),
                )
            logger.info("Using last partition as root")
            partition = len(partitions)

        if partition is None:
            partition = 1

        chosen = partitions[partition - 1]
        start_sector = int(chosen["start"])
        sector_count = int(chosen["size"])
        return (start_sector, sector_count, partition)

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
) -> tuple[int, int | None, int] | _NoPartitionTable:
    """Parse partition table using fdisk (fallback when sfdisk unavailable).

    Returns:
        ``(start_sector, sector_count, partition_number)`` on success, or
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

    if len(partition_lines) > 1 and partition is None:
        logger.info("Found %d partitions:", len(partition_lines))
        for i, line in enumerate(partition_lines, 1):
            logger.debug("  %d: %s", i, line)
        logger.info("Using last partition as root")
        partition = len(partition_lines)

    if partition is None:
        partition = 1

    chosen_line = partition_lines[partition - 1]
    numeric_parts = [p for p in chosen_line.split() if p.isdigit()]
    if len(numeric_parts) < 2:
        raise ImageError("Failed to parse fdisk output for partition sectors")
    start_sector = int(numeric_parts[0])
    sector_count = int(numeric_parts[1]) if len(numeric_parts) >= 3 else None
    return (start_sector, sector_count, partition)


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
) -> Path:
    """Extract root partition from raw disk image.

    Uses fdisk to find partitions and dd to extract.

    Args:
        raw_path: Raw disk image
        output_path: Output filesystem image
        partition: Partition number (auto-detect if None)

    Returns:
        Path to extracted partition image

    Raises:
        ImageError: On extraction failure
    """
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
        start_sector, sector_count, partition = parsed
        logger.info("Extracting partition %d (start=%d)...", partition, start_sector)

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
            subprocess.run(["mount", "-o", "loop", str(output_path), mnt], check=True)
            try:
                subprocess.run(
                    ["tar", "-xf", str(tar_path), "-C", mnt],
                    capture_output=True,
                    check=True,
                )
            finally:
                subprocess.run(["umount", mnt], check=False)

        logger.info("Created %s", output_path.name)
        return True

    except subprocess.CalledProcessError as e:
        # Sanitize: don't expose command details in error message
        raise ImageError("Failed to create image") from e
    except FileNotFoundError as e:
        # Sanitize: don't expose tool path in error message
        raise ImageError("Required tool not found") from e


def _handle_qcow2(download_path: Path, final_path: Path, size_mib: int) -> Path:
    raw_path = download_path.with_suffix(".raw")
    convert_qcow2_to_raw(download_path, raw_path)
    actual_path = extract_partition_from_raw(raw_path, final_path.with_suffix(".img"))
    raw_path.unlink(missing_ok=True)
    return actual_path


def _handle_tar_rootfs(download_path: Path, final_path: Path, size_mib: int) -> Path:
    create_ext4_from_tar(download_path, final_path, size=f"{size_mib}M")
    return final_path


def _handle_raw(download_path: Path, final_path: Path, size_mib: int) -> Path:
    return extract_partition_from_raw(download_path, final_path.with_suffix(".img"))


def _get_template_variables(spec: ImageSpec) -> dict[str, str]:
    import platform

    try:
        from mvmctl.core.config_state import get_firecracker_config

        ci_version = get_firecracker_config().get("ci_version", "")
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


def _fetch_sha256_from_url(sha256_url: str) -> str | None:
    try:
        req = urllib.request.Request(sha256_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT_SHA256_FETCH_S) as resp:
            content = resp.read().decode().strip()
    except (URLError, OSError):
        return None

    parts = content.split()
    if not parts:
        return None
    return str(parts[0]).lower()


def _handle_squashfs(download_path: Path, final_path: Path, size_mib: int) -> Path:
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


_FORMAT_HANDLERS: dict[str, Callable[[Path, Path, int], Path]] = {
    "qcow2": _handle_qcow2,
    "tar-rootfs": _handle_tar_rootfs,
    "raw": _handle_raw,
    "squashfs": _handle_squashfs,
}


def fetch_image(
    spec: ImageSpec,
    output_dir: Path,
    force: bool = False,
) -> Path:
    """Fetch and convert an image.

    Args:
        spec: Image specification
        output_dir: Directory to store images
        force: Re-download even if exists

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
        resolved_sha256 = _fetch_sha256_from_url(sha256_url)

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
    actual_path = handler(download_path, final_path, spec.size_mib)

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
) -> Path:
    """Import a local image file into the image cache.

    Args:
        spec: Import specification (id, name, source_path, format)
        output_dir: Directory to store the imported image
        force: Overwrite existing image if present

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
            actual_path = extract_partition_from_raw(raw_path, extracted_path)

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
