"""Tests for FirecrackerClient."""

import pytest
from pathlib import Path
from unittest.mock import MagicMock, patch

from fcm.core.firecracker import FirecrackerClient, get_vm_socket_path


def test_pause_vm_sends_correct_body() -> None:
    """pause_vm must send state=Paused."""
    client = FirecrackerClient(Path("/nonexistent.socket"))
    with patch.object(client, "_request", return_value=(204, None)) as mock_req:
        client.pause_vm()
    mock_req.assert_called_once_with("PATCH", "/vm", {"state": "Paused"})


def test_resume_vm_sends_correct_body() -> None:
    client = FirecrackerClient(Path("/nonexistent.socket"))
    with patch.object(client, "_request", return_value=(204, None)) as mock_req:
        client.resume_vm()
    mock_req.assert_called_once_with("PATCH", "/vm", {"state": "Resumed"})


def test_get_vm_socket_path_not_found(tmp_path: Path) -> None:
    import os

    os.environ["FCM_CACHE_DIR"] = str(tmp_path)
    try:
        result = get_vm_socket_path("novm")
        assert result is None
    finally:
        del os.environ["FCM_CACHE_DIR"]


def test_get_vm_socket_path_found(tmp_path: Path) -> None:
    import os

    os.environ["FCM_CACHE_DIR"] = str(tmp_path)
    try:
        vm_dir = tmp_path / "vms" / "myvm"
        vm_dir.mkdir(parents=True)
        sock = vm_dir / "firecracker.socket"
        sock.touch()
        result = get_vm_socket_path("myvm")
        assert result == sock
    finally:
        del os.environ["FCM_CACHE_DIR"]


def test_request_raises_socket_not_found_when_no_socket(tmp_path: Path) -> None:
    """_request raises SocketNotFoundError when socket file doesn't exist."""
    from fcm.exceptions import SocketNotFoundError

    client = FirecrackerClient(tmp_path / "nonexistent.socket")
    with pytest.raises(SocketNotFoundError):
        client._request("GET", "/")


def test_request_raises_firecracker_error_on_oserror(tmp_path: Path) -> None:
    """_request raises FirecrackerError on OSError during request."""
    from fcm.exceptions import FirecrackerError

    socket_path = tmp_path / "test.socket"
    socket_path.touch()
    client = FirecrackerClient(socket_path)

    mock_conn = MagicMock()
    mock_conn.request.side_effect = OSError("connection reset")
    client.conn = mock_conn

    with pytest.raises(FirecrackerError, match="API request failed"):
        client._request("GET", "/")


def test_request_returns_status_and_body() -> None:
    """_request returns (status_code, parsed_json) on success."""
    import json

    client = FirecrackerClient(Path("/fake.socket"))
    mock_response = MagicMock()
    mock_response.status = 200
    mock_response.read.return_value = json.dumps({"id": "test"}).encode()

    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = mock_response
    client.conn = mock_conn

    status, data = client._request("GET", "/")
    assert status == 200
    assert data == {"id": "test"}


def test_request_returns_none_body_for_empty_response() -> None:
    """_request returns None body when response is empty."""
    client = FirecrackerClient(Path("/fake.socket"))
    mock_response = MagicMock()
    mock_response.status = 204
    mock_response.read.return_value = b""

    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = mock_response
    client.conn = mock_conn

    status, data = client._request("DELETE", "/vm")
    assert status == 204
    assert data is None


def test_pause_vm_raises_on_non_204() -> None:
    """pause_vm raises FirecrackerError when status is not 204."""
    from fcm.exceptions import FirecrackerError

    client = FirecrackerClient(Path("/fake.socket"))
    with patch.object(client, "_request", return_value=(400, {"fault_message": "bad"})):
        with pytest.raises(FirecrackerError):
            client.pause_vm()


def test_resume_vm_raises_on_non_204() -> None:
    """resume_vm raises FirecrackerError when status is not 204."""
    from fcm.exceptions import FirecrackerError

    client = FirecrackerClient(Path("/fake.socket"))
    with patch.object(client, "_request", return_value=(500, None)):
        with pytest.raises(FirecrackerError):
            client.resume_vm()


def test_create_snapshot_sends_correct_body() -> None:
    """create_snapshot sends the correct paths in the request body."""
    client = FirecrackerClient(Path("/fake.socket"))
    mem_path = Path("/tmp/mem.state")
    snap_path = Path("/tmp/snap.state")
    with patch.object(client, "_request", return_value=(204, None)) as mock_req:
        result = client.create_snapshot(mem_path, snap_path)
    assert result is True
    mock_req.assert_called_once_with(
        "PUT",
        "/snapshot/create",
        {"mem_file_path": str(mem_path), "snapshot_path": str(snap_path)},
    )


def test_send_ctrl_alt_del_returns_false_on_error() -> None:
    """send_ctrl_alt_del returns False when FirecrackerError is raised."""
    from fcm.exceptions import FirecrackerError

    client = FirecrackerClient(Path("/fake.socket"))
    with patch.object(client, "_request", side_effect=FirecrackerError("no vm")):
        result = client.send_ctrl_alt_del()
    assert result is False


def test_client_close_clears_connection() -> None:
    """close() sets conn to None."""
    client = FirecrackerClient(Path("/fake.socket"))
    client.conn = MagicMock()
    client.close()
    assert client.conn is None
