"""NFTables rule database operations — nftables_rules table."""

from __future__ import annotations

from datetime import UTC, datetime
from typing import Any

from mvmctl.core._shared._db import Database
from mvmctl.models import (
    FirewallChain,
    FirewallProtocol,
    FirewallRule,
    FirewallRuleType,
    FirewallTable,
    FirewallTarget,
)


class NFTablesRuleRepository:
    """Database operations for nftables rules (nftables_rules table)."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    def list_all(self) -> list[FirewallRule]:
        """List all nftables rules."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM nftables_rules ORDER BY id",
            ).fetchall()
        return [self._row_to_item(row) for row in rows]

    def list_by_network_id(self, network_id: str) -> list[FirewallRule]:
        """List all nftables rules for a network."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM nftables_rules WHERE network_id = ? ORDER BY id",
                (network_id,),
            ).fetchall()
        return [self._row_to_item(row) for row in rows]

    def list_by_network_id_batch(
        self, network_ids: list[str]
    ) -> list[FirewallRule]:
        """List all nftables rules for multiple networks."""
        if not network_ids:
            return []
        placeholders = ",".join("?" * len(network_ids))
        query = f"SELECT * FROM nftables_rules WHERE network_id IN ({placeholders}) ORDER BY id"
        with self._db.connect() as conn:
            rows = conn.execute(query, network_ids).fetchall()
        return [self._row_to_item(row) for row in rows]

    def get(self, rule_id: int) -> FirewallRule | None:
        """Get a specific rule by ID."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM nftables_rules WHERE id = ?", (rule_id,)
            ).fetchone()
        if row is None:
            return None
        return self._row_to_item(row)

    def get_by_network_id(
        self, network_id: str, active_only: bool = True
    ) -> list[FirewallRule]:
        """Get all nftables rules for a specific network."""
        with self._db.connect() as conn:
            if active_only:
                rows = conn.execute(
                    "SELECT * FROM nftables_rules WHERE network_id = ? AND is_active = 1",
                    (network_id,),
                ).fetchall()
            else:
                rows = conn.execute(
                    "SELECT * FROM nftables_rules WHERE network_id = ?",
                    (network_id,),
                ).fetchall()
        return [self._row_to_item(row) for row in rows]

    def insert(self, rule: FirewallRule) -> FirewallRule:
        """
        Insert a new nftables rule record.

        Returns the rule with the generated id populated.
        """
        with self._db.connect() as conn:
            cursor = conn.execute(
                """
                INSERT INTO nftables_rules (
                    chain, rule_type, table_name, protocol, source, destination,
                    in_interface, out_interface, target, sport, dport,
                    network_id, comment_tag, command_string, created_at, is_active
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    rule.chain_name,
                    rule.rule_type.value,
                    "filter",  # nftables uses a single inet table — no separate nat/filter
                    rule.protocol.value,
                    rule.source,
                    rule.destination,
                    rule.in_interface,
                    rule.out_interface,
                    rule.target.value,
                    rule.sport,
                    rule.dport,
                    rule.network_id,
                    rule.comment_tag,
                    rule.command_string,
                    rule.created_at or datetime.now(tz=UTC).isoformat(),
                    int(rule.is_active),
                ),
            )
            rule.id = cursor.lastrowid
        return rule

    def update_verified_at(self, rule_id: int) -> None:
        """Update the last_verified_at timestamp for a rule."""
        with self._db.connect() as conn:
            conn.execute(
                """UPDATE nftables_rules
                   SET last_verified_at = CURRENT_TIMESTAMP
                   WHERE id = ?""",
                (rule_id,),
            )

    def mark_deleted(self, rule_id: int) -> None:
        """Soft delete a rule (mark is_active=0)."""
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE nftables_rules SET is_active = 0 WHERE id = ?",
                (rule_id,),
            )

    def mark_deleted_by_chain(self, chain: FirewallChain) -> int:
        """
        Soft delete all active rules for a specific chain.

        Marks all rules with is_active=1 for the given chain as is_active=0.
        Returns the number of rules marked as deleted.
        """
        with self._db.connect() as conn:
            cursor = conn.execute(
                """UPDATE nftables_rules
                   SET is_active = 0
                   WHERE chain = ? AND is_active = 1""",
                (chain.value,),
            )
        return cursor.rowcount

    def update_handle(self, rule_id: int, nft_handle: int) -> None:
        """Update the nft handle for a rule."""
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE nftables_rules SET nft_handle = ? WHERE id = ?",
                (nft_handle, rule_id),
            )

    def find_by_attributes(
        self,
        table_name: FirewallTable,
        chain_name: FirewallChain,
        rule_type: FirewallRuleType,
        network_id: str,
        protocol: FirewallProtocol,
        source: str,
        destination: str,
        in_interface: str,
        out_interface: str,
        sport: int,
        dport: int,
    ) -> FirewallRule | None:
        """
        Find an nftables rule by its unique attributes.

        Returns the rule if found, None otherwise.
        """
        with self._db.connect() as conn:
            row = conn.execute(
                """SELECT * FROM nftables_rules
                   WHERE chain = ? AND rule_type = ? AND table_name = ?
                   AND network_id = ? AND protocol = ? AND source = ?
                   AND destination = ? AND in_interface = ? AND out_interface = ?
                   AND sport = ? AND dport = ? AND is_active = 1""",
                (
                    chain_name.value,
                    rule_type.value,
                    "filter",  # nftables uses single inet table
                    network_id,
                    protocol.value,
                    source,
                    destination,
                    in_interface,
                    out_interface,
                    sport,
                    dport,
                ),
            ).fetchone()
        if row is None:
            return None
        return self._row_to_item(row)

    def _row_to_item(self, row: dict[str, Any]) -> FirewallRule:
        """Convert DB row dict to FirewallRule dataclass."""
        row_dict = dict(row)
        row_dict["table_name"] = FirewallTable(row_dict["table_name"])
        row_dict["chain_name"] = FirewallChain(row_dict.pop("chain"))
        row_dict["rule_type"] = FirewallRuleType(row_dict["rule_type"])
        row_dict["protocol"] = FirewallProtocol(row_dict["protocol"])
        row_dict["target"] = FirewallTarget(row_dict["target"])
        row_dict["is_active"] = bool(row_dict["is_active"])
        row_dict.pop("nft_handle", None)
        return FirewallRule(**row_dict)
