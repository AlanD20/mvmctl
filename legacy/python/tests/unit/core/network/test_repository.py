"""Tests for NetworkRepository and LeaseRepository."""

from __future__ import annotations

from datetime import UTC, datetime

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.models import NetworkItem


def _seed_network(
    db: Database,
    name: str = "test-net",
    subnet: str = "10.0.0.0/24",
    bridge: str = "mvm-test-net",
    ipv4_gateway: str = "10.0.0.1",
    is_default: bool = False,
    is_present: bool = True,
    deleted_at: str | None = None,
) -> str:
    """Insert a network row directly and return its ID."""
    now = datetime.now(tz=UTC).isoformat()
    network_id = f"test-{name}-{subnet}"
    with db.connect() as conn:
        conn.execute(
            """
            INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway,
                                  bridge_active, nat_enabled, is_default,
                                  is_present, created_at, updated_at, deleted_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                network_id,
                name,
                subnet,
                bridge,
                ipv4_gateway,
                1,  # bridge_active
                1,  # nat_enabled
                int(is_default),
                1 if is_present else 0,
                now,
                now,
                deleted_at,
            ),
        )
    return network_id


def _seed_lease(
    db: Database,
    network_id: str,
    ipv4: str,
    vm_id: str | None = None,
) -> int:
    """Insert a lease row directly and return its ID."""
    with db.connect() as conn:
        conn.execute(
            "INSERT INTO network_leases (network_id, ipv4, vm_id) VALUES (?, ?, ?)",
            (network_id, ipv4, vm_id),
        )
        return conn.execute("SELECT last_insert_rowid()").fetchone()[0]


class TestNetworkRepository:
    """Tests for NetworkRepository."""

    def test_get_returns_network(self, db: Database) -> None:
        """get() returns a NetworkItem when found."""
        nid = _seed_network(db)
        repo = NetworkRepository(db)
        network = repo.get(nid)
        assert network is not None
        assert network.name == "test-net"
        assert network.subnet == "10.0.0.0/24"

    def test_get_returns_none_when_missing(self, db: Database) -> None:
        """get() returns None when network does not exist."""
        repo = NetworkRepository(db)
        assert repo.get("nonexistent-id") is None

    def test_get_returns_none_when_deleted(self, db: Database) -> None:
        """get() returns None for soft-deleted networks."""
        nid = _seed_network(db, deleted_at=datetime.now(tz=UTC).isoformat())
        repo = NetworkRepository(db)
        assert repo.get(nid) is None

    def test_get_by_name_returns_network(self, db: Database) -> None:
        """get_by_name() returns a NetworkItem when found."""
        _seed_network(db, name="mynet")
        repo = NetworkRepository(db)
        network = repo.get_by_name("mynet")
        assert network is not None
        assert network.name == "mynet"

    def test_get_by_name_returns_none_when_missing(self, db: Database) -> None:
        """get_by_name() returns None when network does not exist."""
        repo = NetworkRepository(db)
        assert repo.get_by_name("nonexistent") is None

    def test_get_by_name_returns_none_when_deleted(self, db: Database) -> None:
        """get_by_name() returns None for soft-deleted networks."""
        _seed_network(
            db, name="gone-net", deleted_at=datetime.now(tz=UTC).isoformat()
        )
        repo = NetworkRepository(db)
        assert repo.get_by_name("gone-net") is None

    def test_find_by_prefix_matches(self, db: Database) -> None:
        """find_by_prefix() returns networks whose ID starts with prefix."""
        nid = _seed_network(db, name="prefix-net")
        prefix = nid[:10]
        repo = NetworkRepository(db)
        results = repo.find_by_prefix(prefix)
        assert len(results) >= 1
        assert results[0].name == "prefix-net"

    def test_find_by_prefix_no_match(self, db: Database) -> None:
        """find_by_prefix() returns empty list when no match."""
        repo = NetworkRepository(db)
        assert repo.find_by_prefix("zzzzzz") == []

    def test_find_by_prefix_excludes_deleted(self, db: Database) -> None:
        """find_by_prefix() excludes soft-deleted networks."""
        nid = _seed_network(
            db, name="del-net", deleted_at=datetime.now(tz=UTC).isoformat()
        )
        prefix = nid[:10]
        repo = NetworkRepository(db)
        assert repo.find_by_prefix(prefix) == []

    def test_list_all_returns_non_deleted(self, db: Database) -> None:
        """list_all() returns only non-deleted networks."""
        _seed_network(db, name="active")
        _seed_network(
            db, name="deleted", deleted_at=datetime.now(tz=UTC).isoformat()
        )
        repo = NetworkRepository(db)
        results = repo.list_all()
        names = {n.name for n in results}
        assert "active" in names
        assert "deleted" not in names

    def test_list_all_ordered_by_created_at(self, db: Database) -> None:
        """list_all() returns networks ordered by created_at ascending."""
        _seed_network(db, name="first")
        _seed_network(db, name="second")
        repo = NetworkRepository(db)
        results = repo.list_all()
        assert len(results) >= 2
        # Verify order — first seeded should come first
        assert results[0].name == "first"

    def test_upsert_inserts_new(self, db: Database) -> None:
        """upsert() inserts a new network record."""
        repo = NetworkRepository(db)
        now = datetime.now(tz=UTC).isoformat()
        network = NetworkItem(
            id="upsert-test-id",
            name="upsert-net",
            subnet="10.10.0.0/24",
            bridge="mvm-upsert-net",
            ipv4_gateway="10.10.0.1",
            bridge_active=True,
            nat_enabled=False,
            is_default=False,
            is_present=True,
            created_at=now,
            updated_at=now,
        )
        repo.upsert(network)
        fetched = repo.get("upsert-test-id")
        assert fetched is not None
        assert fetched.name == "upsert-net"
        assert fetched.subnet == "10.10.0.0/24"

    def test_upsert_updates_existing(self, db: Database) -> None:
        """upsert() updates an existing network record on name conflict."""
        _seed_network(db, name="updatable", subnet="10.0.0.0/24")
        repo = NetworkRepository(db)
        now = datetime.now(tz=UTC).isoformat()
        # When upserting, name must still be unique
        network = NetworkItem(
            id="updated-id",
            name="updatable",
            subnet="10.20.0.0/24",
            bridge="mvm-updatable",
            ipv4_gateway="10.20.0.1",
            bridge_active=True,
            nat_enabled=True,
            is_default=False,
            is_present=True,
            created_at=now,
            updated_at=now,
        )
        repo.upsert(network)
        fetched = repo.get_by_name("updatable")
        assert fetched is not None
        assert fetched.subnet == "10.20.0.0/24"

    def test_soft_delete_sets_deleted_at(self, db: Database) -> None:
        """soft_delete() sets deleted_at and is_present=0."""
        nid = _seed_network(db, name="to-delete")
        repo = NetworkRepository(db)
        repo.soft_delete(nid)
        # Should not appear in normal get
        assert repo.get(nid) is None
        # Verify by raw SQL
        with db.connect() as conn:
            row = conn.execute(
                "SELECT deleted_at, is_present FROM networks WHERE id = ?",
                (nid,),
            ).fetchone()
        assert row is not None
        assert row["deleted_at"] is not None
        assert row["is_present"] == 0

    def test_get_default_returns_default(self, db: Database) -> None:
        """get_default() returns the default network."""
        _seed_network(db, name="not-default")
        _seed_network(db, name="default-net", is_default=True)
        repo = NetworkRepository(db)
        default = repo.get_default()
        assert default is not None
        assert default.name == "default-net"
        assert bool(default.is_default)

    def test_get_default_returns_none_when_not_set(self, db: Database) -> None:
        """get_default() returns None when no default is set."""
        repo = NetworkRepository(db)
        assert repo.get_default() is None

    def test_set_default_clears_others(self, db: Database) -> None:
        """set_default() clears previous default and sets new one."""
        nid1 = _seed_network(db, name="net-a", is_default=True)
        nid2 = _seed_network(db, name="net-b")
        repo = NetworkRepository(db)
        repo.set_default(nid2)
        default = repo.get_default()
        assert default is not None
        assert default.name == "net-b"
        # Previously default should have is_default=0
        net_a = repo.get(nid1)
        assert net_a is not None
        assert not net_a.is_default

    def test_count_single(self, db: Database) -> None:
        """Count non-deleted networks via SQL."""
        _seed_network(db, name="count-me")
        with db.connect() as conn:
            count = conn.execute(
                "SELECT COUNT(*) FROM networks WHERE deleted_at IS NULL"
            ).fetchone()[0]
        assert count >= 1

    def test_count_filters_deleted(self, db: Database) -> None:
        """Count excludes deleted networks."""
        _seed_network(db, name="active-only")
        _seed_network(
            db, name="deleted-too", deleted_at=datetime.now(tz=UTC).isoformat()
        )
        with db.connect() as conn:
            count = conn.execute(
                "SELECT COUNT(*) FROM networks WHERE deleted_at IS NULL"
            ).fetchone()[0]
        assert count == 1

    def test_update_bridge_active(self, db: Database) -> None:
        """update_bridge_active() updates the bridge_active flag."""
        nid = _seed_network(db, name="bridge-test")
        repo = NetworkRepository(db)
        repo.update_bridge_active(nid, False)
        net = repo.get(nid)
        assert net is not None
        assert not net.bridge_active

    def test_hard_delete_removes_record(self, db: Database) -> None:
        """delete() removes the record entirely."""
        nid = _seed_network(db, name="hard-delete")
        repo = NetworkRepository(db)
        repo.delete(nid)
        assert repo.get(nid) is None
        with db.connect() as conn:
            row = conn.execute(
                "SELECT COUNT(*) FROM networks WHERE id = ?", (nid,)
            ).fetchone()[0]
        assert row == 0


class TestLeaseRepository:
    """Tests for LeaseRepository."""

    def test_get_returns_lease(self, db: Database) -> None:
        """get() returns a NetworkLeaseItem when found."""
        nid = _seed_network(db, name="lease-test")
        _seed_lease(db, nid, "10.0.0.2", vm_id="vm-1")
        repo = LeaseRepository(db)
        lease = repo.get(nid, "10.0.0.2")
        assert lease is not None
        assert lease.ipv4 == "10.0.0.2"
        assert lease.vm_id == "vm-1"

    def test_get_returns_none_when_missing(self, db: Database) -> None:
        """get() returns None when lease does not exist."""
        nid = _seed_network(db, name="no-lease")
        repo = LeaseRepository(db)
        assert repo.get(nid, "10.0.0.99") is None

    def test_list_all_returns_leases(self, db: Database) -> None:
        """list_all() returns all leases for a network."""
        nid = _seed_network(db, name="multi-lease")
        _seed_lease(db, nid, "10.0.0.2", vm_id="vm-1")
        _seed_lease(db, nid, "10.0.0.3", vm_id="vm-2")
        repo = LeaseRepository(db)
        leases = repo.list_all(nid)
        assert len(leases) == 2
        ips = {lease.ipv4 for lease in leases}
        assert ips == {"10.0.0.2", "10.0.0.3"}

    def test_list_all_empty(self, db: Database) -> None:
        """list_all() returns empty list when no leases."""
        nid = _seed_network(db, name="empty-lease")
        repo = LeaseRepository(db)
        assert repo.list_all(nid) == []

    def test_list_by_vm(self, db: Database) -> None:
        """list_by_vm() returns leases for a specific VM."""
        nid = _seed_network(db, name="vm-leases")
        _seed_lease(db, nid, "10.0.0.2", vm_id="vm-1")
        _seed_lease(db, nid, "10.0.0.3", vm_id="vm-2")
        repo = LeaseRepository(db)
        vm_leases = repo.list_by_vm(nid, "vm-1")
        assert len(vm_leases) == 1
        assert vm_leases[0].ipv4 == "10.0.0.2"

    def test_list_all_batch(self, db: Database) -> None:
        """list_all_batch() returns leases for multiple networks."""
        nid1 = _seed_network(db, name="batch-a")
        nid2 = _seed_network(db, name="batch-b")
        _seed_lease(db, nid1, "10.0.0.2", vm_id="vm-1")
        _seed_lease(db, nid2, "10.0.0.2", vm_id="vm-2")
        repo = LeaseRepository(db)
        leases = repo.list_all_batch([nid1, nid2])
        assert len(leases) == 2

    def test_list_all_batch_empty_input(self, db: Database) -> None:
        """list_all_batch() returns empty list for empty input."""
        repo = LeaseRepository(db)
        assert repo.list_all_batch([]) == []

    def test_acquire_creates_lease(self, db: Database) -> None:
        """acquire() atomically creates a new lease."""
        nid = _seed_network(db, name="acquire-test")
        repo = LeaseRepository(db)
        lease = repo.acquire(nid, "10.0.0.5", vm_id="vm-acquire")
        assert lease is not None
        assert lease.ipv4 == "10.0.0.5"
        assert lease.vm_id == "vm-acquire"
        # Verify it exists
        fetched = repo.get(nid, "10.0.0.5")
        assert fetched is not None

    def test_release_removes_lease(self, db: Database) -> None:
        """release() removes a specific lease."""
        nid = _seed_network(db, name="release-test")
        _seed_lease(db, nid, "10.0.0.7", vm_id="vm-rel")
        repo = LeaseRepository(db)
        repo.release(nid, "10.0.0.7")
        assert repo.get(nid, "10.0.0.7") is None

    def test_release_noop_when_missing(self, db: Database) -> None:
        """release() is a no-op when lease doesn't exist."""
        nid = _seed_network(db, name="release-noop")
        repo = LeaseRepository(db)
        repo.release(nid, "10.0.0.99")  # Should not raise

    def test_release_by_vm_removes_all(self, db: Database) -> None:
        """release_by_vm() removes all leases for a VM."""
        nid = _seed_network(db, name="release-vm")
        _seed_lease(db, nid, "10.0.0.2", vm_id="vm-to-release")
        _seed_lease(db, nid, "10.0.0.3", vm_id="vm-to-release")
        _seed_lease(db, nid, "10.0.0.4", vm_id="vm-other")
        repo = LeaseRepository(db)
        repo.release_by_vm("vm-to-release")
        remaining = repo.list_all(nid)
        assert len(remaining) == 1
        assert remaining[0].vm_id == "vm-other"

    def test_unique_ip_constraint(self, db: Database) -> None:
        """acquire() raises IntegrityError on duplicate IP in same network."""
        nid = _seed_network(db, name="unique-test")
        repo = LeaseRepository(db)
        repo.acquire(nid, "10.0.0.99", vm_id="vm-first")
        with pytest.raises(Exception):
            repo.acquire(nid, "10.0.0.99", vm_id="vm-second")


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied for each test."""
    database = Database()
    database.migrate()
    return database
