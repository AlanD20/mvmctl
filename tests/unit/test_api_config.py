"""Tests for api/config.py - verifies re-exports are accessible."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.api.config import (
    dump_config,
    get_config_value,
    get_default_binary_entry,
    get_default_image_entry,
    get_default_kernel_entry,
    get_defaults_config,
    get_firecracker_config,
    get_full_user_config,
    initialize_default_config,
    load_config,
    set_config_value,
    set_defaults_value,
    validate_config,
)


class TestReExports:
    def test_dump_config_callable(self):
        assert callable(dump_config)

    def test_load_config_callable(self):
        assert callable(load_config)

    def test_validate_config_callable(self):
        assert callable(validate_config)

    def test_get_config_value_callable(self):
        assert callable(get_config_value)

    def test_set_config_value_callable(self):
        assert callable(set_config_value)

    def test_get_full_user_config_callable(self):
        assert callable(get_full_user_config)

    def test_get_firecracker_config_callable(self):
        assert callable(get_firecracker_config)

    def test_get_defaults_config_callable(self):
        assert callable(get_defaults_config)

    def test_set_defaults_value_callable(self):
        assert callable(set_defaults_value)

    def test_initialize_default_config_callable(self):
        assert callable(initialize_default_config)

    def test_get_default_image_entry_callable(self):
        assert callable(get_default_image_entry)

    def test_get_default_kernel_entry_callable(self):
        assert callable(get_default_kernel_entry)

    def test_get_default_binary_entry_callable(self):
        assert callable(get_default_binary_entry)


class TestGetFirecrackerConfig:
    def test_get_firecracker_config_with_binary(self, tmp_path: Path):
        with patch("mvmctl.api.config.MVMDatabase") as mock_db_class:
            mock_db = MagicMock()
            mock_db.get_default_binary.return_value = MagicMock(
                version="1.15.0", path="/path/to/fc"
            )
            mock_db_class.return_value = mock_db

            with patch("mvmctl.api.config._core_get_firecracker_config") as mock_core:
                mock_core.return_value = {"version": "1.15.0", "path": "/path/to/fc"}
                result = get_firecracker_config()

                assert result["version"] == "1.15.0"
                mock_db.get_default_binary.assert_called_once_with("firecracker")

    def test_get_firecracker_config_no_binary(self, tmp_path: Path):
        with patch("mvmctl.api.config.MVMDatabase") as mock_db_class:
            mock_db = MagicMock()
            mock_db.get_default_binary.return_value = None
            mock_db_class.return_value = mock_db

            with patch("mvmctl.api.config._core_get_firecracker_config") as mock_core:
                mock_core.return_value = {"version": None, "path": None}
                result = get_firecracker_config()

                assert result["version"] is None


class TestGetDefaultsConfig:
    def test_get_defaults_config_with_defaults(self, tmp_path: Path):
        with patch("mvmctl.api.config.MVMDatabase") as mock_db_class:
            mock_db = MagicMock()

            mock_image = MagicMock()
            mock_image.is_default = True
            mock_image.os_slug = "ubuntu-24.04"
            mock_image.id = "img123"

            mock_kernel = MagicMock()
            mock_kernel.is_default = True
            mock_kernel.path = "/path/to/vmlinux"

            mock_db.list_images.return_value = [mock_image]
            mock_db.list_kernels.return_value = [mock_kernel]
            mock_db_class.return_value = mock_db

            with patch("mvmctl.api.config._core_get_defaults_config") as mock_core:
                mock_core.return_value = {"image": "ubuntu-24.04", "kernel": "/path/to/vmlinux"}
                result = get_defaults_config()

                assert result["image"] == "ubuntu-24.04"
                assert result["kernel"] == "/path/to/vmlinux"

    def test_get_defaults_config_no_defaults(self, tmp_path: Path):
        with patch("mvmctl.api.config.MVMDatabase") as mock_db_class:
            mock_db = MagicMock()
            mock_db.list_images.return_value = []
            mock_db.list_kernels.return_value = []
            mock_db_class.return_value = mock_db

            with patch("mvmctl.api.config._core_get_defaults_config") as mock_core:
                mock_core.return_value = {"image": None, "kernel": None}
                result = get_defaults_config()

                assert result["image"] is None
                assert result["kernel"] is None

    def test_get_defaults_config_db_exception(self, tmp_path: Path):
        with patch("mvmctl.api.config.MVMDatabase") as mock_db_class:
            mock_db = MagicMock()
            mock_db.list_images.side_effect = Exception("DB error")
            mock_db.list_kernels.side_effect = Exception("DB error")
            mock_db_class.return_value = mock_db

            with patch("mvmctl.api.config._core_get_defaults_config") as mock_core:
                mock_core.return_value = {"image": None, "kernel": None}
                result = get_defaults_config()

                assert result["image"] is None
                assert result["kernel"] is None


class TestSetDefaultsValue:
    def test_set_defaults_value_image(self, tmp_path: Path):
        with patch("mvmctl.api.config._core_set_defaults_value") as mock_core:
            with patch("mvmctl.api.config.MVMDatabase") as mock_db_class:
                mock_db = MagicMock()
                mock_img = MagicMock()
                mock_img.os_slug = "ubuntu-24.04"
                mock_img.id = "img123"
                mock_db.list_images.return_value = [mock_img]
                mock_db_class.return_value = mock_db

                set_defaults_value("image", "ubuntu-24.04")

                mock_core.assert_called_once_with("image", "ubuntu-24.04")
                mock_db.set_default_image.assert_called_once_with("img123")

    def test_set_defaults_value_kernel(self, tmp_path: Path):
        with patch("mvmctl.api.config._core_set_defaults_value") as mock_core:
            with patch("mvmctl.api.config.MVMDatabase") as mock_db_class:
                mock_db = MagicMock()
                mock_kernel = MagicMock()
                mock_kernel.path = "/path/to/vmlinux"
                mock_kernel.id = "kern123"
                mock_db.list_kernels.return_value = [mock_kernel]
                mock_db_class.return_value = mock_db

                set_defaults_value("kernel", "/path/to/vmlinux")

                mock_core.assert_called_once_with("kernel", "/path/to/vmlinux")
                mock_db.set_default_kernel.assert_called_once_with("kern123")

    def test_set_defaults_value_other_key(self, tmp_path: Path):
        with patch("mvmctl.api.config._core_set_defaults_value") as mock_core:
            with patch("mvmctl.api.config.MVMDatabase") as mock_db_class:
                mock_db = MagicMock()
                mock_db_class.return_value = mock_db

                set_defaults_value("other", "value")

                mock_core.assert_called_once_with("other", "value")
                mock_db.set_default_image.assert_not_called()
                mock_db.set_default_kernel.assert_not_called()

    def test_set_defaults_value_exception(self, tmp_path: Path):
        with patch("mvmctl.api.config._core_set_defaults_value") as mock_core:
            with patch("mvmctl.api.config.MVMDatabase") as mock_db_class:
                mock_db = MagicMock()
                mock_db.list_images.side_effect = Exception("DB error")
                mock_db_class.return_value = mock_db

                set_defaults_value("image", "ubuntu-24.04")

                mock_core.assert_called_once()


class TestLazyMetadataImports:
    def test_get_default_binary_entry(self):
        with patch("mvmctl.api.config._get_metadata_module") as mock_get_meta:
            mock_meta = MagicMock()
            mock_meta.get_default_binary_entry.return_value = ("1.15.0", {})
            mock_get_meta.return_value = mock_meta

            result = get_default_binary_entry()
            assert result == ("1.15.0", {})

    def test_get_default_image_entry(self):
        with patch("mvmctl.api.config._get_metadata_module") as mock_get_meta:
            mock_meta = MagicMock()
            mock_meta.get_default_image_entry.return_value = ("img123", {})
            mock_get_meta.return_value = mock_meta

            result = get_default_image_entry()
            assert result == ("img123", {})

    def test_get_default_kernel_entry(self):
        with patch("mvmctl.api.config._get_metadata_module") as mock_get_meta:
            mock_meta = MagicMock()
            mock_meta.get_default_kernel_entry.return_value = ("kern123", {})
            mock_get_meta.return_value = mock_meta

            result = get_default_kernel_entry()
            assert result == ("kern123", {})
