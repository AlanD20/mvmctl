"""Host initialisation, state inspection, and prune API."""

from __future__ import annotations

from pathlib import Path

from fcm.core.host import (
    HostChange,
    HostState,
    check_kvm_access,
    check_required_binaries,
    get_host_state,
    get_ip_forward_status,
    init_host,
    prune_host,
    restore_host,
)
from fcm.utils.fs import get_cache_dir

__all__ = [
    "HostChange",
    "HostState",
    "check_kvm_access",
    "check_required_binaries",
    "get_ip_forward_status",
    "init_host",
    "prune_host",
    "restore_host",
    "get_host_state",
    "default_cache_dir",
]


def default_cache_dir() -> Path:
    return get_cache_dir()
