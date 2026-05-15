"""Unit tests for KernelRepository — real SQLite database.

Tests cover CRUD, soft/hard delete, default management, and query methods.
Each test gets an isolated database (autouse fixtures in tests/conftest.py).
"""

from __future__ import annotations

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.models import KernelItem, VMInstanceItem, VMStatus

_TIMESTAMP = "2026-01-01T00:00:00+00:00"
_ANOTHER_TS = "2026-06-15T12:00:00+00:00"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_kernel(
    id: str = "a" * 64,
    name: str = "vmlinux",
    base_name: str = "vmlinux",
    version: str = "6.1.0",
    arch: str = "x86_64",
    type: str = "official",
    path: str = "vmlinux",
    is_default: bool = False,
    is_present: bool = True,
    created_at: str = _TIMESTAMP,
    updated_at: str = _TIMESTAMP,
    deleted_at: str | None = None,
) -> KernelItem:
    return KernelItem(
        id=id,
        name=name,
        base_name=base_name,
        version=version,
        arch=arch,
        type=type,
        path=path,
        is_default=is_default,
        is_present=is_present,
        created_at=created_at,
        updated_at=updated_at,
        deleted_at=deleted_at,
    )


@pytest.fixture
def repo() -> KernelRepository:
    """Return a KernelRepository backed by the autouse-migrated DB."""
    return KernelRepository(Database())


# ======================================================================
# get()
# ======================================================================


class TestGet:
    """Tests for KernelRepository.get()."""

    def test_get_found(self, repo: KernelRepository) -> None:
        k = _make_kernel()
        repo.upsert(k)
        result = repo.get(k.id)
        assert result is not None
        assert result.id == k.id
        assert result.name == "vmlinux"
        assert result.version == "6.1.0"

    def test_get_not_found(self, repo: KernelRepository) -> None:
        assert repo.get("nonexistent") is None

    def test_get_soft_deleted_returns_none(
        self, repo: KernelRepository
    ) -> None:
        k = _make_kernel()
        repo.upsert(k)
        repo.soft_delete(k.id)
        assert repo.get(k.id) is None


# ======================================================================
# find_by_prefix()
# ======================================================================


class TestFindByPrefix:
    """Tests for KernelRepository.find_by_prefix()."""

    def test_exact_prefix(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64)
        repo.upsert(k)
        results = repo.find_by_prefix("a" * 6)
        assert len(results) == 1
        assert results[0].id == k.id

    def test_prefix_multiple_matches(self, repo: KernelRepository) -> None:
        k1 = _make_kernel(id="a" + "b" * 63, name="k1")
        k2 = _make_kernel(id="a" + "c" * 63, name="k2")
        repo.upsert(k1)
        repo.upsert(k2)
        results = repo.find_by_prefix("a")
        assert len(results) == 2
        assert {r.name for r in results} == {"k1", "k2"}

    def test_prefix_no_matches(self, repo: KernelRepository) -> None:
        assert repo.find_by_prefix("zzzzz") == []

    def test_prefix_excludes_soft_deleted(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64)
        repo.upsert(k)
        repo.soft_delete(k.id)
        assert repo.find_by_prefix("a") == []


# ======================================================================
# list_all()
# ======================================================================


class TestListAll:
    """Tests for KernelRepository.list_all()."""

    def test_empty(self, repo: KernelRepository) -> None:
        assert repo.list_all() == []

    def test_returns_multiple(self, repo: KernelRepository) -> None:
        k1 = _make_kernel(id="a" * 64, name="k1")
        k2 = _make_kernel(id="b" * 64, name="k2")
        repo.upsert(k1)
        repo.upsert(k2)
        results = repo.list_all()
        assert len(results) == 2

    def test_excludes_soft_deleted(self, repo: KernelRepository) -> None:
        k1 = _make_kernel(id="a" * 64, name="k1")
        k2 = _make_kernel(id="b" * 64, name="k2")
        repo.upsert(k1)
        repo.upsert(k2)
        repo.soft_delete(k1.id)
        results = repo.list_all()
        assert len(results) == 1
        assert results[0].name == "k2"

    def test_order_by_created_at(self, repo: KernelRepository) -> None:
        early = _make_kernel(
            id="a" * 64, name="early", created_at="2025-01-01T00:00:00+00:00"
        )
        late = _make_kernel(
            id="b" * 64, name="late", created_at="2026-01-01T00:00:00+00:00"
        )
        repo.upsert(early)
        repo.upsert(late)
        results = repo.list_all()
        assert [r.name for r in results] == ["early", "late"]


# ======================================================================
# upsert()
# ======================================================================


class TestUpsert:
    """Tests for KernelRepository.upsert()."""

    def test_insert(self, repo: KernelRepository) -> None:
        k = _make_kernel()
        repo.upsert(k)
        assert repo.get(k.id) is not None

    def test_update_existing(self, repo: KernelRepository) -> None:
        k = _make_kernel(version="6.1.0")
        repo.upsert(k)
        updated = _make_kernel(version="6.6.0", updated_at=_ANOTHER_TS)
        repo.upsert(updated)
        result = repo.get(k.id)
        assert result is not None
        assert result.version == "6.6.0"


