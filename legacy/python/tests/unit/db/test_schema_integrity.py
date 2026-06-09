"""Tests for cross-table schema integrity — FK constraints, CASCADE deletes,
and UNIQUE constraints enforced across related tables.

Uses the real Database class and raw SQLite operations to verify that
the database enforces all referential integrity rules defined in the schema.
"""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from mvmctl.core._shared._db import Database

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

IMAGE_ID = "i" * 64
KERNEL_ID = "k" * 64
BINARY_ID = "b" * 64
NETWORK_ID = "n" * 64
VM_ID = "v" * 64


@pytest.fixture
def db(tmp_path: Path) -> Database:
    """Create and migrate a test database."""
    d = Database(db_path=tmp_path / "test.db")
    d.migrate()
    return d


@pytest.fixture
def conn(db: Database) -> sqlite3.Connection:
    """Return a raw connection with FK enforcement."""
    c = sqlite3.connect(db.db_path)
    c.row_factory = sqlite3.Row
    c.execute("PRAGMA foreign_keys = ON")
    return c


def _insert_prerequisites(conn: sqlite3.Connection) -> None:
    """Insert common prerequisite records for VM tests."""
    conn.execute(
        "INSERT INTO images (id, type, version, name, arch, path, fs_type, "
        "fs_uuid, original_size, minimum_rootfs_size_mib, pulled_at, "
        "is_default, is_present, created_at, updated_at) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
        (
            IMAGE_ID,
            "test-image",
            "1.0",
            "Test Image",
            "x86_64",
            "/cache/img",
            "ext4",
            "uuid-abc",
            2048,
            1024,
            "2024-01-01",
        ),
    )
    conn.execute(
        "INSERT INTO kernels (id, name, base_name, version, arch, type, path, "
        "is_default, is_present, created_at, updated_at) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
        (
            KERNEL_ID,
            "vmlinux",
            "vmlinux",
            "6.1",
            "x86_64",
            "elf",
            "/cache/kernel",
        ),
    )
    conn.execute(
        "INSERT INTO binaries (id, name, version, full_version, ci_version, path, "
        "is_default, is_present, created_at, updated_at) "
        "VALUES (?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
        (BINARY_ID, "firecracker", "1.15", "v1.15.0", "1.15.0", "/cache/bin"),
    )
    conn.execute(
        "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
        "is_default, is_present, created_at, updated_at) "
        "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
        (NETWORK_ID, "default", "172.35.0.0/24", "mvm-default", "172.35.0.1"),
    )


def _insert_vm(
    conn: sqlite3.Connection, vm_id: str = VM_ID, name: str = "testvm"
) -> None:
    """Insert a VM record assuming prerequisites exist."""
    _insert_prerequisites(conn)
    conn.execute(
        "INSERT INTO vm_instances (id, name, status, pid, ipv4, mac, "
        "network_id, tap_device, image_id, kernel_id, binary_id, "
        "api_socket_path, config_path, cloud_init_mode, "
        "vcpu_count, mem_size_mib, disk_size_mib, rootfs_path, rootfs_suffix, "
        "pci_enabled, enable_logging, enable_metrics, enable_console, "
        "created_at, updated_at) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, "
        "?, ?, ?, ?, ?, ?, ?, ?, ?, "
        "datetime('now'), datetime('now'))",
        (
            vm_id,
            name,
            "stopped",
            0,
            "0.0.0.0",
            "00:00:00:00:00:00",
            NETWORK_ID,
            "",
            IMAGE_ID,
            KERNEL_ID,
            BINARY_ID,
            "",
            "",
            "nocloud",
            2,
            512,
            2048,
            "",
            ".ext4",
            0,
            1,
            0,
            1,
        ),
    )


# ---------------------------------------------------------------------------
# FK RESTRICT constraints
# ---------------------------------------------------------------------------


class TestFKConstraintsRestrict:
    """Test that RESTRICT FK constraints prevent deletion of referenced records."""

    def test_delete_image_referenced_by_vm_raises_error(
        self, conn: sqlite3.Connection
    ) -> None:
        _insert_vm(conn)
        with pytest.raises(
            sqlite3.IntegrityError, match="FOREIGN KEY|foreign key"
        ):
            conn.execute("DELETE FROM images WHERE id = ?", (IMAGE_ID,))

    def test_delete_kernel_referenced_by_vm_raises_error(
        self, conn: sqlite3.Connection
    ) -> None:
        _insert_vm(conn)
        with pytest.raises(
            sqlite3.IntegrityError, match="FOREIGN KEY|foreign key"
        ):
            conn.execute("DELETE FROM kernels WHERE id = ?", (KERNEL_ID,))

    def test_delete_binary_referenced_by_vm_raises_error(
        self, conn: sqlite3.Connection
    ) -> None:
        _insert_vm(conn)
        with pytest.raises(
            sqlite3.IntegrityError, match="FOREIGN KEY|foreign key"
        ):
            conn.execute("DELETE FROM binaries WHERE id = ?", (BINARY_ID,))

    def test_delete_network_referenced_by_vm_raises_error(
        self, conn: sqlite3.Connection
    ) -> None:
        _insert_vm(conn)
        with pytest.raises(
            sqlite3.IntegrityError, match="FOREIGN KEY|foreign key"
        ):
            conn.execute("DELETE FROM networks WHERE id = ?", (NETWORK_ID,))


