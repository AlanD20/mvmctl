"""Init operations — cross-domain orchestration for the mvm init wizard."""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any, cast

from mvmctl.api.cache_operations import CacheOperation
from mvmctl.api.host_operations import HostOperation
from mvmctl.core._shared import Database
from mvmctl.exceptions import BinaryError
from mvmctl.models import BinaryItem
from mvmctl.models.result import (
    NeedsInteraction,
    OperationResult,
    ProgressEvent,
)
from mvmctl.utils.common import CacheUtils

logger = logging.getLogger(__name__)


@dataclass
class InitStepResult:
    """Result of a single init step."""

    step: str
    success: bool
    message: str


@dataclass
class InitResult:
    """Complete result of the init wizard run."""

    steps: list[InitStepResult]
    host_ready: bool = False
    needs_interaction: NeedsInteraction | None = None


class InitOperation:
    """
    Orchestration layer for the mvm init wizard.

    Sequences local-state → service-binaries → host → guestfs →
    default-network → cache → binary setup in order.
    """

    @staticmethod
    def init_database() -> None:
        """Initialize the local SQLite database."""
        db = Database()
        db.migrate()

    @staticmethod
    def setup_host(cache_dir: Path) -> OperationResult[Any] | NeedsInteraction:
        """Set up host configuration."""
        return HostOperation.init(cache_dir)

    @staticmethod
    def _step_local_state() -> InitStepResult:
        """Step 1: Initialise the local SQLite database."""
        try:
            InitOperation.init_database()
            return InitStepResult("local_state", True, "Local state ready")
        except Exception as e:
            return InitStepResult("local_state", False, f"Failed: {e}")

    @staticmethod
    def _step_host(
        *,
        skip: bool,
        sudo_completed: bool,
        setup_message: str | None = None,
    ) -> tuple[InitStepResult, NeedsInteraction | None]:
        """Step 2: Host privilege setup.

        Args:
            skip: Skip host setup entirely.
            sudo_completed: True if caller already spawned ``sudo mvm host init``.
            setup_message: Descriptive message about what the sudo subprocess
                actually did. If None, defaults to "completed".

        Returns:
            Tuple of (step_result, needs_interaction). When interaction is
            required (privilege escalation), the NeedsInteraction is returned
            alongside the step result.
        """
        if skip:
            return InitStepResult("host", True, "Skipped (--skip-host)"), None

        cache_dir_path = CacheUtils.get_cache_dir()

        if sudo_completed:
            msg = setup_message or "completed"
            return InitStepResult("host", True, msg), None

        result = InitOperation.setup_host(cache_dir_path)

        if isinstance(result, NeedsInteraction):
            return (
                InitStepResult("host", False, "Root privileges required"),
                result,
            )

        # It's an OperationResult
        if result.status == "success":
            changes = result.metadata.get("changes", [])
            if changes:
                return InitStepResult("host", True, "Host initialized"), None
            return InitStepResult("host", True, "Host already configured"), None

        return InitStepResult("host", False, result.message), None

    @staticmethod
    def _step_network_setup() -> InitStepResult:
        """Step 5: Reconcile network DB state with infrastructure, then create default.

        Sync phase pushes DB state to infra: creates missing bridges, updates
        records, and tears down infra that has no corresponding DB entry.

        Create phase ensures a default network record exists in the DB and
        backs it with real infra (bridge, NAT, iptables chains).

        Runs independently of ``--skip-host`` so the user can configure the
        default subnet (via ``mvm config set``) before this step executes.

        Privilege escalation is handled internally by the underlying
        ``run_cmd(privileged=True)`` calls — they now warn instead of
        blocking if the mvm group is missing, and fall back to password
        prompt sudo.
        """

        result = HostOperation.network_setup()
        success = result.status == "success"
        return InitStepResult(
            "network_setup",
            success,
            result.message
            or (
                "Default network ready"
                if success
                else "Failed to create default network"
            ),
        )

    @staticmethod
    def _step_guestfs(
        guestfs_enabled: bool | None = None,
    ) -> tuple[InitStepResult, NeedsInteraction | None]:
        """Step 4: Check libguestfs availability and prompt user.

        When ``guestfs_enabled`` is provided (from a previous interaction
        round), the decision is persisted directly.  Otherwise the method
        detects availability and either auto-disables (not installed) or
        asks the user via ``NeedsInteraction``.

        Args:
            guestfs_enabled: Pre-resolved user decision from CLI prompt.
                ``None`` means the user hasn't decided yet.
        """
        from mvmctl.core._shared._db import Database
        from mvmctl.core.config._repository import SettingsRepository
        from mvmctl.core.config._service import SettingsService

        db = Database()
        repo = SettingsRepository(db)
        svc = SettingsService(repo)

        # ── User already decided (from previous NeedsInteraction round) ──
        if guestfs_enabled is not None:
            svc.set("settings", "guestfs_enabled", guestfs_enabled)
            if guestfs_enabled:
                return InitStepResult("guestfs", True, "enabled"), None
            return InitStepResult("guestfs", True, "disabled"), None

        # ── First pass — detect availability ────────────────────────────
        try:
            import guestfs  # noqa: F401

            available = True
        except ImportError:
            available = False

        if not available:
            # Not installed — no point prompting
            svc.set("settings", "guestfs_enabled", False)
            return InitStepResult("guestfs", True, "not installed"), None

        # Installed but user hasn't decided — prompt
        return InitStepResult(
            "guestfs",
            False,
            "available",
        ), NeedsInteraction(
            code="guestfs.confirm_enable",
            message="libguestfs is available. Enable it as a fallback?",
            input_type="confirm",
            context={},
        )

    @staticmethod
    def _step_cache(
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> InitStepResult:
        """Step 6: Cache directory initialisation."""
        try:
            result = CacheOperation.init_all(on_progress=on_progress)
            if result.is_error:
                return InitStepResult("cache", False, result.message)
            cache_dict = result.item or {}
            guestfs_built = bool(cache_dict.get("guestfs_appliance"))
            msg = (
                "Cache directories ready (libguestfs appliance built)"
                if guestfs_built
                else "Cache directories ready"
            )
            return InitStepResult("cache", True, msg)
        except Exception as e:
            return InitStepResult("cache", False, f"Cache init failed: {e}")

    @staticmethod
    def _step_binary(
        *,
        non_interactive: bool,
        download_version: str | None,
    ) -> tuple[InitStepResult, NeedsInteraction | None]:
        """Step 7: Firecracker binary availability.

        Returns:
            Tuple of (step_result, needs_interaction). When user confirmation
            is required to download a binary, the NeedsInteraction is returned.
        """
        from mvmctl.api.binary_operations import BinaryOperation

        local = cast(list[BinaryItem], BinaryOperation.list_all())
        fc_binaries = [b for b in local if b.name in ("firecracker", "jailer")]
        if fc_binaries:
            active = [v for v in fc_binaries if v.is_default]
            if active:
                return InitStepResult(
                    "binary", True, f"Binary available (v{active[0].version})"
                ), None
            repaired = BinaryOperation.ensure_default()
            if not repaired.is_error and repaired.item:
                return InitStepResult(
                    "binary",
                    True,
                    f"Binary available (v{repaired.item.version}) — set as default",
                ), None
            return InitStepResult(
                "binary", True, f"Binary available (v{fc_binaries[0].version})"
            ), None

        if download_version:
            return InitOperation._download_binary(download_version), None

        if non_interactive:
            return InitOperation._download_binary_latest(), None

        return InitOperation._binary_needs_interaction()

    @staticmethod
    def _download_binary(version: str) -> InitStepResult:
        """Download a specific binary version."""
        from mvmctl.api.binary_operations import BinaryOperation
        from mvmctl.api.inputs._binary_pull_input import BinaryPullInput

        fetch_result = BinaryOperation.pull(
            BinaryPullInput(version=version, set_default=True)
        )
        if isinstance(fetch_result, NeedsInteraction):
            return InitStepResult(
                "binary", False, "Binary download requires interaction"
            )
        if fetch_result.is_error:
            return InitStepResult(
                "binary", False, f"Download failed: {fetch_result.message}"
            )
        binaries = fetch_result.item or []
        fc = [b for b in binaries if b.name == "firecracker"]
        version_str = fc[0].version if fc else binaries[0].version
        return InitStepResult("binary", True, f"Downloaded v{version_str}")

    @staticmethod
    def _download_binary_latest() -> InitStepResult:
        """Download the latest remote binary version (non-interactive only)."""
        from mvmctl.api.binary_operations import BinaryOperation
        from mvmctl.api.inputs._binary_pull_input import BinaryPullInput

        try:
            versions = cast(
                list[str],
                BinaryOperation.list_all(remote=True, limit=1),
            )
            if not versions:
                return InitStepResult(
                    "binary", False, "No remote versions found"
                )

            fetch_result = BinaryOperation.pull(
                BinaryPullInput(version=versions[0], set_default=True)
            )
            if isinstance(fetch_result, NeedsInteraction):
                return InitStepResult(
                    "binary", False, "Binary download requires interaction"
                )
            if fetch_result.is_error:
                return InitStepResult(
                    "binary", False, f"Download failed: {fetch_result.message}"
                )
            binaries = fetch_result.item or []
            fc = [b for b in binaries if b.name == "firecracker"]
            version_str = fc[0].version if fc else binaries[0].version
            return InitStepResult("binary", True, f"Downloaded v{version_str}")
        except BinaryError as e:
            return InitStepResult("binary", False, f"Download failed: {e}")

    @staticmethod
    def _binary_needs_interaction() -> tuple[InitStepResult, NeedsInteraction]:
        """Binary not found locally — prompt user to download."""
        from mvmctl.api.binary_operations import BinaryOperation

        try:
            versions = cast(
                list[str],
                BinaryOperation.list_all(remote=True, limit=5),
            )
        except BinaryError:
            versions = []

        if not versions:
            return InitStepResult(
                "binary", False, "No remote versions available"
            ), NeedsInteraction(
                code="binary.confirm_download",
                message="No remote versions available",
                input_type="confirm",
                context={},
            )

        return InitStepResult(
            "binary",
            False,
            "No Firecracker binary found in cache",
        ), NeedsInteraction(
            code="binary.confirm_download",
            message="No Firecracker binary found in cache",
            input_type="confirm",
            context={
                "latest_version": versions[0],
                "available_versions": versions,
            },
        )

    @staticmethod
    def _step_service_binaries() -> InitStepResult:
        """Step 2: Extract embedded service binaries."""
        try:
            from mvmctl.core.binary._repository import BinaryRepository
            from mvmctl.core.binary._service import BinaryService

            repo = BinaryRepository(Database())
            BinaryService(repo).extract_service_binaries()
            return InitStepResult(
                "service_binaries", True, "Service binaries ready"
            )
        except Exception as e:
            return InitStepResult(
                "service_binaries",
                False,
                f"Service binary extraction failed: {e}",
            )

    @staticmethod
    def run(
        skip_host: bool = False,
        skip_network: bool = False,
        non_interactive: bool = False,
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
        sudo_completed: bool = False,
        host_setup_message: str | None = None,
        download_version: str | None = None,
        guestfs_enabled: bool | None = None,
    ) -> InitResult:
        """
        Run the init wizard steps in sequence.

        Args:
            skip_host: Skip the host privilege-setup step.
            skip_network: Skip the default-network creation step.
            non_interactive: Use defaults, skip all user prompts.
            on_progress: Optional callback for progress events during
                long-running steps (e.g., appliance build).
            sudo_completed: Set to True when the caller has already spawned
                ``sudo mvm host init`` and wants to continue the flow.
            host_setup_message: Descriptive message for the host step result
                when ``sudo_completed`` is True. Reflects what the sudo
                subprocess actually accomplished.
            download_version: If provided, download this binary version
                (used after the CLI handles the download prompt).
            guestfs_enabled: Pre-resolved user decision for libguestfs.
                Pass ``True`` or ``False`` after the CLI handles the
                ``guestfs.confirm_enable`` prompt.

        Returns:
            InitResult with per-step status.  If ``needs_interaction`` is set,
            the caller must handle the interaction before calling ``run()``
            again with the resolved parameters.

        """
        steps: list[InitStepResult] = []

        # ── Step 1: Local state ─────────────────────────────────────────────
        steps.append(InitOperation._step_local_state())

        # ── Step 2: Service binaries ────────────────────────────────────────
        # Must run before Host step so that binaries exist on disk when
        # write_sudoers() validates and writes the sudoers drop-in.
        steps.append(InitOperation._step_service_binaries())

        # ── Step 3: Host ────────────────────────────────────────────────────
        host_result, host_interaction = InitOperation._step_host(
            skip=skip_host,
            sudo_completed=sudo_completed,
            setup_message=host_setup_message,
        )
        steps.append(host_result)

        if host_interaction is not None:
            return InitResult(
                steps=steps,
                host_ready=False,
                needs_interaction=host_interaction,
            )

        # ── Step 4: Guestfs ─────────────────────────────────────────────────
        guestfs_result, guestfs_interaction = InitOperation._step_guestfs(
            guestfs_enabled=guestfs_enabled,
        )
        steps.append(guestfs_result)

        if guestfs_interaction is not None:
            return InitResult(
                steps=steps,
                host_ready=False,
                needs_interaction=guestfs_interaction,
            )

        # ── Step 5: Network setup (sync + default) ──────────────────────────
        if skip_network:
            steps.append(
                InitStepResult(
                    "network_setup", True, "Skipped (--skip-network)"
                )
            )
        else:
            steps.append(InitOperation._step_network_setup())

        # ── Step 6: Cache ───────────────────────────────────────────────────
        steps.append(InitOperation._step_cache(on_progress=on_progress))

        # ── Step 7: Binary ──────────────────────────────────────────────────
        binary_result, binary_interaction = InitOperation._step_binary(
            non_interactive=non_interactive,
            download_version=download_version,
        )
        steps.append(binary_result)

        if binary_interaction is not None:
            return InitResult(
                steps=steps,
                host_ready=False,
                needs_interaction=binary_interaction,
            )

        host_ready = any(s.step == "host" and s.success for s in steps) and any(
            s.step == "binary" and s.success for s in steps
        )

        return InitResult(steps=steps, host_ready=host_ready)


__all__ = ["InitOperation", "InitResult", "InitStepResult"]
