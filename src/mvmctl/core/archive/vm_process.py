from __future__ import annotations

import logging
import os
import signal
import subprocess
import time
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    BRIDGE_NAME,
    CONST_FILE_PERMS_PID_FILE,
    CONST_POLL_STEP_SECONDS,
    DEFAULT_FC_EXITCODE_FILENAME,
    FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S,
    FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S,
    FIRECRACKER_SIGTERM_WAIT_S,
)
from mvmctl.core.firecracker import FirecrackerClient
from mvmctl.core.network import delete_tap, remove_iptables_forward_rules
from mvmctl.exceptions import NetworkError, ProcessError
from mvmctl.utils.fs import read_pid_file, write_exit_code, write_pid_file
from mvmctl.utils.process_signals import ProcessSignalHandler

logger = logging.getLogger(__name__)

__all__ = [
    "spawn_firecracker",
    "kill_firecracker",
    "graceful_shutdown",
    "pause_vm",
    "resume_vm",
    "cleanup_tap",
]


def _write_pid_file(pid_file: Path, pid: int) -> None:
    write_pid_file(pid_file, pid, CONST_FILE_PERMS_PID_FILE)


def _read_pid_file(pid_file: Path) -> int | None:
    return read_pid_file(pid_file)


def _write_exit_code(vm_dir: Path, exit_code: int) -> None:
    write_exit_code(vm_dir, exit_code, DEFAULT_FC_EXITCODE_FILENAME)


def spawn_firecracker(
    config_path: Path,
    socket_path: Path,
    log_path: Path,
    metrics_path: Path | None,
    firecracker_binary: Path,
    jailer_binary: Path | None,
    lsm_flags: str,
    enable_api_socket: bool,
    enable_pci: bool,
) -> int:
    _ = (metrics_path, jailer_binary, lsm_flags, enable_pci)
    command = [str(firecracker_binary), "--no-api", "--config-file", str(config_path)]
    if enable_api_socket:
        command = [
            str(firecracker_binary),
            "--api-sock",
            str(socket_path),
            "--config-file",
            str(config_path),
        ]

    try:
        with open(log_path, "w", buffering=1, encoding="utf-8") as log_fp:
            proc = subprocess.Popen(
                command,
                stdin=subprocess.DEVNULL,
                stdout=log_fp,
                stderr=log_fp,
                start_new_session=True,
            )
            return proc.pid
    except FileNotFoundError as exc:
        raise ProcessError(f"Firecracker binary not found: {firecracker_binary}") from exc
    except OSError as exc:
        raise ProcessError(f"Failed to spawn Firecracker: {exc}") from exc


def kill_firecracker(pid: int, socket_path: Path | None = None) -> None:
    _ = socket_path
    try:
        os.kill(pid, signal.SIGKILL)
    except ProcessLookupError:
        return
    except OSError as exc:
        raise ProcessError(f"Failed to kill Firecracker process {pid}: {exc}") from exc


def graceful_shutdown(pid: int | None, socket_path: Path | None, force: bool = False) -> None:
    if pid is None:
        return

    handler = ProcessSignalHandler(pid)

    if force:
        handler.graceful_shutdown(
            timeout=int(FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S),
            sigterm_wait=float(FIRECRACKER_SIGTERM_WAIT_S),
        )
        return

    if socket_path is not None and Path(socket_path).exists():
        try:
            client = FirecrackerClient(Path(socket_path))
            client.send_ctrl_alt_del()
            client.close()
        except (ProcessLookupError, PermissionError, InterruptedError):
            pass

        poll_steps = int(FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S / CONST_POLL_STEP_SECONDS)
        for _ in range(poll_steps):
            time.sleep(FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S)
            if not handler.is_running():
                break

    handler.graceful_shutdown(
        timeout=int(FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S),
        sigterm_wait=float(FIRECRACKER_SIGTERM_WAIT_S),
    )


def pause_vm(fc_client: Any) -> None:
    fc_client.pause_vm()


def resume_vm(fc_client: Any) -> None:
    fc_client.resume_vm()


def cleanup_tap(tap_name: str, bridge: str | None = None) -> None:
    try:
        remove_iptables_forward_rules(tap_name, bridge=bridge or BRIDGE_NAME)
        delete_tap(tap_name)
    except NetworkError:
        logger.debug("Failed to cleanup TAP %s", tap_name, exc_info=True)
