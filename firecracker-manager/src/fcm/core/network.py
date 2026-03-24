"""Network infrastructure management for Firecracker VM setup.

.. todo:: S-M5 — Add network namespace isolation between VMs.
   Currently all VMs share the host network namespace, separated only by
   iptables rules.  A future improvement should place each VM's TAP in its
   own network namespace to provide kernel-level isolation.  See
   ``ip netns`` / ``ip link set <tap> netns <ns>`` for the primitives.
"""

import ipaddress
import logging
import os
import secrets
import subprocess
from pathlib import Path

from fcm.constants import (
    BRIDGE_NAME,
    DEFAULT_NETWORK_CIDR,
    DEFAULT_NETWORK_GATEWAY,
)
from fcm.exceptions import NetworkError


logger = logging.getLogger(__name__)


def _privileged_cmd(cmd: list[str]) -> list[str]:
    """Prepend sudo if not running as root."""
    if os.getuid() != 0:
        return ["sudo"] + cmd
    return cmd


# Derived defaults from constants — kept as module-level aliases so existing
# function signatures that reference them continue to work.
BRIDGE_IP = DEFAULT_NETWORK_GATEWAY
BRIDGE_CIDR = f"{DEFAULT_NETWORK_GATEWAY}/24"
SUBNET = DEFAULT_NETWORK_CIDR
GATEWAY = DEFAULT_NETWORK_GATEWAY


