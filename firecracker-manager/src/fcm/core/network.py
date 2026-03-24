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
    FCM_FORWARD_CHAIN,
    FCM_POSTROUTING_CHAIN,
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


def chain_exists(chain: str, table: str = "filter") -> bool:
    """Check if an iptables chain exists.

    Uses iptables -L to check if the chain is present.

    Args:
        chain: Chain name to check.
        table: Table name (filter, nat, mangle, raw). Default is filter.

    Returns:
        True if the chain exists, False otherwise.
    """
    cmd = ["iptables", "-t", table, "-L", chain, "-n"]
    result = subprocess.run(
        _privileged_cmd(cmd),
        capture_output=True,
        check=False,
    )
    return result.returncode == 0


def setup_fcm_chains() -> None:
    """Create FCM iptables chains and link them to built-in chains.

    Creates:
    - FCM-FORWARD chain in filter table
    - FCM-POSTROUTING chain in nat table
    - Jumps from built-in chains to FCM chains

    Idempotent: checks if chains exist before creating.
    Raises NetworkError on failure.
    """
    forward_chain = FCM_FORWARD_CHAIN
    postrouting_chain = FCM_POSTROUTING_CHAIN

    # Create FCM-FORWARD chain in filter table
    if not chain_exists(forward_chain, "filter"):
        try:
            subprocess.run(
                _privileged_cmd(["iptables", "-N", forward_chain]),
                check=True,
                capture_output=True,
            )
            logger.debug("Created iptables chain %s", forward_chain)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to create {forward_chain} chain: {e}") from e

    # Create FCM-POSTROUTING chain in nat table
    if not chain_exists(postrouting_chain, "nat"):
        try:
            subprocess.run(
                _privileged_cmd(["iptables", "-t", "nat", "-N", postrouting_chain]),
                check=True,
                capture_output=True,
            )
            logger.debug("Created iptables chain %s in nat table", postrouting_chain)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to create {postrouting_chain} chain: {e}") from e

    # Add jump from FORWARD to FCM-FORWARD
    jump_rule = ["iptables", "-C", "FORWARD", "-j", forward_chain]
    if not _iptables_rule_exists(jump_rule):
        try:
            subprocess.run(
                _privileged_cmd(["iptables", "-A", "FORWARD", "-j", forward_chain]),
                check=True,
                capture_output=True,
            )
            logger.debug("Added jump from FORWARD to %s", forward_chain)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to add jump to {forward_chain}: {e}") from e

    # Add jump from POSTROUTING to FCM-POSTROUTING
    jump_rule_nat = ["iptables", "-t", "nat", "-C", "POSTROUTING", "-j", postrouting_chain]
    if not _iptables_rule_exists(jump_rule_nat):
        try:
            subprocess.run(
                _privileged_cmd(
                    ["iptables", "-t", "nat", "-A", "POSTROUTING", "-j", postrouting_chain]
                ),
                check=True,
                capture_output=True,
            )
            logger.debug("Added jump from POSTROUTING to %s", postrouting_chain)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to add jump to {postrouting_chain}: {e}") from e

    logger.info("FCM iptables chains configured")


