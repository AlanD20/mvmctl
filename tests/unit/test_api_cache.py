"""Tests for api/cache.py."""

from pathlib import Path
from unittest.mock import patch

from mvmctl.api.cache import (
    init_all,
    prune_all,
    prune_images,
    prune_kernels,
    prune_networks,
    prune_vms,
)


class TestInitAll:
    def test_returns_dict_of_strings(self):
        with patch("mvmctl.api.cache.cache_manager") as mock_cm:
            mock_cm.cache_init_all.return_value = {
                "vms": Path("/tmp/vms"),
                "images": Path("/tmp/images"),
            }
            result = init_all()
            assert result == {"vms": "/tmp/vms", "images": "/tmp/images"}

    def test_empty_value_becomes_empty_string(self):
        with patch("mvmctl.api.cache.cache_manager") as mock_cm:
            mock_cm.cache_init_all.return_value = {"vms": None}
            result = init_all()
            assert result == {"vms": ""}


class TestPruneVms:
    def test_delegates_with_privilege_check(self):
        with patch("mvmctl.api.cache.check_privileges_interactive") as mock_chk:
            with patch("mvmctl.api.cache.cache_manager") as mock_cm:
                mock_cm.cache_prune_vms.return_value = ["vm1"]
                result = prune_vms(include_stopped=True, dry_run=True)
                assert result == ["vm1"]
                mock_chk.assert_called_once()
                mock_cm.cache_prune_vms.assert_called_once_with(
                    include_stopped=True, include_running=False, dry_run=True
                )


class TestPruneNetworks:
    def test_delegates_with_privilege_check(self):
        with patch("mvmctl.api.cache.check_privileges_interactive") as mock_chk:
            with patch("mvmctl.api.cache.cache_manager") as mock_cm:
                mock_cm.cache_prune_networks.return_value = ["net1"]
                result = prune_networks(dry_run=True)
                assert result == ["net1"]
                mock_chk.assert_called_once()


class TestPruneImages:
    def test_delegates_without_privilege_check(self):
        with patch("mvmctl.api.cache.cache_manager") as mock_cm:
            mock_cm.cache_prune_images.return_value = ["img1"]
            result = prune_images()
            assert result == ["img1"]


class TestPruneKernels:
    def test_delegates_without_privilege_check(self):
        with patch("mvmctl.api.cache.cache_manager") as mock_cm:
            mock_cm.cache_prune_kernels.return_value = ["kern1"]
            result = prune_kernels()
            assert result == ["kern1"]


class TestPruneAll:
    def test_delegates_with_privilege_check(self):
        with patch("mvmctl.api.cache.check_privileges_interactive") as mock_chk:
            with patch("mvmctl.api.cache.cache_manager") as mock_cm:
                mock_cm.cache_prune_all.return_value = {
                    "vms": ["vm1"],
                    "networks": [],
                    "images": [],
                    "kernels": [],
                }
                result = prune_all(dry_run=True)
                assert "vms" in result
                mock_chk.assert_called_once()
