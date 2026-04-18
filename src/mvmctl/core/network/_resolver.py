"""Network resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.core._internal._enrichment import RelationEnricher
from mvmctl.core.network._iptables_resolver import IPTablesRuleResolver
from mvmctl.core.network._lease_resolver import NetworkLeaseResolver
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.exceptions import NetworkNotFoundError
from mvmctl.models.network import NetworkItem

__all__ = [
    "NetworkResolver",
    "NetworkResolveResult",
]


@dataclass
class NetworkResolveResult:
    items: list[NetworkItem]
    errors: list[str]
    exit_code: int


class NetworkResolver:
    """Resolver for network configuration."""

    RELATIONS: dict[str, tuple[str, type, str]] = {
        "leases": ("id", NetworkLeaseResolver, "list_by_network_id"),
        "iptables_rules": ("id", IPTablesRuleResolver, "list_by_network_id"),
    }

    def __init__(
        self,
        repo: NetworkRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else NetworkRepository()
        self._include = include

    def _enrich(self, networks: list[NetworkItem]) -> list[NetworkItem]:
        """Enrich networks with relations if include is set."""
        if self._include and networks:
            RelationEnricher().enrich(
                networks, self._include, self.RELATIONS, self._repo._db
            )
        return networks

    def by_id(self, network_id: str) -> NetworkItem:
        """Resolve network by ID prefix."""
        matches = self._repo.find_by_prefix(network_id)
        if len(matches) == 0:
            raise NetworkNotFoundError(f"Network not found: {network_id}")
        if len(matches) > 1:
            raise NetworkNotFoundError(f"Network ID is ambiguous: {network_id}")
        return self._enrich(matches)[0]

    def by_name(self, name: str) -> NetworkItem:
        """Resolve network by name."""
        network = self._repo.get_by_name(name)
        if network is None:
            raise NetworkNotFoundError(f"Network not found: {name}")
        return self._enrich([network])[0]

    def get_default(self) -> NetworkItem | None:
        """Resolve the default network, or None if not set."""
        network = self._repo.get_default()
        if network is None:
            return None
        return self._enrich([network])[0]

    def resolve(self, value: str) -> NetworkItem:
        """Resolve network by name or ID prefix."""
        try:
            network = self.by_name(value)
        except NetworkNotFoundError:
            network = self.by_id(value)
        return network

    def resolve_many(self, identifiers: list[str]) -> NetworkResolveResult:
        """Resolve multiple network identifiers by name or id."""
        # Deduplicate identifiers while preserving order
        seen_inputs: set[str] = set()
        unique_ids: list[str] = []
        for ident in identifiers:
            if ident not in seen_inputs:
                seen_inputs.add(ident)
                unique_ids.append(ident)

        items: list[NetworkItem] = []
        errors: list[str] = []
        resolved_ids: set[str] = set()

        for identifier in unique_ids:
            try:
                item = self.resolve(identifier)
                if item.id not in resolved_ids:
                    resolved_ids.add(item.id)
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        items = self._enrich(items)

        exit_code = 1 if errors and not items else 0
        return NetworkResolveResult(
            items=items, errors=errors, exit_code=exit_code
        )
