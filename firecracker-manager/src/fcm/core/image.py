"""Image download and conversion utilities."""

import hashlib
import subprocess
from pathlib import Path
from urllib.request import urlopen, Request
from urllib.error import URLError

from fcm.models.image import ImageSpec
from fcm.utils.console import print_error, print_success, print_info


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
        True if successful, False otherwise
    """
    dest.parent.mkdir(parents=True, exist_ok=True)

    try:
        req = Request(url, headers={"User-Agent": "fcm/0.1.0"})

        if show_progress:
            print_info(f"Downloading {url}")

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
                        print(f"\r  Progress: {percent:.1f}%", end="", flush=True)

        if show_progress:
            print()  # Newline after progress

        # Verify checksum if provided
        if expected_sha256 and sha256_hash:
            actual_sha256 = sha256_hash.hexdigest()
            if actual_sha256.lower() != expected_sha256.lower():
                print_error(f"Checksum mismatch! Expected {expected_sha256}, got {actual_sha256}")
                dest.unlink()
                return False
            print_success("Checksum verified")

        return True

    except URLError as e:
        print_error(f"Download failed: {e}")
        return False
    except IOError as e:
        print_error(f"I/O error: {e}")
        return False


def convert_qcow2_to_raw(
    qcow2_path: Path,
    raw_path: Path,
) -> bool:
    """Convert qcow2 to raw using qemu-img.

    Args:
        qcow2_path: Source qcow2 file
        raw_path: Destination raw file

    Returns:
        True if successful, False otherwise
    """
    try:
        print_info(f"Converting {qcow2_path.name} to raw...")

        subprocess.run(
            ["qemu-img", "convert", "-f", "qcow2", "-O", "raw", str(qcow2_path), str(raw_path)],
            capture_output=True,
            text=True,
            check=True,
        )

        print_success(f"Converted to {raw_path.name}")
        return True

    except subprocess.CalledProcessError as e:
        print_error(f"qemu-img failed: {e.stderr}")
        return False
    except FileNotFoundError:
        print_error("qemu-img not found. Install qemu-utils.")
        return False


def extract_partition_from_raw(
    raw_path: Path,
    output_path: Path,
    partition: int | None = None,
) -> Path | None:
    """Extract root partition from raw disk image.

    Uses fdisk to find partitions and dd to extract.

    Args:
        raw_path: Raw disk image
        output_path: Output filesystem image
        partition: Partition number (auto-detect if None)

    Returns:
        True if successful, False otherwise
    """
    import json as json_mod
    import re

    try:
        start_sector: int | None = None
        sector_count: int | None = None

        sfdisk_ok = False
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
                print_info("No partition table found, using image as-is")
                raw_path.rename(output_path)
                return output_path

            if len(partitions) > 1 and partition is None:
                print_info(f"Found {len(partitions)} partitions:")
                for i, p in enumerate(partitions, 1):
                    print(
                        f"  {i}: start={p.get('start')} size={p.get('size')} type={p.get('type', '?')}"
                    )
                print_info("Using last partition as root")
                partition = len(partitions)

            if partition is None:
                partition = 1

            chosen = partitions[partition - 1]
            start_sector = int(chosen["start"])
            sector_count = int(chosen["size"])
            sfdisk_ok = True

        except (
            FileNotFoundError,
            subprocess.CalledProcessError,
            json_mod.JSONDecodeError,
            KeyError,
        ):
            pass

        if not sfdisk_ok:
            result = subprocess.run(
                ["fdisk", "-l", str(raw_path)],
                capture_output=True,
                text=True,
                check=False,
            )

            partition_lines = []
            for line in result.stdout.split("\n"):
                if re.match(rf"^{re.escape(str(raw_path))}p?\d", line):
                    partition_lines.append(line)

            if not partition_lines:
                print_info("No partition table found, using image as-is")
                raw_path.rename(output_path)
                return output_path

            if len(partition_lines) > 1 and partition is None:
                print_info(f"Found {len(partition_lines)} partitions:")
                for i, line in enumerate(partition_lines, 1):
                    print(f"  {i}: {line}")
                print_info("Using last partition as root")
                partition = len(partition_lines)

            if partition is None:
                partition = 1

            chosen_line = partition_lines[partition - 1]
            numeric_parts = [p for p in chosen_line.split() if p.isdigit()]
            if len(numeric_parts) < 2:
                print_error("Failed to parse fdisk output for partition sectors")
                return None
            start_sector = int(numeric_parts[0])
            sector_count = int(numeric_parts[1]) if len(numeric_parts) >= 3 else None

        assert start_sector is not None

        print_info(f"Extracting partition {partition} (start={start_sector})...")

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

        # Detect filesystem type
        try:
            blkid_result = subprocess.run(
                ["blkid", "-o", "value", "-s", "TYPE", str(output_path)],
                capture_output=True,
                text=True,
                check=False,
            )
            fs_type = blkid_result.stdout.strip()
            if fs_type:
                # Rename with correct extension
                ext_map = {"ext4": ".ext4", "btrfs": ".btrfs", "xfs": ".xfs"}
                ext = ext_map.get(fs_type, ".img")
                final_path = output_path.with_suffix(ext)
                output_path.rename(final_path)
                output_path = final_path
                print_info(f"Detected filesystem: {fs_type}")
        except FileNotFoundError:
            pass

        print_success(f"Extracted to {output_path.name}")
        return output_path

    except subprocess.CalledProcessError as e:
        print_error(f"Extraction failed: {e}")
        return None
    except (IndexError, ValueError) as e:
        print_error(f"Failed to parse partition table: {e}")
        return None


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
        True if successful, False otherwise
    """
    import tempfile

    try:
        print_info(f"Creating ext4 image from {tar_path.name}...")

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

        print_success(f"Created {output_path.name}")
        return True

    except subprocess.CalledProcessError as e:
        print_error(f"Failed to create image: {e}")
        return False
    except FileNotFoundError as e:
        print_error(f"Required tool not found: {e}")
        return False


