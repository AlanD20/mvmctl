"""Tests for VM manager."""

import tempfile
from pathlib import Path

from fcm.core.vm_manager import VMManager
from fcm.models.vm import VMInstance, VMState


def test_vm_manager_register():
    """Test VM registration."""
    with tempfile.TemporaryDirectory() as tmp:
        manager = VMManager(Path(tmp))

        vm = VMInstance(
            name="test-vm",
            pid=1234,
            ip="10.0.0.2",
            status=VMState.RUNNING,
        )

        manager.register(vm)

        retrieved = manager.get("test-vm")
        assert retrieved is not None
        assert retrieved.name == "test-vm"
        assert retrieved.pid == 1234
        assert retrieved.ip == "10.0.0.2"
        assert retrieved.status == VMState.RUNNING


def test_vm_manager_list():
    """Test VM listing."""
    with tempfile.TemporaryDirectory() as tmp:
        manager = VMManager(Path(tmp))

        manager.register(VMInstance(name="vm1", pid=1, status=VMState.RUNNING))
        manager.register(VMInstance(name="vm2", pid=2, status=VMState.STOPPED))

        vms = manager.list_all()
        assert len(vms) == 2


def test_vm_manager_deregister():
    """Test VM deregistration."""
    with tempfile.TemporaryDirectory() as tmp:
        manager = VMManager(Path(tmp))

        manager.register(VMInstance(name="test-vm", pid=1234, status=VMState.RUNNING))
        assert manager.get("test-vm") is not None

        manager.deregister("test-vm")
        assert manager.get("test-vm") is None


def test_vm_manager_not_found():
    """Test getting non-existent VM."""
    with tempfile.TemporaryDirectory() as tmp:
        manager = VMManager(Path(tmp))

        result = manager.get("non-existent")
        assert result is None


def test_vm_manager_update_status_not_found():
    """Test update_status raises VMNotFoundError for nonexistent VM."""
    import pytest
    from fcm.exceptions import VMNotFoundError

    with tempfile.TemporaryDirectory() as tmp:
        manager = VMManager(Path(tmp))

        with pytest.raises(VMNotFoundError):
            manager.update_status("nonexistent", VMState.STOPPED)
