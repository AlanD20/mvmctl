"""SSH connection utilities."""

import logging
import os
import re
import subprocess
from pathlib import Path

from fcm.constants import DEFAULT_VM_SSH_USER
from fcm.exceptions import VMNotFoundError, FCMKeyError, FCMError
from fcm.core.vm_manager import VMManager
from fcm.utils.fs import get_cache_dir
from fcm.utils.validation import is_ip_address

logger = logging.getLogger(__name__)

_VALID_SSH_USERNAME = re.compile(r"^[a-z_][a-z0-9_-]*$")


def _validate_ssh_username(user: str) -> None:
    """Validate that an SSH username matches POSIX conventions.

    Args:
        user: The username string to validate.

    Raises:
        FCMError: If the username contains invalid characters.
    """
    if not _VALID_SSH_USERNAME.match(user):
        raise FCMError(f"Invalid SSH username '{user}': must match ^[a-z_][a-z0-9_-]*$")


def find_ssh_keys(keys_dir: Path | None = None) -> list[Path]:
    """Find SSH private keys in the cache keys directory."""
    if keys_dir is None:
        keys_dir = get_cache_dir() / "keys"
    if not keys_dir.exists():
        return []
    keys = []
    for key_file in keys_dir.glob("id_*"):
        if key_file.suffix == ".pub":
            continue
        keys.append(key_file)
    return keys


def extract_ip_from_config(config_path: Path) -> str | None:
    """Extract IP address from Firecracker JSON config."""
    import json
    import re

    if not config_path.exists():
        return None

    try:
        with open(config_path) as f:
            config = json.load(f)

        boot_args = config.get("boot-source", {}).get("boot_args", "")
        match = re.search(r"ip=(\d+\.\d+\.\d+\.\d+)", boot_args)
        if match:
            return match.group(1)
    except (json.JSONDecodeError, IOError):
        pass

    return None


def build_ssh_command(
    ip: str,
    user: str = DEFAULT_VM_SSH_USER,
    key_path: Path | None = None,
    command: str | None = None,
) -> list[str]:
    """Build SSH command arguments."""
    _validate_ssh_username(user)
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


def exec_ssh(
    ip: str,
    user: str = DEFAULT_VM_SSH_USER,
    key_path: Path | None = None,
    command: str | None = None,
) -> None:
    """Execute SSH, replacing current process."""
    ssh_args = build_ssh_command(ip, user, key_path, command)
    os.execvp("ssh", ssh_args)


def run_ssh(
    ip: str,
    user: str = DEFAULT_VM_SSH_USER,
    key_path: Path | None = None,
    command: str | None = None,
) -> int:
    """Run SSH as subprocess, return exit code."""
    ssh_args = build_ssh_command(ip, user, key_path, command)
    result = subprocess.run(ssh_args)
    return result.returncode


def connect_to_vm(
    vm_name_or_ip: str,
    user: str = DEFAULT_VM_SSH_USER,
    key_path: Path | None = None,
    command: str | None = None,
    exec_mode: bool = True,
    vm_manager: VMManager | None = None,
) -> int:
    """Connect to VM via SSH.

    Args:
        vm_name_or_ip: VM name or IP address
        user: SSH user
        key_path: Specific SSH key to use
        command: Command to execute (optional)
        exec_mode: If True, replace process; if False, run subprocess

    Returns:
        Exit code (0 for success, or subprocess exit code)

    Raises:
        VMNotFoundError: If VM name not found in state
        FCMKeyError: If no SSH keys found or specified key not found
        FCMError: If VM has no IP address
    """
    is_ip = is_ip_address(vm_name_or_ip)

    if is_ip:
        ip = vm_name_or_ip
    else:
        # Look up VM in state via VMManager
        manager = vm_manager if vm_manager is not None else VMManager()
        vm = manager.get(vm_name_or_ip)
        if not vm:
            raise VMNotFoundError(f"VM '{vm_name_or_ip}' not found")
        if not vm.ip:
            raise FCMError(f"VM '{vm_name_or_ip}' has no IP address")
        ip = vm.ip

    if not key_path:
        keys = find_ssh_keys()
        if not keys:
            raise FCMKeyError("No SSH keys found in cache keys directory")
        key_path = keys[0]

    if not key_path.exists():
        raise FCMKeyError(f"SSH key not found: {key_path}")

    key_path.chmod(0o600)
    logger.info("Connecting to %s as %s...", ip, user)

    if exec_mode and not command:
        exec_ssh(ip, user, key_path)
        return 0
    else:
        return run_ssh(ip, user, key_path, command)


def resolve_ssh_key(ssh_key: str | None) -> str | None:
    """Resolve an SSH key from name (key cache) or file path.

    Returns the public key content string, or None.
    When ssh_key is explicitly named but not found, raises FCMKeyError.
    """
    if ssh_key is None:
        # Fall back to any key in cache
        keys_dir = get_cache_dir() / "keys"
        if keys_dir.exists():
            for pub in keys_dir.glob("*.pub"):
                return pub.read_text().strip()
        return None

    # Check key cache first
    keys_dir = get_cache_dir() / "keys"
    cache_key = keys_dir / f"{ssh_key}.pub"
    if cache_key.exists():
        return cache_key.read_text().strip()

    # Treat as file path
    key_path = Path(ssh_key)
    if key_path.exists():
        return key_path.read_text().strip()

    from fcm.core.key_manager import list_keys

    available = list_keys()
    if available:
        names = ", ".join(k.name for k in available)
        raise FCMKeyError(f"SSH key '{ssh_key}' not found.\\nAvailable keys: {names}")
    else:
        raise FCMKeyError(
            f"SSH key '{ssh_key}' not found.\\nNo keys in cache. Add one with: fcm key add <name> <path>"
        )
