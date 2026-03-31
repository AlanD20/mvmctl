"""Unit tests for cache_manager module.

Tests for modular init and prune functions for all cache resources.
"""

from pathlib import Path
from unittest.mock import MagicMock

import pytest
from pytest_mock import MockerFixture

from mvmctl.core.cache_manager import (
    cache_init_all,
    cache_init_images,
    cache_init_kernels,
    cache_init_networks,
    cache_init_vms,
    cache_prune_all,
    cache_prune_images,
    cache_prune_kernels,
    cache_prune_networks,
    cache_prune_vms,
)
from mvmctl.models.vm import VMInstance

# =============================================================================
# Init Tests
# =============================================================================


class TestCacheInitVms:
    """Tests for cache_init_vms function."""

    def test_cache_init_vms_creates_directory(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ):
        """Test VM directory creation."""
        cache_dir = tmp_path / "cache"
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        result = cache_init_vms()

        assert result.exists()
        assert result.is_dir()
        assert result.name == "vms"
        # Check state.json was created
        state_file = result / "state.json"
        assert state_file.exists()
        assert '"vms": {}' in state_file.read_text()


class TestCacheInitImages:
    """Tests for cache_init_images function."""

    def test_cache_init_images_creates_directory(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ):
        """Test images directory creation."""
        cache_dir = tmp_path / "cache"
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        result = cache_init_images()

        assert result.exists()
        assert result.is_dir()
        assert result.name == "images"


class TestCacheInitKernels:
    """Tests for cache_init_kernels function."""

    def test_cache_init_kernels_creates_directory(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ):
        """Test kernels directory creation."""
        cache_dir = tmp_path / "cache"
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        result = cache_init_kernels()

        assert result.exists()
        assert result.is_dir()
        assert result.name == "kernels"


class TestCacheInitNetworks:
    """Tests for cache_init_networks function."""

    def test_cache_init_networks_creates_directory(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ):
        """Test networks directory creation."""
        cache_dir = tmp_path / "cache"
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        result = cache_init_networks()

        assert result.exists()
        assert result.is_dir()
        assert result.name == "networks"


