from __future__ import annotations

import ipaddress
import logging
import random
import secrets
import string
import subprocess
from pathlib import Path

from mvmctl.constants import CLI_NAME, DEFAULT_GUEST_MAC_PREFIX, bridge_name
from mvmctl.exceptions import NetworkError

logger = logging.getLogger(__name__)

_VIRTUAL_INTERFACE_PREFIXES = ("mvm-", "tap", "br-", "virbr", "docker", "veth")
_EXCLUDED_INTERFACES = ("lo",)


def subnet_mask_from_subnet(subnet: str) -> str:
    return str(ipaddress.IPv4Network(subnet, strict=False).netmask)


def prefix_len_from_subnet(subnet: str) -> int:
    return ipaddress.IPv4Network(subnet, strict=False).prefixlen


def ipv4_gateway_for_subnet(subnet: str) -> str:
    net = ipaddress.IPv4Network(subnet, strict=False)
    return str(next(iter(net.hosts())))


def bridge_name_for(network_name: str) -> str:
    from mvmctl.constants import device_prefix

    truncated = network_name[:10]
    return f"{device_prefix()}-{truncated}"


def generate_mac() -> str:
    rand_bytes = secrets.token_bytes(4)
    suffix = ":".join(f"{b:02x}" for b in rand_bytes)
    return f"{DEFAULT_GUEST_MAC_PREFIX}:{suffix}"


def generate_tap_name(network_name: str, vm_name: str) -> str:
    rand_suffix = "".join(random.choices(string.ascii_lowercase, k=3))
    return f"{CLI_NAME}-{network_name[:3]}-{vm_name[:3]}-{rand_suffix}"


def list_network_interfaces() -> list[str]:
    try:
        net_path = Path("/sys/class/net")
        if not net_path.exists():
            raise NetworkError("Unable to access /sys/class/net")

        interfaces: list[str] = []
        for entry in net_path.iterdir():
            name = entry.name
            if name in _EXCLUDED_INTERFACES:
                continue
            if any(name.startswith(prefix) for prefix in _VIRTUAL_INTERFACE_PREFIXES):
                continue
            interfaces.append(name)

        return sorted(interfaces)
    except OSError as e:
        raise NetworkError("Failed to list network interfaces") from e


