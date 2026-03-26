"""Named network management — create, persist, and query named networks."""

from __future__ import annotations

import ipaddress
import json
import logging
import os
import shutil
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import TypedDict

from mvmctl.constants import (
    CONST_FILE_PERMS_DHCP_LEASES,
    CONST_FILE_PERMS_NETWORK_CONFIG,
    CONST_FILE_PERMS_VM_STATE,
    DEFAULT_NETWORK_CIDR,
    DEFAULT_NETWORK_NAME,
    device_prefix,
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
from mvmctl.utils.fs import get_network_dir, get_networks_dir
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


def _chown_to_real_user(path: Path) -> None:
    import os
    import pwd

    sudo_user = os.environ.get("SUDO_USER")
    if not sudo_user or os.getuid() != 0:
        return
    try:
        pw = pwd.getpwnam(sudo_user)
        os.chown(path, pw.pw_uid, pw.pw_gid)
    except (KeyError, OSError):
        pass


def _save_config(network_dir: Path, config: NetworkConfig) -> None:
    network_dir.mkdir(parents=True, exist_ok=True)
    config_file = network_dir / "config.json"
    config_file.write_text(json.dumps(asdict(config), indent=2))
    config_file.chmod(CONST_FILE_PERMS_NETWORK_CONFIG)
    _chown_to_real_user(config_file)
    _chown_to_real_user(network_dir)


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
    leases_file.chmod(CONST_FILE_PERMS_DHCP_LEASES)
    _chown_to_real_user(leases_file)


@dataclass
class NetworkState:
    """Runtime state for a named network, persisted as ``state.json``."""

    bridge_active: bool
    last_checked: str = field(default_factory=lambda: datetime.now(tz=timezone.utc).isoformat())


def _load_network_state(network_dir: Path) -> NetworkState | None:
    """Load persisted runtime state for a network."""
    state_file = network_dir / "state.json"
    if not state_file.exists():
        return None
    data = json.loads(state_file.read_text())
    return NetworkState(**data)


def _save_network_state(network_dir: Path, state: NetworkState) -> None:
    network_dir.mkdir(parents=True, exist_ok=True)
    state_file = network_dir / "state.json"
    state_file.write_text(json.dumps(asdict(state), indent=2))
    state_file.chmod(CONST_FILE_PERMS_VM_STATE)
    _chown_to_real_user(state_file)


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
) -> NetworkConfig:
    """Create a named network.

    Sets up the bridge device, IP range, and optionally NAT rules.
    The network configuration is persisted to disk.

    Args:
        name: Network name.
        cidr: IP subnet in CIDR notation (e.g., "192.168.100.0/24").
        gateway: Gateway IP for the bridge. Defaults to first host in subnet.
        nat: Whether to configure NAT/masquerade. Default True.

    Returns:
        The created NetworkConfig.

    Raises:
        NetworkError: If the network already exists or setup fails.
    """
    validate_entity_name(name, "network")

    network_dir = get_network_dir(name)
    if _load_config(network_dir) is not None:
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
            setup_nat(bridge)
    except NetworkError:
        # Best-effort cleanup on failure
        try:
            teardown_bridge(bridge)
        except NetworkError as e:
            logger.warning("Rollback: failed to tear down bridge: %s", e)
        raise

    _save_config(network_dir, config)
    _save_leases(network_dir, [])
    _save_network_state(network_dir, NetworkState(bridge_active=True))

    _persist_iptables_if_root()

    return config


def remove_network(name: str) -> None:
    """Remove a named network.

    Tears down the bridge and NAT rules, then removes persisted state.

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

    shutil.rmtree(network_dir, ignore_errors=True)

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

    network_dir = get_network_dir(name)
    config = _load_config(network_dir)
    if config is None:
        raise NetworkError(f"Network '{name}' not found")

    leases = _load_leases(network_dir)
    active = bridge_exists(config.bridge)

    _save_network_state(network_dir, NetworkState(bridge_active=active))

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

    For each persisted network, checks whether its bridge device still
    exists on the host.  Updates each network's ``state.json`` and
    returns a list of reconciliation results.  Entries where
    ``stale is True`` indicate that the bridge was expected to be up
    but is no longer present in the kernel.
    """
    results: list[ReconcileResult] = []
    for config in list_networks():
        network_dir = get_network_dir(config.name)
        stored = _load_network_state(network_dir)
        actual_active = bridge_exists(config.bridge)

        stored_active = stored.bridge_active if stored else None
        stale = (stored_active is True) and (not actual_active)

        _save_network_state(network_dir, NetworkState(bridge_active=actual_active))

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
