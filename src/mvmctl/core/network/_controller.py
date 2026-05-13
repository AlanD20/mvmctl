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
