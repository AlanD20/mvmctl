"""Tests for InitOperation class — init wizard orchestration."""

from __future__ import annotations

from pathlib import Path

from mvmctl.api.init_operations import InitOperation, InitResult, InitStepResult
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
        mock_cache.return_value = OperationResult(
            status="success", code="cache.initialized", item={}
        )

        result = InitOperation._step_cache()

        assert result.step == "cache"
        assert result.success is True
        assert "Cache directories ready" in result.message

    def test_success_with_guestfs(self, mocker):
        """_step_cache() reports guestfs when appliance was built."""
        mock_cache = mocker.patch(
            "mvmctl.api.cache_operations.CacheOperation.init_all"
        )
        mock_cache.return_value = OperationResult(
            status="success",
            code="cache.initialized",
            item={"guestfs_appliance": "/tmp/appliance"},
        )

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
        mock_binary.name = "firecracker"
        mock_binary.is_default = True
        mock_binary.version = "1.15.0"

        mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.list_all",
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
        mock_binary.name = "firecracker"
        mock_binary.is_default = False
        mock_binary.version = "1.15.0"

        mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.list_all",
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
            "mvmctl.api.binary_operations.BinaryOperation.list_all",
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
            "mvmctl.api.binary_operations.BinaryOperation.list_all",
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
            "mvmctl.api.binary_operations.BinaryOperation.list_all",
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

    def _mock_all_steps(self, mocker, **overrides):
        """Mock the 6 init steps with defaults that pass through.

        The init flow is now:
          0. _step_local_state
          1. _step_service_binaries
          2. _step_host
          3. _step_guestfs
          4. _step_cache
          5. _step_binary

        Each can be overridden via keyword argument.
        """
        defaults = {
            "_step_local_state": InitStepResult("local_state", True, "Ready"),
            "_step_service_binaries": InitStepResult(
                "service_binaries", True, "Service binaries ready"
            ),
            "_step_host": (
                InitStepResult("host", True, "Ready"),
                None,
            ),
            "_step_guestfs": (
                InitStepResult("guestfs", True, "libguestfs disabled"),
                None,
            ),
            "_step_network_setup": InitStepResult(
                "network_setup", True, "Default network ready"
            ),
            "_step_cache": InitStepResult("cache", True, "Ready"),
            "_step_binary": (
                InitStepResult("binary", True, "Ready"),
                None,
            ),
        }
        final = {**defaults, **overrides}
        for name, ret in final.items():
            mocker.patch.object(InitOperation, name, return_value=ret)

    def test_full_success_flow(self, mocker):
        """run() completes all steps successfully."""
        self._mock_all_steps(mocker)

        result = InitOperation.run()

        assert isinstance(result, InitResult)
        assert result.host_ready is True
        assert result.needs_interaction is None
        assert len(result.steps) == 7

    def test_stops_at_host_interaction(self, mocker):
        """run() stops early when host step needs interaction."""
        self._mock_all_steps(
            mocker,
            _step_host=(
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
        # Should have 3 steps (local_state, service_binaries, host)
        assert len(result.steps) == 3

    def test_stops_at_binary_interaction(self, mocker):
        """run() stops early when binary step needs interaction."""
        self._mock_all_steps(
            mocker,
            _step_binary=(
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
        assert len(result.steps) == 7

    def test_stops_at_guestfs_interaction(self, mocker):
        """run() stops early when guestfs step needs interaction."""
        self._mock_all_steps(
            mocker,
            _step_guestfs=(
                InitStepResult("guestfs", False, "libguestfs is available"),
                NeedsInteraction(
                    code="guestfs.confirm_enable",
                    message="libguestfs is available. Enable it as a fallback?",
                    input_type="confirm",
                ),
            ),
        )

        result = InitOperation.run()

        assert result.needs_interaction is not None
        assert result.host_ready is False
        # local_state + service_binaries + host + guestfs
        assert len(result.steps) == 4

    def test_skip_host(self, mocker):
        """run() skips host step when skip_host=True."""
        self._mock_all_steps(
            mocker,
            _step_host=(InitStepResult("host", True, "Skipped"), None),
        )

        result = InitOperation.run(skip_host=True)

        assert result.host_ready is True
        # step[0]=local_state, step[1]=service_binaries, step[2]=host
        assert result.steps[2].message == "Skipped"

    def test_sudo_completed_flag(self, mocker):
        """run() handles sudo_completed flag correctly."""
        self._mock_all_steps(
            mocker,
            _step_host=(InitStepResult("host", True, "Sudo done"), None),
        )

        result = InitOperation.run(sudo_completed=True)

        assert result.host_ready is True
        # step[0]=local_state, step[1]=service_binaries, step[2]=host
        assert result.steps[2].message == "Sudo done"


class TestStepServiceBinaries:
    """Tests for InitOperation._step_service_binaries()."""

    def test_success(self, mocker):
        """_step_service_binaries returns success when extraction works."""
        mock_extract = mocker.patch(
            "mvmctl.core.binary._service.BinaryService.extract_service_binaries",
            return_value=[],
        )

        result = InitOperation._step_service_binaries()

        assert result.step == "service_binaries"
        assert result.success is True
        assert "Service binaries ready" in result.message
        mock_extract.assert_called_once()

    def test_failure(self, mocker):
        """_step_service_binaries returns failure when extraction fails."""
        mocker.patch(
            "mvmctl.core.binary._service.BinaryService.extract_service_binaries",
            side_effect=RuntimeError("No embedded binary"),
        )

        result = InitOperation._step_service_binaries()

        assert result.step == "service_binaries"
        assert result.success is False
        assert "Service binary extraction failed" in result.message


class TestStepGuestfs:
    """Tests for InitOperation._step_guestfs()."""

    def test_disabled_when_guestfs_enabled_false(self, mocker):
        """_step_guestfs disables and returns success when guestfs_enabled=False."""
        mock_set = mocker.patch(
            "mvmctl.core.config._service.SettingsService.set"
        )

        result, needs = InitOperation._step_guestfs(guestfs_enabled=False)

        assert result.step == "guestfs"
        assert result.success is True
        assert "disabled" in result.message.lower()
        assert needs is None
        mock_set.assert_called_once_with("settings", "guestfs_enabled", False)

    def test_enabled_when_guestfs_enabled_true(self, mocker):
        """_step_guestfs enables and returns success when guestfs_enabled=True."""
        mock_set = mocker.patch(
            "mvmctl.core.config._service.SettingsService.set"
        )

        result, needs = InitOperation._step_guestfs(guestfs_enabled=True)

        assert result.step == "guestfs"
        assert result.success is True
        assert "enabled" in result.message.lower()
        assert needs is None
        mock_set.assert_called_once_with("settings", "guestfs_enabled", True)

    def test_auto_disabled_when_not_installed(self, mocker):
        """_step_guestfs auto-disables when libguestfs is not available."""
        # Mock the import to fail
        import builtins

        real_import = builtins.__import__

        def mock_import(name, *args, **kwargs):
            if name == "guestfs":
                raise ModuleNotFoundError("No module named 'guestfs'")
            return real_import(name, *args, **kwargs)

        mocker.patch.object(builtins, "__import__", mock_import)
        mock_set = mocker.patch(
            "mvmctl.core.config._service.SettingsService.set"
        )

        result, needs = InitOperation._step_guestfs()

        assert result.step == "guestfs"
        assert result.success is True
        assert result.message == "not installed"
        assert needs is None
        mock_set.assert_called_once_with("settings", "guestfs_enabled", False)

    def test_prompts_when_available_and_undecided(self, mocker):
        """_step_guestfs returns NeedsInteraction when guestfs available and undecided."""
        # Make the import succeed
        mocker.patch.dict("sys.modules", {"guestfs": mocker.MagicMock()})
        # Mock settings service set to detect if called
        mock_set = mocker.patch(
            "mvmctl.core.config._service.SettingsService.set"
        )

        result, needs = InitOperation._step_guestfs()

        assert result.step == "guestfs"
        assert result.success is False
        assert result.message == "available"
        assert needs is not None
        assert needs.code == "guestfs.confirm_enable"
        # Should NOT have persisted any setting yet
        mock_set.assert_not_called()


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
