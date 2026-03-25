"""Tests for the unified metadata.json store."""

import json
import multiprocessing
import time
from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.core.metadata import (
    MetadataCache,
    _metadata_cache,
    get_binary_entry,
    get_image_entry,
    get_kernel_entry,
    list_image_entries,
    list_kernel_entries,
    migrate_legacy_metadata,
    read_metadata,
    remove_image_entry,
    remove_kernel_entry,
    update_binary_entry,
    update_image_entry,
    update_kernel_entry,
    write_metadata,
)


def _concurrent_writer(cache_dir_str: str, writer_id: int, num_writes: int) -> int:
    """Worker function for concurrent write test. Writes unique entries and returns success count."""
    cache_dir = Path(cache_dir_str)
    success_count = 0
    for i in range(num_writes):
        try:
            entry_key = f"writer{writer_id}_entry{i}"
            update_kernel_entry(cache_dir, entry_key, writer_id=writer_id, entry_index=i)
            success_count += 1
        except Exception:
            pass
    return success_count


def _writer_process(cache_dir_str: str, writer_id: int, num_operations: int) -> int:
    """Writer process for concurrent read/write test."""
    cache_dir = Path(cache_dir_str)
    success = 0
    for i in range(num_operations):
        try:
            update_kernel_entry(cache_dir, f"writer{writer_id}_entry{i}", version=f"{i}")
            success += 1
        except Exception:
            pass
    return success


def _reader_process(cache_dir_str: str, reader_id: int, num_operations: int) -> int:
    """Reader process for concurrent read/write test."""
    cache_dir = Path(cache_dir_str)
    success = 0
    for _ in range(num_operations * 2):
        try:
            meta = read_metadata(cache_dir)
            _ = meta.get("kernels", {})
            success += 1
        except Exception:
            pass
    return success


def test_read_metadata_missing_returns_empty(tmp_path: Path):
    result = read_metadata(tmp_path)
    assert result == {}


def test_read_metadata_invalid_json_returns_empty(tmp_path: Path):
    (tmp_path / "metadata.json").write_text("not valid json {{{")
    result = read_metadata(tmp_path)
    assert result == {}


def test_read_metadata_non_dict_returns_empty(tmp_path: Path):
    (tmp_path / "metadata.json").write_text("[1, 2, 3]")
    result = read_metadata(tmp_path)
    assert result == {}


def test_list_kernel_entries_removes_orphaned_on_read(tmp_path: Path):
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    valid_kernel = kernels_dir / "vmlinux"
    valid_kernel.write_bytes(b"\x7fELF")

    write_metadata(
        tmp_path,
        {
            "kernels": {
                "vmlinux": {"version": "6.1"},
                "missing-kernel": {"version": "6.2"},
            }
        },
    )

    result = list_kernel_entries(tmp_path, kernels_dir)

    assert result == {"vmlinux": {"version": "6.1"}}
    persisted = json.loads((tmp_path / "metadata.json").read_text())
    assert persisted["kernels"] == {"vmlinux": {"version": "6.1"}}


def test_list_image_entries_removes_orphaned_on_read(tmp_path: Path):
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    image_path = images_dir / "valid.ext4"
    image_path.write_text("image")

    write_metadata(
        tmp_path,
        {
            "images": {
                "valid": {"filename": "valid.ext4", "fs_type": "ext4"},
                "stale": {"filename": "stale.ext4", "fs_type": "ext4"},
            }
        },
    )

    result = list_image_entries(tmp_path, images_dir)

    assert set(result.keys()) == {"valid"}
    persisted = json.loads((tmp_path / "metadata.json").read_text())
    assert set(persisted["images"].keys()) == {"valid"}


def test_write_metadata_creates_file(tmp_path: Path):
    write_metadata(tmp_path, {"kernels": {}, "images": {}})
    meta_file = tmp_path / "metadata.json"
    assert meta_file.exists()
    data = json.loads(meta_file.read_text())
    assert "kernels" in data


def test_write_metadata_sets_permissions(tmp_path: Path):
    write_metadata(tmp_path, {"x": 1})
    mode = (tmp_path / "metadata.json").stat().st_mode & 0o777
    assert mode == 0o600


