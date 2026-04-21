"""Tests for utils/validation.py — entity name and boot arg validation."""

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.utils.validation import (
    is_ip_address,
    validate_boot_arg_component,
    validate_entity_name,
)

# ---------------------------------------------------------------------------
# validate_entity_name — valid inputs
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "name",
    [
        "myvm",
        "vm1",
        "a",
        "test-vm",
        "test.vm",
        "test_vm",
        "0leading-digit",
        "a" * 31,  # max length (1 start char + 30 continuation)
    ],
)
def test_validate_entity_name_accepts_valid(name: str):
    assert validate_entity_name(name, "VM") == name


# ---------------------------------------------------------------------------
# validate_entity_name — invalid inputs
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "name,reason",
    [
        ("", "empty string"),
        ("UPPERCASE", "uppercase letters"),
        ("Mixed", "mixed case"),
        ("../evil", "path traversal"),
        ("has space", "spaces"),
        ("-leading-dash", "leading dash"),
        (".leading-dot", "leading dot"),
        ("_leading-underscore", "leading underscore"),
        ("special!char", "exclamation mark"),
        ("semi;colon", "semicolon"),
        ("pipe|char", "pipe"),
        ("back\\slash", "backslash"),
        ("a" * 32, "too long (32 chars)"),
        ("name\twith\ttabs", "tabs"),
        ("name\nwith\nnewlines", "newlines"),
    ],
)
def test_validate_entity_name_rejects_invalid(name: str, reason: str):
    with pytest.raises(MVMError, match="Invalid .* name"):
        validate_entity_name(name, "test")


def test_validate_entity_name_includes_entity_type_in_error():
    with pytest.raises(MVMError, match="Invalid VM name"):
        validate_entity_name("INVALID", "VM")

    with pytest.raises(MVMError, match="Invalid network name"):
        validate_entity_name("BAD!", "network")


# ---------------------------------------------------------------------------
# validate_boot_arg_component — valid inputs
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "value",
    [
        "10.20.0.2",
        "255.255.255.0",
        "192.168.1.1",
        "landlock,lockdown,yama",
        "pci=off",
    ],
)
def test_validate_boot_arg_accepts_valid(value: str):
    assert validate_boot_arg_component(value, "test") == value


# ---------------------------------------------------------------------------
# validate_boot_arg_component — invalid inputs (shell metacharacters)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "value,reason",
    [
        ("10.20.0.2; rm -rf /", "semicolon injection"),
        ("10.20.0.2 || evil", "space + double pipe"),
        ("10.20.0.2|evil", "single pipe"),
        ("10.20.0.2&evil", "ampersand"),
        ("$(whoami)", "command substitution dollar"),
        ("`whoami`", "command substitution backtick"),
        ('10.20.0.2"evil', "double quote"),
        ("10.20.0.2'evil", "single quote"),
        ("10.20.0.2\\evil", "backslash"),
        ("value with spaces", "spaces"),
        ("value\twith\ttab", "tabs"),
    ],
)
def test_validate_boot_arg_rejects_metacharacters(value: str, reason: str):
    with pytest.raises(MVMError, match="must not contain spaces or shell metacharacters"):
        validate_boot_arg_component(value, "test_field")


def test_validate_boot_arg_includes_component_name_in_error():
    with pytest.raises(MVMError, match="Invalid guest_ip"):
        validate_boot_arg_component("10.0.0.1;evil", "guest_ip")


# ---------------------------------------------------------------------------
# is_ip_address — IPv4 and IPv6 validation (issue #25)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "ip",
    [
        "192.168.1.1",
        "10.0.0.1",
        "255.255.255.255",
        "0.0.0.0",
        "1.2.3.4",
        "::1",
        "fe80::1",
        "2001:db8::1",
    ],
)
def test_is_ip_address_accepts_valid_ips(ip: str):
    """Valid IPs should return True."""
    assert is_ip_address(ip) is True


@pytest.mark.parametrize(
    "ip",
    [
        "999.999.999.999",  # Out of range octets
        "256.1.1.1",  # Octet too large
        "192.168.1",  # Missing octet
        "192.168.1.1.1",  # Extra octet
        "192.168.1.",  # Trailing dot
        "192.168.1.a",  # Non-numeric
        "abc.def.ghi.jkl",  # All non-numeric
        "",  # Empty string
        "...",  # Just dots
        ":::",  # Invalid IPv6
    ],
)
def test_is_ip_address_rejects_invalid_ips(ip: str):
    """Invalid IPs should return False."""
    assert is_ip_address(ip) is False


