"""Tests for core/host.py."""

import json
import subprocess
from unittest.mock import MagicMock, patch

import pytest

from fcm.core.host import (
    HostChange,
    HostState,
    _enable_ip_forward,
    _ensure_kvm_modules,
    _is_module_loaded,
    _load_module,
    _persist_sysctl,
    _save_state,
    _state_dir,
    _state_file,
    check_kvm_access,
    check_required_binaries,
    get_host_state,
    get_ip_forward_status,
    init_host,
    restore_host,
)
from fcm.exceptions import HostError


# ---------------------------------------------------------------------------
# _state_dir / _state_file helpers
# ---------------------------------------------------------------------------


def test_state_dir(tmp_path):
    result = _state_dir(tmp_path)
    assert result == tmp_path / "host"


def test_state_file(tmp_path):
    result = _state_file(tmp_path)
    assert result == tmp_path / "host" / "state.json"


# ---------------------------------------------------------------------------
# check_kvm_access
# ---------------------------------------------------------------------------


@patch("fcm.core.host.os.access", return_value=True)
@patch("fcm.core.host.Path.exists", return_value=True)
def test_check_kvm_access_ok(mock_exists, mock_access):
    assert check_kvm_access() is True


@patch("fcm.core.host.Path.exists", return_value=False)
def test_check_kvm_access_missing(mock_exists):
    assert check_kvm_access() is False


@patch("fcm.core.host.os.access", return_value=False)
@patch("fcm.core.host.Path.exists", return_value=True)
def test_check_kvm_access_no_permission(mock_exists, mock_access):
    assert check_kvm_access() is False


# ---------------------------------------------------------------------------
# check_required_binaries
# ---------------------------------------------------------------------------


@patch("fcm.core.host.shutil.which")
def test_check_required_binaries_all_found(mock_which):
    mock_which.return_value = "/usr/bin/something"
    result = check_required_binaries()
    assert result == []


@patch("fcm.core.host.shutil.which")
def test_check_required_binaries_missing_some(mock_which):
    def side_effect(name):
        if name == "ip":
            return None
        return "/usr/bin/" + name

    mock_which.side_effect = side_effect
    result = check_required_binaries()
    assert "ip" in result


@patch("fcm.core.host.shutil.which")
def test_check_required_binaries_no_iso_tool(mock_which):
    def side_effect(name):
        if name in ("mkisofs", "genisoimage"):
            return None
        return "/usr/bin/" + name

    mock_which.side_effect = side_effect
    result = check_required_binaries()
    assert "mkisofs or genisoimage" in result


@patch("fcm.core.host.shutil.which")
def test_check_required_binaries_has_genisoimage_only(mock_which):
    def side_effect(name):
        if name == "mkisofs":
            return None
        return "/usr/bin/" + name

    mock_which.side_effect = side_effect
    result = check_required_binaries()
    assert result == []


@patch("fcm.core.host.shutil.which")
def test_check_required_binaries_has_mkisofs_only(mock_which):
    def side_effect(name):
        if name == "genisoimage":
            return None
        return "/usr/bin/" + name

    mock_which.side_effect = side_effect
    result = check_required_binaries()
    assert result == []


@patch("fcm.core.host.shutil.which")
def test_check_required_binaries_all_missing(mock_which):
    mock_which.return_value = None
    result = check_required_binaries()
    assert "ip" in result
    assert "iptables" in result
    assert "qemu-img" in result
    assert "mkisofs or genisoimage" in result


# ---------------------------------------------------------------------------
# get_ip_forward_status
# ---------------------------------------------------------------------------


@patch("fcm.core.host.subprocess.run")
def test_get_ip_forward_status_success(mock_run):
    mock_run.return_value = MagicMock(stdout="1\n")
    result = get_ip_forward_status()
    assert result == "1"
    mock_run.assert_called_once_with(
        ["sysctl", "-n", "net.ipv4.ip_forward"],
        capture_output=True,
        text=True,
        check=True,
    )


@patch("fcm.core.host.subprocess.run")
def test_get_ip_forward_status_zero(mock_run):
    mock_run.return_value = MagicMock(stdout="0\n")
    assert get_ip_forward_status() == "0"


