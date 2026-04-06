import json
from pathlib import Path

import pytest

from mvmctl.core import config as config_module
from mvmctl.core.config import dump_config, load_config, load_json, validate_config
from mvmctl.models.config import SystemDefaultsConfig


@pytest.fixture(autouse=True)
def _clear_config_cache() -> None:
    """Clear the config cache before each test to ensure isolation."""
    config_module._config_cache.clear()


def _make_system_defaults(**overrides: object) -> SystemDefaultsConfig:
    """Create a SystemDefaultsConfig with default values for testing."""
    from mvmctl.constants import (
        DEFAULT_NETWORK_NAME,
        DEFAULT_VM_BOOT_ARGS,
        DEFAULT_VM_DISK_SIZE,
        DEFAULT_VM_ENABLE_API_SOCKET,
        DEFAULT_VM_ENABLE_CONSOLE,
        DEFAULT_VM_ENABLE_LOGGING,
        DEFAULT_VM_ENABLE_METRICS,
        DEFAULT_VM_ENABLE_PCI,
        DEFAULT_VM_LSM_FLAGS,
        DEFAULT_VM_MEM_MIB,
        DEFAULT_VM_NETWORK_INTERFACE,
        DEFAULT_VM_SSH_USER,
        DEFAULT_VM_VCPU_COUNT,
    )

    base: dict[str, object] = {
        "vcpu_count": DEFAULT_VM_VCPU_COUNT,
        "mem_size_mib": DEFAULT_VM_MEM_MIB,
        "ssh_user": DEFAULT_VM_SSH_USER,
        "disk_size": DEFAULT_VM_DISK_SIZE,
        "boot_args": DEFAULT_VM_BOOT_ARGS,
        "enable_api_socket": DEFAULT_VM_ENABLE_API_SOCKET,
        "enable_pci": DEFAULT_VM_ENABLE_PCI,
        "enable_logging": DEFAULT_VM_ENABLE_LOGGING,
        "enable_metrics": DEFAULT_VM_ENABLE_METRICS,
        "enable_console": DEFAULT_VM_ENABLE_CONSOLE,
        "lsm_flags": DEFAULT_VM_LSM_FLAGS,
        "cloud_init_mode": "inject",
        "network_interface": DEFAULT_VM_NETWORK_INTERFACE,
        "default_network_name": DEFAULT_NETWORK_NAME,
    }
    base.update(overrides)
    return SystemDefaultsConfig(**base)


def test_load_json_missing_file(tmp_path: Path) -> None:
    result = load_json(tmp_path / "nonexistent.json")
    assert result == {}


def test_load_json_valid_file(tmp_path: Path) -> None:
    data = {"vcpu_count": 4, "mem_size_mib": 2048}
    json_path = tmp_path / "config.json"
    json_path.write_text(json.dumps(data))

    result = load_json(json_path)
    assert result == data


def test_load_config_defaults(tmp_path: Path) -> None:
    from mvmctl.constants import (
        DEFAULT_NETWORK_NAME,
        DEFAULT_VM_DISK_SIZE,
        DEFAULT_VM_MEM_MIB,
        DEFAULT_VM_NETWORK_INTERFACE,
        DEFAULT_VM_VCPU_COUNT,
    )

    defaults = _make_system_defaults()
    config = load_config(tmp_path, defaults)

    assert config.vcpu_count == DEFAULT_VM_VCPU_COUNT
    assert config.mem_size_mib == DEFAULT_VM_MEM_MIB
    assert config.network_interface == DEFAULT_VM_NETWORK_INTERFACE
    assert config.disk_size == DEFAULT_VM_DISK_SIZE
    assert config.enable_api_socket is True
    assert config.enable_pci is False
    assert config.default_network_name == DEFAULT_NETWORK_NAME


def test_load_config_from_json(tmp_path: Path) -> None:
    data = {
        "vcpu_count": 8,
        "mem_size_mib": 4096,
        "ssh_user": "customuser",
        "cloud_init_mode": "iso",
    }
    (tmp_path / "config.json").write_text(json.dumps(data))

    defaults = _make_system_defaults()
    config = load_config(tmp_path, defaults)

    assert config.vcpu_count == 8
    assert config.mem_size_mib == 4096
    assert config.ssh_user == "customuser"
    assert config.cloud_init_mode == "iso"


def test_validate_config_valid() -> None:
    config = _make_system_defaults()
    errors = validate_config(config)

    assert errors == []


@pytest.mark.parametrize("vcpu_count", [0, -1, -100])
def test_validate_config_invalid_vcpu(vcpu_count: int) -> None:
    config = _make_system_defaults(vcpu_count=vcpu_count)
    errors = validate_config(config)

    vcpu_errors = [e for e in errors if "vcpu_count" in e]
    assert len(vcpu_errors) == 1
    assert "Must be at least 1" in vcpu_errors[0]


@pytest.mark.parametrize("mem_size_mib", [32, 63, 0])
def test_validate_config_invalid_mem(mem_size_mib: int) -> None:
    config = _make_system_defaults(mem_size_mib=mem_size_mib)
    errors = validate_config(config)

    mem_errors = [e for e in errors if "mem_size_mib" in e]
    assert len(mem_errors) == 1
    assert "Must be at least 64" in mem_errors[0]


def test_dump_config_all_sections() -> None:
    config = _make_system_defaults()
    result = dump_config(config)

    assert "vm_defaults" in result
    vm_defaults = result["vm_defaults"]
    assert isinstance(vm_defaults, dict)
    assert "vcpu_count" in vm_defaults
    assert "mem_size_mib" in vm_defaults
    assert "ssh_user" in vm_defaults
    assert "cloud_init_mode" in vm_defaults


