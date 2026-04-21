"""Tests for NoCloudNetServerManager class."""

import sys
import threading
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.services.nocloud_server import NoCloudNetServerManager


@pytest.fixture
def cloud_init_dir(tmp_path: Path) -> Path:
    """Create a temporary cloud-init directory with required files."""
    cloud_init = tmp_path / "cloud-init"
    cloud_init.mkdir()
    (cloud_init / "meta-data").write_text("instance-id: test-vm\n")
    (cloud_init / "user-data").write_text("#cloud-config\n")
    (cloud_init / "network-config").write_text("version: 2\n")
    return cloud_init


@pytest.fixture
def vm_dir(tmp_path: Path) -> Path:
    """Create a temporary VM directory."""
    vm = tmp_path / "vms" / "test-vm"
    vm.mkdir(parents=True, exist_ok=True)
    return vm


@pytest.fixture
def manager() -> NoCloudNetServerManager:
    """Create a fresh manager instance."""
    return NoCloudNetServerManager()


class TestAllocatePort:
    """Tests for _allocate_port_for_gateway method."""

    def test_allocate_port_returns_valid_port(self, manager: NoCloudNetServerManager) -> None:
        """Test that allocate_port returns a port within the valid range."""
        port = manager._allocate_port_for_gateway("test-vm", "127.0.0.1")

        assert 8000 <= port <= 9000

    def test_allocate_port_binds_to_gateway_ip(self, manager: NoCloudNetServerManager) -> None:
        """Test that allocate_port binds to the specified gateway IP."""
        gateway_ip = "127.0.0.1"
        port = manager._allocate_port_for_gateway("test-vm", gateway_ip)

        # Verify port is allocated
        assert 8000 <= port <= 9000

        # The port should be bindable to the gateway IP
        import socket

        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
            sock.settimeout(5)
            # Should be able to bind to the port on gateway
            sock.bind((gateway_ip, port))
        sock.close()

    def test_allocate_port_fails_when_all_occupied(self, manager: NoCloudNetServerManager) -> None:
        """Test that allocate_port raises MVMError when all ports are occupied."""
        # Mock socket.bind to always fail
        with patch("socket.socket") as mock_socket_class:
            mock_socket = MagicMock()
            mock_socket.__enter__ = MagicMock(return_value=mock_socket)
            mock_socket.__exit__ = MagicMock(return_value=False)
            mock_socket.bind.side_effect = OSError("Address already in use")
            mock_socket_class.return_value = mock_socket

            with pytest.raises(MVMError, match="No available port"):
                manager._allocate_port_for_gateway("test-vm", "127.0.0.1")


class TestStartServer:
    """Tests for start_server method."""

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_start_server_spawns_subprocess(
        self,
        mock_get_vm_dir: MagicMock,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
        cloud_init_dir: Path,
        tmp_path: Path,
    ) -> None:
        """Test that start_server spawns a subprocess."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-spawn"
        vm_dir = tmp_path / vm_name
        vm_dir.mkdir(parents=True, exist_ok=True)

        mock_get_vm_dir.return_value = vm_dir
        mock_popen.return_value = MagicMock(pid=12345)

        url, port = manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        # Verify subprocess was spawned
        mock_popen.assert_called_once()

        # Verify URL uses gateway IP
        assert url == f"http://{gateway_ip}:{port}/"
        assert 8000 <= port <= 9000

        # Verify command includes correct arguments
        call_args = mock_popen.call_args[0][0]
        assert sys.executable in call_args[0]
        assert "--cloud-init-dir" in call_args
        assert "--host" in call_args
        assert gateway_ip in call_args

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_start_server_raises_if_already_running(
        self,
        mock_get_vm_dir: MagicMock,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
        cloud_init_dir: Path,
        tmp_path: Path,
    ) -> None:
        """Test that starting a server for an already running VM raises an error."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-dup"
        vm_dir = tmp_path / vm_name
        vm_dir.mkdir(parents=True, exist_ok=True)

        mock_get_vm_dir.return_value = vm_dir
        mock_popen.return_value = MagicMock(pid=12345)

        manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        with pytest.raises(MVMError, match="already running"):
            manager.start_server(vm_name, cloud_init_dir, gateway_ip)


