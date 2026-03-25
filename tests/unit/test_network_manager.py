import json
from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.core.network_manager import (
    NetworkConfig,
    NetworkLease,
    _bridge_name_for,
    _gateway_for_subnet,
    _load_config,
    _save_config,
    _load_leases,
    _save_leases,
    list_networks,
    get_network,
    get_network_leases,
    create_network,
    remove_network,
    inspect_network,
    allocate_network_ip,
    release_network_ip,
    ensure_default_network,
    _auto_allocate_subnet,
    _validate_subnet_no_overlap,
)
from mvmctl.exceptions import NetworkError


def test_bridge_name_for():
    assert _bridge_name_for("default") == "mvm-default"
    assert _bridge_name_for("custom_net_name") == "mvm-custom_net"


def test_gateway_for_subnet():
    assert _gateway_for_subnet("10.20.0.0/24") == "10.20.0.1"
    assert _gateway_for_subnet("192.168.100.0/24") == "192.168.100.1"


def test_save_and_load_config(mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "testnet"
    net_dir.mkdir(parents=True)

    config = NetworkConfig(
        name="testnet",
        cidr="10.20.1.0/24",
        gateway="10.20.1.1",
        bridge="mvm-testnet",
        nat_enabled=True,
    )

    _save_config(net_dir, config)
    loaded = _load_config(net_dir)
    assert loaded is not None
    assert loaded.name == "testnet"
    assert loaded.cidr == "10.20.1.0/24"


def test_load_config_migration(mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "testnet"
    net_dir.mkdir(parents=True)

    config_file = net_dir / "config.json"
    config_file.write_text(
        json.dumps(
            {
                "name": "testnet",
                "subnet": "10.20.2.0/24",
                "gateway": "10.20.2.1",
                "bridge": "mvm-testnet",
                "nat_enabled": True,
                "created_at": "2026-01-01T00:00:00Z",
            }
        )
    )

    loaded = _load_config(net_dir)
    assert loaded is not None
    assert loaded.cidr == "10.20.2.0/24"


def test_load_config_not_found():
    assert _load_config(Path("/nonexistent")) is None


def test_save_and_load_leases(mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "testnet"
    net_dir.mkdir(parents=True)

    leases = [NetworkLease(vm_name="vm1", ip="10.20.1.2")]
    _save_leases(net_dir, leases)

    loaded = _load_leases(net_dir)
    assert len(loaded) == 1
    assert loaded[0].vm_name == "vm1"
    assert loaded[0].ip == "10.20.1.2"


def test_load_leases_not_found():
    assert _load_leases(Path("/nonexistent")) == []


def test_list_networks(mock_cache_dir: Path):
    assert list_networks() == []

    net_dir1 = mock_cache_dir / "networks" / "net1"
    net_dir1.mkdir(parents=True)
    _save_config(net_dir1, NetworkConfig("net1", "10.20.1.0/24", "10.20.1.1", "mvm-net1"))

    net_dir2 = mock_cache_dir / "networks" / "net2"
    net_dir2.mkdir(parents=True)
    _save_config(net_dir2, NetworkConfig("net2", "10.20.2.0/24", "10.20.2.1", "mvm-net2"))

    networks = list_networks()
    assert len(networks) == 2
    names = {n.name for n in networks}
    assert names == {"net1", "net2"}


def test_get_network_leases(mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "testnet"
    net_dir.mkdir(parents=True)
    _save_leases(net_dir, [NetworkLease("vm1", "10.20.1.2")])

    leases = get_network_leases("testnet")
    assert len(leases) == 1
    assert leases[0].vm_name == "vm1"


@patch("mvmctl.core.network_manager.setup_bridge")
@patch("mvmctl.core.network_manager.setup_nat")
def test_create_network_success(mock_setup_nat, mock_setup_bridge, mock_cache_dir: Path):
    config = create_network(name="mynet")
    assert config.name == "mynet"
    assert config.cidr == "10.20.0.0/24"
    assert config.gateway == "10.20.0.1"

    # Verify persistence
    assert get_network("mynet") is not None
    mock_setup_bridge.assert_called_once_with("mvm-mynet", gateway_cidr="10.20.0.1/24")
    mock_setup_nat.assert_called_once_with("mvm-mynet")


@patch("mvmctl.core.network_manager.setup_bridge")
@patch("mvmctl.core.network_manager.setup_nat")
def test_create_network_with_legacy_subnet(mock_setup_nat, mock_setup_bridge, mock_cache_dir: Path):
    config = create_network(name="legacynet", subnet="10.20.5.0/24")
    assert config.cidr == "10.20.5.0/24"


def test_create_network_already_exists(mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "mynet"
    net_dir.mkdir(parents=True)
    _save_config(net_dir, NetworkConfig("mynet", "10.20.1.0/24", "10.20.1.1", "mvm-mynet"))

    with pytest.raises(NetworkError, match="already exists"):
        create_network(name="mynet")


@patch("mvmctl.core.network_manager.setup_bridge")
@patch("mvmctl.core.network_manager.teardown_bridge")
def test_create_network_setup_failure(
    mock_teardown_bridge, mock_setup_bridge, mock_cache_dir: Path
):
    mock_setup_bridge.side_effect = NetworkError("Failed to setup bridge")

    with pytest.raises(NetworkError, match="Failed to setup bridge"):
        create_network(name="mynet")

    mock_teardown_bridge.assert_called_once()
    assert get_network("mynet") is None


@patch("mvmctl.core.network_manager.teardown_bridge")
@patch("mvmctl.core.network_manager.teardown_nat")
def test_remove_network(mock_teardown_nat, mock_teardown_bridge, mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "mynet"
    net_dir.mkdir(parents=True)
    _save_config(net_dir, NetworkConfig("mynet", "10.20.1.0/24", "10.20.1.1", "mvm-mynet"))
    _save_leases(net_dir, [])

    remove_network("mynet")

    mock_teardown_nat.assert_called_once_with(bridge="mvm-mynet", force=True)
    mock_teardown_bridge.assert_called_once_with("mvm-mynet")
    assert get_network("mynet") is None


def test_remove_network_not_found():
    with pytest.raises(NetworkError, match="not found"):
        remove_network("nonexistent")


def test_remove_network_with_vms(mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "mynet"
    net_dir.mkdir(parents=True)
    _save_config(net_dir, NetworkConfig("mynet", "10.20.1.0/24", "10.20.1.1", "mvm-mynet"))
    _save_leases(net_dir, [NetworkLease("vm1", "10.20.1.2")])

    with pytest.raises(NetworkError, match="still has VMs attached"):
        remove_network("mynet")


@patch("mvmctl.core.network_manager.teardown_bridge")
@patch("mvmctl.core.network_manager.teardown_nat")
def test_remove_network_partial_failure(
    mock_teardown_nat, mock_teardown_bridge, mock_cache_dir: Path
):
    net_dir = mock_cache_dir / "networks" / "mynet"
    net_dir.mkdir(parents=True)
    _save_config(net_dir, NetworkConfig("mynet", "10.20.1.0/24", "10.20.1.1", "mvm-mynet"))
    _save_leases(net_dir, [])

    mock_teardown_nat.side_effect = NetworkError("NAT cleanup failed")

    # Should log warning but not raise
    remove_network("mynet")
    assert get_network("mynet") is None


@patch("mvmctl.core.network_manager.bridge_exists", return_value=True)
def test_inspect_network(mock_bridge_exists, mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "mynet"
    net_dir.mkdir(parents=True)
    _save_config(net_dir, NetworkConfig("mynet", "10.20.1.0/24", "10.20.1.1", "mvm-mynet"))
    _save_leases(net_dir, [NetworkLease("vm1", "10.20.1.2")])

    info = inspect_network("mynet")
    assert info["name"] == "mynet"
    assert info["bridge_exists"] is True
    vms = info["vms"]
    assert isinstance(vms, list)
    assert len(vms) == 1
    assert vms[0]["vm_name"] == "vm1"
    assert vms[0]["ip"] == "10.20.1.2"
    assert "status" in vms[0]
    assert "pid" in vms[0]
    assert "socket_path" in vms[0]


def test_inspect_network_not_found():
    with pytest.raises(NetworkError, match="not found"):
        inspect_network("nonexistent")


@patch("mvmctl.core.network_manager.allocate_ip")
def test_allocate_network_ip(mock_allocate_ip, mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "mynet"
    net_dir.mkdir(parents=True)
    _save_config(net_dir, NetworkConfig("mynet", "10.20.1.0/24", "10.20.1.1", "mvm-mynet"))
    _save_leases(net_dir, [])

    mock_allocate_ip.return_value = "10.20.1.2"

    ip = allocate_network_ip("mynet", "vm1")
    assert ip == "10.20.1.2"

    # Also reserves the gateway
    mock_allocate_ip.assert_called_once_with(["10.20.1.1"], subnet="10.20.1.0/24")

    leases = get_network_leases("mynet")
    assert len(leases) == 1
    assert leases[0].vm_name == "vm1"
    assert leases[0].ip == "10.20.1.2"


def test_allocate_network_ip_not_found():
    with pytest.raises(NetworkError, match="not found"):
        allocate_network_ip("nonexistent", "vm1")


def test_release_network_ip(mock_cache_dir: Path):
    net_dir = mock_cache_dir / "networks" / "mynet"
    net_dir.mkdir(parents=True)
    _save_config(net_dir, NetworkConfig("mynet", "10.20.1.0/24", "10.20.1.1", "mvm-mynet"))
    _save_leases(net_dir, [NetworkLease("vm1", "10.20.1.2"), NetworkLease("vm2", "10.20.1.3")])

    release_network_ip("mynet", "vm1")

    leases = get_network_leases("mynet")
    assert len(leases) == 1
    assert leases[0].vm_name == "vm2"


@patch("mvmctl.core.network_manager.create_network")
def test_ensure_default_network(mock_create_network, mock_cache_dir: Path):
    # Doesn't exist, will be created
    mock_create_network.return_value = NetworkConfig(
        "default", "172.35.0.0/24", "172.35.0.1", "mvm-default"
    )
    config = ensure_default_network()
    assert config is not None
    mock_create_network.assert_called_once_with("default", cidr="172.35.0.0/24", nat=True)

    # Exists, should return immediately
    net_dir = mock_cache_dir / "networks" / "default"
    net_dir.mkdir(parents=True)
    _save_config(net_dir, config)

    mock_create_network.reset_mock()
    config = ensure_default_network()
    mock_create_network.assert_not_called()


def test_auto_allocate_subnet(mock_cache_dir: Path):
    assert _auto_allocate_subnet() == "10.20.0.0/24"

    net_dir1 = mock_cache_dir / "networks" / "net1"
    net_dir1.mkdir(parents=True)
    _save_config(net_dir1, NetworkConfig("net1", "10.20.0.0/24", "10.20.0.1", "mvm-net1"))

    assert _auto_allocate_subnet() == "10.20.1.0/24"


def test_auto_allocate_subnet_exhausted(mock_cache_dir: Path):
    # Fill up all 256 subnets
    for i in range(256):
        net_dir = mock_cache_dir / "networks" / f"net{i}"
        net_dir.mkdir(parents=True)
        _save_config(
            net_dir, NetworkConfig(f"net{i}", f"10.20.{i}.0/24", f"10.20.{i}.1", f"mvm-net{i}")
        )

    with pytest.raises(NetworkError, match="No available"):
        _auto_allocate_subnet()


def test_validate_subnet_no_overlap(mock_cache_dir: Path):
    net_dir1 = mock_cache_dir / "networks" / "net1"
    net_dir1.mkdir(parents=True)
    _save_config(net_dir1, NetworkConfig("net1", "10.20.0.0/24", "10.20.0.1", "mvm-net1"))

    # Should raise error on overlap
    with pytest.raises(NetworkError, match="overlaps with network"):
        _validate_subnet_no_overlap("10.20.0.0/23")

    # Should not raise if excluding self
    _validate_subnet_no_overlap("10.20.0.0/24", exclude_name="net1")

    # Should not raise on non-overlapping
    _validate_subnet_no_overlap("10.20.1.0/24")