def test_dump_config_specific_section() -> None:
    config = _make_system_defaults(vcpu_count=8)
    result = dump_config(config, section="vm_defaults")

    assert list(result.keys()) == ["vm_defaults"]
    vm_defaults = result["vm_defaults"]
    assert isinstance(vm_defaults, dict)
    assert vm_defaults["vcpu_count"] == 8


def test_load_json_invalid_syntax(tmp_path: Path) -> None:
    json_path = tmp_path / "bad.json"
    json_path.write_text("{ not valid json [[[")

    result = load_json(json_path)
    assert result == {}


def test_load_json_corrupt_file(tmp_path: Path) -> None:
    json_path = tmp_path / "corrupt.json"
    json_path.write_text("{ not valid json at all")

    result = load_json(json_path)
    assert result == {}


def test_load_config_missing_fields_uses_defaults(tmp_path: Path) -> None:
    data = {"vcpu_count": 8}
    (tmp_path / "config.json").write_text(json.dumps(data))

    defaults = _make_system_defaults(mem_size_mib=2048)
    config = load_config(tmp_path, defaults)

    assert config.vcpu_count == 8
    assert config.mem_size_mib == 2048


def test_load_config_type_mismatch_string_for_int(tmp_path: Path) -> None:
    data = {"vcpu_count": "not-a-number"}
    (tmp_path / "config.json").write_text(json.dumps(data))

    defaults = _make_system_defaults()
    config = load_config(tmp_path, defaults)
    # Type mismatches are passed through (validation is separate)
    assert config.vcpu_count == "not-a-number"  # type: ignore[comparison-overlap]


def test_load_config_type_mismatch_int_for_string(tmp_path: Path) -> None:
    data = {"ssh_user": 12345}
    (tmp_path / "config.json").write_text(json.dumps(data))

    defaults = _make_system_defaults()
    config = load_config(tmp_path, defaults)
    # Type mismatches are passed through (validation is separate)
    assert config.ssh_user == 12345  # type: ignore[comparison-overlap]


def test_load_config_extra_unknown_fields_filtered(tmp_path: Path) -> None:
    data = {
        "unknown_field": "should be ignored",
        "vcpu_count": 4,
    }
    (tmp_path / "config.json").write_text(json.dumps(data))

    defaults = _make_system_defaults()
    config = load_config(tmp_path, defaults)
    assert config.vcpu_count == 4


def test_load_config_nested_type_mismatch(tmp_path: Path) -> None:
    data = {"cloud_init_mode": 99999}
    (tmp_path / "config.json").write_text(json.dumps(data))

    defaults = _make_system_defaults()
    config = load_config(tmp_path, defaults)
    # Type mismatches are passed through (validation is separate)
    assert config.cloud_init_mode == 99999  # type: ignore[comparison-overlap]


def test_validate_config_negative_memory() -> None:
    config = _make_system_defaults(mem_size_mib=-100)
    errors = validate_config(config)

    mem_errors = [e for e in errors if "mem_size_mib" in e]
    assert len(mem_errors) == 1


def test_load_config_legacy_nested_format(tmp_path: Path) -> None:
    """Test that legacy nested config format is properly flattened."""
    data = {
        "vm_defaults": {
            "vcpu_count": 16,
            "mem_size_mib": 8192,
        },
        "network": {
            "name": "legacy-net",
        },
    }
    (tmp_path / "config.json").write_text(json.dumps(data))

    defaults = _make_system_defaults()
    config = load_config(tmp_path, defaults)

    # Values from nested vm_defaults should be extracted
    assert config.vcpu_count == 16
    assert config.mem_size_mib == 8192
    # Network name should be extracted to default_network_name
    assert config.default_network_name == "legacy-net"


def test_load_config_caching(tmp_path: Path) -> None:
    """Test that config is cached and subsequent loads return cached value."""
    data = {"vcpu_count": 4}
    (tmp_path / "config.json").write_text(json.dumps(data))

    defaults = _make_system_defaults()

    # First load should read from file
    config1 = load_config(tmp_path, defaults)
    assert config1.vcpu_count == 4

    # Modify the file
    data["vcpu_count"] = 8
    (tmp_path / "config.json").write_text(json.dumps(data))

    # Second load should return cached value (4, not 8)
    config2 = load_config(tmp_path, defaults)
    assert config2.vcpu_count == 4  # Cached value


def test_load_config_all_fields_preserved(tmp_path: Path) -> None:
    """Test that all SystemDefaultsConfig fields are properly loaded and preserved."""

    data = {
        "vcpu_count": 2,
        "mem_size_mib": 1024,
        "ssh_user": "testuser",
        "disk_size": "20G",
        "boot_args": "console=ttyS0 reboot=k panic=1 pci=off",
        "enable_api_socket": False,
        "enable_pci": True,
        "enable_logging": False,
        "enable_metrics": True,
        "enable_console": False,
        "lsm_flags": "apparmor",
        "cloud_init_mode": "net",
        "network_interface": "eth1",
        "default_network_name": "custom-net",
    }
    (tmp_path / "config.json").write_text(json.dumps(data))

    defaults = _make_system_defaults()
    config = load_config(tmp_path, defaults)

    assert config.vcpu_count == 2
    assert config.mem_size_mib == 1024
    assert config.ssh_user == "testuser"
    assert config.disk_size == "20G"
    assert config.boot_args == "console=ttyS0 reboot=k panic=1 pci=off"
    assert config.enable_api_socket is False
    assert config.enable_pci is True
    assert config.enable_logging is False
    assert config.enable_metrics is True
    assert config.enable_console is False
    assert config.lsm_flags == "apparmor"
    assert config.cloud_init_mode == "net"
    assert config.network_interface == "eth1"
    assert config.default_network_name == "custom-net"
