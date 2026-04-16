"""Firecracker API client over Unix socket.

Two layers:
- :class:`UnixSocketHTTPConnection` — low-level HTTP-over-Unix-socket transport.
- :class:`FirecrackerClient` — high-level VM operation API using the transport.
"""

import http.client
import json
import logging
import socket
from pathlib import Path
from typing import override

from mvmctl.constants import (
    CONST_HTTP_STATUS_NO_CONTENT,
    CONST_HTTP_STATUS_SUCCESS,
    CONST_SOCKET_TIMEOUT_SECONDS,
    DEFAULT_FC_API_SOCKET_FILENAME,
)
from mvmctl.exceptions import FirecrackerError, SocketNotFoundError
from mvmctl.models.firecracker import InstanceDescription, InstanceInfo

logger = logging.getLogger(__name__)


class UnixSocketHTTPConnection(http.client.HTTPConnection):
    """HTTP connection over Unix domain socket."""

    def __init__(self, socket_path: Path):
        self.socket_path = socket_path
        super().__init__("localhost")

    @override
    def connect(self) -> None:
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(CONST_SOCKET_TIMEOUT_SECONDS)
        self.sock.connect(str(self.socket_path))


# ---------------------------------------------------------------------------
# VM API operations
# ---------------------------------------------------------------------------


