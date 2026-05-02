"""Tests for InitOperation class — init wizard orchestration."""

from __future__ import annotations

from pathlib import Path

from mvmctl.api.init_operations import InitOperation, InitResult, InitStepResult
from mvmctl.models.result import OperationResult
from mvmctl.models.result import NeedsInteraction, OperationResult


class TestInitDatabase:
    """Tests for InitOperation.init_database()."""

    def test_migrates_database(self, mocker):
        """init_database() calls Database().migrate()."""
        mock_db = mocker.MagicMock()
        # Patch at the point of use (init_operations imports from mvmctl.core._shared)
        mocker.patch(
            "mvmctl.api.init_operations.Database", return_value=mock_db
        )

        InitOperation.init_database()

        mock_db.migrate.assert_called_once()


class TestSetupHost:
    """Tests for InitOperation.setup_host()."""

    def test_delegates_to_host_operation(self, mocker):
        """setup_host() delegates to HostOperation.init()."""
        mock_host_op = mocker.patch(
            "mvmctl.api.host_operations.HostOperation.init"
        )
        mock_host_op.return_value = OperationResult(
            status="success",
            code="host.initialized",
            message="Host initialized",
            metadata={},
        )

        result = InitOperation.setup_host(Path("/tmp/cache"))

        mock_host_op.assert_called_once_with(Path("/tmp/cache"))
        assert result.status == "success"


class TestStepLocalState:
    """Tests for InitOperation._step_local_state()."""

    def test_success(self, mocker):
        """_step_local_state() returns success when DB migration works."""
        mocker.patch.object(InitOperation, "init_database")

        result = InitOperation._step_local_state()

        assert result.step == "local_state"
        assert result.success is True
        assert result.message == "Local state ready"

    def test_failure(self, mocker):
        """_step_local_state() returns failure when DB migration fails."""
        mocker.patch.object(
            InitOperation, "init_database", side_effect=RuntimeError("DB error")
        )

        result = InitOperation._step_local_state()

        assert result.step == "local_state"
        assert result.success is False
        assert "DB error" in result.message


class TestStepHost:
    """Tests for InitOperation._step_host()."""

    def test_skip(self):
        """_step_host() returns skipped result when skip=True."""
        step, needs = InitOperation._step_host(skip=True, sudo_completed=False)

        assert step.step == "host"
        assert step.success is True
        assert step.message == "Skipped (--skip-host)"
        assert needs is None

    def test_sudo_completed(self):
        """_step_host() returns success when sudo_completed=True."""
        step, needs = InitOperation._step_host(skip=False, sudo_completed=True)

        assert step.step == "host"
        assert step.success is True
        assert step.message == "completed"
        assert needs is None

    def test_returns_needs_interaction(self, mocker):
        """_step_host() returns NeedsInteraction when sudo is required."""
        mock_setup = mocker.patch.object(InitOperation, "setup_host")
        mock_setup.return_value = NeedsInteraction(
            code="privilege.sudo_required",
            message="Root required",
            input_type="sudo",
        )

        step, needs = InitOperation._step_host(skip=False, sudo_completed=False)

        assert step.success is False
        assert needs is not None
        assert needs.code == "privilege.sudo_required"

    def test_success_result(self, mocker):
        """_step_host() returns success when HostOperation succeeds."""
        mock_setup = mocker.patch.object(InitOperation, "setup_host")
        mock_setup.return_value = OperationResult(
            status="success",
            code="host.initialized",
            message="Host initialized",
            metadata={},
        )

        step, needs = InitOperation._step_host(skip=False, sudo_completed=False)

        assert step.step == "host"
        assert step.success is True

    def test_skipped_result(self, mocker):
        """_step_host() returns success when HostOperation successfully reports already configured."""
        mock_setup = mocker.patch.object(InitOperation, "setup_host")
        mock_setup.return_value = OperationResult(
            status="success",
            code="host.already_configured",
            message="Already configured",
            metadata={},
        )

        step, needs = InitOperation._step_host(skip=False, sudo_completed=False)

        assert step.step == "host"
        assert step.success is True
        assert "already configured" in step.message

    def test_failure_result(self, mocker):
        """_step_host() returns failure when HostOperation fails."""
        mock_setup = mocker.patch.object(InitOperation, "setup_host")
        mock_setup.return_value = OperationResult(
            status="error",
            code="host.failed",
            message="Something went wrong",
            metadata={},
        )

        step, needs = InitOperation._step_host(skip=False, sudo_completed=False)

        assert step.step == "host"
        assert step.success is False
        assert "Something went wrong" in step.message


