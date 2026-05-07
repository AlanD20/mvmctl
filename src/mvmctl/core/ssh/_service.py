"""SSH connection service — stateful SSH operations."""

from __future__ import annotations

import logging
import os
import re
import subprocess
from pathlib import Path

from mvmctl.constants import CONST_FILE_PERMS_PRIVATE_KEY
from mvmctl.exceptions import MVMKeyError, SSHError
from mvmctl.utils._validators import NetworkValidator

logger = logging.getLogger(__name__)

_VALID_SSH_USERNAME = re.compile(r"^[a-z_][a-z0-9_-]*$")


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

        Raises:
            SSHError: If IP is not a valid IP address.
            MVMKeyError: If key_path is provided but does not exist.

        """
        if not NetworkValidator.is_ip_address(ip):
            raise SSHError(f"Invalid IP address: {ip}")

        if key_path is not None and not key_path.exists():
            raise MVMKeyError(f"SSH key not found: {key_path}")

        if key_path is not None:
            key_path.chmod(CONST_FILE_PERMS_PRIVATE_KEY)

        self._ip = ip
        self._user = user
        self._key_path = key_path
        self._timeout = timeout

        SSHService._validate_username(user)
        logger.info("SSH service initialized for %s@%s", user, ip)

    @staticmethod
    def _validate_username(user: str) -> None:
        """Validate that an SSH username matches POSIX conventions."""
        if not _VALID_SSH_USERNAME.match(user):
            raise SSHError(
                f"Invalid SSH username '{user}': must match ^[a-z_][a-z0-9_-]*$"
            )

    def build_command(self, command: str | None = None) -> list[str]:
        """Build SSH command arguments for this connection."""
        ssh_args = [
            "ssh",
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "UserKnownHostsFile=/dev/null",
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
        result = subprocess.run(ssh_args, timeout=self._timeout)
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
