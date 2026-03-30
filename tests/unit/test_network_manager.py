"""Tests for network_manager.py — metadata-based network storage."""

from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.core.metadata import update_network_entry
from mvmctl.core.network_manager import (
    NetworkConfig,
    _bridge_name_for,
    _gateway_for_subnet,
    _leases_from_entry,
    _network_entry_to_config,
    _validate_subnet_no_overlap,
    allocate_network_ip,
    create_network,
    ensure_default_network,
    get_network,
    get_network_leases,
    inspect_network,
    list_networks,
    release_network_ip,
    remove_network,
)
from mvmctl.exceptions import NetworkError


def test_bridge_name_for():
    assert _bridge_name_for("default") == "mvm-default"
    assert _bridge_name_for("custom_net_name") == "mvm-custom_net"


def test_gateway_for_subnet():
    assert _gateway_for_subnet("10.20.0.0/24") == "10.20.0.1"
    assert _gateway_for_subnet("192.168.100.0/24") == "192.168.100.1"


class TestNetworkEntryConversion:
    """Tests for converting metadata entries to NetworkConfig."""

    def test_network_entry_to_config_success(self):
        entry = {
            "cidr": "10.20.1.0/24",
            "gateway": "10.20.1.1",
            "bridge": "mvm-testnet",
            "nat_enabled": True,
            "created_at": "2026-01-01T00:00:00Z",
            "is_default": 0,
        }
        config = _network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.name == "testnet"
        assert config.cidr == "10.20.1.0/24"
        assert config.gateway == "10.20.1.1"
        assert config.bridge == "mvm-testnet"
        assert config.nat_enabled is True
        assert config.is_default is False

    def test_network_entry_to_config_empty_entry(self):
        assert _network_entry_to_config("testnet", {}) is None

    def test_network_entry_to_config_missing_required_fields(self):
        # Missing gateway
        entry = {"cidr": "10.20.1.0/24", "bridge": "mvm-testnet"}
        assert _network_entry_to_config("testnet", entry) is None

        # Missing cidr
        entry = {"gateway": "10.20.1.1", "bridge": "mvm-testnet"}
        assert _network_entry_to_config("testnet", entry) is None

        # Missing bridge
        entry = {"cidr": "10.20.1.0/24", "gateway": "10.20.1.1"}
        assert _network_entry_to_config("testnet", entry) is None

    def test_network_entry_to_config_is_default_flag(self):
        entry = {
            "cidr": "10.20.1.0/24",
            "gateway": "10.20.1.1",
            "bridge": "mvm-testnet",
            "is_default": 1,
        }
        config = _network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.is_default is True


class TestLeasesFromEntry:
    """Tests for extracting leases from metadata entries."""

    def test_leases_from_entry_success(self):
        entry = {
            "leases": [
                {"vm_name": "vm1", "ip": "10.20.1.2"},
                {"vm_name": "vm2", "ip": "10.20.1.3"},
            ]
        }
        leases = _leases_from_entry(entry)
        assert len(leases) == 2
        assert leases[0].vm_name == "vm1"
        assert leases[0].ip == "10.20.1.2"
        assert leases[1].vm_name == "vm2"
        assert leases[1].ip == "10.20.1.3"

    def test_leases_from_entry_empty(self):
        assert _leases_from_entry({}) == []
        assert _leases_from_entry({"leases": []}) == []

    def test_leases_from_entry_invalid_format(self):
        # leases is not a list
        assert _leases_from_entry({"leases": "invalid"}) == []
        # Missing required fields
        assert _leases_from_entry({"leases": [{"vm_name": "vm1"}]}) == []
        assert _leases_from_entry({"leases": [{"ip": "10.20.1.2"}]}) == []


def _add_network_to_metadata(cache_dir: Path, name: str, **fields) -> None:
    """Helper to add a network entry to metadata.json."""
    defaults = {
        "cidr": "10.20.1.0/24",
        "gateway": "10.20.1.1",
        "bridge": f"mvm-{name}",
        "nat_enabled": True,
        "created_at": "2026-01-01T00:00:00Z",
        "leases": [],
        "bridge_active": True,
    }
    defaults.update(fields)
    update_network_entry(cache_dir, name, **defaults)


def test_list_networks(mock_cache_dir: Path):
    assert list_networks() == []

    _add_network_to_metadata(
        mock_cache_dir, "net1", cidr="10.20.1.0/24", gateway="10.20.1.1", bridge="mvm-net1"
    )
    _add_network_to_metadata(
        mock_cache_dir, "net2", cidr="10.20.2.0/24", gateway="10.20.2.1", bridge="mvm-net2"
    )

    networks = list_networks()
    assert len(networks) == 2
    names = {n.name for n in networks}
    assert names == {"net1", "net2"}


