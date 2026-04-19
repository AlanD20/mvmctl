"""IPTables rule resolution helpers."""

from __future__ import annotations

from mvmctl.core._internal._enrichment import RelationEnricher
from mvmctl.core._internal._iptables_tracker import IPTablesRuleRepository
from mvmctl.models.network import IPTablesRuleItem

__all__ = ["IPTablesRuleResolver"]


class IPTablesRuleResolver:
    """Resolver for iptables rules."""

    RELATIONS: dict[str, tuple[str, type, str]] = {}

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
            RelationEnricher().enrich(
                rules, self._include, self.RELATIONS, self._repo._db
            )
        return rules

    def list_by_network_id(self, network_id: str) -> list[IPTablesRuleItem]:
        """List all iptables rules for a network."""
        rules = self._repo.list_by_network_id(network_id)
        return self._enrich(rules)

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