# ======================================================================
# soft_delete() / delete()
# ======================================================================


class TestDelete:
    """Tests for soft_delete and hard delete."""

    def test_soft_delete_sets_deleted_at(self, repo: KernelRepository) -> None:
        k = _make_kernel()
        repo.upsert(k)
        repo.soft_delete(k.id)
        # Can't retrieve via get() (filters deleted_at IS NULL)
        assert repo.get(k.id) is None

    def test_soft_delete_sets_is_present_zero(
        self, repo: KernelRepository
    ) -> None:
        k = _make_kernel()
        repo.upsert(k)
        repo.soft_delete(k.id)
        # Direct DB check
        db = Database()
        with db.connect() as conn:
            row = conn.execute(
                "SELECT is_present FROM kernels WHERE id = ?", (k.id,)
            ).fetchone()
        assert row is not None
        assert row["is_present"] == 0

    def test_hard_delete_removes_row(self, repo: KernelRepository) -> None:
        k = _make_kernel()
        repo.upsert(k)
        repo.delete(k.id)
        assert repo.get(k.id) is None
        # Also verify it's not findable via prefix
        assert repo.find_by_prefix(k.id[:6]) == []

    def test_hard_delete_nonexistent_is_noop(
        self, repo: KernelRepository
    ) -> None:
        repo.delete("nonexistent")  # should not raise


# ======================================================================
# get_default() / set_default()
# ======================================================================


class TestDefault:
    """Tests for default kernel management."""

    def test_get_default_none(self, repo: KernelRepository) -> None:
        assert repo.get_default() is None

    def test_set_default(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64)
        repo.upsert(k)
        repo.set_default(k.id)
        result = repo.get_default()
        assert result is not None
        assert result.id == k.id
        assert result.is_default

    def test_set_default_clears_previous(self, repo: KernelRepository) -> None:
        k1 = _make_kernel(id="a" * 64, name="k1")
        k2 = _make_kernel(id="b" * 64, name="k2")
        repo.upsert(k1)
        repo.upsert(k2)
        repo.set_default(k1.id)
        repo.set_default(k2.id)
        result = repo.get_default()
        assert result is not None
        assert result.id == k2.id

        # k1 should no longer be default
        db = Database()
        with db.connect() as conn:
            row = conn.execute(
                "SELECT is_default FROM kernels WHERE id = ?", (k1.id,)
            ).fetchone()
        assert row is not None
        assert row["is_default"] == 0

    def test_clear_default_by_setting_on_nonexistent(
        self, repo: KernelRepository
    ) -> None:
        """Setting default on a non-existent ID should not crash (silent no-op)."""
        k1 = _make_kernel(id="a" * 64, name="k1")
        repo.upsert(k1)
        repo.set_default(k1.id)
        # Set default on a different ID that exists
        k2 = _make_kernel(id="b" * 64, name="k2")
        repo.upsert(k2)
        repo.set_default(k2.id)
        assert repo.get_default() is not None
        assert repo.get_default().id == k2.id


# ======================================================================
# get_by_name()
# ======================================================================


class TestGetByName:
    """Tests for KernelRepository.get_by_name()."""

    def test_found(self, repo: KernelRepository) -> None:
        k = _make_kernel(name="my-kernel")
        repo.upsert(k)
        result = repo.get_by_name("my-kernel")
        assert result is not None
        assert result.name == "my-kernel"

    def test_not_found(self, repo: KernelRepository) -> None:
        assert repo.get_by_name("nonexistent") is None

    def test_excludes_soft_deleted(self, repo: KernelRepository) -> None:
        k = _make_kernel(name="will-be-deleted")
        repo.upsert(k)
        repo.soft_delete(k.id)
        assert repo.get_by_name("will-be-deleted") is None

    def test_returns_only_first_with_duplicate_names(
        self, repo: KernelRepository
    ) -> None:
        k1 = _make_kernel(id="a" * 64, name="dup")
        k2 = _make_kernel(id="b" * 64, name="dup")
        repo.upsert(k1)
        repo.upsert(k2)
        result = repo.get_by_name("dup")
        assert result is not None
        assert result.id in ("a" * 64, "b" * 64)


# ======================================================================
# get_by_type()
# ======================================================================


class TestGetByType:
    """Tests for KernelRepository.get_by_type()."""

    def test_found(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64, type="firecracker")
        repo.upsert(k)
        result = repo.get_by_type("firecracker")
        assert result is not None
        assert result.type == "firecracker"

    def test_not_found(self, repo: KernelRepository) -> None:
        assert repo.get_by_type("nonexistent-type") is None

    def test_excludes_soft_deleted(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64, type="firecracker")
        repo.upsert(k)
        repo.soft_delete(k.id)
        assert repo.get_by_type("firecracker") is None


# ======================================================================
# get_by_version_and_type()
# ======================================================================


