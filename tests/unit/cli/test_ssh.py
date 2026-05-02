"""Tests for CLI SSH command."""

from __future__ import annotations

from unittest.mock import patch

from click.testing import CliRunner

from mvmctl.main import app
from mvmctl.models.result import OperationResult

runner = CliRunner()


class TestSSH:
    """Tests for 'ssh' command."""

    @patch("mvmctl.cli.ssh.SSHOperation")
    def test_ssh_success(self, mock_ssh_op):
        mock_ssh_op.connect.return_value = OperationResult(status="success", code="ssh.connected", item=0)
        result = runner.invoke(app, ["ssh", "--name", "myvm"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.ssh.SSHOperation")
    def test_ssh_failure(self, mock_ssh_op):
        mock_ssh_op.connect.return_value = OperationResult(status="error", code="ssh.failed", message="SSH connection failed", item=1)
        result = runner.invoke(app, ["ssh", "--name", "badvm"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.ssh.SSHOperation")
    def test_ssh_with_user(self, mock_ssh_op):
        mock_ssh_op.connect.return_value = OperationResult(status="success", code="ssh.connected", item=0)
        result = runner.invoke(
            app, ["ssh", "--name", "myvm", "--user", "admin"]
        )
        assert result.exit_code == 0
        call_input = mock_ssh_op.connect.call_args[0][0]
        assert call_input.user == "admin"

    @patch("mvmctl.cli.ssh.SSHOperation")
    def test_ssh_with_cmd(self, mock_ssh_op):
        mock_ssh_op.connect.return_value = OperationResult(status="success", code="ssh.connected", item=0)
        result = runner.invoke(
            app, ["ssh", "--name", "myvm", "--cmd", "ls -la"]
        )
        assert result.exit_code == 0
        call_input = mock_ssh_op.connect.call_args[0][0]
        assert call_input.cmd == "ls -la"

    @patch("mvmctl.cli.ssh.SSHOperation")
    def test_ssh_with_key(self, mock_ssh_op, tmp_path):
        mock_ssh_op.connect.return_value = OperationResult(status="success", code="ssh.connected", item=0)
        key_file = tmp_path / "test_key"
        key_file.write_text("private key")
        result = runner.invoke(
            app, ["ssh", "--name", "myvm", "--key", str(key_file)]
        )
        assert result.exit_code == 0
        call_input = mock_ssh_op.connect.call_args[0][0]
        assert str(call_input.key) == str(key_file)

    @patch("mvmctl.cli.ssh.SSHOperation")
    def test_ssh_with_ip(self, mock_ssh_op):
        mock_ssh_op.connect.return_value = OperationResult(status="success", code="ssh.connected", item=0)
        result = runner.invoke(app, ["ssh", "--ip", "10.0.0.2"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.ssh.SSHOperation")
    def test_ssh_with_identifier(self, mock_ssh_op):
        mock_ssh_op.connect.return_value = OperationResult(status="success", code="ssh.connected", item=0)
        result = runner.invoke(app, ["ssh", "myvm"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.ssh.SSHOperation")
    def test_ssh_no_args(self, mock_ssh_op):
        """No args shows help."""
        result = runner.invoke(app, ["ssh"])
        assert result.exit_code == 0

    def test_ssh_help(self):
        result = runner.invoke(app, ["ssh", "--help"])
        assert result.exit_code == 0
        assert "VM SSH access" in result.output
