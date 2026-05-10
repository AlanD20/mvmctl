"""Host privilege helpers."""

from __future__ import annotations

import grp
import os
import pwd
import shutil
from pathlib import Path

from mvmctl.constants import CLI_NAME, MVM_UNIX_GROUP
from mvmctl.exceptions import PrivilegeError


class HostPrivilegeHelper:
    @staticmethod
    def check_privileges(binary: str, operation_description: str = "") -> None:
        """
        Check privileges; if lacking, raise ``PrivilegeError`` with structured details.

        This is a pure check — no console output. Callers (typically the CLI layer
        via ``handle_errors``) are responsible for rendering guidance to the user.

        Args:
            binary: Absolute path or name of the binary requiring elevated privileges.
            operation_description: Human-readable description of the operation requiring
                privileges (used in error messages).

        Raises:
            PrivilegeError: If privileges are insufficient, with ``details`` dict.

        """
        op_str = (
            f" for: {operation_description}" if operation_description else ""
        )

        missing_binaries: list[str] = []
        if not shutil.which(binary) and not Path(binary).exists():
            missing_binaries.append(binary)

        if os.getuid() == 0:
            return

        try:
            g = grp.getgrnam(MVM_UNIX_GROUP)
        except KeyError as e:
            raise PrivilegeError(
                f"Elevated privileges required{op_str}",
                details={
                    "message": (
                        f"Group '{MVM_UNIX_GROUP}' does not exist. "
                        f"Run 'sudo mvm host init' to set up privilege management."
                    ),
                    "missing_capabilities": [],
                    "missing_binaries": missing_binaries,
                    "suggestions": [
                        f"Run with sudo: sudo {CLI_NAME} ...",
                        "Configure persistent access: sudo mvm host init",
                        f"Then log out and back in, or run: newgrp {MVM_UNIX_GROUP}",
                    ],
                },
            ) from e

        user_pw = pwd.getpwuid(os.getuid())
        username = user_pw.pw_name

        # Check if user is in group via supplementary OR primary group
        is_supplementary_member = username in g.gr_mem
        is_primary_group = user_pw.pw_gid == g.gr_gid
        user_in_group = is_supplementary_member or is_primary_group

        if not user_in_group:
            raise PrivilegeError(
                f"Elevated privileges required{op_str}",
                details={
                    "message": (
                        f"User '{username}' is not in the '{MVM_UNIX_GROUP}' group. "
                        f"Run 'sudo mvm host init' to configure privileges, "
                        f"then 'newgrp {MVM_UNIX_GROUP}' or log out and back in."
                    ),
                    "missing_capabilities": [],
                    "missing_binaries": missing_binaries,
                    "suggestions": [
                        f"Run with sudo: sudo {CLI_NAME} ...",
                        "Configure persistent access: sudo mvm host init",
                        f"Then log out and back in, or run: newgrp {MVM_UNIX_GROUP}",
                    ],
                },
            )

        # User is in group per /etc/group — but check if THIS process has the credentials
        process_gids = set(os.getgroups()) | {os.getgid(), os.getegid()}
        if g.gr_gid not in process_gids:
            raise PrivilegeError(
                f"Elevated privileges required{op_str}",
                details={
                    "message": (
                        f"Your user is in the '{MVM_UNIX_GROUP}' group, but your current session "
                        f"does not have the group active yet. Please log out and log back in, "
                        f"or run: newgrp {MVM_UNIX_GROUP}"
                    ),
                    "missing_capabilities": [],
                    "missing_binaries": missing_binaries,
                    "suggestions": [
                        f"Run with sudo: sudo {CLI_NAME} ...",
                        f"Activate group in current session: newgrp {MVM_UNIX_GROUP}",
                        "Or log out and back in for group membership to take effect",
                    ],
                },
            )


__all__ = ["HostPrivilegeHelper"]
