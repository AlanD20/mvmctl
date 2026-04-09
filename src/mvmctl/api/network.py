"""Network management API — create, remove, list, inspect, IP allocation.

This module is the orchestration layer for network operations. It coordinates
between the pure registry functions in core/network_manager and the system-level
network operations in core/network, while handling all metadata persistence.
"""

from __future__ import annotations

import logging
import os
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional

from mvmctl.constants import DEFAULT_NETWORK_NAME, DEFAULT_NETWORK_SUBNET, MVM_POSTROUTING_CHAIN
from mvmctl.core import host_setup
from mvmctl.core import network as network_core
from mvmctl.core.iptables_tracker import IPTablesTracker
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.network_manager import (
    build_network_config,
    should_preserve_current_default,
    validate_bridge_not_conflicting,
    validate_no_subnet_overlap,
)
from mvmctl.db.models import IPTablesRule, IPTablesRuleType
from mvmctl.db.models import Network as DBNetwork
from mvmctl.exceptions import NetworkError
from mvmctl.models import NetworkConfig, NetworkInspectInfo, NetworkItem, NetworkLease
from mvmctl.utils.full_hash import generate_full_hash_network
from mvmctl.utils.network import (
    bridge_exists,
    get_default_interface,
    get_iptables_rules_for_bridge,
    list_network_interfaces,
    validate_network_interface,
)

logger = logging.getLogger(__name__)


def create_iptables_rule(
    rule: IPTablesRule,
    db: Optional[MVMDatabase] = None,
    tracker: Optional[IPTablesTracker] = None,
) -> IPTablesRule:
    """Atomically create an iptables rule and store it in the database.

    This is the STANDARD way to create iptables rules. All rules are tracked by default.
    The function ensures atomicity:
    - Step 1: Create iptables rule on host
    - Step 2: Write rule record to database
    - If DB write fails, automatically roll back the iptables rule

    Args:
        rule: The IPTablesRule dataclass with all parameters set (id can be None)
        db: Optional MVMDatabase instance (creates new if None)
        tracker: Optional IPTablesTracker instance (creates new if None)

    Returns:
        The stored IPTablesRule with id and timestamps populated

    Raises:
        NetworkError: If either iptables creation or DB write fails
    """
    db = db or MVMDatabase()
    tracker = tracker or IPTablesTracker()

    # Step 1: Create iptables rule via Core layer
    result = tracker.ensure_rule(
        table=rule.table_name,
        chain=rule.chain_name,
        rule_type=rule.rule_type,
        target=rule.target,
        network_id=rule.network_id,
        network_name=rule.network_name,
        protocol=rule.protocol,
        source=rule.source,
        destination=rule.destination,
        in_interface=rule.in_interface,
        out_interface=rule.out_interface,
        sport=rule.sport,
        dport=rule.dport,
    )

    if not result.success:
        raise NetworkError(f"Failed to create iptables rule: {result.error_message}")

    if result.rule is None:
        raise NetworkError("Rule creation succeeded but no rule data returned")

    # Step 2: Write to database (API layer responsibility)
    try:
        stored_rule = db.record_iptables_rule(result.rule)
    except Exception as e:
        # Rollback: Delete the iptables rule we just created
        rollback_result = tracker.remove_rule(result.rule)
        if not rollback_result.success:
            logger.warning(
                "Failed to rollback iptables rule after DB error: %s", rollback_result.error_message
            )
        raise NetworkError(f"Failed to store rule in database: {e}") from e

    return stored_rule


