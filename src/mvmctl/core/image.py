"""Image download and conversion utilities."""

import logging
import shutil
import subprocess
import tempfile
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from urllib.error import URLError

import zstandard as zstd

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
from mvmctl.utils.progress import download_with_progress
from mvmctl.utils.template import render_optional_template, render_template

logger = logging.getLogger(__name__)

# Re-export for backward compatibility
download_file = _download_file


@dataclass
class ImageImportResult:
    """Result of image fetch/import operation with filesystem metadata."""

    path: "Path"
    fs_type: str | None
    fs_uuid: str | None
    compressed_size: int | None = None
    original_size: int | None = None
    compression_ratio: float | None = None


_SECTOR_SIZE = CONST_SECTOR_SIZE_BYTES


def shrink_image_with_guestfs(image_path: Path) -> tuple[Path, int, int]:
    """Shrink an image to its minimum size using libguestfs.

    Args:
        image_path: Path to the image file to shrink

    Returns:
        Tuple of (shrunk_image_path, original_size_bytes, final_size_bytes)

    Uses the same guestfs approach as rootfs_injector.py.
    """
    from mvmctl.utils.guestfs import check_libguestfs

    if not check_libguestfs():
        logger.warning("libguestfs not available, skipping image shrink")
        return image_path, image_path.stat().st_size, image_path.stat().st_size

    # Handle case where file doesn't exist (e.g., in mocked tests)
    if not image_path.exists():
        logger.debug("Image file does not exist, skipping shrink: %s", image_path)
        return image_path, 0, 0

    from mvmctl.utils.guestfs import optimized_guestfs

    original_size = image_path.stat().st_size

    try:
        with optimized_guestfs(image_path, readonly=False) as g:
            # Detect root device (usually /dev/sda1 or first partition)
            partitions = g.list_partitions()
            root_device = partitions[0] if partitions else "/dev/sda"

            fs_type = g.vfs_type(root_device)

            if fs_type in ("ext2", "ext3", "ext4"):
                # For ext: mount, check, resize to minimum
                g.mount(root_device, "/")
                # Run e2fsck first (required before resize)
                g.e2fsck(root_device, correct=True)
                g.umount(root_device)
                # Resize to minimum (0 means minimum)
                g.resize2fs_size(root_device, 0)
            elif fs_type == "btrfs":
                g.mount(root_device, "/")
                g.btrfs_filesystem_resize("/", 0)  # 0 = minimum
                g.umount(root_device)
            else:
                logger.warning(f"Cannot shrink {fs_type} filesystem")
                return image_path, original_size, original_size

            # Get new device size
            new_size = g.blockdev_getsize64(root_device)

        # Truncate file to new size + small buffer (1% safety margin)
        final_size = int(new_size * 1.01)
        with open(image_path, "r+b") as f:
            f.truncate(final_size)

        actual_final = image_path.stat().st_size
        logger.info(
            "Shrunk %s: %d MB → %d MB (%.1fx reduction)",
            image_path.name,
            original_size // (1024 * 1024),
            actual_final // (1024 * 1024),
            original_size / actual_final if actual_final > 0 else 1.0,
        )

        return image_path, original_size, actual_final

    except Exception as e:
        logger.warning("Failed to shrink image: %s", e)
        return image_path, original_size, image_path.stat().st_size


def compress_image(image_path: Path, level: int = 6) -> Path:
    """Compress an image using zstd.

    Args:
        image_path: Path to the image file to compress
        level: Compression level (1-22, default 6 for speed/size balance)

    Returns:
        Path to the compressed file (with .zst suffix)

    Raises:
        ImageError: If compression fails
    """
    try:
        compressed_path = image_path.with_suffix(image_path.suffix + ".zst")
        original_size = image_path.stat().st_size

        compressor = zstd.ZstdCompressor(level=level)
        with open(image_path, "rb") as src, open(compressed_path, "wb") as dst:
            compressor.copy_stream(src, dst)

        compressed_size = compressed_path.stat().st_size
        ratio = original_size / compressed_size if compressed_size > 0 else 1.0

        # Remove original uncompressed file
        image_path.unlink()

        logger.info(
            "Compressed %s: %d MB → %d MB (%.1fx reduction)",
            image_path.name,
            original_size // (1024 * 1024),
            compressed_size // (1024 * 1024),
            ratio,
        )

        return compressed_path

    except OSError as e:
        raise ImageError(f"Failed to compress image: {e}") from e


