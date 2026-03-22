"""Log viewing utilities."""

from collections.abc import Generator
from pathlib import Path

from fcm.utils.console import print_error, print_info
from fcm.utils.fs import get_vm_dir


def get_log_path(
    vm_name: str,
    log_type: str = "boot",
) -> Path | None:
    """Get log file path for a VM.

    Args:
        vm_name: VM name
        log_type: 'boot' for console log, 'os' for firecracker log

    Returns:
        Path to log file or None if not found
    """
    vm_dir = get_vm_dir(vm_name)

    if not vm_dir.exists():
        print_error(f"VM '{vm_name}' not found at {vm_dir}")
        return None

    if log_type == "boot":
        log_file = vm_dir / "firecracker.console.log"
    elif log_type == "os":
        log_file = vm_dir / "firecracker.log"
    else:
        print_error(f"Unknown log type '{log_type}'. Valid: boot, os")
        return None

    if not log_file.exists():
        print_error(f"Log file not found: {log_file}")
        return None

    return log_file


def read_log_lines(
    log_file: Path,
    lines: int = 50,
) -> list[str]:
    """Read last N lines from log file."""
    try:
        with open(log_file, "r") as f:
            all_lines = f.readlines()
            return all_lines[-lines:] if len(all_lines) > lines else all_lines
    except IOError as e:
        print_error(f"Error reading log file: {e}")
        return []


def follow_log(
    log_file: Path,
) -> Generator[str, None, None]:
    """Follow log file in real-time (like tail -f).

    Yields new lines as they are written.
    """
    try:
        with open(log_file, "r") as f:
            f.seek(0, 2)  # Seek to end

            while True:
                line = f.readline()
                if not line:
                    import time

                    time.sleep(0.1)  # Wait for new content
                    continue
                yield line.rstrip("\n")
    except KeyboardInterrupt:
        raise
    except IOError as e:
        print_error(f"Error following log: {e}")


def show_logs(
    vm_name: str,
    log_type: str = "boot",
    lines: int = 50,
    follow: bool = False,
) -> int:
    """Show VM logs.

    Args:
        vm_name: VM name
        log_type: 'boot' or 'os'
        lines: Number of lines to show
        follow: If True, follow log output

    Returns:
        Exit code (0 for success)
    """
    log_file = get_log_path(vm_name, log_type)
    if not log_file:
        return 1

    log_type_label = "Boot" if log_type == "boot" else "OS"
    print_info(f"=== {log_type_label} Log for {vm_name} ===")
    print_info(f"File: {log_file}")

    if follow:
        print_info("Press Ctrl+C to exit")
        print()
        try:
            for line in follow_log(log_file):
                print(line)
        except KeyboardInterrupt:
            print()
            return 0
    else:
        log_lines = read_log_lines(log_file, lines)
        for line in log_lines:
            print(line, end="")

    return 0