# ---------------------------------------------------------------------------
# validate_fs_uuid — filesystem UUID validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "uuid",
    [
        "123e4567-e89b-12d3-a456-426614174000",  # Standard UUID format
        "123E4567-E89B-12D3-A456-426614174000",  # Upper case
        "123e4567-e89b-12d3-a456-426614174000",  # Lower case
        "00000000-0000-0000-0000-000000000000",  # All zeros
        "ffffffff-ffff-ffff-ffff-ffffffffffff",  # All Fs
        "FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF",  # All Fs upper case
    ],
)
def test_validate_fs_uuid_accepts_valid(uuid: str):
    """Test valid UUID formats are accepted."""
    from mvmctl.utils.validation import validate_fs_uuid

    validate_fs_uuid(uuid)  # Should not raise


@pytest.mark.parametrize(
    "uuid,reason",
    [
        ("not-a-uuid", "plain string"),
        ("123e4567e89b12d3a456426614174000", "missing dashes"),
        ("123e4567-e89b-12d3-a456", "too short"),
        ("123e4567-e89b-12d3-a456-426614174000-extra", "too long"),
        ("123e4567-e89b-12d3-a456-42661417400g", "invalid hex char g"),
        ("", "empty string"),
        ("123e4567-e89b-12d3-a456-42661417400", "one char short"),
        ("123e4567-e89b-12d3-a456-4266141740000", "one char too many"),
    ],
)
def test_validate_fs_uuid_rejects_invalid(uuid: str, reason: str):
    """Test invalid UUID formats are rejected."""
    from mvmctl.utils.validation import validate_fs_uuid

    with pytest.raises(MVMError, match="Invalid.*format"):
        validate_fs_uuid(uuid)


def test_validate_fs_uuid_allows_none():
    """Test None UUID is allowed (no validation)."""
    from mvmctl.utils.validation import validate_fs_uuid

    validate_fs_uuid(None)  # Should not raise


def test_validate_fs_uuid_includes_field_name_in_error():
    """Test field name is included in error message."""
    from mvmctl.utils.validation import validate_fs_uuid

    with pytest.raises(MVMError, match="Invalid root_uuid format"):
        validate_fs_uuid("invalid-uuid", "root_uuid")


# ---------------------------------------------------------------------------
# validate_fs_type — filesystem type validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "fs_type",
    [
        "ext4",
        "btrfs",
        "xfs",
        "ext3",
        "ext2",
        "EXT4",  # Case insensitive - upper
        "BTRFS",  # Case insensitive - upper
        "XFS",  # Case insensitive - upper
        "Ext4",  # Case insensitive - mixed
        "BtrFs",  # Case insensitive - mixed
    ],
)
def test_validate_fs_type_accepts_valid(fs_type: str):
    """Test valid filesystem types are accepted."""
    from mvmctl.utils.validation import validate_fs_type

    validate_fs_type(fs_type)  # Should not raise


@pytest.mark.parametrize(
    "fs_type,reason",
    [
        ("ntfs", "Windows NTFS"),
        ("fat32", "FAT32"),
        ("vfat", "VFAT"),
        ("zfs", "ZFS"),
        ("reiserfs", "ReiserFS"),
        ("jfs", "JFS"),
        ("", "empty string"),
        ("ext5", "non-existent ext version"),
        ("unknown", "unknown type"),
    ],
)
def test_validate_fs_type_rejects_invalid(fs_type: str, reason: str):
    """Test invalid filesystem types are rejected."""
    from mvmctl.utils.validation import validate_fs_type

    with pytest.raises(MVMError, match="Invalid.*fs_type"):
        validate_fs_type(fs_type)


def test_validate_fs_type_allows_none():
    """Test None fs_type is allowed (no validation)."""
    from mvmctl.utils.validation import validate_fs_type

    validate_fs_type(None)  # Should not raise


def test_validate_fs_type_includes_field_name_in_error():
    """Test field name is included in error message."""
    from mvmctl.utils.validation import validate_fs_type

    with pytest.raises(MVMError, match="Invalid root_fs_type"):
        validate_fs_type("ntfs", "root_fs_type")


def test_validate_fs_type_lists_supported_types_in_error():
    """Test error message lists supported filesystem types."""
    from mvmctl.utils.validation import validate_fs_type

    with pytest.raises(MVMError, match="Supported types:.*btrfs.*ext2.*ext3.*ext4.*xfs"):
        validate_fs_type("ntfs")


