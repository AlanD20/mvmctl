"""
Tests for VMRepository — database-backed VM CRUD operations.

Verifies real SQLite query patterns: SQL-level filtering, COUNT,
INSERT/UPDATE/DELETE, prefix matching, and edge cases.
"""

from __future__ import annotations

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.vm._repository import VMRepository
from mvmctl.models import VMInstanceItem, VMStatus

# ---------------------------------------------------------------------------
# Helpers: seed FK tables + insert VM rows directly
# ---------------------------------------------------------------------------


def _seed_fk_tables(db: Database) -> None:
    """Insert minimal FK records required by vm_instances foreign keys."""
    with db.connect() as conn:
        conn.execute(
            "INSERT OR IGNORE INTO networks "
            "(id, name, subnet, bridge, ipv4_gateway, bridge_active, nat_enabled, is_default, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                "net-001",
                "test-net",
                "10.0.0.0/24",
                "mvm-br0",
                "10.0.0.1",
                1,
                1,
                0,
                "2026-01-01T00:00:00",
                "2026-01-01T00:00:00",
            ),
        )
        conn.execute(
            "INSERT OR IGNORE INTO images "
            "(id, type, name, arch, path, fs_type, minimum_rootfs_size_mib, original_size, pulled_at, is_default, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                "img-001",
                "test-ubuntu",
                "Test Ubuntu",
                "x86_64",
                "/tmp/test.img",
                "ext4",
                1024,
                2147483648,
                "2026-01-01T00:00:00",
                0,
                "2026-01-01T00:00:00",
                "2026-01-01T00:00:00",
            ),
        )
        conn.execute(
            "INSERT OR IGNORE INTO kernels "
            "(id, name, base_name, version, arch, type, path, is_default, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                "kern-001",
                "test-vmlinux",
                "vmlinux",
                "5.10",
                "x86_64",
                "firecracker",
                "/tmp/vmlinux",
                0,
                "2026-01-01T00:00:00",
                "2026-01-01T00:00:00",
            ),
        )
        conn.execute(
            "INSERT OR IGNORE INTO binaries "
            "(id, name, version, full_version, path, is_default, created_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
            (
                "bin-001",
                "firecracker",
                "1.15",
                "1.15.0",
                "/tmp/firecracker",
                0,
                "2026-01-01T00:00:00",
                "2026-01-01T00:00:00",
            ),
        )


def _normalize_value(v: object) -> int | str | None:
    """Convert Python types to SQLite-compatible values."""
    if isinstance(v, bool):
        return 1 if v else 0
    if isinstance(v, (int, str)):
        return v
    return None


def _insert_vm(
    db: Database, **overrides: object
) -> tuple[VMInstanceItem, dict[str, object]]:
    """Insert a VM record via raw SQL and return (VMInstanceItem, raw_data)."""
    data: dict[str, object] = {
        "id": "a" * 64,
        "name": "test-vm",
        "status": VMStatus.RUNNING.value,
        "pid": 12345,
        "process_start_time": None,
        "ipv4": "10.0.0.2",
        "mac": "02:FC:00:00:00:01",
        "network_id": "net-001",
        "tap_device": "tap-test",
        "image_id": "img-001",
        "kernel_id": "kern-001",
        "binary_id": "bin-001",
        "api_socket_path": "",
        "relay_socket_path": None,
        "config_path": "firecracker.json",
        "cloud_init_mode": "inject",
        "nocloud_net_port": None,
        "nocloud_net_pid": None,
        "relay_pid": None,
        "exit_code": None,
        "log_path": None,
        "serial_output_path": None,
        "vcpu_count": 2,
        "mem_size_mib": 512,
        "disk_size_mib": 2048,
        "rootfs_path": "rootfs.ext4",
        "rootfs_suffix": "ext4",
        "enable_pci": 0,
        "lsm_flags": None,
        "enable_logging": 1,
        "enable_metrics": 0,
        "enable_console": 1,
        "boot_args": None,
        "created_at": "2026-01-01T00:00:00",
        "updated_at": "2026-01-01T00:00:00",
    }
    data.update({k: _normalize_value(v) for k, v in overrides.items()})

    columns = ", ".join(data.keys())
    placeholders = ", ".join(["?"] * len(data))
    values = list(data.values())

    with db.connect() as conn:
        conn.execute(
            f"INSERT INTO vm_instances ({columns}) VALUES ({placeholders})",
            values,
        )

    # Build VMInstanceItem from the data dict
    vm_data = dict(data)
    # Convert int 0/1 back to bool for boolean fields
    for bool_field in (
        "enable_pci",
        "enable_logging",
        "enable_metrics",
        "enable_console",
    ):
        if vm_data.get(bool_field) is not None:
            vm_data[bool_field] = bool(vm_data[bool_field])

    return VMInstanceItem(**vm_data), data


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def db() -> Database:
    """Return a migrated test database."""
    d = Database()
    d.migrate()
    _seed_fk_tables(d)
    return d


