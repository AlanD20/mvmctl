"""Console socket client for VM serial console access.

Provides functions to connect to the console relay process,
send input, receive output, and manage the console session.
"""

import select
import socket
from collections.abc import Generator
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    CONST_CONSOLE_BUFFER_SIZE,
    CONST_CONSOLE_RECONNECT_DELAY_S,
    CONST_CONSOLE_SOCKET_TIMEOUT_S,
)
from mvmctl.services.console_relay import ConsoleRelayManager


def connect_to_relay(socket_path: Path) -> socket.socket:
    """Connect to the console relay Unix socket.

    Args:
        socket_path: Path to the Unix socket

    Returns:
        Connected socket object

    Raises:
        ConnectionRefusedError: If connection is refused
        FileNotFoundError: If socket does not exist
        TimeoutError: If connection times out
    """
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.settimeout(CONST_CONSOLE_SOCKET_TIMEOUT_S)
    sock.connect(str(socket_path))
    sock.setblocking(False)
    return sock


def disconnect_from_relay(sock: socket.socket) -> None:
    """Disconnect from the console relay socket.

    Args:
        sock: Socket to close
    """
    try:
        sock.close()
    except OSError:
        pass


def send_console_input(sock: socket.socket, data: bytes) -> bool:
    """Send input data to the console.

    Args:
        sock: Connected socket
        data: Bytes to send

    Returns:
        True if successful, False if connection broken
    """
    if not data:
        return True
    try:
        sock.sendall(data)
        return True
    except (OSError, BrokenPipeError, ConnectionResetError):
        return False


def read_console_output(sock: socket.socket) -> Generator[bytes]:
    """Read output from the console socket.

    Yields bytes as they become available. Returns when socket is closed
    or an error occurs.

    Args:
        sock: Connected socket

    Yields:
        Bytes read from the socket
    """
    while True:
        ready, _, _ = select.select([sock.fileno()], [], [], CONST_CONSOLE_RECONNECT_DELAY_S)
        if sock.fileno() in ready:
            try:
                data = sock.recv(CONST_CONSOLE_BUFFER_SIZE)
                if data:
                    yield data
                else:
                    return
            except (BlockingIOError, InterruptedError):
                continue
            except (OSError, ConnectionResetError):
                return


def check_escape_sequence(buffer: bytearray) -> tuple[bool, str]:
    """Check if the buffer contains a console escape sequence.

    Detects Ctrl+A or Ctrl+X followed by D.

    Args:
        buffer: Byte buffer to check

    Returns:
        Tuple of (matched, action) where action is "detach" if matched
    """
    if bytes(buffer) in (b"\x01d", b"\x18d"):
        return True, ""
    return False, ""


def get_console_state(vm_name: str, vm_hash: str | None = None) -> dict[str, Any]:
    """Get console relay state for a VM.

    Args:
        vm_name: Name of the VM (for tracking)
        vm_hash: VM hash (64-char SHA256) for PID file path. If None, uses vm_name.

    Returns:
        Dict with keys: running (bool), pid (int), socket_path (str)
    """
    mgr = ConsoleRelayManager()
    running = mgr.is_relay_running(vm_name, vm_hash)
    pid = mgr.get_relay_pid(vm_name, vm_hash)
    # If vm_hash not provided, use vm_name as fallback for socket path
    lookup_key = vm_hash if vm_hash is not None else vm_name
    socket_path = mgr.get_socket_path(lookup_key)

    return {
        "running": running,
        "pid": pid,
        "socket_path": str(socket_path) if socket_path else None,
    }
