"""IPTables rule database operations."""

from __future__ import annotations

from mvmctl.core._internal._db import Database
from mvmctl.models.network import IPTablesRuleItem


class IPTablesRuleRepository:
    """Database operations for iptables rules."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    def list_by_network_id(self, network_id: str) -> list[IPTablesRuleItem]:
        """List all iptables rules for a network."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM iptables_rules WHERE network_id = ? ORDER BY id",
                (network_id,),
            ).fetchall()
        return [IPTablesRuleItem(**dict(row)) for row in rows]

    def get(self, rule_id: int) -> IPTablesRuleItem | None:
        """Get a specific rule by ID."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM iptables_rules WHERE id = ?", (rule_id,)
            ).fetchone()
        if row is None:
            return None
        return IPTablesRuleItem(**dict(row))

    def list_all(self) -> list[IPTablesRuleItem]:
        """List all iptables rules."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM iptables_rules ORDER BY id"
            ).fetchall()
        return [IPTablesRuleItem(**dict(row)) for row in rows]
