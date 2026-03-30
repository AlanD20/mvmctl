"""Network infrastructure management for Firecracker VM setup.

.. todo:: S-M5 — Add network namespace isolation between VMs.
   Currently all VMs share the host network namespace, separated only by
   iptables rules.  A future improvement should place each VM's TAP in its
   own network namespace to provide kernel-level isolation.  See
   ``ip netns`` / ``ip link set <tap> netns <ns>`` for the primitives.
"""

import ipaddress
import logging
import secrets
import subprocess
from pathlib import Path

from mvmctl.constants import (
    BRIDGE_NAME,
    DEFAULT_GUEST_MAC_PREFIX,
    DEFAULT_NETWORK_CIDR,
    DEFAULT_NETWORK_GATEWAY,
    IPTABLES_CHAINS,
    MVM_FORWARD_CHAIN,
    MVM_NO_CLOUD_INPUT_CHAIN,
    MVM_POSTROUTING_CHAIN,
)
from mvmctl.exceptions import NetworkError
from mvmctl.utils.process import privileged_cmd as _privileged_cmd

logger = logging.getLogger(__name__)


def _run_ip_batch(commands: list[str]) -> None:
    batch = "\n".join(commands) + "\n"
    subprocess.run(
        _privileged_cmd(["ip", "-batch", "-"]),
        input=batch,
        text=True,
        check=True,
        capture_output=True,
    )


# Derived defaults from constants — kept as module-level aliases so existing
# function signatures that reference them continue to work.
BRIDGE_IP = DEFAULT_NETWORK_GATEWAY
BRIDGE_CIDR = f"{DEFAULT_NETWORK_GATEWAY}/24"
SUBNET = DEFAULT_NETWORK_CIDR
GATEWAY = DEFAULT_NETWORK_GATEWAY

# Interfaces to exclude when listing physical network interfaces
_VIRTUAL_INTERFACE_PREFIXES = ("mvm-", "tap", "br-", "virbr", "docker", "veth")
_EXCLUDED_INTERFACES = ("lo",)


def list_network_interfaces() -> list[str]:
    """List available physical network interfaces.

    Returns a list of network interface names (e.g. ["eth0", "enp0s1", "wlan0"]).
    Excludes loopback, bridges, TAP devices, and virtual interfaces.

    Uses /sys/class/net to enumerate interfaces.

    Returns:
        List of physical network interface names.

    Raises:
        NetworkError: If unable to read network interfaces.
    """
    try:
        net_path = Path("/sys/class/net")
        if not net_path.exists():
            raise NetworkError("Unable to access /sys/class/net")

        interfaces: list[str] = []
        for entry in net_path.iterdir():
            name = entry.name
            # Skip excluded interfaces
            if name in _EXCLUDED_INTERFACES:
                continue
            # Skip virtual interface prefixes
            if any(name.startswith(prefix) for prefix in _VIRTUAL_INTERFACE_PREFIXES):
                continue
            interfaces.append(name)

        return sorted(interfaces)
    except OSError as e:
        logger.debug("Failed to list network interfaces", exc_info=True)
        raise NetworkError("Failed to list network interfaces") from e


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
        logger.debug("Failed to detect default network interface", exc_info=True)
        raise NetworkError("Failed to determine default network interface") from e

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
        logger.debug("Bridge %s already exists, reconciling state", bridge)
        reconcile_cmds: list[str] = []
        if not _bridge_has_ip(bridge, effective_cidr):
            reconcile_cmds.append(f"addr add {effective_cidr} dev {bridge}")
        reconcile_cmds.append(f"link set {bridge} up")
        try:
            _run_ip_batch(reconcile_cmds)
        except subprocess.CalledProcessError as e:
            # Sanitize: don't expose batch commands in error message
            raise NetworkError(f"Failed to setup bridge {bridge}") from e
    else:
        try:
            _run_ip_batch(
                [
                    f"link add name {bridge} type bridge",
                    f"addr add {effective_cidr} dev {bridge}",
                    f"link set {bridge} up",
                ]
            )
        except subprocess.CalledProcessError as e:
            # Sanitize: don't expose batch commands in error message
            raise NetworkError(f"Failed to setup bridge {bridge}") from e

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
            logger.debug("Failed to enable IP forwarding", exc_info=True)
            raise NetworkError("Failed to enable IP forwarding") from e

    logger.info("Bridge %s created with CIDR %s", bridge, cidr)


