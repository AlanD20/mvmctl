"""Tests for validation utilities — entity names, IPs, subnets, boot args."""

from __future__ import annotations

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.utils._validators import NetworkValidator, VMValidator
from mvmctl.utils.common import CommonUtils

# ===========================================================================
# validate_entity_name (via CommonUtils)
# ===========================================================================


class TestValidateEntityName:
    """Test entity name validation — the primary defense against injection."""

    @pytest.mark.parametrize(
        ("name", "entity_type", "scenario"),
        [
            ("my-vm", "VM", "basic hyphenated name"),
            ("my_vm", "VM", "underscore name"),
            ("vm42", "VM", "alphanumeric name"),
            ("a", "VM", "single character"),
            ("z" * 63, "VM", "max length (63 chars)"),
            ("test-key", "key", "key with hyphen"),
            ("validname", "image", "simple name"),
        ],
    )
    def test_valid_names(
        self, name: str, entity_type: str, scenario: str
    ) -> None:
        """Valid entity names should pass validation."""
        assert CommonUtils.validate_entity_name(name, entity_type) == name

    @pytest.mark.parametrize(
        ("name", "entity_type", "scenario"),
        [
            ("../../../etc/passwd", "VM", "path traversal"),
            ("..\\..\\..\\windows\\system32", "VM", "windows path traversal"),
            ("vm/../../etc/shadow", "VM", "relative path escape"),
            ("./../etc/hosts", "VM", "current dir escape"),
            ("vm; rm -rf /", "VM", "command injection semicolon"),
            ("vm && cat /etc/passwd", "VM", "command injection &&"),
            ("vm || evil", "VM", "command injection ||"),
            ("vm|cat /etc/passwd", "VM", "command injection pipe"),
            ("vm`whoami`", "VM", "command substitution backtick"),
            ("vm$(id)", "VM", "command substitution dollar"),
            ('vm"evil', "VM", "double quote injection"),
            ("vm'evil", "VM", "single quote injection"),
            ("vm\x00null", "VM", "null byte injection"),
            ("vm\nnewline", "VM", "newline injection"),
            ("vm\ttab", "VM", "tab injection"),
            ("/absolute/path/vm", "VM", "absolute path"),
            ("~/.ssh/id_rsa", "VM", "tilde expansion"),
            ("vm*", "VM", "glob wildcard"),
            ("vm?", "VM", "glob single char"),
            ("vm[abc]", "VM", "glob character class"),
        ],
    )
    def test_rejects_malicious_names(
        self, name: str, entity_type: str, scenario: str
    ) -> None:
        """Entity names with path traversal or shell metacharacters must be rejected."""
        with pytest.raises(MVMError, match="Invalid .* name"):
            CommonUtils.validate_entity_name(name, entity_type)

    def test_rejects_empty(self) -> None:
        """Empty name should be rejected."""
        with pytest.raises(MVMError, match="cannot be empty"):
            CommonUtils.validate_entity_name("", "VM")

    def test_rejects_too_long(self) -> None:
        """Name exceeding max length should be rejected."""
        with pytest.raises(MVMError, match="exceeds maximum length"):
            CommonUtils.validate_entity_name("a" * 64, "VM")

    def test_rejects_hyphen_prefix(self) -> None:
        """Name starting with hyphen should be rejected."""
        with pytest.raises(MVMError, match="cannot start with a hyphen"):
            CommonUtils.validate_entity_name("-myvm", "VM")

    def test_rejects_reserved_names(self) -> None:
        """Reserved names should be rejected."""
        for reserved in ["help", "all", "default", "none", "root"]:
            with pytest.raises(MVMError, match="reserved name"):
                CommonUtils.validate_entity_name(reserved, "VM")

    def test_rejects_ip_like(self) -> None:
        """IP-address-like names should be rejected (dangerous chars first for dotted IPs)."""
        # 192.168.1.1 contains dots (path traversal chars) → caught by dangerous chars
        with pytest.raises(MVMError, match="Invalid .* name"):
            CommonUtils.validate_entity_name("192.168.1.1", "VM")

    def test_rejects_pattern_mismatch(self) -> None:
        """Names with invalid characters should be rejected."""
        with pytest.raises(MVMError, match="must match"):
            CommonUtils.validate_entity_name("MY_VM_UPPER", "VM")

    def test_reserved_network_names(self) -> None:
        """Reserved network names should be rejected."""
        with pytest.raises(MVMError, match="Invalid network name"):
            CommonUtils.validate_entity_name("default", "network")

    # ------------------------------------------------------------------
    # Network-specific validation
    # ------------------------------------------------------------------

    def test_network_name_no_dots(self) -> None:
        """Network names with dots should be rejected (dangerous chars check first)."""
        # Dots are in _PATH_TRAVERSAL_CHARS, so caught by dangerous chars before dot-specific check
        with pytest.raises(MVMError, match="contains forbidden characters"):
            NetworkValidator.validate_name("my.network")

    def test_network_name_reserved_interface(self) -> None:
        """Network names matching reserved interfaces should be rejected."""
        for iface in ["lo", "eth0", "docker0"]:
            with pytest.raises(MVMError, match="reserved interface name"):
                NetworkValidator.validate_name(iface)


