"""Cache management API — orchestrates cache pruning operations.

This module provides the API layer for cache management, orchestrating
prune operations across VMs, networks, images, and kernels.
"""

from __future__ import annotations

import logging

from mvmctl.api.host import check_privileges_interactive
from mvmctl.api.metadata import get_default_image_entry, get_default_kernel_entry
from mvmctl.api.network import get_network_leases, list_networks, remove_network
from mvmctl.api.vms import remove_vm
from mvmctl.constants import DEFAULT_NETWORK_NAME, SUPPORTED_IMAGE_EXTENSIONS
from mvmctl.core import cache_manager as core_cache_manager
from mvmctl.core.metadata import (
    list_image_entries,
    list_kernel_entries,
    remove_image_entry,
    remove_kernel_entry,
)
from mvmctl.core.vm_manager import get_vm_manager
from mvmctl.models.vm import VMStatus
from mvmctl.utils.fs import (
    get_cache_dir,
    get_images_dir,
    get_kernels_dir,
)

logger = logging.getLogger(__name__)

__all__ = [
    "init_all",
    "prune_vms",
    "prune_networks",
    "prune_images",
    "prune_kernels",
    "prune_all",
]


def init_all() -> dict[str, str]:
    """Initialize all cache resources.

    Creates all necessary cache directories and initializes metadata files.
    Requires proper group privileges (checked via check_privileges).

    Returns:
        Dictionary mapping resource names to their directory paths as strings.
    """
    result = core_cache_manager.cache_init_all()
    return {k: str(v) if v else "" for k, v in result.items()}


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


def prune_vms(
    include_stopped: bool = False,
    include_running: bool = False,
    dry_run: bool = False,
) -> list[str]:
    """Prune VMs based on their status.

    By default, only prunes VMs in ERROR state. Use flags to include
    stopped or running VMs.

    Args:
        include_stopped: Also prune STOPPED VMs.
        include_running: Also prune RUNNING VMs (use with caution).
        dry_run: If True, only report what would be removed.

    Returns:
        List of VM names that were removed.
    """
    check_privileges_interactive("/usr/sbin/ip", "prune VMs")
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


def prune_networks(dry_run: bool = False, include_all: bool = False) -> list[str]:
    """Prune unused networks.

    Args:
        dry_run: If True, only report what would be removed.
        include_all: If True, remove all networks including default and referenced.

    Returns:
        List of network names that were removed.
    """
    check_privileges_interactive("/usr/sbin/ip", "prune networks")
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


def prune_images(dry_run: bool = False, include_all: bool = False) -> list[str]:
    """Prune unused images.

    Args:
        dry_run: If True, only report what would be removed.
        include_all: If True, remove all images including default and referenced.

    Returns:
        List of image IDs that were removed.
    """
    cache_dir = get_cache_dir()
    images_dir = get_images_dir()

    referenced_paths = _get_image_references()
    all_images = list_image_entries(cache_dir, images_dir)

    default_entry = get_default_image_entry()
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


def prune_kernels(dry_run: bool = False, include_all: bool = False) -> list[str]:
    """Prune unused kernels.

    Args:
        dry_run: If True, only report what would be removed.
        include_all: If True, remove all kernels including default and referenced.

    Returns:
        List of kernel IDs that were removed.
    """
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


def prune_all(
    include_stopped: bool = False,
    include_running: bool = False,
    dry_run: bool = False,
) -> dict[str, list[str] | bool]:
    """Prune all cache resources.

    Performs a complete prune operation across all resource types:
    VMs, networks, images, and kernels.

    Args:
        include_stopped: Include stopped VMs in pruning.
        include_running: Include running VMs in pruning (use with caution).
        dry_run: If True, only report what would be removed.

    Returns:
        Dictionary with results per resource type:
        - "vms": list of removed VM names
        - "networks": list of removed network names
        - "images": list of removed image IDs
        - "kernels": list of removed kernel IDs
    """
    check_privileges_interactive("/usr/sbin/ip", "prune all cache resources")
    return {
        "vms": prune_vms(include_stopped, include_running, dry_run),
        "networks": prune_networks(dry_run),
        "images": prune_images(dry_run),
        "kernels": prune_kernels(dry_run),
    }
