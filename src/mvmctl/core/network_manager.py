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
from mvmctl.core.network import (
    allocate_ip,
    bridge_exists,
    setup_bridge,
    setup_nat,
    teardown_bridge,
    teardown_nat,
)
from mvmctl.exceptions import NetworkError
from mvmctl.utils.fs import get_cache_dir
from mvmctl.utils.validation import validate_entity_name

logger = logging.getLogger(__name__)


@dataclass
class NetworkConfig:
    """Persistent configuration for a named network."""

    name: str
    cidr: str
    gateway: str
    bridge: str
    nat_enabled: bool = True
    created_at: str = field(default_factory=lambda: datetime.now(tz=timezone.utc).isoformat())
    is_default: bool = False


@dataclass
class NetworkLease:
    """IP lease assignment for a VM within a network."""

    vm_name: str
    ip: str


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
    """Convert a metadata entry to NetworkConfig, returns None if essential fields missing."""
    if not entry:
        return None
    # Required fields
    cidr = entry.get("cidr")
    gateway = entry.get("gateway")
    bridge = entry.get("bridge")
    if not isinstance(cidr, str) or not isinstance(gateway, str) or not isinstance(bridge, str):
        return None
    return NetworkConfig(
        name=name,
        cidr=cidr,
        gateway=gateway,
        bridge=bridge,
        nat_enabled=entry.get("nat_enabled", True),
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
        if isinstance(item, dict) and "vm_name" in item and "ip" in item:
            leases.append(NetworkLease(vm_name=item["vm_name"], ip=item["ip"]))
    return leases


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------


def list_networks() -> list[NetworkConfig]:
    """List all configured networks with their metadata.

    Returns:
        List of NetworkConfig objects with is_default populated from metadata
    """
    cache_dir = get_cache_dir()
    entries = list_network_entries(cache_dir)
    if not entries:
        return []

    default_entry = get_default_network_entry(cache_dir)
    default_name = default_entry[0] if default_entry else None

    configs: list[NetworkConfig] = []
    for name, entry in entries.items():
        config = _network_entry_to_config(name, entry)
        if config is not None:
            config.is_default = name == default_name
            configs.append(config)

    return sorted(configs, key=lambda c: c.name)


def get_network(name: str) -> NetworkConfig | None:
    """Get a named network by name."""
    cache_dir = get_cache_dir()
    entry = get_network_entry(cache_dir, name)
    return _network_entry_to_config(name, entry)


def get_network_leases(name: str) -> list[NetworkLease]:
    """Get all IP leases for a network."""
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
        if lease.ip == ip:
            raise NetworkError(f"IP {ip} is already in use by VM '{lease.vm_name}'")


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
        if lease.ip == ip:
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
    internet_iface: str | None = None,
) -> NetworkConfig:
    """Create a named network.

    Sets up the bridge device, IP range, and optionally NAT rules.
    The network configuration is persisted to metadata.json.

    Args:
        name: Network name.
        cidr: IP subnet in CIDR notation (e.g., "192.168.100.0/24").
        gateway: Gateway IP for the bridge. Defaults to first host in subnet.
        nat: Whether to configure NAT/masquerade. Default True.
        internet_iface: Physical interface for NAT (auto-detected if not provided).

    Returns:
        The created NetworkConfig.

    Raises:
        NetworkError: If the network already exists or setup fails.
    """
    validate_entity_name(name, "network")

    # Check if network already exists
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
    )

    # Create host-level resources
    try:
        setup_bridge(bridge, gateway_cidr=f"{gateway}/{_prefix_len(cidr)}")
        if nat:
            setup_nat(bridge, internet_iface=internet_iface)
    except NetworkError:
        # Best-effort cleanup on failure
        try:
            teardown_bridge(bridge)
        except NetworkError as e:
            logger.warning("Rollback: failed to tear down bridge: %s", e)
        raise

    # Persist to metadata.json
    cache_dir = get_cache_dir()
    update_network_entry(
        cache_dir,
        name,
        cidr=cidr,
        gateway=config.gateway,
        bridge=config.bridge,
        nat_enabled=config.nat_enabled,
        created_at=config.created_at,
        leases=[],
        bridge_active=True,
    )

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
        vm_names = ", ".join(lease.vm_name for lease in leases)
        raise NetworkError(
            f"Network '{name}' still has VMs attached: {vm_names}. Remove those VMs first."
        )

    # Teardown host resources
    try:
        if config.nat_enabled:
            teardown_nat(bridge=config.bridge, force=True)
        teardown_bridge(config.bridge)
    except NetworkError as e:
        logger.warning("Partial teardown for network '%s': %s", name, e)

    # Remove from metadata.json
    cache_dir = get_cache_dir()
    remove_network_entry(cache_dir, name)

    _persist_iptables_if_root()


