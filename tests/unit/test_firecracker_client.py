"""Tests for Firecracker API client."""

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.firecracker import (
    FirecrackerClient,
    get_vm_socket_path,
)
from mvmctl.exceptions import FirecrackerError, SocketNotFoundError


def _make_client(tmp_path: Path) -> tuple[FirecrackerClient, Path]:
    sock = tmp_path / "test.socket"
    sock.touch()
    client = FirecrackerClient(sock)
    return client, sock


def _mock_response(status: int, body: dict[str, object] | None = None) -> MagicMock:
    resp = MagicMock()
    resp.status = status
    resp.read.return_value = json.dumps(body).encode() if body else b""
    return resp


def test_connect_socket_not_found(tmp_path: Path):
    client = FirecrackerClient(tmp_path / "missing.socket")
    with pytest.raises(SocketNotFoundError):
        client._connect()


def test_connect_socket_exists(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    client._connect()
    assert client.conn is not None


def test_close_clears_connection(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    client._connect()
    client.close()
    assert client.conn is None


def test_request_no_connection(tmp_path: Path):
    client = FirecrackerClient(tmp_path / "missing.socket")
    with pytest.raises(SocketNotFoundError):
        client._request("GET", "/")


def test_create_snapshot_success(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(204)
    client.conn = mock_conn

    assert client.create_snapshot(tmp_path / "mem", tmp_path / "state") is True


def test_create_snapshot_failure(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(400, {"error": "fail"})
    client.conn = mock_conn

    with pytest.raises(FirecrackerError, match="Failed to create snapshot"):
        client.create_snapshot(tmp_path / "mem", tmp_path / "state")


def test_load_snapshot_success(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(204)
    client.conn = mock_conn

    assert client.load_snapshot(tmp_path / "mem", tmp_path / "state", resume=True) is True


def test_load_snapshot_failure(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(400, {"error": "nope"})
    client.conn = mock_conn

    with pytest.raises(FirecrackerError, match="Failed to load snapshot"):
        client.load_snapshot(tmp_path / "mem", tmp_path / "state")


def test_get_instance_info_success(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(200, {"id": "i-123"})
    client.conn = mock_conn

    info = client.get_instance_info()
    assert info is not None
    assert info["id"] == "i-123"


def test_get_instance_info_failure(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(500, None)
    client.conn = mock_conn

    assert client.get_instance_info() is None


def test_describe_instance_success(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(200, {"state": "Running"})
    client.conn = mock_conn

    desc = client.describe_instance()
    assert desc is not None
    assert desc["state"] == "Running"


def test_start_instance_success(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(204)
    client.conn = mock_conn

    assert client.start_instance() is True


def test_start_instance_failure(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(400, None)
    client.conn = mock_conn

    with pytest.raises(FirecrackerError, match="Failed to start VM"):
        client.start_instance()


def test_send_ctrl_alt_del_success(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(204)
    client.conn = mock_conn

    assert client.send_ctrl_alt_del() is True


def test_send_ctrl_alt_del_failure(tmp_path: Path):
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(400, None)
    client.conn = mock_conn

    assert client.send_ctrl_alt_del() is False


def test_get_vm_socket_path_found(tmp_path: Path):
    vm_dir = tmp_path / "myvm"
    vm_dir.mkdir()
    sock = vm_dir / "firecracker.socket"
    sock.touch()

    with patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=vm_dir):
        result = get_vm_socket_path("myvm")

    assert result == sock


def test_get_vm_socket_path_not_found(tmp_path: Path):
    vm_dir = tmp_path / "myvm"
    vm_dir.mkdir()

    with patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=vm_dir):
        result = get_vm_socket_path("myvm")

    assert result is None


def test_pause_vm_success(tmp_path: Path):
    """pause_vm sends PATCH /vm with state Paused."""
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(204)
    client.conn = mock_conn

    client.pause_vm()
    mock_conn.request.assert_called_once()
    call_args = mock_conn.request.call_args
    assert call_args[0][0] == "PATCH"
    assert call_args[0][1] == "/vm"
    assert json.loads(call_args[1]["body"]) == {"state": "Paused"}


def test_pause_vm_failure(tmp_path: Path):
    """pause_vm raises FirecrackerError on non-204."""
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(400, {"error": "fail"})
    client.conn = mock_conn

    with pytest.raises(FirecrackerError, match="Failed to pause VM"):
        client.pause_vm()


def test_resume_vm_success(tmp_path: Path):
    """resume_vm sends PATCH /vm with state Resumed."""
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(204)
    client.conn = mock_conn

    client.resume_vm()
    mock_conn.request.assert_called_once()
    call_args = mock_conn.request.call_args
    assert call_args[0][0] == "PATCH"
    assert call_args[0][1] == "/vm"
    assert json.loads(call_args[1]["body"]) == {"state": "Resumed"}


def test_resume_vm_failure(tmp_path: Path):
    """resume_vm raises FirecrackerError on non-204."""
    client, _ = _make_client(tmp_path)
    mock_conn = MagicMock()
    mock_conn.getresponse.return_value = _mock_response(400, {"error": "fail"})
    client.conn = mock_conn

    with pytest.raises(FirecrackerError, match="Failed to resume VM"):
        client.resume_vm()
