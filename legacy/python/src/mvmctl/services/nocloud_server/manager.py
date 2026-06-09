"""
NoCloud-net server manager for coordinating VM cloud-init servers.

This module provides a manager for NoCloudNetServer subprocess instances,
ensuring proper port allocation and server lifecycle management.

The server runs as a subprocess that survives beyond the CLI process lifetime,
providing better isolation and reliability compared to thread-based servers.
"""

from __future__ import annotations

import logging
import os
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path

from mvmctl.services.nocloud_server._defaults import (
    DEFAULT_NOCLOUD_LOG_FILENAME,
    DEFAULT_NOCLOUD_PID_FILENAME,
)
from mvmctl.services.nocloud_server.exceptions import (
    NoCloudServerAlreadyRunningError,
    NoCloudServerError,
)

logger = logging.getLogger(__name__)


class NoCloudNetServerManager:
    """
    Manager for a single NoCloud-net server subprocess instance.

    Coordinates one NoCloud-net HTTP server, ensuring proper
    lifecycle management and cleanup.

    Attributes:
        _id: Unique identifier for this server (VM hash)
        _path: Directory path for PID file and cloud-init files
        _name: Human-readable name for logging
        _ipv4_gateway: IP address to bind the server to
        _port: Port number the server is bound to
        _pid: Process ID of the running server subprocess
        _url: Base URL for cloud-init access
        _pid_path: Full path to the PID file
        _log_path: Full path to the log file
        _lock: Lock for thread-safe access

    """

    _pid: int | None = None
    _url: str | None = None

    def __init__(
        self,
        *,
        id: str,
        path: Path,
        ipv4_gateway: str,
        port: int,
        name: str | None = None,
        pid_filename: str = DEFAULT_NOCLOUD_PID_FILENAME,
        log_filename: str = DEFAULT_NOCLOUD_LOG_FILENAME,
        port_range_start: int = 8000,
        port_range_end: int = 9000,
        max_port_retries: int = 100,
    ) -> None:
        """
        Initialize the server manager for a specific VM.

        Args:
            id: Unique identifier for this server (VM hash)
            path: Directory path where PID file and cloud-init files are located
            ipv4_gateway: IP address to bind the server to
            port: Port number to use for the server (0 for auto-allocation)
            name: Human-readable name for logging (uses id if None)
            pid_filename: Name of the PID file (default: nocloud-server.pid)
            log_filename: Name of the log file (default: cloud-init.log)
            port_range_start: Start of port range for auto-allocation (default: 8000)
            port_range_end: End of port range for auto-allocation (default: 9000)
            max_port_retries: Max ports to try before giving up (default: 100)

        """
        if port_range_end <= port_range_start:
            raise ValueError(
                f"Port range end ({port_range_end}) must be greater than start ({port_range_start})"
            )
        self._id = id
        self._path = path
        self._ipv4_gateway = ipv4_gateway
        self._port = port
        self._name = name or id
        self._pid_path = path / pid_filename
        self._log_path = path / log_filename
        self._port_range_start = port_range_start
        self._port_range_end = port_range_end
        self._max_port_retries = max_port_retries
        self._lock: threading.Lock | None = None

    @property
    def _thread_lock(self) -> threading.Lock:
        """Lazy initialization of threading lock."""
        if self._lock is None:
            self._lock = threading.Lock()
        return self._lock

    @property
    def id(self) -> str:
        return self._id

    @property
    def name(self) -> str:
        return self._name

    @property
    def pid(self) -> int | None:
        return self._pid

    @property
    def port(self) -> int | None:
        return self._port

    @property
    def url(self) -> str | None:
        return self._url

    @property
    def pid_path(self) -> Path:
        return self._pid_path

    @property
    def log_path(self) -> Path:
        return self._log_path

    def _send_signal(self, pid: int, sig: int) -> bool:
        try:
            os.kill(pid, sig)
            return True
        except ProcessLookupError:
            logger.debug(
                "NoCloud-net server process (PID: %d) already terminated", pid
            )
            return False
        except PermissionError:
            logger.warning(
                "Cannot signal NoCloud-net server (PID: %d) - permission denied",
                pid,
            )
            return False

    def _cleanup_file(self) -> None:
        if self._pid_path.exists():
            try:
                self._pid_path.unlink()
            except OSError:
                pass

    def start(self) -> tuple[str, int, int]:
        """
        Start the NoCloud-net server subprocess.

        Returns:
            Tuple of (url, port, pid)

        Raises:
            NoCloudServerAlreadyRunningError: If server is already running
            NoCloudServerError: If subprocess fails to start or no port available

        """
        with self._thread_lock:
            if self._pid is not None:
                raise NoCloudServerAlreadyRunningError(
                    f"NoCloud-net server already running for ID: {self._id}"
                )

            # Auto-allocate port from range if requested
            if self._port == 0:
                import socket

                from mvmctl.utils.common import CacheUtils

                bin_dir = CacheUtils.get_bin_dir()
                binary = bin_dir / "mvm-nocloud-server"

                allocated = False
                for port in range(
                    self._port_range_start, self._port_range_end + 1
                ):
                    try:
                        with socket.socket(
                            socket.AF_INET, socket.SOCK_STREAM
                        ) as s:
                            s.setsockopt(
                                socket.SOL_SOCKET, socket.SO_REUSEADDR, 1
                            )
                            s.bind((self._ipv4_gateway, port))
                    except OSError:
                        continue

                    # Build server_cmd for this port
                    if binary.exists():
                        server_cmd = [
                            str(binary),
                            "--cloud-init-dir",
                            str(self._path),
                            "--port",
                            str(port),
                            "--host",
                            self._ipv4_gateway,
                            "--pid-file",
                            str(self._pid_path),
                            "--log-file",
                            str(self._log_path),
                        ]
                    else:
                        server_cmd = [
                            sys.executable,
                            "-m",
                            "mvmctl.services.nocloud_server.process",
                            "--cloud-init-dir",
                            str(self._path),
                            "--port",
                            str(port),
                            "--host",
                            self._ipv4_gateway,
                            "--pid-file",
                            str(self._pid_path),
                            "--log-file",
                            str(self._log_path),
                        ]

                    try:
                        proc = subprocess.Popen(
                            server_cmd,
                            stdin=subprocess.DEVNULL,
                            stdout=subprocess.DEVNULL,
                            stderr=subprocess.DEVNULL,
                            start_new_session=True,
                        )
                    except OSError:
                        continue

                    # Verify process is alive (wait briefly for initialization)
                    time.sleep(0.2)
                    if proc.poll() is not None:
                        # Process died immediately — likely port conflict
                        continue

                    self._pid = proc.pid
                    self._port = port
                    self._url = f"http://{self._ipv4_gateway}:{port}/"
                    allocated = True

                    logger.info(
                        "Started NoCloud-net server for %s on %s:%d (PID: %d)",
                        self._name,
                        self._ipv4_gateway,
                        port,
                        proc.pid,
                    )

                    return self._url, self._port, proc.pid

                if not allocated:
                    raise NoCloudServerError(
                        f"No available port in range "
                        f"{self._port_range_start}-{self._port_range_end}"
                    )

            # Pre-allocated port case (self._port != 0)
            # Try compiled binary first, fall back to sys.executable -m
            from mvmctl.utils.common import CacheUtils

            bin_dir = CacheUtils.get_bin_dir()
            binary = bin_dir / "mvm-nocloud-server"
            if binary.exists():
                server_cmd = [
                    str(binary),
                    "--cloud-init-dir",
                    str(self._path),
                    "--port",
                    str(self._port),
                    "--host",
                    self._ipv4_gateway,
                    "--pid-file",
                    str(self._pid_path),
                    "--log-file",
                    str(self._log_path),
                ]
            else:
                server_cmd = [
                    sys.executable,
                    "-m",
                    "mvmctl.services.nocloud_server.process",
                    "--cloud-init-dir",
                    str(self._path),
                    "--port",
                    str(self._port),
                    "--host",
                    self._ipv4_gateway,
                    "--pid-file",
                    str(self._pid_path),
                    "--log-file",
                    str(self._log_path),
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
                raise NoCloudServerError(
                    f"Failed to spawn nocloud-net server process: {e}"
                ) from e

            # Verify process is alive
            time.sleep(0.2)
            if proc.poll() is not None:
                raise NoCloudServerError(
                    f"Pre-allocated port {self._port} — "
                    f"spawned nocloud-net server exited immediately"
                )

            self._pid = proc.pid
            self._url = f"http://{self._ipv4_gateway}:{self._port}/"

            logger.info(
                "Started NoCloud-net server for %s on %s:%d (PID: %d)",
                self._name,
                self._ipv4_gateway,
                self._port,
                proc.pid,
            )

            return self._url, self._port, proc.pid

    def stop(self) -> bool:
        """
        Stop the NoCloud-net server gracefully.

        Returns:
            True if a server was stopped, False otherwise

        """
        with self._thread_lock:
            if self._pid is None:
                return False

            self._send_signal(self._pid, signal.SIGTERM)
            self._cleanup_file()
            self._pid = None
            logger.info("Terminated NoCloud-net server for %s", self._name)
            return True

    def terminate(self) -> bool:
        """
        Forcefully terminate the NoCloud-net server.

        Returns:
            True if server was terminated, False if no server was running

        """
        with self._thread_lock:
            if self._pid is None:
                return False

            self._send_signal(self._pid, signal.SIGTERM)
            self._cleanup_file()
            self._pid = None
            logger.info("Terminated NoCloud-net server for %s", self._name)
            return True

    def is_running(self) -> bool:
        """
        Check if the server is currently running.

        Returns:
            True if server is running, False otherwise

        """
        with self._thread_lock:
            if self._pid is not None:
                return self._send_signal(self._pid, 0)
            return False
