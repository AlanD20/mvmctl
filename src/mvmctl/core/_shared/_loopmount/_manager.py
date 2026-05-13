"""
Loop-mount manager for the mvm-provision binary.

Manages the lifecycle of the mvm-provision subprocess, building JSON
operation payloads and parsing responses.
"""

from __future__ import annotations

import json
import logging
import subprocess
import sys
from pathlib import Path
from typing import Any

from mvmctl.exceptions import (
    LoopMountError,
    LoopMountTimeoutError,
    ProcessError,
)
from mvmctl.utils._system import run_cmd
from mvmctl.utils.common import CacheUtils, is_debug_mode

logger = logging.getLogger(__name__)

# Default timeout for the loop-mount binary (seconds)
LOOP_MOUNT_TIMEOUT = 60

# Development-mode fallback path to process.py
_DEV_PROCESS_PATH = (
    Path(__file__).resolve().parent.parent.parent.parent
    / "services"
    / "loopmount"
    / "process.py"
)


class LoopMountManager:
    """
    Manages the mvm-provision binary lifecycle.

    Builds operations JSON, spawns the binary (either compiled or dev mode),
    and reads the response. Falls back to the development Python path when the
    compiled binary is not available.
    """

    BINARY_NAME = "mvm-provision"

    # ------------------------------------------------------------------
    # Binary path resolution
    # ------------------------------------------------------------------

    @staticmethod
    def _resolve_binary_path() -> Path | None:
        """Locate the compiled mvm-provision binary in the bin cache dir."""
        bin_dir = CacheUtils.get_bin_dir()
        binary = bin_dir / LoopMountManager.BINARY_NAME
        if binary.exists():
            return binary
        return None

    # ------------------------------------------------------------------
    # Payload builder
    # ------------------------------------------------------------------

    @staticmethod
    def _build_ops(
        image_path: str,
        fs_type: str | None = None,
        files: list[dict[str, object]] | None = None,
        copy_dirs: list[dict[str, object]] | None = None,
        commands: list[str] | None = None,
        resize: dict[str, object] | None = None,
    ) -> dict[str, object]:
        """Build the JSON payload dict for the loop-mount binary."""
        ops: dict[str, object] = {}
        if files:
            ops["files"] = files
        if copy_dirs:
            ops["copy_dirs"] = copy_dirs
        if commands:
            ops["commands"] = commands
        if resize:
            ops["resize"] = resize

        payload: dict[str, object] = {
            "image": image_path,
            "operations": ops,
        }
        if fs_type:
            payload["fs_type"] = fs_type
        if is_debug_mode():
            payload["debug"] = True
        return payload

    # ------------------------------------------------------------------
    # Main provision method
    # ------------------------------------------------------------------

    @staticmethod
    def execute(
        image_path: str,
        fs_type: str | None = None,
        files: list[dict[str, object]] | None = None,
        copy_dirs: list[dict[str, object]] | None = None,
        commands: list[str] | None = None,
        resize: dict[str, object] | None = None,
        timeout: int = LOOP_MOUNT_TIMEOUT,
    ) -> dict[str, object]:
        """
        Run the loop-mount binary with the given operations.

        Args:
            image_path: Path to the root filesystem image.
            fs_type: Filesystem type (auto-detected if not provided).
            files: List of file dicts with path, data (base64), mode, uid, gid.
            copy_dirs: List of directory copy dicts with src, dst, mode.
            commands: List of shell commands to run via chroot.
            resize: Resize dict with action (``"grow"``|``"shrink"``) and bytes.
            timeout: Maximum time in seconds to wait for the binary.

        Returns:
            Parsed JSON response dict on success (contains ``"status": "ok"``).

        Raises:
            LoopMountError: If the binary returns an error response or exits
                with a non-zero code.
            LoopMountTimeoutError: If the binary does not complete within the
                timeout.
        """
        binary = LoopMountManager._resolve_binary_path()
        if binary is not None:
            cmd = [str(binary)]
        else:
            # Development mode: run process.py directly via Python
            cmd = [sys.executable, str(_DEV_PROCESS_PATH)]

        ops = LoopMountManager._build_ops(
            image_path=image_path,
            fs_type=fs_type,
            files=files,
            copy_dirs=copy_dirs,
            commands=commands,
            resize=resize,
        )
        payload_bytes = json.dumps(ops).encode("utf-8")

        logger.debug(
            "Running loop-mount binary: %s "
            "(image=%s, files=%d, commands=%d, resize=%s)",
            cmd[0],
            image_path,
            len(files or []),
            len(commands or []),
            "yes" if resize else "no",
        )

        try:
            proc = run_cmd(
                cmd,
                input=payload_bytes.decode("utf-8"),
                timeout=timeout,
                check=False,
                privileged=True,
            )
        except ProcessError as e:
            if "timed out" in str(e):
                raise LoopMountTimeoutError(
                    f"Loop-mount binary timed out after {timeout}s for {image_path}"
                ) from None
            raise ProcessError(f"Failed to run loop-mount binary: {e}") from e

        if proc.returncode != 0:
            error_msg = LoopMountManager._extract_error(proc)
            logger.debug(
                "Loop-mount binary exited with code %d: %s (cmd=%s, image=%s)",
                proc.returncode,
                error_msg,
                cmd[0],
                image_path,
            )
            raise LoopMountError(f"Loop-mount binary failed: {error_msg}")

        try:
            raw_stdout: str | bytes = proc.stdout
            if isinstance(raw_stdout, bytes):
                raw_stdout = raw_stdout.decode("utf-8", errors="replace")
            parsed: Any = json.loads(raw_stdout)
        except (json.JSONDecodeError, ValueError) as e:
            raise LoopMountError(f"Failed to parse loop-mount response: {e}")

        if not isinstance(parsed, dict):
            raise LoopMountError(
                f"Loop-mount response is not a dict: {type(parsed).__name__}"
            )

        status: object = parsed.get("status", "")
        if status == "error":
            raise LoopMountError(
                f"Loop-mount error: {parsed.get('error', 'unknown')} "
                f"(step: {parsed.get('step', 'unknown')})"
            )

        return parsed

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    # ------------------------------------------------------------------
    # OS detection
    # ------------------------------------------------------------------

    @staticmethod
    def detect_os(
        image_path: str,
        fs_type: str | None = None,
        timeout: int = LOOP_MOUNT_TIMEOUT,
    ) -> str:
        """
        Detect the OS from a root filesystem image using the loop-mount binary.

        Args:
            image_path: Path to the root filesystem image.
            fs_type: Filesystem type hint (auto-detected if not provided).
            timeout: Maximum time in seconds.

        Returns:
            OS type string (e.g., ``"ubuntu"``, ``"debian"``, ``"alpine"``,
            ``"arch"``).

        Raises:
            LoopMountError: If binary fails or response is invalid.
            LoopMountTimeoutError: If the binary does not complete within the
                timeout.
        """
        binary = LoopMountManager._resolve_binary_path()
        if binary is not None:
            cmd = [str(binary)]
        else:
            cmd = [sys.executable, str(_DEV_PROCESS_PATH)]

        payload: dict[str, object] = {
            "image": image_path,
            "action": "detect_os",
        }
        if fs_type:
            payload["fs_type"] = fs_type

        payload_bytes = json.dumps(payload).encode("utf-8")

        logger.debug("Running OS detection: image=%s", image_path)

        try:
            proc = run_cmd(
                cmd,
                input=payload_bytes.decode("utf-8"),
                timeout=timeout,
                check=False,
                privileged=True,
            )
        except ProcessError as e:
            if "timed out" in str(e):
                raise LoopMountTimeoutError(
                    f"OS detection timed out after {timeout}s for {image_path}"
                ) from None
            raise ProcessError(f"Failed to run OS detection: {e}") from e

        if proc.returncode != 0:
            error_msg = LoopMountManager._extract_error(proc)
            logger.debug(
                "OS detection via loop-mount binary exited with code %d: %s "
                "(cmd=%s, image=%s)",
                proc.returncode,
                error_msg,
                cmd[0],
                image_path,
            )
            raise LoopMountError(f"OS detection failed: {error_msg}")

        try:
            raw_stdout: str | bytes = proc.stdout
            if isinstance(raw_stdout, bytes):
                raw_stdout = raw_stdout.decode("utf-8", errors="replace")
            parsed: Any = json.loads(raw_stdout)
        except (json.JSONDecodeError, ValueError) as e:
            raise LoopMountError(f"Failed to parse OS detection response: {e}")

        if not isinstance(parsed, dict):
            raise LoopMountError(
                f"OS detection response is not a dict: {type(parsed).__name__}"
            )

        status: object = parsed.get("status", "")
        if status == "error":
            raise LoopMountError(
                f"Loop-mount OS detection error: {parsed.get('error', 'unknown')} "
                f"(step: {parsed.get('step', 'unknown')})"
            )

        os_type_value: object = parsed.get("os_type")
        if not isinstance(os_type_value, str):
            raise LoopMountError(
                "Invalid OS detection response: missing os_type"
            )

        return os_type_value

    @staticmethod
    def _extract_error(
        proc: subprocess.CompletedProcess[str],
    ) -> str:
        """Extract an error message from a failed loop-mount subprocess."""
        stderr_text = (proc.stderr or "").strip()
        error_msg = stderr_text or f"Exit code {proc.returncode}"
        try:
            raw_stdout = proc.stdout or ""
            err_result: Any = json.loads(raw_stdout)
            if (
                isinstance(err_result, dict)
                and err_result.get("status") == "error"
            ):
                error_msg = err_result.get("error", error_msg)
        except (json.JSONDecodeError, ValueError):
            pass
        return error_msg

    # ------------------------------------------------------------------
    # Cleanup mount
    # ------------------------------------------------------------------

    @staticmethod
    def cleanup_mount(mount_point: str) -> bool:
        """Unmount and remove a stale provision mount point.

        Spawns the mvm-provision binary with ``--umount <mount_point>``
        to perform the unmount + rmdir as root.

        Args:
            mount_point: Absolute path to the mount point directory.

        Returns:
            True if the mount point was successfully cleaned (or didn't
            exist in the first place). False if cleanup failed.
        """
        path = Path(mount_point)
        if not path.exists():
            logger.info(
                "Mount point does not exist, nothing to clean: %s", mount_point
            )
            return False

        binary = LoopMountManager._resolve_binary_path()
        if binary is not None:
            cmd = [str(binary), "--umount", mount_point]
        else:
            cmd = [
                sys.executable,
                str(_DEV_PROCESS_PATH),
                "--umount",
                mount_point,
            ]

        logger.debug("Cleaning stale mount point: %s", mount_point)

        try:
            proc = run_cmd(cmd, privileged=True, timeout=30, check=False)
            return proc.returncode == 0
        except ProcessError:
            logger.warning("Failed to clean stale mount point: %s", mount_point)
            return False

    # ------------------------------------------------------------------
    # Availability check
    # ------------------------------------------------------------------

    @staticmethod
    def is_binary_available() -> bool:
        """Check if loop-mount provisioning is available (compiled binary or dev fallback)."""
        if LoopMountManager._resolve_binary_path() is not None:
            return True
        return _DEV_PROCESS_PATH.exists()