def teardown_fcm_chains() -> None:
    """Remove FCM iptables chains and their jumps from built-in chains.

    Removes:
    - Jump rules from FORWARD and POSTROUTING
    - All rules in FCM chains (flush)
    - The FCM chains themselves

    Safe to call even if chains don't exist.
    Raises NetworkError on failure.
    """
    forward_chain = FCM_FORWARD_CHAIN
    postrouting_chain = FCM_POSTROUTING_CHAIN

    # Remove jump from FORWARD to FCM-FORWARD
    if chain_exists(forward_chain, "filter"):
        subprocess.run(
            _privileged_cmd(["iptables", "-D", "FORWARD", "-j", forward_chain]),
            capture_output=True,
            check=False,
        )

        # Flush and delete FCM-FORWARD chain
        try:
            subprocess.run(
                _privileged_cmd(["iptables", "-F", forward_chain]),
                check=True,
                capture_output=True,
            )
            subprocess.run(
                _privileged_cmd(["iptables", "-X", forward_chain]),
                check=True,
                capture_output=True,
            )
            logger.debug("Removed iptables chain %s", forward_chain)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to remove {forward_chain} chain: {e}") from e

    # Remove jump from POSTROUTING to FCM-POSTROUTING
    if chain_exists(postrouting_chain, "nat"):
        subprocess.run(
            _privileged_cmd(
                ["iptables", "-t", "nat", "-D", "POSTROUTING", "-j", postrouting_chain]
            ),
            capture_output=True,
            check=False,
        )

        # Flush and delete FCM-POSTROUTING chain
        try:
            subprocess.run(
                _privileged_cmd(["iptables", "-t", "nat", "-F", postrouting_chain]),
                check=True,
                capture_output=True,
            )
            subprocess.run(
                _privileged_cmd(["iptables", "-t", "nat", "-X", postrouting_chain]),
                check=True,
                capture_output=True,
            )
            logger.debug("Removed iptables chain %s from nat table", postrouting_chain)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to remove {postrouting_chain} chain: {e}") from e

    logger.info("FCM iptables chains removed")


def setup_nat(bridge: str = BRIDGE_NAME, host_iface: str | None = None) -> None:
    """Set up NAT (MASQUERADE) for the bridge subnet using FCM chains.

    - Gets host_iface via get_default_interface() if not provided
    - Adds MASQUERADE rule to FCM-POSTROUTING chain
    - Adds FORWARD rules to FCM-FORWARD chain
    - Is idempotent: checks if rule exists before adding (use -C to check)
    - Raises NetworkError on failure.
    """
    if host_iface is None:
        host_iface = get_default_interface()

    forward_chain = FCM_FORWARD_CHAIN
    postrouting_chain = FCM_POSTROUTING_CHAIN

    # Ensure FCM chains exist before adding rules
    setup_fcm_chains()

    try:
        _ensure_iptables_rule(
            [
                "iptables",
                "-t",
                "nat",
                "-C",
                postrouting_chain,
                "-o",
                host_iface,
                "-j",
                "MASQUERADE",
            ],
            [
                "iptables",
                "-t",
                "nat",
                "-A",
                postrouting_chain,
                "-o",
                host_iface,
                "-j",
                "MASQUERADE",
            ],
            f"Failed to add MASQUERADE rule for {host_iface}",
        )
        _ensure_iptables_rule(
            ["iptables", "-C", forward_chain, "-i", bridge, "-o", host_iface, "-j", "ACCEPT"],
            ["iptables", "-A", forward_chain, "-i", bridge, "-o", host_iface, "-j", "ACCEPT"],
            f"Failed to add FORWARD rule for {bridge} -> {host_iface}",
        )
        _ensure_iptables_rule(
            ["iptables", "-C", forward_chain, "-i", host_iface, "-o", bridge, "-j", "ACCEPT"],
            ["iptables", "-A", forward_chain, "-i", host_iface, "-o", bridge, "-j", "ACCEPT"],
            f"Failed to add FORWARD rule for {host_iface} -> {bridge}",
        )
    except NetworkError as e:
        raise NetworkError(f"Failed to setup NAT for {bridge} via {host_iface}: {e}") from e

    logger.info("NAT rules configured for bridge %s via %s", bridge, host_iface)


