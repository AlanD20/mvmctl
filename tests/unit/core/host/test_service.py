"""Tests for HostService — stateless host setup and stateful restore operations."""

from __future__ import annotations

import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.host._repository import HostRepository
from mvmctl.core.host._service import HostService
from mvmctl.exceptions import HostError
from mvmctl.models import HostStateChangeItem

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def repo() -> HostRepository:
    return HostRepository()


@pytest.fixture
def service(repo: HostRepository) -> HostService:
    return HostService(repo)


# ===========================================================================
# check_kvm_access
# ===========================================================================


class TestCheckKvmAccess:
    def test_kvm_exists_and_accessible(self) -> None:
        """check_kvm_access should return True when /dev/kvm exists and is RW."""
        with (
            patch("pathlib.Path.exists", return_value=True),
            patch("os.access", return_value=True),
        ):
            assert HostService.check_kvm_access() is True

    def test_kvm_missing(self) -> None:
        """check_kvm_access should return False when /dev/kvm does not exist."""
        with patch("pathlib.Path.exists", return_value=False):
            assert HostService.check_kvm_access() is False

    def test_kvm_no_permission(self) -> None:
        """check_kvm_access should return False when /dev/kvm exists but not accessible."""
        with (
            patch("pathlib.Path.exists", return_value=True),
            patch("os.access", return_value=False),
        ):
            assert HostService.check_kvm_access() is False


# ===========================================================================
# check_required_binaries
# ===========================================================================


class TestCheckRequiredBinaries:
    def test_all_found(self) -> None:
        """check_required_binaries should return empty list when all binaries exist."""
        with patch("shutil.which", return_value="/usr/bin/ip"):
            assert HostService.check_required_binaries() == []

    def test_some_missing(self) -> None:
        """check_required_binaries should list missing binary names."""

        def side_effect(name: str) -> str | None:
            if name == "ip":
                return None
            return f"/usr/bin/{name}"

        with patch("shutil.which", side_effect=side_effect):
            missing = HostService.check_required_binaries()
            assert "ip" in missing

    def test_iso_tool_both_missing(self) -> None:
        """check_required_binaries should report cloud-localds as missing when neither found."""

        def side_effect(name: str) -> str | None:
            if name == "cloud-localds":
                return None
            return f"/usr/bin/{name}"

        with patch("shutil.which", side_effect=side_effect):
            missing = HostService.check_required_binaries()
            assert "cloud-localds" in missing

    def test_iso_tool_found(self) -> None:
        """check_required_binaries should succeed when cloud-localds is present."""

        def side_effect(name: str) -> str | None:
            if name == "ip":
                return None
            return f"/usr/bin/{name}"

        with patch("shutil.which", side_effect=side_effect):
            missing = HostService.check_required_binaries()
            assert "ip" in missing
            assert "cloud-localds" not in missing

    def test_all_missing(self) -> None:
        """check_required_binaries should report all missing when none are found."""
        with patch("shutil.which", return_value=None):
            missing = HostService.check_required_binaries()
            assert "ip" in missing
            assert "iptables" in missing
            assert "qemu-img" in missing
            assert "modprobe" in missing
            assert "visudo" in missing


# ===========================================================================
# check_cloud_localds
# ===========================================================================


class TestCheckCloudLocalds:
    def test_cloud_localds_found(self) -> None:
        """check_cloud_localds should return True when cloud-localds is available."""
        with patch("shutil.which", return_value="/usr/bin/cloud-localds"):
            assert HostService.check_cloud_localds() is True

    def test_cloud_localds_not_found(self) -> None:
        """check_cloud_localds should return False when cloud-localds is not available."""
        with patch("shutil.which", return_value=None):
            assert HostService.check_cloud_localds() is False


# ===========================================================================
# _group_exists / _user_in_group
# ===========================================================================


