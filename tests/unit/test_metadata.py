"""Tests for the unified metadata storage (SQLite-only)."""

from pathlib import Path

from mvmctl.core.metadata import (
    find_images_by_id_prefix,
    get_binary_entry,
    get_default_binary_entry,
    get_default_image_entry,
    get_default_kernel_entry,
    get_default_network_entry,
    get_image_entry,
    get_kernel_entry,
    get_network_entry,
    list_binary_entries,
    list_image_entries,
    list_kernel_entries,
    list_network_entries,
    remove_image_entry,
    remove_kernel_entry,
    set_default_binary_entry,
    set_default_image_by_os_slug,
    set_default_image_entry,
    set_default_kernel_by_filename,
    set_default_network_entry,
    update_binary_entry,
    update_image_entry,
    update_kernel_entry,
    update_network_entry,
)
from mvmctl.core.mvm_db import MVMDatabase
from tests.helpers.paths import make_test_paths


class TestKernelMetadata:
    """Tests for kernel metadata operations."""

    def test_update_and_get_kernel_entry(self, tmp_path: Path):
        """Test updating and retrieving a kernel entry."""
        cache_dir = make_test_paths(tmp_path).cache

        update_kernel_entry(
            cache_dir,
            "kernel123",
            name="vmlinux-test",
            version="6.1.0",
            arch="x86_64",
            path="vmlinux-test",
            type="official",
        )

        entry = get_kernel_entry(cache_dir, "kernel123")
        assert entry["name"] == "vmlinux-test"
        assert entry["version"] == "6.1.0"
        assert entry["arch"] == "x86_64"

    def test_list_kernel_entries(self, tmp_path: Path):
        """Test listing all kernel entries."""
        cache_dir = make_test_paths(tmp_path).cache

        update_kernel_entry(cache_dir, "kernel1", name="vmlinux-1", version="6.1.0")
        update_kernel_entry(cache_dir, "kernel2", name="vmlinux-2", version="6.2.0")

        entries = list_kernel_entries(cache_dir)
        assert len(entries) == 2
        assert "kernel1" in entries
        assert "kernel2" in entries

    def test_remove_kernel_entry(self, tmp_path: Path):
        """Test removing a kernel entry."""
        cache_dir = make_test_paths(tmp_path).cache

        update_kernel_entry(cache_dir, "kernel1", name="vmlinux-1", version="6.1.0")
        remove_kernel_entry(cache_dir, "kernel1")

        entry = get_kernel_entry(cache_dir, "kernel1")
        assert entry == {}

    def test_set_default_kernel(self, tmp_path: Path):
        """Test setting a default kernel."""
        cache_dir = make_test_paths(tmp_path).cache

        update_kernel_entry(
            cache_dir, "kernel1", name="vmlinux-1", version="6.1.0", path="vmlinux-1"
        )
        set_default_kernel_by_filename(cache_dir, "vmlinux-1")

        default = get_default_kernel_entry(cache_dir)
        assert default is not None
        assert default[1]["name"] == "vmlinux-1"

    def test_get_kernel_entry_missing_returns_empty(self, tmp_path: Path):
        """Test that missing kernel entries return empty dict."""
        cache_dir = make_test_paths(tmp_path).cache

        entry = get_kernel_entry(cache_dir, "nonexistent")
        assert entry == {}


