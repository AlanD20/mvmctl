"""Tests for filesystem path helpers."""

import os
from pathlib import Path

import pytest

from fcm.utils.fs import (
    get_assets_dir,
    get_cache_dir,
    get_images_dir,
    get_kernels_dir,
    get_logs_dir,
    get_state_file,
    get_vm_dir,
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
        assert get_logs_dir() == tmp_path / "logs"
        assert get_state_file() == tmp_path / "vms" / "state.json"
        assert get_vm_dir("vm1") == tmp_path / "vms" / "vm1"
    finally:
        del os.environ["FCM_CACHE_DIR"]


def test_get_logs_dir_is_under_cache(tmp_path: Path):
    os.environ["FCM_CACHE_DIR"] = str(tmp_path)
    try:
        assert get_logs_dir() == tmp_path / "logs"
    finally:
        del os.environ["FCM_CACHE_DIR"]


def test_get_assets_dir_points_to_package():
    result = get_assets_dir()
    assert result.is_dir()
    assert (result / "defaults.yaml").exists()
    assert (result / "images.yaml").exists()


# ---------------------------------------------------------------------------
# S-H4: FCM_CACHE_DIR path validation
# ---------------------------------------------------------------------------


def test_get_cache_dir_rejects_path_outside_home_and_tmp():
    """FCM_CACHE_DIR pointing to /etc should be rejected."""
    from fcm.exceptions import FCMError

    os.environ["FCM_CACHE_DIR"] = "/etc/shadow"
    try:
        with pytest.raises(FCMError, match="Unsafe"):
            get_cache_dir()
    finally:
        del os.environ["FCM_CACHE_DIR"]


def test_get_cache_dir_rejects_traversal_path():
    """FCM_CACHE_DIR with traversal to /etc should be rejected."""
    from fcm.exceptions import FCMError

    os.environ["FCM_CACHE_DIR"] = "/tmp/../../etc"
    try:
        with pytest.raises(FCMError, match="Unsafe"):
            get_cache_dir()
    finally:
        del os.environ["FCM_CACHE_DIR"]


def test_get_cache_dir_accepts_tmp_subdir():
    """FCM_CACHE_DIR under /tmp should be accepted."""
    os.environ["FCM_CACHE_DIR"] = "/tmp/fcm-test-cache"
    try:
        result = get_cache_dir()
        assert result == Path("/tmp/fcm-test-cache")
    finally:
        del os.environ["FCM_CACHE_DIR"]
