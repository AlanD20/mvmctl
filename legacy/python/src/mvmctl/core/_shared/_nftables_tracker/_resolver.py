"""NFTables rule resolution helpers."""

from __future__ import annotations

from mvmctl.core._shared._enrichment import RelationEnricher, RelationSpec
from mvmctl.core._shared._nftables_tracker._repository import (
    NFTablesRuleRepository,
)
from mvmctl.models import FirewallRule

__all__ = ["NFTablesRuleResolver"]


class NFTablesRuleResolver:
    """Resolver for nftables rules."""

    RELATIONS: dict[str, RelationSpec] = {}

    def __init__(
        self,
        repo: NFTablesRuleRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else NFTablesRuleRepository()
        self._include = include

    def _enrich(self, rules: list[FirewallRule]) -> list[FirewallRule]:
        """Enrich rules with relations if include is set."""
        if self._include and rules:
            RelationEnricher().enrich(rules, self._include, self.RELATIONS)
        return rules

    def list_by_network_id(self, network_id: str) -> list[FirewallRule]:
        """List all nftables rules for a network."""
        rules = self._repo.list_by_network_id(network_id)
        return self._enrich(rules)

    def list_by_network_id_batch(
        self, network_ids: list[str]
    ) -> dict[str, list[FirewallRule]]:
        """Batch-resolve nftables rules by network IDs."""
        rules = self._repo.list_by_network_id_batch(network_ids)
        results: dict[str, list[FirewallRule]] = {
            nid: [] for nid in network_ids
        }
        for rule in rules:
            if rule.network_id in results:
                results[rule.network_id].append(rule)
        return results

    def get(self, rule_id: int) -> FirewallRule | None:
        """Get a specific rule by ID."""
        rule = self._repo.get(rule_id)
        if rule is None:
            return None
        return self._enrich([rule])[0]

    def list_all(self) -> list[FirewallRule]:
        """List all nftables rules."""
        rules = self._repo.list_all()
        return self._enrich(rules)


from mvmctl.core._shared._resolver_registry import register  # noqa: E402

register("nftables_rule", lambda: NFTablesRuleResolver)
