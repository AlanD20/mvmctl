"""Shared helpers for CLI command modules."""

import os
import subprocess
from pathlib import Path

import typer


def check_name_arg(ctx: typer.Context, name: str | None) -> str:
    """Guard for positional name arg: show help on ``"help"`` or ``None``, else return name."""
    if name == "help":
        typer.echo(ctx.get_help())
        raise typer.Exit()
    if name is None:
        typer.echo(ctx.get_help())
        raise typer.Exit(code=1)
    return name


def is_file_missing(path: Path | None) -> bool:
    """Check if a file is missing or None."""
    if path is None:
        return True
    return not path.exists()


def is_vm_process_running(pid: int | None) -> bool:
    """Check if a VM process is still running by PID.

    Args:
        pid: Process ID to check

    Returns:
        True if process is running, False if not running or PID is None
    """
    if pid is None:
        return False
    try:
        os.kill(pid, 0)
        return True
    except (ProcessLookupError, OSError):
        return False


def is_bridge_alive(bridge_name: str) -> bool:
    """Check if a network bridge still exists.

    Args:
        bridge_name: Name of the bridge interface

    Returns:
        True if bridge exists, False otherwise
    """
    try:
        result = subprocess.run(
            ["ip", "link", "show", bridge_name],
            capture_output=True,
            check=False,
        )
        return result.returncode == 0
    except FileNotFoundError:
        return False


def get_state_marker(is_missing: bool) -> str:
    """Get the state marker prefix.

    Returns:
        "X " if resource is missing, "  " (two spaces) if present
    """
    return "X " if is_missing else "  "


def get_combined_marker(is_default: bool, is_missing: bool) -> str:
    """Get combined default and existence marker.

    Combines default marker (* ) with existence marker (X ) into a single
    3-character prefix for display in listing tables.

    Returns:
        "*X " - File missing + default
        "X "  - File missing + not default (with leading space for alignment)
        "* "  - File exists + default (with trailing space for alignment)
        "  "  - File exists + not default
    """
    if is_default and is_missing:
        return "*X "
    elif is_missing:
        return " X "  # Leading space for alignment with "*X "
    elif is_default:
        return "*  "  # Trailing space for alignment
    else:
        return "   "  # Three spaces for alignment