@pytest.fixture
def repo(db: Database) -> VMRepository:
    """Return a VMRepository bound to the test database."""
    return VMRepository(db)


# ---------------------------------------------------------------------------
# Tests: get()
# ---------------------------------------------------------------------------


class TestGet:
    def test_returns_vm_for_valid_id(
        self, repo: VMRepository, db: Database
    ) -> None:
        vm, _ = _insert_vm(db)
        result = repo.get(vm.id)
        assert result is not None
        assert result.id == vm.id
        assert result.name == vm.name
        assert result.status == VMStatus.RUNNING.value

    def test_returns_none_for_invalid_id(self, repo: VMRepository) -> None:
        result = repo.get("nonexistent-id")
        assert result is None

    def test_returns_none_for_empty_id(self, repo: VMRepository) -> None:
        result = repo.get("")
        assert result is None


# ---------------------------------------------------------------------------
# Tests: get_by_name()
# ---------------------------------------------------------------------------


class TestGetByName:
    def test_returns_vm_for_valid_name(
        self, repo: VMRepository, db: Database
    ) -> None:
        vm, _ = _insert_vm(db, name="unique-vm-name")
        result = repo.get_by_name("unique-vm-name")
        assert result is not None
        assert result.name == "unique-vm-name"

    def test_returns_none_for_invalid_name(self, repo: VMRepository) -> None:
        result = repo.get_by_name("nonexistent")
        assert result is None

    def test_empty_name_returns_none(self, repo: VMRepository) -> None:
        result = repo.get_by_name("")
        assert result is None


# ---------------------------------------------------------------------------
# Tests: find_by_prefix()
# ---------------------------------------------------------------------------


class TestFindByPrefix:
    def test_finds_single_match(self, repo: VMRepository, db: Database) -> None:
        vm_id = "abc123" + "d" * 58
        _insert_vm(db, id=vm_id, name="prefix-vm")
        results = repo.find_by_prefix("abc123")
        assert len(results) == 1
        assert results[0].name == "prefix-vm"
        assert results[0].id == vm_id

    def test_finds_multiple_matches(
        self, repo: VMRepository, db: Database
    ) -> None:
        _insert_vm(
            db,
            id="abc123aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            name="vm1",
        )
        _insert_vm(
            db,
            id="abc123bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            name="vm2",
        )
        results = repo.find_by_prefix("abc123")
        assert len(results) == 2
        names = {r.name for r in results}
        assert names == {"vm1", "vm2"}

    def test_returns_empty_for_no_match(self, repo: VMRepository) -> None:
        results = repo.find_by_prefix("zzzzzz")
        assert results == []

    def test_empty_prefix_returns_all(
        self, repo: VMRepository, db: Database
    ) -> None:
        _insert_vm(db, id="x" * 64, name="vm1")
        _insert_vm(db, id="y" * 64, name="vm2")
        results = repo.find_by_prefix("")
        assert len(results) >= 2


# ---------------------------------------------------------------------------
# Tests: find_by_ip()
# ---------------------------------------------------------------------------


class TestFindByIP:
    def test_finds_by_exact_ip(self, repo: VMRepository, db: Database) -> None:
        _insert_vm(db, ipv4="10.0.0.42", name="ip-vm")
        result = repo.find_by_ip("10.0.0.42")
        assert result is not None
        assert result.name == "ip-vm"

    def test_returns_none_for_unused_ip(self, repo: VMRepository) -> None:
        result = repo.find_by_ip("10.0.0.99")
        assert result is None


# ---------------------------------------------------------------------------
# Tests: find_by_mac()
# ---------------------------------------------------------------------------


