"""Network utilities: MAC, TAP, IP, iptables, bridges."""

from __future__ import annotations

import ipaddress
import logging
import random
import secrets
import string
import subprocess
from pathlib import Path

from mvmctl.constants import CLI_NAME
from mvmctl.exceptions import NetworkError

logger = logging.getLogger(__name__)

_VIRTUAL_INTERFACE_PREFIXES = ("mvm-", "tap", "br-", "virbr", "docker", "veth")
_EXCLUDED_INTERFACES = ("lo",)


class NetworkUtils:
    """
    Network computation and system query utilities.

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
        """
        Compute default gateway IP from subnet (first usable host).

        For /31 subnets (RFC 3021), both addresses are usable hosts, so we
        return the second address to avoid colliding with the network address
        in validation.
        """
        network = ipaddress.IPv4Network(subnet, strict=False)
        if network.prefixlen == 31:
            # RFC 3021: both addresses are usable; use the second one
            hosts = list(network.hosts())
            return str(hosts[1]) if len(hosts) > 1 else str(hosts[0])
        hosts_iter = iter(network.hosts())
        return str(next(hosts_iter))

    @staticmethod
    def compute_bridge_address(ipv4_gateway: str, subnet: str) -> str:
        """
        Return gateway IP with subnet prefix (e.g. '172.29.0.1/28').

        Args:
            ipv4_gateway: Gateway IP address (e.g. '172.29.0.1').
            subnet: Subnet CIDR (e.g. '172.29.0.0/28').

        Returns:
            Gateway IP with prefix (e.g. '172.29.0.1/28').

        """
        prefix = ipaddress.IPv4Network(subnet, strict=False).prefixlen
        return f"{ipv4_gateway}/{prefix}"

    @staticmethod
    def compute_bridge_name(network_name: str) -> str:
        """Compute bridge name from network name.

        Ensures the bridge name never exceeds the Linux IFNAMSIZ limit (15 chars).
        If the full {CLI_NAME}-{network_name} would exceed 15 chars, a hash suffix
        is used to preserve uniqueness within the limit.
        """
        from hashlib import sha256

        raw = f"{CLI_NAME}-{network_name}"
        if len(raw) <= 15:
            return raw

        # Truncate while preserving uniqueness via hash
        hash_len = 8
        prefix = f"{CLI_NAME}-"
        max_name = 15 - len(prefix) - hash_len - 1  # -1 for '-' separator
        name_truncated = network_name[:max_name]
        short_hash = sha256(network_name.encode()).hexdigest()[:hash_len]
        return f"{prefix}{name_truncated}-{short_hash}"

    # --- Naming & Generation ---

    @staticmethod
    def generate_mac(mac_prefix: str) -> str:
        """Generate a MAC address with the given prefix."""
        rand_bytes = secrets.token_bytes(4)
        suffix = ":".join(f"{b:02x}" for b in rand_bytes)
        return f"{mac_prefix}:{suffix}".upper()

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
        """
        Allocate the next available IP in a subnet.

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
        """
        Get the outbound (default route) network interface.

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
        """
        Ensure a network interface exists and is usable for NAT.

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
        """
        Detect mixed iptables backend conflict.

        Checks if both iptables-legacy and iptables-nft have active rules.

        Returns:
            Tuple of (has_conflict, diagnosis_string).

        """
        from mvmctl.utils._system import privileged_cmd as _privileged_cmd

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
        """
        Get the bridge that a TAP device is attached to.

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
        batch = "\n".join(commands) + "\n"
        subprocess.run(
            ["ip", "-batch", "-"],
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

    @staticmethod
    def strip_tap_rules(rules_text: str) -> str:
        """
        Strip TAP-related rules from iptables rules text.

        Excludes any rules that reference currently active TAP devices.
        This prevents transient TAP rules from being persisted to disk.

        Args:
            rules_text: Raw iptables-save output.

        Returns:
            Filtered rules text with TAP rules removed.

        """
        tap_names = set(NetworkUtils.get_tuntap_devices())
        if not tap_names:
            return rules_text
        filtered: list[str] = []
        for line in rules_text.splitlines(keepends=True):
            if any(tap in line for tap in tap_names):
                logger.debug(
                    "Excluding transient TAP rule from persistence: %s",
                    line.strip(),
                )
                continue
            filtered.append(line)
        return "".join(filtered)
