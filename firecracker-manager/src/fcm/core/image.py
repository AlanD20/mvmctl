"""Image download and conversion utilities."""

import hashlib
import logging
import subprocess
from pathlib import Path
from urllib.request import urlopen, Request
from urllib.error import URLError

from fcm.constants import HTTP_USER_AGENT
from fcm.exceptions import ImageError, ChecksumMismatchError, ConfigError
from fcm.models.image import ImageSpec

logger = logging.getLogger(__name__)


def download_file(
    url: str,
    dest: Path,
    expected_sha256: str | None = None,
    show_progress: bool = True,
) -> bool:
    """Download file with optional progress display.

    Args:
        url: URL to download from
        dest: Destination path
        expected_sha256: Optional SHA-256 checksum to verify
        show_progress: Show progress bar

    Returns:
        True if successful

    Raises:
        ImageError: On download or I/O failure
        ChecksumMismatchError: On checksum mismatch
    """
    dest.parent.mkdir(parents=True, exist_ok=True)

    if expected_sha256 is None:
        logger.warning("No checksum provided for download: %s", url)

    try:
        req = Request(url, headers={"User-Agent": HTTP_USER_AGENT})

        if show_progress:
            logger.info("Downloading %s", url)

        with urlopen(req, timeout=300) as response:
            total_size = response.headers.get("Content-Length")

            sha256_hash = hashlib.sha256() if expected_sha256 else None
            downloaded = 0

            with open(dest, "wb") as f:
                while True:
                    chunk = response.read(8192)
                    if not chunk:
                        break
                    f.write(chunk)
                    downloaded += len(chunk)

                    if sha256_hash:
                        sha256_hash.update(chunk)

                    if show_progress and total_size:
                        percent = (downloaded / int(total_size)) * 100
                        logger.debug("Progress: %.1f%%", percent)

        # Verify checksum if provided
        if expected_sha256 and sha256_hash:
            actual_sha256 = sha256_hash.hexdigest()
            if actual_sha256.lower() != expected_sha256.lower():
                dest.unlink()
                raise ChecksumMismatchError(
                    f"Checksum mismatch! Expected {expected_sha256}, got {actual_sha256}"
                )
            logger.info("Checksum verified")

        return True

    except URLError as e:
        raise ImageError(f"Download failed: {e}") from e
    except IOError as e:
        raise ImageError(f"I/O error: {e}") from e


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
            ["qemu-img", "convert", "-f", "qcow2", "-O", "raw", str(qcow2_path), str(raw_path)],
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


_NO_PARTITION_TABLE = object()  # Sentinel: raw image is the filesystem


def _parse_partitions_sfdisk(
    raw_path: Path,
    partition: int | None,
) -> tuple[int, int | None, int] | object | None:
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
) -> tuple[int, int | None, int] | object:
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

        if parsed is _NO_PARTITION_TABLE:
            logger.info("No partition table found, using image as-is")
            raw_path.rename(output_path)
            return output_path

        start_sector, sector_count, partition = parsed  # type: ignore[misc]

        logger.info("Extracting partition %d (start=%d)...", partition, start_sector)

        dd_args = [
            "dd",
            f"if={raw_path}",
            f"of={output_path}",
            "bs=512",
            f"skip={start_sector}",
        ]
        if sector_count:
            dd_args.append(f"count={sector_count}")

        subprocess.run(dd_args, capture_output=True, check=True)

        output_path = _detect_and_rename_fs(output_path)

        logger.info("Extracted to %s", output_path.name)
        return output_path

    except subprocess.CalledProcessError as e:
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

    # Download
    download_path = output_dir / f"{spec.id}.download"
    download_file(spec.source, download_path, spec.sha256)

    # Convert based on format
    actual_path: Path | None = None

    if spec.format == "qcow2":
        raw_path = download_path.with_suffix(".raw")
        convert_qcow2_to_raw(download_path, raw_path)
        actual_path = extract_partition_from_raw(raw_path, final_path.with_suffix(".img"))
        raw_path.unlink(missing_ok=True)

    elif spec.format == "tar-rootfs":
        create_ext4_from_tar(download_path, final_path)
        actual_path = final_path

    elif spec.format == "raw":
        actual_path = extract_partition_from_raw(download_path, final_path.with_suffix(".img"))

    else:
        download_path.unlink(missing_ok=True)
        raise ImageError(f"Unknown format: {spec.format}")

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
            )
        )

    return images
