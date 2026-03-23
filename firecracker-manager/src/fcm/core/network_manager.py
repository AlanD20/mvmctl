"""Named network management — create, persist, and query named networks."""

from __future__ import annotations

import ipaddress
import json
import logging
from dataclasses import asdict, dataclass, field
from datetime import datetime
from pathlib import Path

from fcm.constants import device_prefix
from fcm.core.network import (
    allocate_ip,
    setup_bridge,
    setup_nat,
    teardown_bridge,
    teardown_nat,
    bridge_exists,
)
from fcm.exceptions import NetworkError
from fcm.utils.fs import get_networks_dir, get_network_dir

logger = logging.getLogger(__name__)

DEFAULT_NETWORK_NAME = "default"
DEFAULT_SUBNET = "10.20.0.0/24"


@dataclass
class NetworkConfig:
    """Persistent configuration for a named network."""

    name: str
    cidr: str
    gateway: str
    bridge: str
    nat_enabled: bool = True
    created_at: str = field(default_factory=lambda: datetime.now().isoformat())


@dataclass
class NetworkLease:
    """IP lease assignment for a VM within a network."""

    vm_name: str
    ip: str


def _bridge_name_for(network_name: str) -> str:
    """Derive a bridge device name from a network name."""
    prefix = device_prefix()
    if network_name == DEFAULT_NETWORK_NAME:
        return f"{prefix}-br0"
    # Truncate to keep device name <= 15 chars (Linux limit)
    truncated = network_name[:8]
    return f"{prefix}-{truncated}"


def _gateway_for_subnet(subnet: str) -> str:
    """Return the first usable host IP in a subnet as the gateway."""
    net = ipaddress.IPv4Network(subnet, strict=False)
    return str(next(iter(net.hosts())))


# ---------------------------------------------------------------------------
# Persistence
# ---------------------------------------------------------------------------


def _load_config(network_dir: Path) -> NetworkConfig | None:
    config_file = network_dir / "config.json"
    if not config_file.exists():
        return None
    data = json.loads(config_file.read_text())
    # Migrate legacy 'subnet' key to 'cidr'
    if "subnet" in data and "cidr" not in data:
        data["cidr"] = data.pop("subnet")
    return NetworkConfig(**data)


def _save_config(network_dir: Path, config: NetworkConfig) -> None:
    network_dir.mkdir(parents=True, exist_ok=True)
    config_file = network_dir / "config.json"
    config_file.write_text(json.dumps(asdict(config), indent=2))


def _load_leases(network_dir: Path) -> list[NetworkLease]:
    leases_file = network_dir / "leases.json"
    if not leases_file.exists():
        return []
    data = json.loads(leases_file.read_text())
    return [NetworkLease(**entry) for entry in data]


def _save_leases(network_dir: Path, leases: list[NetworkLease]) -> None:
    network_dir.mkdir(parents=True, exist_ok=True)
    leases_file = network_dir / "leases.json"
    leases_file.write_text(json.dumps([asdict(lease) for lease in leases], indent=2))


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------


def list_networks() -> list[NetworkConfig]:
    """List all named networks."""
    networks_dir = get_networks_dir()
    if not networks_dir.exists():
        return []

    result: list[NetworkConfig] = []
    for d in sorted(networks_dir.iterdir()):
        if d.is_dir():
            config = _load_config(d)
            if config:
                result.append(config)
    return result


def get_network(name: str) -> NetworkConfig | None:
    """Get a named network by name."""
    network_dir = get_network_dir(name)
    return _load_config(network_dir)


def get_network_leases(name: str) -> list[NetworkLease]:
    """Get all IP leases for a network."""
    return _load_leases(get_network_dir(name))


