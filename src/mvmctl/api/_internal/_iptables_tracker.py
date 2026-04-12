"""Idempotent iptables rule management.

This module provides IPTablesTracker for creating/removing iptables rules.

IMPORTANT: This is a Core layer module. It does NOT access the database directly.
Database operations are the responsibility of the API layer.

For synchronization between DB and iptables, see api/network_sync.py
"""

from __future__ import annotations

import logging
import shlex
import subprocess
from dataclasses import dataclass
from enum import Enum
from typing import Optional

from mvmctl.constants import CONST_IPTABLES_MAX_COMMENT_LEN
from mvmctl.db.models import (
    IPTablesChain,
    IPTablesPort,
    IPTablesProtocol,
    IPTablesRule,
    IPTablesRuleType,
    IPTablesTable,
    IPTablesWildcard,
)
from mvmctl.exceptions import IPTablesTrackerError
from mvmctl.utils.process import privileged_cmd

logger = logging.getLogger(__name__)


@dataclass
class IPTablesRuleResult:
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

    def ensure_rule(self, rule: IPTablesRule, *, context: str = "") -> IPTablesRuleResult:
        """Idempotently ensure a rule exists in iptables.

        1. Check if rule exists (iptables -C)
        2. If not exists, create it (iptables -A)
        3. Return rule metadata for API layer to store

        Args:
            rule: IPTablesRule dataclass containing all rule parameters.
            context: Optional context string for comment (e.g., "nocloud:vm123").

        Returns:
            IPTablesRuleResult with success status and rule metadata.
        """
        # Build comment if not already set
        if not rule.comment_tag:
            rule.comment_tag = self._build_comment(rule.rule_type, rule.network_name or "", context)

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
            return IPTablesRuleResult(success=True, rule=rule)
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
            return IPTablesRuleResult(
                success=True,
                rule=rule,
                command_executed=add_args,
            )
        except subprocess.CalledProcessError as e:
            return IPTablesRuleResult(
                success=False,
                error_message=f"Failed to create rule: {e.stderr.decode()}",
                command_executed=add_args,
            )

    def remove_rule(self, rule: IPTablesRule) -> IPTablesRuleResult:
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
            return IPTablesRuleResult(
                success=True,
                rule=rule,
                command_executed=delete_args,
            )
        except subprocess.CalledProcessError as e:
            # Check if rule just didn't exist (which is fine for idempotent removal)
            stderr = e.stderr.decode()
            if "No chain/target/match by that name" in stderr:
                return IPTablesRuleResult(
                    success=True,  # Idempotent - rule already gone
                    rule=rule,
                    command_executed=delete_args,
                )
            return IPTablesRuleResult(
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

    def ensure_chain(
        self,
        chain_name: IPTablesChain,
        table: IPTablesTable = IPTablesTable.FILTER,
        auto_jump_from: Optional[str] = None,
        position: int = 1,
    ) -> bool:
        """Create an iptables chain if it doesn't exist.

        Args:
            chain_name: Name of the chain to create (enum value).
            table: Table name. Default is filter.
            auto_jump_from: Optional standard chain to jump from (e.g., "INPUT").

        Returns:
            True if the chain was created, False if it already existed.

        Raises:
            NetworkError: If chain creation fails.
        """
        chain_name_str = chain_name.value

        # Check if chain exists
        cmd_check = ["iptables", "-t", table, "-L", chain_name_str, "-n"]
        result = subprocess.run(
            privileged_cmd(cmd_check),
            capture_output=True,
            check=False,
        )
        if result.returncode == 0:
            logger.debug("Chain %s already exists", chain_name_str)
            return False

        # Create the chain
        cmd_create = ["iptables", "-t", table, "-N", chain_name_str]
        created = False
        try:
            subprocess.run(privileged_cmd(cmd_create), check=True, capture_output=True)
            logger.debug("Created iptables chain %s", chain_name_str)
            created = True
        except subprocess.CalledProcessError as e:
            stderr = ""
            if isinstance(e.stderr, bytes):
                stderr = e.stderr.decode(errors="ignore")
            elif isinstance(e.stderr, str):
                stderr = e.stderr
            if "Chain already exists" in stderr:
                logger.debug("Chain %s already exists", chain_name_str)
                return False
            raise IPTablesTrackerError(f"Failed to create {chain_name_str} chain") from e

        # Add jump rule if requested
        if created and auto_jump_from:
            jump_result = self.ensure_jump_rule(auto_jump_from, chain_name_str, table, position)
            if not jump_result.success:
                raise IPTablesTrackerError(
                    f"Failed to add jump rule {auto_jump_from} -> {chain_name_str}: {jump_result.error_message}"
                )

        return created

    def ensure_jump_rule(
        self,
        from_chain: str,
        to_chain: str,
        table: IPTablesTable = IPTablesTable.FILTER,
        position: int = 1,
    ) -> IPTablesRuleResult:
        """Idempotently ensure a jump rule exists from a standard chain to a custom chain.

        This creates the jump rule that directs traffic from a standard iptables chain
        (e.g., INPUT) to a custom chain (e.g., MVM-NOCLOUDNET-INPUT).

        Args:
            from_chain: Source chain name (e.g., "INPUT").
            to_chain: Target custom chain name (e.g., "MVM-NOCLOUDNET-INPUT").
            table: Table name. Default is filter.
            position: Position to insert the rule (1 = top). Default is 1.

        Returns:
            IPTablesRuleResult with success status.
        """
        # Check if jump rule exists
        cmd_check = ["iptables", "-t", table, "-C", from_chain, "-j", to_chain]
        result = subprocess.run(
            privileged_cmd(cmd_check),
            capture_output=True,
            check=False,
        )

        if result.returncode == 0:
            logger.debug("Jump rule %s -> %s already exists", from_chain, to_chain)
            return IPTablesRuleResult(success=True)

        # Insert jump rule at specified position
        cmd_insert = ["iptables", "-t", table, "-I", from_chain, str(position), "-j", to_chain]
        try:
            subprocess.run(privileged_cmd(cmd_insert), check=True, capture_output=True)
            logger.debug(
                "Inserted jump rule %s -> %s at position %d", from_chain, to_chain, position
            )
            return IPTablesRuleResult(success=True, command_executed=cmd_insert)
        except subprocess.CalledProcessError as e:
            stderr = ""
            if isinstance(e.stderr, bytes):
                stderr = e.stderr.decode(errors="ignore")
            elif isinstance(e.stderr, str):
                stderr = e.stderr
            error_msg = f"Failed to add jump rule {from_chain} -> {to_chain}: {stderr}"
            logger.error(error_msg)
            return IPTablesRuleResult(success=False, error_message=error_msg)

    def flush_chain(
        self, chain_name: IPTablesChain, table: IPTablesTable = IPTablesTable.FILTER
    ) -> bool:
        """Flush all rules from an iptables chain.

        Args:
            chain_name: Name of the chain to flush.
            table: Table name. Default is filter.

        Returns:
            True if the chain was flushed, False if chain doesn't exist.

        Raises:
            IPTablesTrackerError: If flush operation fails unexpectedly.
        """

        chain_name_str = chain_name.value

        # Check if chain exists first
        cmd_check = ["iptables", "-t", table, "-L", chain_name_str, "-n"]
        result = subprocess.run(
            privileged_cmd(cmd_check),
            capture_output=True,
            check=False,
        )
        if result.returncode != 0:
            logger.debug("Chain %s doesn't exist, nothing to flush", chain_name_str)
            return False

        # Flush the chain
        cmd_flush = ["iptables", "-t", table, "-F", chain_name_str]
        try:
            subprocess.run(privileged_cmd(cmd_flush), check=True, capture_output=True)
            logger.debug("Flushed all rules from chain %s", chain_name_str)
            return True
        except subprocess.CalledProcessError as e:
            raise IPTablesTrackerError(f"Failed to flush {chain_name_str} chain") from e


__all__ = ["IPTablesTracker", "IPTablesRuleResult"]
