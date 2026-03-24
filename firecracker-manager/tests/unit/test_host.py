"""Tests for core/host.py."""

import json
import subprocess
from unittest.mock import MagicMock, patch

import pytest

from fcm.core.host import clean_host, reset_host
from fcm.core.host_state import (
    HostChange,
    HostState,
    _save_state,
    _state_dir,
    _state_file,
    get_host_state,
    restore_host,
)
from fcm.core.host_privilege import (
    _add_user_to_group,
    _create_group,
    _generate_sudoers_content,
    _get_current_user,
    _group_exists,
    _remove_group,
    _user_in_group,
    _validate_sudoers_binaries,
    check_privileges,
)
from fcm.core.host_setup import (
    _enable_ip_forward,
    _ensure_kvm_modules,
    _is_module_loaded,
    _load_module,
    _persist_sysctl,
    check_kvm_access,
    check_required_binaries,
    get_ip_forward_status,
    init_host,
)
from fcm.exceptions import HostError, NetworkError, PrivilegeError


# ---------------------------------------------------------------------------
# _state_dir / _state_file helpers
# ---------------------------------------------------------------------------


def test_state_dir(tmp_path):
    """_state_dir should return the 'host' subdirectory of the given cache path."""
    result = _state_dir(tmp_path)
    assert result == tmp_path / "host"


def test_state_file(tmp_path):
    """_state_file should return the state.json path within the host subdirectory."""
    result = _state_file(tmp_path)
    assert result == tmp_path / "host" / "state.json"


# ---------------------------------------------------------------------------
# check_kvm_access
# ---------------------------------------------------------------------------


@patch("fcm.core.host_setup.os.access", return_value=True)
@patch("fcm.core.host_setup.Path.exists", return_value=True)
def test_check_kvm_access_ok(mock_exists, mock_access):
    """check_kvm_access should return True when /dev/kvm exists and is accessible."""
    assert check_kvm_access() is True


@patch("fcm.core.host_setup.Path.exists", return_value=False)
def test_check_kvm_access_missing(mock_exists):
    """check_kvm_access should return False when /dev/kvm does not exist."""
    assert check_kvm_access() is False


@patch("fcm.core.host_setup.os.access", return_value=False)
@patch("fcm.core.host_setup.Path.exists", return_value=True)
def test_check_kvm_access_no_permission(mock_exists, mock_access):
    """check_kvm_access should return False when /dev/kvm exists but is not readable."""
    assert check_kvm_access() is False


# ---------------------------------------------------------------------------
# check_required_binaries
# ---------------------------------------------------------------------------


@patch("fcm.core.host_setup.shutil.which")
def test_check_required_binaries_all_found(mock_which):
    """check_required_binaries should return an empty list when all required binaries are present."""
    mock_which.return_value = "/usr/bin/something"
    result = check_required_binaries()
    assert result == []


@patch("fcm.core.host_setup.shutil.which")
def test_check_required_binaries_missing_some(mock_which):
    """check_required_binaries should list each missing binary name when some are absent."""

    def side_effect(name):
        if name == "ip":
            return None
        return "/usr/bin/" + name

    mock_which.side_effect = side_effect
    result = check_required_binaries()
    assert "ip" in result


@patch("fcm.core.host_setup.shutil.which")
def test_check_required_binaries_no_iso_tool(mock_which):
    """check_required_binaries should report the iso-tool pair as missing when neither is found."""

    def side_effect(name):
        if name in ("mkisofs", "genisoimage"):
            return None
        return "/usr/bin/" + name

    mock_which.side_effect = side_effect
    result = check_required_binaries()
    assert "mkisofs or genisoimage" in result


@patch("fcm.core.host_setup.shutil.which")
def test_check_required_binaries_has_genisoimage_only(mock_which):
    """check_required_binaries should succeed when genisoimage is present even if mkisofs is absent."""

    def side_effect(name):
        if name == "mkisofs":
            return None
        return "/usr/bin/" + name

    mock_which.side_effect = side_effect
    result = check_required_binaries()
    assert result == []


@patch("fcm.core.host_setup.shutil.which")
def test_check_required_binaries_has_mkisofs_only(mock_which):
    """check_required_binaries should succeed when mkisofs is present even if genisoimage is absent."""

    def side_effect(name):
        if name == "genisoimage":
            return None
        return "/usr/bin/" + name

    mock_which.side_effect = side_effect
    result = check_required_binaries()
    assert result == []


@patch("fcm.core.host_setup.shutil.which")
def test_check_required_binaries_all_missing(mock_which):
    """check_required_binaries should report all required binaries when none are found."""
    mock_which.return_value = None
    result = check_required_binaries()
    assert "ip" in result
    assert "iptables" in result
    assert "qemu-img" in result
    assert "mkisofs or genisoimage" in result


# ---------------------------------------------------------------------------
# get_ip_forward_status
# ---------------------------------------------------------------------------


@patch("fcm.core.host_setup.subprocess.run")
def test_get_ip_forward_status_success(mock_run):
    """get_ip_forward_status should return the stripped sysctl value on success."""
    mock_run.return_value = MagicMock(stdout="1\n")
    result = get_ip_forward_status()
    assert result == "1"
    mock_run.assert_called_once_with(
        ["sysctl", "-n", "net.ipv4.ip_forward"],
        capture_output=True,
        text=True,
        check=True,
    )


@patch("fcm.core.host_setup.subprocess.run")
def test_get_ip_forward_status_zero(mock_run):
    """get_ip_forward_status should return '0' when IP forwarding is disabled."""
    mock_run.return_value = MagicMock(stdout="0\n")
    assert get_ip_forward_status() == "0"


@patch("fcm.core.host_setup.subprocess.run")
def test_get_ip_forward_status_called_process_error(mock_run):
    """get_ip_forward_status should raise HostError when the sysctl command fails."""
    mock_run.side_effect = subprocess.CalledProcessError(1, "sysctl")
    with pytest.raises(HostError, match="Failed to read"):
        get_ip_forward_status()


@patch("fcm.core.host_setup.subprocess.run")
def test_get_ip_forward_status_file_not_found(mock_run):
    """get_ip_forward_status should raise HostError when the sysctl binary is not found."""
    mock_run.side_effect = FileNotFoundError("sysctl")
    with pytest.raises(HostError, match="sysctl command not found"):
        get_ip_forward_status()


# ---------------------------------------------------------------------------
# _is_module_loaded
# ---------------------------------------------------------------------------


@patch("fcm.core.host_setup.subprocess.run")
def test_is_module_loaded_found(mock_run):
    """_is_module_loaded should return True when the module name appears in lsmod output."""
    mock_run.return_value = MagicMock(
        returncode=0,
        stdout="kvm                   1234  0\nkvm_intel              567  0\n",
    )
    assert _is_module_loaded("kvm") is True


@patch("fcm.core.host_setup.subprocess.run")
def test_is_module_loaded_not_found(mock_run):
    """_is_module_loaded should return False when the module is absent from lsmod output."""
    mock_run.return_value = MagicMock(
        returncode=0,
        stdout="ext4                  1234  1\n",
    )
    assert _is_module_loaded("kvm") is False


@patch("fcm.core.host_setup.subprocess.run")
def test_is_module_loaded_lsmod_fails(mock_run):
    """_is_module_loaded should return False when lsmod exits with a non-zero code."""
    mock_run.return_value = MagicMock(returncode=1, stdout="")
    assert _is_module_loaded("kvm") is False


