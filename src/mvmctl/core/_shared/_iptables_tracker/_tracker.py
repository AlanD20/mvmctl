"""Idempotent iptables rule management with database synchronization.

This module provides IPTablesTracker for creating/removing iptables rules
and synchronizing them with the database.
"""

from __future__ import annotations

import logging
import shlex
import subprocess
from dataclasses import dataclass
from enum import Enum
from typing import Optional

from mvmctl.constants import CONST_IPTABLES_MAX_COMMENT_LEN
from mvmctl.exceptions import IPTablesTrackerError
from mvmctl.models.network import (
    IPTablesChain,
    IPTablesPort,
    IPTablesProtocol,
    IPTablesRuleItem,
    IPTablesRuleType,
    IPTablesTable,
    IPTablesWildcard,
)
from mvmctl.utils._system import privileged_cmd
from mvmctl.utils.network import NetworkUtils

from ._repository import IPTablesRuleRepository

logger = logging.getLogger(__name__)


@dataclass
class IPTablesRuleResult:
    """Result of a rule operation."""

    success: bool
    rule: Optional[IPTablesRuleItem] = None
    error_message: Optional[str] = None
    command_executed: Optional[list[str]] = None


class IPTablesTracker:
    """Idempotent iptables rule manager with database synchronization.

    This class handles iptables subprocess calls and synchronizes rules
    with the database. Rules are stored in the iptables_rules table.

    Usage:
        tracker = IPTablesTracker()  # Creates own DB instance
        # or
        tracker = IPTablesTracker(repo=existing_repo_instance)

        result = tracker.ensure_rule(
            table="nat", chain="MVM-POSTROUTING",
            source="10.0.0.0/24", out_interface="eth0",
            target="MASQUERADE", network_id="net-abc123",
            network_name="my-network"
        )
        # Rule is automatically stored in database
    """

    COMMENT_PREFIX = "mvm"
    MAX_COMMENT_LEN = CONST_IPTABLES_MAX_COMMENT_LEN

    class RuleAction(str, Enum):
        """iptables action types for command building."""

        CHECK = "-C"
        APPEND = "-A"
        DELETE = "-D"

    def __init__(self, repo: IPTablesRuleRepository) -> None:
        """Initialize IPTablesTracker with optional database instance."""
        self._repo = repo

    def ensure_rule(
        self, rule: IPTablesRuleItem, *, context: str = ""
    ) -> IPTablesRuleResult:
        """Idempotently ensure a rule exists in iptables and database.

        1. Check if rule exists in database by unique attributes
        2. Check if rule exists in iptables (iptables -C)
        3. If not in iptables, create it (iptables -A)
        4. If not in database, insert it; if in DB but inactive, reactivate it
        5. Return rule metadata

        Args:
            rule: IPTablesRuleItem dataclass containing all rule parameters.
            context: Optional context string for comment (e.g., "nocloud:vm123").

        Returns:
            IPTablesRuleResult with success status and rule metadata.
        """
        # Build comment if not already set
        if not rule.comment_tag:
            rule.comment_tag = self._build_comment(
                rule.rule_type, rule.network_name or "", context
            )

        # Generate command strings
        check_args = self._build_iptables_args(rule, self.RuleAction.CHECK)
        add_args = self._build_iptables_args(rule, self.RuleAction.APPEND)
        rule.command_string = " ".join(shlex.quote(arg) for arg in add_args)

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

        # Check if rule exists in iptables
        iptables_exists = False
        try:
            subprocess.run(
                privileged_cmd(check_args),
                capture_output=True,
                check=True,
            )
            iptables_exists = True
        except subprocess.CalledProcessError:
            pass

        # If rule exists in both DB and iptables, update verification timestamp
        if existing_db_rule and iptables_exists:
            if existing_db_rule.id is not None:
                self._repo.update_verified_at(existing_db_rule.id)
            return IPTablesRuleResult(success=True, rule=existing_db_rule)

        # If rule exists in iptables but not in DB, record it
        if iptables_exists and not existing_db_rule:
            recorded_rule = self._repo.insert(rule)
            return IPTablesRuleResult(success=True, rule=recorded_rule)

        # Create the rule in iptables
        try:
            subprocess.run(
                privileged_cmd(add_args),
                capture_output=True,
                check=True,
            )
        except subprocess.CalledProcessError as e:
            return IPTablesRuleResult(
                success=False,
                error_message=f"Failed to create rule: {e.stderr.decode()}",
                command_executed=add_args,
            )

        # Record in database (insert new or reactivate existing)
        if existing_db_rule:
            # Reactivate existing rule
            if existing_db_rule.id is not None:
                self._repo.update_verified_at(existing_db_rule.id)
            rule.id = existing_db_rule.id
            recorded_rule = rule
        else:
            recorded_rule = self._repo.insert(rule)

        return IPTablesRuleResult(
            success=True,
            rule=recorded_rule,
            command_executed=add_args,
        )

    def remove_rule(self, rule: IPTablesRuleItem) -> IPTablesRuleResult:
        """Remove a specific rule from iptables and mark as deleted in database.

        Best-effort removal - if rule doesn't exist in iptables, still returns success.
        Also marks the rule as inactive in the database if found.

        Returns:
            RuleOperationResult with success status
        """
        # Find the rule in database first to get its comment_tag
        db_rule_id = rule.id
        effective_rule = rule

        if db_rule_id is None:
            existing_rule = self._repo.find_by_attributes(
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
            if existing_rule:
                db_rule_id = existing_rule.id
                # Use the DB rule's comment_tag so iptables -D can match
                if not rule.comment_tag and existing_rule.comment_tag:
                    effective_rule = existing_rule

        delete_args = self._build_iptables_args(
            effective_rule, self.RuleAction.DELETE
        )

        # Remove from iptables
        delete_result = subprocess.run(
            privileged_cmd(delete_args),
            capture_output=True,
            check=False,
        )

        if delete_result.returncode != 0:
            # Deletion failed. This can happen because the rule spec doesn't
            # match (e.g. comment mismatch). We can't trust iptables -C to tell
            # us if the rule is gone, because -C also fails when the comment
            # doesn't match. Fallback: delete by line number.
            if not self._remove_by_line_number(effective_rule):
                # Best-effort: check if any rule with these interfaces remains
                if not self._rule_exists_by_interfaces(effective_rule):
                    pass  # Rule is truly gone
                else:
                    stderr = delete_result.stderr.decode()
                    return IPTablesRuleResult(
                        success=False,
                        error_message=f"Failed to remove rule: {stderr}",
                        command_executed=delete_args,
                    )

        # Mark as deleted in database if we found it
        if db_rule_id is not None:
            self._repo.mark_deleted(db_rule_id)

        return IPTablesRuleResult(
            success=True,
            rule=effective_rule,
            command_executed=delete_args,
        )

    def _remove_by_line_number(
        self,
        rule: IPTablesRuleItem,
    ) -> bool:
        """Remove a rule by scanning iptables output and deleting by line number.

        Fallback when iptables -D fails (e.g. comment mismatch).
        Matches rules by in_interface and out_interface.

        Returns:
            True if a matching rule was found and removed.
        """
        list_cmd = [
            "iptables",
            "-t",
            rule.table_name,
            "-L",
            rule.chain_name,
            "-n",
            "--line-numbers",
            "-v",
        ]
        result = subprocess.run(
            privileged_cmd(list_cmd),
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return False

        in_iface = rule.in_interface
        out_iface = rule.out_interface
        if in_iface == IPTablesWildcard.ANY_INTERFACE:
            in_iface = "*"
        if out_iface == IPTablesWildcard.ANY_INTERFACE:
            out_iface = "*"

        for line in result.stdout.splitlines():
            parts = line.split()
            if len(parts) < 9:
                continue
            # Format: num pkts bytes target prot opt in out source destination
            try:
                line_in = parts[6]
                line_out = parts[7]
                if line_in == in_iface and line_out == out_iface:
                    line_num = parts[0]
                    del_cmd = [
                        "iptables",
                        "-t",
                        rule.table_name,
                        "-D",
                        rule.chain_name,
                        line_num,
                    ]
                    del_result = subprocess.run(
                        privileged_cmd(del_cmd),
                        capture_output=True,
                        check=False,
                    )
                    return del_result.returncode == 0
            except (IndexError, ValueError):
                continue
        return False

    def _rule_exists_by_interfaces(
        self,
        rule: IPTablesRuleItem,
    ) -> bool:
        """Check if a rule with the given interfaces exists in the chain.

        Uses iptables -L output, matching only by in/out interfaces.
        """
        list_cmd = [
            "iptables",
            "-t",
            rule.table_name,
            "-L",
            rule.chain_name,
            "-n",
            "-v",
        ]
        result = subprocess.run(
            privileged_cmd(list_cmd),
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return False

        in_iface = rule.in_interface
        out_iface = rule.out_interface
        if in_iface == IPTablesWildcard.ANY_INTERFACE:
            in_iface = "*"
        if out_iface == IPTablesWildcard.ANY_INTERFACE:
            out_iface = "*"

        for line in result.stdout.splitlines():
            parts = line.split()
            if len(parts) < 9:
                continue
            try:
                line_in = parts[6]
                line_out = parts[7]
                if line_in == in_iface and line_out == out_iface:
                    return True
            except (IndexError, ValueError):
                continue
        return False

    def _build_iptables_args(
        self,
        rule: IPTablesRuleItem,
        action: RuleAction,
    ) -> list[str]:
        """Build iptables command arguments from rule specification."""
        args = [
            "iptables",
            "-t",
            rule.table_name,
            action.value,
            rule.chain_name,
        ]

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

        args.extend(["-j", rule.target.value])

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
            subprocess.run(
                privileged_cmd(cmd_create), check=True, capture_output=True
            )
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
            raise IPTablesTrackerError(
                f"Failed to create {chain_name_str} chain"
            ) from e

        # Add jump rule if requested
        if created and auto_jump_from:
            jump_result = self.ensure_jump_rule(
                auto_jump_from, chain_name_str, table, position
            )
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
            logger.debug(
                "Jump rule %s -> %s already exists", from_chain, to_chain
            )
            return IPTablesRuleResult(success=True)

        # Insert jump rule at specified position
        cmd_insert = [
            "iptables",
            "-t",
            table,
            "-I",
            from_chain,
            str(position),
            "-j",
            to_chain,
        ]
        try:
            subprocess.run(
                privileged_cmd(cmd_insert), check=True, capture_output=True
            )
            logger.debug(
                "Inserted jump rule %s -> %s at position %d",
                from_chain,
                to_chain,
                position,
            )
            return IPTablesRuleResult(success=True, command_executed=cmd_insert)
        except subprocess.CalledProcessError as e:
            stderr = ""
            if isinstance(e.stderr, bytes):
                stderr = e.stderr.decode(errors="ignore")
            elif isinstance(e.stderr, str):
                stderr = e.stderr
            error_msg = (
                f"Failed to add jump rule {from_chain} -> {to_chain}: {stderr}"
            )
            logger.error(error_msg)
            return IPTablesRuleResult(success=False, error_message=error_msg)

    def flush_chain(
        self,
        chain_name: IPTablesChain,
        table: IPTablesTable = IPTablesTable.FILTER,
    ) -> bool:
        """Flush all rules from an iptables chain and mark them deleted in DB.

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
            logger.debug(
                "Chain %s doesn't exist, nothing to flush", chain_name_str
            )
            return False

        # Flush the chain in iptables
        cmd_flush = ["iptables", "-t", table, "-F", chain_name_str]
        try:
            subprocess.run(
                privileged_cmd(cmd_flush), check=True, capture_output=True
            )
            logger.debug("Flushed all rules from chain %s", chain_name_str)
        except subprocess.CalledProcessError as e:
            raise IPTablesTrackerError(
                f"Failed to flush {chain_name_str} chain"
            ) from e

        # Mark all rules for this chain as deleted in database
        deleted_count = self._repo.mark_deleted_by_table_chain_name(
            table, chain_name
        )
        logger.debug(
            "Marked %d rules as deleted for chain %s",
            deleted_count,
            chain_name_str,
        )

        return True

    def remove_chain(
        self,
        chain_name: IPTablesChain,
        table: IPTablesTable = IPTablesTable.FILTER,
    ) -> bool:
        """Delete an iptables chain and mark its rules as deleted in DB.

        Args:
            chain_name: Name of the chain to delete.
            table: Table name. Default is filter.

        Returns:
            True if the chain was deleted, False if chain doesn't exist.

        Raises:
            IPTablesTrackerError: If delete operation fails unexpectedly.
        """
        chain_name_str = chain_name.value

        # Check if chain exists
        if not NetworkUtils.chain_exists(chain_name_str, table.value):
            logger.debug(
                "Chain %s doesn't exist, nothing to remove", chain_name_str
            )
            return False

        # Mark all rules for this chain as deleted in database
        # (iptables -X will automatically remove rules when chain is deleted)
        deleted_count = self._repo.mark_deleted_by_table_chain_name(
            table, chain_name
        )
        logger.debug(
            "Marked %d rules as deleted for chain %s",
            deleted_count,
            chain_name_str,
        )

        # Delete the chain (iptables automatically removes rules)
        cmd_delete = ["iptables", "-t", table.value, "-X", chain_name_str]
        try:
            subprocess.run(
                privileged_cmd(cmd_delete), check=True, capture_output=True
            )
            logger.debug("Deleted chain %s", chain_name_str)
        except subprocess.CalledProcessError as e:
            raise IPTablesTrackerError(
                f"Failed to delete {chain_name_str} chain: {e.stderr.decode() if e.stderr else e}"
            ) from e

        return True


__all__ = ["IPTablesTracker", "IPTablesRuleResult"]
