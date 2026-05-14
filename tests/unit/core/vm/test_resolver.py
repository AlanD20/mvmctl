"""
Tests for VMResolver — entity resolution by name, IP, MAC, or ID prefix.

Tests cover: single-name resolution, prefix matching, ambiguous prefix
handling, nonexistent VM errors, and RELATIONS dict structure.
"""

from __future__ import annotations

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._resolver import VMResolver
from mvmctl.exceptions import VMNotFoundError
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


def _insert_vm(db: Database, **overrides: object) -> VMInstanceItem:
    """Insert a VM record via raw SQL and return a VMInstanceItem."""
    data: dict[str, object] = {
        "id": "a" * 64,
        "name": "test-vm",
        "status": VMStatus.RUNNING.value,
        "pid": 12345,
        "ipv4": "10.0.0.2",
        "mac": "02:FC:00:00:00:01",
        "network_id": "net-001",
        "tap_device": "tap-test",
        "image_id": "img-001",
        "kernel_id": "kern-001",
        "binary_id": "bin-001",
        "api_socket_path": "",
        "config_path": "firecracker.json",
        "cloud_init_mode": "inject",
        "vcpu_count": 2,
        "mem_size_mib": 512,
        "disk_size_mib": 2048,
        "rootfs_path": "rootfs.ext4",
        "rootfs_suffix": "ext4",
        "enable_pci": 0,
        "enable_logging": 1,
        "enable_metrics": 0,
        "enable_console": 1,
        "created_at": "2026-01-01T00:00:00",
        "updated_at": "2026-01-01T00:00:00",
    }
    data.update(overrides)
    # Convert bools to ints for SQLite
    for bool_field in (
        "enable_pci",
        "enable_logging",
        "enable_metrics",
        "enable_console",
    ):
        if isinstance(data.get(bool_field), bool):
            data[bool_field] = 1 if data[bool_field] else 0

    columns = ", ".join(data.keys())
    placeholders = ", ".join(["?"] * len(data))
    values = list(data.values())

    with db.connect() as conn:
        conn.execute(
            f"INSERT INTO vm_instances ({columns}) VALUES ({placeholders})",
            values,
        )

    return _row_to_vm(data)


def _row_to_vm(data: dict[str, object]) -> VMInstanceItem:
    """Convert raw data dict to VMInstanceItem, handling bool fields."""
    vm_data = dict(data)
    for bool_field in (
        "enable_pci",
        "enable_logging",
        "enable_metrics",
        "enable_console",
    ):
        if vm_data.get(bool_field) is not None:
            vm_data[bool_field] = bool(vm_data[bool_field])
    return VMInstanceItem(**vm_data)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def db() -> Database:
    """Return a migrated test database with FK tables."""
    d = Database()
    d.migrate()
    _seed_fk_tables(d)
    return d


@pytest.fixture
def repo(db: Database) -> VMRepository:
    """Return a VMRepository bound to the test database."""
    return VMRepository(db)


@pytest.fixture
def resolver(repo: VMRepository) -> VMResolver:
    """Return a VMResolver bound to the test repo."""
    return VMResolver(repo)


# ---------------------------------------------------------------------------
# Tests: resolve() by name
# ---------------------------------------------------------------------------


class TestResolveByName:
    def test_resolves_by_exact_name(
        self, resolver: VMResolver, db: Database
    ) -> None:
        _insert_vm(db, id="a" * 64, name="my-vm")
        vm = resolver.resolve("my-vm")
        assert vm.name == "my-vm"

    def test_resolve_with_dotted_name_not_confused_with_ip(
        self, resolver: VMResolver, db: Database
    ) -> None:
        # A name containing a dot should still resolve by name first
        _insert_vm(db, id="b" * 64, name="my.vm")
        vm = resolver.resolve("my.vm")
        assert vm.name == "my.vm"

    def test_resolve_with_colon_in_name(
        self, resolver: VMResolver, db: Database
    ) -> None:
        # A colon-containing name should still resolve by name first
        _insert_vm(db, id="c" * 64, name="my:vm")
        vm = resolver.resolve("my:vm")
        assert vm.name == "my:vm"


# ---------------------------------------------------------------------------
# Tests: resolve() by IP
# ---------------------------------------------------------------------------


class TestResolveByIP:
    def test_resolves_by_ip(self, resolver: VMResolver, db: Database) -> None:
        _insert_vm(db, id="d" * 64, name="ip-vm", ipv4="10.0.0.50")
        vm = resolver.resolve("10.0.0.50")
        assert vm.name == "ip-vm"

    def test_ip_not_found_raises(self, resolver: VMResolver) -> None:
        with pytest.raises(VMNotFoundError, match="No VM found with IP"):
            resolver.resolve("10.0.0.99")


# ---------------------------------------------------------------------------
# Tests: resolve() by MAC
# ---------------------------------------------------------------------------


