"""Tests for VMOperation class — API layer VM lifecycle orchestration."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.api.inputs._vm_input import VMInput
from mvmctl.api.vm_operations import VMOperation
from mvmctl.exceptions import VMNotFoundError
from mvmctl.models import VMInstanceItem, VMStatus


def _make_vm(
    name: str = "test-vm",
    status: VMStatus = VMStatus.RUNNING,
    vm_id: str | None = None,
    network_id: str | None = None,
    **kwargs,
) -> VMInstanceItem:
    return VMInstanceItem(
        name=name,
        id=vm_id or f"{name}-id-" + "x" * 55,
        pid=1234,
        process_start_time=None,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=network_id,
        tap_device=f"mvm-{name}-tap0",
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
        status=status,
        config_path="vm.json",
        kernel_id="kern-id-" + "x" * 55,
        image_id="img-id-" + "x" * 55,
        binary_id="bin-id-" + "x" * 55,
        disk_size_mib=2048,
        vcpu_count=2,
        mem_size_mib=512,
        api_socket_path="fc.socket",
        rootfs_path="rootfs.ext4",
        rootfs_suffix="ext4",
        enable_pci=False,
        enable_logging=True,
        enable_metrics=False,
        enable_console=False,
        cloud_init_mode="off",
        log_path="fc.log",
        serial_output_path="serial.log",
        exit_code=None,
        lsm_flags="",
        boot_args="console=ttyS0",
        **kwargs,
    )


class TestVMOperationListAll:
    """Tests for VMOperation.list_all()."""

    def test_list_all_no_filter(self, mocker):
        """list_all() returns all VMs when no status is provided."""
        mock_vms = [_make_vm("vm1"), _make_vm("vm2")]
        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = mock_vms
        # Patch at the point of use
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mock_repo,
        )

        result = VMOperation.list_all()
        assert len(result) == 2
        mock_repo.list_all.assert_called_once()

    def test_list_all_with_status_filter(self, mocker):
        """list_all() filters by status when provided."""
        mock_vms = [_make_vm("vm1", status=VMStatus.RUNNING)]
        mock_repo = mocker.MagicMock()
        mock_repo.list_by_status.return_value = mock_vms
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mock_repo,
        )

        result = VMOperation.list_all(status=VMStatus.RUNNING)
        assert len(result) == 1
        mock_repo.list_by_status.assert_called_once_with(VMStatus.RUNNING)

    def test_list_all_empty(self, mocker):
        """list_all() returns empty list when no VMs exist."""
        mock_repo = mocker.MagicMock()
        mock_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mock_repo,
        )

        result = VMOperation.list_all()
        assert result == []


class TestVMOperationGet:
    """Tests for VMOperation.get()."""

    def test_get_by_name(self, mocker):
        """get() returns a single VM by name."""
        mock_vm = _make_vm("test-vm")
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [mock_vm]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )

        result = VMOperation.get(VMInput(identifiers=["test-vm"]))
        assert result.name == "test-vm"
        assert result.status == VMStatus.RUNNING

    def test_get_raises_when_multiple(self, mocker):
        """get() raises VMNotFoundError when multiple VMs match."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [_make_vm("vm1"), _make_vm("vm2")]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )

        with pytest.raises(VMNotFoundError, match="Expected exactly one"):
            VMOperation.get(VMInput(identifiers=["ambigious"]))

    def test_get_resolves_from_vm_request(self, mocker):
        """get() delegates resolution to VMRequest."""
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mocker.MagicMock(
            vms=[_make_vm("test-vm")]
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )

        VMOperation.get(VMInput(identifiers=["test-vm"]))
        mock_request.resolve.assert_called_once()


class TestVMOperationCreate:
    """Tests for VMOperation.create()."""

    def test_create_calls_execute_create(self, mocker):
        """create() orchestrates the full VM creation flow."""
        # Mock VMCreateContext
        mock_ctx = mocker.MagicMock()
        mock_ctx.vm_id = "generated-vm-id"
        mock_ctx.vm_dir = Path("/fake/vm_dir")
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateContext",
            return_value=mock_ctx,
        )

        # Mock VMCreateRequest
        mock_resolved = mocker.MagicMock()
        mock_resolved.vm_id = "generated-vm-id"
        mock_resolved.name = "test-vm"
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateRequest",
            return_value=mock_request,
        )

        # Mock _execute_create
        mock_execute = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._execute_create"
        )

        from mvmctl.api.inputs._vm_create_input import VMCreateInput

        result = VMOperation.create(
            VMCreateInput(name="test-vm", ssh_keys=["key1"])
        )

        assert result.status == "success"
        mock_execute.assert_called_once_with(
            mock_resolved,
            audit_action="vm.create",
            on_progress=None,
        )


