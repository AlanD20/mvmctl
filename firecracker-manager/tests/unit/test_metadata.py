"""Tests for the unified metadata.json store."""

import json
from pathlib import Path

import pytest

from fcm.core.metadata import (
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
    monkeypatch.setenv("FCM_CONFIG_DIR", str(tmp_path))
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
