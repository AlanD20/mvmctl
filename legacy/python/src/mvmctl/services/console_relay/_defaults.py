"""
Console relay service defaults.

These constants are specific to the console relay service and are used
by both the manager (manager.py) and the standalone process (process.py).
"""

from __future__ import annotations

# Process management
DEFAULT_CONSOLE_PID_FILENAME: str = "console.pid"
DEFAULT_CONSOLE_SOCKET_FILENAME: str = "console.sock"
DEFAULT_CONSOLE_LOG_FILENAME: str = "firecracker.console.log"

# Timing
CONST_CONSOLE_KILL_TIMEOUT_S: float = 2.0  # Seconds to wait before SIGKILL
CONST_CONSOLE_READ_BUFFER_SIZE: int = 4096  # PTY read buffer size in bytes
CONST_CONSOLE_SELECT_TIMEOUT_S: float = 0.1  # Socket select timeout in seconds

# Socket settings
CONST_CONSOLE_SOCKET_BACKLOG: int = 1  # Max pending connections

# Client settings
CONST_CONSOLE_DETACH_SEQUENCE: bytes = b"\x18d"  # Ctrl+X followed by 'd'

__all__ = [
    "DEFAULT_CONSOLE_PID_FILENAME",
    "DEFAULT_CONSOLE_SOCKET_FILENAME",
    "DEFAULT_CONSOLE_LOG_FILENAME",
    "CONST_CONSOLE_KILL_TIMEOUT_S",
    "CONST_CONSOLE_READ_BUFFER_SIZE",
    "CONST_CONSOLE_SOCKET_BACKLOG",
    "CONST_CONSOLE_SELECT_TIMEOUT_S",
]
