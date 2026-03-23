from __future__ import annotations
import subprocess
"""Tests for Phase 5 features: privilege model, clean/reset, help command."""


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
# check_privileges
# ---------------------------------------------------------------------------


class TestCheckPrivileges:
    @patch("shutil.which", return_value=None)
    @patch("pathlib.Path.exists", return_value=False)
    def test_binary_not_found(self, mock_exists, mock_which):
        from fcm.api.host import check_privileges

        with pytest.raises(PrivilegeError, match="Binary not found"):
            check_privileges("/usr/sbin/nonexistent")

    @patch("shutil.which", return_value="/usr/sbin/ip")
    @patch("os.getuid", return_value=0)
    def test_root_user_passes(self, mock_uid, mock_which):
        from fcm.api.host import check_privileges

        # Should not raise
        check_privileges("/usr/sbin/ip")

    @patch("shutil.which", return_value="/usr/sbin/ip")
    @patch("os.getuid", return_value=1000)
    def test_user_in_group_passes(self, mock_uid, mock_which):
        import grp
        import pwd

        mock_grp_info = MagicMock()
        mock_grp_info.gr_mem = ["testuser"]
        mock_pwd_info = MagicMock()
        mock_pwd_info.pw_name = "testuser"

        with patch.object(grp, "getgrnam", return_value=mock_grp_info), \
             patch.object(pwd, "getpwuid", return_value=mock_pwd_info):
            from fcm.api.host import check_privileges

            check_privileges("/usr/sbin/ip")

    @patch("shutil.which", return_value="/usr/sbin/ip")
    @patch("os.getuid", return_value=1000)
    def test_user_not_in_group_fails(self, mock_uid, mock_which):
        import grp
        import pwd

        mock_grp_info = MagicMock()
        mock_grp_info.gr_mem = ["otheruser"]
        mock_pwd_info = MagicMock()
        mock_pwd_info.pw_name = "testuser"

        with patch.object(grp, "getgrnam", return_value=mock_grp_info), \
             patch.object(pwd, "getpwuid", return_value=mock_pwd_info):
            from fcm.api.host import check_privileges

            with pytest.raises(PrivilegeError, match="not in the 'fcm' group"):
                check_privileges("/usr/sbin/ip")

    @patch("shutil.which", return_value="/usr/sbin/ip")
    @patch("os.getuid", return_value=1000)
    def test_group_not_exists(self, mock_uid, mock_which):
        import grp

        with patch.object(grp, "getgrnam", side_effect=KeyError("fcm")):
            from fcm.api.host import check_privileges

            with pytest.raises(PrivilegeError, match="does not exist"):
                check_privileges("/usr/sbin/ip")


# ---------------------------------------------------------------------------
# core/host.py helper functions
# ---------------------------------------------------------------------------


