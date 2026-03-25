"""Host privilege management."""

from __future__ import annotations

import grp
import logging
import os
import pwd
import re
import shutil
import subprocess
from pathlib import Path

from mvmctl.constants import CLI_NAME, PRIVILEGED_BINARIES, PROJECT_GROUP
from mvmctl.exceptions import HostError, PrivilegeError

logger = logging.getLogger(__name__)


def check_privileges(binary: str) -> None:
    """Verify the current user has the required privileges to run a binary.

    Checks that the binary exists and either the process is root or the current
    user is a member of the project group configured by ``fcm host init``.

    Args:
        binary: Absolute path or name of the binary to check.

    Raises:
        PrivilegeError: If the binary is missing or the user lacks group membership.
    """
    if not shutil.which(binary) and not Path(binary).exists():
        raise PrivilegeError(
            f"Binary not found: {binary}. Run 'fcm host init' to set up required dependencies."
        )

    if os.getuid() == 0:
        return

    try:
        g = grp.getgrnam(PROJECT_GROUP)
        username = pwd.getpwuid(os.getuid()).pw_name
        if username not in g.gr_mem:
            raise PrivilegeError(
                f"User '{username}' is not in the '{PROJECT_GROUP}' group. "
                f"Run 'sudo mvm host init' to configure privileges, "
                f"then 'newgrp {PROJECT_GROUP}' or log out and back in."
            )
    except KeyError as e:
        raise PrivilegeError(
            f"Group '{PROJECT_GROUP}' does not exist. "
            f"Run 'sudo mvm host init' to set up privilege management."
        ) from e


def check_privileges_interactive(binary: str, operation_description: str = "") -> None:
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
        check_privileges(binary)
    except PrivilegeError as exc:
        from mvmctl.utils.console import print_error, print_info, print_warning

        op_str = f" for: {operation_description}" if operation_description else ""
        print_error(f"Elevated privileges required{op_str}")
        print_warning(f"Details: {exc}")
        print_info("")
        print_info("Options:")
        print_info(f"  1. Run with sudo:              sudo {CLI_NAME} ...")
        print_info("  2. Configure persistent access: sudo mvm host init")
        print_info(f"     (then log out and back in, or run: newgrp {PROJECT_GROUP})")
        raise


def _get_current_user() -> str:
    """Return the username of the current process owner."""
    return pwd.getpwuid(os.getuid()).pw_name


def _group_exists(group_name: str) -> bool:
    """Return ``True`` if the named group exists on the system."""
    try:
        grp.getgrnam(group_name)
        return True
    except KeyError:
        return False


def _user_in_group(username: str, group_name: str) -> bool:
    """Return ``True`` if ``username`` is a member of ``group_name``."""
    try:
        g = grp.getgrnam(group_name)
        return username in g.gr_mem
    except KeyError:
        return False


def _create_group(group_name: str) -> bool:
    """Create a system group. Return ``True`` if created, ``False`` if it already existed.

    Args:
        group_name: Name of the group to create.

    Returns:
        ``True`` if the group was newly created, ``False`` if it already existed.

    Raises:
        HostError: If the group creation command fails or is not found.
    """
    if _group_exists(group_name):
        return False
    try:
        subprocess.run(
            ["groupadd", "--system", group_name],
            capture_output=True,
            text=True,
            check=True,
        )
        return True
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to create group {group_name}: {e}") from e
    except FileNotFoundError as e:
        raise HostError("groupadd command not found") from e


def _add_user_to_group(username: str, group_name: str) -> bool:
    """Add a user to a group. Return ``True`` if added, ``False`` if already a member.

    Args:
        username: The user to add.
        group_name: Target group name.

    Returns:
        ``True`` if the user was added, ``False`` if they were already in the group.

    Raises:
        HostError: If the usermod command fails or is not found.
    """
    if _user_in_group(username, group_name):
        return False
    try:
        subprocess.run(
            ["usermod", "-aG", group_name, username],
            capture_output=True,
            text=True,
            check=True,
        )
        return True
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to add {username} to group {group_name}: {e}") from e
    except FileNotFoundError as e:
        raise HostError("usermod command not found") from e


