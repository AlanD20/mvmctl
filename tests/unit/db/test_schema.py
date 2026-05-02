"""Tests for database schema correctness.

Verifies all tables, columns, indexes, constraints, and defaults
produced by the initial schema migration.
"""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from mvmctl.core._shared._db import Database

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def db(tmp_path: Path) -> Database:
    """Create and migrate a temporary database."""
    d = Database(db_path=tmp_path / "test.db")
    d.migrate()
    return d


@pytest.fixture
def conn(tmp_path: Path) -> sqlite3.Connection:
    """Create a raw connection to a migrated database."""
    db_path = tmp_path / "raw_test.db"
    d = Database(db_path=db_path)
    d.migrate()
    c = sqlite3.connect(db_path)
    c.row_factory = sqlite3.Row
    c.execute("PRAGMA foreign_keys = ON")
    return c


# ---------------------------------------------------------------------------
# Migration execution
# ---------------------------------------------------------------------------


class TestMigrationExecution:
    """Verify migration runs correctly."""

    def test_migration_applies_successfully(self, tmp_path: Path) -> None:
        db_path = tmp_path / "migrate_test.db"
        d = Database(db_path=db_path)
        applied = d.migrate()
        assert applied == 1
        assert d.get_current_version() == 1

    def test_migration_is_idempotent(self, tmp_path: Path) -> None:
        db_path = tmp_path / "idempotent.db"
        d = Database(db_path=db_path)
        d.migrate()
        applied = d.migrate()
        assert applied == 0

    def test_pragma_user_version_is_one(self, db: Database) -> None:
        assert db.get_current_version() == 1


# ---------------------------------------------------------------------------
# Table existence
# ---------------------------------------------------------------------------


class TestTableCreation:
    """Verify all expected tables exist."""

    EXPECTED_TABLES = {
        "images",
        "kernels",
        "binaries",
        "networks",
        "network_leases",
        "vm_instances",
        "host_state",
        "host_state_changes",
        "db_migrations",
        "iptables_rules",
        "ssh_keys",
        "user_settings",
    }

    def test_all_tables_exist(self, conn: sqlite3.Connection) -> None:
        cursor = conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' "
            "AND name NOT LIKE 'sqlite_%' ORDER BY name"
        )
        tables = {row["name"] for row in cursor.fetchall()}
        assert tables == self.EXPECTED_TABLES


# ---------------------------------------------------------------------------
# Column structure
# ---------------------------------------------------------------------------