class TestGroupHelpers:
    def test_group_exists_found(self) -> None:
        """_group_exists should return True when the group is found."""
        with patch("grp.getgrnam") as mock_getgrnam:
            mock_getgrnam.return_value = MagicMock()
            assert HostService._group_exists("mvm") is True

    def test_group_exists_not_found(self) -> None:
        """_group_exists should return False when the group is not found."""
        with patch("grp.getgrnam", side_effect=KeyError("mvm")):
            assert HostService._group_exists("mvm") is False

    def test_user_in_group_true(self) -> None:
        """_user_in_group should return True when user is a member."""
        mock_group = MagicMock()
        mock_group.gr_mem = ["testuser"]
        with patch("grp.getgrnam", return_value=mock_group):
            assert HostService._user_in_group("testuser", "mvm") is True

    def test_user_in_group_false(self) -> None:
        """_user_in_group should return False when user is NOT a member."""
        mock_group = MagicMock()
        mock_group.gr_mem = ["otheruser"]
        with patch("grp.getgrnam", return_value=mock_group):
            assert HostService._user_in_group("testuser", "mvm") is False

    def test_user_in_group_group_missing(self) -> None:
        """_user_in_group should return False when the group does not exist."""
        with patch("grp.getgrnam", side_effect=KeyError("mvm")):
            assert HostService._user_in_group("testuser", "mvm") is False


# ===========================================================================
# create_group
# ===========================================================================


class TestCreateGroup:
    def test_create_group_new(self) -> None:
        """create_group should run groupadd for a new group and return True."""
        with (
            patch.object(HostService, "_group_exists", return_value=False),
            patch.object(HostService, "_run") as mock_run,
        ):
            result = HostService.create_group("mvm")
            assert result is True
            mock_run.assert_called_once_with(
                ["groupadd", "--system", "mvm"],
                failure_msg="Failed to create group mvm",
                missing_msg="groupadd command not found",
            )

    def test_create_group_exists(self) -> None:
        """create_group should return False when group already exists."""
        with patch.object(HostService, "_group_exists", return_value=True):
            result = HostService.create_group("mvm")
            assert result is False


# ===========================================================================
# add_user_to_group
# ===========================================================================


class TestAddUserToGroup:
    def test_add_user_new(self) -> None:
        """add_user_to_group should run usermod and return True."""
        with (
            patch.object(HostService, "_user_in_group", return_value=False),
            patch.object(HostService, "_run") as mock_run,
        ):
            result = HostService.add_user_to_group("testuser", "mvm")
            assert result is True
            mock_run.assert_called_once_with(
                ["usermod", "-aG", "mvm", "testuser"],
                failure_msg="Failed to add testuser to group mvm",
                missing_msg="usermod command not found",
            )

    def test_add_user_already_member(self) -> None:
        """add_user_to_group should return False when user is already a member."""
        with patch.object(HostService, "_user_in_group", return_value=True):
            result = HostService.add_user_to_group("testuser", "mvm")
            assert result is False


# ===========================================================================
# remove_user_from_group
# ===========================================================================


class TestRemoveUserFromGroup:
    def test_remove_user_success(self) -> None:
        """remove_user_from_group should run gpasswd and return True."""
        mock_group = MagicMock()
        mock_group.gr_mem = ["testuser"]
        with (
            patch("grp.getgrnam", return_value=mock_group),
            patch.object(HostService, "_run") as mock_run,
        ):
            result = HostService.remove_user_from_group("testuser", "mvm")
            assert result is True
            mock_run.assert_called_once_with(
                ["gpasswd", "-d", "testuser", "mvm"],
                failure_msg="Failed to remove user testuser from group mvm",
                missing_msg="gpasswd command not found",
            )

    def test_remove_user_not_member(self) -> None:
        """remove_user_from_group should return False when user is not a member."""
        mock_group = MagicMock()
        mock_group.gr_mem = ["otheruser"]
        with patch("grp.getgrnam", return_value=mock_group):
            result = HostService.remove_user_from_group("testuser", "mvm")
            assert result is False

    def test_remove_user_group_missing(self) -> None:
        """remove_user_from_group should return False when group does not exist."""
        with patch("grp.getgrnam", side_effect=KeyError("mvm")):
            result = HostService.remove_user_from_group("testuser", "mvm")
            assert result is False