class TestStepCache:
    """Tests for InitOperation._step_cache()."""

    def test_success(self, mocker):
        """_step_cache() returns success when cache init works."""
        mock_cache = mocker.patch(
            "mvmctl.api.cache_operations.CacheOperation.init_all"
        )
        mock_cache.return_value = OperationResult(status="success", code="cache.initialized", item={})

        result = InitOperation._step_cache()

        assert result.step == "cache"
        assert result.success is True
        assert "Cache directories ready" in result.message

    def test_success_with_guestfs(self, mocker):
        """_step_cache() reports guestfs when appliance was built."""
        mock_cache = mocker.patch(
            "mvmctl.api.cache_operations.CacheOperation.init_all"
        )
        mock_cache.return_value = OperationResult(status="success", code="cache.initialized", item={"guestfs_appliance": "/tmp/appliance"})

        result = InitOperation._step_cache()

        assert result.step == "cache"
        assert result.success is True
        assert "libguestfs appliance built" in result.message

    def test_failure(self, mocker):
        """_step_cache() returns failure when cache init fails."""
        mock_cache = mocker.patch(
            "mvmctl.api.cache_operations.CacheOperation.init_all"
        )
        mock_cache.side_effect = RuntimeError("Permission denied")

        result = InitOperation._step_cache()

        assert result.step == "cache"
        assert result.success is False
        assert "Cache init failed" in result.message


class TestStepBinary:
    """Tests for InitOperation._step_binary()."""

    def test_local_active_default(self, mocker):
        """_step_binary() succeeds when local default binary exists."""
        mock_binary = mocker.MagicMock()
        mock_binary.is_default = True
        mock_binary.version = "1.15.0"

        mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.list_local",
            return_value=[mock_binary],
        )

        step, needs = InitOperation._step_binary(
            non_interactive=False, download_version=None
        )

        assert step.step == "binary"
        assert step.success is True
        assert "Binary available" in step.message
        assert needs is None

    def test_local_with_repair(self, mocker):
        """_step_binary() sets default when local exists but none active."""
        mock_binary = mocker.MagicMock()
        mock_binary.is_default = False
        mock_binary.version = "1.15.0"

        mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.list_local",
            return_value=[mock_binary],
        )

        mock_repaired = mocker.MagicMock()
        mock_repaired.version = "1.15.0"
        mock_repaired.is_error = False
        mock_repaired.item = mock_repaired
        mock_ensure = mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.ensure_default",
            return_value=mock_repaired,
        )

        step, needs = InitOperation._step_binary(
            non_interactive=False, download_version=None
        )

        assert step.step == "binary"
        assert step.success is True
        assert "set as default" in step.message
        mock_ensure.assert_called_once()

    def test_no_local_uses_download_version(self, mocker):
        """_step_binary() downloads specific version when provided."""
        mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.list_local",
            return_value=[],
        )
        mock_download = mocker.patch.object(
            InitOperation,
            "_download_binary",
            return_value=InitStepResult("binary", True, "Downloaded v1.15.0"),
        )

        step, needs = InitOperation._step_binary(
            non_interactive=False, download_version="1.15.0"
        )

        assert step.success is True
        mock_download.assert_called_once_with("1.15.0")

    def test_no_local_non_interactive(self, mocker):
        """_step_binary() downloads latest when non-interactive."""
        mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.list_local",
            return_value=[],
        )
        mock_download_latest = mocker.patch.object(
            InitOperation,
            "_download_binary_latest",
            return_value=InitStepResult("binary", True, "Downloaded v1.16.0"),
        )

        step, needs = InitOperation._step_binary(
            non_interactive=True, download_version=None
        )

        assert step.success is True
        mock_download_latest.assert_called_once()

    def test_no_local_needs_interaction(self, mocker):
        """_step_binary() returns NeedsInteraction when prompting user."""
        mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.list_local",
            return_value=[],
        )
        mocker.patch.object(
            InitOperation,
            "_binary_needs_interaction",
            return_value=(
                InitStepResult("binary", False, "No binary found"),
                NeedsInteraction(
                    code="binary.confirm_download",
                    message="Download?",
                    input_type="confirm",
                    context={},
                ),
            ),
        )

        step, needs = InitOperation._step_binary(
            non_interactive=False, download_version=None
        )

        assert step.success is False
        assert needs is not None
        assert needs.code == "binary.confirm_download"


