"""Tests for utils/validation.py — entity name and boot arg validation."""

import pytest

from mvmctl.exceptions import FCMError
from mvmctl.utils.validation import (
    validate_boot_arg_component,
    validate_entity_name,
    is_ip_address,
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
    with pytest.raises(FCMError, match="Invalid .* name"):
        validate_entity_name(name, "test")


def test_validate_entity_name_includes_entity_type_in_error():
    with pytest.raises(FCMError, match="Invalid VM name"):
        validate_entity_name("INVALID", "VM")

    with pytest.raises(FCMError, match="Invalid network name"):
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
    with pytest.raises(FCMError, match="must not contain spaces or shell metacharacters"):
        validate_boot_arg_component(value, "test_field")


def test_validate_boot_arg_includes_component_name_in_error():
    with pytest.raises(FCMError, match="Invalid guest_ip"):
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
