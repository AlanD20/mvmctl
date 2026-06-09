"""VM database operations - Repository Pattern implementation."""

from __future__ import annotations

import json

from mvmctl.core._shared._db import Database, _graceful_read
from mvmctl.models import VMInstanceItem, VMStatus


class VMRepository:
    """Database operations for VM instances."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    @_graceful_read(default=None)
    def get(self, vm_id: str) -> VMInstanceItem | None:
        """Return a VM by its full 64-char ID, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM vm_instances WHERE id = ?", (vm_id,)
            ).fetchone()
        if row is None:
            return None
        return VMInstanceItem(**dict(row))

    @_graceful_read(default=None)
    def get_by_name(self, name: str) -> VMInstanceItem | None:
        """Return a VM by name, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM vm_instances WHERE name = ?", (name,)
            ).fetchone()
        if row is None:
            return None
        return VMInstanceItem(**dict(row))

    def get_by_names(self, names: list[str]) -> set[str]:
        """Return the set of VM names that already exist from a list.

        Uses a single ``WHERE name IN (...)` query instead of N individual
        lookups, saving N-1 round trips.

        Args:
            names: List of VM names to check for collisions.

        Returns:
            Set of names from the input that already exist in the database.

        """
        if not names:
            return set()
        placeholders = ",".join("?" for _ in names)
        with self._db.connect() as conn:
            rows = conn.execute(
                f"SELECT name FROM vm_instances WHERE name IN ({placeholders})",
                names,
            ).fetchall()
        return {row["name"] for row in rows}

    @_graceful_read(default=None)
    def find_by_ip(self, ipv4: str) -> VMInstanceItem | None:
        """Return a VM by IP address, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM vm_instances WHERE ipv4 = ?", (ipv4,)
            ).fetchone()
        if row is None:
            return None
        return VMInstanceItem(**dict(row))

    @_graceful_read(default=None)
    def find_by_mac(self, mac: str) -> VMInstanceItem | None:
        """Return a VM by MAC address, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM vm_instances WHERE mac = ?", (mac,)
            ).fetchone()
        if row is None:
            return None
        return VMInstanceItem(**dict(row))

    @_graceful_read(factory=list)
    def find_by_prefix(self, prefix: str) -> list[VMInstanceItem]:
        """Return all VMs whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE id LIKE ?",
                (f"{prefix}%",),
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(default=0)
    def count(self) -> int:
        """Return total count of all VMs."""
        with self._db.connect() as conn:
            result = conn.execute(
                "SELECT COUNT(*) FROM vm_instances"
            ).fetchone()
        return result[0] if result else 0

    @_graceful_read(default=0)
    def count_by_status(self, status: VMStatus | list[VMStatus]) -> int:
        """Count VMs by status(es). Accepts single status or list of statuses."""
        statuses = [status] if isinstance(status, VMStatus) else status
        if not statuses:
            return self.count()

        status_values = [s.value for s in statuses]
        placeholders = ",".join(["?"] * len(status_values))
        query = f"SELECT COUNT(*) FROM vm_instances WHERE status IN ({placeholders})"

        with self._db.connect() as conn:
            result = conn.execute(query, status_values).fetchone()
        return result[0] if result else 0

    @_graceful_read(factory=list)
    def find_by_network_id(self, network_id: str) -> list[VMInstanceItem]:
        """Return all VMs that reference the given network ID."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE network_id = ?",
                (network_id,),
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def get_by_network_ids(
        self, network_ids: list[str]
    ) -> list[VMInstanceItem]:
        """Return all VMs referencing any of the given network IDs."""
        if not network_ids:
            return []
        placeholders = ",".join("?" * len(network_ids))
        query = (
            f"SELECT * FROM vm_instances WHERE network_id IN ({placeholders})"
        )
        with self._db.connect() as conn:
            rows = conn.execute(query, network_ids).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def find_by_kernel_id(self, kernel_id: str) -> list[VMInstanceItem]:
        """Return all VMs that reference the given kernel ID."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE kernel_id = ?",
                (kernel_id,),
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def get_by_kernel_ids(self, kernel_ids: list[str]) -> list[VMInstanceItem]:
        """Return all VMs referencing any of the given kernel IDs."""
        if not kernel_ids:
            return []
        placeholders = ",".join("?" * len(kernel_ids))
        query = (
            f"SELECT * FROM vm_instances WHERE kernel_id IN ({placeholders})"
        )
        with self._db.connect() as conn:
            rows = conn.execute(query, kernel_ids).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def find_by_binary_id(self, binary_id: str) -> list[VMInstanceItem]:
        """Return all VMs that reference the given binary ID."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE binary_id = ?",
                (binary_id,),
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def get_by_binary_ids(self, binary_ids: list[str]) -> list[VMInstanceItem]:
        """Return all VMs referencing any of the given binary IDs."""
        if not binary_ids:
            return []
        placeholders = ",".join("?" * len(binary_ids))
        query = (
            f"SELECT * FROM vm_instances WHERE binary_id IN ({placeholders})"
        )
        with self._db.connect() as conn:
            rows = conn.execute(query, binary_ids).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def list_all(self) -> list[VMInstanceItem]:
        """Return all VM records."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances ORDER BY created_at"
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def list_by_status(
        self, status: VMStatus | list[VMStatus]
    ) -> list[VMInstanceItem]:
        """Return VM records filtered by status(es). Accepts single status or list of statuses."""
        statuses = [status] if isinstance(status, VMStatus) else status
        if not statuses:
            return self.list_all()

        status_values = [s.value for s in statuses]
        placeholders = ",".join(["?"] * len(status_values))
        query = f"SELECT * FROM vm_instances WHERE status IN ({placeholders}) ORDER BY created_at"

        with self._db.connect() as conn:
            rows = conn.execute(query, status_values).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def find_by_volume_id(self, volume_id: str) -> list[VMInstanceItem]:
        """Return all VMs whose volume_ids contain the given volume ID.

        Args:
            volume_id: The volume ID to search for in VM volume_ids lists.

        Returns:
            List of VMInstanceItem records that reference this volume.

        """
        with self._db.connect() as conn:
            # Use LIKE with JSON-quoted ID to match inside the JSON array
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE volume_ids LIKE ?",
                (f'%"{volume_id}"%',),
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def find_by_volume_ids_batch(
        self, volume_ids: list[str]
    ) -> list[VMInstanceItem]:
        """Return all VMs whose volume_ids contain any of the given volume IDs.

        Args:
            volume_ids: List of volume IDs to search for.

        Returns:
            List of VMInstanceItem records that reference any of the given volumes.

        """
        with self._db.connect() as conn:
            patterns = [f'%"{vid}"%' for vid in volume_ids]
            rows = conn.execute(
                f"SELECT DISTINCT vm_instances.* FROM vm_instances "
                f"WHERE {' OR '.join('volume_ids LIKE ?' for _ in volume_ids)}",
                patterns,
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    def find_by_ssh_key_id(self, key_id: str) -> list[VMInstanceItem]:
        """Return all VMs whose ssh_keys contain the given key ID.

        Args:
            key_id: The SSH key ID (fingerprint) to search for.

        Returns:
            List of VMInstanceItem records that reference this key.

        """
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE ssh_keys LIKE ?",
                (f'%"{key_id}"%',),
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    @_graceful_read(factory=list)
    def list_excluding_statuses(
        self, excluded_statuses: VMStatus | list[VMStatus]
    ) -> list[VMInstanceItem]:
        """Return VM records excluding certain status(es). Accepts single status or list of statuses."""
        statuses = (
            [excluded_statuses]
            if isinstance(excluded_statuses, VMStatus)
            else excluded_statuses
        )
        if not statuses:
            return self.list_all()

        status_values = [s.value for s in statuses]
        placeholders = ",".join(["?"] * len(status_values))
        query = f"SELECT * FROM vm_instances WHERE status NOT IN ({placeholders}) ORDER BY created_at"

        with self._db.connect() as conn:
            rows = conn.execute(query, status_values).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    def upsert(self, vm: VMInstanceItem) -> None:
        """Insert or replace a VM record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO vm_instances (
                    id, name, status, pid, process_start_time, ipv4, mac, network_id, tap_device,
                    image_id, kernel_id, binary_id, api_socket_path,
                    relay_socket_path, config_path, cloud_init_mode,
                    nocloud_net_port, nocloud_net_pid, relay_pid,
                    exit_code, vcpu_count, mem_size_mib, disk_size_mib,
                    rootfs_path, rootfs_suffix, pci_enabled, nested_virt,
                    enable_logging, enable_metrics, enable_console,
                    ssh_keys, ssh_user,
                    created_at, updated_at,
                    log_path, serial_output_path, lsm_flags, boot_args, volume_ids, cpu_config
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    status = excluded.status,
                    pid = excluded.pid,
                    process_start_time = excluded.process_start_time,
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
                    pci_enabled = excluded.pci_enabled,
                    nested_virt = excluded.nested_virt,
                    enable_logging = excluded.enable_logging,
                    enable_metrics = excluded.enable_metrics,
                    enable_console = excluded.enable_console,
                    ssh_keys = excluded.ssh_keys,
                    ssh_user = excluded.ssh_user,
                    log_path = excluded.log_path,
                    serial_output_path = excluded.serial_output_path,
                    lsm_flags = excluded.lsm_flags,
                    boot_args = excluded.boot_args,
                    volume_ids = excluded.volume_ids,
                    cpu_config = excluded.cpu_config,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    vm.id,
                    vm.name,
                    vm.status,
                    vm.pid,
                    vm.process_start_time,
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
                    vm.pci_enabled,
                    vm.nested_virt,
                    vm.enable_logging,
                    vm.enable_metrics,
                    vm.enable_console,
                    json.dumps(vm.ssh_keys)
                    if vm.ssh_keys is not None
                    else None,
                    vm.ssh_user,
                    vm.created_at,
                    vm.updated_at,
                    vm.log_path,
                    vm.serial_output_path,
                    vm.lsm_flags,
                    vm.boot_args,
                    json.dumps(vm.volume_ids)
                    if vm.volume_ids is not None
                    else None,
                    json.dumps(vm.cpu_config)
                    if vm.cpu_config is not None
                    else None,
                ),
            )

    def update_status(self, vm_id: str, status: str) -> None:
        """Update only the VM status field."""
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE vm_instances SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (status, vm_id),
            )

    def update_pid(self, vm_id: str, pid: int | None) -> None:
        """Update only the VM PID field."""
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE vm_instances SET pid = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (pid, vm_id),
            )

    def update_process_info(
        self, vm_id: str, pid: int | None, process_start_time: int | None
    ) -> None:
        """Update PID and process start time."""
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE vm_instances SET pid = ?, process_start_time = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (pid, process_start_time, vm_id),
            )

    def update_exit_code(self, vm_id: str, exit_code: int) -> None:
        """Update only the VM exit code field."""
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE vm_instances SET exit_code = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                (exit_code, vm_id),
            )

    def delete(self, vm_id: str) -> None:
        """Delete a VM by ID. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM vm_instances WHERE id = ?", (vm_id,))

    @_graceful_read(factory=list)
    def get_by_image_ids(self, image_ids: list[str]) -> list[VMInstanceItem]:
        """
        Return all VMs referencing any of the given image IDs.

        Args:
            image_ids: List of full image IDs to query.

        Returns:
            List of VMInstanceItem records.

        """
        if not image_ids:
            return []
        placeholders = ",".join("?" * len(image_ids))
        query = f"SELECT * FROM vm_instances WHERE image_id IN ({placeholders})"
        with self._db.connect() as conn:
            rows = conn.execute(query, image_ids).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]

    def delete_many(self, vm_ids: list[str]) -> int:
        """
        Delete multiple VMs by ID.

        Uses SQL-level DELETE WHERE id IN (...) for efficiency.

        Args:
            vm_ids: List of VM IDs to delete.

        Returns:
            Number of rows deleted.

        """
        if not vm_ids:
            return 0
        placeholders = ",".join(["?"] * len(vm_ids))
        query = f"DELETE FROM vm_instances WHERE id IN ({placeholders})"
        with self._db.connect() as conn:
            cursor = conn.execute(query, vm_ids)
            return cursor.rowcount