def decompress_image(compressed_path: Path, output_path: Path) -> None:
    """Decompress a zstd compressed image.

    Args:
        compressed_path: Path to the compressed .zst file
        output_path: Path where the decompressed file should be written

    Raises:
        ImageError: If decompression fails
    """
    try:
        decompressor = zstd.ZstdDecompressor()
        with open(compressed_path, "rb") as src, open(output_path, "wb") as dst:
            decompressor.copy_stream(src, dst)

        logger.info("Decompressed %s → %s", compressed_path.name, output_path.name)

    except OSError as e:
        raise ImageError(f"Failed to decompress image: {e}") from e


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


def detect_filesystem_type(image_path: Path) -> str | None:
    """Detect filesystem type using blkid.

    Args:
        image_path: Path to the image file

    Returns:
        Filesystem type string (e.g., 'ext4', 'btrfs', 'xfs') or None if detection fails
    """
    try:
        blkid_result = subprocess.run(
            ["blkid", "-o", "value", "-s", "TYPE", str(image_path)],
            capture_output=True,
            text=True,
            check=False,
        )
        fs_type = blkid_result.stdout.strip()
        return fs_type if fs_type else None
    except FileNotFoundError:
        return None


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

        subprocess.run(
            ["mkfs.ext4", "-d", str(tar_path), "-O", "metadata_csum,64bit", str(output_path)],
            capture_output=True,
            check=True,
        )

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


def _validate_downloaded_file(download_path: Path, format: str) -> None:
    """Validate that a downloaded file is valid for its format.

    Args:
        download_path: Path to the downloaded file
        format: Format type (tar-rootfs, squashfs, etc.)

    Raises:
        ImageError: If validation fails
    """
    if not download_path.exists():
        raise ImageError("Downloaded file not found")

    file_size = download_path.stat().st_size
    if file_size == 0:
        download_path.unlink(missing_ok=True)
        raise ImageError("Downloaded file is empty")

    if format == "tar-rootfs":
        import subprocess

        try:
            subprocess.run(
                ["tar", "-tf", str(download_path)],
                capture_output=True,
                text=True,
                check=True,
            )
        except subprocess.CalledProcessError as e:
            download_path.unlink(missing_ok=True)
            raise ImageError("Invalid tar file: tar validation failed") from e
        except FileNotFoundError as e:
            download_path.unlink(missing_ok=True)
            raise ImageError("tar command not found") from e

    elif format == "squashfs":
        import subprocess

        try:
            subprocess.run(
                ["unsquashfs", "-l", str(download_path)],
                capture_output=True,
                text=True,
                check=True,
            )
        except subprocess.CalledProcessError as e:
            download_path.unlink(missing_ok=True)
            raise ImageError("Invalid squashfs file: unsquashfs validation failed") from e
        except FileNotFoundError as e:
            download_path.unlink(missing_ok=True)
            raise ImageError("unsquashfs command not found") from e