# ---------------------------------------------------------------------------
# FK CASCADE constraints
# ---------------------------------------------------------------------------


class TestFKConstraintsCascade:
    """Test CASCADE FK constraints."""

    def test_delete_network_cascades_to_leases(
        self, conn: sqlite3.Connection
    ) -> None:
        conn.execute(
            "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("net1", "cascade-net", "10.0.0.0/24", "br0", "10.0.0.1"),
        )
        conn.execute(
            "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
            ("net1", "10.0.0.2"),
        )
        conn.execute(
            "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
            ("net1", "10.0.0.3"),
        )

        # Verify leases exist
        cursor = conn.execute(
            "SELECT COUNT(*) FROM network_leases WHERE network_id = ?",
            ("net1",),
        )
        assert cursor.fetchone()[0] == 2

        # Delete network — should cascade delete leases
        conn.execute("DELETE FROM networks WHERE id = ?", ("net1",))

        cursor = conn.execute(
            "SELECT COUNT(*) FROM network_leases WHERE network_id = ?",
            ("net1",),
        )
        assert cursor.fetchone()[0] == 0

    def test_delete_network_cascades_iptables_rules(
        self, conn: sqlite3.Connection
    ) -> None:
        conn.execute(
            "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("net1", "iptables-net", "10.0.0.0/24", "br0", "10.0.0.1"),
        )
        conn.execute(
            "INSERT INTO iptables_rules "
            "(table_name, chain_name, rule_type, protocol, source, destination, "
            "in_interface, out_interface, target, sport, dport, network_id) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                "filter",
                "MVM-FORWARD",
                "forward_in",
                "tcp",
                "0.0.0.0/0",
                "0.0.0.0/0",
                "mvm-default",
                "eth0",
                "ACCEPT",
                0,
                0,
                "net1",
            ),
        )

        conn.execute("DELETE FROM networks WHERE id = ?", ("net1",))
        cursor = conn.execute(
            "SELECT COUNT(*) FROM iptables_rules WHERE network_id = ?",
            ("net1",),
        )
        assert cursor.fetchone()[0] == 0


# ---------------------------------------------------------------------------
# FK constraint on INSERT (referential integrity)
# ---------------------------------------------------------------------------


class TestFKOnInsert:
    """Test that FK constraints are enforced on INSERT."""

    def test_network_leases_network_id_fk(
        self, conn: sqlite3.Connection
    ) -> None:
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
                ("nonexistent", "10.0.0.2"),
            )

    def test_vm_instances_network_id_fk(self, conn: sqlite3.Connection) -> None:
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO vm_instances (id, name, status, network_id) "
                "VALUES (?, ?, ?, ?)",
                ("vm1", "myvm", "running", "nonexistent"),
            )

    def test_vm_instances_image_id_fk(self, conn: sqlite3.Connection) -> None:
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO vm_instances (id, name, status, image_id) "
                "VALUES (?, ?, ?, ?)",
                ("vm1", "myvm", "running", "nonexistent"),
            )

    def test_vm_instances_kernel_id_fk(self, conn: sqlite3.Connection) -> None:
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO vm_instances (id, name, status, kernel_id) "
                "VALUES (?, ?, ?, ?)",
                ("vm1", "myvm", "running", "nonexistent"),
            )

    def test_vm_instances_binary_id_fk(self, conn: sqlite3.Connection) -> None:
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO vm_instances (id, name, status, binary_id) "
                "VALUES (?, ?, ?, ?)",
                ("vm1", "myvm", "running", "nonexistent"),
            )

    def test_iptables_rules_network_id_fk(
        self, conn: sqlite3.Connection
    ) -> None:
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO iptables_rules "
                "(table_name, chain_name, rule_type, protocol, source, destination, "
                "in_interface, out_interface, target, sport, dport, network_id) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    "filter",
                    "MVM-FORWARD",
                    "forward_in",
                    "tcp",
                    "0.0.0.0/0",
                    "0.0.0.0/0",
                    "mvm-default",
                    "eth0",
                    "ACCEPT",
                    0,
                    0,
                    "nonexistent",
                ),
            )


# ---------------------------------------------------------------------------
# Network lease vm_id — NO FK constraint
# ---------------------------------------------------------------------------


class TestLeasesVmIdNoFK:
    """Leases are acquired BEFORE the VM row exists, so vm_id cannot have an FK."""

    def test_lease_with_nonexistent_vm_id_succeeds(
        self, conn: sqlite3.Connection
    ) -> None:
        conn.execute(
            "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("net1", "lease-test", "10.0.0.0/24", "br0", "10.0.0.1"),
        )
        conn.execute(
            "INSERT INTO network_leases (network_id, ipv4, vm_id) VALUES (?, ?, ?)",
            ("net1", "10.0.0.2", "nonexistent-vm"),
        )
        cursor = conn.execute(
            "SELECT vm_id FROM network_leases WHERE network_id = ? AND ipv4 = ?",
            ("net1", "10.0.0.2"),
        )
        assert cursor.fetchone()["vm_id"] == "nonexistent-vm"
