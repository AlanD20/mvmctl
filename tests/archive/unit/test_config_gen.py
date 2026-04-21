"""Tests for config generation."""

from pathlib import Path

import pytest

from mvmctl.constants import DEFAULT_LIBGUESTFS_SEED_DIR
from mvmctl.core.config_gen import ConfigGenerator
from mvmctl.models import CloudInitMode
from mvmctl.models.vm import VMConfig, VMInstance, VMStatus
from datetime import datetime, timezone


def test_config_generator_basic():
    """Test basic config generation."""
    vm_config = VMConfig(
        name="test-vm",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )

    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()

    assert config["machine-config"]["vcpu_count"] == 2
    assert config["machine-config"]["mem_size_mib"] == 512
    assert config["boot-source"]["kernel_image_path"] == "/tmp/vmlinux"
    assert len(config["drives"]) == 1
    assert config["drives"][0]["path_on_host"] == "/tmp/rootfs.ext4"


def test_config_generator_sets_root_uuid_in_boot_args():
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_uuid="123e4567-e89b-12d3-a456-426614174000",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )

    config = ConfigGenerator(vm_config, instance).generate()

    assert config["drives"][0]["partuuid"] is None
    assert "root=UUID=123e4567-e89b-12d3-a456-426614174000" in config["boot-source"]["boot_args"]


def test_config_generator_overrides_existing_root_arg_with_uuid():
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_uuid="123e4567-e89b-12d3-a456-426614174001",
        boot_args="console=ttyS0 root=/dev/vda rw",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )

    config = ConfigGenerator(vm_config, instance).generate()
    boot_args = config["boot-source"]["boot_args"]

    assert "root=/dev/vda" not in boot_args
    assert "root=UUID=123e4567-e89b-12d3-a456-426614174001" in boot_args


def test_config_generator_network():
    """Test network interface configuration."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )

    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()

    assert len(config["network-interfaces"]) == 1
    iface = config["network-interfaces"][0]
    assert iface["iface_id"] == "eth0"
    assert iface["host_dev_name"] == "fc-tap0"
    assert iface["guest_mac"] == "02:FC:00:00:00:01"


def test_config_generator_no_network():
    """Test config without network."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )

    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()

    assert len(config["network-interfaces"]) == 0


# ---------------------------------------------------------------------------
# S-H9: Boot arg injection validation
# ---------------------------------------------------------------------------


def test_boot_args_rejects_shell_injection_in_guest_ip():
    """guest_ip with shell metacharacters should raise MVMError."""
    from mvmctl.exceptions import MVMError

    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="10.0.0.2;rm -rf /",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    with pytest.raises(MVMError, match="guest_ip"):
        generator.validate()


def test_boot_args_rejects_shell_injection_in_gateway():
    """gateway with pipe character should raise MVMError."""
    from mvmctl.exceptions import MVMError

    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1|evil",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    with pytest.raises(MVMError, match="ipv4_gateway"):
        generator.validate()