def remove_iptables_rule(
    rule: IPTablesRule,
    db: Optional[MVMDatabase] = None,
    tracker: Optional[IPTablesTracker] = None,
) -> None:
    """Atomically remove an iptables rule and mark it deleted in the database.

    This is the STANDARD way to remove iptables rules.
    The function ensures atomicity:
    - Step 1: Remove iptables rule from host
    - Step 2: Mark rule as deleted (is_active=0) in database
    - If iptables removal fails, DB is not updated

    Args:
        rule: The IPTablesRule to remove (must have id set)
        db: Optional MVMDatabase instance (creates new if None)
        tracker: Optional IPTablesTracker instance (creates new if None)

    Raises:
        NetworkError: If iptables removal fails
        ValueError: If rule.id is not set
    """
    db = db or MVMDatabase()
    tracker = tracker or IPTablesTracker()

    if rule.id is None:
        raise ValueError("Cannot remove rule without id (not stored in DB)")

    # Step 1: Remove iptables rule via Core layer
    result = tracker.remove_rule(rule)

    if not result.success:
        raise NetworkError(f"Failed to remove iptables rule: {result.error_message}")

    if result.rule is None:
        raise NetworkError("Rule removal succeeded but no rule data returned")

    # Step 2: Mark as deleted in database (soft delete)
    try:
        db.mark_iptables_rule_deleted(rule.id)
    except Exception as e:
        # Log but don't rollback - iptables rule is already removed
        logger.warning(
            "Removed iptables rule but failed to update DB: %s. "
            "Rule may appear orphaned until next sync.",
            e,
        )


def _db_network_to_config(db_network: DBNetwork) -> NetworkConfig:
    """Convert a DB Network row to a NetworkConfig dataclass.

    DB Network fields: id, name, subnet, bridge, ipv4_gateway, bridge_active,
                       nat_gateways (str|None), nat_enabled, is_default, created_at, updated_at
    NetworkConfig fields: name, subnet, ipv4_gateway, bridge, nat_enabled,
                         nat_gateways (list[str]), created_at, is_default
    """
    nat_gateways_list: list[str] = []
    if db_network.nat_gateways:
        nat_gateways_list = [g.strip() for g in db_network.nat_gateways.split(",") if g.strip()]
    return NetworkConfig(
        name=db_network.name,
        subnet=db_network.subnet,
        ipv4_gateway=db_network.ipv4_gateway,
        bridge=db_network.bridge,
        nat_enabled=db_network.nat_enabled,
        nat_gateways=nat_gateways_list,
        created_at=db_network.created_at or "",
        is_default=db_network.is_default,
    )


def get_default_network_entry(cache_dir: Path) -> NetworkItem | None:
    """Get default network entry from database.

    Args:
        cache_dir: Directory containing metadata.json (unused, kept for API compatibility).

    Returns:
        NetworkItem if a default network is set, None otherwise.
    """
    db = MVMDatabase()
    db_network = db.get_default_network()
    if db_network is None:
        return None
    return NetworkItem.from_db(db_network)


__all__ = [
    "NetworkConfig",
    "NetworkLease",
    "allocate_network_ip",
    "check_ip_available",
    "create_network",
    "ensure_default_network",
    "get_iptables_rules_for_bridge",
    "get_network",
    "get_network_leases",
    "inspect_network",
    "list_network_interfaces",
    "list_networks",
    "reconcile_networks",
    "release_network_ip",
    "remove_network",
    "restore_networks",
    "set_default_network",
    "validate_network_interface",
]


# ---------------------------------------------------------------------------
# Network registry operations (with metadata persistence)
# ---------------------------------------------------------------------------


def list_networks() -> list[NetworkConfig]:
    """List all configured networks with their metadata.

    Returns:
        List of NetworkConfig objects with is_default populated from database.
    """
    db = MVMDatabase()
    db_networks = db.list_networks()
    if not db_networks:
        return []

    default_network = db.get_default_network()
    default_name = default_network.name if default_network else None

    configs: list[NetworkConfig] = []
    for db_network in db_networks:
        config = _db_network_to_config(db_network)
        config.is_default = db_network.name == default_name
        configs.append(config)

    return sorted(configs, key=lambda c: c.name)


def get_network(name: str) -> NetworkConfig | None:
    """Get a named network by name."""
    db = MVMDatabase()
    db_network = db.get_network_by_name(name)
    if db_network is None:
        return None
    return _db_network_to_config(db_network)


def get_network_leases(name: str) -> list[NetworkLease]:
    """Get all IP leases for a network."""
    db = MVMDatabase()
    network = db.get_network_by_name(name)
    if network is None:
        return []
    db_leases = db.list_leases(network.id)
    return [NetworkLease(vm_id=lease.vm_id or "", ipv4=lease.ipv4) for lease in db_leases]


def set_default_network(name: str) -> None:
    """Set a network as the default for VM creation.

    Args:
        name: Network name to set as default.

    Raises:
        NetworkError: If network does not exist.
    """
    db = MVMDatabase()
    network = db.get_network_by_name(name)
    if network is None:
        raise NetworkError(f"Network '{name}' does not exist")

    db.set_default_network(network.id)


