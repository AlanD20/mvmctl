"""Firecracker VM spawning logic."""

from __future__ import annotations

import logging
import os
import subprocess
import time
from pathlib import Path
from typing import TYPE_CHECKING, Any

from mvmctl.constants import (
    CONST_POLL_STEP_SECONDS,
    DEFAULT_FC_CONSOLE_LOG_FILENAME,
    DEFAULT_FC_LOG_FILENAME,
    DEFAULT_FC_PID_FILENAME,
)
from mvmctl.exceptions import VMCreateError

if TYPE_CHECKING:
    from mvmctl.api.vm._creation import VMCreationContext
    from mvmctl.api.vm._resolver import VMResolvedDependencies

logger = logging.getLogger(__name__)


def spawn_firecracker_vm(
    ctx: VMCreationContext,
    resolved: VMResolvedDependencies,
    config_file: Path,
) -> tuple[int, Path | None, int | None]:
    """Spawn firecracker process and return PID, socket path, and console relay PID.

    Args:
        ctx: VM creation context with state tracking
        resolved: Resolved VM inputs
        config_file: Path to firecracker config file

    Returns:
        Tuple of (pid, socket_path, console_relay_pid)

    Raises:
        VMCreateError: If firecracker process fails to start
    """
    vm_dir = ctx.vm_dir
    if vm_dir is None:
        raise VMCreateError("VM directory not set in context")

    log_file = vm_dir / DEFAULT_FC_LOG_FILENAME
    console_log_file = vm_dir / DEFAULT_FC_CONSOLE_LOG_FILENAME
    pid_file = vm_dir / DEFAULT_FC_PID_FILENAME

    socket_path: Path | None = None
    if resolved.enable_api_socket:
        from mvmctl.constants import DEFAULT_FC_API_SOCKET_FILENAME

        socket_path = vm_dir / DEFAULT_FC_API_SOCKET_FILENAME

    fc_cmd = [resolved.firecracker_bin, "--no-api", "--config-file", str(config_file)]
    if resolved.enable_api_socket and socket_path:
        fc_cmd = [
            resolved.firecracker_bin,
            "--api-sock",
            str(socket_path),
            "--config-file",
            str(config_file),
        ]

    log_fp = open(log_file, "w", buffering=1, encoding="utf-8")
    ctx.log_fp = log_fp

    console_fp = None
    proc: subprocess.Popen[Any] | None = None

    try:
        if resolved.enable_console and ctx.pty_slave_fd is not None:
            proc = subprocess.Popen(
                fc_cmd,
                stdin=ctx.pty_slave_fd,
                stdout=ctx.pty_slave_fd,
                stderr=log_fp,
                start_new_session=True,
                pass_fds=[ctx.pty_slave_fd],
            )
        else:
            console_fp = open(console_log_file, "w", buffering=1, encoding="utf-8")
            ctx.console_fp = console_fp
            proc = subprocess.Popen(
                fc_cmd,
                stdin=subprocess.DEVNULL,
                stdout=console_fp,
                stderr=log_fp,
                start_new_session=True,
            )

        time.sleep(CONST_POLL_STEP_SECONDS)
        poll_result = proc.poll()
        if poll_result is not None and isinstance(poll_result, int):
            raise VMCreateError(f"Firecracker process exited immediately with code {poll_result}")

        if resolved.enable_console and ctx.pty_slave_fd is not None:
            try:
                os.close(ctx.pty_slave_fd)
                ctx.pty_slave_fd = None
            except OSError:
                pass

        try:
            log_fp.close()
            ctx.log_fp = None
        except OSError:
            pass

        if console_fp is not None:
            try:
                console_fp.close()
                ctx.console_fp = None
            except OSError:
                pass

        console_relay_pid: int | None = None
        if resolved.enable_console and ctx.relay_mgr is not None and ctx.pty_master_fd is not None:
            try:
                console_relay_pid = ctx.relay_mgr.start_relay(
                    resolved.name, ctx.pty_master_fd, vm_dir
                )[1]
                ctx.mark_created("console_relay")
            except Exception as exc:
                logger.warning("Failed to start console relay: %s", exc)
                try:
                    os.close(ctx.pty_master_fd)
                    ctx.pty_master_fd = None
                except OSError:
                    pass

        _write_pid_file(pid_file, proc.pid)

        return proc.pid, socket_path, console_relay_pid

    except Exception as exc:
        logger.error("Failed to start Firecracker VM: %s", exc)
        if log_fp is not None:
            try:
                log_fp.close()
            except OSError:
                pass
        if console_fp is not None:
            try:
                console_fp.close()
            except OSError:
                pass
        raise


def _write_pid_file(pid_file: Path, pid: int) -> None:
    """Write PID to file."""
    pid_file.write_text(str(pid))


__all__ = ["spawn_firecracker_vm"]