@patch("fcm.core.host_setup.subprocess.run")
def test_is_module_loaded_empty_output(mock_run):
    """_is_module_loaded should return False when lsmod returns empty output."""
    mock_run.return_value = MagicMock(returncode=0, stdout="")
    assert _is_module_loaded("kvm") is False


@patch("fcm.core.host_setup.subprocess.run")
def test_is_module_loaded_partial_name_no_match(mock_run):
    """Module name 'kvm_intel' should not match query for 'kvm'."""
    mock_run.return_value = MagicMock(
        returncode=0,
        stdout="kvm_intel              567  0\n",
    )
    assert _is_module_loaded("kvm") is False


@patch("fcm.core.host_setup.subprocess.run")
def test_is_module_loaded_empty_lines(mock_run):
    """_is_module_loaded should correctly parse output that contains blank lines."""
    mock_run.return_value = MagicMock(
        returncode=0,
        stdout="\n\nkvm  1234  0\n\n",
    )
    assert _is_module_loaded("kvm") is True


# ---------------------------------------------------------------------------
# _load_module
# ---------------------------------------------------------------------------


@patch("fcm.core.host_setup.subprocess.run")
def test_load_module_success(mock_run):
    """_load_module should call modprobe without raising when the command succeeds."""
    mock_run.return_value = MagicMock(returncode=0)
    _load_module("kvm")  # Should not raise
    mock_run.assert_called_once_with(
        ["modprobe", "kvm"],
        capture_output=True,
        text=True,
        check=True,
    )


@patch("fcm.core.host_setup.subprocess.run")
def test_load_module_called_process_error(mock_run):
    """_load_module should raise HostError when modprobe exits with an error code."""
    mock_run.side_effect = subprocess.CalledProcessError(1, "modprobe")
    with pytest.raises(HostError, match="Failed to load kernel module kvm"):
        _load_module("kvm")


@patch("fcm.core.host_setup.subprocess.run")
def test_load_module_file_not_found(mock_run):
    """_load_module should raise HostError when the modprobe binary is not found."""
    mock_run.side_effect = FileNotFoundError("modprobe")
    with pytest.raises(HostError, match="modprobe command not found"):
        _load_module("kvm")


# ---------------------------------------------------------------------------
# _enable_ip_forward
# ---------------------------------------------------------------------------


@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.get_ip_forward_status", return_value="1")
def test_enable_ip_forward_already_enabled(mock_status, mock_run):
    """_enable_ip_forward should return None and skip sysctl when forwarding is already enabled."""
    result = _enable_ip_forward()
    assert result is None
    mock_run.assert_not_called()


@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.get_ip_forward_status", return_value="0")
def test_enable_ip_forward_needs_enabling(mock_status, mock_run):
    """_enable_ip_forward should call sysctl and return a HostChange when forwarding is disabled."""
    mock_run.return_value = MagicMock(returncode=0)
    result = _enable_ip_forward()
    assert result is not None
    assert result.setting == "net.ipv4.ip_forward"
    assert result.original_value == "0"
    assert result.applied_value == "1"
    assert result.mechanism == "sysctl"
    mock_run.assert_called_once_with(
        ["sysctl", "-w", "net.ipv4.ip_forward=1"],
        capture_output=True,
        text=True,
        check=True,
    )


@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.get_ip_forward_status", return_value="0")
def test_enable_ip_forward_called_process_error(mock_status, mock_run):
    """_enable_ip_forward should raise HostError when the sysctl -w command fails."""
    mock_run.side_effect = subprocess.CalledProcessError(1, "sysctl")
    with pytest.raises(HostError, match="Failed to enable IP forwarding"):
        _enable_ip_forward()


@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.get_ip_forward_status", return_value="0")
def test_enable_ip_forward_file_not_found(mock_status, mock_run):
    """_enable_ip_forward should raise HostError when the sysctl binary is not found."""
    mock_run.side_effect = FileNotFoundError("sysctl")
    with pytest.raises(HostError, match="sysctl command not found"):
        _enable_ip_forward()


# ---------------------------------------------------------------------------
# _persist_sysctl
# ---------------------------------------------------------------------------


@patch("fcm.core.host_setup.SYSCTL_CONF")
def test_persist_sysctl_already_correct(mock_conf):
    """_persist_sysctl should return None when the sysctl conf file already has the correct content."""
    mock_conf.exists.return_value = True
    mock_conf.read_text.return_value = "net.ipv4.ip_forward = 1\n"
    result = _persist_sysctl()
    assert result is None


@patch("fcm.core.host_setup.SYSCTL_CONF")
def test_persist_sysctl_file_does_not_exist(mock_conf):
    """_persist_sysctl should create the conf file and return a HostChange when it does not exist."""
    mock_conf.exists.return_value = False
    mock_conf.parent = MagicMock()
    mock_conf.__str__ = lambda self: "/etc/sysctl.d/firecracker-manager.conf"
    result = _persist_sysctl()
    assert result is not None
    assert result.setting == "sysctl_persist_file"
    assert result.original_value is None
    assert result.applied_value == "/etc/sysctl.d/firecracker-manager.conf"
    assert result.mechanism == "file_create"
    mock_conf.write_text.assert_called_once_with("net.ipv4.ip_forward = 1\n")


@patch("fcm.core.host_setup.SYSCTL_CONF")
def test_persist_sysctl_file_has_wrong_content(mock_conf):
    """_persist_sysctl should overwrite the conf file and return a HostChange when content is wrong."""
    mock_conf.exists.return_value = True
    mock_conf.read_text.return_value = "net.ipv4.ip_forward = 0\n"
    mock_conf.parent = MagicMock()
    mock_conf.__str__ = lambda self: "/etc/sysctl.d/firecracker-manager.conf"
    result = _persist_sysctl()
    assert result is not None
    assert result.original_value == "net.ipv4.ip_forward = 0\n"
    mock_conf.write_text.assert_called_once_with("net.ipv4.ip_forward = 1\n")


@patch("fcm.core.host_setup.SYSCTL_CONF")
def test_persist_sysctl_write_fails(mock_conf):
    """_persist_sysctl should raise HostError when the directory cannot be created."""
    mock_conf.exists.return_value = False
    mock_conf.parent = MagicMock()
    mock_conf.parent.mkdir.side_effect = OSError("permission denied")
    with pytest.raises(HostError, match="Failed to write"):
        _persist_sysctl()


# ---------------------------------------------------------------------------
# _ensure_kvm_modules
# ---------------------------------------------------------------------------


@patch("fcm.core.host_setup._load_module")
@patch("fcm.core.host_setup._is_module_loaded")
def test_ensure_kvm_modules_all_loaded(mock_loaded, mock_load):
    """_ensure_kvm_modules should return no changes and skip modprobe when all modules are loaded."""
    # kvm loaded, kvm_intel loaded
    mock_loaded.side_effect = lambda m: m in ("kvm", "kvm_intel")
    changes = _ensure_kvm_modules()
    assert changes == []
    mock_load.assert_not_called()


@patch("fcm.core.host_setup._load_module")
@patch("fcm.core.host_setup._is_module_loaded")
def test_ensure_kvm_modules_need_loading(mock_loaded, mock_load):
    """_ensure_kvm_modules should load kvm and a vendor module when neither is present."""
    # Nothing loaded initially
    call_count = {"kvm": 0, "kvm_intel": 0, "kvm_amd": 0}

    def loaded_side_effect(m):
        call_count[m] = call_count.get(m, 0) + 1
        return False

    mock_loaded.side_effect = loaded_side_effect
    # First vendor module load succeeds
    changes = _ensure_kvm_modules()
    assert any(c.setting == "module:kvm" for c in changes)
    assert any(c.setting == "module:kvm_intel" for c in changes)