class TestCacheInitAll:
    """Tests for cache_init_all function."""

    def test_cache_init_all_calls_all_inits(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
        """Test orchestration of all init functions."""
        cache_dir = tmp_path / "cache"
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        result = cache_init_all()

        assert "vms" in result
        assert "images" in result
        assert "kernels" in result
        assert "networks" in result
        # guestfs is not included (removed)
        assert "guestfs" not in result

        # Verify directories were created
        assert result["vms"].exists()
        assert result["images"].exists()
        assert result["kernels"].exists()
        assert result["networks"].exists()


# =============================================================================
# Prune Tests - VMs
# =============================================================================


class TestCachePruneVms:
    """Tests for cache_prune_vms function."""

    def test_cache_prune_vms_default_only_error(
        self,
        mocker: MockerFixture,
        error_vm: VMInstance,
        stopped_vm: VMInstance,
        running_vm: VMInstance,
    ):
        """By default, only ERROR state VMs are pruned."""
        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = [error_vm, stopped_vm, running_vm]
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.remove_vm")

        removed = cache_prune_vms(include_stopped=False, include_running=False)

        assert len(removed) == 1
        assert error_vm.name in removed
        assert stopped_vm.name not in removed
        assert running_vm.name not in removed

    def test_cache_prune_vms_include_stopped(
        self,
        mocker: MockerFixture,
        error_vm: VMInstance,
        stopped_vm: VMInstance,
        running_vm: VMInstance,
    ):
        """Test --include-stopped flag."""
        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = [error_vm, stopped_vm, running_vm]
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.remove_vm")

        removed = cache_prune_vms(include_stopped=True, include_running=False)

        assert len(removed) == 2
        assert error_vm.name in removed
        assert stopped_vm.name in removed
        assert running_vm.name not in removed

    def test_cache_prune_vms_include_running(
        self,
        mocker: MockerFixture,
        error_vm: VMInstance,
        stopped_vm: VMInstance,
        running_vm: VMInstance,
    ):
        """Test --include-running flag (dangerous operation)."""
        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = [error_vm, stopped_vm, running_vm]
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.remove_vm")

        removed = cache_prune_vms(include_stopped=False, include_running=True)

        assert len(removed) == 2
        assert error_vm.name in removed
        assert stopped_vm.name not in removed
        assert running_vm.name in removed

    def test_cache_prune_vms_skips_running_by_default(
        self,
        mocker: MockerFixture,
        running_vm: VMInstance,
    ):
        """Safety: don't prune running VMs by default."""
        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = [running_vm]
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mock_remove = mocker.patch("mvmctl.core.cache_manager.remove_vm")

        removed = cache_prune_vms(include_stopped=False, include_running=False)

        assert len(removed) == 0
        mock_remove.assert_not_called()

    def test_cache_prune_vms_dry_run(
        self,
        mocker: MockerFixture,
        error_vm: VMInstance,
        stopped_vm: VMInstance,
    ):
        """Test dry-run mode."""
        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = [error_vm, stopped_vm]
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mock_remove = mocker.patch("mvmctl.core.cache_manager.remove_vm")

        removed = cache_prune_vms(include_stopped=True, include_running=False, dry_run=True)

        # Should report what would be removed
        assert len(removed) == 2
        assert error_vm.name in removed
        assert stopped_vm.name in removed
        # But not actually remove
        mock_remove.assert_not_called()


# =============================================================================
# Prune Tests - Networks
# =============================================================================


class TestCachePruneNetworks:
    """Tests for cache_prune_networks function."""

    def test_cache_prune_networks_not_in_use(self, mocker: MockerFixture):
        """Prune unused networks."""
        # Create mock network
        mock_network = MagicMock()
        mock_network.name = "unused-network"

        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = []  # No VMs referencing any network
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.list_networks", return_value=[mock_network])
        mocker.patch("mvmctl.core.cache_manager.get_network_leases", return_value=[])
        mock_remove = mocker.patch("mvmctl.core.cache_manager.remove_network")

        removed = cache_prune_networks()

        assert len(removed) == 1
        assert "unused-network" in removed
        mock_remove.assert_called_once_with("unused-network")

    def test_cache_prune_networks_skips_referenced(
        self, mocker: MockerFixture, sample_vm: VMInstance
    ):
        """Keep networks referenced by VMs."""
        sample_vm.network_name = "used-network"

        mock_network = MagicMock()
        mock_network.name = "used-network"

        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = [sample_vm]
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.list_networks", return_value=[mock_network])
        mock_remove = mocker.patch("mvmctl.core.cache_manager.remove_network")

        removed = cache_prune_networks()

        assert len(removed) == 0
        mock_remove.assert_not_called()

    def test_cache_prune_networks_skips_default(self, mocker: MockerFixture):
        """Never prune default network."""
        mock_network = MagicMock()
        mock_network.name = "default"  # DEFAULT_NETWORK_NAME

        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = []
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.list_networks", return_value=[mock_network])
        mock_remove = mocker.patch("mvmctl.core.cache_manager.remove_network")

        removed = cache_prune_networks()

        assert len(removed) == 0
        mock_remove.assert_not_called()


# =============================================================================
# Prune Tests - Images
# =============================================================================


class TestCachePruneImages:
    """Tests for cache_prune_images function."""

    def test_cache_prune_images_not_in_use(
        self,
        mocker: MockerFixture,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
    ):
        """Prune unused images."""
        cache_dir = tmp_path / "cache"
        images_dir = cache_dir / "images"
        images_dir.mkdir(parents=True)
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        # Create unused image file
        unused_image = images_dir / "unused.ext4"
        unused_image.write_text("fake image")

        # Mock metadata
        full_hash = "a" * 16
        mock_entries = {full_hash: {"filename": "unused.ext4", "is_default": 0}}

        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = []  # No VMs using this image
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.list_image_entries", return_value=mock_entries)
        mocker.patch("mvmctl.core.cache_manager.get_default_image_entry", return_value=None)
        mocker.patch("mvmctl.core.cache_manager.remove_image_entry")

        removed = cache_prune_images()

        assert len(removed) == 1
        assert full_hash in removed
        # File should be removed
        assert not unused_image.exists()

    def test_cache_prune_images_skips_referenced(
        self,
        mocker: MockerFixture,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
        sample_vm: VMInstance,
    ):
        """Keep images referenced by VMs."""
        cache_dir = tmp_path / "cache"
        images_dir = cache_dir / "images"
        images_dir.mkdir(parents=True)
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        # Create image file
        image_path = images_dir / "used.ext4"
        image_path.write_text("fake image")

        # VM using this image
        from mvmctl.models.vm import VMConfig

        sample_vm.config = VMConfig(
            name=sample_vm.name, rootfs_path=str(image_path), kernel_path="/fake/kernel"
        )

        full_hash = "b" * 64
        mock_entries = {full_hash: {"filename": "used.ext4", "is_default": 0}}

        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = [sample_vm]
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.list_image_entries", return_value=mock_entries)
        mocker.patch("mvmctl.core.cache_manager.get_default_image_entry", return_value=None)
        mock_remove_entry = mocker.patch("mvmctl.core.cache_manager.remove_image_entry")

        removed = cache_prune_images()

        assert len(removed) == 0
        # Image file should still exist
        assert image_path.exists()
        mock_remove_entry.assert_not_called()

    def test_cache_prune_images_skips_default(
        self,
        mocker: MockerFixture,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
    ):
        """Never prune default image."""
        cache_dir = tmp_path / "cache"
        images_dir = cache_dir / "images"
        images_dir.mkdir(parents=True)
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        # Create default image file
        default_image = images_dir / "default.ext4"
        default_image.write_text("fake image")

        default_hash = "c" * 64
        mock_entries = {default_hash: {"filename": "default.ext4", "is_default": 1}}

        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = []
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.list_image_entries", return_value=mock_entries)
        # Return the default entry
        mocker.patch(
            "mvmctl.core.cache_manager.get_default_image_entry",
            return_value=(default_hash, mock_entries[default_hash]),
        )
        mock_remove_entry = mocker.patch("mvmctl.core.cache_manager.remove_image_entry")

        removed = cache_prune_images()

        assert len(removed) == 0
        # Default image should still exist
        assert default_image.exists()
        mock_remove_entry.assert_not_called()


# =============================================================================
# Prune Tests - Kernels
# =============================================================================


class TestCachePruneKernels:
    """Tests for cache_prune_kernels function."""

    def test_cache_prune_kernels_not_in_use(
        self,
        mocker: MockerFixture,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
    ):
        """Prune unused kernels."""
        cache_dir = tmp_path / "cache"
        kernels_dir = cache_dir / "kernels"
        kernels_dir.mkdir(parents=True)
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        # Create unused kernel file
        unused_kernel = kernels_dir / "unused-vmlinux"
        unused_kernel.write_text("fake kernel")

        full_hash = "d" * 16
        mock_entries = {full_hash: {"filename": "unused-vmlinux", "is_default": 0}}

        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = []
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.list_kernel_entries", return_value=mock_entries)
        mocker.patch("mvmctl.core.cache_manager.get_default_kernel_entry", return_value=None)
        mocker.patch("mvmctl.core.cache_manager.remove_kernel_entry")

        removed = cache_prune_kernels()

        assert len(removed) == 1
        assert full_hash in removed
        assert not unused_kernel.exists()

    def test_cache_prune_kernels_skips_referenced(
        self,
        mocker: MockerFixture,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
        sample_vm: VMInstance,
    ):
        """Keep kernels referenced by VMs."""
        cache_dir = tmp_path / "cache"
        kernels_dir = cache_dir / "kernels"
        kernels_dir.mkdir(parents=True)
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        # Create kernel file
        kernel_path = kernels_dir / "used-vmlinux"
        kernel_path.write_text("fake kernel")

        # VM using this kernel
        from mvmctl.models.vm import VMConfig

        sample_vm.config = VMConfig(
            name=sample_vm.name, rootfs_path="/fake/image", kernel_path=str(kernel_path)
        )

        full_hash = "e" * 64
        mock_entries = {full_hash: {"filename": "used-vmlinux", "is_default": 0}}

        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = [sample_vm]
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.list_kernel_entries", return_value=mock_entries)
        mocker.patch("mvmctl.core.cache_manager.get_default_kernel_entry", return_value=None)
        mock_remove_entry = mocker.patch("mvmctl.core.cache_manager.remove_kernel_entry")

        removed = cache_prune_kernels()

        assert len(removed) == 0
        assert kernel_path.exists()
        mock_remove_entry.assert_not_called()

    def test_cache_prune_kernels_skips_default(
        self,
        mocker: MockerFixture,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
    ):
        """Never prune default kernel."""
        cache_dir = tmp_path / "cache"
        kernels_dir = cache_dir / "kernels"
        kernels_dir.mkdir(parents=True)
        monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))

        # Create default kernel file
        default_kernel = kernels_dir / "default-vmlinux"
        default_kernel.write_text("fake kernel")

        default_hash = "f" * 64
        mock_entries = {default_hash: {"filename": "default-vmlinux", "is_default": 1}}

        mock_manager = mocker.MagicMock()
        mock_manager.list_all.return_value = []
        mocker.patch("mvmctl.core.cache_manager.get_vm_manager", return_value=mock_manager)
        mocker.patch("mvmctl.core.cache_manager.list_kernel_entries", return_value=mock_entries)
        mocker.patch(
            "mvmctl.core.cache_manager.get_default_kernel_entry",
            return_value=(default_hash, mock_entries[default_hash]),
        )
        mock_remove_entry = mocker.patch("mvmctl.core.cache_manager.remove_kernel_entry")

        removed = cache_prune_kernels()

        assert len(removed) == 0
        assert default_kernel.exists()
        mock_remove_entry.assert_not_called()