# ---------------------------------------------------------------------------
# validate_interface_name — security validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "name",
    [
        "eth0",
        "wlan0",
        "enp0s3",
        "br0",
        "docker0",
        "virbr0",
        "lo",
        "eth-0",
        "eth_0",
        "a" * 15,  # max length (IFNAMSIZ)
        "ABC",  # uppercase allowed
        "123abc",  # digits allowed
    ],
)
def test_validate_interface_name_accepts_valid(name: str):
    """Test valid interface names are accepted."""
    from mvmctl.utils.validation import validate_interface_name

    assert validate_interface_name(name) == name


@pytest.mark.parametrize(
    "name,reason",
    [
        ("", "empty string"),
        ("eth0; rm -rf /", "semicolon injection"),
        ("eth0|cat /etc/passwd", "pipe injection"),
        ("eth0&&evil", "ampersand injection"),
        ("eth0||evil", "double pipe injection"),
        ("$(whoami)", "command substitution dollar"),
        ("`whoami`", "command substitution backtick"),
        ("eth0\nreboot", "newline injection"),
        ("eth0\reboot", "carriage return injection"),
        ("eth0\ttab", "tab character"),
        (" lo ", "spaces"),
        ("../../etc/passwd", "path traversal"),
        ("~/evil", "home directory traversal"),
        ("eth\x00", "null byte"),
        ("a" * 16, "too long (exceeds IFNAMSIZ)"),
        ("-eth0", "leading hyphen"),
        ("eth.0", "dot character"),
        ("eth/0", "slash character"),
        ("eth\\0", "backslash"),
        ("eth{0}", "curly braces"),
        ("eth[0]", "square brackets"),
        ("eth(0)", "parentheses"),
        ("eth<0>", "angle brackets"),
        ("eth'0", "single quote"),
        ('eth"0', "double quote"),
    ],
)
def test_validate_interface_name_rejects_injection_attempts(name: str, reason: str):
    """Test interface name validation rejects injection attempts."""
    from mvmctl.utils.validation import validate_interface_name

    with pytest.raises(MVMError, match="Invalid.*interface"):
        validate_interface_name(name)


def test_validate_interface_name_includes_field_name_in_error():
    """Test field name is included in error message."""
    from mvmctl.utils.validation import validate_interface_name

    with pytest.raises(MVMError, match="Invalid nat_interface"):
        validate_interface_name("eth0;evil", "nat_interface")


# ---------------------------------------------------------------------------
# validate_bridge_name — security validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "name",
    [
        "br0",
        "mvm-default",
        "docker0",
        "virbr0",
        "br_0",
        "BRIDGE",  # uppercase allowed
        "br-0",
    ],
)
def test_validate_bridge_name_accepts_valid(name: str):
    """Test valid bridge names are accepted."""
    from mvmctl.utils.validation import validate_bridge_name

    assert validate_bridge_name(name) == name


@pytest.mark.parametrize(
    "name,reason",
    [
        ("", "empty string"),
        ("br0; rm -rf /", "semicolon injection"),
        ("br0|evil", "pipe injection"),
        ("br0&&evil", "ampersand injection"),
        ("$(id)", "command substitution"),
        ("`id`", "backtick injection"),
        ("br0\n", "newline injection"),
        (" br0 ", "spaces"),
        ("../../etc/passwd", "path traversal"),
        ("a" * 16, "too long"),
        ("-br0", "leading hyphen"),
    ],
)
def test_validate_bridge_name_rejects_injection_attempts(name: str, reason: str):
    """Test bridge name validation rejects injection attempts."""
    from mvmctl.utils.validation import validate_bridge_name

    with pytest.raises(MVMError, match="Invalid.*bridge"):
        validate_bridge_name(name)


# ---------------------------------------------------------------------------
# validate_cidr — security validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "subnet",
    [
        "192.168.1.0/24",
        "10.0.0.0/8",
        "172.16.0.0/12",
        "192.168.100.0/24",
        "0.0.0.0/0",
        "255.255.255.255/32",
    ],
)
def test_validate_subnet_accepts_valid(subnet: str):
    """Test valid subnet notations are accepted."""
    from mvmctl.utils.validation import validate_subnet

    assert validate_subnet(subnet) == subnet


