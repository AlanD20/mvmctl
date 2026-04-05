"""Tests for api/vm_config.py."""

from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.api.vm_config import (
    build_vm_config_file,
    load_vm_config_file,
    merge_cli_overrides,
    save_vm_config_file,
)
from mvmctl.models.vm_config_file import VMCreateConfigFile


class TestLoadVmConfigFile:
    def test_delegates_to_model(self, tmp_path):
        config_file = tmp_path / "vm.json"
        config_file.write_text('{"name": "test", "image": "ubuntu-24.04"}')
        result = load_vm_config_file(config_file)
        assert result.name == "test"
        assert result.image == "ubuntu-24.04"

    def test_raises_on_missing_file(self, tmp_path):
        with pytest.raises(FileNotFoundError):
            load_vm_config_file(tmp_path / "nonexistent.json")


class TestSaveVmConfigFile:
    def test_delegates_to_model(self, tmp_path):
        config = VMCreateConfigFile(name="test", image="ubuntu-24.04")
        out_path = tmp_path / "out.json"
        save_vm_config_file(config, out_path)
        assert out_path.exists()


class TestMergeCliOverrides:
    def test_overrides_name_only(self):
        base = VMCreateConfigFile(name="old", image="ubuntu", vcpus=2)
        result = merge_cli_overrides(base, name="new")
        assert result.name == "new"
        assert result.image == "ubuntu"
        assert result.vcpus == 2

    def test_preserves_base_when_no_override(self):
        base = VMCreateConfigFile(name="old", image="ubuntu", vcpus=2, mem=512)
        result = merge_cli_overrides(base)
        assert result.name == "old"
        assert result.image == "ubuntu"
        assert result.vcpus == 2
        assert result.mem == 512

    def test_overrides_multiple_fields(self):
        base = VMCreateConfigFile(name="old", image="ubuntu", vcpus=2)
        result = merge_cli_overrides(base, name="new", vcpus=4, mem=1024)
        assert result.name == "new"
        assert result.vcpus == 4
        assert result.mem == 1024
        assert result.image == "ubuntu"


class TestBuildVmConfigFile:
    def test_returns_vm_create_config_file(self):
        with patch("mvmctl.api.vm_config.ConfigGenerator") as mock_gen:
            mock_gen.return_value.generate.return_value = {"boot-source": {}}
            result = build_vm_config_file(name="test", image="ubuntu-24.04")
            assert isinstance(result, VMCreateConfigFile)
            assert result.name == "test"
            assert result.image == "ubuntu-24.04"

    def test_uses_provided_values_over_defaults(self):
        with patch("mvmctl.api.vm_config.ConfigGenerator") as mock_gen:
            mock_gen.return_value.generate.return_value = {}
            result = build_vm_config_file(name="test", image="ubuntu", vcpus=4, mem=1024)
            assert result.vcpus == 4
            assert result.mem == 1024

    def test_handles_config_generator_exception(self):
        with patch("mvmctl.api.vm_config.ConfigGenerator") as mock_gen:
            mock_gen.return_value.generate.side_effect = RuntimeError("fail")
            result = build_vm_config_file(name="test", image="ubuntu")
            assert result.firecracker_config == {}

    def test_passes_kernel_rootfs_gateway_subnet(self):
        with patch("mvmctl.api.vm_config.ConfigGenerator") as mock_gen:
            mock_gen.return_value.generate.return_value = {}
            result = build_vm_config_file(
                name="test",
                image="ubuntu",
                kernel="/boot/vmlinux",
                rootfs_path=Path("/tmp/root.ext4"),
                ipv4_gateway="10.0.0.1",
                subnet_mask="255.255.255.0",
            )
            assert result.kernel == "/boot/vmlinux"
            vm_config_arg = mock_gen.call_args[0][0]
            vm_instance_arg = mock_gen.call_args[0][1]
            assert vm_config_arg.kernel_path.as_posix().endswith("vmlinux")
            assert vm_config_arg.rootfs_path.as_posix().endswith("root.ext4")
            assert vm_instance_arg.ipv4_gateway == "10.0.0.1"
            assert vm_instance_arg.subnet_mask == "255.255.255.0"
