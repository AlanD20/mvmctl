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
from pathlib import Path
from typing import Generator, Optional

from mvmctl.db import MigrationRunner
from mvmctl.db.models import (
    Binary,
    BinaryDefault,
    HostState,
    HostStateChange,
    Image,
    Kernel,
    Network,
    NetworkLease,
    VMState,
)
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
                    id, os_slug, os_name, path, fs_type, fs_uuid,
                    compressed_size, original_size, compression_ratio,
                    compressed_format, pulled_at, is_default, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    os_slug = excluded.os_slug,
                    os_name = excluded.os_name,
                    path = excluded.path,
                    fs_type = excluded.fs_type,
                    fs_uuid = excluded.fs_uuid,
                    compressed_size = excluded.compressed_size,
                    original_size = excluded.original_size,
                    compression_ratio = excluded.compression_ratio,
                    compressed_format = excluded.compressed_format,
                    pulled_at = excluded.pulled_at,
                    is_default = excluded.is_default,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    image.id,
                    image.os_slug,
                    image.os_name,
                    image.path,
                    image.fs_type,
                    image.fs_uuid,
                    image.compressed_size,
                    image.original_size,
                    image.compression_ratio,
                    image.compressed_format,
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

        Uses explicit BEGIN/COMMIT for atomicity (two UPDATEs).
        """
        with self._connect() as conn:
            conn.execute("BEGIN")
            conn.execute("UPDATE images SET is_default = 0")
            conn.execute("UPDATE images SET is_default = 1 WHERE id = ?", (image_id,))
            conn.execute("COMMIT")

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

    def upsert_binary(self, binary: Binary) -> None:
        """Insert or replace a binary record."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO binaries (
                    id, name, version, full_version, ci_version, path,
                    created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    version = excluded.version,
                    full_version = excluded.full_version,
                    ci_version = excluded.ci_version,
                    path = excluded.path,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    binary.id,
                    binary.name,
                    binary.version,
                    binary.full_version,
                    binary.ci_version,
                    binary.path,
                    binary.created_at,
                    binary.updated_at,
                ),
            )

    def delete_binary(self, binary_id: str) -> None:
        with self._connect() as conn:
            conn.execute("DELETE FROM binaries WHERE id = ?", (binary_id,))

    def set_default_binary(self, name: str, version: str, path: str) -> None:
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO binary_defaults (name, version, path, updated_at)
                VALUES (?, ?, ?, CURRENT_TIMESTAMP)
                ON CONFLICT(name) DO UPDATE SET
                    version = excluded.version,
                    path = excluded.path,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (name, version, path),
            )

    # -------------------------------------------------------------------------
    # binary_defaults
    # -------------------------------------------------------------------------

    def get_binary_default(self, name: str) -> Optional[BinaryDefault]:
        """Return the default binary entry for a given name, or None."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM binary_defaults WHERE name = ?", (name,)).fetchone()
        if row is None:
            return None
        return BinaryDefault(**dict(row))

    def list_binary_defaults(self) -> list[BinaryDefault]:
        """Return all binary default entries."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM binary_defaults").fetchall()
        return [BinaryDefault(**dict(row)) for row in rows]

    def upsert_binary_default(self, default: BinaryDefault) -> None:
        """Insert or replace a binary default record."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO binary_defaults (name, version, path, updated_at)
                VALUES (?, ?, ?, ?)
                ON CONFLICT(name) DO UPDATE SET
                    version = excluded.version,
                    path = excluded.path,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (default.name, default.version, default.path, default.updated_at),
            )

    def delete_binary_default(self, name: str) -> None:
        """Delete a binary default by name. No-op if not found."""
        with self._connect() as conn:
            conn.execute("DELETE FROM binary_defaults WHERE name = ?", (name,))

    # -------------------------------------------------------------------------
    # vm_states
    # -------------------------------------------------------------------------

    def get_vm(self, vm_id: str) -> Optional[VMState]:
        """Return a VM state by its full 64-char ID, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM vm_states WHERE id = ?", (vm_id,)).fetchone()
        if row is None:
            return None
        return VMState(**dict(row))

    def get_vm_by_name(self, name: str) -> Optional[VMState]:
        """Return a VM state by name, or None if not found."""
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM vm_states WHERE name = ?", (name,)).fetchone()
        if row is None:
            return None
        return VMState(**dict(row))

    def find_vm_by_name(self, name: str) -> Optional[VMState]:
        return self.get_vm_by_name(name)

    def find_vm_by_ip(self, ipv4: str) -> Optional[VMState]:
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM vm_states WHERE ipv4 = ?", (ipv4,)).fetchone()
        if row is None:
            return None
        return VMState(**dict(row))

    def find_vms_by_prefix(self, prefix: str) -> list[VMState]:
        """Return all VMs whose ID starts with prefix."""
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_states WHERE id LIKE ?", (f"{prefix}%",)
            ).fetchall()
        return [VMState(**dict(row)) for row in rows]

    def list_vms(self) -> list[VMState]:
        """Return all VM states."""
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM vm_states ORDER BY created_at").fetchall()
        return [VMState(**dict(row)) for row in rows]

    def upsert_vm(self, vm: VMState) -> None:
        """Insert or replace a VM state record."""
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO vm_states (
                    id, name, status, pid, ipv4, mac, network_id, tap_device,
                    image_id, kernel_id, binary_id, api_socket_path,
                    console_socket_path, config_path, cloud_init_mode,
                    nocloud_net_port, nocloud_server_pid, console_relay_pid,
                    exit_code, vcpu_count, mem_size_mib, disk_size_mib,
                    rootfs_path, rootfs_suffix, created_at, updated_at
                ) VALUES (
                    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
                    ?, ?, ?, ?, ?, ?, ?
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
                    console_socket_path = excluded.console_socket_path,
                    config_path = excluded.config_path,
                    cloud_init_mode = excluded.cloud_init_mode,
                    nocloud_net_port = excluded.nocloud_net_port,
                    nocloud_server_pid = excluded.nocloud_server_pid,
                    console_relay_pid = excluded.console_relay_pid,
                    exit_code = excluded.exit_code,
                    vcpu_count = excluded.vcpu_count,
                    mem_size_mib = excluded.mem_size_mib,
                    disk_size_mib = excluded.disk_size_mib,
                    rootfs_path = excluded.rootfs_path,
                    rootfs_suffix = excluded.rootfs_suffix,
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
                    vm.console_socket_path,
                    vm.config_path,
                    vm.cloud_init_mode,
                    vm.nocloud_net_port,
                    vm.nocloud_server_pid,
                    vm.console_relay_pid,
                    vm.exit_code,
                    vm.vcpu_count,
                    vm.mem_size_mib,
                    vm.disk_size_mib,
                    vm.rootfs_path,
                    vm.rootfs_suffix,
                    vm.created_at,
                    vm.updated_at,
                ),
            )

    def update_vm_status(self, vm_id: str, status: str) -> None:
        """Update only the VM status field (row-level lock via WHERE id = ?)."""
        with self._connect() as conn:
            conn.execute(
                "UPDATE vm_states SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (status, vm_id),
            )

    def update_vm_pid(self, vm_id: str, pid: Optional[int]) -> None:
        """Update only the VM PID field."""
        with self._connect() as conn:
            conn.execute(
                "UPDATE vm_states SET pid = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (pid, vm_id),
            )

    def delete_vm(self, vm_id: str) -> None:
        """Delete a VM state by ID. No-op if not found."""
        with self._connect() as conn:
            conn.execute("DELETE FROM vm_states WHERE id = ?", (vm_id,))

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
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
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
        with self._connect() as conn:
            conn.execute(
                """
                INSERT OR IGNORE INTO host_state (id, initialized, updated_at)
                VALUES (1, 0, CURRENT_TIMESTAMP)
                """
            )
        return self.get_host_state()  # type: ignore[return-value]

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
        """Reset all host state flags to False/None (for mvm host reset)."""
        with self._connect() as conn:
            conn.execute(
                """
                UPDATE host_state SET
                    initialized = 0,
                    mvm_group_created = 0,
                    sudoers_configured = 0,
                    default_network_created = 0,
                    initialized_at = NULL,
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
