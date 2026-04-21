"""Tests for vm_process module."""

import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import ProcessError
from mvmctl.core.vm_process import (
    cleanup_tap,
    graceful_shutdown,
    kill_firecracker,
    pause_vm,
    resume_vm,
    spawn_firecracker,
)


class TestSpawnFirecracker:
    def test_spawn_firecracker_success(self, tmp_path: Path):
        config_path = tmp_path / "config.json"
        socket_path = tmp_path / "socket.sock"
        log_path = tmp_path / "log.txt"
        metrics_path = tmp_path / "metrics"
        fc_binary = tmp_path / "firecracker"
        jailer_binary = tmp_path / "jailer"

        config_path.write_text("{}")
        fc_binary.touch()
        fc_binary.chmod(0o755)

        mock_proc = MagicMock()
        mock_proc.pid = 12345

        with patch("subprocess.Popen", return_value=mock_proc) as mock_popen:
            pid = spawn_firecracker(
                config_path=config_path,
                socket_path=socket_path,
                log_path=log_path,
                metrics_path=metrics_path,
                firecracker_binary=fc_binary,
                jailer_binary=jailer_binary,
                lsm_flags="",
                enable_api_socket=False,
                enable_pci=False,
            )
            assert pid == 12345
            mock_popen.assert_called_once()

    def test_spawn_firecracker_with_api_socket(self, tmp_path: Path):
        config_path = tmp_path / "config.json"
        socket_path = tmp_path / "socket.sock"
        log_path = tmp_path / "log.txt"
        fc_binary = tmp_path / "firecracker"

        config_path.write_text("{}")
        fc_binary.touch()
        fc_binary.chmod(0o755)

        mock_proc = MagicMock()
        mock_proc.pid = 12346

        with patch("subprocess.Popen", return_value=mock_proc) as mock_popen:
            pid = spawn_firecracker(
                config_path=config_path,
                socket_path=socket_path,
                log_path=log_path,
                metrics_path=None,
                firecracker_binary=fc_binary,
                jailer_binary=None,
                lsm_flags="",
                enable_api_socket=True,
                enable_pci=False,
            )
            assert pid == 12346
            cmd = mock_popen.call_args[0][0]
            assert "--api-sock" in cmd

    def test_spawn_firecracker_binary_not_found(self, tmp_path: Path):
        config_path = tmp_path / "config.json"
        socket_path = tmp_path / "socket.sock"
        log_path = tmp_path / "log.txt"
        fc_binary = tmp_path / "nonexistent_firecracker"

        config_path.write_text("{}")

        with pytest.raises(ProcessError, match="Firecracker binary not found"):
            spawn_firecracker(
                config_path=config_path,
                socket_path=socket_path,
                log_path=log_path,
                metrics_path=None,
                firecracker_binary=fc_binary,
                jailer_binary=None,
                lsm_flags="",
                enable_api_socket=False,
                enable_pci=False,
            )


class TestKillFirecracker:
    def test_kill_firecracker_success(self):
        with patch("os.kill") as mock_kill:
            kill_firecracker(12345, None)
            mock_kill.assert_called_once_with(12345, 9)

    def test_kill_firecracker_already_dead(self):
        with patch("os.kill", side_effect=ProcessLookupError):
            kill_firecracker(12345, None)

    def test_kill_firecracker_os_error(self):
        with patch("os.kill", side_effect=OSError("Permission denied")):
            with pytest.raises(ProcessError, match="Failed to kill Firecracker"):
                kill_firecracker(12345, None)


class TestGracefulShutdown:
    def test_graceful_shutdown_none_pid(self):
        graceful_shutdown(None, None)

    def test_graceful_shutdown_force_kill(self):
        with patch("os.kill") as mock_kill:
            mock_kill.side_effect = [None, None, None, None]
            graceful_shutdown(12345, None, force=True)
            assert mock_kill.call_count >= 2

    def test_graceful_shutdown_force_already_dead(self):
        with patch("os.kill", side_effect=ProcessLookupError):
            graceful_shutdown(12345, None, force=True)

    def test_graceful_shutdown_with_socket(self, tmp_path: Path):
        socket_path = tmp_path / "socket.sock"
        socket_path.touch()

        mock_client = MagicMock()
        mock_client.send_ctrl_alt_del = MagicMock()
        mock_client.close = MagicMock()

        with patch("mvmctl.core.vm_process.FirecrackerClient", return_value=mock_client):
            with patch("os.kill", side_effect=ProcessLookupError):
                graceful_shutdown(12345, socket_path)
                mock_client.send_ctrl_alt_del.assert_called_once()

    def test_graceful_shutdown_socket_not_exists(self):
        with patch("os.kill", side_effect=ProcessLookupError):
            graceful_shutdown(12345, Path("/nonexistent/socket"))


class TestPauseResume:
    def test_pause_vm(self):
        mock_client = MagicMock()
        pause_vm(mock_client)
        mock_client.pause_vm.assert_called_once()

    def test_resume_vm(self):
        mock_client = MagicMock()
        resume_vm(mock_client)
        mock_client.resume_vm.assert_called_once()


class TestCleanupTap:
    def test_cleanup_tap_success(self):
        with patch("mvmctl.core.vm_process.remove_iptables_forward_rules") as mock_remove:
            with patch("mvmctl.core.vm_process.delete_tap") as mock_delete:
                cleanup_tap("tap0", "mvm-br0")
                mock_remove.assert_called_once_with("tap0", bridge="mvm-br0")
                mock_delete.assert_called_once_with("tap0")

    def test_cleanup_tap_default_bridge(self):
        with patch("mvmctl.core.vm_process.remove_iptables_forward_rules") as mock_remove:
            with patch("mvmctl.core.vm_process.delete_tap") as mock_delete:
                cleanup_tap("tap0")
                mock_remove.assert_called_once()

    def test_cleanup_tap_network_error(self):
        from mvmctl.exceptions import NetworkError

        with patch(
            "mvmctl.core.vm_process.remove_iptables_forward_rules", side_effect=NetworkError("test")
        ):
            with patch("mvmctl.core.vm_process.delete_tap") as mock_delete:
                cleanup_tap("tap0")
                mock_delete.assert_not_called()