# ===========================================================================
# NetworkValidator.validate_ipv4
# ===========================================================================


class TestValidateIPv4:
    def test_valid_ipv4(self) -> None:
        """Valid IPv4 address should pass."""
        assert (
            NetworkValidator.validate_ipv4_address("192.168.1.1")
            == "192.168.1.1"
        )

    def test_empty_ip(self) -> None:
        """Empty IP should be rejected."""
        with pytest.raises(MVMError, match="cannot be empty"):
            NetworkValidator.validate_ipv4_address("")

    def test_ip_with_spaces(self) -> None:
        """IP with spaces should be rejected."""
        with pytest.raises(MVMError, match="cannot contain spaces"):
            NetworkValidator.validate_ipv4_address("192.168.1. 1")

    def test_invalid_ip(self) -> None:
        """Invalid IP format should be rejected."""
        with pytest.raises(MVMError, match="not a valid IPv4 address"):
            NetworkValidator.validate_ipv4_address("not-an-ip")

    def test_ip_out_of_range(self) -> None:
        """IP with octet >255 should be rejected."""
        with pytest.raises(MVMError, match="not a valid IPv4 address"):
            NetworkValidator.validate_ipv4_address("192.168.1.256")

    def test_require_private(self) -> None:
        """Public IP should be rejected when require_private is True."""
        with pytest.raises(MVMError, match="private"):
            NetworkValidator.validate_ipv4_address(
                "8.8.8.8", require_private=True
            )

    def test_private_ip_passes_private_check(self) -> None:
        """Private IP should pass when require_private is True."""
        assert (
            NetworkValidator.validate_ipv4_address(
                "10.0.0.1", require_private=True
            )
            == "10.0.0.1"
        )

    def test_ip_not_in_subnet(self) -> None:
        """IP outside subnet should be rejected."""
        with pytest.raises(MVMError, match="not within subnet"):
            NetworkValidator.validate_ipv4_address(
                "192.168.2.1", subnet="192.168.1.0/24"
            )

    def test_ip_is_network_address(self) -> None:
        """Network address should be rejected when subnet is provided."""
        with pytest.raises(MVMError, match="network address"):
            NetworkValidator.validate_ipv4_address(
                "192.168.1.0", subnet="192.168.1.0/24"
            )

    def test_ip_is_gateway(self) -> None:
        """IP equal to gateway should be rejected."""
        with pytest.raises(MVMError, match="gateway address"):
            NetworkValidator.validate_ipv4_address(
                "192.168.1.1", gateway="192.168.1.1"
            )

    def test_ip_in_subnet_passes(self) -> None:
        """IP within subnet should pass."""
        assert (
            NetworkValidator.validate_ipv4_address(
                "192.168.1.50", subnet="192.168.1.0/24"
            )
            == "192.168.1.50"
        )

    def test_loopback_passes(self) -> None:
        """Loopback address should be accepted (no private check)."""
        assert (
            NetworkValidator.validate_ipv4_address("127.0.0.1") == "127.0.0.1"
        )


# ===========================================================================
# NetworkValidator.validate_subnet
# ===========================================================================