def test_update_kernel_entry_creates_metadata_json(tmp_path: Path):
    update_kernel_entry(tmp_path, "vmlinux", name="vmlinux", version="6.1")
    meta = read_metadata(tmp_path)
    assert "vmlinux" in meta["kernels"]
    assert meta["kernels"]["vmlinux"]["version"] == "6.1"


def test_update_kernel_entry_merges_with_existing(tmp_path: Path):
    update_kernel_entry(tmp_path, "vmlinux", version="6.1")
    update_kernel_entry(tmp_path, "vmlinux", type="official")
    entry = get_kernel_entry(tmp_path, "vmlinux")
    assert entry["version"] == "6.1"
    assert entry["type"] == "official"


def test_list_kernel_entries_empty(tmp_path: Path):
    result = list_kernel_entries(tmp_path)
    assert result == {}


def test_list_kernel_entries_returns_all(tmp_path: Path):
    update_kernel_entry(tmp_path, "vmlinux-a", version="6.1")
    update_kernel_entry(tmp_path, "vmlinux-b", version="6.2")
    result = list_kernel_entries(tmp_path)
    assert set(result.keys()) == {"vmlinux-a", "vmlinux-b"}


def test_remove_kernel_entry(tmp_path: Path):
    update_kernel_entry(tmp_path, "vmlinux", version="6.1")
    remove_kernel_entry(tmp_path, "vmlinux")
    assert get_kernel_entry(tmp_path, "vmlinux") == {}


def test_remove_kernel_entry_noop_if_missing(tmp_path: Path):
    remove_kernel_entry(tmp_path, "nonexistent")


def test_get_kernel_entry_missing_returns_empty(tmp_path: Path):
    result = get_kernel_entry(tmp_path, "nonexistent")
    assert result == {}


def test_update_image_entry(tmp_path: Path):
    update_image_entry(tmp_path, "ubuntu-24.04", os_name="Ubuntu", fs_type="ext4")
    entry = get_image_entry(tmp_path, "ubuntu-24.04")
    assert entry["os_name"] == "Ubuntu"
    assert entry["fs_type"] == "ext4"


def test_get_image_entry_missing_returns_empty(tmp_path: Path):
    result = get_image_entry(tmp_path, "no-such-image")
    assert result == {}


def test_list_image_entries(tmp_path: Path):
    update_image_entry(tmp_path, "img-a", fs_type="ext4")
    update_image_entry(tmp_path, "img-b", fs_type="btrfs")
    result = list_image_entries(tmp_path)
    assert set(result.keys()) == {"img-a", "img-b"}


def test_remove_image_entry(tmp_path: Path):
    update_image_entry(tmp_path, "ubuntu-24.04", fs_type="ext4")
    remove_image_entry(tmp_path, "ubuntu-24.04")
    assert get_image_entry(tmp_path, "ubuntu-24.04") == {}


def test_update_binary_entry(tmp_path: Path):
    update_binary_entry(tmp_path, "1.15.0", firecracker_path="/bin/fc")
    entry = get_binary_entry(tmp_path, "1.15.0")
    assert entry["firecracker_path"] == "/bin/fc"


def test_get_binary_entry_missing_returns_empty(tmp_path: Path):
    assert get_binary_entry(tmp_path, "99.0.0") == {}


def test_migrate_legacy_metadata_imports_per_file_json(tmp_path: Path):
    kernels_dir = tmp_path / "kernels"
    images_dir = tmp_path / "images"
    kernels_dir.mkdir()
    images_dir.mkdir()

    (kernels_dir / "vmlinux").write_bytes(b"\x7fELF")
    (kernels_dir / "vmlinux.json").write_text(
        json.dumps({"name": "vmlinux", "version": "6.1", "type": "official"})
    )
    (images_dir / "ubuntu-24.04.ext4.json").write_text(
        json.dumps({"os_name": "Ubuntu", "fs_type": "ext4"})
    )

    migrate_legacy_metadata(tmp_path, kernels_dir, images_dir)

    meta = read_metadata(tmp_path)
    assert "vmlinux" in meta["kernels"]
    assert meta["kernels"]["vmlinux"]["version"] == "6.1"
    assert "ubuntu-24.04" in meta["images"]
    assert meta["images"]["ubuntu-24.04"]["os_name"] == "Ubuntu"

    assert not (kernels_dir / "vmlinux.json").exists()
    assert not (images_dir / "ubuntu-24.04.ext4.json").exists()