@patch("fcm.core.host.subprocess.run")
def test_get_ip_forward_status_called_process_error(mock_run):
    mock_run.side_effect = subprocess.CalledProcessError(1, "sysctl")
    with pytest.raises(HostError, match="Failed to read"):
        get_ip_forward_status()


@patch("fcm.core.host.subprocess.run")
def test_get_ip_forward_status_file_not_found(mock_run):
    mock_run.side_effect = FileNotFoundError("sysctl")
    with pytest.raises(HostError, match="sysctl command not found"):
        get_ip_forward_status()


# ---------------------------------------------------------------------------
# _is_module_loaded
# ---------------------------------------------------------------------------


@patch("fcm.core.host.subprocess.run")
def test_is_module_loaded_found(mock_run):
    mock_run.return_value = MagicMock(
        returncode=0,
        stdout="kvm                   1234  0\nkvm_intel              567  0\n",
    )
    assert _is_module_loaded("kvm") is True


@patch("fcm.core.host.subprocess.run")
def test_is_module_loaded_not_found(mock_run):
    mock_run.return_value = MagicMock(
        returncode=0,
        stdout="ext4                  1234  1\n",
    )
    assert _is_module_loaded("kvm") is False


@patch("fcm.core.host.subprocess.run")
def test_is_module_loaded_lsmod_fails(mock_run):
    mock_run.return_value = MagicMock(returncode=1, stdout="")
    assert _is_module_loaded("kvm") is False


@patch("fcm.core.host.subprocess.run")
def test_is_module_loaded_empty_output(mock_run):
    mock_run.return_value = MagicMock(returncode=0, stdout="")
    assert _is_module_loaded("kvm") is False


@patch("fcm.core.host.subprocess.run")
def test_is_module_loaded_partial_name_no_match(mock_run):
    """Module name 'kvm_intel' should not match query for 'kvm'."""
    mock_run.return_value = MagicMock(
        returncode=0,
        stdout="kvm_intel              567  0\n",
    )
    assert _is_module_loaded("kvm") is False


@patch("fcm.core.host.subprocess.run")
def test_is_module_loaded_empty_lines(mock_run):
    mock_run.return_value = MagicMock(
        returncode=0,
        stdout="\n\nkvm  1234  0\n\n",
    )
    assert _is_module_loaded("kvm") is True


# ---------------------------------------------------------------------------
# _load_module
# ---------------------------------------------------------------------------


@patch("fcm.core.host.subprocess.run")
def test_load_module_success(mock_run):
    mock_run.return_value = MagicMock(returncode=0)
    _load_module("kvm")  # Should not raise
    mock_run.assert_called_once_with(
        ["modprobe", "kvm"],
        capture_output=True,
        text=True,
        check=True,
    )


@patch("fcm.core.host.subprocess.run")
def test_load_module_called_process_error(mock_run):
    mock_run.side_effect = subprocess.CalledProcessError(1, "modprobe")
    with pytest.raises(HostError, match="Failed to load kernel module kvm"):
        _load_module("kvm")


@patch("fcm.core.host.subprocess.run")
def test_load_module_file_not_found(mock_run):
    mock_run.side_effect = FileNotFoundError("modprobe")
    with pytest.raises(HostError, match="modprobe command not found"):
        _load_module("kvm")


# ---------------------------------------------------------------------------
# _enable_ip_forward
# ---------------------------------------------------------------------------


@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.get_ip_forward_status", return_value="1")
def test_enable_ip_forward_already_enabled(mock_status, mock_run):
    result = _enable_ip_forward()
    assert result is None
    mock_run.assert_not_called()


@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.get_ip_forward_status", return_value="0")
def test_enable_ip_forward_needs_enabling(mock_status, mock_run):
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


@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.get_ip_forward_status", return_value="0")
def test_enable_ip_forward_called_process_error(mock_status, mock_run):
    mock_run.side_effect = subprocess.CalledProcessError(1, "sysctl")
    with pytest.raises(HostError, match="Failed to enable IP forwarding"):
        _enable_ip_forward()


