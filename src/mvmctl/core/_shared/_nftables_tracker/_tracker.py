"""
Idempotent nftables rule management with database synchronization.

This module provides NFTablesTracker for creating/removing nftables rules
and synchronizing them with the nftables_rules database table.
"""

from __future__ import annotations

import logging

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
)
from mvmctl.models.network import FirewallRuleResult
from mvmctl.utils._system import run_cmd

logger = logging.getLogger(__name__)

# Name and address family of the nftables table used by mvmctl.
# Uses ``inet`` family so both IPv4 and IPv6 rules
# (including filter AND nat) live in a single table.
_MVM_NFT_FAMILY = "inet"
_MVM_NFT_TABLE = "mvmctl"


class NFTablesTracker:
    """
    Idempotent nftables rule manager with database synchronization.

    This class handles nft subprocess calls and synchronizes rules
    with the nftables_rules table.
    """

    def __init__(self, repo: NFTablesRuleRepository) -> None:
        """Initialize NFTablesTracker with repository."""
        self._repo = repo

    # ── Table & Chain Management ──────────────────────────────────────

    def initialize(self) -> None:
        """
        Ensure the mvmctl nftables table and base chains exist.

        Checks if the table exists via ``nft list tables``. If not found,
        creates the table with base chains:
        - MVM-FORWARD (filter, forward hook)
        - MVM-POSTROUTING (nat, postrouting hook)
        - MVM-NOCLOUDNET-INPUT (filter, input hook)
        """
        if self._table_exists():
            logger.debug("nftables table %s already exists", _MVM_NFT_TABLE)
            return

        nft_script = (
            f"add table {_MVM_NFT_FAMILY} {_MVM_NFT_TABLE}\n"
            f"add chain {_MVM_NFT_FAMILY} {_MVM_NFT_TABLE} {FirewallChain.MVM_FORWARD.value} {{ type filter hook forward priority 0; policy accept; }}\n"
            f"add chain {_MVM_NFT_FAMILY} {_MVM_NFT_TABLE} {FirewallChain.MVM_POSTROUTING.value} {{ type nat hook postrouting priority srcnat; policy accept; }}\n"
            f"add chain {_MVM_NFT_FAMILY} {_MVM_NFT_TABLE} {FirewallChain.MVM_NOCLOUDNET_INPUT.value} {{ type filter hook input priority 0; policy accept; }}\n"
        )
        try:
            run_cmd(
                ["nft", "-f", "-"],
                privileged=True,
                input=nft_script,
            )
            logger.info(
                "Created nftables table %s with base chains", _MVM_NFT_TABLE
            )
        except ProcessError as e:
            raise RuntimeError(
                f"Failed to initialize nftables table {_MVM_NFT_TABLE}: {e}"
            ) from e

    def _table_exists(self) -> bool:
        """Check if the mvmctl nftables table exists."""
        try:
            result = run_cmd(
                ["nft", "list", "tables"],
                privileged=True,
            )
            for line in result.stdout.splitlines():
                if f"table {_MVM_NFT_FAMILY} {_MVM_NFT_TABLE}" in line:
                    return True
            return False
        except ProcessError:
            return False

    def ensure_chain(
        self,
        chain_name: FirewallChain,
        table: FirewallTable = FirewallTable.FILTER,
        auto_jump_from: str | None = None,
        position: int = 1,
    ) -> bool:
        """
        Ensure a custom chain exists in the mvmctl table.

        For nftables, the base hook chains (FORWARD, POSTROUTING, INPUT)
        and their jump rules to MVM chains are set up once by
        :meth:`initialize`.  This method only creates the custom chain
        if missing — the ``auto_jump_from`` and ``position`` parameters
        are accepted for signature compatibility with ``IPTablesTracker``
        but are ignored.

        Args:
            chain_name: Name of the chain to create (enum value).
            table: Ignored (nftables uses a single ``inet`` table).
            auto_jump_from: Ignored (jump rules are set up in ``initialize``).
            position: Ignored.

        Returns:
            True if the chain was created, False if it already existed.

        """
        cmd_check = [
            "nft",
            "list",
            "chain",
            _MVM_NFT_FAMILY,
            _MVM_NFT_TABLE,
            chain_name,
        ]
        result = run_cmd(
            cmd_check,
            privileged=True,
            check=False,
        )
        if result.returncode == 0:
            logger.debug(
                "Chain %s already exists in %s/%s",
                chain_name,
                _MVM_NFT_TABLE,
                table,
            )
            return False

        try:
            run_cmd(
                [
                    "nft",
                    "add",
                    "chain",
                    _MVM_NFT_FAMILY,
                    _MVM_NFT_TABLE,
                    chain_name,
                ],
                privileged=True,
            )
            logger.debug(
                "Created chain %s in %s/%s",
                chain_name,
                _MVM_NFT_FAMILY,
                _MVM_NFT_TABLE,
            )
            return True
        except ProcessError as e:
            raise RuntimeError(
                f"Failed to create chain {chain_name}: {e}"
            ) from e

    # ── Handle helpers ──────────────────────────────────────────────────

    def _list_chain_rules(self, chain: FirewallChain) -> list[tuple[int, str]]:
        """List all rules in an nftables chain with their handles.

        Returns list of ``(handle, rule_text)`` tuples parsed from
        ``nft -a list chain ...`` output.
        """
        result = run_cmd(
            [
                "nft",
                "-a",
                "list",
                "chain",
                _MVM_NFT_FAMILY,
                _MVM_NFT_TABLE,
                chain.value,
            ],
            privileged=True,
        )
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
        # nft -a output format: \t\t<rule_text> # handle N
        expected = " ".join(nft_expr)

        for handle, rule_text in self._list_chain_rules(rule.chain_name):
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

        # Add rule
        add_cmd = [
            "nft",
            "add",
            "rule",
            _MVM_NFT_FAMILY,
            _MVM_NFT_TABLE,
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
            _MVM_NFT_FAMILY,
            _MVM_NFT_TABLE,
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
        Ensure multiple rules exist in nftables and database in a single ``nft -f -`` call.

        Generates one ``add rule`` statement per rule, pipes the full script
        to ``nft -f -``, and inserts all new rules into the database after success.

        Args:
            rules: List of FirewallRule to ensure.

        Returns:
            FirewallRuleResult indicating batch success/failure.

        """
        lines: list[str] = []
        new_rules: list[FirewallRule] = []

        for rule in rules:
            # Skip rules already in the database
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
                continue

            nft_expr = self._rule_to_nft_expr(rule)
            add_stmt = (
                f"add rule {_MVM_NFT_FAMILY} {_MVM_NFT_TABLE} "
                f"{rule.chain_name.value} {' '.join(nft_expr)}"
            )
            lines.append(add_stmt)
            new_rules.append(rule)

        if not lines:
            return FirewallRuleResult(success=True)

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
                error_message=f"Batch nftables rule creation failed: {e}",
                command_executed=nft_script,
            )

        inserted: list[FirewallRule] = []
        for rule in new_rules:
            recorded = self._repo.insert(rule)
            inserted.append(recorded)

        logger.info("Batch-inserted %d nftables rules", len(inserted))
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
                continue

            del_cmd = [
                "nft",
                "delete",
                "rule",
                _MVM_NFT_FAMILY,
                _MVM_NFT_TABLE,
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

    def teardown(self) -> None:
        """Delete the entire mvmctl nftables table and all its contents.

        Best-effort — uses ``check=False`` so that an already-missing table
        is handled silently.
        Always returns ``None``.
        """
        run_cmd(
            ["nft", "delete", "table", _MVM_NFT_FAMILY, _MVM_NFT_TABLE],
            privileged=True,
            check=False,
        )

    def flush_chain(
        self,
        chain: FirewallChain,
        table_name: FirewallTable = FirewallTable.FILTER,
    ) -> bool:
        """
        Flush all rules from an nftables chain and mark them deleted in DB.

        Args:
            chain: Chain to flush.
            table_name: Table name. Default is filter.

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
                    _MVM_NFT_FAMILY,
                    _MVM_NFT_TABLE,
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