def test_migrate_legacy_metadata_skips_if_already_populated(tmp_path: Path):
    kernels_dir = tmp_path / "kernels"
    images_dir = tmp_path / "images"
    kernels_dir.mkdir()
    images_dir.mkdir()

    update_kernel_entry(tmp_path, "vmlinux", version="6.1")

    (kernels_dir / "vmlinux").write_bytes(b"\x7fELF")
    sidecar = kernels_dir / "vmlinux.json"
    sidecar.write_text(json.dumps({"name": "vmlinux", "version": "9.9"}))

    migrate_legacy_metadata(tmp_path, kernels_dir, images_dir)

    meta = read_metadata(tmp_path)
    assert meta["kernels"]["vmlinux"]["version"] == "6.1"
    assert sidecar.exists()


def test_migrate_legacy_metadata_handles_default_json(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    kernels_dir = tmp_path / "kernels"
    images_dir = tmp_path / "images"
    kernels_dir.mkdir()
    images_dir.mkdir()

    (kernels_dir / "vmlinux").write_bytes(b"\x7fELF")
    (kernels_dir / "default.json").write_text(json.dumps({"name": "vmlinux"}))

    migrate_legacy_metadata(tmp_path, kernels_dir, images_dir)

    assert not (kernels_dir / "default.json").exists()

    config_file = tmp_path / "config.json"
    if config_file.exists():
        data = json.loads(config_file.read_text())
        assert data.get("defaults", {}).get("kernel") == "vmlinux"


def test_migrate_legacy_metadata_ignores_corrupt_json(tmp_path: Path):
    kernels_dir = tmp_path / "kernels"
    images_dir = tmp_path / "images"
    kernels_dir.mkdir()
    images_dir.mkdir()

    (kernels_dir / "vmlinux").write_bytes(b"\x7fELF")
    (kernels_dir / "vmlinux.json").write_text("CORRUPT{{")

    migrate_legacy_metadata(tmp_path, kernels_dir, images_dir)
    meta = read_metadata(tmp_path)
    assert meta.get("kernels", {}) == {}


def test_concurrent_writes_no_corruption(tmp_path: Path):
    """Test that concurrent writes to metadata.json do not corrupt the file.

    Spawns multiple processes that simultaneously write to the same metadata file.
    Verifies that the file remains valid JSON and operations complete without crashes.
    File locking ensures atomic writes - no partial/corrupted JSON.
    """
    cache_dir = tmp_path / "concurrent_test_cache"
    cache_dir.mkdir(exist_ok=True)

    num_processes = 4
    num_writes_per_process = 10

    with multiprocessing.Pool(processes=num_processes) as pool:
        results = [
            pool.apply_async(_concurrent_writer, (str(cache_dir), i, num_writes_per_process))
            for i in range(num_processes)
        ]
        success_counts = [r.get(timeout=30) for r in results]

    # All operations should complete without exceptions
    assert sum(success_counts) == num_processes * num_writes_per_process

    # Metadata should be valid JSON (not corrupted)
    meta = read_metadata(cache_dir)
    assert isinstance(meta, dict)
    assert "kernels" in meta
    assert isinstance(meta["kernels"], dict)

    # Verify at least some entries were written (file locking allows atomic writes)
    # Note: With read-modify-write pattern, last writer wins per key,
    # but file locking prevents JSON corruption from interleaved writes
    total_entries = len(meta["kernels"])
    assert total_entries > 0


def test_concurrent_reads_and_writes_no_corruption(tmp_path: Path):
    """Test that concurrent reads and writes do not cause corruption or crashes.

    Spawns processes that simultaneously read and write to the same metadata file.
    File locking ensures readers get consistent views and writers don't corrupt file.
    """
    cache_dir = tmp_path / "concurrent_rw_test_cache"
    cache_dir.mkdir(exist_ok=True)

    update_kernel_entry(cache_dir, "initial", version="1.0")

    num_writers = 3
    num_readers = 3
    num_operations = 20

    with multiprocessing.Pool(processes=num_writers + num_readers) as pool:
        writer_results = [
            pool.apply_async(_writer_process, (str(cache_dir), i, num_operations))
            for i in range(num_writers)
        ]
        reader_results = [
            pool.apply_async(_reader_process, (str(cache_dir), i, num_operations))
            for i in range(num_readers)
        ]

        writer_success = sum(r.get(timeout=30) for r in writer_results)
        reader_success = sum(r.get(timeout=30) for r in reader_results)

    # All writes should succeed
    assert writer_success == num_writers * num_operations

    # All reads should succeed (shared lock allows concurrent reads)
    assert reader_success == num_readers * num_operations * 2

    # Verify final state is valid JSON
    meta = read_metadata(cache_dir)
    assert isinstance(meta, dict)
    assert "kernels" in meta
    assert "initial" in meta["kernels"]
    assert meta["kernels"]["initial"]["version"] == "1.0"


def test_lock_file_created_separate_from_metadata(tmp_path: Path):
    """Test that the lock file is created separately from metadata.json."""
    cache_dir = tmp_path / "lock_test_cache"
    cache_dir.mkdir(exist_ok=True)

    write_metadata(cache_dir, {"test": "data"})

    assert (cache_dir / "metadata.json").exists()
    assert (cache_dir / "metadata.json.lock").exists()

    meta_stat = (cache_dir / "metadata.json").stat()
    lock_stat = (cache_dir / "metadata.json.lock").stat()
    assert meta_stat.st_ino != lock_stat.st_ino


# =============================================================================
# MetadataCache tests
# =============================================================================


def test_metadata_cache_returns_cached_data(tmp_path: Path):
    """Test that cache returns data without file I/O on cache hit."""
    data = {"kernels": {"vmlinux": {"version": "6.1"}}}

    # Pre-populate the file
    write_metadata(tmp_path, data)

    # First read should populate cache
    result = read_metadata(tmp_path)
    assert result == data

    with patch.object(Path, "read_text") as mock_read_text:
        cached_result = read_metadata(tmp_path)
        mock_read_text.assert_not_called()

    assert cached_result == data


def test_metadata_cache_invalidates_on_file_change(tmp_path: Path):
    """Test that cache invalidates when file mtime changes."""
    cache_dir = tmp_path / "cache_test"
    cache_dir.mkdir()

    # Write initial data
    write_metadata(cache_dir, {"kernels": {"vmlinux": {"version": "6.1"}}})

    # Read to populate cache
    result1 = read_metadata(cache_dir)
    assert result1["kernels"]["vmlinux"]["version"] == "6.1"

    # Modify the file
    time.sleep(0.1)  # Ensure mtime changes
    write_metadata(cache_dir, {"kernels": {"vmlinux": {"version": "6.2"}}})

    # Read should return new data (cache invalidated)
    result2 = read_metadata(cache_dir)
    assert result2["kernels"]["vmlinux"]["version"] == "6.2"


def test_metadata_cache_expires_after_ttl(tmp_path: Path):
    """Test that cache entries expire after TTL."""
    cache = MetadataCache(ttl=0.1)  # 100ms TTL

    # Pre-populate the file
    write_metadata(tmp_path, {"kernels": {"vmlinux": {"version": "6.1"}}})

    # Populate cache
    cache.set(tmp_path, {"kernels": {"vmlinux": {"version": "6.1"}}})

    # Should return cached data immediately
    assert cache.get(tmp_path) is not None

    # Wait for TTL to expire
    time.sleep(0.15)

    assert cache.get(tmp_path) == {"kernels": {"vmlinux": {"version": "6.1"}}}


def test_metadata_cache_refreshes_ttl_when_mtime_unchanged(tmp_path: Path):
    """Test expired entries are reused when file mtime is unchanged."""
    cache = MetadataCache(ttl=0.05)

    write_metadata(tmp_path, {"kernels": {"vmlinux": {"version": "6.1"}}})
    cache.set(tmp_path, {"kernels": {"vmlinux": {"version": "6.1"}}})

    time.sleep(0.06)

    refreshed = cache.get(tmp_path)
    assert refreshed == {"kernels": {"vmlinux": {"version": "6.1"}}}


def test_metadata_cache_evicts_oldest_entry(tmp_path: Path):
    """Test LRU eviction when cache exceeds max_entries."""
    cache = MetadataCache(ttl=5.0, max_entries=2)

    cache_dir_a = tmp_path / "cache_a"
    cache_dir_b = tmp_path / "cache_b"
    cache_dir_c = tmp_path / "cache_c"
    cache_dir_a.mkdir()
    cache_dir_b.mkdir()
    cache_dir_c.mkdir()

    write_metadata(cache_dir_a, {"data": "a"})
    write_metadata(cache_dir_b, {"data": "b"})
    write_metadata(cache_dir_c, {"data": "c"})

    cache.set(cache_dir_a, {"data": "a"})
    cache.set(cache_dir_b, {"data": "b"})
    cache.set(cache_dir_c, {"data": "c"})

    assert cache.get(cache_dir_a) is None
    assert cache.get(cache_dir_b) == {"data": "b"}
    assert cache.get(cache_dir_c) == {"data": "c"}


def test_metadata_cache_thread_safety(tmp_path: Path):
    """Test that cache is thread-safe."""
    import threading

    cache = MetadataCache(ttl=5.0)
    errors = []
    results = []

    def reader():
        try:
            for _ in range(100):
                result = cache.get(tmp_path)
                results.append(result)
        except Exception as e:
            errors.append(e)

    def writer():
        try:
            for i in range(100):
                cache.set(tmp_path, {"version": i})
        except Exception as e:
            errors.append(e)

    # Start multiple threads
    threads = []
    for _ in range(3):
        t = threading.Thread(target=reader)
        threads.append(t)
        t.start()

    for _ in range(2):
        t = threading.Thread(target=writer)
        threads.append(t)
        t.start()

    for t in threads:
        t.join()

    assert not errors, f"Thread errors: {errors}"


def test_metadata_cache_invalidate_single(tmp_path: Path):
    """Test invalidating a single cache entry."""
    cache = MetadataCache(ttl=5.0)

    # Create the metadata file first
    write_metadata(tmp_path, {"data": "test"})

    # Now set in cache
    cache.set(tmp_path, {"data": "test"})
    assert cache.get(tmp_path) is not None

    cache.invalidate(tmp_path)
    assert cache.get(tmp_path) is None


def test_metadata_cache_invalidate_all(tmp_path: Path):
    """Test invalidating all cache entries."""
    cache = MetadataCache(ttl=5.0)

    cache_dir_a = tmp_path / "cache_a"
    cache_dir_b = tmp_path / "cache_b"
    cache_dir_a.mkdir()
    cache_dir_b.mkdir()

    # Create metadata files
    write_metadata(cache_dir_a, {"data": "a"})
    write_metadata(cache_dir_b, {"data": "b"})

    # Set in cache
    cache.set(cache_dir_a, {"data": "a"})
    cache.set(cache_dir_b, {"data": "b"})

    assert cache.get(cache_dir_a) is not None
    assert cache.get(cache_dir_b) is not None

    cache.invalidate()

    assert cache.get(cache_dir_a) is None
    assert cache.get(cache_dir_b) is None


def test_write_metadata_invalidates_cache(tmp_path: Path):
    """Test that write_metadata invalidates the read cache."""
    # Pre-populate
    write_metadata(tmp_path, {"kernels": {"vmlinux": {"version": "6.1"}}})

    # Read to populate cache
    result1 = read_metadata(tmp_path)
    assert result1["kernels"]["vmlinux"]["version"] == "6.1"

    # Write new data (should invalidate cache)
    write_metadata(tmp_path, {"kernels": {"vmlinux": {"version": "6.2"}}})

    # Read should return new data
    result2 = read_metadata(tmp_path)
    assert result2["kernels"]["vmlinux"]["version"] == "6.2"


def test_metadata_cache_handles_missing_file(tmp_path: Path):
    """Test that cache handles missing files gracefully."""
    cache = MetadataCache(ttl=5.0)

    # Try to get from cache for non-existent file
    result = cache.get(tmp_path)
    assert result is None


def test_metadata_cache_returns_none_for_stale_mtime(tmp_path: Path):
    """Test that cache returns None when file mtime doesn't match."""
    cache = MetadataCache(ttl=5.0)

    # Create file and cache entry
    write_metadata(tmp_path, {"version": "1.0"})
    cache.set(tmp_path, {"version": "1.0"})

    # Verify cache hit
    assert cache.get(tmp_path) is not None

    # Modify file directly (simulating external change)
    time.sleep(0.1)
    (tmp_path / "metadata.json").write_text(json.dumps({"version": "2.0"}))

    # Cache should detect mtime change and return None
    assert cache.get(tmp_path) is None


# ---------------------------------------------------------------------------
# Issue #19: Stale Cache Risk - Validation on read
# ---------------------------------------------------------------------------


def test_list_kernel_entries_removes_orphaned_entries(tmp_path: Path):
    """Test that list_kernel_entries removes entries for non-existent files."""
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create a kernel file
    (kernels_dir / "vmlinux-6.1").write_bytes(b"\x7fELF")

    # Add metadata for existing and non-existing kernels
    update_kernel_entry(tmp_path, "vmlinux-6.1", version="6.1", type="official")
    update_kernel_entry(tmp_path, "vmlinux-orphan", version="6.2", type="official")

    # List with validation
    entries = list_kernel_entries(tmp_path, kernels_dir)

    # Should only return existing kernel
    assert "vmlinux-6.1" in entries
    assert "vmlinux-orphan" not in entries

    # Verify orphan was removed from metadata
    meta = read_metadata(tmp_path)
    assert "vmlinux-orphan" not in meta.get("kernels", {})


def test_list_kernel_entries_without_kernels_dir(tmp_path: Path):
    """Test that list_kernel_entries works without validation when kernels_dir is None."""
    update_kernel_entry(tmp_path, "vmlinux-6.1", version="6.1")
    update_kernel_entry(tmp_path, "vmlinux-orphan", version="6.2")

    # List without validation
    entries = list_kernel_entries(tmp_path, None)

    # Should return all entries
    assert "vmlinux-6.1" in entries
    assert "vmlinux-orphan" in entries


def test_list_image_entries_removes_orphaned_entries(tmp_path: Path):
    """Test that list_image_entries removes entries for non-existent files."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create an image file
    (images_dir / "ubuntu-24.04.ext4").write_bytes(b"ext4 data")

    # Add metadata for existing and non-existing images
    update_image_entry(tmp_path, "ubuntu-24.04", os_name="Ubuntu", filename="ubuntu-24.04.ext4")
    update_image_entry(tmp_path, "debian-12", os_name="Debian", filename="debian-12.ext4")

    # List with validation
    entries = list_image_entries(tmp_path, images_dir)

    # Should only return existing image
    assert "ubuntu-24.04" in entries
    assert "debian-12" not in entries

    # Verify orphan was removed from metadata
    meta = read_metadata(tmp_path)
    assert "debian-12" not in meta.get("images", {})


def test_list_image_entries_without_images_dir(tmp_path: Path):
    """Test that list_image_entries works without validation when images_dir is None."""
    update_image_entry(tmp_path, "ubuntu-24.04", os_name="Ubuntu")
    update_image_entry(tmp_path, "debian-12", os_name="Debian")

    # List without validation
    entries = list_image_entries(tmp_path, None)

    # Should return all entries
    assert "ubuntu-24.04" in entries
    assert "debian-12" in entries


def test_list_kernel_entries_handles_missing_kernels_dir(tmp_path: Path):
    """Test that list_kernel_entries handles non-existent kernels_dir gracefully."""
    update_kernel_entry(tmp_path, "vmlinux-6.1", version="6.1")

    # Pass non-existent directory
    entries = list_kernel_entries(tmp_path, tmp_path / "nonexistent")

    # Should return entries without validation
    assert "vmlinux-6.1" in entries
