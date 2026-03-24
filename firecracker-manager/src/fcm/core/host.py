"""Host configuration management for Firecracker prerequisites."""

from __future__ import annotations

import logging
import subprocess
from pathlib import Path

from fcm.constants import PROJECT_GROUP, SUDOERS_DROP_IN_PATH
from fcm.exceptions import HostError

from fcm.core.host_state import HostState, HostChange, get_host_state, restore_host, _state_file
from fcm.core.host_privilege import check_privileges, _remove_sudoers, _remove_group
from fcm.core.host_setup import (
    check_kvm_access,
    check_required_binaries,
    get_ip_forward_status,
    init_host,
)

__all__ = [
    "clean_host",
    "prune_host",
    "reset_host",
    "HostState",
    "HostChange",
    "get_host_state",
    "restore_host",
    "_state_file",
    "check_privileges",
    "_remove_sudoers",
    "_remove_group",
    "check_kvm_access",
    "check_required_binaries",
    "get_ip_forward_status",
    "init_host",
]

logger = logging.getLogger(__name__)


# Allowlists for restore_host() — only these keys/paths may be restored with root privileges.
def prune_host(cache_dir: Path) -> list[str]:
    """Tear down all bridges, TAPs, iptables rules and revert host sysctl changes.

    Does NOT remove VM cache files, images, kernels, or binaries.

    Args:
        cache_dir: Root cache directory containing the host state snapshot.

    Returns:
        A list of summary strings describing what was torn down.
    """
    summary = clean_host(cache_dir)

    # Revert sysctl changes using saved snapshot
    try:
        reverted = restore_host(cache_dir)
        for change in reverted:
            summary.append(f"Reverted {change.setting}")
    except HostError:
        pass  # No saved state is acceptable

    # Remove the state snapshot file if it exists
    state_file = _state_file(cache_dir)
    if state_file.exists():
        try:
            state_file.unlink()
            summary.append("Removed host state snapshot")
        except OSError:
            pass

    return summary






def clean_host(cache_dir: Path) -> list[str]:
    """Remove all networking config (bridges, TAP devices, iptables rules).

    Does NOT revert sysctl, remove sudoers, or remove project group.
    Returns list of summary strings.
    """
    from fcm.core.network_manager import list_networks, remove_network

    summary: list[str] = []
    try:
        networks = list_networks()
    except subprocess.CalledProcessError:
        networks = []
    for net in networks:
        try:
            remove_network(net.name)
            summary.append(f"Removed network '{net.name}' (bridge: {net.bridge})")
        except subprocess.CalledProcessError as e:
            summary.append(f"Warning: failed to remove network '{net.name}': {e}")
    return summary


def reset_host(cache_dir: Path) -> list[str]:
    """Full rollback to pre-init state.

    Removes networking config, reverts sysctl, removes sudoers drop-in, and removes project group.
    Returns list of summary strings.
    """
    summary = clean_host(cache_dir)

    # Revert sysctl changes
    try:
        reverted = restore_host(cache_dir)
        for change in reverted:
            summary.append(f"Reverted {change.setting}")
    except HostError:
        pass  # No saved state is acceptable

    # Remove sudoers drop-in
    sudoers_path = Path(SUDOERS_DROP_IN_PATH)
    try:
        if _remove_sudoers(sudoers_path):
            summary.append(f"Removed sudoers file {sudoers_path}")
    except HostError as e:
        summary.append(f"Warning: {e}")

    # Remove project group
    try:
        if _remove_group(PROJECT_GROUP):
            summary.append(f"Removed group '{PROJECT_GROUP}'")
    except HostError as e:
        summary.append(f"Warning: {e}")

    # Remove state snapshot
    state_file = _state_file(cache_dir)
    if state_file.exists():
        try:
            state_file.unlink()
            summary.append("Removed host state snapshot")
        except OSError:
            pass

    return summary