@patch("fcm.core.host_setup._load_module")
@patch("fcm.core.host_setup._is_module_loaded")
def test_ensure_kvm_modules_vendor_fallback(mock_loaded, mock_load):
    """kvm_intel fails, kvm_amd succeeds."""

    def loaded_side_effect(m):
        return False

    mock_loaded.side_effect = loaded_side_effect

    def load_side_effect(m):
        if m == "kvm_intel":
            raise HostError("kvm_intel failed")

    mock_load.side_effect = load_side_effect

    changes = _ensure_kvm_modules()
    # kvm was loaded
    assert any(c.setting == "module:kvm" for c in changes)
    # kvm_amd was loaded (fallback)
    assert any(c.setting == "module:kvm_amd" for c in changes)


@patch("fcm.core.host_setup._load_module")
@patch("fcm.core.host_setup._is_module_loaded")
def test_ensure_kvm_modules_both_vendor_fail(mock_loaded, mock_load):
    """Both kvm_intel and kvm_amd fail — no vendor change recorded."""
    mock_loaded.return_value = False

    def load_side_effect(m):
        if m in ("kvm_intel", "kvm_amd"):
            raise HostError(f"{m} failed")

    mock_load.side_effect = load_side_effect

    changes = _ensure_kvm_modules()
    # kvm loaded but no vendor module
    assert any(c.setting == "module:kvm" for c in changes)
    assert not any(c.setting.startswith("module:kvm_") for c in changes)


@patch("fcm.core.host_setup._load_module")
@patch("fcm.core.host_setup._is_module_loaded")
def test_ensure_kvm_modules_kvm_already_loaded_vendor_not(mock_loaded, mock_load):
    """_ensure_kvm_modules should load only the vendor module when kvm is already loaded."""

    def loaded_side_effect(m):
        return m == "kvm"

    mock_loaded.side_effect = loaded_side_effect

    changes = _ensure_kvm_modules()
    # kvm was already loaded (no change), vendor module loaded
    assert not any(c.setting == "module:kvm" for c in changes)
    assert any(c.setting == "module:kvm_intel" for c in changes)


# ---------------------------------------------------------------------------
# _save_state
# ---------------------------------------------------------------------------


def test_save_state_writes_json(tmp_path):
    """_save_state should create a state.json with an init_timestamp and the provided changes."""
    changes = [
        HostChange(
            setting="net.ipv4.ip_forward",
            original_value="0",
            applied_value="1",
            mechanism="sysctl",
        ),
    ]
    _save_state(tmp_path, changes)
    state_file = tmp_path / "host" / "state.json"
    assert state_file.exists()
    data = json.loads(state_file.read_text())
    assert "init_timestamp" in data
    assert len(data["changes"]) == 1
    assert data["changes"][0]["setting"] == "net.ipv4.ip_forward"


def test_save_state_empty_changes(tmp_path):
    """_save_state should create a valid state.json with an empty changes list."""
    _save_state(tmp_path, [])
    state_file = tmp_path / "host" / "state.json"
    assert state_file.exists()
    data = json.loads(state_file.read_text())
    assert data["changes"] == []


def test_save_state_creates_directories(tmp_path):
    """_save_state should create any missing parent directories before writing the state file."""
    nested = tmp_path / "deep" / "nested"
    _save_state(nested, [])
    state_file = nested / "host" / "state.json"
    assert state_file.exists()


def test_save_state_multiple_changes(tmp_path):
    """_save_state should persist all provided HostChange entries to the state file."""
    changes = [
        HostChange("a", "0", "1", "sysctl"),
        HostChange("b", None, "v", "modprobe"),
    ]
    _save_state(tmp_path, changes)
    data = json.loads((tmp_path / "host" / "state.json").read_text())
    assert len(data["changes"]) == 2


# ---------------------------------------------------------------------------
# init_host
# ---------------------------------------------------------------------------


def _mock_which_all_found(name):
    return "/usr/bin/" + name


def _mock_lsmod_with_kvm():
    result = MagicMock()
    result.returncode = 0
    result.stdout = "kvm                 1234  0\nkvm_intel            567  0\n"
    return result


@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host_setup.os.access", return_value=False)
@patch("fcm.core.host_setup.Path.exists", return_value=False)
def test_init_host_kvm_not_accessible(mock_exists, mock_access, mock_which, mock_run, tmp_path):
    """init_host should raise HostError when /dev/kvm is not accessible."""
    with pytest.raises(HostError, match="/dev/kvm is not accessible"):
        init_host(tmp_path)


@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.shutil.which")
@patch("fcm.core.host_setup.os.access", return_value=True)
@patch("fcm.core.host_setup.Path.exists", return_value=True)
def test_init_host_missing_binaries(mock_exists, mock_access, mock_which, mock_run, tmp_path):
    """init_host should raise HostError listing missing binaries when required tools are absent."""
    mock_which.return_value = None
    with pytest.raises(HostError, match="Missing required binaries"):
        init_host(tmp_path)


@patch("fcm.core.host_setup._get_current_user", return_value="testuser")
@patch("fcm.core.host_setup._add_user_to_group", return_value=False)
@patch("fcm.core.host_setup._create_group", return_value=False)
@patch("fcm.core.host_setup._validate_sudoers_binaries")
@patch("fcm.core.host_setup.SYSCTL_CONF")
@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host_setup.os.access", return_value=True)
def test_init_host_ip_forward_already_enabled(
    mock_access,
    mock_which,
    mock_run,
    mock_sysctl_conf,
    mock_validate,
    mock_create_grp,
    mock_add_user,
    mock_get_user,
    tmp_path,
):
    """init_host should return no changes when IP forwarding and KVM modules are already configured."""
    # Path.exists for /dev/kvm returns True
    with patch("fcm.core.host_setup.Path.exists", return_value=True):
        # sysctl -n returns "1", lsmod returns kvm loaded
        def run_side_effect(cmd, **kwargs):
            if cmd[0] == "sysctl" and "-n" in cmd:
                return MagicMock(stdout="1\n", returncode=0)
            if cmd[0] == "lsmod":
                return _mock_lsmod_with_kvm()
            return MagicMock(returncode=0)

        mock_run.side_effect = run_side_effect

        # Mock SYSCTL_CONF as a Path-like that already has the right content
        mock_sysctl_conf.exists.return_value = True
        mock_sysctl_conf.read_text.return_value = "net.ipv4.ip_forward = 1\n"

        changes = init_host(tmp_path)

    # ip_forward already "1" and sysctl conf already correct and modules loaded
    assert changes == []
    # State file should be written
    state_file = tmp_path / "host" / "state.json"
    assert state_file.exists()


