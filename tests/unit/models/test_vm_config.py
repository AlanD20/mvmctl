"""Tests for VMConfig model validation and defaults policy."""

import dataclasses
from pathlib import Path

import pytest

from mvmctl.models.cloud_init import CloudInitMode
from mvmctl.models.vm import VMConfig


def test_vmconfig_no_hardcoded_defaults_for_config_backed_fields():
    """Config-backed fields must have no defaults — CLI layer resolves them.

    Per Resolution Layer Mandate: models receive explicit values from CLI/API.
    Exception: Optional[T] fields may have default=None as valid sentinel.
    """
    config_backed_fields = {
        "vcpu_count",
        "mem_size_mib",
        "enable_api_socket",
        "enable_pci",
        "lsm_flags",
        "enable_logging",
        "enable_metrics",
        "enable_console",
        "cloud_init_mode",
    }

    for f in dataclasses.fields(VMConfig):
        if f.name in config_backed_fields:
            assert f.default is dataclasses.MISSING, (
                f"Field {f.name!r} must not have a default value — "
                f"CLI layer should resolve and pass explicit values"
            )
            assert f.default_factory is dataclasses.MISSING, (  # type: ignore[misc]
                f"Field {f.name!r} must not have a default_factory — "
                f"CLI layer should resolve and pass explicit values"
            )


def test_vmconfig_requires_explicit_values():
    """VMConfig instantiation requires explicit values for required fields."""
    config = VMConfig(
        name="test-vm",
        vcpu_count=2,
        mem_size_mib=1024,
        kernel_path=Path("/path/to/vmlinux"),
        rootfs_path=Path("/path/to/rootfs.ext4"),
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown",
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        cloud_init_mode=CloudInitMode.INJECT,
    )
    assert config.name == "test-vm"
    assert config.vcpu_count == 2
    assert config.mem_size_mib == 1024


def test_vmconfig_validation_vcpu_out_of_range():
    """vCPU count must be 1-32."""
    with pytest.raises(ValueError, match="vcpu_count must be between 1 and 32"):
        VMConfig(
            name="test",
            vcpu_count=0,
            mem_size_mib=1024,
            kernel_path=Path("/vmlinux"),
            rootfs_path=Path("/rootfs.ext4"),
            enable_api_socket=True,
            enable_pci=False,
            lsm_flags="",
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
            cloud_init_mode=CloudInitMode.INJECT,
        )

    with pytest.raises(ValueError, match="vcpu_count must be between 1 and 32"):
        VMConfig(
            name="test",
            vcpu_count=64,
            mem_size_mib=1024,
            kernel_path=Path("/vmlinux"),
            rootfs_path=Path("/rootfs.ext4"),
            enable_api_socket=True,
            enable_pci=False,
            lsm_flags="",
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
            cloud_init_mode=CloudInitMode.INJECT,
        )


def test_vmconfig_validation_memory_out_of_range():
    """Memory must be 128-65536 MiB."""
    with pytest.raises(ValueError, match="mem_size_mib must be between 128 and 65536"):
        VMConfig(
            name="test",
            vcpu_count=2,
            mem_size_mib=64,
            kernel_path=Path("/vmlinux"),
            rootfs_path=Path("/rootfs.ext4"),
            enable_api_socket=True,
            enable_pci=False,
            lsm_flags="",
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
            cloud_init_mode=CloudInitMode.INJECT,
        )
