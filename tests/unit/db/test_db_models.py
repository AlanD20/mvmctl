"""Tests for model dataclasses that map to database tables.

The canonical model classes live in mvmctl.models. These tests verify
they instantiate correctly, have correct field types, optional fields
default to None, and basic equality works.
"""

from __future__ import annotations

from mvmctl.models.binary import BinaryItem
from mvmctl.models.host import HostStateChangeItem, HostStateItem
from mvmctl.models.image import ImageItem
from mvmctl.models.kernel import KernelItem
from mvmctl.models.network import NetworkItem, NetworkLeaseItem
from mvmctl.models.vm import VMInstanceItem


class TestImageItem:
    """Tests for ImageItem dataclass."""

    NOW = "2026-04-02T10:00:00Z"

    def _make(self, **overrides: object) -> ImageItem:
        base = {
            "id": "a" * 64,
            "os_slug": "ubuntu-24.04",
            "os_name": "Ubuntu 24.04",
            "arch": "x86_64",
            "path": "/cache/images/ubuntu-24.04.ext4",
            "fs_type": "ext4",
            "minimum_rootfs_size_mib": 2048,
            "original_size": 4096,
            "is_default": False,
            "is_present": True,
            "pulled_at": self.NOW,
            "created_at": self.NOW,
            "updated_at": self.NOW,
        }
        base.update(overrides)
        return ImageItem(**base)

    def test_required_fields(self) -> None:
        image = self._make()
        assert image.id == "a" * 64
        assert image.os_slug == "ubuntu-24.04"
        assert image.path == "/cache/images/ubuntu-24.04.ext4"
        assert image.is_default is False
        assert image.is_present is True

    def test_optional_fields_default_to_none(self) -> None:
        image = self._make()
        assert image.fs_uuid is None
        assert image.compressed_size is None
        assert image.compression_ratio is None
        assert image.compressed_format is None
        assert image.deleted_at is None
        assert image.vms is None

    def test_with_all_fields(self) -> None:
        image = self._make(
            fs_uuid="uuid-1234",
            compressed_size=1024,
            compression_ratio=0.5,
            compressed_format="gzip",
            deleted_at=None,
        )
        assert image.fs_uuid == "uuid-1234"
        assert image.compressed_size == 1024
        assert image.compression_ratio == 0.5

    def test_equality(self) -> None:
        image1 = self._make()
        image2 = self._make()
        assert image1 == image2


class TestKernelItem:
    """Tests for KernelItem dataclass."""

    NOW = "2026-04-02T10:00:00Z"

    def _make(self, **overrides: object) -> KernelItem:
        base = {
            "id": "b" * 64,
            "name": "vmlinux",
            "base_name": "vmlinux-base",
            "version": "5.10.0",
            "arch": "x86_64",
            "type": "elf",
            "path": "vmlinux-5.10.0",
            "is_default": False,
            "is_present": True,
            "created_at": self.NOW,
            "updated_at": self.NOW,
        }
        base.update(overrides)
        return KernelItem(**base)

    def test_required_fields(self) -> None:
        kernel = self._make()
        assert kernel.id == "b" * 64
        assert kernel.name == "vmlinux"
        assert kernel.version == "5.10.0"

    def test_optional_fields_default_to_none(self) -> None:
        kernel = self._make()
        assert kernel.deleted_at is None

    def test_equality(self) -> None:
        k1 = self._make()
        k2 = self._make()
        assert k1 == k2


class TestBinaryItem:
    """Tests for BinaryItem dataclass."""

    NOW = "2026-04-02T10:00:00Z"

    def _make(self, **overrides: object) -> BinaryItem:
        base = {
            "id": "c" * 64,
            "name": "firecracker",
            "version": "1.15.0",
            "full_version": "v1.15.0",
            "ci_version": "1.15.0-ci",
            "path": "firecracker-v1.15.0",
            "is_default": False,
            "is_present": True,
            "created_at": self.NOW,
            "updated_at": self.NOW,
        }
        base.update(overrides)
        return BinaryItem(**base)

    def test_required_fields(self) -> None:
        binary = self._make()
        assert binary.id == "c" * 64
        assert binary.name == "firecracker"
        assert binary.version == "1.15.0"

    def test_ci_version_can_be_none(self) -> None:
        binary = self._make(ci_version=None)
        assert binary.ci_version is None

    def test_equality(self) -> None:
        b1 = self._make()
        b2 = self._make()
        assert b1 == b2