@patch("fcm.core.host_setup._get_current_user", return_value="testuser")
@patch("fcm.core.host_setup._add_user_to_group", return_value=False)
@patch("fcm.core.host_setup._create_group", return_value=False)
@patch("fcm.core.host_setup._validate_sudoers_binaries")
@patch("fcm.core.host_setup.SYSCTL_CONF")
@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host_setup.os.access", return_value=True)
def test_init_host_enables_ip_forward(
    mock_access,
    mock_which,
    mock_run,
    mock_sysctl_conf,
    mock_validate,
    mock_create_grp,
    mock_add_user,
    mock_get_user,
    tmp_path,
):
    """init_host should record ip_forward and sysctl_persist_file changes when forwarding was off."""
    with patch("fcm.core.host_setup.Path.exists", return_value=True):

        def run_side_effect(cmd, **kwargs):
            if cmd[0] == "sysctl" and "-n" in cmd:
                return MagicMock(stdout="0\n", returncode=0)
            if cmd[0] == "sysctl" and "-w" in cmd:
                return MagicMock(returncode=0)
            if cmd[0] == "lsmod":
                return _mock_lsmod_with_kvm()
            return MagicMock(returncode=0)

        mock_run.side_effect = run_side_effect

        # SYSCTL_CONF doesn't exist yet
        mock_sysctl_conf.exists.return_value = False
        mock_sysctl_conf.parent = MagicMock()
        mock_sysctl_conf.__str__ = lambda self: "/etc/sysctl.d/firecracker-manager.conf"

        changes = init_host(tmp_path)

    setting_names = [c.setting for c in changes]
    assert "net.ipv4.ip_forward" in setting_names
    assert "sysctl_persist_file" in setting_names

    # Verify the ip_forward change details
    ip_fwd_change = next(c for c in changes if c.setting == "net.ipv4.ip_forward")
    assert ip_fwd_change.original_value == "0"
    assert ip_fwd_change.applied_value == "1"
    assert ip_fwd_change.mechanism == "sysctl"


@patch("fcm.core.host_setup._get_current_user", return_value="testuser")
@patch("fcm.core.host_setup._add_user_to_group", return_value=False)
@patch("fcm.core.host_setup._create_group", return_value=False)
@patch("fcm.core.host_setup._validate_sudoers_binaries")
@patch("fcm.core.host_setup.SYSCTL_CONF")
@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host_setup.os.access", return_value=True)
def test_init_host_writes_state_file(
    mock_access,
    mock_which,
    mock_run,
    mock_sysctl_conf,
    mock_validate,
    mock_create_grp,
    mock_add_user,
    mock_get_user,
    tmp_path,
):
    """init_host should write a state.json containing init_timestamp and changes fields."""
    with patch("fcm.core.host_setup.Path.exists", return_value=True):

        def run_side_effect(cmd, **kwargs):
            if cmd[0] == "sysctl" and "-n" in cmd:
                return MagicMock(stdout="1\n", returncode=0)
            if cmd[0] == "lsmod":
                return _mock_lsmod_with_kvm()
            return MagicMock(returncode=0)

        mock_run.side_effect = run_side_effect

        mock_sysctl_conf.exists.return_value = True
        mock_sysctl_conf.read_text.return_value = "net.ipv4.ip_forward = 1\n"

        init_host(tmp_path)

    state_file = tmp_path / "host" / "state.json"
    assert state_file.exists()
    data = json.loads(state_file.read_text())
    assert "init_timestamp" in data
    assert "changes" in data


@patch("fcm.core.host_setup._get_current_user", return_value="testuser")
@patch("fcm.core.host_setup._add_user_to_group", return_value=False)
@patch("fcm.core.host_setup._create_group", return_value=False)
@patch("fcm.core.host_setup._validate_sudoers_binaries")
@patch("fcm.core.host_setup.SYSCTL_CONF")
@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host_setup.os.access", return_value=True)
def test_init_host_idempotent(
    mock_access,
    mock_which,
    mock_run,
    mock_sysctl_conf,
    mock_validate,
    mock_create_grp,
    mock_add_user,
    mock_get_user,
    tmp_path,
):
    """init_host should produce fewer changes on the second call when the host is already configured."""
    with patch("fcm.core.host_setup.Path.exists", return_value=True):
        call_count = {"sysctl_n": 0}

        def run_side_effect(cmd, **kwargs):
            if cmd[0] == "sysctl" and "-n" in cmd:
                call_count["sysctl_n"] += 1
                # First call: disabled, second call: already enabled
                if call_count["sysctl_n"] == 1:
                    return MagicMock(stdout="0\n", returncode=0)
                return MagicMock(stdout="1\n", returncode=0)
            if cmd[0] == "sysctl" and "-w" in cmd:
                return MagicMock(returncode=0)
            if cmd[0] == "lsmod":
                return _mock_lsmod_with_kvm()
            return MagicMock(returncode=0)

        mock_run.side_effect = run_side_effect

        mock_sysctl_conf.exists.return_value = False
        mock_sysctl_conf.parent = MagicMock()
        mock_sysctl_conf.__str__ = lambda self: "/etc/sysctl.d/firecracker-manager.conf"

        changes_first = init_host(tmp_path)

        # Second run: ip_forward already "1", sysctl conf now "exists"
        mock_sysctl_conf.exists.return_value = True
        mock_sysctl_conf.read_text.return_value = "net.ipv4.ip_forward = 1\n"

        changes_second = init_host(tmp_path)

    assert len(changes_second) < len(changes_first)


@patch("fcm.core.host_setup._get_current_user", return_value="testuser")
@patch("fcm.core.host_setup._add_user_to_group", return_value=False)
@patch("fcm.core.host_setup._create_group", return_value=False)
@patch("fcm.core.host_setup._validate_sudoers_binaries")
@patch("fcm.core.host_setup.SYSCTL_CONF")
@patch("fcm.core.host_setup.subprocess.run")
@patch("fcm.core.host_setup.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host_setup.os.access", return_value=True)
def test_init_host_with_module_loading(
    mock_access,
    mock_which,
    mock_run,
    mock_sysctl_conf,
    mock_validate,
    mock_create_grp,
    mock_add_user,
    mock_get_user,
    tmp_path,
):
    """init_host loads kvm modules when they're not loaded."""
    with patch("fcm.core.host_setup.Path.exists", return_value=True):

        def run_side_effect(cmd, **kwargs):
            if cmd[0] == "sysctl" and "-n" in cmd:
                return MagicMock(stdout="1\n", returncode=0)
            if cmd[0] == "lsmod":
                # Nothing loaded
                return MagicMock(returncode=0, stdout="Module  Size  Used\n")
            if cmd[0] == "modprobe":
                return MagicMock(returncode=0)
            return MagicMock(returncode=0)

        mock_run.side_effect = run_side_effect

        mock_sysctl_conf.exists.return_value = True
        mock_sysctl_conf.read_text.return_value = "net.ipv4.ip_forward = 1\n"

        changes = init_host(tmp_path)

    module_changes = [c for c in changes if c.mechanism == "modprobe"]
    assert len(module_changes) >= 1


# ---------------------------------------------------------------------------
# get_host_state
# ---------------------------------------------------------------------------


def test_get_host_state_no_file(tmp_path):
    """get_host_state should return None when no state file has been written."""
    result = get_host_state(tmp_path)
    assert result is None


