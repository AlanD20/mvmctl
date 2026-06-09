"""Tests for CLI logs command."""

from __future__ import annotations

from unittest.mock import patch

from click.testing import CliRunner

from mvmctl.exceptions import MVMError
from mvmctl.main import app

runner = CliRunner()


class TestLogs:
    """Tests for 'logs' command."""

    @patch("mvmctl.api.LogOperation")
    def test_logs_success(self, mock_log_op):
        mock_log_op.stream.return_value = ["line 1\n", "line 2\n"]
        result = runner.invoke(app, ["logs", "myvm"])
        assert result.exit_code == 0
        assert "line 1" in result.output

    @patch("mvmctl.api.LogOperation")
    def test_logs_with_os_flag(self, mock_log_op):
        mock_log_op.stream.return_value = ["os log line\n"]
        result = runner.invoke(app, ["logs", "--os", "myvm"])
        assert result.exit_code == 0
        call_input = mock_log_op.stream.call_args[0][0]
        assert call_input.os_log is True

    @patch("mvmctl.api.LogOperation")
    def test_logs_with_lines_limit(self, mock_log_op):
        mock_log_op.stream.return_value = ["line 1\n"]
        result = runner.invoke(app, ["logs", "--lines", "10", "myvm"])
        assert result.exit_code == 0
        call_input = mock_log_op.stream.call_args[0][0]
        assert call_input.lines == 10

    @patch("mvmctl.api.LogOperation")
    def test_logs_error(self, mock_log_op):
        mock_log_op.stream.side_effect = MVMError("VM not found")
        result = runner.invoke(app, ["logs", "nonexistent"])
        assert result.exit_code == 1
        assert "not found" in result.output.lower()

    @patch("mvmctl.api.LogOperation")
    def test_logs_missing_identifier(self, mock_log_op):
        result = runner.invoke(app, ["logs"])
        assert result.exit_code == 0  # no_args_is_help=True shows help

    def test_logs_help(self):
        result = runner.invoke(app, ["logs", "--help"])
        assert result.exit_code == 0
        assert "log" in result.output.lower()
