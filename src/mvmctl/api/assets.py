from __future__ import annotations

import logging
from typing import Literal, TypedDict

from mvmctl.core.binary_manager import (
    BinaryVersion,
    ensure_default_binary,
    fetch_binary,
    get_binary_path,
    list_local_versions,
    list_remote_versions,
    remove_version,
    set_active_version,
)
from mvmctl.core.image import fetch_image, get_filesystem_uuid, import_image, load_images_config
from mvmctl.core.kernel import (
    build_kernel_pipeline,
    download_firecracker_kernel,
    get_default_kernel_path,
    list_kernels,
    resolve_kernel_spec,
    set_default_kernel,
)
from mvmctl.models.image import ImageImportSpec

logger = logging.getLogger(__name__)

__all__ = [
    "AssetInfo",
    "BinaryVersion",
    "ImageImportSpec",
    "ensure_default_binary",
    "fetch_binary",
    "get_binary_path",
    "list_local_versions",
    "list_remote_versions",
    "set_active_version",
    "remove_version",
    "fetch_image",
    "import_image",
    "load_images_config",
    "get_filesystem_uuid",
    "build_kernel_pipeline",
    "list_kernels",
    "set_default_kernel",
    "get_default_kernel_path",
    "resolve_kernel_spec",
    "download_firecracker_kernel",
]


class AssetInfo(TypedDict):
    type: Literal["binary", "kernel", "image"]
    name: str
    active: bool | None
    size_mib: float | None
    details: str | None
