"""Tests for mvmctl.db.models dataclasses."""

from __future__ import annotations

from mvmctl.db.models import (
    Binary,
    HostState,
    HostStateChange,
    Image,
    Kernel,
    Network,
    NetworkLease,
    VMInstance,
)


class TestImage:
    """Tests for Image dataclass."""

    def test_image_instantiation_required_fields_only(self) -> None:
        """Image can be instantiated with required fields only."""
        image = Image(
            id="a" * 64,
            os_slug="ubuntu-24.04",
            os_name="Ubuntu 24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789012",
            minimum_rootfs_size_mib=2048,
            original_size=4096,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert image.id == "a" * 64
        assert image.os_slug == "ubuntu-24.04"
        assert image.path == "/cache/images/ubuntu-24.04.ext4"

    def test_image_optional_fields_default_to_none(self) -> None:
        """Image optional fields default to None."""
        image = Image(
            id="a" * 64,
            os_slug="ubuntu-24.04",
            os_name="Ubuntu 24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789012",
            minimum_rootfs_size_mib=2048,
            original_size=4096,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert image.compressed_size is None

    def test_image_is_default_field(self) -> None:
        """Image is_default field works correctly."""
        image = Image(
            id="a" * 64,
            os_slug="ubuntu-24.04",
            os_name="Ubuntu 24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789012",
            minimum_rootfs_size_mib=2048,
            original_size=4096,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert image.is_default is False

    def test_image_with_all_fields(self) -> None:
        """Image can be instantiated with all fields."""
        image = Image(
            id="a" * 64,
            os_slug="ubuntu-24.04",
            os_name="Ubuntu",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789012",
            minimum_rootfs_size_mib=2048,
            original_size=2048000,
            compressed_size=1024000,
            compression_ratio=0.5,
            compressed_format="gzip",
            pulled_at="2026-04-02T10:00:00Z",
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert image.os_name == "Ubuntu"
        assert image.fs_type == "ext4"
        assert image.is_default is True

    def test_image_equality(self) -> None:
        """Images with same fields are equal."""
        image1 = Image(
            id="a" * 64,
            os_slug="ubuntu-24.04",
            os_name="Ubuntu 24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789012",
            minimum_rootfs_size_mib=2048,
            original_size=4096,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        image2 = Image(
            id="a" * 64,
            os_slug="ubuntu-24.04",
            os_name="Ubuntu 24.04",
            path="/cache/images/ubuntu-24.04.ext4",
            arch="x86_64",
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789012",
            minimum_rootfs_size_mib=2048,
            original_size=4096,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert image1 == image2


class TestKernel:
    """Tests for Kernel dataclass."""

    def test_kernel_instantiation_required_fields_only(self) -> None:
        """Kernel can be instantiated with required fields only."""
        kernel = Kernel(
            id="b" * 64,
            name="vmlinux",
            base_name="vmlinux-base",
            version="5.10.0",
            arch="x86_64",
            type="linux",
            path="/cache/kernels/vmlinux-5.10.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert kernel.id == "b" * 64
        assert kernel.name == "vmlinux"
        assert kernel.version == "5.10.0"
        assert kernel.arch == "x86_64"
        assert kernel.path == "/cache/kernels/vmlinux-5.10.0"

    def test_kernel_fields(self) -> None:
        """Kernel fields are set correctly."""
        kernel = Kernel(
            id="b" * 64,
            name="vmlinux",
            base_name="vmlinux-base",
            version="5.10.0",
            arch="x86_64",
            type="linux",
            path="/cache/kernels/vmlinux-5.10.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert kernel.base_name == "vmlinux-base"
        assert kernel.type == "linux"
        assert kernel.created_at == "2026-04-02T10:00:00Z"
        assert kernel.updated_at == "2026-04-02T10:00:00Z"

    def test_kernel_is_default_field(self) -> None:
        """Kernel is_default field works correctly."""
        kernel = Kernel(
            id="b" * 64,
            name="vmlinux",
            base_name="vmlinux-base",
            version="5.10.0",
            arch="x86_64",
            type="linux",
            path="/cache/kernels/vmlinux-5.10.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert kernel.is_default is False

    def test_kernel_with_all_fields(self) -> None:
        """Kernel can be instantiated with all fields."""
        kernel = Kernel(
            id="b" * 64,
            name="vmlinux",
            base_name="vmlinux-base",
            version="5.10.0",
            arch="x86_64",
            type="linux",
            path="/cache/kernels/vmlinux-5.10.0",
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert kernel.base_name == "vmlinux-base"
        assert kernel.type == "linux"
        assert kernel.is_default is True

    def test_kernel_equality(self) -> None:
        """Kernels with same fields are equal."""
        kernel1 = Kernel(
            id="b" * 64,
            name="vmlinux",
            base_name="vmlinux-base",
            version="5.10.0",
            arch="x86_64",
            type="linux",
            path="/cache/kernels/vmlinux-5.10.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        kernel2 = Kernel(
            id="b" * 64,
            name="vmlinux",
            base_name="vmlinux-base",
            version="5.10.0",
            arch="x86_64",
            type="linux",
            path="/cache/kernels/vmlinux-5.10.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert kernel1 == kernel2


class TestBinary:
    """Tests for Binary dataclass."""

    def test_binary_instantiation_required_fields_only(self) -> None:
        """Binary can be instantiated with required fields only."""
        binary = Binary(
            id="c" * 64,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="1.15.0-ci",
            path="/cache/bin/firecracker-1.15.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert binary.id == "c" * 64
        assert binary.name == "firecracker"
        assert binary.version == "1.15.0"
        assert binary.path == "/cache/bin/firecracker-1.15.0"

    def test_binary_fields(self) -> None:
        """Binary fields are set correctly."""
        binary = Binary(
            id="c" * 64,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="1.15.0-ci",
            path="/cache/bin/firecracker-1.15.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert binary.full_version == "v1.15.0"
        assert binary.ci_version == "1.15.0-ci"
        assert binary.created_at == "2026-04-02T10:00:00Z"
        assert binary.updated_at == "2026-04-02T10:00:00Z"

    def test_binary_with_all_fields(self) -> None:
        """Binary can be instantiated with all fields."""
        binary = Binary(
            id="c" * 64,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="1.15.0-ci",
            path="/cache/bin/firecracker-1.15.0",
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert binary.full_version == "v1.15.0"
        assert binary.ci_version == "1.15.0-ci"
        assert binary.is_default is True

    def test_binary_equality(self) -> None:
        """Binaries with same fields are equal."""
        binary1 = Binary(
            id="c" * 64,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="1.15.0-ci",
            path="/cache/bin/firecracker-1.15.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        binary2 = Binary(
            id="c" * 64,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="1.15.0-ci",
            path="/cache/bin/firecracker-1.15.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert binary1 == binary2

    def test_binary_is_default_field(self) -> None:
        """Binary is_default field works correctly."""
        binary = Binary(
            id="c" * 64,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="1.15.0-ci",
            path="/cache/bin/firecracker-1.15.0",
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert binary.is_default is False

    def test_binary_with_is_default_true(self) -> None:
        """Binary can be instantiated with is_default=True."""
        binary = Binary(
            id="c" * 64,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="1.15.0-ci",
            path="/cache/bin/firecracker-1.15.0",
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert binary.is_default is True

    def test_binary_with_all_fields_including_is_default(self) -> None:
        """Binary can be instantiated with all fields including is_default."""
        binary = Binary(
            id="c" * 64,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="1.15.0-ci",
            path="/cache/bin/firecracker-1.15.0",
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert binary.full_version == "v1.15.0"
        assert binary.ci_version == "1.15.0-ci"
        assert binary.is_default is True


class TestNetwork:
    """Tests for Network dataclass."""

    def test_network_instantiation_required_fields_only(self) -> None:
        """Network can be instantiated with required fields only."""
        network = Network(
            id="d" * 64,
            name="default",
            subnet="192.168.1.0/24",
            bridge="mvm-default",
            ipv4_gateway="192.168.1.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert network.id == "d" * 64
        assert network.name == "default"
        assert network.subnet == "192.168.1.0/24"
        assert network.bridge == "mvm-default"
        assert network.ipv4_gateway == "192.168.1.1"

    def test_network_boolean_fields(self) -> None:
        """Network boolean fields work correctly."""
        network = Network(
            id="d" * 64,
            name="default",
            subnet="192.168.1.0/24",
            bridge="mvm-default",
            ipv4_gateway="192.168.1.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert network.bridge_active is False
        assert network.nat_enabled is False
        assert network.is_default is False

    def test_network_optional_fields(self) -> None:
        """Network optional fields work correctly."""
        network = Network(
            id="d" * 64,
            name="default",
            subnet="192.168.1.0/24",
            bridge="mvm-default",
            ipv4_gateway="192.168.1.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert network.nat_gateways is None

    def test_network_with_all_fields(self) -> None:
        """Network can be instantiated with all fields."""
        network = Network(
            id="d" * 64,
            name="default",
            subnet="192.168.1.0/24",
            bridge="mvm-default",
            ipv4_gateway="192.168.1.1",
            bridge_active=True,
            nat_gateways="eth0,eth1",
            nat_enabled=True,
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert network.bridge_active is True
        assert network.nat_gateways == "eth0,eth1"
        assert network.nat_enabled is True
        assert network.is_default is True

    def test_network_equality(self) -> None:
        """Networks with same fields are equal."""
        network1 = Network(
            id="d" * 64,
            name="default",
            subnet="192.168.1.0/24",
            bridge="mvm-default",
            ipv4_gateway="192.168.1.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        network2 = Network(
            id="d" * 64,
            name="default",
            subnet="192.168.1.0/24",
            bridge="mvm-default",
            ipv4_gateway="192.168.1.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert network1 == network2


class TestNetworkLease:
    """Tests for NetworkLease dataclass."""

    def test_network_lease_instantiation_required_fields_only(self) -> None:
        """NetworkLease can be instantiated with required fields only."""
        lease = NetworkLease(
            network_id="d" * 64,
            ipv4="192.168.1.10",
            leased_at="2026-04-02T10:00:00Z",
        )
        assert lease.network_id == "d" * 64
        assert lease.ipv4 == "192.168.1.10"

    def test_network_lease_optional_fields_default_to_none(self) -> None:
        """NetworkLease optional fields default to None."""
        lease = NetworkLease(
            network_id="d" * 64,
            ipv4="192.168.1.10",
            leased_at="2026-04-02T10:00:00Z",
        )
        assert lease.id is None
        assert lease.vm_id is None
        assert lease.expires_at is None

    def test_network_lease_with_all_fields(self) -> None:
        """NetworkLease can be instantiated with all fields."""
        lease = NetworkLease(
            network_id="d" * 64,
            ipv4="192.168.1.10",
            leased_at="2026-04-02T10:00:00Z",
            id=1,
            vm_id="e" * 64,
            expires_at="2026-04-03T10:00:00Z",
        )
        assert lease.id == 1
        assert lease.vm_id == "e" * 64
        assert lease.leased_at == "2026-04-02T10:00:00Z"
        assert lease.expires_at == "2026-04-03T10:00:00Z"

    def test_network_lease_equality(self) -> None:
        """NetworkLeases with same fields are equal."""
        lease1 = NetworkLease(
            network_id="d" * 64,
            ipv4="192.168.1.10",
            leased_at="2026-04-02T10:00:00Z",
        )
        lease2 = NetworkLease(
            network_id="d" * 64,
            ipv4="192.168.1.10",
            leased_at="2026-04-02T10:00:00Z",
        )
        assert lease1 == lease2


class TestVMInstance:
    """Tests for VMState dataclass."""

    def test_vm_state_instantiation_required_fields_only(self) -> None:
        """VMState can be instantiated with required fields only."""
        vm_state = VMInstance(
            id="e" * 64,
            name="myvm",
            status="RUNNING",
            pid=1234,
            ipv4="192.168.1.10",
            mac="52:54:00:12:34:56",
            network_id="d" * 64,
            tap_device="mvm-def-mvm-tap",
            image_id="a" * 64,
            kernel_id="b" * 64,
            binary_id="c" * 64,
            config_path="/tmp/firecracker.json",
            cloud_init_mode="inject",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=10240,
            rootfs_path="/cache/vms/myvm/rootfs.ext4",
            rootfs_suffix=".ext4",
            enable_api_socket=False,
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert vm_state.id == "e" * 64
        assert vm_state.name == "myvm"
        assert vm_state.status == "RUNNING"

    def test_vm_state_optional_fields_default_to_none(self) -> None:
        """VMState optional fields default to None."""
        vm_state = VMInstance(
            id="e" * 64,
            name="myvm",
            status="RUNNING",
            pid=1234,
            ipv4="192.168.1.10",
            mac="52:54:00:12:34:56",
            network_id="d" * 64,
            tap_device="mvm-def-mvm-tap",
            image_id="a" * 64,
            kernel_id="b" * 64,
            binary_id="c" * 64,
            config_path="/tmp/firecracker.json",
            cloud_init_mode="inject",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=10240,
            rootfs_path="/cache/vms/myvm/rootfs.ext4",
            rootfs_suffix=".ext4",
            enable_api_socket=False,
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert vm_state.api_socket_path is None
        assert vm_state.console_socket_path is None
        assert vm_state.nocloud_net_port is None
        assert vm_state.nocloud_server_pid is None
        assert vm_state.console_relay_pid is None
        assert vm_state.exit_code is None

    def test_vm_state_with_all_fields(self) -> None:
        """VMState can be instantiated with all fields."""
        vm_state = VMInstance(
            id="e" * 64,
            name="myvm",
            status="RUNNING",
            pid=1234,
            ipv4="192.168.1.10",
            mac="52:54:00:12:34:56",
            network_id="d" * 64,
            tap_device="mvm-def-mvm-tap",
            image_id="a" * 64,
            kernel_id="b" * 64,
            binary_id="c" * 64,
            api_socket_path="/tmp/firecracker.sock",
            console_socket_path="/tmp/console.sock",
            config_path="/tmp/firecracker.json",
            cloud_init_mode="inject",
            nocloud_net_port=8080,
            nocloud_server_pid=5678,
            console_relay_pid=5679,
            exit_code=0,
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=10240,
            rootfs_path="/cache/vms/myvm/rootfs.ext4",
            rootfs_suffix=".ext4",
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert vm_state.pid == 1234
        assert vm_state.ipv4 == "192.168.1.10"
        assert vm_state.vcpu_count == 2
        assert vm_state.mem_size_mib == 512

    def test_vm_state_equality(self) -> None:
        """VMStates with same fields are equal."""
        vm_state1 = VMInstance(
            id="e" * 64,
            name="myvm",
            status="RUNNING",
            pid=1234,
            ipv4="192.168.1.10",
            mac="52:54:00:12:34:56",
            network_id="d" * 64,
            tap_device="mvm-def-mvm-tap",
            image_id="a" * 64,
            kernel_id="b" * 64,
            binary_id="c" * 64,
            config_path="/tmp/firecracker.json",
            cloud_init_mode="inject",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=10240,
            rootfs_path="/cache/vms/myvm/rootfs.ext4",
            rootfs_suffix=".ext4",
            enable_api_socket=False,
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        vm_state2 = VMInstance(
            id="e" * 64,
            name="myvm",
            status="RUNNING",
            pid=1234,
            ipv4="192.168.1.10",
            mac="52:54:00:12:34:56",
            network_id="d" * 64,
            tap_device="mvm-def-mvm-tap",
            image_id="a" * 64,
            kernel_id="b" * 64,
            binary_id="c" * 64,
            config_path="/tmp/firecracker.json",
            cloud_init_mode="inject",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=10240,
            rootfs_path="/cache/vms/myvm/rootfs.ext4",
            rootfs_suffix=".ext4",
            enable_api_socket=False,
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert vm_state1 == vm_state2


class TestHostState:
    """Tests for HostState dataclass."""

    def test_host_state_instantiation_required_fields_only(self) -> None:
        """HostState can be instantiated with required fields only."""
        host_state = HostState(
            id=1,
            initialized=False,
            mvm_group_created=False,
            sudoers_configured=False,
            default_network_created=False,
            initialized_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert host_state.id == 1

    def test_host_state_boolean_fields(self) -> None:
        """HostState boolean fields work correctly."""
        host_state = HostState(
            id=1,
            initialized=False,
            mvm_group_created=False,
            sudoers_configured=False,
            default_network_created=False,
            initialized_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert host_state.initialized is False
        assert host_state.mvm_group_created is False
        assert host_state.sudoers_configured is False
        assert host_state.default_network_created is False

    def test_host_state_timestamps(self) -> None:
        """HostState timestamp fields work correctly."""
        host_state = HostState(
            id=1,
            initialized=True,
            mvm_group_created=True,
            sudoers_configured=True,
            default_network_created=True,
            initialized_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T11:00:00Z",
        )
        assert host_state.initialized_at == "2026-04-02T10:00:00Z"
        assert host_state.updated_at == "2026-04-02T11:00:00Z"

    def test_host_state_with_all_fields(self) -> None:
        """HostState can be instantiated with all fields."""
        host_state = HostState(
            id=1,
            initialized=True,
            mvm_group_created=True,
            sudoers_configured=True,
            default_network_created=True,
            initialized_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert host_state.initialized is True
        assert host_state.mvm_group_created is True
        assert host_state.sudoers_configured is True
        assert host_state.default_network_created is True

    def test_host_state_equality(self) -> None:
        """HostStates with same fields are equal."""
        host_state1 = HostState(
            id=1,
            initialized=False,
            mvm_group_created=False,
            sudoers_configured=False,
            default_network_created=False,
            initialized_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        host_state2 = HostState(
            id=1,
            initialized=False,
            mvm_group_created=False,
            sudoers_configured=False,
            default_network_created=False,
            initialized_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        assert host_state1 == host_state2


class TestHostStateChange:
    """Tests for HostStateChange dataclass."""

    def test_host_state_change_instantiation_required_fields_only(self) -> None:
        """HostStateChange can be instantiated with required fields only."""
        change = HostStateChange(
            session_id="session-123",
            init_timestamp="2026-04-02T10:00:00Z",
            setting="mvm_group",
            mechanism="groupadd",
            applied_value="mvm",
            change_order=1,
            reverted=False,
            created_at="2026-04-02T10:00:00Z",
        )
        assert change.session_id == "session-123"
        assert change.init_timestamp == "2026-04-02T10:00:00Z"
        assert change.setting == "mvm_group"
        assert change.mechanism == "groupadd"
        assert change.applied_value == "mvm"
        assert change.change_order == 1

    def test_host_state_change_optional_fields_default_to_none(self) -> None:
        """HostStateChange optional fields default to None."""
        change = HostStateChange(
            session_id="session-123",
            init_timestamp="2026-04-02T10:00:00Z",
            setting="mvm_group",
            mechanism="groupadd",
            applied_value="mvm",
            change_order=1,
            reverted=False,
            created_at="2026-04-02T10:00:00Z",
        )
        assert change.id is None
        assert change.original_value is None
        assert change.reverted_at is None
        assert change.revert_mechanism is None

    def test_host_state_change_reverted_field(self) -> None:
        """HostStateChange reverted field works correctly."""
        change = HostStateChange(
            session_id="session-123",
            init_timestamp="2026-04-02T10:00:00Z",
            setting="mvm_group",
            mechanism="groupadd",
            applied_value="mvm",
            change_order=1,
            reverted=False,
            created_at="2026-04-02T10:00:00Z",
        )
        assert change.reverted is False

    def test_host_state_change_with_all_fields(self) -> None:
        """HostStateChange can be instantiated with all fields."""
        change = HostStateChange(
            session_id="session-123",
            init_timestamp="2026-04-02T10:00:00Z",
            setting="mvm_group",
            mechanism="groupadd",
            applied_value="mvm",
            change_order=1,
            reverted=True,
            created_at="2026-04-02T10:00:00Z",
            id=1,
            original_value=None,
            reverted_at="2026-04-02T11:00:00Z",
            revert_mechanism="groupdel",
        )
        assert change.id == 1
        assert change.reverted is True
        assert change.reverted_at == "2026-04-02T11:00:00Z"
        assert change.revert_mechanism == "groupdel"

    def test_host_state_change_equality(self) -> None:
        """HostStateChanges with same fields are equal."""
        change1 = HostStateChange(
            session_id="session-123",
            init_timestamp="2026-04-02T10:00:00Z",
            setting="mvm_group",
            mechanism="groupadd",
            applied_value="mvm",
            change_order=1,
            reverted=False,
            created_at="2026-04-02T10:00:00Z",
        )
        change2 = HostStateChange(
            session_id="session-123",
            init_timestamp="2026-04-02T10:00:00Z",
            setting="mvm_group",
            mechanism="groupadd",
            applied_value="mvm",
            change_order=1,
            reverted=False,
            created_at="2026-04-02T10:00:00Z",
        )
        assert change1 == change2