class TestFindByMAC:
    def test_finds_by_exact_mac(self, repo: VMRepository, db: Database) -> None:
        _insert_vm(db, name="mac-vm", mac="02:FC:AA:BB:CC:DD")
        result = repo.find_by_mac("02:FC:AA:BB:CC:DD")
        assert result is not None
        assert result.name == "mac-vm"

    def test_returns_none_for_unused_mac(self, repo: VMRepository) -> None:
        result = repo.find_by_mac("02:FC:FF:EE:DD:CC")
        assert result is None


# ---------------------------------------------------------------------------
# Tests: list_all()
# ---------------------------------------------------------------------------


class TestListAll:
    def test_returns_all_vms(self, repo: VMRepository, db: Database) -> None:
        _insert_vm(db, id="v" * 64, name="vm-a")
        _insert_vm(db, id="w" * 64, name="vm-b")
        _insert_vm(db, id="z" * 64, name="vm-c")
        results = repo.list_all()
        assert len(results) == 3

    def test_returns_empty_when_no_vms(self, repo: VMRepository) -> None:
        results = repo.list_all()
        assert results == []


# ---------------------------------------------------------------------------
# Tests: list_by_status()
# ---------------------------------------------------------------------------


class TestListByStatus:
    def test_filters_by_single_status(
        self, repo: VMRepository, db: Database
    ) -> None:
        _insert_vm(db, id="r" * 64, name="running-vm")
        _insert_vm(
            db,
            id="s" * 64,
            name="stopped-vm",
            status=VMStatus.STOPPED.value,
            pid=0,
        )
        results = repo.list_by_status(VMStatus.RUNNING)
        assert len(results) == 1
        assert results[0].name == "running-vm"

    def test_filters_by_multiple_statuses(
        self, repo: VMRepository, db: Database
    ) -> None:
        _insert_vm(db, id="r" * 64, name="running-vm")
        _insert_vm(
            db,
            id="s" * 64,
            name="stopped-vm",
            status=VMStatus.STOPPED.value,
            pid=0,
        )
        _insert_vm(
            db,
            id="p" * 64,
            name="paused-vm",
            status=VMStatus.PAUSED.value,
            pid=0,
        )
        results = repo.list_by_status([VMStatus.RUNNING, VMStatus.PAUSED])
        assert len(results) == 2
        names = {r.name for r in results}
        assert names == {"running-vm", "paused-vm"}

    def test_empty_status_list_returns_all(
        self, repo: VMRepository, db: Database
    ) -> None:
        _insert_vm(db, id="r" * 64, name="vm-one")
        results = repo.list_by_status([])
        assert len(results) >= 1


# ---------------------------------------------------------------------------
# Tests: count()
# ---------------------------------------------------------------------------


class TestCount:
    def test_returns_correct_count(
        self, repo: VMRepository, db: Database
    ) -> None:
        assert repo.count() == 0
        _insert_vm(db, id="a" * 64, name="vm-1")
        assert repo.count() == 1
        _insert_vm(db, id="b" * 64, name="vm-2")
        assert repo.count() == 2

    def test_zero_when_empty(self, repo: VMRepository) -> None:
        assert repo.count() == 0


# ---------------------------------------------------------------------------
# Tests: count_by_status()
# ---------------------------------------------------------------------------


class TestCountByStatus:
    def test_counts_single_status(
        self, repo: VMRepository, db: Database
    ) -> None:
        _insert_vm(db, id="r" * 64, name="vm-r")
        _insert_vm(
            db, id="s" * 64, name="vm-s", status=VMStatus.STOPPED.value, pid=0
        )
        assert repo.count_by_status(VMStatus.RUNNING) == 1
        assert repo.count_by_status(VMStatus.STOPPED) == 1

    def test_counts_multiple_statuses(
        self, repo: VMRepository, db: Database
    ) -> None:
        _insert_vm(db, id="r" * 64, name="vm-r")
        _insert_vm(
            db, id="p" * 64, name="vm-p", status=VMStatus.PAUSED.value, pid=0
        )
        _insert_vm(
            db,
            id="s" * 64,
            name="vm-s",
            status=VMStatus.STOPPED.value,
            pid=0,
        )
        assert repo.count_by_status([VMStatus.RUNNING, VMStatus.PAUSED]) == 2

    def test_empty_list_returns_total(
        self, repo: VMRepository, db: Database
    ) -> None:
        _insert_vm(db, id="r" * 64, name="vm")
        assert repo.count_by_status([]) == 1


