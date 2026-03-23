from pathlib import Path

import yaml

from fcm.core.config import (
    FCMConfig,
    FirecrackerConfig,
    MultiVMNetworkConfig,
    NetworkConfig,
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

    assert config.network.multi_vm.bridge_name == "fc-br0"
    assert config.network.multi_vm.bridge_ip == "10.20.0.1/24"

    assert config.paths.assets_dir == ""


def test_load_config_from_yaml(tmp_path: Path) -> None:
    data = {
        "firecracker": {"binary": "/opt/firecracker"},
        "vm_defaults": {"vcpu_count": 8, "mem_size_mib": 4096},
        "network": {
            "multi_vm": {"bridge_name": "custom-br0", "bridge_ip": "172.16.0.1/16"},
        },
        "paths": {"assets_dir": "/tmp/assets"},
    }
    (tmp_path / "defaults.yaml").write_text(yaml.dump(data))

    config = load_config(tmp_path)

    assert config.firecracker.binary == "/opt/firecracker"
    assert config.vm_defaults.vcpu_count == 8
    assert config.vm_defaults.mem_size_mib == 4096
    assert config.network.multi_vm.bridge_name == "custom-br0"
    assert config.network.multi_vm.bridge_ip == "172.16.0.1/16"
    assert config.paths.assets_dir == "/tmp/assets"


def test_validate_config_valid() -> None:
    config = FCMConfig()
    errors = validate_config(config)

    binary_errors = [e for e in errors if "firecracker.binary" in e]
    assert len(binary_errors) == 1

    other_errors = [e for e in errors if "firecracker.binary" not in e]
    assert other_errors == []


def test_validate_config_invalid_vcpu() -> None:
    config = FCMConfig(vm_defaults=VMDefaultsConfig(vcpu_count=0))
    errors = validate_config(config)

    vcpu_errors = [e for e in errors if "vcpu_count" in e]
    assert len(vcpu_errors) == 1
    assert "Must be at least 1" in vcpu_errors[0]


def test_validate_config_invalid_mem() -> None:
    config = FCMConfig(vm_defaults=VMDefaultsConfig(mem_size_mib=32))
    errors = validate_config(config)

    mem_errors = [e for e in errors if "mem_size_mib" in e]
    assert len(mem_errors) == 1
    assert "Must be at least 64" in mem_errors[0]


def test_validate_config_invalid_cidr() -> None:
    config = FCMConfig(
        network=NetworkConfig(
            multi_vm=MultiVMNetworkConfig(bridge_ip="not-a-cidr"),
        ),
    )
    errors = validate_config(config)

    cidr_errors = [e for e in errors if "bridge_ip" in e]
    assert len(cidr_errors) == 1
    assert "Invalid CIDR" in cidr_errors[0]


def test_dump_config_all_sections() -> None:
    config = FCMConfig()
    result = dump_config(config)

    assert "firecracker" in result
    assert "vm_defaults" in result
    assert "network" in result
    assert "paths" in result

    network = result["network"]
    assert isinstance(network, dict)
    assert "multi_vm" in network


def test_dump_config_specific_section() -> None:
    config = FCMConfig(
        firecracker=FirecrackerConfig(binary="/custom/bin"),
    )
    result = dump_config(config, section="firecracker")

    assert list(result.keys()) == ["firecracker"]
    fc = result["firecracker"]
    assert isinstance(fc, dict)
    assert fc["binary"] == "/custom/bin"
