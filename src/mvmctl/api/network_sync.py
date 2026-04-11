"""Synchronizes iptables rules with database state.

This module lives in the API layer because it accesses the database.
It orchestrates between DB state and actual iptables rules using
IPTablesTracker from the Core layer for iptables operations.

DB is source of truth:
- Rules in DB but missing in iptables → CREATE
- Rules in iptables but missing in DB → DELETE (orphaned)
- Rules in both → VERIFY (update last_verified_at)
"""

from __future__ import annotations

import shlex
import subprocess
from dataclasses import dataclass, field
from typing import Optional

from mvmctl.core.iptables_tracker import IPTablesTracker, RuleOperationResult
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import IPTablesRule
from mvmctl.exceptions import NetworkError


@dataclass
class ParsedRule:
    """Rule parsed from iptables-save output."""

    table_name: str
    chain_name: str
    rule_spec: str
    comment: Optional[str] = None


@dataclass
class SyncStatus:
    """Status of a sync operation."""

    to_create: list[IPTablesRule] = field(default_factory=list)
    to_delete: list[ParsedRule] = field(default_factory=list)
    verified: list[IPTablesRule] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)


class IPTablesSynchronizer:
    """Synchronizes database state with actual iptables rules.

    DB is source of truth:
    - Rules in DB but missing in iptables → CREATE
    - Rules in iptables but missing in DB → DELETE (orphaned)
    - Rules in both → VERIFY (update last_verified_at)

    IMPORTANT: Only operates on MVM-* chains.
    """

    MVM_CHAIN_PREFIX = "MVM-"

    def __init__(self, db: Optional[MVMDatabase] = None):
        self.db = db or MVMDatabase()
        self.tracker = IPTablesTracker()

    def sync_network(
        self,
        network_id: str,
        *,
        dry_run: bool = False,
    ) -> SyncStatus:
        """Sync iptables rules for a specific network.

        Args:
            network_id: The network ID to sync
            dry_run: If True, only report what would be done

        Returns:
            SyncStatus with categorized rules
        """
        status = SyncStatus()

        expected_rules = self.db.get_iptables_rules_for_network(network_id, active_only=True)
        actual_rules = self._parse_mvm_chains()

        expected_sigs = {self._rule_signature(r): r for r in expected_rules}
        actual_sigs = {self._rule_signature(r): r for r in actual_rules}

        for sig, rule in expected_sigs.items():
            if sig not in actual_sigs:
                status.to_create.append(rule)
            else:
                status.verified.append(rule)

        for sig, parsed_rule in actual_sigs.items():
            if sig not in expected_sigs:
                status.to_delete.append(parsed_rule)

        if dry_run:
            return status

        if status.to_create:
            try:
                from mvmctl.api.network import sync_iptables_rules

                sync_iptables_rules(network_id, db=self.db, tracker=self.tracker)
            except NetworkError as e:
                for rule in status.to_create:
                    status.errors.append(f"Failed to create {rule.rule_type} rule: {e}")

        for parsed_rule in status.to_delete:
            result = self._remove_parsed_rule(parsed_rule)
            if not result.success:
                status.errors.append(f"Failed to delete orphaned rule: {result.error_message}")

        for rule in status.verified:
            if rule.id:
                self.db.update_iptables_rule_verified(rule.id)

        return status

    def sync_all_networks(
        self,
        *,
        dry_run: bool = False,
    ) -> dict[str, SyncStatus]:
        """Sync all networks."""
        networks = self.db.list_networks()
        results = {}

        for network in networks:
            results[network.name] = self.sync_network(network.id, dry_run=dry_run)

        return results

    def _parse_mvm_chains(self) -> list[ParsedRule]:
        """Parse all MVM-* chains from iptables-save output."""
        try:
            result = subprocess.run(
                ["iptables-save"],
                capture_output=True,
                text=True,
                check=True,
            )
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to run iptables-save: {e.stderr}")

        rules = []
        current_table = None

        for line in result.stdout.splitlines():
            if line.startswith("*"):
                current_table = line[1:]
                continue

            if line.startswith("-A MVM-"):
                comment = None
                if "--comment" in line:
                    parts = shlex.split(line)
                    for i, part in enumerate(parts):
                        if part == "--comment" and i + 1 < len(parts):
                            comment = parts[i + 1]
                            break

                rule = ParsedRule(
                    table_name=current_table or "filter",
                    chain_name=line.split()[1],
                    rule_spec=line,
                    comment=comment,
                )
                rules.append(rule)

        return rules

    def _rule_signature(self, rule: IPTablesRule | ParsedRule) -> tuple[str | int | None, ...]:
        """Generate unique signature for rule comparison."""
        if isinstance(rule, IPTablesRule):
            return (
                rule.table_name,
                rule.chain_name,
                rule.rule_type.value,
                rule.protocol.value,
                rule.source,
                rule.destination,
                rule.in_interface or "",
                rule.out_interface or "",
                rule.target,
                rule.sport or 0,
                rule.dport or 0,
            )
        return (rule.table_name, rule.chain_name, rule.comment or "", rule.rule_spec)

    def _remove_parsed_rule(self, parsed_rule: ParsedRule) -> RuleOperationResult:
        """Remove a rule that was parsed from iptables-save."""
        args = ["iptables", "-t", parsed_rule.table_name, "-D"]
        args.extend(shlex.split(parsed_rule.rule_spec)[2:])

        try:
            subprocess.run(args, capture_output=True, check=True)
            return RuleOperationResult(success=True)
        except subprocess.CalledProcessError as e:
            return RuleOperationResult(
                success=False, error_message=f"Failed to delete: {e.stderr.decode()}"
            )
