"""Console socket client for VM serial console access.

Provides both low-level socket operations and high-level console management.
"""

import select
import socket
from collections.abc import Generator
from pathlib import Path
from typing import Any

from mvmctl.services.console_relay import ConsoleRelayClient, ConsoleRelayManager
from mvmctl.services.console_relay._defaults import CONST_CONSOLE_SELECT_TIMEOUT_S


class VMConsole:
    """High-level console interface for a specific VM.

    Combines relay manager (lifecycle) and client (connection) for
    easy console operations.
    """

    def __init__(self, vm_id: str, vm_dir: Path, vm_name: str | None = None) -> None:
        """Initialize console for a VM.

        Args:
            vm_id: VM unique identifier
            vm_dir: VM directory path
            vm_name: Human-readable name (uses vm_id if None)
        """
        self._manager = ConsoleRelayManager(
            id=vm_id,
            path=vm_dir,
            name=vm_name or vm_id,
        )
        self._client: ConsoleRelayClient | None = None

    def start_relay(self, pty_controller_fd: int) -> tuple[Path, int]:
        """Start the console relay for this VM.

        Args:
            pty_controller_fd: File descriptor of PTY controller

        Returns:
            Tuple of (socket_path, pid)
        """
        return self._manager.start(pty_controller_fd)

    def stop_relay(self) -> None:
        """Stop the console relay gracefully."""
        self._manager.stop()

    def terminate_relay(self) -> bool:
        """Forcefully terminate the relay.

        Returns:
            True if terminated, False if not running
        """
        return self._manager.terminate()

    def is_running(self) -> bool:
        """Check if relay is running."""
        return self._manager.is_running()

    def get_pid(self) -> int | None:
        """Get relay PID if running."""
        return self._manager.get_pid()

    def connect(self, timeout: float = 5.0) -> ConsoleRelayClient:
        """Connect to the console.

        Args:
            timeout: Connection timeout in seconds

        Returns:
            Connected ConsoleRelayClient
        """
        self._client = ConsoleRelayClient(self._manager.socket_path())
        self._client.connect(timeout)
        return self._client

    def disconnect(self) -> None:
        """Disconnect from console."""
        if self._client:
            self._client.disconnect()
            self._client = None

    def get_state(self) -> dict[str, Any]:
        """Get console state.

        Returns:
            Dict with: running (bool), pid (int|None), socket_path (str)
        """
        return {
            "running": self._manager.is_running(),
            "pid": self._manager.get_pid(),
            "socket_path": str(self._manager.socket_path()),
        }

    @property
    def manager(self) -> ConsoleRelayManager:
        """Access the underlying relay manager."""
        return self._manager


# Low-level socket operations (for advanced use)
def connect_to_relay(socket_path: Path, timeout: float = 5.0) -> socket.socket:
    """Connect to console relay Unix socket.

    Args:
        socket_path: Path to Unix socket
        timeout: Connection timeout in seconds

    Returns:
        Connected socket
    """
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.settimeout(timeout)
    sock.connect(str(socket_path))
    sock.setblocking(False)
    return sock


def disconnect_from_relay(sock: socket.socket) -> None:
    """Disconnect from relay socket."""
    try:
        sock.close()
    except OSError:
        pass


def send_console_input(sock: socket.socket, data: bytes) -> bool:
    """Send input to console.

    Args:
        sock: Connected socket
        data: Bytes to send

    Returns:
        True if successful
    """
    if not data:
        return True
    try:
        sock.sendall(data)
        return True
    except (OSError, BrokenPipeError, ConnectionResetError):
        return False


def read_console_output(
    sock: socket.socket, buffer_size: int = 4096
) -> Generator[bytes, None, None]:
    """Read output from console.

    Args:
        sock: Connected socket
        buffer_size: Read buffer size

    Yields:
        Bytes from console
    """
    while True:
        ready, _, _ = select.select([sock.fileno()], [], [], CONST_CONSOLE_SELECT_TIMEOUT_S)
        if sock.fileno() in ready:
            try:
                data = sock.recv(buffer_size)
                if data:
                    yield data
                else:
                    return
            except (BlockingIOError, InterruptedError):
                continue
            except (OSError, ConnectionResetError):
                return


def check_escape_sequence(buffer: bytearray, sequence: bytes = b"\x18d") -> bool:
    """Check if buffer ends with escape sequence.

    Args:
        buffer: Input buffer
        sequence: Escape sequence to check (default: Ctrl+X then 'd')

    Returns:
        True if sequence found
    """
    if len(buffer) >= len(sequence):
        return bytes(buffer[-len(sequence) :]) == sequence
    return False


def get_console_state(vm_id: str, vm_dir: Path, vm_name: str | None = None) -> dict[str, Any]:
    """Get console state for a VM.

    Args:
        vm_id: VM unique identifier
        vm_dir: VM directory path
        vm_name: VM name (optional)

    Returns:
        Dict with: running (bool), pid (int|None), socket_path (str)
    """
    console = VMConsole(vm_id, vm_dir, vm_name)
    return console.get_state()
