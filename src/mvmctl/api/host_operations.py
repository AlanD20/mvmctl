"""Host operations - cross-domain orchestration for host management."""

from __future__ import annotations

import logging
import os
import pwd
import subprocess
from datetime import datetime, timezone
from pathlib import Path

from mvmctl.constants import (
    DEFAULT_NETWORK_NAME,
    MVM_UNIX_GROUP,
    SUDOERS_DROP_IN_PATH,
    TAP_PREFIX,
    device_prefix,
)
from mvmctl.core._internal._db import Database
from mvmctl.core.host._controller import HostController
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.host._repository import HostRepository
from mvmctl.core.host._service import HostService
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.core.vm._repository import VMRepository
from mvmctl.exceptions import HostError, NetworkError
from mvmctl.models.host import HostStateChangeItem, HostStateItem
from mvmctl.models.vm import VMInstanceItem, VMStatus
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.fs import chown_to_real_user
from mvmctl.utils.network import NetworkUtils

logger = logging.getLogger(__name__)


class HostOperation:
    """Host management orchestration."""

    @staticmethod
    def init(cache_dir: Path) -> list[HostStateChangeItem]:
        """Initialize host configuration."""
        HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "initialize host")

        # Ensure DB schema exists before any DB writes.
        Database().migrate()

        if os.getuid() != 0:
            raise HostError("Root privileges required")

        # Chown the cache directory to the real user immediately so that
        # anything we create during init is accessible after sudo exits.
        chown_to_real_user(cache_dir)

        # --- System health checks ---
        if not HostService.check_kvm_access():
            kvm = Path("/dev/kvm")
            if not kvm.exists():
                raise HostError(
                    "/dev/kvm does not exist.\n\n"
                    "KVM kernel modules are not loaded. Run:\n"
                    "  sudo modprobe kvm\n"
                    "  sudo modprobe kvm_intel   # or kvm_amd\n\n"
                    "If modules are missing, install qemu-kvm or linux-modules-extra."
                )
            raise HostError(
                "/dev/kvm exists but is not readable/writable.\n\n"
                "Fix permissions with one of:\n"
                "  1. Add your user to the 'kvm' group:\n"
                "       sudo usermod -aG kvm $USER && newgrp kvm\n"
                "  2. Or run with sudo: sudo mvm host init\n\n"
                "Current permissions: " + kvm.stat().st_mode.__format__("o")
            )

        has_conflict, diagnosis = (
            NetworkUtils.detect_iptables_backend_conflict()
        )
        if has_conflict:
            raise HostError(
                "Mixed iptables backend detected. VM networking may not work correctly.\n"
                "See troubleshooting: docs/TROUBLESHOOTING.md#mixed-iptables-backend\n"
                f"Diagnosis: {diagnosis}\n\n"
                "Remediation:\n"
                "  1. Quick fix (clears orphaned legacy rules): sudo iptables-legacy -F\n"
                "  2. Reboot host (clears both backends cleanly)\n"
                "  3. Configure Docker to use same backend: edit /etc/docker/daemon.json\n\n"
                "Then re-run: mvm host init"
            )

        missing = HostService.check_required_binaries()
        if missing:
            raise HostError(f"Missing required binaries: {', '.join(missing)}")

        HostService.validate_sudoers_binaries()

        if not HostService.check_cloud_localds():
            logger.warning(
                "cloud-localds not found. Install cloud-image-utils (Debian/Ubuntu) "
                "or cloud-utils (Arch) package"
            )

        # --- Privilege setup (group, user, sudoers) ---
        changes: list[HostStateChangeItem] = []

        group_created = HostService.create_group(MVM_UNIX_GROUP)
        if group_created:
            changes.append(
                HostStateChangeItem(
                    session_id="",
                    init_timestamp="",
                    setting=f"group:{MVM_UNIX_GROUP}",
                    original_value=None,
                    applied_value=MVM_UNIX_GROUP,
                    mechanism="groupadd",
                    reverted=False,
                    change_order=0,
                    created_at="",
                )
            )

        username = (
            os.environ.get("SUDO_USER") or pwd.getpwuid(os.getuid()).pw_name
        )
        user_added = HostService.add_user_to_group(username, MVM_UNIX_GROUP)
        if user_added:
            changes.append(
                HostStateChangeItem(
                    session_id="",
                    init_timestamp="",
                    setting=f"group_member:{username}",
                    original_value=None,
                    applied_value=f"{username}:{MVM_UNIX_GROUP}",
                    mechanism="usermod",
                    reverted=False,
                    change_order=0,
                    created_at="",
                )
            )

        sudoers_path = Path(SUDOERS_DROP_IN_PATH)
        sudoers_stale = True
        try:
            if sudoers_path.exists():
                existing = sudoers_path.read_text()
                expected = HostService._generate_sudoers_content(MVM_UNIX_GROUP)
                sudoers_stale = existing != expected
        except (PermissionError, OSError):
            pass
        if sudoers_stale:
            HostService.write_sudoers(sudoers_path, MVM_UNIX_GROUP)
            changes.append(
                HostStateChangeItem(
                    session_id="",
                    init_timestamp="",
                    setting="sudoers_dropin",
                    original_value=None,
                    applied_value=str(sudoers_path),
                    mechanism="file_create",
                    reverted=False,
                    change_order=0,
                    created_at="",
                )
            )

        # --- Network & kernel setup ---
        change = HostService.enable_ip_forward()
        if change:
            changes.append(change)

        change = HostService.persist_sysctl()
        if change:
            changes.append(change)

        module_changes = HostService.ensure_kvm_modules()
        changes.extend(module_changes)

        net_repo = NetworkRepository()
        net_service = NetworkService(net_repo)
        net_service.ensure_mvm_chains()
        changes.append(
            HostStateChangeItem(
                session_id="",
                init_timestamp="",
                setting="iptables_chains",
                original_value=None,
                applied_value="MVM chains ensured",
                mechanism="iptables",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )

        iptables_change = HostService.save_iptables_rules()
        if iptables_change:
            changes.append(iptables_change)

        # --- Persist state & finalize ---
        repo = HostRepository()
        controller = HostController(repo)
        try:
            controller.record_changes(changes)
        except Exception as e:
            logger.warning("Could not record host changes to DB: %s", e)
        if group_created:
            try:
                repo.update_component("mvm_group_created", True)
            except Exception as e:
                logger.warning("Could not update host state: %s", e)
        if sudoers_stale:
            try:
                repo.update_component("sudoers_configured", True)
            except Exception as e:
                logger.warning("Could not update host state: %s", e)

        now = datetime.now(timezone.utc).isoformat()
        try:
            controller.mark_initialized(now)
        except Exception as e:
            logger.warning("Could not mark host as initialized: %s", e)

        chown_to_real_user(cache_dir)

        AuditLog.log("host.init", {"changes": len(changes)})

        return changes

    @staticmethod
    def get_state() -> HostStateItem | None:
        """Get current host state snapshot."""
        return HostRepository().get_state()

    @staticmethod
    def check_kvm_access() -> bool:
        """Check if /dev/kvm is accessible."""
        return HostService.check_kvm_access()

    @staticmethod
    def check_required_binaries() -> list[str]:
        """Check for missing required binaries."""
        return HostService.check_required_binaries()

    @staticmethod
    def get_ip_forward_status() -> str:
        """Get current IP forwarding status."""
        return HostService._get_ip_forward_status()

    @staticmethod
    def clean(cache_dir: Path) -> list[str]:
        """Clean host networking configuration."""
        HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "clean host")

        summary: list[str] = []

        # Remove TAP devices
        tap_names = NetworkUtils.get_tuntap_devices()
        fallback_tap_candidates = sorted(
            {
                tap
                for tap in tap_names
                if tap.startswith(TAP_PREFIX) or tap.startswith("mvm-")
            }
        )
        for tap_name in fallback_tap_candidates:
            try:
                NetworkUtils._run_batch(
                    [
                        f"link set {tap_name} down",
                        f"link delete {tap_name}",
                    ]
                )
                summary.append(f"Removed TAP device '{tap_name}'")
            except (NetworkError, subprocess.CalledProcessError) as e:
                summary.append(
                    f"Warning: failed to remove TAP '{tap_name}': {e}"
                )

        # Get networks from repository
        try:
            net_repo = NetworkRepository()
            networks = net_repo.list_all()
        except Exception:
            networks = []
        metadata_bridges: set[str] = {net.bridge for net in networks}

        # Teardown NAT and bridges for each network
        net_service = NetworkService(net_repo)
        for net in networks:
            if net.nat_enabled:
                try:
                    net_service.remove_nat(
                        net.bridge,
                        net.nat_gateways_list,
                        subnet=net.subnet,
                        network_id=net.id,
                    )
                except NetworkError:
                    pass
            try:
                net_service.remove_bridge(net.bridge, network_id=net.id)
                summary.append(
                    f"Removed network '{net.name}' (bridge: {net.bridge})"
                )
            except NetworkError as e:
                summary.append(
                    f"Warning: failed to remove network '{net.name}' "
                    f"(already clean or insufficient privileges): {e}"
                )

        # Remove default bridge if it exists
        default_bridge = f"{device_prefix()}-{DEFAULT_NETWORK_NAME[:10]}"
        if NetworkUtils.bridge_exists(default_bridge):
            try:
                NetworkUtils._run_batch(
                    [
                        f"link set {default_bridge} down",
                        f"link delete {default_bridge} type bridge",
                    ]
                )
                summary.append(f"Removed orphan bridge '{default_bridge}'")
            except (NetworkError, subprocess.CalledProcessError) as e:
                summary.append(
                    f"Warning: failed to remove orphan bridge '{default_bridge}' "
                    f"(already clean or insufficient privileges): {e}"
                )

        # Remove orphan bridges
        for bridge in NetworkUtils.get_bridges():
            if not bridge.startswith(f"{device_prefix()}-"):
                continue
            if bridge == default_bridge:
                continue
            if bridge in metadata_bridges:
                continue

            try:
                NetworkUtils._run_batch(
                    [
                        f"link set {bridge} down",
                        f"link delete {bridge} type bridge",
                    ]
                )
                summary.append(f"Removed orphan bridge '{bridge}'")
            except (NetworkError, subprocess.CalledProcessError) as e:
                summary.append(
                    f"Warning: failed to remove orphan bridge '{bridge}' "
                    f"(already clean or insufficient privileges): {e}"
                )

        # Remove default network from database
        default_net = next(
            (n for n in networks if n.name == DEFAULT_NETWORK_NAME), None
        )
        if default_net:
            try:
                from mvmctl.api.inputs._network_input import NetworkInput
                from mvmctl.api.network_operations import NetworkOperation

                NetworkOperation.remove(
                    NetworkInput(name=[DEFAULT_NETWORK_NAME]), force=True
                )
                summary.append(
                    f"Removed default network '{DEFAULT_NETWORK_NAME}'"
                )
            except NetworkError as e:
                summary.append(
                    f"Warning: failed to remove default network: {e}"
                )

        # Remove MVM chains
        try:
            net_service.remove_mvm_chains()
            summary.append("Removed MVM iptables chains")
        except NetworkError as e:
            summary.append(
                f"Warning: failed to remove MVM iptables chains: {e}"
            )

        if not summary:
            summary.append(
                "Warning: skipped host networking cleanup (already clean)"
            )

        AuditLog.log("host.clean", {"actions": len(summary)})

        return summary

    @staticmethod
    def reset(cache_dir: Path) -> list[str]:
        """Reset host to pre-init state."""
        HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "reset host")

        summary = HostOperation.clean(cache_dir)

        repo = HostRepository()
        service = HostService(repo)
        try:
            reverted = service.restore_state()
            for change in reverted:
                summary.append(f"Reverted {change.setting}")
        except HostError as e:
            logger.warning("No saved host state to restore: %s", e)

        sudoers_path = Path(SUDOERS_DROP_IN_PATH)
        try:
            if HostService.remove_sudoers(sudoers_path):
                summary.append(f"Removed sudoers file {sudoers_path}")
        except HostError as e:
            summary.append(f"Warning: {e}")

        # Remove user from group first, then remove group
        usermod_changes = [
            c
            for c in repo.list_changes(include_reverted=False)
            if c.mechanism == "usermod"
        ]
        if usermod_changes:
            # Extract username from applied_value like "user:group"
            applied = usermod_changes[-1].applied_value
            username = applied.split(":")[0] if ":" in applied else applied
            try:
                if HostService.remove_user_from_group(username, MVM_UNIX_GROUP):
                    summary.append(
                        f"Removed user '{username}' from group "
                        f"'{MVM_UNIX_GROUP}'"
                    )
            except HostError as e:
                summary.append(f"Warning: {e}")

        # Now remove the group
        try:
            if HostService.remove_group(MVM_UNIX_GROUP):
                summary.append(f"Removed group '{MVM_UNIX_GROUP}'")
        except HostError as e:
            summary.append(f"Warning: {e}")

        repo.reset_state()

        AuditLog.log("host.reset", {"actions": len(summary)})

        return summary

    @staticmethod
    def prune(cache_dir: Path) -> list[str]:
        """Tear down all bridges, TAPs, iptables rules and revert host sysctl changes."""
        HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune host")

        summary = HostOperation.clean(cache_dir)

        repo = HostRepository()
        service = HostService(repo)
        try:
            reverted = service.restore_state()
            for change in reverted:
                summary.append(f"Reverted {change.setting}")
        except HostError as e:
            logger.warning("No saved host state to restore: %s", e)

        return summary

    @staticmethod
    def get_running_vms() -> list[VMInstanceItem]:
        """Get list of currently running VMs."""
        return VMRepository().list_by_status(VMStatus.RUNNING)


__all__ = ["HostOperation"]
