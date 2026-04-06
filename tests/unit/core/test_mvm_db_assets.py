"""Tests for MVMDatabase asset operations (images, kernels, binaries).

Comprehensive test suite covering:
- Image CRUD operations and default management
- Kernel CRUD operations and default management
- Binary CRUD operations and default management
- Database migrations
- Edge cases and atomicity constraints
"""

from __future__ import annotations

from pathlib import Path

import pytest

from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Binary, Image, Kernel


@pytest.fixture
def db(tmp_path: Path) -> MVMDatabase:
    """Create a database with migrations applied.

    Args:
        tmp_path: Pytest temporary directory fixture.

    Returns:
        MVMDatabase instance with schema initialized.
    """
    db_instance = MVMDatabase(db_path=tmp_path / "test.db")
    db_instance.migrate()
    return db_instance


class TestImageOperations:
    """Tests for image CRUD operations."""

    def test_get_image_found(self, db: MVMDatabase) -> None:
        """Test retrieving an existing image by full ID."""
        image = Image(
            id="a" * 64,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            os_name="Ubuntu",
            fs_type="ext4",
        )
        db.upsert_image(image)

        retrieved = db.get_image("a" * 64)
        assert retrieved is not None
        assert retrieved.id == "a" * 64
        assert retrieved.os_slug == "ubuntu-24.04"
        assert retrieved.os_name == "Ubuntu"

    def test_get_image_not_found(self, db: MVMDatabase) -> None:
        """Test that get_image returns None for non-existent ID."""
        result = db.get_image("nonexistent" + "a" * 53)
        assert result is None

    def test_upsert_image_insert(self, db: MVMDatabase) -> None:
        """Test inserting a new image record."""
        image = Image(
            id="b" * 64,
            os_slug="alpine-3.21",
            path="/cache/images/alpine-3.21.ext4",
            arch="x86_64",
        )
        db.upsert_image(image)

        retrieved = db.get_image("b" * 64)
        assert retrieved is not None
        assert retrieved.os_slug == "alpine-3.21"

    def test_upsert_image_update(self, db: MVMDatabase) -> None:
        """Test updating an existing image (ON CONFLICT)."""
        image1 = Image(
            id="c" * 64,
            os_slug="debian-12",
            path="/cache/images/debian-12.ext4",
            arch="x86_64",
            os_name="Debian",
        )
        db.upsert_image(image1)

        image2 = Image(
            id="c" * 64,
            os_slug="debian-12",
            path="/cache/images/debian-12.ext4",
            arch="x86_64",
            os_name="Debian 12",  # Updated
            fs_type="ext4",  # New field
        )
        db.upsert_image(image2)

        retrieved = db.get_image("c" * 64)
        assert retrieved is not None
        assert retrieved.os_name == "Debian 12"
        assert retrieved.fs_type == "ext4"

    def test_delete_image_found(self, db: MVMDatabase) -> None:
        """Test deleting an existing image."""
        image = Image(
            id="d" * 64,
            os_slug="fedora-40",
            path="/cache/images/fedora-40.ext4",
            arch="x86_64",
        )
        db.upsert_image(image)
        assert db.get_image("d" * 64) is not None

        db.delete_image("d" * 64)
        assert db.get_image("d" * 64) is None

    def test_delete_image_not_found(self, db: MVMDatabase) -> None:
        """Test that deleting non-existent image is a no-op."""
        # Should not raise an exception
        db.delete_image("nonexistent" + "a" * 53)
        assert db.get_image("nonexistent" + "a" * 53) is None

    def test_list_images_empty(self, db: MVMDatabase) -> None:
        """Test listing images when none exist."""
        images = db.list_images()
        assert images == []

    def test_list_images_multiple(self, db: MVMDatabase) -> None:
        """Test listing multiple images ordered by created_at."""
        image1 = Image(
            id="e" * 64,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            created_at="2026-04-01T10:00:00Z",
        )
        image2 = Image(
            id="f" * 64,
            os_slug="alpine-3.21",
            path="/cache/images/alpine-3.21.ext4",
            arch="x86_64",
            created_at="2026-04-02T10:00:00Z",
        )
        db.upsert_image(image1)
        db.upsert_image(image2)

        images = db.list_images()
        assert len(images) == 2
        assert images[0].id == "e" * 64
        assert images[1].id == "f" * 64

    def test_find_images_by_prefix_exact(self, db: MVMDatabase) -> None:
        """Test finding image by exact prefix match."""
        image = Image(
            id="abc123" + "d" * 58,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
        )
        db.upsert_image(image)

        results = db.find_images_by_prefix("abc123")
        assert len(results) == 1
        assert results[0].id == "abc123" + "d" * 58

    def test_find_images_by_prefix_multiple(self, db: MVMDatabase) -> None:
        """Test finding multiple images with same prefix."""
        image1 = Image(
            id="abc000" + "d" * 58,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
        )
        image2 = Image(
            id="abc111" + "d" * 58,
            os_slug="alpine-3.21",
            path="/cache/images/alpine-3.21.ext4",
            arch="x86_64",
        )
        db.upsert_image(image1)
        db.upsert_image(image2)

        results = db.find_images_by_prefix("abc")
        assert len(results) == 2

    def test_find_images_by_prefix_no_match(self, db: MVMDatabase) -> None:
        """Test finding images with non-matching prefix."""
        image = Image(
            id="xyz" + "a" * 61,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
        )
        db.upsert_image(image)

        results = db.find_images_by_prefix("abc")
        assert results == []

    def test_set_default_image_single(self, db: MVMDatabase) -> None:
        """Test setting one image as default."""
        image = Image(
            id="g" * 64,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            is_default=False,
        )
        db.upsert_image(image)

        db.set_default_image("g" * 64)

        retrieved = db.get_image("g" * 64)
        assert retrieved is not None
        # SQLite returns 0/1 for booleans, so check truthiness
        assert bool(retrieved.is_default) is True

    def test_set_default_image_clears_others(self, db: MVMDatabase) -> None:
        """Test that set_default_image ensures only one is_default=True."""
        image1 = Image(
            id="h" * 64,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            is_default=True,
        )
        image2 = Image(
            id="i" * 64,
            os_slug="alpine-3.21",
            path="/cache/images/alpine-3.21.ext4",
            arch="x86_64",
            is_default=False,
        )
        db.upsert_image(image1)
        db.upsert_image(image2)

        db.set_default_image("i" * 64)

        img1 = db.get_image("h" * 64)
        img2 = db.get_image("i" * 64)
        assert img1 is not None
        assert img2 is not None
        assert bool(img1.is_default) is False
        assert bool(img2.is_default) is True

    def test_set_default_image_idempotent(self, db: MVMDatabase) -> None:
        """Test that calling set_default_image twice is safe."""
        image = Image(
            id="j" * 64,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
        )
        db.upsert_image(image)

        db.set_default_image("j" * 64)
        db.set_default_image("j" * 64)

        retrieved = db.get_image("j" * 64)
        assert retrieved is not None
        assert bool(retrieved.is_default) is True