class TestRun:
    """Tests for InitOperation.run() — full orchestration."""

    def test_full_success_flow(self, mocker):
        """run() completes all steps successfully."""
        mocker.patch.object(
            InitOperation,
            "_step_local_state",
            return_value=InitStepResult("local_state", True, "Ready"),
        )
        mocker.patch.object(
            InitOperation,
            "_step_host",
            return_value=(InitStepResult("host", True, "Ready"), None),
        )
        mocker.patch.object(
            InitOperation,
            "_step_cache",
            return_value=InitStepResult("cache", True, "Ready"),
        )
        mocker.patch.object(
            InitOperation,
            "_step_binary",
            return_value=(InitStepResult("binary", True, "Ready"), None),
        )

        result = InitOperation.run()

        assert isinstance(result, InitResult)
        assert result.host_ready is True
        assert result.needs_interaction is None
        assert len(result.steps) == 4

    def test_stops_at_host_interaction(self, mocker):
        """run() stops early when host step needs interaction."""
        mocker.patch.object(
            InitOperation,
            "_step_local_state",
            return_value=InitStepResult("local_state", True, "Ready"),
        )
        mocker.patch.object(
            InitOperation,
            "_step_host",
            return_value=(
                InitStepResult("host", False, "Need sudo"),
                NeedsInteraction(
                    code="privilege.sudo_required",
                    message="Need sudo",
                    input_type="sudo",
                ),
            ),
        )

        result = InitOperation.run()

        assert result.needs_interaction is not None
        assert result.host_ready is False
        # Should only have 2 steps (local_state + host) — not cache or binary
        assert len(result.steps) == 2

    def test_stops_at_binary_interaction(self, mocker):
        """run() stops early when binary step needs interaction."""
        mocker.patch.object(
            InitOperation,
            "_step_local_state",
            return_value=InitStepResult("local_state", True, "Ready"),
        )
        mocker.patch.object(
            InitOperation,
            "_step_host",
            return_value=(InitStepResult("host", True, "Ready"), None),
        )
        mocker.patch.object(
            InitOperation,
            "_step_cache",
            return_value=InitStepResult("cache", True, "Ready"),
        )
        mocker.patch.object(
            InitOperation,
            "_step_binary",
            return_value=(
                InitStepResult("binary", False, "Need download"),
                NeedsInteraction(
                    code="binary.confirm_download",
                    message="Download?",
                    input_type="confirm",
                ),
            ),
        )

        result = InitOperation.run()

        assert result.needs_interaction is not None
        assert result.host_ready is False
        assert len(result.steps) == 4

    def test_skip_host(self, mocker):
        """run() skips host step when skip_host=True."""
        mocker.patch.object(
            InitOperation,
            "_step_local_state",
            return_value=InitStepResult("local_state", True, "Ready"),
        )
        mocker.patch.object(
            InitOperation,
            "_step_host",
            return_value=(InitStepResult("host", True, "Skipped"), None),
        )
        mocker.patch.object(
            InitOperation,
            "_step_cache",
            return_value=InitStepResult("cache", True, "Ready"),
        )
        mocker.patch.object(
            InitOperation,
            "_step_binary",
            return_value=(InitStepResult("binary", True, "Ready"), None),
        )

        result = InitOperation.run(skip_host=True)

        assert result.host_ready is True
        assert result.steps[1].message == "Skipped"

    def test_sudo_completed_flag(self, mocker):
        """run() handles sudo_completed flag correctly."""
        mocker.patch.object(
            InitOperation,
            "_step_local_state",
            return_value=InitStepResult("local_state", True, "Ready"),
        )
        mocker.patch.object(
            InitOperation,
            "_step_host",
            return_value=(InitStepResult("host", True, "Sudo done"), None),
        )
        mocker.patch.object(
            InitOperation,
            "_step_cache",
            return_value=InitStepResult("cache", True, "Ready"),
        )
        mocker.patch.object(
            InitOperation,
            "_step_binary",
            return_value=(InitStepResult("binary", True, "Ready"), None),
        )

        result = InitOperation.run(sudo_completed=True)

        assert result.host_ready is True
        assert result.steps[1].message == "Sudo done"


class TestInitStepResult:
    """Tests for InitStepResult dataclass."""

    def test_creation(self):
        """InitStepResult stores step, success, message."""
        result = InitStepResult("test_step", True, "All good")
        assert result.step == "test_step"
        assert result.success is True
        assert result.message == "All good"

    def test_failure(self):
        """InitStepResult stores failure info."""
        result = InitStepResult("fail_step", False, "Something broke")
        assert result.step == "fail_step"
        assert result.success is False
