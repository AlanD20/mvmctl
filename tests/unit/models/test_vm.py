"""Tests for VM models."""

from datetime import datetime

from mvmctl.models.vm import VMInstance, VMStatus


def test_vminstance_dataclass_structure():
    """Verify VMInstance dataclass has expected fields."""
    # Create VMInstance with all standard fields
    vm = VMInstance(
        name="test-vm",
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        pid=1234,
        status=VMStatus.RUNNING,
        created_at=datetime(2026, 1, 1, 12, 0, 0),
    )

    # Verify all expected attributes exist
    assert hasattr(vm, "name")
    assert hasattr(vm, "id")
    assert hasattr(vm, "pid")
    assert hasattr(vm, "api_socket_path")
    assert hasattr(vm, "ipv4")
    assert hasattr(vm, "mac")
    assert hasattr(vm, "network_name")
    assert hasattr(vm, "tap_device")
    assert hasattr(vm, "created_at")
    assert hasattr(vm, "status")
    assert hasattr(vm, "config")
    assert hasattr(vm, "ipv4_gateway")
    assert hasattr(vm, "subnet_mask")
    assert hasattr(vm, "nocloud_net_port")
    assert hasattr(vm, "nocloud_server_pid")
    assert hasattr(vm, "console_relay_pid")
    assert hasattr(vm, "console_socket_path")

    # Verify values
    assert vm.name == "test-vm"
    assert vm.ipv4 == "10.0.0.2"
    assert vm.mac == "02:FC:00:00:00:01"
    assert vm.pid == 1234
    assert vm.status == VMStatus.RUNNING


def test_vminstance_default_values():
    """Verify VMInstance default values."""
    vm = VMInstance(name="minimal-vm")

    # Verify defaults
    assert vm.id == ""
    assert vm.pid is None
    assert vm.api_socket_path is None
    assert vm.ipv4 is None
    assert vm.mac is None
    assert vm.network_name is None
    assert vm.tap_device is None
    assert vm.status == VMStatus.STOPPED
    assert vm.config is None
    assert vm.nocloud_net_port is None
    assert vm.nocloud_server_pid is None
    assert vm.console_relay_pid is None
    assert vm.console_socket_path is None


def test_vminstance_serialization():
    """Verify VMInstance can be serialized to dict."""
    vm = VMInstance(
        name="serializable-vm",
        ipv4="10.0.0.3",
        mac="02:FC:00:00:00:02",
        pid=5678,
        status=VMStatus.RUNNING,
        created_at=datetime(2026, 1, 1, 12, 0, 0),
    )

    data = vm.to_dict()

    # Verify dict structure
    assert data["name"] == "serializable-vm"
    assert data["ipv4"] == "10.0.0.3"
    assert data["mac"] == "02:FC:00:00:00:02"
    assert data["pid"] == 5678
    assert data["status"] == "running"
    assert "created_at" in data


def test_vminstance_deserialization():
    """Verify VMInstance can be deserialized from dict."""
    data = {
        "name": "deserialized-vm",
        "id": "abc123" + "x" * 58,
        "pid": 9999,
        "api_socket_path": None,
        "ipv4": "10.0.0.4",
        "mac": "02:FC:00:00:00:03",
        "network_name": "default",
        "tap_device": "tap0",
        "ipv4_gateway": "10.0.0.1",
        "subnet_mask": "255.255.255.0",
        "created_at": "2026-01-01T12:00:00+00:00",
        "status": "stopped",
        "config": None,
        "nocloud_net_port": None,
        "nocloud_server_pid": None,
        "console_relay_pid": None,
        "console_socket_path": None,
    }

    vm = VMInstance.from_dict(data)

    # Verify deserialized values
    assert vm.name == "deserialized-vm"
    assert vm.id == "abc123" + "x" * 58
    assert vm.pid == 9999
    assert vm.ipv4 == "10.0.0.4"
    assert vm.mac == "02:FC:00:00:00:03"
    assert vm.status == VMStatus.STOPPED


def test_vmstate_enum_values():
    """Verify VMState enum values."""
    assert VMStatus.RUNNING.value == "running"
    assert VMStatus.STOPPED.value == "stopped"
    assert VMStatus.ERROR.value == "error"