class _VMLease(TypedDict):
    vm_name: str
    ip: str
    status: str
    pid: int | None
    socket_path: str | None


class NetworkInspect(TypedDict):
    name: str
    cidr: str
    gateway: str
    bridge: str
    nat_enabled: bool
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
        vm = vm_manager.get(lease.vm_name)
        if vm is not None:
            enriched_vms.append(
                {
                    "vm_name": lease.vm_name,
                    "ip": lease.ip,
                    "status": vm.status.value,
                    "pid": vm.pid,
                    "socket_path": str(vm.socket_path) if vm.socket_path else None,
                }
            )
        else:
            enriched_vms.append(
                {
                    "vm_name": lease.vm_name,
                    "ip": lease.ip,
                    "status": "unknown",
                    "pid": None,
                    "socket_path": None,
                }
            )

    return {
        "name": config.name,
        "cidr": config.cidr,
        "gateway": config.gateway,
        "bridge": config.bridge,
        "nat_enabled": config.nat_enabled,
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
    used_ips = [lease.ip for lease in leases]
    # Also reserve the gateway
    used_ips.append(config.gateway)

    ip = allocate_ip(used_ips, subnet=config.cidr)
    leases.append(NetworkLease(vm_name=vm_name, ip=ip))

    # Persist updated leases to metadata
    cache_dir = get_cache_dir()
    update_network_entry(cache_dir, network_name, leases=[asdict(lease) for lease in leases])

    return ip


def release_network_ip(network_name: str, vm_name: str) -> None:
    """Release a VM's IP lease from a network."""
    leases = get_network_leases(network_name)
    leases = [lease for lease in leases if lease.vm_name != vm_name]

    # Persist updated leases to metadata
    cache_dir = get_cache_dir()
    update_network_entry(cache_dir, network_name, leases=[asdict(lease) for lease in leases])


def ensure_default_network() -> NetworkConfig:
    """Ensure the default network exists with all host resources materialized.

    If the network exists in metadata but the actual bridge, iptables chains,
    or NAT rules are missing (e.g., after a reboot), this function recreates
    them from the stored configuration.
    """
    from mvmctl.core.host_setup import save_iptables_rules
    from mvmctl.core.network import bridge_exists, setup_bridge, setup_mvm_chains, setup_nat

    config = get_network(DEFAULT_NETWORK_NAME)

    if config is not None:
        # Metadata claims network exists, verify actual resources
        bridge_missing = not bridge_exists(config.bridge)
        # Check for our custom chains by trying to set them up (idempotent)
        chains_missing = not setup_mvm_chains()

        # Check if NAT rules are missing (when NAT is enabled)
        nat_missing = False
        if config.nat_enabled:
            from mvmctl.constants import MVM_POSTROUTING_CHAIN
            from mvmctl.core.network import _iptables_rule_exists, get_default_interface

            try:
                internet_iface = get_default_interface()
                masquerade_check = [
                    "iptables",
                    "-t",
                    "nat",
                    "-C",
                    MVM_POSTROUTING_CHAIN,
                    "-s",
                    config.cidr,
                    "-o",
                    internet_iface,
                    "-j",
                    "MASQUERADE",
                ]
                nat_missing = not _iptables_rule_exists(masquerade_check)
            except Exception:
                nat_missing = True

        if bridge_missing or chains_missing or nat_missing:
            # Recreate from stored config
            gateway_cidr = f"{config.gateway}/{_prefix_len(config.cidr)}"
            try:
                if bridge_missing:
                    setup_bridge(config.bridge, gateway_cidr=gateway_cidr)
                if config.nat_enabled:
                    setup_nat(config.bridge)
                    if os.getuid() == 0:
                        save_iptables_rules()
                # Update metadata to reflect reality
                cache_dir = get_cache_dir()
                update_network_entry(cache_dir, DEFAULT_NETWORK_NAME, bridge_active=True)
            except NetworkError:
                # Best-effort cleanup on failure
                if bridge_missing:
                    try:
                        teardown_bridge(config.bridge)
                    except NetworkError:
                        pass
                raise
        return config

    # No metadata entry, create from scratch
    return create_network(DEFAULT_NETWORK_NAME, cidr=DEFAULT_NETWORK_CIDR, nat=True)


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