class FirecrackerClient:
    """Firecracker API client."""

    def __init__(self, socket_path: Path):
        self.socket_path = Path(socket_path)
        self.conn: UnixSocketHTTPConnection | None = None

    def __enter__(self) -> "FirecrackerClient":
        """Connect to Firecracker socket and return client."""
        self._connect()
        return self

    def __exit__(
        self, _exc_type: type[BaseException] | None, _exc_val: BaseException | None, _exc_tb: object
    ) -> None:
        """Close the socket connection."""
        self.close()

    def _connect(self) -> None:
        """Connect to Firecracker socket.

        Raises:
            SocketNotFoundError: If the socket file does not exist.
            FirecrackerError: If connection to the socket fails.
        """
        if not self.socket_path.exists():
            raise SocketNotFoundError(f"Socket not found: {self.socket_path}")

        try:
            self.conn = UnixSocketHTTPConnection(self.socket_path)
        except OSError as e:
            raise FirecrackerError(f"Failed to connect to socket: {e}") from e

    def _request(
        self,
        method: str,
        path: str,
        body: dict[str, object] | None = None,
    ) -> tuple[int, dict[str, object] | None]:
        """Make HTTP request to Firecracker API.

        Raises:
            SocketNotFoundError: If the socket file does not exist.
            FirecrackerError: If the API request fails.
        """
        if not self.conn:
            self._connect()
        assert self.conn is not None

        headers = {"Content-Type": "application/json"} if body else {}
        body_json = json.dumps(body) if body else None

        try:
            self.conn.request(method, path, body=body_json, headers=headers)
            response = self.conn.getresponse()
            status = response.status

            # Read response body
            response_body = response.read().decode("utf-8")
            data = json.loads(response_body) if response_body else None

            return status, data

        except (SocketNotFoundError, FirecrackerError):
            raise
        except OSError as e:
            raise FirecrackerError(f"API request failed: {e}") from e

    def close(self) -> None:
        """Close connection."""
        if self.conn:
            self.conn.close()
            self.conn = None

    def create_snapshot(
        self,
        mem_path: Path,
        snapshot_path: Path,
    ) -> bool:
        """Create VM snapshot.

        Args:
            mem_path: Path to save memory state
            snapshot_path: Path to save VM state

        Returns:
            True if successful.

        Raises:
            FirecrackerError: If snapshot creation fails.
        """
        logger.info("Creating snapshot...")

        body: dict[str, object] = {
            "mem_file_path": str(mem_path),
            "snapshot_path": str(snapshot_path),
        }

        status, data = self._request("PUT", "/snapshot/create", body)

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("Snapshot created")
            logger.info("  Memory: %s", mem_path)
            logger.info("  State: %s", snapshot_path)
            return True
        else:
            msg = f"Failed to create snapshot: {status}"
            if data:
                msg += f" Response: {data}"
            raise FirecrackerError(msg)

    def load_snapshot(
        self,
        mem_path: Path,
        snapshot_path: Path,
        resume: bool = True,
    ) -> bool:
        """Load VM from snapshot.

        Args:
            mem_path: Path to memory state file
            snapshot_path: Path to VM state file
            resume: Whether to resume VM after loading

        Returns:
            True if successful.

        Raises:
            FirecrackerError: If snapshot loading fails.
        """
        logger.info("Loading snapshot...")

        body = {
            "mem_file_path": str(mem_path),
            "snapshot_path": str(snapshot_path),
            "resume_vm": resume,
        }

        status, data = self._request("PUT", "/snapshot/load", body)

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("Snapshot loaded")
            return True
        else:
            msg = f"Failed to load snapshot: {status}"
            if data:
                msg += f" Response: {data}"
            raise FirecrackerError(msg)

    def get_instance_info(self) -> InstanceInfo | None:
        """Get VM instance information.

        Returns:
            InstanceInfo TypedDict or None
        """
        status, data = self._request("GET", "/")

        if status == CONST_HTTP_STATUS_SUCCESS and data:
            return data  # type: ignore[return-value]
        return None

    def describe_instance(self) -> InstanceDescription | None:
        """Describe the VM instance.

        Returns:
            InstanceDescription TypedDict or None
        """
        status, data = self._request("GET", "/vm")

        if status == CONST_HTTP_STATUS_SUCCESS and data:
            return data  # type: ignore[return-value]
        return None

    def start_instance(self) -> bool:
        """Start the VM instance.

        Returns:
            True if successful.

        Raises:
            FirecrackerError: If the start operation fails.
        """
        logger.info("Starting VM...")
        status, _ = self._request("PUT", "/actions", {"action_type": "InstanceStart"})

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("VM started")
            return True
        else:
            raise FirecrackerError(f"Failed to start VM: {status}")

    def send_ctrl_alt_del(self) -> bool:
        """Send Ctrl+Alt+Del to VM.

        Returns:
            True if successful, False otherwise
        """
        try:
            status, _ = self._request("PUT", "/actions", {"action_type": "SendCtrlAltDel"})
        except (SocketNotFoundError, FirecrackerError):
            logger.error("Failed to send Ctrl+Alt+Del")
            return False

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("Ctrl+Alt+Del sent")
            return True
        else:
            logger.error("Failed to send Ctrl+Alt+Del: %s", status)
            return False

    def pause_vm(self) -> None:
        """Pause the microVM via PATCH /vm.

        Raises:
            FirecrackerError: If the pause operation fails.
        """
        logger.info("Pausing VM...")
        status, _ = self._request("PATCH", "/vm", {"state": "Paused"})

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("VM paused")
        else:
            raise FirecrackerError(f"Failed to pause VM: {status}")

    def resume_vm(self) -> None:
        """Resume a paused microVM via PATCH /vm.

        Raises:
            FirecrackerError: If the resume operation fails.
        """
        logger.info("Resuming VM...")
        status, _ = self._request("PATCH", "/vm", {"state": "Resumed"})

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("VM resumed")
        else:
            raise FirecrackerError(f"Failed to resume VM: {status}")


def get_vm_socket_path(vm_hash: str) -> Path | None:
    """Get socket path for a VM from cache directory by its hash."""
    from mvmctl.utils.fs import get_vm_dir_by_hash

    vm_dir = get_vm_dir_by_hash(vm_hash)
    for name in [
        DEFAULT_FC_API_SOCKET_FILENAME,
        "firecracker.socket",
        "firecracker.sock",
        "socket",
    ]:
        p = vm_dir / name
        if p.exists():
            return p
    return None
