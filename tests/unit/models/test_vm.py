"""Tests for VM data models — VMInstanceItem, VMStatus, VMInspectInfo.

Verifies:
- VMInstanceItem dataclass structure — all fields exist, no hardcoded defaults
- VMStatus enum members and values
- VMInspectInfo structure
- ConsoleInfo and ConsoleState dataclasses
"""

from __future__ import annotations

from mvmctl.models.vm import (
    ConsoleInfo,
    ConsoleState,
    VMInspectInfo,
    VMInstanceItem,
    VMStatus,
)


class TestVMStatus:
    """Tests for VMStatus StrEnum."""

    def test_enum_values(self) -> None:
        """Verify all expected enum members exist with correct auto() values."""
        expected = {
            "STARTING": "starting",
            "RUNNING": "running",
            "PAUSED": "paused",
            "STOPPING": "stopping",
            "STOPPED": "stopped",
            "CRASHED": "crashed",
            "ERROR": "error",
        }
        for name, value in expected.items():
            member = getattr(VMStatus, name)
            assert member.value == value
            assert member.name == name

    def test_from_string(self) -> None:
        """Verify enum can be constructed from string value."""
        assert VMStatus("running") == VMStatus.RUNNING
        assert VMStatus("stopped") == VMStatus.STOPPED
        assert VMStatus("error") == VMStatus.ERROR

    def test_is_str_enum(self) -> None:
        """VMStatus values should be usable as strings."""
        assert str(VMStatus.RUNNING) == "running"
        assert VMStatus.RUNNING.value == "running"


class TestVMInstanceItem:
    """Tests for VMInstanceItem dataclass."""

    NOW = "2026-04-02T10:00:00Z"

    def _make_default(self, **overrides: object) -> VMInstanceItem:
        """Create a minimal VMInstanceItem."""
        base = {
            "id": "abc123" + "x" * 58,
            "name": "test-vm",
            "status": "running",
            "pid": 1234,
            "ipv4": "10.0.0.2",
            "mac": "02:FC:00:00:00:01",
            "network_id": "net123",
            "tap_device": "tap0",
            "image_id": "image123",
            "kernel_id": "kernel123",
            "binary_id": "binary123",
            "api_socket_path": "/tmp/vm.sock",
            "config_path": "/tmp/vm.json",
            "cloud_init_mode": "nocloud",
            "vcpu_count": 2,
            "mem_size_mib": 1024,
            "disk_size_mib": 2048,
            "rootfs_path": "/cache/vms/test-vm/rootfs.ext4",
            "rootfs_suffix": ".ext4",
            "pci_enabled": False,
            "nested_virt": False,
            "enable_logging": True,
            "enable_metrics": False,
            "enable_console": True,
            "created_at": self.NOW,
            "updated_at": self.NOW,
        }
        base.update(overrides)
        return VMInstanceItem(**base)

    def test_dataclass_structure(self) -> None:
        """Verify all expected attributes exist."""
        vm = self._make_default()
        assert hasattr(vm, "name")
        assert hasattr(vm, "id")
        assert hasattr(vm, "pid")
        assert hasattr(vm, "status")
        assert hasattr(vm, "ipv4")
        assert hasattr(vm, "mac")
        assert hasattr(vm, "network_id")
        assert hasattr(vm, "tap_device")
        assert hasattr(vm, "image_id")
        assert hasattr(vm, "kernel_id")
        assert hasattr(vm, "binary_id")
        assert hasattr(vm, "api_socket_path")
        assert hasattr(vm, "config_path")
        assert hasattr(vm, "cloud_init_mode")
        assert hasattr(vm, "vcpu_count")
        assert hasattr(vm, "mem_size_mib")
        assert hasattr(vm, "disk_size_mib")
        assert hasattr(vm, "rootfs_path")
        assert hasattr(vm, "rootfs_suffix")
        assert hasattr(vm, "pci_enabled")
        assert hasattr(vm, "enable_logging")
        assert hasattr(vm, "enable_metrics")
        assert hasattr(vm, "enable_console")
        assert hasattr(vm, "created_at")
        assert hasattr(vm, "updated_at")

    def test_required_fields(self) -> None:
        vm = self._make_default()
        assert vm.name == "test-vm"
        assert vm.id == "abc123" + "x" * 58
        assert vm.ipv4 == "10.0.0.2"
        assert vm.mac == "02:FC:00:00:00:01"
        assert vm.pid == 1234
        assert vm.status == "running"
        assert vm.vcpu_count == 2
        assert vm.mem_size_mib == 1024
        assert vm.disk_size_mib == 2048
        assert vm.rootfs_suffix == ".ext4"

    def test_optional_fields_default_to_none(self) -> None:
        vm = self._make_default()
        assert vm.relay_socket_path is None
        assert vm.process_start_time is None
        assert vm.nocloud_net_port is None
        assert vm.nocloud_net_pid is None
        assert vm.relay_pid is None
        assert vm.exit_code is None
        assert vm.log_path is None
        assert vm.serial_output_path is None
        assert vm.lsm_flags is None
        assert vm.boot_args is None

    def test_vm_dir_property(self) -> None:
        vm = self._make_default()
        r = vm.vm_dir
        assert r.name == vm.id

    def test_equality(self) -> None:
        vm1 = self._make_default()
        vm2 = self._make_default()
        assert vm1 == vm2

    def test_inequality(self) -> None:
        vm1 = self._make_default(name="vm-a")
        vm2 = self._make_default(name="vm-b")
        assert vm1 != vm2


