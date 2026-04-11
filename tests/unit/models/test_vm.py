"""Tests for VM models."""

from datetime import datetime, timezone

from mvmctl.models.vm import VMInstance, VMStatus


def test_vminstance_dataclass_structure():
    """Verify VMInstance dataclass has expected fields."""
    # Create VMInstance with all required fields (no defaults allowed)
    now = datetime.now(tz=timezone.utc)
    vm = VMInstance(
        name="test-vm",
        id="abc123" + "x" * 58,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        pid=1234,
        network_id="net123",
        tap_device="tap0",
        created_at=now,
        updated_at=now,
        status=VMStatus.RUNNING,
        rootfs_suffix=".ext4",
        kernel_id="kernel123",
        image_id="image123",
        binary_id="binary123",
        disk_size_mib=2048,
    )

    # Verify all expected attributes exist
    assert hasattr(vm, "name")
    assert hasattr(vm, "id")
    assert hasattr(vm, "pid")
    assert hasattr(vm, "api_socket_path")
    assert hasattr(vm, "ipv4")
    assert hasattr(vm, "mac")
    assert hasattr(vm, "network_id")
    assert hasattr(vm, "tap_device")
    assert hasattr(vm, "created_at")
    assert hasattr(vm, "updated_at")
    assert hasattr(vm, "status")
    assert hasattr(vm, "config")
    assert hasattr(vm, "ipv4_gateway")
    assert hasattr(vm, "subnet_mask")
    assert hasattr(vm, "nocloud_net_port")
    assert hasattr(vm, "nocloud_server_pid")
    assert hasattr(vm, "console_relay_pid")
    assert hasattr(vm, "console_socket_path")
    assert hasattr(vm, "rootfs_suffix")
    assert hasattr(vm, "kernel_id")
    assert hasattr(vm, "image_id")
    assert hasattr(vm, "binary_id")
    assert hasattr(vm, "disk_size_mib")

    # Verify values
    assert vm.name == "test-vm"
    assert vm.id == "abc123" + "x" * 58
    assert vm.ipv4 == "10.0.0.2"
    assert vm.mac == "02:FC:00:00:00:01"
    assert vm.pid == 1234
    assert vm.network_id == "net123"
    assert vm.tap_device == "tap0"
    assert vm.status == VMStatus.RUNNING
    assert vm.rootfs_suffix == ".ext4"
    assert vm.kernel_id == "kernel123"
    assert vm.image_id == "image123"
    assert vm.binary_id == "binary123"
    assert vm.disk_size_mib == 2048


def test_vminstance_explicit_values():
    """Verify VMInstance stores explicit values correctly (no defaults)."""
    now = datetime.now(tz=timezone.utc)
    vm = VMInstance(
        name="explicit-vm",
        id="def456" + "y" * 58,
        ipv4="10.0.0.5",
        mac="02:FC:00:00:00:04",
        pid=5678,
        network_id="net456",
        tap_device="tap1",
        created_at=now,
        updated_at=now,
        status=VMStatus.STOPPED,
        rootfs_suffix=".btrfs",
        kernel_id="kernel456",
        image_id="image456",
        binary_id="binary456",
        disk_size_mib=4096,
        api_socket_path=None,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
        config=None,
        nocloud_net_port=None,
        nocloud_server_pid=None,
        console_relay_pid=None,
        console_socket_path=None,
    )

    # Verify explicit values are stored correctly
    assert vm.name == "explicit-vm"
    assert vm.id == "def456" + "y" * 58
    assert vm.ipv4 == "10.0.0.5"
    assert vm.mac == "02:FC:00:00:00:04"
    assert vm.pid == 5678
    assert vm.network_id == "net456"
    assert vm.tap_device == "tap1"
    assert vm.status == VMStatus.STOPPED
    assert vm.rootfs_suffix == ".btrfs"
    assert vm.kernel_id == "kernel456"
    assert vm.image_id == "image456"
    assert vm.binary_id == "binary456"
    assert vm.disk_size_mib == 4096
    assert vm.api_socket_path is None
    assert vm.ipv4_gateway == "10.0.0.1"
    assert vm.subnet_mask == "255.255.255.0"
    assert vm.config is None
    assert vm.nocloud_net_port is None
    assert vm.nocloud_server_pid is None
    assert vm.console_relay_pid is None
    assert vm.console_socket_path is None


def test_vminstance_serialization():
    """Verify VMInstance can be serialized to dict."""
    now = datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc)
    vm = VMInstance(
        name="serializable-vm",
        id="ghi789" + "z" * 58,
        ipv4="10.0.0.3",
        mac="02:FC:00:00:00:02",
        pid=5678,
        network_id="net789",
        tap_device="tap2",
        created_at=now,
        updated_at=now,
        status=VMStatus.RUNNING,
        rootfs_suffix=".ext4",
        kernel_id="kernel789",
        image_id="image789",
        binary_id="binary789",
        disk_size_mib=2048,
    )

    data = vm.to_dict()

    assert data["name"] == "serializable-vm"
    assert data["id"] == "ghi789" + "z" * 58
    assert data["ipv4"] == "10.0.0.3"
    assert data["mac"] == "02:FC:00:00:00:02"
    assert data["pid"] == 5678
    assert data["network_id"] == "net789"
    assert data["tap_device"] == "tap2"
    assert data["status"] == "running"
    assert data["rootfs_suffix"] == ".ext4"
    assert data["kernel_id"] == "kernel789"
    assert data["image_id"] == "image789"
    assert data["binary_id"] == "binary789"
    assert data["disk_size_mib"] == 2048
    assert "created_at" in data
    assert "updated_at" in data


def test_vminstance_deserialization():
    """Verify VMInstance can be deserialized from dict."""
    data = {
        "name": "deserialized-vm",
        "id": "abc123" + "x" * 58,
        "pid": 9999,
        "api_socket_path": None,
        "ipv4": "10.0.0.4",
        "mac": "02:FC:00:00:00:03",
        "network_id": "net999",
        "tap_device": "tap0",
        "ipv4_gateway": "10.0.0.1",
        "subnet_mask": "255.255.255.0",
        "created_at": "2026-01-01T12:00:00+00:00",
        "updated_at": "2026-01-02T12:00:00+00:00",
        "status": "stopped",
        "config": None,
        "nocloud_net_port": None,
        "nocloud_server_pid": None,
        "console_relay_pid": None,
        "console_socket_path": None,
        "rootfs_suffix": ".ext4",
        "kernel_id": "kernel999",
        "image_id": "image999",
        "binary_id": "binary999",
        "disk_size_mib": 2048,
    }

    vm = VMInstance.from_dict(data)

    assert vm.name == "deserialized-vm"
    assert vm.id == "abc123" + "x" * 58
    assert vm.pid == 9999
    assert vm.ipv4 == "10.0.0.4"
    assert vm.mac == "02:FC:00:00:00:03"
    assert vm.network_id == "net999"
    assert vm.tap_device == "tap0"
    assert vm.status == VMStatus.STOPPED
    assert vm.rootfs_suffix == ".ext4"
    assert vm.kernel_id == "kernel999"
    assert vm.image_id == "image999"
    assert vm.binary_id == "binary999"
    assert vm.disk_size_mib == 2048


def test_vmstate_enum_values():
    """Verify VMState enum values."""
    assert VMStatus.RUNNING.value == "running"
    assert VMStatus.STOPPED.value == "stopped"
    assert VMStatus.ERROR.value == "error"
