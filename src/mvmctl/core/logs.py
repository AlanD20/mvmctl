"""Log viewing utilities."""

import logging
import time
from collections import deque
from collections.abc import Callable, Generator
from pathlib import Path

from mvmctl.constants import (
    DEFAULT_FC_LOG_FILENAME,
    DEFAULT_FC_SERIAL_OUTPUT_FILENAME,
    LOG_FOLLOW_POLL_INTERVAL_S,
)
from mvmctl.exceptions import ConfigError, MVMError, VMNotFoundError
from mvmctl.utils.fs import get_vm_dir_by_hash

logger = logging.getLogger(__name__)

_LOG_TYPE_FILES: dict[str, str] = {
    "boot": DEFAULT_FC_SERIAL_OUTPUT_FILENAME,
    "os": DEFAULT_FC_LOG_FILENAME,
}


def get_log_path(
    vm_hash: str,
    log_type: str,
) -> Path:
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
    vm_dir = get_vm_dir_by_hash(vm_hash)

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


def read_log_lines(
    log_file: Path,
    lines: int,
) -> list[str]:
    """Read last *lines* lines from a log file.

    Args:
        log_file: Path to the log file.
        lines: Number of trailing lines to return.

    Returns:
        List of line strings (including newlines).

    Raises:
        MVMError: If the log file cannot be read.
    """
    try:
        with open(log_file, "r") as f:
            last_lines = deque(f, maxlen=lines)
            return list(last_lines)
    except IOError as e:
        raise MVMError(f"Error reading log file: {e}") from e


def follow_log(
    log_file: Path,
) -> Generator[str, None, None]:
    """Follow log file in real-time (like tail -f).

    Yields new lines as they are written.

    Raises:
        MVMError: If the log file cannot be read
    """
    try:
        with open(log_file, "r") as f:
            f.seek(0, 2)  # Seek to end

            while True:
                line = f.readline()
                if not line:
                    time.sleep(LOG_FOLLOW_POLL_INTERVAL_S)  # Wait for new content
                    continue
                yield line.rstrip("\n")
    except IOError as e:
        raise MVMError(f"Error following log: {e}") from e


def show_logs(
    vm_hash: str,
    log_type: str,
    lines: int,
    follow: bool,
    output: Callable[[str], object] | None = None,
) -> list[str]:
    """Retrieve VM log lines.

    In non-follow mode, returns the last *lines* lines from the log file.
    In follow mode, streams lines through the *output* callback and returns
    the lines that were streamed before the caller interrupted (Ctrl-C).

    Args:
        vm_hash: VM hash (64-char SHA256)
        log_type: 'boot' or 'os'
        lines: Number of lines to show (non-follow mode)
        follow: If True, follow log output via *output* callback
        output: Callable to emit each line in follow mode (default: ``print``)

    Returns:
        List of log line strings.

    Raises:
        VMNotFoundError: If VM not found
        MVMError: On log access errors
    """
    log_file = get_log_path(vm_hash, log_type)

    log_type_label = "Boot" if log_type == "boot" else "OS"
    logger.info("=== %s Log ===", log_type_label)
    logger.info("File: %s", log_file)

    if follow:
        logger.info("Press Ctrl+C to exit")
        emit = output or print
        collected: list[str] = []
        try:
            for line in follow_log(log_file):
                emit(line)
                collected.append(line)
        except KeyboardInterrupt:
            return collected
        return collected
    else:
        log_lines = read_log_lines(log_file, lines)
        return log_lines