# =============================================================================
# Prune Tests - All
# =============================================================================


class TestCachePruneAll:
    """Tests for cache_prune_all function."""

    def test_cache_prune_all_orchestration(self, mocker: MockerFixture):
        """Prune all resources."""
        mock_prune_vms = mocker.patch(
            "mvmctl.core.cache_manager.cache_prune_vms", return_value=["vm1"]
        )
        mock_prune_networks = mocker.patch(
            "mvmctl.core.cache_manager.cache_prune_networks", return_value=["net1"]
        )
        mock_prune_images = mocker.patch(
            "mvmctl.core.cache_manager.cache_prune_images", return_value=["img1"]
        )
        mock_prune_kernels = mocker.patch(
            "mvmctl.core.cache_manager.cache_prune_kernels", return_value=["kern1"]
        )

        result = cache_prune_all()

        assert "vms" in result
        assert "networks" in result
        assert "images" in result
        assert "kernels" in result
        # guestfs is not included (removed)
        assert "guestfs" not in result

        assert result["vms"] == ["vm1"]
        assert result["networks"] == ["net1"]
        assert result["images"] == ["img1"]
        assert result["kernels"] == ["kern1"]

        mock_prune_vms.assert_called_once()
        mock_prune_networks.assert_called_once()
        mock_prune_images.assert_called_once()
        mock_prune_kernels.assert_called_once()

    def test_cache_prune_all_respects_flags(self, mocker: MockerFixture):
        """Pass flags to sub-prunes."""
        mock_prune_vms = mocker.patch("mvmctl.core.cache_manager.cache_prune_vms", return_value=[])
        mock_prune_networks = mocker.patch(
            "mvmctl.core.cache_manager.cache_prune_networks", return_value=[]
        )
        mock_prune_images = mocker.patch(
            "mvmctl.core.cache_manager.cache_prune_images", return_value=[]
        )
        mock_prune_kernels = mocker.patch(
            "mvmctl.core.cache_manager.cache_prune_kernels", return_value=[]
        )

        cache_prune_all(include_stopped=True, include_running=True, dry_run=True)

        # Verify flags passed to VM prune (positional args)
        mock_prune_vms.assert_called_once_with(True, True, True)
        # Verify dry_run passed to other prunes (positional args)
        mock_prune_networks.assert_called_once_with(True)
        mock_prune_images.assert_called_once_with(True)
        mock_prune_kernels.assert_called_once_with(True)