class TestResolveByMAC:
    def test_resolves_by_mac(self, resolver: VMResolver, db: Database) -> None:
        _insert_vm(db, id="e" * 64, name="mac-vm", mac="02:FC:AA:BB:CC:DD")
        vm = resolver.resolve("02:FC:AA:BB:CC:DD")
        assert vm.name == "mac-vm"

    def test_mac_not_found_raises(self, resolver: VMResolver) -> None:
        with pytest.raises(VMNotFoundError, match="No VM found with MAC"):
            resolver.resolve("02:FC:FF:EE:DD:CC")


# ---------------------------------------------------------------------------
# Tests: resolve() by ID prefix
# ---------------------------------------------------------------------------


class TestResolveByID:
    def test_resolves_by_unique_prefix(
        self, resolver: VMResolver, db: Database
    ) -> None:
        vm_id = "abc123" + "d" * 58
        _insert_vm(db, id=vm_id, name="prefix-vm")
        vm = resolver.resolve("abc123")
        assert vm.name == "prefix-vm"

    def test_ambiguous_prefix_raises(
        self, resolver: VMResolver, db: Database
    ) -> None:
        _insert_vm(
            db,
            id="abc123aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            name="vm-1",
        )
        _insert_vm(
            db,
            id="abc123bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            name="vm-2",
        )
        with pytest.raises(VMNotFoundError, match="matches multiple"):
            resolver.resolve("abc123")

    def test_nonexistent_prefix_raises(self, resolver: VMResolver) -> None:
        with pytest.raises(VMNotFoundError, match="VM not found"):
            resolver.resolve("zzzzzz")


# ---------------------------------------------------------------------------
# Tests: resolve() nonexistent name
# ---------------------------------------------------------------------------


class TestResolveNonexistent:
    def test_raises_vm_not_found_for_unknown_name(
        self, resolver: VMResolver
    ) -> None:
        with pytest.raises(VMNotFoundError, match="VM not found"):
            resolver.resolve("completely-unknown-vm")


# ---------------------------------------------------------------------------
# Tests: by_name() / by_id() / by_ip() / by_mac() direct methods
# ---------------------------------------------------------------------------


class TestByName:
    def test_finds_exact_name(self, resolver: VMResolver, db: Database) -> None:
        _insert_vm(db, id="f" * 64, name="exact-name")
        vm = resolver.by_name("exact-name")
        assert vm.name == "exact-name"

    def test_not_found_raises(self, resolver: VMResolver) -> None:
        with pytest.raises(VMNotFoundError, match="VM not found"):
            resolver.by_name("no-such-vm")


class TestByID:
    def test_finds_by_prefix(self, resolver: VMResolver, db: Database) -> None:
        vm_id = "xyz789" + "d" * 57
        _insert_vm(db, id=vm_id, name="id-vm")
        vm = resolver.by_id("xyz789")
        assert vm.name == "id-vm"

    def test_not_found_raises(self, resolver: VMResolver) -> None:
        with pytest.raises(VMNotFoundError, match="VM not found"):
            resolver.by_id("nonexistent")

    def test_ambiguous_raises(self, resolver: VMResolver, db: Database) -> None:
        _insert_vm(
            db,
            id="prefix1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            name="vm-a",
        )
        _insert_vm(
            db,
            id="prefix1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            name="vm-b",
        )
        with pytest.raises(VMNotFoundError, match="matches multiple"):
            resolver.by_id("prefix1")


class TestByIP:
    def test_finds_by_exact_ip(
        self, resolver: VMResolver, db: Database
    ) -> None:
        _insert_vm(db, id="g" * 64, name="ip-vm", ipv4="192.168.1.100")
        vm = resolver.by_ip("192.168.1.100")
        assert vm.name == "ip-vm"

    def test_not_found_raises(self, resolver: VMResolver) -> None:
        with pytest.raises(VMNotFoundError, match="No VM found with IP"):
            resolver.by_ip("10.0.0.99")


class TestByMAC:
    def test_finds_by_exact_mac(
        self, resolver: VMResolver, db: Database
    ) -> None:
        _insert_vm(db, id="h" * 64, name="mac-vm", mac="AA:BB:CC:DD:EE:FF")
        vm = resolver.by_mac("AA:BB:CC:DD:EE:FF")
        assert vm.name == "mac-vm"

    def test_not_found_raises(self, resolver: VMResolver) -> None:
        with pytest.raises(VMNotFoundError, match="No VM found with MAC"):
            resolver.by_mac("FF:EE:DD:CC:BB:AA")


# ---------------------------------------------------------------------------
# Tests: RELATIONS dict
# ---------------------------------------------------------------------------


class TestRelations:
    def test_relations_dict_has_expected_structure(self) -> None:
        relations = VMResolver.RELATIONS

        assert "kernel" in relations
        assert relations["kernel"].fk_field == "kernel_id"
        assert relations["kernel"].resolver == "kernel"

        assert "image" in relations
        assert relations["image"].fk_field == "image_id"
        assert relations["image"].resolver == "image"

        assert "binary" in relations
        assert relations["binary"].fk_field == "binary_id"

        assert "network" in relations
        assert relations["network"].fk_field == "network_id"

        assert "network.leases" in relations
        assert relations["network.leases"].resolver == "network_lease"
        assert relations["network.leases"].method == "list_by_network_id"
        assert relations["network.leases"].relation_name == "leases"
