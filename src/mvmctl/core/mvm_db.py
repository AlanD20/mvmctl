"""SQLite database interface for mvmctl.

This module is the SOLE public interface to the database layer.
Only this file may import from mvmctl.db — all other modules must
use MVMDatabase exclusively.

Database file: ~/.cache/mvmctl/mvmdb.db

CRITICAL DESIGN PRINCIPLES:
- All UPDATE queries must include a WHERE clause on the primary key.
- Table-level locks (UPDATE without WHERE) are forbidden.
- Multi-row atomic operations must use explicit BEGIN/COMMIT transactions.
"""

from __future__ import annotations

import sqlite3
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Generator, Optional

from mvmctl.db import MigrationRunner
from mvmctl.db.models import (
    Binary,
    HostState,
    HostStateChange,
    Image,
    IPTablesChain,
    IPTablesProtocol,
    IPTablesRule,
    IPTablesRuleType,
    IPTablesTable,
    Kernel,
    Network,
    NetworkLease,
    SSHKey,
    VMInstance,
)
from mvmctl.exceptions import DatabaseError
from mvmctl.utils.fs import get_mvm_db_path


class MVMDatabase:
    """SQLite database manager for mvmctl.

    This class is the single, authoritative interface for all database
    operations. No other module in the application may import from
    ``mvmctl.db`` directly.

    Usage::

        db = MVMDatabase()
        image = db.get_image("fbbcdb3b23...")
        db.upsert_image(image)
    """

    def __init__(self, db_path: Optional[Path] = None) -> None:
        self._db_path = db_path or get_mvm_db_path()
        self._db_path.parent.mkdir(parents=True, exist_ok=True)

    def _get_migrations_dir(self) -> Path:
        """Return the migrations directory bundled with the package."""
        import mvmctl.db

        return Path(mvmctl.db.__file__).parent / "migrations"

    @contextmanager
    def _connect(self) -> Generator[sqlite3.Connection, None, None]:
        """Context manager for database connections.

        Sets required PRAGMAs on every connection.
        Uses isolation_level=None (autocommit) for simplicity;
        multi-row atomic operations must use explicit BEGIN/COMMIT.
        """
        conn = sqlite3.connect(
            self._db_path,
            check_same_thread=False,
            isolation_level=None,
        )
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA foreign_keys = ON")
        conn.execute("PRAGMA journal_mode = WAL")
        conn.execute("PRAGMA synchronous = NORMAL")
        conn.execute("PRAGMA busy_timeout = 5000")
        conn.execute("PRAGMA wal_autocheckpoint = 1000")
        conn.execute("PRAGMA cache_size = -64000")
        try:
            yield conn
        finally:
            conn.close()

    def _ensure_schema_exists(self) -> None:
        try:
            with self._connect() as conn:
                conn.execute("SELECT 1 FROM vm_instances LIMIT 1").fetchone()
        except sqlite3.OperationalError:
            raise DatabaseError("Database not migrated. Run 'mvm init' first.") from None

    def migrate(self) -> int:
        """Run pending database migrations.

        Returns:
            Number of migrations applied.
        """
        runner = MigrationRunner(self._db_path, self._get_migrations_dir())
        return runner.migrate()

    def get_current_version(self) -> int:
        """Return the current schema version (PRAGMA user_version)."""
        runner = MigrationRunner(self._db_path, self._get_migrations_dir())
        return runner.get_current_version()

    # -------------------------------------------------------------------------
    # images
    # -------------------------------------------------------------------------

    def get_image(self, image_id: str) -> Optional[Image]:
        """Return an image by its full 64-char ID, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM images WHERE id = ?", (image_id,)).fetchone()
        if row is None:
            return None
        return Image(**dict(row))

    def find_images_by_prefix(self, prefix: str) -> list[Image]:
        """Return all images whose ID starts with prefix."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM images WHERE id LIKE ?", (f"{prefix}%",)).fetchall()
        return [Image(**dict(row)) for row in rows]

    def get_image_by_os_slug(self, os_slug: str) -> Optional[Image]:
        """Return an image by its os_slug, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM images WHERE os_slug = ?", (os_slug,)).fetchone()
        if row is None:
            return None
        return Image(**dict(row))

    def list_images(self) -> list[Image]:
        """Return all images."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM images ORDER BY created_at").fetchall()
        return [Image(**dict(row)) for row in rows]

    def upsert_image(self, image: Image) -> None:
        """Insert or replace an image record."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO images (
                    id, os_slug, os_name, arch, path, fs_type, fs_uuid,
                    compressed_size, original_size, compression_ratio,
                    compressed_format, minimum_rootfs_size_mib, pulled_at, is_default, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(os_slug) DO UPDATE SET
                    os_slug = excluded.os_slug,
                    os_name = excluded.os_name,
                    arch = excluded.arch,
                    path = excluded.path,
                    fs_type = excluded.fs_type,
                    fs_uuid = excluded.fs_uuid,
                    compressed_size = excluded.compressed_size,
                    original_size = excluded.original_size,
                    compression_ratio = excluded.compression_ratio,
                    compressed_format = excluded.compressed_format,
                    minimum_rootfs_size_mib = excluded.minimum_rootfs_size_mib,
                    pulled_at = excluded.pulled_at,
                    is_default = excluded.is_default,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    image.id,
                    image.os_slug,
                    image.os_name,
                    image.arch,
                    image.path,
                    image.fs_type,
                    image.fs_uuid,
                    image.compressed_size,
                    image.original_size,
                    image.compression_ratio,
                    image.compressed_format,
                    image.minimum_rootfs_size_mib,
                    image.pulled_at,
                    int(image.is_default),
                    image.created_at,
                    image.updated_at,
                ),
            )

    def delete_image(self, image_id: str) -> None:
        """Delete an image by ID. No-op if not found."""
        with self._connect() as conn:
            conn.execute("DELETE FROM images WHERE id = ?", (image_id,))

    def set_default_image(self, image_id: str) -> None:
        """Set one image as default, clearing all others.

        Uses explicit BEGIN/COMMIT for atomicity (two UPDATEEs).
        """
        with self._connect() as conn:
            conn.execute("BEGIN")
            conn.execute("UPDATE images SET is_default = 0")
            conn.execute("UPDATE images SET is_default = 1 WHERE id = ?", (image_id,))
            conn.execute("COMMIT")

    def get_default_image(self) -> Optional[Image]:
        """Return the default image entry, or None if not set."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM images WHERE is_default = 1 LIMIT 1").fetchone()
        if row is None:
            return None
        return Image(**dict(row))

    # -------------------------------------------------------------------------
    # kernels
    # -------------------------------------------------------------------------

    def get_kernel(self, kernel_id: str) -> Optional[Kernel]:
        """Return a kernel by its full 64-char ID, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM kernels WHERE id = ?", (kernel_id,)).fetchone()
        if row is None:
            return None
        return Kernel(**dict(row))

    def find_kernels_by_prefix(self, prefix: str) -> list[Kernel]:
        """Return all kernels whose ID starts with prefix."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM kernels WHERE id LIKE ?", (f"{prefix}%",)).fetchall()
        return [Kernel(**dict(row)) for row in rows]

    def list_kernels(self) -> list[Kernel]:
        """Return all kernels."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM kernels ORDER BY created_at").fetchall()
        return [Kernel(**dict(row)) for row in rows]

    def upsert_kernel(self, kernel: Kernel) -> None:
        """Insert or replace a kernel record."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO kernels (
                    id, name, base_name, version, arch, type, path,
                    is_default, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    base_name = excluded.base_name,
                    version = excluded.version,
                    arch = excluded.arch,
                    type = excluded.type,
                    path = excluded.path,
                    is_default = excluded.is_default,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    kernel.id,
                    kernel.name,
                    kernel.base_name,
                    kernel.version,
                    kernel.arch,
                    kernel.type,
                    kernel.path,
                    int(kernel.is_default),
                    kernel.created_at,
                    kernel.updated_at,
                ),
            )

    def delete_kernel(self, kernel_id: str) -> None:
        """Delete a kernel by ID. No-op if not found."""
        with self._connect() as conn:
            conn.execute("DELETE FROM kernels WHERE id = ?", (kernel_id,))

    def set_default_kernel(self, kernel_id: str) -> None:
        """Set one kernel as default, clearing all others atomically."""
        with self._connect() as conn:
            conn.execute("BEGIN")
            conn.execute("UPDATE kernels SET is_default = 0")
            conn.execute("UPDATE kernels SET is_default = 1 WHERE id = ?", (kernel_id,))
            conn.execute("COMMIT")

    def get_default_kernel(self) -> Optional[Kernel]:
        """Return the default kernel entry, or None if not set."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM kernels WHERE is_default = 1 LIMIT 1").fetchone()
        if row is None:
            return None
        return Kernel(**dict(row))

    def get_kernel_by_name(self, name: str) -> Optional[Kernel]:
        """Return a kernel by its name, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM kernels WHERE name = ? LIMIT 1", (name,)).fetchone()
        if row is None:
            return None
        return Kernel(**dict(row))

    def get_kernel_by_version_and_type(self, version: str, type: str) -> Optional[Kernel]:
        """Return a kernel by its version and type, or None if not found."""
        with self._connect() as conn:
            row = conn.execute(
                "SELECT * FROM kernels WHERE version = ? AND type = ? LIMIT 1",
                (version, type),
            ).fetchone()
        if row is None:
            return None
        return Kernel(**dict(row))

    # -------------------------------------------------------------------------
    # binaries
    # -------------------------------------------------------------------------

    def get_binary(self, binary_id: str) -> Optional[Binary]:
        """Return a binary by its full 64-char ID, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM binaries WHERE id = ?", (binary_id,)).fetchone()
        if row is None:
            return None
        return Binary(**dict(row))

    def find_binaries_by_prefix(self, prefix: str) -> list[Binary]:
        """Return all binaries whose ID starts with prefix."""
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM binaries WHERE id LIKE ?", (f"{prefix}%",)
            ).fetchall()
        return [Binary(**dict(row)) for row in rows]

    def list_binaries(self) -> list[Binary]:
        """Return all binaries."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM binaries ORDER BY created_at").fetchall()
        return [Binary(**dict(row)) for row in rows]

    def list_binaries_by_name(self, name: str) -> list[Binary]:
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM binaries WHERE name = ? ORDER BY created_at",
                (name,),
            ).fetchall()
        return [Binary(**dict(row)) for row in rows]

    def get_binary_by_name_and_version(self, name: str, version: str) -> Optional[Binary]:
        """Return a binary by its name and version, or None if not found."""
        with self._connect() as conn:
            row = conn.execute(
                "SELECT * FROM binaries WHERE name = ? AND version = ? LIMIT 1",
                (name, version),
            ).fetchone()
        if row is None:
            return None
        return Binary(**dict(row))

    def upsert_binary(self, binary: Binary) -> None:
        """Insert or replace a binary record."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO binaries (
                    id, name, version, full_version, ci_version, path,
                    is_default, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    version = excluded.version,
                    full_version = excluded.full_version,
                    ci_version = excluded.ci_version,
                    path = excluded.path,
                    is_default = excluded.is_default,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    binary.id,
                    binary.name,
                    binary.version,
                    binary.full_version,
                    binary.ci_version,
                    binary.path,
                    int(binary.is_default),
                    binary.created_at,
                    binary.updated_at,
                ),
            )

    def delete_binary(self, binary_id: str) -> None:
        with self._connect() as conn:
            conn.execute("DELETE FROM binaries WHERE id = ?", (binary_id,))

    def delete_binary_by_name_and_version(self, name: str, version: str) -> None:
        """Delete the binary row matching *name* AND *version*.

        Matches both the raw version string and its ``v``-prefixed form so that
        callers do not need to normalise before calling.  No-op if no row
        matches.  Scoped to a single (name, version) pair — never deletes
        rows for other binary names.
        """
        normalized = version.removeprefix("v")
        prefixed = f"v{normalized}"
        with self._connect() as conn:
            conn.execute(
                "DELETE FROM binaries WHERE name = ? AND (version = ? OR version = ?)",
                (name, normalized, prefixed),
            )

    def set_default_binary(self, name: str, version: str, path: str) -> None:
        """Set a binary as default, clearing all others with the same name atomically.

        Uses explicit BEGIN/COMMIT for atomicity.
        """
        with self._connect() as conn:
            conn.execute("BEGIN")
            # Clear is_default for all binaries with the same name
            conn.execute(
                "UPDATE binaries SET is_default = 0 WHERE name = ?",
                (name,),
            )
            # Set is_default = 1 for the matching binary
            conn.execute(
                """
                UPDATE binaries SET is_default = 1, updated_at = CURRENT_TIMESTAMP
                WHERE name = ? AND version = ?
                """,
                (name, version),
            )
            conn.execute("COMMIT")

    def get_default_binary(self, name: str) -> Optional[Binary]:
        """Return the default binary entry for a given name, or None."""
        with self._connect() as conn:
            row = conn.execute(
                "SELECT * FROM binaries WHERE name = ? AND is_default = 1 LIMIT 1",
                (name,),
            ).fetchone()
        if row is None:
            return None
        return Binary(**dict(row))

    # -------------------------------------------------------------------------
    # vm_instances
    # -------------------------------------------------------------------------

    def get_vm(self, vm_id: str) -> Optional[VMInstance]:
        """Return a VM by its full 64-char ID, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM vm_instances WHERE id = ?", (vm_id,)).fetchone()
        if row is None:
            return None
        return VMInstance(**dict(row))

    def get_vm_by_name(self, name: str) -> Optional[VMInstance]:
        """Return a VM by name, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM vm_instances WHERE name = ?", (name,)).fetchone()
        if row is None:
            return None
        return VMInstance(**dict(row))

    def find_vm_by_name(self, name: str) -> Optional[VMInstance]:
        return self.get_vm_by_name(name)

    def find_vm_by_ip(self, ipv4: str) -> Optional[VMInstance]:
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM vm_instances WHERE ipv4 = ?", (ipv4,)).fetchone()
        if row is None:
            return None
        return VMInstance(**dict(row))

    def find_vm_by_mac(self, mac: str) -> Optional[VMInstance]:
        """Return a VM by MAC address, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM vm_instances WHERE mac = ?", (mac,)).fetchone()
        if row is None:
            return None
        return VMInstance(**dict(row))

    def find_vms_by_prefix(self, prefix: str) -> list[VMInstance]:
        """Return all VMs whose ID starts with prefix."""
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE id LIKE ?", (f"{prefix}%",)
            ).fetchall()
        return [VMInstance(**dict(row)) for row in rows]

    def list_vms(self) -> list[VMInstance]:
        """Return all VM records."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM vm_instances ORDER BY created_at").fetchall()
        return [VMInstance(**dict(row)) for row in rows]

    def list_vms_by_status(self, statuses: list[str]) -> list[VMInstance]:
        """Return VM records filtered by status(es).

        Args:
            statuses: List of status values to filter by (e.g., ['running', 'stopped'])

        Returns:
            List of VMInstance objects matching the given statuses
        """
        if not statuses:
            return self.list_vms()

        placeholders = ",".join(["?"] * len(statuses))
        query = f"SELECT * FROM vm_instances WHERE status IN ({placeholders}) ORDER BY created_at"

        with self._connect() as conn:
            rows = conn.execute(query, statuses).fetchall()
        return [VMInstance(**dict(row)) for row in rows]

    def list_vms_excluding_statuses(self, excluded_statuses: list[str]) -> list[VMInstance]:
        """Return VM records excluding certain status(es).

        Args:
            excluded_statuses: List of status values to exclude (e.g., ['stopped', 'error'])

        Returns:
            List of VMInstance objects with status not in the excluded list
        """
        if not excluded_statuses:
            return self.list_vms()

        placeholders = ",".join(["?"] * len(excluded_statuses))
        query = (
            f"SELECT * FROM vm_instances WHERE status NOT IN ({placeholders}) ORDER BY created_at"
        )

        with self._connect() as conn:
            rows = conn.execute(query, excluded_statuses).fetchall()
        return [VMInstance(**dict(row)) for row in rows]

    def upsert_vm(self, vm: VMInstance) -> None:
        """Insert or replace a VM record."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO vm_instances (
                    id, name, status, pid, ipv4, mac, network_id, tap_device,
                    image_id, kernel_id, binary_id, api_socket_path,
                    relay_socket_path, config_path, cloud_init_mode,
                    nocloud_net_port, nocloud_net_pid, relay_pid,
                    exit_code, vcpu_count, mem_size_mib, disk_size_mib,
                    rootfs_path, rootfs_suffix, enable_pci, enable_logging,
                    enable_metrics, enable_console, created_at, updated_at
                    log_path, serial_output_path, lsm_flags, boot_args

                ) VALUES (
                    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
                    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
                )
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    status = excluded.status,
                    pid = excluded.pid,
                    ipv4 = excluded.ipv4,
                    mac = excluded.mac,
                    network_id = excluded.network_id,
                    tap_device = excluded.tap_device,
                    image_id = excluded.image_id,
                    kernel_id = excluded.kernel_id,
                    binary_id = excluded.binary_id,
                    api_socket_path = excluded.api_socket_path,
                    relay_socket_path = excluded.relay_socket_path,
                    config_path = excluded.config_path,
                    cloud_init_mode = excluded.cloud_init_mode,
                    nocloud_net_port = excluded.nocloud_net_port,
                    nocloud_net_pid = excluded.nocloud_net_pid,
                    relay_pid = excluded.relay_pid,
                    exit_code = excluded.exit_code,
                    vcpu_count = excluded.vcpu_count,
                    mem_size_mib = excluded.mem_size_mib,
                    disk_size_mib = excluded.disk_size_mib,
                    rootfs_path = excluded.rootfs_path,
                    rootfs_suffix = excluded.rootfs_suffix,
                    enable_pci = excluded.enable_pci,
                    enable_logging = excluded.enable_logging,
                    enable_metrics = excluded.enable_metrics,
                    enable_console = excluded.enable_console,
                    log_path = excluded.log_path,
                    serial_output_path = excluded.serial_output_path,
                    lsm_flags = excluded.lsm_flags,
                    boot_args = excluded.boot_args,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    vm.id,
                    vm.name,
                    vm.status,
                    vm.pid,
                    vm.ipv4,
                    vm.mac,
                    vm.network_id,
                    vm.tap_device,
                    vm.image_id,
                    vm.kernel_id,
                    vm.binary_id,
                    vm.api_socket_path,
                    vm.relay_socket_path,
                    vm.config_path,
                    vm.cloud_init_mode,
                    vm.nocloud_net_port,
                    vm.nocloud_net_pid,
                    vm.relay_pid,
                    vm.exit_code,
                    vm.vcpu_count,
                    vm.mem_size_mib,
                    vm.disk_size_mib,
                    vm.rootfs_path,
                    vm.rootfs_suffix,
                    vm.enable_pci,
                    vm.enable_logging,
                    vm.enable_metrics,
                    vm.enable_console,
                    vm.created_at,
                    vm.updated_at,
                    vm.log_path,
                    vm.serial_output_path,
                    vm.lsm_flags,
                    vm.boot_args,
                ),
            )

    def update_vm_status(self, vm_id: str, status: str) -> None:
        """Update only the VM status field (row-level lock via WHERE id = ?)."""
        with self._connect() as conn:
            conn.execute(
                "UPDATE vm_instances SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (status, vm_id),
            )

    def update_vm_pid(self, vm_id: str, pid: Optional[int]) -> None:
        """Update only the VM PID field."""
        with self._connect() as conn:
            conn.execute(
                "UPDATE vm_instances SET pid = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (pid, vm_id),
            )

    def delete_vm(self, vm_id: str) -> None:
        """Delete a VM by ID. No-op if not found."""
        with self._connect() as conn:
            conn.execute("DELETE FROM vm_instances WHERE id = ?", (vm_id,))

    # -------------------------------------------------------------------------
    # networks
    # -------------------------------------------------------------------------

    def get_network(self, network_id: str) -> Optional[Network]:
        """Return a network by its full 64-char ID, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM networks WHERE id = ?", (network_id,)).fetchone()
        if row is None:
            return None
        return Network(**dict(row))

    def get_network_by_name(self, name: str) -> Optional[Network]:
        """Return a network by name, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM networks WHERE name = ?", (name,)).fetchone()
        if row is None:
            return None
        return Network(**dict(row))

    def find_networks_by_prefix(self, prefix: str) -> list[Network]:
        """Return all networks whose ID starts with prefix."""
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM networks WHERE id LIKE ?", (f"{prefix}%",)
            ).fetchall()
        return [Network(**dict(row)) for row in rows]

    def list_networks(self) -> list[Network]:
        """Return all networks."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM networks ORDER BY created_at").fetchall()
        return [Network(**dict(row)) for row in rows]

    def upsert_network(self, network: Network) -> None:
        """Insert or replace a network record."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO networks (
                    id, name, subnet, bridge, ipv4_gateway, bridge_active,
                    nat_gateways, nat_enabled, is_default, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(name) DO UPDATE SET
                    subnet = excluded.subnet,
                    bridge = excluded.bridge,
                    ipv4_gateway = excluded.ipv4_gateway,
                    bridge_active = excluded.bridge_active,
                    nat_gateways = excluded.nat_gateways,
                    nat_enabled = excluded.nat_enabled,
                    is_default = excluded.is_default,
                    updated_at = CURRENT_TIMESTAMP
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
                    network.created_at,
                    network.updated_at,
                ),
            )

    def update_network_bridge_active(self, network_id: str, active: bool) -> None:
        """Update only the bridge_active field for a network."""
        with self._connect() as conn:
            conn.execute(
                "UPDATE networks SET bridge_active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (int(active), network_id),
            )

    def set_default_network(self, network_id: str) -> None:
        """Set one network as default, clearing all others atomically."""
        with self._connect() as conn:
            conn.execute("BEGIN")
            conn.execute("UPDATE networks SET is_default = 0")
            conn.execute("UPDATE networks SET is_default = 1 WHERE id = ?", (network_id,))
            conn.execute("COMMIT")

    def get_default_network(self) -> Optional[Network]:
        """Return the default network entry, or None if not set."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM networks WHERE is_default = 1 LIMIT 1").fetchone()
        if row is None:
            return None
        return Network(**dict(row))

    def delete_network(self, network_id: str) -> None:
        """Delete a network by ID. No-op if not found."""
        with self._connect() as conn:
            conn.execute("DELETE FROM networks WHERE id = ?", (network_id,))

    # -------------------------------------------------------------------------
    # network_leases
    # -------------------------------------------------------------------------

    def get_lease(self, network_id: str, ipv4: str) -> Optional[NetworkLease]:
        """Return a lease by network_id + ipv4, or None if not found."""
        with self._connect() as conn:
            row = conn.execute(
                "SELECT * FROM network_leases WHERE network_id = ? AND ipv4 = ?",
                (network_id, ipv4),
            ).fetchone()
        if row is None:
            return None
        return NetworkLease(**dict(row))

    def list_leases(self, network_id: str) -> list[NetworkLease]:
        """Return all leases for a network."""
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM network_leases WHERE network_id = ? ORDER BY leased_at",
                (network_id,),
            ).fetchall()
        return [NetworkLease(**dict(row)) for row in rows]

    def list_leases_for_vm(self, network_id: str, vm_id: str) -> list[NetworkLease]:
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM network_leases WHERE network_id = ? AND vm_id = ? ORDER BY leased_at",
                (network_id, vm_id),
            ).fetchall()
        return [NetworkLease(**dict(row)) for row in rows]

    def acquire_lease(
        self, network_id: str, ipv4: str, vm_id: Optional[str] = None
    ) -> NetworkLease:
        """Atomically acquire an IP lease.

        Uses explicit BEGIN/COMMIT to ensure check-then-insert is atomic.

        Raises:
            sqlite3.IntegrityError: If the IP is already leased on this network.
        """
        with self._connect() as conn:
            conn.execute("BEGIN")
            conn.execute(
                "INSERT INTO network_leases (network_id, ipv4, vm_id) VALUES (?, ?, ?)",
                (network_id, ipv4, vm_id),
            )
            conn.execute("COMMIT")
        lease = self.get_lease(network_id, ipv4)
        assert lease is not None
        return lease

    def release_lease(self, network_id: str, ipv4: str) -> None:
        """Release an IP lease. No-op if not found."""
        with self._connect() as conn:
            conn.execute(
                "DELETE FROM network_leases WHERE network_id = ? AND ipv4 = ?",
                (network_id, ipv4),
            )

    def release_vm_leases(self, vm_id: str) -> None:
        """Release all IP leases held by a VM."""
        with self._connect() as conn:
            conn.execute("DELETE FROM network_leases WHERE vm_id = ?", (vm_id,))

    # -------------------------------------------------------------------------
    # host_state (singleton — always id=1)
    # -------------------------------------------------------------------------

    def get_host_state(self) -> Optional[HostState]:
        """Return the singleton host state row, or None if not yet initialized."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM host_state WHERE id = 1").fetchone()
        if row is None:
            return None
        return HostState(**dict(row))

    def initialize_host_state(self) -> HostState:
        """Insert the singleton host state row (id=1) if it doesn't exist.

        No-op if the row already exists. Returns the current host state.
        """
        self.migrate()
        with self._connect() as conn:
            conn.execute(
                """
                INSERT OR IGNORE INTO host_state
                (id, initialized, mvm_group_created, sudoers_configured, default_network_created, initialized_at, updated_at)
                VALUES (1, 0, 0, 0, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
                """
            )
        host_state = self.get_host_state()
        assert host_state is not None
        return host_state

    def set_host_initialized(self, initialized_at: str) -> None:
        """Mark host as fully initialized.

        Args:
            initialized_at: ISO-format timestamp of when initialization completed.
        """
        with self._connect() as conn:
            conn.execute(
                """
                UPDATE host_state
                SET initialized = 1, initialized_at = ?, updated_at = CURRENT_TIMESTAMP
                WHERE id = 1
                """,
                (initialized_at,),
            )

    def update_host_component(self, component: str, value: bool) -> None:
        """Update a single host initialization component flag.

        Args:
            component: One of "mvm_group_created", "sudoers_configured",
                       "default_network_created".
            value: True if the component is configured, False otherwise.

        Raises:
            ValueError: If component is not a recognized host state field.
        """
        allowed = {"mvm_group_created", "sudoers_configured", "default_network_created"}
        if component not in allowed:
            raise ValueError(
                f"Unknown host state component: {component!r}. Must be one of: {sorted(allowed)}"
            )
        with self._connect() as conn:
            conn.execute(
                f"UPDATE host_state SET {component} = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
                (int(value),),
            )

    def reset_host_state(self) -> None:
        """Reset all host state flags to False (for mvm host reset)."""
        with self._connect() as conn:
            conn.execute(
                """
                UPDATE host_state SET
                    initialized = 0,
                    mvm_group_created = 0,
                    sudoers_configured = 0,
                    default_network_created = 0,
                    updated_at = CURRENT_TIMESTAMP
                WHERE id = 1
                """
            )

    # -------------------------------------------------------------------------
    # host_state_changes (LIFO revert for mvm host reset)
    # -------------------------------------------------------------------------

    def add_host_change(self, change: HostStateChange) -> None:
        """Record a host configuration change made during mvm host init."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO host_state_changes (
                    session_id, init_timestamp, setting, mechanism,
                    original_value, applied_value, reverted, reverted_at,
                    revert_mechanism, change_order, created_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
                """,
                (
                    change.session_id,
                    change.init_timestamp,
                    change.setting,
                    change.mechanism,
                    change.original_value,
                    change.applied_value,
                    int(change.reverted),
                    change.reverted_at,
                    change.revert_mechanism,
                    change.change_order,
                ),
            )

    def list_host_changes(
        self, session_id: Optional[str] = None, include_reverted: bool = True
    ) -> list[HostStateChange]:
        """Return host state changes, optionally filtered by session.

        Args:
            session_id: If provided, return only changes for this session.
            include_reverted: If False, exclude already-reverted changes.

        Returns:
            Changes ordered by change_order ASC (use reversed() for LIFO revert).
        """
        query = "SELECT * FROM host_state_changes"
        params: list[object] = []
        conditions: list[str] = []

        if session_id is not None:
            conditions.append("session_id = ?")
            params.append(session_id)
        if not include_reverted:
            conditions.append("reverted = 0")

        if conditions:
            query += " WHERE " + " AND ".join(conditions)
        query += " ORDER BY change_order ASC"

        with self._connect() as conn:
            rows = conn.execute(query, params).fetchall()
        return [HostStateChange(**dict(row)) for row in rows]

    def mark_change_reverted(
        self, change_id: int, reverted_at: str, revert_mechanism: Optional[str] = None
    ) -> None:
        """Mark a single host change as reverted.

        Args:
            change_id: The auto-increment ID of the change record.
            reverted_at: ISO-format timestamp of when the change was reverted.
            revert_mechanism: Optional description of how it was reverted.
        """
        with self._connect() as conn:
            conn.execute(
                """
                UPDATE host_state_changes
                SET reverted = 1, reverted_at = ?, revert_mechanism = ?
                WHERE id = ?
                """,
                (reverted_at, revert_mechanism, change_id),
            )

    def revert_host_changes(self, session_id: str, reverted_at: str) -> list[HostStateChange]:
        """Mark all unreverted changes for a session as reverted (LIFO order).

        Returns the list of changes that were marked reverted, in the order
        they should be processed (change_order DESC — last in, first out).

        This method marks the changes in the database but does NOT execute
        the actual revert operations — the caller is responsible for that.
        """
        changes = self.list_host_changes(session_id=session_id, include_reverted=False)
        for change in reversed(changes):
            if change.id is not None:
                self.mark_change_reverted(change.id, reverted_at)
        return list(reversed(changes))

    # -------------------------------------------------------------------------
    # iptables_rules
    # -------------------------------------------------------------------------

    def record_iptables_rule(self, rule: IPTablesRule) -> IPTablesRule:
        """Insert a new iptables rule record.

        Returns the rule with the generated id populated.
        """
        with self._connect() as conn:
            cursor = conn.execute(
                """
                INSERT INTO iptables_rules (
                    table_name, chain_name, rule_type, protocol, source, destination,
                    in_interface, out_interface, target, sport, dport,
                    network_id, comment_tag, command_string, created_at, is_active
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    rule.table_name,
                    rule.chain_name,
                    rule.rule_type.value,
                    rule.protocol.value,
                    rule.source,
                    rule.destination,
                    rule.in_interface,
                    rule.out_interface,
                    rule.target,
                    rule.sport,
                    rule.dport,
                    rule.network_id,
                    rule.comment_tag,
                    rule.command_string,
                    rule.created_at or datetime.now(tz=timezone.utc).isoformat(),
                    int(rule.is_active),
                ),
            )
            rule.id = cursor.lastrowid
        return rule

    def get_iptables_rules_for_network(
        self, network_id: str, active_only: bool = True
    ) -> list[IPTablesRule]:
        """Get all iptables rules for a specific network."""
        with self._connect() as conn:
            if active_only:
                rows = conn.execute(
                    "SELECT * FROM iptables_rules WHERE network_id = ? AND is_active = 1",
                    (network_id,),
                ).fetchall()
            else:
                rows = conn.execute(
                    "SELECT * FROM iptables_rules WHERE network_id = ?",
                    (network_id,),
                ).fetchall()
        return [self._row_to_iptables_rule(row) for row in rows]

    def get_iptables_rules_for_chain(
        self, table_name: str, chain_name: str, active_only: bool = True
    ) -> list[IPTablesRule]:
        """Get all rules for a specific chain."""
        with self._connect() as conn:
            if active_only:
                rows = conn.execute(
                    """SELECT * FROM iptables_rules
                       WHERE table_name = ? AND chain_name = ? AND is_active = 1""",
                    (table_name, chain_name),
                ).fetchall()
            else:
                rows = conn.execute(
                    """SELECT * FROM iptables_rules
                       WHERE table_name = ? AND chain_name = ?""",
                    (table_name, chain_name),
                ).fetchall()
        return [self._row_to_iptables_rule(row) for row in rows]

    def update_iptables_rule_verified(self, rule_id: int) -> None:
        """Update the last_verified_at timestamp for a rule."""
        with self._connect() as conn:
            conn.execute(
                """UPDATE iptables_rules
                   SET last_verified_at = CURRENT_TIMESTAMP
                   WHERE id = ?""",
                (rule_id,),
            )

    def mark_iptables_rule_deleted(self, rule_id: int) -> None:
        """Soft delete a rule (mark is_active=0)."""
        with self._connect() as conn:
            conn.execute(
                "UPDATE iptables_rules SET is_active = 0 WHERE id = ?",
                (rule_id,),
            )

    def delete_iptables_rules_for_network(self, network_id: str) -> int:
        """Delete all iptables rules for a network (hard delete).

        Note: CASCADE delete on networks table also handles this.
        Returns number of rows deleted.
        """
        with self._connect() as conn:
            cursor = conn.execute(
                "DELETE FROM iptables_rules WHERE network_id = ?",
                (network_id,),
            )
        return cursor.rowcount

    def cleanup_inactive_iptables_rules(self) -> int:
        """Hard delete all inactive iptables rules (is_active=0).

        This is a maintenance operation to remove soft-deleted records
        that are no longer needed for audit purposes.

        Returns:
            Number of records permanently deleted.
        """
        with self._connect() as conn:
            cursor = conn.execute("DELETE FROM iptables_rules WHERE is_active = 0")
        return cursor.rowcount

    def mark_iptables_rules_deleted_for_chain(
        self, table_name: IPTablesTable, chain_name: IPTablesChain
    ) -> int:
        """Soft delete all active rules for a specific chain.

        Marks all rules with is_active=1 for the given table/chain as is_active=0.
        Returns the number of rules marked as deleted.
        """
        with self._connect() as conn:
            cursor = conn.execute(
                """UPDATE iptables_rules
                   SET is_active = 0
                   WHERE table_name = ? AND chain_name = ? AND is_active = 1""",
                (table_name.value, chain_name.value),
            )
        return cursor.rowcount

    def find_iptables_rule_by_attributes(
        self,
        table_name: IPTablesTable,
        chain_name: IPTablesChain,
        rule_type: IPTablesRuleType,
        network_id: str,
        protocol: IPTablesProtocol,
        source: str,
        destination: str,
        in_interface: str,
        out_interface: str,
        sport: int,
        dport: int,
    ) -> Optional[IPTablesRule]:
        """Find an iptables rule by its unique attributes.

        Returns the rule if found, None otherwise.
        """
        with self._connect() as conn:
            row = conn.execute(
                """SELECT * FROM iptables_rules
                   WHERE table_name = ? AND chain_name = ? AND rule_type = ?
                   AND network_id = ? AND protocol = ? AND source = ?
                   AND destination = ? AND in_interface = ? AND out_interface = ?
                   AND sport = ? AND dport = ? AND is_active = 1""",
                (
                    table_name.value,
                    chain_name.value,
                    rule_type.value,
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
        return self._row_to_iptables_rule(row)

    def _row_to_iptables_rule(self, row: sqlite3.Row) -> IPTablesRule:
        """Convert DB row to IPTablesRule dataclass."""
        row_dict = dict(row)
        row_dict["rule_type"] = IPTablesRuleType(row_dict["rule_type"])
        row_dict["protocol"] = IPTablesProtocol(row_dict["protocol"])
        row_dict["is_active"] = bool(row_dict["is_active"])
        return IPTablesRule(**row_dict)

    # -------------------------------------------------------------------------
    # ssh_keys
    # -------------------------------------------------------------------------

    def get_ssh_key(self, key_id: str) -> Optional[SSHKey]:
        """Return an SSH key by its ID (fingerprint), or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM ssh_keys WHERE id = ?", (key_id,)).fetchone()
        if row is None:
            return None
        return SSHKey(**dict(row))

    def get_ssh_key_by_name(self, name: str) -> Optional[SSHKey]:
        """Return an SSH key by name, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM ssh_keys WHERE name = ?", (name,)).fetchone()
        if row is None:
            return None
        return SSHKey(**dict(row))

    def find_ssh_keys_by_prefix(self, prefix: str) -> list[SSHKey]:
        """Return all SSH keys whose ID starts with prefix."""
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM ssh_keys WHERE id LIKE ?", (f"{prefix}%",)
            ).fetchall()
        return [SSHKey(**dict(row)) for row in rows]

    def find_ssh_keys_by_fingerprint_prefix(self, prefix: str) -> list[SSHKey]:
        """Return all SSH keys whose fingerprint starts with prefix."""
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM ssh_keys WHERE fingerprint LIKE ?", (f"{prefix}%",)
            ).fetchall()
        return [SSHKey(**dict(row)) for row in rows]

    def list_ssh_keys(self) -> list[SSHKey]:
        """Return all SSH keys."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM ssh_keys ORDER BY created_at").fetchall()
        return [SSHKey(**dict(row)) for row in rows]

    def upsert_ssh_key(self, key: SSHKey) -> None:
        """Insert or replace an SSH key record."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO ssh_keys (
                    id, name, fingerprint, algorithm, comment,
                    private_key_path, public_key_path, is_default, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    fingerprint = excluded.fingerprint,
                    algorithm = excluded.algorithm,
                    comment = excluded.comment,
                    private_key_path = excluded.private_key_path,
                    public_key_path = excluded.public_key_path,
                    is_default = excluded.is_default,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    key.id,
                    key.name,
                    key.fingerprint,
                    key.algorithm,
                    key.comment,
                    key.private_key_path,
                    key.public_key_path,
                    int(key.is_default),
                    key.created_at,
                    key.updated_at,
                ),
            )

    def delete_ssh_key(self, key_id: str) -> None:
        """Delete an SSH key by ID. No-op if not found."""
        with self._connect() as conn:
            conn.execute("DELETE FROM ssh_keys WHERE id = ?", (key_id,))

    def delete_ssh_key_by_name(self, name: str) -> None:
        """Delete an SSH key by name. No-op if not found."""
        with self._connect() as conn:
            conn.execute("DELETE FROM ssh_keys WHERE name = ?", (name,))

    def set_default_ssh_key(self, key_id: str) -> None:
        """Set one SSH key as default, clearing all others atomically."""
        with self._connect() as conn:
            conn.execute("BEGIN")
            conn.execute("UPDATE ssh_keys SET is_default = 0")
            conn.execute("UPDATE ssh_keys SET is_default = 1 WHERE id = ?", (key_id,))
            conn.execute("COMMIT")

    def add_default_ssh_key(self, key_id: str) -> None:
        """Add an SSH key to the default list without clearing existing defaults."""
        with self._connect() as conn:
            conn.execute("UPDATE ssh_keys SET is_default = 1 WHERE id = ?", (key_id,))

    def remove_default_ssh_key(self, key_id: str) -> None:
        """Remove an SSH key from the default list."""
        with self._connect() as conn:
            conn.execute("UPDATE ssh_keys SET is_default = 0 WHERE id = ?", (key_id,))

    def get_default_ssh_keys(self) -> list[SSHKey]:
        """Return all SSH keys marked as default."""
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM ssh_keys WHERE is_default = 1 ORDER BY created_at"
            ).fetchall()
        return [SSHKey(**dict(row)) for row in rows]

    def clear_default_ssh_keys(self) -> None:
        """Clear all default SSH keys."""
        with self._connect() as conn:
            conn.execute("UPDATE ssh_keys SET is_default = 0")

    def set_default_ssh_keys_bulk(self, key_ids: list[str]) -> None:
        """Set multiple SSH keys as default in a single transaction.

        Clears all existing defaults and sets the specified keys as default.
        """
        with self._connect() as conn:
            conn.execute("BEGIN")
            conn.execute("UPDATE ssh_keys SET is_default = 0")
            if key_ids:
                placeholders = ",".join(["?"] * len(key_ids))
                conn.execute(
                    f"UPDATE ssh_keys SET is_default = 1 WHERE id IN ({placeholders})",
                    key_ids,
                )
            conn.execute("COMMIT")