def fetch_image(
    spec: ImageSpec,
    output_dir: Path,
    force: bool = False,
) -> Path | None:
    """Fetch and convert an image.

    Args:
        spec: Image specification
        output_dir: Directory to store images
        force: Re-download even if exists

    Returns:
        Path to final image or None if failed
    """
    output_dir.mkdir(parents=True, exist_ok=True)

    # Determine final output path
    final_path = output_dir / f"{spec.id}.{spec.convert_to}"

    if final_path.exists() and not force:
        print_success(f"Image already exists: {final_path}")
        return final_path

    # Download
    download_path = output_dir / f"{spec.id}.download"
    if not download_file(spec.source, download_path, spec.sha256):
        return None

    # Convert based on format
    success = False
    actual_path: Path | None = None

    if spec.format == "qcow2":
        raw_path = download_path.with_suffix(".raw")
        if convert_qcow2_to_raw(download_path, raw_path):
            result = extract_partition_from_raw(raw_path, final_path.with_suffix(".img"))
            raw_path.unlink(missing_ok=True)
            if result is not None:
                success = True
                actual_path = result

    elif spec.format == "tar-rootfs":
        success = create_ext4_from_tar(download_path, final_path)
        if success:
            actual_path = final_path

    elif spec.format == "raw":
        result = extract_partition_from_raw(download_path, final_path.with_suffix(".img"))
        if result is not None:
            success = True
            actual_path = result

    else:
        print_error(f"Unknown format: {spec.format}")

    # Cleanup download
    download_path.unlink(missing_ok=True)

    if success:
        return actual_path
    return None


def load_images_config(config_path: Path) -> list[ImageSpec]:
    """Load images from YAML config.

    Args:
        config_path: Path to images.yaml

    Returns:
        List of image specifications
    """
    import yaml

    if not config_path.exists():
        print_error(f"Config not found: {config_path}")
        return []

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
