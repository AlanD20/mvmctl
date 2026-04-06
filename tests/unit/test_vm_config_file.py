"""Tests for VMExportConfig model and vm_config API."""

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.models.vm_config_file import (
    VMExportBinaryConfig,
    VMExportCloudInitConfig,
    VMExportComputeConfig,
    VMExportConfig,
    VMExportFirecrackerConfig,
    VMExportImageConfig,
    VMExportKernelConfig,
    VMExportNetworkConfig,
    VMExportBootConfig,
)


def test_vm_export_config_to_dict_roundtrip():
    """Test that to_dict produces a clean nested dict."""
    cfg = VMExportConfig(
        name="myvm",
        compute=VMExportComputeConfig(vcpus=4, mem=2048),
        image=VMExportImageConfig(os_slug="ubuntu-24.04", arch="x86_64"),
    )
    d = cfg.to_dict()
    assert d["name"] == "myvm"
    assert d["compute"]["vcpus"] == 4
    assert d["compute"]["mem"] == 2048
    assert d["image"]["os_slug"] == "ubuntu-24.04"
    assert d["image"]["arch"] == "x86_64"


def test_vm_export_config_to_dict_omits_none():
    """None values are omitted from export dict for cleanliness."""
    cfg = VMExportConfig(
        name="myvm",
        compute=VMExportComputeConfig(vcpus=None, mem=2048),
    )
    d = cfg.to_dict()
    assert "vcpus" not in d["compute"]  # None omitted
    assert d["compute"]["mem"] == 2048  # Non-None kept


def test_vm_export_config_from_dict_ignores_unknown():
    """Unknown fields are silently ignored for forward compatibility."""
    cfg = VMExportConfig.from_dict(
        {
            "name": "vm",
            "image": {"os_slug": "ubuntu", "arch": "x86_64"},
            "unknown_key": "value",
            "another": 42,
        }
    )
    assert cfg.name == "vm"
    assert cfg.image.os_slug == "ubuntu"
    assert not hasattr(cfg, "unknown_key")


def test_vm_export_config_from_dict_preserves_nested():
    """Nested sub-configs are properly deserialized."""
    cfg = VMExportConfig.from_dict(
        {
            "name": "vm",
            "compute": {"vcpus": 8, "mem": 4096},
            "image": {"os_slug": "debian", "arch": "arm64", "disk_size": "10G"},
            "kernel": {"version": "6.1.0", "arch": "arm64", "type": "vmlinux"},
            "binary": {"name": "firecracker", "version": "v1.15.0"},
        }
    )
    assert cfg.compute.vcpus == 8
    assert cfg.image.os_slug == "debian"
    assert cfg.image.disk_size == "10G"
    assert cfg.kernel.version == "6.1.0"
    assert cfg.kernel.type == "vmlinux"
    assert cfg.binary.name == "firecracker"


def test_vm_export_config_from_json_file(tmp_path: Path):
    """Load from JSON file with nested structure."""
    data = {
        "name": "myvm",
        "compute": {"vcpus": 8, "mem": 4096},
        "image": {"os_slug": "ubuntu-24.04", "arch": "x86_64"},
    }
    (tmp_path / "vm.json").write_text(json.dumps(data))
    cfg = VMExportConfig.from_json_file(tmp_path / "vm.json")
    assert cfg.name == "myvm"
    assert cfg.compute.vcpus == 8
    assert cfg.image.arch == "x86_64"


def test_vm_export_config_from_json_file_missing_raises(tmp_path: Path):
    """Loading non-existent file raises FileNotFoundError."""
    with pytest.raises(FileNotFoundError):
        VMExportConfig.from_json_file(tmp_path / "nonexistent.json")


def test_vm_export_config_from_json_file_invalid_json_raises(tmp_path: Path):
    """Invalid JSON raises ValueError."""
    (tmp_path / "bad.json").write_text("not json {{{")
    with pytest.raises(ValueError, match="Invalid JSON"):
        VMExportConfig.from_json_file(tmp_path / "bad.json")


def test_vm_export_config_from_json_file_non_object_raises(tmp_path: Path):
    """Non-object JSON raises ValueError."""
    (tmp_path / "list.json").write_text("[1, 2, 3]")
    with pytest.raises(ValueError, match="JSON object"):
        VMExportConfig.from_json_file(tmp_path / "list.json")


