"""Network management API — create, remove, list, inspect, IP allocation."""

from __future__ import annotations

from fcm.core.network_manager import (
    NetworkConfig,
    NetworkLease,
    allocate_network_ip,
    create_network,
    ensure_default_network,
    get_network,
    get_network_leases,
    inspect_network,
    list_networks,
    release_network_ip,
    remove_network,
)

__all__ = [
    "NetworkConfig",
    "NetworkLease",
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