def _get_default_network_entry_name() -> str | None:
    """Get the name of the current default network."""
    db = MVMDatabase()
    default_network = db.get_default_network()
    if default_network is None:
        return None
    return default_network.name


# ---------------------------------------------------------------------------
# IP lease management (with metadata persistence)
# ---------------------------------------------------------------------------


def check_ip_available(network_name: str, ip: str) -> None:
    """Check if an IP is available for use in a network.

    Args:
        network_name: Name of the network to check.
        ip: IP address to verify availability.

    Raises:
        NetworkError: If the IP is already leased to another VM.
    """
    config = get_network(network_name)
    if config is None:
        raise NetworkError(f"Network '{network_name}' not found")

    leases = get_network_leases(network_name)
    from mvmctl.core.network_manager import check_ip_available as _check_ip_available

    _check_ip_available(config, leases, ip)


def is_ip_available(network_name: str, ip: str) -> bool:
    """Check if an IP address is available for use in a network.

    Args:
        network_name: Name of the network to check.
        ip: IP address to verify availability.

    Returns:
        True if the IP is not currently leased, False otherwise.
    """
    config = get_network(network_name)
    if config is None:
        return False

    leases = get_network_leases(network_name)
    from mvmctl.core.network_manager import is_ip_available as _is_ip_available

    return _is_ip_available(config, leases, ip)


def allocate_network_ip(network_name: str, vm_id: str) -> str:
    """Allocate the next available IP from a network's subnet.

    Registers the lease in database.

    Returns:
        The allocated IP address string.
    """
    config = get_network(network_name)
    if config is None:
        raise NetworkError(f"Network '{network_name}' not found")

    leases = get_network_leases(network_name)
    from mvmctl.core.network_manager import allocate_network_ip as _allocate_network_ip

    ip, _updated_leases = _allocate_network_ip(config, leases, vm_id)

    # Persist the lease to database
    db = MVMDatabase()
    network = db.get_network_by_name(network_name)
    if network is None:
        raise NetworkError(f"Network '{network_name}' not found")
    db.acquire_lease(network.id, ip, vm_id)

    return ip


def release_network_ip(network_id: str, vm_id: str) -> None:
    """Release a VM's IP lease from a network."""
    db = MVMDatabase()
    db.release_vm_leases(vm_id)


# ---------------------------------------------------------------------------
# Network orchestration (create, remove, ensure, restore, reconcile)
# ---------------------------------------------------------------------------


