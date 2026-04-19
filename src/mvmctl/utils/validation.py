"""Input validation utilities for entity names and paths."""

import ipaddress
import os
import re
import warnings
from pathlib import Path

from mvmctl.exceptions import MVMError, NetworkError
from mvmctl.utils._network_validator import NetworkValidator
from mvmctl.utils.common import CommonUtils

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
    warnings.warn(
        "validate_entity_name is deprecated, use CommonUtils.validate_entity_name()",
        DeprecationWarning,
        stacklevel=2,
    )
    return CommonUtils.validate_entity_name(name, entity_type=entity_type)


def validate_boot_arg_component(
    value: str, component_name: str = "boot arg"
) -> str:
    """Deprecated: Use VMValidator.validate_boot_arg_component()."""
    warnings.warn(
        "validate_boot_arg_component is deprecated, use VMValidator.validate_boot_arg_component()",
        DeprecationWarning,
        stacklevel=2,
    )
    from mvmctl.utils._vm_validator import VMValidator

    return VMValidator.validate_boot_arg_component(value, component_name)


def is_ip_address(value: str) -> bool:
    """Deprecated: Use NetworkValidator.is_ip_address()."""
    warnings.warn(
        "is_ip_address is deprecated, use NetworkValidator.is_ip_address()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkValidator.is_ip_address(value)


def validate_fs_uuid(uuid: str | None, field_name: str = "fs_uuid") -> None:
    """Deprecated: Use ImageValidator.validate_fs_uuid()."""
    warnings.warn(
        "validate_fs_uuid is deprecated, use ImageValidator.validate_fs_uuid()",
        DeprecationWarning,
        stacklevel=2,
    )
    from mvmctl.utils._image_validator import ImageValidator

    ImageValidator.validate_fs_uuid(uuid, field_name)


def validate_fs_type(fs_type: str | None, field_name: str = "fs_type") -> None:
    """Deprecated: Use ImageValidator.validate_fs_type()."""
    warnings.warn(
        "validate_fs_type is deprecated, use ImageValidator.validate_fs_type()",
        DeprecationWarning,
        stacklevel=2,
    )
    from mvmctl.utils._image_validator import ImageValidator

    ImageValidator.validate_fs_type(fs_type, field_name)


# ---------------------------------------------------------------------------
# Network metadata security validation
# ---------------------------------------------------------------------------

# Linux IFNAMSIZ limit for interface names
IFNAMSIZ = 15


def _contains_dangerous_chars(value: str) -> bool:
    """Check if value contains shell metacharacters, path traversal, or control chars."""
    return CommonUtils.contains_dangerous_chars(value)


def validate_interface_name(name: str, field_name: str = "interface") -> str:
    """Deprecated: Use CommonUtils.validate_entity_name()."""
    warnings.warn(
        "validate_interface_name is deprecated, use CommonUtils.validate_entity_name()",
        DeprecationWarning,
        stacklevel=2,
    )
    CommonUtils.validate_entity_name(name, entity_type=field_name)
    if len(name) > 15:
        raise MVMError(
            f"Invalid {field_name}: '{name}' exceeds maximum length of 15 characters"
        )
    return name


def validate_bridge_name(name: str, field_name: str = "bridge") -> str:
    """Validate bridge interface name for security.

    Same rules as interface names but with clearer error messages.

    Args:
        name: Bridge name to validate
        field_name: Field name for error messages

    Returns:
        The validated bridge name

    Raises:
        MVMError: If the name is invalid or contains dangerous characters
    """
    warnings.warn(
        "validate_bridge_name is deprecated, use NetworkValidator.validate_bridge_name()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkValidator.validate_bridge_name(name)


def validate_subnet(subnet: str, field_name: str = "SUBNET") -> str:
    """Validate SUBNET notation and return sanitized version.

    Validates that the SUBNET is a valid IPv4 network notation.

    Args:
        subnet: SUBNET notation string (e.g., "192.168.1.0/24")
        field_name: Field name for error messages

    Returns:
        The validated SUBNET string

    Raises:
        MVMError: If the SUBNET is invalid
    """
    warnings.warn(
        "validate_subnet is deprecated, use NetworkValidator.validate_subnet()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkValidator.validate_subnet(subnet)


def validate_ipv4_address(
    ip: str,
    field_name: str = "IP address",
    require_private: bool = False,
    subnet: str | None = None,
    gateway: str | None = None,
) -> str:
    """Deprecated: Use NetworkValidator.validate_ipv4_address()."""
    warnings.warn(
        "validate_ipv4_address is deprecated, use NetworkValidator.validate_ipv4_address()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkValidator.validate_ipv4_address(
        ip,
        field_name=field_name,
        require_private=require_private,
        subnet=subnet,
        gateway=gateway,
    )


def validate_nat_gateways(gateways_str: str) -> list[str]:
    """Validate and parse comma-separated NAT gateway interfaces.

    Splits the input string by commas, validates each interface name,
    and returns a list of validated interface names.

    Args:
        gateways_str: Comma-separated interface names (e.g., "eth0,eth1")

    Returns:
        List of validated interface names

    Raises:
        MVMError: If any interface name is invalid
    """
    warnings.warn(
        "validate_nat_gateways is deprecated, use NetworkValidator.validate_nat_gateways()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkValidator.validate_nat_gateways(gateways_str.split(","))


def sanitize_metadata_string(
    value: str,
    field_name: str,
    max_length: int = 255,
    allow_hyphen: bool = True,
) -> str:
    """Sanitize a metadata string field for safe use.

    Removes/rejects:
    - Shell metacharacters: ; | & $ ` \\ " ' \n \r \t < > { } [ ] ( )
    - Path traversal: . / ~ \
    - Null bytes
    - Control characters

    Args:
        value: String value to sanitize
        field_name: Field name for error messages
        max_length: Maximum allowed length (default 255)
        allow_hyphen: Whether to allow hyphens (default True)

    Returns:
        The sanitized string

    Raises:
        MVMError: If the value contains dangerous characters or exceeds limits
    """
    if not value:
        raise MVMError(f"Invalid {field_name}: value cannot be empty")

    if len(value) > max_length:
        raise MVMError(
            f"Invalid {field_name}: value exceeds maximum length of {max_length} characters"
        )

    if _contains_dangerous_chars(value):
        raise MVMError(
            f"Invalid {field_name}: value contains forbidden characters "
            "(shell metacharacters, path traversal, or control characters)"
        )

    if " " in value:
        raise MVMError(f"Invalid {field_name}: value cannot contain spaces")

    # Build allowed character pattern
    allowed_pattern = r"^[a-zA-Z0-9_" + (r"-" if allow_hyphen else r"") + r"]+$"

    if not re.match(allowed_pattern, value):
        chars = "alphanumeric and underscore"
        if allow_hyphen:
            chars += " and hyphen"
        raise MVMError(
            f"Invalid {field_name}: '{value}' must contain only {chars}"
        )

    return value


def validate_mac(mac: str) -> None:
    """Deprecated: Use NetworkValidator.validate_mac()."""
    warnings.warn(
        "validate_mac is deprecated, use NetworkValidator.validate_mac()",
        DeprecationWarning,
        stacklevel=2,
    )
    NetworkValidator.validate_mac(mac)


def validate_vm_name(name: str) -> str:
    """Validate VM name format.

    Args:
        name: Name to validate

    Returns:
        The validated name.

    Raises:
        MVMError: If name is invalid
    """
    warnings.warn(
        "validate_vm_name is deprecated, use VMValidator.validate_name()",
        DeprecationWarning,
        stacklevel=2,
    )
    from mvmctl.utils._vm_validator import VMValidator

    return VMValidator.validate_name(name)


def validate_boot_args(
    boot_args: str, root_uuid: str, guest_ip: str
) -> list[str]:
    """Deprecated: Use VMValidator.validate_boot_args()."""
    warnings.warn(
        "validate_boot_args is deprecated, use VMValidator.validate_boot_args()",
        DeprecationWarning,
        stacklevel=2,
    )
    from mvmctl.utils._vm_validator import VMValidator

    return VMValidator.validate_boot_args(boot_args, root_uuid, guest_ip)


def validate_file_exists(path: str | None, description: str) -> None:
    """Validate that a file exists.

    Args:
        path: File path to check
        description: Description for error message

    Raises:
        MVMError: If file doesn't exist
    """
    if path is None:
        return

    file_path = Path(path)
    if not file_path.exists():
        raise MVMError(f"{description} not found: {path}")


def validate_cidr(
    subnet: str, field_name: str = "subnet"
) -> ipaddress.IPv4Network:
    """Deprecated: DO NOT USE. Use NetworkValidator.validate_subnet() instead."""
    warnings.warn(
        "validate_cidr is deprecated and should not be used. "
        "Use NetworkValidator.validate_subnet() for subnet validation.",
        DeprecationWarning,
        stacklevel=2,
    )
    try:
        return ipaddress.IPv4Network(subnet, strict=False)
    except ValueError as e:
        raise MVMError(f"Invalid {field_name}: {subnet!r} - {e}") from e


def validate_ip_in_subnet(ip: str, subnet: str, field_name: str = "IP") -> None:
    """Validate that an IP is within a subnet.

    Args:
        ip: IP address to validate
        subnet: Subnet CIDR notation
        field_name: Field name for error messages

    Raises:
        NetworkError: If IP is outside subnet
    """
    try:
        network = ipaddress.IPv4Network(subnet, strict=False)
        ip_addr = ipaddress.IPv4Address(ip.split("/")[0])
        if ip_addr not in network:
            raise NetworkError(f"{field_name} {ip} is outside subnet {subnet}")
    except ValueError as e:
        raise NetworkError(f"Invalid {field_name}: {e}") from e


def validate_file_readable(path: Path, field_name: str = "file") -> None:
    """Validate that a file exists and is readable.

    Args:
        path: File path to check
        field_name: Field name for error messages

    Raises:
        MVMError: If file doesn't exist or isn't readable
    """
    if not path.exists():
        raise MVMError(f"{field_name} not found: {path}")
    if not os.access(path, os.R_OK):
        raise MVMError(f"{field_name} not readable: {path}")


def validate_file_executable(path: Path, field_name: str = "binary") -> None:
    """Validate that a file exists and is executable.

    Args:
        path: File path to check
        field_name: Field name for error messages

    Raises:
        MVMError: If file doesn't exist or isn't executable
    """
    if not path.exists():
        raise MVMError(f"{field_name} not found: {path}")
    if not os.access(path, os.X_OK):
        raise MVMError(f"{field_name} not executable: {path}")


def validate_resource_count(
    current: int,
    limit: int,
    resource_name: str = "resource",
) -> None:
    """Validate that resource count is within limit.

    Args:
        current: Current count of resources
        limit: Maximum allowed resources
        resource_name: Name of the resource for error messages

    Raises:
        MVMError: If limit is reached
    """
    if current >= limit:
        raise MVMError(
            f"{resource_name} limit reached ({limit}). "
            f"Remove existing {resource_name}s before creating new ones."
        )


def validate_range(
    value: int,
    min_val: int,
    max_val: int,
    field_name: str = "value",
) -> None:
    """Validate that a value is within range.

    Args:
        value: Value to validate
        min_val: Minimum allowed value
        max_val: Maximum allowed value
        field_name: Field name for error messages

    Raises:
        MVMError: If value is out of range
    """
    if not (min_val <= value <= max_val):
        raise MVMError(
            f"Invalid {field_name}={value}: must be between {min_val} and {max_val}"
        )
