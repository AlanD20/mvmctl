"""NoCloud-net server manager for coordinating VM cloud-init servers.

This module provides a thread-safe manager for NoCloudNetServer instances,
ensuring proper port allocation, server lifecycle management, and cleanup
of orphaned servers from crashed sessions.
"""

import logging
import socket
import threading
from pathlib import Path

from mvmctl.constants import (
    CONST_NO_CLOUD_NET_BIND_TIMEOUT_S,
    CONST_NO_CLOUD_NET_MAX_PORT_RETRIES,
    CONST_NO_CLOUD_NET_PORT_RANGE,
)
from mvmctl.core.nocloud_net_server import NoCloudNetServer
from mvmctl.exceptions import MVMError

logger = logging.getLogger(__name__)


class NoCloudNetServerManager:
    """Thread-safe manager for NoCloudNetServer instances.

    Coordinates HTTP servers for VM cloud-init datasource, ensuring
    proper port allocation and cleanup of orphaned servers.

    Attributes:
        _servers: Registry of active servers keyed by VM name
        _lock: Lock for thread-safe access to server registry
    """

    _servers: dict[str, NoCloudNetServer]
    _lock: threading.Lock

    def __init__(self) -> None:
        """Initialize the server manager."""
        self._servers = {}
        self._lock = threading.Lock()
        self.cleanup_orphans()

    def allocate_port(self, vm_name: str) -> int:
        """Find an available port with collision detection.

        Scans the configured port range (8000-9000) and uses socket.bind()
        to detect available ports. Respects max retry limit for collision
        avoidance.

        Args:
            vm_name: Name of the VM (used for logging context)

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
                    sock.bind(("0.0.0.0", port))
                    logger.debug("Allocated port %d for VM %s", port, vm_name)
                    return port
            except OSError:
                continue

        raise MVMError(
            f"No available port found in range {port_min}-{port_max} after {max_retries} attempts"
        )

    def start_server(self, vm_name: str, cloud_init_dir: Path, gateway_ip: str) -> tuple[str, int]:
        """Start a NoCloud-net server for the specified VM.

        Allocates a port, creates a server bound to gateway_ip (not 0.0.0.0),
        and starts it in a background thread.

        Args:
            vm_name: Unique name identifying the VM
            cloud_init_dir: Directory containing cloud-init files (meta-data,
                user-data, network-config)
            gateway_ip: IP address to bind the server to (typically the
                bridge gateway IP)

        Returns:
            Tuple of (url, port) where url is the base URL for cloud-init
            access and port is the allocated port number

        Raises:
            MVMError: If a server is already running for this VM or if
                port allocation fails
        """
        with self._lock:
            if vm_name in self._servers:
                raise MVMError(f"Server already running for VM: {vm_name}")

            port = self.allocate_port(vm_name)

            server = NoCloudNetServer(cloud_init_dir, port=port, host=gateway_ip)
            server.start()

            self._servers[vm_name] = server

            logger.info(
                "Started NoCloud-net server for VM %s on %s:%d",
                vm_name,
                gateway_ip,
                port,
            )

            return server.url, port

    def stop_server(self, vm_name: str) -> None:
        """Stop the server for the specified VM.

        Idempotent operation - safe to call multiple times. If no server
        exists for the VM, this is a no-op.

        Args:
            vm_name: Name of the VM whose server should be stopped
        """
        with self._lock:
            server = self._servers.get(vm_name)
            if server is None:
                logger.debug("No server running for VM %s", vm_name)
                return

            try:
                server.stop()
                logger.info("Stopped NoCloud-net server for VM %s", vm_name)
            except MVMError:
                # Server may have already stopped or crashed
                logger.warning("Error stopping server for VM %s", vm_name)
            finally:
                del self._servers[vm_name]

    def get_server(self, vm_name: str) -> NoCloudNetServer | None:
        """Get the running server for the specified VM.

        Args:
            vm_name: Name of the VM

        Returns:
            The NoCloudNetServer instance if running, None otherwise
        """
        with self._lock:
            return self._servers.get(vm_name)

    def cleanup_orphans(self) -> None:
        """Clean up any orphaned servers from previous crashed sessions.

        This method is called during initialization to ensure no stale
        servers remain. In the current implementation, orphan detection
        would require tracking server state externally (e.g., via PID files).
        This method serves as a hook for future enhancement.

        Note:
            Full orphan cleanup would need external state tracking since
            this manager only tracks servers it creates in-memory.
        """
        logger.debug("Running orphan cleanup check")
        # Currently a no-op since servers are tracked in-memory only.
        # External state tracking (PID files) would be needed for
        # true orphan detection from crashed sessions.