def get_default_interface() -> str:
    try:
        result = subprocess.run(
            ["ip", "route", "show", "default"],
            capture_output=True,
            text=True,
            check=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError("Failed to determine default network interface") from e

    for line in result.stdout.splitlines():
        parts = line.split()
        if "dev" in parts:
            dev_idx = parts.index("dev")
            if dev_idx + 1 < len(parts):
                return parts[dev_idx + 1]

    raise NetworkError("Could not detect default network interface from 'ip route show default'")


def bridge_exists(bridge: str | None = None) -> bool:
    effective_bridge = bridge if bridge is not None else bridge_name()
    result = subprocess.run(
        ["ip", "link", "show", effective_bridge],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return result.returncode == 0


def tap_exists(tap_name: str) -> bool:
    result = subprocess.run(
        ["ip", "link", "show", tap_name],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return result.returncode == 0


def chain_exists(chain: str, table: str = "filter") -> bool:
    result = subprocess.run(
        ["iptables", "-t", table, "-L", chain, "-n"],
        capture_output=True,
        check=False,
    )
    return result.returncode == 0


def list_tuntap_devices() -> list[str]:
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


def list_bridges() -> list[str]:
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


def allocate_ip(
    existing_ips: list[str],
    subnet: str,
    ipv4_gateway: str | None = None,
) -> str:
    network = ipaddress.IPv4Network(subnet, strict=False)
    existing_set = set(existing_ips)

    for host in network.hosts():
        ip_str = str(host)
        if ipv4_gateway is not None and ip_str == ipv4_gateway:
            continue
        if ip_str not in existing_set:
            return ip_str

    raise NetworkError(f"No available IPs in subnet {subnet}")


def get_iptables_rules_for_bridge(bridge: str) -> list[str]:
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


def validate_network_interface(interface: str) -> bool:
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
        raise NetworkError("'ip' command not found — install iproute2") from e

    if result.returncode != 0 or not result.stdout.strip():
        raise NetworkError(
            f"Interface '{interface}' has no IPv4 address assigned. NAT requires an interface with a valid IP address."
        )

    return True


def _run_ip_batch(commands: list[str]) -> None:
    """Execute a batch of ip commands using ip -batch mode.

    Args:
        commands: List of ip command arguments (without 'ip' prefix).

    Raises:
        subprocess.CalledProcessError: If any command fails.
    """
    from mvmctl.utils.process import privileged_cmd as _privileged_cmd

    batch = "\n".join(commands) + "\n"
    subprocess.run(
        _privileged_cmd(["ip", "-batch", "-"]),
        input=batch,
        text=True,
        check=True,
        capture_output=True,
    )


def _get_bridge_name() -> str:
    """Get the default bridge name.

    Returns:
        The default bridge name (e.g. "mvm-default").
    """
    from mvmctl.constants import bridge_name

    return bridge_name()


def _bridge_has_ip(bridge: str, subnet: str) -> bool:
    """Check if a bridge already has a given subnet assigned.

    Args:
        bridge: Bridge interface name.
        subnet: Subnet to check (e.g. "172.35.0.0/24").

    Returns:
        True if the bridge has the subnet assigned, False otherwise.
    """
    result = subprocess.run(
        ["ip", "-o", "addr", "show", bridge],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        return False
    return subnet in result.stdout


def _iptables_rule_exists(rule_args: list[str]) -> bool:
    """Check if an iptables rule exists.

    Args:
        rule_args: iptables command arguments (without 'iptables' prefix).

    Returns:
        True if the rule exists, False otherwise.
    """
    from mvmctl.utils.process import privileged_cmd as _privileged_cmd

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
    """Ensure an iptables rule exists, adding it if necessary.

    Args:
        check_args: iptables command to check if rule exists.
        add_args: iptables command to add the rule.
        error_label: Error message if rule addition fails.

    Raises:
        NetworkError: If rule addition fails.
    """
    from mvmctl.utils.process import privileged_cmd as _privileged_cmd

    if _iptables_rule_exists(check_args):
        return
    try:
        subprocess.run(_privileged_cmd(add_args), check=True, capture_output=True)
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"{error_label}") from e


def _build_iptables_restore_input(rules: list[dict[str, str]]) -> str:
    """Build iptables-restore format input from a list of rule dictionaries.

    Args:
        rules: List of rule dicts with keys: table, chain, rule.

    Returns:
        String in iptables-restore format.
    """
    tables: dict[str, list[dict[str, str]]] = {}
    for rule in rules:
        table = rule.get("table", "filter")
        if table not in tables:
            tables[table] = []
        tables[table].append(rule)

    lines: list[str] = []
    for table, table_rules in tables.items():
        lines.append(f"*{table}")

        chains: set[str] = set()
        for rule in table_rules:
            chains.add(rule["chain"])

        for chain in chains:
            # Only declare chain if it doesn't exist (redeclaring may flush rules)
            if not chain_exists(chain, table):
                lines.append(f":{chain} - [0:0]")

        for rule in table_rules:
            lines.append(f"-A {rule['chain']} {rule['rule']}")

        lines.append("COMMIT")

    return "\n".join(lines) + "\n"


def _apply_iptables_rules_batch(
    rules: list[dict[str, str]],
    error_label: str = "Failed to apply iptables rules",
) -> None:
    """Apply a batch of iptables rules using iptables-restore.

    Args:
        rules: List of rule dicts with keys: table, chain, rule.
        error_label: Error message if application fails.

    Raises:
        NetworkError: If rule application fails.
    """
    from mvmctl.utils.process import privileged_cmd as _privileged_cmd

    if not rules:
        return

    restore_input = _build_iptables_restore_input(rules)

    logger.debug("Applying iptables rules batch:\n%s", restore_input)
    try:
        subprocess.run(
            _privileged_cmd(["iptables-restore", "--noflush"]),
            input=restore_input,
            text=True,
            check=True,
            capture_output=True,
        )
        logger.debug("Successfully applied iptables rules batch")
    except subprocess.CalledProcessError as e:
        logger.error("Failed to apply iptables rules batch. Input was:\n%s", restore_input)
        raise NetworkError(error_label) from e


def _detect_subnet_for_bridge(bridge: str) -> str | None:
    """Detect the SUBNET used for NAT rules associated with a bridge.

    Examines existing iptables rules in MVM-POSTROUTING chain to find
    the source SUBNET used for MASQUERADE rules matching the bridge.

    Args:
        bridge: Bridge interface name.

    Returns:
        The detected SUBNET string (e.g. "172.35.0.0/24") or None if not found.
    """
    from mvmctl.constants import MVM_POSTROUTING_CHAIN
    from mvmctl.utils.process import privileged_cmd as _privileged_cmd

    postrouting_chain = MVM_POSTROUTING_CHAIN

    try:
        result = subprocess.run(
            _privileged_cmd(["iptables", "-t", "nat", "-L", postrouting_chain, "-n", "-v"]),
            capture_output=True,
            text=True,
            check=True,
        )
    except subprocess.CalledProcessError:
        return None

    comment = f"mvm-nat:{bridge}"
    for line in result.stdout.splitlines():
        if comment in line and "MASQUERADE" in line:
            # Parse the line to extract source SUBNET
            # Format: num   packets   bytes target     prot opt in     out     source               destination
            parts = line.split()
            if len(parts) >= 9:
                # Source is typically the 9th field (index 8)
                source = parts[8]
                if "/" in source:
                    return source

    return None


def get_tap_devices(bridge: str | None = None) -> list[str]:
    """List all TAP devices currently attached to the bridge.

    Uses `ip link show master {bridge}` and parses output.

    Args:
        bridge: Bridge interface name. If None, uses default bridge.

    Returns:
        List of TAP device names attached to the bridge.
    """
    effective_bridge = bridge if bridge is not None else _get_bridge_name()
    result = subprocess.run(
        ["ip", "link", "show", "master", effective_bridge],
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


def is_bridge_alive(bridge_name: str) -> bool:
    """Check if a network bridge still exists.

    Args:
        bridge_name: Name of the bridge interface

    Returns:
        True if bridge exists, False otherwise
    """
    try:
        result = subprocess.run(
            ["ip", "link", "show", bridge_name],
            capture_output=True,
            check=False,
        )
        return result.returncode == 0
    except FileNotFoundError:
        return False