def test_get_network(mock_cache_dir: Path):
    assert get_network("nonexistent") is None

    _add_network_to_metadata(
        mock_cache_dir, "testnet", cidr="10.20.1.0/24", gateway="10.20.1.1", bridge="mvm-testnet"
    )

    config = get_network("testnet")
    assert config is not None
    assert config.name == "testnet"
    assert config.cidr == "10.20.1.0/24"


def test_get_network_leases(mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir,
        "testnet",
        leases=[{"vm_name": "vm1", "ip": "10.20.1.2"}],
    )

    leases = get_network_leases("testnet")
    assert len(leases) == 1
    assert leases[0].vm_name == "vm1"
    assert leases[0].ip == "10.20.1.2"


def test_get_network_leases_empty(mock_cache_dir: Path):
    # Network doesn't exist — returns empty list
    leases = get_network_leases("nonexistent")
    assert leases == []


@patch("mvmctl.core.network_manager.setup_nat")
@patch("mvmctl.core.network_manager.setup_bridge")
@patch("mvmctl.core.network.list_network_interfaces", return_value=["eth0"])
def test_create_network_success(
    mock_interfaces, mock_setup_bridge, mock_setup_nat, mock_cache_dir: Path
):
    config = create_network(name="mynet", cidr="10.20.0.0/24")
    assert config.name == "mynet"
    assert config.cidr == "10.20.0.0/24"
    assert config.gateway == "10.20.0.1"

    # Verify persistence in metadata
    assert get_network("mynet") is not None
    mock_setup_bridge.assert_called_once_with("mvm-mynet", gateway_cidr="10.20.0.1/24")
    mock_setup_nat.assert_called_once_with("mvm-mynet", internet_iface=None)


def test_create_network_already_exists(mock_cache_dir: Path):
    _add_network_to_metadata(mock_cache_dir, "mynet")

    with pytest.raises(NetworkError, match="already exists"):
        create_network(name="mynet", cidr="10.20.1.0/24")


@patch("mvmctl.core.network_manager.setup_bridge")
@patch("mvmctl.core.network_manager.teardown_bridge")
def test_create_network_setup_failure(
    mock_teardown_bridge, mock_setup_bridge, mock_cache_dir: Path
):
    mock_setup_bridge.side_effect = NetworkError("Failed to setup bridge")

    with pytest.raises(NetworkError, match="Failed to setup bridge"):
        create_network(name="mynet", cidr="10.20.0.0/24")

    mock_teardown_bridge.assert_called_once()
    assert get_network("mynet") is None


@patch("mvmctl.core.network_manager.teardown_bridge")
@patch("mvmctl.core.network_manager.teardown_nat")
def test_remove_network(mock_teardown_nat, mock_teardown_bridge, mock_cache_dir: Path):
    _add_network_to_metadata(mock_cache_dir, "mynet", leases=[])

    remove_network("mynet")

    mock_teardown_nat.assert_called_once_with(bridge="mvm-mynet", force=True)
    mock_teardown_bridge.assert_called_once_with("mvm-mynet")
    assert get_network("mynet") is None


def test_remove_network_not_found():
    with pytest.raises(NetworkError, match="not found"):
        remove_network("nonexistent")


def test_remove_network_with_vms(mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir, "mynet", leases=[{"vm_name": "vm1", "ip": "10.20.1.2"}]
    )

    with pytest.raises(NetworkError, match="still has VMs attached"):
        remove_network("mynet")


@patch("mvmctl.core.network_manager.teardown_bridge")
@patch("mvmctl.core.network_manager.teardown_nat")
def test_remove_network_partial_failure(
    mock_teardown_nat, mock_teardown_bridge, mock_cache_dir: Path
):
    _add_network_to_metadata(mock_cache_dir, "mynet", leases=[])

    mock_teardown_nat.side_effect = NetworkError("NAT cleanup failed")

    # Should log warning but not raise
    remove_network("mynet")
    assert get_network("mynet") is None


@patch("mvmctl.core.network_manager.bridge_exists", return_value=True)
def test_inspect_network(mock_bridge_exists, mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir, "mynet", leases=[{"vm_name": "vm1", "ip": "10.20.1.2"}]
    )

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
    _add_network_to_metadata(mock_cache_dir, "mynet", leases=[])

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
    _add_network_to_metadata(
        mock_cache_dir,
        "mynet",
        leases=[
            {"vm_name": "vm1", "ip": "10.20.1.2"},
            {"vm_name": "vm2", "ip": "10.20.1.3"},
        ],
    )

    release_network_ip("mynet", "vm1")

    leases = get_network_leases("mynet")
    assert len(leases) == 1
    assert leases[0].vm_name == "vm2"


