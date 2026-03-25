import json
from pathlib import Path
from unittest.mock import patch, MagicMock

import pytest

from mvmctl.core.ssh import (
    find_ssh_keys,
    extract_ip_from_config,
    build_ssh_command,
    connect_to_vm,
    _validate_ssh_username,
)
from mvmctl.exceptions import VMNotFoundError, MVMKeyError, MVMError
from mvmctl.models.vm import VMInstance, VMState


def test_find_ssh_keys_empty_dir(tmp_path: Path):
    """Empty directory returns no keys."""
    keys_dir = tmp_path / "keys"
    keys_dir.mkdir()
    assert find_ssh_keys(keys_dir) == []


def test_find_ssh_keys_no_dir(tmp_path: Path):
    """Non-existent directory returns no keys."""
    keys_dir = tmp_path / "nonexistent"
    assert find_ssh_keys(keys_dir) == []


def test_find_ssh_keys_finds_private(tmp_path: Path):
    """Only private keys returned, not .pub files."""
    keys_dir = tmp_path / "keys"
    keys_dir.mkdir()
    (keys_dir / "id_rsa").write_text("private")
    (keys_dir / "id_rsa.pub").write_text("public")

    result = find_ssh_keys(keys_dir)
    assert len(result) == 1
    assert result[0].name == "id_rsa"


def test_find_ssh_keys_multiple(tmp_path: Path):
    """Multiple private keys are all returned."""
    keys_dir = tmp_path / "keys"
    keys_dir.mkdir()
    (keys_dir / "id_rsa").write_text("private-rsa")
    (keys_dir / "id_ed25519").write_text("private-ed25519")

    result = find_ssh_keys(keys_dir)
    assert len(result) == 2
    names = sorted(k.name for k in result)
    assert names == ["id_ed25519", "id_rsa"]


def test_extract_ip_from_config_valid(tmp_path: Path):
    """Extracts IP from valid Firecracker JSON config."""
    config = {
        "boot-source": {
            "kernel_image_path": "vmlinux",
            "boot_args": "console=ttyS0 ip=10.20.0.2::10.20.0.1:255.255.255.0::eth0:off",
        }
    }
    config_path = tmp_path / "firecracker.json"
    config_path.write_text(json.dumps(config))

    assert extract_ip_from_config(config_path) == "10.20.0.2"


def test_extract_ip_from_config_no_ip(tmp_path: Path):
    """Returns None when boot_args has no ip= parameter."""
    config = {
        "boot-source": {
            "kernel_image_path": "vmlinux",
            "boot_args": "console=ttyS0 reboot=k panic=1",
        }
    }
    config_path = tmp_path / "firecracker.json"
    config_path.write_text(json.dumps(config))

    assert extract_ip_from_config(config_path) is None


def test_extract_ip_from_config_missing_file(tmp_path: Path):
    """Returns None for non-existent config file."""
    config_path = tmp_path / "nonexistent.json"
    assert extract_ip_from_config(config_path) is None


def test_extract_ip_from_config_invalid_json(tmp_path: Path):
    """Returns None for file with invalid JSON."""
    config_path = tmp_path / "bad.json"
    config_path.write_text("{not valid json!!!")

    assert extract_ip_from_config(config_path) is None


def test_build_ssh_command_basic():
    """Basic command has ssh, strict-host options, and user@ip."""
    cmd = build_ssh_command("10.20.0.2")
    assert cmd == [
        "ssh",
        "-o",
        "StrictHostKeyChecking=no",
        "-o",
        "UserKnownHostsFile=/dev/null",
        "root@10.20.0.2",
    ]


def test_build_ssh_command_with_key(tmp_path: Path):
    """Includes -i KEY_PATH when key exists."""
    key = tmp_path / "id_rsa"
    key.write_text("private")

    cmd = build_ssh_command("10.20.0.2", key_path=key)
    assert "-i" in cmd
    assert str(key) in cmd


def test_build_ssh_command_with_command():
    """Appends command at end."""
    cmd = build_ssh_command("10.20.0.2", command="echo hi")
    assert cmd[-1] == "echo hi"


def test_build_ssh_command_custom_user():
    """Uses provided user instead of root."""
    cmd = build_ssh_command("10.20.0.2", user="ubuntu")
    assert "ubuntu@10.20.0.2" in cmd


@patch("mvmctl.core.ssh.run_ssh", return_value=0)
@patch("mvmctl.core.ssh.find_ssh_keys")
def test_connect_to_vm_by_ip(mock_find_keys: MagicMock, mock_run_ssh: MagicMock, tmp_path: Path):
    """Connect by IP: uses find_ssh_keys and calls run_ssh."""
    key = tmp_path / "id_rsa"
    key.write_text("private")
    key.chmod(0o600)
    mock_find_keys.return_value = [key]

    result = connect_to_vm("10.20.0.5", exec_mode=False, command="echo hi")

    assert result == 0
    mock_find_keys.assert_called_once()
    mock_run_ssh.assert_called_once_with("10.20.0.5", "root", key, "echo hi")


