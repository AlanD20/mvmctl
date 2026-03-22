"""Tests for config generation."""

from pathlib import Path

from fcm.core.config_gen import ConfigGenerator
from fcm.models.vm import VMConfig


def test_config_generator_basic():
    """Test basic config generation."""
    vm_config = VMConfig(
        name="test-vm",
        vcpu_count=2,
        mem_size_mib=512,
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        guest_ip="10.0.0.2",
        guest_mac="02:FC:00:00:00:01",
        tap_device="fc-tap0",
    )

    generator = ConfigGenerator(vm_config)
    config = generator.generate()

    assert config["machine-config"]["vcpu_count"] == 2
    assert config["machine-config"]["mem_size_mib"] == 512
    assert config["boot-source"]["kernel_image_path"] == "/tmp/vmlinux"
    assert len(config["drives"]) == 1
    assert config["drives"][0]["path_on_host"] == "/tmp/rootfs.ext4"


def test_config_generator_network():
    """Test network interface configuration."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )

    generator = ConfigGenerator(vm_config)
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
    )

    generator = ConfigGenerator(vm_config)
    config = generator.generate()

    assert len(config["network-interfaces"]) == 0