@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.get_ip_forward_status", return_value="0")
def test_enable_ip_forward_file_not_found(mock_status, mock_run):
    mock_run.side_effect = FileNotFoundError("sysctl")
    with pytest.raises(HostError, match="sysctl command not found"):
        _enable_ip_forward()


# ---------------------------------------------------------------------------
# _persist_sysctl
# ---------------------------------------------------------------------------


@patch("fcm.core.host.SYSCTL_CONF")
def test_persist_sysctl_already_correct(mock_conf):
    mock_conf.exists.return_value = True
    mock_conf.read_text.return_value = "net.ipv4.ip_forward = 1\n"
    result = _persist_sysctl()
    assert result is None


@patch("fcm.core.host.SYSCTL_CONF")
def test_persist_sysctl_file_does_not_exist(mock_conf):
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


@patch("fcm.core.host.SYSCTL_CONF")
def test_persist_sysctl_file_has_wrong_content(mock_conf):
    mock_conf.exists.return_value = True
    mock_conf.read_text.return_value = "net.ipv4.ip_forward = 0\n"
    mock_conf.parent = MagicMock()
    mock_conf.__str__ = lambda self: "/etc/sysctl.d/firecracker-manager.conf"
    result = _persist_sysctl()
    assert result is not None
    assert result.original_value == "net.ipv4.ip_forward = 0\n"
    mock_conf.write_text.assert_called_once_with("net.ipv4.ip_forward = 1\n")


@patch("fcm.core.host.SYSCTL_CONF")
def test_persist_sysctl_write_fails(mock_conf):
    mock_conf.exists.return_value = False
    mock_conf.parent = MagicMock()
    mock_conf.parent.mkdir.side_effect = OSError("permission denied")
    with pytest.raises(HostError, match="Failed to write"):
        _persist_sysctl()


# ---------------------------------------------------------------------------
# _ensure_kvm_modules
# ---------------------------------------------------------------------------


@patch("fcm.core.host._load_module")
@patch("fcm.core.host._is_module_loaded")
def test_ensure_kvm_modules_all_loaded(mock_loaded, mock_load):
    # kvm loaded, kvm_intel loaded
    mock_loaded.side_effect = lambda m: m in ("kvm", "kvm_intel")
    changes = _ensure_kvm_modules()
    assert changes == []
    mock_load.assert_not_called()


@patch("fcm.core.host._load_module")
@patch("fcm.core.host._is_module_loaded")
def test_ensure_kvm_modules_need_loading(mock_loaded, mock_load):
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


@patch("fcm.core.host._load_module")
@patch("fcm.core.host._is_module_loaded")
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


@patch("fcm.core.host._load_module")
@patch("fcm.core.host._is_module_loaded")
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


@patch("fcm.core.host._load_module")
@patch("fcm.core.host._is_module_loaded")
def test_ensure_kvm_modules_kvm_already_loaded_vendor_not(mock_loaded, mock_load):
    """kvm already loaded, vendor not loaded."""

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
    _save_state(tmp_path, [])
    state_file = tmp_path / "host" / "state.json"
    assert state_file.exists()
    data = json.loads(state_file.read_text())
    assert data["changes"] == []


def test_save_state_creates_directories(tmp_path):
    nested = tmp_path / "deep" / "nested"
    _save_state(nested, [])
    state_file = nested / "host" / "state.json"
    assert state_file.exists()


def test_save_state_multiple_changes(tmp_path):
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


@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host.os.access", return_value=False)
@patch("fcm.core.host.Path.exists", return_value=False)
def test_init_host_kvm_not_accessible(mock_exists, mock_access, mock_which, mock_run, tmp_path):
    with pytest.raises(HostError, match="/dev/kvm is not accessible"):
        init_host(tmp_path)


@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.shutil.which")
@patch("fcm.core.host.os.access", return_value=True)
@patch("fcm.core.host.Path.exists", return_value=True)
def test_init_host_missing_binaries(mock_exists, mock_access, mock_which, mock_run, tmp_path):
    mock_which.return_value = None
    with pytest.raises(HostError, match="Missing required binaries"):
        init_host(tmp_path)


