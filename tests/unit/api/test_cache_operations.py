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
    """Tests for CacheOperation.prune_vms()."""

    def test_prune_skips_running_vms(self, mocker):
        """prune_vms() skips RUNNING/STARTING VMs by default."""
        mock_vm_running = MagicMock()
        mock_vm_running.name = "running-vm"
        mock_vm_running.status = VMStatus.RUNNING

        mock_vm_stopped = MagicMock()
        mock_vm_stopped.name = "stopped-vm"
        mock_vm_stopped.status = VMStatus.STOPPED

        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = [mock_vm_running, mock_vm_stopped]
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_repo,
        )

        # VMOperation is imported locally inside prune_vms()
        mock_vm_op = mocker.patch("mvmctl.api.vm_operations.VMOperation.remove")

        result = CacheOperation.prune_vms()
        assert result.item == ["stopped-vm"]
        mock_vm_op.assert_called_once()

    def test_prune_dry_run(self, mocker):
        """prune_vms() with dry_run doesn't actually remove VMs."""
        mock_vm = MagicMock()
        mock_vm.name = "stopped-vm"
        mock_vm.status = VMStatus.STOPPED

        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = [mock_vm]
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_repo,
        )

        mock_vm_op = mocker.patch("mvmctl.api.vm_operations.VMOperation.remove")

        result = CacheOperation.prune_vms(dry_run=True)
        assert result.item == ["stopped-vm"]
        mock_vm_op.assert_not_called()

    def test_prune_include_all(self, mocker):
        """prune_vms() with include_all prunes RUNNING VMs too."""
        mock_vm = MagicMock()
        mock_vm.name = "running-vm"
        mock_vm.status = VMStatus.RUNNING

        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = [mock_vm]
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_repo,
        )

        mock_vm_op = mocker.patch("mvmctl.api.vm_operations.VMOperation.remove")

        result = CacheOperation.prune_vms(include_all=True)
        assert result.item == ["running-vm"]
        mock_vm_op.assert_called_once()

    def test_prune_handles_remove_failure(self, mocker):
        """prune_vms() logs warning when VM removal fails."""
        mock_vm = MagicMock()
        mock_vm.name = "failing-vm"
        mock_vm.status = VMStatus.STOPPED

        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = [mock_vm]
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_repo,
        )

        mocker.patch(
            "mvmctl.api.vm_operations.VMOperation.remove",
            side_effect=RuntimeError("Failed"),
        )
        mock_logger = mocker.patch("mvmctl.api.cache_operations.logger")

        result = CacheOperation.prune_vms()
        assert result.item == []
        mock_logger.warning.assert_called()


class TestCachePruneNetworks:
    """Tests for CacheOperation.prune_networks()."""

    def test_prune_networks_skips_default_and_referenced(self, mocker):
        """prune_networks() skips default and referenced networks."""
        mock_default_net = MagicMock()
        mock_default_net.name = "default"
        mock_default_net.id = "net-default"

        mock_unused_net = MagicMock()
        mock_unused_net.name = "unused-net"
        mock_unused_net.id = "net-unused"

        mock_net_repo = mocker.MagicMock()
        mock_net_repo.list_all.return_value = [
            mock_default_net,
            mock_unused_net,
        ]
        mocker.patch(
            "mvmctl.api.cache_operations.NetworkRepository",
            return_value=mock_net_repo,
        )

        mock_vm_repo = mocker.MagicMock()
        mock_vm_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_vm_repo,
        )

        mock_lease_repo = mocker.MagicMock()
        mock_lease_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.cache_operations.LeaseRepository",
            return_value=mock_lease_repo,
        )

        mocker.patch(
            "mvmctl.api.cache_operations.SettingsService.resolve",
            return_value="default",
        )

        mock_net_op = mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.remove"
        )
        mock_net_op.return_value = OperationResult(status="success", code="network.removed")

        result = CacheOperation.prune_networks()
        assert result.item == ["unused-net"]
        mock_net_op.assert_called_once()

    def test_prune_networks_dry_run(self, mocker):
        """prune_networks() with dry_run doesn't actually remove networks."""
        mock_unused_net = MagicMock()
        mock_unused_net.name = "unused-net"
        mock_unused_net.id = "net-unused"

        mock_net_repo = mocker.MagicMock()
        mock_net_repo.list_all.return_value = [mock_unused_net]
        mocker.patch(
            "mvmctl.api.cache_operations.NetworkRepository",
            return_value=mock_net_repo,
        )
        mock_vm_repo = mocker.MagicMock()
        mock_vm_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_vm_repo,
        )
        mock_lease_repo = mocker.MagicMock()
        mock_lease_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.cache_operations.LeaseRepository",
            return_value=mock_lease_repo,
        )
        mocker.patch(
            "mvmctl.api.cache_operations.SettingsService.resolve",
            return_value="default",
        )

        mock_net_op = mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.remove"
        )
        mock_net_op.return_value = OperationResult(status="success", code="network.removed")

        result = CacheOperation.prune_networks(dry_run=True)
        assert result.item == ["unused-net"]
        mock_net_op.assert_not_called()