def teardown_nat(bridge: str = BRIDGE_NAME, force: bool = False) -> None:
    """Remove NAT (MASQUERADE + FORWARD) rules for the bridge from FCM chains.

    IMPORTANT: Only removes rules if `force=True` OR no VMs are currently
    using the bridge (i.e., no TAP devices attached to it).
    This fixes the bash PoC bug where deleting one VM removed the shared rule.

    Removes rules from FCM chains:
    - MASQUERADE rule from FCM-POSTROUTING chain
    - FORWARD rules from FCM-FORWARD chain

    Raises NetworkError if the MASQUERADE deletion fails.
    FORWARD rule deletions are best-effort (ignored if missing).
    """
    if not force:
        tap_devices = get_tap_devices(bridge)
        if len(tap_devices) > 0:
            logger.debug(
                "Skipping NAT teardown: %d TAP device(s) still attached to %s",
                len(tap_devices),
                bridge,
            )
            return

    try:
        host_iface = get_default_interface()
    except NetworkError:
        logger.warning("Could not detect default interface, skipping NAT teardown")
        return

    forward_chain = FCM_FORWARD_CHAIN
    postrouting_chain = FCM_POSTROUTING_CHAIN

    # Only try to remove rules if FCM chains exist
    if not chain_exists(forward_chain, "filter") or not chain_exists(postrouting_chain, "nat"):
        logger.debug("FCM chains do not exist, skipping NAT teardown")
        return

    try:
        subprocess.run(
            _privileged_cmd(
                [
                    "iptables",
                    "-t",
                    "nat",
                    "-D",
                    postrouting_chain,
                    "-o",
                    host_iface,
                    "-j",
                    "MASQUERADE",
                ]
            ),
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to remove MASQUERADE rule for {host_iface}: {e}") from e

    for rule in [
        ["iptables", "-D", forward_chain, "-i", bridge, "-o", host_iface, "-j", "ACCEPT"],
        ["iptables", "-D", forward_chain, "-i", host_iface, "-o", bridge, "-j", "ACCEPT"],
    ]:
        subprocess.run(_privileged_cmd(rule), capture_output=True, check=False)

    logger.info("NAT rules removed for bridge %s via %s", bridge, host_iface)


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
    """Add iptables FORWARD rules for a specific TAP device to FCM chain.

    Rules to add in FCM-FORWARD chain (idempotent — check with -C before adding with -A):
    - `iptables -A FCM-FORWARD -i {bridge} -o {tap_name} -j ACCEPT`
    - `iptables -A FCM-FORWARD -i {tap_name} -o {bridge} -j ACCEPT`
    """
    forward_chain = FCM_FORWARD_CHAIN

    # Ensure FCM chains exist before adding rules
    setup_fcm_chains()

    try:
        _ensure_iptables_rule(
            ["iptables", "-C", forward_chain, "-i", bridge, "-o", tap_name, "-j", "ACCEPT"],
            ["iptables", "-A", forward_chain, "-i", bridge, "-o", tap_name, "-j", "ACCEPT"],
            f"Failed to add FORWARD rule for {bridge} -> {tap_name}",
        )
        _ensure_iptables_rule(
            ["iptables", "-C", forward_chain, "-i", tap_name, "-o", bridge, "-j", "ACCEPT"],
            ["iptables", "-A", forward_chain, "-i", tap_name, "-o", bridge, "-j", "ACCEPT"],
            f"Failed to add FORWARD rule for {tap_name} -> {bridge}",
        )
    except NetworkError as e:
        raise NetworkError(f"Failed to add FORWARD rules for {tap_name}: {e}") from e

    logger.debug("FORWARD rules added for TAP %s ↔ bridge %s", tap_name, bridge)


def remove_iptables_forward_rules(tap_name: str, bridge: str = BRIDGE_NAME) -> None:
    """Remove iptables FORWARD rules for a specific TAP device from FCM chain.

    - `iptables -D FCM-FORWARD -i {bridge} -o {tap_name} -j ACCEPT`
    - `iptables -D FCM-FORWARD -i {tap_name} -o {bridge} -j ACCEPT`
    - Safe to call even if rules don't exist (ignore errors).
    """
    forward_chain = FCM_FORWARD_CHAIN

    # Only try to remove rules if FCM chain exists
    if not chain_exists(forward_chain, "filter"):
        return

    subprocess.run(
        _privileged_cmd(
            ["iptables", "-D", forward_chain, "-i", bridge, "-o", tap_name, "-j", "ACCEPT"]
        ),
        capture_output=True,
        check=False,
    )
    subprocess.run(
        _privileged_cmd(
            ["iptables", "-D", forward_chain, "-i", tap_name, "-o", bridge, "-j", "ACCEPT"]
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
