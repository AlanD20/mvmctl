"""Tests for SSHOperation class — SSH connection orchestration."""

from __future__ import annotations

from pathlib import Path

from mvmctl.api.inputs._ssh_input import SSHInput
from mvmctl.api.ssh_operations import SSHOperation


class TestSSHOperationConnect:
    """Tests for SSHOperation.connect()."""

    def test_connect_with_command(self, mocker):
        """connect() resolves inputs and calls SSHService.connect with a command."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.target_ip = "10.0.0.2"
        mock_resolved.user = "ubuntu"
        mock_resolved.key = Path("/keys/test-key")
        mock_resolved.cmd = "uptime"

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved

        # Patch at the point of use
        mocker.patch(
            "mvmctl.api.ssh_operations.SSHRequest",
            return_value=mock_request,
        )

        mock_ssh_service = mocker.patch(
            "mvmctl.api.ssh_operations.SSHService",
        )
        mock_ssh_service.connect.return_value = 0

        # OperationResult check
        _connect = SSHOperation.connect(
            SSHInput(name="test-vm", user="ubuntu", cmd="uptime")
        )

        assert _connect.item == 0
        mock_ssh_service.connect.assert_called_once_with(
            ip="10.0.0.2",
            user="ubuntu",
            key_path=Path("/keys/test-key"),
            command="uptime",
            exec_mode=False,
        )

    def test_connect_interactive(self, mocker):
        """connect() uses exec_mode=True when no command is provided."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.target_ip = "10.0.0.2"
        mock_resolved.user = "ubuntu"
        mock_resolved.key = Path("/keys/test-key")
        mock_resolved.cmd = None

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.ssh_operations.SSHRequest",
            return_value=mock_request,
        )

        mock_ssh_service = mocker.patch(
            "mvmctl.api.ssh_operations.SSHService",
        )
        mock_ssh_service.connect.return_value = 0

        # OperationResult check
        _connect = SSHOperation.connect(SSHInput(name="test-vm", user="ubuntu"))

        assert _connect.item == 0
        mock_ssh_service.connect.assert_called_once_with(
            ip="10.0.0.2",
            user="ubuntu",
            key_path=Path("/keys/test-key"),
            command=None,
            exec_mode=True,
        )

    def test_connect_returns_exit_code(self, mocker):
        """connect() returns non-zero exit code on failure."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.target_ip = "10.0.0.2"
        mock_resolved.user = "ubuntu"
        mock_resolved.key = None
        mock_resolved.cmd = "false"

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.ssh_operations.SSHRequest",
            return_value=mock_request,
        )

        mock_ssh_service = mocker.patch(
            "mvmctl.api.ssh_operations.SSHService",
        )
        mock_ssh_service.connect.return_value = 1

        # OperationResult check
        _connect = SSHOperation.connect(SSHInput(name="test-vm", cmd="false"))

        assert _connect.item == 1

    def test_connect_logs_audit(self, mocker):
        """connect() logs an audit event."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.target_ip = "10.0.0.2"
        mock_resolved.user = "root"
        mock_resolved.key = None
        mock_resolved.cmd = None

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.ssh_operations.SSHRequest",
            return_value=mock_request,
        )

        mock_ssh_service = mocker.patch(
            "mvmctl.api.ssh_operations.SSHService",
        )
        mock_ssh_service.connect.return_value = 0

        mock_audit = mocker.patch("mvmctl.utils.auditlog.AuditLog.log")

        SSHOperation.connect(SSHInput(name="test-vm", user="root"))

        mock_audit.assert_called_once_with(
            "vm.ssh",
            changes={"ip": "10.0.0.2", "user": "root"},
        )