def get_default_interface() -> str:
    """Get the default network interface by parsing ``ip route show default``.

    Returns:
        Interface name (e.g. ``"eth0"``, ``"ens3"``).

    Raises:
        NetworkError: If the default route cannot be determined.
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
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return result.returncode == 0


def _bridge_has_ip(bridge: str, cidr: str) -> bool:
    """Return True if the bridge already has the given CIDR assigned."""
    result = subprocess.run(
        ["ip", "-o", "addr", "show", bridge],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        return False
    return cidr in result.stdout


def setup_bridge(
    bridge: str = BRIDGE_NAME, cidr: str = BRIDGE_CIDR, gateway_cidr: str | None = None
) -> None:
    """Create and configure the bridge interface.

    - Creates bridge with `ip link add {bridge} type bridge`
    - Sets IP with `ip addr add {cidr} dev {bridge}`
    - Brings it up with `ip link set {bridge} up`
    - Enables IP forwarding: writes 1 to /proc/sys/net/ipv4/ip_forward
    - Raises NetworkError on failure.
    - Is idempotent: if bridge already exists, does nothing.
    """
    effective_cidr = gateway_cidr if gateway_cidr else cidr

    if bridge_exists(bridge):
        logger.debug("Bridge %s already exists, skipping creation", bridge)
        return

    try:
        batch = f"link add name {bridge} type bridge\naddr add {effective_cidr} dev {bridge}\nlink set {bridge} up\n"
        subprocess.run(
            _privileged_cmd(["ip", "-batch", "-"]),
            input=batch,
            text=True,
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to setup bridge {bridge}: {e}\n{e.stderr}") from e

    try:
        Path("/proc/sys/net/ipv4/ip_forward").write_text("1\n")
    except OSError:
        try:
            subprocess.run(
                _privileged_cmd(["sysctl", "-w", "net.ipv4.ip_forward=1"]),
                capture_output=True,
                check=True,
            )
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to enable IP forwarding: {e}") from e

    logger.info("Bridge %s created with CIDR %s", bridge, cidr)


def teardown_bridge(bridge: str = BRIDGE_NAME) -> None:
    """Remove the bridge interface.

    - `ip link set {bridge} down`
    - `ip link delete {bridge} type bridge`
    - Raises NetworkError on failure.
    """
    try:
        batch = f"link set {bridge} down\nlink delete {bridge} type bridge\n"
        subprocess.run(
            _privileged_cmd(["ip", "-batch", "-"]),
            input=batch,
            text=True,
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to teardown bridge {bridge}: {e}\n{e.stderr}") from e

    logger.info("Bridge %s removed", bridge)


def _iptables_rule_exists(rule_args: list[str]) -> bool:
    result = subprocess.run(
        _privileged_cmd(rule_args),
        capture_output=True,
        check=False,
    )
    return result.returncode == 0


def _ensure_iptables_rule(
    check_args: list[str],
    add_args: list[str],
    error_label: str,
) -> None:
    if _iptables_rule_exists(check_args):
        return
    try:
        subprocess.run(_privileged_cmd(add_args), check=True, capture_output=True)
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"{error_label}: {e}") from e


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

    try:
        _ensure_iptables_rule(
            ["iptables", "-t", "nat", "-C", "POSTROUTING", "-o", host_iface, "-j", "MASQUERADE"],
            ["iptables", "-t", "nat", "-A", "POSTROUTING", "-o", host_iface, "-j", "MASQUERADE"],
            f"Failed to add MASQUERADE rule for {host_iface}",
        )
        _ensure_iptables_rule(
            ["iptables", "-C", "FORWARD", "-i", bridge, "-o", host_iface, "-j", "ACCEPT"],
            ["iptables", "-A", "FORWARD", "-i", bridge, "-o", host_iface, "-j", "ACCEPT"],
            f"Failed to add FORWARD rule for {bridge} -> {host_iface}",
        )
        _ensure_iptables_rule(
            ["iptables", "-C", "FORWARD", "-i", host_iface, "-o", bridge, "-j", "ACCEPT"],
            ["iptables", "-A", "FORWARD", "-i", host_iface, "-o", bridge, "-j", "ACCEPT"],
            f"Failed to add FORWARD rule for {host_iface} -> {bridge}",
        )
    except NetworkError as e:
        raise NetworkError(f"Failed to setup NAT for {bridge} via {host_iface}: {e}") from e

    logger.info("NAT rules configured for bridge %s via %s", bridge, host_iface)


def teardown_nat(bridge: str = BRIDGE_NAME, force: bool = False) -> None:
    """Remove NAT rules for the bridge.

    IMPORTANT: Only removes the MASQUERADE rule if `force=True` OR no VMs
    are currently using the bridge (i.e., no TAP devices attached to it).
    This fixes the bash PoC bug where deleting one VM removed the shared rule.

    - Removes: `iptables -t nat -D POSTROUTING -o {host_iface} -j MASQUERADE`
    - Raises NetworkError on failure.
    """
    if not force:
        tap_devices = get_tap_devices(bridge)
        if len(tap_devices) > 0:
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
            _privileged_cmd(
                ["iptables", "-t", "nat", "-D", "POSTROUTING", "-o", host_iface, "-j", "MASQUERADE"]
            ),
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
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
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
        batch = f"tuntap add dev {tap_name} mode tap\nlink set {tap_name} master {bridge}\nlink set {tap_name} up\n"
        subprocess.run(
            _privileged_cmd(["ip", "-batch", "-"]),
            input=batch,
            text=True,
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to create TAP {tap_name}: {e}\n{e.stderr}") from e

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
        batch = f"link set {tap_name} down\nlink delete {tap_name}\n"
        subprocess.run(
            _privileged_cmd(["ip", "-batch", "-"]),
            input=batch,
            text=True,
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to delete TAP {tap_name}: {e}\n{e.stderr}") from e

    logger.info("TAP device %s deleted", tap_name)


def add_iptables_forward_rules(tap_name: str, bridge: str = BRIDGE_NAME) -> None:
    """Add iptables FORWARD rules for a specific TAP device.

    Rules to add (idempotent — check with -C before adding with -A):
    - `iptables -A FORWARD -i {bridge} -o {tap_name} -j ACCEPT`
    - `iptables -A FORWARD -i {tap_name} -o {bridge} -j ACCEPT`
    """
    try:
        _ensure_iptables_rule(
            ["iptables", "-C", "FORWARD", "-i", bridge, "-o", tap_name, "-j", "ACCEPT"],
            ["iptables", "-A", "FORWARD", "-i", bridge, "-o", tap_name, "-j", "ACCEPT"],
            f"Failed to add FORWARD rule for {bridge} -> {tap_name}",
        )
        _ensure_iptables_rule(
            ["iptables", "-C", "FORWARD", "-i", tap_name, "-o", bridge, "-j", "ACCEPT"],
            ["iptables", "-A", "FORWARD", "-i", tap_name, "-o", bridge, "-j", "ACCEPT"],
            f"Failed to add FORWARD rule for {tap_name} -> {bridge}",
        )
    except NetworkError as e:
        raise NetworkError(f"Failed to add FORWARD rules for {tap_name}: {e}") from e

    logger.debug("FORWARD rules added for TAP %s ↔ bridge %s", tap_name, bridge)


def remove_iptables_forward_rules(tap_name: str, bridge: str = BRIDGE_NAME) -> None:
    """Remove iptables FORWARD rules for a specific TAP device.

    - `iptables -D FORWARD -i {bridge} -o {tap_name} -j ACCEPT`
    - `iptables -D FORWARD -i {tap_name} -o {bridge} -j ACCEPT`
    - Safe to call even if rules don't exist (ignore errors).
    """
    subprocess.run(
        _privileged_cmd(
            ["iptables", "-D", "FORWARD", "-i", bridge, "-o", tap_name, "-j", "ACCEPT"]
        ),
        capture_output=True,
        check=False,
    )
    subprocess.run(
        _privileged_cmd(
            ["iptables", "-D", "FORWARD", "-i", tap_name, "-o", bridge, "-j", "ACCEPT"]
        ),
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


def get_iptables_rules_for_bridge(bridge: str) -> list[str]:
    """Return iptables rules that reference the given bridge interface.

    Runs iptables -L FORWARD and iptables -t nat -L POSTROUTING and filters
    lines that contain the bridge name.

    Returns a list of matching rule strings (may be empty).
    """
    rules: list[str] = []

    for cmd in [
        ["iptables", "-L", "FORWARD", "--line-numbers", "-n"],
        ["iptables", "-t", "nat", "-L", "POSTROUTING", "--line-numbers", "-n"],
    ]:
        result = subprocess.run(cmd, capture_output=True, text=True, check=False)
        if result.returncode == 0:
            for line in result.stdout.splitlines():
                if bridge in line:
                    rules.append(line.strip())

    return rules


def generate_mac() -> str:
    """Generate a random MAC address with 02:FC: prefix.

    Uses ``secrets`` for cryptographically strong randomness.

    Format: 02:FC:XX:XX:XX:XX where X is random hex.
    """
    rand_bytes = secrets.token_bytes(4)
    suffix = ":".join(f"{b:02x}" for b in rand_bytes)
    return f"02:FC:{suffix}"
