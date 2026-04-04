"""Named network management — create, persist, and query named networks."""

from __future__ import annotations

import ipaddress
import logging
import os
import sqlite3
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from typing import Any, TypedDict

from mvmctl.constants import (
    DEFAULT_NETWORK_NAME,
    DEFAULT_NETWORK_SUBNET,
)
from mvmctl.core.metadata import (
    get_default_network_entry,
    get_network_entry,
    list_network_entries,
    remove_network_entry,
    set_default_network_entry,
    update_network_entry,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.network import setup_bridge, setup_nat, teardown_bridge, teardown_nat
from mvmctl.exceptions import MVMError, NetworkError
from mvmctl.models.network import NetworkConfig as NetworkConfig
from mvmctl.models.network import NetworkLease as NetworkLease
from mvmctl.utils.fs import get_cache_dir
from mvmctl.utils.full_hash import generate_full_hash_network
from mvmctl.utils.network import allocate_ip, bridge_exists
from mvmctl.utils.network import (
    bridge_name_for as _bridge_name_for_util,
)
from mvmctl.utils.network import (
    ipv4_gateway_for_subnet as _ipv4_gateway_for_subnet_util,
)
from mvmctl.utils.network import (
    prefix_len_from_subnet as _prefix_len_util,
)
from mvmctl.utils.validation import (
    validate_bridge_name,
    validate_entity_name,
    validate_interface_name,
    validate_ipv4_address,
    validate_subnet,
)

logger = logging.getLogger(__name__)


def _mark_default_network_created_in_db() -> None:
    try:
        db = MVMDatabase()
        db.initialize_host_state()
        db.update_host_component("default_network_created", True)
    except (MVMError, sqlite3.OperationalError):
        pass


def _upsert_network_to_sqlite(config: NetworkConfig, bridge_active: bool | None = None) -> None:
    """Write a NetworkConfig to SQLite (creates or updates the row)."""
    from mvmctl.db.models import Network as DBNetwork

    now = datetime.now(timezone.utc).isoformat()
    network_id = generate_full_hash_network(config.name, config.subnet, config.created_at or now)
    db = MVMDatabase()
    try:
        db.upsert_network(
            DBNetwork(
                id=network_id,
                name=config.name,
                subnet=config.subnet,
                bridge=config.bridge,
                ipv4_gateway=config.ipv4_gateway,
                bridge_active=bridge_active if bridge_active is not None else False,
                nat_gateways=",".join(config.nat_gateways) if config.nat_gateways else None,
                nat_enabled=config.nat_enabled,
                is_default=config.is_default,
                created_at=config.created_at or now,
                updated_at=now,
            )
        )
    except sqlite3.OperationalError:
        pass


def _bridge_name_for(network_name: str) -> str:
    return _bridge_name_for_util(network_name)


def _ipv4_gateway_for_subnet(subnet: str) -> str:
    return _ipv4_gateway_for_subnet_util(subnet)


# ---------------------------------------------------------------------------
# Metadata helpers
# ---------------------------------------------------------------------------


def _network_entry_to_config(name: str, entry: dict[str, Any]) -> NetworkConfig | None:
    """Convert a metadata entry to NetworkConfig, returns None if essential fields missing or invalid.

    Validates all fields from metadata to prevent injection attacks:
    - name: validated via validate_entity_name()
    - bridge: validated via validate_bridge_name()
    - nat_gateways: validated via validate_nat_gateways()
    - subnet: validated via validate_subnet()
    - ipv4_gateway: validated via validate_ipv4_address()

    Invalid entries are logged as warnings and skipped.
    """
    if not entry:
        return None

    # Validate network name
    try:
        name = validate_entity_name(name, "network")
    except MVMError as e:
        logger.warning("Invalid network name in metadata: %s", e)
        return None

    # Extract and validate subent
    subnet = entry.get("subnet")
    if not isinstance(subnet, str):
        logger.warning("Invalid SUBNET in metadata for network '%s': not a string", name)
        return None
    try:
        subnet = validate_subnet(subnet)
    except MVMError as e:
        logger.warning("Invalid SUBNET in metadata for network '%s': %s", name, e)
        return None

    # Extract and validate ipv4_gateway
    ipv4_gateway = entry.get("ipv4_gateway")
    if not isinstance(ipv4_gateway, str):
        logger.warning("Invalid ipv4_gateway in metadata for network '%s': not a string", name)
        return None
    try:
        ipv4_gateway = validate_ipv4_address(ipv4_gateway)
    except MVMError as e:
        logger.warning("Invalid ipv4_gateway in metadata for network '%s': %s", name, e)
        return None

    # Extract and validate bridge
    bridge = entry.get("bridge")
    if not isinstance(bridge, str):
        logger.warning("Invalid bridge in metadata for network '%s': not a string", name)
        return None
    try:
        bridge = validate_bridge_name(bridge)
    except MVMError as e:
        logger.warning("Invalid bridge name in metadata for network '%s': %s", name, e)
        return None

    # Extract and validate nat_gateways if present
    nat_gateways: list[str] = []
    raw_nat_gateways = entry.get("nat_gateways")
    if raw_nat_gateways is not None:
        if isinstance(raw_nat_gateways, list):
            for iface in raw_nat_gateways:
                if isinstance(iface, str):
                    try:
                        validated_iface = validate_interface_name(iface)
                        nat_gateways.append(validated_iface)
                    except MVMError as e:
                        logger.warning(
                            "Invalid NAT gateway in metadata for network '%s': %s", name, e
                        )
                else:
                    logger.warning(
                        "Invalid NAT gateway in metadata for network '%s': not a string", name
                    )
        else:
            logger.warning("Invalid nat_gateways in metadata for network '%s': not a list", name)

    return NetworkConfig(
        name=name,
        subnet=subnet,
        ipv4_gateway=ipv4_gateway,
        bridge=bridge,
        nat_enabled=entry.get("nat_enabled", True),
        nat_gateways=nat_gateways,
        created_at=entry.get("created_at", ""),
        is_default=entry.get("is_default", 0) == 1,
    )


def _leases_from_entry(entry: dict[str, Any]) -> list[NetworkLease]:
    """Extract leases from a metadata entry."""
    raw_leases = entry.get("leases", [])
    if not isinstance(raw_leases, list):
        return []
    leases = []
    for item in raw_leases:
        if isinstance(item, dict) and "vm_id" in item and "ipv4" in item:
            leases.append(NetworkLease(vm_id=item["vm_id"], ipv4=item["ipv4"]))
    return leases


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------


def list_networks() -> list[NetworkConfig]:
    """List all configured networks with their metadata.

    Returns:
        List of NetworkConfig objects with is_default populated from metadata
    """
    # Try SQLite first
    try:
        db = MVMDatabase()
        db_networks = db.list_networks()
        if db_networks:
            configs: list[NetworkConfig] = []
            for network in db_networks:
                config = NetworkConfig(
                    name=network.name,
                    subnet=network.subnet,
                    ipv4_gateway=network.ipv4_gateway,
                    bridge=network.bridge,
                    nat_enabled=network.nat_enabled,
                    nat_gateways=network.nat_gateways.split(",") if network.nat_gateways else [],
                    created_at=network.created_at or "",
                    is_default=network.is_default,
                )
                configs.append(config)
            return sorted(configs, key=lambda c: c.name)
    except Exception:
        pass

    # Fall back to JSON
    cache_dir = get_cache_dir()
    entries = list_network_entries(cache_dir)
    if not entries:
        return []

    default_entry = get_default_network_entry(cache_dir)
    default_name = default_entry[0] if default_entry else None

    json_configs: list[NetworkConfig] = []
    for name, entry in entries.items():
        json_config = _network_entry_to_config(name, entry)
        if json_config is not None:
            json_config.is_default = name == default_name
            json_configs.append(json_config)

    return sorted(json_configs, key=lambda c: c.name)


def get_network(name: str) -> NetworkConfig | None:
    """Get a named network by name."""
    # Try SQLite first
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(name)
        if network:
            return NetworkConfig(
                name=network.name,
                subnet=network.subnet,
                ipv4_gateway=network.ipv4_gateway,
                bridge=network.bridge,
                nat_enabled=network.nat_enabled,
                nat_gateways=network.nat_gateways.split(",") if network.nat_gateways else [],
                created_at=network.created_at or "",
                is_default=network.is_default,
            )
    except Exception:
        pass

    # Fall back to JSON
    cache_dir = get_cache_dir()
    entry = get_network_entry(cache_dir, name)
    return _network_entry_to_config(name, entry)


def get_network_leases(name: str) -> list[NetworkLease]:
    """Get all IP leases for a network."""
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(name)
        if network:
            db_leases = db.list_leases(network.id)
            return [NetworkLease(vm_id=lease.vm_id or "", ipv4=lease.ipv4) for lease in db_leases]
    except Exception:
        pass

    cache_dir = get_cache_dir()
    entry = get_network_entry(cache_dir, name)
    return _leases_from_entry(entry)


def check_ip_available(network_name: str, ip: str) -> None:
    """Check if an IP is available for use in a network.

    Args:
        network_name: Name of the network to check
        ip: IP address to verify availability

    Raises:
        NetworkError: If the IP is already leased to another VM
    """
    leases = get_network_leases(network_name)
    for lease in leases:
        if lease.ipv4 == ip:
            raise NetworkError(f"IP {ip} is already in use by VM '{lease.vm_id}'")


def is_ip_available(network_name: str, ip: str) -> bool:
    """Check if an IP address is available for use in a network.

    Args:
        network_name: Name of the network to check
        ip: IP address to verify availability

    Returns:
        True if the IP is not currently leased, False otherwise
    """
    leases = get_network_leases(network_name)
    for lease in leases:
        if lease.ipv4 == ip:
            return False
    return True


def set_default_network(name: str) -> None:
    """Set a network as the default for VM creation.

    Args:
        name: Network name to set as default

    Raises:
        NetworkError: If network does not exist
    """
    config = get_network(name)
    if config is None:
        raise NetworkError(f"Network '{name}' does not exist")

    cache_dir = get_cache_dir()
    set_default_network_entry(cache_dir, name)

    try:
        db = MVMDatabase()
        network = db.get_network_by_name(name)
        if network:
            db.set_default_network(network.id)
    except sqlite3.OperationalError:
        pass


def _should_preserve_current_default(name: str) -> bool:
    try:
        db = MVMDatabase()
        current_default = db.get_default_network()
    except sqlite3.OperationalError:
        return False

    return current_default is not None and current_default.name != name


def _persist_iptables_if_root() -> None:
    if os.getuid() != 0:
        return
    from mvmctl.core.host_setup import save_iptables_rules

    save_iptables_rules()


def create_network(
    name: str,
    subnet: str,
    ipv4_gateway: str | None = None,
    nat: bool = True,
    nat_gateways: list[str] | None = None,
) -> NetworkConfig:
    """Create a named network.

    Sets up the bridge device, IP range, and optionally NAT rules.
    The network configuration is persisted to metadata.json.

    Args:
        name: Network name.
        subnet: IP subnet in SUBNET notation (e.g., "192.168.100.0/24").
        ipv4_gateway: Gateway IPv4 for the bridge. Defaults to first host in subnet.
        nat: Whether to configure NAT/masquerade. Default True.
        nat_gateways: Physical interfaces for NAT (auto-detected if not provided).

    Returns:
        The created NetworkConfig.

    Raises:
        NetworkError: If the network already exists or setup fails.
    """
    validate_entity_name(name, "network")

    if get_network(name) is not None:
        raise NetworkError(f"Network '{name}' already exists")

    _validate_subnet_no_overlap(subnet, name)

    if ipv4_gateway is None:
        ipv4_gateway = _ipv4_gateway_for_subnet(subnet)

    bridge = _bridge_name_for(name)

    existing_with_bridge = [n for n in list_networks() if n.bridge == bridge]
    if existing_with_bridge:
        raise NetworkError(
            f"Bridge name '{bridge}' conflicts with network '{existing_with_bridge[0].name}'"
        )

    config = NetworkConfig(
        name=name,
        subnet=subnet,
        ipv4_gateway=ipv4_gateway,
        bridge=bridge,
        nat_enabled=nat,
        nat_gateways=nat_gateways or [],
    )

    try:
        setup_bridge(bridge, ipv4_gateway_subnet=f"{ipv4_gateway}/{_prefix_len(subnet)}")
        if nat:
            setup_nat(bridge, nat_gateways=nat_gateways, subnet=subnet)
    except NetworkError:
        try:
            teardown_bridge(bridge)
        except NetworkError as e:
            logger.warning("Rollback: failed to tear down bridge: %s", e)
        raise

    cache_dir = get_cache_dir()
    update_network_entry(
        cache_dir,
        name,
        subnet=subnet,
        gateway=config.ipv4_gateway,
        bridge=config.bridge,
        nat_enabled=config.nat_enabled,
        nat_gateways=config.nat_gateways,
        created_at=config.created_at,
        leases=[],
        bridge_active=True,
    )

    _upsert_network_to_sqlite(config, bridge_active=True)
    _persist_iptables_if_root()

    return config


def remove_network(name: str) -> None:
    """Remove a named network.

    Tears down the bridge and NAT rules, then removes metadata entry.

    Raises:
        NetworkError: If the network has VMs attached or doesn't exist.
    """
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

    leases = get_network_leases(name)
    if leases:
        vm_names = ", ".join(lease.vm_id for lease in leases)
        raise NetworkError(
            f"Network '{name}' still has VMs attached: {vm_names}. Remove those VMs first."
        )

    # Teardown host resources
    try:
        if config.nat_enabled:
            teardown_nat(bridge=config.bridge, force=True, subnet=config.subnet)
        teardown_bridge(config.bridge)
    except NetworkError as e:
        logger.warning("Partial teardown for network '%s': %s", name, e)

    # Remove from metadata.json
    cache_dir = get_cache_dir()
    remove_network_entry(cache_dir, name)

    try:
        db = MVMDatabase()
        network = db.get_network_by_name(name)
        if network:
            db.delete_network(network.id)
    except sqlite3.OperationalError:
        pass

    _persist_iptables_if_root()


class _VMLease(TypedDict):
    vm_id: str
    ipv4: str
    status: str
    pid: int | None
    api_socket_path: str | None


class NetworkInspect(TypedDict):
    name: str
    subnet: str
    ipv4_gateway: str
    bridge: str
    nat_enabled: bool
    nat_gateways: list[str]
    created_at: str
    bridge_exists: bool
    vms: list[_VMLease]


def inspect_network(name: str) -> NetworkInspect:
    """Return full details for a named network."""
    from mvmctl.core.vm_manager import VMManager

    config = get_network(name)
    if config is None:
        raise NetworkError(f"Network '{name}' not found")

    leases = get_network_leases(name)
    active = bridge_exists(config.bridge)

    cache_dir = get_cache_dir()
    update_network_entry(cache_dir, name, bridge_active=active)
    _upsert_network_to_sqlite(config, bridge_active=active)

    vm_manager = VMManager()
    enriched_vms: list[_VMLease] = []
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


def allocate_network_ip(network_name: str, vm_name: str) -> str:
    """Allocate the next available IP from a network's subnet.

    Registers the lease in metadata.json.

    Returns:
        The allocated IP address string.
    """
    config = get_network(network_name)
    if config is None:
        raise NetworkError(f"Network '{network_name}' not found")

    leases = get_network_leases(network_name)
    used_ips = [lease.ipv4 for lease in leases]
    # Also reserve the gateway
    used_ips.append(config.ipv4_gateway)

    ip = allocate_ip(used_ips, subnet=config.subnet)
    leases.append(NetworkLease(vm_id=vm_name, ipv4=ip))

    # Persist updated leases to metadata
    cache_dir = get_cache_dir()
    update_network_entry(cache_dir, network_name, leases=[asdict(lease) for lease in leases])

    _upsert_network_to_sqlite(config)
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(network_name)
        if network:
            vm = db.get_vm_by_name(vm_name)
            db.acquire_lease(network.id, ip, vm.id if vm else None)
    except sqlite3.OperationalError:
        pass

    return ip


def release_network_ip(network_name: str, vm_name: str) -> None:
    """Release a VM's IP lease from a network."""
    leases = get_network_leases(network_name)
    released_ip = None
    for lease in leases:
        if lease.vm_id == vm_name:
            released_ip = lease.ipv4
            break

    leases = [lease for lease in leases if lease.vm_id != vm_name]

    # Persist updated leases to metadata
    cache_dir = get_cache_dir()
    update_network_entry(cache_dir, network_name, leases=[asdict(lease) for lease in leases])

    if released_ip:
        try:
            db = MVMDatabase()
            network = db.get_network_by_name(network_name)
            if network:
                db.release_lease(network.id, released_ip)
        except sqlite3.OperationalError:
            pass


def ensure_default_network() -> NetworkConfig:
    """Ensure the default network exists with all host resources materialized.

    If the network exists in metadata but the actual bridge, iptables chains,
    or NAT rules are missing (e.g., after a reboot), this function recreates
    them from the stored configuration.
    """
    from mvmctl.constants import MVM_POSTROUTING_CHAIN
    from mvmctl.core.host_setup import save_iptables_rules
    from mvmctl.core.network import (
        setup_bridge,
        setup_mvm_chains,
        setup_nat,
    )
    from mvmctl.utils.network import (
        _iptables_rule_exists,
        bridge_exists,
        get_default_interface,
    )

    config = get_network(DEFAULT_NETWORK_NAME)

    if config is not None:
        bridge_missing = not bridge_exists(config.bridge)
        chains_missing = not setup_mvm_chains()

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
            ipv4_gateway_subnet = f"{config.ipv4_gateway}/{_prefix_len(config.subnet)}"
            try:
                if bridge_missing:
                    setup_bridge(config.bridge, ipv4_gateway_subnet=ipv4_gateway_subnet)
                if config.nat_enabled:
                    nat_gateways = config.nat_gateways or [get_default_interface()]
                    setup_nat(config.bridge, nat_gateways=nat_gateways, subnet=config.subnet)
                    if os.getuid() == 0:
                        save_iptables_rules()
                cache_dir = get_cache_dir()
                update_network_entry(cache_dir, DEFAULT_NETWORK_NAME, bridge_active=True)
                _upsert_network_to_sqlite(config, bridge_active=True)
                _mark_default_network_created_in_db()
                if not _should_preserve_current_default(DEFAULT_NETWORK_NAME):
                    set_default_network(DEFAULT_NETWORK_NAME)
            except NetworkError:
                if bridge_missing:
                    try:
                        teardown_bridge(config.bridge)
                    except NetworkError:
                        pass
                raise
        else:
            _mark_default_network_created_in_db()
            if not _should_preserve_current_default(DEFAULT_NETWORK_NAME):
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
    if not _should_preserve_current_default(DEFAULT_NETWORK_NAME):
        set_default_network(DEFAULT_NETWORK_NAME)
    return config


@dataclass
class ReconcileResult:
    """Result of reconciling stored network state against kernel state."""

    name: str
    bridge: str
    stored_active: bool | None
    actual_active: bool
    stale: bool


def reconcile_networks() -> list[ReconcileResult]:
    """Compare stored network state with actual kernel bridge state.

    For each network in metadata, checks whether its bridge device still
    exists on the host. Updates bridge_active in metadata.json and
    returns a list of reconciliation results. Entries where
    ``stale is True`` indicate that the bridge was expected to be up
    but is no longer present in the kernel.
    """
    cache_dir = get_cache_dir()
    results: list[ReconcileResult] = []

    for config in list_networks():
        entry = get_network_entry(cache_dir, config.name)
        stored_active = entry.get("bridge_active")
        actual_active = bridge_exists(config.bridge)

        stale = (stored_active is True) and (not actual_active)

        update_network_entry(cache_dir, config.name, bridge_active=actual_active)
        _upsert_network_to_sqlite(config, bridge_active=actual_active)

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

    return results


def restore_networks() -> list[str]:
    """Restore all networks from metadata, recreating bridges and NAT rules.

    This function is called during host init to restore networks after a clean
    or reboot. It validates stored interfaces and recreates network resources.

    Returns:
        List of status messages describing what was restored.
    """
    from mvmctl.core.network import (
        setup_bridge,
        setup_nat,
    )
    from mvmctl.utils.network import (
        bridge_exists,
        get_default_interface,
        validate_network_interface,
    )

    networks = list_networks()
    if not networks:
        return []

    status: list[str] = []
    cache_dir = get_cache_dir()

    for config in networks:
        if bridge_exists(config.bridge):
            status.append(f"Network '{config.name}': bridge already exists, skipping")
            continue

        ipv4_gateway_subnet = f"{config.ipv4_gateway}/{_prefix_len(config.subnet)}"

        try:
            setup_bridge(config.bridge, ipv4_gateway_subnet=ipv4_gateway_subnet)
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
                setup_nat(config.bridge, nat_gateways=validated_gateways, subnet=config.subnet)
                status.append(
                    f"Network '{config.name}': NAT configured via {', '.join(validated_gateways)}"
                )

                if config.nat_gateways != validated_gateways:
                    update_network_entry(cache_dir, config.name, nat_gateways=validated_gateways)
                    config.nat_gateways = validated_gateways
            except NetworkError as e:
                status.append(f"Network '{config.name}': failed to configure NAT: {e}")

        update_network_entry(cache_dir, config.name, bridge_active=True)
        _upsert_network_to_sqlite(config, bridge_active=True)

    return status


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _prefix_len(subnet: str) -> int:
    return _prefix_len_util(subnet)


def _validate_subnet_no_overlap(subnet: str, exclude_name: str = "") -> None:
    """Check that the given subnet doesn't overlap with existing networks."""
    new_net = ipaddress.IPv4Network(subnet, strict=False)
    for existing in list_networks():
        if existing.name == exclude_name:
            continue
        existing_net = ipaddress.IPv4Network(existing.subnet, strict=False)
        if new_net.overlaps(existing_net):
            raise NetworkError(
                f"Subnet {subnet} overlaps with network '{existing.name}' ({existing.subnet})"
            )