def teardown_bridge(bridge: str = BRIDGE_NAME) -> None:
    """Remove the bridge interface.

    - `ip link set {bridge} down`
    - `ip link delete {bridge} type bridge`
    - Raises NetworkError on failure.
    """
    try:
        _run_ip_batch([f"link set {bridge} down", f"link delete {bridge} type bridge"])
    except subprocess.CalledProcessError as e:
        # Sanitize: don't expose batch commands in error message
        raise NetworkError(f"Failed to teardown bridge {bridge}") from e

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
        raise NetworkError(f"{error_label}") from e


def _build_iptables_restore_input(rules: list[dict[str, str]]) -> str:
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


def setup_mvm_chains() -> bool:
    """Create MVM iptables chains and link them to built-in chains.

    Creates:
    - MVM-FORWARD chain in filter table
    - MVM-POSTROUTING chain in nat table
    - Jumps from built-in chains to MVM chains

    Idempotent: checks if chains exist before creating.
    Raises NetworkError on failure.
    """
    forward_chain = MVM_FORWARD_CHAIN
    postrouting_chain = MVM_POSTROUTING_CHAIN
    had_existing_chains = False

    def _create_chain_if_missing(chain_name: str, table: str | None = None) -> bool:
        nonlocal had_existing_chains
        if chain_exists(chain_name, table or "filter"):
            had_existing_chains = True
            logger.debug("iptables chain %s already exists; keeping existing chain", chain_name)
            return False
        cmd = ["iptables"]
        if table:
            cmd.extend(["-t", table])
        cmd.extend(["-N", chain_name])
        try:
            subprocess.run(_privileged_cmd(cmd), check=True, capture_output=True)
            logger.debug("Created iptables chain %s", chain_name)
            return True
        except subprocess.CalledProcessError as e:
            stderr = ""
            if isinstance(e.stderr, bytes):
                stderr = e.stderr.decode(errors="ignore")
            elif isinstance(e.stderr, str):
                stderr = e.stderr
            if "Chain already exists" in stderr:
                had_existing_chains = True
                logger.debug("iptables chain %s already exists; keeping existing chain", chain_name)
                return False
            raise NetworkError(f"Failed to create {chain_name} chain") from e

    # Create MVM-FORWARD chain in filter table
    _create_chain_if_missing(forward_chain)

    # Create MVM-POSTROUTING chain in nat table
    _create_chain_if_missing(postrouting_chain, "nat")

    subprocess.run(
        _privileged_cmd(["iptables", "-D", "FORWARD", "-j", forward_chain]),
        capture_output=True,
        check=False,
    )
    jump_rule = ["iptables", "-C", "FORWARD", "-j", forward_chain]
    if not _iptables_rule_exists(jump_rule):
        try:
            subprocess.run(
                _privileged_cmd(["iptables", "-I", "FORWARD", "1", "-j", forward_chain]),
                check=True,
                capture_output=True,
            )
            logger.debug("Inserted high-priority jump from FORWARD to %s", forward_chain)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to add jump to {forward_chain}") from e

    subprocess.run(
        _privileged_cmd(["iptables", "-t", "nat", "-D", "POSTROUTING", "-j", postrouting_chain]),
        capture_output=True,
        check=False,
    )
    jump_rule_nat = ["iptables", "-t", "nat", "-C", "POSTROUTING", "-j", postrouting_chain]
    if not _iptables_rule_exists(jump_rule_nat):
        try:
            subprocess.run(
                _privileged_cmd(
                    ["iptables", "-t", "nat", "-I", "POSTROUTING", "1", "-j", postrouting_chain]
                ),
                check=True,
                capture_output=True,
            )
            logger.debug("Inserted high-priority jump from POSTROUTING to %s", postrouting_chain)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to add jump to {postrouting_chain}") from e

    logger.info("MVM iptables chains configured")
    return had_existing_chains


