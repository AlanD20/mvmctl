"""Host initialisation, state inspection, prune, clean, reset, and privilege API."""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.core.host import (
    HostChange,
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
from mvmctl.core.image import clean_ready_pool as _clean_ready_pool
from mvmctl.core.vm_manager import get_vm_manager
from mvmctl.exceptions import HostError
from mvmctl.utils.fs import get_cache_dir

if TYPE_CHECKING:
    from collections.abc import Sequence

__all__ = [
    "HostChange",
    "HostState",
    "check_kvm_access",
    "check_privileges",
    "check_privileges_interactive",
    "check_required_binaries",
    "clean_host",
    "clean_ready_pool",
    "default_cache_dir",
    "escalate_and_init_host",
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

    Checks the ``MVM_CACHE_DIR`` environment variable first; if set, that path
    is used (it must be under ``$HOME`` or ``/tmp``). Falls back to
    ``~/.cache/mvmctl`` when the variable is unset.

    Returns:
        Absolute path to the MVM cache root directory.
    """
    return get_cache_dir()


def escalate_and_init_host(
    argv: Sequence[str] | None = None,
) -> list[HostChange]:
    """Initialize host with sudo escalation if needed.

    Attempts to run host init. If it fails due to missing root privileges,
    escalates to sudo and re-executes the command.

    Args:
        argv: Command-line arguments to pass to sudo. Defaults to sys.argv.

    Returns:
        List of changes applied during host initialization.

    Raises:
        HostError: If initialization fails after escalation or if sudo is unavailable.
    """
    cache_dir = get_cache_dir()
    try:
        return init_host(cache_dir)
    except HostError as e:
        if "Root privileges" not in str(e):
            raise

        if os.environ.get("MVM_SUDO_RESTART"):
            raise HostError("Recursive sudo restart detected. Aborting to prevent lockout.")

        try:
            env = os.environ.copy()
            env["MVM_SUDO_RESTART"] = "1"
            cmd = ["sudo"] + list(argv if argv is not None else sys.argv)
            subprocess.run(cmd, check=False, env=env)
            raise SystemExit(0) from None
        except FileNotFoundError:
            raise HostError("sudo command not found") from None


def clean_host(cache_dir: Path | None = None) -> list[str]:
    """Remove all networking config (bridges, TAP devices, iptables rules, MVM chains).

    Does NOT revert sysctl, remove sudoers, or remove project group.

    Args:
        cache_dir: Root cache directory. Defaults to ``get_cache_dir()``.

    Returns:
        A list of summary strings describing what was removed.

    """
    if cache_dir is None:
        cache_dir = get_cache_dir()
    return _clean_host(cache_dir)


def reset_host(cache_dir: Path | None = None) -> list[str]:
    """Full rollback to pre-init state.

    Removes networking config, reverts sysctl, removes sudoers drop-in, and removes project group.

    Args:
        cache_dir: Root cache directory. Defaults to ``get_cache_dir()``.

    Returns:
        A list of summary strings describing what was removed/reverted.

    """
    if cache_dir is None:
        cache_dir = get_cache_dir()
    return _reset_host(cache_dir)


def prune_host(cache_dir: Path | None = None) -> list[str]:
    """Tear down all bridges, TAPs, iptables rules and revert host sysctl changes.

    Does NOT remove VM cache files, images, kernels, or binaries.

    Args:
        cache_dir: Root cache directory. Defaults to ``get_cache_dir()``.

    Returns:
        A list of summary strings describing what was torn down.

    """
    if cache_dir is None:
        cache_dir = get_cache_dir()
    return _prune_host(cache_dir)


def clean_ready_pool() -> int:
    """Remove all images from the tmpfs ready pool.

    The ready pool holds decompressed VM images in tmpfs (RAM) for fast cloning.
    This clears all cached images to free up memory.

    Returns:
        Number of files removed from the ready pool.
    """
    return _clean_ready_pool()
