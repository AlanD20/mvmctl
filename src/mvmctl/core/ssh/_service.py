"""SSH connection service — stateful SSH operations."""

from __future__ import annotations

import logging
import os
from pathlib import Path

from mvmctl.constants import CONST_FILE_PERMS_PRIVATE_KEY
from mvmctl.exceptions import ProcessError
from mvmctl.utils._system import run_cmd

logger = logging.getLogger(__name__)


class SSHService:
    """Stateful SSH connection service — stores connection params as instance state."""

    def __init__(
        self,
        ip: str,
        user: str,
        key_path: Path | None = None,
        timeout: int | None = None,
    ) -> None:
        """
        Initialize SSH service with connection parameters.

        Args:
            ip: IP address of the target host.
            user: SSH username.
            key_path: Optional path to SSH private key.
            timeout: Optional SSH connection timeout in seconds.

        Notes:
            Input format validation (IP, username, key existence) is handled
            by the API layer before this constructor is called.

        """
        if key_path is not None:
            key_path.chmod(CONST_FILE_PERMS_PRIVATE_KEY)

        self._ip = ip
        self._user = user
        self._key_path = key_path
        self._timeout = timeout

        logger.info("SSH service initialized for %s@%s", user, ip)

    def build_command(self, command: str | None = None) -> list[str]:
        """Build SSH command arguments for this connection."""
        ssh_args = [
            "ssh",
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "UserKnownHostsFile=/dev/null",
            "-o",
            "BatchMode=yes",
            "-o",
            "ServerAliveInterval=2",
            "-o",
            "ServerAliveCountMax=3",
        ]

        if self._timeout is not None:
            ssh_args.extend(["-o", f"ConnectTimeout={self._timeout}"])

        if self._key_path and self._key_path.exists():
            ssh_args.extend(["-i", str(self._key_path)])

        ssh_args.append(f"{self._user}@{self._ip}")

        if command:
            ssh_args.append(command)

        return ssh_args

    def exec_command(self, command: str | None = None) -> None:
        """Execute SSH, replacing current process."""
        ssh_args = self.build_command(command)
        os.execvp("ssh", ssh_args)

    def run_command(self, command: str | None = None) -> int:
        """Run SSH as subprocess, return exit code."""
        ssh_args = self.build_command(command)
        try:
            result = run_cmd(
                ssh_args, timeout=self._timeout, capture=False, check=False
            )
        except ProcessError as e:
            if "timed out" in str(e):
                raise ProcessError(
                    f"SSH command timed out after {self._timeout}s"
                ) from None
            raise
        return result.returncode

    def connect(
        self,
        command: str | None = None,
        *,
        exec_mode: bool = True,
    ) -> int:
        """
        Connect to host via SSH.

        Args:
            command: Command to execute (optional).
            exec_mode: If True, replace process; if False, run subprocess.

        Returns:
            Exit code (0 for success, or subprocess exit code).

        """
        if exec_mode and not command:
            self.exec_command(command)
            return 0
        else:
            return self.run_command(command)
