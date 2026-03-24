"""Host initialisation, state inspection, prune, clean, reset, and privilege API."""

from __future__ import annotations

from pathlib import Path

from fcm.core.host import (
    HostChange,
    HostState,
    check_kvm_access,
    check_privileges,
    check_privileges_interactive,
    check_required_binaries,
    clean_host,
    get_host_state,
    get_ip_forward_status,
    init_host,
    prune_host,
    reset_host,
    restore_host,
)
from fcm.core.vm_manager import get_vm_manager
from fcm.utils.fs import get_cache_dir

__all__ = [
    "HostChange",
    "HostState",
    "check_kvm_access",
    "check_privileges",
    "check_privileges_interactive",
    "check_required_binaries",
    "clean_host",
    "default_cache_dir",
    "get_host_state",
    "get_ip_forward_status",
    "get_vm_manager",
    "init_host",
    "prune_host",
    "reset_host",
    "restore_host",
]


def default_cache_dir() -> Path:
    """Return the default cache root directory.

    Checks the ``FCM_CACHE_DIR`` environment variable first; if set, that path
    is used (it must be under ``$HOME`` or ``/tmp``). Falls back to
    ``~/.cache/firecracker-manager`` when the variable is unset.

    Returns:
        Absolute path to the FCM cache root directory.
    """
    return get_cache_dir()
