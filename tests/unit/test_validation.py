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
