"""Console controller for VM serial console access.

Provides high-level console relay management.
"""

import os
from pathlib import Path

from mvmctl.exceptions import ConsoleError
from mvmctl.services.console_relay import ConsoleRelayClient, ConsoleRelayManager


class ConsoleController:
    """High-level console relay interface for a specific VM.

    Combines relay manager (lifecycle) and client (connection) for
    easy console operations. Manages PTY lifecycle internally.
    """

    def __init__(
        self,
        vm_id: str,
        vm_dir: Path,
        vm_name: str | None = None,
        pid_filename: str = "console.pid",
        socket_filename: str = "console.sock",
        log_filename: str = "firecracker.console.log",
    ) -> None:
        """Initialize console relay for a VM.

        Args:
            vm_id: VM unique identifier
            vm_dir: VM directory path
            vm_name: Human-readable name (uses vm_id if None)
            pid_filename: Name of the PID file
            socket_filename: Name of the socket file
            log_filename: Name of the log file
        """
        self._manager = ConsoleRelayManager(
            id=vm_id,
            path=vm_dir,
            name=vm_name or vm_id,
            pid_filename=pid_filename,
            socket_filename=socket_filename,
            log_filename=log_filename,
        )
        self._client: ConsoleRelayClient | None = None
        self._controller_fd: int | None = None
        self._client_fd: int | None = None
        self._pid: int | None = None
        self._socket_path: Path | None = None

    @property
    def controller_fd(self) -> int | None:
        """Access controller FD (for relay)."""
        return self._controller_fd

    @property
    def client_fd(self) -> int | None:
        """Access client FD (for Firecracker)."""
        return self._client_fd

    @property
    def manager(self) -> ConsoleRelayManager:
        """Access the underlying relay manager."""
        return self._manager

    @property
    def socket_path(self) -> Path | None:
        return self._socket_path

    @property
    def pid(self) -> int | None:
        return self._pid

    def create_pty(self) -> int:
        """Lazy PTY creation - returns client FD for Firecracker.

        Creates PTY only when first called. Subsequent calls return
        the already-created client FD.

        Returns:
            Client file descriptor for Firecracker stdin/stdout

        Raises:
            OSError: If PTY creation fails
            ConsoleError: If PTY client FD is None after creation
        """
        if self._controller_fd is None:
            self._controller_fd, self._client_fd = os.openpty()

        if self._client_fd is None:
            raise ConsoleError("PTY allocation failed: client FD is None after creation")

        return self._client_fd

    def close_client_fd(self) -> None:
        """Close client FD after Firecracker has taken ownership.

        Safe to call multiple times. Called by orchestrator after
        Firecracker process is spawned.
        """
        if self._client_fd is not None:
            try:
                os.close(self._client_fd)
            except OSError:
                pass
            self._client_fd = None

    def close_pty(self) -> None:
        """Close both PTY FDs. For cleanup on error paths."""
        self.close_client_fd()

        if self._controller_fd is not None:
            try:
                os.close(self._controller_fd)
            except OSError:
                pass
            self._controller_fd = None

    def start(self) -> tuple[Path, int]:
        """Start the console relay for this VM.

        Must call create_pty() before this method.

        Returns:
            Tuple of (socket_path, pid)

        Raises:
            RuntimeError: If create_pty() was not called first
        """
        if self._controller_fd is None:
            raise RuntimeError("Must call create_pty() before start()")

        self._socket_path, self._pid = self._manager.start(self._controller_fd)

        return self._socket_path, self._pid

    def cleanup(self) -> None:
        self.stop()
        self.close_pty()
        self.close_client_fd()

    def stop(self) -> None:
        """Stop the console relay gracefully."""
        self._manager.stop()

    def terminate(self) -> bool:
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

    def connect(self) -> ConsoleRelayClient:
        """Connect to the console.

        Returns:
            Connected ConsoleRelayClient
        """
        self._client = ConsoleRelayClient(self._manager.socket_path)
        self._client.connect()
        return self._client

    def disconnect(self) -> None:
        """Disconnect from console."""
        if self._client:
            self._client.disconnect()
            self._client = None