def test_get_host_state_valid(tmp_path):
    """get_host_state should return a HostState object when a valid state file exists."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "net.ipv4.ip_forward",
                "original_value": "0",
                "applied_value": "1",
                "mechanism": "sysctl",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    result = get_host_state(tmp_path)
    assert isinstance(result, HostState)
    assert result.init_timestamp == "2025-01-01T00:00:00+00:00"
    assert len(result.changes) == 1
    assert result.changes[0].setting == "net.ipv4.ip_forward"
    assert result.changes[0].original_value == "0"
    assert result.changes[0].applied_value == "1"
    assert result.changes[0].mechanism == "sysctl"


def test_get_host_state_corrupt_json(tmp_path):
    """get_host_state should raise HostError when the state file contains invalid JSON."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    (state_dir / "state.json").write_text("{bad json")

    with pytest.raises(HostError, match="Corrupt state file"):
        get_host_state(tmp_path)


def test_get_host_state_missing_key(tmp_path):
    """get_host_state should raise HostError when the state file is missing required keys."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    (state_dir / "state.json").write_text(json.dumps({"init_timestamp": "t"}))

    with pytest.raises(HostError, match="Corrupt state file"):
        get_host_state(tmp_path)


def test_get_host_state_empty_changes(tmp_path):
    """get_host_state should return a HostState with an empty changes list when none were recorded."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {"init_timestamp": "2025-01-01T00:00:00+00:00", "changes": []}
    (state_dir / "state.json").write_text(json.dumps(data))

    result = get_host_state(tmp_path)
    assert isinstance(result, HostState)
    assert result.changes == []


def test_get_host_state_type_error(tmp_path):
    """JSON is valid but changes entries have wrong types."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {"init_timestamp": "t", "changes": [{"bad_key": "val"}]}
    (state_dir / "state.json").write_text(json.dumps(data))

    with pytest.raises(HostError, match="Corrupt state file"):
        get_host_state(tmp_path)


def test_get_host_state_multiple_changes(tmp_path):
    """get_host_state should return all HostChange entries when the state file has multiple changes."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "net.ipv4.ip_forward",
                "original_value": "0",
                "applied_value": "1",
                "mechanism": "sysctl",
            },
            {
                "setting": "sysctl_persist_file",
                "original_value": None,
                "applied_value": "/etc/sysctl.d/fc.conf",
                "mechanism": "file_create",
            },
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    result = get_host_state(tmp_path)
    assert result is not None
    assert len(result.changes) == 2


# ---------------------------------------------------------------------------
# restore_host
# ---------------------------------------------------------------------------


def test_restore_host_no_state(tmp_path):
    """restore_host should raise HostError when no state file exists to restore from."""
    with pytest.raises(HostError, match="No saved host state to restore"):
        restore_host(tmp_path)


@patch("fcm.core.host_state.subprocess.run")
def test_restore_host_reverts_sysctl(mock_run, tmp_path):
    """restore_host should revert a sysctl change to its original value and delete the state file."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "net.ipv4.ip_forward",
                "original_value": "0",
                "applied_value": "1",
                "mechanism": "sysctl",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    mock_run.return_value = MagicMock(returncode=0)

    reverted = restore_host(tmp_path)
    assert len(reverted) == 1
    assert reverted[0].setting == "net.ipv4.ip_forward"
    assert reverted[0].original_value == "1"
    assert reverted[0].applied_value == "0"
    assert reverted[0].mechanism == "sysctl"

    mock_run.assert_called_once_with(
        ["sysctl", "-w", "net.ipv4.ip_forward=0"],
        capture_output=True,
        text=True,
        check=True,
    )

    # State file should be deleted
    assert not (state_dir / "state.json").exists()


def test_restore_host_reverts_file_create(tmp_path):
    """restore_host should delete a file that was created during init when original_value is None."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)

    # Create the file that should be removed
    target_file = tmp_path / "test-sysctl.conf"
    target_file.write_text("net.ipv4.ip_forward = 1\n")

    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "sysctl_persist_file",
                "original_value": None,
                "applied_value": str(target_file),
                "mechanism": "file_create",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    # Patch allowlist to include test file path (S-C2 allowlist blocks test paths)
    with patch(
        "fcm.core.host_state.RESTORABLE_FILE_PATHS",
        frozenset({target_file}),
    ):
        reverted = restore_host(tmp_path)
    assert len(reverted) == 1
    assert reverted[0].setting == "sysctl_persist_file"
    assert reverted[0].applied_value == "(removed)"
    assert reverted[0].mechanism == "file_remove"

    # File should be deleted
    assert not target_file.exists()
    # State file should be deleted
    assert not (state_dir / "state.json").exists()


def test_restore_host_reverts_file_create_with_original(tmp_path):
    """restore_host should restore a file to its original content when original_value is set."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)

    target_file = tmp_path / "test-sysctl.conf"
    target_file.write_text("net.ipv4.ip_forward = 1\n")

    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "sysctl_persist_file",
                "original_value": "old content\n",
                "applied_value": str(target_file),
                "mechanism": "file_create",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    # Patch allowlist to include test file path (S-C2 allowlist blocks test paths)
    with patch(
        "fcm.core.host_state.RESTORABLE_FILE_PATHS",
        frozenset({target_file}),
    ):
        reverted = restore_host(tmp_path)
    assert len(reverted) == 1
    # File should be restored to original content, not deleted
    assert target_file.read_text() == "old content\n"


@patch("fcm.core.host_state.subprocess.run")
def test_restore_host_deletes_state_file(mock_run, tmp_path):
    """restore_host should remove the state.json after a successful restore."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [],
    }
    state_file = state_dir / "state.json"
    state_file.write_text(json.dumps(data))

    restore_host(tmp_path)
    assert not state_file.exists()


@patch("fcm.core.host_state.subprocess.run")
def test_restore_host_sysctl_null_original_skipped(mock_run, tmp_path):
    """restore_host should skip reverting a sysctl change when original_value is None."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "net.ipv4.ip_forward",
                "original_value": None,
                "applied_value": "1",
                "mechanism": "sysctl",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    reverted = restore_host(tmp_path)
    # sysctl with None original_value is not reverted
    assert reverted == []
    mock_run.assert_not_called()


@patch("fcm.core.host_state.subprocess.run")
def test_restore_host_sysctl_failure(mock_run, tmp_path):
    """restore_host should raise HostError when the sysctl revert command fails."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "net.ipv4.ip_forward",
                "original_value": "0",
                "applied_value": "1",
                "mechanism": "sysctl",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    mock_run.side_effect = subprocess.CalledProcessError(1, "sysctl")

    with pytest.raises(HostError, match="Failed to revert"):
        restore_host(tmp_path)


@patch("fcm.core.host_state.subprocess.run")
def test_restore_host_sysctl_file_not_found(mock_run, tmp_path):
    """sysctl command not found during restore."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "net.ipv4.ip_forward",
                "original_value": "0",
                "applied_value": "1",
                "mechanism": "sysctl",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    mock_run.side_effect = FileNotFoundError("sysctl")

    with pytest.raises(HostError, match="sysctl command not found"):
        restore_host(tmp_path)


@patch("fcm.core.host_state.subprocess.run")
def test_restore_host_multiple_changes_reversed_order(mock_run, tmp_path):
    """restore_host should revert changes in reverse order so later changes are undone first."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)

    target_file = tmp_path / "test-sysctl.conf"
    target_file.write_text("content")

    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "net.ipv4.ip_forward",
                "original_value": "0",
                "applied_value": "1",
                "mechanism": "sysctl",
            },
            {
                "setting": "sysctl_persist_file",
                "original_value": None,
                "applied_value": str(target_file),
                "mechanism": "file_create",
            },
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))
    mock_run.return_value = MagicMock(returncode=0)

    # Patch allowlist to include test file path (S-C2 allowlist blocks test paths)
    with patch(
        "fcm.core.host_state.RESTORABLE_FILE_PATHS",
        frozenset({target_file}),
    ):
        reverted = restore_host(tmp_path)
    # Changes should be reverted in reverse order
    assert len(reverted) == 2
    # file_create was second, so reverted first
    assert reverted[0].setting == "sysctl_persist_file"
    assert reverted[1].setting == "net.ipv4.ip_forward"


