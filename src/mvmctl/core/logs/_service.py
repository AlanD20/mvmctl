"""Stateless log file operations."""

from __future__ import annotations

import time
from collections import deque
from collections.abc import Generator
from pathlib import Path

from mvmctl.constants import (
    DEFAULT_FC_LOG_FILENAME,
    DEFAULT_FC_SERIAL_OUTPUT_FILENAME,
    LOG_FOLLOW_POLL_INTERVAL_S,
)
from mvmctl.exceptions import ConfigError, MVMError, VMNotFoundError
from mvmctl.utils.common import CacheUtils

_LOG_TYPE_FILES: dict[str, str] = {
    "boot": DEFAULT_FC_SERIAL_OUTPUT_FILENAME,
    "os": DEFAULT_FC_LOG_FILENAME,
}


class LogService:
    """Stateless log file operations."""

    @staticmethod
    def get_log_path(vm_hash: str, log_type: str) -> Path:
        """Get log file path for a VM by its hash.

        Args:
            vm_hash: VM hash (64-char SHA256)
            log_type: 'boot' for console log, 'os' for firecracker log

        Returns:
            Path to log file

        Raises:
            VMNotFoundError: If VM directory does not exist
            MVMError: If log type is unknown or log file not found
        """
        vm_dir = CacheUtils.get_vm_dir(vm_hash)

        if not vm_dir.exists():
            raise VMNotFoundError(f"VM directory not found at {vm_dir}")

        log_filename = _LOG_TYPE_FILES.get(log_type)
        if log_filename is None:
            valid = ", ".join(_LOG_TYPE_FILES)
            raise ConfigError(f"Unknown log type '{log_type}'. Valid: {valid}")
        log_file = vm_dir / log_filename

        if not log_file.exists():
            raise VMNotFoundError(f"Log file not found for VM: {log_file}")

        return log_file

    @staticmethod
    def read_log_lines(log_file: Path, lines: int) -> list[str]:
        """Read last *lines* lines from a log file.

        Args:
            log_file: Path to the log file.
            lines: Number of trailing lines to return.

        Returns:
            List of line strings.

        Raises:
            MVMError: If the log file cannot be read.
        """
        try:
            with open(log_file) as f:
                last_lines = deque(f, maxlen=lines)
                return [line.rstrip("\n") for line in last_lines]
        except IOError as e:
            raise MVMError(f"Error reading log file: {e}") from e

    @staticmethod
    def follow_log(log_file: Path) -> Generator[str, None, None]:
        """Follow log file in real-time (like tail -f).

        Yields new lines as they are written.

        Raises:
            MVMError: If the log file cannot be read
        """
        try:
            with open(log_file) as f:
                f.seek(0, 2)  # Seek to end

                while True:
                    line = f.readline()
                    if not line:
                        time.sleep(LOG_FOLLOW_POLL_INTERVAL_S)
                        continue
                    yield line.rstrip("\n")
        except IOError as e:
            raise MVMError(f"Error following log: {e}") from e