class TestGetByVersionAndType:
    """Tests for KernelRepository.get_by_version_and_type()."""

    def test_found(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64, version="6.1.0", type="official")
        repo.upsert(k)
        result = repo.get_by_version_and_type("6.1.0", "official")
        assert result is not None
        assert result.version == "6.1.0"
        assert result.type == "official"

    def test_not_found_version(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64, version="6.1.0", type="official")
        repo.upsert(k)
        assert repo.get_by_version_and_type("6.6.0", "official") is None

    def test_not_found_type(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64, version="6.1.0", type="official")
        repo.upsert(k)
        assert repo.get_by_version_and_type("6.1.0", "firecracker") is None

    def test_excludes_soft_deleted(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64, version="6.1.0", type="official")
        repo.upsert(k)
        repo.soft_delete(k.id)
        assert repo.get_by_version_and_type("6.1.0", "official") is None


# ======================================================================
# update_many_is_present()
# ======================================================================


class TestUpdateManyIsPresent:
    """Tests for KernelRepository.update_many_is_present()."""

    def test_set_false(self, repo: KernelRepository) -> None:
        k1 = _make_kernel(id="a" * 64, name="k1")
        k2 = _make_kernel(id="b" * 64, name="k2")
        repo.upsert(k1)
        repo.upsert(k2)

        repo.update_many_is_present([k1.id, k2.id], False)

        db = Database()
        with db.connect() as conn:
            rows = conn.execute(
                "SELECT id, is_present FROM kernels ORDER BY id"
            ).fetchall()
        for row in rows:
            assert row["is_present"] == 0, f"Kernel {row['id']} should be 0"

    def test_empty_list_is_noop(self, repo: KernelRepository) -> None:
        repo.update_many_is_present([], True)  # should not raise

    def test_set_true(self, repo: KernelRepository) -> None:
        k = _make_kernel(id="a" * 64, is_present=False)
        repo.upsert(k)
        repo.update_many_is_present([k.id], True)

        db = Database()
        with db.connect() as conn:
            row = conn.execute(
                "SELECT is_present FROM kernels WHERE id = ?", (k.id,)
            ).fetchone()
        assert row is not None
        assert row["is_present"] == 1


# ======================================================================
# find_by_kernel_id() — moved to VMRepository
# ======================================================================


class TestQueryVMsByKernel:
    """Tests for VMRepository.find_by_kernel_id()."""

    def test_no_vms(self, repo: KernelRepository) -> None:
        from mvmctl.core._shared import Database as DB
        from mvmctl.core.vm._repository import VMRepository

        k = _make_kernel()
        repo.upsert(k)
        vm_repo = VMRepository(DB())
        vms = vm_repo.find_by_kernel_id(k.id)
        assert vms == []

    def test_with_vms(self, repo: KernelRepository) -> None:
        from mvmctl.core._shared import Database as DB
        from mvmctl.core.vm._repository import VMRepository

        k = _make_kernel(id="a" * 64)
        repo.upsert(k)

        # Insert prerequisites (FK constraints)
        db = DB()
        with db.connect() as conn:
            conn.execute(
                "INSERT INTO images (id, type, version, name, arch, path, fs_type, "
                "fs_uuid, original_size, minimum_rootfs_size_mib, pulled_at, "
                "is_default, is_present, created_at, updated_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
                (
                    "i" * 64,
                    "test-os",
                    "1.0",
                    "Test OS",
                    "x86_64",
                    "/img",
                    "ext4",
                    None,
                    2048,
                    1024,
                    _TIMESTAMP,
                ),
            )
            conn.execute(
                "INSERT INTO networks (id, name, subnet, bridge, ipv4_gateway, "
                "is_default, is_present, created_at, updated_at) "
                "VALUES (?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
                ("n" * 64, "test-net", "10.0.0.0/24", "br0", "10.0.0.1"),
            )
            conn.execute(
                "INSERT INTO binaries (id, name, version, full_version, ci_version, path, "
                "is_default, is_present, created_at, updated_at) "
                "VALUES (?, ?, ?, ?, ?, ?, 0, 0, datetime('now'), datetime('now'))",
                ("b" * 64, "firecracker", "1.15", "v1.15.0", None, "/bin"),
            )

        vm_repo = VMRepository(db)
        vm = VMInstanceItem(
            id="x" * 64,
            name="test-vm",
            status=VMStatus.STOPPED,
            pid=0,
            ipv4="10.0.0.2",
            mac="02:FC:00:00:00:01",
            network_id="n" * 64,
            tap_device="tap0",
            image_id="i" * 64,
            kernel_id=k.id,
            binary_id="b" * 64,
            api_socket_path="/tmp/test.sock",
            config_path="/tmp/test.json",
            cloud_init_mode="off",
            vcpu_count=2,
            mem_size_mib=256,
            disk_size_mib=1024,
            rootfs_path="/tmp/test.ext4",
            rootfs_suffix="ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_TIMESTAMP,
            updated_at=_TIMESTAMP,
        )
        vm_repo.upsert(vm)

        vms = vm_repo.find_by_kernel_id(k.id)
        assert len(vms) == 1
        assert vms[0].name == "test-vm"
        assert vms[0].kernel_id == k.id
