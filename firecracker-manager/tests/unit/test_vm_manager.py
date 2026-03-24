"""Tests for VM manager."""

import pytest

from fcm.core.vm_manager import VMManager
from fcm.models.vm import VMInstance, VMState


@pytest.mark.parametrize(
    "vm_name,pid,ip",
    [
        ("test-vm", 1234, "10.0.0.2"),
        ("my-vm", 5678, "10.0.0.5"),
        ("vm123", 9999, "192.168.1.10"),
    ],
)
def test_vm_manager_register(vm_manager: VMManager, vm_name: str, pid: int, ip: str):
    """register should store a VMInstance that is retrievable by name with correct attributes."""
    vm = VMInstance(
        name=vm_name,
        pid=pid,
        ip=ip,
        status=VMState.RUNNING,
    )

    vm_manager.register(vm)

    retrieved = vm_manager.get(vm_name)
    assert retrieved is not None
    assert retrieved.name == vm_name
    assert retrieved.pid == pid
    assert retrieved.ip == ip
    assert retrieved.status == VMState.RUNNING


def test_vm_manager_list(vm_manager: VMManager):
    """list_all should return all registered VMs."""
    vm_manager.register(VMInstance(name="vm1", pid=1, status=VMState.RUNNING))
    vm_manager.register(VMInstance(name="vm2", pid=2, status=VMState.STOPPED))

    vms = vm_manager.list_all()
    assert len(vms) == 2


def test_vm_manager_deregister(vm_manager: VMManager):
    """deregister should remove a VM so that get returns None for that name."""
    vm_manager.register(VMInstance(name="test-vm", pid=1234, status=VMState.RUNNING))
    assert vm_manager.get("test-vm") is not None

    vm_manager.deregister("test-vm")
    assert vm_manager.get("test-vm") is None


@pytest.mark.parametrize("vm_name", ["non-existent", "ghost-vm", "missing-123"])
def test_vm_manager_not_found(vm_manager: VMManager, vm_name: str):
    """get should return None when a VM with the given name has not been registered."""
    result = vm_manager.get(vm_name)
    assert result is None


@pytest.mark.parametrize(
    "vm_name,new_status",
    [
        ("nonexistent", VMState.STOPPED),
        ("ghost-vm", VMState.RUNNING),
        ("missing-vm", VMState.STOPPED),
    ],
)
def test_vm_manager_update_status_not_found(
    vm_manager: VMManager, vm_name: str, new_status: VMState
):
    """update_status should raise VMNotFoundError when the named VM does not exist."""
    from fcm.exceptions import VMNotFoundError

    with pytest.raises(VMNotFoundError):
        vm_manager.update_status(vm_name, new_status)
