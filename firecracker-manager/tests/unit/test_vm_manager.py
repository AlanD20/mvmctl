"""Tests for VM manager."""

from pathlib import Path

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


def test_vm_manager_count_vms(vm_manager: VMManager):
    """count_vms should return the number of VMs without loading full metadata."""
    assert vm_manager.count_vms() == 0

    vm_manager.register(VMInstance(name="vm1", pid=1, status=VMState.RUNNING))
    assert vm_manager.count_vms() == 1

    vm_manager.register(VMInstance(name="vm2", pid=2, status=VMState.STOPPED))
    assert vm_manager.count_vms() == 2

    registered = vm_manager.get("vm1")
    assert registered is not None
    vm_manager.deregister(registered.id)
    assert vm_manager.count_vms() == 1


def test_vm_manager_deregister(vm_manager: VMManager):
    vm = VMInstance(name="test-vm", pid=1234, status=VMState.RUNNING)
    vm_manager.register(vm)
    registered = vm_manager.get("test-vm")
    assert registered is not None

    vm_manager.deregister(registered.id)
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


def test_vm_manager_find_by_short_id(vm_manager: VMManager):
    vm = VMInstance(name="myvm", pid=1, status=VMState.RUNNING)
    vm_manager.register(vm)
    registered = vm_manager.get("myvm")
    assert registered is not None
    short_id = registered.id[:6]
    matches = vm_manager.find_by_short_id(short_id)
    assert len(matches) == 1
    assert matches[0].name == "myvm"


def test_vm_manager_find_by_short_id_no_match(vm_manager: VMManager):
    assert vm_manager.find_by_short_id("zzzzzz") == []


def test_vm_manager_get_by_short_id_unique(vm_manager: VMManager):
    vm = VMInstance(name="uniquevm", pid=2, status=VMState.RUNNING)
    vm_manager.register(vm)
    registered = vm_manager.get("uniquevm")
    assert registered is not None
    result = vm_manager.get_by_short_id(registered.id[:6])
    assert result is not None
    assert result.name == "uniquevm"


def test_vm_manager_get_by_name_multiple(vm_manager: VMManager):
    vm1 = VMInstance(name="dup", pid=1, status=VMState.RUNNING)
    vm2 = VMInstance(name="dup", pid=2, status=VMState.RUNNING)
    vm_manager.register(vm1)
    vm_manager.register(vm2)
    results = vm_manager.get_by_name("dup")
    assert len(results) == 2


def test_vm_manager_update_status_success(vm_manager: VMManager):
    vm = VMInstance(name="statusvm", pid=3, status=VMState.RUNNING)
    vm_manager.register(vm)
    vm_manager.update_status("statusvm", VMState.STOPPED)
    updated = vm_manager.get("statusvm")
    assert updated is not None
    assert updated.status == VMState.STOPPED


def test_vm_manager_migration(tmp_path: Path):
    import json
    from pathlib import Path
    from datetime import datetime, timezone

    vms_dir = tmp_path / "vms"
    vms_dir.mkdir()
    state_file = vms_dir / "state.json"
    state_file.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "vms": {
                    "mylegacyvm": {
                        "pid": 42,
                        "socket_path": None,
                        "ip": "10.0.0.2",
                        "mac": "02:FC:00:00:00:01",
                        "network_name": "default",
                        "tap_device": "fcm-tap0",
                        "created_at": datetime.now(tz=timezone.utc).isoformat(),
                        "status": "running",
                    }
                },
            }
        )
    )
    mgr = VMManager(vms_dir)
    vms = mgr.list_all()
    assert len(vms) == 1
    assert vms[0].name == "mylegacyvm"
    assert len(vms[0].id) == 64
