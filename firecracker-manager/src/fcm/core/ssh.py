"""SSH connection utilities."""

import os
import subprocess
from pathlib import Path
from typing import Optional

from fcm.utils.console import print_error, print_success


def find_ssh_keys(assets_dir: Path) -> list[Path]:
    """Find SSH private keys in assets/keys/."""
    keys_dir = assets_dir / "keys"
    if not keys_dir.exists():
        return []

    keys = []
    for key_file in keys_dir.glob("id_*"):
        if key_file.suffix == ".pub":
            continue
        keys.append(key_file)
    return keys


def extract_ip_from_config(config_path: Path) -> Optional[str]:
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
    key_path: Optional[Path] = None,
    command: Optional[str] = None,
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
    key_path: Optional[Path] = None,
    command: Optional[str] = None,
) -> None:
    """Execute SSH, replacing current process."""
    ssh_args = build_ssh_command(ip, user, key_path, command)
    os.execvp("ssh", ssh_args)


def run_ssh(
    ip: str,
    user: str = "root",
    key_path: Optional[Path] = None,
    command: Optional[str] = None,
) -> int:
    """Run SSH as subprocess, return exit code."""
    ssh_args = build_ssh_command(ip, user, key_path, command)
    result = subprocess.run(ssh_args)
    return result.returncode


def connect_to_vm(
    vm_name_or_ip: str,
    user: str = "root",
    key_path: Optional[Path] = None,
    command: Optional[str] = None,
    multi_vm_dir: Path = Path("../multi-vm"),
    assets_dir: Path = Path("../assets"),
    exec_mode: bool = True,
) -> int:
    """Connect to VM via SSH.

    Args:
        vm_name_or_ip: VM name or IP address
        user: SSH user
        key_path: Specific SSH key to use
        command: Command to execute (optional)
        multi_vm_dir: Path to multi-vm directory
        assets_dir: Path to assets directory
        exec_mode: If True, replace process; if False, run subprocess

    Returns:
        Exit code (0 for success)
    """
    import re

    is_ip = re.match(r"^\d+\.\d+\.\d+\.\d+$", vm_name_or_ip)

    if is_ip:
        ip = vm_name_or_ip
    else:
        config_path = multi_vm_dir / "env" / vm_name_or_ip / "firecracker.json"
        ip = extract_ip_from_config(config_path)
        if not ip:
            print_error(f"Could not find IP for VM '{vm_name_or_ip}'")
            return 1

    if not key_path:
        keys = find_ssh_keys(assets_dir)
        if not keys:
            print_error("No SSH keys found in assets/keys/")
            return 1
        key_path = keys[0]

    if not key_path.exists():
        print_error(f"SSH key not found: {key_path}")
        return 1

    key_path.chmod(0o600)

    print_success(f"Connecting to {ip} as {user}...")

    if exec_mode and not command:
        exec_ssh(ip, user, key_path)
        return 0
    else:
        return run_ssh(ip, user, key_path, command)
