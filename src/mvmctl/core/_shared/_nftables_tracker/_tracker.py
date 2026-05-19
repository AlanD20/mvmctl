"""
Idempotent nftables rule management with database synchronization.

This module provides NFTablesTracker for creating/removing nftables rules
and synchronizing them with the nftables_rules database table.

Unlike the legacy iptables-based approach, nftables uses non-hook "MVM-*"
chains inside the system ``ip filter`` and ``ip nat`` tables, with jump rules
at position 0 of the built-in ``FORWARD`` / ``POSTROUTING`` / ``INPUT``
chains.  This means ``accept`` verdicts in MVM rules terminate FORWARD
processing *before* UFW rules are evaluated — mimicking what
``iptables -I FORWARD 1 -j MVM-FORWARD`` does.
"""

from __future__ import annotations

import logging
import re

from mvmctl.core._shared._nftables_tracker._repository import (
    NFTablesRuleRepository,
)
from mvmctl.exceptions import ProcessError
from mvmctl.models import (
    FirewallChain,
    FirewallPort,
    FirewallProtocol,
    FirewallRule,
    FirewallTable,
    FirewallWildcard,
    NetworkItem,
)
from mvmctl.models.network import FirewallRuleResult
from mvmctl.utils._system import run_cmd

logger = logging.getLogger(__name__)

# Map MVM chain → system nftables table name.
# MVM-FORWARD and MVM-NOCLOUDNET-INPUT live in ``ip filter``;
# MVM-POSTROUTING lives in ``ip nat``.
_CHAIN_TO_TABLE: dict[FirewallChain, str] = {
    FirewallChain.MVM_FORWARD: "filter",
    FirewallChain.MVM_POSTROUTING: "nat",
    FirewallChain.MVM_NOCLOUDNET_INPUT: "filter",
}

# (nftables family, system table, built-in chain, MVM chain) for jump rules.
_JUMP_RULES: list[tuple[str, str, str, str]] = [
    ("ip", "filter", "FORWARD", FirewallChain.MVM_FORWARD.value),
    ("ip", "nat", "POSTROUTING", FirewallChain.MVM_POSTROUTING.value),
    ("ip", "filter", "INPUT", FirewallChain.MVM_NOCLOUDNET_INPUT.value),
]

# Built-in base chains that the MVM chains hook into via jump rules.
# Keyed by ``(family, table, chain_name)`` → nftables hook definition.
_BASE_CHAINS: dict[tuple[str, str, str], str] = {
    (
        "ip",
        "filter",
        "FORWARD",
    ): "{ type filter hook forward priority filter; policy accept; }",
    (
        "ip",
        "filter",
        "INPUT",
    ): "{ type filter hook input priority filter; policy accept; }",
    (
        "ip",
        "nat",
        "POSTROUTING",
    ): "{ type nat hook postrouting priority srcnat; policy accept; }",
}