class TestVMInspectInfo:
    """Tests for VMInspectInfo dataclass."""

    def test_dataclass_structure(self) -> None:
        info = VMInspectInfo(
            id="vm1",
            name="test-vm",
            status="running",
            created_at="2026-04-02T10:00:00Z",
            pid=1234,
            ip="10.0.0.2",
            mac="aa:bb:cc:dd:ee:ff",
            network_name="default",
            tap_device="tap0",
            cloud_init_mode="nocloud",
            image_id="img1",
            image_name="Ubuntu 24.04",
            kernel_id="kern1",
            kernel_name="vmlinux",
            paths={
                "vm_dir": "/cache/vms/vm1",
                "rootfs": "/rootfs.ext4",
                "config": "/cfg.json",
            },
            features={
                "api_socket": True,
                "console": True,
                "nocloud_net": False,
            },
        )
        assert info.id == "vm1"
        assert info.name == "test-vm"
        assert info.status == "running"
        assert info.cloud_init_mode == "nocloud"

    def test_optional_fields(self) -> None:
        info = VMInspectInfo(
            id="vm1",
            name="test-vm",
            status="running",
            created_at=None,
            pid=None,
            ip=None,
            mac=None,
            network_name=None,
            tap_device=None,
            cloud_init_mode="off",
            image_id=None,
            image_name=None,
            kernel_id=None,
            kernel_name=None,
            paths={},
            features={},
            nocloud_net=None,
            console=None,
        )
        assert info.nocloud_net is None
        assert info.console is None


class TestConsoleInfo:
    """Tests for ConsoleInfo dataclass."""

    def test_fields(self) -> None:
        from pathlib import Path

        info = ConsoleInfo(
            socket_path=Path("/tmp/console.sock"),
            vm_name="test-vm",
        )
        assert str(info.socket_path) == "/tmp/console.sock"
        assert info.vm_name == "test-vm"


class TestConsoleState:
    """Tests for ConsoleState dataclass."""

    def test_running_state(self) -> None:
        state = ConsoleState(
            running=True,
            pid=1234,
            socket_path="/tmp/console.sock",
        )
        assert state.running is True
        assert state.pid == 1234
        assert state.socket_path == "/tmp/console.sock"

    def test_stopped_state(self) -> None:
        state = ConsoleState(
            running=False,
            pid=None,
            socket_path=None,
        )
        assert state.running is False
        assert state.pid is None
        assert state.socket_path is None
