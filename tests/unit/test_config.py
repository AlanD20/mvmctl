from pathlib import Path

import pytest
import yaml

from mvmctl.core.config import (
    FirecrackerConfig,
    MVMConfig,
    NetworkDefaultsConfig,
    NetworkTopologyConfig,
    VMDefaultsConfig,
    dump_config,
    load_config,
    load_yaml,
    validate_config,
)

# Note: SingleVMNetworkConfig was removed — networks are now managed via
# the named network system (core/network_manager.py).


def test_load_yaml_missing_file(tmp_path: Path) -> None:
    result = load_yaml(tmp_path / "nonexistent.yaml")
    assert result == {}


def test_load_yaml_valid_file(tmp_path: Path) -> None:
    data = {"firecracker": {"binary": "/usr/bin/fc"}, "vm_defaults": {"vcpu_count": 4}}
    yaml_path = tmp_path / "config.yaml"
    yaml_path.write_text(yaml.dump(data))

    result = load_yaml(yaml_path)
    assert result == data


def test_load_config_defaults(tmp_path: Path) -> None:
    config = load_config(tmp_path)

    assert config.firecracker.binary == "/usr/local/bin/firecracker"
    assert config.firecracker.socket_dir == ""
    assert config.firecracker.run_dir == ""
    assert config.firecracker.log_dir == ""

    assert config.vm_defaults.vcpu_count == 2
    assert config.vm_defaults.mem_size_mib == 2048
    assert config.vm_defaults.network_interface == "eth0"
    assert config.vm_defaults.disk_size == "2G"
    assert config.vm_defaults.enable_api_socket is False
    assert config.vm_defaults.enable_pci is False

    assert config.network.defaults.name == "default"
    assert config.network.defaults.cidr == "172.35.0.0/24"
    assert config.network.defaults.gateway == "172.35.0.1"

    assert config.paths.assets_dir != ""


def test_load_config_from_yaml(tmp_path: Path) -> None:
    data = {
        "firecracker": {"binary": "/opt/firecracker"},
        "vm_defaults": {"vcpu_count": 8, "mem_size_mib": 4096},
        "network": {
            "defaults": {
                "name": "custom",
                "cidr": "172.16.0.0/16",
                "gateway": "172.16.0.1",
            },
        },
        "paths": {"assets_dir": "/tmp/assets"},
    }
    (tmp_path / "defaults.yaml").write_text(yaml.dump(data))

    config = load_config(tmp_path)

    assert config.firecracker.binary == "/opt/firecracker"
    assert config.vm_defaults.vcpu_count == 8
    assert config.vm_defaults.mem_size_mib == 4096
    assert config.network.defaults.name == "custom"
    assert config.network.defaults.cidr == "172.16.0.0/16"
    assert config.network.defaults.gateway == "172.16.0.1"
    assert config.paths.assets_dir == "/tmp/assets"


def test_validate_config_valid() -> None:
    config = MVMConfig()
    errors = validate_config(config)

    binary_errors = [e for e in errors if "firecracker.binary" in e]
    assert len(binary_errors) == 1

    other_errors = [e for e in errors if "firecracker.binary" not in e]
    assert other_errors == []


@pytest.mark.parametrize("vcpu_count", [0, -1, -100])
def test_validate_config_invalid_vcpu(vcpu_count: int) -> None:
    config = MVMConfig(vm_defaults=VMDefaultsConfig(vcpu_count=vcpu_count))
    errors = validate_config(config)

    vcpu_errors = [e for e in errors if "vcpu_count" in e]
    assert len(vcpu_errors) == 1
    assert "Must be at least 1" in vcpu_errors[0]


@pytest.mark.parametrize("mem_size_mib", [32, 63, 0])
def test_validate_config_invalid_mem(mem_size_mib: int) -> None:
    config = MVMConfig(vm_defaults=VMDefaultsConfig(mem_size_mib=mem_size_mib))
    errors = validate_config(config)

    mem_errors = [e for e in errors if "mem_size_mib" in e]
    assert len(mem_errors) == 1
    assert "Must be at least 64" in mem_errors[0]


def test_validate_config_invalid_cidr() -> None:
    config = MVMConfig(
        network=NetworkTopologyConfig(
            defaults=NetworkDefaultsConfig(cidr="not-a-cidr"),
        ),
    )
    errors = validate_config(config)

    cidr_errors = [e for e in errors if "network.defaults.cidr" in e]
    assert len(cidr_errors) == 1
    assert "Invalid CIDR" in cidr_errors[0]