class TestValidateSubnet:
    def test_valid_cidr(self) -> None:
        """Valid CIDR subnet should pass and be normalized."""
        assert (
            NetworkValidator.validate_subnet("192.168.1.0/24")
            == "192.168.1.0/24"
        )

    def test_valid_cidr_non_strict(self) -> None:
        """CIDR with non-network bits set should be normalized."""
        result = NetworkValidator.validate_subnet("192.168.1.10/24")
        assert result == "192.168.1.0/24"

    def test_empty_subnet(self) -> None:
        """Empty subnet should be rejected."""
        with pytest.raises(MVMError, match="cannot be empty"):
            NetworkValidator.validate_subnet("")

    def test_subnet_with_spaces(self) -> None:
        """Subnet with spaces should be rejected."""
        with pytest.raises(MVMError, match="cannot contain spaces"):
            NetworkValidator.validate_subnet("192.168.1.0 /24")

    def test_invalid_cidr_format(self) -> None:
        """Invalid CIDR format should be rejected."""
        with pytest.raises(MVMError, match="not a valid IPv4 CIDR"):
            NetworkValidator.validate_subnet("not-a-cidr")

    def test_invalid_prefix_length(self) -> None:
        """Prefix length > 32 should be rejected."""
        with pytest.raises(MVMError, match="not a valid IPv4 CIDR"):
            NetworkValidator.validate_subnet("192.168.1.0/33")

    def test_ipv6_rejected(self) -> None:
        """IPv6 CIDR should be rejected (IPv4Network)."""
        with pytest.raises(MVMError, match="not a valid IPv4 CIDR"):
            NetworkValidator.validate_subnet("2001:db8::/32")

    def test_private_cidr(self) -> None:
        """Valid private CIDR should pass."""
        assert NetworkValidator.validate_subnet("10.0.0.0/8") == "10.0.0.0/8"


# ===========================================================================
# NetworkValidator.validate_ipv4_gateway
# ===========================================================================


class TestValidateIPv4Gateway:
    def test_valid_gateway(self) -> None:
        """Valid gateway in subnet should pass."""
        assert (
            NetworkValidator.validate_ipv4_gateway(
                "192.168.1.1", subnet="192.168.1.0/24"
            )
            == "192.168.1.1"
        )

    def test_empty_gateway(self) -> None:
        """Empty gateway should be rejected."""
        with pytest.raises(MVMError, match="cannot be empty"):
            NetworkValidator.validate_ipv4_gateway("", subnet="10.0.0.0/8")

    def test_gateway_not_private(self) -> None:
        """Public IP gateway should be rejected."""
        with pytest.raises(MVMError, match="private"):
            NetworkValidator.validate_ipv4_gateway(
                "8.8.8.8", subnet="0.0.0.0/0"
            )

    def test_gateway_not_in_subnet(self) -> None:
        """Gateway outside subnet should be rejected."""
        with pytest.raises(MVMError, match="not within subnet"):
            NetworkValidator.validate_ipv4_gateway(
                "10.0.1.1", subnet="10.0.0.0/24"
            )

    def test_gateway_is_network_address(self) -> None:
        """Gateway equal to network address should be rejected."""
        with pytest.raises(MVMError, match="network address"):
            NetworkValidator.validate_ipv4_gateway(
                "192.168.1.0", subnet="192.168.1.0/24"
            )

    def test_gateway_public_rejected(self) -> None:
        """Non-private gateway should be rejected."""
        with pytest.raises(MVMError, match="private"):
            NetworkValidator.validate_ipv4_gateway(
                "1.2.3.4", subnet="1.0.0.0/8"
            )


# ===========================================================================
# VMValidator.validate_boot_arg_component
# ===========================================================================


class TestValidateBootArgComponent:
    @pytest.mark.parametrize(
        ("value", "scenario"),
        [
            ("10.20.0.2; rm -rf /", "semicolon injection"),
            ("10.20.0.2 && cat /etc/passwd", "logical AND injection"),
            ("10.20.0.2 || evil", "logical OR injection"),
            ("10.20.0.2|cat /etc/passwd", "pipe injection"),
            ("10.20.0.2&background", "background job"),
            ("$(whoami)", "command substitution"),
            ("`id`", "backtick substitution"),
            ('10.20.0.2"evil', "double quote"),
            ("10.20.0.2'evil", "single quote"),
            ("10.20.0.2\\evil", "backslash escape"),
            ("value with spaces", "space injection"),
            ("value\twith\ttabs", "tab injection"),
            ("value\nwith\nnewlines", "newline injection"),
        ],
    )
    def test_rejects_injection(self, value: str, scenario: str) -> None:
        """Boot arguments with shell metacharacters must be rejected."""
        with pytest.raises(
            MVMError, match="must not contain spaces or shell metacharacters"
        ):
            VMValidator.validate_boot_arg_component(value, "guest_ip")

    @pytest.mark.parametrize(
        ("value", "scenario"),
        [
            ("10.20.0.2", "simple IP"),
            ("eth0", "interface name"),
            ("console=ttyS0", "kernel arg with ="),
            ("ro", "single flag"),
            ("panic=1", "simple value"),
            ("net.ifnames=0", "dotted key"),
        ],
    )
    def test_valid_boot_args(self, value: str, scenario: str) -> None:
        """Valid boot argument components should pass."""
        assert VMValidator.validate_boot_arg_component(value) == value

    def test_empty_value_passes(self) -> None:
        """Empty value should pass (validation is for non-empty values)."""
        assert VMValidator.validate_boot_arg_component("") == ""


