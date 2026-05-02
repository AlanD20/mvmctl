"""Tests for ConsoleRelayClient — socket connection, I/O, and error handling."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.services.console_relay.client import ConsoleRelayClient
from mvmctl.services.console_relay.exceptions import ConsoleRelayConnectionError


@pytest.fixture
def socket_path(tmp_path: Path) -> Path:
    return tmp_path / "console.sock"


@pytest.fixture
def client(socket_path: Path) -> ConsoleRelayClient:
    return ConsoleRelayClient(socket_path)


class TestInit:
    """Tests for __init__."""

    def test_stores_socket_path(self, socket_path: Path) -> None:
        client = ConsoleRelayClient(socket_path)
        assert client.socket_path == socket_path

    def test_not_connected_after_init(self, socket_path: Path) -> None:
        client = ConsoleRelayClient(socket_path)
        assert client.is_connected() is False

    def test_default_detach_sequence(self, socket_path: Path) -> None:
        client = ConsoleRelayClient(socket_path)
        assert client.detach_sequence == b"\x18d"

    def test_custom_detach_sequence(self, socket_path: Path) -> None:
        client = ConsoleRelayClient(socket_path, detach_sequence=b"\x01\x02")
        assert client.detach_sequence == b"\x01\x02"


class TestConnect:
    """Tests for connect()."""

    def test_connect_success(self, client: ConsoleRelayClient) -> None:
        mock_sock = MagicMock()
        with patch("socket.socket", return_value=mock_sock):
            client.connect(timeout=5.0)
            assert client.is_connected() is True
            mock_sock.settimeout.assert_called_once_with(5.0)
            mock_sock.connect.assert_called_once_with(str(client.socket_path))
            mock_sock.setblocking.assert_called_once_with(False)

    def test_connect_connection_refused(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        mock_sock.connect.side_effect = ConnectionRefusedError(
            "Connection refused"
        )
        with patch("socket.socket", return_value=mock_sock):
            with pytest.raises(
                ConsoleRelayConnectionError, match="Failed to connect"
            ):
                client.connect()

    def test_connect_file_not_found(self, client: ConsoleRelayClient) -> None:
        mock_sock = MagicMock()
        mock_sock.connect.side_effect = FileNotFoundError(
            "No such file or directory"
        )
        with patch("socket.socket", return_value=mock_sock):
            with pytest.raises(
                ConsoleRelayConnectionError, match="Failed to connect"
            ):
                client.connect()

    def test_connect_timeout(self, client: ConsoleRelayClient) -> None:
        mock_sock = MagicMock()
        mock_sock.connect.side_effect = TimeoutError("Connection timed out")
        with patch("socket.socket", return_value=mock_sock):
            with pytest.raises(
                ConsoleRelayConnectionError, match="Failed to connect"
            ):
                client.connect()


class TestDisconnect:
    """Tests for disconnect()."""

    def test_disconnect_closes_socket(self, client: ConsoleRelayClient) -> None:
        mock_sock = MagicMock()
        client._sock = mock_sock
        client.disconnect()
        mock_sock.close.assert_called_once()
        assert client.is_connected() is False

    def test_disconnect_noop_when_not_connected(
        self, client: ConsoleRelayClient
    ) -> None:
        client.disconnect()
        assert client.is_connected() is False

    def test_disconnect_ignores_os_error(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        mock_sock.close.side_effect = OSError("close error")
        client._sock = mock_sock
        client.disconnect()
        assert client.is_connected() is False


class TestSend:
    """Tests for send()."""

    def test_send_success(self, client: ConsoleRelayClient) -> None:
        mock_sock = MagicMock()
        client._sock = mock_sock
        result = client.send(b"test data")
        assert result is True
        mock_sock.sendall.assert_called_once_with(b"test data")

    def test_send_noop_when_not_connected(
        self, client: ConsoleRelayClient
    ) -> None:
        result = client.send(b"test")
        assert result is False

    def test_send_noop_with_empty_data(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        client._sock = mock_sock
        result = client.send(b"")
        assert result is False
        mock_sock.sendall.assert_not_called()

    def test_send_handles_broken_pipe(self, client: ConsoleRelayClient) -> None:
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = BrokenPipeError()
        client._sock = mock_sock
        result = client.send(b"test")
        assert result is False

    def test_send_handles_connection_reset(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = ConnectionResetError()
        client._sock = mock_sock
        result = client.send(b"test")
        assert result is False

    def test_send_handles_os_error(self, client: ConsoleRelayClient) -> None:
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = OSError("send error")
        client._sock = mock_sock
        result = client.send(b"test")
        assert result is False


class TestReceive:
    """Tests for receive()."""

    def test_receive_yields_data(self, client: ConsoleRelayClient) -> None:
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 3
        mock_sock.recv.side_effect = [b"data1", b"data2", b""]
        client._sock = mock_sock

        results: list[bytes] = []
        with patch("select.select", return_value=([3], [], [])):
            for chunk in client.receive(buffer_size=1024):
                results.append(chunk)
                if len(results) >= 2:
                    break

        assert results == [b"data1", b"data2"]

    def test_receive_stops_on_empty_data(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 3
        mock_sock.recv.return_value = b""
        client._sock = mock_sock

        results: list[bytes] = []
        with patch("select.select", return_value=([3], [], [])):
            for chunk in client.receive():
                results.append(chunk)

        assert results == []

    def test_receive_noop_when_not_connected(
        self, client: ConsoleRelayClient
    ) -> None:
        results: list[bytes] = []
        for chunk in client.receive():
            results.append(chunk)
        assert results == []

    def test_receive_handles_blocking_io_error(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 3
        call_count: list[int] = [0]

        def _recv_side(*args: object, **kwargs: object) -> bytes:
            call_count[0] += 1
            if call_count[0] == 1:
                raise BlockingIOError()
            return b""

        mock_sock.recv.side_effect = _recv_side
        client._sock = mock_sock

        results: list[bytes] = []
        with patch("select.select", return_value=([3], [], [])):
            for chunk in client.receive():
                results.append(chunk)

        assert results == []

    def test_receive_handles_interrupted_error(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 3
        call_count: list[int] = [0]

        def _recv_side(*args: object, **kwargs: object) -> bytes:
            call_count[0] += 1
            if call_count[0] == 1:
                raise InterruptedError()
            return b""

        mock_sock.recv.side_effect = _recv_side
        client._sock = mock_sock

        results: list[bytes] = []
        with patch("select.select", return_value=([3], [], [])):
            for chunk in client.receive():
                results.append(chunk)

        assert results == []

    def test_receive_handles_connection_reset(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 3
        mock_sock.recv.side_effect = ConnectionResetError()
        client._sock = mock_sock

        results: list[bytes] = []
        with patch("select.select", return_value=([3], [], [])):
            for chunk in client.receive():
                results.append(chunk)

        assert results == []

    def test_receive_handles_os_error(self, client: ConsoleRelayClient) -> None:
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 3
        mock_sock.recv.side_effect = OSError("recv error")
        client._sock = mock_sock

        results: list[bytes] = []
        with patch("select.select", return_value=([3], [], [])):
            for chunk in client.receive():
                results.append(chunk)

        assert results == []

    def test_receive_skips_when_socket_not_in_ready(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 3
        mock_sock.recv.return_value = b""
        client._sock = mock_sock

        results: list[bytes] = []
        with patch("select.select", return_value=([], [], [])):
            for chunk in client.receive():
                results.append(chunk)

        assert results == []


class TestCheckDetach:
    """Tests for check_detach()."""

    def test_detects_detach_sequence(self, client: ConsoleRelayClient) -> None:
        buffer = bytearray(b"some data\x18d")
        assert client.check_detach(buffer) is True

    def test_no_detach_when_no_match(self, client: ConsoleRelayClient) -> None:
        buffer = bytearray(b"some data\x18e")
        assert client.check_detach(buffer) is False

    def test_no_detach_when_buffer_too_short(
        self, client: ConsoleRelayClient
    ) -> None:
        buffer = bytearray(b"\x18")
        assert client.check_detach(buffer) is False

    def test_empty_buffer(self, client: ConsoleRelayClient) -> None:
        buffer = bytearray()
        assert client.check_detach(buffer) is False

    def test_custom_detach_sequence(self, socket_path: Path) -> None:
        client = ConsoleRelayClient(
            socket_path, detach_sequence=b"\x01\x02\x03"
        )
        buffer = bytearray(b"prefix\x01\x02\x03")
        assert client.check_detach(buffer) is True

    def test_detach_at_buffer_start(self, client: ConsoleRelayClient) -> None:
        buffer = bytearray(b"\x18d")
        assert client.check_detach(buffer) is True

    def test_detach_with_multiple_sequences(
        self, client: ConsoleRelayClient
    ) -> None:
        buffer = bytearray(b"\x18d\x18d")
        assert client.check_detach(buffer) is True


class TestGetSocket:
    """Tests for get_socket()."""

    def test_returns_socket_when_connected(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        client._sock = mock_sock
        assert client.get_socket() is mock_sock

    def test_raises_when_not_connected(
        self, client: ConsoleRelayClient
    ) -> None:
        with pytest.raises(RuntimeError, match="Not connected"):
            client.get_socket()


class TestContextManager:
    """Tests for context manager protocol."""

    def test_context_manager_connects_and_disconnects(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        with patch("socket.socket", return_value=mock_sock):
            with client as c:
                assert c.is_connected() is True
        assert client.is_connected() is False

    def test_context_manager_disconnects_on_error(
        self, client: ConsoleRelayClient
    ) -> None:
        mock_sock = MagicMock()
        with patch("socket.socket", return_value=mock_sock):
            try:
                with client:
                    raise ValueError("test error")
            except ValueError:
                pass
        assert client.is_connected() is False
