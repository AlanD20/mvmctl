import json
from pathlib import Path

import pytest

from mvmctl.core.config import (
    FirecrackerConfig,
    MVMConfig,
    NetworkDefaultsConfig,
    PathsConfig,
    VMDefaultsConfig,
    dump_config,
    load_config,
    load_json,
    validate_config,
)


def _make_vm_defaults(**overrides: object) -> VMDefaultsConfig:
    from mvmctl.constants import (
        DEFAULT_VM_BOOT_ARGS,
        DEFAULT_VM_DISK_SIZE,
        DEFAULT_VM_ENABLE_API_SOCKET,
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
        "network_interface": DEFAULT_VM_NETWORK_INTERFACE,
        "boot_args": DEFAULT_VM_BOOT_ARGS,
        "disk_size": DEFAULT_VM_DISK_SIZE,
        "enable_api_socket": DEFAULT_VM_ENABLE_API_SOCKET,
        "enable_pci": DEFAULT_VM_ENABLE_PCI,
        "lsm_flags": DEFAULT_VM_LSM_FLAGS,
    }
    base.update(overrides)
    return VMDefaultsConfig(**base)  # type: ignore[arg-type]


def _make_network_defaults(**overrides: object) -> NetworkDefaultsConfig:
    from mvmctl.constants import (
        DEFAULT_NETWORK_IPV4_GATEWAY,
        DEFAULT_NETWORK_NAME,
        DEFAULT_NETWORK_SUBNET,
    )

    base: dict[str, object] = {
        "name": DEFAULT_NETWORK_NAME,
        "subnet": DEFAULT_NETWORK_SUBNET,
        "ipv4_gateway": DEFAULT_NETWORK_IPV4_GATEWAY,
    }
    base.update(overrides)
    return NetworkDefaultsConfig(**base)  # type: ignore[arg-type]


def _make_mvm_config(**overrides: object) -> MVMConfig:
    from mvmctl.constants import DEFAULT_FIRECRACKER_BINARY_PATH
    from mvmctl.utils.fs import get_cache_dir

    base: dict[str, object] = {
        "firecracker": FirecrackerConfig(binary=DEFAULT_FIRECRACKER_BINARY_PATH),
        "vm_defaults": _make_vm_defaults(),
        "network": _make_network_defaults(),
        "paths": PathsConfig(assets_dir=str(get_cache_dir())),
    }
    base.update(overrides)
    return MVMConfig(**base)  # type: ignore[arg-type]


def test_load_json_missing_file(tmp_path: Path) -> None:
    result = load_json(tmp_path / "nonexistent.json")
    assert result == {}


def test_load_json_valid_file(tmp_path: Path) -> None:
    data = {"firecracker": {"binary": "/usr/bin/fc"}, "vm_defaults": {"vcpu_count": 4}}
    json_path = tmp_path / "config.json"
    json_path.write_text(json.dumps(data))

    result = load_json(json_path)
    assert result == data


def test_load_config_defaults(tmp_path: Path) -> None:
    from mvmctl.constants import (
        DEFAULT_NETWORK_IPV4_GATEWAY,
        DEFAULT_NETWORK_NAME,
        DEFAULT_NETWORK_SUBNET,
        DEFAULT_VM_DISK_SIZE,
        DEFAULT_VM_MEM_MIB,
        DEFAULT_VM_NETWORK_INTERFACE,
        DEFAULT_VM_VCPU_COUNT,
    )

    config = load_config(tmp_path, _make_mvm_config())

    assert config.vm_defaults.vcpu_count == DEFAULT_VM_VCPU_COUNT
    assert config.vm_defaults.mem_size_mib == DEFAULT_VM_MEM_MIB
    assert config.vm_defaults.network_interface == DEFAULT_VM_NETWORK_INTERFACE
    assert config.vm_defaults.disk_size == DEFAULT_VM_DISK_SIZE
    assert config.vm_defaults.enable_api_socket is True
    assert config.vm_defaults.enable_pci is False

    assert config.network.name == DEFAULT_NETWORK_NAME
    assert config.network.subnet == DEFAULT_NETWORK_SUBNET
    assert config.network.ipv4_gateway == DEFAULT_NETWORK_IPV4_GATEWAY

    assert config.paths.assets_dir != ""


def test_load_config_from_json(tmp_path: Path) -> None:
    data = {
        "firecracker": {"binary": "/opt/firecracker"},
        "vm_defaults": {"vcpu_count": 8, "mem_size_mib": 4096},
        "network": {
            "name": "custom",
            "subnet": "172.16.0.0/16",
            "ipv4_gateway": "172.16.0.1",
        },
        "paths": {"assets_dir": "/tmp/assets"},
    }
    (tmp_path / "config.json").write_text(json.dumps(data))

    config = load_config(tmp_path, _make_mvm_config())

    assert config.firecracker.binary == "/opt/firecracker"
    assert config.vm_defaults.vcpu_count == 8
    assert config.vm_defaults.mem_size_mib == 4096
    assert config.network.name == "custom"
    assert config.network.subnet == "172.16.0.0/16"
    assert config.network.ipv4_gateway == "172.16.0.1"
    assert config.paths.assets_dir == "/tmp/assets"