class TestKernelOperations:
    """Tests for kernel CRUD operations."""

    def test_get_kernel_found(self, db: MVMDatabase) -> None:
        """Test retrieving an existing kernel by full ID."""
        kernel = Kernel(
            id="k" * 64,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
        )
        db.upsert_kernel(kernel)

        retrieved = db.get_kernel("k" * 64)
        assert retrieved is not None
        assert retrieved.id == "k" * 64
        assert retrieved.name == "vmlinux"
        assert retrieved.version == "5.10.0"

    def test_get_kernel_not_found(self, db: MVMDatabase) -> None:
        """Test that get_kernel returns None for non-existent ID."""
        result = db.get_kernel("nonexistent" + "a" * 53)
        assert result is None

    def test_upsert_kernel_insert(self, db: MVMDatabase) -> None:
        """Test inserting a new kernel record."""
        kernel = Kernel(
            id="l" * 64,
            name="vmlinux",
            version="6.1.0",
            arch="aarch64",
            path="/cache/kernels/vmlinux-6.1.0",
        )
        db.upsert_kernel(kernel)

        retrieved = db.get_kernel("l" * 64)
        assert retrieved is not None
        assert retrieved.version == "6.1.0"
        assert retrieved.arch == "aarch64"

    def test_upsert_kernel_update(self, db: MVMDatabase) -> None:
        """Test updating an existing kernel (ON CONFLICT)."""
        kernel1 = Kernel(
            id="m" * 64,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
            base_name="vmlinux-base",
        )
        db.upsert_kernel(kernel1)

        kernel2 = Kernel(
            id="m" * 64,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
            base_name="vmlinux-base-updated",
            type="linux",
        )
        db.upsert_kernel(kernel2)

        retrieved = db.get_kernel("m" * 64)
        assert retrieved is not None
        assert retrieved.base_name == "vmlinux-base-updated"
        assert retrieved.type == "linux"

    def test_delete_kernel_found(self, db: MVMDatabase) -> None:
        """Test deleting an existing kernel."""
        kernel = Kernel(
            id="n" * 64,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
        )
        db.upsert_kernel(kernel)
        assert db.get_kernel("n" * 64) is not None

        db.delete_kernel("n" * 64)
        assert db.get_kernel("n" * 64) is None

    def test_delete_kernel_not_found(self, db: MVMDatabase) -> None:
        """Test that deleting non-existent kernel is a no-op."""
        db.delete_kernel("nonexistent" + "a" * 53)
        assert db.get_kernel("nonexistent" + "a" * 53) is None

    def test_list_kernels_empty(self, db: MVMDatabase) -> None:
        """Test listing kernels when none exist."""
        kernels = db.list_kernels()
        assert kernels == []

    def test_list_kernels_multiple(self, db: MVMDatabase) -> None:
        """Test listing multiple kernels ordered by created_at."""
        kernel1 = Kernel(
            id="o" * 64,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
            created_at="2026-04-01T10:00:00Z",
        )
        kernel2 = Kernel(
            id="p" * 64,
            name="vmlinux",
            version="6.1.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-6.1.0",
            created_at="2026-04-02T10:00:00Z",
        )
        db.upsert_kernel(kernel1)
        db.upsert_kernel(kernel2)

        kernels = db.list_kernels()
        assert len(kernels) == 2
        assert kernels[0].id == "o" * 64
        assert kernels[1].id == "p" * 64

    def test_find_kernels_by_prefix_exact(self, db: MVMDatabase) -> None:
        """Test finding kernel by exact prefix match."""
        kernel = Kernel(
            id="def456" + "d" * 58,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
        )
        db.upsert_kernel(kernel)

        results = db.find_kernels_by_prefix("def456")
        assert len(results) == 1
        assert results[0].id == "def456" + "d" * 58

    def test_find_kernels_by_prefix_multiple(self, db: MVMDatabase) -> None:
        """Test finding multiple kernels with same prefix."""
        kernel1 = Kernel(
            id="def000" + "d" * 58,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
        )
        kernel2 = Kernel(
            id="def111" + "d" * 58,
            name="vmlinux",
            version="6.1.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-6.1.0",
        )
        db.upsert_kernel(kernel1)
        db.upsert_kernel(kernel2)

        results = db.find_kernels_by_prefix("def")
        assert len(results) == 2

    def test_find_kernels_by_prefix_no_match(self, db: MVMDatabase) -> None:
        """Test finding kernels with non-matching prefix."""
        kernel = Kernel(
            id="xyz" + "a" * 61,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
        )
        db.upsert_kernel(kernel)

        results = db.find_kernels_by_prefix("def")
        assert results == []

    def test_set_default_kernel_single(self, db: MVMDatabase) -> None:
        """Test setting one kernel as default."""
        kernel = Kernel(
            id="q" * 64,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
            is_default=False,
        )
        db.upsert_kernel(kernel)

        db.set_default_kernel("q" * 64)

        retrieved = db.get_kernel("q" * 64)
        assert retrieved is not None
        # SQLite returns 0/1 for booleans, so check truthiness
        assert bool(retrieved.is_default) is True

    def test_set_default_kernel_clears_others(self, db: MVMDatabase) -> None:
        """Test that set_default_kernel ensures only one is_default=True."""
        kernel1 = Kernel(
            id="r" * 64,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
            is_default=True,
        )
        kernel2 = Kernel(
            id="s" * 64,
            name="vmlinux",
            version="6.1.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-6.1.0",
            is_default=False,
        )
        db.upsert_kernel(kernel1)
        db.upsert_kernel(kernel2)

        db.set_default_kernel("s" * 64)

        k1 = db.get_kernel("r" * 64)
        k2 = db.get_kernel("s" * 64)
        assert k1 is not None
        assert k2 is not None
        assert bool(k1.is_default) is False
        assert bool(k2.is_default) is True

    def test_set_default_kernel_idempotent(self, db: MVMDatabase) -> None:
        """Test that calling set_default_kernel twice is safe."""
        kernel = Kernel(
            id="t" * 64,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
        )
        db.upsert_kernel(kernel)

        db.set_default_kernel("t" * 64)
        db.set_default_kernel("t" * 64)

        retrieved = db.get_kernel("t" * 64)
        assert retrieved is not None
        assert bool(retrieved.is_default) is True


