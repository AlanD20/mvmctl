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
from mvmctl.models.vm_config_file import VMExportConfig, VMExportImageConfig


class TestLoadVmConfigFile:
    def test_delegates_to_model(self, tmp_path):
        config_file = tmp_path / "vm.json"
        config_file.write_text('{"name": "test", "image": {"os_slug": "ubuntu-24.04"}}')
        result = load_vm_config_file(config_file)
        assert result.name == "test"
        assert result.image.os_slug == "ubuntu-24.04"

    def test_raises_on_missing_file(self, tmp_path):
        with pytest.raises(FileNotFoundError):
            load_vm_config_file(tmp_path / "nonexistent.json")


class TestSaveVmConfigFile:
    def test_delegates_to_model(self, tmp_path):
        config = VMExportConfig(name="test", image=VMExportImageConfig(os_slug="ubuntu-24.04"))
        out_path = tmp_path / "out.json"
        save_vm_config_file(config, out_path)
        assert out_path.exists()


class TestMergeCliOverrides:
    def test_overrides_name_only(self):
        base = VMExportConfig(name="old", image=VMExportImageConfig(os_slug="ubuntu"))
        result = merge_cli_overrides(base, name="new")
        assert result.name == "new"
        assert result.image.os_slug == "ubuntu"

    def test_preserves_base_when_no_override(self):
        base = VMExportConfig(name="old", image=VMExportImageConfig(os_slug="ubuntu"))
        result = merge_cli_overrides(base)
        assert result.name == "old"
        assert result.image.os_slug == "ubuntu"

    def test_overrides_compute_fields(self):
        from mvmctl.models.vm_config_file import VMExportComputeConfig

        base = VMExportConfig(
            name="old",
            image=VMExportImageConfig(os_slug="ubuntu"),
            compute=VMExportComputeConfig(vcpus=2, mem=512),
        )
        result = merge_cli_overrides(base, vcpus=4, mem=1024)
        assert result.name == "old"
        assert result.compute.vcpus == 4
        assert result.compute.mem == 1024


class TestBuildVmConfigFile:
    def test_returns_vm_export_config(self):
        with patch("mvmctl.api.vm_config.ConfigGenerator") as mock_gen:
            mock_gen.return_value.generate.return_value = {"boot-source": {}}
            result = build_vm_config_file(name="test", image="ubuntu-24.04")
            assert isinstance(result, VMExportConfig)
            assert result.name == "test"
            assert result.image.os_slug == "ubuntu-24.04"

    def test_uses_provided_values(self):
        with patch("mvmctl.api.vm_config.ConfigGenerator") as mock_gen:
            mock_gen.return_value.generate.return_value = {}
            result = build_vm_config_file(name="test", image="ubuntu", vcpus=4, mem=1024)
            assert result.compute.vcpus == 4
            assert result.compute.mem == 1024

    def test_handles_config_generator_exception(self):
        with patch("mvmctl.api.vm_config.ConfigGenerator") as mock_gen:
            mock_gen.return_value.generate.side_effect = RuntimeError("fail")
            result = build_vm_config_file(name="test", image="ubuntu")
            # Firecracker config won't be populated on exception
            assert result.name == "test"

    def test_passes_kernel_and_network_params(self):
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
            # Verify kernel version is extracted from path
            assert result.kernel.version == "vmlinux"
            vm_config_arg = mock_gen.call_args[0][0]
            vm_instance_arg = mock_gen.call_args[0][1]
            assert vm_config_arg.kernel_path.as_posix().endswith("vmlinux")
            assert vm_config_arg.rootfs_path.as_posix().endswith("root.ext4")
            assert vm_instance_arg.ipv4_gateway == "10.0.0.1"
            assert vm_instance_arg.subnet_mask == "255.255.255.0"
