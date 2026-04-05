"""Host initialisation, state inspection, prune, clean, reset, and privilege API."""

from __future__ import annotations

from pathlib import Path

from mvmctl.core.host import (
    HostState,
    check_kvm_access,
    check_privileges,
    check_privileges_interactive,
    check_required_binaries,
    get_host_state,
    get_ip_forward_status,
    init_host,
    restore_host,
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
from mvmctl.core.host_state import HostStateChange
from mvmctl.core.image import clean_ready_pool as _clean_ready_pool
from mvmctl.core.image import get_ready_pool_dir as _get_ready_pool_dir
from mvmctl.core.vm_manager import get_vm_manager
from mvmctl.utils.fs import get_cache_dir

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


def clean_host(cache_dir: Path | None = None) -> list[str]:
    if cache_dir is None:
        cache_dir = get_cache_dir()
    return _clean_host(cache_dir)


def reset_host(cache_dir: Path | None = None) -> list[str]:
    if cache_dir is None:
        cache_dir = get_cache_dir()
    return _reset_host(cache_dir)


def prune_host(cache_dir: Path | None = None) -> list[str]:
    if cache_dir is None:
        cache_dir = get_cache_dir()
    return _prune_host(cache_dir)


def clean_ready_pool() -> int:
    return _clean_ready_pool()


def get_ready_pool_dir() -> Path:
    return _get_ready_pool_dir()