class TestVMOperationRemove:
    """Tests for VMOperation.remove()."""

    def test_remove_calls_stop_and_cleanup(self, mocker):
        """remove() stops VM controller and performs cleanup."""
        mock_vm = _make_vm("test-vm")
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [mock_vm]
        mock_resolved.force = False
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )

        mock_repo = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mock_repo,
        )

        mock_controller = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMController",
            return_value=mock_controller,
        )

        mock_cleanup = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._perform_removal_cleanup"
        )
        mock_rmtree = mocker.patch("shutil.rmtree")
        # Make vm_dir "exist" so shutil.rmtree gets called
        mock_vm_dir = mocker.MagicMock()
        mock_vm_dir.exists.return_value = True
        mocker.patch(
            "mvmctl.api.vm_operations.CacheUtils.get_vm_dir",
            return_value=mock_vm_dir,
        )

        VMOperation.remove(VMInput(identifiers=["test-vm"]))

        mock_controller.stop.assert_called_once_with(force=False)
        mock_cleanup.assert_called_once_with(mock_vm, None)
        mock_repo.delete.assert_called_once_with(mock_vm.id)
        mock_rmtree.assert_called_once()

    def test_remove_force_flag(self, mocker):
        """remove() passes force=True to VMController.stop()."""
        mock_vm = _make_vm("test-vm")
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [mock_vm]
        mock_resolved.force = True
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mocker.MagicMock(),
        )

        mock_controller = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMController",
            return_value=mock_controller,
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._perform_removal_cleanup"
        )
        mocker.patch("shutil.rmtree")
        mocker.patch(
            "mvmctl.api.vm_operations.CacheUtils.get_vm_dir",
            return_value=Path("/fake"),
        )

        VMOperation.remove(VMInput(identifiers=["test-vm"], force=True))
        mock_controller.stop.assert_called_once_with(force=True)

    def test_remove_multiple_vms(self, mocker):
        """remove() handles multiple VM identifiers."""
        mock_vms = [_make_vm("vm1"), _make_vm("vm2")]
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = mock_vms
        mock_resolved.force = False
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mocker.MagicMock(),
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMController",
            return_value=mocker.MagicMock(),
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._perform_removal_cleanup"
        )
        mocker.patch("shutil.rmtree")
        mocker.patch(
            "mvmctl.api.vm_operations.CacheUtils.get_vm_dir",
            return_value=Path("/fake"),
        )

        VMOperation.remove(VMInput(identifiers=["vm1", "vm2"]))


class TestVMOperationStateTransitions:
    """Tests for VMOperation state transition methods."""

    @pytest.fixture
    def mock_resolved(self):
        mock_vm = _make_vm("test-vm")
        resolved = MagicMock()
        resolved.vms = [mock_vm]
        resolved.force = False
        return resolved

    def _setup_mocks(self, mocker, mock_resolved):
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )
        mock_service = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMService",
            return_value=mock_service,
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mocker.MagicMock(),
        )
        mock_audit = mocker.patch("mvmctl.utils.auditlog.AuditLog.log")
        return mock_service, mock_audit

    def test_start(self, mocker, mock_resolved):
        """start() calls VMService.start()."""
        mock_service, _ = self._setup_mocks(mocker, mock_resolved)
        VMOperation.start(VMInput(identifiers=["test-vm"]))
        mock_service.start.assert_called_once()

    def test_stop(self, mocker, mock_resolved):
        """stop() calls VMService.stop()."""
        mock_service, _ = self._setup_mocks(mocker, mock_resolved)
        VMOperation.stop(VMInput(identifiers=["test-vm"]))
        assert mock_service.stop.call_count == 1

    def test_pause(self, mocker, mock_resolved):
        """pause() calls VMService.pause()."""
        mock_service, _ = self._setup_mocks(mocker, mock_resolved)
        VMOperation.pause(VMInput(identifiers=["test-vm"]))
        mock_service.pause.assert_called_once()

    def test_resume(self, mocker, mock_resolved):
        """resume() calls VMService.resume()."""
        mock_service, _ = self._setup_mocks(mocker, mock_resolved)
        VMOperation.resume(VMInput(identifiers=["test-vm"]))
        mock_service.resume.assert_called_once()

    def test_reboot(self, mocker, mock_resolved):
        """reboot() stops VM controller and respawns firecracker."""
        _, _ = self._setup_mocks(mocker, mock_resolved)
        mock_vm = mock_resolved.vms[0]
        mock_controller = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMController",
            return_value=mock_controller,
        )
        mock_respawn = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._respawn_firecracker"
        )
        VMOperation.reboot(VMInput(identifiers=["test-vm"]))
        mock_controller.stop.assert_called_once_with(force=False)
        mock_respawn.assert_called_once_with(mock_vm)


