"""Host configuration management for Firecracker prerequisites."""

from __future__ import annotations

import logging
from pathlib import Path

from fcm.constants import PROJECT_GROUP, SUDOERS_DROP_IN_PATH
from fcm.core.host_privilege import (
    _remove_group,
    _remove_sudoers,
    check_privileges,
    check_privileges_interactive,
)
from fcm.core.host_setup import (
    check_kvm_access,
    check_required_binaries,
    get_ip_forward_status,
    init_host,
)
from fcm.core.host_state import HostChange, HostState, _state_file, get_host_state, restore_host
from fcm.exceptions import HostError

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
    "check_privileges_interactive",
    "_remove_sudoers",
    "_remove_group",
    "check_kvm_access",
    "check_required_binaries",
    "get_ip_forward_status",
    "init_host",
]

logger = logging.getLogger(__name__)


def prune_host(cache_dir: Path) -> list[str]:
    """Tear down all bridges, TAPs, iptables rules and revert host sysctl changes.

    Does NOT remove VM cache files, images, kernels, or binaries.

    Args:
        cache_dir: Root cache directory containing the host state snapshot.

    Returns:
        A list of summary strings describing what was torn down.
    """
    summary = clean_host(cache_dir)

    try:
        reverted = restore_host(cache_dir)
        for change in reverted:
            summary.append(f"Reverted {change.setting}")
    except HostError as e:
        logger.warning("No saved host state to restore: %s", e)

    state_file = _state_file(cache_dir)
    if state_file.exists():
        try:
            state_file.unlink()
            summary.append("Removed host state snapshot")
        except OSError:
            pass

    return summary


def clean_host(cache_dir: Path) -> list[str]:
    """Remove all networking config (bridges, TAP devices, iptables rules, FCM chains).

    Does NOT revert sysctl, remove sudoers, or remove project group.
    Returns list of summary strings.
    """
    from fcm.core.network import teardown_fcm_chains
    from fcm.core.network_manager import list_networks, remove_network
    from fcm.exceptions import NetworkError

    summary: list[str] = []
    try:
        networks = list_networks()
    except NetworkError:
        networks = []
    for net in networks:
        try:
            remove_network(net.name)
            summary.append(f"Removed network '{net.name}' (bridge: {net.bridge})")
        except NetworkError as e:
            summary.append(f"Warning: failed to remove network '{net.name}': {e}")

    # Remove FCM iptables chains after networks are removed
    try:
        teardown_fcm_chains()
        summary.append("Removed FCM iptables chains")
    except NetworkError as e:
        summary.append(f"Warning: failed to remove FCM chains: {e}")

    return summary


def reset_host(cache_dir: Path) -> list[str]:
    """Full rollback to pre-init state.

    Removes networking config, reverts sysctl, removes sudoers drop-in, and removes project group.
    Returns list of summary strings.
    """
    summary = clean_host(cache_dir)

    try:
        reverted = restore_host(cache_dir)
        for change in reverted:
            summary.append(f"Reverted {change.setting}")
    except HostError as e:
        logger.warning("No saved host state to restore: %s", e)

    sudoers_path = Path(SUDOERS_DROP_IN_PATH)
    try:
        if _remove_sudoers(sudoers_path):
            summary.append(f"Removed sudoers file {sudoers_path}")
    except HostError as e:
        summary.append(f"Warning: {e}")

    try:
        if _remove_group(PROJECT_GROUP):
            summary.append(f"Removed group '{PROJECT_GROUP}'")
    except HostError as e:
        summary.append(f"Warning: {e}")

    state_file = _state_file(cache_dir)
    if state_file.exists():
        try:
            state_file.unlink()
            summary.append("Removed host state snapshot")
        except OSError:
            pass

    return summary
