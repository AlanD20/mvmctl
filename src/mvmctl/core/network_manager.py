"""Named network management — create, persist, and query named networks."""

from __future__ import annotations

import ipaddress
import logging
import os
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from typing import Any, TypedDict

from mvmctl.constants import (
    DEFAULT_NETWORK_CIDR,
    DEFAULT_NETWORK_NAME,
    device_prefix,
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
from mvmctl.core.network import (
    allocate_ip,
    bridge_exists,
    setup_bridge,
    setup_nat,
    teardown_bridge,
    teardown_nat,
)
from mvmctl.exceptions import MVMError, NetworkError
from mvmctl.utils.fs import get_cache_dir
from mvmctl.utils.validation import (
    validate_bridge_name,
    validate_cidr,
    validate_entity_name,
    validate_interface_name,
    validate_ipv4_address,
)

logger = logging.getLogger(__name__)


@dataclass
class NetworkConfig:
    """Persistent configuration for a named network."""

    name: str
    cidr: str
    gateway: str
    bridge: str
    nat_enabled: bool = True
    nat_gateways: list[str] = field(default_factory=list)
    created_at: str = field(default_factory=lambda: datetime.now(tz=timezone.utc).isoformat())
    is_default: bool = False


@dataclass
class NetworkLease:
    """IP lease assignment for a VM within a network."""

    vm_id: str
    ipv4: str


def _bridge_name_for(network_name: str) -> str:
    prefix = device_prefix()
    truncated = network_name[:10]
    return f"{prefix}-{truncated}"


def _gateway_for_subnet(subnet: str) -> str:
    """Return the first usable host IP in a subnet as the gateway."""
    net = ipaddress.IPv4Network(subnet, strict=False)
    return str(next(iter(net.hosts())))


# ---------------------------------------------------------------------------
# Metadata helpers
# ---------------------------------------------------------------------------


def _network_entry_to_config(name: str, entry: dict[str, Any]) -> NetworkConfig | None:
    """Convert a metadata entry to NetworkConfig, returns None if essential fields missing or invalid.

    Validates all fields from metadata to prevent injection attacks:
    - name: validated via validate_entity_name()
    - bridge: validated via validate_bridge_name()
    - nat_gateways: validated via validate_nat_gateways()
    - cidr: validated via validate_cidr()
    - gateway: validated via validate_ipv4_address()

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

    # Extract and validate CIDR
    cidr = entry.get("cidr")
    if not isinstance(cidr, str):
        logger.warning("Invalid CIDR in metadata for network '%s': not a string", name)
        return None
    try:
        cidr = validate_cidr(cidr)
    except MVMError as e:
        logger.warning("Invalid CIDR in metadata for network '%s': %s", name, e)
        return None

    # Extract and validate gateway
    gateway = entry.get("gateway")
    if not isinstance(gateway, str):
        logger.warning("Invalid gateway in metadata for network '%s': not a string", name)
        return None
    try:
        gateway = validate_ipv4_address(gateway)
    except MVMError as e:
        logger.warning("Invalid gateway in metadata for network '%s': %s", name, e)
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
        cidr=cidr,
        gateway=gateway,
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
                    cidr=network.subnet,
                    gateway=network.ipv4_gateway,
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
                cidr=network.subnet,
                gateway=network.ipv4_gateway,
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
    # Try SQLite first
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(name)
        if network:
            db_leases = db.list_leases(network.id)
            if db_leases:
                return [
                    NetworkLease(
                        vm_id=lease.vm_id or "",
                        ipv4=lease.ipv4,
                    )
                    for lease in db_leases
                ]
    except Exception:
        pass

    # Fall back to JSON
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

    # NEW: Also update SQLite
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(name)
        if network:
            db.set_default_network(network.id)
    except Exception:
        pass


def _persist_iptables_if_root() -> None:
    if os.getuid() != 0:
        return
    from mvmctl.core.host_setup import save_iptables_rules

    save_iptables_rules()


def create_network(
    name: str,
    cidr: str,
    gateway: str | None = None,
    nat: bool = True,
    nat_gateways: list[str] | None = None,
) -> NetworkConfig:
    """Create a named network.

    Sets up the bridge device, IP range, and optionally NAT rules.
    The network configuration is persisted to metadata.json.

    Args:
        name: Network name.
        cidr: IP subnet in CIDR notation (e.g., "192.168.100.0/24").
        gateway: Gateway IP for the bridge. Defaults to first host in subnet.
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

    _validate_subnet_no_overlap(cidr, name)

    if gateway is None:
        gateway = _gateway_for_subnet(cidr)

    bridge = _bridge_name_for(name)

    existing_with_bridge = [n for n in list_networks() if n.bridge == bridge]
    if existing_with_bridge:
        raise NetworkError(
            f"Bridge name '{bridge}' conflicts with network '{existing_with_bridge[0].name}'"
        )

    config = NetworkConfig(
        name=name,
        cidr=cidr,
        gateway=gateway,
        bridge=bridge,
        nat_enabled=nat,
        nat_gateways=nat_gateways or [],
    )

    try:
        setup_bridge(bridge, gateway_cidr=f"{gateway}/{_prefix_len(cidr)}")
        if nat:
            setup_nat(bridge, nat_gateways=nat_gateways, cidr=cidr)
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
        cidr=cidr,
        gateway=config.gateway,
        bridge=config.bridge,
        nat_enabled=config.nat_enabled,
        nat_gateways=config.nat_gateways,
        created_at=config.created_at,
        leases=[],
        bridge_active=True,
    )

    # NEW: Also write to SQLite
    try:
        db = MVMDatabase()
        import hashlib

        from mvmctl.db.models import Network as DBNetwork

        network_id = hashlib.sha256(name.encode()).hexdigest()
        db_network = DBNetwork(
            id=network_id,
            name=name,
            subnet=cidr,
            bridge=config.bridge,
            ipv4_gateway=config.gateway,
            bridge_active=True,
            nat_gateways=",".join(config.nat_gateways) if config.nat_gateways else None,
            nat_enabled=config.nat_enabled,
            is_default=False,
            created_at=config.created_at,
            updated_at=None,
        )
        db.upsert_network(db_network)
    except Exception:
        pass

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
            teardown_nat(bridge=config.bridge, force=True, cidr=config.cidr)
        teardown_bridge(config.bridge)
    except NetworkError as e:
        logger.warning("Partial teardown for network '%s': %s", name, e)

    # Remove from metadata.json
    cache_dir = get_cache_dir()
    remove_network_entry(cache_dir, name)

    # NEW: Also delete from SQLite
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(name)
        if network:
            db.delete_network(network.id)
    except Exception:
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
    cidr: str
    gateway: str
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

    # Update bridge_active in metadata
    cache_dir = get_cache_dir()
    update_network_entry(cache_dir, name, bridge_active=active)

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
        "cidr": config.cidr,
        "gateway": config.gateway,
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
    used_ips.append(config.gateway)

    ip = allocate_ip(used_ips, subnet=config.cidr)
    leases.append(NetworkLease(vm_id=vm_name, ipv4=ip))

    # Persist updated leases to metadata
    cache_dir = get_cache_dir()
    update_network_entry(cache_dir, network_name, leases=[asdict(lease) for lease in leases])

    # NEW: Also write to SQLite
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(network_name)
        if network:
            db.acquire_lease(network.id, ip, vm_name)
    except Exception:
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

    # NEW: Also release from SQLite
    if released_ip:
        try:
            db = MVMDatabase()
            network = db.get_network_by_name(network_name)
            if network:
                db.release_lease(network.id, released_ip)
        except Exception:
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
        _iptables_rule_exists,
        bridge_exists,
        get_default_interface,
        setup_bridge,
        setup_mvm_chains,
        setup_nat,
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
                        config.cidr,
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
            gateway_cidr = f"{config.gateway}/{_prefix_len(config.cidr)}"
            try:
                if bridge_missing:
                    setup_bridge(config.bridge, gateway_cidr=gateway_cidr)
                if config.nat_enabled:
                    nat_gateways = config.nat_gateways or [get_default_interface()]
                    setup_nat(config.bridge, nat_gateways=nat_gateways, cidr=config.cidr)
                    if os.getuid() == 0:
                        save_iptables_rules()
                cache_dir = get_cache_dir()
                update_network_entry(cache_dir, DEFAULT_NETWORK_NAME, bridge_active=True)

                # NEW: Also update SQLite
                try:
                    db = MVMDatabase()
                    network = db.get_network_by_name(DEFAULT_NETWORK_NAME)
                    if network:
                        db.update_network_bridge_active(network.id, True)
                except Exception:
                    pass
            except NetworkError:
                if bridge_missing:
                    try:
                        teardown_bridge(config.bridge)
                    except NetworkError:
                        pass
                raise
        return config

    # Auto-detect internet-facing interface for NAT gateway
    default_iface = get_default_interface()
    if not default_iface:
        raise NetworkError(
            "Could not auto-detect internet-facing interface. "
            "Please create the default network manually with: "
            "mvm network create default --nat-gateways <interface>"
        )
    return create_network(
        DEFAULT_NETWORK_NAME, cidr=DEFAULT_NETWORK_CIDR, nat=True, nat_gateways=[default_iface]
    )


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

        # Update bridge_active in metadata
        update_network_entry(cache_dir, config.name, bridge_active=actual_active)

        # NEW: Also update SQLite
        try:
            db = MVMDatabase()
            network = db.get_network_by_name(config.name)
            if network:
                db.update_network_bridge_active(network.id, actual_active)
        except Exception:
            pass

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
        bridge_exists,
        get_default_interface,
        setup_bridge,
        setup_nat,
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

        gateway_cidr = f"{config.gateway}/{_prefix_len(config.cidr)}"

        try:
            setup_bridge(config.bridge, gateway_cidr=gateway_cidr)
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
                setup_nat(config.bridge, nat_gateways=validated_gateways, cidr=config.cidr)
                status.append(
                    f"Network '{config.name}': NAT configured via {', '.join(validated_gateways)}"
                )

                if config.nat_gateways != validated_gateways:
                    update_network_entry(cache_dir, config.name, nat_gateways=validated_gateways)
            except NetworkError as e:
                status.append(f"Network '{config.name}': failed to configure NAT: {e}")

        update_network_entry(cache_dir, config.name, bridge_active=True)

    return status


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _prefix_len(subnet: str) -> int:
    return ipaddress.IPv4Network(subnet, strict=False).prefixlen


def _validate_subnet_no_overlap(subnet: str, exclude_name: str = "") -> None:
    """Check that the given subnet doesn't overlap with existing networks."""
    new_net = ipaddress.IPv4Network(subnet, strict=False)
    for existing in list_networks():
        if existing.name == exclude_name:
            continue
        existing_net = ipaddress.IPv4Network(existing.cidr, strict=False)
        if new_net.overlaps(existing_net):
            raise NetworkError(
                f"Subnet {subnet} overlaps with network '{existing.name}' ({existing.cidr})"
            )