def test_restore_host_file_create_target_missing(tmp_path):
    """file_create target doesn't exist anymore — skip silently."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    target_file = tmp_path / "nonexistent.conf"
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "sysctl_persist_file",
                "original_value": None,
                "applied_value": str(target_file),
                "mechanism": "file_create",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    # Patch allowlist so the test reaches the target.exists() check (not blocked by S-C2)
    with patch(
        "fcm.core.host_state.RESTORABLE_FILE_PATHS",
        frozenset({target_file}),
    ):
        reverted = restore_host(tmp_path)
    # Target file doesn't exist, so nothing to revert
    assert reverted == []


def test_restore_host_file_create_os_error(tmp_path):
    """OS error during file revert raises HostError."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)

    target_file = tmp_path / "test.conf"
    target_file.write_text("content")

    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "sysctl_persist_file",
                "original_value": None,
                "applied_value": str(target_file),
                "mechanism": "file_create",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    # Patch allowlist to include test file path (S-C2 allowlist blocks test paths)
    with patch(
        "fcm.core.host_state.RESTORABLE_FILE_PATHS",
        frozenset({target_file}),
    ):
        with patch("pathlib.Path.unlink", side_effect=OSError("permission denied")):
            with pytest.raises(HostError, match="Failed to revert file"):
                restore_host(tmp_path)


def test_restore_host_modprobe_mechanism_ignored(tmp_path):
    """modprobe mechanism changes are not reverted."""
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "module:kvm",
                "original_value": None,
                "applied_value": "kvm",
                "mechanism": "modprobe",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

    reverted = restore_host(tmp_path)
    assert reverted == []


# ---------------------------------------------------------------------------
# HostChange / HostState dataclass tests
# ---------------------------------------------------------------------------


def test_host_change_dataclass():
    """HostChange should store and expose all four fields correctly."""
    change = HostChange(
        setting="test",
        original_value="old",
        applied_value="new",
        mechanism="sysctl",
    )
    assert change.setting == "test"
    assert change.original_value == "old"
    assert change.applied_value == "new"
    assert change.mechanism == "sysctl"


def test_host_state_dataclass():
    """HostState should store init_timestamp and changes fields correctly."""
    state = HostState(
        init_timestamp="2025-01-01T00:00:00+00:00",
        changes=[],
    )
    assert state.init_timestamp == "2025-01-01T00:00:00+00:00"
    assert state.changes == []


# ---------------------------------------------------------------------------
# check_privileges (moved from test_phase5.py)
# ---------------------------------------------------------------------------


class TestCheckPrivileges:
    @patch("fcm.core.host_privilege.shutil.which", return_value=None)
    @patch("fcm.core.host_privilege.Path.exists", return_value=False)
    def test_binary_not_found(self, mock_exists, mock_which):
        with pytest.raises(PrivilegeError, match="Binary not found"):
            check_privileges("/usr/sbin/nonexistent")

    @patch("fcm.core.host_privilege.shutil.which", return_value="/usr/sbin/ip")
    @patch("fcm.core.host_privilege.os.getuid", return_value=0)
    def test_root_user_passes(self, mock_uid, mock_which):
        check_privileges("/usr/sbin/ip")

    @patch("fcm.core.host_privilege.shutil.which", return_value="/usr/sbin/ip")
    @patch("fcm.core.host_privilege.os.getuid", return_value=1000)
    def test_user_in_group_passes(self, mock_uid, mock_which):
        import grp
        import pwd

        mock_grp_info = MagicMock()
        mock_grp_info.gr_mem = ["testuser"]
        mock_pwd_info = MagicMock()
        mock_pwd_info.pw_name = "testuser"

        with (
            patch.object(grp, "getgrnam", return_value=mock_grp_info),
            patch.object(pwd, "getpwuid", return_value=mock_pwd_info),
        ):
            check_privileges("/usr/sbin/ip")

    @patch("fcm.core.host_privilege.shutil.which", return_value="/usr/sbin/ip")
    @patch("fcm.core.host_privilege.os.getuid", return_value=1000)
    def test_user_not_in_group_fails(self, mock_uid, mock_which):
        import grp
        import pwd

        mock_grp_info = MagicMock()
        mock_grp_info.gr_mem = ["otheruser"]
        mock_pwd_info = MagicMock()
        mock_pwd_info.pw_name = "testuser"

        with (
            patch.object(grp, "getgrnam", return_value=mock_grp_info),
            patch.object(pwd, "getpwuid", return_value=mock_pwd_info),
        ):
            with pytest.raises(PrivilegeError, match="not in the 'fcm' group"):
                check_privileges("/usr/sbin/ip")

    @patch("fcm.core.host_privilege.shutil.which", return_value="/usr/sbin/ip")
    @patch("fcm.core.host_privilege.os.getuid", return_value=1000)
    def test_group_not_exists(self, mock_uid, mock_which):
        import grp

        with patch.object(grp, "getgrnam", side_effect=KeyError("fcm")):
            with pytest.raises(PrivilegeError, match="does not exist"):
                check_privileges("/usr/sbin/ip")


# ---------------------------------------------------------------------------
# Host helper functions (moved from test_phase5.py)
# ---------------------------------------------------------------------------


class TestHostHelpers:
    def test_get_current_user(self):
        import os
        import pwd

        mock_pwd_info = MagicMock()
        mock_pwd_info.pw_name = "testuser"
        with (
            patch.object(os, "getuid", return_value=1000),
            patch.object(pwd, "getpwuid", return_value=mock_pwd_info),
        ):
            assert _get_current_user() == "testuser"

    def test_group_exists_true(self):
        import grp

        with patch.object(grp, "getgrnam", return_value=MagicMock()):
            assert _group_exists("fcm") is True

    def test_group_exists_false(self):
        import grp

        with patch.object(grp, "getgrnam", side_effect=KeyError("fcm")):
            assert _group_exists("fcm") is False

    def test_user_in_group_true(self):
        import grp

        mock_grp = MagicMock()
        mock_grp.gr_mem = ["alice", "bob"]
        with patch.object(grp, "getgrnam", return_value=mock_grp):
            assert _user_in_group("alice", "fcm") is True

    def test_user_in_group_false(self):
        import grp

        mock_grp = MagicMock()
        mock_grp.gr_mem = ["bob"]
        with patch.object(grp, "getgrnam", return_value=mock_grp):
            assert _user_in_group("alice", "fcm") is False

    def test_user_in_group_no_group(self):
        import grp

        with patch.object(grp, "getgrnam", side_effect=KeyError("fcm")):
            assert _user_in_group("alice", "fcm") is False

    @patch("fcm.core.host_privilege._group_exists", return_value=True)
    def test_create_group_already_exists(self, mock_exists):
        assert _create_group("fcm") is False

    @patch("fcm.core.host_privilege._group_exists", return_value=False)
    @patch("fcm.core.host_privilege.subprocess.run")
    def test_create_group_success(self, mock_run, mock_exists):
        assert _create_group("fcm") is True
        mock_run.assert_called_once()

    @patch("fcm.core.host_privilege._group_exists", return_value=False)
    @patch(
        "fcm.core.host_privilege.subprocess.run",
        side_effect=FileNotFoundError("groupadd"),
    )
    def test_create_group_command_not_found(self, mock_run, mock_exists):
        with pytest.raises(HostError, match="groupadd command not found"):
            _create_group("fcm")

    @patch("fcm.core.host_privilege._user_in_group", return_value=True)
    def test_add_user_to_group_already_member(self, mock_in_group):
        assert _add_user_to_group("alice", "fcm") is False

    @patch("fcm.core.host_privilege._user_in_group", return_value=False)
    @patch("fcm.core.host_privilege.subprocess.run")
    def test_add_user_to_group_success(self, mock_run, mock_in_group):
        assert _add_user_to_group("alice", "fcm") is True

    def test_generate_sudoers_content(self):
        content = _generate_sudoers_content("fcm")
        assert "%fcm ALL=(root) NOPASSWD:" in content
        assert "/usr/sbin/ip" in content
        assert "do not edit manually" in content

    @patch("fcm.core.host_privilege.Path.exists", return_value=True)
    def test_validate_sudoers_binaries_all_present(self, mock_exists):
        _validate_sudoers_binaries()  # Should not raise

    @patch("fcm.core.host_privilege.Path.exists", return_value=False)
    def test_validate_sudoers_binaries_missing(self, mock_exists):
        with pytest.raises(HostError, match="Required binary not found"):
            _validate_sudoers_binaries()

    @patch("fcm.core.host_privilege._group_exists", return_value=False)
    def test_remove_group_not_exists(self, mock_exists):
        assert _remove_group("fcm") is False

    @patch("fcm.core.host_privilege._group_exists", return_value=True)
    @patch("fcm.core.host_privilege.subprocess.run")
    def test_remove_group_success(self, mock_run, mock_exists):
        assert _remove_group("fcm") is True