class TestHostHelpers:
    def test_get_current_user(self):
        import os
        import pwd

        mock_pwd_info = MagicMock()
        mock_pwd_info.pw_name = "testuser"
        with patch.object(os, "getuid", return_value=1000), \
             patch.object(pwd, "getpwuid", return_value=mock_pwd_info):
            from fcm.core.host import _get_current_user

            assert _get_current_user() == "testuser"

    def test_group_exists_true(self):
        import grp

        with patch.object(grp, "getgrnam", return_value=MagicMock()):
            from fcm.core.host import _group_exists

            assert _group_exists("fcm") is True

    def test_group_exists_false(self):
        import grp

        with patch.object(grp, "getgrnam", side_effect=KeyError("fcm")):
            from fcm.core.host import _group_exists

            assert _group_exists("fcm") is False

    def test_user_in_group_true(self):
        import grp

        mock_grp = MagicMock()
        mock_grp.gr_mem = ["alice", "bob"]
        with patch.object(grp, "getgrnam", return_value=mock_grp):
            from fcm.core.host import _user_in_group

            assert _user_in_group("alice", "fcm") is True

    def test_user_in_group_false(self):
        import grp

        mock_grp = MagicMock()
        mock_grp.gr_mem = ["bob"]
        with patch.object(grp, "getgrnam", return_value=mock_grp):
            from fcm.core.host import _user_in_group

            assert _user_in_group("alice", "fcm") is False

    def test_user_in_group_no_group(self):
        import grp

        with patch.object(grp, "getgrnam", side_effect=KeyError("fcm")):
            from fcm.core.host import _user_in_group

            assert _user_in_group("alice", "fcm") is False

    @patch("fcm.core.host._group_exists", return_value=True)
    def test_create_group_already_exists(self, mock_exists):
        from fcm.core.host import _create_group

        assert _create_group("fcm") is False

    @patch("fcm.core.host._group_exists", return_value=False)
    @patch("fcm.core.host.subprocess.run")
    def test_create_group_success(self, mock_run, mock_exists):
        from fcm.core.host import _create_group

        assert _create_group("fcm") is True
        mock_run.assert_called_once()

    @patch("fcm.core.host._group_exists", return_value=False)
    @patch(
        "fcm.core.host.subprocess.run",
        side_effect=FileNotFoundError("groupadd"),
    )
    def test_create_group_command_not_found(self, mock_run, mock_exists):
        from fcm.core.host import _create_group

        with pytest.raises(HostError, match="groupadd command not found"):
            _create_group("fcm")

    @patch("fcm.core.host._user_in_group", return_value=True)
    def test_add_user_to_group_already_member(self, mock_in_group):
        from fcm.core.host import _add_user_to_group

        assert _add_user_to_group("alice", "fcm") is False

    @patch("fcm.core.host._user_in_group", return_value=False)
    @patch("fcm.core.host.subprocess.run")
    def test_add_user_to_group_success(self, mock_run, mock_in_group):
        from fcm.core.host import _add_user_to_group

        assert _add_user_to_group("alice", "fcm") is True

    def test_generate_sudoers_content(self):
        from fcm.core.host import _generate_sudoers_content

        content = _generate_sudoers_content("fcm")
        assert "%fcm ALL=(root) NOPASSWD:" in content
        assert "/usr/sbin/ip" in content
        assert "do not edit manually" in content

    @patch("fcm.core.host.Path.exists", return_value=True)
    def test_validate_sudoers_binaries_all_present(self, mock_exists):
        from fcm.core.host import _validate_sudoers_binaries

        _validate_sudoers_binaries()  # Should not raise

    @patch("fcm.core.host.Path.exists", return_value=False)
    def test_validate_sudoers_binaries_missing(self, mock_exists):
        from fcm.core.host import _validate_sudoers_binaries

        with pytest.raises(HostError, match="Required binary not found"):
            _validate_sudoers_binaries()

    @patch("fcm.core.host._group_exists", return_value=False)
    def test_remove_group_not_exists(self, mock_exists):
        from fcm.core.host import _remove_group

        assert _remove_group("fcm") is False

    @patch("fcm.core.host._group_exists", return_value=True)
    @patch("fcm.core.host.subprocess.run")
    def test_remove_group_success(self, mock_run, mock_exists):
        from fcm.core.host import _remove_group

        assert _remove_group("fcm") is True


# ---------------------------------------------------------------------------
# clean_host
# ---------------------------------------------------------------------------


class TestCleanHost:
    @patch("fcm.core.network_manager.list_networks", return_value=[])
    def test_clean_host_no_networks(self, mock_list):
        from fcm.core.host import clean_host

        summary = clean_host(MagicMock())
        assert summary == []

    @patch("fcm.core.network_manager.remove_network")
    @patch("fcm.core.network_manager.list_networks")
    def test_clean_host_removes_networks(self, mock_list, mock_remove):
        net = MagicMock()
        net.name = "default"
        net.bridge = "fcm-br0"
        mock_list.return_value = [net]
        from fcm.core.host import clean_host

        summary = clean_host(MagicMock())
        assert len(summary) == 1
        assert "Removed network 'default'" in summary[0]

    @patch("fcm.core.network_manager.remove_network", side_effect=subprocess.CalledProcessError(1, "fail"))
    @patch("fcm.core.network_manager.list_networks")
    def test_clean_host_handles_network_failure(self, mock_list, mock_remove):
        net = MagicMock()
        net.name = "default"
        net.bridge = "fcm-br0"
        mock_list.return_value = [net]
        from fcm.core.host import clean_host

        summary = clean_host(MagicMock())
        assert "Warning" in summary[0]

    @patch("fcm.core.network_manager.list_networks", side_effect=subprocess.CalledProcessError(1, "fail"))
    def test_clean_host_handles_list_failure(self, mock_list):
        from fcm.core.host import clean_host

        summary = clean_host(MagicMock())
        assert summary == []