class TestImageMetadata:
    """Tests for image metadata operations."""

    def test_update_and_get_image_entry(self, tmp_path: Path):
        """Test updating and retrieving an image entry."""
        cache_dir = make_test_paths(tmp_path).cache

        update_image_entry(
            cache_dir,
            "img123",
            os_slug="ubuntu-24.04",
            path="ubuntu-24.04.ext4",
            os_name="Ubuntu 24.04",
            fs_type="ext4",
        )

        entry = get_image_entry(cache_dir, "img123")
        assert entry["os_slug"] == "ubuntu-24.04"
        assert entry["os_name"] == "Ubuntu 24.04"
        assert entry["fs_type"] == "ext4"

    def test_list_image_entries(self, tmp_path: Path):
        """Test listing all image entries."""
        cache_dir = make_test_paths(tmp_path).cache

        update_image_entry(
            cache_dir, "img1", os_slug="ubuntu-24.04", os_name="Ubuntu", fs_type="ext4"
        )
        update_image_entry(cache_dir, "img2", os_slug="debian-12", os_name="Debian", fs_type="ext4")

        entries = list_image_entries(cache_dir)
        assert len(entries) == 2
        assert "img1" in entries
        assert "img2" in entries

    def test_remove_image_entry(self, tmp_path: Path):
        """Test removing an image entry."""
        cache_dir = make_test_paths(tmp_path).cache

        update_image_entry(cache_dir, "img1", os_name="Ubuntu", fs_type="ext4")
        remove_image_entry(cache_dir, "img1")

        entry = get_image_entry(cache_dir, "img1")
        assert entry == {}

    def test_set_default_image(self, tmp_path: Path):
        """Test setting a default image."""
        cache_dir = make_test_paths(tmp_path).cache

        update_image_entry(
            cache_dir,
            "img1",
            os_slug="ubuntu-24.04",
            path="ubuntu-24.04.ext4",
            os_name="Ubuntu 24.04",
        )
        set_default_image_entry(cache_dir, "img1")

        default = get_default_image_entry(cache_dir)
        assert default is not None
        assert default[0] == "img1"

    def test_set_default_image_by_os_slug(self, tmp_path: Path):
        """Test setting a default image by internal ID."""
        cache_dir = make_test_paths(tmp_path).cache

        update_image_entry(
            cache_dir,
            "img1",
            os_slug="ubuntu-24.04",
            path="ubuntu-24.04.ext4",
            os_name="Ubuntu 24.04",
        )
        set_default_image_by_os_slug(cache_dir, "ubuntu-24.04")

        default = get_default_image_entry(cache_dir)
        assert default is not None

    def test_find_images_by_prefix(self, tmp_path: Path):
        """Test finding images by ID prefix."""
        cache_dir = make_test_paths(tmp_path).cache

        update_image_entry(
            cache_dir, "abc123", os_slug="ubuntu-24.04", os_name="Ubuntu", fs_type="ext4"
        )
        update_image_entry(
            cache_dir, "abc456", os_slug="debian-12", os_name="Debian", fs_type="ext4"
        )
        update_image_entry(
            cache_dir, "def789", os_slug="fedora-40", os_name="Fedora", fs_type="ext4"
        )

        matches = find_images_by_id_prefix(cache_dir, "abc")
        assert len(matches) == 2

        single = find_images_by_id_prefix(cache_dir, "def")
        assert len(single) == 1

    def test_find_image_by_id_prefix(self, tmp_path: Path):
        """Test finding a single image by ID prefix."""
        cache_dir = make_test_paths(tmp_path).cache

        update_image_entry(cache_dir, "abc123", os_name="Ubuntu", fs_type="ext4")

        from mvmctl.core.metadata import find_image_by_id_prefix

        match = find_image_by_id_prefix(cache_dir, "abc")
        assert match is not None
        assert match[0] == "abc123"


class TestBinaryMetadata:
    """Tests for binary metadata operations."""

    def test_update_and_get_binary_entry(self, tmp_path: Path):
        """Test updating and retrieving a binary entry."""
        cache_dir = make_test_paths(tmp_path).cache

        # Create fake binary files
        bin_dir = cache_dir / "bin"
        bin_dir.mkdir()
        fc_path = bin_dir / "firecracker-v1.15.0"
        jl_path = bin_dir / "jailer-v1.15.0"
        fc_path.write_bytes(b"firecracker")
        jl_path.write_bytes(b"jailer")

        update_binary_entry(
            cache_dir,
            "1.15.0",
            firecracker_path=str(fc_path),
            jailer_path=str(jl_path),
        )

        entry = get_binary_entry(cache_dir, "1.15.0")
        assert entry["package_version"] == "1.15.0"

    def test_list_binary_entries(self, tmp_path: Path):
        """Test listing all binary entries."""
        cache_dir = make_test_paths(tmp_path).cache

        bin_dir = cache_dir / "bin"
        bin_dir.mkdir()
        fc_path = bin_dir / "firecracker-v1.15.0"
        jl_path = bin_dir / "jailer-v1.15.0"
        fc_path.write_bytes(b"firecracker")
        jl_path.write_bytes(b"jailer")

        update_binary_entry(
            cache_dir,
            "1.15.0",
            firecracker_path=str(fc_path),
            jailer_path=str(jl_path),
        )

        entries = list_binary_entries(cache_dir)
        assert "firecracker" in entries
        assert "jailer" in entries

    def test_set_and_get_default_binary(self, tmp_path: Path):
        """Test setting and retrieving default binary."""
        cache_dir = make_test_paths(tmp_path).cache

        bin_dir = cache_dir / "bin"
        bin_dir.mkdir()
        fc_path = bin_dir / "firecracker-v1.15.0"
        jl_path = bin_dir / "jailer-v1.15.0"
        fc_path.write_bytes(b"firecracker")
        jl_path.write_bytes(b"jailer")

        update_binary_entry(
            cache_dir,
            "1.15.0",
            firecracker_path=str(fc_path),
            jailer_path=str(jl_path),
        )
        set_default_binary_entry(cache_dir, "1.15.0")

        default = get_default_binary_entry(cache_dir)
        assert default is not None
        assert default[0] == "1.15.0"


