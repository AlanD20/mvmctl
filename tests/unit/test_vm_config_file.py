"""Tests for VMCreateConfigFile model and vm_config API."""

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.api.vm_config import (
    build_vm_config_file,
    load_vm_config_file,
    merge_cli_overrides,
    save_vm_config_file,
)
from mvmctl.constants import (
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
)
from mvmctl.models.vm_config_file import VMCreateConfigFile


def test_vm_create_config_file_to_dict_roundtrip():
    cfg = VMCreateConfigFile(name="myvm", image="ubuntu-24.04", vcpus=4)
    d = cfg.to_dict()
    assert d["name"] == "myvm"
    assert d["image"] == "ubuntu-24.04"
    assert d["vcpus"] == 4
    assert d["mem"] == 2048
    assert d["firecracker_config"] is None


def test_vm_create_config_file_from_dict_ignores_unknown():
    cfg = VMCreateConfigFile.from_dict(
        {"name": "vm", "image": "img", "unknown_key": "value", "another": 42}
    )
    assert cfg.name == "vm"
    assert cfg.image == "img"
    assert not hasattr(cfg, "unknown_key")


def test_vm_create_config_file_from_dict_uses_defaults():
    cfg = VMCreateConfigFile.from_dict({"name": "vm", "image": "img"})
    assert cfg.vcpus == DEFAULT_VM_VCPU_COUNT
    assert cfg.mem == DEFAULT_VM_MEM_MIB
    assert cfg.user == DEFAULT_VM_SSH_USER
    assert cfg.enable_api_socket is True


def test_vm_create_config_file_from_json_file(tmp_path: Path):
    data = {"name": "myvm", "image": "ubuntu-24.04", "vcpus": 8, "mem": 4096}
    (tmp_path / "vm.json").write_text(json.dumps(data))
    cfg = VMCreateConfigFile.from_json_file(tmp_path / "vm.json")
    assert cfg.name == "myvm"
    assert cfg.vcpus == 8
    assert cfg.mem == 4096


def test_vm_create_config_file_from_json_file_missing_raises(tmp_path: Path):
    with pytest.raises(FileNotFoundError):
        VMCreateConfigFile.from_json_file(tmp_path / "nonexistent.json")


def test_vm_create_config_file_from_json_file_invalid_json_raises(tmp_path: Path):
    (tmp_path / "bad.json").write_text("not json {{{")
    with pytest.raises(ValueError, match="Invalid JSON"):
        VMCreateConfigFile.from_json_file(tmp_path / "bad.json")


def test_vm_create_config_file_from_json_file_non_object_raises(tmp_path: Path):
    (tmp_path / "list.json").write_text("[1, 2, 3]")
    with pytest.raises(ValueError, match="JSON object"):
        VMCreateConfigFile.from_json_file(tmp_path / "list.json")


def test_vm_create_config_file_to_json_file(tmp_path: Path):
    cfg = VMCreateConfigFile(name="myvm", image="ubuntu-24.04")
    cfg.to_json_file(tmp_path / "out.json")
    assert (tmp_path / "out.json").exists()
    data = json.loads((tmp_path / "out.json").read_text())
    assert data["name"] == "myvm"
    assert data["image"] == "ubuntu-24.04"


def test_vm_create_config_file_to_json_file_creates_parent(tmp_path: Path):
    cfg = VMCreateConfigFile(name="vm", image="img")
    path = tmp_path / "subdir" / "nested" / "vm.json"
    cfg.to_json_file(path)
    assert path.exists()


def test_load_vm_config_file(tmp_path: Path):
    data = {"name": "testvm", "image": "ubuntu", "vcpus": 2}
    (tmp_path / "vm.json").write_text(json.dumps(data))
    cfg = load_vm_config_file(tmp_path / "vm.json")
    assert cfg.name == "testvm"
    assert cfg.image == "ubuntu"


def test_save_vm_config_file(tmp_path: Path):
    cfg = VMCreateConfigFile(name="testvm", image="ubuntu-24.04")
    save_vm_config_file(cfg, tmp_path / "vm.json")
    assert (tmp_path / "vm.json").exists()
    data = json.loads((tmp_path / "vm.json").read_text())
    assert data["name"] == "testvm"


def test_merge_cli_overrides_applies_non_none_values():
    base = VMCreateConfigFile(name="oldvm", image="ubuntu", vcpus=2, mem=2048)
    result = merge_cli_overrides(base, name="newvm", vcpus=4, mem=8192)
    assert result.name == "newvm"
    assert result.vcpus == 4
    assert result.mem == 8192
    assert result.image == "ubuntu"


def test_merge_cli_overrides_keeps_base_for_none():
    base = VMCreateConfigFile(name="vm", image="ubuntu", vcpus=8)
    result = merge_cli_overrides(base, name=None, vcpus=None)
    assert result.name == "vm"
    assert result.vcpus == 8


def test_merge_cli_overrides_all_fields():
    base = VMCreateConfigFile(name="vm", image="img")
    result = merge_cli_overrides(
        base,
        name="new",
        image="new-img",
        kernel="/path/k",
        vcpus=4,
        mem=4096,
        ip="10.0.0.5",
        network="mynet",
        mac="02:fc:00:00:00:01",
        ssh_key="mykey",
        user="ubuntu",
        enable_api_socket=True,
        enable_pci=True,
        firecracker_bin="/bin/fc",
    )
    assert result.name == "new"
    assert result.kernel == "/path/k"
    assert result.ip == "10.0.0.5"
    assert result.network == "mynet"
    assert result.user == "ubuntu"
    assert result.enable_api_socket is True
    assert result.firecracker_bin == "/bin/fc"


