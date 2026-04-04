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
from mvmctl.models.vm import VMStatus

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

    def _try_bind_specific_port(self, vm_name: str, gateway_ip: str, port: int) -> int | None:
        """Try to bind to a specific port.

        Args:
            vm_name: Name of the VM (used for logging context)
            gateway_ip: IP address to bind the port to
            port: Specific port number to try

        Returns:
            The port number if successfully bound, None otherwise
        """
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                sock.settimeout(CONST_NO_CLOUD_NET_BIND_TIMEOUT_S)
                sock.bind((gateway_ip, port))
                logger.debug(
                    "Bound to requested port %d for VM %s on %s", port, vm_name, gateway_ip
                )
                return port
        except OSError:
            logger.debug(
                "Requested port %d not available for VM %s on %s", port, vm_name, gateway_ip
            )
            return None

    def _get_pid_file_path(self, vm_hash: str) -> Path:
        """Get the PID file path for a VM's nocloud server.

        Args:
            vm_hash: VM hash (64-char SHA256)

        Returns:
            Path to the nocloud-server.pid file
        """
        from mvmctl.utils.fs import get_vm_dir_by_hash

        return get_vm_dir_by_hash(vm_hash) / "nocloud-server.pid"

    def _stop_by_pid_file(self, vm_hash: str) -> bool:
        """Stop a server using only its PID file (recovery path).

        This method is used when the server is not tracked in memory
        but a PID file exists from a previous manager instance.

        Args:
            vm_hash: VM hash (64-char SHA256)

        Returns:
            True if a server was stopped using the PID file, False otherwise
        """
        pid_file = self._get_pid_file_path(vm_hash)

        if not pid_file.exists():
            logger.debug("No PID file found for VM hash %s", vm_hash)
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
                "Stopped NoCloud-net server via PID file recovery (PID: %d) for VM hash %s",
                pid,
                vm_hash,
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

    def start_server(
        self,
        vm_name: str,
        cloud_init_dir: Path,
        gateway_ip: str,
        vm_hash: str | None = None,
        preferred_port: int = 0,
    ) -> tuple[str, int]:
        """Start a NoCloud-net server for the specified VM.

        Allocates a port, creates a server subprocess bound to gateway_ip,
        and starts it as a persistent background process.

        Args:
            vm_name: Unique name identifying the VM (for tracking)
            cloud_init_dir: Directory containing cloud-init files (meta-data,
                user-data, network-config)
            gateway_ip: IP address to bind the server to (typically the
                bridge gateway IP, e.g., "10.20.0.1")
            vm_hash: VM hash (64-char SHA256) for PID file path. If None, uses vm_name.
            preferred_port: Preferred port number (0 for auto-allocation)

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

            # Try preferred port first if specified, otherwise auto-allocate
            port: int | None = None
            if preferred_port > 0:
                port = self._try_bind_specific_port(vm_name, gateway_ip, preferred_port)
            if port is None:
                port = self._allocate_port_for_gateway(vm_name, gateway_ip)

            # Create PID file path in VM directory (hash-based, fallback to name)
            lookup_key = vm_hash if vm_hash is not None else vm_name
            pid_file = self._get_pid_file_path(lookup_key)

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

    def stop_server(self, vm_name: str, vm_hash: str | None = None) -> None:
        """Stop the server for the specified VM.

        Idempotent operation - safe to call multiple times. If no server
        exists for the VM, this is a no-op.

        This method can stop servers that were started by a previous manager
        instance by reading from the PID file on disk.

        Args:
            vm_name: Name of the VM whose server should be stopped (for tracking)
            vm_hash: VM hash (64-char SHA256) for PID file path. If None, uses vm_name.
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
                lookup_key = vm_hash if vm_hash is not None else vm_name
                self._stop_by_pid_file(lookup_key)

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

    def get_server_pid(self, vm_name: str, vm_hash: str | None = None) -> int | None:
        """Get the PID of the running server for the specified VM.

        Args:
            vm_name: Name of the VM (for tracking)
            vm_hash: VM hash (64-char SHA256) for PID file path. If None, uses vm_name.

        Returns:
            The subprocess PID if running, None otherwise
        """
        with self._thread_lock:
            info = self._servers.get(vm_name)
            if info is None:
                # Try to get PID from PID file
                lookup_key = vm_hash if vm_hash is not None else vm_name
                return self._get_pid_from_file(lookup_key)
            return info.get("pid")

    def _get_pid_from_file(self, vm_hash: str) -> int | None:
        """Get PID from PID file if it exists and process is running.

        Args:
            vm_hash: VM hash (64-char SHA256)

        Returns:
            PID if file exists and process is running, None otherwise
        """
        pid_file = self._get_pid_file_path(vm_hash)
        if not pid_file.exists():
            return None

        try:
            pid = int(pid_file.read_text().strip())
            # Check if process exists
            os.kill(pid, 0)
            return pid
        except (ValueError, OSError, ProcessLookupError, PermissionError):
            return None

    def is_server_running(self, vm_name: str, vm_hash: str | None = None) -> bool:
        """Check if the server is currently running.

        Args:
            vm_name: Name of the VM (for tracking)
            vm_hash: VM hash (64-char SHA256) for PID file path. If None, uses vm_name.

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
            lookup_key = vm_hash if vm_hash is not None else vm_name
            return self._get_pid_from_file(lookup_key) is not None

    def cleanup_orphans(self) -> list[str]:
        """Clean up any orphaned servers from previous crashed sessions.

        This method is called during initialization to ensure no stale
        servers remain. It scans for nocloud-server.pid files where the
        associated VM is no longer running and stops those servers.

        Returns:
            List of VM hashes that were cleaned up
        """
        from mvmctl.core.vm_manager import get_vm_manager
        from mvmctl.utils.fs import get_cache_dir

        logger.debug("Running orphan cleanup check")
        cleaned_up: list[str] = []

        vms_dir = get_cache_dir() / "vms"
        if not vms_dir.exists():
            return cleaned_up

        # Get VM manager to check which VMs are running
        vm_manager = get_vm_manager()

        for vm_entry in vms_dir.iterdir():
            if not vm_entry.is_dir():
                continue

            vm_hash = vm_entry.name
            pid_file = vm_entry / "nocloud-server.pid"
            if not pid_file.exists():
                continue

            # Check if VM is registered and running (lookup by full hash to avoid collision risk)
            vm = vm_manager.get_by_full_id(vm_hash)
            if vm is None or vm.status != VMStatus.RUNNING:
                # VM is not running - this is an orphan, stop it
                if self._stop_by_pid_file(vm_hash):
                    cleaned_up.append(vm_hash)
                    logger.info("Cleaned up orphaned NoCloud-net server for VM hash %s", vm_hash)
                else:
                    # _stop_by_pid_file failed (e.g., invalid PID file)
                    # Try to clean up the PID file directly
                    try:
                        if pid_file.exists():
                            pid_file.unlink()
                            logger.info("Cleaned up orphaned PID file for VM hash %s", vm_hash)
                    except OSError:
                        pass
            else:
                # VM is running - check if process actually exists
                try:
                    pid = int(pid_file.read_text().strip())
                    # Check if process still exists
                    os.kill(pid, 0)
                    # Process exists and VM is running - not an orphan
                    logger.debug(
                        "Skipping orphan cleanup for VM hash %s - process %d still running",
                        vm_hash,
                        pid,
                    )
                except (ValueError, OSError):
                    # Can't read PID or PID file is invalid
                    try:
                        pid_file.unlink()
                        logger.info("Cleaned up invalid PID file for VM hash %s", vm_hash)
                    except OSError:
                        pass
                except ProcessLookupError:
                    # Process no longer exists but VM is marked running - clean up
                    try:
                        pid_file.unlink()
                        logger.info("Cleaned up stale PID file for VM hash %s", vm_hash)
                    except OSError:
                        pass
                except PermissionError:
                    # Process exists but we can't signal it - leave it alone
                    logger.debug(
                        "Skipping orphan cleanup for VM hash %s - permission denied on process %d",
                        vm_hash,
                        pid,
                    )

        return cleaned_up