def create_network(
    name: str,
    subnet: str,
    ipv4_gateway: str | None = None,
    nat: bool = True,
    nat_gateways: list[str] | None = None,
) -> NetworkConfig:
    """Create a named network, setting up bridge and NAT rules.

    Orchestrates:
    1. Privilege check
    2. Validation (no overlap, no conflict)
    3. Bridge setup
    4. NAT setup
    5. Metadata persistence
    6. iptables persistence

    Args:
        name: Network name.
        subnet: IP subnet in CIDR notation.
        ipv4_gateway: Gateway IPv4 (auto-computed if None).
        nat: Whether to configure NAT/masquerade.
        nat_gateways: Physical interfaces for NAT (auto-detected if None).

    Returns:
        The created NetworkConfig.

    Raises:
        NetworkError: If the network already exists or setup fails.
    """
    from mvmctl.api.host import check_privileges_interactive

    check_privileges_interactive("/usr/sbin/ip", f"create network '{name}'")

    # Validate name
    from mvmctl.utils.validation import validate_entity_name

    validate_entity_name(name, "network")

    # Check if network already exists
    if get_network(name) is not None:
        raise NetworkError(f"Network '{name}' already exists")

    # Validate no subnet overlap
    existing_networks = list_networks()
    validate_no_subnet_overlap(subnet, existing_networks, name)

    # Build config (validates inputs)
    config = build_network_config(
        name=name,
        subnet=subnet,
        ipv4_gateway=ipv4_gateway,
        nat_enabled=nat,
        nat_gateways=nat_gateways,
    )

    # Validate bridge doesn't conflict
    validate_bridge_not_conflicting(config.bridge, existing_networks, name)

    # Setup bridge and NAT
    ipv4_gateway_subnet = f"{config.ipv4_gateway}/{config.subnet.split('/')[1]}"
    db = MVMDatabase()
    try:
        network_core.setup_bridge(config.bridge, ipv4_gateway_subnet=ipv4_gateway_subnet)

        # Create tracked iptables rules for NAT
        if config.nat_enabled:
            network_id = generate_full_hash_network(
                name, config.subnet, datetime.now(tz=timezone.utc).isoformat()
            )
            for gateway_iface in config.nat_gateways or [get_default_interface()]:
                # Create MASQUERADE rule
                masquerade_rule = IPTablesRule(
                    table_name="nat",
                    chain_name="MVM-POSTROUTING",
                    rule_type=IPTablesRuleType.MASQUERADE,
                    target="MASQUERADE",
                    network_id=network_id,
                    network_name=name,
                    source=config.subnet,
                    out_interface=gateway_iface,
                )
                create_iptables_rule(masquerade_rule, db=db)

                # Create FORWARD IN rule (bridge -> gateway)
                forward_in_rule = IPTablesRule(
                    table_name="filter",
                    chain_name="MVM-FORWARD",
                    rule_type=IPTablesRuleType.FORWARD_IN,
                    target="ACCEPT",
                    network_id=network_id,
                    network_name=name,
                    source=config.subnet,
                    in_interface=config.bridge,
                    out_interface=gateway_iface,
                )
                create_iptables_rule(forward_in_rule, db=db)

                # Create FORWARD OUT rule (gateway -> bridge)
                forward_out_rule = IPTablesRule(
                    table_name="filter",
                    chain_name="MVM-FORWARD",
                    rule_type=IPTablesRuleType.FORWARD_OUT,
                    target="ACCEPT",
                    network_id=network_id,
                    network_name=name,
                    destination=config.subnet,
                    in_interface=gateway_iface,
                    out_interface=config.bridge,
                )
                create_iptables_rule(forward_out_rule, db=db)

            # Sync rules to host (replaces _ensure_iptables_rule pattern)
            try:
                sync_iptables_rules(network_id, db=db)
            except NetworkError as e:
                # Cleanup on failure - delete the network (CASCADE removes rules)
                db.delete_network(network_id)
                try:
                    network_core.teardown_bridge(config.bridge)
                except NetworkError as teardown_error:
                    logger.warning("Rollback: failed to tear down bridge: %s", teardown_error)
                raise NetworkError(f"Failed to sync iptables rules to host: {e}") from e

    except NetworkError:
        # Rollback on failure
        try:
            network_core.teardown_bridge(config.bridge)
        except NetworkError as e:
            logger.warning("Rollback: failed to tear down bridge: %s", e)
        raise

    # Persist to database
    created_at = datetime.now(tz=timezone.utc).isoformat()
    db_network = DBNetwork(
        id=generate_full_hash_network(name, config.subnet, created_at),
        name=config.name,
        subnet=config.subnet,
        bridge=config.bridge,
        ipv4_gateway=config.ipv4_gateway,
        bridge_active=True,
        nat_gateways=",".join(config.nat_gateways) if config.nat_gateways else None,
        nat_enabled=config.nat_enabled,
        is_default=False,
        created_at=created_at,
    )
    db.upsert_network(db_network)

    # Persist iptables rules if root
    if os.getuid() == 0:
        host_setup.save_iptables_rules()

    from mvmctl.utils.audit import log_audit

    log_audit("network.create", f"name={name},subnet={subnet}")

    return config