# ===========================================================================
# validate_sudoers_binaries
# ===========================================================================


class TestValidateSudoersBinaries:
    def test_all_binaries_exist(self) -> None:
        """validate_sudoers_binaries should not raise when all binaries exist."""
        with patch("pathlib.Path.exists", return_value=True):
            HostService.validate_sudoers_binaries()  # Should not raise

    def test_binary_missing(self) -> None:
        """validate_sudoers_binaries should raise HostError when a binary is missing."""
        with patch("pathlib.Path.exists", return_value=False):
            with pytest.raises(HostError, match="Required binary not found"):
                HostService.validate_sudoers_binaries()


# ===========================================================================
# _generate_sudoers_content
# ===========================================================================


class TestGenerateSudoersContent:
    def test_valid_group(self) -> None:
        """_generate_sudoers_content should produce valid sudoers content."""
        content = HostService._generate_sudoers_content("mvm")
        assert "%mvm ALL=(root) NOPASSWD:" in content
        assert "# Managed by" in content
        assert "# To remove:" in content

    def test_group_with_dash(self) -> None:
        """_generate_sudoers_content should accept group names with dashes."""
        content = HostService._generate_sudoers_content("mvm-users")
        assert "%mvm-users ALL=(root) NOPASSWD:" in content

    def test_group_with_underscore(self) -> None:
        """_generate_sudoers_content should accept group names with underscores."""
        content = HostService._generate_sudoers_content("mvm_users")
        assert "%mvm_users ALL=(root) NOPASSWD:" in content


# ===========================================================================
# write_sudoers / remove_sudoers
# ===========================================================================


class TestWriteSudoers:
    def test_write_sudoers_success(self, tmp_path: Path) -> None:
        """write_sudoers should write and validate sudoers file."""
        sudoers_path = tmp_path / "sudoers.d" / "mvmctl"
        with (
            patch.object(HostService, "validate_sudoers_binaries"),
            patch("mvmctl.core.host._service.subprocess.run") as mock_run,
        ):
            # visudo validation succeeds
            mock_run.return_value = MagicMock(
                returncode=0, stdout="", stderr=""
            )
            result = HostService.write_sudoers(sudoers_path, "mvm")
            assert result is True
            assert sudoers_path.exists()
            content = sudoers_path.read_text()
            assert "%mvm ALL=(root) NOPASSWD:" in content

    def test_write_sudoers_visudo_fails(self, tmp_path: Path) -> None:
        """write_sudoers should raise HostError when visudo validation fails."""
        sudoers_path = tmp_path / "sudoers" / "mvmctl"
        with (
            patch.object(HostService, "validate_sudoers_binaries"),
            patch("mvmctl.core.host._service.subprocess.run") as mock_run,
        ):
            mock_run.return_value = MagicMock(
                returncode=1, stderr="parse error"
            )
            with pytest.raises(HostError, match="visudo validation"):
                HostService.write_sudoers(sudoers_path, "mvm")

    def test_write_sudoers_visudo_not_found(self, tmp_path: Path) -> None:
        """write_sudoers should raise HostError when visudo binary is missing."""
        sudoers_path = tmp_path / "sudoers" / "mvmctl"
        with (
            patch.object(HostService, "validate_sudoers_binaries"),
            patch(
                "mvmctl.core.host._service.subprocess.run",
                side_effect=FileNotFoundError("visudo"),
            ),
        ):
            with pytest.raises(HostError, match="visudo not found"):
                HostService.write_sudoers(sudoers_path, "mvm")

    def test_remove_sudoers_exists(self, tmp_path: Path) -> None:
        """remove_sudoers should remove existing file and return True."""
        sudoers_path = tmp_path / "sudoers" / "mvmctl"
        sudoers_path.parent.mkdir(parents=True)
        sudoers_path.write_text("content")
        result = HostService.remove_sudoers(sudoers_path)
        assert result is True
        assert not sudoers_path.exists()

    def test_remove_sudoers_not_exists(self, tmp_path: Path) -> None:
        """remove_sudoers should return False when file does not exist."""
        sudoers_path = tmp_path / "nonexistent"
        result = HostService.remove_sudoers(sudoers_path)
        assert result is False

    def test_remove_sudoers_os_error(self, tmp_path: Path) -> None:
        """remove_sudoers should raise HostError on OS error."""
        sudoers_path = tmp_path / "sudoers" / "mvmctl"
        sudoers_path.parent.mkdir(parents=True)
        sudoers_path.write_text("content")
        with patch.object(
            Path, "unlink", side_effect=OSError("permission denied")
        ):
            with pytest.raises(
                HostError, match="Failed to remove sudoers file"
            ):
                HostService.remove_sudoers(sudoers_path)