@patch("mvmctl.core.network_manager.create_network")
def test_ensure_default_network_creates_when_missing(mock_create_network, mock_cache_dir: Path):
    # Doesn't exist, will be created
    mock_create_network.return_value = NetworkConfig(
        "default", "172.35.0.0/24", "172.35.0.1", "mvm-default"
    )
    config = ensure_default_network()
    assert config is not None
    mock_create_network.assert_called_once_with("default", cidr="172.35.0.0/24", nat=True)


def test_ensure_default_network_returns_existing(mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        cidr="172.35.0.0/24",
        gateway="172.35.0.1",
        bridge="mvm-default",
    )

    config = ensure_default_network()
    assert config is not None
    assert config.name == "default"


@patch("mvmctl.core.network.bridge_exists", return_value=False)
@patch("mvmctl.core.network.setup_bridge")
@patch("mvmctl.core.network.setup_nat")
@patch("mvmctl.core.network.setup_mvm_chains", return_value=True)
def test_ensure_default_network_recreates_missing_bridge(
    mock_setup_chains, mock_setup_nat, mock_setup_bridge, mock_bridge_exists, mock_cache_dir: Path
):
    """When metadata exists but bridge is missing, setup_bridge should be called."""
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        cidr="172.35.0.0/24",
        gateway="172.35.0.1",
        bridge="mvm-default",
        nat_enabled=True,
    )

    config = ensure_default_network()
    assert config is not None
    assert config.name == "default"
    mock_setup_bridge.assert_called_once_with("mvm-default", gateway_cidr="172.35.0.1/24")
    mock_setup_nat.assert_called_once_with("mvm-default")


@patch("mvmctl.core.network.bridge_exists", return_value=True)
@patch("mvmctl.core.network.setup_bridge")
@patch("mvmctl.core.network.setup_nat")
@patch("mvmctl.core.network.setup_mvm_chains", return_value=False)
def test_ensure_default_network_recreates_missing_chains(
    mock_setup_chains, mock_setup_nat, mock_setup_bridge, mock_bridge_exists, mock_cache_dir: Path
):
    """When bridge exists but chains were just created, NAT should be set up."""
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        cidr="172.35.0.0/24",
        gateway="172.35.0.1",
        bridge="mvm-default",
        nat_enabled=True,
    )

    config = ensure_default_network()
    assert config is not None
    assert config.name == "default"
    # Bridge exists, so setup_bridge should not be called
    mock_setup_bridge.assert_not_called()
    # Chains were missing (setup_mvm_chains returned False), so NAT should be set up
    mock_setup_nat.assert_called_once_with("mvm-default")


@patch("mvmctl.core.network.bridge_exists", return_value=True)
@patch("mvmctl.core.network.setup_bridge")
@patch("mvmctl.core.network.setup_nat")
@patch("mvmctl.core.network.setup_mvm_chains", return_value=True)
def test_ensure_default_network_idempotent_when_all_exists(
    mock_setup_chains, mock_setup_nat, mock_setup_bridge, mock_bridge_exists, mock_cache_dir: Path
):
    """When both metadata and resources exist, no setup functions should be called."""
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        cidr="172.35.0.0/24",
        gateway="172.35.0.1",
        bridge="mvm-default",
        nat_enabled=True,
    )

    config = ensure_default_network()
    assert config is not None
    assert config.name == "default"
    # All resources exist, no setup should be called
    mock_setup_bridge.assert_not_called()
    mock_setup_nat.assert_not_called()


def test_validate_subnet_no_overlap(mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir, "net1", cidr="10.20.0.0/24", gateway="10.20.0.1", bridge="mvm-net1"
    )

    # Should raise error on overlap
    with pytest.raises(NetworkError, match="overlaps with network"):
        _validate_subnet_no_overlap("10.20.0.0/23")

    # Should not raise if excluding self
    _validate_subnet_no_overlap("10.20.0.0/24", exclude_name="net1")

    # Should not raise on non-overlapping
    _validate_subnet_no_overlap("10.20.1.0/24")


class TestSetDefaultNetwork:
    """Tests for set_default_network."""

    def test_set_default_network_success(self, mock_cache_dir: Path):
        from mvmctl.core.network_manager import set_default_network

        _add_network_to_metadata(mock_cache_dir, "mynet")

        set_default_network("mynet")

        # Verify the network is now default
        get_network("mynet")  # Verify network exists
        # is_default should be determined by get_default_network_entry
        from mvmctl.core.metadata import get_default_network_entry

        default = get_default_network_entry(mock_cache_dir)
        assert default is not None
        assert default[0] == "mynet"

    def test_set_default_network_not_found(self, mock_cache_dir: Path):
        from mvmctl.core.network_manager import set_default_network

        with pytest.raises(NetworkError, match="does not exist"):
            set_default_network("nonexistent")