def remove_network(name: str) -> None:
    """Remove a named network, tearing down its bridge and NAT rules.

    Orchestrates:
    1. Privilege check
    2. Check for attached VMs
    3. Teardown NAT rules
    4. Teardown bridge
    5. Remove metadata entry
    6. iptables persistence

    Args:
        name: Network name to remove.

    Raises:
        NetworkError: If the network has VMs attached or doesn't exist.
    """
    from mvmctl.api.host import check_privileges_interactive

    check_privileges_interactive("/usr/sbin/ip", f"remove network '{name}'")

    # Check if trying to remove default network while VMs exist
    if name == DEFAULT_NETWORK_NAME:
        from mvmctl.core.vm_manager import VMManager

        existing_vms = VMManager().list_all()
        if existing_vms:
            raise NetworkError(
                "Cannot remove the 'default' network while VMs exist. Remove all VMs first."
            )

    config = get_network(name)
    if config is None:
        raise NetworkError(f"Network '{name}' not found")

    # Check for attached VMs
    leases = get_network_leases(name)
    if leases:
        vm_names = ", ".join(lease.vm_id for lease in leases)
        raise NetworkError(
            f"Network '{name}' still has VMs attached: {vm_names}. Remove those VMs first."
        )

    # Teardown host resources
    db = MVMDatabase()
    network = db.get_network_by_name(name)
    if network is None:
        raise NetworkError(f"Network '{name}' not found in database")

    # Remove tracked iptables rules
    rules = db.get_iptables_rules_for_network(network.id, active_only=True)
    cleanup_errors = []

    for rule in rules:
        try:
            remove_iptables_rule(rule, db=db)
        except NetworkError as e:
            cleanup_errors.append(str(e))

    # Also teardown bridge via core functions (for non-tracked legacy)
    try:
        if config.nat_enabled:
            network_core.teardown_nat(bridge=config.bridge, force=True, subnet=config.subnet)
        network_core.teardown_bridge(config.bridge)
    except NetworkError as e:
        cleanup_errors.append(str(e))

    # Delete network (CASCADE will delete remaining rule records from DB)
    db.delete_network(network.id)

    if cleanup_errors:
        logger.warning("Partial cleanup for network '%s': %s", name, "; ".join(cleanup_errors))

    if os.getuid() == 0:
        host_setup.save_iptables_rules()

    from mvmctl.utils.audit import log_audit

    log_audit("network.remove", f"name={name}")


def inspect_network(name: str) -> NetworkInspectInfo:
    """Return full details for a named network.

    Args:
        name: Network name to inspect.

    Returns:
        NetworkInspectInfo with network details and attached VMs.

    Raises:
        NetworkError: If network not found.
    """
    from mvmctl.core.vm_manager import VMManager

    config = get_network(name)
    if config is None:
        raise NetworkError(f"Network '{name}' not found")

    leases = get_network_leases(name)
    active = bridge_exists(config.bridge)

    db = MVMDatabase()
    network = db.get_network_by_name(name)
    if network:
        db.update_network_bridge_active(network.id, active)

    vm_manager = VMManager()
    enriched_vms: list[dict[str, Any]] = []
    for lease in leases:
        vm = vm_manager.get(lease.vm_id)
        if vm is not None:
            enriched_vms.append(
                {
                    "vm_id": lease.vm_id,
                    "ipv4": lease.ipv4,
                    "status": vm.status.value,
                    "pid": vm.pid,
                    "api_socket_path": str(vm.api_socket_path) if vm.api_socket_path else None,
                }
            )
        else:
            enriched_vms.append(
                {
                    "vm_id": lease.vm_id,
                    "ipv4": lease.ipv4,
                    "status": "unknown",
                    "pid": None,
                    "api_socket_path": None,
                }
            )

    return NetworkInspectInfo(
        name=config.name,
        subnet=config.subnet,
        ipv4_gateway=config.ipv4_gateway,
        bridge=config.bridge,
        nat_enabled=config.nat_enabled,
        nat_gateways=config.nat_gateways,
        created_at=config.created_at,
        bridge_exists=active,
        vms=enriched_vms,
    )


