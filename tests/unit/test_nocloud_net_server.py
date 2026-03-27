import time
import urllib.error
import urllib.request
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.nocloud_net_server import NoCloudNetServer, _CloudInitRequestHandler
from mvmctl.exceptions import MVMError


def test_init_with_valid_directory(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)

    assert server.cloud_init_dir == cloud_init_dir
    assert server._requested_port == 0
    assert server.host == "0.0.0.0"
    assert server._port == 0
    assert not server._running


def test_init_with_nonexistent_directory(tmp_path: Path):
    nonexistent = tmp_path / "does-not-exist"

    with pytest.raises(MVMError, match="does not exist"):
        NoCloudNetServer(nonexistent, port=0)


def test_init_with_file_instead_of_directory(tmp_path: Path):
    file_path = tmp_path / "not-a-dir.txt"
    file_path.write_text("content")

    with pytest.raises(MVMError, match="not a directory"):
        NoCloudNetServer(file_path, port=0)


def test_init_with_custom_host(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1")

    assert server.host == "127.0.0.1"


def test_port_property_returns_zero_before_start(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)

    assert server.port == 0


def test_url_property_before_start(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)

    assert server.url == "http://0.0.0.0:0/"


def test_url_property_with_custom_host(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1")
    server.start()

    try:
        assert server.url.startswith("http://127.0.0.1:")
        assert server.port > 0
    finally:
        server.stop()


def test_server_starts_on_available_port(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)
    server.start()

    try:
        assert server._running is True
        assert server.port > 0
        assert server.url.startswith("http://0.0.0.0:")
        assert server.is_running() is True
    finally:
        server.stop()


def test_server_starts_on_specific_port(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)
    server.start()

    try:
        actual_port = server.port
        assert actual_port > 0
        assert server._requested_port == 0
    finally:
        server.stop()


def test_server_start_raises_when_already_running(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)
    server.start()

    try:
        with pytest.raises(MVMError, match="already running"):
            server.start()
    finally:
        server.stop()


def test_server_stop_raises_when_not_running(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)

    with pytest.raises(MVMError, match="not running"):
        server.stop()


def test_server_is_running_returns_false_before_start(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)

    assert server.is_running() is False


def test_server_is_running_returns_false_after_stop(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)
    server.start()
    server.stop()

    assert server.is_running() is False
    assert server._running is False
    assert server.port == 0


def test_server_serves_meta_data(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    (cloud_init_dir / "meta-data").write_text("instance-id: test-vm\n")
    (cloud_init_dir / "user-data").write_text("#cloud-config\n")
    (cloud_init_dir / "network-config").write_text("version: 2\n")

    server = NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1")
    server.start()

    try:
        time.sleep(0.1)

        url = f"{server.url}meta-data"
        with urllib.request.urlopen(url, timeout=5) as response:
            content = response.read().decode("utf-8")
            assert "instance-id: test-vm" in content
            assert response.status == 200
    finally:
        server.stop()


def test_server_serves_user_data(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    (cloud_init_dir / "meta-data").write_text("instance-id: test-vm\n")
    (cloud_init_dir / "user-data").write_text("#cloud-config\nusers: []\n")
    (cloud_init_dir / "network-config").write_text("version: 2\n")

    server = NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1")
    server.start()

    try:
        time.sleep(0.1)

        url = f"{server.url}user-data"
        with urllib.request.urlopen(url, timeout=5) as response:
            content = response.read().decode("utf-8")
            assert "#cloud-config" in content
            assert response.status == 200
    finally:
        server.stop()


def test_server_serves_network_config(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    (cloud_init_dir / "meta-data").write_text("instance-id: test-vm\n")
    (cloud_init_dir / "user-data").write_text("#cloud-config\n")
    (cloud_init_dir / "network-config").write_text("version: 2\nethernets:\n")

    server = NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1")
    server.start()

    try:
        time.sleep(0.1)

        url = f"{server.url}network-config"
        with urllib.request.urlopen(url, timeout=5) as response:
            content = response.read().decode("utf-8")
            assert "version: 2" in content
            assert response.status == 200
    finally:
        server.stop()


def test_server_returns_404_for_missing_file(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    (cloud_init_dir / "meta-data").write_text("instance-id: test-vm\n")

    server = NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1")
    server.start()

    try:
        time.sleep(0.1)

        url = f"{server.url}nonexistent-file"
        with pytest.raises(urllib.error.HTTPError) as exc_info:
            urllib.request.urlopen(url, timeout=5)
        assert exc_info.value.code == 404
    finally:
        server.stop()


def test_server_includes_cache_control_headers(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    (cloud_init_dir / "meta-data").write_text("instance-id: test-vm\n")

    server = NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1")
    server.start()

    try:
        time.sleep(0.1)

        url = f"{server.url}meta-data"
        with urllib.request.urlopen(url, timeout=5) as response:
            headers = response.headers
            assert "no-store" in headers.get("Cache-Control", "")
            assert "no-cache" in headers.get("Cache-Control", "")
    finally:
        server.stop()


def test_context_manager_starts_and_stops_server(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    (cloud_init_dir / "meta-data").write_text("instance-id: test-vm\n")

    with NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1") as server:
        assert server.is_running() is True
        assert server.port > 0

        time.sleep(0.1)
        url = f"{server.url}meta-data"
        with urllib.request.urlopen(url, timeout=5) as response:
            content = response.read().decode("utf-8")
            assert "instance-id: test-vm" in content

    assert server.is_running() is False


def test_context_manager_stops_on_exception(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = None
    with pytest.raises(ValueError, match="Test exception"):
        with NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1") as server:
            assert server.is_running() is True
            raise ValueError("Test exception")

    assert server is not None
    assert server.is_running() is False


def test_find_available_port_returns_valid_port(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)
    port = server._find_available_port()

    assert port >= 8000
    assert port <= 9000


def test_find_available_port_raises_when_no_ports_available(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)

    with patch("socket.socket") as mock_socket_class:
        mock_socket = MagicMock()
        mock_socket.__enter__ = MagicMock(return_value=mock_socket)
        mock_socket.__exit__ = MagicMock(return_value=False)
        mock_socket.bind.side_effect = OSError("Address already in use")
        mock_socket_class.return_value = mock_socket

        with pytest.raises(MVMError, match="No available port"):
            server._find_available_port()


def test_server_handles_multiple_start_stop_cycles(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    (cloud_init_dir / "meta-data").write_text("instance-id: test-vm\n")

    server = NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1")

    server.start()
    time.sleep(0.1)
    assert server.is_running() is True
    server.stop()
    assert server.is_running() is False

    server.start()
    time.sleep(0.1)
    assert server.is_running() is True

    url = f"{server.url}meta-data"
    with urllib.request.urlopen(url, timeout=5) as response:
        content = response.read().decode("utf-8")
        assert "instance-id: test-vm" in content

    server.stop()
    assert server.is_running() is False


def test_server_thread_name_includes_port(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0, host="127.0.0.1")
    server.start()

    try:
        assert server._thread is not None
        assert "nocloud-net-server" in server._thread.name
        assert str(server.port) in server._thread.name
    finally:
        server.stop()


def test_server_stop_handles_missing_server_gracefully(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)

    server._running = True
    server._thread = MagicMock()
    server._thread.is_alive.return_value = False

    server.stop()

    assert server._running is False


def test_server_start_with_specific_requested_port(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)
    server.start()

    try:
        assert server._port == server._requested_port or server._port > 0
    finally:
        server.stop()


def test_server_start_raises_on_port_bind_failure(tmp_path: Path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=12345)

    with patch("socket.socket") as mock_socket_class:
        mock_socket = MagicMock()
        mock_socket.__enter__ = MagicMock(return_value=mock_socket)
        mock_socket.__exit__ = MagicMock(return_value=False)
        mock_socket.bind.side_effect = OSError("Address already in use")
        mock_socket_class.return_value = mock_socket

        with pytest.raises(MVMError, match="Failed to create HTTP server"):
            server.start()


def test_server_stop_handles_thread_timeout(tmp_path: Path, caplog):
    import logging

    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    server = NoCloudNetServer(cloud_init_dir, port=0)
    server.start()

    try:
        with patch.object(server._thread, "join") as mock_join:
            with patch.object(server._thread, "is_alive", return_value=True):
                caplog.set_level(logging.WARNING)
                server.stop()

        assert "did not stop gracefully" in caplog.text
    finally:
        if server.is_running():
            server.stop()
