"""Streaming subprocess utilities."""

from __future__ import annotations

import logging
import os
import shlex
import subprocess
import threading
import time
from collections.abc import Iterator

from mvmctl.constants import CONST_TIMESTAMP_INITIAL, PROJECT_GROUP
from mvmctl.exceptions import ProcessError

logger = logging.getLogger(__name__)

_STDERR_PREVIEW_LIMIT = 100


def _sanitize_stderr(stderr: str | None) -> str:
    cleaned = (stderr or "").strip()
    if len(cleaned) > _STDERR_PREVIEW_LIMIT:
        return f"{cleaned[:_STDERR_PREVIEW_LIMIT]}..."
    return cleaned


def run_cmd(
    args: list[str],
    *,
    check: bool = True,
    capture: bool = True,
    cwd: str | None = None,
) -> subprocess.CompletedProcess[str]:
    """Run a subprocess command and return the completed-process result.

    Args:
        args: Command and arguments to execute.
        check: Raise ``ProcessError`` on non-zero exit code when ``True``.
        capture: Capture stdout/stderr when ``True``; inherit from parent otherwise.
        cwd: Working directory for the subprocess, or ``None`` for the current directory.

    Returns:
        The ``subprocess.CompletedProcess`` result.

    Raises:
        ProcessError: If the command is not found or exits with a non-zero code.
    """
    logger.debug("$ %s", shlex.join(args))
    try:
        result = subprocess.run(
            args,
            capture_output=capture,
            text=True,
            check=check,
            cwd=cwd,
        )
    except FileNotFoundError as e:
        raise ProcessError(f"Command not found: {args[0]}") from e
    except subprocess.CalledProcessError as e:
        stderr = (e.stderr or "").strip()
        logger.debug(
            "Command failed (exit %s): %s\nstderr=%s",
            e.returncode,
            shlex.join(args),
            stderr,
            exc_info=True,
        )
        sanitized_stderr = _sanitize_stderr(stderr)
        raise ProcessError(
            f"Command failed (exit {e.returncode}): {args[0]}"
            + (f"\n{sanitized_stderr}" if sanitized_stderr else "")
        ) from e
    return result


def stream_cmd(
    args: list[str],
    *,
    cwd: str | None = None,
) -> Iterator[str]:
    """Stream stdout lines from a subprocess command as they are produced.

    Args:
        args: Command and arguments to execute.
        cwd: Working directory for the subprocess, or ``None`` for the current directory.

    Yields:
        Each output line with the trailing newline stripped.

    Raises:
        ProcessError: If the command is not found or exits with a non-zero code.
    """
    logger.debug("$ %s", shlex.join(args))
    try:
        proc = subprocess.Popen(
            args,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            cwd=cwd,
        )
    except FileNotFoundError as e:
        raise ProcessError(f"Command not found: {args[0]}") from e

    if proc.stdout is None:
        raise ProcessError("stdout is None — stdout=PIPE was not set")
    try:
        for line in proc.stdout:
            yield line.rstrip("\n")
    finally:
        proc.stdout.close()
        returncode = proc.wait()
        if returncode != 0:
            # Sanitize error message: only show command name
            raise ProcessError(f"Command failed (exit {returncode}): {args[0]}")


# Sudo credential cache with TTL (60 seconds)
_SUDO_CACHE_LOCK = threading.Lock()
_SUDO_CREDENTIALS_VALID = False
_SUDO_CACHE_TIMESTAMP: float = CONST_TIMESTAMP_INITIAL
_SUDO_CACHE_TTL_SECONDS = 60
_SUDO_VALIDATION_IN_PROGRESS = False


def _is_sudo_cached() -> bool:
    """Check if sudo credentials are currently cached and valid.

    Returns True if credentials are cached and haven't expired.
    """
    global _SUDO_CREDENTIALS_VALID, _SUDO_CACHE_TIMESTAMP

    with _SUDO_CACHE_LOCK:
        if not _SUDO_CREDENTIALS_VALID:
            return False

        elapsed = time.monotonic() - _SUDO_CACHE_TIMESTAMP
        if elapsed > _SUDO_CACHE_TTL_SECONDS:
            _SUDO_CREDENTIALS_VALID = False
            return False

        return True


