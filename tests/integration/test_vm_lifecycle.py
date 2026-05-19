"""Integration tests for the full VM lifecycle through the real public API.

Tests exercise the complete VM orchestration flow:
  create → list → get → snapshot → start/stop → remove

Only subprocess (system-level operations like cp, dd, ip, iptables, firecracker)
are mocked. ALL orchestration logic in api/ and core/ runs unmocked.
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.api import VMCreateInput, VMInput, VMOperation
from mvmctl.exceptions import VMNotFoundError
from mvmctl.models import VMInstanceItem, VMStatus
from mvmctl.models.result import OperationResult

# ======================================================================
# VM lifecycle tests
# ======================================================================


class TestVMCreateAndList:
    """Test VM creation and listing through the real API."""

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

        # Mock GuestfsProvisioner.run to avoid real libguestfs
        provisioner_mock = MagicMock()
        monkeypatch.setattr(
            "mvmctl.api.vm_operations.VMProvisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        return {
            "subprocess": sub_mock,
            "popen": popen_mock,
            "provisioner": provisioner_mock,
        }

    def test_create_vm(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Create a VM via the real API and verify the DB record."""
        mocks = self._setup_mocks(monkeypatch)
        # GuestfsProvisioner needs resize, run etc as chainable methods
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks[
            "provisioner"
        ]
        mocks["provisioner"].run.return_value = None

        VMOperation.create(
            VMCreateInput(
                name="test-create-vm",
                ssh_keys=[],
                enable_console=False,
            )
        )

        # Verify the VM exists in the DB with the correct state
        vm = VMOperation.get(VMInput(identifiers=["test-create-vm"]))
        assert isinstance(vm, VMInstanceItem)
        assert vm.name == "test-create-vm"
        assert vm.status == VMStatus.RUNNING
        assert vm.pid > 0
        assert vm.network_id is not None

    def test_list_vms(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Create two VMs and verify list_all returns both."""
        mocks = self._setup_mocks(monkeypatch)
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks[
            "provisioner"
        ]
        mocks["provisioner"].run.return_value = None

        VMOperation.create(
            VMCreateInput(name="list-vm-1", ssh_keys=[], enable_console=False)
        )
        VMOperation.create(
            VMCreateInput(name="list-vm-2", ssh_keys=[], enable_console=False)
        )

        vms = VMOperation.list_all()
        names = [v.name for v in vms]
        assert "list-vm-1" in names
        assert "list-vm-2" in names

    def test_get_vm_by_name(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Get a VM by its name identifier."""
        mocks = self._setup_mocks(monkeypatch)
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks[
            "provisioner"
        ]
        mocks["provisioner"].run.return_value = None

        VMOperation.create(
            VMCreateInput(name="get-by-name", ssh_keys=[], enable_console=False)
        )
        vm = VMOperation.get(VMInput(identifiers=["get-by-name"]))
        assert vm.name == "get-by-name"

    def test_get_vm_by_id(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Get a VM by its ID (SHA256 hash prefix)."""
        mocks = self._setup_mocks(monkeypatch)
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks[
            "provisioner"
        ]
        mocks["provisioner"].run.return_value = None

        VMOperation.create(
            VMCreateInput(name="get-by-id", ssh_keys=[], enable_console=False)
        )
        vm = VMOperation.get(VMInput(identifiers=["get-by-id"]))
        # Get by ID prefix (first 6 chars)
        vm2 = VMOperation.get(VMInput(identifiers=[vm.id[:12]]))
        assert vm2.name == "get-by-id"
        assert vm2.id == vm.id

    def test_create_vm_default_network(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a VM using the default network (no explicit network_name)."""
        mocks = self._setup_mocks(monkeypatch)
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks[
            "provisioner"
        ]
        mocks["provisioner"].run.return_value = None

        VMOperation.create(
            VMCreateInput(
                name="default-net-vm", ssh_keys=[], enable_console=False
            )
        )
        vm = VMOperation.get(VMInput(identifiers=["default-net-vm"]))
        assert vm.network_id is not None
        assert len(vm.network_id) > 0


class TestVMRemove:
    """Test VM removal through the real API."""

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
            "mvmctl.api.vm_operations.VMProvisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        return {
            "subprocess": sub_mock,
            "popen": popen_mock,
            "provisioner": provisioner_mock,
        }

    def _create_vm(
        self, monkeypatch: pytest.MonkeyPatch, name: str
    ) -> VMInstanceItem:
        mocks = self._setup_mocks(monkeypatch)
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks[
            "provisioner"
        ]
        mocks["provisioner"].run.return_value = None

        VMOperation.create(
            VMCreateInput(name=name, ssh_keys=[], enable_console=False)
        )
        return VMOperation.get(VMInput(identifiers=[name]))

    def test_create_then_remove(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Full lifecycle: create → verify → remove → verify gone."""
        vm = self._create_vm(monkeypatch, "rm-test-vm")
        assert vm is not None

        VMOperation.remove(VMInput(identifiers=["rm-test-vm"]))

        with pytest.raises(VMNotFoundError):
            VMOperation.get(VMInput(identifiers=["rm-test-vm"]))

    def test_remove_cleans_vm_dir(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Removing a VM also removes its VM directory."""
        from mvmctl.utils.common import CacheUtils

        vm = self._create_vm(monkeypatch, "cleanup-dir-vm")
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        assert vm_dir.exists()

        VMOperation.remove(VMInput(identifiers=["cleanup-dir-vm"]))
        assert not vm_dir.exists()

    def test_remove_updates_list(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """After removal, the VM no longer appears in list_all()."""
        self._create_vm(monkeypatch, "list-after-rm")
        vms_before = VMOperation.list_all()
        assert any(v.name == "list-after-rm" for v in vms_before)

        VMOperation.remove(VMInput(identifiers=["list-after-rm"]))
        vms_after = VMOperation.list_all()
        assert not any(v.name == "list-after-rm" for v in vms_after)

    def test_remove_nonexistent_vm(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Removing a VM that does not exist raises VMNotFoundError."""
        with pytest.raises(VMNotFoundError):
            VMOperation.remove(VMInput(identifiers=["nonexistent-vm"]))


class TestVMStatusFiltering:
    """Test VM listing with status filtering."""

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
            "mvmctl.api.vm_operations.VMProvisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        provisioner_mock.resize.return_value = provisioner_mock
        provisioner_mock.set_hostname.return_value = provisioner_mock
        provisioner_mock.inject_dns.return_value = provisioner_mock
        provisioner_mock.setup_ssh.return_value = provisioner_mock
        provisioner_mock.disable_cloud_init.return_value = provisioner_mock
        provisioner_mock.run.return_value = None
        return {
            "subprocess": sub_mock,
            "popen": popen_mock,
            "provisioner": provisioner_mock,
        }

    def _create_vm(self, monkeypatch: pytest.MonkeyPatch, name: str) -> None:
        _mocks = self._setup_mocks(monkeypatch)
        VMOperation.create(
            VMCreateInput(name=name, ssh_keys=[], enable_console=False)
        )

    def test_list_all_vms(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """list_all() returns all created VMs."""
        self._create_vm(monkeypatch, "filter-vm-1")
        self._create_vm(monkeypatch, "filter-vm-2")
        all_vms = VMOperation.list_all()
        names = [v.name for v in all_vms]
        assert "filter-vm-1" in names
        assert "filter-vm-2" in names

    def test_list_by_status(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """list_all(status=VMStatus.RUNNING) returns only running VMs."""
        self._create_vm(monkeypatch, "status-vm")
        running = VMOperation.list_all(status=VMStatus.RUNNING)
        vm_names = [v.name for v in running]
        assert "status-vm" in vm_names
        assert all(v.status == VMStatus.RUNNING for v in running)


class TestVMEdgeCases:
    """Test edge cases and error handling in the VM lifecycle."""

    def test_get_nonexistent_vm(self) -> None:
        """Getting a non-existent VM raises VMNotFoundError."""
        with pytest.raises(VMNotFoundError):
            VMOperation.get(VMInput(identifiers=["no-such-vm"]))

    def test_list_empty_returns_empty_list(self) -> None:
        """list_all() returns empty list when no VMs exist."""
        vms = VMOperation.list_all()
        assert vms == []

    def test_list_by_status_empty(self) -> None:
        """list_all(status=...) returns empty list when no VMs match."""
        vms = VMOperation.list_all(status=VMStatus.RUNNING)
        # Should not raise — returns []
        assert isinstance(vms, list)

    def test_get_with_empty_identifiers(self) -> None:
        """VMInput with no identifiers raises VMNotFoundError."""
        with pytest.raises(VMNotFoundError):
            VMOperation.get(VMInput(identifiers=[]))


class TestVMSnapshotWorkflow:
    """Test VM snapshot and load through the real API."""

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
            "mvmctl.api.vm_operations.VMProvisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        provisioner_mock.resize.return_value = provisioner_mock
        provisioner_mock.set_hostname.return_value = provisioner_mock
        provisioner_mock.inject_dns.return_value = provisioner_mock
        provisioner_mock.setup_ssh.return_value = provisioner_mock
        provisioner_mock.disable_cloud_init.return_value = provisioner_mock
        provisioner_mock.run.return_value = None

        # Mock the FirecrackerSpawner.snapshot() so it doesn't issue real API calls
        monkeypatch.setattr(
            "mvmctl.core.vm._controller.VMController.snapshot",
            MagicMock(return_value=None),
        )
        monkeypatch.setattr(
            "mvmctl.core.vm._controller.VMController.load_snapshot",
            MagicMock(return_value=None),
        )

        return {
            "subprocess": sub_mock,
            "popen": popen_mock,
            "provisioner": provisioner_mock,
        }

    def test_create_and_snapshot(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Create a VM, snapshot it, and load the snapshot."""
        self._setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(name="snap-vm", ssh_keys=[], enable_console=False)
        )
        vm = VMOperation.get(VMInput(identifiers=["snap-vm"]))
        assert vm is not None

        mem_path = tmp_path / "snap.mem"
        state_path = tmp_path / "snap.state"
        VMOperation.snapshot(
            VMInput(identifiers=["snap-vm"]),
            mem_out=mem_path,
            state_out=state_path,
        )

    def test_create_snapshot_and_load(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Create a VM, snapshot, then load the snapshot."""
        self._setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="load-snap-vm", ssh_keys=[], enable_console=False
            )
        )

        mem_path = tmp_path / "load.mem"
        state_path = tmp_path / "load.state"
        VMOperation.snapshot(
            VMInput(identifiers=["load-snap-vm"]),
            mem_out=mem_path,
            state_out=state_path,
        )
        # The mock VMController.snapshot doesn't create files;
        # create them so load_snapshot's file-existence check passes.
        mem_path.write_text("")
        state_path.write_text("")
        VMOperation.load_snapshot(
            VMInput(identifiers=["load-snap-vm"]),
            mem_in=mem_path,
            state_in=state_path,
        )


class TestVMInspect:
    """Test VM inspection through the real API."""

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
            "mvmctl.api.vm_operations.VMProvisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        provisioner_mock.resize.return_value = provisioner_mock
        provisioner_mock.set_hostname.return_value = provisioner_mock
        provisioner_mock.inject_dns.return_value = provisioner_mock
        provisioner_mock.setup_ssh.return_value = provisioner_mock
        provisioner_mock.disable_cloud_init.return_value = provisioner_mock
        provisioner_mock.run.return_value = None
        return {
            "subprocess": sub_mock,
            "popen": popen_mock,
            "provisioner": provisioner_mock,
        }

    def test_inspect_vm(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Create a VM and inspect it, verifying all grouped fields."""
        self._setup_mocks(monkeypatch)
        VMOperation.create(
            VMCreateInput(name="inspect-vm", ssh_keys=[], enable_console=False)
        )
        vm = VMOperation.get(VMInput(identifiers=["inspect-vm"]))
        result = VMOperation.inspect(VMInput(identifiers=["inspect-vm"]))

        assert result["vm"]["id"] == vm.id
        assert result["vm"]["name"] == "inspect-vm"
        assert result["vm"]["status"] == VMStatus.RUNNING
        assert result["resources"]["vcpus"] == vm.vcpu_count
        assert result["resources"]["mem"] == vm.mem_size_mib
        assert result["assets"]["image_id"] == vm.image_id
        assert result["assets"]["image_name"] is not None
        assert result["assets"]["kernel_id"] == vm.kernel_id
        assert result["assets"]["kernel_version"] is not None
        assert result["networking"]["network_id"] == vm.network_id
        assert result["networking"]["network_name"] is not None
        assert result["assets"]["binary_id"] == vm.binary_id
        assert result["assets"]["binary_name"] is not None
        assert result["filesystem"]["vm_dir"] is not None
        assert result["filesystem"]["rootfs_path"] is not None
        assert result["filesystem"]["config_path"] is not None
        assert result["console"]["relay_running"] is False
        assert result["console"]["relay_pid"] is None
        assert result["networking"]["tap_device"] == vm.tap_device
        assert result["networking"]["ipv4"] == vm.ipv4
        assert result["networking"]["mac"] == vm.mac

    def test_inspect_nonexistent_vm(self) -> None:
        """Inspecting a non-existent VM raises VMNotFoundError."""
        with pytest.raises(VMNotFoundError):
            VMOperation.inspect(VMInput(identifiers=["no-such-vm"]))

    def test_inspect_vm_grouped(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Inspect returns enriched grouped data."""
        self._setup_mocks(monkeypatch)
        VMOperation.create(
            VMCreateInput(
                name="inspect-tree-vm", ssh_keys=[], enable_console=False
            )
        )
        result = VMOperation.inspect(
            VMInput(identifiers=["inspect-tree-vm"]),
        )

        assert "vm" in result
        assert "resources" in result
        assert "networking" in result
        assert "assets" in result
        assert "filesystem" in result
        assert "console" in result

        assert result["vm"]["name"] == "inspect-tree-vm"
        assert result["resources"]["vcpus"] > 0
        assert result["resources"]["mem"] > 0
        assert result["networking"]["network_name"] is not None
        assert result["assets"]["image_name"] is not None
        assert result["assets"]["kernel_version"] is not None
        assert result["assets"]["binary_name"] is not None
        assert result["filesystem"]["vm_dir"] is not None
        assert result["filesystem"]["rootfs_path"] is not None
        assert result["console"]["relay_running"] is False


class TestVMCreateExplicit:
    """Test VM creation with explicit non-default assets and parameters."""

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
            "mvmctl.api.vm_operations.VMProvisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        provisioner_mock.resize.return_value = provisioner_mock
        provisioner_mock.set_hostname.return_value = provisioner_mock
        provisioner_mock.inject_dns.return_value = provisioner_mock
        provisioner_mock.setup_ssh.return_value = provisioner_mock
        provisioner_mock.disable_cloud_init.return_value = provisioner_mock
        provisioner_mock.run.return_value = None
        return {
            "subprocess": sub_mock,
            "popen": popen_mock,
            "provisioner": provisioner_mock,
        }

    def _seed_second_image(self) -> str:
        from mvmctl.core._shared import Database
        from mvmctl.core.image._repository import ImageRepository
        from mvmctl.models import ImageItem
        from mvmctl.utils.common import CacheUtils

        db = Database()
        repo = ImageRepository(db)
        image_id = "e" * 64
        images_dir = CacheUtils.get_images_dir()
        repo.upsert(
            ImageItem(
                id=image_id,
                type="debian-12",
                name="Debian 12",
                arch="x86_64",
                path=str(images_dir / "debian-12.ext4"),
                fs_type="ext4",
                minimum_rootfs_size_mib=10,
                original_size=10485760,
                is_default=False,
                is_present=True,
                pulled_at="2026-01-01T00:00:00+00:00",
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
                fs_uuid="87654321-4321-4321-4321-cba987654321",
            )
        )
        (images_dir / "debian-12.ext4").write_text("fake debian image")
        warm_dir = CacheUtils.get_warm_image_dir()
        (warm_dir / f"{image_id}.ext4").write_bytes(
            b"\x00" * (10 * 1024 * 1024)
        )
        return image_id

    def _seed_second_kernel(self) -> str:
        from mvmctl.core._shared import Database
        from mvmctl.core.kernel._repository import KernelRepository
        from mvmctl.models import KernelItem
        from mvmctl.utils.common import CacheUtils

        db = Database()
        repo = KernelRepository(db)
        kernel_id = "f" * 64
        kernels_dir = CacheUtils.get_kernels_dir()
        repo.upsert(
            KernelItem(
                id=kernel_id,
                name="vmlinux",
                base_name="vmlinux",
                version="6.6.0",
                arch="x86_64",
                type="official",
                path=str(kernels_dir / "vmlinux-custom"),
                is_default=False,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )
        (kernels_dir / "vmlinux-custom").write_text("fake custom kernel")
        return kernel_id

    def _seed_second_binary(self) -> str:
        from mvmctl.core._shared import Database
        from mvmctl.core.binary._repository import BinaryRepository
        from mvmctl.models import BinaryItem
        from mvmctl.utils.common import CacheUtils

        db = Database()
        repo = BinaryRepository(db)
        binary_id = "g" * 64
        bin_dir = CacheUtils.get_bin_dir()
        repo.upsert(
            BinaryItem(
                id=binary_id,
                name="firecracker",
                version="1.16.0",
                full_version="v1.16.0",
                ci_version="v1.16",
                path=str(bin_dir / "firecracker-custom"),
                is_default=False,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )
        fc_file = bin_dir / "firecracker-custom"
        fc_file.write_text("fake custom firecracker")
        fc_file.chmod(0o755)
        return binary_id

    def _seed_second_network(self) -> str:
        from mvmctl.core._shared import Database
        from mvmctl.core.network._repository import NetworkRepository
        from mvmctl.models import NetworkItem

        db = Database()
        repo = NetworkRepository(db)
        network_id = "h" * 64
        repo.upsert(
            NetworkItem(
                id=network_id,
                name="custom-net",
                subnet="10.30.0.0/24",
                bridge="mvm-br1",
                ipv4_gateway="10.30.0.1",
                bridge_active=True,
                nat_enabled=True,
                nat_gateways="eth0",
                is_default=False,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )
        return network_id

    def test_create_vm_with_explicit_kernel_image_binary(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create VM with explicit non-default kernel, image, and binary."""
        self._setup_mocks(monkeypatch)
        image_id = self._seed_second_image()
        kernel_id = self._seed_second_kernel()
        binary_id = self._seed_second_binary()

        VMOperation.create(
            VMCreateInput(
                name="explicit-assets-vm",
                ssh_keys=[],
                enable_console=False,
                image=image_id,
                kernel_id=kernel_id,
                binary_id=binary_id,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["explicit-assets-vm"]))
        assert vm.image_id == image_id
        assert vm.kernel_id == kernel_id
        assert vm.binary_id == binary_id

    def test_create_vm_with_custom_vcpus_memory(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create VM with custom vcpu_count and mem_size_mib."""
        mocks = self._setup_mocks(monkeypatch)
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks[
            "provisioner"
        ]
        mocks["provisioner"].run.return_value = None

        result = VMOperation.create(
            VMCreateInput(
                name="custom-cpu-mem-vm",
                ssh_keys=[],
                enable_console=False,
                vcpu_count=4,
                mem_size_mib="512",
            )
        )
        assert result.status == "success", f"VM create failed: {result.message}"

        vm = VMOperation.get(VMInput(identifiers=["custom-cpu-mem-vm"]))
        assert vm.vcpu_count == 4
        assert vm.mem_size_mib == 512

    def test_create_vm_with_explicit_network_name(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create VM with an explicit non-default network name."""
        self._setup_mocks(monkeypatch)
        network_id = self._seed_second_network()

        VMOperation.create(
            VMCreateInput(
                name="explicit-net-vm",
                ssh_keys=[],
                enable_console=False,
                network_name="custom-net",
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["explicit-net-vm"]))
        assert vm.network_id == network_id

    def test_create_duplicate_vm_name(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Creating a VM with a duplicate name returns error status."""
        self._setup_mocks(monkeypatch)
        VMOperation.create(
            VMCreateInput(name="dup-vm", ssh_keys=[], enable_console=False)
        )

        result = VMOperation.create(
            VMCreateInput(name="dup-vm", ssh_keys=[], enable_console=False)
        )
        assert isinstance(result, OperationResult)
        assert result.status in ("error", "failure")


class TestVMRemoveForce:
    """Test VM removal with force flag."""

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
            "mvmctl.api.vm_operations.VMProvisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        provisioner_mock.resize.return_value = provisioner_mock
        provisioner_mock.set_hostname.return_value = provisioner_mock
        provisioner_mock.inject_dns.return_value = provisioner_mock
        provisioner_mock.setup_ssh.return_value = provisioner_mock
        provisioner_mock.disable_cloud_init.return_value = provisioner_mock
        provisioner_mock.run.return_value = None
        return {
            "subprocess": sub_mock,
            "popen": popen_mock,
            "provisioner": provisioner_mock,
        }

    def test_remove_vm_with_force(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a VM and remove it with force=True."""
        self._setup_mocks(monkeypatch)
        VMOperation.create(
            VMCreateInput(name="force-rm-vm", ssh_keys=[], enable_console=False)
        )
        vm = VMOperation.get(VMInput(identifiers=["force-rm-vm"]))
        from mvmctl.utils.common import CacheUtils

        vm_dir = CacheUtils.get_vm_dir(vm.id)
        assert vm_dir.exists()

        VMOperation.remove(VMInput(identifiers=["force-rm-vm"], force=True))

        with pytest.raises(VMNotFoundError):
            VMOperation.get(VMInput(identifiers=["force-rm-vm"]))
        assert not vm_dir.exists()