class TestBinaryOperations:
    """Tests for binary CRUD operations."""

    def test_get_binary_found(self, db: MVMDatabase) -> None:
        """Test retrieving an existing binary by full ID."""
        binary = Binary(
            id="u" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
        )
        db.upsert_binary(binary)

        retrieved = db.get_binary("u" * 64)
        assert retrieved is not None
        assert retrieved.id == "u" * 64
        assert retrieved.name == "firecracker"
        assert retrieved.version == "1.15.0"

    def test_get_binary_not_found(self, db: MVMDatabase) -> None:
        """Test that get_binary returns None for non-existent ID."""
        result = db.get_binary("nonexistent" + "a" * 53)
        assert result is None

    def test_upsert_binary_insert(self, db: MVMDatabase) -> None:
        """Test inserting a new binary record."""
        binary = Binary(
            id="v" * 64,
            name="jailer",
            version="1.15.0",
            path="/cache/bin/jailer-v1.15.0",
        )
        db.upsert_binary(binary)

        retrieved = db.get_binary("v" * 64)
        assert retrieved is not None
        assert retrieved.name == "jailer"

    def test_upsert_binary_update(self, db: MVMDatabase) -> None:
        """Test updating an existing binary (ON CONFLICT)."""
        binary1 = Binary(
            id="w" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
        )
        db.upsert_binary(binary1)

        binary2 = Binary(
            id="w" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            full_version="v1.15.0",
            ci_version="v1.15",
        )
        db.upsert_binary(binary2)

        retrieved = db.get_binary("w" * 64)
        assert retrieved is not None
        assert retrieved.full_version == "v1.15.0"
        assert retrieved.ci_version == "v1.15"

    def test_delete_binary_found(self, db: MVMDatabase) -> None:
        """Test deleting an existing binary."""
        binary = Binary(
            id="x" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
        )
        db.upsert_binary(binary)
        assert db.get_binary("x" * 64) is not None

        db.delete_binary("x" * 64)
        assert db.get_binary("x" * 64) is None

    def test_delete_binary_not_found(self, db: MVMDatabase) -> None:
        """Test that deleting non-existent binary is a no-op."""
        db.delete_binary("nonexistent" + "a" * 53)
        assert db.get_binary("nonexistent" + "a" * 53) is None

    def test_list_binaries_empty(self, db: MVMDatabase) -> None:
        """Test listing binaries when none exist."""
        binaries = db.list_binaries()
        assert binaries == []

    def test_list_binaries_multiple(self, db: MVMDatabase) -> None:
        """Test listing multiple binaries ordered by created_at."""
        binary1 = Binary(
            id="y" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            created_at="2026-04-01T10:00:00Z",
        )
        binary2 = Binary(
            id="z" * 64,
            name="jailer",
            version="1.15.0",
            path="/cache/bin/jailer-v1.15.0",
            created_at="2026-04-02T10:00:00Z",
        )
        db.upsert_binary(binary1)
        db.upsert_binary(binary2)

        binaries = db.list_binaries()
        assert len(binaries) == 2
        assert binaries[0].id == "y" * 64
        assert binaries[1].id == "z" * 64

    def test_find_binaries_by_prefix_exact(self, db: MVMDatabase) -> None:
        """Test finding binary by exact prefix match."""
        binary = Binary(
            id="ghi789" + "d" * 58,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
        )
        db.upsert_binary(binary)

        results = db.find_binaries_by_prefix("ghi789")
        assert len(results) == 1
        assert results[0].id == "ghi789" + "d" * 58

    def test_find_binaries_by_prefix_multiple(self, db: MVMDatabase) -> None:
        """Test finding multiple binaries with same prefix."""
        binary1 = Binary(
            id="ghi000" + "d" * 58,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
        )
        binary2 = Binary(
            id="ghi111" + "d" * 58,
            name="jailer",
            version="1.15.0",
            path="/cache/bin/jailer-v1.15.0",
        )
        db.upsert_binary(binary1)
        db.upsert_binary(binary2)

        results = db.find_binaries_by_prefix("ghi")
        assert len(results) == 2

    def test_find_binaries_by_prefix_no_match(self, db: MVMDatabase) -> None:
        """Test finding binaries with non-matching prefix."""
        binary = Binary(
            id="xyz" + "a" * 61,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
        )
        db.upsert_binary(binary)

        results = db.find_binaries_by_prefix("ghi")
        assert results == []