def fetch_image(
    spec: ImageSpec,
    output_dir: Path,
    force: bool = False,
    partition: int | None = None,
) -> ImageImportResult:
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
        # Detect filesystem info for existing image
        fs_type = detect_filesystem_type(final_path)
        fs_uuid = get_filesystem_uuid(final_path)
        return ImageImportResult(path=final_path, fs_type=fs_type, fs_uuid=fs_uuid)

    template_vars = _get_template_variables(spec)
    source = spec.source
    if "{" in spec.source:
        source = _resolve_source_template(spec)

    resolved_sha256 = spec.sha256.lower() if spec.sha256 is not None else None
    sha256_url = render_optional_template(spec.sha256_url, template_vars)
    if resolved_sha256 is None and sha256_url is not None:
        source_basename = source.rsplit("/", 1)[-1] if source else None
        resolved_sha256 = _fetch_sha256_from_url(sha256_url, source_filename=source_basename)

    download_path = output_dir / f"{spec.id}.download"

    # Clean up stale .download file if force=True
    if force and download_path.exists():
        logger.info("Removing stale download file: %s", download_path)
        download_path.unlink()

    try:
        download_with_progress(
            source,
            download_path,
            title=f"Downloading image {spec.id}",
            expected_sha256=resolved_sha256,
            timeout=HTTP_TIMEOUT_SHA256_FETCH_S,
            allow_missing_checksum=resolved_sha256 is None,
        )

        # Validate downloaded file before processing
        _validate_downloaded_file(download_path, spec.format)

        handler = _FORMAT_HANDLERS.get(spec.format)
        if handler is None:
            download_path.unlink(missing_ok=True)
            raise ImageError(f"Unknown format: {spec.format}")
        actual_path = handler(download_path, final_path, spec.size_mib, partition, None)

        # Cleanup download on success
        download_path.unlink(missing_ok=True)

        # Detect filesystem type and UUID
        fs_type = detect_filesystem_type(actual_path)
        fs_uuid = get_filesystem_uuid(actual_path)

        # Compress the image if it exists (may be mocked in tests)
        if actual_path.exists():
            original_size = actual_path.stat().st_size
            compressed_path = compress_image(actual_path)
            compressed_size = compressed_path.stat().st_size
            compression_ratio = original_size / compressed_size if compressed_size > 0 else 1.0

            return ImageImportResult(
                path=compressed_path,
                fs_type=fs_type,
                fs_uuid=fs_uuid,
                compressed_size=compressed_size,
                original_size=original_size,
                compression_ratio=compression_ratio,
            )

        return ImageImportResult(path=actual_path, fs_type=fs_type, fs_uuid=fs_uuid)
    except Exception:
        # Cleanup download on any failure
        download_path.unlink(missing_ok=True)
        raise


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
) -> ImageImportResult:
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

            # Detect filesystem type and UUID
            fs_type = detect_filesystem_type(destination_path)
            fs_uuid = get_filesystem_uuid(destination_path)

            # Compress the image if it exists (may be mocked in tests)
            if destination_path.exists():
                original_size = destination_path.stat().st_size
                compressed_path = compress_image(destination_path)
                compressed_size = compressed_path.stat().st_size
                compression_ratio = original_size / compressed_size if compressed_size > 0 else 1.0

                return ImageImportResult(
                    path=compressed_path,
                    fs_type=fs_type,
                    fs_uuid=fs_uuid,
                    compressed_size=compressed_size,
                    original_size=original_size,
                    compression_ratio=compression_ratio,
                )

            return ImageImportResult(path=destination_path, fs_type=fs_type, fs_uuid=fs_uuid)

    elif spec.format == "raw":
        shutil.copy2(spec.source_path, final_path)

        # Detect filesystem type and UUID
        fs_type = detect_filesystem_type(final_path)
        fs_uuid = get_filesystem_uuid(final_path)

        # Compress the image if it exists (may be mocked in tests)
        if final_path.exists():
            original_size = final_path.stat().st_size
            compressed_path = compress_image(final_path)
            compressed_size = compressed_path.stat().st_size
            compression_ratio = original_size / compressed_size if compressed_size > 0 else 1.0

            return ImageImportResult(
                path=compressed_path,
                fs_type=fs_type,
                fs_uuid=fs_uuid,
                compressed_size=compressed_size,
                original_size=original_size,
                compression_ratio=compression_ratio,
            )

        return ImageImportResult(path=final_path, fs_type=fs_type, fs_uuid=fs_uuid)

    elif spec.format == "tar-rootfs":
        size_str = f"{spec.size_mib}M"
        create_ext4_from_tar(spec.source_path, final_path, size=size_str)

        # Detect filesystem type and UUID for tar-rootfs (always ext4)
        fs_type = detect_filesystem_type(final_path)
        fs_uuid = get_filesystem_uuid(final_path)

        # Compress the image if it exists (may be mocked in tests)
        if final_path.exists():
            original_size = final_path.stat().st_size
            compressed_path = compress_image(final_path)
            compressed_size = compressed_path.stat().st_size
            compression_ratio = original_size / compressed_size if compressed_size > 0 else 1.0

            return ImageImportResult(
                path=compressed_path,
                fs_type=fs_type,
                fs_uuid=fs_uuid,
                compressed_size=compressed_size,
                original_size=original_size,
                compression_ratio=compression_ratio,
            )

        return ImageImportResult(path=final_path, fs_type=fs_type, fs_uuid=fs_uuid)

    else:
        raise ImageError(f"Unsupported import format: {spec.format}")