def ensure_default_network() -> NetworkConfig:
    """Ensure the default network exists with all host resources materialized.

    If the network exists in metadata but the actual bridge, iptables chains,
    or NAT rules are missing (e.g., after a reboot), this function recreates
    them from the stored configuration.

    Returns:
        The default NetworkConfig.

    Raises:
        NetworkError: If setup fails.
    """
    from mvmctl.utils.network import _iptables_rule_exists

    config = get_network(DEFAULT_NETWORK_NAME)

    if config is not None:
        bridge_missing = not bridge_exists(config.bridge)
        chains_missing = not network_core.setup_mvm_chains()

        nat_missing = False
        if config.nat_enabled:
            try:
                nat_gateways = config.nat_gateways or [get_default_interface()]
                # Check if at least one gateway has a MASQUERADE rule
                for gateway_iface in nat_gateways:
                    masquerade_check = [
                        "iptables",
                        "-t",
                        "nat",
                        "-C",
                        MVM_POSTROUTING_CHAIN,
                        "-s",
                        config.subnet,
                        "-o",
                        gateway_iface,
                        "-j",
                        "MASQUERADE",
                    ]
                    if not _iptables_rule_exists(masquerade_check):
                        nat_missing = True
                        break
            except Exception:
                nat_missing = True

        if bridge_missing or chains_missing or nat_missing:
            ipv4_gateway_subnet = f"{config.ipv4_gateway}/{config.subnet.split('/')[1]}"
            try:
                if bridge_missing:
                    network_core.setup_bridge(
                        config.bridge, ipv4_gateway_subnet=ipv4_gateway_subnet
                    )
                if config.nat_enabled:
                    nat_gateways = config.nat_gateways or [get_default_interface()]
                    network_core.setup_nat(
                        config.bridge, nat_gateways=nat_gateways, subnet=config.subnet
                    )
                    if os.getuid() == 0:
                        host_setup.save_iptables_rules()
                db = MVMDatabase()
                network = db.get_network_by_name(DEFAULT_NETWORK_NAME)
                if network:
                    db.update_network_bridge_active(network.id, True)
                current_default = _get_default_network_entry_name()
                if not should_preserve_current_default(current_default, DEFAULT_NETWORK_NAME):
                    set_default_network(DEFAULT_NETWORK_NAME)
            except NetworkError:
                if bridge_missing:
                    try:
                        network_core.teardown_bridge(config.bridge)
                    except NetworkError:
                        pass
                raise
        else:
            current_default = _get_default_network_entry_name()
            if not should_preserve_current_default(current_default, DEFAULT_NETWORK_NAME):
                set_default_network(DEFAULT_NETWORK_NAME)
        return config

    # Auto-detect internet-facing interface for NAT gateway
    default_iface = get_default_interface()
    if not default_iface:
        raise NetworkError(
            "Could not auto-detect internet-facing interface. "
            "Please create the default network manually with: "
            "mvm network create default --nat-gateways <interface>"
        )

    config = create_network(
        DEFAULT_NETWORK_NAME, subnet=DEFAULT_NETWORK_SUBNET, nat=True, nat_gateways=[default_iface]
    )
    current_default = _get_default_network_entry_name()
    if not should_preserve_current_default(current_default, DEFAULT_NETWORK_NAME):
        set_default_network(DEFAULT_NETWORK_NAME)
    return config


def reconcile_networks() -> list[NetworkInspectInfo]:
    """Compare stored network state with actual kernel bridge state.

    For each network in database, checks whether its bridge device still
    exists on the host. Updates bridge_active in database and
    returns a list of network inspection results.

    Returns:
        List of NetworkInspectInfo with reconciliation results.
    """
    db = MVMDatabase()
    results: list[NetworkInspectInfo] = []

    for config in list_networks():
        network = db.get_network_by_name(config.name)
        stored_active = network.bridge_active if network else False
        actual_active = bridge_exists(config.bridge)

        stale = (stored_active is True) and (not actual_active)

        if network:
            db.update_network_bridge_active(network.id, actual_active)

        leases = get_network_leases(config.name)
        vms: list[dict[str, Any]] = [{"vm_id": lease.vm_id, "ipv4": lease.ipv4} for lease in leases]

        results.append(
            NetworkInspectInfo(
                name=config.name,
                subnet=config.subnet,
                ipv4_gateway=config.ipv4_gateway,
                bridge=config.bridge,
                nat_enabled=config.nat_enabled,
                nat_gateways=config.nat_gateways,
                created_at=config.created_at,
                bridge_exists=actual_active,
                vms=vms,
            )
        )

        if stale:
            logger.warning("Stale network detected (bridge missing): %s", config.name)

    return results