class TestBinaryDefaultOperations:
    """Tests for binary default operations using is_default column."""

    def test_get_default_binary_found(self, db: MVMDatabase) -> None:
        """Test retrieving a default binary by name."""
        binary = Binary(
            id="u" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            is_default=True,
        )
        db.upsert_binary(binary)

        retrieved = db.get_default_binary("firecracker")
        assert retrieved is not None
        assert retrieved.name == "firecracker"
        assert retrieved.version == "1.15.0"
        assert bool(retrieved.is_default) is True

    def test_get_default_binary_not_found(self, db: MVMDatabase) -> None:
        """Test that get_default_binary returns None when no default set."""
        result = db.get_default_binary("nonexistent")
        assert result is None

    def test_set_default_binary_sets_flag(self, db: MVMDatabase) -> None:
        """Test that set_default_binary sets is_default=True."""
        binary = Binary(
            id="u" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            is_default=False,
        )
        db.upsert_binary(binary)

        db.set_default_binary("firecracker", "1.15.0", "/cache/bin/firecracker-v1.15.0")

        retrieved = db.get_default_binary("firecracker")
        assert retrieved is not None
        assert bool(retrieved.is_default) is True

    def test_set_default_binary_clears_others_same_name(self, db: MVMDatabase) -> None:
        """Test that set_default_binary clears is_default on other binaries with same name."""
        binary1 = Binary(
            id="u" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            is_default=True,
        )
        binary2 = Binary(
            id="v" * 64,
            name="firecracker",
            version="1.16.0",
            path="/cache/bin/firecracker-v1.16.0",
            is_default=False,
        )
        db.upsert_binary(binary1)
        db.upsert_binary(binary2)

        db.set_default_binary("firecracker", "1.16.0", "/cache/bin/firecracker-v1.16.0")

        b1 = db.get_binary("u" * 64)
        b2 = db.get_binary("v" * 64)
        assert b1 is not None
        assert b2 is not None
        assert bool(b1.is_default) is False
        assert bool(b2.is_default) is True

    def test_set_default_binary_different_names_independent(self, db: MVMDatabase) -> None:
        """Test that set_default_binary only affects binaries with the same name."""
        fc_binary = Binary(
            id="u" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            is_default=True,
        )
        jailer_binary = Binary(
            id="v" * 64,
            name="jailer",
            version="1.15.0",
            path="/cache/bin/jailer-v1.15.0",
            is_default=True,
        )
        db.upsert_binary(fc_binary)
        db.upsert_binary(jailer_binary)

        fc_binary2 = Binary(
            id="w" * 64,
            name="firecracker",
            version="1.16.0",
            path="/cache/bin/firecracker-v1.16.0",
            is_default=False,
        )
        db.upsert_binary(fc_binary2)
        db.set_default_binary("firecracker", "1.16.0", "/cache/bin/firecracker-v1.16.0")

        fc_default = db.get_default_binary("firecracker")
        jailer_default = db.get_default_binary("jailer")
        assert fc_default is not None
        assert fc_default.version == "1.16.0"
        assert jailer_default is not None
        assert jailer_default.version == "1.15.0"

    def test_binary_is_default_preserved_in_upsert(self, db: MVMDatabase) -> None:
        """Test that is_default field is preserved during upsert."""
        binary = Binary(
            id="u" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            is_default=True,
        )
        db.upsert_binary(binary)

        binary_updated = Binary(
            id="u" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            is_default=False,
        )
        db.upsert_binary(binary_updated)

        retrieved = db.get_binary("u" * 64)
        assert retrieved is not None
        assert bool(retrieved.is_default) is False