class TestNetworkItem:
    """Tests for NetworkItem dataclass."""

    NOW = "2026-04-02T10:00:00Z"

    def _make(self, **overrides: object) -> NetworkItem:
        base = {
            "id": "d" * 64,
            "name": "default",
            "subnet": "192.168.1.0/24",
            "bridge": "mvm-default",
            "ipv4_gateway": "192.168.1.1",
            "bridge_active": False,
            "nat_enabled": False,
            "is_default": False,
            "is_present": True,
            "created_at": self.NOW,
            "updated_at": self.NOW,
        }
        base.update(overrides)
        return NetworkItem(**base)

    def test_required_fields(self) -> None:
        net = self._make()
        assert net.id == "d" * 64
        assert net.name == "default"
        assert net.bridge == "mvm-default"

    def test_optional_fields(self) -> None:
        net = self._make()
        assert net.nat_gateways is None
        assert net.leases is None
        assert net.iptables_rules is None

    def test_with_nat_gateways(self) -> None:
        net = self._make(nat_gateways="eth0,eth1")
        assert net.nat_gateways == "eth0,eth1"
        assert net.nat_gateways_list == ["eth0", "eth1"]

    def test_equality(self) -> None:
        n1 = self._make()
        n2 = self._make()
        assert n1 == n2


class TestNetworkLeaseItem:
    """Tests for NetworkLeaseItem dataclass."""

    NOW = "2026-04-02T10:00:00Z"

    def test_required_fields(self) -> None:
        lease = NetworkLeaseItem(
            network_id="n" * 64,
            ipv4="192.168.1.10",
            leased_at=self.NOW,
        )
        assert lease.network_id == "n" * 64
        assert lease.ipv4 == "192.168.1.10"

    def test_optional_fields_default_to_none(self) -> None:
        lease = NetworkLeaseItem(
            network_id="n" * 64,
            ipv4="192.168.1.10",
            leased_at=self.NOW,
        )
        assert lease.id is None
        assert lease.vm_id is None
        assert lease.expires_at is None

    def test_with_all_fields(self) -> None:
        lease = NetworkLeaseItem(
            network_id="n" * 64,
            ipv4="192.168.1.10",
            leased_at=self.NOW,
            id=1,
            vm_id="v" * 64,
            expires_at="2026-04-03T10:00:00Z",
        )
        assert lease.id == 1
        assert lease.vm_id == "v" * 64
        assert lease.expires_at == "2026-04-03T10:00:00Z"

    def test_equality(self) -> None:
        l1 = NetworkLeaseItem(
            network_id="n" * 64, ipv4="192.168.1.10", leased_at=self.NOW
        )
        l2 = NetworkLeaseItem(
            network_id="n" * 64, ipv4="192.168.1.10", leased_at=self.NOW
        )
        assert l1 == l2


