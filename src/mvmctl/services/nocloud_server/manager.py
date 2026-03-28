"""NoCloud-net server manager for coordinating VM cloud-init servers.

This module provides a manager for NoCloudNetServer subprocess instances,
ensuring proper port allocation, server lifecycle management, and cleanup
of orphaned servers from crashed sessions.

The server runs as a subprocess that survives beyond the CLI process lifetime,
providing better isolation and reliability compared to thread-based servers.
"""

import logging
import os
import signal
import socket
import subprocess
import sys
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    CONST_NO_CLOUD_NET_BIND_TIMEOUT_S,
    CONST_NO_CLOUD_NET_MAX_PORT_RETRIES,
    CONST_NO_CLOUD_NET_PORT_RANGE,
)
from mvmctl.exceptions import MVMError

logger = logging.getLogger(__name__)


class NoCloudNetServerManager:
    """Manager for NoCloudNetServer subprocess instances.

    Coordinates HTTP servers for VM cloud-init datasource, ensuring
    proper port allocation and cleanup of orphaned servers.

    The server runs as a subprocess that persists beyond CLI process lifetime,
    using PID files for cleanup tracking.

    Attributes:
        _servers: Registry of active servers keyed by VM name
        _lock: Lock for thread-safe access to server registry
    """

    _servers: dict[str, dict[str, Any]]

    def __init__(self) -> None:
        """Initialize the server manager."""
        self._servers: dict[str, dict[str, object]] = {}
        self._lock: object = None  # Will be initialized lazily
        self.cleanup_orphans()

    @property
    def _thread_lock(self) -> Any:
        """Lazy initialization of threading lock."""
        import threading

        if self._lock is None:
            self._lock = threading.Lock()
        return self._lock

    def _allocate_port_for_gateway(self, vm_name: str, gateway_ip: str) -> int:
        """Find an available port by binding to gateway_ip.

        Scans the configured port range (8000-9000) and uses socket.bind()
        to detect available ports. Binds specifically to the gateway IP
        to ensure the port is available for VM connectivity.

        Args:
            vm_name: Name of the VM (used for logging context)
            gateway_ip: IP address to bind the port to

        Returns:
            Available port number in the configured range

        Raises:
            MVMError: If no available port found after max retries
        """
        port_min, port_max = CONST_NO_CLOUD_NET_PORT_RANGE
        max_retries = CONST_NO_CLOUD_NET_MAX_PORT_RETRIES

        for attempt in range(max_retries):
            port = port_min + (attempt % (port_max - port_min + 1))
            try:
                with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                    sock.settimeout(CONST_NO_CLOUD_NET_BIND_TIMEOUT_S)
                    sock.bind((gateway_ip, port))
                    logger.debug("Allocated port %d for VM %s on %s", port, vm_name, gateway_ip)
                    return port
            except OSError:
                continue

        raise MVMError(
            f"No available port found in range {port_min}-{port_max} after {max_retries} attempts"
        )

    def _get_pid_file_path(self, vm_name: str) -> Path:
        """Get the PID file path for a VM's nocloud server.

        Args:
            vm_name: Name of the VM

        Returns:
            Path to the nocloud-server.pid file
        """
        from mvmctl.utils.fs import get_vm_dir

        return get_vm_dir(vm_name) / "nocloud-server.pid"

    def _stop_by_pid_file(self, vm_name: str) -> bool:
        """Stop a server using only its PID file (recovery path).

        This method is used when the server is not tracked in memory
        but a PID file exists from a previous manager instance.

        Args:
            vm_name: Name of the VM whose server should be stopped

        Returns:
            True if a server was stopped using the PID file, False otherwise
        """
        pid_file = self._get_pid_file_path(vm_name)

        if not pid_file.exists():
            logger.debug("No PID file found for VM %s", vm_name)
            return False

        try:
            pid_text = pid_file.read_text().strip()
            pid = int(pid_text)
        except (ValueError, OSError) as e:
            logger.debug("Could not read PID from file %s: %s", pid_file, e)
            return False

        try:
            # Check if process exists
            os.kill(pid, 0)
            # Process exists, send SIGTERM
            os.kill(pid, signal.SIGTERM)
            logger.info(
                "Stopped NoCloud-net server via PID file recovery (PID: %d) for VM %s",
                pid,
                vm_name,
            )
        except ProcessLookupError:
            logger.debug("NoCloud-net server process (PID: %d) already terminated", pid)
        except PermissionError:
            logger.warning("Cannot kill NoCloud-net server (PID: %d) - permission denied", pid)
            # Still try to clean up the PID file even if we can't kill

        # Always clean up the PID file
        try:
            pid_file.unlink()
        except OSError:
            pass

        return True

    def start_server(self, vm_name: str, cloud_init_dir: Path, gateway_ip: str) -> tuple[str, int]:
        """Start a NoCloud-net server for the specified VM.

        Allocates a port, creates a server subprocess bound to gateway_ip,
        and starts it as a persistent background process.

        Args:
            vm_name: Unique name identifying the VM
            cloud_init_dir: Directory containing cloud-init files (meta-data,
                user-data, network-config)
            gateway_ip: IP address to bind the server to (typically the
                bridge gateway IP, e.g., "10.20.0.1")

        Returns:
            Tuple of (url, port) where url is the base URL for cloud-init
            access and port is the allocated port number

        Raises:
            MVMError: If a server is already running for this VM or if
                port allocation fails
        """
        with self._thread_lock:
            if vm_name in self._servers:
                raise MVMError(f"Server already running for VM: {vm_name}")

            # Allocate port by binding to gateway_ip
            port = self._allocate_port_for_gateway(vm_name, gateway_ip)

            # Create PID file path in VM directory
            pid_file = self._get_pid_file_path(vm_name)

            # Spawn subprocess via sys.executable -m mvmctl.services.nocloud_server.process
            server_cmd = [
                sys.executable,
                "-m",
                "mvmctl.services.nocloud_server.process",
                "--cloud-init-dir",
                str(cloud_init_dir),
                "--port",
                str(port),
                "--host",
                gateway_ip,
                "--pid-file",
                str(pid_file),
            ]

            try:
                proc = subprocess.Popen(
                    server_cmd,
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    start_new_session=True,
                )
            except OSError as e:
                raise MVMError(f"Failed to spawn nocloud-net server process: {e}") from e

            # Store process info
            self._servers[vm_name] = {
                "pid": proc.pid,
                "port": port,
                "pid_file": pid_file,
                "gateway_ip": gateway_ip,
            }

            logger.info(
                "Started NoCloud-net server for VM %s on %s:%d (PID: %d)",
                vm_name,
                gateway_ip,
                port,
                proc.pid,
            )

            return f"http://{gateway_ip}:{port}/", port

    def stop_server(self, vm_name: str) -> None:
        """Stop the server for the specified VM.

        Idempotent operation - safe to call multiple times. If no server
        exists for the VM, this is a no-op.

        This method can stop servers that were started by a previous manager
        instance by reading from the PID file on disk.

        Args:
            vm_name: Name of the VM whose server should be stopped
        """
        with self._thread_lock:
            info = self._servers.get(vm_name)
            if info is not None:
                # Server is tracked in memory - use normal path
                pid = info["pid"]
                pid_file = info["pid_file"]

                try:
                    # Send SIGTERM to kill subprocess
                    os.kill(pid, signal.SIGTERM)
                    logger.info(
                        "Sent SIGTERM to NoCloud-net server (PID: %d) for VM %s",
                        pid,
                        vm_name,
                    )
                except ProcessLookupError:
                    logger.debug("NoCloud-net server process (PID: %d) already terminated", pid)
                except PermissionError:
                    logger.warning(
                        "Cannot kill NoCloud-net server (PID: %d) - permission denied", pid
                    )

                # Clean up PID file
                if pid_file and pid_file.exists():
                    try:
                        pid_file.unlink()
                    except OSError:
                        pass

                del self._servers[vm_name]
                logger.info("Stopped NoCloud-net server for VM %s", vm_name)
            else:
                # Server not tracked in memory - try PID file recovery
                self._stop_by_pid_file(vm_name)

    def get_server(self, vm_name: str) -> None:
        """Get the running server info for the specified VM.

        Note:
            This method now returns None as the server runs as a subprocess.
            Use start_server() and stop_server() for server lifecycle management.

        Args:
            vm_name: Name of the VM

        Returns:
            Always returns None (server info is stored internally)
        """
        with self._thread_lock:
            _ = vm_name  # Acknowledge parameter
            # Server now runs as subprocess - return None
            return None

    def get_server_pid(self, vm_name: str) -> int | None:
        """Get the PID of the running server for the specified VM.

        Args:
            vm_name: Name of the VM

        Returns:
            The subprocess PID if running, None otherwise
        """
        with self._thread_lock:
            info = self._servers.get(vm_name)
            if info is None:
                # Try to get PID from PID file
                return self._get_pid_from_file(vm_name)
            return info.get("pid")

    def _get_pid_from_file(self, vm_name: str) -> int | None:
        """Get PID from PID file if it exists and process is running.

        Args:
            vm_name: Name of the VM

        Returns:
            PID if file exists and process is running, None otherwise
        """
        pid_file = self._get_pid_file_path(vm_name)
        if not pid_file.exists():
            return None

        try:
            pid = int(pid_file.read_text().strip())
            # Check if process exists
            os.kill(pid, 0)
            return pid
        except (ValueError, OSError, ProcessLookupError, PermissionError):
            return None

    def is_server_running(self, vm_name: str) -> bool:
        """Check if the server is currently running.

        Args:
            vm_name: Name of the VM

        Returns:
            True if server is running, False otherwise
        """
        with self._thread_lock:
            info = self._servers.get(vm_name)
            if info is not None:
                pid = info.get("pid")
                if pid is None:
                    return False

                try:
                    os.kill(pid, 0)
                    return True
                except (ProcessLookupError, PermissionError):
                    return False

            # Not in memory - check PID file
            return self._get_pid_from_file(vm_name) is not None

    def cleanup_orphans(self) -> None:
        """Clean up any orphaned servers from previous crashed sessions.

        This method is called during initialization to ensure no stale
        servers remain. It scans for nocloud-server.pid files where the
        associated processes are no longer running and cleans them up.

        Note:
            Only cleans up PID files for processes that have terminated.
            Active servers started by other manager instances are left alone.
        """
        from mvmctl.utils.fs import get_cache_dir

        logger.debug("Running orphan cleanup check")

        vms_dir = get_cache_dir() / "vms"
        if not vms_dir.exists():
            return

        for vm_entry in vms_dir.iterdir():
            if not vm_entry.is_dir():
                continue

            pid_file = vm_entry / "nocloud-server.pid"
            if not pid_file.exists():
                continue

            try:
                pid = int(pid_file.read_text().strip())
                # Check if process still exists
                os.kill(pid, 0)
                # Process still running - don't clean up
                logger.debug(
                    "Skipping orphan cleanup for VM %s - process %d still running",
                    vm_entry.name,
                    pid,
                )
            except (ValueError, OSError):
                # Can't read PID or PID file is invalid
                try:
                    pid_file.unlink()
                    logger.info("Cleaned up invalid PID file for VM %s", vm_entry.name)
                except OSError:
                    pass
            except ProcessLookupError:
                # Process no longer exists - clean up PID file
                try:
                    pid_file.unlink()
                    logger.info(
                        "Cleaned up stale PID file for VM %s (process terminated)",
                        vm_entry.name,
                    )
                except OSError:
                    pass
            except PermissionError:
                # Process exists but we can't signal it - leave it alone
                logger.debug(
                    "Skipping orphan cleanup for VM %s - permission denied on process %d",
                    vm_entry.name,
                    pid,
                )
