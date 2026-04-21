"""Tests for api/assets module."""

from pathlib import Path
from unittest.mock import MagicMock, patch

from mvmctl.api.assets import (
    AssetInfo,
    fetch_binary,
    list_local_versions,
)
from mvmctl.api.kernel import list_kernels


def test_asset_info_typed_dict():
    info: AssetInfo = {
        "type": "binary",
        "name": "1.5.0",
        "active": True,
        "size_mib": 100.5,
        "details": "/path/to/binary",
    }
    assert info["type"] == "binary"
    assert info["active"] is True


def test_asset_info_with_none_values():
    info: AssetInfo = {
        "type": "kernel",
        "name": "vmlinux",
        "active": None,
        "size_mib": None,
        "details": None,
    }
    assert info["active"] is None
    assert info["size_mib"] is None


class TestFetchBinary:
    def test_fetch_binary_success(self, tmp_path: Path):
        with patch("mvmctl.core.binary_manager.fetch_binary") as mock_fetch:
            with patch("mvmctl.api.assets.MVMDatabase") as mock_db_class:
                mock_db = MagicMock()
                mock_db_class.return_value = mock_db

                result = fetch_binary("1.15.0", bin_dir=tmp_path)

                mock_fetch.assert_called_once()


class TestListLocalVersions:
    def test_list_local_versions(self, tmp_path: Path):
        with patch("mvmctl.api.assets.get_cache_dir") as mock_get_cache:
            mock_get_cache.return_value = tmp_path

            result = list_local_versions(bin_dir=tmp_path)

            assert isinstance(result, list)


class TestListKernels:
    def test_list_kernels(self, tmp_path: Path):
        result = list_kernels(kernels_dir=tmp_path)

        assert isinstance(result, list)