def _validate_sudo_credentials() -> bool:
    """Validate sudo credentials are cached and refresh if needed.

    Uses sudo -n (non-interactive) to check if credentials are cached.
    If not cached, uses sudo -v to validate (which may prompt for password).

    Includes anti-recursion protection to prevent infinite loops.

    Returns:
        True if sudo credentials are valid and cached.
    """
    global _SUDO_CREDENTIALS_VALID, _SUDO_CACHE_TIMESTAMP, _SUDO_VALIDATION_IN_PROGRESS

    # Anti-recursion protection
    if _SUDO_VALIDATION_IN_PROGRESS:
        return False

    # Fast path: check if already cached and not expired
    if _is_sudo_cached():
        return True

    try:
        _SUDO_VALIDATION_IN_PROGRESS = True

        # First, try non-interactive check (sudo -n true)
        # This succeeds if credentials are cached without requiring password
        result = subprocess.run(
            ["sudo", "-n", "true"],
            capture_output=True,
            check=False,
        )

        if result.returncode == 0:
            with _SUDO_CACHE_LOCK:
                _SUDO_CREDENTIALS_VALID = True
                _SUDO_CACHE_TIMESTAMP = time.monotonic()
            logger.debug("Sudo credentials validated (cached)")
            return True

        # Credentials not cached - try to validate with sudo -v
        # This may prompt for password if required
        result = subprocess.run(
            ["sudo", "-v"],
            capture_output=True,
            check=False,
        )

        if result.returncode == 0:
            with _SUDO_CACHE_LOCK:
                _SUDO_CREDENTIALS_VALID = True
                _SUDO_CACHE_TIMESTAMP = time.monotonic()
            logger.debug("Sudo credentials validated (refreshed)")
            return True

        logger.debug("Sudo credential validation failed")
        return False

    finally:
        _SUDO_VALIDATION_IN_PROGRESS = False


def privileged_cmd(cmd: list[str]) -> list[str]:
    """Prepend sudo if not running as root.

    Requires the user to be in the mvm group (configured by 'mvm host init').
    Raises PrivilegeError if the user lacks group membership.
    """
    if os.getuid() != 0:
        require_mvm_group_membership()
        return ["sudo"] + cmd
    return cmd


def require_mvm_group_membership() -> None:
    """Raise PrivilegeError if user is not in the mvm group with active credentials."""
    import grp
    import pwd

    from mvmctl.exceptions import PrivilegeError

    try:
        g = grp.getgrnam(PROJECT_GROUP)
    except KeyError:
        raise PrivilegeError(
            f"Group '{PROJECT_GROUP}' does not exist. "
            f"Run 'sudo mvm host init' to set up privilege management."
        )

    user_pw = pwd.getpwuid(os.getuid())
    username = user_pw.pw_name

    is_supplementary_member = username in g.gr_mem
    is_primary_group = user_pw.pw_gid == g.gr_gid
    if not (is_supplementary_member or is_primary_group):
        raise PrivilegeError(
            f"User '{username}' is not in the '{PROJECT_GROUP}' group. "
            f"Run 'sudo mvm host init' to configure privileges, "
            f"then 'newgrp {PROJECT_GROUP}' or log out and back in."
        )

    process_gids = set(os.getgroups()) | {os.getgid(), os.getegid()}
    if g.gr_gid not in process_gids:
        raise PrivilegeError(
            f"Your user is in the '{PROJECT_GROUP}' group, but your current session "
            f"does not have the group active yet. Please log out and log back in, "
            f"or run: newgrp {PROJECT_GROUP}"
        )


def is_process_running(pid: int | None) -> bool:
    """Check if a process is still running by PID.

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
