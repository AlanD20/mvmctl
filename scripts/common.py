#!/usr/bin/env python3
"""Shared infrastructure for mvmctl scripts.

Provides terminal color codes, path constants, output helpers, and
command runners that scripts import instead of redefining locally.

Everything in this module is a **building block** — scripts compose
these into their own output and error-handling logic.  No output
format is imposed on importers.
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

# ---------------------------------------------------------------------------
# ANSI colour codes (identical strings everywhere)
# ---------------------------------------------------------------------------

RED = "\033[91m"
GREEN = "\033[92m"
YELLOW = "\033[93m"
BLUE = "\033[94m"
CYAN = "\033[96m"
BOLD = "\033[1m"
RESET = "\033[0m"

# ---------------------------------------------------------------------------
# Path constants (resolved once from this file's location)
# ---------------------------------------------------------------------------

SCRIPT_DIR = Path(__file__).resolve().parent
PROJECT_ROOT = SCRIPT_DIR.parent
DEFAULT_MIRROR = Path.home() / ".cache" / "mvm-asset-mirror"

# ---------------------------------------------------------------------------
# Output helpers — simple wrappers that print directly.
# Importing scripts that want different formatting keep their own helpers.
# ---------------------------------------------------------------------------


def print_banner(text: str) -> None:
    """Print a prominent blue banner around *text*."""
    print(f"\n{BOLD}{CYAN}{'=' * 60}{RESET}")
    print(f"{BOLD}{CYAN}  {text}{RESET}")
    print(f"{BOLD}{CYAN}{'=' * 60}{RESET}\n")


def print_step(number: int, text: str) -> None:
    """Print a numbered step header."""
    print(f"\n  {CYAN}[{number}]{RESET} {BOLD}{text}{RESET}")


def print_info(text: str) -> None:
    """Print indented informational text."""
    print(f"     {text}")


def print_success(text: str) -> None:
    """Print a success message with a green checkmark."""
    print(f"  {GREEN}\u2713{RESET} {text}")


def print_fail(text: str) -> None:
    """Print a failure message with a red ballot X."""
    print(f"  {RED}\u2717{RESET} {text}")


def print_warn(text: str) -> None:
    """Print a warning message with a yellow warning sign."""
    print(f"  {YELLOW}\u26a0{RESET} {text}")


# ---------------------------------------------------------------------------
# Raw command runner (no output, no error handling — caller's choice)
# ---------------------------------------------------------------------------


def run_cmd(
    cmd: list[str],
    *,
    timeout: int = 7200,
    capture: bool = False,
    env: dict[str, str] | None = None,
    check: bool = False,
) -> subprocess.CompletedProcess[str] | None:
    """Execute *cmd* and return the ``CompletedProcess``.

    This is a thin wrapper around ``subprocess.run`` that adds sensible
    defaults (timeout, optional capture).  It prints nothing and raises
    nothing — the caller decides how to format success/failure.

    Returns ``None`` when a timeout expires (the caller can distinguish
    this from a non-zero exit by checking ``is None``).

    Parameters
    ----------
    cmd:
        Command vector to execute.
    timeout:
        Maximum wall-clock seconds (default 2 hours).
    capture:
        If True, capture stdout+stderr as text; otherwise inherit tty.
    env:
        Environment dict (default: ``os.environ.copy()``).
    check:
        If True, raise ``subprocess.CalledProcessError`` on non-zero exit.
    """
    if env is None:
        env = os.environ.copy()

    kwargs: dict = {}
    if capture:
        kwargs["capture_output"] = True
        kwargs["text"] = True
    if check:
        kwargs["check"] = True

    try:
        return subprocess.run(cmd, env=env, timeout=timeout, **kwargs)
    except subprocess.TimeoutExpired:
        return None


# ---------------------------------------------------------------------------
# mvm-specific runner (uses common.info / common.ok / common.fail)
# ---------------------------------------------------------------------------


def run_mvm(
    mvm_cmd: str,
    args: list[str],
    *,
    sudo: bool = False,
    description: str | None = None,
    timeout: int = 7200,
) -> bool:
    """Run an mvm subcommand, streaming stdout/stderr to the terminal.

    Sets ``MVM_ASSET_MIRROR`` in the subprocess environment.
    For sudo commands uses ``sudo -E`` so the user's ``PATH`` (and thus
    ``uv``) is preserved.

    Prints progress/report lines via :func:`print_info`,
    :func:`print_success`, and :func:`print_fail`.

    Returns True on exit code 0, False otherwise.
    """
    desc = description or "mvm " + " ".join(args)
    print_info(f"Running: {desc}")
    sys.stdout.flush()

    mirror_val = str(DEFAULT_MIRROR)
    DEFAULT_MIRROR.mkdir(parents=True, exist_ok=True)

    env = os.environ.copy()
    env["MVM_ASSET_MIRROR"] = mirror_val

    if sudo:
        # Resolve executable to full path so sudo can find it
        # (uv is often in a user-local path like ~/.pyenv/shims/)
        cmd_parts = mvm_cmd.split()
        exe_path = shutil.which(cmd_parts[0])
        if exe_path:
            cmd_parts[0] = exe_path
        cmd = ["sudo", "-E"] + cmd_parts + args
    else:
        cmd = mvm_cmd.split() + args

    start = time.monotonic()
    try:
        result = subprocess.run(cmd, env=env, timeout=timeout)
    except subprocess.TimeoutExpired:
        print_fail(f"Timed out after {timeout}s")
        return False
    elapsed = int(time.monotonic() - start)

    if result.returncode == 0:
        print_success(f"Completed ({elapsed}s)")
        return True

    print_fail(f"Failed (exit {result.returncode}, {elapsed}s)")
    return False


# ---------------------------------------------------------------------------
# Timer
# ---------------------------------------------------------------------------


class Timer:
    """Simple elapsed-time tracker for build steps and long operations.

    Usage::

        timer = Timer()
        do_work()
        print(f"Done in {timer.elapsed}s")
        timer.reset()
        do_more_work()
        print(f"Second phase: {timer.elapsed}s")
    """

    def __init__(self) -> None:
        self._start: float = time.monotonic()

    @property
    def elapsed(self) -> int:
        """Seconds since the timer was created or last reset."""
        return int(time.monotonic() - self._start)

    def reset(self) -> None:
        """Reset the timer back to zero."""
        self._start = time.monotonic()


# ---------------------------------------------------------------------------
# CLI helpers
# ---------------------------------------------------------------------------


def add_bin_arg(parser: argparse.ArgumentParser) -> None:
    """Add a ``--bin`` argument to *parser*."""
    parser.add_argument(
        "--bin",
        default=None,
        help=(
            "mvm command or binary path (default: 'uv run mvm'). "
            "Examples: ~/.local/bin/mvm, 'uv run --frozen mvm'"
        ),
    )


def resolve_mvm_cmd(bin_arg: str | None) -> str:
    """Return the mvm command string from a ``--bin`` argument.

    Expands ``~`` and defaults to ``"uv run mvm"``.
    """
    raw = bin_arg or "uv run mvm"
    return str(Path(raw).expanduser())
