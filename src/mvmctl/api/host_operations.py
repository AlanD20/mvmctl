"""Host operations - cross-domain orchestration for host management."""

from __future__ import annotations  # ruff: isort: skip

import logging
import os
import pwd
import subprocess
import uuid
from collections.abc import Callable
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    CLI_NAME,
    MVM_UNIX_GROUP,
    SUDOERS_DROP_IN_PATH,
)
from mvmctl.core._shared import Database
from mvmctl.core.config._service import SettingsService
from mvmctl.core.host._controller import HostController
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.host._repository import HostRepository
from mvmctl.core.host._service import HostService
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.core.vm._repository import VMRepository
from mvmctl.exceptions import HostError, NetworkError, PrivilegeError
from mvmctl.models import (
    HostStateChangeItem,
    HostStateItem,
    VMInstanceItem,
    VMStatus,
)
from mvmctl.models.result import (
    NeedsInteraction,
    OperationResult,
    ProgressEvent,
)
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.fs import FsUtils
from mvmctl.utils.network import NetworkUtils

logger = logging.getLogger(__name__)


class HostOperation:
    """Host management orchestration."""

    @staticmethod
    def init(
        cache_dir: Path,
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> OperationResult[Any] | NeedsInteraction:
        """Initialize host configuration.

        Returns:
            OperationResult on success/error/skipped, or NeedsInteraction
            when root privileges are needed.
        """
        try:
            HostPrivilegeHelper.check_privileges(
                "/usr/sbin/ip", "initialize host"
            )
        except PrivilegeError:
            return NeedsInteraction(
                code="privilege.sudo_required",
                message="Elevated privileges required for host initialization",
                input_type="sudo",
                context={
                    "command": "sudo mvm host init",
                    "operation": "initialize host",
                },
            )

        # Ensure DB schema exists before any DB writes.
        Database().migrate()

        if os.getuid() != 0:
            return NeedsInteraction(
                code="privilege.sudo_required",
                message="Root privileges required for host initialization",
                input_type="sudo",
                context={
                    "command": "sudo mvm host init",
                    "operation": "initialize host",
                },
            )

        # Chown the cache directory to the real user immediately so that
        # anything we create during init is accessible after sudo exits.
        FsUtils.chown_to_real_user(cache_dir)

        # --- System health checks ---
        if not HostService.check_kvm_access():
            kvm = Path("/dev/kvm")
            if not kvm.exists():
                return OperationResult(
                    status="error",
                    code="host.kvm.missing",
                    message=(
                        "/dev/kvm does not exist.\n\n"
                        "KVM kernel modules are not loaded. Run:\n"
                        "  sudo modprobe kvm\n"
                        "  sudo modprobe kvm_intel   # or kvm_amd\n\n"
                        "If modules are missing, install qemu-kvm or linux-modules-extra."
                    ),
                )
            return OperationResult(
                status="error",
                code="host.kvm.unreadable",
                message=(
                    "/dev/kvm exists but is not readable/writable.\n\n"
                    "Fix permissions with one of:\n"
                    "  1. Add your user to the 'kvm' group:\n"
                    "       sudo usermod -aG kvm $USER && newgrp kvm\n"
                    "  2. Or run with sudo: sudo mvm host init\n\n"
                    f"Current permissions: {kvm.stat().st_mode.__format__('o')}"
                ),
            )

        has_conflict, diagnosis = (
            NetworkUtils.detect_iptables_backend_conflict()
        )
        if has_conflict:
            return OperationResult(
                status="error",
                code="host.iptables.conflict",
                message=(
                    "Mixed iptables backend detected. VM networking may not work correctly.\n"
                    "See troubleshooting: docs/TROUBLESHOOTING.md#mixed-iptables-backend\n"
                    f"Diagnosis: {diagnosis}\n\n"
                    "Remediation:\n"
                    "  1. Quick fix (clears orphaned legacy rules): sudo iptables-legacy -F\n"
                    "  2. Reboot host (clears both backends cleanly)\n"
                    "  3. Configure Docker to use same backend: edit /etc/docker/daemon.json\n\n"
                    "Then re-run: mvm host init"
                ),
            )

        missing = HostService.check_required_binaries()
        if missing:
            return OperationResult(
                status="error",
                code="host.binaries.missing",
                message=f"Missing required binaries: {', '.join(missing)}",
            )

        HostService.validate_sudoers_binaries()

        if not HostService.check_cloud_localds():
            logger.warning(
                "cloud-localds not found. Install cloud-image-utils (Debian/Ubuntu) "
                "or cloud-utils (Arch) package"
            )

        # --- Privilege setup (group, user, sudoers) ---
        db_changes: list[HostStateChangeItem] = []
        all_changes: list[HostStateChangeItem] = []

        group_created = HostService.create_group(MVM_UNIX_GROUP)
        if group_created:
            change_item = HostStateChangeItem(
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
            db_changes.append(change_item)
            all_changes.append(change_item)

        username = (
            os.environ.get("SUDO_USER") or pwd.getpwuid(os.getuid()).pw_name
        )
        user_added = HostService.add_user_to_group(username, MVM_UNIX_GROUP)
        if user_added:
            change_item = HostStateChangeItem(
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
            db_changes.append(change_item)
            all_changes.append(change_item)

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
            change_item = HostStateChangeItem(
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
            db_changes.append(change_item)
            all_changes.append(change_item)

        # --- Network & kernel setup ---
        change = HostService.enable_ip_forward()
        if change:
            db_changes.append(change)
            all_changes.append(change)

        change = HostService.persist_sysctl()
        if change:
            db_changes.append(change)
            all_changes.append(change)

        # Create repo early so module loads can be recorded with a session ID.
        repo = HostRepository()
        repo.initialize_state()
        session_id = str(uuid.uuid4())

        module_changes, next_order = HostService.ensure_kvm_modules(
            repo=repo, session_id=session_id, change_order_start=0
        )
        all_changes.extend(module_changes)

        net_repo = NetworkRepository()
        net_service = NetworkService(net_repo)
        net_service.ensure_mvm_chains()
        chain_change = HostStateChangeItem(
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
        db_changes.append(chain_change)
        all_changes.append(chain_change)

        # --- Default network (first-time init or post-reboot restore) ---
        from mvmctl.api.network_operations import NetworkOperation

        try:
            restored_result = NetworkOperation.restore()
            if restored_result.is_ok and not restored_result.item:
                default_result = NetworkOperation.create_default_network()
                if default_result.is_error:
                    logger.warning(
                        "Could not create default network: %s",
                        default_result.message,
                    )
                else:
                    net_change = HostStateChangeItem(
                        session_id="",
                        init_timestamp="",
                        setting="default_network",
                        original_value=None,
                        applied_value=str(
                            SettingsService.resolve(
                                Database(),
                                "defaults.network",
                                "name",
                            )
                        ),
                        mechanism="network_create",
                        reverted=False,
                        change_order=0,
                        created_at="",
                    )
                    db_changes.append(net_change)
                    all_changes.append(net_change)
        except Exception:
            logger.warning("Could not set up default network during host init")

        iptables_change = HostService.save_iptables_rules()
        if iptables_change:
            db_changes.append(iptables_change)
            all_changes.append(iptables_change)

        # --- Persist state & finalize ---
        controller = HostController(repo)
        try:
            controller.record_changes(
                db_changes,
                session_id=session_id,
                change_order_offset=next_order,
            )
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

        now = datetime.now(UTC).isoformat()
        try:
            controller.mark_initialized(now)
        except Exception as e:
            logger.warning("Could not mark host as initialized: %s", e)

        FsUtils.chown_to_real_user(cache_dir)

        AuditLog.log("host.init", {"changes": len(all_changes)})

        if not all_changes:
            return OperationResult(
                status="skipped",
                code="host.init.noop",
                message="Host already configured — nothing to do.",
            )

        return OperationResult(
            status="success",
            code="host.init.complete",
            message=f"Host initialized ({len(all_changes)} change(s) applied).",
            metadata={
                "changes": all_changes,
                "user_added_to_group": user_added,
            },
        )

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
    def clean(cache_dir: Path) -> OperationResult[list[str]]:
        """Clean host networking configuration."""
        try:
            HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "clean host")

            summary: list[str] = []

            # Remove TAP devices
            tap_names = NetworkUtils.get_tuntap_devices()
            fallback_tap_candidates = sorted(
                {tap for tap in tap_names if tap.startswith(f"{CLI_NAME}-")}
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
            default_net_name = str(
                SettingsService.resolve(Database(), "defaults.network", "name")
            )
            default_bridge = f"{CLI_NAME}-{default_net_name[:10]}"
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
                if not bridge.startswith(f"{CLI_NAME}-"):
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
                (n for n in networks if n.name == default_net_name), None
            )
            if default_net:
                try:
                    from mvmctl.api.inputs._network_input import NetworkInput
                    from mvmctl.api.network_operations import NetworkOperation

                    remove_result = NetworkOperation.remove(
                        NetworkInput(name=[default_net_name]), force=True
                    )
                    if remove_result.is_error:
                        summary.append(
                            f"Warning: failed to remove default network: {remove_result.message}"
                        )
                    else:
                        summary.append(
                            f"Removed default network '{default_net_name}'"
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

            return OperationResult(
                status="success",
                code="host.cleaned",
                message=f"Cleaned {len(summary)} networking item(s)",
                item=summary,
            )
        except (HostError, NetworkError) as e:
            return OperationResult(
                status="error",
                code="host.clean_failed",
                message=str(e),
                exception=e,
            )

    @staticmethod
    def reset(cache_dir: Path) -> OperationResult[list[str]]:
        """Reset host to pre-init state."""
        try:
            HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "reset host")

            clean_result = HostOperation.clean(cache_dir)
            if clean_result.is_error:
                return clean_result
            summary = list(clean_result.item) if clean_result.item else []

            repo = HostRepository()
            service = HostService(repo)
            try:
                reverted = service.restore_state()
                for change in reverted:
                    summary.append(f"Reverted {change.setting}")
            except HostError as e:
                logger.warning("No saved host state to restore: %s", e)

            # Notify about kernel modules that were loaded but not reverted
            module_changes = [
                c
                for c in repo.list_changes(include_reverted=False)
                if c.setting == "kernel_module_load"
            ]
            if module_changes:
                modules = [c.applied_value for c in module_changes]
                summary.append(
                    f"Modules loaded by mvm: {modules}. These were left loaded. "
                    f"Unload manually with 'modprobe -r <module>' if desired."
                )

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
                    if HostService.remove_user_from_group(
                        username, MVM_UNIX_GROUP
                    ):
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

            return OperationResult(
                status="success",
                code="host.reset",
                message=f"Reset {len(summary)} item(s)",
                item=summary,
            )
        except (HostError, NetworkError) as e:
            return OperationResult(
                status="error",
                code="host.reset_failed",
                message=str(e),
                exception=e,
            )

    @staticmethod
    def get_running_vms() -> list[VMInstanceItem]:
        """Get list of currently running VMs."""
        return VMRepository().list_by_status(VMStatus.RUNNING)


__all__ = ["HostOperation"]
