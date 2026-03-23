"""Asset management API — kernels, images, Firecracker binaries."""

from __future__ import annotations

from fcm.core.binary_manager import (
    BinaryVersion,
    fetch_binary,
    list_local_versions,
    list_remote_versions,
    remove_version,
    set_active_version,
)
from fcm.core.image import fetch_image, load_images_config
from fcm.core.kernel import build_kernel_pipeline

__all__ = [
    "BinaryVersion",
    "fetch_binary",
    "list_local_versions",
    "list_remote_versions",
    "set_active_version",
    "remove_version",
    "fetch_image",
    "load_images_config",
    "build_kernel_pipeline",
]
