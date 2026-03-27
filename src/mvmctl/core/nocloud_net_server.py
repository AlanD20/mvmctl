"""NoCloud-net HTTP server for cloud-init datasource.

This module provides an HTTP server that serves cloud-init files
(meta-data, user-data, network-config) to VMs via the nocloud-net
datasource mechanism.
"""

import logging
import socket
import threading
from http.server import HTTPServer, SimpleHTTPRequestHandler
from pathlib import Path
from typing import Any, Final

from mvmctl.constants import CONST_NO_CLOUD_NET_SHUTDOWN_TIMEOUT_S
from mvmctl.exceptions import MVMError

logger = logging.getLogger(__name__)

# Default port range for auto-discovery
DEFAULT_PORT_MIN: Final[int] = 8000
DEFAULT_PORT_MAX: Final[int] = 9000

# Host to bind to (0.0.0.0 for all interfaces)
DEFAULT_BIND_HOST: Final[str] = "0.0.0.0"


class _CloudInitRequestHandler(SimpleHTTPRequestHandler):
    """Custom request handler for cloud-init files.

    Serves files from the specified cloud-init directory with
    proper content types for cloud-init consumption.
    """

    def __init__(self, cloud_init_dir: Path, *args: Any, **kwargs: Any) -> None:
        self.cloud_init_dir = cloud_init_dir
        super().__init__(*args, directory=str(cloud_init_dir), **kwargs)

    def log_message(self, format: str, *args: Any) -> None:
        """Log HTTP requests at debug level."""
        logger.debug("NoCloud-net HTTP: %s", format % args)

    def end_headers(self) -> None:
        """Add headers to prevent caching."""
        self.send_header("Cache-Control", "no-store, no-cache, must-revalidate")
        self.send_header("Pragma", "no-cache")
        super().end_headers()


class NoCloudNetServer:
    """HTTP server for nocloud-net cloud-init datasource.

    Serves cloud-init configuration files (meta-data, user-data,
    network-config) via HTTP, allowing VMs to fetch their configuration
    from a URL rather than requiring a local ISO.

    Attributes:
        cloud_init_dir: Directory containing cloud-init files
        port: Port number the server is listening on (0 for auto-assign)
        host: Host address the server binds to
        url: Full URL to access the server

    Example:
        server = NoCloudNetServer(Path("/path/to/cloud-init"), port=0)
        server.start()
        print(f"Server running at {server.url}")
        # ... VM boots with ds=nocloud-net;s=http://host:port/
        server.stop()
    """

    def __init__(self, cloud_init_dir: Path, port: int = 0, host: str = DEFAULT_BIND_HOST):
        """Initialize the NoCloud-net HTTP server.

        Args:
            cloud_init_dir: Directory containing meta-data, user-data,
                and network-config files
            port: Port to listen on (0 for auto-assign available port)
            host: Host address to bind to (default: 0.0.0.0)

        Raises:
            MVMError: If cloud_init_dir does not exist or is not a directory
        """
        self.cloud_init_dir = Path(cloud_init_dir)
        if not self.cloud_init_dir.exists():
            raise MVMError(f"Cloud-init directory does not exist: {cloud_init_dir}")
        if not self.cloud_init_dir.is_dir():
            raise MVMError(f"Cloud-init path is not a directory: {cloud_init_dir}")

        self._requested_port = port
        self.host = host
        self._server: HTTPServer | None = None
        self._thread: threading.Thread | None = None
        self._port: int = 0
        self._running = False

    @property
    def port(self) -> int:
        """Return the actual port the server is listening on.

        Returns:
            Port number (0 if server not started)
        """
        return self._port

    @property
    def url(self) -> str:
        """Return the base URL for accessing cloud-init files.

        Returns:
            URL in format http://HOST:PORT/
        """
        return f"http://{self.host}:{self._port}/"

    def _find_available_port(self) -> int:
        """Find an available port in the default range.

        Returns:
            Available port number

        Raises:
            MVMError: If no available port found in range
        """
        for port in range(DEFAULT_PORT_MIN, DEFAULT_PORT_MAX + 1):
            try:
                with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                    sock.bind((self.host, port))
                    return port
            except OSError:
                continue
        raise MVMError(f"No available port found in range {DEFAULT_PORT_MIN}-{DEFAULT_PORT_MAX}")

    def _create_handler_class(self) -> type[SimpleHTTPRequestHandler]:
        """Create a request handler class bound to our cloud-init directory."""
        cloud_init_dir = self.cloud_init_dir

        class _BoundHandler(_CloudInitRequestHandler):
            def __init__(self, *args: Any, **kwargs: Any) -> None:
                super().__init__(cloud_init_dir, *args, **kwargs)

        return _BoundHandler

    def start(self) -> None:
        """Start the HTTP server in a background thread.

        If port was set to 0, automatically finds an available port.

        Raises:
            MVMError: If server fails to start or is already running
        """
        if self._running:
            raise MVMError("NoCloud-net server is already running")

        # Determine port to use
        if self._requested_port == 0:
            self._port = self._find_available_port()
        else:
            self._port = self._requested_port

        try:
            handler_class = self._create_handler_class()
            self._server = HTTPServer((self.host, self._port), handler_class)
        except OSError as e:
            raise MVMError(f"Failed to create HTTP server on port {self._port}: {e}") from e

        # Start server in daemon thread
        self._thread = threading.Thread(
            target=self._server.serve_forever,
            name=f"nocloud-net-server-{self._port}",
            daemon=True,
        )
        self._thread.start()
        self._running = True

        logger.info(
            "NoCloud-net HTTP server started on %s:%d (serving %s)",
            self.host,
            self._port,
            self.cloud_init_dir,
        )

    def stop(self) -> None:
        """Stop the HTTP server.

        Gracefully shuts down the server and waits for the background
        thread to complete.

        Raises:
            MVMError: If server is not running
        """
        if not self._running:
            raise MVMError("NoCloud-net server is not running")

        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()

        if self._thread is not None:
            self._thread.join(timeout=CONST_NO_CLOUD_NET_SHUTDOWN_TIMEOUT_S)
            if self._thread.is_alive():
                logger.warning("NoCloud-net server thread did not stop gracefully")

        self._running = False
        self._port = 0

        logger.info("NoCloud-net HTTP server stopped")

    def is_running(self) -> bool:
        """Check if the server is currently running.

        Returns:
            True if server is running, False otherwise
        """
        return self._running and self._thread is not None and self._thread.is_alive()

    def __enter__(self) -> "NoCloudNetServer":
        """Context manager entry - starts the server."""
        self.start()
        return self

    def __exit__(
        self, exc_type: type[BaseException] | None, exc_val: BaseException | None, exc_tb: Any
    ) -> None:
        """Context manager exit - stops the server."""
        self.stop()