def test_build_vm_config_file_includes_firecracker_config(
    tmp_path: Path,
):
    with patch("mvmctl.api.vm_config.ConfigGenerator") as mock_gen_cls:
        mock_gen = MagicMock()
        mock_gen.generate.return_value = {
            "boot-source": {"kernel_image_path": "/k", "boot_args": "console=ttyS0"},
            "machine-config": {"vcpu_count": 2, "mem_size_mib": 2048},
        }
        mock_gen_cls.return_value = mock_gen

        cfg = build_vm_config_file(
            name="myvm",
            image="ubuntu-24.04",
            vcpus=2,
            mem=2048,
        )

    assert cfg.name == "myvm"
    assert cfg.image == "ubuntu-24.04"
    assert cfg.firecracker_config is not None
    assert "boot-source" in cfg.firecracker_config


def test_build_vm_config_file_with_firecracker_config_error(
    tmp_path: Path,
):
    with patch("mvmctl.api.vm_config.ConfigGenerator", side_effect=Exception("fail")):
        cfg = build_vm_config_file(name="vm", image="img")

    assert cfg.firecracker_config == {}


def test_vm_create_config_file_cloud_init_to_dict():
    """Test that cloud_init field can be set and retrieved via to_dict()."""
    cloud_init_data = {"mode": "nocloud-net", "enabled": True}
    cfg = VMCreateConfigFile(name="myvm", image="ubuntu-24.04", cloud_init=cloud_init_data)
    d = cfg.to_dict()
    assert d["cloud_init"] == cloud_init_data
    assert d["name"] == "myvm"
    assert d["image"] == "ubuntu-24.04"


def test_vm_create_config_file_cloud_init_serialization(tmp_path: Path):
    """Test that cloud_init is properly serialized to JSON."""
    cloud_init_data = {"mode": "iso", "enabled": False, "iso_path": "/path/to/cloud-init.iso"}
    cfg = VMCreateConfigFile(name="myvm", image="ubuntu-24.04", cloud_init=cloud_init_data)
    cfg.to_json_file(tmp_path / "out.json")
    data = json.loads((tmp_path / "out.json").read_text())
    assert data["cloud_init"] == cloud_init_data


def test_vm_create_config_file_cloud_init_deserialization():
    """Test that cloud_init is properly deserialized from JSON via from_dict()."""
    cloud_init_data = {"mode": "auto", "enabled": True}
    cfg = VMCreateConfigFile.from_dict(
        {
            "name": "myvm",
            "image": "ubuntu-24.04",
            "cloud_init": cloud_init_data,
        }
    )
    assert cfg.cloud_init == cloud_init_data


def test_vm_create_config_file_cloud_init_roundtrip():
    """Test full roundtrip: create -> to_dict -> from_dict -> verify."""
    original_cloud_init = {"mode": "nocloud-net", "enabled": True, "user_data": "/tmp/user-data"}
    cfg = VMCreateConfigFile(name="testvm", image="ubuntu-24.04", cloud_init=original_cloud_init)
    d = cfg.to_dict()
    cfg2 = VMCreateConfigFile.from_dict(d)
    assert cfg2.cloud_init == original_cloud_init
    assert cfg2.name == "testvm"
    assert cfg2.image == "ubuntu-24.04"


def test_vm_create_config_file_cloud_init_none_default():
    """Test that cloud_init defaults to None when not provided."""
    cfg = VMCreateConfigFile.from_dict({"name": "vm", "image": "img"})
    assert cfg.cloud_init is None


def test_vm_create_config_file_cloud_init_in_json_file_roundtrip(tmp_path: Path):
    """Test cloud_init survives a to_json_file/from_json_file roundtrip."""
    cloud_init_data = {"mode": "disabled", "enabled": False}
    cfg = VMCreateConfigFile(name="myvm", image="ubuntu-24.04", cloud_init=cloud_init_data)
    cfg.to_json_file(tmp_path / "vm.json")
    cfg2 = VMCreateConfigFile.from_json_file(tmp_path / "vm.json")
    assert cfg2.cloud_init == cloud_init_data


def test_build_vm_config_file_includes_cloud_init(tmp_path: Path):
    """Test that build_vm_config_file includes cloud_init in returned config."""

    cloud_init_data = {"mode": "nocloud-net", "enabled": True}

    with patch("mvmctl.api.vm_config.ConfigGenerator") as mock_gen_cls:
        mock_gen = MagicMock()
        mock_gen.generate.return_value = {
            "boot-source": {"kernel_image_path": "/k", "boot_args": "console=ttyS0"},
            "machine-config": {"vcpu_count": 2, "mem_size_mib": 2048},
        }
        mock_gen_cls.return_value = mock_gen

        cfg = build_vm_config_file(
            name="myvm",
            image="ubuntu-24.04",
            vcpus=2,
            mem=2048,
            cloud_init=cloud_init_data,
        )

    assert cfg.name == "myvm"
    assert cfg.cloud_init == cloud_init_data