class TestVMInstanceItem:
    """Tests for VMInstanceItem dataclass."""

    NOW = "2026-04-02T10:00:00Z"

    def _make(self, **overrides: object) -> VMInstanceItem:
        base = {
            "id": "e" * 64,
            "name": "myvm",
            "status": "running",
            "pid": 1234,
            "ipv4": "192.168.1.10",
            "mac": "52:54:00:12:34:56",
            "network_id": "d" * 64,
            "tap_device": "tap-mvm",
            "image_id": "a" * 64,
            "kernel_id": "b" * 64,
            "binary_id": "c" * 64,
            "api_socket_path": "/tmp/firecracker.sock",
            "config_path": "/tmp/config.json",
            "cloud_init_mode": "nocloud",
            "vcpu_count": 2,
            "mem_size_mib": 512,
            "disk_size_mib": 10240,
            "rootfs_path": "/cache/vms/myvm/rootfs.ext4",
            "rootfs_suffix": ".ext4",
            "enable_pci": False,
            "enable_logging": True,
            "enable_metrics": False,
            "enable_console": True,
            "created_at": self.NOW,
            "updated_at": self.NOW,
        }
        base.update(overrides)
        return VMInstanceItem(**base)

    def test_required_fields(self) -> None:
        vm = self._make()
        assert vm.id == "e" * 64
        assert vm.name == "myvm"
        assert vm.status == "running"
        assert vm.vcpu_count == 2
        assert vm.mem_size_mib == 512

    def test_optional_fields_default_to_none(self) -> None:
        vm = self._make()
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
        assert vm.kernel is None
        assert vm.image is None
        assert vm.binary is None
        assert vm.network is None

    def test_equality(self) -> None:
        vm1 = self._make()
        vm2 = self._make()
        assert vm1 == vm2


class TestHostStateItem:
    """Tests for HostStateItem dataclass."""

    NOW = "2026-04-02T10:00:00Z"

    def test_required_fields(self) -> None:
        state = HostStateItem(
            id=1,
            initialized=False,
            mvm_group_created=False,
            sudoers_configured=False,
            default_network_created=False,
            initialized_at=self.NOW,
            updated_at=self.NOW,
        )
        assert state.id == 1
        assert state.initialized is False
        assert state.initialized_at == self.NOW

    def test_boolean_fields(self) -> None:
        state = HostStateItem(
            id=1,
            initialized=True,
            mvm_group_created=True,
            sudoers_configured=True,
            default_network_created=True,
            initialized_at=self.NOW,
            updated_at=self.NOW,
        )
        assert state.initialized is True
        assert state.mvm_group_created is True
        assert state.sudoers_configured is True
        assert state.default_network_created is True

    def test_equality(self) -> None:
        s1 = HostStateItem(
            id=1,
            initialized=False,
            mvm_group_created=False,
            sudoers_configured=False,
            default_network_created=False,
            initialized_at=self.NOW,
            updated_at=self.NOW,
        )
        s2 = HostStateItem(
            id=1,
            initialized=False,
            mvm_group_created=False,
            sudoers_configured=False,
            default_network_created=False,
            initialized_at=self.NOW,
            updated_at=self.NOW,
        )
        assert s1 == s2


class TestHostStateChangeItem:
    """Tests for HostStateChangeItem dataclass."""

    NOW = "2026-04-02T10:00:00Z"

    def test_required_fields(self) -> None:
        change = HostStateChangeItem(
            session_id="session-123",
            init_timestamp=self.NOW,
            setting="mvm_group",
            mechanism="groupadd",
            applied_value="mvm",
            reverted=False,
            change_order=1,
            created_at=self.NOW,
        )
        assert change.session_id == "session-123"
        assert change.setting == "mvm_group"
        assert change.change_order == 1

    def test_optional_fields_default_to_none(self) -> None:
        change = HostStateChangeItem(
            session_id="session-123",
            init_timestamp=self.NOW,
            setting="mvm_group",
            mechanism="groupadd",
            applied_value="mvm",
            reverted=False,
            change_order=1,
            created_at=self.NOW,
        )
        assert change.id is None
        assert change.original_value is None
        assert change.reverted_at is None
        assert change.revert_mechanism is None

    def test_equality(self) -> None:
        c1 = HostStateChangeItem(
            session_id="s1",
            init_timestamp=self.NOW,
            setting="s",
            mechanism="m",
            applied_value="v",
            reverted=False,
            change_order=1,
            created_at=self.NOW,
        )
        c2 = HostStateChangeItem(
            session_id="s1",
            init_timestamp=self.NOW,
            setting="s",
            mechanism="m",
            applied_value="v",
            reverted=False,
            change_order=1,
            created_at=self.NOW,
        )
        assert c1 == c2
