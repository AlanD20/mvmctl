"""Firewall management for per-VM INPUT rules via nocloud.

This module manages the MVM-NOCLOUD-INPUT chain which controls
VM-to-host access for nocloud service ports.
"""

from __future__ import annotations

import logging
import subprocess

from mvmctl.constants import MVM_NOCLOUD_NET_INPUT_CHAIN
from mvmctl.exceptions import NetworkError
from mvmctl.utils.process import privileged_cmd as _privileged_cmd

logger = logging.getLogger(__name__)


def _chain_exists(chain: str, table: str = "filter") -> bool:
    """Check if an iptables chain exists.

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


def _iptables_rule_exists(rule_args: list[str]) -> bool:
    """Check if an iptables rule exists.

    Args:
        rule_args: The full rule arguments (including iptables command).

    Returns:
        True if the rule exists, False otherwise.
    """
    result = subprocess.run(
        _privileged_cmd(rule_args),
        capture_output=True,
        check=False,
    )
    return result.returncode == 0


def _build_iptables_restore_input(rules: list[dict[str, str]]) -> str:
    """Build iptables-restore input from rule list.

    Args:
        rules: List of rule dicts with 'table', 'chain', 'rule' keys.

    Returns:
        iptables-restore formatted input string.
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
            lines.append(f":{chain} - [0:0]")
        for rule in table_rules:
            lines.append(f"-A {rule['chain']} {rule['rule']}")
        lines.append("COMMIT")

    return "\n".join(lines) + "\n"


def _apply_iptables_rules_batch(
    rules: list[dict[str, str]],
    error_label: str = "Failed to apply iptables rules",
) -> None:
    """Apply multiple iptables rules atomically.

    Args:
        rules: List of rule dicts with 'table', 'chain', 'rule' keys.
        error_label: Error message prefix for failures.

    Raises:
        NetworkError: If iptables-restore fails.
    """
    if not rules:
        return

    restore_input = _build_iptables_restore_input(rules)

    try:
        subprocess.run(
            _privileged_cmd(["iptables-restore", "--noflush"]),
            input=restore_input,
            text=True,
            check=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as e:
        raise NetworkError(error_label) from e


def _create_chain_if_missing(chain_name: str, table: str = "filter") -> bool:
    """Create an iptables chain if it doesn't exist.

    Args:
        chain_name: Name of the chain to create.
        table: Table name. Default is filter.

    Returns:
        True if the chain was created, False if it already existed.
    """
    if _chain_exists(chain_name, table):
        logger.debug("Chain %s already exists", chain_name)
        return False

    cmd = ["iptables", "-t", table, "-N", chain_name]
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
            logger.debug("Chain %s already exists", chain_name)
            return False
        raise NetworkError(f"Failed to create {chain_name} chain") from e


def setup_nocloud_input_chain() -> None:
    """Create MVM-NOCLOUD-INPUT chain and link from INPUT.

    Creates the firewall chain for per-VM INPUT rules and ensures
    it's linked from the main INPUT chain. Idempotent - safe to call
    multiple times.

    Raises:
        NetworkError: If chain creation or jump rule setup fails.
    """
    chain_name = MVM_NOCLOUD_NET_INPUT_CHAIN

    # Create the chain if it doesn't exist
    _create_chain_if_missing(chain_name)

    # Remove any existing jump to our chain (cleanup stale rules)
    subprocess.run(
        _privileged_cmd(["iptables", "-D", "INPUT", "-j", chain_name]),
        capture_output=True,
        check=False,
    )

    # Add jump from INPUT to our chain if not already present
    jump_rule = ["iptables", "-C", "INPUT", "-j", chain_name]
    if not _iptables_rule_exists(jump_rule):
        try:
            subprocess.run(
                _privileged_cmd(["iptables", "-I", "INPUT", "1", "-j", chain_name]),
                check=True,
                capture_output=True,
            )
            logger.debug("Inserted jump from INPUT to %s", chain_name)
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to add jump to {chain_name}") from e

    logger.info("MVM-NOCLOUD-INPUT chain configured")


def add_nocloud_input_rule(vm_ip: str, vm_name: str, port: int) -> None:
    """Add INPUT rule allowing a specific VM to reach a specific port.

    Creates the chain if needed, then adds a rule allowing the given
    VM IP to reach the specified port. The rule includes a comment
    for identification: # mvm-nocloud:<vm_name>:<port>

    Args:
        vm_ip: The VM's IP address (e.g., "10.0.0.2").
        vm_name: The VM's name for identification in rule comment.
        port: The port number to allow.

    Raises:
        NetworkError: If rule addition fails.
    """
    chain_name = MVM_NOCLOUD_NET_INPUT_CHAIN

    # Ensure the chain exists
    setup_nocloud_input_chain()

    # Build rule with comment for identification
    rule_spec = (
        f"-s {vm_ip} -p tcp --dport {port} "
        f'-j ACCEPT -m comment --comment "# mvm-nocloud:{vm_name}:{port}"'
    )

    rules: list[dict[str, str]] = [
        {
            "table": "filter",
            "chain": chain_name,
            "rule": rule_spec,
        },
    ]

    try:
        _apply_iptables_rules_batch(rules, f"Failed to add INPUT rule for {vm_name}:{port}")
    except NetworkError:
        raise

    logger.debug("Added INPUT rule for %s (%s) on port %d", vm_name, vm_ip, port)


def remove_nocloud_input_rule(vm_ip: str, vm_name: str, port: int) -> None:
    """Remove INPUT rule for a specific VM and port.

    Removes the rule that allows the given VM IP to reach the port.
    Idempotent - safe to call even if the rule doesn't exist.

    Args:
        vm_ip: The VM's IP address (e.g., "10.0.0.2").
        vm_name: The VM's name (used in comment matching if needed).
        port: The port number that was allowed.
    """
    chain_name = MVM_NOCLOUD_NET_INPUT_CHAIN

    # Only try to remove rules if chain exists
    if not _chain_exists(chain_name):
        return

    # Build the same rule spec to delete
    rule_spec = (
        f"-s {vm_ip} -p tcp --dport {port} "
        f'-j ACCEPT -m comment --comment "# mvm-nocloud:{vm_name}:{port}"'
    )

    # Delete the rule (idempotent - check=False ignores "No such file" errors)
    subprocess.run(
        _privileged_cmd(["iptables", "-D", chain_name] + rule_spec.split()),
        capture_output=True,
        check=False,
    )

    logger.debug("Removed INPUT rule for %s (%s) on port %d", vm_name, vm_ip, port)


def cleanup_nocloud_input_rules() -> None:
    """Flush all rules from the MVM-NOCLOUD-INPUT chain.

    Removes all rules from the chain but keeps the chain itself
    and the jump from INPUT. Idempotent - safe to call even if
    chain doesn't exist.

    Raises:
        NetworkError: If flush operation fails unexpectedly.
    """
    chain_name = MVM_NOCLOUD_NET_INPUT_CHAIN

    # Only flush if chain exists
    if not _chain_exists(chain_name):
        return

    try:
        subprocess.run(
            _privileged_cmd(["iptables", "-F", chain_name]),
            capture_output=True,
            check=True,
        )
        logger.debug("Flushed all rules from %s", chain_name)
    except subprocess.CalledProcessError as e:
        raise NetworkError(f"Failed to flush {chain_name}") from e
