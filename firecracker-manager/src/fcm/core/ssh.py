"""SSH connection utilities."""

import logging
import os
import subprocess
from pathlib import Path

from fcm.exceptions import VMNotFoundError, FCMKeyError, FCMError
from fcm.core.vm_manager import VMManager
from fcm.utils.fs import get_cache_dir

logger = logging.getLogger(__name__)


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
    user: str = "root",
    key_path: Path | None = None,
    command: str | None = None,
) -> list[str]:
    """Build SSH command arguments."""
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
    user: str = "root",
    key_path: Path | None = None,
    command: str | None = None,
) -> None:
    """Execute SSH, replacing current process."""
    ssh_args = build_ssh_command(ip, user, key_path, command)
    os.execvp("ssh", ssh_args)


def run_ssh(
    ip: str,
    user: str = "root",
    key_path: Path | None = None,
    command: str | None = None,
) -> int:
    """Run SSH as subprocess, return exit code."""
    ssh_args = build_ssh_command(ip, user, key_path, command)
    result = subprocess.run(ssh_args)
    return result.returncode


def connect_to_vm(
    vm_name_or_ip: str,
    user: str = "root",
    key_path: Path | None = None,
    command: str | None = None,
    exec_mode: bool = True,
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
    import re

    is_ip = bool(re.match(r"^\d+\.\d+\.\d+\.\d+$", vm_name_or_ip))

    if is_ip:
        ip = vm_name_or_ip
    else:
        # Look up VM in state via VMManager
        manager = VMManager()
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