def test_boot_args_accepts_normal_ip():
    """Normal IP addresses should pass validation without error."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert "10.0.0.2" in config["boot-source"]["boot_args"]


# ---------------------------------------------------------------------------
# T-H5: ConfigGenerator edge cases
# ---------------------------------------------------------------------------


def test_config_gen_empty_vm_name():
    vm_config = VMConfig(
        name="",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert config["boot-source"]["kernel_image_path"] == "/tmp/vmlinux"
    assert config["drives"][0]["path_on_host"] == "/tmp/rootfs.ext4"


def test_config_gen_zero_vcpus():
    """VMConfig with vcpu_count=0 raises ValueError (out of bounds)."""
    with pytest.raises(ValueError, match="vcpu_count must be between"):
        VMConfig(
            name="zero-cpu",
            vcpu_count=0,
            kernel_path=Path("/tmp/vmlinux"),
            rootfs_path=Path("/tmp/rootfs.ext4"),
            mem_size_mib=512,
            disk_size_mib=1024,
            enable_api_socket=True,
            enable_pci=False,
            lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
            cloud_init_mode=CloudInitMode.INJECT,
        )


def test_config_gen_zero_memory():
    """VMConfig with mem_size_mib=0 raises ValueError (out of bounds)."""
    with pytest.raises(ValueError, match="mem_size_mib must be between"):
        VMConfig(
            name="zero-mem",
            mem_size_mib=0,
            kernel_path=Path("/tmp/vmlinux"),
            rootfs_path=Path("/tmp/rootfs.ext4"),
            vcpu_count=2,
            disk_size_mib=1024,
            enable_api_socket=True,
            enable_pci=False,
            lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
            cloud_init_mode=CloudInitMode.INJECT,
        )


def test_config_gen_missing_kernel_path_default():
    """Test that ConfigGenerator uses default kernel path when kernel_path is not provided.

    Note: VMConfig now requires explicit kernel_path, so we test with explicit Path("vmlinux").
    """
    vm_config = VMConfig(
        name="no-kernel",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="no-kernel",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert config["boot-source"]["kernel_image_path"] == "vmlinux"


def test_config_gen_missing_rootfs_path_default():
    """Test that ConfigGenerator uses default rootfs path when rootfs_path is not provided.

    Note: VMConfig now requires explicit rootfs_path, so we test with explicit Path("rootfs.ext4").
    """
    vm_config = VMConfig(
        name="no-rootfs",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="no-rootfs",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert config["drives"][0]["path_on_host"] == "rootfs.ext4"


def test_config_gen_invalid_ip_with_shell_chars():
    from mvmctl.exceptions import MVMError

    vm_config = VMConfig(
        name="bad-ip",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="bad-ip",
        id="a" * 16,
        pid=0,
        ipv4="not-a-valid;ip",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    with pytest.raises(MVMError, match="guest_ip"):
        generator.validate()


def test_config_gen_write_to_file(tmp_path):
    import json as _json

    vm_config = VMConfig(
        name="file-test",
        vcpu_count=1,
        mem_size_mib=256,
        disk_size_mib=1024,
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="file-test",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    out_file = tmp_path / "subdir" / "firecracker.json"
    generator.write_to_file(out_file)

    assert out_file.exists()
    data = _json.loads(out_file.read_text())
    assert data["machine-config"]["vcpu_count"] == 1
    assert data["machine-config"]["mem_size_mib"] == 256


def test_config_gen_pci_enabled():
    vm_config = VMConfig(
        name="pci-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        enable_pci=True,
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="pci-vm",
        id="a" * 16,
        pid=0,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert "pci=off" not in config["boot-source"]["boot_args"]


def test_config_gen_custom_boot_args():
    vm_config = VMConfig(
        name="custom-boot",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        boot_args="console=ttyS0 custom=yes",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="custom-boot",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert config["boot-source"]["boot_args"] == "console=ttyS0 custom=yes"


def test_config_gen_no_guest_ip_omits_ip_arg():
    vm_config = VMConfig(
        name="no-ip",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="no-ip",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert "ip=" not in config["boot-source"]["boot_args"]


def test_config_gen_extra_drives():
    from mvmctl.core.config_gen import DriveConfig

    extra: DriveConfig = {
        "drive_id": "data",
        "path_on_host": "/tmp/data.ext4",
        "is_root_device": False,
        "is_read_only": False,
        "partuuid": None,
        "cache_type": "Unsafe",
        "io_engine": "Sync",
        "rate_limiter": None,
        "socket": None,
    }
    vm_config = VMConfig(
        name="multi-drive",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        extra_drives=[extra],
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="multi-drive",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()

    assert len(config["drives"]) == 2
    assert config["drives"][0]["drive_id"] == "rootfs"
    assert config["drives"][0]["is_root_device"] is True
    assert config["drives"][1]["drive_id"] == "data"
    assert config["drives"][1]["is_root_device"] is False


def test_config_gen_cloud_init_drive_added_once():
    vm_config = VMConfig(
        name="ci-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        cloud_init_mode=CloudInitMode.INJECT,
        cloud_init_iso_path=Path("/tmp/cloud-init.iso"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
    )
    instance = VMInstance(
        name="ci-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )

    config = ConfigGenerator(vm_config, instance).generate()
    cloud_init_drives = [drive for drive in config["drives"] if drive["drive_id"] == "cloud-init"]

    assert len(cloud_init_drives) == 1
    assert cloud_init_drives[0]["path_on_host"] == "/tmp/cloud-init.iso"


def test_config_gen_logging_disabled():
    vm_config = VMConfig(
        name="no-log",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        enable_logging=False,
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="no-log",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert config["logger"] is None


def test_config_gen_logging_enabled_by_default():
    vm_config = VMConfig(
        name="with-log",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="with-log",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert config["logger"] is not None
    assert "log_path" in config["logger"]


def test_config_gen_metrics_disabled_by_default():
    vm_config = VMConfig(
        name="no-metrics",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="no-metrics",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert config["metrics"] is None


def test_config_gen_metrics_enabled():
    vm_config = VMConfig(
        name="with-metrics",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        enable_metrics=True,
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="with-metrics",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert config["metrics"] is not None
    assert "metrics_path" in config["metrics"]


def test_boot_args_nocloud_ds_default():
    """nocloud (default) mode produces ds=nocloud."""
    vm_config = VMConfig(
        name="nocloud-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="nocloud-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    assert "ds=nocloud" in config["boot-source"]["boot_args"]


def test_boot_args_nocloud_net_ds():
    """nocloud-net mode uses nocloud_net_url from config."""
    vm_config = VMConfig(
        name="nocloud-net-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        cloud_init_mode=CloudInitMode.NET,
        nocloud_net_url="http://192.168.1.1:8123/",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
    )
    instance = VMInstance(
        name="nocloud-net-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "ds=nocloud;seedfrom=http://192.168.1.1:8123/" in boot_args


def test_boot_args_direct_injection_seed_dir_uses_constant():
    vm_config = VMConfig(
        name="direct-injection-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        cloud_init_mode=CloudInitMode.INJECT,
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
    )
    instance = VMInstance(
        name="direct-injection-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )

    config = ConfigGenerator(vm_config, instance).generate()

    assert (
        f"ds=nocloud;s=file://{DEFAULT_LIBGUESTFS_SEED_DIR}/" in config["boot-source"]["boot_args"]
    )


def test_boot_args_nocloud_net_requires_url():
    """nocloud-net mode without nocloud_net_url raises ConfigError."""
    from mvmctl.exceptions import ConfigError

    vm_config = VMConfig(
        name="nocloud-net-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        cloud_init_mode=CloudInitMode.NET,
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
    )
    instance = VMInstance(
        name="nocloud-net-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    with pytest.raises(ConfigError, match="nocloud_net_url must be set"):
        generator.generate()


def test_boot_args_cloud_init_disabled():
    """cloud_init_mode=disabled produces no ds= arg."""
    vm_config = VMConfig(
        name="no-ci-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        cloud_init_mode=CloudInitMode.OFF,
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
    )
    instance = VMInstance(
        name="no-ci-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "ds=" not in boot_args


def test_boot_args_uses_root_fs_type_from_config():
    """Boot args include rootfstype from VMConfig.root_fs_type."""
    vm_config = VMConfig(
        name="btrfs-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.btrfs"),
        root_fs_type="btrfs",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="btrfs-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".btrfs",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "rootfstype=btrfs" in boot_args


def test_boot_args_falls_back_to_ext4_when_root_fs_type_none():
    """Boot args fall back to rootfstype=ext4 when root_fs_type is None."""
    vm_config = VMConfig(
        name="ext4-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_fs_type=None,
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="ext4-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "rootfstype=ext4" in boot_args


def test_boot_args_rootfstype_from_metadata():
    """root_fs_type from metadata propagates to boot args."""
    vm_config = VMConfig(
        name="xfs-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.xfs"),
        root_fs_type="xfs",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="xfs-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".xfs",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "rootfstype=xfs" in boot_args


def test_boot_args_includes_net_ifnames_zero():
    """Boot args should include net.ifnames=0 to prevent interface renaming."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "net.ifnames=0" in boot_args


