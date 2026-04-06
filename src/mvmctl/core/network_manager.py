"""Named network management — IP lease management and network config CRUD.

This module is a PURE registry layer. It works only with in-memory NetworkConfig
objects. All persistence and network setup/teardown is handled by the API layer.
"""

from __future__ import annotations

import ipaddress
import logging
from dataclasses import asdict, dataclass
from typing import Any, TypedDict

from mvmctl.constants import (
    DEFAULT_NETWORK_NAME,
    DEFAULT_NETWORK_SUBNET,
)
from mvmctl.exceptions import MVMError, NetworkError
from mvmctl.models.network import NetworkConfig as NetworkConfig
from mvmctl.models.network import NetworkLease as NetworkLease
from mvmctl.utils.network import allocate_ip
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
    validate_ipv4_address,
    validate_subnet,
)

logger = logging.getLogger(__name__)


def _bridge_name_for(network_name: str) -> str:
    return _bridge_name_for_util(network_name)


def _ipv4_gateway_for_subnet(subnet: str) -> str:
    return _ipv4_gateway_for_subnet_util(subnet)


def _prefix_len(subnet: str) -> int:
    return _prefix_len_util(subnet)


# ---------------------------------------------------------------------------
# Network config builders (pure functions)
# ---------------------------------------------------------------------------


def build_network_config(
    name: str,
    subnet: str,
    ipv4_gateway: str | None = None,
    nat_enabled: bool = True,
    nat_gateways: list[str] | None = None,
    created_at: str = "",
    is_default: bool = False,
) -> NetworkConfig:
    """Build a NetworkConfig from validated inputs.

    This is a pure function that creates a NetworkConfig without any side effects.
    The API layer is responsible for persisting this config.

    Args:
        name: Network name (validated).
        subnet: IP subnet in CIDR notation (validated).
        ipv4_gateway: Gateway IPv4 (auto-computed from subnet if None).
        nat_enabled: Whether NAT is enabled.
        nat_gateways: Physical interfaces for NAT.
        created_at: Creation timestamp (auto-generated if empty).
        is_default: Whether this is the default network.

    Returns:
        Validated NetworkConfig object.

    Raises:
        NetworkError: If validation fails.
    """
    name = validate_entity_name(name, "network")
    subnet = validate_subnet(subnet)

    if ipv4_gateway is None:
        ipv4_gateway = _ipv4_gateway_for_subnet(subnet)
    else:
        ipv4_gateway = validate_ipv4_address(ipv4_gateway)

    bridge = _bridge_name_for(name)
    bridge = validate_bridge_name(bridge)

    return NetworkConfig(
        name=name,
        subnet=subnet,
        ipv4_gateway=ipv4_gateway,
        bridge=bridge,
        nat_enabled=nat_enabled,
        nat_gateways=nat_gateways or [],
        created_at=created_at,
        is_default=is_default,
    )


def network_entry_to_config(name: str, entry: dict[str, Any]) -> NetworkConfig | None:
    """Convert a metadata entry dict to NetworkConfig.

    Validates all fields from metadata to prevent injection attacks.
    Invalid entries are logged as warnings and skipped.

    Args:
        name: Network name.
        entry: Metadata entry dict.

    Returns:
        NetworkConfig or None if entry is invalid.
    """
    if not entry:
        return None

    # Validate network name
    try:
        name = validate_entity_name(name, "network")
    except MVMError as e:
        logger.warning("Invalid network name in metadata: %s", e)
        return None

    # Extract and validate subnet
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
                    nat_gateways.append(iface)
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


def config_to_network_entry(config: NetworkConfig) -> dict[str, Any]:
    """Convert a NetworkConfig to a metadata entry dict.

    Args:
        config: NetworkConfig object.

    Returns:
        Dict suitable for persistence via metadata API.
    """
    return {
        "subnet": config.subnet,
        "ipv4_gateway": config.ipv4_gateway,
        "bridge": config.bridge,
        "nat_enabled": config.nat_enabled,
        "nat_gateways": config.nat_gateways,
        "created_at": config.created_at,
        "is_default": 1 if config.is_default else 0,
    }


def leases_from_entry(entry: dict[str, Any]) -> list[NetworkLease]:
    """Extract leases from a metadata entry dict."""
    raw_leases = entry.get("leases", [])
    if not isinstance(raw_leases, list):
        return []
    leases = []
    for item in raw_leases:
        if isinstance(item, dict) and "vm_id" in item and "ipv4" in item:
            leases.append(NetworkLease(vm_id=item["vm_id"], ipv4=item["ipv4"]))
    return leases


def leases_to_dicts(leases: list[NetworkLease]) -> list[dict[str, str]]:
    """Convert NetworkLease objects to dicts for persistence."""
    return [asdict(lease) for lease in leases]


# ---------------------------------------------------------------------------
# IP lease management (pure functions)
# ---------------------------------------------------------------------------


def check_ip_available(network_config: NetworkConfig, leases: list[NetworkLease], ip: str) -> None:
    """Check if an IP is available for use in a network.

    Args:
        network_config: Network configuration (for gateway IP).
        leases: Current leases for the network.
        ip: IP address to verify availability.

    Raises:
        NetworkError: If the IP is already leased to another VM.
    """
    for lease in leases:
        if lease.ipv4 == ip:
            raise NetworkError(f"IP {ip} is already in use by VM '{lease.vm_id}'")


