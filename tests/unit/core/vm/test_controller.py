"""
Tests for VMController — stateful VM lifecycle manager.

Tests cover: init (with item vs string), start, stop (graceful/force),
pause, resume, reboot, snapshot, load_snapshot, and error handling.
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.vm._controller import VMController
from mvmctl.core.vm._repository import VMRepository
from mvmctl.exceptions import MVMError
from mvmctl.models import VMInstanceItem, VMStatus

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_vm(**overrides: object) -> VMInstanceItem:
    """Create a minimal VMInstanceItem with sensible defaults."""
    data: dict[str, object] = {
        "id": "a" * 64,
        "name": "test-vm",
        "status": VMStatus.RUNNING.value,
        "pid": 12345,
        "process_start_time": 1000000,
        "ipv4": "10.0.0.2",
        "mac": "02:FC:00:00:00:01",
        "network_id": "net-001",
        "tap_device": "tap-test",
        "image_id": "img-001",
        "kernel_id": "kern-001",
        "binary_id": "bin-001",
        "api_socket_path": "firecracker.api.socket",
        "relay_socket_path": None,
        "config_path": "firecracker.json",
        "cloud_init_mode": "inject",
        "nocloud_net_port": None,
        "nocloud_net_pid": None,
        "relay_pid": None,
        "exit_code": None,
        "log_path": None,
        "serial_output_path": None,
        "vcpu_count": 2,
        "mem_size_mib": 512,
        "disk_size_mib": 2048,
        "rootfs_path": "rootfs.ext4",
        "rootfs_suffix": "ext4",
        "enable_pci": False,
        "lsm_flags": None,
        "enable_logging": True,
        "enable_metrics": False,
        "enable_console": True,
        "boot_args": None,
        "created_at": "2026-01-01T00:00:00",
        "updated_at": "2026-01-01T00:00:00",
    }
    data.update(overrides)
    return VMInstanceItem(**data)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def mock_repo() -> MagicMock:
    """Return a mocked VMRepository."""
    return MagicMock(spec=VMRepository)


# ---------------------------------------------------------------------------
# Tests: __init__
# ---------------------------------------------------------------------------


class TestInit:
    def test_accepts_vm_instance_item(self, mock_repo: MagicMock) -> None:
        vm = _make_vm()
        controller = VMController(entity=vm, repo=mock_repo)
        assert controller is not None

    @patch("mvmctl.core.vm._resolver.VMResolver")
    def test_resolves_string_name(
        self, mock_resolver_cls: MagicMock, mock_repo: MagicMock
    ) -> None:
        vm = _make_vm()
        mock_resolver = MagicMock()
        mock_resolver.resolve.return_value = vm
        mock_resolver_cls.return_value = mock_resolver

        controller = VMController(entity="test-vm", repo=mock_repo)
        mock_resolver_cls.assert_called_once_with(mock_repo)
        mock_resolver.resolve.assert_called_once_with("test-vm")
        assert controller is not None


# ---------------------------------------------------------------------------
# Tests: stop()
# ---------------------------------------------------------------------------


class TestStop:
    def test_stop_is_idempotent_when_not_running(self, mock_repo: MagicMock) -> None:
        vm = _make_vm(status=VMStatus.STOPPED.value)
        controller = VMController(entity=vm, repo=mock_repo)
        controller.stop()
        mock_repo.update_status.assert_not_called()

    @patch("mvmctl.core.vm._controller.ProcessSignalHandler")
    @patch("mvmctl.core.vm._controller.FirecrackerClient")
    def test_graceful_stop_with_socket(
        self,
        mock_fc_cls: MagicMock,
        mock_handler_cls: MagicMock,
        mock_repo: MagicMock,
    ) -> None:
        vm = _make_vm(api_socket_path="firecracker.api.socket")
        mock_repo.update_status.return_value = None
        mock_repo.update_exit_code.return_value = None

        mock_client = MagicMock()
        mock_fc_cls.return_value = mock_client

        mock_handler = MagicMock()
        mock_handler.graceful_shutdown.return_value = 0
        mock_handler_cls.return_value = mock_handler

        controller = VMController(entity=vm, repo=mock_repo)
        controller.stop()

        # Verify signals
        mock_handler_cls.assert_called_once_with(
            vm.pid, expected_start_time=vm.process_start_time
        )
        mock_client.send_ctrl_alt_del.assert_called_once()
        mock_client.close.assert_called_once()

        # Verify status updates
        mock_repo.update_status.assert_any_call(vm.id, VMStatus.STOPPING.value)
        mock_repo.update_exit_code.assert_called_once_with(vm.id, 0)
        mock_repo.update_status.assert_any_call(vm.id, VMStatus.STOPPED.value)

    @patch("mvmctl.core.vm._controller.ProcessSignalHandler")
    def test_force_stop(
        self, mock_handler_cls: MagicMock, mock_repo: MagicMock
    ) -> None:
        vm = _make_vm()
        mock_repo.update_status.return_value = None
        mock_repo.update_exit_code.return_value = None

        mock_handler = MagicMock()
        mock_handler.graceful_shutdown.return_value = 0
        mock_handler_cls.return_value = mock_handler

        controller = VMController(entity=vm, repo=mock_repo)
        controller.stop(force=True)

        # With force=True, kill() is called before graceful_shutdown
        mock_handler.kill.assert_called_once()
        mock_handler.graceful_shutdown.assert_called_once()

        mock_repo.update_status.assert_any_call(vm.id, VMStatus.STOPPED.value)

    @patch("mvmctl.core.vm._controller.ProcessSignalHandler")
    def test_stop_sets_error_on_failure(
        self, mock_handler_cls: MagicMock, mock_repo: MagicMock
    ) -> None:
        vm = _make_vm()
        mock_handler = MagicMock()
        mock_handler.graceful_shutdown.side_effect = RuntimeError("boom")
        mock_handler_cls.return_value = mock_handler

        controller = VMController(entity=vm, repo=mock_repo)
        controller.stop()

        # Error status should be set
        mock_repo.update_status.assert_any_call(vm.id, VMStatus.ERROR.value)


# ---------------------------------------------------------------------------
# Tests: pause()
# ---------------------------------------------------------------------------


class TestPause:
    def test_pause_is_idempotent_when_already_paused(self, mock_repo: MagicMock) -> None:
        vm = _make_vm(status=VMStatus.PAUSED.value)
        controller = VMController(entity=vm, repo=mock_repo)
        controller.pause()
        mock_repo.update_status.assert_not_called()

    def test_raises_when_no_api_socket(self, mock_repo: MagicMock) -> None:
        vm = _make_vm(api_socket_path="")
        controller = VMController(entity=vm, repo=mock_repo)
        with pytest.raises(MVMError, match="no API socket"):
            controller.pause()

    @patch("mvmctl.core.vm._controller.FirecrackerClient")
    def test_pauses_successfully(
        self, mock_fc_cls: MagicMock, mock_repo: MagicMock
    ) -> None:
        vm = _make_vm()
        mock_client = MagicMock()
        mock_fc_cls.return_value = mock_client

        controller = VMController(entity=vm, repo=mock_repo)
        controller.pause()

        mock_client.pause_vm.assert_called_once()
        mock_client.close.assert_called_once()
        mock_repo.update_status.assert_called_once_with(
            vm.id, VMStatus.PAUSED.value
        )


# ---------------------------------------------------------------------------
# Tests: resume()
# ---------------------------------------------------------------------------


class TestResume:
    def test_resume_is_idempotent_when_running(self, mock_repo: MagicMock) -> None:
        vm = _make_vm(status=VMStatus.RUNNING.value)
        controller = VMController(entity=vm, repo=mock_repo)
        controller.resume()
        mock_repo.update_status.assert_not_called()

    def test_raises_when_no_api_socket(self, mock_repo: MagicMock) -> None:
        vm = _make_vm(status=VMStatus.PAUSED.value, api_socket_path="")
        controller = VMController(entity=vm, repo=mock_repo)
        with pytest.raises(MVMError, match="no API socket"):
            controller.resume()

    @patch("mvmctl.core.vm._controller.FirecrackerClient")
    def test_resumes_successfully(
        self, mock_fc_cls: MagicMock, mock_repo: MagicMock
    ) -> None:
        vm = _make_vm(status=VMStatus.PAUSED.value)
        mock_client = MagicMock()
        mock_fc_cls.return_value = mock_client

        controller = VMController(entity=vm, repo=mock_repo)
        controller.resume()

        mock_client.resume_vm.assert_called_once()
        mock_client.close.assert_called_once()
        mock_repo.update_status.assert_called_once_with(
            vm.id, VMStatus.RUNNING.value
        )


# ---------------------------------------------------------------------------
# Tests: start()
# ---------------------------------------------------------------------------


class TestStart:
    def test_start_is_idempotent_when_running(self, mock_repo: MagicMock) -> None:
        vm = _make_vm(status=VMStatus.RUNNING.value)
        controller = VMController(entity=vm, repo=mock_repo)
        controller.start()
        mock_repo.update_status.assert_not_called()

    def test_raises_when_no_api_socket(self, mock_repo: MagicMock) -> None:
        vm = _make_vm(status=VMStatus.STOPPED.value, api_socket_path="")
        controller = VMController(entity=vm, repo=mock_repo)
        with pytest.raises(MVMError, match="no API socket"):
            controller.start()

    @patch("mvmctl.core.vm._controller.FirecrackerClient")
    def test_starts_successfully(
        self, mock_fc_cls: MagicMock, mock_repo: MagicMock
    ) -> None:
        vm = _make_vm(status=VMStatus.STOPPED.value)
        mock_client = MagicMock()
        mock_fc_cls.return_value = mock_client

        controller = VMController(entity=vm, repo=mock_repo)
        controller.start()

        mock_client.start_instance.assert_called_once()
        mock_client.close.assert_called_once()
        mock_repo.update_status.assert_called_once_with(
            vm.id, VMStatus.RUNNING.value
        )


# ---------------------------------------------------------------------------
# Tests: reboot()
# ---------------------------------------------------------------------------


class TestReboot:
    @patch.object(VMController, "stop")
    @patch.object(VMController, "start")
    def test_reboot_calls_stop_then_start(
        self, mock_start: MagicMock, mock_stop: MagicMock, mock_repo: MagicMock
    ) -> None:
        vm = _make_vm()
        controller = VMController(entity=vm, repo=mock_repo)
        controller.reboot(force=True)

        mock_stop.assert_called_once_with(force=True)
        mock_start.assert_called_once()


# ---------------------------------------------------------------------------
# Tests: snapshot()
# ---------------------------------------------------------------------------


class TestSnapshot:
    def test_raises_when_no_socket(self, mock_repo: MagicMock) -> None:
        vm = _make_vm(api_socket_path="")
        controller = VMController(entity=vm, repo=mock_repo)
        with pytest.raises(MVMError, match="Socket not found"):
            controller.snapshot(Path("/tmp/mem"), Path("/tmp/state"))

    @patch("mvmctl.core.vm._controller.FirecrackerClient")
    def test_creates_snapshot(
        self, mock_fc_cls: MagicMock, mock_repo: MagicMock
    ) -> None:
        vm = _make_vm()
        mock_client = MagicMock()
        mock_fc_cls.return_value = mock_client

        controller = VMController(entity=vm, repo=mock_repo)
        controller.snapshot(Path("/tmp/mem"), Path("/tmp/state"))

        mock_client.create_snapshot.assert_called_once_with(
            Path("/tmp/mem"), Path("/tmp/state")
        )
        mock_client.close.assert_called_once()


# ---------------------------------------------------------------------------
# Tests: load_snapshot()
# ---------------------------------------------------------------------------


class TestLoadSnapshot:
    def test_raises_when_no_socket(self, mock_repo: MagicMock) -> None:
        vm = _make_vm(api_socket_path="")
        controller = VMController(entity=vm, repo=mock_repo)
        with pytest.raises(MVMError, match="Socket not found"):
            controller.load_snapshot(Path("/tmp/mem"), Path("/tmp/state"))

    @patch("mvmctl.core.vm._controller.FirecrackerClient")
    def test_loads_snapshot(
        self, mock_fc_cls: MagicMock, mock_repo: MagicMock
    ) -> None:
        vm = _make_vm()
        mock_client = MagicMock()
        mock_fc_cls.return_value = mock_client

        controller = VMController(entity=vm, repo=mock_repo)
        controller.load_snapshot(
            Path("/tmp/mem"), Path("/tmp/state"), resume_after=True
        )

        mock_client.load_snapshot.assert_called_once_with(
            Path("/tmp/mem"), Path("/tmp/state"), True
        )
        mock_client.close.assert_called_once()