@pytest.mark.parametrize(
    "subnet,reason",
    [
        ("", "empty string"),
        ("192.168.1.0/24; rm -rf /", "semicolon injection"),
        ("192.168.1.0/24|cat /etc/passwd", "pipe injection"),
        ("192.168.1.0/24&&evil", "ampersand injection"),
        ("$(whoami)/24", "command substitution"),
        ("`whoami`/24", "backtick injection"),
        ("192.168.1.0/24\n", "newline injection"),
        (" 192.168.1.0/24 ", "spaces"),
        ("../../etc/passwd", "path traversal"),
        ("192.168.1.0/33", "invalid prefix length"),
        ("999.999.999.999/24", "invalid IP"),
        ("192.168.1.0/24/extra", "extra parts"),
    ],
)
def test_validate_subnet_rejects_injection_attempts(subnet: str, reason: str):
    """Test subnet validation rejects injection attempts."""
    from mvmctl.utils.validation import validate_subnet

    with pytest.raises(MVMError, match="Invalid.*SUBNET"):
        validate_subnet(subnet)


def test_validate_subnet_includes_field_name_in_error():
    """Test field name is included in error message."""
    from mvmctl.utils.validation import validate_subnet

    with pytest.raises(MVMError, match="Invalid subnet"):
        validate_subnet("192.168.1.0/24;evil", "subnet")


# ---------------------------------------------------------------------------
# validate_ipv4_address — security validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "ip",
    [
        "192.168.1.1",
        "10.0.0.1",
        "172.16.0.1",
        "0.0.0.0",
        "255.255.255.255",
        "127.0.0.1",
    ],
)
def test_validate_ipv4_address_accepts_valid(ip: str):
    """Test valid IPv4 addresses are accepted."""
    from mvmctl.utils.validation import validate_ipv4_address

    assert validate_ipv4_address(ip) == ip


@pytest.mark.parametrize(
    "ip,reason",
    [
        ("", "empty string"),
        ("192.168.1.1; rm -rf /", "semicolon injection"),
        ("192.168.1.1|cat /etc/passwd", "pipe injection"),
        ("192.168.1.1&&evil", "ampersand injection"),
        ("$(whoami)", "command substitution"),
        ("`whoami`", "backtick injection"),
        ("192.168.1.1\n", "newline injection"),
        (" 192.168.1.1 ", "spaces"),
        ("../../etc/passwd", "path traversal"),
        ("999.999.999.999", "invalid octets"),
        ("256.1.1.1", "octet too large"),
        ("192.168.1", "missing octet"),
        ("192.168.1.1.1", "extra octet"),
        ("192.168.1.a", "non-numeric"),
        ("::1", "IPv6 not supported"),
        ("2001:db8::1", "IPv6 not supported"),
    ],
)
def test_validate_ipv4_address_rejects_injection_attempts(ip: str, reason: str):
    """Test IPv4 address validation rejects injection attempts."""
    from mvmctl.utils.validation import validate_ipv4_address

    with pytest.raises(MVMError, match="Invalid.*IP address"):
        validate_ipv4_address(ip)


def test_validate_ipv4_address_includes_field_name_in_error():
    """Test field name is included in error message."""
    from mvmctl.utils.validation import validate_ipv4_address

    with pytest.raises(MVMError, match="Invalid ipv4 gateway"):
        validate_ipv4_address("192.168.1.1;evil", "ipv4 gateway")


# ---------------------------------------------------------------------------
# sanitize_metadata_string — security validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "value",
    [
        "valid_name",
        "valid-name",
        "valid_name_123",
        "ValidName",
        "VALID123",
        "a" * 255,  # max length
    ],
)
def test_sanitize_metadata_string_accepts_valid(value: str):
    """Test valid metadata strings are accepted."""
    from mvmctl.utils.validation import sanitize_metadata_string

    assert sanitize_metadata_string(value, "test_field") == value


