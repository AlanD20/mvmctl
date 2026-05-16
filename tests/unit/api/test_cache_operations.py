"""Tests for CacheOperation class — cache management orchestration."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

from mvmctl.api.cache_operations import CacheOperation
from mvmctl.models import CleanResult, PruneAllResult, VMStatus
from mvmctl.models.result import OperationResult


class TestCacheInitAll:
    """Tests for CacheOperation.init_all()."""

    def test_creates_directories_and_builds_appliance(self, mocker):
        """init_all() creates cache directories and builds guestfs appliance."""
        # Enable guestfs so appliance build runs
        from mvmctl.core.config._service import SettingsService

        mocker.patch.object(SettingsService, "resolve", return_value=True)
        mock_cache_utils = mocker.patch(
            "mvmctl.api.cache_operations.CacheUtils"
        )
        mock_cache_utils.get_cache_dir.return_value = Path("/fake/cache")
        mock_cache_utils.get_vms_dir.return_value = Path("/fake/cache/vms")
        mock_cache_utils.get_images_dir.return_value = Path(
            "/fake/cache/images"
        )
        mock_cache_utils.get_kernels_dir.return_value = Path(
            "/fake/cache/kernels"
        )
        mock_cache_utils.get_bin_dir.return_value = Path("/fake/cache/bin")
        mock_cache_utils.get_logs_dir.return_value = Path("/fake/cache/logs")
        mock_cache_utils.get_keys_dir.return_value = Path("/fake/cache/keys")

        mock_guestfs = mocker.patch(
            "mvmctl.api.cache_operations.GuestfsService"
        )
        mock_guestfs.build_appliance.return_value = Path("/fake/appliance")

        # KernelDetector is imported inside method body
        mock_kernel_detector = mocker.patch(
            "mvmctl.core._shared._guestfs.KernelDetector",
        )
        mock_kernel_detector.find_best_kernel.return_value = (Path("/vmlinuz"),)

        result = CacheOperation.init_all()

        assert "cache_dir" in result.item
        assert "directories" in result.item
        assert "guestfs_appliance" in result.item
        assert "guestfs_kernel" in result.item
        assert len(result.item["directories"]) == 6
        mock_guestfs.build_appliance.assert_called_once()

    def test_calls_on_progress(self, mocker):
        """init_all() invokes on_progress callback for appliance phase."""
        from mvmctl.core.config._service import SettingsService

        mocker.patch.object(SettingsService, "resolve", return_value=True)
        mocker.patch("mvmctl.api.cache_operations.CacheUtils")
        mock_guestfs = mocker.patch(
            "mvmctl.api.cache_operations.GuestfsService"
        )
        mock_guestfs.build_appliance.return_value = Path("/fake/appliance")
        mock_kd = mocker.patch(
            "mvmctl.core._shared._guestfs.KernelDetector",
        )
        mock_kd.find_best_kernel.return_value = (Path("/vmlinuz"),)

        on_progress = MagicMock()
        CacheOperation.init_all(on_progress=on_progress)

        assert on_progress.call_count == 1

    def test_guestfs_build_fails_gracefully(self, mocker):
        """init_all() handles guestfs build failure."""
        from mvmctl.core.config._service import SettingsService

        mocker.patch.object(SettingsService, "resolve", return_value=True)
        mocker.patch("mvmctl.api.cache_operations.CacheUtils")
        mock_guestfs = mocker.patch(
            "mvmctl.api.cache_operations.GuestfsService"
        )
        mock_guestfs.build_appliance.return_value = None
        mock_kd = mocker.patch(
            "mvmctl.core._shared._guestfs.KernelDetector",
        )
        mock_kd.find_best_kernel.return_value = (Path("/vmlinuz"),)

        result = CacheOperation.init_all()
        assert result.item["guestfs_appliance"] is None


class TestCachePruneVMs:
    """Tests for CacheOperation.prune_vms() — now delegates to VMOperation.prune()."""

    def test_prune_skips_running_vms(self, mocker):
        """prune_vms() delegates to VMOperation.prune which skips RUNNING/STARTING VMs by default."""
        mock_prune = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation.prune",
            return_value=OperationResult(
                status="success",
                code="cache.pruned",
                item=["stopped-vm"],
            ),
        )

        result = CacheOperation.prune_vms()

        assert result.item == ["stopped-vm"]
        mock_prune.assert_called_once_with(dry_run=False, include_all=False)

    def test_prune_dry_run(self, mocker):
        """prune_vms() delegates to VMOperation.prune with dry_run=True."""
        mock_prune = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation.prune",
            return_value=OperationResult(
                status="success",
                code="cache.pruned",
                item=["stopped-vm"],
            ),
        )

        result = CacheOperation.prune_vms(dry_run=True)

        assert result.item == ["stopped-vm"]
        mock_prune.assert_called_once_with(dry_run=True, include_all=False)

    def test_prune_include_all(self, mocker):
        """prune_vms() delegates to VMOperation.prune with include_all=True."""
        mock_prune = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation.prune",
            return_value=OperationResult(
                status="success",
                code="cache.pruned",
                item=["running-vm"],
            ),
        )

        result = CacheOperation.prune_vms(include_all=True)

        assert result.item == ["running-vm"]
        mock_prune.assert_called_once_with(dry_run=False, include_all=True)

    def test_prune_handles_remove_failure(self, mocker):
        """prune_vms() delegates to VMOperation.prune which handles removal failures."""
        mock_prune = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation.prune",
            return_value=OperationResult(
                status="success",
                code="cache.pruned",
                item=[],
            ),
        )

        result = CacheOperation.prune_vms()

        assert result.item == []
        mock_prune.assert_called_once_with(dry_run=False, include_all=False)


class TestCachePruneNetworks:
    """Tests for CacheOperation.prune_networks() — now delegates to NetworkOperation.prune()."""

    def test_prune_networks_skips_default_and_referenced(self, mocker):
        """prune_networks() delegates to NetworkOperation.prune which skips default/referenced."""
        mock_prune = mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.prune",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["unused-net"]
            ),
        )

        result = CacheOperation.prune_networks()
        assert result.item == ["unused-net"]
        mock_prune.assert_called_once_with(dry_run=False, include_all=False)

    def test_prune_networks_dry_run(self, mocker):
        """prune_networks() delegates to NetworkOperation.prune with dry_run=True."""
        mock_prune = mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.prune",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["unused-net"]
            ),
        )

        result = CacheOperation.prune_networks(dry_run=True)
        assert result.item == ["unused-net"]
        mock_prune.assert_called_once_with(dry_run=True, include_all=False)


class TestCachePruneImages:
    """Tests for CacheOperation.prune_images() — now delegates to ImageOperation.prune()."""

    def test_prune_images_skips_default_and_referenced(self, mocker):
        """prune_images() delegates to ImageOperation.prune which skips default/referenced."""
        mock_prune = mocker.patch(
            "mvmctl.api.image_operations.ImageOperation.prune",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["img-unused"]
            ),
        )

        result = CacheOperation.prune_images()
        assert result.item == ["img-unused"]
        mock_prune.assert_called_once_with(dry_run=False, include_all=False)

    def test_prune_images_dry_run(self, mocker):
        """prune_images() delegates to ImageOperation.prune with dry_run=True."""
        mock_prune = mocker.patch(
            "mvmctl.api.image_operations.ImageOperation.prune",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["img-unused"]
            ),
        )

        result = CacheOperation.prune_images(dry_run=True)
        assert result.item == ["img-unused"]
        mock_prune.assert_called_once_with(dry_run=True, include_all=False)


class TestCachePruneKernels:
    """Tests for CacheOperation.prune_kernels() — now delegates to KernelOperation.prune()."""

    def test_prune_kernels_skips_default_and_referenced(self, mocker):
        """prune_kernels() delegates to KernelOperation.prune which skips default/referenced."""
        mock_prune = mocker.patch(
            "mvmctl.api.kernel_operations.KernelOperation.prune",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["kern-unused"]
            ),
        )

        result = CacheOperation.prune_kernels()
        assert result.item == ["kern-unused"]
        mock_prune.assert_called_once_with(dry_run=False, include_all=False)


class TestCachePruneBinaries:
    """Tests for CacheOperation.prune_binaries() — now delegates to BinaryOperation.prune()."""

    def test_prune_binaries_skips_default(self, mocker):
        """prune_binaries() delegates to BinaryOperation.prune which skips default version."""
        mock_prune = mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.prune",
            return_value=OperationResult(
                status="success",
                code="cache.pruned",
                item=["firecracker:1.14.0"],
            ),
        )

        result = CacheOperation.prune_binaries()
        assert result.item == ["firecracker:1.14.0"]
        mock_prune.assert_called_once_with(dry_run=False, include_all=False)


class TestCachePruneMisc:
    """Tests for CacheOperation.prune_misc()."""

    def test_prune_misc_returns_dict(self, mocker):
        """prune_misc() returns dict with appliance, warm_images, guestfs_state,
        stale_provision_mounts."""
        mocker.patch(
            "mvmctl.api.cache_operations.GuestfsService.prune_appliance",
            return_value=True,
        )
        mocker.patch(
            "mvmctl.api.cache_operations.CacheService.prune_warm_images",
            return_value=True,
        )
        mocker.patch(
            "mvmctl.api.cache_operations.GuestfsService.clean_stale_guestfs_state",
            return_value=True,
        )
        mocker.patch(
            "mvmctl.api.cache_operations.CacheService.clean_stale_provision_mounts",
            return_value=True,
        )

        result = CacheOperation.prune_misc()
        assert result.item == {
            "service_binaries": True,
            "appliance": True,
            "warm_images": True,
            "guestfs_state": True,
            "stale_provision_mounts": True,
        }


class TestCachePruneAll:
    """Tests for CacheOperation.prune_all()."""

    def test_prune_all_aggregates_results(self, mocker):
        """prune_all() aggregates results from all sub-prunes."""
        mocker.patch.object(
            CacheOperation,
            "prune_vms",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["vm1", "vm2"]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_networks",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["net1"]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_images",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["img1"]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_kernels",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["kern1"]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_binaries",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=["bin1"]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_misc",
            return_value=OperationResult(
                status="success",
                code="cache.pruned",
                item={
                    "appliance": True,
                    "warm_images": False,
                    "guestfs_state": True,
                },
            ),
        )

        mock_vm_repo = mocker.MagicMock()
        mock_vm_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_vm_repo,
        )

        result = CacheOperation.prune_all()

        assert isinstance(result, OperationResult)
        assert result.item.had_running_vms is False
        assert "vm1" in result.item.pruned_ids
        assert "vm2" in result.item.pruned_ids
        assert "net1" in result.item.pruned_ids
        assert "img1" in result.item.pruned_ids
        assert "kern1" in result.item.pruned_ids
        assert "bin1" in result.item.pruned_ids
        assert "appliance" in result.item.pruned_ids
        assert "guestfs_state" in result.item.pruned_ids

    def test_prune_all_detects_running_vms(self, mocker):
        """prune_all() detects running VMs during the operation."""
        mock_vm = MagicMock()
        mock_vm.status = VMStatus.RUNNING

        mock_vm_repo = mocker.MagicMock()
        mock_vm_repo.list_all.return_value = [mock_vm]
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_vm_repo,
        )

        mocker.patch.object(
            CacheOperation,
            "prune_vms",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=[]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_networks",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=[]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_images",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=[]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_kernels",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=[]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_binaries",
            return_value=OperationResult(
                status="success", code="cache.pruned", item=[]
            ),
        )
        mocker.patch.object(
            CacheOperation,
            "prune_misc",
            return_value=OperationResult(
                status="success",
                code="cache.pruned",
                item={
                    "appliance": False,
                    "warm_images": False,
                    "guestfs_state": False,
                },
            ),
        )

        result = CacheOperation.prune_all()
        assert result.item.had_running_vms is True


class TestCacheClean:
    """Tests for CacheOperation.clean()."""

    def test_clean_removes_cache_dir(self, mocker):
        """clean() removes the cache directory."""
        mocker.patch.object(
            CacheOperation,
            "prune_all",
            return_value=OperationResult(
                status="success",
                code="cache.pruned",
                item=PruneAllResult(
                    pruned_ids=[], failed_ids=[], had_running_vms=False
                ),
            ),
        )
        mock_host_op = mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean"
        )
        mock_rmtree = mocker.patch("shutil.rmtree")

        mock_cache_dir = mocker.MagicMock()
        mock_cache_dir.exists.return_value = True
        mocker.patch(
            "mvmctl.api.cache_operations.CacheUtils.get_cache_dir",
            return_value=mock_cache_dir,
        )

        result = CacheOperation.clean()

        assert isinstance(result, OperationResult)
        assert isinstance(result.item, CleanResult)
        assert result.item.cache_dir_removed is True
        mock_host_op.assert_called_once()
        mock_rmtree.assert_called_once()
