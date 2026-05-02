"""
Tests for VMService — stateless VM operations coordinator.

Tests cover: single VM operations (stop, start, pause, resume, reboot)
and bulk operations (stop_many, start_many, pause_many, resume_many,
reboot_many) with parallel and sequential modes.
"""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._service import VMService
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


@pytest.fixture
def service(mock_repo: MagicMock) -> VMService:
    """Return a VMService with mocked repo."""
    return VMService(repo=mock_repo)


# ---------------------------------------------------------------------------
# Tests: single VM operations
# ---------------------------------------------------------------------------


class TestStop:
    @patch("mvmctl.core.vm._service.VMController")
    def test_stop_creates_controller_and_stops(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vm = _make_vm()
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        service.stop(vm, force=False)

        mock_ctrl_cls.assert_called_once_with(entity=vm, repo=service._repo)
        mock_ctrl.stop.assert_called_once_with(force=False)

    @patch("mvmctl.core.vm._service.VMController")
    def test_stop_with_force(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vm = _make_vm()
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        service.stop(vm, force=True)

        mock_ctrl.stop.assert_called_once_with(force=True)


class TestStart:
    @patch("mvmctl.core.vm._service.VMController")
    def test_start_creates_controller_and_starts(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vm = _make_vm()
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        service.start(vm)

        mock_ctrl_cls.assert_called_once_with(entity=vm, repo=service._repo)
        mock_ctrl.start.assert_called_once()


class TestPause:
    @patch("mvmctl.core.vm._service.VMController")
    def test_pause_creates_controller_and_pauses(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vm = _make_vm()
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        service.pause(vm)

        mock_ctrl_cls.assert_called_once_with(entity=vm, repo=service._repo)
        mock_ctrl.pause.assert_called_once()


class TestResume:
    @patch("mvmctl.core.vm._service.VMController")
    def test_resume_creates_controller_and_resumes(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vm = _make_vm()
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        service.resume(vm)

        mock_ctrl_cls.assert_called_once_with(entity=vm, repo=service._repo)
        mock_ctrl.resume.assert_called_once()


class TestReboot:
    @patch("mvmctl.core.vm._service.VMController")
    def test_reboot_creates_controller_and_reboots(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vm = _make_vm()
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        service.reboot(vm, force=True)

        mock_ctrl_cls.assert_called_once_with(entity=vm, repo=service._repo)
        mock_ctrl.reboot.assert_called_once_with(force=True)


# ---------------------------------------------------------------------------
# Tests: bulk operations
# ---------------------------------------------------------------------------


class TestStopMany:
    @patch("mvmctl.core.vm._service.VMController")
    def test_stops_multiple_vms(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vms = [
            _make_vm(name="vm-a", id="a" * 64),
            _make_vm(name="vm-b", id="b" * 64),
        ]
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        result = service.stop_many(vms)

        assert len(result.items) == 2
        assert all(item.error is None for item in result.items)
        assert mock_ctrl.stop.call_count == 2

    @patch("mvmctl.core.vm._service.VMController")
    def test_stops_in_parallel(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vms = [
            _make_vm(name="vm-a", id="a" * 64),
            _make_vm(name="vm-b", id="b" * 64),
            _make_vm(name="vm-c", id="c" * 64),
        ]
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        result = service.stop_many(vms, parallel=True, max_workers=2)

        assert len(result.items) == 3
        assert all(item.error is None for item in result.items)

    def test_empty_list(self, service: VMService) -> None:
        result = service.stop_many([])
        assert result.items == []


class TestStartMany:
    @patch("mvmctl.core.vm._service.VMController")
    def test_starts_multiple_vms(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vms = [
            _make_vm(name="vm-a", id="a" * 64),
            _make_vm(name="vm-b", id="b" * 64),
        ]
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        result = service.start_many(vms)

        assert len(result.items) == 2
        assert all(item.error is None for item in result.items)
        assert mock_ctrl.start.call_count == 2

    def test_empty_list(self, service: VMService) -> None:
        result = service.start_many([])
        assert result.items == []


class TestPauseMany:
    @patch("mvmctl.core.vm._service.VMController")
    def test_pauses_multiple_vms(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vms = [
            _make_vm(name="vm-a", id="a" * 64),
            _make_vm(name="vm-b", id="b" * 64),
        ]
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        result = service.pause_many(vms)

        assert len(result.items) == 2
        assert all(item.error is None for item in result.items)
        assert mock_ctrl.pause.call_count == 2


class TestResumeMany:
    @patch("mvmctl.core.vm._service.VMController")
    def test_resumes_multiple_vms(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vms = [
            _make_vm(name="vm-a", id="a" * 64),
            _make_vm(name="vm-b", id="b" * 64),
        ]
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        result = service.resume_many(vms)

        assert len(result.items) == 2
        assert all(item.error is None for item in result.items)
        assert mock_ctrl.resume.call_count == 2


class TestRebootMany:
    @patch("mvmctl.core.vm._service.VMController")
    def test_reboots_multiple_vms(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vms = [
            _make_vm(name="vm-a", id="a" * 64),
            _make_vm(name="vm-b", id="b" * 64),
        ]
        mock_ctrl = MagicMock()
        mock_ctrl_cls.return_value = mock_ctrl

        result = service.reboot_many(vms, force=True)

        assert len(result.items) == 2
        assert all(item.error is None for item in result.items)
        assert mock_ctrl.reboot.call_count == 2


# ---------------------------------------------------------------------------
# Tests: error handling in bulk operations
# ---------------------------------------------------------------------------


class TestBulkErrorHandling:
    @patch("mvmctl.core.vm._service.VMController")
    def test_continues_on_error(
        self, mock_ctrl_cls: MagicMock, service: VMService
    ) -> None:
        vms = [
            _make_vm(name="vm-ok", id="a" * 64),
            _make_vm(name="vm-fail", id="b" * 64),
        ]
        mock_ctrl = MagicMock()
        mock_ctrl.stop.side_effect = [None, ValueError("stop failed")]
        mock_ctrl_cls.return_value = mock_ctrl

        result = service.stop_many(vms)

        assert len(result.items) == 2
        # First VM succeeded
        assert result.items[0].error is None
        # Second VM failed
        assert result.items[1].error is not None
        assert "stop failed" in str(result.items[1].error)