class TestMigrations:
    """Tests for database migrations."""

    def test_migrate_applies_migrations(self, tmp_path: Path) -> None:
        """Test that migrate() applies migrations on first run."""
        db_instance = MVMDatabase(db_path=tmp_path / "test.db")
        applied = db_instance.migrate()
        assert applied > 0

    def test_migrate_idempotent(self, tmp_path: Path) -> None:
        """Test that migrate() is idempotent (returns 0 on second run)."""
        db_instance = MVMDatabase(db_path=tmp_path / "test.db")
        db_instance.migrate()
        applied = db_instance.migrate()
        assert applied == 0

    def test_get_current_version_after_migrate(self, db: MVMDatabase) -> None:
        """Test that get_current_version returns correct version after migration."""
        version = db.get_current_version()
        assert version > 0

    def test_get_current_version_before_migrate(self, tmp_path: Path) -> None:
        """Test that get_current_version returns 0 before migration."""
        db_instance = MVMDatabase(db_path=tmp_path / "test.db")
        version = db_instance.get_current_version()
        assert version == 0


class TestEdgeCases:
    """Tests for edge cases and constraints."""

    def test_image_with_all_optional_fields(self, db: MVMDatabase) -> None:
        """Test that upsert/get preserves all optional image fields."""
        image = Image(
            id="1" * 64,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            os_name="Ubuntu",
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789012",
            compressed_size=1024000,
            original_size=2048000,
            compression_ratio=0.5,
            compressed_format="gzip",
            pulled_at="2026-04-02T10:00:00Z",
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        db.upsert_image(image)

        retrieved = db.get_image("1" * 64)
        assert retrieved is not None
        assert retrieved.os_name == "Ubuntu"
        assert retrieved.fs_type == "ext4"
        assert retrieved.fs_uuid == "12345678-1234-1234-1234-123456789012"
        assert retrieved.compressed_size == 1024000
        assert retrieved.original_size == 2048000
        assert retrieved.compression_ratio == 0.5
        assert retrieved.compressed_format == "gzip"
        assert retrieved.pulled_at == "2026-04-02T10:00:00Z"
        assert bool(retrieved.is_default) is True
        assert retrieved.created_at == "2026-04-02T10:00:00Z"

    def test_kernel_with_all_optional_fields(self, db: MVMDatabase) -> None:
        """Test that upsert/get preserves all optional kernel fields."""
        kernel = Kernel(
            id="2" * 64,
            name="vmlinux",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10.0",
            base_name="vmlinux-base",
            type="linux",
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        db.upsert_kernel(kernel)

        retrieved = db.get_kernel("2" * 64)
        assert retrieved is not None
        assert retrieved.base_name == "vmlinux-base"
        assert retrieved.type == "linux"
        assert bool(retrieved.is_default) is True
        assert retrieved.created_at == "2026-04-02T10:00:00Z"

    def test_binary_with_all_optional_fields(self, db: MVMDatabase) -> None:
        """Test that upsert/get preserves all optional binary fields."""
        binary = Binary(
            id="3" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            full_version="v1.15.0",
            ci_version="v1.15",
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        db.upsert_binary(binary)

        retrieved = db.get_binary("3" * 64)
        assert retrieved is not None
        assert retrieved.full_version == "v1.15.0"
        assert retrieved.ci_version == "v1.15"
        assert bool(retrieved.is_default) is True
        assert retrieved.created_at == "2026-04-02T10:00:00Z"

    def test_set_default_image_nonexistent(self, db: MVMDatabase) -> None:
        """Test setting non-existent image as default (no error)."""
        # Should not raise an exception
        db.set_default_image("nonexistent" + "a" * 53)

    def test_set_default_kernel_nonexistent(self, db: MVMDatabase) -> None:
        """Test setting non-existent kernel as default (no error)."""
        # Should not raise an exception
        db.set_default_kernel("nonexistent" + "a" * 53)

    def test_multiple_defaults_cleared_atomically(self, db: MVMDatabase) -> None:
        """Test that set_default_* operations are atomic."""
        # Create multiple images with one as default
        image1 = Image(
            id="4" * 64,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            is_default=True,
        )
        image2 = Image(
            id="5" * 64,
            os_slug="alpine-3.21",
            path="/cache/images/alpine-3.21.ext4",
            arch="x86_64",
            is_default=False,
        )
        image3 = Image(
            id="6" * 64,
            os_slug="debian-12",
            path="/cache/images/debian-12.ext4",
            arch="x86_64",
            is_default=False,
        )
        db.upsert_image(image1)
        db.upsert_image(image2)
        db.upsert_image(image3)

        # Set image3 as default
        db.set_default_image("6" * 64)

        # Verify only image3 is default
        img1 = db.get_image("4" * 64)
        img2 = db.get_image("5" * 64)
        img3 = db.get_image("6" * 64)
        assert img1 is not None
        assert img2 is not None
        assert img3 is not None
        assert bool(img1.is_default) is False
        assert bool(img2.is_default) is False
        assert bool(img3.is_default) is True

    def test_binary_is_default_atomic_update(self, db: MVMDatabase) -> None:
        """Test that set_default_binary atomically updates is_default flags."""
        binary1 = Binary(
            id="4" * 64,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
            is_default=True,
        )
        binary2 = Binary(
            id="5" * 64,
            name="firecracker",
            version="1.16.0",
            path="/cache/bin/firecracker-v1.16.0",
            is_default=False,
        )
        db.upsert_binary(binary1)
        db.upsert_binary(binary2)

        db.set_default_binary("firecracker", "1.16.0", "/cache/bin/firecracker-v1.16.0")

        b1 = db.get_binary("4" * 64)
        b2 = db.get_binary("5" * 64)
        assert b1 is not None
        assert b2 is not None
        assert bool(b1.is_default) is False
        assert bool(b2.is_default) is True

    def test_find_by_prefix_case_insensitive(self, db: MVMDatabase) -> None:
        """Test that prefix search is case-insensitive (SQLite LIKE default)."""
        image = Image(
            id="ABC123" + "d" * 58,
            os_slug="ubuntu-24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
        )
        db.upsert_image(image)

        # SQLite LIKE is case-insensitive by default
        results = db.find_images_by_prefix("abc123")
        assert len(results) == 1
        assert results[0].id == "ABC123" + "d" * 58

        # Uppercase should also match
        results = db.find_images_by_prefix("ABC123")
        assert len(results) == 1


class TestKernelByName:
    def test_get_kernel_by_name_found(self, db: MVMDatabase) -> None:
        kernel = Kernel(
            id="n" * 64,
            name="vmlinux-5.10",
            version="5.10.0",
            arch="x86_64",
            path="/cache/kernels/vmlinux-5.10",
        )
        db.upsert_kernel(kernel)
        result = db.get_kernel_by_name("vmlinux-5.10")
        assert result is not None
        assert result.name == "vmlinux-5.10"

    def test_get_kernel_by_name_not_found(self, db: MVMDatabase) -> None:
        result = db.get_kernel_by_name("nonexistent-kernel")
        assert result is None


class TestDeleteBinaryByNameAndVersion:
    def test_delete_by_plain_version(self, db: MVMDatabase) -> None:
        binary = Binary(
            id="dv" * 32,
            name="firecracker",
            version="1.15.0",
            path="/cache/bin/firecracker-v1.15.0",
        )
        db.upsert_binary(binary)
        assert db.get_binary("dv" * 32) is not None
        db.delete_binary_by_name_and_version("firecracker", "1.15.0")
        assert db.get_binary("dv" * 32) is None

    def test_delete_by_prefixed_version(self, db: MVMDatabase) -> None:
        binary = Binary(
            id="pv" * 32,
            name="firecracker",
            version="1.16.0",
            path="/cache/bin/firecracker-v1.16.0",
        )
        db.upsert_binary(binary)
        db.delete_binary_by_name_and_version("firecracker", "v1.16.0")
        assert db.get_binary("pv" * 32) is None

    def test_delete_noop_when_not_found(self, db: MVMDatabase) -> None:
        db.delete_binary_by_name_and_version("firecracker", "9.9.9")
        assert db.list_binaries() == []
