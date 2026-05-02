"""
Console relay client for connecting to the relay Unix socket.

Provides a high-level client for bidirectional console communication
with detach keybind support.
"""

import select
import socket
from collections.abc import Generator
from pathlib import Path

from mvmctl.constants import CONST_CONSOLE_SOCKET_TIMEOUT_S
from mvmctl.services.console_relay._defaults import (
    CONST_CONSOLE_DETACH_SEQUENCE,
    CONST_CONSOLE_SELECT_TIMEOUT_S,
)
from mvmctl.services.console_relay.exceptions import ConsoleRelayConnectionError


class ConsoleRelayClient:
    """
    Client for connecting to a console relay Unix socket.

        Handles connection management, bidirectional I/O, and graceful
    disconnection with optional detach keybind support.

    Attributes:
            _socket_path: Path to the Unix socket
            _detach_sequence: Byte sequence to trigger detach
            _sock: Connected socket or None

    """

    def __init__(
        self,
        socket_path: Path,
        detach_sequence: bytes = CONST_CONSOLE_DETACH_SEQUENCE,
    ) -> None:
        """
        Initialize the console relay client.

        Args:
            socket_path: Path to the console relay Unix socket
            detach_sequence: Byte sequence to trigger detach (default: Ctrl+X then 'd')

        """
        self._socket_path = socket_path
        self._detach_sequence = detach_sequence
        self._sock: socket.socket | None = None

    def connect(self, timeout: float = CONST_CONSOLE_SOCKET_TIMEOUT_S) -> None:
        """
        Connect to the console relay socket.

        Args:
            timeout: Connection timeout in seconds

        Raises:
            ConsoleRelayConnectionError: If connection fails

        """
        try:
            self._sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self._sock.settimeout(timeout)
            self._sock.connect(str(self._socket_path))
            self._sock.setblocking(False)
        except (ConnectionRefusedError, FileNotFoundError, TimeoutError) as e:
            raise ConsoleRelayConnectionError(
                f"Failed to connect to console relay at {self._socket_path}: {e}"
            ) from e

    def disconnect(self) -> None:
        """Disconnect from the console relay socket."""
        if self._sock is not None:
            try:
                self._sock.close()
            except OSError:
                pass
            self._sock = None

    def is_connected(self) -> bool:
        """Check if client is currently connected."""
        return self._sock is not None

    def send(self, data: bytes) -> bool:
        """
        Send data to the console.

        Args:
            data: Bytes to send

        Returns:
            True if successful, False if connection broken

        """
        if self._sock is None or not data:
            return False
        try:
            self._sock.sendall(data)
            return True
        except (OSError, BrokenPipeError, ConnectionResetError):
            return False

    def receive(self, buffer_size: int = 4096) -> Generator[bytes]:
        """
        Receive data from the console.

        Yields bytes as they become available. Check for detach sequence
        in your handler.

        Args:
            buffer_size: Size of read buffer

        Yields:
            Bytes received from the console

        """
        if self._sock is None:
            return

        while True:
            ready, _, _ = select.select(
                [self._sock.fileno()], [], [], CONST_CONSOLE_SELECT_TIMEOUT_S
            )
            if self._sock.fileno() in ready:
                try:
                    data = self._sock.recv(buffer_size)
                    if data:
                        yield data
                    else:
                        return
                except (BlockingIOError, InterruptedError):
                    continue
                except (OSError, ConnectionResetError):
                    return

    def check_detach(self, buffer: bytearray) -> bool:
        """
        Check if buffer ends with detach sequence.

        Args:
            buffer: Accumulated input buffer

        Returns:
            True if detach sequence detected

        """
        if len(buffer) >= len(self._detach_sequence):
            return (
                bytes(buffer[-len(self._detach_sequence) :])
                == self._detach_sequence
            )
        return False

    @property
    def socket_path(self) -> Path:
        """Return the socket path."""
        return self._socket_path

    @property
    def detach_sequence(self) -> bytes:
        """Return the detach sequence."""
        return self._detach_sequence

    def get_socket(self) -> socket.socket:
        """
        Return the underlying connected socket for advanced use.

        Returns:
            Connected socket (set to non-blocking mode)

        Raises:
            RuntimeError: If not connected

        """
        if self._sock is None:
            raise RuntimeError("Not connected - call connect() first")
        return self._sock

    def __enter__(self) -> "ConsoleRelayClient":
        """Context manager entry."""
        self.connect()
        return self

    def __exit__(
        self, exc_type: object, exc_val: object, exc_tb: object
    ) -> None:
        """Context manager exit."""
        self.disconnect()
