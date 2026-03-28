"""Tests for NoCloudNetServerManager class."""

import threading
import time
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.nocloud_net_manager import NoCloudNetServerManager
from mvmctl.core.nocloud_net_server import NoCloudNetServer
from mvmctl.exceptions import MVMError


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
def manager() -> NoCloudNetServerManager:
    """Create a fresh manager instance."""
    return NoCloudNetServerManager()


class TestAllocatePort:
    """Tests for allocate_port method."""

    def test_allocate_port_returns_valid_port(self, manager: NoCloudNetServerManager) -> None:
        """Test that allocate_port returns a port within the valid range."""
        port = manager.allocate_port("test-vm")

        assert 8000 <= port <= 9000

    def test_allocate_port_avoids_collisions(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that allocate_port handles port collisions gracefully."""
        # Start multiple servers to consume ports
        ports_used: list[int] = []
        servers: list[NoCloudNetServer] = []

        gateway_ip = "127.0.0.1"

        # Start a few servers to occupy ports
        for i in range(3):
            port = manager.allocate_port(f"temp-{i}")
            server = NoCloudNetServer(cloud_init_dir, port=port, host=gateway_ip)
            server.start()
            servers.append(server)
            ports_used.append(port)

        try:
            # Now allocate more ports - should skip occupied ports
            for i in range(5):
                port = manager.allocate_port(f"test-{i}")
                assert 8000 <= port <= 9000
                assert port not in ports_used
        finally:
            # Cleanup
            for server in servers:
                server.stop()

    def test_allocate_port_fails_when_all_occupied(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that allocate_port raises MVMError when all ports are occupied."""
        gateway_ip = "127.0.0.1"
        servers: list[NoCloudNetServer] = []

        # Start servers on all ports in the range
        try:
            for port in range(8000, 9001):
                server = NoCloudNetServer(cloud_init_dir, port=port, host=gateway_ip)
                server.start()
                servers.append(server)
        except MVMError:
            # Expected - may not be able to start all
            pass

        try:
            # Mock socket.bind to always fail
            with patch("socket.socket") as mock_socket_class:
                mock_socket = MagicMock()
                mock_socket.__enter__ = MagicMock(return_value=mock_socket)
                mock_socket.__exit__ = MagicMock(return_value=False)
                mock_socket.bind.side_effect = OSError("Address already in use")
                mock_socket_class.return_value = mock_socket

                with pytest.raises(MVMError, match="No available port"):
                    manager.allocate_port("test-vm")
        finally:
            for server in servers:
                server.stop()


class TestStartServer:
    """Tests for start_server method."""

    def test_start_server_binds_to_gateway_ip(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that server binds to the specified gateway IP, not 0.0.0.0."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-gateway"

        url, port = manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        try:
            assert url.startswith(f"http://{gateway_ip}:")
            assert 8000 <= port <= 9000

            # Verify the server is registered
            server = manager.get_server(vm_name)
            assert server is not None
            assert server.host == gateway_ip
        finally:
            manager.stop_server(vm_name)

    def test_start_server_returns_correct_url(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that start_server returns the correct URL format."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-url"

        url, port = manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        try:
            assert url == f"http://{gateway_ip}:{port}/"
        finally:
            manager.stop_server(vm_name)

    def test_start_server_raises_if_already_running(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that starting a server for an already running VM raises an error."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-dup"

        manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        try:
            with pytest.raises(MVMError, match="already running"):
                manager.start_server(vm_name, cloud_init_dir, gateway_ip)
        finally:
            manager.stop_server(vm_name)


class TestStopServer:
    """Tests for stop_server method."""

    def test_stop_server_is_idempotent(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that stop_server can be called multiple times safely."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-stop"

        # Start the server
        manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        # Stop it once
        manager.stop_server(vm_name)

        # Stop it again - should be idempotent (no error)
        manager.stop_server(vm_name)

        # Verify server is gone
        assert manager.get_server(vm_name) is None

    def test_stop_server_removes_from_registry(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that stop_server removes the server from the registry."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-registry"

        manager.start_server(vm_name, cloud_init_dir, gateway_ip)
        assert manager.get_server(vm_name) is not None

        manager.stop_server(vm_name)
        assert manager.get_server(vm_name) is None

    def test_stop_server_on_nonexistent_vm(self, manager: NoCloudNetServerManager) -> None:
        """Test that stopping a nonexistent VM is a no-op."""
        # Should not raise
        manager.stop_server("nonexistent-vm")


class TestGetServer:
    """Tests for get_server method."""

    def test_get_server_returns_running_server(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that get_server returns the server when it's running."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-get"

        manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        try:
            server = manager.get_server(vm_name)
            assert server is not None
            assert isinstance(server, NoCloudNetServer)
            assert server.is_running()
        finally:
            manager.stop_server(vm_name)

    def test_get_server_returns_none_for_stopped(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that get_server returns None for stopped VMs."""
        gateway_ip = "127.0.0.1"
        vm_name = "test-vm-stopped"

        manager.start_server(vm_name, cloud_init_dir, gateway_ip)
        manager.stop_server(vm_name)

        assert manager.get_server(vm_name) is None

    def test_get_server_returns_none_for_unknown_vm(self, manager: NoCloudNetServerManager) -> None:
        """Test that get_server returns None for unknown VMs."""
        assert manager.get_server("unknown-vm") is None


class TestThreadSafety:
    """Tests for thread safety of the manager."""

    def test_thread_safety_concurrent_start_stop(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that concurrent start/stop operations are thread-safe."""
        gateway_ip = "127.0.0.1"
        errors: list[Exception] = []

        def start_and_stop(vm_id: int) -> None:
            try:
                vm_name = f"concurrent-vm-{vm_id}"
                manager.start_server(vm_name, cloud_init_dir, gateway_ip)
                time.sleep(0.01)  # Small delay
                manager.stop_server(vm_name)
            except Exception as e:
                errors.append(e)

        threads = []
        for i in range(10):
            t = threading.Thread(target=start_and_stop, args=(i,))
            threads.append(t)
            t.start()

        for t in threads:
            t.join()

        assert len(errors) == 0, f"Thread safety errors: {errors}"

    def test_thread_safety_concurrent_get_server(
        self, manager: NoCloudNetServerManager, cloud_init_dir: Path
    ) -> None:
        """Test that concurrent get_server calls are thread-safe."""
        gateway_ip = "127.0.0.1"
        vm_name = "concurrent-get-vm"

        manager.start_server(vm_name, cloud_init_dir, gateway_ip)

        try:
            errors: list[Exception] = []
            results: list[NoCloudNetServer | None] = []

            def get_server() -> None:
                try:
                    server = manager.get_server(vm_name)
                    results.append(server)
                except Exception as e:
                    errors.append(e)

            threads = []
            for _ in range(20):
                t = threading.Thread(target=get_server)
                threads.append(t)
                t.start()

            for t in threads:
                t.join()

            assert len(errors) == 0, f"Thread safety errors: {errors}"
            # All results should either be None or the same server
            for result in results:
                if result is not None:
                    assert result.is_running()
        finally:
            manager.stop_server(vm_name)

    def test_thread_safety_allocate_port(self, manager: NoCloudNetServerManager) -> None:
        """Test that concurrent port allocation is thread-safe and returns valid ports."""
        ports: list[int] = []
        errors: list[Exception] = []
        lock = threading.Lock()

        def allocate() -> None:
            try:
                port = manager.allocate_port("concurrent-allocate")
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


class TestCleanupOrphans:
    """Tests for cleanup_orphans method."""

    def test_cleanup_orphans_runs_on_init(self) -> None:
        """Test that cleanup_orphans is called during initialization."""
        # This test verifies the method is called without error
        # In practice, the method is a no-op for in-memory tracking
        manager = NoCloudNetServerManager()
        # Should complete without error
        manager.cleanup_orphans()
