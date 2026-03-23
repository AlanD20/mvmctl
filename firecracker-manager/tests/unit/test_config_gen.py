"""Tests for config generation."""

from pathlib import Path

import pytest

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


# ---------------------------------------------------------------------------
# S-H9: Boot arg injection validation
# ---------------------------------------------------------------------------


def test_boot_args_rejects_shell_injection_in_guest_ip():
    """guest_ip with shell metacharacters should raise FCMError."""
    from fcm.exceptions import FCMError

    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        guest_ip="10.0.0.2;rm -rf /",
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
    with pytest.raises(FCMError, match="guest_ip"):
        generator.generate()


def test_boot_args_rejects_shell_injection_in_gateway():
    """gateway with pipe character should raise FCMError."""
    from fcm.exceptions import FCMError

    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        guest_ip="10.0.0.2",
        gateway="10.0.0.1|evil",
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
    with pytest.raises(FCMError, match="gateway"):
        generator.generate()


def test_boot_args_accepts_normal_ip():
    """Normal IP addresses should pass validation without error."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        guest_ip="10.0.0.2",
        gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
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
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["boot-source"]["kernel_image_path"] == "/tmp/vmlinux"
    assert config["drives"][0]["path_on_host"] == "/tmp/rootfs.ext4"


def test_config_gen_zero_vcpus():
    vm_config = VMConfig(
        name="zero-cpu",
        vcpu_count=0,
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["machine-config"]["vcpu_count"] == 0


def test_config_gen_zero_memory():
    vm_config = VMConfig(
        name="zero-mem",
        mem_size_mib=0,
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["machine-config"]["mem_size_mib"] == 0


def test_config_gen_missing_kernel_path_default():
    vm_config = VMConfig(name="no-kernel")
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["boot-source"]["kernel_image_path"] == "vmlinux"


def test_config_gen_missing_rootfs_path_default():
    vm_config = VMConfig(name="no-rootfs")
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["drives"][0]["path_on_host"] == "rootfs.ext4"


def test_config_gen_invalid_ip_with_shell_chars():
    from fcm.exceptions import FCMError

    vm_config = VMConfig(
        name="bad-ip",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        guest_ip="not-a-valid;ip",
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
    with pytest.raises(FCMError, match="guest_ip"):
        generator.generate()


def test_config_gen_write_to_file(tmp_path):
    import json as _json

    vm_config = VMConfig(
        name="file-test",
        vcpu_count=1,
        mem_size_mib=256,
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
    )
    generator = ConfigGenerator(vm_config)
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
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert "pci=off" not in config["boot-source"]["boot_args"]


def test_config_gen_custom_boot_args():
    vm_config = VMConfig(
        name="custom-boot",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        boot_args="console=ttyS0 custom=yes",
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["boot-source"]["boot_args"] == "console=ttyS0 custom=yes"


def test_config_gen_no_guest_ip_omits_ip_arg():
    vm_config = VMConfig(
        name="no-ip",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert "ip=" not in config["boot-source"]["boot_args"]
