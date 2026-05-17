"""
Unified firewall tracker — delegates to the appropriate backend.

Usage::

    tracker = FirewallTracker(db)
    result = tracker.ensure_rule(rule)

    with tracker.batch():
        tracker.ensure_rule(rule1)
        tracker.ensure_rule(rule2)
    # on exit: rules flushed atomically via single ``nft -f -`` (nftables)
    #          or executed individually (iptables)
"""

from __future__ import annotations

import logging
from collections.abc import Generator
from contextlib import contextmanager
from typing import TYPE_CHECKING

from mvmctl.core._shared._db import Database
from mvmctl.models import FirewallTable
from mvmctl.models.network import FirewallRuleResult

if TYPE_CHECKING:
    from mvmctl.core._shared._iptables_tracker import IPTablesTracker
    from mvmctl.core._shared._iptables_tracker._repository import (
        IPTablesRuleRepository,
    )
    from mvmctl.core._shared._nftables_tracker import NFTablesTracker
    from mvmctl.core._shared._nftables_tracker._repository import (
        NFTablesRuleRepository,
    )
    from mvmctl.models import (
        FirewallChain,
        FirewallRule,
        FirewallTable,
        NetworkItem,
    )
    from mvmctl.models.network import FirewallRuleResult

logger = logging.getLogger(__name__)


class FirewallTracker:
    """Unified firewall tracker that selects iptables or nftables backend.

    All ``ensure_*`` and ``remove_*`` methods delegate to the active backend
    so callers never need to know which firewall system is in use.

    When inside a :meth:`batch` context, :meth:`ensure_rule` queues rules
    instead of executing them.  On context exit, all queued rules are flushed
    atomically via ``nft -f -`` (nftables) or individually (iptables).
    """

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()
        self._fw_repo: IPTablesRuleRepository | NFTablesRuleRepository
        self._backend: IPTablesTracker | NFTablesTracker
        self._batch_mode: bool = False
        self._batch_rules: list[FirewallRule] = []

        from mvmctl.core._shared._iptables_tracker import (
            IPTablesRuleRepository,
            IPTablesTracker,
        )
        from mvmctl.core._shared._nftables_tracker import (
            NFTablesRuleRepository,
            NFTablesTracker,
        )
        from mvmctl.core.config._service import SettingsService

        backend = SettingsService.resolve(
            self._db, "settings", "firewall_backend"
        )
        if backend == "nftables":
            self._fw_repo = NFTablesRuleRepository(self._db)
            self._backend = NFTablesTracker(repo=self._fw_repo)
        else:
            self._fw_repo = IPTablesRuleRepository(self._db)
            xtcomment_avail = bool(
                SettingsService.resolve(
                    self._db,
                    "settings.firewall",
                    "iptables_xtcomment",
                )
            )
            self._backend = IPTablesTracker(
                repo=self._fw_repo,
                xtcomment_available=xtcomment_avail,
            )

    # -- batch context ------------------------------------------------------

    @contextmanager
    def batch(self) -> Generator[None, None, None]:
        """Context manager: queue :meth:`ensure_rule` calls, flush on exit.

        For nftables: all queued rules are applied via a single ``nft -f -``
        call.  For iptables: each rule is executed individually (no batch
        optimisation), but the interface is identical.
        """
        self._batch_mode = True
        self._batch_rules.clear()
        try:
            yield
        finally:
            if self._batch_rules:
                result = self.batch_ensure_rules(self._batch_rules)
                if not result.success:
                    logger.error(
                        "Batch firewall rule flush failed: %s",
                        result.error_message,
                    )
            self._batch_rules.clear()
            self._batch_mode = False

    # -- repo access -------------------------------------------------------

    @property
    def repo(self) -> IPTablesRuleRepository | NFTablesRuleRepository:
        """Return the active firewall rule repository."""
        return self._fw_repo

    # -- rule lifecycle ----------------------------------------------------

    def ensure_rule(
        self,
        rule: FirewallRule,
        *,
        context: str = "",
    ) -> FirewallRuleResult:
        """Ensure a rule exists.

        When inside a :meth:`batch` context, queues the rule instead of
        executing it immediately.  All queued rules are flushed on context
        exit.
        """
        if self._batch_mode:
            self._batch_rules.append(rule)
            return FirewallRuleResult(success=True)
        return self._backend.ensure_rule(rule, context=context)

    def batch_ensure_rules(
        self,
        rules: list[FirewallRule],
    ) -> FirewallRuleResult:
        return self._backend.batch_ensure_rules(rules)

    def remove_rule(self, rule: FirewallRule) -> FirewallRuleResult:
        return self._backend.remove_rule(rule)

    def batch_remove_rules(
        self,
        rules: list[FirewallRule],
    ) -> FirewallRuleResult:
        return self._backend.batch_remove_rules(rules)

    def count_orphaned_rules(self, network: NetworkItem) -> int:
        """Delegate orphan counting to the active backend."""
        return self._backend.count_orphaned_rules(network)

    # -- chain lifecycle ---------------------------------------------------

    def ensure_chain(
        self,
        chain_name: FirewallChain,
        table: FirewallTable = FirewallTable.FILTER,
        *,
        auto_jump_from: str | None = None,
        position: int = 1,
    ) -> bool:
        return self._backend.ensure_chain(
            chain_name,
            table,
            auto_jump_from=auto_jump_from,
            position=position,
        )

    def flush_chain(
        self,
        chain_name: FirewallChain,
        table: FirewallTable = FirewallTable.FILTER,
    ) -> bool:
        return self._backend.flush_chain(chain_name, table)

    def initialize(self) -> None:
        """Create base chains for the active backend."""
        self._backend.initialize()

    def teardown(self) -> None:
        """Remove all MVM firewall chains for the active backend.

        Delegates to the active backend's teardown logic.
        Always returns ``None``.
        """
        self._backend.teardown()