# ---------------------------------------------------------------------------
# clean_host (moved from test_phase5.py)
# ---------------------------------------------------------------------------


class TestCleanHost:
    @patch("fcm.core.network_manager.list_networks", return_value=[])
    def test_clean_host_no_networks(self, mock_list):
        summary = clean_host(MagicMock())
        assert summary == []

    @patch("fcm.core.network_manager.remove_network")
    @patch("fcm.core.network_manager.list_networks")
    def test_clean_host_removes_networks(self, mock_list, mock_remove):
        net = MagicMock()
        net.name = "default"
        net.bridge = "fcm-br0"
        mock_list.return_value = [net]

        summary = clean_host(MagicMock())
        assert len(summary) == 1
        assert "Removed network 'default'" in summary[0]

    @patch(
        "fcm.core.network_manager.remove_network",
        side_effect=NetworkError("bridge teardown failed"),
    )
    @patch("fcm.core.network_manager.list_networks")
    def test_clean_host_handles_network_failure(self, mock_list, mock_remove):
        net = MagicMock()
        net.name = "default"
        net.bridge = "fcm-br0"
        mock_list.return_value = [net]

        summary = clean_host(MagicMock())
        assert "Warning" in summary[0]

    @patch(
        "fcm.core.network_manager.list_networks",
        side_effect=NetworkError("list failed"),
    )
    def test_clean_host_handles_list_failure(self, mock_list):
        summary = clean_host(MagicMock())
        assert summary == []


# ---------------------------------------------------------------------------
# reset_host (moved from test_phase5.py)
# ---------------------------------------------------------------------------


class TestResetHost:
    @patch("fcm.core.host._state_file")
    @patch("fcm.core.host._remove_group", return_value=True)
    @patch("fcm.core.host._remove_sudoers", return_value=True)
    @patch("fcm.core.host.restore_host", return_value=[])
    @patch("fcm.core.host.clean_host", return_value=["Removed network 'default' (bridge: fcm-br0)"])
    def test_reset_host_full(
        self, mock_clean, mock_restore, mock_rm_sudoers, mock_rm_group, mock_state_file
    ):
        mock_sf = MagicMock()
        mock_sf.exists.return_value = True
        mock_state_file.return_value = mock_sf

        summary = reset_host(MagicMock())
        assert any("Removed network" in s for s in summary)
        assert any("sudoers" in s for s in summary)
        assert any("group" in s for s in summary)

    @patch("fcm.core.host._state_file")
    @patch("fcm.core.host._remove_group", return_value=False)
    @patch("fcm.core.host._remove_sudoers", return_value=False)
    @patch("fcm.core.host.restore_host", side_effect=HostError("no state"))
    @patch("fcm.core.host.clean_host", return_value=[])
    def test_reset_host_nothing_to_do(
        self, mock_clean, mock_restore, mock_rm_sudoers, mock_rm_group, mock_state_file
    ):
        mock_sf = MagicMock()
        mock_sf.exists.return_value = False
        mock_state_file.return_value = mock_sf

        summary = reset_host(MagicMock())
        assert summary == []


# ---------------------------------------------------------------------------
# T-H7: Error-path tests for host.py
# ---------------------------------------------------------------------------


class TestInitHostErrorPaths:
    """Error-path tests for init_host / configure_host."""

    @patch("fcm.core.host_setup.os.access", return_value=False)
    @patch("fcm.core.host_setup.Path.exists", return_value=False)
    def test_configure_host_kvm_not_available(self, mock_exists, mock_access, tmp_path):
        """init_host raises HostError when /dev/kvm is not available."""
        with pytest.raises(HostError, match="/dev/kvm is not accessible"):
            init_host(tmp_path)

    @patch("fcm.core.host_setup._get_current_user", return_value="testuser")
    @patch("fcm.core.host_setup._add_user_to_group", return_value=False)
    @patch("fcm.core.host_setup._create_group", return_value=False)
    @patch("fcm.core.host_setup._validate_sudoers_binaries")
    @patch("fcm.core.host_setup.SYSCTL_CONF")
    @patch("fcm.core.host_setup.subprocess.run")
    @patch("fcm.core.host_setup.shutil.which", side_effect=_mock_which_all_found)
    @patch("fcm.core.host_setup.os.access", return_value=True)
    def test_configure_host_ip_forward_sysctl_fails(
        self,
        mock_access,
        mock_which,
        mock_run,
        mock_sysctl_conf,
        mock_validate,
        mock_create_grp,
        mock_add_user,
        mock_get_user,
        tmp_path,
    ):
        """init_host raises HostError when sysctl -w ip_forward=1 fails."""
        with patch("fcm.core.host_setup.Path.exists", return_value=True):

            def run_side_effect(cmd, **kwargs):
                if cmd[0] == "sysctl" and "-n" in cmd:
                    return MagicMock(stdout="0\n", returncode=0)
                if cmd[0] == "sysctl" and "-w" in cmd:
                    raise subprocess.CalledProcessError(1, "sysctl", stderr="permission denied")
                if cmd[0] == "lsmod":
                    return _mock_lsmod_with_kvm()
                return MagicMock(returncode=0)

            mock_run.side_effect = run_side_effect
            mock_sysctl_conf.exists.return_value = False
            mock_sysctl_conf.parent = MagicMock()

            with pytest.raises(HostError, match="Failed to enable IP forwarding"):
                init_host(tmp_path)


class TestCleanHostErrorPaths:
    """Error-path tests for clean_host."""

    @patch("fcm.core.network_manager.list_networks", return_value=[])
    def test_clean_host_bridge_doesnt_exist(self, mock_list):
        """clean_host returns empty summary when no bridges/networks exist."""
        summary = clean_host(MagicMock())
        assert summary == []
        mock_list.assert_called_once()

    @patch("fcm.core.network_manager.remove_network")
    @patch("fcm.core.network_manager.list_networks")
    def test_clean_host_remove_network_subprocess_error(self, mock_list, mock_remove):
        net = MagicMock()
        net.name = "stale-net"
        net.bridge = "fcm-br99"
        mock_list.return_value = [net]
        mock_remove.side_effect = NetworkError("ip link delete failed")

        summary = clean_host(MagicMock())
        assert len(summary) == 1
        assert "Warning" in summary[0]
        assert "stale-net" in summary[0]


