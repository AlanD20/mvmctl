"""Network IP lease management.

This module handles IP address allocation, release, and lease tracking
for network management using a class-based design with network resolution.
"""

from __future__ import annotations

import ipaddress

from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.core.network._resolver import NetworkResolver
from mvmctl.exceptions import NetworkError
from mvmctl.models.network import NetworkItem, NetworkLeaseItem
from mvmctl.utils.network import NetworkUtils


class LeaseService:
    """Manages IP leases for a specific network.

    This class handles IP address allocation, release, and lease tracking
    for a single network identified by name or ID.

    Args:
        entity: Network name, ID prefix, or NetworkItem instance.
        repo: LeaseRepository instance for database operations.

    Raises:
        NetworkNotFoundError: If the network cannot be resolved.
    """

    def __init__(
        self, entity: str | NetworkItem, repo: LeaseRepository
    ) -> None:
        self._lease_repo = repo

        if isinstance(entity, NetworkItem):
            self._network = entity
        else:
            self._resolver = NetworkResolver(self._lease_repo)
            self._network = self._resolver.resolve(entity)

    @property
    def network_id(self) -> str:
        """Get the resolved network ID."""
        return self._network.id

    @property
    def network_name(self) -> str:
        """Get the resolved network name."""
        return self._network.name

    def get_leases(self) -> list[NetworkLeaseItem]:
        """Get all IP leases for this network.

        Returns:
            List of NetworkLeaseItem objects for the network.
        """
        db_leases = self._lease_repo.list_all(self._network.id)
        return [
            NetworkLeaseItem(
                network_id=lease.network_id,
                ipv4=lease.ipv4,
                vm_id=lease.vm_id,
                id=lease.id,
                leased_at=lease.leased_at,
                expires_at=lease.expires_at,
            )
            for lease in db_leases
        ]

    def get(self, ip: str) -> NetworkLeaseItem | None:
        """Get lease for a specific IP address.

        Args:
            ip: IP address to look up.

        Returns:
            NetworkLeaseItem if found, None otherwise.
        """
        lease = self._lease_repo.get(self._network.id, ip)
        if lease is None:
            return None
        return NetworkLeaseItem(
            network_id=lease.network_id,
            ipv4=lease.ipv4,
            vm_id=lease.vm_id,
            id=lease.id,
            leased_at=lease.leased_at,
            expires_at=lease.expires_at,
        )

    def get_by_vm_id(self, vm_id: str) -> list[NetworkLeaseItem]:
        """Get all leases for a specific VM on this network.

        Args:
            vm_id: VM ID to look up.

        Returns:
            List of NetworkLeaseItem objects for the VM.
        """
        db_leases = self._lease_repo.list_by_vm(self._network.id, vm_id)
        return [
            NetworkLeaseItem(
                network_id=lease.network_id,
                ipv4=lease.ipv4,
                vm_id=lease.vm_id,
                id=lease.id,
                leased_at=lease.leased_at,
                expires_at=lease.expires_at,
            )
            for lease in db_leases
        ]

    def is_available(self, ip: str) -> bool:
        """Check if an IP address is available (not leased).

        Queries the database directly to check availability.

        Args:
            ip: IP address to verify availability.

        Returns:
            True if the IP is not currently leased, False otherwise.
        """
        return self._lease_repo.get(self._network.id, ip) is None

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
        leases = self.get_leases()
        used_ips = {lease.ipv4 for lease in leases}
        allocated_ip = NetworkUtils.allocate_next_ip(
            list(used_ips),
            self._network.subnet,
            self._network.ipv4_gateway,
        )
        self._lease_repo.acquire(self._network.id, allocated_ip, vm_id)
        return allocated_ip

    def lease_specific(self, ip: str, vm_id: str) -> str:
        """Allocate a specific IP address from this network's subnet.

        Validates that the IP is in the subnet, not already leased, and not the gateway.

        Args:
            ip: The specific IP address to allocate.
            vm_id: ID of the VM requesting the IP.

        Returns:
            The allocated IP address string.

        Raises:
            NetworkError: If IP is not in subnet, is the gateway, or is already leased.
        """
        network = ipaddress.IPv4Network(self._network.subnet, strict=False)
        try:
            ip_obj = ipaddress.IPv4Address(ip)
            if ip_obj not in network:
                raise NetworkError(
                    f"IP {ip} is not in subnet {self._network.subnet}"
                )
        except ValueError as exc:
            raise NetworkError(f"Invalid IP address: {ip}") from exc

        if ip == self._network.ipv4_gateway:
            raise NetworkError(
                f"IP {ip} is the network gateway and cannot be allocated"
            )

        if not self.is_available(ip):
            raise NetworkError(f"IP {ip} is already leased")

        self._lease_repo.acquire(self._network.id, ip, vm_id)
        return ip

    def release(self, vm_id: str) -> None:
        """Release all leases for a VM from this network.

        Args:
            vm_id: ID of the VM whose leases should be released.
        """
        self._lease_repo.release_by_vm(vm_id)


__all__ = [
    "LeaseService",
]