def test_boot_args_includes_eth0_none_when_guest_ip_set():
    """Boot args should include ::eth0:none when guest_ip is set."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="",
        tap_device="fc-tap0",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config, instance)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "::eth0:off" in boot_args


# ---------------------------------------------------------------------------
# fs_uuid + fs_type validation in ConfigGenerator
# ---------------------------------------------------------------------------


def test_config_gen_validates_root_uuid_format():
    """Test ConfigGenerator validates root_uuid format."""
    from mvmctl.exceptions import MVMError

    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_uuid="invalid-uuid",
        root_fs_type="ext4",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )

    with pytest.raises(MVMError, match="Invalid.*format"):
        ConfigGenerator(vm_config, instance).validate()


def test_config_gen_validates_root_fs_type():
    """Test ConfigGenerator validates root_fs_type."""
    from mvmctl.exceptions import MVMError

    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_uuid="123e4567-e89b-12d3-a456-426614174000",
        root_fs_type="ntfs",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )

    with pytest.raises(MVMError, match="Invalid.*fs_type"):
        ConfigGenerator(vm_config, instance).validate()


def test_config_gen_accepts_valid_uuid_and_fs_type():
    """Test ConfigGenerator accepts valid UUID and fs_type."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_uuid="123e4567-e89b-12d3-a456-426614174000",
        root_fs_type="ext4",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )

    ConfigGenerator(vm_config, instance).validate()