# ---------------------------------------------------------------------------
# Tests: upsert()
# ---------------------------------------------------------------------------


class TestUpsert:
    def test_inserts_new_vm(self, repo: VMRepository, db: Database) -> None:
        vm_data: dict[str, object] = {
            "id": "n" * 64,
            "name": "new-vm",
            "status": VMStatus.STOPPED.value,
            "pid": 0,
            "ipv4": "10.0.0.10",
            "mac": "02:FC:00:00:00:0A",
            "network_id": "net-001",
            "tap_device": "tap-new",
            "image_id": "img-001",
            "kernel_id": "kern-001",
            "binary_id": "bin-001",
            "api_socket_path": "",
            "config_path": "firecracker.json",
            "cloud_init_mode": "inject",
            "vcpu_count": 1,
            "mem_size_mib": 256,
            "disk_size_mib": 1024,
            "rootfs_path": "rootfs.ext4",
            "rootfs_suffix": "ext4",
            "enable_pci": False,
            "enable_logging": True,
            "enable_metrics": False,
            "enable_console": False,
            "created_at": "2026-06-01T00:00:00",
            "updated_at": "2026-06-01T00:00:00",
        }
        vm = VMInstanceItem(**vm_data)
        repo.upsert(vm)

        result = repo.get("n" * 64)
        assert result is not None
        assert result.name == "new-vm"
        assert result.status == VMStatus.STOPPED.value

    def test_updates_existing_vm(
        self, repo: VMRepository, db: Database
    ) -> None:
        # Insert via raw SQL
        _insert_vm(db, id="u" * 64, name="update-vm")
        # Upsert with new status
        updated = repo.get("u" * 64)
        assert updated is not None
        updated.status = VMStatus.PAUSED.value
        repo.upsert(updated)

        result = repo.get("u" * 64)
        assert result is not None
        assert result.status == VMStatus.PAUSED.value


# ---------------------------------------------------------------------------
# Tests: update_status()
# ---------------------------------------------------------------------------


class TestUpdateStatus:
    def test_updates_status(self, repo: VMRepository, db: Database) -> None:
        vm, _ = _insert_vm(db)
        repo.update_status(vm.id, VMStatus.STOPPED.value)
        result = repo.get(vm.id)
        assert result is not None
        assert result.status == VMStatus.STOPPED.value

    def test_noop_for_nonexistent_id(self, repo: VMRepository) -> None:
        # Should not raise
        repo.update_status("nonexistent", VMStatus.STOPPED.value)


# ---------------------------------------------------------------------------
# Tests: delete()
# ---------------------------------------------------------------------------


class TestDelete:
    def test_deletes_vm(self, repo: VMRepository, db: Database) -> None:
        vm, _ = _insert_vm(db)
        assert repo.get(vm.id) is not None
        repo.delete(vm.id)
        assert repo.get(vm.id) is None

    def test_noop_for_nonexistent_id(self, repo: VMRepository) -> None:
        repo.delete("nonexistent")


# ---------------------------------------------------------------------------
# Tests: update_pid / update_exit_code
# ---------------------------------------------------------------------------


class TestUpdatePid:
    def test_updates_pid(self, repo: VMRepository, db: Database) -> None:
        vm, _ = _insert_vm(db)
        repo.update_pid(vm.id, 99999)
        result = repo.get(vm.id)
        assert result is not None
        assert result.pid == 99999

    def test_sets_pid_to_zero(self, repo: VMRepository, db: Database) -> None:
        vm, _ = _insert_vm(db)
        repo.update_pid(vm.id, 0)
        result = repo.get(vm.id)
        assert result is not None
        assert result.pid == 0


class TestExitCode:
    def test_updates_exit_code(self, repo: VMRepository, db: Database) -> None:
        vm, _ = _insert_vm(db)
        repo.update_exit_code(vm.id, 0)
        result = repo.get(vm.id)
        assert result is not None
        assert result.exit_code == 0

    def test_updates_exit_code_nonzero(
        self, repo: VMRepository, db: Database
    ) -> None:
        vm, _ = _insert_vm(db)
        repo.update_exit_code(vm.id, 137)
        result = repo.get(vm.id)
        assert result is not None
        assert result.exit_code == 137
