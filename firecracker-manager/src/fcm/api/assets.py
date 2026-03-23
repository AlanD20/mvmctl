"""Asset management API --- kernels, images, Firecracker binaries.

Provides both granular operations (re-exported from core modules) and
higher-level composite helpers for common workflows.
"""

from __future__ import annotations

import logging
from pathlib import Path

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

logger = logging.getLogger(__name__)

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
    "setup_assets",
]


def setup_assets(
    version: str,
    bin_dir: Path | None = None,
) -> BinaryVersion:
    """Fetch Firecracker binaries and set them as the active version.

    This is a convenience composite that combines ``fetch_binary`` and
    ``set_active_version`` into a single call, suitable for initial
    setup workflows.

    Args:
        version: Firecracker release version to fetch (e.g. ``"1.5.0"``).
        bin_dir: Override binary cache directory.  Uses the default
            cache location when *None*.

    Returns:
        The :class:`BinaryVersion` for the fetched/activated binaries.

    Raises:
        BinaryError: If the download or extraction fails.
    """
    bv = fetch_binary(version, bin_dir=bin_dir)
    set_active_version(version, bin_dir=bin_dir)
    logger.info("Firecracker %s fetched and set as active", version)
    return bv