def test_dump_config_all_sections() -> None:
    config = MVMConfig()
    result = dump_config(config)

    assert "firecracker" in result
    assert "vm_defaults" in result
    assert "network" in result
    assert "paths" in result

    network = result["network"]
    assert isinstance(network, dict)
    assert "defaults" in network


def test_dump_config_specific_section() -> None:
    config = MVMConfig(
        firecracker=FirecrackerConfig(binary="/custom/bin"),
    )
    result = dump_config(config, section="firecracker")

    assert list(result.keys()) == ["firecracker"]
    fc = result["firecracker"]
    assert isinstance(fc, dict)
    assert fc["binary"] == "/custom/bin"


# ---------------------------------------------------------------------------
# T-M2: Negative test cases for config validation
# ---------------------------------------------------------------------------


def test_load_yaml_invalid_syntax(tmp_path: Path) -> None:
    """YAML with invalid syntax should return empty dict (graceful degradation)."""
    yaml_path = tmp_path / "bad.yaml"
    yaml_path.write_text("invalid: yaml: syntax: [[[")

    result = load_yaml(yaml_path)
    assert result == {}


def test_load_yaml_corrupt_file(tmp_path: Path) -> None:
    """Corrupt YAML file should return empty dict."""
    yaml_path = tmp_path / "corrupt.yaml"
    yaml_path.write_text("{ not valid yaml at all")

    result = load_yaml(yaml_path)
    assert result == {}


def test_load_config_missing_fields_uses_defaults(tmp_path: Path) -> None:
    """YAML with missing fields should use defaults."""
    # Only provide a subset of fields
    data = {"vm_defaults": {"vcpu_count": 8}}
    (tmp_path / "defaults.yaml").write_text(yaml.dump(data))

    config = load_config(tmp_path)

    # Provided value should be used
    assert config.vm_defaults.vcpu_count == 8
    # Missing fields should use defaults
    assert config.vm_defaults.mem_size_mib == 2048  # default
    assert config.firecracker.binary == "/usr/local/bin/firecracker"  # default


def test_load_config_type_mismatch_string_for_int(tmp_path: Path) -> None:
    """Type mismatch (string instead of int) should use default or raise."""
    data = {"vm_defaults": {"vcpu_count": "not-a-number"}}
    (tmp_path / "defaults.yaml").write_text(yaml.dump(data))

    # Should not crash - either uses default or handles gracefully
    config = load_config(tmp_path)
    # The dataclass will use the default value when type coercion fails
    assert config.vm_defaults.vcpu_count is not None


def test_load_config_type_mismatch_int_for_string(tmp_path: Path) -> None:
    """Type mismatch (int instead of string) should use default or handle gracefully."""
    data = {
        "firecracker": {"binary": 12345}  # Should be string
    }
    (tmp_path / "defaults.yaml").write_text(yaml.dump(data))

    config = load_config(tmp_path)
    # Should not crash
    assert config.firecracker.binary is not None


def test_load_config_extra_unknown_fields_filtered(tmp_path: Path) -> None:
    """Unknown fields in YAML should be silently ignored."""
    data = {
        "unknown_section": {"foo": "bar"},
        "vm_defaults": {"vcpu_count": 4, "unknown_field": "should be ignored"},
    }
    (tmp_path / "defaults.yaml").write_text(yaml.dump(data))

    config = load_config(tmp_path)
    assert config.vm_defaults.vcpu_count == 4


def test_load_config_nested_type_mismatch(tmp_path: Path) -> None:
    """Nested type mismatches should be handled gracefully."""
    data = {
        "network": {
            "defaults": {"cidr": 99999}  # Should be string
        }
    }
    (tmp_path / "defaults.yaml").write_text(yaml.dump(data))

    # Should not crash
    config = load_config(tmp_path)
    # The invalid value may remain or use default
    assert config.network.defaults.cidr is not None


def test_validate_config_empty_binary_path() -> None:
    """Empty binary path should be caught by validation."""
    config = MVMConfig(firecracker=FirecrackerConfig(binary=""))
    errors = validate_config(config)

    binary_errors = [e for e in errors if "firecracker.binary" in e and "empty" in e.lower()]
    assert len(binary_errors) == 1


def test_validate_config_negative_memory() -> None:
    """Negative memory should be caught by validation."""
    config = MVMConfig(vm_defaults=VMDefaultsConfig(mem_size_mib=-100))
    errors = validate_config(config)

    mem_errors = [e for e in errors if "mem_size_mib" in e]
    assert len(mem_errors) == 1