class TestNetworkMetadata:
    """Tests for network metadata operations."""

    def test_update_and_get_network_entry(self, tmp_path: Path):
        """Test updating and retrieving a network entry."""
        cache_dir = make_test_paths(tmp_path).cache

        update_network_entry(
            cache_dir,
            "testnet",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-testnet",
        )

        entry = get_network_entry(cache_dir, "testnet")
        assert entry["subnet"] == "10.20.0.0/24"
        assert entry["ipv4_gateway"] == "10.20.0.1"

    def test_list_network_entries(self, tmp_path: Path):
        """Test listing all network entries."""
        cache_dir = make_test_paths(tmp_path).cache

        update_network_entry(cache_dir, "net1", subnet="10.20.1.0/24", ipv4_gateway="10.20.1.1")
        update_network_entry(cache_dir, "net2", subnet="10.20.2.0/24", ipv4_gateway="10.20.2.1")

        entries = list_network_entries(cache_dir)
        assert len(entries) == 2
        assert "net1" in entries
        assert "net2" in entries

    def test_set_and_get_default_network(self, tmp_path: Path):
        """Test setting and retrieving default network."""
        cache_dir = make_test_paths(tmp_path).cache

        update_network_entry(cache_dir, "default", cidr="172.35.0.0/24", gateway="172.35.0.1")
        set_default_network_entry(cache_dir, "default")

        default = get_default_network_entry(cache_dir)
        assert default is not None
        assert default[0] == "default"


class TestOrphanedEntryCleanup:
    """Tests for orphaned entry cleanup."""

    def test_list_kernel_entries_removes_orphaned(self, tmp_path: Path):
        """Test that orphaned kernel entries are removed."""
        cache_dir = make_test_paths(tmp_path).cache

        kernels_dir = cache_dir / "kernels"
        kernels_dir.mkdir()

        # Create a real kernel file
        valid_kernel = kernels_dir / "vmlinux"
        valid_kernel.write_bytes(b"\x7fELF")

        # Add entries - one with matching file, one orphaned
        update_kernel_entry(cache_dir, "valid123", name="vmlinux", path="vmlinux")
        update_kernel_entry(cache_dir, "orphan456", name="missing", path="missing")

        # List with validation
        entries = list_kernel_entries(cache_dir, kernels_dir)

        # Should only have the valid entry
        assert "valid123" in entries
        assert "orphan456" not in entries

        # Orphan should be deleted from DB
        assert get_kernel_entry(cache_dir, "orphan456") == {}

    def test_list_image_entries_removes_orphaned(self, tmp_path: Path):
        """Test that orphaned image entries are removed."""
        cache_dir = make_test_paths(tmp_path).cache

        images_dir = cache_dir / "images"
        images_dir.mkdir()

        # Create a real image file
        valid_image = images_dir / "ubuntu.ext4"
        valid_image.write_bytes(b"ext4 data")

        # Add entries - one with matching file, one orphaned
        update_image_entry(cache_dir, "valid123", os_slug="ubuntu-24.04", path="ubuntu.ext4")
        update_image_entry(cache_dir, "orphan456", os_slug="debian-12", path="missing.ext4")

        # List with validation
        entries = list_image_entries(cache_dir, images_dir)

        # Should only have the valid entry
        assert "valid123" in entries
        assert "orphan456" not in entries


class TestDualWriteCompatibility:
    """Tests ensuring SQLite-only behavior works correctly."""

    def test_image_fetch_writes_to_sqlite(self, tmp_path: Path):
        """Test that image entries are written to SQLite."""
        cache_dir = make_test_paths(tmp_path).cache
        db = MVMDatabase()

        image_id = "a" * 64
        update_image_entry(
            cache_dir,
            image_id,
            internal_id="alpine-3.21",
            path="alpine-3.21.ext4",
            os_name="Alpine Linux",
            is_default=0,
        )

        row = db.get_image(image_id)
        assert row is not None
        assert row.os_slug == "alpine-3.21"
        assert row.path == "alpine-3.21.ext4"
        assert row.os_name == "Alpine Linux"

    def test_kernel_fetch_writes_to_sqlite(self, tmp_path: Path):
        """Test that kernel entries are written to SQLite."""
        cache_dir = make_test_paths(tmp_path).cache
        db = MVMDatabase()

        kernel_id = "c" * 64
        update_kernel_entry(
            cache_dir,
            kernel_id,
            name="vmlinux-6.1",
            version="6.1.102",
            arch="x86_64",
            path="vmlinux-6.1",
            type="official",
            is_default=0,
        )

        row = db.get_kernel(kernel_id)
        assert row is not None
        assert row.name == "vmlinux-6.1"
        assert row.version == "6.1.102"
        assert row.arch == "x86_64"

    def test_bin_set_default_writes_to_sqlite(self, tmp_path: Path):
        """Test that binary defaults are written to SQLite."""
        cache_dir = make_test_paths(tmp_path).cache
        db = MVMDatabase()

        bin_dir = cache_dir / "bin"
        bin_dir.mkdir()
        fc_path = bin_dir / "firecracker-v1.15.0"
        jl_path = bin_dir / "jailer-v1.15.0"
        fc_path.write_bytes(b"firecracker")
        jl_path.write_bytes(b"jailer")

        update_binary_entry(
            cache_dir,
            "1.15.0",
            firecracker_path=str(fc_path),
            jailer_path=str(jl_path),
        )
        set_default_binary_entry(cache_dir, "1.15.0")

        fc_default = db.get_default_binary("firecracker")
        jl_default = db.get_default_binary("jailer")

        assert fc_default is not None
        assert fc_default.version == "1.15.0"
        assert bool(fc_default.is_default) is True

        assert jl_default is not None
        assert jl_default.version == "1.15.0"
        assert bool(jl_default.is_default) is True
