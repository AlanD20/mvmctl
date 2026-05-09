"""Tests for VM batch creation (--count/--atomic) in VMOperation.create()."""

from __future__ import annotations

from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

from mvmctl.api.inputs._vm_create_input import VMCreateInput
from mvmctl.api.vm_operations import VMOperation
from mvmctl.exceptions import VMCreateError
from mvmctl.models import VMInstanceItem, VMStatus
from mvmctl.utils.common import CommonUtils


def _make_vm(name: str = "test-vm", **kwargs):
    return VMInstanceItem(
        name=name,
        id=f"{name}-id-" + "x" * 55,
        pid=1234,
        process_start_time=None,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="net-id-" + "x" * 55,
        tap_device=f"mvm-{name}-tap0",
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
        status=VMStatus.RUNNING,
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


class TestGenerateBatchNames:
    """Test generate_batch_names()."""

    def test_single_vm_keeps_base_name(self):
        """count=1 returns just [base_name] — no suffix."""
        names = CommonUtils.generate_batch_names("my-vm", 1)
        assert names == ["my-vm"]

    def test_batch_appends_numeric_suffix(self):
        """count=3 returns my-vm, my-vm-2, my-vm-3."""
        names = CommonUtils.generate_batch_names("my-vm", 3)
        assert names == ["my-vm", "my-vm-2", "my-vm-3"]

    def test_batch_does_not_include_index_1(self):
        """Second VM should be -2, not -1."""
        names = CommonUtils.generate_batch_names("my-vm", 2)
        assert names == ["my-vm", "my-vm-2"]

    def test_large_batch_all_unique(self):
        """All names in a batch of 10 should be unique."""
        names = CommonUtils.generate_batch_names("vm", 10)
        assert len(names) == 10
        assert len(set(names)) == 10
        assert names[0] == "vm"
        assert names[9] == "vm-10"


class TestBatchCreation:
    """Test VMOperation.create() with --count."""

    def _setup_create_mocks(self, mocker):
        """Set up common mocks for VMOperation.create()."""
        # Mock VMCreateContext
        mock_ctx = MagicMock()
        mock_ctx.vm_id = "test-vm-id"
        mock_ctx.vm_dir = Path("/fake/vm_dir")
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateContext",
            return_value=mock_ctx,
        )

        # Mock VMCreateRequest to return a resolved input
        # Use a SimpleNamespace that works with dataclasses.replace
        resolved = SimpleNamespace(
            name="test-vm",
            vm_id="test-vm-id",
            vm_dir=Path("/fake/vm_dir"),
            vcpu_count=2,
            mem_size_mib=512,
            user="mvm",
            dns_server="1.1.1.1",
            root_uid=0,
            root_gid=0,
            user_uid=1000,
            user_gid=1000,
            guest_mac_prefix="02:FC",
            network=MagicMock(),
            image=MagicMock(),
            kernel=MagicMock(),
            binary=MagicMock(),
            network_prefix_len=24,
            cloud_init_mode=MagicMock(),
            skip_ci_network_config=False,
            enable_pci=False,
            enable_console=False,
            enable_logging=True,
            enable_metrics=False,
            keep_cloud_init_iso=False,
            skip_cleanup=False,
            network_netmask="255.255.255.0",
            disk_size_bytes=2147483648,
            disk_size_mib=2048,
            lsm_flags="",
            log_level="info",
            log_filename="fc.log",
            serial_output_filename="serial.log",
            metrics_filename="metrics.log",
            api_socket_filename="fc.socket",
            pid_filename="fc.pid",
            config_filename="vm.json",
            console_socket_filename="console.sock",
            console_pid_filename="console.pid",
            cloud_init_iso_name="cloud-init.iso",
            nocloud_port_range_start=8000,
            nocloud_port_range_end=9000,
            nocloud_max_port_retries=10,
            requested_guest_ip=None,
            requested_guest_mac=None,
            nocloud_net_port=None,
            custom_user_data_path=None,
            cloud_init_iso_path=None,
            boot_args="console=ttyS0",
            ssh_keys=[],
            provisioner=MagicMock(),
            extra_drives=[],
            volumes=[],
            __dataclass_fields__=None,  # Trick dataclasses.replace
        )

        mock_request = MagicMock()
        mock_request.resolve.return_value = resolved
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateRequest",
            return_value=mock_request,
        )

        # Mock Database
        mocker.patch("mvmctl.api.vm_operations.Database")

        # Mock VMRepository for name collision check (uses batch get_by_names)
        mock_vm_repo = MagicMock()
        mock_vm_repo.get_by_names.return_value = set()  # No collisions
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mock_vm_repo,
        )

        # Mock dataclasses.replace for the batch path
        # (replace is imported inside create() method body, so patch at source)
        def _fake_replace(obj, **kwargs):
            """Return a copy of obj with updated attributes."""
            new = SimpleNamespace(**vars(obj))
            for k, v in kwargs.items():
                setattr(new, k, v)
            return new

        mocker.patch(
            "dataclasses.replace",
            side_effect=_fake_replace,
        )

        return resolved

    def test_count_one_returns_list_of_one(self, mocker):
        """--count 1 should return OperationResult with single VM in list."""
        mock_vm = _make_vm("single-vm")
        mock_execute = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._execute_create",
            return_value=mock_vm,
        )
        self._setup_create_mocks(mocker)

        result = VMOperation.create(
            VMCreateInput(name="single-vm", ssh_keys=[], count=1)
        )

        assert result.status == "success"
        assert result.code == "vm.created"
        assert result.item is not None
        assert len(result.item) == 1
        assert result.item[0].name == "single-vm"
        mock_execute.assert_called_once()

    def test_batch_three_creates_three_vms(self, mocker):
        """--count 3 should create and return 3 VMs."""
        vm1 = _make_vm("test-vm")
        vm2 = _make_vm("test-vm-2")
        vm3 = _make_vm("test-vm-3")
        mock_execute = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._execute_create",
            side_effect=[vm1, vm2, vm3],
        )
        self._setup_create_mocks(mocker)

        # Mock HashGenerator and CacheUtils for batch path
        mocker.patch(
            "mvmctl.api.vm_operations.HashGenerator.vm",
            side_effect=lambda name, ts: f"{name}-hash",
        )
        mocker.patch(
            "mvmctl.api.vm_operations.CacheUtils.get_vm_dir",
            return_value=Path("/fake/vm_dir"),
        )

        result = VMOperation.create(
            VMCreateInput(name="test-vm", ssh_keys=[], count=3)
        )

        assert result.status == "success"
        assert result.code == "vm.created_batch"
        assert result.item is not None
        assert len(result.item) == 3
        assert mock_execute.call_count == 3

    def test_atomic_failure_removes_created_vms(self, mocker):
        """--atomic: failure in VM 3 should remove VMs 1 & 2."""
        vm1 = _make_vm("test-vm")
        vm2 = _make_vm("test-vm-2")
        mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._execute_create",
            side_effect=[vm1, vm2, VMCreateError("VM 3 failed")],
        )
        self._setup_create_mocks(mocker)

        mocker.patch(
            "mvmctl.api.vm_operations.HashGenerator.vm",
            side_effect=lambda name, ts: f"{name}-hash",
        )
        mocker.patch(
            "mvmctl.api.vm_operations.CacheUtils.get_vm_dir",
            return_value=Path("/fake/vm_dir"),
        )
        mock_remove = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation.remove",
        )

        result = VMOperation.create(
            VMCreateInput(name="test-vm", ssh_keys=[], count=3, atomic=True)
        )

        assert result.status == "error"
        assert result.code == "vm.atomic_failed"
        assert "atomic" in result.message.lower()
        # Should have called remove() for each successfully created VM (2 removals)
        assert mock_remove.call_count == 2

    def test_non_atomic_continues_after_failure(self, mocker):
        """Without --atomic, failure in VM 2 should not remove VM 1."""
        vm1 = _make_vm("test-vm")
        mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._execute_create",
            side_effect=[vm1, VMCreateError("VM 2 failed")],
        )
        self._setup_create_mocks(mocker)

        mocker.patch(
            "mvmctl.api.vm_operations.HashGenerator.vm",
            side_effect=lambda name, ts: f"{name}-hash",
        )
        mocker.patch(
            "mvmctl.api.vm_operations.CacheUtils.get_vm_dir",
            return_value=Path("/fake/vm_dir"),
        )
        mock_remove = mocker.patch(
            "mvmctl.api.vm_operations.VMOperation.remove",
        )

        result = VMOperation.create(
            VMCreateInput(name="test-vm", ssh_keys=[], count=2, atomic=False)
        )

        # Non-atomic with partial success returns status="warning"
        assert result.status == "warning"
        # Should NOT have called remove() — non-atomic continues without rollback
        assert mock_remove.call_count == 0

    def test_all_fail_returns_error(self, mocker):
        """When all VMs fail, return error status."""
        mocker.patch(
            "mvmctl.api.vm_operations.VMOperation._execute_create",
            side_effect=VMCreateError("all failed"),
        )
        self._setup_create_mocks(mocker)

        mocker.patch(
            "mvmctl.api.vm_operations.HashGenerator.vm",
            side_effect=lambda name, ts: f"{name}-hash",
        )
        mocker.patch(
            "mvmctl.api.vm_operations.CacheUtils.get_vm_dir",
            return_value=Path("/fake/vm_dir"),
        )

        result = VMOperation.create(
            VMCreateInput(name="test-vm", ssh_keys=[], count=3)
        )

        assert result.status == "error"
        assert result.code == "vm.create_failure"
        assert result.item is None or result.item == []

    def test_name_collision_returns_error(self, mocker):
        """Pre-allocate collision check: existing name should return error."""
        self._setup_create_mocks(mocker)

        # Make VMRepository.get_by_names return "test-vm-2" as already existing
        mock_vm_repo = MagicMock()
        mock_vm_repo.get_by_names.return_value = {"test-vm-2"}
        mocker.patch(
            "mvmctl.api.vm_operations.VMRepository",
            return_value=mock_vm_repo,
        )

        result = VMOperation.create(
            VMCreateInput(name="test-vm", ssh_keys=[], count=3)
        )

        assert result.status == "error"
        assert result.code == "vm.name_collision"
        assert "already exist" in result.message


