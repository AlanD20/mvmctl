"""Tests for config generation."""

from pathlib import Path

import pytest

from mvmctl.constants import DEFAULT_LIBGUESTFS_SEED_DIR
from mvmctl.core.config_gen import ConfigGenerator
from mvmctl.models import CloudInitMode
from mvmctl.models.vm import VMConfig


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


def test_config_generator_sets_root_uuid_in_boot_args():
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_uuid="123e4567-e89b-12d3-a456-426614174000",
    )

    config = ConfigGenerator(vm_config).generate()

    assert config["drives"][0]["partuuid"] is None
    assert "root=UUID=123e4567-e89b-12d3-a456-426614174000" in config["boot-source"]["boot_args"]


def test_config_generator_overrides_existing_root_arg_with_uuid():
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        root_uuid="123e4567-e89b-12d3-a456-426614174001",
        boot_args="console=ttyS0 root=/dev/vda rw",
    )

    config = ConfigGenerator(vm_config).generate()
    boot_args = config["boot-source"]["boot_args"]

    assert "root=/dev/vda" not in boot_args
    assert "root=UUID=123e4567-e89b-12d3-a456-426614174001" in boot_args


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
    """guest_ip with shell metacharacters should raise MVMError."""
    from mvmctl.exceptions import MVMError

    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("vmlinux"),
        rootfs_path=Path("rootfs.ext4"),
        guest_ip="10.0.0.2;rm -rf /",
        gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
    with pytest.raises(MVMError, match="guest_ip"):
        generator.validate()


def test_boot_args_rejects_shell_injection_in_gateway():
    """gateway with pipe character should raise MVMError."""
    from mvmctl.exceptions import MVMError

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
    with pytest.raises(MVMError, match="gateway"):
        generator.validate()


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
    """VMConfig with vcpu_count=0 raises ValueError (out of bounds)."""
    with pytest.raises(ValueError, match="vcpu_count must be between"):
        VMConfig(
            name="zero-cpu",
            vcpu_count=0,
            kernel_path=Path("/tmp/vmlinux"),
            rootfs_path=Path("/tmp/rootfs.ext4"),
        )


def test_config_gen_zero_memory():
    """VMConfig with mem_size_mib=0 raises ValueError (out of bounds)."""
    with pytest.raises(ValueError, match="mem_size_mib must be between"):
        VMConfig(
            name="zero-mem",
            mem_size_mib=0,
            kernel_path=Path("/tmp/vmlinux"),
            rootfs_path=Path("/tmp/rootfs.ext4"),
        )


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
    from mvmctl.exceptions import MVMError

    vm_config = VMConfig(
        name="bad-ip",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        guest_ip="not-a-valid;ip",
        gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
    with pytest.raises(MVMError, match="guest_ip"):
        generator.validate()


def test_config_gen_write_to_file(tmp_path):
    import json as _json

    vm_config = VMConfig(
        name="file-test",
        vcpu_count=1,
        mem_size_mib=256,
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
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
    )
    generator = ConfigGenerator(vm_config)
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
        cloud_init_mode=CloudInitMode.AUTO,
        cloud_init_iso_path=Path("/tmp/cloud-init.iso"),
    )

    config = ConfigGenerator(vm_config).generate()
    cloud_init_drives = [drive for drive in config["drives"] if drive["drive_id"] == "cloud-init"]

    assert len(cloud_init_drives) == 1
    assert cloud_init_drives[0]["path_on_host"] == "/tmp/cloud-init.iso"


def test_config_gen_logging_disabled():
    vm_config = VMConfig(
        name="no-log",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        enable_logging=False,
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["logger"] is None


def test_config_gen_logging_enabled_by_default():
    vm_config = VMConfig(
        name="with-log",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["logger"] is not None
    assert "log_path" in config["logger"]


def test_config_gen_metrics_disabled_by_default():
    vm_config = VMConfig(
        name="no-metrics",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["metrics"] is None


def test_config_gen_metrics_enabled():
    vm_config = VMConfig(
        name="with-metrics",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        enable_metrics=True,
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert config["metrics"] is not None
    assert "metrics_path" in config["metrics"]


def test_boot_args_nocloud_ds_default():
    """nocloud (default) mode produces ds=nocloud."""
    vm_config = VMConfig(
        name="nocloud-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    assert "ds=nocloud" in config["boot-source"]["boot_args"]


def test_boot_args_nocloud_net_ds():
    """nocloud-net mode uses nocloud_net_url from config."""
    vm_config = VMConfig(
        name="nocloud-net-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        cloud_init_mode=CloudInitMode.NO_CLOUD_NET,
        nocloud_net_url="http://192.168.1.1:8123/",
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "ds=nocloud;seedfrom=http://192.168.1.1:8123/" in boot_args


def test_boot_args_direct_injection_seed_dir_uses_constant():
    vm_config = VMConfig(
        name="direct-injection-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        cloud_init_mode=CloudInitMode.DIRECT_INJECTION,
    )

    config = ConfigGenerator(vm_config).generate()

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
        cloud_init_mode=CloudInitMode.NO_CLOUD_NET,
    )
    generator = ConfigGenerator(vm_config)
    with pytest.raises(ConfigError, match="nocloud_net_url must be set"):
        generator.generate()


def test_boot_args_cloud_init_disabled():
    """cloud_init_mode=disabled produces no ds= arg."""
    vm_config = VMConfig(
        name="no-ci-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        cloud_init_mode=CloudInitMode.DISABLED,
    )
    generator = ConfigGenerator(vm_config)
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
        gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config)
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
        gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config)
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
        gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "rootfstype=xfs" in boot_args


def test_boot_args_includes_net_ifnames_zero():
    """Boot args should include net.ifnames=0 to prevent interface renaming."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        guest_ip="10.0.0.2",
        gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "net.ifnames=0" in boot_args


def test_boot_args_includes_eth0_none_when_guest_ip_set():
    """Boot args should include ::eth0:none when guest_ip is set."""
    vm_config = VMConfig(
        name="test-vm",
        kernel_path=Path("/tmp/vmlinux"),
        rootfs_path=Path("/tmp/rootfs.ext4"),
        guest_ip="10.0.0.2",
        gateway="10.0.0.1",
        subnet_mask="255.255.255.0",
        tap_device="fc-tap0",
        guest_mac="02:FC:00:00:00:01",
    )
    generator = ConfigGenerator(vm_config)
    config = generator.generate()
    boot_args = config["boot-source"]["boot_args"]
    assert "::eth0:none" in boot_args
