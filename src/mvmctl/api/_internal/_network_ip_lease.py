"""Network IP lease management.

This module handles IP address allocation, release, and lease tracking
for network management using a class-based design with network resolution.
"""

from __future__ import annotations

import ipaddress

from mvmctl.api._internal._resolvers import NetworkResolver
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Network as DBNetwork
from mvmctl.exceptions import NetworkError
from mvmctl.models import NetworkLease


class NetworkIPLeaseManager:
    """Manages IP leases for a specific network.

    This class handles IP address allocation, release, and lease tracking
    for a single network identified by name or ID.

    Args:
        network: Network name, ID prefix, or DBNetwork instance.
        db: Optional MVMDatabase instance (creates new if None).

    Raises:
        NetworkNotFoundError: If the network cannot be resolved.
    """

    def __init__(self, network: str | DBNetwork, db: MVMDatabase | None = None) -> None:
        self._db = db if db is not None else MVMDatabase()

        if isinstance(network, DBNetwork):
            self._network = network
        else:
            self._resolver = NetworkResolver(self._db)
            self._network = self._resolver.resolve(network)

    @property
    def network_id(self) -> str:
        """Get the resolved network ID."""
        return self._network.id

    @property
    def network_name(self) -> str:
        """Get the resolved network name."""
        return self._network.name

    def list_leases(self) -> list[NetworkLease]:
        """Get all IP leases for this network.

        Returns:
            List of NetworkLease objects for the network.
        """
        db_leases = self._db.list_leases(self._network.id)
        return [NetworkLease(vm_id=lease.vm_id or "", ipv4=lease.ipv4) for lease in db_leases]

    def get(self, ip: str) -> NetworkLease | None:
        """Get lease for a specific IP address.

        Args:
            ip: IP address to look up.

        Returns:
            NetworkLease if found, None otherwise.
        """
        lease = self._db.get_lease(self._network.id, ip)
        if lease is None:
            return None
        return NetworkLease(vm_id=lease.vm_id or "", ipv4=lease.ipv4)

    def get_by_vm_id(self, vm_id: str) -> list[NetworkLease]:
        """Get all leases for a specific VM on this network.

        Args:
            vm_id: VM ID to look up.

        Returns:
            List of NetworkLease objects for the VM.
        """
        db_leases = self._db.list_leases_for_vm(self._network.id, vm_id)
        return [NetworkLease(vm_id=lease.vm_id or "", ipv4=lease.ipv4) for lease in db_leases]

    def is_available(self, ip: str) -> bool:
        """Check if an IP address is available (not leased).

        Queries the database directly to check availability.

        Args:
            ip: IP address to verify availability.

        Returns:
            True if the IP is not currently leased, False otherwise.
        """
        return self._db.get_lease(self._network.id, ip) is None

    def lease(self, vm_id: str) -> str:
        """Allocate the next available IP from this network's subnet.

        Registers the lease in database.

        Args:
            vm_id: ID of the VM requesting the IP.
            config: Network configuration.

        Returns:
            The allocated IP address string.

        Raises:
            NetworkError: If no IPs available.
        """
        leases = self.list_leases()
        used_ips = {lease.ipv4 for lease in leases}
        used_ips.add(self._network.ipv4_gateway)

        network = ipaddress.IPv4Network(self._network.subnet, strict=False)
        for host in network.hosts():
            ip_str = str(host)
            if ip_str == self._network.ipv4_gateway:
                continue
            if ip_str not in used_ips:
                self._db.acquire_lease(self._network.id, ip_str, vm_id)
                return ip_str

        raise NetworkError(f"No available IPs in subnet {self._network.subnet}")

    def release(self, vm_id: str) -> None:
        """Release all leases for a VM from this network.

        Args:
            vm_id: ID of the VM whose leases should be released.
        """
        self._db.release_vm_leases(vm_id)


__all__ = [
    "NetworkIPLeaseManager",
]
