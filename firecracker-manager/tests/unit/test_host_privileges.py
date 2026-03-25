"""Tests for privilege/exception hierarchy and host clean/reset CLI commands."""

from __future__ import annotations


from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from fcm.exceptions import FCMError, HostError, PrivilegeError

runner = CliRunner()


# ---------------------------------------------------------------------------
# PrivilegeError hierarchy
# ---------------------------------------------------------------------------


class TestPrivilegeError:
    def test_is_host_error(self):
        assert issubclass(PrivilegeError, HostError)

    def test_is_fcm_error(self):
        assert issubclass(PrivilegeError, FCMError)

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
    @patch("fcm.core.vm_manager.VMManager.list_all", return_value=[])
    @patch("fcm.cli.host.get_cache_dir")
    @patch("fcm.cli.host.clean_host")
    def test_clean_success(self, mock_clean, mock_cache, mock_list_all, tmp_path):
        from fcm.cli.host import app

        mock_cache.return_value = tmp_path
        mock_clean.return_value = ["Removed network 'default' (bridge: fcm-br0)"]
        result = runner.invoke(app, ["clean", "--force"])
        assert result.exit_code == 0
        assert "cleaned successfully" in result.output

    @patch("fcm.core.vm_manager.VMManager.list_all")
    def test_clean_refuses_running_vms(self, mock_list_all):
        from fcm.cli.host import app
        from fcm.models.vm import VMState

        vm = MagicMock()
        vm.name = "myvm"
        vm.status = VMState.RUNNING
        mock_list_all.return_value = [vm]
        result = runner.invoke(app, ["clean", "--force"])
        assert result.exit_code == 1
        assert "Cannot clean" in result.output


# ---------------------------------------------------------------------------
# CLI: host reset
# ---------------------------------------------------------------------------


class TestCliReset:
    @patch("fcm.core.vm_manager.VMManager.list_all", return_value=[])
    @patch("fcm.cli.host.get_cache_dir")
    @patch("fcm.cli.host.reset_host")
    def test_reset_success(self, mock_reset, mock_cache, mock_list_all, tmp_path):
        from fcm.cli.host import app

        mock_cache.return_value = tmp_path
        mock_reset.return_value = ["Removed network 'default'"]
        result = runner.invoke(app, ["reset", "--force"])
        assert result.exit_code == 0
        assert "reset successfully" in result.output

    @patch("fcm.core.vm_manager.VMManager.list_all")
    def test_reset_refuses_running_vms(self, mock_list_all):
        from fcm.cli.host import app
        from fcm.models.vm import VMState

        vm = MagicMock()
        vm.name = "myvm"
        vm.status = VMState.RUNNING
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
        from fcm.main import app

        click_runner = CliRunner()
        result = click_runner.invoke(app, ["help"])
        assert result.exit_code == 0
        assert "Firecracker Manager" in result.output or "fcm" in result.output

    def test_help_vm(self):
        from click.testing import CliRunner
        from fcm.main import app

        click_runner = CliRunner()
        result = click_runner.invoke(app, ["help", "vm"])
        assert result.exit_code == 0
        assert "vm" in result.output.lower()

    def test_help_host(self):
        from click.testing import CliRunner
        from fcm.main import app

        click_runner = CliRunner()
        result = click_runner.invoke(app, ["help", "host"])
        assert result.exit_code == 0
        assert "host" in result.output.lower()

    def test_help_unknown_command(self):
        from click.testing import CliRunner
        from fcm.main import app

        click_runner = CliRunner()
        result = click_runner.invoke(app, ["help", "nonexistent"])
        assert result.exit_code == 1
        assert "Unknown command" in result.output


def test_check_privileges_interactive_prints_guidance():
    """FIX-008: check_privileges_interactive prints helpful options when privileges lacking."""
    import pytest
    from fcm.core.host_privilege import check_privileges_interactive
    from fcm.exceptions import PrivilegeError
    from unittest.mock import patch

    with patch("fcm.core.host_privilege.check_privileges") as mock_check:
        mock_check.side_effect = PrivilegeError("not in group")
        with pytest.raises(PrivilegeError):
            check_privileges_interactive("/usr/sbin/ip", "network create")
