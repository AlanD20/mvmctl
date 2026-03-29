"""Rootfs disk resizing utilities."""

import json
import subprocess
from pathlib import Path

from mvmctl.exceptions import MVMError
from mvmctl.utils.disk_size import format_disk_size


def resize_rootfs(image_path: Path, target_size_bytes: int) -> None:
    """Resize a rootfs image to target size.

    Uses qemu-img resize for raw/qcow2 images. For other formats,
    may require conversion first.

    Args:
        image_path: Path to the rootfs image
        target_size_bytes: Target size in bytes

    Raises:
        MVMError: If resize fails
    """
    if not image_path.exists():
        raise MVMError(f"Image not found: {image_path}")

    # Get current size
    try:
        result = subprocess.run(
            ["qemu-img", "info", "--output=json", str(image_path)],
            capture_output=True,
            text=True,
            check=True,
        )
        info = json.loads(result.stdout)
        current_bytes = info.get("virtual-size", 0)
    except (subprocess.CalledProcessError, json.JSONDecodeError) as e:
        raise MVMError(f"Failed to get image info: {e}") from e

    if current_bytes >= target_size_bytes:
        # Image is already large enough
        return

    # Resize the image
    target_str = format_disk_size(target_size_bytes)
    try:
        subprocess.run(
            ["qemu-img", "resize", str(image_path), target_str],
            capture_output=True,
            text=True,
            check=True,
        )
    except subprocess.CalledProcessError as e:
        raise MVMError(f"Failed to resize image: {e.stderr}") from e