def is_ip_available(network_config: NetworkConfig, leases: list[NetworkLease], ip: str) -> bool:
    """Check if an IP address is available for use in a network.

    Args:
        network_config: Network configuration (for gateway IP).
        leases: Current leases for the network.
        ip: IP address to verify availability.

    Returns:
        True if the IP is not currently leased, False otherwise.
    """
    for lease in leases:
        if lease.ipv4 == ip:
            return False
    return True


def allocate_network_ip(
    network_config: NetworkConfig, leases: list[NetworkLease], vm_name: str
) -> tuple[str, list[NetworkLease]]:
    """Allocate the next available IP from a network's subnet.

    Args:
        network_config: Network configuration.
        leases: Current leases for the network.
        vm_name: VM name to associate with the lease.

    Returns:
        Tuple of (allocated_ip, updated_leases).
        The API layer is responsible for persisting the updated leases.

    Raises:
        NetworkError: If no IPs are available.
    """
    used_ips = [lease.ipv4 for lease in leases]
    # Also reserve the gateway
    used_ips.append(network_config.ipv4_gateway)

    ip = allocate_ip(used_ips, subnet=network_config.subnet)
    new_lease = NetworkLease(vm_id=vm_name, ipv4=ip)
    updated_leases = leases + [new_lease]

    return ip, updated_leases


def release_network_ip(leases: list[NetworkLease], vm_name: str) -> list[NetworkLease]:
    """Release a VM's IP lease from a network.

    Args:
        leases: Current leases for the network.
        vm_name: VM name whose lease should be released.

    Returns:
        Updated leases list (without the released VM).
        The API layer is responsible for persisting the updated leases.
    """
    return [lease for lease in leases if lease.vm_id != vm_name]


# ---------------------------------------------------------------------------
# Validation helpers (pure functions)
# ---------------------------------------------------------------------------


def validate_no_subnet_overlap(
    subnet: str, existing_networks: list[NetworkConfig], exclude_name: str = ""
) -> None:
    """Check that the given subnet doesn't overlap with existing networks.

    Args:
        subnet: Subnet to validate.
        existing_networks: List of existing network configs to check against.
        exclude_name: Network name to exclude from overlap check.

    Raises:
        NetworkError: If subnet overlaps with an existing network.
    """
    new_net = ipaddress.IPv4Network(subnet, strict=False)
    for existing in existing_networks:
        if existing.name == exclude_name:
            continue
        existing_net = ipaddress.IPv4Network(existing.subnet, strict=False)
        if new_net.overlaps(existing_net):
            raise NetworkError(
                f"Subnet {subnet} overlaps with network '{existing.name}' ({existing.subnet})"
            )


def validate_bridge_not_conflicting(
    bridge_name: str, existing_networks: list[NetworkConfig], exclude_name: str = ""
) -> None:
    """Check that the bridge name doesn't conflict with existing networks.

    Args:
        bridge_name: Bridge name to validate.
        existing_networks: List of existing network configs to check against.
        exclude_name: Network name to exclude from conflict check.

    Raises:
        NetworkError: If bridge name conflicts with an existing network.
    """
    for existing in existing_networks:
        if existing.name == exclude_name:
            continue
        if existing.bridge == bridge_name:
            raise NetworkError(
                f"Bridge name '{bridge_name}' conflicts with network '{existing.name}'"
            )


# ---------------------------------------------------------------------------
# Data structures for inspect/reconcile results
# ---------------------------------------------------------------------------


class VMLease(TypedDict):
    """VM lease information for inspect output."""

    vm_id: str
    ipv4: str
    status: str
    pid: int | None
    api_socket_path: str | None


class NetworkInspect(TypedDict):
    """Full network details for inspect output."""

    name: str
    subnet: str
    ipv4_gateway: str
    bridge: str
    nat_enabled: bool
    nat_gateways: list[str]
    created_at: str
    bridge_exists: bool
    vms: list[VMLease]


@dataclass
class ReconcileResult:
    """Result of reconciling stored network state against kernel state."""

    name: str
    bridge: str
    stored_active: bool | None
    actual_active: bool
    stale: bool


# ---------------------------------------------------------------------------
# Default network helpers
# ---------------------------------------------------------------------------


def should_preserve_current_default(
    current_default_name: str | None, new_default_name: str
) -> bool:
    """Check if current default should be preserved (not the same as new default).

    Args:
        current_default_name: Current default network name (None if no default).
        new_default_name: New default network name being set.

    Returns:
        True if current default should be preserved, False otherwise.
    """
    if current_default_name is None:
        return False
    return current_default_name != new_default_name


def get_default_network_gateway() -> str:
    """Get the default gateway for the default network subnet."""
    return _ipv4_gateway_for_subnet(DEFAULT_NETWORK_SUBNET)


def get_default_network_bridge() -> str:
    """Get the bridge name for the default network."""
    return _bridge_name_for(DEFAULT_NETWORK_NAME)
