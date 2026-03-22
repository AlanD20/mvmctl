"""Tests for FirecrackerClient."""

from pathlib import Path
from unittest.mock import patch

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


def test_get_vm_socket_path_not_found(tmp_path: Path, monkeypatch: object) -> None:
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
