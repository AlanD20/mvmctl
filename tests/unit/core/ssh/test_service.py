"""Tests for core/ssh/_service.py — SSHService."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.ssh._service import SSHService


class TestSSHServiceBuildCommand:
    """Tests for SSHService.build_command()."""

    def test_basic(self):
        cmd = SSHService("10.20.0.2", "root").build_command()
        assert cmd == [
            "ssh",
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "UserKnownHostsFile=/dev/null",
            "-o",
            "BatchMode=yes",
            "-o",
            "ServerAliveInterval=2",
            "-o",
            "ServerAliveCountMax=3",
            "root@10.20.0.2",
        ]

    def test_with_key(self, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("private")

        cmd = SSHService("10.20.0.2", "root", key_path=key).build_command()
        assert "-i" in cmd
        assert str(key) in cmd

    def test_with_command(self):
        cmd = SSHService("10.20.0.2", "root").build_command(command="echo hi")
        assert cmd[-1] == "echo hi"

    def test_custom_user(self):
        cmd = SSHService("10.20.0.2", "ubuntu").build_command()
        assert "ubuntu@10.20.0.2" in cmd


class TestSSHServiceRunCommand:
    """Tests for SSHService.run_command()."""

    @patch("mvmctl.core.ssh._service.run_cmd")
    def test_success(self, mock_run, tmp_path: Path):
        mock_run.return_value = MagicMock(returncode=0)
        key = tmp_path / "id_rsa"
        key.write_text("key")
        result = SSHService("10.0.0.1", "root", key_path=key).run_command(
            "uptime"
        )
        assert result == 0
        mock_run.assert_called_once()
        assert mock_run.call_args[0][0][0] == "ssh"

    @patch("mvmctl.core.ssh._service.run_cmd")
    def test_failure(self, mock_run, tmp_path: Path):
        mock_run.return_value = MagicMock(returncode=1)
        key = tmp_path / "id_rsa"
        key.write_text("key")
        result = SSHService("10.0.0.1", "root", key_path=key).run_command(
            "bad_cmd"
        )
        assert result == 1

    @patch("mvmctl.core.ssh._service.run_cmd")
    def test_key_not_exist(self, mock_run):
        """Works with no key path provided."""
        mock_run.return_value = MagicMock(returncode=0)
        result = SSHService("10.0.0.1", "root").run_command("uptime")
        assert result == 0
        mock_run.assert_called_once()
        assert mock_run.call_args[0][0][0] == "ssh"


class TestSSHServiceExecCommand:
    """Tests for SSHService.exec_command()."""

    @patch("mvmctl.core.ssh._service.os.execvp")
    def test_exec_called(self, mock_execvp, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("key")
        SSHService("10.0.0.1", "root", key_path=key).exec_command()
        mock_execvp.assert_called_once()
        assert mock_execvp.call_args[0][0] == "ssh"
        assert "root@10.0.0.1" in mock_execvp.call_args[0][1]

    @patch("mvmctl.core.ssh._service.os.execvp")
    def test_exec_oserror(self, mock_execvp, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("key")
        mock_execvp.side_effect = OSError("No such file or directory")
        with pytest.raises(OSError, match="No such file"):
            SSHService("10.0.0.1", "root", key_path=key).exec_command()


class TestSSHServiceConnect:
    """Tests for SSHService.connect()."""

    @patch("mvmctl.core.ssh._service.SSHService.exec_command")
    def test_exec_mode(self, mock_exec, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("fake key")

        result = SSHService("10.0.0.2", "root", key_path=key).connect(
            exec_mode=True
        )
        assert result == 0
        mock_exec.assert_called_once()

    @patch("mvmctl.core.ssh._service.SSHService.run_command")
    def test_subprocess_mode(self, mock_run, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("fake key")
        mock_run.return_value = 0

        result = SSHService("10.0.0.2", "root", key_path=key).connect(
            exec_mode=False
        )
        assert result == 0
        mock_run.assert_called_once()

    @patch("mvmctl.core.ssh._service.SSHService.run_command")
    def test_subprocess_with_command(self, mock_run, tmp_path: Path):
        key = tmp_path / "id_rsa"
        key.write_text("fake key")
        mock_run.return_value = 0

        result = SSHService("10.0.0.2", "root", key_path=key).connect(
            command="echo hi", exec_mode=False
        )
        assert result == 0
        mock_run.assert_called_once()
