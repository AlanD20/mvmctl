"""Host privilege management."""

from __future__ import annotations

import grp
import logging
import os
import pwd
import shutil
import subprocess
from pathlib import Path

from fcm.constants import CLI_NAME, PRIVILEGED_BINARIES, PROJECT_GROUP
from fcm.exceptions import HostError, PrivilegeError

logger = logging.getLogger(__name__)

def check_privileges(binary: str) -> None:
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
                f"Run 'sudo fcm host init' to configure privileges, "
                f"then 'newgrp {PROJECT_GROUP}' or log out and back in."
            )
    except KeyError as e:
        raise PrivilegeError(
            f"Group '{PROJECT_GROUP}' does not exist. "
            f"Run 'sudo fcm host init' to set up privilege management."
        ) from e

def _get_current_user() -> str:
    return pwd.getpwuid(os.getuid()).pw_name

def _group_exists(group_name: str) -> bool:
    try:
        grp.getgrnam(group_name)
        return True
    except KeyError:
        return False

def _user_in_group(username: str, group_name: str) -> bool:
    try:
        g = grp.getgrnam(group_name)
        return username in g.gr_mem
    except KeyError:
        return False

def _create_group(group_name: str) -> bool:
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
    binaries_str = ", ".join(PRIVILEGED_BINARIES)
    return (
        f"# Managed by {CLI_NAME} — do not edit manually.\n"
        f"# To remove: {CLI_NAME} host reset\n"
        f"%{group_name} ALL=(root) NOPASSWD: {binaries_str}\n"
    )

def _write_sudoers(path: Path, group_name: str) -> None:
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
        logger.warning("visudo not found — skipping sudoers validation")
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
    if not path.exists():
        return False
    try:
        path.unlink()
        return True
    except OSError as e:
        raise HostError(f"Failed to remove sudoers file {path}: {e}") from e

def _remove_group(group_name: str) -> bool:
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