def create_network(
    name: str,
    cidr: str | None = None,
    gateway: str | None = None,
    nat: bool = True,
    # Legacy alias for backward compatibility
    subnet: str | None = None,
) -> NetworkConfig:
    """Create a named network.

    Sets up the bridge device, IP range, and optionally NAT rules.
    The network configuration is persisted to disk.

    Args:
        name: Network name.
        cidr: IP subnet in CIDR notation (e.g., "192.168.100.0/24").
        gateway: Gateway IP for the bridge. Defaults to first host in subnet.
        nat: Whether to configure NAT/masquerade. Default True.
        subnet: Deprecated alias for cidr.

    Returns:
        The created NetworkConfig.

    Raises:
        NetworkError: If the network already exists or setup fails.
    """
    # Handle legacy subnet parameter
    if cidr is None and subnet is not None:
        cidr = subnet

    network_dir = get_network_dir(name)
    if _load_config(network_dir) is not None:
        raise NetworkError(f"Network '{name}' already exists")

    # Resolve defaults
    if cidr is None:
        cidr = _auto_allocate_subnet()
    _validate_subnet_no_overlap(cidr, name)

    if gateway is None:
        gateway = _gateway_for_subnet(cidr)

    bridge = _bridge_name_for(name)

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
            setup_nat(bridge)
    except NetworkError:
        # Best-effort cleanup on failure
        try:
            teardown_bridge(bridge)
        except NetworkError:
            pass
        raise

    _save_config(network_dir, config)
    _save_leases(network_dir, [])
    return config


def remove_network(name: str) -> None:
    """Remove a named network.

    Tears down the bridge and NAT rules, then removes persisted state.

    Raises:
        NetworkError: If the network has VMs attached or doesn't exist.
    """
    network_dir = get_network_dir(name)
    config = _load_config(network_dir)
    if config is None:
        raise NetworkError(f"Network '{name}' not found")

    leases = _load_leases(network_dir)
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

    # Remove persisted state
    import shutil

    shutil.rmtree(network_dir, ignore_errors=True)


def inspect_network(name: str) -> dict[str, object]:
    """Return full details for a named network."""
    network_dir = get_network_dir(name)
    config = _load_config(network_dir)
    if config is None:
        raise NetworkError(f"Network '{name}' not found")

    leases = _load_leases(network_dir)
    return {
        "name": config.name,
        "cidr": config.cidr,
        "gateway": config.gateway,
        "bridge": config.bridge,
        "nat_enabled": config.nat_enabled,
        "created_at": config.created_at,
        "bridge_exists": bridge_exists(config.bridge),
        "vms": [{"vm_name": lease.vm_name, "ip": lease.ip} for lease in leases],
    }


def allocate_network_ip(network_name: str, vm_name: str) -> str:
    """Allocate the next available IP from a network's subnet.

    Registers the lease in the network's leases.json.

    Returns:
        The allocated IP address string.
    """
    network_dir = get_network_dir(network_name)
    config = _load_config(network_dir)
    if config is None:
        raise NetworkError(f"Network '{network_name}' not found")

    leases = _load_leases(network_dir)
    used_ips = [lease.ip for lease in leases]
    # Also reserve the gateway
    used_ips.append(config.gateway)

    ip = allocate_ip(used_ips, subnet=config.cidr)
    leases.append(NetworkLease(vm_name=vm_name, ip=ip))
    _save_leases(network_dir, leases)
    return ip


def release_network_ip(network_name: str, vm_name: str) -> None:
    """Release a VM's IP lease from a network."""
    network_dir = get_network_dir(network_name)
    leases = _load_leases(network_dir)
    leases = [lease for lease in leases if lease.vm_name != vm_name]
    _save_leases(network_dir, leases)


def ensure_default_network() -> NetworkConfig:
    """Ensure the default network exists, creating it if needed."""
    config = get_network(DEFAULT_NETWORK_NAME)
    if config is not None:
        return config
    return create_network(DEFAULT_NETWORK_NAME, cidr=DEFAULT_SUBNET, nat=True)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _prefix_len(subnet: str) -> int:
    return ipaddress.IPv4Network(subnet, strict=False).prefixlen


def _auto_allocate_subnet() -> str:
    """Auto-allocate the next available /24 subnet."""
    existing = list_networks()
    used_nets = set()
    for net in existing:
        used_nets.add(ipaddress.IPv4Network(net.cidr, strict=False))

    # Start from 10.20.0.0/24 and increment the third octet
    for i in range(256):
        candidate = ipaddress.IPv4Network(f"10.20.{i}.0/24")
        if candidate not in used_nets:
            return str(candidate)

    raise NetworkError("No available /24 subnets in 10.20.0.0/16 pool")


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
