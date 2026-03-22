"""Network infrastructure management for Firecracker multi-VM setup."""

import ipaddress
import logging
import random
import subprocess
from pathlib import Path

from fcm.exceptions import NetworkError


logger = logging.getLogger(__name__)

BRIDGE_NAME = "fc-br0"
BRIDGE_IP = "10.20.0.1"
BRIDGE_CIDR = "10.20.0.1/24"
SUBNET = "10.20.0.0/24"
GATEWAY = "10.20.0.1"


def get_default_interface() -> str:
    """Get the default network interface by parsing `ip route show default`.

    Returns the interface name (e.g., 'eth0', 'ens3').
    Raises NetworkError if not found.
    """
    try:
        result = subprocess.run(
            ["ip", "route", "show", "default"],
            capture_output=True,
            text=True,
            check=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to run 'ip route show default': {e}") from e

    for line in result.stdout.splitlines():
        parts = line.split()
        if "dev" in parts:
            dev_idx = parts.index("dev")
            if dev_idx + 1 < len(parts):
                return parts[dev_idx + 1]

    raise NetworkError("Could not detect default network interface from 'ip route show default'")


def bridge_exists(bridge: str = BRIDGE_NAME) -> bool:
    """Return True if the bridge interface exists."""
    result = subprocess.run(
        ["ip", "link", "show", bridge],
        capture_output=True,
        check=False,
    )
    return result.returncode == 0


def setup_bridge(bridge: str = BRIDGE_NAME, cidr: str = BRIDGE_CIDR) -> None:
    """Create and configure the bridge interface.

    - Creates bridge with `ip link add {bridge} type bridge`
    - Sets IP with `ip addr add {cidr} dev {bridge}`
    - Brings it up with `ip link set {bridge} up`
    - Enables IP forwarding: writes 1 to /proc/sys/net/ipv4/ip_forward
    - Raises NetworkError on failure.
    - Is idempotent: if bridge already exists, does nothing.
    """
    if bridge_exists(bridge):
        logger.debug("Bridge %s already exists, skipping creation", bridge)
        return

    try:
        subprocess.run(
            ["ip", "link", "add", "name", bridge, "type", "bridge"],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to create bridge {bridge}: {e}") from e

    try:
        subprocess.run(
            ["ip", "addr", "add", cidr, "dev", bridge],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to assign IP {cidr} to bridge {bridge}: {e}") from e

    try:
        subprocess.run(
            ["ip", "link", "set", bridge, "up"],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to bring up bridge {bridge}: {e}") from e

    try:
        Path("/proc/sys/net/ipv4/ip_forward").write_text("1\n")
    except OSError as e:
        raise NetworkError(f"Failed to enable IP forwarding: {e}") from e

    logger.info("Bridge %s created with CIDR %s", bridge, cidr)


def teardown_bridge(bridge: str = BRIDGE_NAME) -> None:
    """Remove the bridge interface.

    - `ip link set {bridge} down`
    - `ip link delete {bridge} type bridge`
    - Raises NetworkError on failure.
    """
    try:
        subprocess.run(
            ["ip", "link", "set", bridge, "down"],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to bring down bridge {bridge}: {e}") from e

    try:
        subprocess.run(
            ["ip", "link", "delete", bridge, "type", "bridge"],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to delete bridge {bridge}: {e}") from e

    logger.info("Bridge %s removed", bridge)


def setup_nat(bridge: str = BRIDGE_NAME, host_iface: str | None = None) -> None:
    """Set up NAT (MASQUERADE) for the bridge subnet.

    - Gets host_iface via get_default_interface() if not provided
    - Adds iptables rule: `iptables -t nat -A POSTROUTING -o {host_iface} -j MASQUERADE`
    - Also adds FORWARD rules to allow traffic through the bridge
    - Is idempotent: checks if rule exists before adding (use -C to check)
    - Raises NetworkError on failure.
    """
    if host_iface is None:
        host_iface = get_default_interface()

    check = subprocess.run(
        ["iptables", "-t", "nat", "-C", "POSTROUTING", "-o", host_iface, "-j", "MASQUERADE"],
        capture_output=True,
        check=False,
    )
    if check.returncode != 0:
        try:
            subprocess.run(
                [
                    "iptables",
                    "-t",
                    "nat",
                    "-A",
                    "POSTROUTING",
                    "-o",
                    host_iface,
                    "-j",
                    "MASQUERADE",
                ],
                check=True,
                capture_output=True,
            )
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to add MASQUERADE rule for {host_iface}: {e}") from e

    check = subprocess.run(
        ["iptables", "-C", "FORWARD", "-i", bridge, "-o", host_iface, "-j", "ACCEPT"],
        capture_output=True,
        check=False,
    )
    if check.returncode != 0:
        try:
            subprocess.run(
                ["iptables", "-A", "FORWARD", "-i", bridge, "-o", host_iface, "-j", "ACCEPT"],
                check=True,
                capture_output=True,
            )
        except subprocess.CalledProcessError as e:
            raise NetworkError(
                f"Failed to add FORWARD rule bridge→host ({bridge}→{host_iface}): {e}"
            ) from e

    check = subprocess.run(
        ["iptables", "-C", "FORWARD", "-i", host_iface, "-o", bridge, "-j", "ACCEPT"],
        capture_output=True,
        check=False,
    )
    if check.returncode != 0:
        try:
            subprocess.run(
                ["iptables", "-A", "FORWARD", "-i", host_iface, "-o", bridge, "-j", "ACCEPT"],
                check=True,
                capture_output=True,
            )
        except subprocess.CalledProcessError as e:
            raise NetworkError(
                f"Failed to add FORWARD rule host→bridge ({host_iface}→{bridge}): {e}"
            ) from e

    logger.info("NAT rules configured for bridge %s via %s", bridge, host_iface)


def teardown_nat(bridge: str = BRIDGE_NAME, force: bool = False) -> None:
    """Remove NAT rules for the bridge.

    IMPORTANT: Only removes the MASQUERADE rule if `force=True` OR no VMs
    are currently using the bridge (i.e., no TAP devices attached to it).
    This fixes the bash PoC bug where deleting one VM removed the shared rule.

    - Removes: `iptables -t nat -D POSTROUTING -o {host_iface} -j MASQUERADE`
    - Raises NetworkError on failure.
    """
    tap_devices = get_tap_devices(bridge)

    if not force and len(tap_devices) > 0:
        logger.debug(
            "Skipping MASQUERADE removal: %d TAP device(s) still attached to %s",
            len(tap_devices),
            bridge,
        )
        return

    try:
        host_iface = get_default_interface()
    except NetworkError:
        logger.warning("Could not detect default interface, skipping NAT teardown")
        return

    try:
        subprocess.run(
            ["iptables", "-t", "nat", "-D", "POSTROUTING", "-o", host_iface, "-j", "MASQUERADE"],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to remove MASQUERADE rule for {host_iface}: {e}") from e

    logger.info("MASQUERADE NAT rule removed for %s", host_iface)


def tap_exists(tap_name: str) -> bool:
    """Return True if the TAP device exists."""
    result = subprocess.run(
        ["ip", "link", "show", tap_name],
        capture_output=True,
        check=False,
    )
    return result.returncode == 0


def create_tap(tap_name: str, bridge: str = BRIDGE_NAME) -> None:
    """Create a TAP device and attach it to the bridge.

    - `ip tuntap add dev {tap_name} mode tap`
    - `ip link set {tap_name} master {bridge}`
    - `ip link set {tap_name} up`
    - Raises NetworkError if tap already exists or creation fails.
    """
    if tap_exists(tap_name):
        raise NetworkError(f"TAP device {tap_name} already exists")

    try:
        subprocess.run(
            ["ip", "tuntap", "add", "dev", tap_name, "mode", "tap"],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to create TAP {tap_name}: {e}") from e

    try:
        subprocess.run(
            ["ip", "link", "set", tap_name, "master", bridge],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to attach TAP {tap_name} to bridge {bridge}: {e}") from e

    try:
        subprocess.run(
            ["ip", "link", "set", tap_name, "up"],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to bring up TAP {tap_name}: {e}") from e

    logger.info("TAP device %s created and attached to bridge %s", tap_name, bridge)


def delete_tap(tap_name: str) -> None:
    """Delete a TAP device.

    - `ip link set {tap_name} down`
    - `ip link delete {tap_name}`
    - Raises NetworkError on failure.
    - Is safe to call if tap doesn't exist (logs warning, doesn't raise).
    """
    if not tap_exists(tap_name):
        logger.warning("TAP device %s does not exist, skipping deletion", tap_name)
        return

    try:
        subprocess.run(
            ["ip", "link", "set", tap_name, "down"],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to bring down TAP {tap_name}: {e}") from e

    try:
        subprocess.run(
            ["ip", "link", "delete", tap_name],
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to delete TAP {tap_name}: {e}") from e

    logger.info("TAP device %s deleted", tap_name)


def add_iptables_forward_rules(tap_name: str, bridge: str = BRIDGE_NAME) -> None:
    """Add iptables FORWARD rules for a specific TAP device.

    Rules to add (idempotent — check with -C before adding with -A):
    - `iptables -A FORWARD -i {bridge} -o {tap_name} -j ACCEPT`
    - `iptables -A FORWARD -i {tap_name} -o {bridge} -j ACCEPT`
    """
    check = subprocess.run(
        ["iptables", "-C", "FORWARD", "-i", bridge, "-o", tap_name, "-j", "ACCEPT"],
        capture_output=True,
        check=False,
    )
    if check.returncode != 0:
        try:
            subprocess.run(
                ["iptables", "-A", "FORWARD", "-i", bridge, "-o", tap_name, "-j", "ACCEPT"],
                check=True,
                capture_output=True,
            )
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to add FORWARD rule {bridge}→{tap_name}: {e}") from e

    check = subprocess.run(
        ["iptables", "-C", "FORWARD", "-i", tap_name, "-o", bridge, "-j", "ACCEPT"],
        capture_output=True,
        check=False,
    )
    if check.returncode != 0:
        try:
            subprocess.run(
                ["iptables", "-A", "FORWARD", "-i", tap_name, "-o", bridge, "-j", "ACCEPT"],
                check=True,
                capture_output=True,
            )
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to add FORWARD rule {tap_name}→{bridge}: {e}") from e

    logger.debug("FORWARD rules added for TAP %s ↔ bridge %s", tap_name, bridge)


def remove_iptables_forward_rules(tap_name: str, bridge: str = BRIDGE_NAME) -> None:
    """Remove iptables FORWARD rules for a specific TAP device.

    - `iptables -D FORWARD -i {bridge} -o {tap_name} -j ACCEPT`
    - `iptables -D FORWARD -i {tap_name} -o {bridge} -j ACCEPT`
    - Safe to call even if rules don't exist (ignore errors).
    """
    subprocess.run(
        ["iptables", "-D", "FORWARD", "-i", bridge, "-o", tap_name, "-j", "ACCEPT"],
        capture_output=True,
        check=False,
    )
    subprocess.run(
        ["iptables", "-D", "FORWARD", "-i", tap_name, "-o", bridge, "-j", "ACCEPT"],
        capture_output=True,
        check=False,
    )
    logger.debug("FORWARD rules removed for TAP %s ↔ bridge %s", tap_name, bridge)


def get_tap_devices(bridge: str = BRIDGE_NAME) -> list[str]:
    """List all TAP devices currently attached to the bridge.

    Uses `ip link show master {bridge}` and parses output.
    Returns list of interface names.
    """
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


def allocate_ip(
    existing_ips: list[str],
    subnet: str = SUBNET,
    gateway: str = GATEWAY,
) -> str:
    """Allocate the next available IP in the subnet.

    - Skips the gateway IP and network/broadcast addresses
    - Returns first available IP not in existing_ips
    - Raises NetworkError if no IPs available
    """
    network = ipaddress.IPv4Network(subnet, strict=False)
    existing_set = set(existing_ips)

    for host in network.hosts():
        ip_str = str(host)
        if ip_str == gateway:
            continue
        if ip_str not in existing_set:
            return ip_str

    raise NetworkError(f"No available IPs in subnet {subnet}")


def generate_mac() -> str:
    """Generate a random MAC address with 02:FC: prefix.

    Format: 02:FC:XX:XX:XX:XX where X is random hex.
    """
    rand_bytes = [random.randint(0, 255) for _ in range(4)]
    suffix = ":".join(f"{b:02x}" for b in rand_bytes)
    return f"02:FC:{suffix}"
