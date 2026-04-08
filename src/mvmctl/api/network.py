"""Network management API — create, remove, list, inspect, IP allocation.

This module is the orchestration layer for network operations. It coordinates
between the pure registry functions in core/network_manager and the system-level
network operations in core/network, while handling all metadata persistence.
"""

from __future__ import annotations

import logging
import os
from pathlib import Path
from typing import Any

from mvmctl.constants import DEFAULT_NETWORK_NAME, DEFAULT_NETWORK_SUBNET, MVM_POSTROUTING_CHAIN
from mvmctl.core import host_setup, metadata
from mvmctl.core import network as network_core
from mvmctl.core.network_manager import (
    NetworkConfig,
    NetworkLease,
    build_network_config,
    config_to_network_entry,
    leases_from_entry,
    leases_to_dicts,
    network_entry_to_config,
    should_preserve_current_default,
    validate_bridge_not_conflicting,
    validate_no_subnet_overlap,
)
from mvmctl.exceptions import NetworkError
from mvmctl.utils.fs import get_cache_dir
from mvmctl.utils.network import (
    bridge_exists,
    get_default_interface,
    get_iptables_rules_for_bridge,
    list_network_interfaces,
    validate_network_interface,
)

logger = logging.getLogger(__name__)


def get_default_network_entry(cache_dir: Path) -> tuple[str, dict[str, Any]] | None:
    """Get default network entry from metadata API."""
    from mvmctl.api import metadata as metadata_api

    return metadata_api.get_default_network_entry(cache_dir)


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
        List of NetworkConfig objects with is_default populated from metadata.
    """
    cache_dir = get_cache_dir()
    entries = metadata.list_network_entries(cache_dir)
    if not entries:
        return []

    default_entry = get_default_network_entry(cache_dir)
    default_name = default_entry[0] if default_entry else None

    configs: list[NetworkConfig] = []
    for name, entry in entries.items():
        config = network_entry_to_config(name, entry)
        if config is not None:
            config.is_default = name == default_name
            configs.append(config)

    return sorted(configs, key=lambda c: c.name)


def get_network(name: str) -> NetworkConfig | None:
    """Get a named network by name."""
    cache_dir = get_cache_dir()
    entry = metadata.get_network_entry(cache_dir, name)
    return network_entry_to_config(name, entry)


def get_network_leases(name: str) -> list[NetworkLease]:
    """Get all IP leases for a network."""
    cache_dir = get_cache_dir()
    entry = metadata.get_network_entry(cache_dir, name)
    return leases_from_entry(entry)


def set_default_network(name: str) -> None:
    """Set a network as the default for VM creation.

    Args:
        name: Network name to set as default.

    Raises:
        NetworkError: If network does not exist.
    """
    config = get_network(name)
    if config is None:
        raise NetworkError(f"Network '{name}' does not exist")

    cache_dir = get_cache_dir()
    metadata.set_default_network_entry(cache_dir, name)


def _get_default_network_entry_name() -> str | None:
    """Get the name of the current default network."""
    cache_dir = get_cache_dir()
    default_entry = get_default_network_entry(cache_dir)
    if default_entry is None:
        return None
    return default_entry[0]


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


def allocate_network_ip(network_name: str, vm_name: str) -> str:
    """Allocate the next available IP from a network's subnet.

    Registers the lease in metadata.

    Returns:
        The allocated IP address string.
    """
    config = get_network(network_name)
    if config is None:
        raise NetworkError(f"Network '{network_name}' not found")

    leases = get_network_leases(network_name)
    from mvmctl.core.network_manager import allocate_network_ip as _allocate_network_ip

    ip, updated_leases = _allocate_network_ip(config, leases, vm_name)

    # Persist updated leases to metadata
    cache_dir = get_cache_dir()
    metadata.update_network_entry(cache_dir, network_name, leases=leases_to_dicts(updated_leases))

    return ip


def release_network_ip(network_name: str, vm_name: str) -> None:
    """Release a VM's IP lease from a network."""
    leases = get_network_leases(network_name)
    from mvmctl.core.network_manager import release_network_ip as _release_network_ip

    updated_leases = _release_network_ip(leases, vm_name)

    # Persist updated leases to metadata
    cache_dir = get_cache_dir()
    metadata.update_network_entry(cache_dir, network_name, leases=leases_to_dicts(updated_leases))


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
    try:
        network_core.setup_bridge(config.bridge, ipv4_gateway_subnet=ipv4_gateway_subnet)
        if config.nat_enabled:
            network_core.setup_nat(
                config.bridge, nat_gateways=config.nat_gateways, subnet=config.subnet
            )
    except NetworkError:
        # Rollback on failure
        try:
            network_core.teardown_bridge(config.bridge)
        except NetworkError as e:
            logger.warning("Rollback: failed to tear down bridge: %s", e)
        raise

    # Persist to metadata
    cache_dir = get_cache_dir()
    entry = config_to_network_entry(config)
    entry["leases"] = []
    entry["bridge_active"] = True
    metadata.update_network_entry(cache_dir, name, **entry)

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
    try:
        if config.nat_enabled:
            network_core.teardown_nat(bridge=config.bridge, force=True, subnet=config.subnet)
        network_core.teardown_bridge(config.bridge)
    except NetworkError as e:
        logger.warning("Partial teardown for network '%s': %s", name, e)

    # Remove from metadata
    cache_dir = get_cache_dir()
    metadata.remove_network_entry(cache_dir, name)

    # Persist iptables rules if root
    if os.getuid() == 0:
        host_setup.save_iptables_rules()

    from mvmctl.utils.audit import log_audit

    log_audit("network.remove", f"name={name}")