def teardown_mvm_chains() -> None:
    """Remove MVM iptables chains and their jumps from built-in chains.

    Removes:
    - Jump rules from FORWARD and POSTROUTING
    - All rules in MVM chains (flush)
    - The MVM chains themselves

    Safe to call even if chains don't exist.
    Raises NetworkError on failure.
    """
    forward_chain = MVM_FORWARD_CHAIN
    postrouting_chain = MVM_POSTROUTING_CHAIN

    # Remove jump from FORWARD to MVM-FORWARD
    if chain_exists(forward_chain, "filter"):
        subprocess.run(
            _privileged_cmd(["iptables", "-D", "FORWARD", "-j", forward_chain]),
            capture_output=True,
            check=False,
        )

        # Flush and delete MVM-FORWARD chain
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
            raise NetworkError(f"Failed to remove {forward_chain} chain") from e

    # Remove jump from POSTROUTING to MVM-POSTROUTING
    if chain_exists(postrouting_chain, "nat"):
        subprocess.run(
            _privileged_cmd(
                ["iptables", "-t", "nat", "-D", "POSTROUTING", "-j", postrouting_chain]
            ),
            capture_output=True,
            check=False,
        )

        # Flush and delete MVM-POSTROUTING chain
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
            raise NetworkError(f"Failed to remove {postrouting_chain} chain") from e

    logger.info("MVM iptables chains removed")


def teardown_mvm_chains_with_status() -> list[str]:
    forward_chain = MVM_FORWARD_CHAIN
    postrouting_chain = MVM_POSTROUTING_CHAIN

    status: list[str] = []

    if chain_exists(forward_chain, "filter"):
        subprocess.run(
            _privileged_cmd(["iptables", "-D", "FORWARD", "-j", forward_chain]),
            capture_output=True,
            check=False,
        )
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
            status.append(f"MVM Networking: deleted chain {forward_chain}")
        except subprocess.CalledProcessError:
            status.append(f"Warning: MVM Networking: failed to delete chain {forward_chain}")
    else:
        status.append(f"MVM Networking: chain {forward_chain} already deleted, skipping")

    if chain_exists(postrouting_chain, "nat"):
        subprocess.run(
            _privileged_cmd(
                ["iptables", "-t", "nat", "-D", "POSTROUTING", "-j", postrouting_chain]
            ),
            capture_output=True,
            check=False,
        )
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
            status.append(f"MVM Networking: deleted chain {postrouting_chain}")
        except subprocess.CalledProcessError:
            status.append(f"Warning: MVM Networking: failed to delete chain {postrouting_chain}")
    else:
        status.append(f"MVM Networking: chain {postrouting_chain} already deleted, skipping")

    return status


def teardown_all_mvm_chains() -> None:
    """Remove all MVM iptables chains and their jumps from built-in chains.

    Iterates over IPTABLES_CHAINS from constants and removes each chain
    along with its jump rule from the appropriate built-in chain.

    Safe to call even if chains don't exist.
    Raises NetworkError on failure.
    """
    for chain_name, table in IPTABLES_CHAINS:
        # Determine the built-in chain to remove jump from
        if chain_name == MVM_FORWARD_CHAIN:
            built_in = "FORWARD"
        elif chain_name == MVM_POSTROUTING_CHAIN:
            built_in = "POSTROUTING"
        elif chain_name == MVM_NO_CLOUD_INPUT_CHAIN:
            built_in = "INPUT"
        else:
            continue

        # Check if chain exists
        if not chain_exists(chain_name, table):
            continue

        # Remove jump rule (ignore errors - may not exist)
        if table == "nat":
            subprocess.run(
                _privileged_cmd(["iptables", "-t", "nat", "-D", built_in, "-j", chain_name]),
                capture_output=True,
                check=False,
            )
        else:
            subprocess.run(
                _privileged_cmd(["iptables", "-D", built_in, "-j", chain_name]),
                capture_output=True,
                check=False,
            )

        # Flush and delete the chain
        try:
            if table == "nat":
                subprocess.run(
                    _privileged_cmd(["iptables", "-t", "nat", "-F", chain_name]),
                    check=True,
                    capture_output=True,
                )
                subprocess.run(
                    _privileged_cmd(["iptables", "-t", "nat", "-X", chain_name]),
                    check=True,
                    capture_output=True,
                )
            else:
                subprocess.run(
                    _privileged_cmd(["iptables", "-F", chain_name]),
                    check=True,
                    capture_output=True,
                )
                subprocess.run(
                    _privileged_cmd(["iptables", "-X", chain_name]),
                    check=True,
                    capture_output=True,
                )
            logger.debug("Removed iptables chain %s from %s table", chain_name, table)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to remove {chain_name} chain from {table} table") from e

    logger.info("All MVM iptables chains removed")


