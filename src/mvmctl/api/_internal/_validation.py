"""Shared validation helpers."""

from __future__ import annotations

import re

__all__ = [
    "validate_mac",
    "validate_vm_name",
    "validate_boot_args",
    "validate_file_exists",
]

MAC_REGEX = re.compile(r"^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$")


def validate_mac(mac: str) -> None:
    """Validate MAC address format.

    Args:
        mac: MAC address string

    Raises:
        VMCreateError: If MAC is invalid
    """
    if not MAC_REGEX.match(mac):
        from mvmctl.exceptions import VMCreateError

        raise VMCreateError(f"Invalid MAC address format: {mac}")


def validate_vm_name(name: str) -> None:
    """Validate VM/entity name.

    Args:
        name: Name to validate

    Raises:
        VMCreateError: If name is invalid
    """
    from mvmctl.exceptions import VMCreateError

    if not name or len(name) > 255:
        raise VMCreateError(f"Invalid name: {name!r}")
    if not re.match(r"^[a-zA-Z0-9_-]+$", name):
        raise VMCreateError(f"Invalid name format: {name!r}")


def validate_boot_args(boot_args: str, root_uuid: str, guest_ip: str) -> list[str]:
    """Validate boot arguments.

    Args:
        boot_args: Kernel boot arguments
        root_uuid: Root filesystem UUID
        guest_ip: Guest IP address

    Returns:
        List of validation error messages (empty if valid)
    """
    errors: list[str] = []

    if not root_uuid:
        errors.append("root UUID is required")

    if not guest_ip:
        errors.append("guest IP is required")

    if boot_args:
        if "root_uuid" in boot_args and not re.match(
            r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}",
            root_uuid,
        ):
            errors.append(f"Invalid root UUID format: {root_uuid}")

    return errors


def validate_file_exists(path: str | None, description: str) -> None:
    """Validate that a file exists.

    Args:
        path: File path to check
        description: Description for error message

    Raises:
        VMCreateError: If file doesn't exist
    """
    from pathlib import Path

    from mvmctl.exceptions import VMCreateError

    if path is None:
        return

    file_path = Path(path)
    if not file_path.exists():
        raise VMCreateError(f"{description} not found: {path}")
