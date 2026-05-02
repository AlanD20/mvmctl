"""Tests for VMInput, VMRequest, and ResolvedVMInput."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.api.inputs._vm_input import VMInput, VMRequest, ResolvedVMInput
from mvmctl.exceptions import VMNotFoundError
from mvmctl.models import VMInstanceItem


def _make_vm(name="test-vm", vm_id="id-001") -> VMInstanceItem:
    return VMInstanceItem(
        name=name,
        id=vm_id,
        pid=1234,
        process_start_time=None,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="net-default",
        tap_device="tap0",
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
        status="running",
        config_path="vm.json",
        kernel_id="kern-id",
        image_id="img-id",
        binary_id="bin-id",
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
    )


class TestVMInput:
    """Tests for VMInput dataclass."""

    def test_defaults(self):
        inp = VMInput()
        assert inp.identifiers == []
        assert inp.force is None

    def test_with_identifiers(self):
        inp = VMInput(identifiers=["vm1", "vm2"], force=True)
        assert inp.identifiers == ["vm1", "vm2"]
        assert inp.force is True


class TestResolvedVMInput:
    """Tests for ResolvedVMInput frozen dataclass."""

    def test_frozen(self):
        resolved = ResolvedVMInput(vms=[_make_vm()], force=False)
        assert len(resolved.vms) == 1
        assert resolved.force is False

    def test_cannot_modify(self):
        resolved = ResolvedVMInput(vms=[], force=False)
        with pytest.raises(AttributeError):
            resolved.force = True  # type: ignore[misc]


class TestVMRequest:
    """Tests for VMRequest resolution."""

    def test_resolve_by_name(self, mocker):
        vm = _make_vm(name="myvm")
        mocker.patch("mvmctl.api.inputs._vm_input.Database")
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = MagicMock(
            items=[vm], errors=[]
        )
        mocker.patch(
            "mvmctl.api.inputs._vm_input.VMResolver",
            return_value=mock_resolver,
        )

        req = VMRequest(inputs=VMInput(identifiers=["myvm"]))
        result = req.resolve()
        assert len(result.vms) == 1
        assert result.vms[0].name == "myvm"

    def test_resolve_raises_on_all_errors(self, mocker):
        mocker.patch("mvmctl.api.inputs._vm_input.Database")
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = MagicMock(
            items=[], errors=["vm1: not found"]
        )
        mocker.patch(
            "mvmctl.api.inputs._vm_input.VMResolver",
            return_value=mock_resolver,
        )

        req = VMRequest(inputs=VMInput(identifiers=["vm1"]))
        with pytest.raises(VMNotFoundError, match="Could not resolve"):
            req.resolve()

    def test_resolve_multiple_vms(self, mocker):
        vms = [_make_vm(name="vm1", vm_id="id1"),
               _make_vm(name="vm2", vm_id="id2")]
        mocker.patch("mvmctl.api.inputs._vm_input.Database")
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = MagicMock(
            items=vms, errors=[]
        )
        mocker.patch(
            "mvmctl.api.inputs._vm_input.VMResolver",
            return_value=mock_resolver,
        )

        req = VMRequest(inputs=VMInput(identifiers=["vm1", "vm2"]))
        result = req.resolve()
        assert len(result.vms) == 2

    def test_force_flag(self, mocker):
        vm = _make_vm()
        mocker.patch("mvmctl.api.inputs._vm_input.Database")
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = MagicMock(
            items=[vm], errors=[]
        )
        mocker.patch(
            "mvmctl.api.inputs._vm_input.VMResolver",
            return_value=mock_resolver,
        )

        req = VMRequest(inputs=VMInput(identifiers=["vm1"], force=True))
        result = req.resolve()
        assert result.force is True


class TestVMRequestValidation:
    """Tests for VMRequest identifier validation."""

    def test_invalid_mac_raises(self, mocker):
        mocker.patch("mvmctl.api.inputs._vm_input.Database")
        req = VMRequest(inputs=VMInput(identifiers=["not:a:mac"]))
        with pytest.raises(Exception):
            req.resolve()

    def test_result_property_none_before_resolve(self, mocker):
        mocker.patch("mvmctl.api.inputs._vm_input.Database")
        req = VMRequest(inputs=VMInput(identifiers=["vm1"]))
        assert req.result is None

    def test_result_property_after_resolve(self, mocker):
        vm = _make_vm()
        mocker.patch("mvmctl.api.inputs._vm_input.Database")
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = MagicMock(
            items=[vm], errors=[]
        )
        mocker.patch(
            "mvmctl.api.inputs._vm_input.VMResolver",
            return_value=mock_resolver,
        )

        req = VMRequest(inputs=VMInput(identifiers=["vm1"]))
        req.resolve()
        assert req.result is not None
