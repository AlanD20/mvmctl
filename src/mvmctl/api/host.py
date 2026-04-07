"""Host initialisation, state inspection, prune, clean, reset, and privilege API."""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    DEFAULT_NETWORK_NAME,
    PROJECT_GROUP,
    SUDOERS_DROP_IN_PATH,
    TAP_PREFIX,
    device_prefix,
)
from mvmctl.core.host_privilege import (
    _remove_group,
    _remove_sudoers,
    check_privileges,
    check_privileges_interactive,
)
from mvmctl.core.host_setup import (
    _ensure_kvm_modules,
    _persist_sysctl,
    check_cloud_localds,
    check_kvm_access,
    check_required_binaries,
    get_ip_forward_status,
    save_iptables_rules,
)
from mvmctl.core.host_state import (
    HostState,
    HostStateChange,
    _save_state,
    _state_file,
)
from mvmctl.core.host_state import (
    get_host_state as _get_host_state,
)
from mvmctl.core.host_state import (
    restore_host as _restore_host,
)
from mvmctl.core.image import clean_ready_pool as _clean_ready_pool
from mvmctl.core.image import get_ready_pool_dir as _get_ready_pool_dir
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.network import (
    delete_tap,
    teardown_all_mvm_chains_with_status,
    teardown_bridge,
    teardown_nat,
)
from mvmctl.core.vm_manager import get_vm_manager
from mvmctl.exceptions import HostError, NetworkError
from mvmctl.utils.fs import chown_to_real_user, get_cache_dir
from mvmctl.utils.network import bridge_exists, list_bridges, list_tuntap_devices

logger = logging.getLogger(__name__)

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
    import os

    from mvmctl.constants import (
        PROJECT_GROUP,
        SUDOERS_DROP_IN_PATH,
    )
    from mvmctl.core.host_privilege import (
        _add_user_to_group,
        _create_group,
        _generate_sudoers_content,
        _get_current_user,
        _validate_sudoers_binaries,
        _write_sudoers,
    )
    from mvmctl.core.network import setup_mvm_chains

    check_privileges("/usr/sbin/ip")

    if cache_dir is None:
        cache_dir = get_cache_dir()

    if os.getuid() != 0:
        raise HostError("Root privileges required")

    if not check_kvm_access():
        raise HostError("/dev/kvm is not accessible — check permissions or load KVM modules")

    missing = check_required_binaries()
    if missing:
        raise HostError(f"Missing required binaries: {', '.join(missing)}")

    _validate_sudoers_binaries()

    if not check_cloud_localds():
        logger.warning(
            "cloud-localds not found. Install cloud-image-utils (Debian/Ubuntu) or cloud-utils (Arch) package"
        )

    changes: list[HostStateChange] = []

    group_created = _create_group(PROJECT_GROUP)
    if group_created:
        changes.append(
            HostStateChange(
                setting=f"group:{PROJECT_GROUP}",
                original_value=None,
                applied_value=PROJECT_GROUP,
                mechanism="groupadd",
            )
        )

    username = _get_current_user()
    user_added = _add_user_to_group(username, PROJECT_GROUP)
    if user_added:
        changes.append(
            HostStateChange(
                setting=f"group_member:{username}",
                original_value=None,
                applied_value=f"{username}:{PROJECT_GROUP}",
                mechanism="usermod",
            )
        )

    sudoers_path = Path(SUDOERS_DROP_IN_PATH)
    sudoers_stale = True
    try:
        if sudoers_path.exists():
            existing = sudoers_path.read_text()
            expected = _generate_sudoers_content(PROJECT_GROUP)
            sudoers_stale = existing != expected
    except (PermissionError, OSError):
        pass
    if sudoers_stale:
        _write_sudoers(sudoers_path, PROJECT_GROUP)
        changes.append(
            HostStateChange(
                setting="sudoers_dropin",
                original_value=None,
                applied_value=str(sudoers_path),
                mechanism="file_create",
            )
        )

    change = _enable_ip_forward()
    if change:
        changes.append(change)

    change = _persist_sysctl()
    if change:
        changes.append(change)

    module_changes = _ensure_kvm_modules()
    changes.extend(module_changes)

    chains_already_exist = setup_mvm_chains()
    if chains_already_exist:
        logger.warning("MVM iptables chains already exist; keeping existing chain state")
        changes.append(
            HostStateChange(
                setting="iptables_chains",
                original_value=None,
                applied_value="MVM chains already exist",
                mechanism="noop",
            )
        )

    iptables_change = save_iptables_rules()
    if iptables_change:
        changes.append(iptables_change)

    db = MVMDatabase()
    _save_state(db, changes)
    _persist_host_state_to_db(db, changes)

    chown_to_real_user(cache_dir)

    from mvmctl.utils.audit import log_audit

    log_audit("host.init", f"changes={len(changes)}")

    return changes