def get_ready_pool_dir() -> Path:
    """Get the tmpfs ready pool directory for fast clones.

    Returns:
        Path to the ready pool directory (typically /tmp/mvmctl/ready/)
    """
    ready_dir = Path(tempfile.gettempdir()) / "mvmctl" / "ready"
    ready_dir.mkdir(parents=True, exist_ok=True)
    return ready_dir


def _get_ready_image_path(image_hash: str, fs_type: str) -> Path:
    """Get path to ready image in tmpfs pool.

    Args:
        image_hash: The image hash (short or full) for naming
        fs_type: Filesystem type (e.g., 'ext4', 'btrfs')

    Returns:
        Path to the ready pool image file
    """
    return get_ready_pool_dir() / f"{image_hash}.{fs_type}"


def ensure_image_in_ready_pool(
    compressed_path: Path,
    image_hash: str,
    fs_type: str = "ext4",
) -> Path:
    """Ensure decompressed image exists in ready pool, creating if needed.

    This function maintains a tmpfs-based cache of decompressed images
    for fast cloning. First call decompresses to RAM, subsequent calls
    return the cached path immediately.

    Args:
        compressed_path: Path to the compressed .zst image file
        image_hash: Hash identifier for the image (used for naming)
        fs_type: Filesystem type extension (default: 'ext4')

    Returns:
        Path to the ready pool image (in tmpfs/RAM)

    Raises:
        ImageError: If decompression fails
    """
    ready_path = _get_ready_image_path(image_hash, fs_type)

    if ready_path.exists():
        logger.debug("Found image in ready pool: %s", ready_path)
        return ready_path

    # Decompress to ready pool
    logger.info("Decompressing to ready pool: %s", ready_path.name)
    decompress_image(compressed_path, ready_path)

    return ready_path


def copy_from_ready_pool(
    image_hash: str,
    fs_type: str,
    output_path: Path,
) -> None:
    """Fast copy from tmpfs ready pool to VM directory.

    Uses reflink (copy-on-write) if available (btrfs/xfs), otherwise
    falls back to regular copy. Since the ready pool is in tmpfs (RAM),
    the copy is fast regardless of the underlying filesystem.

    Args:
        image_hash: Hash identifier for the image
        fs_type: Filesystem type extension
        output_path: Destination path for the VM rootfs

    Raises:
        ImageError: If the image is not found in the ready pool
    """
    ready_path = _get_ready_image_path(image_hash, fs_type)

    if not ready_path.exists():
        raise ImageError(f"Image not in ready pool: {image_hash}")

    # Use reflink if available (btrfs/xfs), fallback to copy
    try:
        # Try reflink first (instant copy on supported filesystems)
        subprocess.run(
            ["cp", "--reflink=auto", str(ready_path), str(output_path)],
            check=True,
            capture_output=True,
        )
        logger.info("Fast-copied from ready pool: %s", output_path.name)
    except subprocess.CalledProcessError:
        # Fallback to regular copy
        shutil.copy2(ready_path, output_path)
        logger.info("Copied from ready pool: %s", output_path.name)


def clean_ready_pool() -> int:
    """Remove all images from the ready pool.

    Returns:
        Number of files removed
    """
    ready_dir = get_ready_pool_dir()
    removed_count = 0

    if ready_dir.exists():
        for item in ready_dir.iterdir():
            try:
                item.unlink()
                removed_count += 1
                logger.debug("Removed from ready pool: %s", item.name)
            except OSError as e:
                logger.warning("Failed to remove %s: %s", item.name, e)

    logger.info("Cleaned ready pool: removed %d file(s)", removed_count)
    return removed_count