class TestColumnStructure:
    """Verify column definitions for each table."""

    IMAGES_COLUMNS = {
        "id": "TEXT",
        "os_slug": "TEXT",
        "os_name": "TEXT",
        "arch": "TEXT",
        "path": "TEXT",
        "fs_type": "TEXT",
        "fs_uuid": "TEXT",
        "compressed_size": "INTEGER",
        "original_size": "INTEGER",
        "compression_ratio": "REAL",
        "compressed_format": "TEXT",
        "minimum_rootfs_size_mib": "INTEGER",
        "pulled_at": "TIMESTAMP",
        "is_default": "INTEGER",
        "is_present": "INTEGER",
        "created_at": "TIMESTAMP",
        "updated_at": "TIMESTAMP",
        "deleted_at": "TIMESTAMP",
    }

    KERNELS_COLUMNS = {
        "id": "TEXT",
        "name": "TEXT",
        "base_name": "TEXT",
        "version": "TEXT",
        "arch": "TEXT",
        "type": "TEXT",
        "path": "TEXT",
        "is_default": "INTEGER",
        "is_present": "INTEGER",
        "created_at": "TIMESTAMP",
        "updated_at": "TIMESTAMP",
        "deleted_at": "TIMESTAMP",
    }

    BINARIES_COLUMNS = {
        "id": "TEXT",
        "name": "TEXT",
        "version": "TEXT",
        "full_version": "TEXT",
        "ci_version": "TEXT",
        "path": "TEXT",
        "is_default": "INTEGER",
        "is_present": "INTEGER",
        "created_at": "TIMESTAMP",
        "updated_at": "TIMESTAMP",
        "deleted_at": "TIMESTAMP",
    }

    NETWORKS_COLUMNS = {
        "id": "TEXT",
        "name": "TEXT",
        "subnet": "TEXT",
        "bridge": "TEXT",
        "ipv4_gateway": "TEXT",
        "bridge_active": "INTEGER",
        "nat_gateways": "TEXT",
        "nat_enabled": "INTEGER",
        "is_default": "INTEGER",
        "is_present": "INTEGER",
        "created_at": "TIMESTAMP",
        "updated_at": "TIMESTAMP",
        "deleted_at": "TIMESTAMP",
    }

    NETWORK_LEASES_COLUMNS = {
        "id": "INTEGER",
        "network_id": "TEXT",
        "ipv4": "TEXT",
        "vm_id": "TEXT",
        "leased_at": "TIMESTAMP",
        "expires_at": "TIMESTAMP",
    }

    VM_INSTANCES_COLUMNS = {
        "id": "TEXT",
        "name": "TEXT",
        "status": "TEXT",
        "pid": "INTEGER",
        "process_start_time": "INTEGER",
        "ipv4": "TEXT",
        "mac": "TEXT",
        "network_id": "TEXT",
        "tap_device": "TEXT",
        "image_id": "TEXT",
        "kernel_id": "TEXT",
        "binary_id": "TEXT",
        "api_socket_path": "TEXT",
        "relay_socket_path": "TEXT",
        "config_path": "TEXT",
        "cloud_init_mode": "TEXT",
        "nocloud_net_port": "INTEGER",
        "nocloud_net_pid": "INTEGER",
        "relay_pid": "INTEGER",
        "exit_code": "INTEGER",
        "log_path": "TEXT",
        "serial_output_path": "TEXT",
        "vcpu_count": "INTEGER",
        "mem_size_mib": "INTEGER",
        "disk_size_mib": "INTEGER",
        "rootfs_path": "TEXT",
        "rootfs_suffix": "TEXT",
        "enable_pci": "INTEGER",
        "lsm_flags": "TEXT",
        "enable_logging": "INTEGER",
        "enable_metrics": "INTEGER",
        "enable_console": "INTEGER",
        "boot_args": "TEXT",
        "created_at": "TIMESTAMP",
        "updated_at": "TIMESTAMP",
    }

    HOST_STATE_COLUMNS = {
        "id": "INTEGER",
        "initialized": "INTEGER",
        "mvm_group_created": "INTEGER",
        "sudoers_configured": "INTEGER",
        "default_network_created": "INTEGER",
        "initialized_at": "TIMESTAMP",
        "updated_at": "TIMESTAMP",
    }

    HOST_STATE_CHANGES_COLUMNS = {
        "id": "INTEGER",
        "session_id": "TEXT",
        "init_timestamp": "TIMESTAMP",
        "setting": "TEXT",
        "mechanism": "TEXT",
        "original_value": "TEXT",
        "applied_value": "TEXT",
        "reverted": "INTEGER",
        "reverted_at": "TIMESTAMP",
        "revert_mechanism": "TEXT",
        "change_order": "INTEGER",
        "created_at": "TIMESTAMP",
    }

    DB_MIGRATIONS_COLUMNS = {
        "id": "INTEGER",
        "version": "INTEGER",
        "name": "TEXT",
        "applied_at": "TIMESTAMP",
        "checksum": "TEXT",
        "snapshot_path": "TEXT",
    }

    def _get_columns(
        self, conn: sqlite3.Connection, table: str
    ) -> dict[str, str]:
        cursor = conn.execute(f"PRAGMA table_info({table})")
        return {row["name"]: row["type"] for row in cursor.fetchall()}

    def test_images_columns(self, conn: sqlite3.Connection) -> None:
        assert self._get_columns(conn, "images") == self.IMAGES_COLUMNS

    def test_kernels_columns(self, conn: sqlite3.Connection) -> None:
        assert self._get_columns(conn, "kernels") == self.KERNELS_COLUMNS

    def test_binaries_columns(self, conn: sqlite3.Connection) -> None:
        assert self._get_columns(conn, "binaries") == self.BINARIES_COLUMNS

    def test_networks_columns(self, conn: sqlite3.Connection) -> None:
        assert self._get_columns(conn, "networks") == self.NETWORKS_COLUMNS

    def test_network_leases_columns(self, conn: sqlite3.Connection) -> None:
        assert (
            self._get_columns(conn, "network_leases")
            == self.NETWORK_LEASES_COLUMNS
        )

    def test_vm_instances_columns(self, conn: sqlite3.Connection) -> None:
        assert (
            self._get_columns(conn, "vm_instances") == self.VM_INSTANCES_COLUMNS
        )

    def test_host_state_columns(self, conn: sqlite3.Connection) -> None:
        assert self._get_columns(conn, "host_state") == self.HOST_STATE_COLUMNS

    def test_host_state_changes_columns(self, conn: sqlite3.Connection) -> None:
        assert (
            self._get_columns(conn, "host_state_changes")
            == self.HOST_STATE_CHANGES_COLUMNS
        )

    def test_db_migrations_columns(self, conn: sqlite3.Connection) -> None:
        assert (
            self._get_columns(conn, "db_migrations")
            == self.DB_MIGRATIONS_COLUMNS
        )


