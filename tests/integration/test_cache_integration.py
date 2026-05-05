"""Integration tests for Cache API operations through the real public API.

Tests exercise cache management workflows:
  init → prune VMs/images/kernels/binaries/networks/misc → prune_all → clean

Only subprocess (system-level operations like cp, dd, ip, iptables, firecracker)
are mocked. ALL orchestration logic in api/ and core/ runs unmocked.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.api import CacheOperation, VMCreateInput, VMInput, VMOperation
from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.exceptions import VMNotFoundError
from mvmctl.models.result import OperationResult
from mvmctl.models import (
    BinaryItem,
    CleanResult,
    ImageItem,
    KernelItem,
    PruneAllResult,
    VMStatus,
)
from mvmctl.utils.common import CacheUtils

# ======================================================================
# Cache init tests
# ======================================================================


class TestCacheInit:
    """Test CacheOperation.init_all directory creation."""

    def test_init_all_creates_directories(self) -> None:
        """init_all creates all expected cache subdirectories."""
        result = CacheOperation.init_all()

        assert isinstance(result, OperationResult)
        assert result.status == "success"
        assert "cache_dir" in result.item
        assert "directories" in result.item
        assert result.item["cache_dir"] == str(CacheUtils.get_cache_dir())

        dirs = result.item["directories"]
        assert isinstance(dirs, list)
        assert str(CacheUtils.get_vms_dir()) in dirs
        assert str(CacheUtils.get_images_dir()) in dirs
        assert str(CacheUtils.get_kernels_dir()) in dirs
        assert str(CacheUtils.get_bin_dir()) in dirs
        assert str(CacheUtils.get_logs_dir()) in dirs
        assert str(CacheUtils.get_keys_dir()) in dirs

        assert CacheUtils.get_vms_dir().exists()
        assert CacheUtils.get_images_dir().exists()
        assert CacheUtils.get_kernels_dir().exists()
        assert CacheUtils.get_bin_dir().exists()
        assert CacheUtils.get_logs_dir().exists()
        assert CacheUtils.get_keys_dir().exists()


# ======================================================================
# VM prune tests
# ======================================================================


class TestCachePruneVMs:
    """Test CacheOperation.prune_vms with real VM lifecycle."""

    @staticmethod
    def _setup_mocks(monkeypatch: pytest.MonkeyPatch) -> dict[str, object]:
        """Apply subprocess mocks and return references for assertions."""
        from tests.integration.conftest import (
            SmartPopenMock,
            SmartSubprocessMock,
        )

        sub_mock = SmartSubprocessMock()
        popen_mock = SmartPopenMock()
        monkeypatch.setattr("subprocess.run", sub_mock)
        monkeypatch.setattr("subprocess.Popen", popen_mock)

        provisioner_mock = MagicMock()
        monkeypatch.setattr(
            "mvmctl.api.vm_operations.Provisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        provisioner_mock.resize.return_value = provisioner_mock
        provisioner_mock.set_hostname.return_value = provisioner_mock
        provisioner_mock.inject_dns.return_value = provisioner_mock
        provisioner_mock.setup_ssh.return_value = provisioner_mock
        provisioner_mock.disable_cloud_init.return_value = provisioner_mock
        provisioner_mock.run.return_value = None
        return {"subprocess": sub_mock, "popen": popen_mock, "provisioner": provisioner_mock}

    def _create_vm(self, monkeypatch: pytest.MonkeyPatch, name: str) -> None:
        """Create a VM with all mocks applied."""
        mocks = self._setup_mocks(monkeypatch)
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks["provisioner"]
        mocks["provisioner"].run.return_value = None
        VMOperation.create(
            VMCreateInput(name=name, ssh_keys=[], enable_console=False)
        )

    def test_prune_vms_dry_run_skips_running(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """dry_run=True with default include_all=False skips running VMs."""
        self._create_vm(monkeypatch, "prune-dry-vm")

        result = CacheOperation.prune_vms(dry_run=True)
        assert result.item == []

        vm = VMOperation.get(VMInput(identifiers=["prune-dry-vm"]))
        assert vm.name == "prune-dry-vm"
        assert vm.status == VMStatus.RUNNING.value

    def test_prune_vms_dry_run_include_all_reports_running(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """dry_run=True with include_all=True reports running VMs for removal."""
        self._create_vm(monkeypatch, "prune-include-vm")

        result = CacheOperation.prune_vms(dry_run=True, include_all=True)
        assert result.item == ["prune-include-vm"]

        vm = VMOperation.get(VMInput(identifiers=["prune-include-vm"]))
        assert vm.name == "prune-include-vm"

    def test_prune_vms_actual_removes_stopped(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """prune_vms(dry_run=False) removes stopped VMs."""
        self._create_vm(monkeypatch, "prune-real-vm")

        # Patch VMController.stop to avoid real OS signals in tests
        # and to allow VMOperation.remove() to succeed on stopped VMs.
        def _patched_stop(self, force: bool = False) -> None:
            self._repo.update_status(self._vm.id, VMStatus.STOPPED.value)

        monkeypatch.setattr(
            "mvmctl.core.vm._controller.VMController.stop",
            _patched_stop,
        )

        VMOperation.stop(VMInput(identifiers=["prune-real-vm"]))

        vm = VMOperation.get(VMInput(identifiers=["prune-real-vm"]))
        assert vm.status == VMStatus.STOPPED.value

        result = CacheOperation.prune_vms(dry_run=False)
        assert result.item == ["prune-real-vm"]

        with pytest.raises(VMNotFoundError):
            VMOperation.get(VMInput(identifiers=["prune-real-vm"]))

    def test_prune_vms_empty_returns_empty(self) -> None:
        """prune_vms with no VMs returns an empty list."""
        result = CacheOperation.prune_vms(dry_run=True)
        assert result.item == []


# ======================================================================
# Asset prune tests
# ======================================================================


class TestCachePruneAssets:
    """Test pruning of images, kernels, binaries, and networks."""

    @staticmethod
    def _seed_extra_image(image_id: str = "e" * 64) -> str:
        """Seed an image that is not default and not referenced by VMs."""
        db = Database()
        repo = ImageRepository(db)
        repo.upsert(
            ImageItem(
                id=image_id,
                os_slug="alpine-3.19",
                os_name="Alpine 3.19",
                arch="x86_64",
                path="alpine-3.19.ext4",
                fs_type="ext4",
                minimum_rootfs_size_mib=5,
                original_size=5242880,
                is_default=False,
                is_present=True,
                pulled_at="2026-01-01T00:00:00+00:00",
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
                fs_uuid="87654321-4321-4321-4321-210987654321",
            )
        )
        return image_id

    @staticmethod
    def _seed_extra_kernel(kernel_id: str = "f" * 64) -> str:
        """Seed a kernel that is not default and not referenced by VMs."""
        db = Database()
        repo = KernelRepository(db)
        repo.upsert(
            KernelItem(
                id=kernel_id,
                name="vmlinux-custom",
                base_name="vmlinux-custom",
                version="6.6.0",
                arch="x86_64",
                type="custom",
                path="vmlinux-custom",
                is_default=False,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )
        return kernel_id

    @staticmethod
    def _seed_extra_binary(binary_id: str = "0" * 64) -> str:
        """Seed a binary that is not the default version."""
        db = Database()
        repo = BinaryRepository(db)
        repo.upsert(
            BinaryItem(
                id=binary_id,
                name="firecracker",
                version="1.14.0",
                full_version="v1.14.0",
                ci_version="v1.14",
                path="firecracker-1.14.0",
                is_default=False,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )
        return binary_id

    def test_prune_images_removes_unreferenced(self) -> None:
        """prune_images removes images that are not default and not referenced."""
        image_id = self._seed_extra_image()

        result = CacheOperation.prune_images(dry_run=False)
        assert isinstance(result, OperationResult)
        assert image_id in result.item

        db = Database()
        repo = ImageRepository(db)
        assert repo.get(image_id) is None

    def test_prune_images_dry_run_reports_only(self) -> None:
        """prune_images(dry_run=True) reports but does not remove."""
        image_id = self._seed_extra_image()

        result = CacheOperation.prune_images(dry_run=True)
        assert isinstance(result, OperationResult)
        assert image_id in result.item

        db = Database()
        repo = ImageRepository(db)
        assert repo.get(image_id) is not None

    def test_prune_kernels_removes_unreferenced(self) -> None:
        """prune_kernels removes kernels that are not default and not referenced."""
        kernel_id = self._seed_extra_kernel()

        result = CacheOperation.prune_kernels(dry_run=False)
        assert isinstance(result, OperationResult)
        assert kernel_id in result.item

        db = Database()
        repo = KernelRepository(db)
        assert repo.get(kernel_id) is None

    def test_prune_kernels_dry_run_reports_only(self) -> None:
        """prune_kernels(dry_run=True) reports but does not remove."""
        kernel_id = self._seed_extra_kernel()

        result = CacheOperation.prune_kernels(dry_run=True)
        assert isinstance(result, OperationResult)
        assert kernel_id in result.item

        db = Database()
        repo = KernelRepository(db)
        assert repo.get(kernel_id) is not None

    def test_prune_binaries_removes_unreferenced(self) -> None:
        """prune_binaries removes binaries that are not the default version."""
        binary_id = self._seed_extra_binary()

        result = CacheOperation.prune_binaries(dry_run=False)
        assert isinstance(result, OperationResult)
        assert "firecracker:1.14.0" in result.item

        db = Database()
        repo = BinaryRepository(db)
        assert repo.get(binary_id) is None

    def test_prune_binaries_dry_run_reports_only(self) -> None:
        """prune_binaries(dry_run=True) reports but does not remove."""
        binary_id = self._seed_extra_binary()

        result = CacheOperation.prune_binaries(dry_run=True)
        assert isinstance(result, OperationResult)
        assert "firecracker:1.14.0" in result.item

        db = Database()
        repo = BinaryRepository(db)
        assert repo.get(binary_id) is not None

    def test_prune_networks_dry_run_respects_defaults(self) -> None:
        """prune_networks(dry_run=True) skips default and referenced networks."""
        result = CacheOperation.prune_networks(dry_run=True)
        assert isinstance(result, OperationResult)
        assert len(result.item) == 0

    def test_prune_misc_returns_dict(self) -> None:
        """prune_misc returns a dictionary with expected keys."""
        result = CacheOperation.prune_misc(dry_run=True)
        assert isinstance(result, OperationResult)
        assert isinstance(result.item, dict)
        assert "appliance" in result.item
        assert "warm_images" in result.item
        assert "guestfs_state" in result.item


# ======================================================================
# prune_all tests
# ======================================================================


class TestCachePruneAll:
    """Test CacheOperation.prune_all aggregated pruning."""

    @staticmethod
    def _setup_mocks(monkeypatch: pytest.MonkeyPatch) -> dict[str, object]:
        from tests.integration.conftest import (
            SmartPopenMock,
            SmartSubprocessMock,
        )

        sub_mock = SmartSubprocessMock()
        popen_mock = SmartPopenMock()
        monkeypatch.setattr("subprocess.run", sub_mock)
        monkeypatch.setattr("subprocess.Popen", popen_mock)

        provisioner_mock = MagicMock()
        monkeypatch.setattr(
            "mvmctl.api.vm_operations.Provisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        provisioner_mock.resize.return_value = provisioner_mock
        provisioner_mock.set_hostname.return_value = provisioner_mock
        provisioner_mock.inject_dns.return_value = provisioner_mock
        provisioner_mock.setup_ssh.return_value = provisioner_mock
        provisioner_mock.disable_cloud_init.return_value = provisioner_mock
        provisioner_mock.run.return_value = None
        return {"subprocess": sub_mock, "popen": popen_mock, "provisioner": provisioner_mock}

    def _create_vm(self, monkeypatch: pytest.MonkeyPatch, name: str) -> None:
        """Create a VM with all mocks applied."""
        mocks = self._setup_mocks(monkeypatch)
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks["provisioner"]
        mocks["provisioner"].run.return_value = None
        VMOperation.create(
            VMCreateInput(name=name, ssh_keys=[], enable_console=False)
        )

    def test_prune_all_dry_run_returns_aggregated_result(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """prune_all(dry_run=True) returns PruneAllResult with aggregated IDs."""
        self._create_vm(monkeypatch, "prune-all-vm")

        result = CacheOperation.prune_all(dry_run=True, include_all=True)

        assert isinstance(result, OperationResult)
        assert isinstance(result.item, PruneAllResult)
        assert "prune-all-vm" in result.item.pruned_ids
        assert result.item.had_running_vms is True
        assert result.item.failed_ids == []

    def test_prune_all_empty_returns_empty_result(self) -> None:
        """prune_all with no VMs returns empty pruned_ids for VMs but still
        reports miscellaneous cache items like warm images."""
        result = CacheOperation.prune_all(dry_run=True)

        assert isinstance(result, OperationResult)
        assert isinstance(result.item, PruneAllResult)
        assert result.item.failed_ids == []
        assert result.item.had_running_vms is False
        # Integration conftest seeds a warm image, so prune_misc reports it
        assert "warm_images" in result.item.pruned_ids


# ======================================================================
# clean tests
# ======================================================================


class TestCacheClean:
    """Test CacheOperation.clean."""

    def test_clean_dry_run_returns_clean_result(self) -> None:
        """clean(dry_run=True) returns CleanResult without removing anything."""
        result = CacheOperation.clean(dry_run=True)

        assert isinstance(result, OperationResult)
        assert isinstance(result.item, CleanResult)
        assert isinstance(result.item.prune_result, PruneAllResult)
        assert result.item.cache_dir_removed is True
        assert result.item.cache_dir == str(CacheUtils.get_cache_dir())
        assert CacheUtils.get_cache_dir().exists()

    def test_clean_empty_cache_handles_gracefully(self) -> None:
        """clean(dry_run=True) handles empty cache gracefully.

        clean() calls prune_all with include_all=True, so default assets
        seeded by the integration conftest are reported for removal.
        """
        result = CacheOperation.clean(dry_run=True)

        assert isinstance(result, OperationResult)
        assert isinstance(result.item, CleanResult)
        assert isinstance(result.item.prune_result, PruneAllResult)
        assert result.item.prune_result.failed_ids == []
        assert result.item.prune_result.had_running_vms is False
        assert result.item.cache_dir_removed is True
        # clean() uses include_all=True — default assets are reported
        assert "net" in result.item.prune_result.pruned_ids
        assert "warm_images" in result.item.prune_result.pruned_ids
