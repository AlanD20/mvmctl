"""Input validation utilities for entity names and paths."""

import ipaddress
import re

from mvmctl.exceptions import MVMError

_NAME_PATTERN = re.compile(r"^[a-z0-9][a-z0-9._-]{0,30}$")


def validate_entity_name(name: str, entity_type: str = "entity") -> str:
    """Validate and return a safe entity name (VM, network, key).

    Args:
        name: The name to validate.
        entity_type: Label for error messages (e.g. "VM", "network", "key").

    Returns:
        The validated name.

    Raises:
        MVMError: If the name doesn't match the allowed pattern.
    """
    if not _NAME_PATTERN.match(name):
        raise MVMError(
            f"Invalid {entity_type} name '{name}': must match [a-z0-9][a-z0-9._-]{{0,30}}"
        )
    return name


def validate_boot_arg_component(value: str, component_name: str) -> str:
    """Validate a kernel boot argument component has no injection characters.

    Args:
        value: The value to validate.
        component_name: Label for error messages.

    Returns:
        The validated value.

    Raises:
        MVMError: If the value contains spaces or shell metacharacters.
    """
    if re.search(r"[\s;|&$`\\\"']", value):
        raise MVMError(
            f"Invalid {component_name} '{value}': must not contain spaces or shell metacharacters"
        )
    return value


def is_ip_address(value: str) -> bool:
    """Validate that the given string is a valid IPv4 or IPv6 address.

    Uses the ipaddress module for proper validation instead of regex,
    which can accept invalid IPs like "999.999.999.999".

    Args:
        value: The string to validate as an IP address.

    Returns:
        True if the value is a valid IP address, False otherwise.
    """
    try:
        ipaddress.ip_address(value)
        return True
    except ValueError:
        return False


def validate_fs_uuid(uuid: str | None, field_name: str = "fs_uuid") -> None:
    """Validate filesystem UUID format.

    Supports standard UUID formats:
    - 11111111-2222-3333-4444-555555555555

    Args:
        uuid: UUID string to validate
        field_name: Field name for error messages

    Raises:
        MVMError: If UUID format is invalid
    """
    if uuid is None:
        return

    # Standard UUID pattern: 8-4-4-4-12 hex digits
    uuid_pattern = re.compile(
        r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"
    )

    if not uuid_pattern.match(uuid):
        raise MVMError(
            f"Invalid {field_name} format: '{uuid}'. "
            "Expected format: XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX"
        )


def validate_fs_type(fs_type: str | None, field_name: str = "fs_type") -> None:
    """Validate filesystem type.

    Args:
        fs_type: Filesystem type string
        field_name: Field name for error messages

    Raises:
        MVMError: If filesystem type is invalid
    """
    if fs_type is None:
        return

    supported_types = {"ext4", "btrfs", "xfs", "ext3", "ext2"}

    if fs_type.lower() not in supported_types:
        raise MVMError(
            f"Invalid {field_name}: '{fs_type}'. "
            f"Supported types: {', '.join(sorted(supported_types))}"
        )