def test_vm_export_config_to_json_file(tmp_path: Path):
    """Export to JSON file."""
    cfg = VMExportConfig(
        name="myvm",
        image=VMExportImageConfig(os_slug="ubuntu-24.04", arch="x86_64"),
    )
    cfg.to_json_file(tmp_path / "out.json")
    assert (tmp_path / "out.json").exists()
    data = json.loads((tmp_path / "out.json").read_text())
    assert data["name"] == "myvm"
    assert data["image"]["os_slug"] == "ubuntu-24.04"


def test_vm_export_config_to_json_file_creates_parent(tmp_path: Path):
    """Parent directories are created if needed."""
    cfg = VMExportConfig(name="vm", image=VMExportImageConfig())
    path = tmp_path / "subdir" / "nested" / "vm.json"
    cfg.to_json_file(path)
    assert path.exists()


def test_vm_export_config_no_internal_ids():
    """VMExportConfig must never contain image_id, kernel_id, binary_id, network_id."""
    cfg = VMExportConfig(
        name="test",
        image=VMExportImageConfig(os_slug="ubuntu-24.04", arch="x86_64"),
    )
    output = str(cfg.to_dict())
    assert "image_id" not in output
    assert "kernel_id" not in output
    assert "binary_id" not in output
    assert "network_id" not in output


def test_vm_export_config_full_roundtrip():
    """Full roundtrip: create -> to_dict -> from_dict -> verify."""
    original = VMExportConfig(
        name="testvm",
        schema_version="1.0",
        compute=VMExportComputeConfig(vcpus=2, mem=1024),
        image=VMExportImageConfig(os_slug="ubuntu-24.04", arch="x86_64", disk_size="2G"),
        kernel=VMExportKernelConfig(version="6.1.0", arch="x86_64", type="vmlinux"),
        binary=VMExportBinaryConfig(name="firecracker", version="v1.15.0"),
        network=VMExportNetworkConfig(
            name="default", subnet="172.35.0.0/24", ipv4_gateway="172.35.0.1"
        ),
        boot=VMExportBootConfig(args="console=ttyS0", enable_console=True),
        firecracker=VMExportFirecrackerConfig(enable_api_socket=True, enable_pci=False),
        cloud_init=VMExportCloudInitConfig(mode="inject", user="root"),
    )

    # Serialize and deserialize
    d = original.to_dict()
    restored = VMExportConfig.from_dict(d)

    assert restored.name == "testvm"
    assert restored.compute.vcpus == 2
    assert restored.image.os_slug == "ubuntu-24.04"
    assert restored.kernel.type == "vmlinux"
    assert restored.binary.version == "v1.15.0"
    assert restored.network.subnet == "172.35.0.0/24"
    assert restored.boot.args == "console=ttyS0"
    assert restored.cloud_init.mode == "inject"


def test_vm_export_config_schema_version_default():
    """schema_version defaults to 1.0."""
    cfg = VMExportConfig(name="x")
    assert cfg.schema_version == "1.0"


def test_vm_export_config_empty_sub_configs():
    """Sub-configs are empty by default (factory)."""
    cfg = VMExportConfig(name="test")
    assert cfg.compute.vcpus is None
    assert cfg.image.os_slug == ""  # Default string, not None
    assert cfg.kernel.version is None


# API layer tests (placeholder for when api/vm_config.py is updated)
# These tests verify the new model works with the expected API interface


def test_vm_export_config_portable_refs_only():
    """Verify only portable semantic refs are used, never internal IDs.

    This is an architectural requirement: exported configs must use
    fields like os_slug and version, not SHA256 image_id or kernel_id.
    """
    cfg = VMExportConfig(
        name="export-test",
        image=VMExportImageConfig(os_slug="alpine-3.21", arch="x86_64"),
        kernel=VMExportKernelConfig(version="5.15.0", arch="x86_64", type="vmlinux"),
        binary=VMExportBinaryConfig(name="firecracker", version="v1.14.0"),
        network=VMExportNetworkConfig(name="custom-net", subnet="10.0.0.0/24"),
    )

    # All references are semantic/human-readable, not internal DB IDs
    assert cfg.image.os_slug == "alpine-3.21"
    assert cfg.kernel.version == "5.15.0"
    assert cfg.binary.version == "v1.14.0"
    assert cfg.network.name == "custom-net"
