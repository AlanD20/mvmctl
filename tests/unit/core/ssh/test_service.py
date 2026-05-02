"""Tests for core/ssh/_service.py — SSHService."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.ssh._service import SSHService
from mvmctl.exceptions import MVMKeyError, SSHError


class TestSSHServiceValidateUsername:
    """Tests for SSHService.validate_username()."""

    def test_valid_usernames(self):
        for name in ("root", "ubuntu", "_svc", "user-1", "a_b_c"):
            SSHService.validate_username(name)  # Should not raise

    def test_invalid_usernames(self):
        for name in (
            "Root",
            "user name",
            "1start",
            "user@host",
            "$(whoami)",
            "a;b",
            "",
        ):
            with pytest.raises(SSHError, match="Invalid SSH username"):
                SSHService.validate_username(name)


class TestSSHServiceBuildCommand:
    """Tests for SSHService.build_command()."""

    def test_basic(self):
        cmd = SSHService.build_command("10.20.0.2", user="root")
        assert cmd == [
            "ssh",
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "UserKnownHostsFile=/dev/null",
            "root@10.20.0.2",
        ]

    def test_with_key(self, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("private")

        cmd = SSHService.build_command("10.20.0.2", user="root", key_path=key)
        assert "-i" in cmd
        assert str(key) in cmd

    def test_with_command(self):
        cmd = SSHService.build_command(
            "10.20.0.2", user="root", command="echo hi"
        )
        assert cmd[-1] == "echo hi"

    def test_custom_user(self):
        cmd = SSHService.build_command("10.20.0.2", user="ubuntu")
        assert "ubuntu@10.20.0.2" in cmd

    def test_rejects_bad_username(self):
        with pytest.raises(SSHError, match="Invalid SSH username"):
            SSHService.build_command("10.20.0.2", user="$(whoami)")


class TestSSHServiceRunCommand:
    """Tests for SSHService.run_command()."""

    @patch("mvmctl.core.ssh._service.subprocess.run")
    def test_success(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0)
        result = SSHService.run_command(
            "10.0.0.1", "root", Path("key"), "uptime"
        )
        assert result == 0
        mock_run.assert_called_once()
        assert mock_run.call_args[0][0][0] == "ssh"

    @patch("mvmctl.core.ssh._service.subprocess.run")
    def test_failure(self, mock_run):
        mock_run.return_value = MagicMock(returncode=1)
        result = SSHService.run_command(
            "10.0.0.1", "root", Path("key"), "bad_cmd"
        )
        assert result == 1

    @patch("mvmctl.core.ssh._service.subprocess.run")
    def test_key_not_exist(self, mock_run, tmp_path: Path):
        """Should still work even if key doesn't exist (handled by build_command)."""
        mock_run.return_value = MagicMock(returncode=0)
        missing_key = tmp_path / "nonexistent"
        result = SSHService.run_command(
            "10.0.0.1", "root", missing_key, "uptime"
        )
        assert result == 0


class TestSSHServiceExecCommand:
    """Tests for SSHService.exec_command()."""

    @patch("mvmctl.core.ssh._service.os.execvp")
    def test_exec_called(self, mock_execvp):
        SSHService.exec_command("10.0.0.1", "root", Path("key"))
        mock_execvp.assert_called_once()
        assert mock_execvp.call_args[0][0] == "ssh"
        assert "root@10.0.0.1" in mock_execvp.call_args[0][1]

    @patch("mvmctl.core.ssh._service.os.execvp")
    def test_exec_oserror(self, mock_execvp):
        mock_execvp.side_effect = OSError("No such file or directory")
        with pytest.raises(OSError, match="No such file"):
            SSHService.exec_command("10.0.0.1", "root", Path("key"))


class TestSSHServiceConnect:
    """Tests for SSHService.connect()."""

    def test_invalid_ip(self):
        with pytest.raises(SSHError, match="Invalid IP address"):
            SSHService.connect("not-an-ip", "root", exec_mode=False)

    def test_key_path_not_exists(self, tmp_path: Path):
        missing_key = tmp_path / "missing_key"
        with pytest.raises(MVMKeyError, match="SSH key not found"):
            SSHService.connect(
                "10.0.0.2", "root", key_path=missing_key, exec_mode=False
            )

    @patch("mvmctl.core.ssh._service.SSHService.exec_command")
    def test_exec_mode(self, mock_exec, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("fake key")

        result = SSHService.connect(
            "10.0.0.2", "root", key_path=key, exec_mode=True
        )
        assert result == 0
        mock_exec.assert_called_once()

    @patch("mvmctl.core.ssh._service.SSHService.run_command")
    def test_subprocess_mode(self, mock_run, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("fake key")
        mock_run.return_value = 0

        result = SSHService.connect(
            "10.0.0.2", "root", key_path=key, exec_mode=False
        )
        assert result == 0
        mock_run.assert_called_once()

    @patch("mvmctl.core.ssh._service.SSHService.run_command")
    def test_subprocess_with_command(self, mock_run, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("fake key")
        mock_run.return_value = 0

        result = SSHService.connect(
            "10.0.0.2", "root", key_path=key, command="echo hi", exec_mode=False
        )
        assert result == 0
        mock_run.assert_called_once()
