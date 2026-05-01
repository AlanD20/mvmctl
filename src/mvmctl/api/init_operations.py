"""Init operations — cross-domain orchestration for the mvm init wizard."""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from mvmctl.api.cache_operations import CacheOperation
from mvmctl.api.host_operations import HostOperation
from mvmctl.core._shared import Database
from mvmctl.exceptions import BinaryError, HostError
from mvmctl.models.host import HostStateChangeItem
from mvmctl.utils.common import CacheUtils

logger = logging.getLogger(__name__)


@dataclass
class InitStepResult:
    """Result of a single init step."""

    step: str
    success: bool
    message: str
    needs_interaction: bool = False
    interaction_type: str | None = None
    interaction_data: dict[str, Any] = field(default_factory=dict)


@dataclass
class InitResult:
    """Complete result of the init wizard run."""

    steps: list[InitStepResult]
    host_ready: bool = False


class InitOperation:
    """
    Orchestration layer for the mvm init wizard.

    Sequences local-state → host → cache → binary setup in order.
    """

    @staticmethod
    def init_database() -> None:
        """Initialize the local SQLite database."""
        db = Database()
        db.migrate()

    @staticmethod
    def setup_host(cache_dir: Path) -> list[HostStateChangeItem]:
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
    def _step_host(*, skip: bool, sudo_completed: bool) -> InitStepResult:
        """Step 2: Host privilege setup."""
        if skip:
            return InitStepResult("host", True, "Skipped (--skip-host)")

        cache_dir_path = CacheUtils.get_cache_dir()

        if sudo_completed:
            # Caller already spawned ``sudo mvm host init`` successfully.
            # Don't query the DB — the subprocess already handled everything.
            return InitStepResult("host", True, "Host initialized")

        try:
            changes = InitOperation.setup_host(cache_dir_path)
            if changes:
                return InitStepResult("host", True, "Host initialized")
            return InitStepResult("host", True, "Host already configured")
        except HostError as e:
            error_msg = str(e)
            if "Root privileges" in error_msg:
                return InitStepResult(
                    "host",
                    False,
                    "Root privileges required",
                    needs_interaction=True,
                    interaction_type="sudo",
                )
            return InitStepResult("host", False, error_msg)

    @staticmethod
    def _step_cache() -> InitStepResult:
        """Step 3: Cache directory initialisation."""
        try:
            result = CacheOperation.init_all()
            guestfs_built = bool(result.get("guestfs_appliance"))
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
    ) -> InitStepResult:
        """Step 4: Firecracker binary availability."""
        from mvmctl.api.binary_operations import BinaryOperation

        local = BinaryOperation.list_local()
        if local:
            active = [v for v in local if v.is_default]
            if active:
                return InitStepResult(
                    "binary", True, f"Binary available (v{active[0].version})"
                )
            repaired = BinaryOperation.ensure_default()
            if repaired:
                return InitStepResult(
                    "binary",
                    True,
                    f"Binary available (v{repaired.version}) — set as default",
                )
            return InitStepResult(
                "binary", True, f"Binary available (v{local[0].version})"
            )

        if download_version:
            return InitOperation._download_binary(download_version)

        if non_interactive:
            return InitOperation._download_binary_latest()

        return InitOperation._binary_needs_interaction()

    @staticmethod
    def _download_binary(version: str) -> InitStepResult:
        """Download a specific binary version."""
        from mvmctl.api.binary_operations import BinaryOperation
        from mvmctl.api.inputs._binary_fetch_input import BinaryFetchInput

        try:
            fetch_result = BinaryOperation.fetch(
                BinaryFetchInput(version=version, set_as_default=True)
            )
            fc = [b for b in fetch_result.result if b.name == "firecracker"]
            version_str = (
                fc[0].version if fc else fetch_result.result[0].version
            )
            return InitStepResult("binary", True, f"Downloaded v{version_str}")
        except BinaryError as e:
            return InitStepResult("binary", False, f"Download failed: {e}")

    @staticmethod
    def _download_binary_latest() -> InitStepResult:
        """Download the latest remote binary version (non-interactive only)."""
        from mvmctl.api.binary_operations import BinaryOperation
        from mvmctl.api.inputs._binary_fetch_input import BinaryFetchInput

        try:
            versions = BinaryOperation.list_remote(limit=1)
            if not versions:
                return InitStepResult(
                    "binary", False, "No remote versions found"
                )

            fetch_result = BinaryOperation.fetch(
                BinaryFetchInput(version=versions[0], set_as_default=True)
            )
            fc = [b for b in fetch_result.result if b.name == "firecracker"]
            version_str = (
                fc[0].version if fc else fetch_result.result[0].version
            )
            return InitStepResult("binary", True, f"Downloaded v{version_str}")
        except BinaryError as e:
            return InitStepResult("binary", False, f"Download failed: {e}")

    @staticmethod
    def _binary_needs_interaction() -> InitStepResult:
        """Binary not found locally — prompt user to download."""
        from mvmctl.api.binary_operations import BinaryOperation

        try:
            versions = BinaryOperation.list_remote(limit=5)
        except BinaryError:
            versions = []

        if not versions:
            return InitStepResult(
                "binary", False, "No remote versions available"
            )

        return InitStepResult(
            "binary",
            False,
            "No Firecracker binary found in cache",
            needs_interaction=True,
            interaction_type="confirm_download",
            interaction_data={
                "latest_version": versions[0],
                "available_versions": versions,
            },
        )

    @staticmethod
    def run(
        skip_host: bool = False,
        non_interactive: bool = False,
        *,
        sudo_completed: bool = False,
        download_version: str | None = None,
    ) -> InitResult:
        """
        Run the init wizard steps in sequence.

        Args:
            skip_host: Skip the host privilege-setup step.
            non_interactive: Use defaults, skip all user prompts.
            sudo_completed: Set to True when the caller has already spawned
                ``sudo mvm host init`` and wants to continue the flow.
            download_version: If provided, download this binary version
                (used after the CLI handles the download prompt).

        Returns:
            InitResult with per-step status.  If a step needs user
            interaction, the corresponding InitStepResult has
            ``needs_interaction=True`` and the caller should act on it
            before calling ``run()`` again.

        """
        steps: list[InitStepResult] = []

        # ── Step 1: Local state ─────────────────────────────────────────────
        steps.append(InitOperation._step_local_state())

        # ── Step 2: Host ────────────────────────────────────────────────────
        host_result = InitOperation._step_host(
            skip=skip_host, sudo_completed=sudo_completed
        )
        steps.append(host_result)

        # If host needs sudo, stop here — let CLI handle it, then re-run.
        # Don't waste time on cache/binary while waiting for sudo.
        if host_result.needs_interaction:
            return InitResult(steps=steps, host_ready=False)

        # ── Step 3: Cache ───────────────────────────────────────────────────
        steps.append(InitOperation._step_cache())

        # ── Step 4: Binary ──────────────────────────────────────────────────
        steps.append(
            InitOperation._step_binary(
                non_interactive=non_interactive,
                download_version=download_version,
            )
        )

        host_ready = any(s.step == "host" and s.success for s in steps) and any(
            s.step == "binary" and s.success for s in steps
        )

        return InitResult(steps=steps, host_ready=host_ready)


__all__ = ["InitOperation", "InitResult", "InitStepResult"]
