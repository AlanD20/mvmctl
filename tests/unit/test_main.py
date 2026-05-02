"""Tests for main.py — CLI entry point."""

from __future__ import annotations

import importlib
import sys

from click.testing import CliRunner

import mvmctl.main as main_module

runner = CliRunner()


def invoke_cli(args: list[str]):
    return runner.invoke(main_module.app, args)


class TestMainHelp:
    """Tests for help output."""

    def test_help(self):
        result = invoke_cli(["--help"])
        assert result.exit_code == 0
        assert "MicroVM Manager" in result.output

    def test_verbose_flag(self):
        result = invoke_cli(["--verbose", "--help"])
        assert result.exit_code == 0

    def test_debug_flag(self):
        result = invoke_cli(["--debug", "--help"])
        assert result.exit_code == 0

    def test_version_flag(self):
        result = invoke_cli(["--version"])
        assert result.exit_code == 0
        assert (
            "mvm" in result.output.lower() or "mvmctl" in result.output.lower()
        )


class TestMainSubcommands:
    """Tests for subcommand registration."""

    def test_vm_subcommand_registered(self):
        result = invoke_cli(["vm", "--help"])
        assert result.exit_code == 0

    def test_asset_subcommands_registered(self):
        for subcmd in ("kernel", "image", "bin"):
            result = invoke_cli([subcmd, "--help"])
            assert result.exit_code == 0

    def test_network_subcommand_registered(self):
        result = invoke_cli(["network", "--help"])
        assert result.exit_code == 0

    def test_config_subcommand_registered(self):
        result = invoke_cli(["config", "--help"])
        assert result.exit_code == 0

    def test_key_subcommand_registered(self):
        result = invoke_cli(["key", "--help"])
        assert result.exit_code == 0

    def test_host_subcommand_registered(self):
        result = invoke_cli(["host", "--help"])
        assert result.exit_code == 0

    def test_ssh_subcommand_registered(self):
        result = invoke_cli(["ssh", "--help"])
        assert result.exit_code == 0

    def test_logs_subcommand_registered(self):
        result = invoke_cli(["logs", "--help"])
        assert result.exit_code == 0

    def test_console_subcommand_registered(self):
        result = invoke_cli(["console", "--help"])
        assert result.exit_code == 0

    def test_cache_subcommand_registered(self):
        result = invoke_cli(["cache", "--help"])
        assert result.exit_code == 0

    def test_init_subcommand_registered(self):
        result = invoke_cli(["init", "--help"])
        assert result.exit_code == 0


class TestMainLazyLoading:
    """Tests for lazy import behavior."""

    def test_main_import_is_lazy(self, monkeypatch):
        monkeypatch.delitem(sys.modules, "mvmctl.cli.vm", raising=False)
        monkeypatch.delitem(sys.modules, "mvmctl.cli.bin", raising=False)
        monkeypatch.delitem(sys.modules, "mvmctl.cli.host", raising=False)

        importlib.reload(main_module)

        assert "mvmctl.cli.vm" not in sys.modules
        assert "mvmctl.cli.bin" not in sys.modules
        assert "mvmctl.cli.host" not in sys.modules

    def test_root_help_does_not_import_cli_modules(self, monkeypatch):
        monkeypatch.delitem(sys.modules, "mvmctl.cli.vm", raising=False)
        monkeypatch.delitem(sys.modules, "mvmctl.cli.bin", raising=False)

        importlib.reload(main_module)
        result = invoke_cli(["--help"])

        assert result.exit_code == 0
        assert "mvmctl.cli.vm" not in sys.modules
        assert "mvmctl.cli.bin" not in sys.modules

    def test_version_flag_does_not_import_cli_modules(self, monkeypatch):
        monkeypatch.delitem(sys.modules, "mvmctl.cli.vm", raising=False)
        monkeypatch.delitem(sys.modules, "mvmctl.cli.bin", raising=False)

        importlib.reload(main_module)
        result = invoke_cli(["--version"])

        assert result.exit_code == 0
        assert "mvmctl.cli.vm" not in sys.modules
        assert "mvmctl.cli.bin" not in sys.modules

    def test_vm_help_imports_only_requested_module(self, monkeypatch):
        monkeypatch.delitem(sys.modules, "mvmctl.cli.vm", raising=False)
        monkeypatch.delitem(sys.modules, "mvmctl.cli.bin", raising=False)

        importlib.reload(main_module)
        result = invoke_cli(["vm", "--help"])

        assert result.exit_code == 0
        assert "mvmctl.cli.vm" in sys.modules
        assert "mvmctl.cli.bin" not in sys.modules
