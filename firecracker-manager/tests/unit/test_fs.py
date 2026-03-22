"""Tests for filesystem path helpers."""

import os
from pathlib import Path

from fcm.utils.fs import (
    get_cache_dir,
    get_vm_dir,
    get_images_dir,
    get_kernels_dir,
    get_state_file,
    get_assets_dir,
    get_vms_dir,
)


def test_get_cache_dir_default():
    original = os.environ.pop("FCM_CACHE_DIR", None)
    try:
        result = get_cache_dir()
        assert result == Path.home() / ".cache" / "firecracker-manager"
    finally:
        if original is not None:
            os.environ["FCM_CACHE_DIR"] = original


def test_get_cache_dir_override(tmp_path: Path):
    os.environ["FCM_CACHE_DIR"] = str(tmp_path)
    try:
        result = get_cache_dir()
        assert result == tmp_path
    finally:
        del os.environ["FCM_CACHE_DIR"]


def test_subdirs_are_under_cache(tmp_path: Path):
    os.environ["FCM_CACHE_DIR"] = str(tmp_path)
    try:
        assert get_vms_dir() == tmp_path / "vms"
        assert get_images_dir() == tmp_path / "images"
        assert get_kernels_dir() == tmp_path / "kernels"
        assert get_state_file() == tmp_path / "vms" / "state.json"
        assert get_vm_dir("vm1") == tmp_path / "vms" / "vm1"
    finally:
        del os.environ["FCM_CACHE_DIR"]


def test_get_assets_dir_points_to_package():
    result = get_assets_dir()
    assert result.is_dir()
    assert (result / "defaults.yaml").exists()
    assert (result / "images.yaml").exists()