# ===========================================================================
# remove_group
# ===========================================================================


class TestRemoveGroup:
    def test_remove_group_exists(self) -> None:
        """remove_group should run groupdel and return True."""
        with (
            patch.object(HostService, "_group_exists", return_value=True),
            patch.object(HostService, "_run") as mock_run,
        ):
            result = HostService.remove_group("mvm")
            assert result is True
            mock_run.assert_called_once_with(
                ["groupdel", "mvm"],
                failure_msg="Failed to remove group mvm",
                missing_msg="groupdel command not found",
            )

    def test_remove_group_not_exists(self) -> None:
        """remove_group should return False when group does not exist."""
        with patch.object(HostService, "_group_exists", return_value=False):
            result = HostService.remove_group("mvm")
            assert result is False


# ===========================================================================
# _run helper
# ===========================================================================


class TestRunHelper:
    def test_run_success(self) -> None:
        """_run should return CompletedProcess on success."""
        with patch("mvmctl.core.host._service.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(stdout="ok", returncode=0)
            result = HostService._run(
                ["echo", "ok"],
                failure_msg="failed",
                missing_msg="missing",
            )
            assert result.stdout == "ok"

    def test_run_called_process_error(self) -> None:
        """_run should raise HostError on CalledProcessError."""
        with patch(
            "mvmctl.core.host._service.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, "cmd"),
        ):
            with pytest.raises(HostError, match="failed"):
                HostService._run(
                    ["cmd"], failure_msg="failed", missing_msg="missing"
                )

    def test_run_file_not_found(self) -> None:
        """_run should raise HostError on FileNotFoundError."""
        with patch(
            "mvmctl.core.host._service.subprocess.run",
            side_effect=FileNotFoundError("cmd"),
        ):
            with pytest.raises(HostError, match="missing"):
                HostService._run(
                    ["cmd"], failure_msg="failed", missing_msg="missing"
                )


# ===========================================================================
# get_ip_forward_status / enable_ip_forward
# ===========================================================================


class TestIpForward:
    def test_get_ip_forward_enabled(self) -> None:
        """_get_ip_forward_status should return the sysctl value."""
        with patch.object(HostService, "_run") as mock_run:
            mock_run.return_value = MagicMock(stdout="1\n")
            result = HostService._get_ip_forward_status()
            assert result == "1"

    def test_get_ip_forward_disabled(self) -> None:
        """_get_ip_forward_status should return '0' when disabled."""
        with patch.object(HostService, "_run") as mock_run:
            mock_run.return_value = MagicMock(stdout="0\n")
            assert HostService._get_ip_forward_status() == "0"

    def test_enable_ip_forward_already_enabled(self) -> None:
        """enable_ip_forward should return None when already enabled."""
        with patch.object(
            HostService, "_get_ip_forward_status", return_value="1"
        ):
            assert HostService.enable_ip_forward() is None

    def test_enable_ip_forward_needs_enabling(self) -> None:
        """enable_ip_forward should call sysctl -w and return a change."""
        with (
            patch.object(
                HostService, "_get_ip_forward_status", return_value="0"
            ),
            patch.object(HostService, "_run") as mock_run,
        ):
            mock_run.return_value = MagicMock(returncode=0)
            change = HostService.enable_ip_forward()
            assert change is not None
            assert change.setting == "net.ipv4.ip_forward"
            assert change.original_value == "0"
            assert change.applied_value == "1"
            assert change.mechanism == "sysctl"
            mock_run.assert_called_once_with(
                ["sysctl", "-w", "net.ipv4.ip_forward=1"],
                failure_msg="Failed to enable IP forwarding",
                missing_msg="sysctl command not found",
            )


# ===========================================================================
# persist_sysctl
# ===========================================================================


class TestPersistSysctl:
    def test_already_correct(self, tmp_path: Path) -> None:
        """persist_sysctl should return None when file already has correct content."""
        conf_file = tmp_path / "sysctl.d" / "mvmctl.conf"
        conf_file.parent.mkdir(parents=True)
        conf_file.write_text("net.ipv4.ip_forward = 1\n")
        with patch("mvmctl.core.host._service.SYSCTL_CONF", conf_file):
            result = HostService.persist_sysctl()
            assert result is None

    def test_file_does_not_exist(self, tmp_path: Path) -> None:
        """persist_sysctl should create file and return a change when file does not exist."""
        conf_file = tmp_path / "sysctl.d" / "mvmctl.conf"
        with patch("mvmctl.core.host._service.SYSCTL_CONF", conf_file):
            change = HostService.persist_sysctl()
            assert change is not None
            assert change.setting == "sysctl_persist_file"
            assert change.original_value is None
            assert change.applied_value == str(conf_file)
            assert change.mechanism == "file_create"
            assert conf_file.exists()
            assert conf_file.read_text() == "net.ipv4.ip_forward = 1\n"

    def test_file_has_wrong_content(self, tmp_path: Path) -> None:
        """persist_sysctl should overwrite file and return change when content differs."""
        conf_file = tmp_path / "sysctl.d" / "mvmctl.conf"
        conf_file.parent.mkdir(parents=True)
        conf_file.write_text("old content\n")
        with patch("mvmctl.core.host._service.SYSCTL_CONF", conf_file):
            change = HostService.persist_sysctl()
            assert change is not None
            assert change.original_value == "old content\n"
            assert conf_file.read_text() == "net.ipv4.ip_forward = 1\n"

    def test_write_failure(self, tmp_path: Path) -> None:
        """persist_sysctl should raise HostError when write fails."""
        conf_file = tmp_path / "sysctl.d" / "mvmctl.conf"
        with (
            patch("mvmctl.core.host._service.SYSCTL_CONF", conf_file),
            patch.object(
                Path, "write_text", side_effect=OSError("readonly fs")
            ),
        ):
            with pytest.raises(HostError, match="Failed to write"):
                HostService.persist_sysctl()


# ===========================================================================
# _is_module_loaded
# ===========================================================================


class TestIsModuleLoaded:
    def test_module_found(self) -> None:
        """_is_module_loaded should return True when module is in lsmod output."""
        with patch("mvmctl.core.host._service.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(
                returncode=0,
                stdout="kvm                   1234  0\nkvm_intel              567  0\n",
            )
            assert HostService._is_module_loaded("kvm") is True

    def test_module_not_found(self) -> None:
        """_is_module_loaded should return False when module is absent."""
        with patch("mvmctl.core.host._service.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(
                returncode=0,
                stdout="ext4                  1234  1\n",
            )
            assert HostService._is_module_loaded("kvm") is False

    def test_lsmod_fails(self) -> None:
        """_is_module_loaded should return False when lsmod fails."""
        with patch("mvmctl.core.host._service.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(returncode=1, stdout="")
            assert HostService._is_module_loaded("kvm") is False

    def test_empty_output(self) -> None:
        """_is_module_loaded should return False when lsmod returns empty."""
        with patch("mvmctl.core.host._service.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0, stdout="")
            assert HostService._is_module_loaded("kvm") is False

    def test_partial_name_no_match(self) -> None:
        """Module name 'kvm_intel' should not match query for 'kvm'."""
        with patch("mvmctl.core.host._service.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(
                returncode=0,
                stdout="kvm_intel  567  0\n",
            )
            assert HostService._is_module_loaded("kvm") is False

    def test_blank_lines(self) -> None:
        """_is_module_loaded should parse output with blank lines."""
        with patch("mvmctl.core.host._service.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(
                returncode=0,
                stdout="\n\nkvm  1234  0\n\n",
            )
            assert HostService._is_module_loaded("kvm") is True

    def test_os_error(self) -> None:
        """_is_module_loaded should return False on OSError."""
        with patch(
            "mvmctl.core.host._service.subprocess.run",
            side_effect=OSError("no lsmod"),
        ):
            assert HostService._is_module_loaded("kvm") is False


# ===========================================================================
# _load_module
# ===========================================================================


class TestLoadModule:
    def test_load_success(self) -> None:
        """_load_module should call modprobe and return a HostStateChangeItem."""
        with patch.object(HostService, "_run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)
            change = HostService._load_module("kvm")
            assert change.setting == "kernel_module_load"
            assert change.mechanism == "modprobe"
            assert change.applied_value == "kvm"
            mock_run.assert_called_once_with(
                ["modprobe", "kvm"],
                failure_msg="Failed to load kernel module kvm",
                missing_msg="modprobe command not found",
            )

    def test_load_with_repo(self, repo: HostRepository) -> None:
        """_load_module should record the change via repo when provided."""
        with patch.object(HostService, "_run"):
            change = HostService._load_module(
                "kvm", repo=repo, session_id="sess"
            )
            assert change.applied_value == "kvm"
            stored = repo.list_changes(include_reverted=True)
            assert len(stored) == 1
            assert stored[0].setting == "kernel_module_load"


# ===========================================================================
# ensure_kvm_modules
# ===========================================================================


class TestEnsureKvmModules:
    def test_all_loaded(self) -> None:
        """ensure_kvm_modules should return no changes when all modules are loaded."""
        with (
            patch.object(
                HostService,
                "_is_module_loaded",
                side_effect=lambda m: m in ("kvm", "kvm_intel", "kvm_amd"),
            ),
            patch("mvmctl.core.host._service.subprocess.run"),
        ):
            # modprobe --dry-run not called for loaded modules
            changes, _ = HostService.ensure_kvm_modules()
            assert changes == []

    def test_kvm_needs_loading(self) -> None:
        """ensure_kvm_modules should load kvm and a vendor module when neither is present."""
        loaded = set()

        def is_loaded(m: str) -> bool:
            return m in loaded

        def load_side(m: str) -> HostStateChangeItem | None:
            loaded.add(m)
            return None

        with (
            patch.object(
                HostService, "_is_module_loaded", side_effect=is_loaded
            ),
            patch.object(
                HostService,
                "_load_module",
                side_effect=lambda m, **kw: HostStateChangeItem(
                    session_id="",
                    init_timestamp="",
                    setting="kernel_module_load",
                    mechanism="modprobe",
                    applied_value=m,
                    reverted=False,
                    change_order=0,
                    created_at="",
                ),
            ),
            patch("mvmctl.core.host._service.subprocess.run") as mock_run,
        ):
            mock_run.return_value = MagicMock(returncode=0, stdout="")
            changes, _ = HostService.ensure_kvm_modules()
            assert any(c.applied_value == "kvm" for c in changes)
            assert any(
                c.applied_value in ("kvm_intel", "kvm_amd") for c in changes
            )

    def test_no_vendor_modules_available(self) -> None:
        """ensure_kvm_modules should raise HostError when no vendor modules available."""

        def is_loaded(m: str) -> bool:
            return False

        with (
            patch.object(
                HostService, "_is_module_loaded", side_effect=is_loaded
            ),
            patch("mvmctl.core.host._service.subprocess.run") as mock_run,
        ):
            # modprobe --dry-run fails for both
            mock_run.return_value = MagicMock(returncode=1)
            with pytest.raises(
                HostError, match="No KVM vendor modules available"
            ):
                HostService.ensure_kvm_modules()


# ===========================================================================
# save_firewall_rules
# ===========================================================================


class TestSaveFirewallRules:
    def test_save_iptables_success(self, tmp_path: Path) -> None:
        """save_firewall_rules('iptables') should persist iptables rules and return a change."""
        rules_path = tmp_path / "iptables" / "rules.v4"
        rules_path.parent.mkdir(parents=True)
        with (
            patch("mvmctl.core.host._service.subprocess.run") as mock_run,
            patch(
                "mvmctl.core.host._service.IPTABLES_RULES_V4", str(rules_path)
            ),
            patch(
                "mvmctl.utils.network.NetworkUtils.strip_tap_rules",
                side_effect=lambda s: s,
            ),
        ):
            mock_run.return_value = MagicMock(
                stdout="*filter\n-A INPUT -j ACCEPT\nCOMMIT\n", returncode=0
            )
            change = HostService.save_firewall_rules("iptables")
            assert change is not None
            assert change.setting == "iptables_rules_v4"
            assert change.mechanism == "iptables_save"
            assert rules_path.exists()
            content = rules_path.read_text()
            assert "ACCEPT" in content

    def test_save_iptables_save_unavailable(self) -> None:
        """save_firewall_rules('iptables') should return None when iptables-save fails."""
        with patch(
            "mvmctl.core.host._service.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, "iptables-save"),
        ):
            assert HostService.save_firewall_rules("iptables") is None

    def test_save_iptables_save_not_found(self) -> None:
        """save_firewall_rules('iptables') should return None when iptables-save binary is missing."""
        with patch(
            "mvmctl.core.host._service.subprocess.run",
            side_effect=FileNotFoundError("iptables-save"),
        ):
            assert HostService.save_firewall_rules("iptables") is None


# ===========================================================================
# restore_state
# ===========================================================================


class TestRestoreState:
    def test_restore_no_state(self, service: HostService) -> None:
        """restore_state should raise HostError when no state exists."""
        with pytest.raises(HostError, match="No saved host state to restore"):
            service.restore_state()

    def test_restore_sysctl_change(
        self, service: HostService, repo: HostRepository
    ) -> None:
        """restore_state should revert a sysctl change to its original value."""
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="net.ipv4.ip_forward",
                mechanism="sysctl",
                original_value="0",
                applied_value="1",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        with patch.object(HostService, "_run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)
            reverted = service.restore_state()
            assert len(reverted) == 1
            assert reverted[0].setting == "net.ipv4.ip_forward"
            assert reverted[0].mechanism == "sysctl"
            assert reverted[0].applied_value == "0"
            mock_run.assert_called_once_with(
                ["sysctl", "-w", "net.ipv4.ip_forward=0"],
                failure_msg="Failed to revert net.ipv4.ip_forward",
                missing_msg="sysctl command not found",
            )

    def test_restore_sysctl_null_original_skipped(
        self, service: HostService, repo: HostRepository
    ) -> None:
        """restore_state should skip sysctl change when original_value is None."""
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="net.ipv4.ip_forward",
                mechanism="sysctl",
                original_value=None,
                applied_value="1",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        with patch.object(HostService, "_run") as mock_run:
            reverted = service.restore_state()
            assert reverted == []
            mock_run.assert_not_called()

    def test_restore_file_create_removes_file(
        self, service: HostService, repo: HostRepository, tmp_path: Path
    ) -> None:
        """restore_state should delete a created file when original_value is None."""
        target_file = tmp_path / "test.conf"
        target_file.write_text("content")
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="sysctl_persist_file",
                mechanism="file_create",
                original_value=None,
                applied_value=str(target_file),
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        # The restorable_files check requires paths under DEFAULT_SYSCTL_CONF_DIR
        # which is "/etc/sysctl.d/". Since target_file is under tmp_path, it won't
        # match. Let's test the logic via patching SUDOERS_DROP_IN_PATH.
        # Instead, test the code path for a file that IS restorable.
        # We need SUDOERS_DROP_IN_PATH to point to our tmp_path file.
        with (
            patch(
                "mvmctl.core.host._service.SUDOERS_DROP_IN_PATH",
                str(target_file),
            ),
        ):
            reverted = service.restore_state()
            assert len(reverted) == 1
            assert reverted[0].mechanism == "file_remove"
            assert not target_file.exists()

    def test_restore_file_create_with_original(
        self, service: HostService, repo: HostRepository, tmp_path: Path
    ) -> None:
        """restore_state should restore file content when original_value is set."""
        target_file = tmp_path / "sudoers.d" / "mvm"
        target_file.parent.mkdir(parents=True)
        target_file.write_text("new content")
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="sudoers_dropin",
                mechanism="file_create",
                original_value="old content",
                applied_value=str(target_file),
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        with (
            patch(
                "mvmctl.core.host._service.SUDOERS_DROP_IN_PATH",
                str(target_file),
            ),
            patch("mvmctl.core.host._service.subprocess.run") as mock_run,
        ):
            mock_run.return_value = MagicMock(returncode=0)
            reverted = service.restore_state()
            assert len(reverted) == 1
            assert reverted[0].mechanism == "file_remove"
            assert target_file.read_text() == "old content"

    def test_restore_file_create_target_missing(
        self, service: HostService, repo: HostRepository, tmp_path: Path
    ) -> None:
        """restore_state should silently skip file revert when target doesn't exist."""
        target_file = tmp_path / "nonexistent.conf"
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="sysctl_persist_file",
                mechanism="file_create",
                original_value=None,
                applied_value=str(target_file),
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        with (
            patch(
                "mvmctl.core.host._service.SUDOERS_DROP_IN_PATH",
                str(target_file),
            ),
        ):
            reverted = service.restore_state()
            assert reverted == []

    def test_restore_reverse_order(
        self, service: HostService, repo: HostRepository, tmp_path: Path
    ) -> None:
        """restore_state should revert changes in reverse order (LIFO)."""
        target_file = tmp_path / "sysctl.d" / "mvmctl.conf"
        target_file.parent.mkdir(parents=True)
        target_file.write_text("content")
        repo.add_change(
            HostStateChangeItem(
                id=1,
                session_id="sess",
                init_timestamp="",
                setting="net.ipv4.ip_forward",
                mechanism="sysctl",
                original_value="0",
                applied_value="1",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        repo.add_change(
            HostStateChangeItem(
                id=2,
                session_id="sess",
                init_timestamp="",
                setting="sysctl_persist_file",
                mechanism="file_create",
                original_value=None,
                applied_value=str(target_file),
                reverted=False,
                change_order=1,
                created_at="",
            )
        )
        with (
            patch.object(HostService, "_run"),
            patch(
                "mvmctl.core.host._service.SUDOERS_DROP_IN_PATH",
                str(target_file),
            ),
        ):
            reverted = service.restore_state()
            assert len(reverted) == 2
            assert (
                reverted[0].setting == "sysctl_persist_file"
            )  # Last change reverted first
            assert reverted[1].setting == "net.ipv4.ip_forward"

    def test_restore_modprobe_ignored(
        self, service: HostService, repo: HostRepository
    ) -> None:
        """restore_state should skip modprobe changes."""
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="kernel_module_load",
                mechanism="modprobe",
                applied_value="kvm",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        with patch.object(HostService, "_run"):
            reverted = service.restore_state()
            assert reverted == []
