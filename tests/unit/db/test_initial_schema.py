"""Tests for initial database schema migration (001_initial_schema.sql).

Verifies:
- Migration runs successfully via MigrationRunner
- All 9 schema tables are created with correct structure (db_migrations is created by runner)
- All indexes exist
- Foreign key constraints work
- CHECK constraints on ipv4/mac work
- UNIQUE constraints work
- PRAGMA user_version is set to 1
"""

from __future__ import annotations

import sqlite3
from contextlib import closing
from pathlib import Path

import pytest

from mvmctl.db.migrations.runner import MigrationRunner


@pytest.fixture
def migrations_dir(tmp_path: Path) -> Path:
    """Create a temporary migrations directory with all migration SQL files."""
    d = tmp_path / "migrations"
    d.mkdir()
    migrations_src_dir = (
        Path(__file__).parent.parent.parent.parent / "src" / "mvmctl" / "db" / "migrations"
    )
    for sql_file in sorted(migrations_src_dir.glob("[0-9]*_*.sql")):
        (d / sql_file.name).write_text(sql_file.read_text())
    return d


@pytest.fixture
def db_path(tmp_path: Path) -> Path:
    """Temporary database path."""
    return tmp_path / "test.db"


@pytest.fixture
def runner(db_path: Path, migrations_dir: Path) -> MigrationRunner:
    """Create a MigrationRunner instance."""
    return MigrationRunner(db_path=db_path, migrations_dir=migrations_dir)


class TestMigrationExecution:
    """Test that the migration runs successfully."""

    def test_migration_applies_successfully(self, runner: MigrationRunner) -> None:
        """Verify all schema migrations apply without errors."""
        applied = runner.migrate()
        assert applied == 1
        assert runner.get_current_version() == 1

    def test_migration_is_idempotent(self, runner: MigrationRunner) -> None:
        """Verify running migration twice is safe."""
        runner.migrate()
        applied = runner.migrate()
        assert applied == 0


