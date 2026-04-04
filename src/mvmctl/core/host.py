"""Host configuration management for Firecracker prerequisites."""

from __future__ import annotations

import logging
from pathlib import Path

from mvmctl.constants import PROJECT_GROUP, SUDOERS_DROP_IN_PATH
from mvmctl.core.host_privilege import (
    _remove_group,
    _remove_sudoers,
    check_privileges,
    check_privileges_interactive,
)
from mvmctl.core.host_setup import (
    check_kvm_access,
    check_required_binaries,
    get_ip_forward_status,
    init_host,
)
from mvmctl.core.host_state import (
    HostState,
    HostStateChange,
    _state_file,
    get_host_state,
    restore_host,
)
from mvmctl.exceptions import HostError

__all__ = [
    "clean_host",
    "prune_host",
    "reset_host",
    "HostState",
    "HostStateChange",
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
    """Remove all networking config (bridges, TAP devices, iptables rules, MVM chains).

    Does NOT revert sysctl, remove sudoers, or remove project group.
    Preserves network metadata to allow restoration during host init.
    Returns list of summary strings.
    """
    from mvmctl.constants import DEFAULT_NETWORK_NAME, TAP_PREFIX, device_prefix
    from mvmctl.core.network import (
        bridge_exists,
        delete_tap,
        list_bridges,
        list_tuntap_devices,
        teardown_all_mvm_chains_with_status,
        teardown_bridge,
        teardown_nat,
    )
    from mvmctl.core.network_manager import list_networks
    from mvmctl.exceptions import NetworkError

    summary: list[str] = []

    tap_names = list_tuntap_devices()
    fallback_tap_candidates = sorted(
        {tap for tap in tap_names if tap.startswith(TAP_PREFIX) or tap.startswith("mvm-")}
    )
    for tap_name in fallback_tap_candidates:
        try:
            delete_tap(tap_name)
            summary.append(f"Removed TAP device '{tap_name}'")
        except NetworkError as e:
            summary.append(f"Warning: failed to remove TAP '{tap_name}': {e}")

    metadata_bridges: set[str] = set()
    try:
        networks = list_networks()
    except NetworkError as e:
        summary.append(
            f"Warning: skipped network inventory cleanup (already clean or insufficient privileges): {e}"
        )
        networks = []
    metadata_bridges.update(net.bridge for net in networks)

    for net in networks:
        if net.nat_enabled:
            try:
                teardown_nat(bridge=net.bridge, force=True)
            except NetworkError:
                pass
        try:
            teardown_bridge(net.bridge)
            summary.append(f"Removed network '{net.name}' (bridge: {net.bridge})")
        except NetworkError as e:
            summary.append(
                f"Warning: failed to remove network '{net.name}' "
                f"(already clean or insufficient privileges): {e}"
            )

    default_bridge = f"{device_prefix()}-{DEFAULT_NETWORK_NAME[:10]}"
    if bridge_exists(default_bridge):
        try:
            teardown_nat(bridge=default_bridge, force=True)
        except NetworkError:
            pass
        try:
            teardown_bridge(default_bridge)
            summary.append(f"Removed orphan bridge '{default_bridge}'")
        except NetworkError as e:
            summary.append(
                f"Warning: failed to remove orphan bridge '{default_bridge}' "
                f"(already clean or insufficient privileges): {e}"
            )

    for bridge in list_bridges():
        if not bridge.startswith(f"{device_prefix()}-"):
            continue
        if bridge == default_bridge:
            continue
        if bridge in metadata_bridges:
            continue

        try:
            teardown_nat(bridge=bridge, force=True)
        except NetworkError:
            pass
        try:
            teardown_bridge(bridge)
            summary.append(f"Removed orphan bridge '{bridge}'")
        except NetworkError as e:
            summary.append(
                f"Warning: failed to remove orphan bridge '{bridge}' "
                f"(already clean or insufficient privileges): {e}"
            )

    summary.extend(teardown_all_mvm_chains_with_status())

    if not summary:
        summary.append("Warning: skipped host networking cleanup (already clean)")

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