class TestCachePruneImages:
    """Tests for CacheOperation.prune_images()."""

    def test_prune_images_skips_default_and_referenced(self, mocker):
        """prune_images() skips default and referenced images."""
        mock_default = MagicMock()
        mock_default.id = "img-default"
        mock_referenced = MagicMock()
        mock_referenced.id = "img-referenced"
        mock_unused = MagicMock()
        mock_unused.id = "img-unused"

        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = [
            mock_default,
            mock_referenced,
            mock_unused,
        ]
        mock_repo.get_default.return_value = mock_default
        mocker.patch(
            "mvmctl.api.cache_operations.ImageRepository",
            return_value=mock_repo,
        )

        mock_vm_repo = mocker.MagicMock()
        mock_vm_with_ref = MagicMock()
        mock_vm_with_ref.image_id = "img-referenced"
        mock_vm_repo.list_all.return_value = [mock_vm_with_ref]
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_vm_repo,
        )

        mock_image_op = mocker.patch(
            "mvmctl.api.image_operations.ImageOperation.remove"
        )

        result = CacheOperation.prune_images()
        assert result.item == ["img-unused"]
        mock_image_op.assert_called_once()

    def test_prune_images_dry_run(self, mocker):
        """prune_images() with dry_run doesn't remove images."""
        mock_unused = MagicMock()
        mock_unused.id = "img-unused"

        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = [mock_unused]
        mock_repo.get_default.return_value = None
        mocker.patch(
            "mvmctl.api.cache_operations.ImageRepository",
            return_value=mock_repo,
        )
        mock_vm_repo = mocker.MagicMock()
        mock_vm_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_vm_repo,
        )

        mock_image_op = mocker.patch(
            "mvmctl.api.image_operations.ImageOperation.remove"
        )

        result = CacheOperation.prune_images(dry_run=True)
        assert result.item == ["img-unused"]
        mock_image_op.assert_not_called()


class TestCachePruneKernels:
    """Tests for CacheOperation.prune_kernels()."""

    def test_prune_kernels_skips_default_and_referenced(self, mocker):
        """prune_kernels() skips default and referenced kernels."""
        mock_default = MagicMock()
        mock_default.id = "kern-default"
        mock_unused = MagicMock()
        mock_unused.id = "kern-unused"

        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = [mock_default, mock_unused]
        mock_repo.get_default.return_value = mock_default
        mocker.patch(
            "mvmctl.api.cache_operations.KernelRepository",
            return_value=mock_repo,
        )

        mock_vm_repo = mocker.MagicMock()
        mock_vm_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.cache_operations.VMRepository",
            return_value=mock_vm_repo,
        )

        mock_kernel_op = mocker.patch(
            "mvmctl.api.kernel_operations.KernelOperation.remove"
        )

        result = CacheOperation.prune_kernels()
        assert result.item == ["kern-unused"]
        mock_kernel_op.assert_called_once()


class TestCachePruneBinaries:
    """Tests for CacheOperation.prune_binaries()."""

    def test_prune_binaries_skips_default(self, mocker):
        """prune_binaries() skips default binary version."""
        mock_default = MagicMock()
        mock_default.name = "firecracker"
        mock_default.version = "1.15.0"
        mock_other = MagicMock()
        mock_other.name = "firecracker"
        mock_other.version = "1.14.0"

        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = [mock_default, mock_other]
        mock_repo.get_default.return_value = mock_default
        mocker.patch(
            "mvmctl.api.cache_operations.BinaryRepository",
            return_value=mock_repo,
        )

        mock_bin_op = mocker.patch(
            "mvmctl.api.binary_operations.BinaryOperation.remove"
        )

        result = CacheOperation.prune_binaries()
        assert result.item == ["firecracker:1.14.0"]
        mock_bin_op.assert_called_once()


class TestCachePruneMisc:
    """Tests for CacheOperation.prune_misc()."""

    def test_prune_misc_returns_dict(self, mocker):
        """prune_misc() returns dict with appliance, warm_images, guestfs_state."""
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

        result = CacheOperation.prune_misc()
        assert result.item == {
            "appliance": True,
            "warm_images": True,
            "guestfs_state": True,
        }


class TestCachePruneAll:
    """Tests for CacheOperation.prune_all()."""

    def test_prune_all_aggregates_results(self, mocker):
        """prune_all() aggregates results from all sub-prunes."""
        mocker.patch.object(
            CacheOperation, "prune_vms", return_value=OperationResult(status="success", code="cache.pruned", item=["vm1", "vm2"])
        )
        mocker.patch.object(
            CacheOperation, "prune_networks", return_value=OperationResult(status="success", code="cache.pruned", item=["net1"])
        )
        mocker.patch.object(
            CacheOperation, "prune_images", return_value=OperationResult(status="success", code="cache.pruned", item=["img1"])
        )
        mocker.patch.object(
            CacheOperation, "prune_kernels", return_value=OperationResult(status="success", code="cache.pruned", item=["kern1"])
        )
        mocker.patch.object(
            CacheOperation, "prune_binaries", return_value=OperationResult(status="success", code="cache.pruned", item=["bin1"])
        )
        mocker.patch.object(
            CacheOperation,
            "prune_misc",
            return_value=OperationResult(status="success", code="cache.pruned", item={
                "appliance": True,
                "warm_images": False,
                "guestfs_state": True,
            }),
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

        mocker.patch.object(CacheOperation, "prune_vms", return_value=OperationResult(status="success", code="cache.pruned", item=[]))
        mocker.patch.object(CacheOperation, "prune_networks", return_value=OperationResult(status="success", code="cache.pruned", item=[]))
        mocker.patch.object(CacheOperation, "prune_images", return_value=OperationResult(status="success", code="cache.pruned", item=[]))
        mocker.patch.object(CacheOperation, "prune_kernels", return_value=OperationResult(status="success", code="cache.pruned", item=[]))
        mocker.patch.object(CacheOperation, "prune_binaries", return_value=OperationResult(status="success", code="cache.pruned", item=[]))
        mocker.patch.object(
            CacheOperation,
            "prune_misc",
            return_value=OperationResult(status="success", code="cache.pruned", item={
                "appliance": False,
                "warm_images": False,
                "guestfs_state": False,
            }),
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
