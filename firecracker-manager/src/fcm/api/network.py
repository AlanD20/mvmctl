"""Network management API — create, remove, list, inspect, IP allocation."""

from __future__ import annotations

from fcm.core.network import get_iptables_rules_for_bridge
from fcm.core.network_manager import (
    NetworkConfig,
    NetworkLease,
    allocate_network_ip,
    create_network as _create_network,
    ensure_default_network,
    get_network,
    get_network_leases,
    inspect_network,
    list_networks,
    release_network_ip,
    remove_network as _remove_network,
)
from fcm.api.host import check_privileges

__all__ = [
    "NetworkConfig",
    "NetworkLease",
    "get_iptables_rules_for_bridge",
    "list_networks",
    "get_network",
    "get_network_leases",
    "create_network",
    "remove_network",
    "inspect_network",
    "allocate_network_ip",
    "release_network_ip",
    "ensure_default_network",
]


def create_network(
    name: str,
    cidr: str | None = None,
    gateway: str | None = None,
    nat: bool = True,
    subnet: str | None = None,
) -> NetworkConfig:
    """Create a named network, setting up bridge and NAT rules."""
    check_privileges("/usr/sbin/ip")
    return _create_network(name, cidr=cidr, gateway=gateway, nat=nat, subnet=subnet)


def remove_network(name: str) -> None:
    """Remove a named network, tearing down its bridge and NAT rules."""
    check_privileges("/usr/sbin/ip")
    return _remove_network(name)