class TestTableCreation:
    """Test that all 9 schema tables are created (db_migrations created by runner)."""

    def test_all_tables_exist(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify all 9 schema tables plus db_migrations are created."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
                )
                tables = {row[0] for row in cursor.fetchall()}

        expected_tables = {
            "images",
            "kernels",
            "binaries",
            "networks",
            "network_leases",
            "vm_instances",
            "host_state",
            "host_state_changes",
            "db_migrations",
        }
        assert tables == expected_tables

    def test_images_table_structure(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify images table has correct columns."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute("PRAGMA table_info(images)")
                columns = {row[1]: row[2] for row in cursor.fetchall()}

        expected_columns = {
            "id": "TEXT",
            "os_slug": "TEXT",
            "os_name": "TEXT",
            "path": "TEXT",
            "fs_type": "TEXT",
            "fs_uuid": "TEXT",
            "compressed_size": "INTEGER",
            "original_size": "INTEGER",
            "compression_ratio": "REAL",
            "compressed_format": "TEXT",
            "pulled_at": "TIMESTAMP",
            "is_default": "BOOLEAN",
            "created_at": "TIMESTAMP",
            "updated_at": "TIMESTAMP",
        }
        assert columns == expected_columns

    def test_kernels_table_structure(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify kernels table has correct columns."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute("PRAGMA table_info(kernels)")
                columns = {row[1]: row[2] for row in cursor.fetchall()}

        expected_columns = {
            "id": "TEXT",
            "name": "TEXT",
            "base_name": "TEXT",
            "version": "TEXT",
            "arch": "TEXT",
            "type": "TEXT",
            "path": "TEXT",
            "is_default": "BOOLEAN",
            "created_at": "TIMESTAMP",
            "updated_at": "TIMESTAMP",
        }
        assert columns == expected_columns

    def test_binaries_table_structure(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify binaries table has correct columns."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute("PRAGMA table_info(binaries)")
                columns = {row[1]: row[2] for row in cursor.fetchall()}

        expected_columns = {
            "id": "TEXT",
            "name": "TEXT",
            "version": "TEXT",
            "full_version": "TEXT",
            "ci_version": "TEXT",
            "path": "TEXT",
            "is_default": "BOOLEAN",
            "created_at": "TIMESTAMP",
            "updated_at": "TIMESTAMP",
        }
        assert columns == expected_columns

    def test_networks_table_structure(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify networks table has correct columns."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute("PRAGMA table_info(networks)")
                columns = {row[1]: row[2] for row in cursor.fetchall()}

        expected_columns = {
            "id": "TEXT",
            "name": "TEXT",
            "subnet": "TEXT",
            "bridge": "TEXT",
            "ipv4_gateway": "TEXT",
            "bridge_active": "BOOLEAN",
            "nat_gateways": "TEXT",
            "nat_enabled": "BOOLEAN",
            "is_default": "BOOLEAN",
            "created_at": "TIMESTAMP",
            "updated_at": "TIMESTAMP",
        }
        assert columns == expected_columns

    def test_network_leases_table_structure(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify network_leases table has correct columns."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute("PRAGMA table_info(network_leases)")
                columns = {row[1]: row[2] for row in cursor.fetchall()}

        expected_columns = {
            "id": "INTEGER",
            "network_id": "TEXT",
            "ipv4": "TEXT",
            "vm_id": "TEXT",
            "leased_at": "TIMESTAMP",
            "expires_at": "TIMESTAMP",
        }
        assert columns == expected_columns

    def test_vm_instances_table_structure(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify vm_instances table has correct columns."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute("PRAGMA table_info(vm_instances)")
                columns = {row[1]: row[2] for row in cursor.fetchall()}

        expected_columns = {
            "id": "TEXT",
            "name": "TEXT",
            "status": "TEXT",
            "pid": "INTEGER",
            "ipv4": "TEXT",
            "mac": "TEXT",
            "network_id": "TEXT",
            "tap_device": "TEXT",
            "image_id": "TEXT",
            "kernel_id": "TEXT",
            "binary_id": "TEXT",
            "api_socket_path": "TEXT",
            "console_socket_path": "TEXT",
            "config_path": "TEXT",
            "cloud_init_mode": "TEXT",
            "nocloud_net_port": "INTEGER",
            "nocloud_server_pid": "INTEGER",
            "console_relay_pid": "INTEGER",
            "exit_code": "INTEGER",
            "vcpu_count": "INTEGER",
            "mem_size_mib": "INTEGER",
            "disk_size_mib": "INTEGER",
            "rootfs_path": "TEXT",
            "rootfs_suffix": "TEXT",
            "created_at": "TIMESTAMP",
            "updated_at": "TIMESTAMP",
        }
        assert columns == expected_columns

    def test_host_state_table_structure(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify host_state table has correct columns."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute("PRAGMA table_info(host_state)")
                columns = {row[1]: row[2] for row in cursor.fetchall()}

        expected_columns = {
            "id": "INTEGER",
            "initialized": "BOOLEAN",
            "mvm_group_created": "BOOLEAN",
            "sudoers_configured": "BOOLEAN",
            "default_network_created": "BOOLEAN",
            "initialized_at": "TIMESTAMP",
            "updated_at": "TIMESTAMP",
        }
        assert columns == expected_columns

    def test_host_state_changes_table_structure(
        self, runner: MigrationRunner, db_path: Path
    ) -> None:
        """Verify host_state_changes table has correct columns."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute("PRAGMA table_info(host_state_changes)")
                columns = {row[1]: row[2] for row in cursor.fetchall()}

        expected_columns = {
            "id": "INTEGER",
            "session_id": "TEXT",
            "init_timestamp": "TIMESTAMP",
            "setting": "TEXT",
            "mechanism": "TEXT",
            "original_value": "TEXT",
            "applied_value": "TEXT",
            "reverted": "BOOLEAN",
            "reverted_at": "TIMESTAMP",
            "revert_mechanism": "TEXT",
            "change_order": "INTEGER",
            "created_at": "TIMESTAMP",
        }
        assert columns == expected_columns

    def test_db_migrations_table_structure(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify db_migrations table has correct columns (created by runner)."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute("PRAGMA table_info(db_migrations)")
                columns = {row[1]: row[2] for row in cursor.fetchall()}

        expected_columns = {
            "id": "INTEGER",
            "version": "INTEGER",
            "name": "TEXT",
            "applied_at": "TIMESTAMP",
            "checksum": "TEXT",
        }
        assert columns == expected_columns


class TestIndexes:
    """Test that all expected indexes are created."""

    def test_images_indexes_exist(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify images table indexes."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='images'"
                )
                indexes = {row[0] for row in cursor.fetchall()}

        expected_indexes = {"idx_images_os_slug", "idx_images_name"}
        assert expected_indexes.issubset(indexes)

    def test_kernels_indexes_exist(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify kernels table indexes."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='kernels'"
                )
                indexes = {row[0] for row in cursor.fetchall()}

        expected_indexes = {"idx_kernels_name", "idx_kernels_version"}
        assert expected_indexes.issubset(indexes)

    def test_binaries_indexes_exist(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify binaries table indexes."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='binaries'"
                )
                indexes = {row[0] for row in cursor.fetchall()}

        expected_indexes = {"idx_binaries_name", "idx_binaries_version"}
        assert expected_indexes.issubset(indexes)

    def test_networks_indexes_exist(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify networks table indexes."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='networks'"
                )
                indexes = {row[0] for row in cursor.fetchall()}

        expected_indexes = {"idx_networks_name"}
        assert expected_indexes.issubset(indexes)

    def test_network_leases_indexes_exist(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify network_leases table indexes."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='network_leases'"
                )
                indexes = {row[0] for row in cursor.fetchall()}

        expected_indexes = {"idx_leases_network", "idx_leases_vm", "idx_leases_ipv4"}
        assert expected_indexes.issubset(indexes)

    def test_vm_instances_indexes_exist(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify vm_instances table indexes."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='vm_instances'"
                )
                indexes = {row[0] for row in cursor.fetchall()}

        expected_indexes = {"idx_vm_instances_name", "idx_vm_instances_status"}
        assert expected_indexes.issubset(indexes)

    def test_host_state_changes_indexes_exist(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify host_state_changes table indexes."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                cursor = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='host_state_changes'"
                )
                indexes = {row[0] for row in cursor.fetchall()}

        expected_indexes = {
            "idx_host_changes_session",
            "idx_host_changes_setting",
            "idx_host_changes_reverted",
        }
        assert expected_indexes.issubset(indexes)


class TestUniqueConstraints:
    """Test UNIQUE constraints."""

    def test_images_os_slug_unique(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify os_slug is unique in images table."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert first image
                conn.execute(
                    "INSERT INTO images (id, os_slug, path) VALUES (?, ?, ?)",
                    ("id1", "ubuntu-24.04", "/path/to/image1"),
                )
                # Try to insert duplicate os_slug
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO images (id, os_slug, path) VALUES (?, ?, ?)",
                        ("id2", "ubuntu-24.04", "/path/to/image2"),
                    )

    def test_networks_name_unique(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify name is unique in networks table."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert first network
                conn.execute(
                    "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                    ("net1", "default", "10.0.0.0/24", "mvm-default", "10.0.0.1"),
                )
                # Try to insert duplicate name
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                        ("net2", "default", "10.1.0.0/24", "mvm-other", "10.1.0.1"),
                    )

    def test_vm_instances_name_unique(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify name is unique in vm_instances table."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert first VM
                conn.execute(
                    "INSERT INTO vm_instances (id, name, status) VALUES (?, ?, ?)",
                    ("vm1", "myvm", "running"),
                )
                # Try to insert duplicate name
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO vm_instances (id, name, status) VALUES (?, ?, ?)",
                        ("vm2", "myvm", "stopped"),
                    )

    def test_network_leases_unique_constraint(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify (network_id, ipv4) is unique in network_leases."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert network first
                conn.execute(
                    "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                    ("net1", "default", "10.0.0.0/24", "mvm-default", "10.0.0.1"),
                )
                # Insert first lease
                conn.execute(
                    "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
                    ("net1", "10.0.0.2"),
                )
                # Try to insert duplicate (network_id, ipv4)
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
                        ("net1", "10.0.0.2"),
                    )


class TestCheckConstraints:
    """Test CHECK constraints on ipv4 and mac addresses."""

    def test_network_leases_ipv4_check_valid(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify valid IPv4 addresses pass CHECK constraint."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert network
                conn.execute(
                    "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                    ("net1", "default", "10.0.0.0/24", "mvm-default", "10.0.0.1"),
                )
                # Insert valid IPv4
                conn.execute(
                    "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
                    ("net1", "10.0.0.2"),
                )
                # Verify it was inserted
                cursor = conn.execute(
                    "SELECT ipv4 FROM network_leases WHERE network_id = ?", ("net1",)
                )
                assert cursor.fetchone()[0] == "10.0.0.2"

    def test_network_leases_ipv4_check_invalid(
        self, runner: MigrationRunner, db_path: Path
    ) -> None:
        """Verify invalid IPv4 addresses fail CHECK constraint."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert network
                conn.execute(
                    "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                    ("net1", "default", "10.0.0.0/24", "mvm-default", "10.0.0.1"),
                )
                # Try to insert invalid IPv4
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
                        ("net1", "not-an-ip"),
                    )

    def test_vm_instances_ipv4_check_valid(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify valid IPv4 in vm_instances passes CHECK constraint."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert VM with valid IPv4
                conn.execute(
                    "INSERT INTO vm_instances (id, name, status, ipv4) VALUES (?, ?, ?, ?)",
                    ("vm1", "myvm", "running", "10.0.0.5"),
                )
                # Verify it was inserted
                cursor = conn.execute("SELECT ipv4 FROM vm_instances WHERE id = ?", ("vm1",))
                assert cursor.fetchone()[0] == "10.0.0.5"

    def test_vm_instances_ipv4_check_null_allowed(
        self, runner: MigrationRunner, db_path: Path
    ) -> None:
        """Verify NULL ipv4 is allowed in vm_instances."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert VM with NULL IPv4
                conn.execute(
                    "INSERT INTO vm_instances (id, name, status, ipv4) VALUES (?, ?, ?, ?)",
                    ("vm1", "myvm", "stopped", None),
                )
                # Verify it was inserted
                cursor = conn.execute("SELECT ipv4 FROM vm_instances WHERE id = ?", ("vm1",))
                assert cursor.fetchone()[0] is None

    def test_vm_instances_ipv4_check_invalid(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify invalid IPv4 in vm_instances fails CHECK constraint."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Try to insert VM with invalid IPv4
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO vm_instances (id, name, status, ipv4) VALUES (?, ?, ?, ?)",
                        ("vm1", "myvm", "running", "invalid-ip"),
                    )

    def test_vm_instances_mac_check_valid(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify valid MAC address in vm_instances passes CHECK constraint."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert VM with valid MAC
                conn.execute(
                    "INSERT INTO vm_instances (id, name, status, mac) VALUES (?, ?, ?, ?)",
                    ("vm1", "myvm", "running", "aa:bb:cc:dd:ee:ff"),
                )
                # Verify it was inserted
                cursor = conn.execute("SELECT mac FROM vm_instances WHERE id = ?", ("vm1",))
                assert cursor.fetchone()[0] == "aa:bb:cc:dd:ee:ff"

    def test_vm_instances_mac_check_null_allowed(
        self, runner: MigrationRunner, db_path: Path
    ) -> None:
        """Verify NULL mac is allowed in vm_instances."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert VM with NULL MAC
                conn.execute(
                    "INSERT INTO vm_instances (id, name, status, mac) VALUES (?, ?, ?, ?)",
                    ("vm1", "myvm", "stopped", None),
                )
                # Verify it was inserted
                cursor = conn.execute("SELECT mac FROM vm_instances WHERE id = ?", ("vm1",))
                assert cursor.fetchone()[0] is None

    def test_vm_instances_mac_check_invalid(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify invalid MAC address in vm_instances fails CHECK constraint."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Try to insert VM with invalid MAC
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO vm_instances (id, name, status, mac) VALUES (?, ?, ?, ?)",
                        ("vm1", "myvm", "running", "not-a-mac"),
                    )


class TestForeignKeyConstraints:
    """Test foreign key constraints."""

    def test_network_leases_network_id_fk(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify network_id foreign key constraint in network_leases."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Try to insert lease with non-existent network_id
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
                        ("nonexistent", "10.0.0.2"),
                    )

    def test_network_leases_vm_id_fk(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify vm_id foreign key constraint in network_leases."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert network
                conn.execute(
                    "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                    ("net1", "default", "10.0.0.0/24", "mvm-default", "10.0.0.1"),
                )
                # Try to insert lease with non-existent vm_id
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO network_leases (network_id, ipv4, vm_id) VALUES (?, ?, ?)",
                        ("net1", "10.0.0.2", "nonexistent"),
                    )

    def test_vm_instances_network_id_fk(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify network_id foreign key constraint in vm_instances."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Try to insert VM with non-existent network_id
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO vm_instances (id, name, status, network_id) VALUES (?, ?, ?, ?)",
                        ("vm1", "myvm", "running", "nonexistent"),
                    )

    def test_vm_instances_image_id_fk(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify image_id foreign key constraint in vm_instances."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Try to insert VM with non-existent image_id
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO vm_instances (id, name, status, image_id) VALUES (?, ?, ?, ?)",
                        ("vm1", "myvm", "running", "nonexistent"),
                    )

    def test_vm_instances_kernel_id_fk(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify kernel_id foreign key constraint in vm_instances."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Try to insert VM with non-existent kernel_id
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO vm_instances (id, name, status, kernel_id) VALUES (?, ?, ?, ?)",
                        ("vm1", "myvm", "running", "nonexistent"),
                    )

    def test_vm_instances_binary_id_fk(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify binary_id foreign key constraint in vm_instances."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Try to insert VM with non-existent binary_id
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute(
                        "INSERT INTO vm_instances (id, name, status, binary_id) VALUES (?, ?, ?, ?)",
                        ("vm1", "myvm", "running", "nonexistent"),
                    )

    def test_network_leases_cascade_delete_on_network(
        self, runner: MigrationRunner, db_path: Path
    ) -> None:
        """Verify network_leases cascade delete when network is deleted."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert network and lease
                conn.execute(
                    "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                    ("net1", "default", "10.0.0.0/24", "mvm-default", "10.0.0.1"),
                )
                conn.execute(
                    "INSERT INTO network_leases (network_id, ipv4) VALUES (?, ?)",
                    ("net1", "10.0.0.2"),
                )
                # Delete network
                conn.execute("DELETE FROM networks WHERE id = ?", ("net1",))
                # Verify lease was deleted
                cursor = conn.execute(
                    "SELECT COUNT(*) FROM network_leases WHERE network_id = ?", ("net1",)
                )
                assert cursor.fetchone()[0] == 0

    def test_network_leases_cascade_delete_on_vm(
        self, runner: MigrationRunner, db_path: Path
    ) -> None:
        """Verify network_leases cascade delete when VM is deleted."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert network and VM
                conn.execute(
                    "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                    ("net1", "default", "10.0.0.0/24", "mvm-default", "10.0.0.1"),
                )
                conn.execute(
                    "INSERT INTO vm_instances (id, name, status) VALUES (?, ?, ?)",
                    ("vm1", "myvm", "running"),
                )
                # Insert lease with VM reference
                conn.execute(
                    "INSERT INTO network_leases (network_id, ipv4, vm_id) VALUES (?, ?, ?)",
                    ("net1", "10.0.0.2", "vm1"),
                )
                # Delete VM
                conn.execute("DELETE FROM vm_instances WHERE id = ?", ("vm1",))
                # Verify lease was deleted
                cursor = conn.execute(
                    "SELECT COUNT(*) FROM network_leases WHERE vm_id = ?", ("vm1",)
                )
                assert cursor.fetchone()[0] == 0

    def test_vm_instances_restrict_delete_on_network(
        self, runner: MigrationRunner, db_path: Path
    ) -> None:
        """Verify vm_instances cannot delete network (RESTRICT)."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert network and VM
                conn.execute(
                    "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                    ("net1", "default", "10.0.0.0/24", "mvm-default", "10.0.0.1"),
                )
                conn.execute(
                    "INSERT INTO vm_instances (id, name, status, network_id) VALUES (?, ?, ?, ?)",
                    ("vm1", "myvm", "running", "net1"),
                )
                # Try to delete network (should fail due to RESTRICT)
                with pytest.raises(sqlite3.IntegrityError):
                    conn.execute("DELETE FROM networks WHERE id = ?", ("net1",))


class TestPragmaUserVersion:
    """Test PRAGMA user_version is set correctly."""

    def test_pragma_user_version_is_one(self, runner: MigrationRunner, db_path: Path) -> None:
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                result = conn.execute("PRAGMA user_version").fetchone()
                assert result[0] == 1


class TestDataIntegrity:
    """Test data integrity and basic operations."""

    def test_insert_and_retrieve_image(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify can insert and retrieve image data."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert image
                conn.execute(
                    "INSERT INTO images (id, os_slug, path) VALUES (?, ?, ?)",
                    ("img1", "ubuntu-24.04", "/path/to/image"),
                )
                # Retrieve image
                cursor = conn.execute("SELECT os_slug, path FROM images WHERE id = ?", ("img1",))
                row = cursor.fetchone()
                assert row == ("ubuntu-24.04", "/path/to/image")

    def test_insert_and_retrieve_network(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify can insert and retrieve network data."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert network
                conn.execute(
                    "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway) VALUES (?, ?, ?, ?, ?)",
                    ("net1", "default", "10.0.0.0/24", "mvm-default", "10.0.0.1"),
                )
                # Retrieve network
                cursor = conn.execute(
                    "SELECT name, subnet, bridge FROM networks WHERE id = ?", ("net1",)
                )
                row = cursor.fetchone()
                assert row == ("default", "10.0.0.0/24", "mvm-default")

    def test_insert_and_retrieve_vm_state(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify can insert and retrieve VM state data."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert VM
                conn.execute(
                    "INSERT INTO vm_instances (id, name, status, vcpu_count, mem_size_mib) VALUES (?, ?, ?, ?, ?)",
                    ("vm1", "myvm", "running", 2, 1024),
                )
                # Retrieve VM
                cursor = conn.execute(
                    "SELECT name, status, vcpu_count, mem_size_mib FROM vm_instances WHERE id = ?",
                    ("vm1",),
                )
                row = cursor.fetchone()
                assert row == ("myvm", "running", 2, 1024)

    def test_insert_and_retrieve_host_state(self, runner: MigrationRunner, db_path: Path) -> None:
        """Verify can insert and retrieve host state data."""
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                conn.execute("PRAGMA foreign_keys = ON")
                # Insert host state
                conn.execute(
                    "INSERT INTO host_state (id, initialized, mvm_group_created) VALUES (?, ?, ?)",
                    (1, True, True),
                )
                # Retrieve host state
                cursor = conn.execute(
                    "SELECT initialized, mvm_group_created FROM host_state WHERE id = ?", (1,)
                )
                row = cursor.fetchone()
                assert row == (True, True)
