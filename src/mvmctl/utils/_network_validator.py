"""Network validation utilities."""

from __future__ import annotations

import ipaddress
import re
from typing import Any

from mvmctl.exceptions import MVMError
from mvmctl.utils.common import CommonUtils
from mvmctl.utils.network import NetworkUtils

# Linux IFNAMSIZ limit for interface names
_IFNAMSIZ = 15


class NetworkValidator:
    """Validate all network-related inputs.

    All methods are static — no instance state needed.
    """

    RESERVED_INTERFACES = frozenset(
        {"lo", "eth0", "eth1", "wlan0", "virbr0", "docker0"}
    )

    @staticmethod
    def validate_name(name: str) -> str:
        """Validate network name.

        Rules: lowercase alphanumeric, hyphen, underscore, starts with
        alphanumeric, 1-31 chars, no dots, no reserved names.

        Args:
            name: Network name to validate.

        Returns:
            The validated name.

        Raises:
            MVMError: If name is invalid.
        """
        # Apply common entity name validation first (uses max_length=31 for networks)
        CommonUtils.validate_entity_name(
            name, entity_type="network", max_length=31
        )

        # Network names must not contain dots
        if "." in name:
            raise MVMError(
                f"Invalid network name '{name}': cannot contain dots"
            )

        # Network names must not be reserved interface names
        if name.lower() in NetworkValidator.RESERVED_INTERFACES:
            raise MVMError(
                f"Invalid network name '{name}': '{name}' is a reserved interface name"
            )

        return name

    @staticmethod
    def validate_subnet(subnet: str) -> str:
        """Validate CIDR subnet notation.

        Args:
            subnet: CIDR string (e.g., "192.168.1.0/24").

        Returns:
            Normalized subnet string (e.g., "192.168.1.0/24").

        Raises:
            MVMError: If subnet is invalid.
        """
        if not subnet:
            raise MVMError("Invalid subnet: cannot be empty")

        if CommonUtils.contains_dangerous_chars(subnet):
            raise MVMError(
                f"Invalid subnet: '{subnet}' contains forbidden characters"
            )

        if " " in subnet:
            raise MVMError(f"Invalid subnet: '{subnet}' cannot contain spaces")

        try:
            network = ipaddress.IPv4Network(subnet, strict=False)
            return str(network)
        except ValueError as e:
            raise MVMError(
                f"Invalid subnet: '{subnet}' is not a valid IPv4 CIDR: {e}"
            ) from e

    @staticmethod
    def validate_ipv4_gateway(gateway: str, *, subnet: str) -> str:
        """Validate IPv4 gateway address.

        Args:
            gateway: IPv4 address string.
            subnet: CIDR subnet the gateway must belong to.

        Returns:
            Normalized gateway IP string.

        Raises:
            MVMError: If gateway is invalid or not in subnet.
        """
        if not gateway:
            raise MVMError("Invalid gateway: cannot be empty")

        if CommonUtils.contains_dangerous_chars(gateway):
            raise MVMError(
                f"Invalid gateway: '{gateway}' contains forbidden characters"
            )

        if " " in gateway:
            raise MVMError(
                f"Invalid gateway: '{gateway}' cannot contain spaces"
            )

        try:
            addr = ipaddress.IPv4Address(gateway)
        except ValueError as e:
            raise MVMError(
                f"Invalid gateway: '{gateway}' is not a valid IPv4 address: {e}"
            ) from e

        if not addr.is_private:
            raise MVMError(
                f"Invalid gateway: '{gateway}' must be a private/internal address"
            )

        network = ipaddress.IPv4Network(subnet, strict=False)
        if addr not in network:
            raise MVMError(
                f"Invalid gateway: '{gateway}' is not within subnet {subnet}"
            )

        if addr == network.network_address:
            raise MVMError(
                f"Invalid gateway: '{gateway}' is the network address of {subnet}"
            )

        return str(addr)

    @staticmethod
    def validate_ipv4_address(
        ip: str,
        *,
        field_name: str = "IP address",
        require_private: bool = False,
        subnet: str | None = None,
        gateway: str | None = None,
    ) -> str:
        """Validate IPv4 address.

        Args:
            ip: IPv4 address string.
            field_name: Field name for error messages.
            require_private: If True, IP must be private/internal.
            subnet: Optional CIDR. IP must be within this range.
            gateway: Optional gateway IP. IP must not equal this.

        Returns:
            Normalized IP string.

        Raises:
            MVMError: If IP is invalid.
        """
        if not ip:
            raise MVMError(f"Invalid {field_name}: cannot be empty")

        if CommonUtils.contains_dangerous_chars(ip):
            raise MVMError(
                f"Invalid {field_name}: '{ip}' contains forbidden characters"
            )

        if " " in ip:
            raise MVMError(
                f"Invalid {field_name}: '{ip}' cannot contain spaces"
            )

        try:
            addr = ipaddress.IPv4Address(ip)
        except ValueError as e:
            raise MVMError(
                f"Invalid {field_name}: '{ip}' is not a valid IPv4 address: {e}"
            ) from e

        if require_private and not addr.is_private:
            raise MVMError(
                f"Invalid {field_name}: '{ip}' must be a private/internal address"
            )

        if subnet is not None:
            network = ipaddress.IPv4Network(subnet, strict=False)
            if addr not in network:
                raise MVMError(
                    f"Invalid {field_name}: '{ip}' is not within subnet {subnet}"
                )
            if addr == network.network_address:
                raise MVMError(
                    f"Invalid {field_name}: '{ip}' is the network address of {subnet}"
                )

        if gateway is not None:
            gateway_addr = ipaddress.IPv4Address(gateway)
            if addr == gateway_addr:
                raise MVMError(
                    f"Invalid {field_name}: '{ip}' is the gateway address"
                )

        return str(addr)

    @staticmethod
    def validate_bridge_name(bridge: str) -> str:
        """Validate bridge interface name.

        Must be lowercase alphanumeric with hyphens/underscores, max 15 chars
        (Linux IFNAMSIZ limit).

        Args:
            bridge: Bridge name to validate.

        Returns:
            The validated bridge name.

        Raises:
            MVMError: If name is invalid.
        """
        if not bridge:
            raise MVMError("Invalid bridge name: cannot be empty")

        if len(bridge) > _IFNAMSIZ:
            raise MVMError(
                f"Invalid bridge name: '{bridge}' exceeds maximum length of {_IFNAMSIZ}"
            )

        if bridge.startswith("-"):
            raise MVMError(
                f"Invalid bridge name: '{bridge}' cannot start with a hyphen"
            )

        if CommonUtils.contains_dangerous_chars(bridge):
            raise MVMError(
                f"Invalid bridge name: '{bridge}' contains forbidden characters "
                "(shell metacharacters, path traversal, or control characters)"
            )

        if not re.match(r"^[a-z0-9_-]+$", bridge):
            raise MVMError(
                f"Invalid bridge name: '{bridge}' must contain only lowercase "
                "alphanumeric, hyphen, and underscore characters"
            )

        # Check if bridge already exists on host (non-mvm interface)
        if NetworkUtils.bridge_exists(bridge):
            raise MVMError(f"Bridge '{bridge}' already exists on this host")

        return bridge

    @staticmethod
    def validate_nat_gateways(gateways: list[str]) -> list[str]:
        """Validate list of NAT gateway interface names.

        Args:
            gateways: List of interface names.

        Returns:
            List of validated interface names.

        Raises:
            MVMError: If any interface name is invalid or does not exist.
        """
        if not gateways:
            raise MVMError("NAT gateways cannot be empty")

        validated: list[str] = []
        for iface in gateways:
            iface = iface.strip()
            if not iface:
                raise MVMError("NAT gateway interface name cannot be empty")

            if len(iface) > _IFNAMSIZ:
                raise MVMError(
                    f"Invalid NAT gateway '{iface}': exceeds maximum length of {_IFNAMSIZ}"
                )

            if CommonUtils.contains_dangerous_chars(iface):
                raise MVMError(
                    f"Invalid NAT gateway '{iface}': contains forbidden characters "
                    "(shell metacharacters, path traversal, or control characters)"
                )

            if not re.match(r"^[a-z0-9_-]+$", iface):
                raise MVMError(
                    f"Invalid NAT gateway '{iface}': must contain only lowercase "
                    "alphanumeric, hyphen, and underscore characters"
                )

            # Check that interface actually exists on the host
            if not NetworkUtils.ensure_interface_ready(iface):
                raise MVMError(
                    f"NAT gateway '{iface}': interface does not exist on this host"
                )

            validated.append(iface)

        return validated

    @staticmethod
    def is_ip_address(value: str) -> bool:
        """Validate that the given string is a valid IPv4 or IPv6 address.

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

    @staticmethod
    def validate_mac(mac: str) -> None:
        """Validate MAC address format.

        Args:
            mac: MAC address to validate.

        Raises:
            MVMError: If MAC address format is invalid.
        """
        MAC_REGEX = re.compile(r"^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$")
        if not MAC_REGEX.match(mac):
            raise MVMError(f"Invalid MAC address format: {mac}")

    @staticmethod
    def validate_subnet_no_overlap(
        subnet: str,
        existing: list[Any],
        exclude_name: str = "",
    ) -> None:
        """Check that subnet doesn't overlap with existing networks.

        Args:
            subnet: CIDR subnet to check.
            existing: List of objects with `.name` and `.subnet` attributes.
            exclude_name: Network name to skip (for updates).

        Raises:
            MVMError: If subnet overlaps with an existing network.
        """
        new_net = ipaddress.IPv4Network(subnet, strict=True)
        for item in existing:
            if item.name == exclude_name:
                continue
            existing_net = ipaddress.IPv4Network(item.subnet, strict=False)
            if new_net.overlaps(existing_net):
                raise MVMError(
                    f"Subnet {subnet} overlaps with network '{item.name}' ({item.subnet})"
                )
