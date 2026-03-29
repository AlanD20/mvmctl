"""Cache management API — delegates to core/cache_manager.py."""

from __future__ import annotations

from mvmctl.api.host import check_privileges
from mvmctl.core import cache_manager

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
    check_privileges("/usr/sbin/ip")
    result = cache_manager.cache_init_all()
    return {k: str(v) if v else "" for k, v in result.items()}


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
    check_privileges("/usr/sbin/ip")
    return cache_manager.cache_prune_vms(
        include_stopped=include_stopped,
        include_running=include_running,
        dry_run=dry_run,
    )


def prune_networks(dry_run: bool = False) -> list[str]:
    """Prune unused networks.

    Removes networks that are not referenced by any VM and have no
    active leases. The default network is never pruned.

    Args:
        dry_run: If True, only report what would be removed.

    Returns:
        List of network names that were removed.
    """
    check_privileges("/usr/sbin/ip")
    return cache_manager.cache_prune_networks(dry_run=dry_run)


def prune_images(dry_run: bool = False) -> list[str]:
    """Prune unused images.

    Removes images that are not referenced by any VM. The default
    image is never pruned.

    Args:
        dry_run: If True, only report what would be removed.

    Returns:
        List of image IDs (short form, first 6 chars) that were removed.
    """
    check_privileges("/usr/sbin/ip")
    return cache_manager.cache_prune_images(dry_run=dry_run)


def prune_kernels(dry_run: bool = False) -> list[str]:
    """Prune unused kernels.

    Removes kernels that are not referenced by any VM. The default
    kernel is never pruned.

    Args:
        dry_run: If True, only report what would be removed.

    Returns:
        List of kernel IDs (short form, first 6 chars) that were removed.
    """
    check_privileges("/usr/sbin/ip")
    return cache_manager.cache_prune_kernels(dry_run=dry_run)


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
    check_privileges("/usr/sbin/ip")
    return cache_manager.cache_prune_all(
        include_stopped=include_stopped,
        include_running=include_running,
        dry_run=dry_run,
    )