class NFTablesTracker:
    """
    Idempotent nftables rule manager with database synchronization.

    Manages non-hook MVM chains inside the system ``ip filter`` and ``ip nat``
    tables.  Jump rules at position 0 of the built-in base chains ensure MVM
    rules are evaluated before any UFW or third-party rules.
    """

    def __init__(self, repo: NFTablesRuleRepository) -> None:
        """Initialize NFTablesTracker with repository."""
        self._repo = repo

    # ── Chain & Jump Rule Management ──────────────────────────────────

    def _chain_exists(self, family: str, table: str, chain: str) -> bool:
        """Check if a chain exists in a system nftables table."""
        result = run_cmd(
            ["nft", "list", "chain", family, table, chain],
            privileged=True,
            check=False,
        )
        return result.returncode == 0

    def _jump_rule_exists(
        self, family: str, table: str, builtin_chain: str, target_chain: str
    ) -> bool:
        """Check if a jump rule from a built-in chain to an MVM chain exists."""
        result = run_cmd(
            ["nft", "list", "chain", family, table, builtin_chain],
            privileged=True,
            check=False,
        )
        if result.returncode != 0:
            return False
        return f"jump {target_chain}" in result.stdout

    def _find_jump_rule_handle(
        self, family: str, table: str, builtin_chain: str, target_chain: str
    ) -> int | None:
        """Find the nftables handle for a jump rule in a built-in chain.

        Parses ``nft -a list chain ...`` output and looks for a rule
        containing ``jump <target_chain>``.
        """
        result = run_cmd(
            ["nft", "-a", "list", "chain", family, table, builtin_chain],
            privileged=True,
            check=False,
        )
        if result.returncode != 0:
            return None
        for line in result.stdout.splitlines():
            stripped = line.strip()
            if f"jump {target_chain}" in stripped and " # handle " in stripped:
                handle_str = stripped.split(" # handle ")[-1].strip()
                try:
                    return int(handle_str)
                except ValueError:
                    continue
        return None

    def initialize(self) -> None:
        """
        Create non-hook MVM chains in system tables and add jump rules.

        Creates the three MVM chains (MVM-FORWARD, MVM-POSTROUTING,
        MVM-NOCLOUDNET-INPUT) in the appropriate system table, then inserts
        jump rules at position 0 of the built-in base chains.

        All operations are idempotent — already-existing chains and jump
        rules are silently skipped.
        """
        # ── Ensure system tables exist (idempotent) ──────────────────
        for table in set(_CHAIN_TO_TABLE.values()):
            run_cmd(
                ["nft", "add", "table", "ip", table],
                privileged=True,
                check=False,
            )

        # ── Create chains in system tables ────────────────────────────
        for chain, table in _CHAIN_TO_TABLE.items():
            if self._chain_exists("ip", table, chain.value):
                logger.debug(
                    "Chain %s already exists in ip/%s", chain.value, table
                )
                continue
            try:
                run_cmd(
                    ["nft", "add", "chain", "ip", table, chain.value],
                    privileged=True,
                )
                logger.info("Created chain %s in ip/%s", chain.value, table)
            except ProcessError as e:
                raise RuntimeError(
                    f"Failed to create nftables chain {chain.value} "
                    f"in ip/{table}: {e}"
                ) from e

        # ── Ensure built-in base chains exist (FORWARD, INPUT, POSTROUTING)
        # Native nftables tables don't come with these automatically.
        for (
            family,
            table,
            builtin_chain_name,
        ), hook_def in _BASE_CHAINS.items():
            if self._chain_exists(family, table, builtin_chain_name):
                continue
            try:
                run_cmd(
                    [
                        "nft",
                        "add",
                        "chain",
                        family,
                        table,
                        builtin_chain_name,
                        hook_def,
                    ],
                    privileged=True,
                )
                logger.info(
                    "Created built-in chain %s in %s/%s",
                    builtin_chain_name,
                    family,
                    table,
                )
            except ProcessError as e:
                raise RuntimeError(
                    f"Failed to create built-in chain {builtin_chain_name} "
                    f"in {family}/{table}: {e}"
                ) from e

        # ── Insert jump rules at position 0 of built-in chains ────────
        for family, table, builtin_chain, target_chain in _JUMP_RULES:
            if self._jump_rule_exists(
                family, table, builtin_chain, target_chain
            ):
                logger.debug(
                    "Jump rule %s → %s already exists in %s/%s",
                    builtin_chain,
                    target_chain,
                    family,
                    table,
                )
                continue
            try:
                run_cmd(
                    [
                        "nft",
                        "insert",
                        "rule",
                        family,
                        table,
                        builtin_chain,
                        "jump",
                        target_chain,
                    ],
                    privileged=True,
                )
                logger.info(
                    "Inserted jump rule %s → %s in %s/%s",
                    builtin_chain,
                    target_chain,
                    family,
                    table,
                )
            except ProcessError as e:
                raise RuntimeError(
                    f"Failed to insert jump rule {builtin_chain} → "
                    f"{target_chain} in {family}/{table}: {e}"
                ) from e

    def ensure_chain(
        self,
        chain_name: FirewallChain,
        table: FirewallTable = FirewallTable.FILTER,
        auto_jump_from: str | None = None,
        position: int = 1,
    ) -> bool:
        """
        Ensure a custom MVM chain exists in the system nftables table.

        Creates the chain as a non-hook chain in the system table
        (``ip filter`` or ``ip nat`` depending on *table*).  The
        ``auto_jump_from`` and ``position`` parameters are accepted for
        signature compatibility with ``IPTablesTracker`` but are not
        needed — jump rules are managed by :meth:`initialize`.

        Args:
            chain_name: Name of the chain to create (enum value).
            table: System table to create the chain in (``filter`` or ``nat``).
            auto_jump_from: Ignored (jump rules managed by initialize).
            position: Ignored.

        Returns:
            True if the chain was created, False if it already existed.
        """
        if self._chain_exists("ip", table.value, chain_name.value):
            logger.debug(
                "Chain %s already exists in ip/%s",
                chain_name.value,
                table.value,
            )
            return False

        # Ensure the table exists before adding a chain to it.
        run_cmd(
            ["nft", "add", "table", "ip", table.value],
            privileged=True,
            check=False,
        )

        try:
            run_cmd(
                ["nft", "add", "chain", "ip", table.value, chain_name.value],
                privileged=True,
            )
            logger.debug(
                "Created chain %s in ip/%s",
                chain_name.value,
                table.value,
            )
            return True
        except ProcessError as e:
            raise RuntimeError(
                f"Failed to create chain {chain_name.value}: {e}"
            ) from e

    # ── Handle helpers ──────────────────────────────────────────────────

    def _list_chain_rules(
        self, chain: FirewallChain, table: str = "filter"
    ) -> list[tuple[int, str]]:
        """List all rules in an nftables chain with their handles.

        Returns list of ``(handle, rule_text)`` tuples parsed from
        ``nft -a list chain ip <table> <chain>`` output.

        If the chain does not exist (e.g. after a reboot), returns an
        empty list instead of raising.
        """
        try:
            result = run_cmd(
                [
                    "nft",
                    "-a",
                    "list",
                    "chain",
                    "ip",
                    table,
                    chain.value,
                ],
                privileged=True,
            )
        except ProcessError:
            return []
        rules: list[tuple[int, str]] = []
        for line in result.stdout.splitlines():
            stripped = line.strip()
            if (
                not stripped
                or stripped.startswith("#")
                or stripped.startswith("chain ")
                or stripped.startswith("type ")
                or stripped.startswith("}")
            ):
                continue
            # Look for handle comment at end
            if " # handle " not in stripped:
                continue
            handle_str = stripped.split(" # handle ")[-1].strip()
            try:
                handle = int(handle_str)
            except ValueError:
                continue
            # Rule text is the rest (remove the handle comment)
            rule_text = stripped.rsplit(" # handle ", 1)[0].strip()
            rules.append((handle, rule_text))
        return rules

    def _find_rule_handle(self, rule: FirewallRule) -> int | None:
        """Find the nftables handle for a rule by matching its expression.

        Parses the chain's rules with ``nft -a`` and compares each rule's
        text against the expression generated by :meth:`_rule_to_nft_expr`.
        Returns the handle if found, ``None`` otherwise.
        """
        nft_expr = self._rule_to_nft_expr(rule)
        # Build the expected rule text (without handle)
        expected = " ".join(nft_expr)

        for handle, rule_text in self._list_chain_rules(
            rule.chain_name, rule.table_name.value
        ):
            if expected in rule_text:
                return handle
        return None

    # ── Single Rule Operations ────────────────────────────────────────

    def ensure_rule(
        self, rule: FirewallRule, *, context: str = ""
    ) -> FirewallRuleResult:
        """
        Idempotently ensure a rule exists in nftables and database.

        Args:
            rule: FirewallRule dataclass containing all rule parameters.
            context: Optional context string for logging.

        Returns:
            FirewallRuleResult with success status and rule metadata.
        """
        nft_expr = self._rule_to_nft_expr(rule)

        # Check if rule exists in database
        existing_db_rule = self._repo.find_by_attributes(
            table_name=rule.table_name,
            chain_name=rule.chain_name,
            rule_type=rule.rule_type,
            network_id=rule.network_id,
            protocol=rule.protocol,
            source=rule.source,
            destination=rule.destination,
            in_interface=rule.in_interface,
            out_interface=rule.out_interface,
            sport=rule.sport,
            dport=rule.dport,
        )

        if existing_db_rule:
            if existing_db_rule.id is not None:
                self._repo.update_verified_at(existing_db_rule.id)
            return FirewallRuleResult(success=True, rule=existing_db_rule)

        # Add rule in the system table indicated by rule.table_name
        add_cmd = [
            "nft",
            "add",
            "rule",
            "ip",
            rule.table_name.value,
            rule.chain_name.value,
        ]
        add_cmd.extend(nft_expr)

        command_str = " ".join(add_cmd)
        rule.command_string = command_str

        try:
            run_cmd(
                add_cmd,
                privileged=True,
            )
        except ProcessError as e:
            return FirewallRuleResult(
                success=False,
                error_message=f"Failed to create nftables rule: {e}",
                command_executed=command_str,
            )

        recorded_rule = self._repo.insert(rule)

        return FirewallRuleResult(
            success=True,
            rule=recorded_rule,
            command_executed=command_str,
        )

    def remove_rule(self, rule: FirewallRule) -> FirewallRuleResult:
        """
        Remove a specific rule from nftables and mark as deleted in database.

        First attempts to find the rule's handle via :meth:`_find_rule_handle`,
        then deletes by handle.  ``nft delete rule`` requires a handle in
        nftables v1.1+ — specification-based deletion is not supported.

        Args:
            rule: FirewallRule to remove.

        Returns:
            FirewallRuleResult with success status.
        """
        # Try to find the rule in the database first
        db_rule = rule
        if rule.id is None:
            existing = self._repo.find_by_attributes(
                table_name=rule.table_name,
                chain_name=rule.chain_name,
                rule_type=rule.rule_type,
                network_id=rule.network_id,
                protocol=rule.protocol,
                source=rule.source,
                destination=rule.destination,
                in_interface=rule.in_interface,
                out_interface=rule.out_interface,
                sport=rule.sport,
                dport=rule.dport,
            )
            if existing:
                db_rule = existing

        handle = self._find_rule_handle(db_rule)
        if handle is None:
            return FirewallRuleResult(
                success=False,
                error_message="Rule not found in nftables (no matching handle)",
            )

        del_cmd = [
            "nft",
            "delete",
            "rule",
            "ip",
            db_rule.table_name.value,
            db_rule.chain_name.value,
            "handle",
            str(handle),
        ]

        delete_result = run_cmd(
            del_cmd,
            privileged=True,
            check=False,
        )

        if delete_result.returncode != 0:
            return FirewallRuleResult(
                success=False,
                error_message=f"Failed to remove nftables rule: {delete_result.stderr}",
                command_executed=" ".join(del_cmd),
            )

        if db_rule.id is not None:
            self._repo.mark_deleted(db_rule.id)

        return FirewallRuleResult(
            success=True,
            rule=db_rule,
            command_executed=" ".join(del_cmd),
        )

    # ── Batch Operations ──────────────────────────────────────────────

    def batch_ensure_rules(
        self,
        rules: list[FirewallRule],
    ) -> FirewallRuleResult:
        """
        Ensure multiple rules atomically via ``nft -f -``.

        Flushes only MVM custom chains (NOT the entire table/ruleset),
        adds a conntrack ``established,related accept`` rule as the first
        rule in MVM-FORWARD and MVM-NOCLOUDNET-INPUT to preserve
        established connections during the atomic swap, then applies
        all provided rules in a single privileged subprocess call.

        Args:
            rules: List of FirewallRule to ensure.

        Returns:
            FirewallRuleResult indicating batch success/failure.
        """
        lines: list[str] = []

        # 1. Flush only our MVM custom chains — NOT the entire table/ruleset
        for chain, table in _CHAIN_TO_TABLE.items():
            lines.append(f"flush chain ip {table} {chain.value}")
        lines.append("")

        # 2. Conntrack rule first — preserves established connections
        for chain, table in _CHAIN_TO_TABLE.items():
            if table == "filter":
                lines.append(
                    f"add rule ip {table} {chain.value} "
                    f"ct state established,related accept"
                )
        lines.append("")

        # 2. Conntrack rule first — preserves established connections
        lines.append(
            f"add rule ip filter {FirewallChain.MVM_FORWARD.value} "
            f"ct state established,related accept"
        )
        lines.append(
            f"add rule ip filter "
            f"{FirewallChain.MVM_NOCLOUDNET_INPUT.value} "
            f"ct state established,related accept"
        )
        lines.append("")

        # 3. Add all DB rules
        new_rules: list[FirewallRule] = []
        for rule in rules:
            nft_expr = self._rule_to_nft_expr(rule)
            lines.append(
                f"add rule ip {rule.table_name.value} "
                f"{rule.chain_name.value} {' '.join(nft_expr)}"
            )
            new_rules.append(rule)

        nft_script = "\n".join(lines) + "\n"

        try:
            run_cmd(
                ["nft", "-f", "-"],
                privileged=True,
                input=nft_script,
            )
        except ProcessError as e:
            return FirewallRuleResult(
                success=False,
                error_message=str(e),
            )

        # Update verified_at for existing rules (insert if new)
        for rule in new_rules:
            existing = self._repo.find_by_attributes(
                table_name=rule.table_name,
                chain_name=rule.chain_name,
                rule_type=rule.rule_type,
                network_id=rule.network_id,
                protocol=rule.protocol,
                source=rule.source,
                destination=rule.destination,
                in_interface=rule.in_interface,
                out_interface=rule.out_interface,
                sport=rule.sport,
                dport=rule.dport,
            )
            if existing is not None and existing.id is not None:
                self._repo.update_verified_at(existing.id)
            else:
                self._repo.insert(rule)

        return FirewallRuleResult(success=True)

    def batch_remove_rules(
        self,
        rules: list[FirewallRule],
    ) -> FirewallRuleResult:
        """
        Remove multiple rules using handle-based deletion.

        ``nft delete rule`` requires a handle in nftables v1.1+.
        For each rule, :meth:`_find_rule_handle` retrieves the handle,
        then deletes by handle.  Failure of any individual delete is
        reported but does not abort the batch; subsequent rules are
        still attempted.

        Args:
            rules: List of FirewallRule to remove.

        Returns:
            FirewallRuleResult indicating overall batch success.
        """
        last_error: str | None = None
        for rule in rules:
            handle = self._find_rule_handle(rule)
            if handle is None:
                last_error = (
                    f"Rule not found in nftables: {rule.chain_name.value} "
                    f"in={rule.in_interface} out={rule.out_interface}"
                )
                logger.warning("batch_remove_rules: %s", last_error)
                # Already gone from kernel — clean up DB entry too
                if rule.id is not None:
                    self._repo.mark_deleted(rule.id)
                continue

            del_cmd = [
                "nft",
                "delete",
                "rule",
                "ip",
                rule.table_name.value,
                rule.chain_name.value,
                "handle",
                str(handle),
            ]

            result = run_cmd(del_cmd, privileged=True, check=False)
            if result.returncode != 0:
                last_error = result.stderr or f"exit {result.returncode}"
                logger.warning("Failed to delete nftables rule: %s", last_error)
            elif rule.id is not None:
                self._repo.mark_deleted(rule.id)

        if last_error:
            return FirewallRuleResult(
                success=False,
                error_message=(
                    f"Some nftables rules could not be deleted: {last_error}"
                ),
            )

        logger.info("Removed %d nftables rules", len(rules))
        return FirewallRuleResult(success=True)

    def count_orphaned_rules(self, network: NetworkItem) -> int:
        """Count host nftables rules for this network with no matching DB record.

        Scans active MVM chains via ``nft -a`` for rules whose ``comment``
        references the given network but has no corresponding active record
        in the database.

        Args:
            network: The network to check orphaned rules for.

        Returns:
            Number of orphaned rules detected.
        """
        db_rules = self._repo.get_by_network_id(network.id, active_only=True)
        db_comments = {r.comment_tag for r in db_rules if r.comment_tag}

        chain_mapping: list[tuple[FirewallChain, str]] = [
            (FirewallChain.MVM_FORWARD, "filter"),
            (FirewallChain.MVM_POSTROUTING, "nat"),
            (FirewallChain.MVM_NOCLOUDNET_INPUT, "filter"),
        ]

        orphaned = 0
        for chain, table in chain_mapping:
            try:
                rules = self._list_chain_rules(chain, table)
            except ProcessError:
                continue

            for _handle, rule_text in rules:
                match = re.search(r'comment\s+"([^"]+)"', rule_text)
                if not match:
                    continue
                comment = match.group(1)

                if network.name in comment and comment not in db_comments:
                    orphaned += 1
                    logger.warning(
                        "Orphaned nftables rule on host for network %s: %s",
                        network.name,
                        rule_text,
                    )

        return orphaned

    def teardown(self) -> None:
        """
        Remove MVM jump rules and chains from system tables.

        Best-effort — uses ``check=False`` so that already-removed chains
        or rules are handled silently.

        Steps:
        1. Find and delete jump rules from built-in chains (FORWARD,
           POSTROUTING, INPUT) by handle.
        2. Flush the MVM chain (remove all rules inside it).
        3. Delete the MVM chain itself.

        Always returns ``None``.
        """
        for family, table, builtin_chain, target_chain in _JUMP_RULES:
            # 1. Remove jump rule from built-in chain
            handle = self._find_jump_rule_handle(
                family, table, builtin_chain, target_chain
            )
            if handle is not None:
                run_cmd(
                    [
                        "nft",
                        "delete",
                        "rule",
                        family,
                        table,
                        builtin_chain,
                        "handle",
                        str(handle),
                    ],
                    privileged=True,
                    check=False,
                )

            # 2. Flush the MVM chain (empty it before delete)
            run_cmd(
                ["nft", "flush", "chain", family, table, target_chain],
                privileged=True,
                check=False,
            )

            # 3. Delete the MVM chain
            run_cmd(
                ["nft", "delete", "chain", family, table, target_chain],
                privileged=True,
                check=False,
            )

    def flush_chain(
        self,
        chain: FirewallChain,
        table_name: FirewallTable = FirewallTable.FILTER,
    ) -> bool:
        """
        Flush all rules from an MVM chain and mark them deleted in DB.

        Args:
            chain: Chain to flush.
            table_name: System table containing the chain (``filter`` or ``nat``).

        Returns:
            True if flushed, False if chain does not exist.
        """
        chain_name = chain.value

        try:
            run_cmd(
                [
                    "nft",
                    "flush",
                    "chain",
                    "ip",
                    table_name.value,
                    chain_name,
                ],
                privileged=True,
            )
        except ProcessError:
            logger.debug("Chain %s not found, nothing to flush", chain_name)
            return False

        deleted_count = self._repo.mark_deleted_by_chain(chain)
        logger.debug(
            "Marked %d rules as deleted for chain %s",
            deleted_count,
            chain_name,
        )
        return True

    # ── Expression Conversion ─────────────────────────────────────────

    def _rule_to_nft_expr(self, rule: FirewallRule) -> list[str]:
        """
        Convert a FirewallRule to nftables expression arguments.

        Uses nftables syntax:
        - ``iifname`` / ``oifname`` for interfaces
        - ``ip saddr`` / ``ip daddr`` for addresses
        - ``masquerade`` for MASQUERADE target (lowercase)
        - ``accept`` for ACCEPT target (lowercase)

        Returns:
            List of string arguments suitable for ``nft add rule ...``.
        """
        expr: list[str] = []

        # Protocol
        if rule.protocol != FirewallProtocol.ALL:
            expr.append(rule.protocol.value)

        # Source address
        if rule.source != FirewallWildcard.ANY_CIDR:
            expr.extend(["ip", "saddr", rule.source])

        # Destination address
        if rule.destination != FirewallWildcard.ANY_CIDR:
            expr.extend(["ip", "daddr", rule.destination])

        # Input interface
        if rule.in_interface != FirewallWildcard.ANY_INTERFACE:
            expr.extend(["iifname", f'"{rule.in_interface}"'])

        # Output interface
        if rule.out_interface != FirewallWildcard.ANY_INTERFACE:
            expr.extend(["oifname", f'"{rule.out_interface}"'])

        # Source port
        if rule.sport != FirewallPort.ANY:
            expr.extend([rule.protocol.value, "sport", str(rule.sport)])

        # Destination port
        if rule.dport != FirewallPort.ANY:
            expr.extend([rule.protocol.value, "dport", str(rule.dport)])

        # Target (lowercase for nftables)
        target_lower = rule.target.value.lower()
        expr.extend([target_lower])

        if rule.comment_tag:
            expr.extend(["comment", f'"{rule.comment_tag}"'])

        return expr


__all__ = ["NFTablesTracker", "FirewallRuleResult"]
