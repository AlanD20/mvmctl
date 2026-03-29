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

from mvmctl.constants import (
    CONST_CONSOLE_KILL_TIMEOUT_S,
    DEFAULT_CONSOLE_PID_FILENAME,
    DEFAULT_CONSOLE_SOCKET_FILENAME,
)
from mvmctl.exceptions import MVMError
from mvmctl.utils.fs import get_vm_dir_by_hash

logger = logging.getLogger(__name__)


class ConsoleRelayManager:
    """Manager for console relay subprocess instances.

    Coordinates console relay processes for VMs, ensuring proper
    lifecycle management and cleanup of orphaned relays.

    Attributes:
        _relays: Registry of active relays keyed by VM name
        _lock: Lock for thread-safe access to relay registry
    """

    def __init__(self) -> None:
        """Initialize the relay manager."""
        self._relays: dict[str, Any] = {}
        self._lock: threading.Lock | None = None
        self.cleanup_orphans()

    @property
    def _thread_lock(self) -> Any:
        """Lazy initialization of threading lock."""
        if self._lock is None:
            self._lock = threading.Lock()
        return self._lock

    def _get_pid_file_path(self, vm_hash: str) -> Path:
        """Get the PID file path for a VM's console relay.

        Args:
            vm_hash: VM hash (64-char SHA256)

        Returns:
            Path to the console.pid file
        """
        return get_vm_dir_by_hash(vm_hash) / DEFAULT_CONSOLE_PID_FILENAME

    def _get_socket_path(self, vm_hash: str) -> Path:
        """Get the socket path for a VM's console relay.

        Args:
            vm_hash: VM hash (64-char SHA256)

        Returns:
            Path to the console.sock file
        """
        return get_vm_dir_by_hash(vm_hash) / DEFAULT_CONSOLE_SOCKET_FILENAME

    def start_relay(self, vm_name: str, pty_master_fd: int, vm_dir: Path) -> tuple[Path, int]:
        """Start a console relay for the specified VM.

        Spawns a relay subprocess that reads from the PTY master and
        writes to both the console.log file and a Unix socket.

        Args:
            vm_name: Unique name identifying the VM (for tracking)
            pty_master_fd: File descriptor of the PTY master
            vm_dir: Path to the VM directory (hash-based)

        Returns:
            Tuple of (socket_path, pid)

        Raises:
            MVMError: If a relay is already running for this VM
        """
        with self._thread_lock:
            if vm_name in self._relays:
                raise MVMError(f"Console relay already running for VM: {vm_name}")

            socket_path = vm_dir / DEFAULT_CONSOLE_SOCKET_FILENAME
            pid_file = vm_dir / DEFAULT_CONSOLE_PID_FILENAME
            log_file = vm_dir / "firecracker.console.log"

            relay_cmd = [
                sys.executable,
                "-m",
                "mvmctl.services.console_relay.process",
                "--vm-name",
                vm_name,
                "--pty-master-fd",
                str(pty_master_fd),
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
                    pass_fds=[pty_master_fd],
                )
            except OSError as e:
                raise MVMError(f"Failed to spawn console relay process: {e}") from e

            self._relays[vm_name] = {
                "pid": proc.pid,
                "socket_path": socket_path,
                "pid_file": pid_file,
            }

            logger.info(
                "Started console relay for VM %s (PID: %d)",
                vm_name,
                proc.pid,
            )

            return socket_path, proc.pid

    def stop_relay(self, vm_name: str, vm_hash: str | None = None) -> None:
        """Stop the console relay for the specified VM.

        Idempotent operation - safe to call multiple times. If no relay
        exists for the VM, this is a no-op.

        Args:
            vm_name: Name of the VM whose relay should be stopped (for tracking)
            vm_hash: VM hash (64-char SHA256) for PID file path. If None, uses vm_name.
        """
        with self._thread_lock:
            info = self._relays.get(vm_name)
            if info is not None:
                pid = int(info["pid"])
                pid_file = info["pid_file"]
                socket_path = info["socket_path"]

                try:
                    os.kill(pid, signal.SIGTERM)
                    logger.info(
                        "Sent SIGTERM to console relay (PID: %d) for VM %s",
                        pid,
                        vm_name,
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

                del self._relays[vm_name]
                logger.info("Stopped console relay for VM %s", vm_name)
            else:
                # Try PID file recovery using hash if provided, otherwise use name
                lookup_key = vm_hash if vm_hash is not None else vm_name
                self._stop_by_pid_file(lookup_key)

    def _stop_by_pid_file(self, vm_hash: str) -> bool:
        """Stop a relay using only its PID file (recovery path).

        Args:
            vm_hash: VM hash (64-char SHA256)

        Returns:
            True if a relay was stopped using the PID file, False otherwise
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
            os.kill(pid, 0)
            os.kill(pid, signal.SIGTERM)
            logger.info(
                "Stopped console relay via PID file recovery (PID: %d) for VM hash %s",
                pid,
                vm_hash,
            )
        except ProcessLookupError:
            logger.debug("Console relay process (PID: %d) already terminated", pid)
        except PermissionError:
            logger.warning("Cannot kill console relay (PID: %d) - permission denied", pid)

        try:
            pid_file.unlink()
        except OSError:
            pass

        socket_path = self._get_socket_path(vm_hash)
        if socket_path.exists():
            try:
                socket_path.unlink()
            except OSError:
                pass

        return True

    def kill_relay(self, vm_name: str, vm_hash: str | None = None) -> bool:
        """Forcefully kill the console relay for the specified VM.

        Sends SIGTERM first, waits up to CONST_CONSOLE_KILL_TIMEOUT_S seconds,
        then sends SIGKILL if still running.

        Args:
            vm_name: Name of the VM whose relay should be killed (for tracking)
            vm_hash: VM hash (64-char SHA256) for PID file path. If None, uses vm_name.

        Returns:
            True if a relay was killed, False if no relay was running
        """
        with self._thread_lock:
            info = self._relays.get(vm_name)
            pid_file = None
            pid = None

            if info is not None:
                pid = info["pid"]
                pid_file = info["pid_file"]
            else:
                # Use hash if provided, otherwise fall back to name
                lookup_key = vm_hash if vm_hash is not None else vm_name
                pid_file = self._get_pid_file_path(lookup_key)
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
                if vm_name in self._relays:
                    del self._relays[vm_name]
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

            # Use hash if provided, otherwise fall back to name for socket path
            lookup_key = vm_hash if vm_hash is not None else vm_name
            socket_path = self._get_socket_path(lookup_key)
            if socket_path.exists():
                try:
                    socket_path.unlink()
                except OSError:
                    pass

            if vm_name in self._relays:
                del self._relays[vm_name]

            logger.info("Killed console relay for VM %s", vm_name)
            return True

    def get_relay_pid(self, vm_name: str, vm_hash: str | None = None) -> int | None:
        """Get the PID of the running relay for the specified VM.

        Args:
            vm_name: Name of the VM (for tracking)
            vm_hash: VM hash (64-char SHA256) for PID file path. If None, uses vm_name.

        Returns:
            The subprocess PID if running, None otherwise
        """
        with self._thread_lock:
            info = self._relays.get(vm_name)
            if info is not None:
                return int(info["pid"])
            # If vm_hash not provided, try vm_name as fallback for backward compatibility
            lookup_key = vm_hash if vm_hash is not None else vm_name
            return self._get_pid_from_file(lookup_key)

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
            os.kill(pid, 0)
            return pid
        except (ValueError, OSError, ProcessLookupError, PermissionError):
            return None

    def is_relay_running(self, vm_name: str, vm_hash: str | None = None) -> bool:
        """Check if the relay is currently running.

        Args:
            vm_name: Name of the VM (for tracking)
            vm_hash: VM hash (64-char SHA256) for PID file path. If None, uses vm_name.

        Returns:
            True if relay is running, False otherwise
        """
        with self._thread_lock:
            info = self._relays.get(vm_name)
            if info is not None:
                pid = info.get("pid")
                if pid is None:
                    return False
                try:
                    os.kill(pid, 0)
                    return True
                except (ProcessLookupError, PermissionError):
                    return False
            # If vm_hash not provided, try vm_name as fallback for backward compatibility
            lookup_key = vm_hash if vm_hash is not None else vm_name
            return self._get_pid_from_file(lookup_key) is not None

    def get_socket_path(self, vm_hash: str) -> Path:
        """Get the socket path for a VM's console relay.

        Args:
            vm_hash: VM hash (64-char SHA256)

        Returns:
            Path to the console.sock file
        """
        return self._get_socket_path(vm_hash)

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

        for vm_entry in vms_dir.iterdir():
            if not vm_entry.is_dir():
                continue

            vm_hash = vm_entry.name
            pid_file = vm_entry / DEFAULT_CONSOLE_PID_FILENAME
            if not pid_file.exists():
                continue

            try:
                pid = int(pid_file.read_text().strip())
                os.kill(pid, 0)
                logger.debug(
                    "Skipping orphan cleanup for VM hash %s - process %d still running",
                    vm_hash,
                    pid,
                )
            except (ValueError, OSError):
                try:
                    pid_file.unlink()
                    logger.info("Cleaned up invalid PID file for VM hash %s", vm_hash)
                except OSError:
                    pass
            except ProcessLookupError:
                try:
                    pid_file.unlink()
                    logger.info(
                        "Cleaned up stale PID file for VM hash %s (process terminated)",
                        vm_hash,
                    )
                except OSError:
                    pass
                socket_path = vm_entry / DEFAULT_CONSOLE_SOCKET_FILENAME
                if socket_path.exists():
                    try:
                        socket_path.unlink()
                    except OSError:
                        pass
            except PermissionError:
                logger.debug(
                    "Skipping orphan cleanup for VM hash %s - permission denied on process %d",
                    vm_hash,
                    pid,
                )
