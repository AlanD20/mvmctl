"""Firecracker API client over Unix socket."""

import http.client
import json
import socket
from pathlib import Path
from typing import Optional, Any

from fcm.utils.console import print_error, print_success, print_info


class UnixSocketHTTPConnection(http.client.HTTPConnection):
    """HTTP connection over Unix domain socket."""

    def __init__(self, socket_path: Path):
        self.socket_path = socket_path
        super().__init__("localhost")

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.connect(str(self.socket_path))


class FirecrackerClient:
    """Firecracker API client."""

    def __init__(self, socket_path: Path):
        self.socket_path = Path(socket_path)
        self.conn: Optional[UnixSocketHTTPConnection] = None

    def _connect(self) -> bool:
        """Connect to Firecracker socket."""
        if not self.socket_path.exists():
            print_error(f"Socket not found: {self.socket_path}")
            return False

        try:
            self.conn = UnixSocketHTTPConnection(self.socket_path)
            return True
        except Exception as e:
            print_error(f"Failed to connect to socket: {e}")
            return False

    def _request(
        self,
        method: str,
        path: str,
        body: Optional[dict] = None,
    ) -> tuple[int, Optional[dict]]:
        """Make HTTP request to Firecracker API."""
        if not self.conn:
            if not self._connect():
                return 0, None

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

        except Exception as e:
            print_error(f"API request failed: {e}")
            return 0, None

    def close(self):
        """Close connection."""
        if self.conn:
            self.conn.close()
            self.conn = None

    def pause_vm(self) -> bool:
        """Pause the VM.

        Returns:
            True if successful, False otherwise
        """
        print_info("Pausing VM...")
        status, data = self._request("PATCH", "/vm")

        if status == 204:
            print_success("VM paused")
            return True
        else:
            print_error(f"Failed to pause VM: {status}")
            if data:
                print_error(f"Response: {data}")
            return False

    def resume_vm(self) -> bool:
        """Resume the VM.

        Returns:
            True if successful, False otherwise
        """
        print_info("Resuming VM...")
        status, data = self._request("PATCH", "/vm", {"state": "Resumed"})

        if status == 204:
            print_success("VM resumed")
            return True
        else:
            print_error(f"Failed to resume VM: {status}")
            if data:
                print_error(f"Response: {data}")
            return False

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
            True if successful, False otherwise
        """
        print_info("Creating snapshot...")

        body = {
            "mem_file_path": str(mem_path),
            "snapshot_path": str(snapshot_path),
        }

        status, data = self._request("PUT", "/snapshot/create", body)

        if status == 204:
            print_success(f"Snapshot created")
            print_info(f"  Memory: {mem_path}")
            print_info(f"  State: {snapshot_path}")
            return True
        else:
            print_error(f"Failed to create snapshot: {status}")
            if data:
                print_error(f"Response: {data}")
            return False

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
            True if successful, False otherwise
        """
        print_info("Loading snapshot...")

        body = {
            "mem_file_path": str(mem_path),
            "snapshot_path": str(snapshot_path),
            "resume_vm": resume,
        }

        status, data = self._request("PUT", "/snapshot/load", body)

        if status == 204:
            print_success("Snapshot loaded")
            return True
        else:
            print_error(f"Failed to load snapshot: {status}")
            if data:
                print_error(f"Response: {data}")
            return False

    def get_instance_info(self) -> Optional[dict]:
        """Get VM instance information.

        Returns:
            Instance info dict or None
        """
        status, data = self._request("GET", "/")

        if status == 200 and data:
            return data
        return None

    def describe_instance(self) -> Optional[dict]:
        """Describe the VM instance.

        Returns:
            Instance description or None
        """
        status, data = self._request("GET", "/vm")

        if status == 200 and data:
            return data
        return None

    def start_instance(self) -> bool:
        """Start the VM instance.

        Returns:
            True if successful, False otherwise
        """
        print_info("Starting VM...")
        status, data = self._request("PUT", "/actions", {"action_type": "InstanceStart"})

        if status == 204:
            print_success("VM started")
            return True
        else:
            print_error(f"Failed to start VM: {status}")
            return False

    def send_ctrl_alt_del(self) -> bool:
        """Send Ctrl+Alt+Del to VM.

        Returns:
            True if successful, False otherwise
        """
        status, data = self._request("PUT", "/actions", {"action_type": "SendCtrlAltDel"})

        if status == 204:
            print_success("Ctrl+Alt+Del sent")
            return True
        else:
            print_error(f"Failed to send Ctrl+Alt+Del: {status}")
            return False


def get_vm_socket_path(vm_name: str, multi_vm_dir: Path) -> Optional[Path]:
    """Get socket path for a VM.

    Args:
        vm_name: VM name
        multi_vm_dir: Path to multi-vm directory

    Returns:
        Socket path or None if not found
    """
    vm_dir = multi_vm_dir / "env" / vm_name
    socket_path = vm_dir / f"{vm_name}.socket"

    if socket_path.exists():
        return socket_path

    # Try alternative names
    for alt_name in ["firecracker.socket", "socket"]:
        alt_path = vm_dir / alt_name
        if alt_path.exists():
            return alt_path

    return None
