"""Network utilities: MAC, TAP, IP, iptables, bridges."""

from __future__ import annotations

import ipaddress
import logging
import random
import secrets
import string
import subprocess
import warnings
from pathlib import Path

from mvmctl.constants import CLI_NAME, DEFAULT_GUEST_MAC_PREFIX, bridge_name
from mvmctl.exceptions import NetworkError

logger = logging.getLogger(__name__)

_VIRTUAL_INTERFACE_PREFIXES = ("mvm-", "tap", "br-", "virbr", "docker", "veth")
_EXCLUDED_INTERFACES = ("lo",)


class NetworkUtils:
    """Network computation and system query utilities.

    All methods are static — no instance state needed.
    """

    # --- Subnet Math & Computation ---

    @staticmethod
    def compute_subnet_mask(subnet: str) -> str:
        """Return netmask from CIDR subnet."""
        return str(ipaddress.IPv4Network(subnet, strict=False).netmask)

    @staticmethod
    def compute_prefix_length(subnet: str) -> int:
        """Return prefix length from CIDR subnet."""
        return ipaddress.IPv4Network(subnet, strict=False).prefixlen

    @staticmethod
    def compute_ipv4_gateway(subnet: str) -> str:
        """Compute default gateway IP from subnet (first usable host)."""
        network = ipaddress.IPv4Network(subnet, strict=False)
        hosts_iter = iter(network.hosts())
        return str(next(hosts_iter))

    @staticmethod
    def compute_bridge_name(network_name: str) -> str:
        """Compute bridge name from network name."""
        return f"{CLI_NAME}-{network_name}"

    # --- Naming & Generation ---

    @staticmethod
    def generate_mac() -> str:
        """Generate a MAC address with the project prefix."""
        rand_bytes = secrets.token_bytes(4)
        suffix = ":".join(f"{b:02x}" for b in rand_bytes)
        return f"{DEFAULT_GUEST_MAC_PREFIX}:{suffix}".upper()

    @staticmethod
    def generate_tap_name(network_name: str, vm_name: str) -> str:
        """Generate a TAP device name."""
        rand_suffix = "".join(random.choices(string.ascii_lowercase, k=3))
        return f"{CLI_NAME}-{network_name[:3]}-{vm_name[:3]}-{rand_suffix}"

    # --- IP Allocation ---

    @staticmethod
    def allocate_next_ip(
        existing_ips: list[str],
        subnet: str,
        gateway: str | None = None,
    ) -> str:
        """Allocate the next available IP in a subnet.

        Args:
            existing_ips: List of already allocated IP strings.
            subnet: CIDR subnet (e.g., "10.20.0.0/24").
            gateway: Gateway IP to skip.

        Returns:
            The next available IP string.

        Raises:
            NetworkError: If no IPs are available.
        """
        network = ipaddress.IPv4Network(subnet, strict=False)
        existing_set = set(existing_ips)

        for host in network.hosts():
            ip_str = str(host)
            if gateway is not None and ip_str == gateway:
                continue
            if ip_str not in existing_set:
                return ip_str

        raise NetworkError(f"No available IPs in subnet {subnet}")

    # --- System Queries (Host State) ---

    @staticmethod
    def get_physical_interfaces() -> list[str]:
        """Get available physical network interfaces."""
        try:
            net_path = Path("/sys/class/net")
            if not net_path.exists():
                raise NetworkError("Unable to access /sys/class/net")

            interfaces: list[str] = []
            for entry in net_path.iterdir():
                name = entry.name
                if name in _EXCLUDED_INTERFACES:
                    continue
                if any(
                    name.startswith(prefix)
                    for prefix in _VIRTUAL_INTERFACE_PREFIXES
                ):
                    continue
                interfaces.append(name)

            return sorted(interfaces)
        except OSError as e:
            logger.debug("Failed to list network interfaces", exc_info=True)
            raise NetworkError("Failed to list network interfaces") from e

    @staticmethod
    def detect_outbound_interface() -> str | None:
        """Get the outbound (default route) network interface.

        Returns:
            The interface name (e.g., "eth0") or None if not found.
        """
        try:
            result = subprocess.run(
                ["ip", "route", "show", "default"],
                capture_output=True,
                text=True,
                check=True,
            )
        except subprocess.CalledProcessError:
            logger.debug(
                "Failed to detect outbound network interface", exc_info=True
            )
            return None

        for line in result.stdout.splitlines():
            parts = line.split()
            if "dev" in parts:
                dev_idx = parts.index("dev")
                if dev_idx + 1 < len(parts):
                    return parts[dev_idx + 1]

        return None

    @staticmethod
    def bridge_exists(bridge: str) -> bool:
        """Check if a bridge interface exists."""
        result = subprocess.run(
            ["ip", "link", "show", bridge],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        return result.returncode == 0

    @staticmethod
    def tap_exists(tap: str) -> bool:
        """Check if a TAP interface exists."""
        result = subprocess.run(
            ["ip", "link", "show", tap],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        return result.returncode == 0

    @staticmethod
    def chain_exists(chain: str, table: str = "filter") -> bool:
        """Check if an iptables chain exists."""
        result = subprocess.run(
            ["iptables", "-t", table, "-L", chain, "-n"],
            capture_output=True,
            check=False,
        )
        return result.returncode == 0

    @staticmethod
    def get_tuntap_devices() -> list[str]:
        """List all TUN/TAP devices."""
        result = subprocess.run(
            ["ip", "-o", "link", "show", "type", "tuntap"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return []

        devices: list[str] = []
        for line in result.stdout.splitlines():
            parts = line.split()
            if len(parts) >= 2:
                devices.append(parts[1].rstrip(":"))
        return devices

    @staticmethod
    def get_bridges() -> list[str]:
        """List all bridge interfaces."""
        result = subprocess.run(
            ["ip", "-o", "link", "show", "type", "bridge"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return []

        bridges: list[str] = []
        for line in result.stdout.splitlines():
            parts = line.split()
            if len(parts) >= 2:
                bridges.append(parts[1].rstrip(":"))
        return bridges

    @staticmethod
    def get_bridge_taps(bridge: str) -> list[str]:
        """List all TAP devices currently attached to the bridge."""
        result = subprocess.run(
            ["ip", "link", "show", "master", bridge],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return []

        devices: list[str] = []
        for line in result.stdout.splitlines():
            parts = line.split()
            if parts and parts[0][0].isdigit() and len(parts) >= 2:
                iface = parts[1].rstrip(":")
                devices.append(iface)

        return devices

    @staticmethod
    def ensure_interface_ready(interface: str) -> bool:
        """Ensure a network interface exists and is usable for NAT.

        Checks:
        - Not loopback
        - Interface exists in /sys/class/net
        - Interface is UP (operstate != "down")
        - Interface has an IPv4 address assigned

        Args:
            interface: Interface name to check.

        Returns:
            True if interface is ready.

        Raises:
            NetworkError: If interface is not ready for use.
        """
        if interface == "lo":
            raise NetworkError("Loopback interface 'lo' cannot be used for NAT")

        net_path = Path(f"/sys/class/net/{interface}")
        if not net_path.exists():
            raise NetworkError(f"Interface '{interface}' does not exist")

        operstate_path = net_path / "operstate"
        try:
            state = operstate_path.read_text().strip()
        except OSError:
            state = "unknown"

        if state == "down":
            raise NetworkError(
                f"Interface '{interface}' is down. Bring it up with: ip link set {interface} up"
            )

        try:
            result = subprocess.run(
                ["ip", "-o", "-4", "addr", "show", interface],
                capture_output=True,
                text=True,
                check=False,
            )
        except FileNotFoundError as e:
            raise NetworkError(
                "'ip' command not found — install iproute2"
            ) from e

        if result.returncode != 0 or not result.stdout.strip():
            raise NetworkError(
                f"Interface '{interface}' has no IPv4 address assigned. "
                "NAT requires an interface with a valid IP address."
            )

        return True

    @staticmethod
    def detect_iptables_backend_conflict() -> tuple[bool, str]:
        """Detect mixed iptables backend conflict.

        Checks if both iptables-legacy and iptables-nft have active rules.

        Returns:
            Tuple of (has_conflict, diagnosis_string).
        """
        from mvmctl.utils.process import privileged_cmd as _privileged_cmd

        result = subprocess.run(
            ["iptables", "--version"],
            capture_output=True,
            text=True,
        )
        current_backend = "nft" if "nf_tables" in result.stderr else "legacy"

        legacy_active = False
        try:
            legacy_result = subprocess.run(
                _privileged_cmd(["iptables-legacy", "-L", "-n", "-v"]),
                capture_output=True,
                text=True,
                check=False,
            )
            if legacy_result.returncode == 0:
                for line in legacy_result.stdout.splitlines():
                    parts = line.split()
                    if len(parts) >= 2:
                        try:
                            pkts = int(parts[0])
                            if pkts > 0:
                                legacy_active = True
                                break
                        except ValueError:
                            continue
        except Exception:
            pass

        nft_active = False
        try:
            nft_result = subprocess.run(
                _privileged_cmd(["iptables", "-L", "-n", "-v"]),
                capture_output=True,
                text=True,
                check=False,
            )
            if nft_result.returncode == 0:
                for line in nft_result.stdout.splitlines():
                    parts = line.split()
                    if len(parts) >= 2:
                        try:
                            pkts = int(parts[0])
                            if pkts > 0:
                                nft_active = True
                                break
                        except ValueError:
                            continue
        except Exception:
            pass

        has_conflict = legacy_active and nft_active
        diagnosis = (
            f"iptables backend: {current_backend}, "
            f"legacy active: {legacy_active}, "
            f"nft active: {nft_active}"
        )
        return has_conflict, diagnosis

    @staticmethod
    def get_tap_bridge(tap: str) -> str | None:
        """Get the bridge that a TAP device is attached to.

        Returns:
            Bridge name or None if tap doesn't exist or isn't attached.
        """
        try:
            result = subprocess.run(
                ["ip", "link", "show", tap],
                capture_output=True,
                text=True,
                check=True,
            )
            for line in result.stdout.splitlines():
                if "master" in line:
                    parts = line.split()
                    for i, part in enumerate(parts):
                        if part == "master" and i + 1 < len(parts):
                            return parts[i + 1]
        except subprocess.CalledProcessError:
            pass
        return None

    # --- Internal Helpers ---

    @staticmethod
    def _run_batch(commands: list[str]) -> None:
        """Execute a batch of ip commands using ip -batch mode."""
        from mvmctl.utils.process import privileged_cmd as _privileged_cmd

        batch = "\n".join(commands) + "\n"
        subprocess.run(
            _privileged_cmd(["ip", "-batch", "-"]),
            input=batch,
            text=True,
            check=True,
            capture_output=True,
        )

    @staticmethod
    def bridge_has_subnet(bridge: str, subnet: str) -> bool:
        """Check if a bridge already has a given subnet assigned."""
        result = subprocess.run(
            ["ip", "-o", "addr", "show", bridge],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return False
        return subnet in result.stdout


# =====================================================================
# DEPRECATED — Use NetworkUtils instead
# =====================================================================


def subnet_mask_from_subnet(subnet: str) -> str:
    """Deprecated: Use NetworkUtils.compute_subnet_mask()."""
    warnings.warn(
        "subnet_mask_from_subnet is deprecated, use NetworkUtils.compute_subnet_mask()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.compute_subnet_mask(subnet)


def prefix_len_from_subnet(subnet: str) -> int:
    """Deprecated: Use NetworkUtils.compute_prefix_length()."""
    warnings.warn(
        "prefix_len_from_subnet is deprecated, use NetworkUtils.compute_prefix_length()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.compute_prefix_length(subnet)


def ipv4_gateway_for_subnet(subnet: str) -> str:
    """Deprecated: Use NetworkUtils.compute_ipv4_gateway()."""
    warnings.warn(
        "ipv4_gateway_for_subnet is deprecated, use NetworkUtils.compute_ipv4_gateway()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.compute_ipv4_gateway(subnet)


def bridge_name_for(network_name: str) -> str:
    """Deprecated: Use NetworkUtils.compute_bridge_name()."""
    warnings.warn(
        "bridge_name_for is deprecated, use NetworkUtils.compute_bridge_name()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.compute_bridge_name(network_name)


def generate_mac() -> str:
    """Deprecated: Use NetworkUtils.generate_mac()."""
    warnings.warn(
        "generate_mac is deprecated, use NetworkUtils.generate_mac()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.generate_mac()


def generate_tap_name(network_name: str, vm_name: str) -> str:
    """Deprecated: Use NetworkUtils.generate_tap_name()."""
    warnings.warn(
        "generate_tap_name is deprecated, use NetworkUtils.generate_tap_name()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.generate_tap_name(network_name, vm_name)


def allocate_ip(
    existing_ips: list[str], subnet: str, ipv4_gateway: str | None = None
) -> str:
    """Deprecated: Use NetworkUtils.allocate_next_ip()."""
    warnings.warn(
        "allocate_ip is deprecated, use NetworkUtils.allocate_next_ip()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.allocate_next_ip(existing_ips, subnet, ipv4_gateway)


def list_network_interfaces() -> list[str]:
    """Deprecated: Use NetworkUtils.get_physical_interfaces()."""
    warnings.warn(
        "list_network_interfaces is deprecated, use NetworkUtils.get_physical_interfaces()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.get_physical_interfaces()


def get_default_interface() -> str | None:
    """Deprecated: Use NetworkUtils.detect_outbound_interface()."""
    warnings.warn(
        "get_default_interface is deprecated, use NetworkUtils.detect_outbound_interface()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.detect_outbound_interface()


def bridge_exists(bridge: str | None = None) -> bool:
    """Deprecated: Use NetworkUtils.bridge_exists()."""
    warnings.warn(
        "bridge_exists is deprecated, use NetworkUtils.bridge_exists()",
        DeprecationWarning,
        stacklevel=2,
    )
    effective_bridge = bridge if bridge is not None else bridge_name()
    return NetworkUtils.bridge_exists(effective_bridge)


def tap_exists(tap_name: str) -> bool:
    """Deprecated: Use NetworkUtils.tap_exists()."""
    warnings.warn(
        "tap_exists is deprecated, use NetworkUtils.tap_exists()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.tap_exists(tap_name)


def chain_exists(chain: str, table: str = "filter") -> bool:
    """Deprecated: Use NetworkUtils.chain_exists()."""
    warnings.warn(
        "chain_exists is deprecated, use NetworkUtils.chain_exists()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.chain_exists(chain, table)


def list_tuntap_devices() -> list[str]:
    """Deprecated: Use NetworkUtils.get_tuntap_devices()."""
    warnings.warn(
        "list_tuntap_devices is deprecated, use NetworkUtils.get_tuntap_devices()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.get_tuntap_devices()


def list_bridges() -> list[str]:
    """Deprecated: Use NetworkUtils.get_bridges()."""
    warnings.warn(
        "list_bridges is deprecated, use NetworkUtils.get_bridges()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.get_bridges()


def get_tap_devices(bridge: str | None = None) -> list[str]:
    """Deprecated: Use NetworkUtils.get_bridge_taps()."""
    warnings.warn(
        "get_tap_devices is deprecated, use NetworkUtils.get_bridge_taps()",
        DeprecationWarning,
        stacklevel=2,
    )
    effective_bridge = bridge if bridge is not None else bridge_name()
    return NetworkUtils.get_bridge_taps(effective_bridge)


def validate_network_interface(interface: str) -> bool:
    """Deprecated: Use NetworkUtils.ensure_interface_ready()."""
    warnings.warn(
        "validate_network_interface is deprecated, use NetworkUtils.ensure_interface_ready()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.ensure_interface_ready(interface)


def is_bridge_alive(bridge_name: str) -> bool:
    """Deprecated: Use NetworkUtils.bridge_exists()."""
    warnings.warn(
        "is_bridge_alive is deprecated, use NetworkUtils.bridge_exists()",
        DeprecationWarning,
        stacklevel=2,
    )
    return NetworkUtils.bridge_exists(bridge_name)