# ===========================================================================
# VMValidator.validate_boot_args (full boot arg string)
# ===========================================================================


class TestValidateBootArgs:
    def test_valid_boot_args(self) -> None:
        """Complete valid boot argument string should return no errors."""
        errors = VMValidator.validate_boot_args(
            boot_args="console=ttyS0 reboot=k panic=1",
            root_uuid="550e8400-e29b-41d4-a716-446655440000",
            guest_ip="10.0.0.2",
        )
        assert errors == []

    def test_empty_root_uuid(self) -> None:
        """Missing root UUID should produce an error."""
        errors = VMValidator.validate_boot_args(
            boot_args="console=ttyS0",
            root_uuid="",
            guest_ip="10.0.0.2",
        )
        assert "root UUID is required" in errors

    def test_empty_guest_ip(self) -> None:
        """Missing guest IP should produce an error."""
        errors = VMValidator.validate_boot_args(
            boot_args="console=ttyS0",
            root_uuid="550e8400-e29b-41d4-a716-446655440000",
            guest_ip="",
        )
        assert "guest IP is required" in errors

    def test_injection_in_boot_arg_value(self) -> None:
        """Injection in a boot arg value should produce an error."""
        errors = VMValidator.validate_boot_args(
            boot_args="console=ttyS0 guest_ip=$(whoami)",
            root_uuid="550e8400-e29b-41d4-a716-446655440000",
            guest_ip="10.0.0.2",
        )
        assert any("must not contain" in e for e in errors)

    def test_invalid_root_uuid_format(self) -> None:
        """Invalid UUID format should produce an error when root_uuid is in boot_args."""
        errors = VMValidator.validate_boot_args(
            boot_args="console=ttyS0 root_uuid=bad",
            root_uuid="not-a-uuid",
            guest_ip="10.0.0.2",
        )
        assert any("Invalid root UUID format" in e for e in errors)


# ===========================================================================
# NetworkValidator.validate_mac
# ===========================================================================


class TestValidateMac:
    def test_valid_mac(self) -> None:
        """Valid MAC address should pass."""
        NetworkValidator.validate_mac("02:FC:00:00:00:01")

    def test_invalid_mac_format(self) -> None:
        """Invalid MAC address should be rejected."""
        with pytest.raises(MVMError, match="Invalid MAC address format"):
            NetworkValidator.validate_mac("not-a-mac")

    def test_mac_wrong_length(self) -> None:
        """MAC with wrong octet count should be rejected."""
        with pytest.raises(MVMError, match="Invalid MAC address format"):
            NetworkValidator.validate_mac("02:FC:00:00:00")

    def test_mac_lowercase(self) -> None:
        """Lowercase MAC should pass."""
        NetworkValidator.validate_mac("02:fc:00:00:00:01")


# ===========================================================================
# NetworkValidator.is_ip_address
# ===========================================================================


class TestIsIPAddress:
    def test_valid_ipv4(self) -> None:
        """Valid IPv4 address should return True."""
        assert NetworkValidator.is_ip_address("192.168.1.1") is True

    def test_valid_ipv6(self) -> None:
        """Valid IPv6 address should return True."""
        assert NetworkValidator.is_ip_address("::1") is True

    def test_not_an_ip(self) -> None:
        """Non-IP string should return False."""
        assert NetworkValidator.is_ip_address("not-an-ip") is False

    def test_empty_string(self) -> None:
        """Empty string should return False."""
        assert NetworkValidator.is_ip_address("") is False


# ===========================================================================
# Edge cases and bypass attempts
# ===========================================================================


class TestSecurityEdgeCases:
    @pytest.mark.parametrize(
        ("bypass_value", "scenario"),
        [
            ("....//....//....//etc/passwd", "double dot slash bypass"),
            (
                "....\\\\....\\\\....\\\\windows\\\\system32",
                "double backslash bypass",
            ),
            ("..%2f..%2f..%2fetc%2fpasswd", "URL encoded traversal"),
            ("..\\/..\\/..\\/etc/passwd", "mixed slash bypass"),
            ("vm%3b rm -rf /", "URL encoded semicolon"),
            ("vm\x00; rm -rf /", "null byte before injection"),
            ("vm\x00../../../etc/passwd", "null byte before traversal"),
        ],
    )
    def test_edge_cases_rejected(
        self, bypass_value: str, scenario: str
    ) -> None:
        """Edge case bypass attempts must still be rejected."""
        with pytest.raises(MVMError):
            CommonUtils.validate_entity_name(bypass_value, "VM")
