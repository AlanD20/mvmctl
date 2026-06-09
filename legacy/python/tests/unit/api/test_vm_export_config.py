"""Tests for VMExportConfig and related models — portable VM configuration."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from mvmctl.api.inputs._vm_export_config import (
    VMExportBinaryConfig,
    VMExportBootConfig,
    VMExportCloudInitConfig,
    VMExportComputeConfig,
    VMExportConfig,
    VMExportFirecrackerConfig,
    VMExportImageConfig,
    VMExportKernelConfig,
    VMExportNetworkConfig,
)


class TestVMExportConfigToDict:
    """Tests for VMExportConfig.to_dict()."""

    def test_to_dict_produces_clean_nested_dict(self) -> None:
        """to_dict() produces a clean nested dict structure."""
        cfg = VMExportConfig(
            name="myvm",
            compute=VMExportComputeConfig(vcpus=4, mem=2048),
            image=VMExportImageConfig(type="ubuntu-24.04", arch="x86_64"),
        )
        d = cfg.to_dict()
        assert d["name"] == "myvm"
        assert d["compute"]["vcpus"] == 4
        assert d["compute"]["mem"] == 2048
        assert d["image"]["type"] == "ubuntu-24.04"
        assert d["image"]["arch"] == "x86_64"

    def test_to_dict_omits_none_values(self) -> None:
        """None values are omitted from export dict for cleanliness."""
        cfg = VMExportConfig(
            name="myvm",
            compute=VMExportComputeConfig(vcpus=None, mem=2048),
        )
        d = cfg.to_dict()
        assert "vcpus" not in d["compute"]
        assert d["compute"]["mem"] == 2048

    def test_to_dict_omits_none_in_all_sub_configs(self) -> None:
        """None values in all sub-configs are omitted, leaving empty dicts."""
        cfg = VMExportConfig(name="minimal")
        d = cfg.to_dict()
        # Sub-configs with all-None values become empty dicts
        assert "compute" in d
        assert d["compute"] == {}
        assert "image" in d
        assert d["image"] == {}
        assert d["name"] == "minimal"
        assert d.get("schema_version") == "1.0"

    def test_to_dict_includes_schema_version(self) -> None:
        """schema_version is always present in to_dict()."""
        cfg = VMExportConfig(name="test")
        d = cfg.to_dict()
        assert d["schema_version"] == "1.0"


class TestVMExportConfigFromDict:
    """Tests for VMExportConfig.from_dict()."""

    def test_from_dict_ignores_unknown_fields(self) -> None:
        """Unknown fields are silently ignored for forward compatibility."""
        cfg = VMExportConfig.from_dict(
            {
                "name": "vm",
                "image": {
                    "type": "ubuntu",
                    "arch": "x86_64",
                },
                "unknown_key": "value",
                "another": 42,
            }
        )
        assert cfg.name == "vm"
        assert cfg.image.type == "ubuntu"
        assert not hasattr(cfg, "unknown_key")

    def test_from_dict_preserves_nested_sub_configs(self) -> None:
        """Nested sub-configs are properly deserialized."""
        cfg = VMExportConfig.from_dict(
            {
                "name": "vm",
                "compute": {"vcpus": 8, "mem": 4096},
                "image": {
                    "type": "debian",
                    "arch": "arm64",
                    "disk_size": "10G",
                },
                "kernel": {
                    "version": "6.1.0",
                    "arch": "arm64",
                    "type": "vmlinux",
                },
                "binary": {
                    "name": "firecracker",
                    "version": "v1.15.0",
                },
            }
        )
        assert cfg.compute.vcpus == 8
        assert cfg.image.type == "debian"
        assert cfg.image.disk_size == "10G"
        assert cfg.kernel.version == "6.1.0"
        assert cfg.kernel.type == "vmlinux"
        assert cfg.binary.name == "firecracker"

    def test_from_dict_empty_uses_defaults(self) -> None:
        """from_dict() with minimal data uses default factory values."""
        cfg = VMExportConfig.from_dict({"name": "test"})
        assert cfg.name == "test"
        assert cfg.compute.vcpus is None
        assert cfg.kernel.version is None
        assert cfg.binary.name == "firecracker"

    def test_from_dict_ignores_unknown_nested_fields(self) -> None:
        """Unknown fields in nested sub-configs are ignored."""
        cfg = VMExportConfig.from_dict(
            {
                "name": "vm",
                "compute": {
                    "vcpus": 4,
                    "unknown_field": "should be ignored",
                    "mem": 1024,
                },
            }
        )
        assert cfg.compute.vcpus == 4
        assert cfg.compute.mem == 1024

    def test_from_dict_network_sub_config(self) -> None:
        """Network sub-config is properly deserialized."""
        cfg = VMExportConfig.from_dict(
            {
                "name": "net-vm",
                "network": {
                    "name": "custom-net",
                    "subnet": "10.0.0.0/24",
                    "ipv4_gateway": "10.0.0.1",
                    "nat_enabled": True,
                    "ip": "10.0.0.5",
                    "mac": "02:FC:00:00:00:01",
                },
            }
        )
        assert cfg.network.name == "custom-net"
        assert cfg.network.subnet == "10.0.0.0/24"
        assert cfg.network.nat_enabled is True
        assert cfg.network.ip == "10.0.0.5"

    def test_from_dict_boot_config(self) -> None:
        """Boot sub-config is properly deserialized."""
        cfg = VMExportConfig.from_dict(
            {
                "name": "boot-vm",
                "boot": {
                    "args": "console=ttyS0 reboot=k",
                    "enable_console": True,
                },
            }
        )
        assert cfg.boot.args == "console=ttyS0 reboot=k"
        assert cfg.boot.enable_console is True

    def test_from_dict_firecracker_config(self) -> None:
        """Firecracker sub-config is properly deserialized."""
        cfg = VMExportConfig.from_dict(
            {
                "name": "fc-vm",
                "firecracker": {
                    "enable_api_socket": True,
                    "pci_enabled": False,
                    "lsm_flags": "landlock,selinux",
                },
            }
        )
        assert cfg.firecracker.enable_api_socket is True
        assert cfg.firecracker.pci_enabled is False
        assert cfg.firecracker.lsm_flags == "landlock,selinux"

    def test_from_dict_cloud_init_config(self) -> None:
        """Cloud-init sub-config is properly deserialized."""
        cfg = VMExportConfig.from_dict(
            {
                "name": "ci-vm",
                "cloud_init": {
                    "mode": "net",
                    "user": "admin",
                    "ssh_key": "my-key",
                    "keep_iso": True,
                    "nocloud_net_port": 8123,
                },
            }
        )
        assert cfg.cloud_init.mode == "net"
        assert cfg.cloud_init.user == "admin"
        assert cfg.cloud_init.keep_iso is True
        assert cfg.cloud_init.nocloud_net_port == 8123


class TestVMExportConfigRoundtrip:
    """Full roundtrip tests for VMExportConfig."""

    def test_to_dict_from_dict_roundtrip(self) -> None:
        """Full roundtrip: create -> to_dict -> from_dict -> verify."""
        original = VMExportConfig(
            name="testvm",
            schema_version="1.0",
            compute=VMExportComputeConfig(vcpus=2, mem=1024),
            image=VMExportImageConfig(
                type="ubuntu-24.04",
                arch="x86_64",
                disk_size="2G",
            ),
            kernel=VMExportKernelConfig(
                version="6.1.0", arch="x86_64", type="vmlinux"
            ),
            binary=VMExportBinaryConfig(name="firecracker", version="v1.15.0"),
            network=VMExportNetworkConfig(
                name="default",
                subnet="172.35.0.0/24",
                ipv4_gateway="172.35.0.1",
            ),
            boot=VMExportBootConfig(args="console=ttyS0", enable_console=True),
            firecracker=VMExportFirecrackerConfig(
                enable_api_socket=True, pci_enabled=False
            ),
            cloud_init=VMExportCloudInitConfig(mode="inject", user="root"),
        )

        d = original.to_dict()
        restored = VMExportConfig.from_dict(d)

        assert restored.name == "testvm"
        assert restored.compute.vcpus == 2
        assert restored.image.type == "ubuntu-24.04"
        assert restored.kernel.type == "vmlinux"
        assert restored.binary.version == "v1.15.0"
        assert restored.network.subnet == "172.35.0.0/24"
        assert restored.boot.args == "console=ttyS0"
        assert restored.cloud_init.mode == "inject"

    def test_empty_to_dict_from_dict_roundtrip(self) -> None:
        """Minimal config roundtrips without error."""
        original = VMExportConfig(name="minimal")
        d = original.to_dict()
        restored = VMExportConfig.from_dict(d)
        assert restored.name == "minimal"
        assert restored.schema_version == "1.0"

    def test_roundtrip_preserves_all_sub_configs(self) -> None:
        """All sub-config types survive a roundtrip."""
        original = VMExportConfig(
            name="full-vm",
            compute=VMExportComputeConfig(vcpus=4, mem=2048),
            image=VMExportImageConfig(
                type="alpine-3.21",
                arch="aarch64",
                disk_size="5G",
            ),
            kernel=VMExportKernelConfig(
                version="6.6.0",
                arch="aarch64",
                type="bzImage",
            ),
            binary=VMExportBinaryConfig(
                name="firecracker",
                version="v1.14.0",
            ),
            network=VMExportNetworkConfig(
                name="dmz",
                subnet="10.99.0.0/16",
                ipv4_gateway="10.99.0.1",
                nat_gateways="10.99.0.1",
                nat_enabled=True,
                ip="10.99.0.10",
                mac="AA:BB:CC:DD:EE:FF",
            ),
            boot=VMExportBootConfig(
                args="console=hvc0",
                enable_console=False,
            ),
            firecracker=VMExportFirecrackerConfig(
                enable_api_socket=False,
                pci_enabled=True,
                lsm_flags="apparmor",
            ),
            cloud_init=VMExportCloudInitConfig(
                mode="iso",
                user="deploy",
                ssh_key="deploy-key",
                keep_iso=False,
                nocloud_net_port=0,
            ),
        )

        d = original.to_dict()
        restored = VMExportConfig.from_dict(d)

        assert restored.compute.vcpus == 4
        assert restored.image.type == "alpine-3.21"
        assert restored.kernel.version == "6.6.0"
        assert restored.binary.name == "firecracker"
        assert restored.network.subnet == "10.99.0.0/16"
        assert restored.boot.args == "console=hvc0"
        assert restored.firecracker.lsm_flags == "apparmor"
        assert restored.cloud_init.mode == "iso"


class TestVMExportConfigJsonFile:
    """Tests for JSON file import/export."""

    def test_to_json_file(self, tmp_path: Path) -> None:
        """Export to JSON file produces valid JSON."""
        cfg = VMExportConfig(
            name="myvm",
            image=VMExportImageConfig(type="ubuntu-24.04", arch="x86_64"),
        )
        cfg.to_json_file(tmp_path / "out.json")
        assert (tmp_path / "out.json").exists()
        data = json.loads((tmp_path / "out.json").read_text())
        assert data["name"] == "myvm"
        assert data["image"]["type"] == "ubuntu-24.04"

    def test_to_json_file_creates_parent_dirs(self, tmp_path: Path) -> None:
        """Parent directories are created if needed."""
        cfg = VMExportConfig(name="vm", image=VMExportImageConfig())
        path = tmp_path / "subdir" / "nested" / "vm.json"
        cfg.to_json_file(path)
        assert path.exists()

    def test_from_json_file(self, tmp_path: Path) -> None:
        """Load from JSON file with nested structure."""
        data = {
            "name": "myvm",
            "compute": {"vcpus": 8, "mem": 4096},
            "image": {
                "type": "ubuntu-24.04",
                "arch": "x86_64",
            },
        }
        (tmp_path / "vm.json").write_text(json.dumps(data))
        cfg = VMExportConfig.from_json_file(tmp_path / "vm.json")
        assert cfg.name == "myvm"
        assert cfg.compute.vcpus == 8
        assert cfg.image.arch == "x86_64"

    def test_from_json_file_missing_raises(self, tmp_path: Path) -> None:
        """Loading non-existent file raises FileNotFoundError."""
        with pytest.raises(FileNotFoundError):
            VMExportConfig.from_json_file(tmp_path / "nonexistent.json")

    def test_from_json_file_invalid_json_raises(self, tmp_path: Path) -> None:
        """Invalid JSON raises ValueError."""
        (tmp_path / "bad.json").write_text("not json {{{")
        with pytest.raises(ValueError, match="Invalid JSON"):
            VMExportConfig.from_json_file(tmp_path / "bad.json")

    def test_from_json_file_non_object_raises(self, tmp_path: Path) -> None:
        """Non-object JSON raises ValueError."""
        (tmp_path / "list.json").write_text("[1, 2, 3]")
        with pytest.raises(ValueError, match="JSON object"):
            VMExportConfig.from_json_file(tmp_path / "list.json")

    def test_json_roundtrip(self, tmp_path: Path) -> None:
        """Full JSON file roundtrip: to_json -> from_json -> verify."""
        original = VMExportConfig(
            name="roundtrip",
            compute=VMExportComputeConfig(vcpus=2, mem=512),
            image=VMExportImageConfig(type="ubuntu-22.04", arch="x86_64"),
        )
        path = tmp_path / "export.json"
        original.to_json_file(path)
        restored = VMExportConfig.from_json_file(path)
        assert restored.name == "roundtrip"
        assert restored.compute.vcpus == 2
        assert restored.image.type == "ubuntu-22.04"


class TestVMExportNoInternalIds:
    """VMExportConfig must never contain internal SHA256 IDs."""

    def test_no_internal_ids_in_to_dict(self) -> None:
        """to_dict() output must not contain image_id, kernel_id, etc."""
        cfg = VMExportConfig(
            name="test",
            image=VMExportImageConfig(type="ubuntu-24.04", arch="x86_64"),
        )
        output = str(cfg.to_dict())
        assert "image_id" not in output
        assert "kernel_id" not in output
        assert "binary_id" not in output
        assert "network_id" not in output

    def test_no_internal_ids_in_full_config(self) -> None:
        """Full config with all sub-configs has no internal IDs."""
        cfg = VMExportConfig(
            name="full-test",
            compute=VMExportComputeConfig(vcpus=2, mem=1024),
            image=VMExportImageConfig(type="alpine-3.21", arch="x86_64"),
            kernel=VMExportKernelConfig(
                version="5.15.0", arch="x86_64", type="vmlinux"
            ),
            binary=VMExportBinaryConfig(name="firecracker", version="v1.14.0"),
            network=VMExportNetworkConfig(
                name="custom-net", subnet="10.0.0.0/24"
            ),
        )
        output = str(cfg.to_dict())
        assert "image_id" not in output
        assert "kernel_id" not in output
        assert "binary_id" not in output
        assert "network_id" not in output

    def test_portable_refs_only(self) -> None:
        """Only portable semantic refs are used, never internal IDs."""
        cfg = VMExportConfig(
            name="export-test",
            image=VMExportImageConfig(type="alpine-3.21", arch="x86_64"),
            kernel=VMExportKernelConfig(
                version="5.15.0", arch="x86_64", type="vmlinux"
            ),
            binary=VMExportBinaryConfig(name="firecracker", version="v1.14.0"),
            network=VMExportNetworkConfig(
                name="custom-net", subnet="10.0.0.0/24"
            ),
        )
        assert cfg.image.type == "alpine-3.21"
        assert cfg.kernel.version == "5.15.0"
        assert cfg.binary.version == "v1.14.0"
        assert cfg.network.name == "custom-net"


class TestSubConfigDataclasses:
    """Individual sub-config dataclass tests."""

    def test_compute_config_defaults(self) -> None:
        """VMExportComputeConfig defaults."""
        c = VMExportComputeConfig()
        assert c.vcpus is None
        assert c.mem is None

    def test_image_config_defaults(self) -> None:
        """VMExportImageConfig defaults."""
        c = VMExportImageConfig()
        assert c.type is None
        assert c.arch is None
        assert c.disk_size is None

    def test_kernel_config_defaults(self) -> None:
        """VMExportKernelConfig defaults."""
        c = VMExportKernelConfig()
        assert c.version is None
        assert c.arch is None
        assert c.type is None

    def test_binary_config_defaults(self) -> None:
        """VMExportBinaryConfig defaults (name is structural constant)."""
        c = VMExportBinaryConfig()
        assert c.name == "firecracker"
        assert c.version is None

    def test_network_config_defaults(self) -> None:
        """VMExportNetworkConfig defaults."""
        c = VMExportNetworkConfig()
        assert c.name is None
        assert c.subnet is None
        assert c.nat_enabled is None

    def test_boot_config_defaults(self) -> None:
        """VMExportBootConfig defaults."""
        c = VMExportBootConfig()
        assert c.args is None
        assert c.enable_console is None

    def test_firecracker_config_defaults(self) -> None:
        """VMExportFirecrackerConfig defaults."""
        c = VMExportFirecrackerConfig()
        assert c.enable_api_socket is None
        assert c.pci_enabled is None
        assert c.lsm_flags is None

    def test_cloud_init_config_defaults(self) -> None:
        """VMExportCloudInitConfig defaults."""
        c = VMExportCloudInitConfig()
        assert c.mode is None
        assert c.user is None
        assert c.ssh_key is None
        assert c.keep_iso is None
        assert c.nocloud_net_port is None
