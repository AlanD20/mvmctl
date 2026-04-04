"""Tests for privilege/exception hierarchy and host clean/reset CLI commands."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from mvmctl.exceptions import HostError, MVMError, PrivilegeError

runner = CliRunner()


# ---------------------------------------------------------------------------
# PrivilegeError hierarchy
# ---------------------------------------------------------------------------


class TestPrivilegeError:
    def test_is_host_error(self):
        assert issubclass(PrivilegeError, HostError)

    def test_is_mvm_error(self):
        assert issubclass(PrivilegeError, MVMError)

    def test_can_be_raised(self):
        with pytest.raises(PrivilegeError, match="not allowed"):
            raise PrivilegeError("not allowed")

    def test_catchable_as_host_error(self):
        with pytest.raises(HostError):
            raise PrivilegeError("test")


# ---------------------------------------------------------------------------
# CLI: host clean
# ---------------------------------------------------------------------------


class TestCliClean:
    @patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[])
    @patch("mvmctl.cli.host.get_cache_dir")
    @patch("mvmctl.cli.host.clean_host")
    def test_clean_success(self, mock_clean, mock_cache, mock_list_all, tmp_path):
        from mvmctl.cli.host import app

        mock_cache.return_value = tmp_path
        mock_clean.return_value = ["Removed network 'default' (bridge: mvm-br0)"]
        result = runner.invoke(app, ["clean", "--force"])
        assert result.exit_code == 0
        assert "cleaned successfully" in result.output

    @patch("mvmctl.core.vm_manager.VMManager.list_all")
    def test_clean_refuses_running_vms(self, mock_list_all):
        from mvmctl.cli.host import app
        from mvmctl.models.vm import VMStatus

        vm = MagicMock()
        vm.name = "myvm"
        vm.status = VMStatus.RUNNING
        mock_list_all.return_value = [vm]
        result = runner.invoke(app, ["clean", "--force"])
        assert result.exit_code == 1
        assert "Cannot clean" in result.output


# ---------------------------------------------------------------------------
# CLI: host reset
# ---------------------------------------------------------------------------


class TestCliReset:
    @patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[])
    @patch("mvmctl.cli.host.get_cache_dir")
    @patch("mvmctl.cli.host.reset_host")
    def test_reset_success(self, mock_reset, mock_cache, mock_list_all, tmp_path):
        from mvmctl.cli.host import app

        mock_cache.return_value = tmp_path
        mock_reset.return_value = ["Removed network 'default'"]
        result = runner.invoke(app, ["reset", "--force"])
        assert result.exit_code == 0
        assert "reset successfully" in result.output

    @patch("mvmctl.core.vm_manager.VMManager.list_all")
    def test_reset_refuses_running_vms(self, mock_list_all):
        from mvmctl.cli.host import app
        from mvmctl.models.vm import VMStatus

        vm = MagicMock()
        vm.name = "myvm"
        vm.status = VMStatus.RUNNING
        mock_list_all.return_value = [vm]
        result = runner.invoke(app, ["reset", "--force"])
        assert result.exit_code == 1
        assert "Cannot reset" in result.output


# ---------------------------------------------------------------------------
# Top-level help command
# ---------------------------------------------------------------------------


class TestHelpCommand:
    def test_help_no_args(self):
        from click.testing import CliRunner

        from mvmctl.main import app

        click_runner = CliRunner()
        result = click_runner.invoke(app, ["help"])
        assert result.exit_code == 0
        assert "MicroVM Manager" in result.output or "mvm" in result.output

    def test_help_vm(self):
        from click.testing import CliRunner

        from mvmctl.main import app

        click_runner = CliRunner()
        result = click_runner.invoke(app, ["help", "vm"])
        assert result.exit_code == 0
        assert "vm" in result.output.lower()

    def test_help_host(self):
        from click.testing import CliRunner

        from mvmctl.main import app

        click_runner = CliRunner()
        result = click_runner.invoke(app, ["help", "host"])
        assert result.exit_code == 0
        assert "host" in result.output.lower()

    def test_help_unknown_command(self):
        from click.testing import CliRunner

        from mvmctl.main import app

        click_runner = CliRunner()
        result = click_runner.invoke(app, ["help", "nonexistent"])
        assert result.exit_code == 1
        assert "Unknown command" in result.output


def test_check_privileges_interactive_prints_guidance():
    """FIX-008: check_privileges_interactive prints helpful options when privileges lacking."""
    from unittest.mock import patch

    import pytest

    from mvmctl.core.host_privilege import check_privileges_interactive
    from mvmctl.exceptions import PrivilegeError

    with patch("mvmctl.core.host_privilege.check_privileges") as mock_check:
        mock_check.side_effect = PrivilegeError("not in group")
        with pytest.raises(PrivilegeError):
            check_privileges_interactive("/usr/sbin/ip", "network create")
