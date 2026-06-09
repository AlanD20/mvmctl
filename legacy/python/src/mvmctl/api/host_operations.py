"""Host operations - cross-domain orchestration for host management."""

from __future__ import annotations  # ruff: isort: skip

import logging
import os
import pwd
import re
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
from mvmctl.core.host._detector import HostDetector
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.host._probe import HostProbe
from mvmctl.core.host._repository import HostRepository
from mvmctl.core.host._service import HostService
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.core.vm._repository import VMRepository
from mvmctl.exceptions import HostError, NetworkError, PrivilegeError
from mvmctl.models import (
    HostHardware,
    HostInfo,
    HostLimits,
    HostResources,
    HostStateChangeItem,
    HostStateItem,
    ProbeResult,
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
    def _detect_session_has_group() -> bool:
        """Check if the current process has the mvm group GID active.

        Delegates to ``HostPrivilegeHelper.session_has_group()``.
        """
        return HostPrivilegeHelper.session_has_group()

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
                    "session_has_group": HostOperation._detect_session_has_group(),
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
                    "session_has_group": HostOperation._detect_session_has_group(),
                },
            )

        # Chown the cache directory to the real user immediately so that
        # anything we create during init is accessible after sudo exits.
        FsUtils.chown_to_real_user(cache_dir)

        # --- Extract embedded service binaries (compiled mode only) ---
        try:
            from mvmctl.constants import is_compiled_mode

            if is_compiled_mode():
                from mvmctl.core.binary._repository import BinaryRepository
                from mvmctl.core.binary._service import BinaryService

                bin_repo = BinaryRepository(Database())
                BinaryService(bin_repo).extract_service_binaries()
        except Exception:
            logger.exception("Failed to extract embedded service binaries")

        # --- Phase 1: Pre-flight probes ---
        probe_result = HostProbe.run_all()
        if probe_result.has_critical:
            critical_names = ", ".join(c.name for c in probe_result.critical)
            return OperationResult(
                status="error",
                code="host.init.probe_failed",
                message=f"Probe failures: {critical_names}",
                metadata={
                    "probe_result": probe_result,
                },
            )

        # --- iptables comment module check ---
        from mvmctl.core.config._repository import SettingsRepository

        db = Database()
        settings_svc = SettingsService(SettingsRepository(db))
        current_backend = settings_svc.resolve(
            db, "settings", "firewall_backend"
        )
        if current_backend == "iptables":
            from mvmctl.core._shared._iptables_tracker._tracker import (
                IPTablesTracker,
            )

            if not IPTablesTracker.check_comment_available():
                logger.info(
                    "iptables comment module (xt_comment) not available; "
                    "rule comments will be skipped"
                )
                try:
                    settings_svc.set(
                        "settings.firewall", "iptables_xtcomment", False
                    )
                except Exception:
                    pass

        # --- Phase 2: Setup host environment ---
        repo = HostRepository()
        repo.initialize_state()
        session_id = str(uuid.uuid4())

        all_changes = HostOperation._setup_host_environment(
            repo=repo,
            session_id=session_id,
        )

        # --- Finalize ---
        now = datetime.now(UTC).isoformat()
        controller = HostController(repo)
        try:
            controller.mark_initialized(now)
        except Exception as e:
            logger.warning("Could not mark host as initialized: %s", e)

        FsUtils.chown_to_real_user(cache_dir)

        AuditLog.log("host.init", {"changes": len(all_changes)})

        was_user_added = any(c.mechanism == "usermod" for c in all_changes)

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
                "user_added_to_group": was_user_added,
                "session_has_group": HostOperation._detect_session_has_group(),
            },
        )

    @staticmethod
    def _setup_host_environment(
        repo: HostRepository,
        session_id: str,
    ) -> list[HostStateChangeItem]:
        """Orchestrate group/sudoers/sysctl/module/chains host setup.

        Sequences calls to HostService for each configuration step and
        returns all changes made. Runs as root (called from init() after
        privilege escalation).
        """
        all_changes: list[HostStateChangeItem] = []
        db_changes: list[HostStateChangeItem] = []

        # --- Group setup ---
        group_created = HostService.create_group(MVM_UNIX_GROUP)
        if group_created:
            change = HostStateChangeItem(
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
            db_changes.append(change)
            all_changes.append(change)

        username = (
            os.environ.get("SUDO_USER") or pwd.getpwuid(os.getuid()).pw_name
        )
        user_added = HostService.add_user_to_group(username, MVM_UNIX_GROUP)
        if user_added:
            change = HostStateChangeItem(
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
            db_changes.append(change)
            all_changes.append(change)

        # --- Sudoers setup ---
        if not re.fullmatch(r"[a-z][a-z0-9_-]{0,30}", MVM_UNIX_GROUP):
            raise HostError(f"Invalid group name: {MVM_UNIX_GROUP!r}")

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
            change = HostStateChangeItem(
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
            db_changes.append(change)
            all_changes.append(change)

        # --- IP forwarding ---
        fwd_change = HostService.enable_ip_forward()
        if fwd_change:
            db_changes.append(fwd_change)
            all_changes.append(fwd_change)

        # --- Persist sysctl ---
        sysctl_change = HostService.persist_sysctl()
        if sysctl_change:
            db_changes.append(sysctl_change)
            all_changes.append(sysctl_change)

        # --- KVM modules ---
        module_changes, next_order = HostService.ensure_kvm_modules(
            repo=repo, session_id=session_id, change_order_start=0
        )
        all_changes.extend(module_changes)

        # --- Firewall chains ---
        net_repo = NetworkRepository()
        net_service = NetworkService(net_repo)
        net_service.ensure_mvm_chains()

        backend = SettingsService.resolve(
            Database(), "settings", "firewall_backend"
        )
        chain_change = HostStateChangeItem(
            session_id="",
            init_timestamp="",
            setting=f"{backend}_chains",
            original_value=None,
            applied_value="MVM chains ensured",
            mechanism=backend,
            reverted=False,
            change_order=0,
            created_at="",
        )
        db_changes.append(chain_change)
        all_changes.append(chain_change)

        # --- Persist state ---
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

        return all_changes

    @staticmethod
    def get_state() -> HostStateItem | None:
        """Get current host state snapshot."""
        return HostRepository().get_state()

    @staticmethod
    def detect_resources() -> HostResources | None:
        """Detect live host resources from stored hardware/limits, falling back to live detection."""
        from mvmctl.utils.common import CacheUtils

        state = HostRepository().get_state()
        if state is not None and state.cpu_model is not None:
            hardware = HostHardware.from_state(state)
            limits = HostLimits.from_state(state)
        else:
            # No persisted state — fall back to live detection.
            hardware = HostDetector.detect_hardware()
            limits = HostDetector.detect_limits()
        if hardware is None or limits is None:
            return None
        return HostDetector.detect_resources(
            hardware, limits, CacheUtils.get_cache_dir()
        )

    @staticmethod
    def network_setup() -> OperationResult[Any]:
        """Create the default network if it does not exist yet.

        Idempotent — safe to call multiple times.  Logs a warning (does
        not raise) on failure so this can be safely called during init.
        """
        from mvmctl.api.network_operations import NetworkOperation

        try:
            restored_result = NetworkOperation.sync()
            if restored_result.is_ok and not restored_result.item:
                default_result = NetworkOperation.create_default_network()
                if default_result.is_error:
                    logger.warning(
                        "Could not create default network: %s",
                        default_result.message,
                    )
                    return default_result
            return OperationResult(
                status="success", code="network.default_ready"
            )
        except Exception:
            logger.warning("Could not set up default network")
            return OperationResult(
                status="error", code="network.default_failed"
            )

    @staticmethod
    def info() -> OperationResult[dict[str, object]]:
        """Return current host info with capacity analysis.

        Returns:
            OperationResult with nested dict containing hardware, limits,
            resource usage, and capacity projections.

        """
        from mvmctl.utils.common import CacheUtils

        state = HostRepository().get_state()
        if state is None:
            return OperationResult(
                status="error",
                code="host.info.no_state",
                message="Host not yet detected. Run 'mvm host init' first.",
            )

        # Reconstruct hardware/limits from stored state, or detect fresh
        hardware = HostHardware.from_state(state)
        limits = HostLimits.from_state(state)

        if hardware is None or limits is None:
            # Auto-detect if this is the first time
            repo = HostRepository()
            hardware, limits = HostService.detect_and_save_capacity(repo)
            state = repo.get_state()
            if state is None:
                return OperationResult(
                    status="error",
                    code="host.info.detect_failed",
                    message="Failed to detect host capacity.",
                )

        cache_dir = CacheUtils.get_cache_dir()
        resources = HostDetector.detect_resources(hardware, limits, cache_dir)
        info_dict = HostInfo(
            state=state, resources=resources, limits=limits, hardware=hardware
        ).to_dict()

        return OperationResult(
            status="success",
            code="host.info",
            item=info_dict,
        )

    @staticmethod
    def refresh_capacity() -> OperationResult[dict[str, object]]:
        """Redetect host hardware/limits and refresh info output.

        Returns:
            OperationResult with the same structure as info().

        """
        from mvmctl.utils.common import CacheUtils

        repo = HostRepository()
        try:
            hardware, limits = HostService.detect_and_save_capacity(repo)
        except Exception as e:
            logger.exception("Failed to detect host capacity")
            return OperationResult(
                status="error",
                code="host.capacity.detect_failed",
                message=f"Failed to detect host capacity: {e}",
            )

        state = repo.get_state()
        if state is None:
            return OperationResult(
                status="error",
                code="host.info.no_state",
                message="Failed to retrieve host state after detection.",
            )

        cache_dir = CacheUtils.get_cache_dir()
        resources = HostDetector.detect_resources(hardware, limits, cache_dir)
        info_dict = HostInfo(
            state=state, resources=resources, limits=limits, hardware=hardware
        ).to_dict()

        return OperationResult(
            status="success",
            code="host.capacity.refreshed",
            item=info_dict,
        )

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
                    NetworkService.remove_raw_tap(tap_name)
                    summary.append(f"Removed TAP device '{tap_name}'")
                except NetworkError as e:
                    summary.append(
                        f"Warning: failed to remove TAP '{tap_name}': {e}"
                    )

            db = Database()
            # Get networks from repository
            repo = NetworkRepository(db)
            networks = repo.list_all()
            net_service = NetworkService(repo)
            summary.extend(net_service.remove_stale_interfaces(f"{CLI_NAME}-"))
            metadata_bridges: set[str] = {net.bridge for net in networks}

            # Teardown NAT and bridges for each network
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
                SettingsService.resolve(db, "defaults.network", "name")
            )
            default_bridge = f"{CLI_NAME}-{default_net_name[:10]}"
            if NetworkUtils.bridge_exists(default_bridge):
                try:
                    NetworkService.remove_raw_bridge(default_bridge)
                    summary.append(f"Removed orphan bridge '{default_bridge}'")
                except NetworkError as e:
                    summary.append(
                        f"Warning: failed to remove orphan bridge '{default_bridge}': {e}"
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
                    NetworkService.remove_raw_bridge(bridge)
                    summary.append(f"Removed orphan bridge '{bridge}'")
                except NetworkError as e:
                    summary.append(
                        f"Warning: failed to remove orphan bridge '{bridge}': {e}"
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

            # Remove MVM chains and jump rules from system tables
            net_service.teardown()
            summary.append("Removed MVM firewall chains")

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

    @staticmethod
    def is_initialized() -> bool:
        """Check whether the host has been initialized via ``mvm init``.

        Returns:
            True if the host_state row exists and ``initialized`` is 1.

        """
        from mvmctl.core._shared import Database
        from mvmctl.core.host._repository import HostRepository

        state = HostRepository(Database()).get_state()
        return state is not None and bool(state.initialized)

    @staticmethod
    def check_readiness() -> ProbeResult:
        """Run pre-flight checks and return structured probe results."""
        return HostProbe.run_all()


__all__ = ["HostOperation"]
