"""Network lease resolution helpers."""

from __future__ import annotations

from mvmctl.core._internal._enrichment import RelationEnricher
from mvmctl.core.network._repository import LeaseRepository
from mvmctl.models.network import NetworkLeaseItem

__all__ = ["NetworkLeaseResolver"]


class NetworkLeaseResolver:
    """Resolver for network IP leases."""

    RELATIONS: dict[str, tuple[str, type, str]] = {}

    def __init__(
        self,
        repo: LeaseRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else LeaseRepository()
        self._include = include

    def _enrich(self, leases: list[NetworkLeaseItem]) -> list[NetworkLeaseItem]:
        """Enrich leases with relations if include is set."""
        if self._include and leases:
            RelationEnricher().enrich(
                leases, self._include, self.RELATIONS, self._repo._db
            )
        return leases

    def list_by_network_id(self, network_id: str) -> list[NetworkLeaseItem]:
        """List all leases for a network."""
        leases = self._repo.list_all(network_id)
        return self._enrich(leases)

    def get(self, network_id: str, ipv4: str) -> NetworkLeaseItem | None:
        """Get a specific lease by network_id + ipv4."""
        lease = self._repo.get(network_id, ipv4)
        if lease is None:
            return None
        return self._enrich([lease])[0]

    def list_by_vm(self, network_id: str, vm_id: str) -> list[NetworkLeaseItem]:
        """List all leases for a VM on a specific network."""
        leases = self._repo.list_by_vm(network_id, vm_id)
        return self._enrich(leases)