def teardown_all_mvm_chains_with_status() -> list[str]:
    """Remove all MVM iptables chains with status reporting.

    Returns:
        List of status strings describing what was cleaned up.
    """
    status: list[str] = []

    for chain_name, table in IPTABLES_CHAINS:
        # Determine the built-in chain to remove jump from
        if chain_name == MVM_FORWARD_CHAIN:
            built_in = "FORWARD"
        elif chain_name == MVM_POSTROUTING_CHAIN:
            built_in = "POSTROUTING"
        elif chain_name == MVM_NO_CLOUD_INPUT_CHAIN:
            built_in = "INPUT"
        else:
            continue

        if not chain_exists(chain_name, table):
            status.append(f"MVM Networking: chain {chain_name} already deleted, skipping")
            continue

        # Remove jump rule (ignore errors)
        if table == "nat":
            subprocess.run(
                _privileged_cmd(["iptables", "-t", "nat", "-D", built_in, "-j", chain_name]),
                capture_output=True,
                check=False,
            )
        else:
            subprocess.run(
                _privileged_cmd(["iptables", "-D", built_in, "-j", chain_name]),
                capture_output=True,
                check=False,
            )

        # Flush and delete the chain
        try:
            if table == "nat":
                subprocess.run(
                    _privileged_cmd(["iptables", "-t", "nat", "-F", chain_name]),
                    check=True,
                    capture_output=True,
                )
                subprocess.run(
                    _privileged_cmd(["iptables", "-t", "nat", "-X", chain_name]),
                    check=True,
                    capture_output=True,
                )
            else:
                subprocess.run(
                    _privileged_cmd(["iptables", "-F", chain_name]),
                    check=True,
                    capture_output=True,
                )
                subprocess.run(
                    _privileged_cmd(["iptables", "-X", chain_name]),
                    check=True,
                    capture_output=True,
                )
            status.append(f"MVM Networking: deleted chain {chain_name}")
        except subprocess.CalledProcessError:
            status.append(f"Warning: MVM Networking: failed to delete chain {chain_name}")

    return status


def setup_nat(
    bridge: str = BRIDGE_NAME,
    internet_iface: str | None = None,
    *,
    cidr: str | None = None,
) -> None:
    """Set up NAT (MASQUERADE) for the bridge subnet using MVM chains.

    - Gets internet_iface via get_default_interface() if not provided
    - Uses provided cidr or defaults to SUBNET constant for source filtering
    - Adds MASQUERADE rule with source filtering to MVM-POSTROUTING chain
    - Adds FORWARD rules to MVM-FORWARD chain
    - Is idempotent: uses _ensure_iptables_rule for atomic rule application
    - Raises NetworkError on failure.

    Args:
        bridge: Bridge interface name.
        internet_iface: Physical interface for NAT (defaults to default route interface).
        cidr: Source CIDR for NAT rules (defaults to SUBNET).
    """
    if internet_iface is None:
        internet_iface = get_default_interface()

    if cidr is None:
        cidr = SUBNET

    forward_chain = MVM_FORWARD_CHAIN
    postrouting_chain = MVM_POSTROUTING_CHAIN
    comment = f"mvm-nat:{bridge}"

    setup_mvm_chains()

    # MASQUERADE rule with source filtering and comment
    masquerade_check = [
        "iptables",
        "-t",
        "nat",
        "-C",
        postrouting_chain,
        "-s",
        cidr,
        "-o",
        internet_iface,
        "-j",
        "MASQUERADE",
        "-m",
        "comment",
        "--comment",
        comment,
    ]
    masquerade_add = [
        "iptables",
        "-t",
        "nat",
        "-A",
        postrouting_chain,
        "-s",
        cidr,
        "-o",
        internet_iface,
        "-j",
        "MASQUERADE",
        "-m",
        "comment",
        "--comment",
        comment,
    ]
    _ensure_iptables_rule(
        masquerade_check,
        masquerade_add,
        f"Failed to add MASQUERADE rule for {bridge}",
    )

    # FORWARD out rule with source filtering
    forward_out_check = [
        "iptables",
        "-t",
        "filter",
        "-C",
        forward_chain,
        "-s",
        cidr,
        "-i",
        bridge,
        "-o",
        internet_iface,
        "-j",
        "ACCEPT",
    ]
    forward_out_add = [
        "iptables",
        "-t",
        "filter",
        "-A",
        forward_chain,
        "-s",
        cidr,
        "-i",
        bridge,
        "-o",
        internet_iface,
        "-j",
        "ACCEPT",
    ]
    _ensure_iptables_rule(
        forward_out_check,
        forward_out_add,
        f"Failed to add FORWARD rule for {bridge}",
    )

    # FORWARD in rule with destination filtering
    forward_in_check = [
        "iptables",
        "-t",
        "filter",
        "-C",
        forward_chain,
        "-d",
        cidr,
        "-i",
        internet_iface,
        "-o",
        bridge,
        "-j",
        "ACCEPT",
    ]
    forward_in_add = [
        "iptables",
        "-t",
        "filter",
        "-A",
        forward_chain,
        "-d",
        cidr,
        "-i",
        internet_iface,
        "-o",
        bridge,
        "-j",
        "ACCEPT",
    ]
    _ensure_iptables_rule(
        forward_in_check,
        forward_in_add,
        f"Failed to add FORWARD rule for {bridge}",
    )

    logger.info(
        "NAT rules configured for bridge %s via %s (source %s)", bridge, internet_iface, cidr
    )


