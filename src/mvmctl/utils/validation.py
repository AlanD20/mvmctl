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


# ---------------------------------------------------------------------------
# Network metadata security validation
# ---------------------------------------------------------------------------

# Linux IFNAMSIZ limit for interface names
IFNAMSIZ = 15

# Shell metacharacters that must be rejected
_SHELL_METACHARACTERS = set(";|&$`\\\"'\n\r\t<>{}[]()")

# Path traversal characters
_PATH_TRAVERSAL_CHARS = set("./~\\")

# Null byte and control characters
_CONTROL_CHARS = set(chr(i) for i in range(32)) | {chr(127)}


def _contains_dangerous_chars(value: str) -> bool:
    """Check if value contains shell metacharacters, path traversal, or control chars."""
    dangerous = _SHELL_METACHARACTERS | _PATH_TRAVERSAL_CHARS | _CONTROL_CHARS
    return any(c in dangerous for c in value)


def validate_interface_name(name: str, field_name: str = "interface") -> str:
    """Validate network interface name for security.

    Prevents command injection through interface names by rejecting:
    - Shell metacharacters (;|&$` etc.)
    - Path traversal characters (../~)
    - Control characters and null bytes
    - Spaces
    - Leading hyphens

    Args:
        name: Interface name to validate
        field_name: Field name for error messages

    Returns:
        The validated interface name

    Raises:
        MVMError: If the name is invalid or contains dangerous characters
    """
    if not name:
        raise MVMError(f"Invalid {field_name}: name cannot be empty")

    if len(name) > IFNAMSIZ:
        raise MVMError(
            f"Invalid {field_name}: '{name}' exceeds maximum length of {IFNAMSIZ} characters"
        )

    if name.startswith("-"):
        raise MVMError(f"Invalid {field_name}: '{name}' cannot start with a hyphen")

    if _contains_dangerous_chars(name):
        raise MVMError(
            f"Invalid {field_name}: '{name}' contains forbidden characters "
            "(shell metacharacters, path traversal, or control characters)"
        )

    if " " in name:
        raise MVMError(f"Invalid {field_name}: '{name}' cannot contain spaces")

    # Allow alphanumeric, hyphen, underscore only
    if not re.match(r"^[a-zA-Z0-9_-]+$", name):
        raise MVMError(
            f"Invalid {field_name}: '{name}' must contain only alphanumeric, "
            "hyphen, and underscore characters"
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
    return validate_interface_name(name, field_name)


def validate_cidr(cidr: str, field_name: str = "CIDR") -> str:
    """Validate CIDR notation and return sanitized version.

    Validates that the CIDR is a valid IPv4 network notation.

    Args:
        cidr: CIDR notation string (e.g., "192.168.1.0/24")
        field_name: Field name for error messages

    Returns:
        The validated CIDR string

    Raises:
        MVMError: If the CIDR is invalid
    """
    if not cidr:
        raise MVMError(f"Invalid {field_name}: CIDR cannot be empty")

    # Check for shell metacharacters and control characters (but allow . and /)
    # CIDR notation legitimately contains dots and slashes
    dangerous_chars = _SHELL_METACHARACTERS | _CONTROL_CHARS
    if any(c in dangerous_chars for c in cidr):
        raise MVMError(
            f"Invalid {field_name}: '{cidr}' contains forbidden characters "
            "(shell metacharacters or control characters)"
        )

    if " " in cidr:
        raise MVMError(f"Invalid {field_name}: '{cidr}' cannot contain spaces")

    try:
        network = ipaddress.IPv4Network(cidr, strict=False)
        return str(network)
    except ValueError as e:
        raise MVMError(f"Invalid {field_name}: '{cidr}' is not a valid IPv4 CIDR: {e}") from e


def validate_ipv4_address(ip: str, field_name: str = "IP address") -> str:
    """Validate IPv4 address and return sanitized version.

    Args:
        ip: IPv4 address string
        field_name: Field name for error messages

    Returns:
        The validated IP address string

    Raises:
        MVMError: If the IP address is invalid
    """
    if not ip:
        raise MVMError(f"Invalid {field_name}: IP address cannot be empty")

    # Check for shell metacharacters and control characters (but allow .)
    # IP addresses legitimately contain dots
    dangerous_chars = _SHELL_METACHARACTERS | _CONTROL_CHARS
    if any(c in dangerous_chars for c in ip):
        raise MVMError(
            f"Invalid {field_name}: '{ip}' contains forbidden characters "
            "(shell metacharacters or control characters)"
        )

    if " " in ip:
        raise MVMError(f"Invalid {field_name}: '{ip}' cannot contain spaces")

    try:
        addr = ipaddress.IPv4Address(ip)
        return str(addr)
    except ValueError as e:
        raise MVMError(f"Invalid {field_name}: '{ip}' is not a valid IPv4 address: {e}") from e


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
    if not gateways_str or not gateways_str.strip():
        raise MVMError("NAT gateways cannot be empty")

    # Split by comma and strip whitespace
    interfaces = [iface.strip() for iface in gateways_str.split(",")]

    # Remove empty strings
    interfaces = [iface for iface in interfaces if iface]

    if not interfaces:
        raise MVMError("NAT gateways cannot be empty")

    # Validate each interface
    validated: list[str] = []
    for iface in interfaces:
        try:
            validated_iface = validate_interface_name(iface, "NAT gateway")
            validated.append(validated_iface)
        except MVMError as e:
            raise MVMError(f"Invalid NAT gateway '{iface}': {e}") from e

    return validated


def sanitize_metadata_string(
    value: str, field_name: str, max_length: int = 255, allow_hyphen: bool = True
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
        raise MVMError(f"Invalid {field_name}: '{value}' must contain only {chars}")

    return value
