"""Cache management — modular init functions for all cache resources."""

from __future__ import annotations

import logging
import shutil
import subprocess
from pathlib import Path

from mvmctl.utils.fs import (
    get_cache_dir,
    get_images_dir,
    get_kernels_dir,
    get_vms_dir,
)

logger = logging.getLogger(__name__)


def cache_init_vms() -> Path:
    """Initialize VM directory structure.

    Creates vms/ directory and ensures state.json exists.
    Returns the vms directory path.
    """
    vms_dir = get_vms_dir()
    vms_dir.mkdir(parents=True, exist_ok=True)

    # Ensure state.json exists (empty but valid)
    state_file = vms_dir / "state.json"
    if not state_file.exists():
        state_file.write_text('{"vms": {}, "schema_version": 1}')

    return vms_dir


def cache_init_images() -> Path:
    """Initialize images directory."""
    images_dir = get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)
    return images_dir


def cache_init_kernels() -> Path:
    """Initialize kernels directory."""
    kernels_dir = get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)
    return kernels_dir


def cache_init_guestfs_appliance() -> Path | None:
    """Build the libguestfs fixed appliance into $MVM_CACHE_DIR/appliance/.

    Building a fixed appliance with libguestfs-make-fixed-appliance eliminates
    the supermin appliance-construction phase on every guestfs launch, reducing
    inject_cloud_init() from 8-60s down to sub-second launch times.

    Returns the appliance directory path if build succeeded, None if
    libguestfs-make-fixed-appliance is not installed or the build failed.
    """
    make_tool = shutil.which("libguestfs-make-fixed-appliance")
    if not make_tool:
        logger.debug("libguestfs-make-fixed-appliance not found — skipping appliance build")
        return None

    appliance_dir = get_cache_dir() / "appliance"
    appliance_dir.mkdir(parents=True, exist_ok=True)

    try:
        subprocess.run(
            [make_tool, str(appliance_dir)],
            check=True,
            capture_output=True,
            text=True,
        )
        logger.debug("libguestfs fixed appliance built at %s", appliance_dir)
        return appliance_dir
    except subprocess.CalledProcessError as e:
        logger.warning("libguestfs appliance build failed: %s", e.stderr)
        return None


def cache_init_all() -> dict[str, Path | None]:
    """Initialize all cache resources.

    Returns dict mapping resource names to their directory paths.
    """
    return {
        "vms": cache_init_vms(),
        "images": cache_init_images(),
        "kernels": cache_init_kernels(),
        "guestfs_appliance": cache_init_guestfs_appliance(),
    }