@patch("mvmctl.core.ssh.run_ssh", return_value=0)
@patch("mvmctl.core.ssh.find_ssh_keys")
@patch("mvmctl.core.ssh.VMManager")
def test_connect_to_vm_by_name(
    mock_vm_manager_cls: MagicMock,
    mock_find_keys: MagicMock,
    mock_run_ssh: MagicMock,
    tmp_path: Path,
):
    """Connect by name: looks up VM, uses its IP."""
    vm = VMInstance(name="myvm", ip="10.20.0.3", status=VMState.RUNNING)
    mock_manager = MagicMock()
    mock_manager.get.return_value = vm
    mock_vm_manager_cls.return_value = mock_manager

    key = tmp_path / "id_rsa"
    key.write_text("private")
    key.chmod(0o600)
    mock_find_keys.return_value = [key]

    result = connect_to_vm("myvm", exec_mode=False, command="echo hi")

    assert result == 0
    mock_manager.get.assert_called_once_with("myvm")
    mock_run_ssh.assert_called_once_with("10.20.0.3", "root", key, "echo hi")


@patch("mvmctl.core.ssh.VMManager")
def test_connect_to_vm_name_not_found(mock_vm_manager_cls: MagicMock):
    """Raises VMNotFoundError when VM name not found."""
    mock_manager = MagicMock()
    mock_manager.get.return_value = None
    mock_vm_manager_cls.return_value = mock_manager

    with pytest.raises(VMNotFoundError, match="not found"):
        connect_to_vm("nonexistent", exec_mode=False)


@patch("mvmctl.core.ssh.find_ssh_keys", return_value=[])
def test_connect_to_vm_no_keys(mock_find_keys: MagicMock):
    """Raises MVMKeyError when no SSH keys found."""
    with pytest.raises(MVMKeyError, match="No SSH keys found"):
        connect_to_vm("10.20.0.5", exec_mode=False)

    mock_find_keys.assert_called_once()


# ---------------------------------------------------------------------------
# SSH username validation (S-M7)
# ---------------------------------------------------------------------------


def test_validate_ssh_username_valid():
    """Valid POSIX usernames should not raise."""
    for name in ("root", "ubuntu", "_svc", "user-1", "a_b_c"):
        _validate_ssh_username(name)


def test_validate_ssh_username_invalid():
    """Invalid usernames should raise MVMError."""
    for name in ("Root", "user name", "1start", "user@host", "$(whoami)", "a;b", ""):
        with pytest.raises(MVMError, match="Invalid SSH username"):
            _validate_ssh_username(name)


def test_build_ssh_command_rejects_bad_username():
    """build_ssh_command should reject invalid usernames."""
    with pytest.raises(MVMError, match="Invalid SSH username"):
        build_ssh_command("10.20.0.2", user="$(whoami)")


# ---------------------------------------------------------------------------
# run_ssh and exec_ssh coverage (3e)
# ---------------------------------------------------------------------------

from mvmctl.core.ssh import run_ssh, exec_ssh  # noqa: E402


@patch("mvmctl.core.ssh.subprocess.run")
def test_run_ssh_success(mock_run):
    """run_ssh calls subprocess.run successfully."""
    mock_run.return_value = MagicMock(returncode=0)
    result = run_ssh("10.0.0.1", "root", Path("key"), "uptime")
    assert result == 0
    mock_run.assert_called_once()
    assert mock_run.call_args[0][0][0] == "ssh"


@patch("mvmctl.core.ssh.subprocess.run")
def test_run_ssh_failure(mock_run):
    """run_ssh returns exit code on failure."""
    mock_run.return_value = MagicMock(returncode=1)
    assert run_ssh("10.0.0.1", "root", Path("key"), "bad_cmd") == 1


@patch("mvmctl.core.ssh.os.execvp")
def test_exec_ssh(mock_execvp):
    """exec_ssh calls os.execvp with the correct arguments."""
    exec_ssh("10.0.0.1", "root", Path("key"))
    mock_execvp.assert_called_once()
    assert mock_execvp.call_args[0][0] == "ssh"
    assert "root@10.0.0.1" in mock_execvp.call_args[0][1]


@patch("mvmctl.core.ssh.os.execvp")
def test_exec_ssh_oserror(mock_execvp):
    """exec_ssh raises OSError if os.execvp throws OSError."""
    mock_execvp.side_effect = OSError("No such file or directory")
    with pytest.raises(OSError, match="No such file"):
        exec_ssh("10.0.0.1", "root", Path("key"))