@pytest.mark.parametrize(
    "value,reason",
    [
        ("", "empty string"),
        ("value; rm -rf /", "semicolon injection"),
        ("value|cat /etc/passwd", "pipe injection"),
        ("value&&evil", "ampersand injection"),
        ("value||evil", "double pipe injection"),
        ("$(whoami)", "command substitution"),
        ("`whoami`", "backtick injection"),
        ("value\nreboot", "newline injection"),
        ("value\rtab", "carriage return injection"),
        ("value\ttab", "tab character"),
        (" value ", "spaces"),
        ("../../etc/passwd", "path traversal"),
        ("~/evil", "home directory traversal"),
        ("./evil", "dot path traversal"),
        ("value\x00", "null byte"),
        ("value{evil}", "curly braces"),
        ("value[evil]", "square brackets"),
        ("value(evil)", "parentheses"),
        ("value<evil>", "angle brackets"),
        ("value'evil", "single quote"),
        ('value"evil', "double quote"),
        ("value\\evil", "backslash"),
        ("a" * 256, "too long"),
        ("value.test", "dot character"),
        ("value/test", "slash character"),
    ],
)
def test_sanitize_metadata_string_rejects_injection_attempts(value: str, reason: str):
    """Test metadata string sanitization rejects injection attempts."""
    from mvmctl.utils.validation import sanitize_metadata_string

    with pytest.raises(MVMError, match="Invalid.*test_field"):
        sanitize_metadata_string(value, "test_field")


def test_sanitize_metadata_string_custom_max_length():
    """Test custom max_length parameter."""
    from mvmctl.utils.validation import sanitize_metadata_string

    # Should pass with default max_length=255
    long_value = "a" * 255
    assert sanitize_metadata_string(long_value, "test_field") == long_value

    # Should fail with custom max_length=10
    with pytest.raises(MVMError, match="exceeds maximum length"):
        sanitize_metadata_string("a" * 11, "test_field", max_length=10)


def test_sanitize_metadata_string_no_hyphen():
    """Test allow_hyphen=False parameter."""
    from mvmctl.utils.validation import sanitize_metadata_string

    # Should pass with hyphen allowed (default)
    assert sanitize_metadata_string("valid-name", "test_field") == "valid-name"

    # Should fail with hyphen disallowed
    with pytest.raises(MVMError, match="must contain only"):
        sanitize_metadata_string("valid-name", "test_field", allow_hyphen=False)


def test_sanitize_metadata_string_includes_field_name_in_error():
    """Test field name is included in error message."""
    from mvmctl.utils.validation import sanitize_metadata_string

    with pytest.raises(MVMError, match="Invalid custom_field"):
        sanitize_metadata_string("value;evil", "custom_field")


# ---------------------------------------------------------------------------
# validate_nat_gateways — comma-separated interface validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "gateways_str",
    [
        "eth0",
        "eth0,eth1",
        "eth0, eth1, eth2",
        "wlan0",
        "enp0s3,wlan0",
    ],
)
def test_validate_nat_gateways_accepts_valid(gateways_str: str):
    """Test valid NAT gateway strings are accepted."""
    from mvmctl.utils.validation import validate_nat_gateways

    result = validate_nat_gateways(gateways_str)
    assert isinstance(result, list)
    assert len(result) > 0
    assert all(isinstance(iface, str) for iface in result)


@pytest.mark.parametrize(
    "gateways_str,reason",
    [
        ("", "empty string"),
        ("   ", "only whitespace"),
        ("eth0;evil", "semicolon injection"),
        ("eth0|cat /etc/passwd", "pipe injection"),
        ("eth0&&evil", "ampersand injection"),
        ("eth0,eth1;evil", "injection in second interface"),
    ],
)
def test_validate_nat_gateways_rejects_invalid(gateways_str: str, reason: str):
    """Test NAT gateway validation rejects invalid inputs."""
    from mvmctl.utils.validation import validate_nat_gateways

    with pytest.raises(MVMError, match="Invalid.*NAT gateway|cannot be empty"):
        validate_nat_gateways(gateways_str)


def test_validate_nat_gateways_returns_list():
    """Test that validate_nat_gateways returns a list."""
    from mvmctl.utils.validation import validate_nat_gateways

    result = validate_nat_gateways("eth0,eth1")
    assert isinstance(result, list)
    assert len(result) == 2
    assert "eth0" in result
    assert "eth1" in result


def test_validate_interface_name_rejects_at_sign():
    from mvmctl.utils.validation import validate_interface_name

    with pytest.raises(MVMError):
        validate_interface_name("eth@0")


def test_validate_nat_gateways_empty_after_split():
    from mvmctl.utils.validation import validate_nat_gateways

    with pytest.raises(MVMError, match="cannot be empty"):
        validate_nat_gateways(",")


def test_sanitize_metadata_string_allow_hyphen_error_message():
    from mvmctl.utils.validation import sanitize_metadata_string

    with pytest.raises(MVMError, match="hyphen"):
        sanitize_metadata_string("value@here", "field", allow_hyphen=True)