# ---------------------------------------------------------------------------
# reset_host
# ---------------------------------------------------------------------------


class TestResetHost:
    @patch("fcm.core.host._state_file")
    @patch("fcm.core.host._remove_group", return_value=True)
    @patch("fcm.core.host._remove_sudoers", return_value=True)
    @patch("fcm.core.host.restore_host", return_value=[])
    @patch("fcm.core.host.clean_host", return_value=["Removed network 'default' (bridge: fcm-br0)"])
    def test_reset_host_full(self, mock_clean, mock_restore, mock_rm_sudoers, mock_rm_group, mock_state_file):
        mock_sf = MagicMock()
        mock_sf.exists.return_value = True
        mock_state_file.return_value = mock_sf
        from fcm.core.host import reset_host

        summary = reset_host(MagicMock())
        assert any("Removed network" in s for s in summary)
        assert any("sudoers" in s for s in summary)
        assert any("group" in s for s in summary)

    @patch("fcm.core.host._state_file")
    @patch("fcm.core.host._remove_group", return_value=False)
    @patch("fcm.core.host._remove_sudoers", return_value=False)
    @patch("fcm.core.host.restore_host", side_effect=HostError("no state"))
    @patch("fcm.core.host.clean_host", return_value=[])
    def test_reset_host_nothing_to_do(self, mock_clean, mock_restore, mock_rm_sudoers, mock_rm_group, mock_state_file):
        mock_sf = MagicMock()
        mock_sf.exists.return_value = False
        mock_state_file.return_value = mock_sf
        from fcm.core.host import reset_host

        summary = reset_host(MagicMock())
        assert summary == []


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
# Deprecated prune alias
# ---------------------------------------------------------------------------


class TestDeprecatedPrune:
    @patch("fcm.core.vm_manager.VMManager.list_all", return_value=[])
    @patch("fcm.cli.host.get_cache_dir")
    @patch("fcm.cli.host.prune_host")
    def test_prune_shows_deprecation(self, mock_prune, mock_cache, mock_list_all, tmp_path):
        from fcm.cli.host import app

        mock_cache.return_value = tmp_path
        mock_prune.return_value = []
        result = runner.invoke(app, ["prune", "--force"])
        assert result.exit_code == 0
        assert "deprecated" in result.output


class TestDeprecatedRestore:
    @patch("fcm.cli.host.get_cache_dir")
    @patch("fcm.cli.host.restore_host")
    def test_restore_shows_deprecation(self, mock_restore, mock_cache, tmp_path):
        from fcm.cli.host import app

        mock_cache.return_value = tmp_path
        mock_restore.return_value = [
            MagicMock(setting="net.ipv4.ip_forward", original_value="1", applied_value="0"),
        ]
        result = runner.invoke(app, ["restore"])
        assert result.exit_code == 0
        assert "deprecated" in result.output


# ---------------------------------------------------------------------------
# Top-level help command
# ---------------------------------------------------------------------------


class TestHelpCommand:
    def test_help_no_args(self):
        from fcm.main import app

        result = runner.invoke(app, ["help"])
        assert result.exit_code == 0
        assert "Firecracker Manager" in result.output or "fcm" in result.output

    def test_help_vm(self):
        from fcm.main import app

        result = runner.invoke(app, ["help", "vm"])
        assert result.exit_code == 0
        # Should show VM subcommand help
        assert "vm" in result.output.lower()

    def test_help_host(self):
        from fcm.main import app

        result = runner.invoke(app, ["help", "host"])
        assert result.exit_code == 0
        assert "host" in result.output.lower()

    def test_help_unknown_command(self):
        from fcm.main import app

        result = runner.invoke(app, ["help", "nonexistent"])
        assert result.exit_code == 1
        assert "Unknown command" in result.output