class TestBatchValidationViaCreate:
    """Test validation errors surface through create()."""

    def test_count_gt_one_with_ip_returns_error(self, mocker):
        """--count 3 with --ip should return error result."""
        mock_ctx = MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateContext",
            return_value=mock_ctx,
        )
        mock_request = MagicMock()
        mock_request.resolve.side_effect = VMCreateError(
            "Cannot specify --ip with --count > 1"
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.vm_operations.Database")

        result = VMOperation.create(
            VMCreateInput(
                name="test-vm",
                ssh_keys=[],
                count=3,
                requested_guest_ip="10.0.0.5",
            )
        )

        assert result.status == "error"
        assert "Cannot specify --ip" in result.message

    def test_count_gt_one_with_mac_returns_error(self, mocker):
        """--count 3 with --mac should return error result."""
        mock_ctx = MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateContext",
            return_value=mock_ctx,
        )
        mock_request = MagicMock()
        mock_request.resolve.side_effect = VMCreateError(
            "Cannot specify --mac with --count > 1"
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.vm_operations.Database")

        result = VMOperation.create(
            VMCreateInput(
                name="test-vm",
                ssh_keys=[],
                count=3,
                requested_guest_mac="02:FC:00:00:00:05",
            )
        )

        assert result.status == "error"
        assert "Cannot specify --mac" in result.message

    def test_count_zero_returns_error(self, mocker):
        """--count 0 returns error via VMCreateRequest."""
        mock_ctx = MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateContext",
            return_value=mock_ctx,
        )
        mock_request = MagicMock()
        mock_request.resolve.side_effect = VMCreateError(
            "--count must be at least 1"
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.vm_operations.Database")

        result = VMOperation.create(
            VMCreateInput(name="test-vm", ssh_keys=[], count=0)
        )

        assert result.status == "error"
        assert "count must be at least 1" in result.message

    def test_count_negative_returns_error(self, mocker):
        """--count -1 returns error via VMCreateRequest."""
        mock_ctx = MagicMock()
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateContext",
            return_value=mock_ctx,
        )
        mock_request = MagicMock()
        mock_request.resolve.side_effect = VMCreateError(
            "--count must be at least 1"
        )
        mocker.patch(
            "mvmctl.api.vm_operations.VMCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.vm_operations.Database")

        result = VMOperation.create(
            VMCreateInput(name="test-vm", ssh_keys=[], count=-1)
        )

        assert result.status == "error"
        assert "count must be at least 1" in result.message


