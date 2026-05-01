"""Network lease resolution helpers."""

from __future__ import annotations

from mvmctl.core._shared import RelationEnricher, RelationSpec
from mvmctl.core.network._repository import LeaseRepository
from mvmctl.models.network import NetworkLeaseItem

__all__ = ["NetworkLeaseResolver"]


class NetworkLeaseResolver:
    """Resolver for network IP leases."""

    RELATIONS: dict[str, RelationSpec] = {}

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
            RelationEnricher().enrich(leases, self._include, self.RELATIONS)
        return leases

    def list_by_network_id(self, network_id: str) -> list[NetworkLeaseItem]:
        """List all leases for a network."""
        leases = self._repo.list_all(network_id)
        return self._enrich(leases)

    def list_by_network_id_batch(
        self, network_ids: list[str]
    ) -> dict[str, list[NetworkLeaseItem]]:
        """Batch-resolve leases by network IDs."""
        leases = self._repo.list_all_batch(network_ids)
        results: dict[str, list[NetworkLeaseItem]] = {
            nid: [] for nid in network_ids
        }
        for lease in leases:
            if lease.network_id in results:
                results[lease.network_id].append(lease)
        return results

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


from mvmctl.core._shared import register  # noqa: E402

register("network_lease", lambda: NetworkLeaseResolver)
