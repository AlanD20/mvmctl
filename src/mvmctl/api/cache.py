"""Cache management API — delegates to core/cache_manager.py."""

from __future__ import annotations

from mvmctl.api.host import check_privileges_interactive
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
    check_privileges_interactive("/usr/sbin/ip", "initialize cache")
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
    check_privileges_interactive("/usr/sbin/ip", "prune VMs")
    return cache_manager.cache_prune_vms(
        include_stopped=include_stopped,
        include_running=include_running,
        dry_run=dry_run,
    )


def prune_networks(dry_run: bool = False, include_all: bool = False) -> list[str]:
    check_privileges_interactive("/usr/sbin/ip", "prune networks")
    return cache_manager.cache_prune_networks(dry_run=dry_run, include_all=include_all)


def prune_images(dry_run: bool = False, include_all: bool = False) -> list[str]:
    check_privileges_interactive("/usr/sbin/ip", "prune images")
    return cache_manager.cache_prune_images(dry_run=dry_run, include_all=include_all)


def prune_kernels(dry_run: bool = False, include_all: bool = False) -> list[str]:
    check_privileges_interactive("/usr/sbin/ip", "prune kernels")
    return cache_manager.cache_prune_kernels(dry_run=dry_run, include_all=include_all)


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
    return cache_manager.cache_prune_all(
        include_stopped=include_stopped,
        include_running=include_running,
        dry_run=dry_run,
    )
