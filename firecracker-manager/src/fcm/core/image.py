"""Image download and conversion utilities."""

import logging
import shutil
import subprocess
from collections.abc import Callable
from pathlib import Path

from fcm.exceptions import ImageError, ConfigError
from fcm.models.image import ImageSpec, ImageImportSpec
from fcm.utils.http import download_file  # re-exported for backward compatibility

logger = logging.getLogger(__name__)

_SECTOR_SIZE = 512


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
        raise ImageError(f"qemu-img failed: {e.stderr}") from e
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


_COPY_CHUNK_SIZE = 1024 * 1024  # 1 MiB


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
        raise ImageError(f"Extraction failed: {e}") from e
    except (IndexError, ValueError) as e:
        raise ImageError(f"Failed to parse partition table: {e}") from e


def create_ext4_from_tar(
    tar_path: Path,
    output_path: Path,
    size: str = "2G",
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
    import tempfile

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
        raise ImageError(f"Failed to create image: {e}") from e
    except FileNotFoundError as e:
        raise ImageError(f"Required tool not found: {e}") from e


def _handle_qcow2(download_path: Path, final_path: Path) -> Path:
    raw_path = download_path.with_suffix(".raw")
    convert_qcow2_to_raw(download_path, raw_path)
    actual_path = extract_partition_from_raw(raw_path, final_path.with_suffix(".img"))
    raw_path.unlink(missing_ok=True)
    return actual_path


def _handle_tar_rootfs(download_path: Path, final_path: Path) -> Path:
    create_ext4_from_tar(download_path, final_path)
    return final_path


def _handle_raw(download_path: Path, final_path: Path) -> Path:
    return extract_partition_from_raw(download_path, final_path.with_suffix(".img"))


def _resolve_ubuntu_fc_source(spec: ImageSpec) -> str:
    """Resolve the ubuntu-fc S3 source URL dynamically."""
    import platform
    from urllib.request import Request, urlopen

    from fcm.constants import (
        DEFAULT_FC_KERNEL_ARCH,
        DEFAULT_FIRECRACKER_CI_VERSION,
        HTTP_USER_AGENT,
    )

    try:
        from fcm.core.config_state import get_firecracker_config

        ci_version = get_firecracker_config().get("ci_version", "")
    except Exception:
        ci_version = ""

    if not ci_version:
        ci_version = DEFAULT_FIRECRACKER_CI_VERSION

    arch = platform.machine() or DEFAULT_FC_KERNEL_ARCH

    list_url = f"http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/{ci_version}/{arch}/ubuntu-&list-type=2"

    try:
        req = Request(list_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urlopen(req, timeout=30) as resp:
            xml_content = resp.read().decode("utf-8")
    except Exception as e:
        raise ImageError(f"Failed to list Firecracker CI ubuntu images: {e}") from e

    import re

    pattern = rf"<Key>(firecracker-ci/{re.escape(ci_version)}/{re.escape(arch)}/ubuntu-[0-9.]+\.squashfs)</Key>"
    keys = re.findall(pattern, xml_content)
    if not keys:
        raise ImageError(f"No ubuntu squashfs found for CI version {ci_version} / arch {arch}")

    keys.sort()
    chosen_key = keys[-1]
    return f"https://s3.amazonaws.com/spec.ccfc.min/{chosen_key}"


def _handle_squashfs(download_path: Path, final_path: Path) -> Path:
    """Extract squashfs to ext4 image."""
    import tempfile

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
            raise ImageError(f"unsquashfs failed: {e.stderr}") from e
        except FileNotFoundError as e:
            raise ImageError("unsquashfs not found. Install squashfs-tools.") from e

        try:
            subprocess.run(
                ["truncate", "-s", "1G", str(final_path)],
                capture_output=True,
                check=True,
            )
            subprocess.run(
                ["mkfs.ext4", "-d", str(extract_dir), "-F", str(final_path)],
                capture_output=True,
                check=True,
            )
        except subprocess.CalledProcessError as e:
            raise ImageError(f"Failed to create ext4 from squashfs: {e}") from e

    logger.info("Created ext4 from squashfs: %s", final_path)
    return final_path


_FORMAT_HANDLERS: dict[str, Callable[[Path, Path], Path]] = {
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

    # Resolve source URL dynamically for ubuntu-fc
    source = spec.source
    if spec.id == "ubuntu-fc" and spec.format == "squashfs":
        source = _resolve_ubuntu_fc_source(spec)

    resolved_sha256 = spec.sha256

    if not resolved_sha256 and spec.sha256_url:
        import tempfile

        try:
            with tempfile.NamedTemporaryFile(suffix=".sha256", delete=False) as tmp:
                tmp_path = Path(tmp.name)
            download_file(spec.sha256_url, tmp_path, expected_sha256=None)
            checksum_text = tmp_path.read_text().strip()
            source_basename = source.rstrip("/").split("/")[-1]
            for line in checksum_text.splitlines():
                parts = line.split()
                if len(parts) >= 2 and parts[1].lstrip("*") == source_basename:
                    resolved_sha256 = parts[0]
                    break
            if not resolved_sha256 and checksum_text:
                first_token = checksum_text.splitlines()[0].split()[0]
                if len(first_token) in (64, 128):
                    resolved_sha256 = first_token
            tmp_path.unlink(missing_ok=True)
        except Exception as e:
            logger.warning("Failed to fetch checksum from %s: %s", spec.sha256_url, e)

    if not resolved_sha256:
        from fcm.utils.console import print_warning

        print_warning(
            f"No checksum available for '{spec.id}'. "
            "Integrity cannot be verified — set sha256 or sha256_url in images.yaml to enable verification."
        )

    # Download
    download_path = output_dir / f"{spec.id}.download"
    download_file(source, download_path, resolved_sha256)

    # Convert based on format
    handler = _FORMAT_HANDLERS.get(spec.format)
    if handler is None:
        download_path.unlink(missing_ok=True)
        raise ImageError(f"Unknown format: {spec.format}")
    actual_path = handler(download_path, final_path)

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
        raise ConfigError(f"Config not found: {config_path}")

    with open(config_path) as f:
        data = yaml.safe_load(f)

    images = []
    for img in data.get("images", []):
        images.append(
            ImageSpec(
                id=img["id"],
                name=img.get("name", img["id"]),
                source=img["source"],
                format=img["format"],
                convert_to=img["convert_to"],
                size_mib=img.get("size_mib", 2048),
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
        raise ImageError(f"Image already exists: {final_path}. Use --force to overwrite.")

    if not spec.source_path.exists():
        raise ImageError(f"Source file not found: {spec.source_path}")

    logger.info(
        "Importing %s as '%s' (format: %s)...",
        spec.source_path.name,
        spec.id,
        spec.format,
    )

    if spec.format == "qcow2":
        raw_path = output_dir / f"{spec.id}.raw"
        convert_qcow2_to_raw(spec.source_path, raw_path)
        try:
            actual_path = extract_partition_from_raw(raw_path, final_path.with_suffix(".img"))
        finally:
            raw_path.unlink(missing_ok=True)
        return actual_path

    elif spec.format == "raw":
        shutil.copy2(spec.source_path, final_path)
        return final_path

    elif spec.format == "tar-rootfs":
        size_str = f"{spec.size_mib}M"
        create_ext4_from_tar(spec.source_path, final_path, size=size_str)
        return final_path

    else:
        raise ImageError(f"Unsupported import format: {spec.format}")
