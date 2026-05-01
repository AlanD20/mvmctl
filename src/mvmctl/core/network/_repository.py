"""Network database operations - Repository Pattern implementation."""

from __future__ import annotations

from mvmctl.core._shared import Database
from mvmctl.models.network import NetworkItem, NetworkLeaseItem
from mvmctl.models.vm import VMInstanceItem


class NetworkRepository:
    """Database operations for networks."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    def get(self, network_id: str) -> NetworkItem | None:
        """Return a network by its full 64-char ID, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM networks WHERE id = ? AND deleted_at IS NULL",
                (network_id,),
            ).fetchone()
        if row is None:
            return None
        return NetworkItem(**dict(row))

    def get_by_name(self, name: str) -> NetworkItem | None:
        """Return a network by name, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM networks WHERE name = ? AND deleted_at IS NULL",
                (name,),
            ).fetchone()
        if row is None:
            return None
        return NetworkItem(**dict(row))

    def find_by_prefix(self, prefix: str) -> list[NetworkItem]:
        """Return all networks whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM networks WHERE id LIKE ? AND deleted_at IS NULL",
                (f"{prefix}%",),
            ).fetchall()
        return [NetworkItem(**dict(row)) for row in rows]

    def list_all(self) -> list[NetworkItem]:
        """Return all non-deleted networks."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM networks WHERE deleted_at IS NULL ORDER BY created_at"
            ).fetchall()
        return [NetworkItem(**dict(row)) for row in rows]

    def upsert(self, network: NetworkItem) -> None:
        """Insert or replace a network record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO networks (
                    id, name, subnet, bridge, ipv4_gateway, bridge_active,
                    nat_gateways, nat_enabled, is_default, is_present,
                    created_at, updated_at, deleted_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(name) DO UPDATE SET
                    subnet = excluded.subnet,
                    bridge = excluded.bridge,
                    ipv4_gateway = excluded.ipv4_gateway,
                    bridge_active = excluded.bridge_active,
                    nat_gateways = excluded.nat_gateways,
                    nat_enabled = excluded.nat_enabled,
                    is_default = excluded.is_default,
                    is_present = excluded.is_present,
                    updated_at = CURRENT_TIMESTAMP,
                    deleted_at = excluded.deleted_at
                """,
                (
                    network.id,
                    network.name,
                    network.subnet,
                    network.bridge,
                    network.ipv4_gateway,
                    int(network.bridge_active),
                    network.nat_gateways,
                    int(network.nat_enabled),
                    int(network.is_default),
                    int(network.is_present),
                    network.created_at,
                    network.updated_at,
                    network.deleted_at,
                ),
            )

    def update_bridge_active(self, network_id: str, active: bool) -> None:
        """Update only the bridge_active field for a network."""
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE networks SET bridge_active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (int(active), network_id),
            )

    def set_default(self, network_id: str) -> None:
        """Set one network as default, clearing all others atomically."""
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            conn.execute(
                "UPDATE networks SET is_default = 0 WHERE deleted_at IS NULL"
            )
            conn.execute(
                "UPDATE networks SET is_default = 1 WHERE id = ? AND deleted_at IS NULL",
                (network_id,),
            )
            conn.execute("COMMIT")

    def get_default(self) -> NetworkItem | None:
        """Return the default network entry, or None if not set."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM networks WHERE is_default = 1 AND deleted_at IS NULL LIMIT 1"
            ).fetchone()
        if row is None:
            return None
        return NetworkItem(**dict(row))

    def update_many_is_present(
        self, network_ids: list[str], is_present: bool
    ) -> None:
        """Bulk update is_present flag for multiple networks."""
        if not network_ids:
            return
        placeholders = ",".join("?" * len(network_ids))
        with self._db.connect() as conn:
            conn.execute(
                f"UPDATE networks SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN ({placeholders})",
                [int(is_present)] + list(network_ids),
            )

    def soft_delete(self, network_id: str) -> None:
        """Soft-delete a network by setting deleted_at and is_present=0."""
        from datetime import datetime, timezone

        now = datetime.now(tz=timezone.utc).isoformat()
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE networks SET deleted_at = ?, is_present = 0 WHERE id = ?",
                (now, network_id),
            )

    def query_vms_by_network(self, network_id: str) -> list[VMInstanceItem]:
        """Return all VMs that reference the given network ID.

        Args:
            network_id: Full network ID to query.

        Returns:
            List of VMInstanceItem records referencing this network.
        """
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE network_id = ?",
                (network_id,),
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    def delete(self, network_id: str) -> None:
        """Delete a network by ID. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM networks WHERE id = ?", (network_id,))


class LeaseRepository:
    """Database operations for network IP leases."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    def get(self, network_id: str, ipv4: str) -> NetworkLeaseItem | None:
        """Return a lease by network_id + ipv4, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM network_leases WHERE network_id = ? AND ipv4 = ?",
                (network_id, ipv4),
            ).fetchone()
        if row is None:
            return None
        return NetworkLeaseItem(**dict(row))

    def list_all(self, network_id: str) -> list[NetworkLeaseItem]:
        """Return all leases for a network."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM network_leases WHERE network_id = ? ORDER BY leased_at",
                (network_id,),
            ).fetchall()
        return [NetworkLeaseItem(**dict(row)) for row in rows]

    def list_by_vm(self, network_id: str, vm_id: str) -> list[NetworkLeaseItem]:
        """Return all leases for a VM on a specific network."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM network_leases WHERE network_id = ? AND vm_id = ? ORDER BY leased_at",
                (network_id, vm_id),
            ).fetchall()
        return [NetworkLeaseItem(**dict(row)) for row in rows]

    def list_all_batch(self, network_ids: list[str]) -> list[NetworkLeaseItem]:
        """Return all leases for multiple networks."""
        if not network_ids:
            return []
        placeholders = ",".join("?" * len(network_ids))
        query = f"SELECT * FROM network_leases WHERE network_id IN ({placeholders}) ORDER BY leased_at"
        with self._db.connect() as conn:
            rows = conn.execute(query, network_ids).fetchall()
        return [NetworkLeaseItem(**dict(row)) for row in rows]

    def acquire(
        self, network_id: str, ipv4: str, vm_id: str | None = None
    ) -> NetworkLeaseItem:
        """Atomically acquire an IP lease."""
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            conn.execute(
                "INSERT INTO network_leases (network_id, ipv4, vm_id) VALUES (?, ?, ?)",
                (network_id, ipv4, vm_id),
            )
            conn.execute("COMMIT")
        lease = self.get(network_id, ipv4)
        assert lease is not None
        return lease

    def release(self, network_id: str, ipv4: str) -> None:
        """Release an IP lease. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute(
                "DELETE FROM network_leases WHERE network_id = ? AND ipv4 = ?",
                (network_id, ipv4),
            )

    def release_by_vm(self, vm_id: str) -> None:
        """Release all IP leases held by a VM."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM network_leases WHERE vm_id = ?", (vm_id,))