class TestRestoreHostErrorPaths:
    """Error-path tests for restore_host."""

    def test_restore_host_state_file_missing(self, tmp_path):
        """restore_host raises HostError when no state snapshot exists."""
        with pytest.raises(HostError, match="No saved host state to restore"):
            restore_host(tmp_path)

    def test_restore_host_state_file_corrupt_json(self, tmp_path):
        """restore_host raises HostError when state file has invalid JSON."""
        state_dir = tmp_path / "host"
        state_dir.mkdir(parents=True)
        (state_dir / "state.json").write_text("{{not valid json!!")

        with pytest.raises(HostError, match="Corrupt state file"):
            restore_host(tmp_path)

    def test_restore_host_state_file_missing_keys(self, tmp_path):
        """restore_host raises HostError when state file has valid JSON but missing required keys."""
        state_dir = tmp_path / "host"
        state_dir.mkdir(parents=True)
        (state_dir / "state.json").write_text(json.dumps({"foo": "bar"}))

        with pytest.raises(HostError, match="Corrupt state file"):
            restore_host(tmp_path)

    def test_restore_host_state_file_wrong_change_schema(self, tmp_path):
        """restore_host raises HostError when changes have unexpected field names."""
        state_dir = tmp_path / "host"
        state_dir.mkdir(parents=True)
        data = {
            "init_timestamp": "2025-01-01T00:00:00+00:00",
            "changes": [{"wrong_field": "value"}],
        }
        (state_dir / "state.json").write_text(json.dumps(data))

        with pytest.raises(HostError, match="Corrupt state file"):
            restore_host(tmp_path)

    def test_restore_host_state_file_empty_object(self, tmp_path):
        """restore_host raises HostError when state file is an empty JSON object."""
        state_dir = tmp_path / "host"
        state_dir.mkdir(parents=True)
        (state_dir / "state.json").write_text("{}")

        with pytest.raises(HostError, match="Corrupt state file"):
            restore_host(tmp_path)


class TestCheckRequiredBinariesErrorPaths:
    """Error-path tests for check_required_binaries."""

    @patch("fcm.core.host_setup.shutil.which")
    def test_single_required_binary_missing(self, mock_which):
        """check_required_binaries detects when only 'iptables' is missing."""

        def side_effect(name):
            if name == "iptables":
                return None
            return "/usr/bin/" + name

        mock_which.side_effect = side_effect
        result = check_required_binaries()
        assert "iptables" in result
        assert "ip" not in result
        assert "qemu-img" not in result

    @patch("fcm.core.host_setup.shutil.which")
    def test_qemu_img_missing(self, mock_which):
        """check_required_binaries detects when only 'qemu-img' is missing."""

        def side_effect(name):
            if name == "qemu-img":
                return None
            return "/usr/bin/" + name

        mock_which.side_effect = side_effect
        result = check_required_binaries()
        assert "qemu-img" in result
        assert "ip" not in result

    @patch("fcm.core.host_setup.shutil.which", return_value=None)
    def test_all_binaries_missing(self, mock_which):
        """check_required_binaries reports all binaries when none are found."""
        result = check_required_binaries()
        assert "ip" in result
        assert "iptables" in result
        assert "qemu-img" in result
        assert "mkisofs or genisoimage" in result
        assert len(result) == 4

    @patch("fcm.core.host_setup.shutil.which")
    def test_only_iso_binaries_missing(self, mock_which):
        """check_required_binaries detects when both ISO tools are missing."""

        def side_effect(name):
            if name in ("mkisofs", "genisoimage"):
                return None
            return "/usr/bin/" + name

        mock_which.side_effect = side_effect
        result = check_required_binaries()
        assert result == ["mkisofs or genisoimage"]


class TestGetIpForwardErrorPaths:
    """Error-path tests for get_ip_forward_status."""

    @patch("fcm.core.host_setup.subprocess.run")
    def test_sysctl_command_not_found(self, mock_run):
        mock_run.side_effect = FileNotFoundError("sysctl")
        with pytest.raises(HostError, match="sysctl command not found"):
            get_ip_forward_status()

    @patch("fcm.core.host_setup.subprocess.run")
    def test_sysctl_returns_error(self, mock_run):
        mock_run.side_effect = subprocess.CalledProcessError(1, "sysctl", stderr="unknown key")
        with pytest.raises(HostError, match="Failed to read"):
            get_ip_forward_status()


class TestWriteSudoersErrorPaths:
    @patch("fcm.core.host_privilege.subprocess.run")
    def test_visudo_failure(self, mock_run, tmp_path):
        """_write_sudoers raises HostError if visudo validation fails."""
        mock_run.return_value = MagicMock(returncode=1, stderr="syntax error")
        from fcm.core.host_privilege import _write_sudoers

        with pytest.raises(HostError, match="Generated sudoers file failed visudo validation"):
            _write_sudoers(tmp_path / "sudoers", "testgrp")


class TestAddUserToGroupErrorPaths:
    @patch("fcm.core.host_privilege._user_in_group", return_value=False)
    @patch("fcm.core.host_privilege.subprocess.run")
    def test_usermod_failure(self, mock_run, mock_in_group):
        """_add_user_to_group raises HostError if usermod fails."""
        mock_run.side_effect = subprocess.CalledProcessError(1, "usermod", stderr="user not found")
        with pytest.raises(HostError, match="Failed to add usr to group grp"):
            _add_user_to_group("usr", "grp")

    @patch("fcm.core.host_privilege._user_in_group", return_value=False)
    @patch("fcm.core.host_privilege.subprocess.run", side_effect=FileNotFoundError("usermod"))
    def test_usermod_not_found(self, mock_run, mock_in_group):
        """_add_user_to_group raises HostError if usermod is missing."""
        with pytest.raises(HostError, match="usermod command not found"):
            _add_user_to_group("usr", "grp")


class TestPruneHostErrorPaths:
    @patch("fcm.core.host.clean_host", return_value=["cleaned"])
    @patch("fcm.core.host.restore_host")
    @patch("fcm.core.host._state_file")
    def test_prune_host_restore_fails(self, mock_state_file, mock_restore, mock_clean, tmp_path):
        """prune_host catches HostError from restore_host but still returns clean summary and removes state."""
        mock_restore.side_effect = HostError("fake restore error")
        mock_sf = MagicMock()
        mock_sf.exists.return_value = True
        mock_state_file.return_value = mock_sf

        from fcm.core.host import prune_host

        summary = prune_host(tmp_path)

        assert "cleaned" in summary
        assert "Removed host state snapshot" in summary
        mock_sf.unlink.assert_called_once()


class TestResetHostErrorPaths:
    @patch("fcm.core.host.clean_host", return_value=["cleaned"])
    @patch("fcm.core.host.restore_host", side_effect=HostError("fake restore error"))
    @patch("fcm.core.host._remove_sudoers", side_effect=HostError("fake sudoers error"))
    @patch("fcm.core.host._remove_group", side_effect=HostError("fake group error"))
    @patch("fcm.core.host._state_file")
    def test_reset_host_all_errors(
        self, mock_state_file, mock_rg, mock_rs, mock_rh, mock_ch, tmp_path
    ):
        """reset_host catches all intermediary HostErrors and still returns a summary."""
        mock_sf = MagicMock()
        mock_sf.exists.return_value = True
        mock_state_file.return_value = mock_sf

        from fcm.core.host import reset_host

        summary = reset_host(tmp_path)

        assert "cleaned" in summary
        assert "Warning: fake sudoers error" in summary
        assert "Warning: fake group error" in summary
        assert "Removed host state snapshot" in summary
        mock_sf.unlink.assert_called_once()
