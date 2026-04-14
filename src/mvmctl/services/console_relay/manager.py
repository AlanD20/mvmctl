"""Console relay manager for VM serial console sessions.

Manages the lifecycle of console relay processes, including starting,
stopping, and cleanup of relay instances.
"""

import logging
import os
import signal
import subprocess
import sys
import threading
from pathlib import Path
from typing import Any

from mvmctl.services.console_relay._defaults import (
    CONST_CONSOLE_KILL_TIMEOUT_S,
    DEFAULT_CONSOLE_LOG_FILENAME,
    DEFAULT_CONSOLE_PID_FILENAME,
    DEFAULT_CONSOLE_SOCKET_FILENAME,
)
from mvmctl.services.console_relay.exceptions import (
    ConsoleRelayAlreadyRunningError,
    ConsoleRelayProcessError,
)

logger = logging.getLogger(__name__)


class ConsoleRelayManager:
    """Manager for a single console relay subprocess instance.

    Coordinates one console relay process, ensuring proper
    lifecycle management and cleanup.

    Attributes:
        _id: Unique identifier for this relay
        _path: Directory path for socket, PID, and log files
        _name: Human-readable name for logging
        _info: Relay info dict or None if not running
        _lock: Lock for thread-safe access
    """

    def __init__(
        self,
        id: str,
        path: Path,
        name: str | None = None,
        pid_filename: str = DEFAULT_CONSOLE_PID_FILENAME,
        socket_filename: str = DEFAULT_CONSOLE_SOCKET_FILENAME,
        log_filename: str = DEFAULT_CONSOLE_LOG_FILENAME,
    ) -> None:
        """Initialize the relay manager for a specific resource.

        Args:
            id: Unique identifier for registry and file paths
            path: Directory path where socket, PID, and log files will be created
            name: Human-readable name for logging (uses id if None)
            pid_filename: Name of the PID file (default: console.pid)
            socket_filename: Name of the socket file (default: console.sock)
            log_filename: Name of the log file (default: firecracker.console.log)
        """
        self._id = id
        self._path = path
        self._name = name or id
        self._pid_filename = pid_filename
        self._socket_filename = socket_filename
        self._log_filename = log_filename
        self._info: dict[str, Any] | None = None
        self._lock: threading.Lock | None = None

    @property
    def _thread_lock(self) -> threading.Lock:
        """Lazy initialization of threading lock."""
        if self._lock is None:
            self._lock = threading.Lock()
        return self._lock

    @property
    def id(self) -> str:
        """Return the relay's unique identifier."""
        return self._id

    @property
    def name(self) -> str:
        """Return the relay's human-readable name."""
        return self._name

    def start(self, pty_controller_fd: int) -> tuple[Path, int]:
        """Start the console relay subprocess.

        Spawns a relay subprocess that reads from the PTY controller and
        writes to both the console.log file and a Unix socket.

        Args:
            pty_controller_fd: File descriptor of the PTY controller (primary)

        Returns:
            Tuple of (socket_path, pid)

        Raises:
            ConsoleRelayAlreadyRunningError: If relay is already running
        """
        with self._thread_lock:
            if self._info is not None:
                raise ConsoleRelayAlreadyRunningError(
                    f"Console relay already running for ID: {self._id}"
                )

            socket_path = self._path / self._socket_filename
            pid_file = self._path / self._pid_filename
            log_file = self._path / self._log_filename

            relay_cmd = [
                sys.executable,
                "-m",
                "mvmctl.services.console_relay.process",
                "--id",
                self._id,
                "--name",
                self._name,
                "--pty-controller-fd",
                str(pty_controller_fd),
                "--socket-path",
                str(socket_path),
                "--pid-file",
                str(pid_file),
                "--log-file",
                str(log_file),
            ]

            try:
                proc = subprocess.Popen(
                    relay_cmd,
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    start_new_session=True,
                    pass_fds=[pty_controller_fd],
                )
            except OSError as e:
                raise ConsoleRelayProcessError(f"Failed to spawn console relay process: {e}") from e

            self._info = {
                "name": self._name,
                "pid": proc.pid,
                "socket_path": socket_path,
                "pid_file": pid_file,
            }

            logger.info(
                "Started console relay for %s (ID: %s, PID: %d)",
                self._name,
                self._id,
                proc.pid,
            )

            return socket_path, proc.pid

    def stop(self) -> None:
        """Stop the console relay.

        Idempotent operation - safe to call multiple times. If no relay
        is running, this is a no-op.
        """
        with self._thread_lock:
            if self._info is not None:
                pid = int(self._info["pid"])
                pid_file = self._info["pid_file"]
                socket_path = self._info["socket_path"]

                try:
                    os.kill(pid, signal.SIGTERM)
                    logger.info(
                        "Sent SIGTERM to console relay (PID: %d) for %s",
                        pid,
                        self._name,
                    )
                except ProcessLookupError:
                    logger.debug("Console relay process (PID: %d) already terminated", pid)
                except PermissionError:
                    logger.warning("Cannot kill console relay (PID: %d) - permission denied", pid)

                if isinstance(pid_file, Path) and pid_file.exists():
                    try:
                        pid_file.unlink()
                    except OSError:
                        pass

                if isinstance(socket_path, Path) and socket_path.exists():
                    try:
                        socket_path.unlink()
                    except OSError:
                        pass

                self._info = None
                logger.info("Stopped console relay for %s", self._name)
            else:
                self._stop_by_path()

    def _stop_by_path(self) -> bool:
        """Stop a relay using only its PID file (recovery path).

        Returns:
            True if a relay was stopped using the PID file, False otherwise
        """
        pid_file = self._path / self._pid_filename

        if not pid_file.exists():
            logger.debug("No PID file found at %s", pid_file)
            return False

        try:
            pid_text = pid_file.read_text().strip()
            pid = int(pid_text)
        except (ValueError, OSError) as e:
            logger.debug("Could not read PID from file %s: %s", pid_file, e)
            return False

        try:
            os.kill(pid, 0)
            os.kill(pid, signal.SIGTERM)
            logger.info(
                "Stopped console relay via PID file recovery (PID: %d) for %s",
                pid,
                self._name,
            )
        except ProcessLookupError:
            logger.debug("Console relay process (PID: %d) already terminated", pid)
        except PermissionError:
            logger.warning("Cannot kill console relay (PID: %d) - permission denied", pid)

        try:
            pid_file.unlink()
        except OSError:
            pass

        socket_path = self._path / self._socket_filename
        if socket_path.exists():
            try:
                socket_path.unlink()
            except OSError:
                pass

        return True

    def terminate(self) -> bool:
        """Forcefully terminate the console relay.

        Sends SIGTERM first, waits up to CONST_CONSOLE_KILL_TIMEOUT_S seconds,
        then sends SIGKILL if still running.

        Returns:
            True if relay was terminated, False if no relay was running
        """
        with self._thread_lock:
            pid_file = None
            pid = None

            if self._info is not None:
                pid = self._info["pid"]
                pid_file = self._info["pid_file"]
            else:
                pid_file = self._path / self._pid_filename
                if pid_file.exists():
                    try:
                        pid = int(pid_file.read_text().strip())
                    except (ValueError, OSError):
                        pass

            if pid is None:
                return False

            try:
                os.kill(pid, 0)
            except (ProcessLookupError, PermissionError):
                if pid_file and pid_file.exists():
                    try:
                        pid_file.unlink()
                    except OSError:
                        pass
                self._info = None
                return False

            try:
                os.kill(pid, signal.SIGTERM)
                import time

                for _ in range(int(CONST_CONSOLE_KILL_TIMEOUT_S * 10)):
                    time.sleep(0.1)
                    try:
                        os.kill(pid, 0)
                    except ProcessLookupError:
                        break
                else:
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
            except ProcessLookupError:
                pass
            except PermissionError:
                logger.warning("Cannot kill console relay (PID: %d) - permission denied", pid)

            if pid_file and pid_file.exists():
                try:
                    pid_file.unlink()
                except OSError:
                    pass

            socket_path = self._path / self._socket_filename
            if socket_path.exists():
                try:
                    socket_path.unlink()
                except OSError:
                    pass

            self._info = None

            logger.info("Terminated console relay for %s", self._name)
            return True

    def get_pid(self) -> int | None:
        """Get the PID of the running relay.

        Returns:
            The subprocess PID if running, None otherwise
        """
        with self._thread_lock:
            if self._info is not None:
                return int(self._info["pid"])
            return self._get_pid_from_path()

    def _get_pid_from_path(self) -> int | None:
        """Get PID from PID file if it exists and process is running.

        Returns:
            PID if file exists and process is running, None otherwise
        """
        pid_file = self._path / self._pid_filename
        if not pid_file.exists():
            return None

        try:
            pid = int(pid_file.read_text().strip())
            os.kill(pid, 0)
            return pid
        except (ValueError, OSError, ProcessLookupError, PermissionError):
            return None

    def is_running(self) -> bool:
        """Check if the relay is currently running.

        Returns:
            True if relay is running, False otherwise
        """
        with self._thread_lock:
            if self._info is not None:
                pid = self._info.get("pid")
                if pid is None:
                    return False
                try:
                    os.kill(pid, 0)
                    return True
                except (ProcessLookupError, PermissionError):
                    return False
            return self._get_pid_from_path() is not None

    def get_socket_path(self) -> Path:
        """Get the socket path for this relay.

        Returns:
            Path to the socket file
        """
        return self._path / self._socket_filename

    def cleanup_orphans(self) -> None:
        """Clean up any orphaned relays from previous crashed sessions.

        This method is called during initialization to ensure no stale
        relays remain. It scans for console.pid files where the
        associated processes are no longer running and cleans them up.
        """
        from mvmctl.utils.fs import get_cache_dir

        logger.debug("Running console relay orphan cleanup check")

        vms_dir = get_cache_dir() / "vms"
        if not vms_dir.exists():
            return

        for entry in vms_dir.iterdir():
            if not entry.is_dir():
                continue

            id = entry.name
            pid_file = entry / DEFAULT_CONSOLE_PID_FILENAME
            if not pid_file.exists():
                continue

            try:
                pid = int(pid_file.read_text().strip())
                os.kill(pid, 0)
                logger.debug(
                    "Skipping orphan cleanup for %s - process %d still running",
                    id,
                    pid,
                )
            except (ValueError, OSError, ProcessLookupError, PermissionError) as e:
                if isinstance(e, ProcessLookupError):
                    # Process terminated - clean up stale PID file and socket
                    try:
                        pid_file.unlink()
                        logger.info(
                            "Cleaned up stale PID file for %s (process terminated)",
                            id,
                        )
                    except OSError:
                        pass
                    socket_path = entry / DEFAULT_CONSOLE_SOCKET_FILENAME
                    if socket_path.exists():
                        try:
                            socket_path.unlink()
                        except OSError:
                            pass
                elif isinstance(e, PermissionError):
                    # Get pid from the file for logging
                    pid_str = "unknown"
                    try:
                        pid_str = pid_file.read_text().strip()
                    except OSError:
                        pass
                    logger.debug(
                        "Skipping orphan cleanup for %s - permission denied on process %s",
                        id,
                        pid_str,
                    )
                else:
                    # Invalid PID file
                    try:
                        pid_file.unlink()
                        logger.info("Cleaned up invalid PID file for %s", id)
                    except OSError:
                        pass