def _validate_sudoers_binaries() -> None:
    """Verify that all privileged binaries referenced in the sudoers drop-in exist on disk.

    Raises:
        HostError: If any required binary is missing, with an install hint.
    """
    for binary in PRIVILEGED_BINARIES:
        if not Path(binary).exists():
            pkg_map = {
                "/usr/sbin/ip": "iproute2",
                "/usr/sbin/iptables": "iptables",
                "/usr/sbin/iptables-restore": "iptables",
                "/usr/sbin/iptables-save": "iptables",
                "/usr/sbin/sysctl": "procps",
            }
            pkg = pkg_map.get(binary, "unknown package")
            raise HostError(f"Required binary not found: {binary} (install {pkg})")


def _generate_sudoers_content(group_name: str) -> str:
    """Generate the sudoers drop-in content granting the group passwordless access.

    Args:
        group_name: The group to grant NOPASSWD privileges.

    Returns:
        A string containing the sudoers drop-in file content.

    Raises:
        HostError: If the group name is invalid.
    """
    if not re.fullmatch(r"[a-z][a-z0-9_-]{0,30}", group_name):
        raise HostError(f"Invalid group name: {group_name!r}")
    binaries_str = ", ".join(PRIVILEGED_BINARIES)
    return (
        f"# Managed by {CLI_NAME} — do not edit manually.\n"
        f"# To remove: {CLI_NAME} host reset\n"
        f"%{group_name} ALL=(root) NOPASSWD: {binaries_str}\n"
    )


def _write_sudoers(path: Path, group_name: str) -> None:
    """Write and validate the sudoers drop-in file for the given group.

    The generated content is validated with ``visudo -c`` before being written
    to ``path`` with mode 0o440.

    Args:
        path: Destination path for the sudoers drop-in file.
        group_name: The group to include in the sudoers rule.

    Raises:
        HostError: If visudo validation fails or the file cannot be written.
    """
    import tempfile

    content = _generate_sudoers_content(group_name)
    with tempfile.NamedTemporaryFile(mode="w", suffix=".sudoers", delete=False) as f:
        f.write(content)
        tmp_path = f.name
    try:
        result = subprocess.run(
            ["visudo", "-c", "-f", tmp_path],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise HostError(f"Generated sudoers file failed visudo validation: {result.stderr}")
    except FileNotFoundError:
        raise HostError("visudo not found — cannot validate sudoers syntax")
    finally:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        path.chmod(0o440)
    except OSError as e:
        raise HostError(f"Failed to write sudoers file {path}: {e}") from e


def _remove_sudoers(path: Path) -> bool:
    """Remove the sudoers drop-in file if it exists.

    Args:
        path: Path to the sudoers drop-in file.

    Returns:
        ``True`` if the file was removed, ``False`` if it did not exist.

    Raises:
        HostError: If the file exists but cannot be removed.
    """
    if not path.exists():
        return False
    try:
        path.unlink()
        return True
    except OSError as e:
        raise HostError(f"Failed to remove sudoers file {path}: {e}") from e


def _remove_group(group_name: str) -> bool:
    """Delete a system group if it exists.

    Args:
        group_name: Name of the group to remove.

    Returns:
        ``True`` if the group was removed, ``False`` if it did not exist.

    Raises:
        HostError: If the groupdel command fails or is not found.
    """
    if not _group_exists(group_name):
        return False
    try:
        subprocess.run(
            ["groupdel", group_name],
            capture_output=True,
            text=True,
            check=True,
        )
        return True
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to remove group {group_name}: {e}") from e
    except FileNotFoundError as e:
        raise HostError("groupdel command not found") from e
