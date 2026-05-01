"""IPTables rule resolution helpers."""

from __future__ import annotations

from mvmctl.core._shared._enrichment import RelationEnricher, RelationSpec
from mvmctl.core._shared._iptables_tracker import IPTablesRuleRepository
from mvmctl.models import IPTablesRuleItem

__all__ = ["IPTablesRuleResolver"]


class IPTablesRuleResolver:
    """Resolver for iptables rules."""

    RELATIONS: dict[str, RelationSpec] = {}

    def __init__(
        self,
        repo: IPTablesRuleRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else IPTablesRuleRepository()
        self._include = include

    def _enrich(self, rules: list[IPTablesRuleItem]) -> list[IPTablesRuleItem]:
        """Enrich rules with relations if include is set."""
        if self._include and rules:
            RelationEnricher().enrich(rules, self._include, self.RELATIONS)
        return rules

    def list_by_network_id(self, network_id: str) -> list[IPTablesRuleItem]:
        """List all iptables rules for a network."""
        rules = self._repo.list_by_network_id(network_id)
        return self._enrich(rules)

    def list_by_network_id_batch(
        self, network_ids: list[str]
    ) -> dict[str, list[IPTablesRuleItem]]:
        """Batch-resolve iptables rules by network IDs."""
        rules = self._repo.list_by_network_id_batch(network_ids)
        results: dict[str, list[IPTablesRuleItem]] = {
            nid: [] for nid in network_ids
        }
        for rule in rules:
            if rule.network_id in results:
                results[rule.network_id].append(rule)
        return results

    def get(self, rule_id: int) -> IPTablesRuleItem | None:
        """Get a specific rule by ID."""
        rule = self._repo.get(rule_id)
        if rule is None:
            return None
        return self._enrich([rule])[0]

    def list_all(self) -> list[IPTablesRuleItem]:
        """List all iptables rules."""
        rules = self._repo.list_all()
        return self._enrich(rules)


from mvmctl.core._shared._resolver_registry import register  # noqa: E402

register("iptables_rule", lambda: IPTablesRuleResolver)