def teardown_nat(
    bridge: str = BRIDGE_NAME,
    force: bool = False,
    *,
    cidr: str | None = None,
) -> None:
    """Remove NAT (MASQUERADE + FORWARD) rules for the bridge from MVM chains.

    IMPORTANT: Only removes rules if `force=True` OR no VMs are currently
    using the bridge (i.e., no TAP devices attached to it).
    This fixes the bash PoC bug where deleting one VM removed the shared rule.

    Removes rules from MVM chains:
    - MASQUERADE rule from MVM-POSTROUTING chain
    - FORWARD rules from MVM-FORWARD chain

    Args:
        bridge: Bridge interface name.
        force: If True, remove rules even if TAP devices are attached.
        cidr: Source CIDR used in NAT rules (for precise rule deletion).
               If None, attempts to detect from existing rules.

    Raises:
        NetworkError: If the MASQUERADE deletion fails.
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
        internet_iface = get_default_interface()
    except NetworkError:
        logger.warning("Could not detect default interface, skipping NAT teardown")
        return

    forward_chain = MVM_FORWARD_CHAIN
    postrouting_chain = MVM_POSTROUTING_CHAIN

    # Only try to remove rules if MVM chains exist
    if not chain_exists(forward_chain, "filter") or not chain_exists(postrouting_chain, "nat"):
        logger.debug("MVM chains do not exist, skipping NAT teardown")
        return

    # If cidr not provided, try to detect it from existing rules
    if cidr is None:
        cidr = _detect_cidr_for_bridge(bridge)

    comment = f"mvm-nat:{bridge}"

    # Build MASQUERADE deletion rule - use source filtering if cidr known
    masquerade_del_args: list[str] = [
        "iptables",
        "-t",
        "nat",
        "-D",
        postrouting_chain,
    ]
    if cidr:
        masquerade_del_args.extend(["-s", cidr])
    masquerade_del_args.extend(
        [
            "-o",
            internet_iface,
            "-j",
            "MASQUERADE",
            "-m",
            "comment",
            "--comment",
            comment,
        ]
    )

    try:
        subprocess.run(
            _privileged_cmd(masquerade_del_args),
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError("Failed to remove MASQUERADE rule") from e

    # FORWARD rule deletions - use source/destination filtering if cidr known
    forward_del_rules: list[list[str]] = []
    if cidr:
        forward_del_rules = [
            [
                "iptables",
                "-D",
                forward_chain,
                "-s",
                cidr,
                "-i",
                bridge,
                "-o",
                internet_iface,
                "-j",
                "ACCEPT",
            ],
            [
                "iptables",
                "-D",
                forward_chain,
                "-d",
                cidr,
                "-i",
                internet_iface,
                "-o",
                bridge,
                "-j",
                "ACCEPT",
            ],
        ]
    else:
        forward_del_rules = [
            ["iptables", "-D", forward_chain, "-i", bridge, "-o", internet_iface, "-j", "ACCEPT"],
            ["iptables", "-D", forward_chain, "-i", internet_iface, "-o", bridge, "-j", "ACCEPT"],
        ]

    for rule in forward_del_rules:
        subprocess.run(_privileged_cmd(rule), capture_output=True, check=False)

    logger.info("NAT rules removed for bridge %s via %s", bridge, internet_iface)


def _detect_cidr_for_bridge(bridge: str) -> str | None:
    """Detect the CIDR used for NAT rules associated with a bridge.

    Examines existing iptables rules in MVM-POSTROUTING chain to find
    the source CIDR used for MASQUERADE rules matching the bridge.

    Args:
        bridge: Bridge interface name.

    Returns:
        The detected CIDR string (e.g. "172.35.0.0/24") or None if not found.
    """
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
            # Parse the line to extract source CIDR
            # Format: num   packets   bytes target     prot opt in     out     source               destination
            parts = line.split()
            if len(parts) >= 9:
                # Source is typically the 9th field (index 8)
                source = parts[8]
                if "/" in source:
                    return source

    return None


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
        _run_ip_batch(
            [
                f"tuntap add dev {tap_name} mode tap",
                f"link set {tap_name} master {bridge}",
                f"link set {tap_name} up",
            ]
        )
    except subprocess.CalledProcessError as e:
        # Sanitize: don't expose batch commands in error message
        raise NetworkError(f"Failed to create TAP {tap_name}") from e

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
        _run_ip_batch([f"link set {tap_name} down", f"link delete {tap_name}"])
    except subprocess.CalledProcessError as e:
        # Sanitize: don't expose batch commands in error message
        raise NetworkError(f"Failed to delete TAP {tap_name}") from e

    logger.info("TAP device %s deleted", tap_name)


def add_iptables_forward_rules(tap_name: str, bridge: str = BRIDGE_NAME) -> None:
    forward_chain = MVM_FORWARD_CHAIN

    setup_mvm_chains()

    forward_out_check = [
        "iptables",
        "-t",
        "filter",
        "-C",
        forward_chain,
        "-i",
        bridge,
        "-o",
        tap_name,
        "-j",
        "ACCEPT",
    ]
    forward_out_add = [
        "iptables",
        "-t",
        "filter",
        "-A",
        forward_chain,
        "-i",
        bridge,
        "-o",
        tap_name,
        "-j",
        "ACCEPT",
    ]
    _ensure_iptables_rule(
        forward_out_check,
        forward_out_add,
        f"Failed to add FORWARD rule for {tap_name}",
    )

    forward_in_check = [
        "iptables",
        "-t",
        "filter",
        "-C",
        forward_chain,
        "-i",
        tap_name,
        "-o",
        bridge,
        "-j",
        "ACCEPT",
    ]
    forward_in_add = [
        "iptables",
        "-t",
        "filter",
        "-A",
        forward_chain,
        "-i",
        tap_name,
        "-o",
        bridge,
        "-j",
        "ACCEPT",
    ]
    _ensure_iptables_rule(
        forward_in_check,
        forward_in_add,
        f"Failed to add FORWARD rule for {tap_name}",
    )

    logger.debug("FORWARD rules added for TAP %s ↔ bridge %s", tap_name, bridge)


def remove_iptables_forward_rules(tap_name: str, bridge: str = BRIDGE_NAME) -> None:
    """Remove iptables FORWARD rules for a specific TAP device from MVM chain.

    - `iptables -D MVM-FORWARD -i {bridge} -o {tap_name} -j ACCEPT`
    - `iptables -D MVM-FORWARD -i {tap_name} -o {bridge} -j ACCEPT`
    - Safe to call even if rules don't exist (ignore errors).
    """
    forward_chain = MVM_FORWARD_CHAIN

    # Only try to remove rules if MVM chain exists
    if not chain_exists(forward_chain, "filter"):
        logger.debug(
            "%s chain does not exist, skipping rule removal for TAP %s", forward_chain, tap_name
        )
        return

    result1 = subprocess.run(
        _privileged_cmd(
            ["iptables", "-D", forward_chain, "-i", bridge, "-o", tap_name, "-j", "ACCEPT"]
        ),
        capture_output=True,
        check=False,
    )
    if result1.returncode != 0:
        logger.warning(
            "Failed to remove iptables FORWARD rule (bridge->tap) for TAP %s: rc=%d",
            tap_name,
            result1.returncode,
        )

    result2 = subprocess.run(
        _privileged_cmd(
            ["iptables", "-D", forward_chain, "-i", tap_name, "-o", bridge, "-j", "ACCEPT"]
        ),
        capture_output=True,
        check=False,
    )
    if result2.returncode != 0:
        logger.warning(
            "Failed to remove iptables FORWARD rule (tap->bridge) for TAP %s: rc=%d",
            tap_name,
            result2.returncode,
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
    """Generate a random MAC address with {DEFAULT_GUEST_MAC_PREFIX} prefix.

    Uses ``secrets`` for cryptographically strong randomness.

    Format: {DEFAULT_GUEST_MAC_PREFIX}:XX:XX:XX:XX where X is random hex.
    """
    rand_bytes = secrets.token_bytes(4)
    suffix = ":".join(f"{b:02x}" for b in rand_bytes)
    return f"{DEFAULT_GUEST_MAC_PREFIX}:{suffix}"
