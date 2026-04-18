"""VM database operations - Repository Pattern implementation."""

from __future__ import annotations

from mvmctl.core._internal._db import Database
from mvmctl.models import VMStatus
from mvmctl.models.vm import VMInstance


class VMRepository:
    """Database operations for VM instances."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    def get(self, vm_id: str) -> VMInstance | None:
        """Return a VM by its full 64-char ID, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM vm_instances WHERE id = ?", (vm_id,)
            ).fetchone()
        if row is None:
            return None
        return VMInstance(**dict(row))

    def get_by_name(self, name: str) -> VMInstance | None:
        """Return a VM by name, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM vm_instances WHERE name = ?", (name,)
            ).fetchone()
        if row is None:
            return None
        return VMInstance(**dict(row))

    def find_by_ip(self, ipv4: str) -> VMInstance | None:
        """Return a VM by IP address, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM vm_instances WHERE ipv4 = ?", (ipv4,)
            ).fetchone()
        if row is None:
            return None
        return VMInstance(**dict(row))

    def find_by_mac(self, mac: str) -> VMInstance | None:
        """Return a VM by MAC address, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM vm_instances WHERE mac = ?", (mac,)
            ).fetchone()
        if row is None:
            return None
        return VMInstance(**dict(row))

    def find_by_prefix(self, prefix: str) -> list[VMInstance]:
        """Return all VMs whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE id LIKE ?",
                (f"{prefix}%",),
            ).fetchall()
        return [VMInstance(**dict(row)) for row in rows]

    def count(self) -> int:
        """Return total count of all VMs."""
        with self._db.connect() as conn:
            result = conn.execute(
                "SELECT COUNT(*) FROM vm_instances"
            ).fetchone()
        return result[0] if result else 0

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

    def list_all(self) -> list[VMInstance]:
        """Return all VM records."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances ORDER BY created_at"
            ).fetchall()
        return [VMInstance(**dict(row)) for row in rows]

    def list_by_status(
        self, status: VMStatus | list[VMStatus]
    ) -> list[VMInstance]:
        """Return VM records filtered by status(es). Accepts single status or list of statuses."""
        statuses = [status] if isinstance(status, VMStatus) else status
        if not statuses:
            return self.list_all()

        status_values = [s.value for s in statuses]
        placeholders = ",".join(["?"] * len(status_values))
        query = f"SELECT * FROM vm_instances WHERE status IN ({placeholders}) ORDER BY created_at"

        with self._db.connect() as conn:
            rows = conn.execute(query, status_values).fetchall()
        return [VMInstance(**dict(row)) for row in rows]

    def list_excluding_statuses(
        self, excluded_statuses: VMStatus | list[VMStatus]
    ) -> list[VMInstance]:
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
        return [VMInstance(**dict(row)) for row in rows]

    def upsert(self, vm: VMInstance) -> None:
        """Insert or replace a VM record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO vm_instances (
                    id, name, status, pid, ipv4, mac, network_id, tap_device,
                    image_id, kernel_id, binary_id, api_socket_path,
                    relay_socket_path, config_path, cloud_init_mode,
                    nocloud_net_port, nocloud_net_pid, relay_pid,
                    exit_code, vcpu_count, mem_size_mib, disk_size_mib,
                    rootfs_path, rootfs_suffix, enable_pci, enable_logging,
                    enable_metrics, enable_console, created_at, updated_at,
                    log_path, serial_output_path, lsm_flags, boot_args
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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

    def delete(self, vm_id: str) -> None:
        """Delete a VM by ID. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM vm_instances WHERE id = ?", (vm_id,))

    def delete_many(self, vm_ids: list[str]) -> int:
        """Delete multiple VMs by ID.

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
