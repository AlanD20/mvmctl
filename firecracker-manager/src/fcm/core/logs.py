"""Log viewing utilities."""

import logging
import time
from collections.abc import Callable, Generator
from pathlib import Path

from fcm.exceptions import ConfigError, FCMError, VMNotFoundError
from fcm.utils.fs import get_vm_dir

logger = logging.getLogger(__name__)


def get_log_path(
    vm_name: str,
    log_type: str = "boot",
) -> Path:
    """Get log file path for a VM.

    Args:
        vm_name: VM name
        log_type: 'boot' for console log, 'os' for firecracker log

    Returns:
        Path to log file

    Raises:
        VMNotFoundError: If VM directory does not exist
        FCMError: If log type is unknown or log file not found
    """
    vm_dir = get_vm_dir(vm_name)

    if not vm_dir.exists():
        raise VMNotFoundError(f"VM '{vm_name}' not found at {vm_dir}")

    if log_type == "boot":
        log_file = vm_dir / "firecracker.console.log"
    elif log_type == "os":
        log_file = vm_dir / "firecracker.log"
    else:
        raise ConfigError(f"Unknown log type '{log_type}'. Valid: boot, os")

    if not log_file.exists():
        raise VMNotFoundError(f"Log file not found for VM '{vm_name}': {log_file}")

    return log_file


def read_log_lines(
    log_file: Path,
    lines: int = 50,
) -> list[str]:
    """Read last N lines from log file.

    Raises:
        FCMError: If the log file cannot be read
    """
    try:
        with open(log_file, "r") as f:
            all_lines = f.readlines()
            return all_lines[-lines:] if len(all_lines) > lines else all_lines
    except IOError as e:
        raise FCMError(f"Error reading log file: {e}") from e


def follow_log(
    log_file: Path,
) -> Generator[str, None, None]:
    """Follow log file in real-time (like tail -f).

    Yields new lines as they are written.

    Raises:
        FCMError: If the log file cannot be read
    """
    try:
        with open(log_file, "r") as f:
            f.seek(0, 2)  # Seek to end

            while True:
                line = f.readline()
                if not line:
                    time.sleep(0.1)  # Wait for new content
                    continue
                yield line.rstrip("\n")
    except KeyboardInterrupt:
        raise
    except IOError as e:
        raise FCMError(f"Error following log: {e}") from e


def show_logs(
    vm_name: str,
    log_type: str = "boot",
    lines: int = 50,
    follow: bool = False,
    output: Callable[[str], object] | None = None,
) -> list[str]:
    """Retrieve VM log lines.

    In non-follow mode, returns the last *lines* lines from the log file.
    In follow mode, streams lines through the *output* callback and returns
    the lines that were streamed before the caller interrupted (Ctrl-C).

    Args:
        vm_name: VM name
        log_type: 'boot' or 'os'
        lines: Number of lines to show (non-follow mode)
        follow: If True, follow log output via *output* callback
        output: Callable to emit each line in follow mode (default: ``print``)

    Returns:
        List of log line strings.

    Raises:
        VMNotFoundError: If VM not found
        FCMError: On log access errors
    """
    log_file = get_log_path(vm_name, log_type)

    log_type_label = "Boot" if log_type == "boot" else "OS"
    logger.info("=== %s Log for %s ===", log_type_label, vm_name)
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
