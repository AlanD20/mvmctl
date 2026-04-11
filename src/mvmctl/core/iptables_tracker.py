"""Idempotent iptables rule management.

This module provides IPTablesTracker for creating/removing iptables rules.

IMPORTANT: This is a Core layer module. It does NOT access the database directly.
Database operations are the responsibility of the API layer.

For synchronization between DB and iptables, see api/network_sync.py
"""

from __future__ import annotations

import shlex
import subprocess
from dataclasses import dataclass
from enum import Enum
from typing import Optional

from mvmctl.constants import CONST_IPTABLES_MAX_COMMENT_LEN
from mvmctl.db.models import (
    IPTablesPort,
    IPTablesProtocol,
    IPTablesRule,
    IPTablesRuleType,
    IPTablesWildcard,
)
from mvmctl.utils.process import privileged_cmd


@dataclass
class RuleOperationResult:
    """Result of a rule operation."""

    success: bool
    rule: Optional[IPTablesRule] = None
    error_message: Optional[str] = None
    command_executed: Optional[list[str]] = None


class IPTablesTracker:
    """Idempotent iptables rule manager.

    This class handles the actual iptables subprocess calls.
    It does NOT interact with the database - that is the API layer's responsibility.

    Usage (by API layer):
        tracker = IPTablesTracker()
        result = tracker.ensure_rule(
            table="nat", chain="MVM-POSTROUTING",
            source="10.0.0.0/24", out_interface="eth0",
            target="MASQUERADE", network_id="net-abc123",
            network_name="my-network"
        )
        # API layer should store result.rule to database
    """

    COMMENT_PREFIX = "mvm"
    MAX_COMMENT_LEN = CONST_IPTABLES_MAX_COMMENT_LEN

    class RuleAction(str, Enum):
        """iptables action types for command building."""

        CHECK = "-C"
        APPEND = "-A"
        DELETE = "-D"

    def ensure_rule(
        self,
        table: str,
        chain: str,
        rule_type: IPTablesRuleType,
        target: str,
        network_id: str,
        network_name: str,
        *,
        protocol: Optional[str] = None,
        source: Optional[str] = None,
        destination: Optional[str] = None,
        in_interface: Optional[str] = None,
        out_interface: Optional[str] = None,
        sport: Optional[int] = None,
        dport: Optional[int] = None,
        context: str = "",
    ) -> RuleOperationResult:
        """Idempotently ensure a rule exists in iptables.

        1. Check if rule exists (iptables -C)
        2. If not exists, create it (iptables -A)
        3. Return rule metadata for API layer to store

        Args:
            network_id: Database ID for foreign key
            network_name: Human-readable name for comment (e.g., "production-net")

        Returns:
            RuleOperationResult with success status and rule metadata
        """
        # Build the rule specification (use network_name for human-readable comment)
        comment = self._build_comment(rule_type, network_name, context)

        rule = IPTablesRule(
            table_name=table,
            chain_name=chain,
            rule_type=rule_type,
            target=target,
            network_id=network_id,
            protocol=IPTablesProtocol(protocol) if protocol else IPTablesProtocol.ALL,
            source=source if source else IPTablesWildcard.ANY_CIDR,
            destination=destination if destination else IPTablesWildcard.ANY_CIDR,
            in_interface=in_interface if in_interface else IPTablesWildcard.ANY_INTERFACE,
            out_interface=out_interface if out_interface else IPTablesWildcard.ANY_INTERFACE,
            sport=sport if sport else IPTablesPort.ANY,
            dport=dport if dport else IPTablesPort.ANY,
            is_active=True,
            network_name=network_name,
            comment_tag=comment,
        )

        # Generate command strings
        check_args = self._build_iptables_args(rule, self.RuleAction.CHECK)
        add_args = self._build_iptables_args(rule, self.RuleAction.APPEND)
        rule.command_string = " ".join(shlex.quote(arg) for arg in add_args)

        # Check if rule exists
        try:
            subprocess.run(
                privileged_cmd(check_args),
                capture_output=True,
                check=True,
            )
            # Rule exists - return success with existing rule info
            return RuleOperationResult(success=True, rule=rule)
        except subprocess.CalledProcessError:
            # Rule doesn't exist - need to create it
            pass

        # Create the rule
        try:
            subprocess.run(
                privileged_cmd(add_args),
                capture_output=True,
                check=True,
            )
            return RuleOperationResult(
                success=True,
                rule=rule,
                command_executed=add_args,
            )
        except subprocess.CalledProcessError as e:
            return RuleOperationResult(
                success=False,
                error_message=f"Failed to create rule: {e.stderr.decode()}",
                command_executed=add_args,
            )

    def remove_rule(self, rule: IPTablesRule) -> RuleOperationResult:
        """Remove a specific rule from iptables.

        Best-effort removal - if rule doesn't exist, still returns success.

        Returns:
            RuleOperationResult with success status
        """
        delete_args = self._build_iptables_args(rule, self.RuleAction.DELETE)

        try:
            subprocess.run(
                privileged_cmd(delete_args),
                capture_output=True,
                check=True,
            )
            return RuleOperationResult(
                success=True,
                rule=rule,
                command_executed=delete_args,
            )
        except subprocess.CalledProcessError as e:
            # Check if rule just didn't exist (which is fine for idempotent removal)
            stderr = e.stderr.decode()
            if "No chain/target/match by that name" in stderr:
                return RuleOperationResult(
                    success=True,  # Idempotent - rule already gone
                    rule=rule,
                    command_executed=delete_args,
                )
            return RuleOperationResult(
                success=False,
                error_message=f"Failed to remove rule: {stderr}",
                command_executed=delete_args,
            )

    def _build_iptables_args(
        self,
        rule: IPTablesRule,
        action: RuleAction,
    ) -> list[str]:
        """Build iptables command arguments from rule specification."""
        args = ["iptables", "-t", rule.table_name, action.value, rule.chain_name]

        # Only add -p flag if protocol is not ALL (wildcard)
        if rule.protocol != IPTablesProtocol.ALL:
            args.extend(["-p", rule.protocol.value])

        if rule.source != IPTablesWildcard.ANY_CIDR:
            args.extend(["-s", rule.source])

        if rule.destination != IPTablesWildcard.ANY_CIDR:
            args.extend(["-d", rule.destination])

        if rule.in_interface != IPTablesWildcard.ANY_INTERFACE:
            args.extend(["-i", rule.in_interface])

        if rule.out_interface != IPTablesWildcard.ANY_INTERFACE:
            args.extend(["-o", rule.out_interface])

        if rule.sport != IPTablesPort.ANY:
            args.extend(["--sport", str(rule.sport)])

        if rule.dport != IPTablesPort.ANY:
            args.extend(["--dport", str(rule.dport)])

        args.extend(["-j", rule.target])

        if rule.comment_tag:
            args.extend(["-m", "comment", "--comment", rule.comment_tag])

        return args

    def _build_comment(
        self,
        rule_type: IPTablesRuleType,
        network_name: str,
        context: str,
    ) -> str:
        """Build standardized comment tag using network name (human readable)."""
        comment = f"{self.COMMENT_PREFIX}:{rule_type.value}:{network_name}"
        if context:
            comment = f"{comment}:{context}"
        # Truncate if exceeds max length
        if len(comment) > self.MAX_COMMENT_LEN:
            comment = comment[: self.MAX_COMMENT_LEN]
        return comment
