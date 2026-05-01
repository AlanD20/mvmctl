"""SSH connection service — stateless SSH operations."""

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
    """Stateless SSH operations service."""

    @staticmethod
    def validate_username(user: str) -> None:
        """Validate that an SSH username matches POSIX conventions."""
        if not _VALID_SSH_USERNAME.match(user):
            raise SSHError(
                f"Invalid SSH username '{user}': must match ^[a-z_][a-z0-9_-]*$"
            )

    @staticmethod
    def build_command(
        ip: str,
        user: str,
        key_path: Path | None = None,
        command: str | None = None,
    ) -> list[str]:
        """Build SSH command arguments."""
        SSHService.validate_username(user)
        ssh_args = [
            "ssh",
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "UserKnownHostsFile=/dev/null",
        ]

        if key_path and key_path.exists():
            ssh_args.extend(["-i", str(key_path)])

        ssh_args.append(f"{user}@{ip}")

        if command:
            ssh_args.append(command)

        return ssh_args

    @staticmethod
    def exec_command(
        ip: str,
        user: str,
        key_path: Path | None = None,
        command: str | None = None,
    ) -> None:
        """Execute SSH, replacing current process."""
        ssh_args = SSHService.build_command(ip, user, key_path, command)
        os.execvp("ssh", ssh_args)

    @staticmethod
    def run_command(
        ip: str,
        user: str,
        key_path: Path | None = None,
        command: str | None = None,
    ) -> int:
        """Run SSH as subprocess, return exit code."""
        ssh_args = SSHService.build_command(ip, user, key_path, command)
        result = subprocess.run(ssh_args)
        return result.returncode

    @staticmethod
    def connect(
        ip: str,
        user: str,
        key_path: Path | None = None,
        command: str | None = None,
        *,
        exec_mode: bool = True,
    ) -> int:
        """
        Connect to host via SSH.

        Args:
            ip: IP address of the host
            user: SSH user
            key_path: Specific SSH key to use (already resolved by API layer)
            command: Command to execute (optional)
            exec_mode: If True, replace process; if False, run subprocess

        Returns:
            Exit code (0 for success, or subprocess exit code)

        Raises:
            SSHError: If IP is not a valid IP address
            MVMKeyError: If key_path is provided but does not exist

        """
        if not NetworkValidator.is_ip_address(ip):
            raise SSHError(f"Invalid IP address: {ip}")

        if key_path is not None and not key_path.exists():
            raise MVMKeyError(f"SSH key not found: {key_path}")

        if key_path is not None:
            key_path.chmod(CONST_FILE_PERMS_PRIVATE_KEY)

        logger.info("Connecting to %s as %s...", ip, user)

        if exec_mode and not command:
            SSHService.exec_command(ip, user, key_path, command)
            return 0
        else:
            return SSHService.run_command(ip, user, key_path, command)