class TestStopServer:
    """Tests for stop_server method."""

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_stop_server_is_idempotent(
        self,
        mock_get_vm_dir: MagicMock,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
        cloud_init_dir: Path,
        tmp_path: Path,
    ) -> None:
        """Test that stop_server can be called multiple times safely."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-stop"
        vm_dir = tmp_path / vm_name
        vm_dir.mkdir(parents=True, exist_ok=True)

        mock_get_vm_dir.return_value = vm_dir
        mock_proc = MagicMock(pid=12345)
        mock_popen.return_value = mock_proc

        # Start the server
        manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        # Stop it once
        manager.stop_server(vm_name)

        # Stop it again - should be idempotent (no error)
        manager.stop_server(vm_name)

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_stop_server_sends_sigterm(
        self,
        mock_get_vm_dir: MagicMock,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
        cloud_init_dir: Path,
        tmp_path: Path,
    ) -> None:
        """Test that stop_server sends SIGTERM to the subprocess."""
        import signal

        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-sigterm"
        vm_dir = tmp_path / vm_name
        vm_dir.mkdir(parents=True, exist_ok=True)

        mock_get_vm_dir.return_value = vm_dir
        mock_proc = MagicMock(pid=12345)
        mock_popen.return_value = mock_proc

        # Start the server
        manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        # Stop the server
        with patch("mvmctl.services.nocloud_server.manager.os.kill") as mock_kill:
            manager.stop_server(vm_name)
            mock_kill.assert_called_once_with(12345, signal.SIGTERM)

    def test_stop_server_on_nonexistent_vm(self, manager: NoCloudNetServerManager) -> None:
        """Test that stopping a nonexistent VM is a no-op."""
        # Should not raise
        manager.stop_server("nonexistent-vm")


class TestStopServerPIDFileRecovery:
    """Tests for stop_server PID file recovery path."""

    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_stop_server_recovers_from_pid_file(
        self,
        mock_get_vm_dir: MagicMock,
        mock_kill: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that stop_server can recover from PID file when server not in memory."""
        import signal

        vm_name = "test-vm-recover"
        vm_dir = tmp_path / vm_name
        vm_dir.mkdir(parents=True, exist_ok=True)

        # Create a PID file
        pid_file = vm_dir / "nocloud-server.pid"
        pid_file.write_text("99999")

        mock_get_vm_dir.return_value = vm_dir

        # Create a fresh manager (server not in memory)
        fresh_manager = NoCloudNetServerManager()

        # stop_server should find the PID file and try to stop the process
        # First call (0) checks if process exists, second (SIGTERM) stops it
        mock_kill.side_effect = [None, None]
        fresh_manager.stop_server(vm_name)

        # Verify SIGTERM was sent
        mock_kill.assert_called_with(99999, signal.SIGTERM)

        # Verify PID file was cleaned up
        assert not pid_file.exists()

    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_stop_server_handles_process_already_dead(
        self,
        mock_get_vm_dir: MagicMock,
        mock_kill: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that stop_server handles case where process already terminated."""
        vm_name = "test-vm-dead"
        vm_dir = tmp_path / vm_name
        vm_dir.mkdir(parents=True, exist_ok=True)

        # Create a PID file
        pid_file = vm_dir / "nocloud-server.pid"
        pid_file.write_text("99999")

        mock_get_vm_dir.return_value = vm_dir

        # Create a fresh manager (server not in memory)
        fresh_manager = NoCloudNetServerManager()

        # Process doesn't exist (ProcessLookupError on os.kill(pid, 0))
        mock_kill.side_effect = ProcessLookupError
        fresh_manager.stop_server(vm_name)

        # PID file should still be cleaned up
        assert not pid_file.exists()

    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_stop_server_no_pid_file_no_op(
        self,
        mock_get_vm_dir: MagicMock,
        mock_kill: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that stop_server is a no-op when no PID file exists."""
        vm_name = "test-vm-no-pid"
        vm_dir = tmp_path / vm_name
        vm_dir.mkdir(parents=True, exist_ok=True)

        mock_get_vm_dir.return_value = vm_dir

        # Create a fresh manager (server not in memory, no PID file)
        fresh_manager = NoCloudNetServerManager()
        fresh_manager.stop_server(vm_name)

        # No os.kill calls should be made
        mock_kill.assert_not_called()


class TestCleanupOrphans:
    """Tests for cleanup_orphans method."""

    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_cache_dir")
    def test_cleanup_orphans_removes_stale_pid_files(
        self,
        mock_get_cache_dir: MagicMock,
        mock_kill: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that cleanup_orphans removes PID files for terminated processes."""
        from mvmctl.core.mvm_db import MVMDatabase

        # Set up fake cache directory structure with unique names to avoid conflicts
        cache_dir = tmp_path / "cache1"
        cache_dir.mkdir(exist_ok=True)
        vms_dir = cache_dir / "vms"
        vms_dir.mkdir(exist_ok=True)

        # Initialize database for this cache dir
        MVMDatabase(cache_dir / "mvmdb.db").migrate()

        # Create two VM directories
        vm1_dir = vms_dir / "vm1"
        vm1_dir.mkdir(exist_ok=True)
        vm2_dir = vms_dir / "vm2"
        vm2_dir.mkdir(exist_ok=True)

        # Create stale PID files
        pid_file1 = vm1_dir / "nocloud-server.pid"
        pid_file1.write_text("99998")
        pid_file2 = vm2_dir / "nocloud-server.pid"
        pid_file2.write_text("99999")

        mock_get_cache_dir.return_value = cache_dir

        # First call (vm1): process doesn't exist -> remove PID file
        # Second call (vm2): process doesn't exist -> remove PID file
        mock_kill.side_effect = [ProcessLookupError, ProcessLookupError]

        # Creating a manager runs cleanup_orphans as part of __init__
        NoCloudNetServerManager()

        # PID files should be cleaned up
        assert not pid_file1.exists()
        assert not pid_file2.exists()

    @patch("mvmctl.core.vm_manager.get_vm_manager")
    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_cache_dir")
    def test_cleanup_orphans_leaves_running_processes(
        self,
        mock_get_cache_dir: MagicMock,
        mock_kill: MagicMock,
        mock_get_vm_manager: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that cleanup_orphans doesn't touch PID files for running processes."""
        # Set up fake cache directory structure with unique names
        cache_dir = tmp_path / "cache2"
        cache_dir.mkdir(exist_ok=True)
        vms_dir = cache_dir / "vms"
        vms_dir.mkdir(exist_ok=True)

        vm_dir = vms_dir / "running-vm"
        vm_dir.mkdir(exist_ok=True)

        # Create PID file for a still-running process
        pid_file = vm_dir / "nocloud-server.pid"
        pid_file.write_text("88888")

        mock_get_cache_dir.return_value = cache_dir

        # Mock VM manager to return a running VM
        mock_vm_manager = MagicMock()
        mock_vm = MagicMock()
        from mvmctl.models.vm import VMStatus

        mock_vm.status = VMStatus.RUNNING  # Use enum, not string
        mock_vm_manager.get_by_full_id.return_value = mock_vm
        mock_get_vm_manager.return_value = mock_vm_manager

        # Process is still running (os.kill(pid, 0) succeeds)
        mock_kill.return_value = None

        # Creating a manager runs cleanup_orphans as part of __init__
        NoCloudNetServerManager()

        # PID file should NOT be removed
        assert pid_file.exists()
        assert pid_file.read_text().strip() == "88888"

    @patch("mvmctl.utils.fs.get_cache_dir")
    def test_cleanup_orphans_handles_missing_vms_dir(
        self,
        mock_get_cache_dir: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that cleanup_orphans handles missing vms directory gracefully."""
        cache_dir = tmp_path / "cache3"
        cache_dir.mkdir(exist_ok=True)

        mock_get_cache_dir.return_value = cache_dir

        # Should not raise - creating a manager runs cleanup_orphans
        NoCloudNetServerManager()

    @patch("mvmctl.core.vm_manager.get_vm_manager")
    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_cache_dir")
    def test_cleanup_orphans_handles_invalid_pid_file(
        self,
        mock_get_cache_dir: MagicMock,
        mock_kill: MagicMock,
        mock_get_vm_manager: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that cleanup_orphans handles invalid PID files gracefully."""
        cache_dir = tmp_path / "cache4"
        cache_dir.mkdir(exist_ok=True)
        vms_dir = cache_dir / "vms"
        vms_dir.mkdir(exist_ok=True)

        vm_dir = vms_dir / "bad-pid-vm"
        vm_dir.mkdir(exist_ok=True)

        # Create invalid PID file (not a number)
        pid_file = vm_dir / "nocloud-server.pid"
        pid_file.write_text("not-a-number")

        mock_get_cache_dir.return_value = cache_dir

        # Mock VM manager to return None (VM not found)
        mock_vm_manager = MagicMock()
        mock_vm_manager.get.return_value = None
        mock_get_vm_manager.return_value = mock_vm_manager

        # Should not raise - just log and continue
        NoCloudNetServerManager()

        # PID file should be cleaned up (VM not running, so _stop_by_pid_file is called)
        assert not pid_file.exists()

    @patch("mvmctl.core.vm_manager.get_vm_manager")
    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_cache_dir")
    def test_cleanup_orphans_skips_running_vms(
        self,
        mock_get_cache_dir: MagicMock,
        mock_kill: MagicMock,
        mock_get_vm_manager: MagicMock,
        tmp_path: Path,
    ):
        """Test that VMs with status=VMState.RUNNING are not cleaned up."""
        from mvmctl.models.vm import VMStatus

        cache_dir = tmp_path / "cache_running"
        cache_dir.mkdir(exist_ok=True)
        vms_dir = cache_dir / "vms"
        vms_dir.mkdir(exist_ok=True)

        vm_dir = vms_dir / "running-vm-hash"
        vm_dir.mkdir(exist_ok=True)

        # Create PID file
        pid_file = vm_dir / "nocloud-server.pid"
        pid_file.write_text("88888")

        mock_get_cache_dir.return_value = cache_dir

        # Mock VM manager to return a RUNNING VM (using VMState enum)
        mock_vm_manager = MagicMock()
        mock_vm = MagicMock()
        mock_vm.status = VMStatus.RUNNING  # Use enum, not string
        mock_vm_manager.get_by_full_id.return_value = mock_vm
        mock_get_vm_manager.return_value = mock_vm_manager

        # Process is still running
        mock_kill.return_value = None

        # Creating a manager runs cleanup_orphans
        NoCloudNetServerManager()

        # PID file should NOT be removed - VM is running
        assert pid_file.exists()

    @patch("mvmctl.core.vm_manager.get_vm_manager")
    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_cache_dir")
    def test_cleanup_orphans_removes_stopped_vms(
        self,
        mock_get_cache_dir: MagicMock,
        mock_kill: MagicMock,
        mock_get_vm_manager: MagicMock,
        tmp_path: Path,
    ):
        """Test that VMs with status=VMState.STOPPED are properly cleaned up."""
        from mvmctl.models.vm import VMStatus

        cache_dir = tmp_path / "cache_stopped"
        cache_dir.mkdir(exist_ok=True)
        vms_dir = cache_dir / "vms"
        vms_dir.mkdir(exist_ok=True)

        vm_dir = vms_dir / "stopped-vm-hash"
        vm_dir.mkdir(exist_ok=True)

        # Create PID file
        pid_file = vm_dir / "nocloud-server.pid"
        pid_file.write_text("88888")

        mock_get_cache_dir.return_value = cache_dir

        # Mock VM manager to return a STOPPED VM
        mock_vm_manager = MagicMock()
        mock_vm = MagicMock()
        mock_vm.status = VMStatus.STOPPED  # Use enum
        mock_vm_manager.get_by_full_id.return_value = mock_vm
        mock_get_vm_manager.return_value = mock_vm_manager

        # Process doesn't exist anymore
        mock_kill.side_effect = ProcessLookupError

        # Creating a manager runs cleanup_orphans
        NoCloudNetServerManager()

        # PID file should be removed - VM is stopped
        assert not pid_file.exists()

    @patch("mvmctl.core.vm_manager.get_vm_manager")
    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_cache_dir")
    def test_cleanup_orphans_removes_error_vms(
        self,
        mock_get_cache_dir: MagicMock,
        mock_kill: MagicMock,
        mock_get_vm_manager: MagicMock,
        tmp_path: Path,
    ):
        """Test that VMs with status=VMState.ERROR are properly cleaned up."""
        from mvmctl.models.vm import VMStatus

        cache_dir = tmp_path / "cache_error"
        cache_dir.mkdir(exist_ok=True)
        vms_dir = cache_dir / "vms"
        vms_dir.mkdir(exist_ok=True)

        vm_dir = vms_dir / "error-vm-hash"
        vm_dir.mkdir(exist_ok=True)

        # Create PID file
        pid_file = vm_dir / "nocloud-server.pid"
        pid_file.write_text("88888")

        mock_get_cache_dir.return_value = cache_dir

        # Mock VM manager to return an ERROR VM
        mock_vm_manager = MagicMock()
        mock_vm = MagicMock()
        mock_vm.status = VMStatus.ERROR  # Use enum
        mock_vm_manager.get_by_full_id.return_value = mock_vm
        mock_get_vm_manager.return_value = mock_vm_manager

        # Process doesn't exist anymore
        mock_kill.side_effect = ProcessLookupError

        # Creating a manager runs cleanup_orphans
        NoCloudNetServerManager()

        # PID file should be removed - VM is in error state
        assert not pid_file.exists()


class TestGetServer:
    """Tests for get_server method."""

    def test_get_server_returns_none(self, manager: NoCloudNetServerManager) -> None:
        """Test that get_server always returns None (subprocess-based)."""
        # get_server() is deprecated and always returns None
        assert manager.get_server("test-vm") is None

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_get_server_returns_none_even_when_running(
        self,
        mock_get_vm_dir: MagicMock,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
        cloud_init_dir: Path,
        tmp_path: Path,
    ) -> None:
        """Test that get_server returns None even when server is running."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-running"
        vm_dir = tmp_path / vm_name
        vm_dir.mkdir(parents=True, exist_ok=True)

        mock_get_vm_dir.return_value = vm_dir
        mock_popen.return_value = MagicMock(pid=12345)

        manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        # get_server() is deprecated and always returns None
        assert manager.get_server(vm_name) is None


class TestIsServerRunning:
    """Tests for is_server_running method."""

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_is_server_running_true(
        self,
        mock_get_vm_dir: MagicMock,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
        cloud_init_dir: Path,
        tmp_path: Path,
    ) -> None:
        """Test is_server_running returns True when process is alive."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-alive"
        vm_dir = tmp_path / vm_name
        vm_dir.mkdir(parents=True, exist_ok=True)

        mock_get_vm_dir.return_value = vm_dir
        mock_popen.return_value = MagicMock(pid=12345)

        manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        with patch("mvmctl.services.nocloud_server.manager.os.kill", return_value=None):
            assert manager.is_server_running(vm_name) is True

    def test_is_server_running_false_for_unknown(self, manager: NoCloudNetServerManager) -> None:
        """Test is_server_running returns False for unknown VM."""
        assert manager.is_server_running("unknown-vm") is False


class TestThreadSafety:
    """Tests for thread safety of the manager."""

    def test_thread_safety_concurrent_allocate_port(self, manager: NoCloudNetServerManager) -> None:
        """Test that concurrent port allocation is thread-safe and returns valid ports."""
        ports: list[int] = []
        errors: list[Exception] = []
        lock = threading.Lock()

        def allocate() -> None:
            try:
                port = manager._allocate_port_for_gateway("concurrent-allocate", "127.0.0.1")
                with lock:
                    ports.append(port)
            except Exception as e:
                errors.append(e)

        threads = []
        for i in range(10):
            t = threading.Thread(target=allocate)
            threads.append(t)
            t.start()

        for t in threads:
            t.join()

        assert len(errors) == 0, f"Thread safety errors: {errors}"
        assert len(ports) == 10
        # All ports should be in valid range (note: ports may be reused after socket close)
        for port in ports:
            assert 8000 <= port <= 9000


class TestCleanupOrphansInit:
    """Tests for cleanup_orphans being called on init."""

    @patch("mvmctl.services.nocloud_server.manager.os.kill")
    @patch("mvmctl.utils.fs.get_cache_dir")
    def test_cleanup_orphans_runs_on_init(
        self,
        mock_get_cache_dir: MagicMock,
        mock_kill: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that cleanup_orphans is called during initialization."""
        from mvmctl.core.mvm_db import MVMDatabase

        cache_dir = tmp_path / "cache5"
        cache_dir.mkdir(exist_ok=True)
        vms_dir = cache_dir / "vms"
        vms_dir.mkdir(exist_ok=True)

        # Initialize database for this cache dir
        MVMDatabase(cache_dir / "mvmdb.db").migrate()

        # Create a VM with a stale PID file
        vm_dir = vms_dir / "stale-vm"
        vm_dir.mkdir(exist_ok=True)
        pid_file = vm_dir / "nocloud-server.pid"
        pid_file.write_text("99999")

        mock_get_cache_dir.return_value = cache_dir
        mock_kill.side_effect = ProcessLookupError  # Process is dead

        # Creating a manager should run cleanup_orphans
        NoCloudNetServerManager()

        # Stale PID file should be cleaned up
        assert not pid_file.exists()
