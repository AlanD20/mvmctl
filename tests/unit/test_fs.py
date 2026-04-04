"""Tests for filesystem path helpers."""

import os
from pathlib import Path

import pytest

from mvmctl.utils.fs import (
    get_assets_dir,
    get_cache_dir,
    get_images_dir,
    get_kernels_dir,
    get_logs_dir,
    get_mvm_db_path,
    get_state_file,
    get_vm_dir,
    get_vms_dir,
)


def test_get_cache_dir_default():
    original = os.environ.pop("MVM_CACHE_DIR", None)
    try:
        result = get_cache_dir()
        assert result == Path.home() / ".cache" / "mvmctl"
    finally:
        if original is not None:
            os.environ["MVM_CACHE_DIR"] = original


def test_get_cache_dir_override(tmp_path: Path):
    os.environ["MVM_CACHE_DIR"] = str(tmp_path)
    try:
        result = get_cache_dir()
        assert result == tmp_path
    finally:
        del os.environ["MVM_CACHE_DIR"]


def test_subdirs_are_under_cache(tmp_path: Path):
    os.environ["MVM_CACHE_DIR"] = str(tmp_path)
    try:
        assert get_vms_dir() == tmp_path / "vms"
        assert get_images_dir() == tmp_path / "images"
        assert get_kernels_dir() == tmp_path / "kernels"
        assert get_logs_dir() == tmp_path / "logs"
        assert get_state_file() == tmp_path / "vms" / "state.json"
        assert get_vm_dir("vm1") == tmp_path / "vms" / "vm1"
    finally:
        del os.environ["MVM_CACHE_DIR"]


def test_get_logs_dir_is_under_cache(tmp_path: Path):
    os.environ["MVM_CACHE_DIR"] = str(tmp_path)
    try:
        assert get_logs_dir() == tmp_path / "logs"
    finally:
        del os.environ["MVM_CACHE_DIR"]


def test_get_assets_dir_points_to_package():
    result = get_assets_dir()
    assert result.is_dir()
    assert (result / "defaults.yaml").exists()
    assert (result / "images.yaml").exists()


# ---------------------------------------------------------------------------
# Tests for get_mvm_db_path()
# ---------------------------------------------------------------------------


def test_get_mvm_db_path_returns_correct_filename(isolate_config_and_cache):
    path = get_mvm_db_path()
    assert path.name == "mvmdb.db"


def test_get_mvm_db_path_is_under_cache_dir(isolate_config_and_cache):
    path = get_mvm_db_path()
    assert path.parent == get_cache_dir()


def test_get_mvm_db_path_respects_mvm_cache_dir(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    path = get_mvm_db_path()
    assert path == tmp_path / "mvmdb.db"


# ---------------------------------------------------------------------------
# S-H4: MVM_CACHE_DIR path validation
# ---------------------------------------------------------------------------


def test_get_cache_dir_rejects_path_outside_home_and_tmp():
    """MVM_CACHE_DIR pointing to /etc should be rejected."""
    from mvmctl.exceptions import MVMError

    os.environ["MVM_CACHE_DIR"] = "/etc/shadow"
    try:
        with pytest.raises(MVMError, match="Unsafe"):
            get_cache_dir()
    finally:
        del os.environ["MVM_CACHE_DIR"]


def test_get_cache_dir_rejects_traversal_path():
    """MVM_CACHE_DIR with traversal to /etc should be rejected."""
    from mvmctl.exceptions import MVMError

    os.environ["MVM_CACHE_DIR"] = "/tmp/../../etc"
    try:
        with pytest.raises(MVMError, match="Unsafe"):
            get_cache_dir()
    finally:
        del os.environ["MVM_CACHE_DIR"]


def test_get_cache_dir_accepts_tmp_subdir():
    """MVM_CACHE_DIR under /tmp should be accepted."""
    os.environ["MVM_CACHE_DIR"] = "/tmp/mvm-test-cache"
    try:
        result = get_cache_dir()
        assert result == Path("/tmp/mvm-test-cache")
    finally:
        del os.environ["MVM_CACHE_DIR"]


def test_get_config_dir_rejects_path_outside_home_and_tmp():
    """MVM_CONFIG_DIR pointing to /etc should be rejected."""
    from mvmctl.exceptions import MVMError
    from mvmctl.utils.fs import get_config_dir

    os.environ["MVM_CONFIG_DIR"] = "/etc/shadow"
    try:
        with pytest.raises(MVMError, match="Unsafe"):
            get_config_dir()
    finally:
        del os.environ["MVM_CONFIG_DIR"]


def test_get_config_dir_accepts_home_subdir(tmp_path):
    """MVM_CONFIG_DIR under home should be accepted."""
    from mvmctl.utils.fs import get_config_dir

    os.environ["MVM_CONFIG_DIR"] = str(tmp_path / "config")
    try:
        result = get_config_dir()
        assert result == (tmp_path / "config").resolve()
    finally:
        del os.environ["MVM_CONFIG_DIR"]


def test_get_real_user_ids_returns_none_when_not_root(monkeypatch):
    """get_real_user_ids should return None when not running as root."""
    from mvmctl.utils.fs import get_real_user_ids

    monkeypatch.setattr(os, "getuid", lambda: 1000)
    assert get_real_user_ids() is None


def test_get_real_user_ids_returns_none_when_no_sudo_user(monkeypatch):
    """get_real_user_ids should return None when SUDO_USER is not set."""
    from mvmctl.utils.fs import get_real_user_ids

    monkeypatch.setattr(os, "getuid", lambda: 0)
    monkeypatch.delenv("SUDO_USER", raising=False)
    assert get_real_user_ids() is None


def test_chown_to_real_user_noop_when_not_root(monkeypatch, tmp_path):
    """chown_to_real_user should be a no-op when not running as root."""
    from mvmctl.utils.fs import chown_to_real_user

    monkeypatch.setattr(os, "getuid", lambda: 1000)
    target = tmp_path / "test"
    target.write_text("test")
    chown_to_real_user(target)


def test_chown_to_real_user_noop_when_path_not_exists(monkeypatch, tmp_path):
    """chown_to_real_user should be a no-op when path doesn't exist."""
    from mvmctl.utils.fs import chown_to_real_user

    monkeypatch.setattr(os, "getuid", lambda: 0)
    monkeypatch.setenv("SUDO_USER", "testuser")
    chown_to_real_user(tmp_path / "nonexistent")


def test_get_real_home_with_sudo_user(monkeypatch):
    """_get_real_home should use SUDO_USER's home when set."""
    from mvmctl.utils.fs import _get_real_home

    monkeypatch.setenv("SUDO_USER", "root")
    result = _get_real_home()
    assert result == Path("/root")


def test_get_real_home_with_invalid_sudo_user(monkeypatch):
    """_get_real_home should fall back to Path.home() for invalid SUDO_USER."""
    from mvmctl.utils.fs import _get_real_home

    monkeypatch.setenv("SUDO_USER", "nonexistent_user_xyz_123")
    result = _get_real_home()
    assert result == Path.home()