# ---------------------------------------------------------------------------
# Indexes
# ---------------------------------------------------------------------------


class TestIndexes:
    """Verify indexes exist for each table."""

    @staticmethod
    def _get_indexes(conn: sqlite3.Connection, table: str) -> set[str]:
        cursor = conn.execute(
            "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name=?",
            (table,),
        )
        return {row["name"] for row in cursor.fetchall()}

    def test_images_indexes(self, conn: sqlite3.Connection) -> None:
        idxs = self._get_indexes(conn, "images")
        assert "idx_images_os_slug" in idxs
        assert "idx_images_name" in idxs

    def test_kernels_indexes(self, conn: sqlite3.Connection) -> None:
        idxs = self._get_indexes(conn, "kernels")
        assert "idx_kernels_name" in idxs
        assert "idx_kernels_version" in idxs

    def test_binaries_indexes(self, conn: sqlite3.Connection) -> None:
        idxs = self._get_indexes(conn, "binaries")
        assert "idx_binaries_name" in idxs
        assert "idx_binaries_version" in idxs

    def test_networks_indexes(self, conn: sqlite3.Connection) -> None:
        idxs = self._get_indexes(conn, "networks")
        assert "idx_networks_name" in idxs

    def test_network_leases_indexes(self, conn: sqlite3.Connection) -> None:
        idxs = self._get_indexes(conn, "network_leases")
        assert "idx_leases_network" in idxs
        assert "idx_leases_vm" in idxs
        assert "idx_leases_ipv4" in idxs

    def test_vm_instances_indexes(self, conn: sqlite3.Connection) -> None:
        idxs = self._get_indexes(conn, "vm_instances")
        assert "idx_vm_instances_name" in idxs
        assert "idx_vm_instances_status" in idxs

    def test_host_state_changes_indexes(self, conn: sqlite3.Connection) -> None:
        idxs = self._get_indexes(conn, "host_state_changes")
        assert "idx_host_changes_session" in idxs
        assert "idx_host_changes_setting" in idxs
        assert "idx_host_changes_reverted" in idxs

    def test_iptables_rules_indexes(self, conn: sqlite3.Connection) -> None:
        idxs = self._get_indexes(conn, "iptables_rules")
        assert "idx_iptables_rules_network" in idxs
        assert "idx_iptables_rules_chain" in idxs
        assert "idx_iptables_rules_type" in idxs
        assert "idx_iptables_rules_active" in idxs
        assert "idx_iptables_rules_interfaces" in idxs
        assert "idx_iptables_rules_network_type" in idxs
        assert "idx_iptables_rules_unique_active" in idxs

    def test_ssh_keys_indexes(self, conn: sqlite3.Connection) -> None:
        idxs = self._get_indexes(conn, "ssh_keys")
        assert "idx_ssh_keys_name" in idxs
        assert "idx_ssh_keys_fingerprint" in idxs
        assert "idx_ssh_keys_is_default" in idxs


# ---------------------------------------------------------------------------
# UNIQUE and CHECK constraints
# ---------------------------------------------------------------------------


