import grp
import os
import pwd
import shutil
from pathlib import Path

from src.mvmctl.constants import CLI_NAME, MVM_UNIX_GROUP
from src.mvmctl.exceptions import PrivilegeError


class HostInteractiveService:
    @staticmethod
    def check_privileges(binary: str, operation_description: str = "") -> None:
        """Check privileges; if lacking, show interactive guidance with actionable options.

        This wrapper is intended for CLI command handlers. It catches ``PrivilegeError``
        from :func:`check_privileges` and prints structured guidance so the user knows
        exactly what to do — without leaving them with a raw exception traceback.

        Args:
            binary: Absolute path or name of the binary requiring elevated privileges.
            operation_description: Human-readable description of the operation requiring
                privileges (used in error messages).

        Raises:
            PrivilegeError: Re-raised after printing guidance (caller decides exit strategy).
        """
        try:
            if not shutil.which(binary) and not Path(binary).exists():
                raise PrivilegeError(
                    f"Binary not found: {binary}. Run 'mvm host init' to set up required dependencies."
                )

            if os.getuid() == 0:
                return

            try:
                g = grp.getgrnam(MVM_UNIX_GROUP)
            except KeyError as e:
                raise PrivilegeError(
                    f"Group '{MVM_UNIX_GROUP}' does not exist. "
                    f"Run 'sudo mvm host init' to set up privilege management."
                ) from e

            user_pw = pwd.getpwuid(os.getuid())
            username = user_pw.pw_name

            # Check if user is in group via supplementary OR primary group
            is_supplementary_member = username in g.gr_mem
            is_primary_group = user_pw.pw_gid == g.gr_gid
            user_in_group = is_supplementary_member or is_primary_group

            if not user_in_group:
                raise PrivilegeError(
                    f"User '{username}' is not in the '{MVM_UNIX_GROUP}' group. "
                    f"Run 'sudo mvm host init' to configure privileges, "
                    f"then 'newgrp {MVM_UNIX_GROUP}' or log out and back in."
                )

            # User is in group per /etc/group — but check if THIS process has the credentials
            process_gids = set(os.getgroups()) | {os.getgid(), os.getegid()}
            if g.gr_gid not in process_gids:
                raise PrivilegeError(
                    f"Your user is in the '{MVM_UNIX_GROUP}' group, but your current session "
                    f"does not have the group active yet. Please log out and log back in, "
                    f"or run: newgrp {MVM_UNIX_GROUP}"
                )
        except PrivilegeError as exc:
            from mvmctl.utils.console import print_error, print_info, print_warning

            op_str = f" for: {operation_description}" if operation_description else ""
            print_error(f"Elevated privileges required{op_str}")
            print_warning(f"Details: {exc}")
            print_info("")
            print_info("Options:")
            print_info(f"  1. Run with sudo:              sudo {CLI_NAME} ...")
            print_info("  2. Configure persistent access: sudo mvm host init")
            print_info(f"     (then log out and back in, or run: newgrp {MVM_UNIX_GROUP})")
            raise
