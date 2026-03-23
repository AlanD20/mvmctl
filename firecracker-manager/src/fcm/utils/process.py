"""Streaming subprocess utilities."""

from __future__ import annotations

import logging
import subprocess
from collections.abc import Iterator

from fcm.exceptions import ProcessError

logger = logging.getLogger(__name__)


def run_cmd(
    args: list[str],
    *,
    check: bool = True,
    capture: bool = True,
    cwd: str | None = None,
) -> subprocess.CompletedProcess[str]:
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
        raise ProcessError(
            f"Command failed (exit {e.returncode}): {' '.join(args)}"
            + (f"\n{stderr}" if stderr else "")
        ) from e
    return result


def stream_cmd(
    args: list[str],
    *,
    cwd: str | None = None,
) -> Iterator[str]:
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
            raise ProcessError(f"Command failed (exit {returncode}): {' '.join(args)}")