class TestVMOperationSnapshot:
    """Tests for VMOperation snapshot operations."""

    def test_snapshot_calls_controller(self, mocker):
        """snapshot() resolves VM and calls VMController.snapshot()."""
        mock_vm = _make_vm("test-vm")
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [mock_vm]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mocker.MagicMock(),
        )
        mock_controller = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMController",
            return_value=mock_controller,
        )

        mem_out = Path("/tmp/mem")
        state_out = Path("/tmp/state")
        VMOperation.snapshot(
            VMInput(identifiers=["test-vm"]), mem_out, state_out
        )
        mock_controller.snapshot.assert_called_once_with(mem_out, state_out)

    def test_snapshot_raises_on_multiple(self, mocker):
        """snapshot() raises if more than one VM matches."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [_make_vm("vm1"), _make_vm("vm2")]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )

        with pytest.raises(VMNotFoundError, match="Expected exactly one"):
            VMOperation.snapshot(
                VMInput(identifiers=["amb"]), Path("/mem"), Path("/state")
            )

    def test_load_snapshot_calls_controller(self, mocker):
        """load_snapshot() calls VMController.load_snapshot()."""
        mock_vm = _make_vm("test-vm")
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [mock_vm]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mocker.MagicMock(),
        )
        mock_controller = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMController",
            return_value=mock_controller,
        )

        mem_in = Path("/tmp/mem")
        state_in = Path("/tmp/state")
        VMOperation.load_snapshot(
            VMInput(identifiers=["test-vm"]), mem_in, state_in
        )
        mock_controller.load_snapshot.assert_called_once_with(
            mem_in, state_in, False
        )


class TestVMOperationInspect:
    """Tests for VMOperation.inspect()."""

    def test_inspect_flat_mode(self, mocker):
        """inspect() returns flat dict with enriched data."""
        mock_vm = _make_vm("test-vm")
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [mock_vm]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )

        for name in (
            "ImageRepository",
            "KernelRepository",
            "NetworkRepository",
            "BinaryRepository",
        ):
            mock_repo = mocker.MagicMock()
            mock_repo.get.return_value = None
            mocker.patch(
                f"mvmctl.api.vm_operations.{name}",
                return_value=mock_repo,
            )

        mocker.patch(
            "mvmctl.api.vm_operations.CacheUtils.get_vm_dir",
            return_value=Path("/fake/vm_dir"),
        )

        result = VMOperation.inspect(VMInput(identifiers=["test-vm"]))
        assert isinstance(result, dict)
        assert result["name"] == "test-vm"
        assert result["status"] == VMStatus.RUNNING

    def test_inspect_tree_mode(self, mocker):
        """inspect() returns tree dict when tree=True."""
        mock_vm = _make_vm("test-vm")
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [mock_vm]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )

        for name in (
            "ImageRepository",
            "KernelRepository",
            "NetworkRepository",
            "BinaryRepository",
        ):
            mock_repo = mocker.MagicMock()
            mock_repo.get.return_value = None
            mocker.patch(
                f"mvmctl.api.vm_operations.{name}",
                return_value=mock_repo,
            )

        mocker.patch(
            "mvmctl.api.vm_operations.CacheUtils.get_vm_dir",
            return_value=Path("/fake/vm_dir"),
        )

        result = VMOperation.inspect(
            VMInput(identifiers=["test-vm"]), tree=True
        )
        assert "vm" in result
        assert "resources" in result
        assert "networking" in result
        assert "assets" in result
        assert "filesystem" in result
        assert "console" in result

    def test_inspect_raises_on_multiple(self, mocker):
        """inspect() raises when multiple VMs match."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [_make_vm("vm1"), _make_vm("vm2")]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )

        with pytest.raises(VMNotFoundError, match="Expected exactly one"):
            VMOperation.inspect(VMInput(identifiers=["amb"]))


