"""Host initialisation, state inspection, prune, clean, reset, and privilege API."""

from __future__ import annotations

import grp
import os
import shutil
from pathlib import Path

from fcm.constants import PROJECT_GROUP
from fcm.core.host import (
    HostChange,
    HostState,
    check_kvm_access,
    check_required_binaries,
    clean_host,
    get_host_state,
    get_ip_forward_status,
    init_host,
    prune_host,
    reset_host,
    restore_host,
)
from fcm.exceptions import PrivilegeError
from fcm.utils.fs import get_cache_dir

__all__ = [
    "HostChange",
    "HostState",
    "check_kvm_access",
    "check_privileges",
    "check_required_binaries",
    "clean_host",
    "default_cache_dir",
    "get_host_state",
    "get_ip_forward_status",
    "init_host",
    "prune_host",
    "reset_host",
    "restore_host",
]


def default_cache_dir() -> Path:
    return get_cache_dir()


def check_privileges(binary: str) -> None:
    """Check that the current process can invoke ``binary`` with elevated privileges.

    Verifies:
    1. The binary exists on the host.
    2. The current user is root OR a member of the project group.

    Raises :class:`PrivilegeError` if either check fails.
    """
    # Check binary exists
    if not shutil.which(binary) and not Path(binary).exists():
        raise PrivilegeError(
            f"Binary not found: {binary}. "
            f"Run 'fcm host init' to set up required dependencies."
        )

    # Check if already root
    if os.getuid() == 0:
        return

    # Check group membership
    import pwd

    try:
        g = grp.getgrnam(PROJECT_GROUP)
        username = pwd.getpwuid(os.getuid()).pw_name
        if username not in g.gr_mem:
            raise PrivilegeError(
                f"User '{username}' is not in the '{PROJECT_GROUP}' group. "
                f"Run 'sudo fcm host init' to configure privileges, "
                f"then 'newgrp {PROJECT_GROUP}' or log out and back in."
            )
    except KeyError:
        raise PrivilegeError(
            f"Group '{PROJECT_GROUP}' does not exist. "
            f"Run 'sudo fcm host init' to set up privilege management."
        )
