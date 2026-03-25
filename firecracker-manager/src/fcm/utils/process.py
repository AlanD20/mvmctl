"""Streaming subprocess utilities."""

from __future__ import annotations

import logging
import shlex
import subprocess
from collections.abc import Iterator

from fcm.exceptions import ProcessError

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