class TestConstraints:
    """Verify UNIQUE, CHECK, and NOT NULL constraints."""

    def test_images_os_slug_indexed(self, conn: sqlite3.Connection) -> None:
        """Verify images table has a performance index on os_slug."""
        cursor = conn.execute(
            "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='images'"
        )
        idxs = {row[0] for row in cursor.fetchall()}
        assert "idx_images_os_slug" in idxs

    def test_networks_name_unique(self, conn: sqlite3.Connection) -> None:
        conn.execute(
            "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("net1", "default-net", "10.0.0.0/24", "br0", "10.0.0.1"),
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
                "is_default, is_present, created_at, updated_at) "
                "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
                ("net2", "default-net", "10.1.0.0/24", "br1", "10.1.0.1"),
            )

    def test_vm_instances_name_unique(self, conn: sqlite3.Connection) -> None:
        # Insert prerequisites
        conn.execute(
            "INSERT INTO images (id, os_slug, os_name, arch, path, fs_type, "
            "fs_uuid, original_size, minimum_rootfs_size_mib, pulled_at, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            (
                "img1",
                "ubuntu-24.04",
                "Ubuntu",
                "x86_64",
                "/p",
                "ext4",
                "u1",
                2048,
                1024,
                "now",
            ),
        )
        conn.execute(
            "INSERT INTO kernels (id, name, base_name, version, arch, type, path, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("kern1", "vmlinux", "vmlinux", "5.10", "x86_64", "elf", "/k"),
        )
        conn.execute(
            "INSERT INTO binaries (id, name, version, full_version, ci_version, path, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("bin1", "firecracker", "1.15", "v1.15.0", "1.15.0", "/b"),
        )
        conn.execute(
            "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("net1", "default", "10.0.0.0/24", "br0", "10.0.0.1"),
        )

        conn.execute(
            "INSERT INTO vm_instances (id, name, status, pid, ipv4, mac, "
            "network_id, tap_device, image_id, kernel_id, binary_id, "
            "api_socket_path, config_path, cloud_init_mode, "
            "vcpu_count, mem_size_mib, disk_size_mib, rootfs_path, rootfs_suffix, "
            "enable_pci, enable_logging, enable_metrics, enable_console, "
            "created_at, updated_at) "
"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, "
                "?, ?, ?, ?, ?, ?, ?, ?, ?, "
                "datetime('now'), datetime('now'))",
                (
                    "vm1",
                    "myvm",
                    "running",
                    1000,
                    "10.0.0.2",
                    "aa:bb:cc:dd:ee:ff",
                    "net1",
                    "tap0",
                    "img1",
                    "kern1",
                    "bin1",
                    "/sock",
                    "/cfg",
                    "nocloud",
                    2,
                    1024,
                    5120,
                    "/rootfs",
                    ".ext4",
                    0,
                    1,
                    0,
                    1,
                ),
            )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO vm_instances (id, name, status, pid, ipv4, mac, "
                "network_id, tap_device, image_id, kernel_id, binary_id, "
                "api_socket_path, config_path, cloud_init_mode, "
                "vcpu_count, mem_size_mib, disk_size_mib, rootfs_path, rootfs_suffix, "
                "enable_pci, enable_logging, enable_metrics, enable_console, "
                "created_at, updated_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, "
                "?, ?, ?, ?, ?, ?, ?, ?, ?, "
                "datetime('now'), datetime('now'))",
                (
                    "vm2",
                    "myvm",
                    "stopped",
                    1001,
                    "10.0.0.3",
                    "aa:bb:cc:dd:ee:00",
                    "net1",
                    "tap1",
                    "img1",
                    "kern1",
                    "bin1",
                    "/sock2",
                    "/cfg2",
                    "nocloud",
                    2,
                    1024,
                    5120,
                    "/rootfs2",
                    ".ext4",
                    0,
                    1,
                    0,
                    1,
                ),
            )

    def test_network_leases_unique_constraint(
        self, conn: sqlite3.Connection
    ) -> None:
        conn.execute(
            "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("net1", "lease-net", "10.0.0.0/24", "br0", "10.0.0.1"),
        )
        conn.execute(
            "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
            ("net1", "10.0.0.2"),
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
                ("net1", "10.0.0.2"),
            )

    def test_network_leases_ipv4_check_invalid(
        self, conn: sqlite3.Connection
    ) -> None:
        conn.execute(
            "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("net1", "check-net", "10.0.0.0/24", "br0", "10.0.0.1"),
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
                ("net1", "not-an-ip"),
            )

    def test_vm_instances_ipv4_check_invalid(
        self, conn: sqlite3.Connection
    ) -> None:
        conn.execute(
            "INSERT INTO images (id, os_slug, os_name, arch, path, fs_type, "
            "fs_uuid, original_size, minimum_rootfs_size_mib, pulled_at, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            (
                "img1",
                "ubuntu",
                "Ubuntu",
                "x86_64",
                "/p",
                "ext4",
                "u1",
                2048,
                1024,
                "now",
            ),
        )
        conn.execute(
            "INSERT INTO kernels (id, name, base_name, version, arch, type, path, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("kern1", "vmlinux", "vmlinux", "5.10", "x86_64", "elf", "/k"),
        )
        conn.execute(
            "INSERT INTO binaries (id, name, version, full_version, ci_version, path, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("bin1", "firecracker", "1.15", "v1.15.0", "1.15.0", "/b"),
        )
        conn.execute(
            "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("net1", "default", "10.0.0.0/24", "br0", "10.0.0.1"),
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO vm_instances (id, name, status, pid, ipv4, mac, "
                "network_id, tap_device, image_id, kernel_id, binary_id, "
                "api_socket_path, config_path, cloud_init_mode, "
                "vcpu_count, mem_size_mib, disk_size_mib, rootfs_path, rootfs_suffix, "
                "enable_pci, enable_logging, enable_metrics, enable_console, "
                "created_at, updated_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, "
                "?, ?, ?, ?, ?, ?, ?, ?, ?, "
                "datetime('now'), datetime('now'))",
                (
                    "vm1",
                    "badip-vm",
                    "running",
                    1000,
                    "invalid-ip",
                    "aa:bb:cc:dd:ee:ff",
                    "net1",
                    "tap0",
                    "img1",
                    "kern1",
                    "bin1",
                    "/sock",
                    "/cfg",
                    "nocloud",
                    2,
                    1024,
                    5120,
                    "/rootfs",
                    ".ext4",
                    0,
                    1,
                    0,
                    1,
                ),
            )

    def test_vm_instances_mac_check_invalid(
        self, conn: sqlite3.Connection
    ) -> None:
        conn.execute(
            "INSERT INTO images (id, os_slug, os_name, arch, path, fs_type, "
            "fs_uuid, original_size, minimum_rootfs_size_mib, pulled_at, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            (
                "img1",
                "ubuntu",
                "Ubuntu",
                "x86_64",
                "/p",
                "ext4",
                "u1",
                2048,
                1024,
                "now",
            ),
        )
        conn.execute(
            "INSERT INTO kernels (id, name, base_name, version, arch, type, path, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("kern1", "vmlinux", "vmlinux", "5.10", "x86_64", "elf", "/k"),
        )
        conn.execute(
            "INSERT INTO binaries (id, name, version, full_version, ci_version, path, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("bin1", "firecracker", "1.15", "v1.15.0", "1.15.0", "/b"),
        )
        conn.execute(
            "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
            "is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("net1", "default", "10.0.0.0/24", "br0", "10.0.0.1"),
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO vm_instances (id, name, status, pid, ipv4, mac, "
                "network_id, tap_device, image_id, kernel_id, binary_id, "
                "api_socket_path, config_path, cloud_init_mode, "
                "vcpu_count, mem_size_mib, disk_size_mib, rootfs_path, rootfs_suffix, "
                "enable_pci, enable_logging, enable_metrics, enable_console, "
                "created_at, updated_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, "
                "?, ?, ?, ?, ?, ?, ?, ?, ?, "
                "datetime('now'), datetime('now'))",
                (
                    "vm1",
                    "badmac-vm",
                    "running",
                    1000,
                    "10.0.0.2",
                    "not-a-mac",
                    "net1",
                    "tap0",
                    "img1",
                    "kern1",
                    "bin1",
                    "/sock",
                    "/cfg",
                    "nocloud",
                    2,
                    1024,
                    5120,
                    "/rootfs",
                    ".ext4",
                    0,
                    1,
                    0,
                    1,
                ),
            )

    def test_host_state_changes_session_order_unique(
        self, conn: sqlite3.Connection
    ) -> None:
        conn.execute(
            "INSERT INTO host_state_changes "
            "(session_id, init_timestamp, setting, mechanism, applied_value, "
            "change_order, reverted, created_at) "
            "VALUES (?, ?, ?, ?, ?, ?, 0, datetime('now'))",
            ("session-1", "2024-01-01", "setting1", "mech1", "val1", 1),
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO host_state_changes "
                "(session_id, init_timestamp, setting, mechanism, applied_value, "
                "change_order, reverted, created_at) "
                "VALUES (?, ?, ?, ?, ?, ?, 0, datetime('now'))",
                ("session-1", "2024-01-01", "setting2", "mech2", "val2", 1),
            )

    def test_ssh_keys_name_unique(self, conn: sqlite3.Connection) -> None:
        conn.execute(
            "INSERT INTO ssh_keys (id, name, fingerprint, algorithm, comment, "
            "public_key_path, is_default, is_present, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
            ("key1", "my-key", "aa:bb:cc", "ed25519", "my key", "/path/pub"),
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO ssh_keys (id, name, fingerprint, algorithm, comment, "
                "public_key_path, is_default, is_present, created_at, updated_at) "
                "VALUES (?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
                ("key2", "my-key", "dd:ee:ff", "rsa", "dup key", "/path/pub2"),
            )
