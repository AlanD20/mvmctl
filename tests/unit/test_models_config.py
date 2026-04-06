"""Tests for SystemDefaultsConfig dataclass."""

import dataclasses

import pytest

from mvmctl.models.config import SystemDefaultsConfig


def test_system_defaults_config_all_fields_required():
    """SystemDefaultsConfig must have no defaults — all fields explicit."""
    config = SystemDefaultsConfig(
        vcpu_count=1,
        mem_size_mib=512,
        ssh_user="root",
        disk_size="2G",
        boot_args="console=ttyS0 reboot=k panic=1 pci=off",
        enable_console=True,
        enable_api_socket=True,
        enable_pci=False,
        lsm_flags="landlock,lockdown",
        cloud_init_mode="inject",
        default_network_name="default",
    )
    assert config.vcpu_count == 1
    assert config.mem_size_mib == 512
    assert config.cloud_init_mode == "inject"


def test_system_defaults_config_is_dataclass():
    """SystemDefaultsConfig must be a proper dataclass."""
    assert dataclasses.is_dataclass(SystemDefaultsConfig)


def test_system_defaults_config_no_field_defaults():
    """No field in SystemDefaultsConfig should have a default value.

    Default values belong ONLY in the CLI layer. Models must receive
    explicit values.
    """
    for f in dataclasses.fields(SystemDefaultsConfig):
        assert f.default is dataclasses.MISSING, (
            f"Field {f.name!r} must not have a default value — defaults belong in CLI layer only"
        )
        assert f.default_factory is dataclasses.MISSING, (  # type: ignore[misc]
            f"Field {f.name!r} must not have a default_factory — defaults belong in CLI layer only"
        )


def test_system_defaults_config_has_all_required_fields():
    """SystemDefaultsConfig must have exactly the expected fields."""
    required_fields = {
        "vcpu_count",
        "mem_size_mib",
        "ssh_user",
        "disk_size",
        "boot_args",
        "enable_console",
        "enable_api_socket",
        "enable_pci",
        "lsm_flags",
        "cloud_init_mode",
        "default_network_name",
    }
    actual_fields = {f.name for f in dataclasses.fields(SystemDefaultsConfig)}
    assert actual_fields == required_fields, (
        f"Missing: {required_fields - actual_fields}, Extra: {actual_fields - required_fields}"
    )


def test_system_defaults_config_cloud_init_mode_values():
    """cloud_init_mode accepts valid mode strings."""
    for mode in ["inject", "iso", "net", "off"]:
        config = SystemDefaultsConfig(
            vcpu_count=2,
            mem_size_mib=1024,
            ssh_user="root",
            disk_size="2G",
            boot_args="console=ttyS0",
            enable_console=True,
            enable_api_socket=True,
            enable_pci=False,
            lsm_flags="",
            cloud_init_mode=mode,
            default_network_name="default",
        )
        assert config.cloud_init_mode == mode