class TestVMCreateRequestCountValidation:
    """Direct tests for VMCreateRequest validation logic for --count.

    These tests mock sub-resolvers to exercise the actual validation
    code path through ensure_validate().
    """

    def _mock_resolvers(self, mocker):
        """Set up all sub-resolvers needed by VMCreateRequest.resolve()."""
        mocker.patch(
            "mvmctl.api.inputs._vm_create_input.VMValidator.validate_name"
        )
        mocker.patch("mvmctl.api.inputs._vm_create_input.NetworkValidator")

        # Mock the DB-dependent resolvers
        mock_image = MagicMock()
        mock_image.id = "img-id"
        mock_image.minimum_rootfs_size_mib = 10
        mock_image.fs_type = "ext4"
        mock_image.fs_uuid = "abc-def"
        mock_image_resolver = MagicMock()
        mock_image_resolver.get_default.return_value = mock_image
        mocker.patch(
            "mvmctl.api.inputs._vm_create_input.ImageResolver",
            return_value=mock_image_resolver,
        )

        mock_kernel = MagicMock()
        mock_kernel.id = "kern-id"
        mock_kernel.resolved_path = MagicMock()
        mock_kernel.resolved_path.exists.return_value = True
        mock_kernel_resolver = MagicMock()
        mock_kernel_resolver.get_default.return_value = mock_kernel
        mocker.patch(
            "mvmctl.api.inputs._vm_create_input.KernelResolver",
            return_value=mock_kernel_resolver,
        )

        mock_network = MagicMock()
        mock_network.id = "net-id"
        mock_network.subnet = "10.0.0.0/24"
        mock_network.ipv4_gateway = "10.0.0.1"
        mock_network.bridge = "mvmbr0"
        mock_network.nat_enabled = False
        mock_network.nat_gateways = ""
        mock_network.nat_gateways_list = []
        mock_network_resolver = MagicMock()
        mock_network_resolver.get_default.return_value = mock_network
        mocker.patch(
            "mvmctl.api.inputs._vm_create_input.NetworkResolver",
            return_value=mock_network_resolver,
        )

        mock_binary = MagicMock()
        mock_binary.id = "bin-id"
        mock_binary.resolved_path = MagicMock()
        mock_binary.resolved_path.exists.return_value = True
        mock_binary.resolved_path.__fspath__ = lambda: "/fake/bin"
        mock_binary_resolver = MagicMock()
        mock_binary_resolver.resolve.return_value = mock_binary
        mocker.patch(
            "mvmctl.api.inputs._vm_create_input.BinaryResolver",
            return_value=mock_binary_resolver,
        )
        mock_binary_svc = MagicMock()
        mock_binary_svc.get_default_firecracker.return_value = mock_binary
        mocker.patch(
            "mvmctl.api.inputs._vm_create_input.BinaryService",
            return_value=mock_binary_svc,
        )

        mocker.patch("mvmctl.api.inputs._vm_create_input.KeyResolver")
        mocker.patch(
            "mvmctl.api.inputs._vm_create_input.SettingsService.resolve",
            return_value=42,
        )
        # os.access is called on the binary resolved_path in ensure_validate
        mocker.patch("os.access", return_value=True)
        # LoopMountManager is imported inside _resolve_provisioner, not at module level
        mocker.patch(
            "mvmctl.core._shared._loopmount.LoopMountManager.is_binary_available",
            return_value=True,
        )
        return mock_image, mock_kernel, mock_network, mock_binary

    def test_count_zero_raises_in_validate(self, mocker):
        """VMCreateRequest should raise VMCreateError for count=0."""
        from mvmctl.api.inputs._vm_create_input import VMCreateRequest
        from mvmctl.core._shared import Database

        self._mock_resolvers(mocker)

        inputs = VMCreateInput(
            name="test-vm",
            ssh_keys=[],
            count=0,
            vcpu_count=2,
            mem_size_mib=512,
            boot_args="console=ttyS0",
            lsm_flags="",
        )
        request = VMCreateRequest(
            vm_id="test-id",
            vm_dir=MagicMock(),
            inputs=inputs,
            db=Database(),
        )
        with pytest.raises(VMCreateError, match="--count must be at least 1"):
            request.resolve()

    def test_count_negative_raises_in_validate(self, mocker):
        """VMCreateRequest should raise VMCreateError for count=-1."""
        from mvmctl.api.inputs._vm_create_input import VMCreateRequest
        from mvmctl.core._shared import Database

        self._mock_resolvers(mocker)

        inputs = VMCreateInput(
            name="test-vm",
            ssh_keys=[],
            count=-1,
            vcpu_count=2,
            mem_size_mib=512,
            boot_args="console=ttyS0",
            lsm_flags="",
        )
        request = VMCreateRequest(
            vm_id="test-id",
            vm_dir=MagicMock(),
            inputs=inputs,
            db=Database(),
        )
        with pytest.raises(VMCreateError, match="--count must be at least 1"):
            request.resolve()

    def test_count_one_with_ip_passes(self, mocker):
        """count=1 with ip should pass validation (no batch conflict)."""
        from mvmctl.api.inputs._vm_create_input import VMCreateRequest
        from mvmctl.core._shared import Database

        self._mock_resolvers(mocker)

        inputs = VMCreateInput(
            name="test-vm",
            ssh_keys=[],
            count=1,
            requested_guest_ip="10.0.0.5",
            vcpu_count=2,
            mem_size_mib=512,
            boot_args="console=ttyS0",
            lsm_flags="",
        )
        request = VMCreateRequest(
            vm_id="test-id",
            vm_dir=MagicMock(),
            inputs=inputs,
            db=Database(),
        )
        # Should resolve without raising
        resolved = request.resolve()
        assert resolved is not None
        assert resolved.requested_guest_ip == "10.0.0.5"

    def test_count_gt_one_with_ip_raises(self, mocker):
        """count=3 with ip should raise VMCreateError."""
        from mvmctl.api.inputs._vm_create_input import VMCreateRequest
        from mvmctl.core._shared import Database

        self._mock_resolvers(mocker)

        inputs = VMCreateInput(
            name="test-vm",
            ssh_keys=[],
            count=3,
            requested_guest_ip="10.0.0.5",
            vcpu_count=2,
            mem_size_mib=512,
            boot_args="console=ttyS0",
            lsm_flags="",
        )
        request = VMCreateRequest(
            vm_id="test-id",
            vm_dir=MagicMock(),
            inputs=inputs,
            db=Database(),
        )
        with pytest.raises(
            VMCreateError, match="Cannot specify --ip with --count > 1"
        ):
            request.resolve()

    def test_count_gt_one_with_mac_raises(self, mocker):
        """count=3 with mac should raise VMCreateError."""
        from mvmctl.api.inputs._vm_create_input import VMCreateRequest
        from mvmctl.core._shared import Database

        self._mock_resolvers(mocker)

        inputs = VMCreateInput(
            name="test-vm",
            ssh_keys=[],
            count=3,
            requested_guest_mac="02:FC:00:00:00:05",
            vcpu_count=2,
            mem_size_mib=512,
            boot_args="console=ttyS0",
            lsm_flags="",
        )
        request = VMCreateRequest(
            vm_id="test-id",
            vm_dir=MagicMock(),
            inputs=inputs,
            db=Database(),
        )
        with pytest.raises(
            VMCreateError, match="Cannot specify --mac with --count > 1"
        ):
            request.resolve()

    def test_count_with_default_not_set_passes(self, mocker):
        """count=None (default) should act like count=1."""
        from mvmctl.api.inputs._vm_create_input import VMCreateRequest
        from mvmctl.core._shared import Database

        self._mock_resolvers(mocker)

        inputs = VMCreateInput(
            name="test-vm",
            ssh_keys=[],
            vcpu_count=2,
            mem_size_mib=512,
            boot_args="console=ttyS0",
            lsm_flags="",
        )
        request = VMCreateRequest(
            vm_id="test-id",
            vm_dir=MagicMock(),
            inputs=inputs,
            db=Database(),
        )
        # Should resolve without raising
        resolved = request.resolve()
        assert resolved is not None