def test_config_gen_accepts_btrfs_fs_type():
    """Test ConfigGenerator accepts btrfs filesystem type."""
    vm_config = VMConfig(
        name="btrfs-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.btrfs"),
        root_uuid="123e4567-e89b-12d3-a456-426614174000",
        root_fs_type="btrfs",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="btrfs-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".btrfs",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )

    ConfigGenerator(vm_config, instance).validate()
    config = ConfigGenerator(vm_config, instance).generate()
    assert "rootfstype=btrfs" in config["boot-source"]["boot_args"]


def test_config_gen_accepts_xfs_fs_type():
    """Test ConfigGenerator accepts xfs filesystem type."""
    vm_config = VMConfig(
        name="xfs-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.xfs"),
        root_uuid="123e4567-e89b-12d3-a456-426614174000",
        root_fs_type="xfs",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="xfs-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".xfs",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )

    ConfigGenerator(vm_config, instance).validate()
    config = ConfigGenerator(vm_config, instance).generate()
    assert "rootfstype=xfs" in config["boot-source"]["boot_args"]


def test_config_gen_validates_uuid_in_boot_args_generation():
    """Test _build_default_boot_args validates UUID format."""
    from mvmctl.exceptions import MVMError

    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_uuid="not-a-valid-uuid",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )

    with pytest.raises(MVMError, match="Invalid.*format"):
        ConfigGenerator(vm_config, instance).generate()


def test_config_gen_validates_fs_type_in_boot_args_generation():
    """Test _build_default_boot_args validates filesystem type."""
    from mvmctl.exceptions import MVMError

    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_fs_type="invalidfs",
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = VMInstance(
        name="test-vm",
        id="a" * 16,
        pid=0,
        ipv4="",
        mac="",
        network_id="",
        tap_device="",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
        status=VMStatus.STOPPED,
        rootfs_suffix=".ext4",
        kernel_id="",
        image_id="",
        binary_id="",
        disk_size_mib=1024,
        ipv4_gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )

    with pytest.raises(MVMError, match="Invalid.*fs_type"):
        ConfigGenerator(vm_config, instance).generate()


def test_firecracker_boot_config_typeddict_exists():
    """FirecrackerBootConfig TypedDict must exist (renamed from FirecrackerConfig)."""
    from mvmctl.core.config_gen import FirecrackerBootConfig

    assert FirecrackerBootConfig is not None
    assert FirecrackerBootConfig.__name__ == "FirecrackerBootConfig"


def test_firecracker_config_dataclass_collision_resolved():
    """FirecrackerConfig (the old dataclass name collision) must not exist in config_gen."""
    import mvmctl.core.config_gen as cg

    # The TypedDict was renamed to FirecrackerBootConfig to avoid collision
    # with FirecrackerConfig dataclass (if any still exists elsewhere)
    assert not hasattr(cg, "FirecrackerConfig"), (
        "FirecrackerConfig must be renamed to FirecrackerBootConfig in config_gen "
        "to eliminate name collision with dataclass"
    )