def restore_networks() -> list[str]:
    """Restore all networks from database, recreating bridges and NAT rules.

    This function is called during host init to restore networks after a clean
    or reboot. It validates stored interfaces and recreates network resources.

    Returns:
        List of status messages describing what was restored.
    """
    networks = list_networks()
    if not networks:
        return []

    status: list[str] = []
    db = MVMDatabase()

    for config in networks:
        if bridge_exists(config.bridge):
            status.append(f"Network '{config.name}': bridge already exists, skipping")
            continue

        ipv4_gateway_subnet = f"{config.ipv4_gateway}/{config.subnet.split('/')[1]}"

        try:
            network_core.setup_bridge(config.bridge, ipv4_gateway_subnet=ipv4_gateway_subnet)
            status.append(f"Network '{config.name}': created bridge {config.bridge}")
        except NetworkError as e:
            status.append(f"Network '{config.name}': failed to create bridge: {e}")
            continue

        if config.nat_enabled:
            nat_gateways = config.nat_gateways or []

            # Validate stored gateways
            validated_gateways: list[str] = []
            for gateway_iface in nat_gateways:
                try:
                    validate_network_interface(gateway_iface)
                    validated_gateways.append(gateway_iface)
                except NetworkError:
                    logger.warning(
                        "Network '%s': stored gateway '%s' is invalid, skipping",
                        config.name,
                        gateway_iface,
                    )

            # If no valid gateways, auto-detect
            if not validated_gateways:
                try:
                    default_iface = get_default_interface()
                    validate_network_interface(default_iface)
                    validated_gateways = [default_iface]
                except NetworkError as e:
                    status.append(f"Network '{config.name}': no valid interface for NAT: {e}")
                    continue

            try:
                network_core.setup_nat(
                    config.bridge, nat_gateways=validated_gateways, subnet=config.subnet
                )
                status.append(
                    f"Network '{config.name}': NAT configured via {', '.join(validated_gateways)}"
                )

                if config.nat_gateways != validated_gateways:
                    network = db.get_network_by_name(config.name)
                    if network:
                        network.nat_gateways = ",".join(validated_gateways)
                        db.upsert_network(network)
                    config.nat_gateways = validated_gateways
            except NetworkError as e:
                status.append(f"Network '{config.name}': failed to configure NAT: {e}")

        network = db.get_network_by_name(config.name)
        if network:
            db.update_network_bridge_active(network.id, True)

    return status


def sync_iptables_rules(
    network_id: str,
    db: Optional[MVMDatabase] = None,
    tracker: Optional[IPTablesTracker] = None,
) -> list[IPTablesRule]:
    """Sync iptables rules from database to host.

    This function:
    1. Reads all active rules for the network from DB
    2. Ensures each rule exists on the host (creates if missing)
    3. Returns list of verified rules

    Used by:
    - Automatic sync processes (called periodically)
    - mvm network sync command (via IPTablesSynchronizer)
    - Network creation to ensure rules are in sync

    Args:
        network_id: The network ID to sync rules for
        db: Optional MVMDatabase instance
        tracker: Optional IPTablesTracker instance

    Returns:
        List of IPTablesRule objects that were synced (all active rules for network)

    Raises:
        NetworkError: If any rule creation fails
    """
    db = db or MVMDatabase()
    tracker = tracker or IPTablesTracker()

    # Get network name for human-readable comments
    network = db.get_network(network_id)
    if not network:
        raise NetworkError(f"Network {network_id} not found")
    network_name = network.name

    # Get all active rules for this network from DB
    rules = db.get_iptables_rules_for_network(network_id, active_only=True)

    # Populate network_name in rules (needed for comments)
    for rule in rules:
        rule.network_name = network_name

    synced_rules: list[IPTablesRule] = []
    failed_rules: list[tuple[IPTablesRule, Optional[str]]] = []

    for rule in rules:
        result = tracker.ensure_rule(
            table=rule.table_name,
            chain=rule.chain_name,
            rule_type=rule.rule_type,
            target=rule.target,
            network_id=rule.network_id,
            network_name=rule.network_name,
            protocol=rule.protocol,
            source=rule.source,
            destination=rule.destination,
            in_interface=rule.in_interface,
            out_interface=rule.out_interface,
            sport=rule.sport,
            dport=rule.dport,
        )

        if result.success:
            synced_rules.append(rule)
            # Update verification timestamp
            if rule.id:
                db.update_iptables_rule_verified(rule.id)
        else:
            failed_rules.append((rule, result.error_message))

    if failed_rules:
        error_details = "; ".join([f"{r.rule_type}: {e}" for r, e in failed_rules])
        raise NetworkError(f"Failed to sync {len(failed_rules)} rules: {error_details}")

    return synced_rules