@patch("fcm.core.host.SYSCTL_CONF")
@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host.os.access", return_value=True)
def test_init_host_ip_forward_already_enabled(
    mock_access, mock_which, mock_run, mock_sysctl_conf, tmp_path
):
    # Path.exists for /dev/kvm returns True
    with patch("fcm.core.host.Path.exists", return_value=True):
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


@patch("fcm.core.host.SYSCTL_CONF")
@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host.os.access", return_value=True)
def test_init_host_enables_ip_forward(
    mock_access, mock_which, mock_run, mock_sysctl_conf, tmp_path
):
    with patch("fcm.core.host.Path.exists", return_value=True):

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


@patch("fcm.core.host.SYSCTL_CONF")
@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host.os.access", return_value=True)
def test_init_host_writes_state_file(mock_access, mock_which, mock_run, mock_sysctl_conf, tmp_path):
    with patch("fcm.core.host.Path.exists", return_value=True):

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


@patch("fcm.core.host.SYSCTL_CONF")
@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host.os.access", return_value=True)
def test_init_host_idempotent(mock_access, mock_which, mock_run, mock_sysctl_conf, tmp_path):
    with patch("fcm.core.host.Path.exists", return_value=True):
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


@patch("fcm.core.host.SYSCTL_CONF")
@patch("fcm.core.host.subprocess.run")
@patch("fcm.core.host.shutil.which", side_effect=_mock_which_all_found)
@patch("fcm.core.host.os.access", return_value=True)
def test_init_host_with_module_loading(
    mock_access, mock_which, mock_run, mock_sysctl_conf, tmp_path
):
    """init_host loads kvm modules when they're not loaded."""
    with patch("fcm.core.host.Path.exists", return_value=True):

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
    result = get_host_state(tmp_path)
    assert result is None


def test_get_host_state_valid(tmp_path):
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
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    (state_dir / "state.json").write_text("{bad json")

    with pytest.raises(HostError, match="Corrupt state file"):
        get_host_state(tmp_path)


def test_get_host_state_missing_key(tmp_path):
    state_dir = tmp_path / "host"
    state_dir.mkdir(parents=True)
    (state_dir / "state.json").write_text(json.dumps({"init_timestamp": "t"}))

    with pytest.raises(HostError, match="Corrupt state file"):
        get_host_state(tmp_path)


def test_get_host_state_empty_changes(tmp_path):
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
    with pytest.raises(HostError, match="No saved host state to restore"):
        restore_host(tmp_path)


@patch("fcm.core.host.subprocess.run")
def test_restore_host_reverts_sysctl(mock_run, tmp_path):
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

    reverted = restore_host(tmp_path)
    assert len(reverted) == 1
    # File should be restored to original content, not deleted
    assert target_file.read_text() == "old content\n"


@patch("fcm.core.host.subprocess.run")
def test_restore_host_deletes_state_file(mock_run, tmp_path):
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


@patch("fcm.core.host.subprocess.run")
def test_restore_host_sysctl_null_original_skipped(mock_run, tmp_path):
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


@patch("fcm.core.host.subprocess.run")
def test_restore_host_sysctl_failure(mock_run, tmp_path):
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


@patch("fcm.core.host.subprocess.run")
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


@patch("fcm.core.host.subprocess.run")
def test_restore_host_multiple_changes_reversed_order(mock_run, tmp_path):
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
    data = {
        "init_timestamp": "2025-01-01T00:00:00+00:00",
        "changes": [
            {
                "setting": "sysctl_persist_file",
                "original_value": None,
                "applied_value": str(tmp_path / "nonexistent.conf"),
                "mechanism": "file_create",
            }
        ],
    }
    (state_dir / "state.json").write_text(json.dumps(data))

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

    with patch("fcm.core.host.Path.unlink", side_effect=OSError("permission denied")):
        # The Path object used in restore is created from change.applied_value,
        # so we need to patch Path objects' unlink
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
    state = HostState(
        init_timestamp="2025-01-01T00:00:00+00:00",
        changes=[],
    )
    assert state.init_timestamp == "2025-01-01T00:00:00+00:00"
    assert state.changes == []