def _enable_ip_forward() -> HostStateChange | None:
    """Enable IP forwarding if not already enabled."""
    import subprocess

    current = get_ip_forward_status()
    if current == "1":
        logger.debug("IP forwarding already enabled")
        return None

    try:
        subprocess.run(
            ["sysctl", "-w", "net.ipv4.ip_forward=1"],
            capture_output=True,
            text=True,
            check=True,
        )
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to enable IP forwarding: {e}") from e
    except FileNotFoundError as e:
        raise HostError("sysctl command not found") from e

    return HostStateChange(
        setting="net.ipv4.ip_forward",
        original_value=current,
        applied_value="1",
        mechanism="sysctl",
    )


def _persist_host_state_to_db(db: MVMDatabase, changes: list[HostStateChange]) -> None:
    """Persist host state changes to database."""
    import sqlite3
    import uuid
    from datetime import datetime, timezone

    from mvmctl.db.models import HostStateChange as DBHostStateChange
    from mvmctl.exceptions import MVMError

    try:
        now = datetime.now(timezone.utc).isoformat()
        db.initialize_host_state()

        group_created = any(
            c.setting.startswith("group:") and c.mechanism == "groupadd" for c in changes
        )
        sudoers_written = any(
            c.setting == "sudoers_dropin" and c.mechanism == "file_create" for c in changes
        )
        if group_created:
            db.update_host_component("mvm_group_created", True)
        if sudoers_written:
            db.update_host_component("sudoers_configured", True)

        session_id = str(uuid.uuid4())
        for order, change in enumerate(changes):
            db.add_host_change(
                DBHostStateChange(
                    session_id=session_id,
                    init_timestamp=now,
                    setting=change.setting,
                    mechanism=change.mechanism,
                    original_value=change.original_value,
                    applied_value=change.applied_value,
                    change_order=order,
                )
            )

        db.set_host_initialized(now)
    except (MVMError, sqlite3.OperationalError):
        return


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
    check_privileges("/usr/sbin/ip")

    if cache_dir is None:
        cache_dir = get_cache_dir()

    db = MVMDatabase()
    return _restore_host(db)


def _list_networks_from_api() -> list[Any]:
    """Get list of networks from api/network module.

    Returns:
        List of NetworkConfig objects.
    """
    from mvmctl.api.network import list_networks

    return list_networks()


def clean_host(cache_dir: Path | None = None) -> list[str]:
    """Remove all networking config (bridges, TAP devices, iptables rules, MVM chains).

    Args:
        cache_dir: Root cache directory. If None, uses default cache dir.

    Returns:
        List of summary strings describing what was cleaned.
    """
    if cache_dir is None:
        cache_dir = get_cache_dir()

    summary: list[str] = []

    # Remove TAP devices
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

    # Get networks from API layer
    metadata_bridges: set[str] = set()
    try:
        networks = _list_networks_from_api()
    except NetworkError as e:
        summary.append(
            f"Warning: skipped network inventory cleanup (already clean or insufficient privileges): {e}"
        )
        networks = []
    metadata_bridges.update(net.bridge for net in networks)

    # Teardown NAT and bridges for each network
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

    # Remove default bridge if it exists
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

    # Remove orphan bridges
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

    from mvmctl.utils.audit import log_audit

    log_audit("host.clean", f"actions={len(summary)}")

    return summary


def reset_host(cache_dir: Path | None = None) -> list[str]:
    """Full rollback to pre-init state.

    Args:
        cache_dir: Root cache directory. If None, uses default cache dir.

    Returns:
        List of summary strings describing what was reset.
    """
    check_privileges("/usr/sbin/ip")

    if cache_dir is None:
        cache_dir = get_cache_dir()

    db = MVMDatabase()
    summary = clean_host(cache_dir)

    try:
        reverted = _restore_host(db)
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

    from mvmctl.utils.audit import log_audit

    log_audit("host.reset", f"actions={len(summary)}")

    return summary


def prune_host(cache_dir: Path | None = None) -> list[str]:
    """Tear down all bridges, TAPs, iptables rules and revert host sysctl changes.

    Does NOT remove VM cache files, images, kernels, or binaries.

    Args:
        cache_dir: Root cache directory. If None, uses default cache dir.

    Returns:
        A list of summary strings describing what was torn down.
    """
    check_privileges("/usr/sbin/ip")

    if cache_dir is None:
        cache_dir = get_cache_dir()

    db = MVMDatabase()
    summary = clean_host(cache_dir)

    try:
        reverted = _restore_host(db)
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


def clean_ready_pool() -> int:
    """Clean the ready pool and return the count of removed images."""
    return _clean_ready_pool()


def get_ready_pool_dir() -> Path:
    """Get the path to the ready pool directory."""
    return _get_ready_pool_dir()
