from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.ssh import (
    _validate_ssh_username,
    build_ssh_command,
    connect_to_vm,
    find_ssh_keys,
    resolve_ssh_key,
)
from mvmctl.exceptions import MVMError, MVMKeyError, VMNotFoundError
from mvmctl.models.vm import VMInstance, VMStatus


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


def test_build_ssh_command_basic():
    """Basic command has ssh, strict-host options, and user@ip."""
    cmd = build_ssh_command("10.20.0.2", user="root")
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

    cmd = build_ssh_command("10.20.0.2", user="root", key_path=key)
    assert "-i" in cmd
    assert str(key) in cmd


def test_build_ssh_command_with_command():
    """Appends command at end."""
    cmd = build_ssh_command("10.20.0.2", user="root", command="echo hi")
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

    result = connect_to_vm("10.20.0.5", user="root", exec_mode=False, command="echo hi")

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
    vm = VMInstance(name="myvm", ipv4="10.20.0.3", status=VMStatus.RUNNING)
    mock_manager = MagicMock()
    mock_manager.get.return_value = vm
    mock_vm_manager_cls.return_value = mock_manager

    key = tmp_path / "id_rsa"
    key.write_text("private")
    key.chmod(0o600)
    mock_find_keys.return_value = [key]

    result = connect_to_vm("myvm", user="root", exec_mode=False, command="echo hi")

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
        connect_to_vm("nonexistent", user="root", exec_mode=False)


@patch("mvmctl.core.ssh.find_ssh_keys", return_value=[])
def test_connect_to_vm_no_keys(mock_find_keys: MagicMock):
    """Raises MVMKeyError when no SSH keys found."""
    with pytest.raises(MVMKeyError, match="No SSH keys found"):
        connect_to_vm("10.20.0.5", user="root", exec_mode=False)

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

from mvmctl.core.ssh import exec_ssh, run_ssh  # noqa: E402


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


class TestConnectToVm:
    """Tests for connect_to_vm edge cases."""

    def test_connect_with_ip_address(self, tmp_path):
        """connect_to_vm should accept an IP address directly."""
        key = tmp_path / "id_rsa"
        key.write_text("fake key")
        with patch("mvmctl.core.ssh.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)
            result = connect_to_vm("10.0.0.1", user="root", key_path=key, exec_mode=False)
            assert result == 0

    def test_connect_vm_no_ip(self, tmp_path):
        """connect_to_vm should raise MVMError when VM has no IP."""
        mock_mgr = MagicMock()
        vm = VMInstance(name="noip", status=VMStatus.STOPPED)
        mock_mgr.get.return_value = vm

        with pytest.raises(MVMError, match="has no IP address"):
            connect_to_vm("noip", user="root", vm_manager=mock_mgr)

    def test_connect_no_keys_found(self, tmp_path):
        """connect_to_vm should raise MVMKeyError when no keys found."""
        mock_mgr = MagicMock()
        vm = VMInstance(name="testvm", ipv4="10.0.0.2", status=VMStatus.RUNNING)
        mock_mgr.get.return_value = vm

        keys_dir = tmp_path / "empty_keys"
        keys_dir.mkdir()

        with patch("mvmctl.core.ssh.find_ssh_keys", return_value=[]):
            with pytest.raises(MVMKeyError, match="No SSH keys found"):
                connect_to_vm("testvm", user="root", vm_manager=mock_mgr)

    def test_connect_key_path_not_exists(self, tmp_path):
        """connect_to_vm should raise MVMKeyError when key file doesn't exist."""
        mock_mgr = MagicMock()
        vm = VMInstance(name="testvm", ipv4="10.0.0.2", status=VMStatus.RUNNING)
        mock_mgr.get.return_value = vm

        missing_key = tmp_path / "missing_key"
        with pytest.raises(MVMKeyError, match="SSH key not found"):
            connect_to_vm("testvm", user="root", key_path=missing_key, vm_manager=mock_mgr)

    def test_connect_exec_mode(self, tmp_path):
        """connect_to_vm should call exec_ssh in exec mode."""
        mock_mgr = MagicMock()
        vm = VMInstance(name="testvm", ipv4="10.0.0.2", status=VMStatus.RUNNING)
        mock_mgr.get.return_value = vm

        key = tmp_path / "id_rsa"
        key.write_text("fake key")

        with patch("mvmctl.core.ssh.exec_ssh") as mock_exec:
            result = connect_to_vm(
                "testvm", user="root", key_path=key, exec_mode=True, vm_manager=mock_mgr
            )
            assert result == 0
            mock_exec.assert_called_once()


class TestResolveSshKey:
    """Tests for resolve_ssh_key."""

    def test_resolve_none_returns_first_pub_key(self, tmp_path):
        """resolve_ssh_key(None) should return first .pub key content."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()
        pub_key = keys_dir / "id_rsa.pub"
        pub_key.write_text("ssh-rsa AAAA fake")

        with patch("mvmctl.utils.fs.get_keys_dir", return_value=keys_dir):
            result = resolve_ssh_key(None)
            assert result == "ssh-rsa AAAA fake"

    def test_resolve_none_no_keys_dir(self, tmp_path):
        """resolve_ssh_key(None) should return None when keys dir doesn't exist."""
        missing_dir = tmp_path / "missing"
        with patch("mvmctl.utils.fs.get_keys_dir", return_value=missing_dir):
            result = resolve_ssh_key(None)
            assert result is None

    def test_resolve_by_store_name(self, tmp_path):
        """resolve_ssh_key should find key by name in store."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()
        pub_key = keys_dir / "mykey.pub"
        pub_key.write_text("ssh-ed25519 AAAA fake")

        with patch("mvmctl.utils.fs.get_keys_dir", return_value=keys_dir):
            result = resolve_ssh_key("mykey")
            assert result == "ssh-ed25519 AAAA fake"

    def test_resolve_by_file_path(self, tmp_path):
        """resolve_ssh_key should find key by direct file path."""
        key_file = tmp_path / "my_key.pub"
        key_file.write_text("ssh-rsa BBBB fake")

        result = resolve_ssh_key(str(key_file))
        assert result == "ssh-rsa BBBB fake"

    def test_resolve_not_found_with_available_keys(self, tmp_path):
        """resolve_ssh_key should list available keys in error message."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        with patch("mvmctl.utils.fs.get_keys_dir", return_value=keys_dir):
            with patch("mvmctl.core.key_manager.list_keys") as mock_list:
                from mvmctl.core.key_manager import KeyInfo

                mock_list.return_value = [
                    KeyInfo(
                        name="existing",
                        fingerprint="SHA256:abc",
                        algorithm="ssh-ed25519",
                        comment="test",
                        added_at="2026-01-01",
                    )
                ]
                with pytest.raises(MVMKeyError, match="Available keys: existing"):
                    resolve_ssh_key("nonexistent")

    def test_resolve_not_found_no_keys(self, tmp_path):
        """resolve_ssh_key should suggest adding keys when none exist."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        with patch("mvmctl.utils.fs.get_keys_dir", return_value=keys_dir):
            with patch("mvmctl.core.key_manager.list_keys", return_value=[]):
                with pytest.raises(MVMKeyError, match="No keys found"):
                    resolve_ssh_key("nonexistent")
