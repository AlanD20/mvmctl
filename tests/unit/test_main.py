"""Tests for main.py — CLI entry point."""

from __future__ import annotations

import importlib
import inspect
import sys
from unittest.mock import MagicMock, patch

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


class TestMainVersion:
    """Tests for version command and --version flag."""

    def test_version_cmd(self):
        result = invoke_cli(["version"])
        assert result.exit_code == 0
        assert "mvm" in result.output.lower()


class TestMainHelpCommand:
    """Tests for help command."""

    def test_help_no_args(self):
        result = invoke_cli(["help"])
        assert result.exit_code == 0

    def test_help_with_arg(self):
        result = invoke_cli(["help", "vm"])
        assert result.exit_code == 0

    def test_help_with_unknown_arg(self):
        result = invoke_cli(["help", "nonexistent"])
        assert result.exit_code == 1
        assert "Unknown command" in result.output

    def test_no_subcommand_shows_help(self):
        result = invoke_cli([])
        assert result.exit_code == 0
        assert "MicroVM Manager" in result.output


class TestMainEdgeCases:
    """Tests for main app edge cases."""

    def test_unknown_command(self):
        result = invoke_cli(["nonexistent"])
        assert result.exit_code != 0

    def test_version_console_and_cache_registered(self):
        for subcmd in ("version", "console", "cache"):
            result = invoke_cli([subcmd, "--help"])
            assert result.exit_code == 0

    def test_verbose_help(self):
        result = invoke_cli(["--verbose", "vm", "--help"])
        assert result.exit_code == 0


class TestMainUtils:
    """Tests for internal utility functions in main.py."""

    def test_get_env_var(self):
        from mvmctl.main import _get_env_var

        result = _get_env_var("CACHE_DIR")
        assert result == "MVM_CACHE_DIR"

    def test_get_git_version_info_no_git_dir(self, mocker):
        from mvmctl.main import _get_git_version_info

        mocker.patch("mvmctl.main.Path.exists", return_value=False)
        result = _get_git_version_info()
        assert result is None

    def test_get_git_version_info_tagged(self, mocker):
        from mvmctl.main import _get_git_version_info

        mocker.patch("mvmctl.main.Path.exists", return_value=True)
        mock_run = mocker.patch("mvmctl.main.subprocess.run")
        mock_run.return_value = MagicMock(returncode=0, stdout="v1.0.0\n")
        result = _get_git_version_info()
        assert result == "v1.0.0"

    def test_get_git_version_info_untagged(self, mocker):
        from mvmctl.main import _get_git_version_info

        mocker.patch("mvmctl.main.Path.exists", return_value=True)
        mock_run = mocker.patch("mvmctl.main.subprocess.run")
        mock_run.side_effect = [
            MagicMock(returncode=1, stdout=""),
            MagicMock(returncode=0, stdout="abc1234\n"),
        ]
        result = _get_git_version_info()
        assert result == "git+abc1234"

    def test_get_git_version_info_both_fail(self, mocker):
        from mvmctl.main import _get_git_version_info

        mocker.patch("mvmctl.main.Path.exists", return_value=True)
        mock_run = mocker.patch("mvmctl.main.subprocess.run")
        mock_run.side_effect = [
            MagicMock(returncode=1, stdout=""),
            MagicMock(returncode=1, stdout=""),
        ]
        result = _get_git_version_info()
        assert result is None

    def test_get_git_version_info_exception(self, mocker):
        from mvmctl.main import _get_git_version_info

        mocker.patch("mvmctl.main.Path.exists", side_effect=Exception("permission denied"))
        result = _get_git_version_info()
        assert result is None

    def test_get_version_fallback(self, mocker):
        from mvmctl.main import _get_version

        mocker.patch("mvmctl.main.importlib.metadata.version", side_effect=Exception("not found"))
        mocker.patch.object(main_module, "_get_git_version_info", return_value=None)
        result = _get_version()
        assert result is not None

    def test_get_version_with_git_tag(self, mocker):
        from mvmctl.main import _get_version

        mocker.patch("mvmctl.main.importlib.metadata.version", return_value="1.0.0")
        mocker.patch.object(main_module, "_get_git_version_info", return_value="v2.0.0")
        result = _get_version()
        assert result == "v2.0.0"

    def test_get_version_with_git_commit(self, mocker):
        from mvmctl.main import _get_version

        mocker.patch("mvmctl.main.importlib.metadata.version", return_value="1.0.0")
        mocker.patch.object(main_module, "_get_git_version_info", return_value="git+deadbeef")
        result = _get_version()
        assert "git+" in result

    def test_get_bootstrap_name(self):
        from mvmctl.main import _get_bootstrap_name

        result = _get_bootstrap_name()
        assert result is not None

    def test_get_cli_name(self):
        from mvmctl.main import _get_cli_name

        result = _get_cli_name()
        assert result is not None


class TestMainRootWarning:
    """Tests for root warning behavior."""

    def test_warn_if_running_as_root_escalated(self, mocker, monkeypatch):
        """Should skip warning when MVM_ESCALATED is set."""
        monkeypatch.setenv("MVM_ESCALATED", "1")
        mocker.patch("mvmctl.main.os.getuid", return_value=0)
        mock_print_warning = mocker.patch("mvmctl.utils._io.print_warning")
        main_module._warn_if_running_as_root()
        mock_print_warning.assert_not_called()

    def test_warn_if_running_as_root_not_root(self, mocker):
        """Should skip warning when not running as root."""
        mocker.patch("mvmctl.main.os.getuid", return_value=1000)
        mock_print_warning = mocker.patch("mvmctl.utils._io.print_warning")
        main_module._warn_if_running_as_root()
        mock_print_warning.assert_not_called()


class TestMainVersionExtended:
    """Extended tests for version command with git info."""

    def test_version_cmd_with_git_tag(self, mocker):
        mocker.patch.object(main_module, "_get_git_version_info", return_value="v2.0.0")
        result = invoke_cli(["version"])
        assert result.exit_code == 0
        assert "tagged" in result.output

    def test_version_cmd_with_git_commit(self, mocker):
        mocker.patch.object(main_module, "_get_git_version_info", return_value="git+abc1234")
        result = invoke_cli(["version"])
        assert result.exit_code == 0
        assert "built from" in result.output

    def test_version_cmd_no_git(self, mocker):
        mocker.patch.object(main_module, "_get_git_version_info", return_value=None)
        result = invoke_cli(["version"])
        assert result.exit_code == 0


class TestMainHelpExtended:
    """Extended tests for help command uncovered paths."""

    def test_help_cmd_arg_has_no_subcommands(self):
        """Should error when navigating into non-MultiCommand."""
        result = invoke_cli(["help", "version", "extra"])
        assert result.exit_code == 1
        assert "has no subcommands" in result.output


class TestMainEntryPoint:
    """Tests for __main__ entry point."""

    def test_main_block_exists(self):
        """The __main__ guard should be present."""
        source = inspect.getsource(main_module)
        assert 'if __name__ == "__main__":' in source
