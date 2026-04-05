"""Cache management — modular init and prune functions for all cache resources."""

from __future__ import annotations

import logging
import shutil
import subprocess
from pathlib import Path

from mvmctl.constants import DEFAULT_NETWORK_NAME, SUPPORTED_IMAGE_EXTENSIONS
from mvmctl.core.metadata import (
    get_default_image_entry,
    get_default_kernel_entry,
    list_image_entries,
    list_kernel_entries,
    remove_image_entry,
    remove_kernel_entry,
)
from mvmctl.core.network_manager import (
    get_network_leases,
    list_networks,
    remove_network,
)
from mvmctl.core.vm_lifecycle import remove_vm
from mvmctl.core.vm_manager import get_vm_manager
from mvmctl.models.vm import VMStatus
from mvmctl.utils.fs import (
    get_cache_dir,
    get_images_dir,
    get_kernels_dir,
    get_vms_dir,
)

logger = logging.getLogger(__name__)


# =============================================================================
# Init Functions
# =============================================================================


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


# =============================================================================
# Helper Functions (internal)
# =============================================================================


def _get_image_references() -> set[str]:
    """Get set of image paths referenced by all VMs."""
    vm_manager = get_vm_manager()
    vms = vm_manager.list_all()

    referenced: set[str] = set()
    for vm in vms:
        if vm.config and vm.config.rootfs_path:
            referenced.add(str(vm.config.rootfs_path))

    return referenced


def _get_kernel_references() -> set[str]:
    """Get set of kernel paths referenced by all VMs."""
    vm_manager = get_vm_manager()
    vms = vm_manager.list_all()

    referenced: set[str] = set()
    for vm in vms:
        if vm.config and vm.config.kernel_path:
            referenced.add(str(vm.config.kernel_path))

    return referenced


def _get_network_references() -> set[str]:
    """Get set of network names referenced by all VMs."""
    vm_manager = get_vm_manager()
    vms = vm_manager.list_all()

    referenced: set[str] = set()
    for vm in vms:
        if vm.network_name:
            referenced.add(vm.network_name)

    return referenced


# =============================================================================
# Prune Functions
# =============================================================================


def cache_prune_vms(
    include_stopped: bool = False,
    include_running: bool = False,
    dry_run: bool = False,
) -> list[str]:
    """Prune VMs based on status.

    By default (no flags), only prunes VMs in ERROR state.

    Args:
        include_stopped: Also prune STOPPED VMs
        include_running: Also prune RUNNING VMs (use with caution)
        dry_run: If True, only report what would be removed

    Returns:
        List of VM names that were (or would be) removed
    """
    vm_manager = get_vm_manager()
    vms = vm_manager.list_all()

    removed: list[str] = []
    for vm in vms:
        should_remove = False

        if vm.status == VMStatus.ERROR:
            should_remove = True
        elif vm.status == VMStatus.STOPPED and include_stopped:
            should_remove = True
        elif vm.status == VMStatus.RUNNING and include_running:
            should_remove = True

        if should_remove:
            if not dry_run:
                try:
                    remove_vm(vm.name)
                    removed.append(vm.name)
                except Exception as e:
                    logger.warning(f"Failed to remove VM {vm.name}: {e}")
            else:
                removed.append(vm.name)

    return removed


def cache_prune_networks(dry_run: bool = False, include_all: bool = False) -> list[str]:
    referenced_networks = _get_network_references()
    all_networks = list_networks()

    removed: list[str] = []
    for network in all_networks:
        if not include_all:
            if network.name == DEFAULT_NETWORK_NAME:
                continue
            if network.name in referenced_networks:
                continue
            leases = get_network_leases(network.name)
            if leases:
                continue

        if not dry_run:
            try:
                remove_network(network.name)
                removed.append(network.name)
            except Exception as e:
                logger.warning(f"Failed to remove network {network.name}: {e}")
        else:
            removed.append(network.name)

    return removed


def cache_prune_images(dry_run: bool = False, include_all: bool = False) -> list[str]:
    cache_dir = get_cache_dir()
    images_dir = get_images_dir()

    referenced_paths = _get_image_references()
    all_images = list_image_entries(cache_dir, images_dir)

    default_entry = get_default_image_entry(cache_dir)
    default_id = default_entry[0] if default_entry else None

    removed: list[str] = []
    for image_id, meta in all_images.items():
        if not include_all:
            if image_id == default_id:
                continue

            path = str(meta.get("path", ""))
            if path:
                image_path = str(images_dir / path)
                if image_path in referenced_paths:
                    continue

            os_slug = str(meta.get("os_slug", ""))
            if os_slug:
                is_referenced = False
                for ref_path in referenced_paths:
                    if os_slug in ref_path:
                        is_referenced = True
                        break
                if is_referenced:
                    continue

        path = str(meta.get("path", ""))
        if not dry_run:
            try:
                if path:
                    (images_dir / path).unlink(missing_ok=True)
                else:
                    for ext in SUPPORTED_IMAGE_EXTENSIONS:
                        (images_dir / f"{image_id}{ext}").unlink(missing_ok=True)
                remove_image_entry(cache_dir, image_id)
                removed.append(image_id)
            except Exception as e:
                logger.warning(f"Failed to remove image {image_id}: {e}")
        else:
            removed.append(image_id)

    return removed


def cache_prune_kernels(dry_run: bool = False, include_all: bool = False) -> list[str]:
    cache_dir = get_cache_dir()
    kernels_dir = get_kernels_dir()

    referenced_paths = _get_kernel_references()
    all_kernels = list_kernel_entries(cache_dir, kernels_dir)

    default_entry = get_default_kernel_entry(cache_dir)
    default_id = default_entry[0] if default_entry else None

    removed: list[str] = []
    for kernel_id, meta in all_kernels.items():
        path = str(meta.get("path", ""))
        if not include_all:
            if kernel_id == default_id:
                continue
            if path:
                kernel_path = str(kernels_dir / path)
                if kernel_path in referenced_paths:
                    continue

        if not dry_run:
            try:
                if path:
                    (kernels_dir / path).unlink(missing_ok=True)
                else:
                    (kernels_dir / kernel_id).unlink(missing_ok=True)
                remove_kernel_entry(cache_dir, kernel_id)
                removed.append(kernel_id)
            except Exception as e:
                logger.warning(f"Failed to remove kernel {kernel_id}: {e}")
        else:
            removed.append(kernel_id)

    return removed


def cache_prune_all(
    include_stopped: bool = False,
    include_running: bool = False,
    dry_run: bool = False,
) -> dict[str, list[str] | bool]:
    """Prune all cache resources.

    Args:
        include_stopped: Also prune STOPPED VMs
        include_running: Also prune RUNNING VMs (use with caution)
        dry_run: If True, only report what would be removed

    Returns:
        Dict with results per resource type:
        - "vms": list of removed VM names
        - "networks": list of removed network names
        - "images": list of removed image IDs (short)
        - "kernels": list of removed kernel IDs (short)
    """
    return {
        "vms": cache_prune_vms(include_stopped, include_running, dry_run),
        "networks": cache_prune_networks(dry_run),
        "images": cache_prune_images(dry_run),
        "kernels": cache_prune_kernels(dry_run),
    }
