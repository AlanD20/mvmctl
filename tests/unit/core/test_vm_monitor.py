"""Unit tests for vm_monitor module.

Tests for VM state reconciliation from live signals.
"""

from pathlib import Path
from unittest.mock import MagicMock, patch

from pytest_mock import MockerFixture

from mvmctl.core.vm_monitor import reconcile_vm
from mvmctl.models.vm import VMInstance, VMStatus


class TestReconcileVM:
    """Tests for reconcile_vm function."""

    def test_reconcile_running_vm(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test pid alive, FC API returns 'Running' → RUNNING."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.api_socket_path = Path("/tmp/test.sock")
        sample_vm.status = VMStatus.RUNNING

        with patch("os.kill", return_value=None):
            mock_client_class = mocker.patch("mvmctl.core.firecracker.FirecrackerClient")
            mock_client = MagicMock()
            mock_client.describe_instance.return_value = {"state": "Paused"}
            mock_client_class.return_value.__enter__.return_value = mock_client

            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.PAUSED
        assert sample_vm.status == VMStatus.PAUSED
        mock_manager.update_status.assert_called_once_with(sample_vm.name, VMStatus.PAUSED)

    def test_reconcile_dead_vm_clean_exit(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test pid dead, exit_code=0 → STOPPED."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.exit_code = 0
        sample_vm.status = VMStatus.RUNNING

        with patch("os.kill", side_effect=ProcessLookupError("No such process")):
            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.STOPPED
        assert sample_vm.status == VMStatus.STOPPED
        mock_manager.update_status.assert_called_once_with(sample_vm.name, VMStatus.STOPPED)

    def test_reconcile_dead_vm_crashed(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test pid dead, exit_code=137 → CRASHED."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.exit_code = 137
        sample_vm.status = VMStatus.RUNNING

        with patch("os.kill", side_effect=ProcessLookupError("No such process")):
            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.CRASHED
        assert sample_vm.status == VMStatus.CRASHED
        mock_manager.update_status.assert_called_once_with(sample_vm.name, VMStatus.CRASHED)

    def test_reconcile_dead_vm_no_exit_code(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test pid dead, exit_code=None → ERROR."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.exit_code = None
        sample_vm.status = VMStatus.RUNNING

        with patch("os.kill", side_effect=ProcessLookupError("No such process")):
            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.ERROR
        assert sample_vm.status == VMStatus.ERROR
        mock_manager.update_status.assert_called_once_with(sample_vm.name, VMStatus.ERROR)

    def test_reconcile_no_pid(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test pid=None → returns existing status unchanged."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = None
        sample_vm.status = VMStatus.STOPPED

        result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.STOPPED
        assert sample_vm.status == VMStatus.STOPPED
        mock_manager.update_status.assert_not_called()

    def test_reconcile_socket_unreachable(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test pid alive, socket error → RUNNING."""

        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.api_socket_path = Path("/tmp/test.sock")
        sample_vm.status = VMStatus.RUNNING

        with patch("os.kill", return_value=None):
            mock_client_class = mocker.patch("mvmctl.core.firecracker.FirecrackerClient")
            mock_client = MagicMock()
            mock_client.describe_instance.return_value = {"state": "Running"}
            mock_client_class.return_value.__enter__.return_value = mock_client

            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.RUNNING
        assert sample_vm.status == VMStatus.RUNNING
        mock_manager.update_status.assert_not_called()

    def test_reconcile_permission_error_treats_as_alive(
        self, mocker: MockerFixture, sample_vm: VMInstance
    ):
        """Test PermissionError from os.kill → treats as alive → RUNNING."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.api_socket_path = Path("/tmp/test.sock")
        sample_vm.status = VMStatus.STARTING

        with patch("os.kill", side_effect=PermissionError("Permission denied")):
            mock_client_class = mocker.patch("mvmctl.core.firecracker.FirecrackerClient")
            mock_client = MagicMock()
            mock_client.describe_instance.return_value = {"state": "Running"}
            mock_client_class.return_value.__enter__.return_value = mock_client

            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.RUNNING
        mock_manager.update_status.assert_called_once_with(sample_vm.name, VMStatus.RUNNING)

    def test_reconcile_oserror_treats_as_dead(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test OSError from os.kill → treats as dead → STOPPED."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.exit_code = 0
        sample_vm.status = VMStatus.RUNNING

        with patch("os.kill", side_effect=OSError("Some OS error")):
            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.STOPPED
        mock_manager.update_status.assert_called_once_with(sample_vm.name, VMStatus.STOPPED)

    def test_reconcile_no_socket_path(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test pid alive but no socket_path → RUNNING."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.api_socket_path = None
        sample_vm.status = VMStatus.STARTING

        with patch("os.kill", return_value=None):
            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.RUNNING
        mock_manager.update_status.assert_called_once_with(sample_vm.name, VMStatus.RUNNING)

    def test_reconcile_firecracker_error(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test FirecrackerError → RUNNING (process alive)."""
        from mvmctl.exceptions import FirecrackerError

        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.api_socket_path = Path("/tmp/test.sock")
        sample_vm.status = VMStatus.STARTING

        with patch("os.kill", return_value=None):
            mock_client_class = mocker.patch("mvmctl.core.firecracker.FirecrackerClient")
            mock_client_class.side_effect = FirecrackerError("Connection failed")

            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.RUNNING
        mock_manager.update_status.assert_called_once_with(sample_vm.name, VMStatus.RUNNING)

    def test_reconcile_update_status_failure(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test update_status failure → still returns new state."""
        mock_manager = mocker.MagicMock()
        mock_manager.update_status.side_effect = Exception("Update failed")
        sample_vm.pid = 1234
        sample_vm.exit_code = 0
        sample_vm.status = VMStatus.RUNNING

        with patch("os.kill", side_effect=ProcessLookupError("No such process")):
            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.STOPPED
        assert sample_vm.status == VMStatus.STOPPED
        mock_manager.update_status.assert_called_once()

    def test_reconcile_unknown_fc_state(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test unknown FC state → RUNNING."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.api_socket_path = Path("/tmp/test.sock")
        sample_vm.status = VMStatus.STARTING

        with patch("os.kill", return_value=None):
            mock_client_class = mocker.patch("mvmctl.core.firecracker.FirecrackerClient")
            mock_client = MagicMock()
            mock_client.describe_instance.return_value = {"state": "UnknownState"}
            mock_client_class.return_value.__enter__.return_value = mock_client

            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.RUNNING

    def test_reconcile_none_instance_info(self, mocker: MockerFixture, sample_vm: VMInstance):
        """Test describe_instance returns None → RUNNING."""
        mock_manager = mocker.MagicMock()
        sample_vm.pid = 1234
        sample_vm.api_socket_path = Path("/tmp/test.sock")
        sample_vm.status = VMStatus.STARTING

        with patch("os.kill", return_value=None):
            mock_client_class = mocker.patch("mvmctl.core.firecracker.FirecrackerClient")
            mock_client = MagicMock()
            mock_client.describe_instance.return_value = None
            mock_client_class.return_value.__enter__.return_value = mock_client

            result = reconcile_vm(sample_vm, mock_manager)

        assert result == VMStatus.RUNNING
