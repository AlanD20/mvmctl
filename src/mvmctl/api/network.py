"""Network management API — create, remove, list, inspect, IP allocation."""

from __future__ import annotations

from mvmctl.api.host import check_privileges_interactive
from mvmctl.core.network import get_iptables_rules_for_bridge, list_network_interfaces
from mvmctl.core.network_manager import (
    NetworkConfig,
    NetworkLease,
    allocate_network_ip,
    check_ip_available,
    ensure_default_network,
    get_network,
    get_network_leases,
    inspect_network,
    list_networks,
    release_network_ip,
    set_default_network,
)
from mvmctl.core.network_manager import (
    create_network as _create_network,
)
from mvmctl.core.network_manager import (
    remove_network as _remove_network,
)

__all__ = [
    "NetworkConfig",
    "NetworkLease",
    "check_ip_available",
    "get_iptables_rules_for_bridge",
    "list_network_interfaces",
    "list_networks",
    "get_network",
    "get_network_leases",
    "create_network",
    "remove_network",
    "inspect_network",
    "allocate_network_ip",
    "release_network_ip",
    "ensure_default_network",
    "set_default_network",
]


def create_network(
    name: str,
    cidr: str,
    gateway: str | None = None,
    nat: bool = True,
    internet_iface: str | None = None,
) -> NetworkConfig:
    """Create a named network, setting up bridge and NAT rules."""
    check_privileges_interactive("/usr/sbin/ip", f"create network '{name}'")
    return _create_network(name, cidr=cidr, gateway=gateway, nat=nat, internet_iface=internet_iface)


def remove_network(name: str) -> None:
    """Remove a named network, tearing down its bridge and NAT rules."""
    check_privileges_interactive("/usr/sbin/ip", f"remove network '{name}'")
    return _remove_network(name)
