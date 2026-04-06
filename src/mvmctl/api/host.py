"""Host initialisation, state inspection, prune, clean, reset, and privilege API."""

from __future__ import annotations

from pathlib import Path

from mvmctl.core.host import (
    HostState,
    check_kvm_access,
    check_privileges,
    check_privileges_interactive,
    check_required_binaries,
    get_ip_forward_status,
)
from mvmctl.core.host import (
    clean_host as _clean_host,
)
from mvmctl.core.host import (
    prune_host as _prune_host,
)
from mvmctl.core.host import (
    reset_host as _reset_host,
)
from mvmctl.core.host_privilege import check_privileges as _check_privileges
from mvmctl.core.host_setup import init_host as _init_host
from mvmctl.core.host_state import HostStateChange
from mvmctl.core.host_state import get_host_state as _get_host_state
from mvmctl.core.host_state import restore_host as _restore_host
from mvmctl.core.image import clean_ready_pool as _clean_ready_pool
from mvmctl.core.image import get_ready_pool_dir as _get_ready_pool_dir
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.vm_manager import get_vm_manager
from mvmctl.utils.fs import get_cache_dir, chown_to_real_user

__all__ = [
    "HostStateChange",
    "HostState",
    "check_kvm_access",
    "check_privileges",
    "check_privileges_interactive",
    "check_required_binaries",
    "clean_host",
    "clean_ready_pool",
    "get_ready_pool_dir",
    "get_host_state",
    "get_ip_forward_status",
    "get_vm_manager",
    "init_host",
    "prune_host",
    "reset_host",
    "restore_host",
]


def init_host(cache_dir: Path | None = None) -> list[HostStateChange]:
    """Initialize the host with proper privileges and setup.

    Args:
        cache_dir: Root cache directory. If None, uses default cache dir.

    Returns:
        List of HostStateChange records describing changes made.
    """
    _check_privileges("/usr/sbin/ip")

    if cache_dir is None:
        cache_dir = get_cache_dir()

    db = MVMDatabase()
    changes = _init_host(cache_dir, db)

    # Chown the entire cache root so the real user can write to all subdirs
    chown_to_real_user(cache_dir)

    return changes


def get_host_state(cache_dir: Path | None = None) -> HostState | None:
    """Load the saved host state changes, or return None if none exist.

    Args:
        cache_dir: Root cache directory (unused, kept for API compatibility).

    Returns:
        HostState object with init_timestamp and changes, or None if no state is saved.
    """
    db = MVMDatabase()
    return _get_host_state(db)


def restore_host(cache_dir: Path | None = None) -> list[HostStateChange]:
    """Revert host changes recorded in the database.

    Args:
        cache_dir: Root cache directory (unused, kept for API compatibility).

    Returns:
        List of HostChange records describing each reverted change.
    """
    _check_privileges("/usr/sbin/ip")

    if cache_dir is None:
        cache_dir = get_cache_dir()

    db = MVMDatabase()
    return _restore_host(db)


def clean_host(cache_dir: Path | None = None) -> list[str]:
    """Remove all networking config (bridges, TAP devices, iptables rules, MVM chains).

    Args:
        cache_dir: Root cache directory. If None, uses default cache dir.

    Returns:
        List of summary strings describing what was cleaned.
    """
    if cache_dir is None:
        cache_dir = get_cache_dir()

    db = MVMDatabase()
    return _clean_host(cache_dir, db)


def reset_host(cache_dir: Path | None = None) -> list[str]:
    """Full rollback to pre-init state.

    Args:
        cache_dir: Root cache directory. If None, uses default cache dir.

    Returns:
        List of summary strings describing what was reset.
    """
    _check_privileges("/usr/sbin/ip")

    if cache_dir is None:
        cache_dir = get_cache_dir()

    db = MVMDatabase()
    return _reset_host(cache_dir, db)


def prune_host(cache_dir: Path | None = None) -> list[str]:
    """Tear down all bridges, TAPs, iptables rules and revert host sysctl changes.

    Args:
        cache_dir: Root cache directory. If None, uses default cache dir.

    Returns:
        List of summary strings describing what was pruned.
    """
    _check_privileges("/usr/sbin/ip")

    if cache_dir is None:
        cache_dir = get_cache_dir()

    db = MVMDatabase()
    return _prune_host(cache_dir, db)


def clean_ready_pool() -> int:
    return _clean_ready_pool()


def get_ready_pool_dir() -> Path:
    return _get_ready_pool_dir()