class TestVMOperationExport:
    """Tests for VMOperation.export()."""

    def test_export_returns_config(self, mocker):
        """export() returns a VMExportConfig with resolved asset data."""
        mock_vm = _make_vm("test-vm")
        mock_resolved = mocker.MagicMock()
        mock_resolved.vms = [mock_vm]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMRequest",
            return_value=mock_request,
        )

        for name in (
            "ImageRepository",
            "KernelRepository",
            "NetworkRepository",
            "BinaryRepository",
        ):
            mock_repo = mocker.MagicMock()
            mock_repo.get.return_value = None
            mocker.patch(
                f"mvmctl.api.vm_operations.{name}",
                return_value=mock_repo,
            )

        result = VMOperation.export(VMInput(identifiers=["test-vm"]))
        assert result.name == "test-vm"
        assert result.compute.vcpus == 2
        assert result.compute.mem == 512


class TestVMOperationExecuteCreate:
    """Tests for VMOperation._execute_create()."""

    def test_execute_create_creates_vm(self, mocker):
        """_execute_create() performs the full VM creation workflow."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.name = "test-vm"
        mock_resolved.vm_id = "test-vm-id"
        mock_resolved.vm_dir = Path("/fake/vm_dir")

        # Mock VMCreateContext
        mock_ctx = mocker.MagicMock()
        mock_ctx.to_model.return_value = _make_vm("test-vm")
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateContext",
            return_value=mock_ctx,
        )

        # Mock Database (used in _execute_create)
        mock_db = mocker.MagicMock()
        mocker.patch("mvmctl.core._shared._db.Database", return_value=mock_db)

        # Mock VMRepository
        mock_repo = mocker.MagicMock()
        mock_repo.count.return_value = 0
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mock_repo,
        )

        # Mock SettingsService.resolve
        mocker.patch(
            "mvmctl.api.vm_operations.SettingsService.resolve",
            return_value="10",
        )

        # Mock SigtermContext
        mocker.patch("mvmctl.utils._system.SigtermContext")

        # Mock AuditLog
        mock_audit = mocker.patch("mvmctl.utils.auditlog.AuditLog.log")

        result = VMOperation._execute_create(
            mock_resolved, audit_action="vm.create"
        )

        assert result.status == VMStatus.RUNNING
        mock_ctx.set_resolved.assert_called_once_with(mock_resolved)
        mock_ctx.execute.assert_called_once()
        mock_repo.upsert.assert_called_once()
        mock_audit.assert_called_once_with("vm.create", context="name=test-vm")


class TestVMOperationPerformRemovalCleanup:
    """Tests for VMOperation._perform_removal_cleanup()."""

    def test_cleanup_stops_console_and_releases_ip(self, mocker):
        """_perform_removal_cleanup() stops console relay and releases IP."""
        mock_vm = _make_vm("test-vm", network_id="net-1", relay_pid=9999)
        mock_vm.id = "test-vm-id"

        mock_relay_mgr = mocker.MagicMock()
        mocker.patch(
            "mvmctl.services.console_relay.manager.ConsoleRelayManager",
            return_value=mock_relay_mgr,
        )
        mocker.patch(
            "mvmctl.core.network._repository.NetworkRepository",
        )
        mocker.patch(
            "mvmctl.core.network._service.NetworkService",
        )
        mocker.patch(
            "mvmctl.core.network._repository.LeaseRepository",
        )
        mocker.patch("subprocess.run")

        VMOperation._perform_removal_cleanup(mock_vm, "net-1")

        mock_relay_mgr.stop.assert_called_once()
