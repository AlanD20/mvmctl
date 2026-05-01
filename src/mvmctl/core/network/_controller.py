"""Network entity lifecycle management."""

from __future__ import annotations

import logging

from mvmctl.core.network._lease_service import LeaseService
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.core.network._resolver import NetworkResolver
from mvmctl.models import NetworkItem, NetworkLeaseItem

logger = logging.getLogger(__name__)

__all__ = ["NetworkController"]


class NetworkController:
    """
    Stateful network entity lifecycle manager.

    Resolves network entity in __init__ and operates on cached network instance.
    """

    def __init__(
        self,
        entity: str | NetworkItem,
        repo: NetworkRepository,
    ) -> None:
        self._repo = repo

        if isinstance(entity, NetworkItem):
            self._network = entity
        else:
            self._resolver = NetworkResolver(self._repo)
            self._network = self._resolver.resolve(entity)

    def get(self) -> NetworkItem:
        """Return the resolved network entity."""
        return self._network

    def set_default(self) -> None:
        """Set this network as the default."""
        self._repo.set_default(self._network.id)

    def get_leases(self) -> list[NetworkLeaseItem]:
        """Get all IP leases for this network."""
        lease_service = LeaseService(
            self._network, LeaseRepository(self._repo._db)
        )
        return lease_service.get_leases()

    def remove(self, *, force: bool = False) -> None:
        """
        Remove this network from database.

        Hard-deletes when no VMs reference the network.
        Soft-deletes only when VMs still reference it (to preserve history).

        Infrastructure teardown (NAT, bridge) is handled by NetworkService
        before this method is called.

        Args:
            force: If True, remove even if referenced by VMs.

        Raises:
            NetworkError: If network is referenced by VMs and force is False.

        """
        from mvmctl.exceptions import NetworkError

        vms = self._repo.query_vms_by_network(self._network.id)
        has_vms = bool(vms)

        # 1. VM reference check
        if has_vms and not force:
            raise NetworkError(
                f"Network referenced by VMs: {', '.join(v.name for v in vms)}"
            )

        # 2. Hard delete if no VMs, soft delete if VMs exist (with force)
        if has_vms:
            self._repo.soft_delete(self._network.id)
        else:
            self._repo.delete(self._network.id)
