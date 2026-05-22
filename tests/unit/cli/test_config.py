"""Tests for CLI config commands."""

from __future__ import annotations

from unittest.mock import patch

from click.testing import CliRunner

from mvmctl.main import app
from mvmctl.models.result import OperationResult

runner = CliRunner()


class TestConfigGet:
    """Tests for 'config get' command."""

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_get_value(self, mock_cfg_op):
        mock_cfg_op.get.return_value = "eth0"
        result = runner.invoke(
            app, ["config", "get", "defaults.vm", "network_interface"]
        )
        assert result.exit_code == 0
        assert "eth0" in result.output

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_get_not_set(self, mock_cfg_op):
        mock_cfg_op.get.return_value = None
        result = runner.invoke(
            app, ["config", "get", "defaults.vm", "nonexistent"]
        )
        assert result.exit_code == 0

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_get_section(self, mock_cfg_op):
        mock_cfg_op.get.return_value = {
            "vcpu_count": {
                "override": None,
                "default": "2",
                "type": "int",
            },
        }
        result = runner.invoke(app, ["config", "get", "defaults.vm"])
        assert result.exit_code == 0
        assert "vcpu_count" in result.output

    def test_get_help(self):
        result = runner.invoke(app, ["config", "get", "--help"])
        assert result.exit_code == 0


class TestConfigSet:
    """Tests for 'config set' command."""

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_set_value(self, mock_cfg_op):
        mock_cfg_op.set.return_value = OperationResult(
            status="success", code="config.set", message="Configuration updated"
        )
        result = runner.invoke(
            app, ["config", "set", "defaults.vm", "network_interface", "eth0"]
        )
        assert result.exit_code == 0
        assert "Configuration updated" in result.output

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_set_invalid_key(self, mock_cfg_op):
        mock_cfg_op.set.side_effect = ValueError("bad key")
        result = runner.invoke(
            app, ["config", "set", "defaults.vm", "bad_key", "val"]
        )
        assert result.exit_code == 1

    def test_set_help(self):
        result = runner.invoke(app, ["config", "set", "--help"])
        assert result.exit_code == 0


class TestConfigList:
    """Tests for 'config ls' command."""

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_list_settings(self, mock_cfg_op):
        mock_cfg_op.list_all.return_value = {
            "defaults.vm": {
                "vcpu_count": {"override": None, "default": 2, "type": "int"},
                "mem_size_mib": {"override": "1024", "default": 2048, "type": "int"},
            },
        }
        result = runner.invoke(app, ["config", "ls"])
        assert result.exit_code == 0
        assert "defaults.vm" in result.output
        assert "vcpu_count" in result.output
        assert "1024" in result.output

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_list_empty(self, mock_cfg_op):
        mock_cfg_op.list_all.return_value = {}
        result = runner.invoke(app, ["config", "ls"])
        assert result.exit_code == 0

    def test_list_help(self):
        result = runner.invoke(app, ["config", "ls", "--help"])
        assert result.exit_code == 0


class TestConfigReset:
    """Tests for 'config reset' command."""

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_reset_key(self, mock_cfg_op):
        mock_cfg_op.reset.return_value = OperationResult(
            status="success", code="config.reset", item=1
        )
        result = runner.invoke(
            app, ["config", "reset", "defaults.vm", "network_interface"]
        )
        assert result.exit_code == 0
        assert "Reset" in result.output

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_reset_category(self, mock_cfg_op):
        mock_cfg_op.reset.return_value = OperationResult(
            status="success", code="config.reset", item=3
        )
        result = runner.invoke(app, ["config", "reset", "defaults.vm"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_reset_all(self, mock_cfg_op):
        mock_cfg_op.reset.return_value = OperationResult(
            status="success", code="config.reset", item=5
        )
        result = runner.invoke(app, ["config", "reset", "--all"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.config.ConfigOperation")
    def test_reset_no_args(self, mock_cfg_op):
        result = runner.invoke(app, ["config", "reset"])
        assert result.exit_code == 0

    def test_reset_help(self):
        result = runner.invoke(app, ["config", "reset", "--help"])
        assert result.exit_code == 0


class TestConfigHelp:
    """Tests for config command group help."""

    def test_config_help(self):
        result = runner.invoke(app, ["config", "--help"])
        assert result.exit_code == 0
        assert "Configuration" in result.output

    def test_config_no_args(self):
        result = runner.invoke(app, ["config"])
        assert result.exit_code == 0