def test_validate_config_valid() -> None:
    config = _make_mvm_config()
    errors = validate_config(config)

    binary_errors = [e for e in errors if "firecracker.binary" in e]
    assert len(binary_errors) == 1

    other_errors = [e for e in errors if "firecracker.binary" not in e]
    assert other_errors == []


@pytest.mark.parametrize("vcpu_count", [0, -1, -100])
def test_validate_config_invalid_vcpu(vcpu_count: int) -> None:
    config = _make_mvm_config(vm_defaults=_make_vm_defaults(vcpu_count=vcpu_count))
    errors = validate_config(config)

    vcpu_errors = [e for e in errors if "vcpu_count" in e]
    assert len(vcpu_errors) == 1
    assert "Must be at least 1" in vcpu_errors[0]


@pytest.mark.parametrize("mem_size_mib", [32, 63, 0])
def test_validate_config_invalid_mem(mem_size_mib: int) -> None:
    config = _make_mvm_config(vm_defaults=_make_vm_defaults(mem_size_mib=mem_size_mib))
    errors = validate_config(config)

    mem_errors = [e for e in errors if "mem_size_mib" in e]
    assert len(mem_errors) == 1
    assert "Must be at least 64" in mem_errors[0]


def test_validate_config_invalid_cidr() -> None:
    config = _make_mvm_config(
        network=_make_network_defaults(subnet="not-a-cidr"),
    )
    errors = validate_config(config)

    cidr_errors = [e for e in errors if "network.defaults.cidr" in e]
    assert len(cidr_errors) == 1
    assert "Invalid CIDR" in cidr_errors[0]


def test_dump_config_all_sections() -> None:
    config = _make_mvm_config()
    result = dump_config(config)

    assert "firecracker" in result
    assert "vm_defaults" in result
    assert "network" in result
    assert "paths" in result

    network = result["network"]
    assert isinstance(network, dict)
    assert "name" in network
    assert "subnet" in network


def test_dump_config_specific_section() -> None:
    config = _make_mvm_config(
        firecracker=FirecrackerConfig(binary="/custom/bin"),
    )
    result = dump_config(config, section="firecracker")

    assert list(result.keys()) == ["firecracker"]
    fc = result["firecracker"]
    assert isinstance(fc, dict)
    assert fc["binary"] == "/custom/bin"


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
    data = {"vm_defaults": {"vcpu_count": 8}}
    (tmp_path / "config.json").write_text(json.dumps(data))

    config = load_config(tmp_path, _make_mvm_config())

    assert config.vm_defaults.vcpu_count == 8
    assert config.vm_defaults.mem_size_mib == 2048
    assert config.firecracker.binary == "/usr/local/bin/firecracker"


def test_load_config_type_mismatch_string_for_int(tmp_path: Path) -> None:
    data = {"vm_defaults": {"vcpu_count": "not-a-number"}}
    (tmp_path / "config.json").write_text(json.dumps(data))

    config = load_config(tmp_path, _make_mvm_config())
    assert config.vm_defaults.vcpu_count is not None


def test_load_config_type_mismatch_int_for_string(tmp_path: Path) -> None:
    data = {"firecracker": {"binary": 12345}}
    (tmp_path / "config.json").write_text(json.dumps(data))

    config = load_config(tmp_path, _make_mvm_config())
    assert config.firecracker.binary is not None


def test_load_config_extra_unknown_fields_filtered(tmp_path: Path) -> None:
    data = {
        "unknown_section": {"foo": "bar"},
        "vm_defaults": {"vcpu_count": 4, "unknown_field": "should be ignored"},
    }
    (tmp_path / "config.json").write_text(json.dumps(data))

    config = load_config(tmp_path, _make_mvm_config())
    assert config.vm_defaults.vcpu_count == 4


def test_load_config_nested_type_mismatch(tmp_path: Path) -> None:
    data = {"network": {"subnet": 99999}}
    (tmp_path / "config.json").write_text(json.dumps(data))

    config = load_config(tmp_path, _make_mvm_config())
    assert config.network.subnet is not None


def test_validate_config_empty_binary_path() -> None:
    config = _make_mvm_config(firecracker=FirecrackerConfig(binary=""))
    errors = validate_config(config)

    binary_errors = [e for e in errors if "firecracker.binary" in e and "empty" in e.lower()]
    assert len(binary_errors) == 1


def test_validate_config_negative_memory() -> None:
    config = _make_mvm_config(vm_defaults=_make_vm_defaults(mem_size_mib=-100))
    errors = validate_config(config)

    mem_errors = [e for e in errors if "mem_size_mib" in e]
    assert len(mem_errors) == 1