def inspect_network(name: str) -> dict[str, Any]:
    """Return full details for a named network.

    Args:
        name: Network name to inspect.

    Returns:
        Dict with network details and attached VMs.

    Raises:
        NetworkError: If network not found.
    """
    from mvmctl.core.network_manager import VMLease
    from mvmctl.core.vm_manager import VMManager

    config = get_network(name)
    if config is None:
        raise NetworkError(f"Network '{name}' not found")

    leases = get_network_leases(name)
    active = bridge_exists(config.bridge)

    # Update bridge_active in metadata
    cache_dir = get_cache_dir()
    metadata.update_network_entry(cache_dir, name, bridge_active=active)

    vm_manager = VMManager()
    enriched_vms: list[VMLease] = []
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

    return {
        "name": config.name,
        "subnet": config.subnet,
        "ipv4_gateway": config.ipv4_gateway,
        "bridge": config.bridge,
        "nat_enabled": config.nat_enabled,
        "nat_gateways": config.nat_gateways,
        "created_at": config.created_at,
        "bridge_exists": active,
        "vms": enriched_vms,
    }


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
                cache_dir = get_cache_dir()
                metadata.update_network_entry(cache_dir, DEFAULT_NETWORK_NAME, bridge_active=True)
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


def reconcile_networks() -> list[dict[str, Any]]:
    """Compare stored network state with actual kernel bridge state.

    For each network in metadata, checks whether its bridge device still
    exists on the host. Updates bridge_active in metadata and
    returns a list of reconciliation results.

    Returns:
        List of dicts with reconciliation results.
    """
    from mvmctl.core.network_manager import ReconcileResult

    cache_dir = get_cache_dir()
    results: list[ReconcileResult] = []

    for config in list_networks():
        entry = metadata.get_network_entry(cache_dir, config.name)
        stored_active = entry.get("bridge_active")
        actual_active = bridge_exists(config.bridge)

        stale = (stored_active is True) and (not actual_active)

        metadata.update_network_entry(cache_dir, config.name, bridge_active=actual_active)

        results.append(
            ReconcileResult(
                name=config.name,
                bridge=config.bridge,
                stored_active=stored_active,
                actual_active=actual_active,
                stale=stale,
            )
        )

    if any(r.stale for r in results):
        stale_names = [r.name for r in results if r.stale]
        logger.warning("Stale networks detected (bridge missing): %s", ", ".join(stale_names))

    return [r.__dict__ for r in results]


def restore_networks() -> list[str]:
    """Restore all networks from metadata, recreating bridges and NAT rules.

    This function is called during host init to restore networks after a clean
    or reboot. It validates stored interfaces and recreates network resources.

    Returns:
        List of status messages describing what was restored.
    """
    networks = list_networks()
    if not networks:
        return []

    status: list[str] = []
    cache_dir = get_cache_dir()

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
                    metadata.update_network_entry(
                        cache_dir, config.name, nat_gateways=validated_gateways
                    )
                    config.nat_gateways = validated_gateways
            except NetworkError as e:
                status.append(f"Network '{config.name}': failed to configure NAT: {e}")

        metadata.update_network_entry(cache_dir, config.name, bridge_active=True)

    return status
