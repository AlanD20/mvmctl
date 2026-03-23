"""Host initialisation, state inspection, prune, clean, reset, and privilege API."""

from __future__ import annotations

from pathlib import Path

from fcm.core.host import (
    HostChange,
    HostState,
    check_kvm_access,
    check_privileges,
    check_required_binaries,
    clean_host,
    get_host_state,
    get_ip_forward_status,
    init_host,
    prune_host,
    reset_host,
    restore_host,
)
from fcm.utils.fs import get_cache_dir

__all__ = [
    "HostChange",
    "HostState",
    "check_kvm_access",
    "check_privileges",
    "check_required_binaries",
    "clean_host",
    "default_cache_dir",
    "get_host_state",
    "get_ip_forward_status",
    "init_host",
    "prune_host",
    "reset_host",
    "restore_host",
]


def default_cache_dir() -> Path:
    return get_cache_dir()
