"""Tests for network_manager.py — SQLite-based network storage."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.api.network import (
    allocate_network_ip,
    create_network,
    ensure_default_network,
    get_network,
    get_network_leases,
    inspect_network,
    list_networks,
    release_network_ip,
    remove_network,
    restore_networks,
    set_default_network,
)
from mvmctl.api.metadata import get_default_network_entry
from mvmctl.core.metadata import update_network_entry
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.network_manager import (
    NetworkConfig,
    _bridge_name_for,
    _ipv4_gateway_for_subnet,
    leases_from_entry,
    network_entry_to_config,
    validate_no_subnet_overlap,
)
from mvmctl.db.models import Network as DBNetwork, NetworkLease as DBNetworkLease
from mvmctl.exceptions import NetworkError
from mvmctl.models.network import NetworkItem


def test_bridge_name_for():
    assert _bridge_name_for("default") == "mvm-default"
    assert _bridge_name_for("custom_net_name") == "mvm-custom_net"


def test_ipv4_gateway_for_subnet():
    assert _ipv4_gateway_for_subnet("10.20.0.0/24") == "10.20.0.1"
    assert _ipv4_gateway_for_subnet("192.168.100.0/24") == "192.168.100.1"


class TestNetworkEntryConversion:
    """Tests for converting metadata entries to NetworkConfig."""

    def test_network_entry_to_config_success(self):
        entry = {
            "subnet": "10.20.1.0/24",
            "ipv4_gateway": "10.20.1.1",
            "bridge": "mvm-testnet",
            "nat_enabled": True,
            "created_at": "2026-01-01T00:00:00Z",
            "is_default": 0,
        }
        config = network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.name == "testnet"
        assert config.subnet == "10.20.1.0/24"
        assert config.ipv4_gateway == "10.20.1.1"
        assert config.bridge == "mvm-testnet"
        assert config.nat_enabled is True
        assert config.is_default is False

    def test_network_entry_to_config_empty_entry(self):
        assert network_entry_to_config("testnet", {}) is None

    def test_network_entry_to_config_missing_required_fields(self):
        # Missing gateway
        entry = {"subnet": "10.20.1.0/24", "bridge": "mvm-testnet"}
        assert network_entry_to_config("testnet", entry) is None

        # Missing subnet
        entry = {"ipv4_gateway": "10.20.1.1", "bridge": "mvm-testnet"}
        assert network_entry_to_config("testnet", entry) is None

        # Missing bridge
        entry = {"subnet": "10.20.1.0/24", "ipv4_gateway": "10.20.1.1"}
        assert network_entry_to_config("testnet", entry) is None

    def test_network_entry_to_config_is_default_flag(self):
        entry = {
            "subnet": "10.20.1.0/24",
            "ipv4_gateway": "10.20.1.1",
            "bridge": "mvm-testnet",
            "is_default": 1,
        }
        config = network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.is_default is True


class TestLeasesFromEntry:
    """Tests for extracting leases from metadata entries."""

    def test_leases_from_entry_success(self):
        entry = {
            "leases": [
                {"vm_id": "vm1", "ipv4": "10.20.1.2"},
                {"vm_id": "vm2", "ipv4": "10.20.1.3"},
            ]
        }
        leases = leases_from_entry(entry)
        assert len(leases) == 2
        assert leases[0].vm_id == "vm1"
        assert leases[0].ipv4 == "10.20.1.2"
        assert leases[1].vm_id == "vm2"
        assert leases[1].ipv4 == "10.20.1.3"

    def test_leases_from_entry_empty(self):
        assert leases_from_entry({}) == []
        assert leases_from_entry({"leases": []}) == []

    def test_leases_from_entry_invalid_format(self):
        # leases is not a list
        assert leases_from_entry({"leases": "invalid"}) == []
        # Missing required fields
        assert leases_from_entry({"leases": [{"vm_id": "vm1"}]}) == []
        assert leases_from_entry({"leases": [{"ipv4": "10.20.1.2"}]}) == []


def test_list_networks():
    """Test listing networks via MVMDatabase."""
    # Empty case
    with patch.object(MVMDatabase, "list_networks", return_value=[]):
        with patch.object(MVMDatabase, "get_default_network", return_value=None):
            assert list_networks() == []

    # Networks exist
    net1 = DBNetwork(
        id="a" * 64,
        name="net1",
        subnet="10.20.1.0/24",
        bridge="mvm-net1",
        ipv4_gateway="10.20.1.1",
    )
    net2 = DBNetwork(
        id="b" * 64,
        name="net2",
        subnet="10.20.2.0/24",
        bridge="mvm-net2",
        ipv4_gateway="10.20.2.1",
    )

    with patch.object(MVMDatabase, "list_networks", return_value=[net1, net2]):
        with patch.object(MVMDatabase, "get_default_network", return_value=None):
            networks = list_networks()
            assert len(networks) == 2
            names = {n.name for n in networks}
            assert names == {"net1", "net2"}


def test_get_network():
    """Test getting a network by name via MVMDatabase."""
    # Nonexistent network
    with patch.object(MVMDatabase, "get_network_by_name", return_value=None):
        assert get_network("nonexistent") is None

    # Existing network
    db_net = DBNetwork(
        id="a" * 64,
        name="testnet",
        subnet="10.20.1.0/24",
        bridge="mvm-testnet",
        ipv4_gateway="10.20.1.1",
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        config = get_network("testnet")
        assert config is not None
        assert config.name == "testnet"
        assert config.subnet == "10.20.1.0/24"


def test_get_network_leases():
    """Test getting network leases via MVMDatabase."""
    db_net = DBNetwork(
        id="net-id-123",
        name="testnet",
        subnet="10.20.1.0/24",
        bridge="mvm-testnet",
        ipv4_gateway="10.20.1.1",
    )
    db_lease = DBNetworkLease(
        network_id="net-id-123",
        vm_id="vm1",
        ipv4="10.20.1.2",
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "list_leases", return_value=[db_lease]):
            leases = get_network_leases("testnet")
            assert len(leases) == 1
            assert leases[0].vm_id == "vm1"
            assert leases[0].ipv4 == "10.20.1.2"


def test_get_network_leases_empty():
    """Test getting leases for nonexistent network returns empty list."""
    with patch.object(MVMDatabase, "get_network_by_name", return_value=None):
        leases = get_network_leases("nonexistent")
        assert leases == []


@patch("mvmctl.api.network.sync_iptables_rules")
@patch("mvmctl.api.network.network_core.setup_bridge")
@patch("mvmctl.utils.network.list_network_interfaces", return_value=["eth0"])
def test_create_network_success(
    mock_interfaces, mock_setup_bridge, mock_sync_rules, mock_cache_dir: Path
):
    """Test creating a network successfully."""
    with patch.object(MVMDatabase, "get_network_by_name", return_value=None):
        with patch.object(MVMDatabase, "upsert_network"):
            with patch.object(MVMDatabase, "delete_network"):
                with patch.object(MVMDatabase, "record_iptables_rule"):
                    config = create_network(name="mynet", subnet="10.20.0.0/24")
                    assert config.name == "mynet"
                    assert config.subnet == "10.20.0.0/24"
                    assert config.ipv4_gateway == "10.20.0.1"

    mock_setup_bridge.assert_called_once_with("mvm-mynet", ipv4_gateway_subnet="10.20.0.1/24")


def test_create_network_already_exists():
    """Test creating a network that already exists raises error."""
    existing_net = DBNetwork(
        id="a" * 64,
        name="mynet",
        subnet="10.20.1.0/24",
        bridge="mvm-mynet",
        ipv4_gateway="10.20.1.1",
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=existing_net):
        with pytest.raises(NetworkError, match="already exists"):
            create_network(name="mynet", subnet="10.20.1.0/24")


@patch("mvmctl.api.network.network_core.setup_bridge")
@patch("mvmctl.api.network.network_core.teardown_bridge")
def test_create_network_setup_failure(
    mock_teardown_bridge, mock_setup_bridge, mock_cache_dir: Path
):
    """Test network creation rollback on setup failure."""
    mock_setup_bridge.side_effect = NetworkError("Failed to setup bridge")

    with patch.object(MVMDatabase, "get_network_by_name", return_value=None):
        with pytest.raises(NetworkError, match="Failed to setup bridge"):
            create_network(name="mynet", subnet="10.20.0.0/24")

    mock_teardown_bridge.assert_called_once()


@patch("mvmctl.api.network.network_core.teardown_bridge")
@patch("mvmctl.api.network.network_core.teardown_nat")
def test_remove_network(mock_teardown_nat, mock_teardown_bridge, mock_cache_dir: Path):
    db_net = DBNetwork(
        id="net-id-123",
        name="mynet",
        subnet="10.20.1.0/24",
        bridge="mvm-mynet",
        ipv4_gateway="10.20.1.1",
        nat_enabled=True,
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "list_leases", return_value=[]):
            with patch.object(MVMDatabase, "delete_network"):
                remove_network("mynet")

    mock_teardown_nat.assert_called_once_with(bridge="mvm-mynet", force=True, subnet="10.20.1.0/24")
    mock_teardown_bridge.assert_called_once_with("mvm-mynet")


def test_remove_network_not_found():
    """Test removing a nonexistent network raises error."""
    with patch.object(MVMDatabase, "get_network_by_name", return_value=None):
        with pytest.raises(NetworkError, match="not found"):
            remove_network("nonexistent")


def test_remove_network_with_vms():
    """Test removing a network with attached VMs raises error."""
    db_net = DBNetwork(
        id="net-id-123",
        name="mynet",
        subnet="10.20.1.0/24",
        bridge="mvm-mynet",
        ipv4_gateway="10.20.1.1",
    )
    db_lease = DBNetworkLease(
        network_id="net-id-123",
        vm_id="vm1",
        ipv4="10.20.1.2",
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "list_leases", return_value=[db_lease]):
            with pytest.raises(NetworkError, match="still has VMs attached"):
                remove_network("mynet")


@patch("mvmctl.api.network.network_core.teardown_bridge")
@patch("mvmctl.api.network.network_core.teardown_nat")
def test_remove_network_partial_failure(
    mock_teardown_nat, mock_teardown_bridge, mock_cache_dir: Path
):
    """Test network removal with partial teardown failure."""
    db_net = DBNetwork(
        id="net-id-123",
        name="mynet",
        subnet="10.20.1.0/24",
        bridge="mvm-mynet",
        ipv4_gateway="10.20.1.1",
    )

    mock_teardown_nat.side_effect = NetworkError("NAT cleanup failed")

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "list_leases", return_value=[]):
            with patch.object(MVMDatabase, "delete_network"):
                # Should log warning but not raise
                remove_network("mynet")


@patch("mvmctl.api.network.bridge_exists", return_value=True)
def test_inspect_network(mock_bridge_exists, mock_cache_dir: Path):
    """Test inspecting a network."""
    db_net = DBNetwork(
        id="net-id-123",
        name="mynet",
        subnet="10.20.1.0/24",
        bridge="mvm-mynet",
        ipv4_gateway="10.20.1.1",
    )
    db_lease = DBNetworkLease(
        network_id="net-id-123",
        vm_id="vm1",
        ipv4="10.20.1.2",
    )

    mock_vm = MagicMock()
    mock_vm.status.value = "running"
    mock_vm.pid = 1234
    mock_vm.api_socket_path = Path("/tmp/test.sock")

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "list_leases", return_value=[db_lease]):
            with patch.object(MVMDatabase, "update_network_bridge_active"):
                with patch("mvmctl.core.vm_manager.VMManager") as mock_vm_mgr:
                    mock_vm_mgr.return_value.get.return_value = mock_vm
                    info = inspect_network("mynet")

    assert info.name == "mynet"
    assert info.bridge_exists is True
    vms = info.vms
    assert isinstance(vms, list)
    assert len(vms) == 1
    assert vms[0]["vm_id"] == "vm1"
    assert vms[0]["ipv4"] == "10.20.1.2"
    assert "status" in vms[0]
    assert "pid" in vms[0]
    assert "api_socket_path" in vms[0]


def test_inspect_network_not_found():
    """Test inspecting a nonexistent network raises error."""
    with patch.object(MVMDatabase, "get_network_by_name", return_value=None):
        with pytest.raises(NetworkError, match="not found"):
            inspect_network("nonexistent")


@patch("mvmctl.api.network.network_core.allocate_ip")
def test_allocate_network_ip(mock_allocate_ip, mock_cache_dir: Path):
    db_net = DBNetwork(
        id="net-id-123",
        name="mynet",
        subnet="10.20.1.0/24",
        bridge="mvm-mynet",
        ipv4_gateway="10.20.1.1",
    )

    mock_allocate_ip.return_value = "10.20.1.2"

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "list_leases", return_value=[]):
            with patch.object(MVMDatabase, "acquire_lease"):
                ip = allocate_network_ip("mynet", "vm1")
                assert ip == "10.20.1.2"


def test_allocate_network_ip_not_found():
    """Test allocating IP for nonexistent network raises error."""
    with patch.object(MVMDatabase, "get_network_by_name", return_value=None):
        with pytest.raises(NetworkError, match="not found"):
            allocate_network_ip("nonexistent", "vm1")


def test_release_network_ip(mock_cache_dir: Path):
    """Test releasing an IP lease."""
    db_net = DBNetwork(
        id="net-id-123",
        name="mynet",
        subnet="10.20.1.0/24",
        bridge="mvm-mynet",
        ipv4_gateway="10.20.1.1",
    )
    db_lease1 = DBNetworkLease(
        network_id="net-id-123",
        vm_id="vm1",
        ipv4="10.20.1.2",
    )
    db_lease2 = DBNetworkLease(
        network_id="net-id-123",
        vm_id="vm2",
        ipv4="10.20.1.3",
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "list_leases", return_value=[db_lease1, db_lease2]):
            with patch.object(MVMDatabase, "release_vm_leases") as mock_release:
                release_network_ip("net-id-123", "vm1")
                mock_release.assert_called_once_with("vm1")


@patch("mvmctl.api.network.get_default_interface")
@patch("mvmctl.api.network.create_network")
@patch("mvmctl.api.network.set_default_network")
@patch("mvmctl.api.network.create_iptables_rule")
def test_ensure_default_network_creates_when_missing(
    mock_create_iptables_rule,
    mock_set_default,
    mock_create_network,
    mock_get_default_iface,
    mock_cache_dir: Path,
):
    """Test ensure_default_network creates network when missing."""
    mock_get_default_iface.return_value = "wlo1"
    mock_create_network.return_value = NetworkConfig(
        "default", "172.35.0.0/24", "172.35.0.1", "mvm-default"
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=None):
        config = ensure_default_network()

    assert config is not None
    mock_create_network.assert_called_once_with(
        "default", subnet="172.35.0.0/24", nat=True, nat_gateways=["wlo1"]
    )
    mock_set_default.assert_called_once_with("default")


def test_create_network_does_not_mark_default_network_created_in_sqlite(
    mock_cache_dir: Path, monkeypatch: pytest.MonkeyPatch
):
    """Creating a network named 'default' no longer marks host state - only ensure_default_network does."""
    db_path = mock_cache_dir / "mvmdb.db"
    monkeypatch.setattr("mvmctl.core.mvm_db.get_mvm_db_path", lambda: db_path)

    db = MVMDatabase(db_path=db_path)
    db.migrate()

    with (
        patch("mvmctl.api.network.network_core.setup_bridge"),
        patch("mvmctl.api.network.sync_iptables_rules"),
        patch("mvmctl.utils.network.list_network_interfaces", return_value=["eth0"]),
        patch.object(MVMDatabase, "get_network_by_name", return_value=None),
        patch.object(MVMDatabase, "upsert_network"),
        patch.object(MVMDatabase, "delete_network"),
        patch.object(MVMDatabase, "record_iptables_rule"),
    ):
        config = create_network(name="default", subnet="10.20.0.0/24")

    assert config.name == "default"
    # Host state should NOT be marked - only ensure_default_network does that now
    state = db.get_host_state()
    assert state is None or bool(state.default_network_created) is False


@patch("mvmctl.api.network.sync_iptables_rules")
@patch("mvmctl.api.network.bridge_exists", return_value=True)
@patch("mvmctl.api.network.network_core.setup_nat")
@patch("mvmctl.api.network.network_core.setup_mvm_chains", return_value=True)
@patch("mvmctl.api.network.create_iptables_rule")
def test_ensure_default_network_returns_existing(
    mock_create_iptables_rule,
    mock_setup_chains,
    mock_setup_nat,
    mock_bridge_exists,
    mock_sync_rules,
    mock_cache_dir: Path,
):
    """Test ensure_default_network returns existing network."""
    db_net = DBNetwork(
        id="net-default-123",
        name="default",
        subnet="172.35.0.0/24",
        bridge="mvm-default",
        ipv4_gateway="172.35.0.1",
        nat_enabled=True,
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "get_default_network", return_value=db_net):
            config = ensure_default_network()

    assert config is not None
    assert config.name == "default"


def test_ensure_default_network_creates_default_network_metadata(
    mock_cache_dir: Path, monkeypatch: pytest.MonkeyPatch
):
    db_net = DBNetwork(
        id="net-default-123",
        name="default",
        subnet="172.35.0.0/24",
        bridge="mvm-default",
        ipv4_gateway="172.35.0.1",
        nat_enabled=True,
    )

    with (
        patch("mvmctl.api.network.sync_iptables_rules"),
        patch("mvmctl.api.network.bridge_exists", return_value=True),
        patch("mvmctl.api.network.network_core.setup_nat"),
        patch("mvmctl.api.network.network_core.setup_mvm_chains", return_value=True),
        patch("mvmctl.api.network.create_iptables_rule"),
        patch.object(MVMDatabase, "get_network_by_name", return_value=db_net),
        patch.object(MVMDatabase, "get_default_network", return_value=db_net),
        patch("mvmctl.api.metadata.get_default_network_entry") as mock_get_default,
    ):
        mock_get_default.return_value = NetworkItem(
            id="net-default-123",
            name="default",
            subnet="172.35.0.0/24",
            bridge="mvm-default",
            ipv4_gateway="172.35.0.1",
        )
        config = ensure_default_network()
        assert config is not None
        default_entry = get_default_network_entry(mock_cache_dir)
        assert default_entry is not None
        assert default_entry.name == "default"


def test_ensure_default_network_sets_default_when_none_exists(mock_cache_dir: Path):
    db_net = DBNetwork(
        id="net-default-456",
        name="default",
        subnet="172.35.0.0/24",
        bridge="mvm-default",
        ipv4_gateway="172.35.0.1",
        nat_enabled=True,
    )

    with (
        patch("mvmctl.api.network.sync_iptables_rules"),
        patch("mvmctl.api.network.bridge_exists", return_value=True),
        patch("mvmctl.api.network.network_core.setup_nat"),
        patch("mvmctl.api.network.network_core.setup_mvm_chains", return_value=True),
        patch("mvmctl.api.network.create_iptables_rule"),
        patch.object(MVMDatabase, "get_network_by_name", return_value=db_net),
        patch.object(MVMDatabase, "get_default_network", return_value=db_net),
        patch.object(MVMDatabase, "set_default_network"),
        patch("mvmctl.api.metadata.get_default_network_entry") as mock_get_default,
    ):
        mock_get_default.return_value = NetworkItem(
            id="net-default-456",
            name="default",
            subnet="172.35.0.0/24",
            bridge="mvm-default",
            ipv4_gateway="172.35.0.1",
        )
        config = ensure_default_network()
        assert config is not None
        default_entry = get_default_network_entry(mock_cache_dir)
        assert default_entry is not None
        assert default_entry.name == "default"


def test_ensure_default_network_preserves_existing_other_default(mock_cache_dir: Path):
    custom_net = DBNetwork(
        id="net-custom-123",
        name="custom",
        subnet="10.20.0.0/24",
        bridge="mvm-custom",
        ipv4_gateway="10.20.0.1",
        nat_enabled=True,
        is_default=True,
    )
    default_net = DBNetwork(
        id="net-default-789",
        name="default",
        subnet="172.35.0.0/24",
        bridge="mvm-default",
        ipv4_gateway="172.35.0.1",
        nat_enabled=True,
        is_default=False,
    )

    with (
        patch("mvmctl.api.network.sync_iptables_rules"),
        patch("mvmctl.api.network.bridge_exists", return_value=True),
        patch("mvmctl.api.network.network_core.setup_nat"),
        patch("mvmctl.api.network.network_core.setup_mvm_chains", return_value=True),
        patch("mvmctl.api.network.create_iptables_rule"),
        patch.object(MVMDatabase, "get_network_by_name", return_value=default_net),
        patch.object(MVMDatabase, "get_default_network", return_value=custom_net),
        patch("mvmctl.api.metadata.get_default_network_entry") as mock_get_default,
    ):
        mock_get_default.return_value = NetworkItem(
            id="net-custom-123",
            name="custom",
            subnet="10.20.0.0/24",
            bridge="mvm-custom",
            ipv4_gateway="10.20.0.1",
        )
        config = ensure_default_network()
        assert config is not None
        default_entry = get_default_network_entry(mock_cache_dir)
        assert default_entry is not None
        assert default_entry.name == "custom"


@patch("mvmctl.api.network.sync_iptables_rules")
@patch("mvmctl.api.network.bridge_exists", return_value=False)
@patch("mvmctl.api.network.network_core.setup_bridge")
@patch("mvmctl.api.network.network_core.setup_nat")
@patch("mvmctl.api.network.network_core.setup_mvm_chains", return_value=True)
@patch("mvmctl.api.network.get_default_interface", return_value="eth0")
@patch("mvmctl.api.network.create_iptables_rule")
def test_ensure_default_network_recreates_missing_bridge(
    mock_create_iptables_rule,
    mock_get_iface,
    mock_setup_chains,
    mock_setup_nat,
    mock_setup_bridge,
    mock_bridge_exists,
    mock_sync_rules,
    mock_cache_dir: Path,
):
    """When metadata exists but bridge is missing, setup_bridge should be called."""
    db_net = DBNetwork(
        id="net-default-123",
        name="default",
        subnet="172.35.0.0/24",
        bridge="mvm-default",
        ipv4_gateway="172.35.0.1",
        nat_enabled=True,
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "get_default_network", return_value=db_net):
            with patch.object(MVMDatabase, "update_network_bridge_active"):
                with patch.object(MVMDatabase, "set_default_network"):
                    config = ensure_default_network()

    assert config is not None
    assert config.name == "default"
    mock_setup_bridge.assert_called_once_with("mvm-default", ipv4_gateway_subnet="172.35.0.1/24")
    # New implementation calls create_iptables_rule instead of setup_nat
    assert mock_create_iptables_rule.call_count >= 3  # MASQUERADE + FORWARD_IN + FORWARD_OUT


@patch("mvmctl.api.network.sync_iptables_rules")
@patch("mvmctl.api.network.bridge_exists", return_value=True)
@patch("mvmctl.api.network.network_core.setup_bridge")
@patch("mvmctl.api.network.network_core.setup_nat")
@patch("mvmctl.api.network.network_core.setup_mvm_chains", return_value=False)
@patch("mvmctl.api.network.get_default_interface", return_value="eth0")
@patch("mvmctl.api.network.create_iptables_rule")
def test_ensure_default_network_recreates_missing_chains(
    mock_create_iptables_rule,
    mock_get_iface,
    mock_setup_chains,
    mock_setup_nat,
    mock_setup_bridge,
    mock_bridge_exists,
    mock_sync_rules,
    mock_cache_dir: Path,
):
    """When bridge exists but chains were just created, NAT should be set up."""
    db_net = DBNetwork(
        id="net-default-123",
        name="default",
        subnet="172.35.0.0/24",
        bridge="mvm-default",
        ipv4_gateway="172.35.0.1",
        nat_enabled=True,
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "get_default_network", return_value=db_net):
            with patch.object(MVMDatabase, "update_network_bridge_active"):
                with patch.object(MVMDatabase, "set_default_network"):
                    config = ensure_default_network()

    assert config is not None
    assert config.name == "default"
    mock_setup_bridge.assert_not_called()
    # New implementation calls create_iptables_rule instead of setup_nat
    assert mock_create_iptables_rule.call_count >= 3  # MASQUERADE + FORWARD_IN + FORWARD_OUT


@patch("mvmctl.api.network.sync_iptables_rules")
@patch("mvmctl.api.network.bridge_exists", return_value=True)
@patch("mvmctl.api.network.network_core.setup_bridge")
@patch("mvmctl.api.network.network_core.setup_nat")
@patch("mvmctl.api.network.network_core.setup_mvm_chains", return_value=True)
@patch("mvmctl.utils.network._iptables_rule_exists", return_value=True)
@patch("mvmctl.api.network.create_iptables_rule")
def test_ensure_default_network_idempotent_when_all_exists(
    mock_create_iptables_rule,
    mock_rule_exists,
    mock_setup_chains,
    mock_setup_nat,
    mock_setup_bridge,
    mock_bridge_exists,
    mock_sync_rules,
    mock_cache_dir: Path,
):
    """When both metadata and resources exist, no setup functions should be called."""
    db_net = DBNetwork(
        id="net-default-123",
        name="default",
        subnet="172.35.0.0/24",
        bridge="mvm-default",
        ipv4_gateway="172.35.0.1",
        nat_enabled=True,
    )

    with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
        with patch.object(MVMDatabase, "get_default_network", return_value=db_net):
            with patch.object(MVMDatabase, "update_network_bridge_active"):
                config = ensure_default_network()

    assert config is not None
    assert config.name == "default"
    # All resources exist, no setup should be called
    mock_setup_bridge.assert_not_called()
    mock_setup_nat.assert_not_called()


def test_validate_subnet_no_overlap():
    """Test subnet overlap validation."""
    net1 = DBNetwork(
        id="a" * 64,
        name="net1",
        subnet="10.20.0.0/24",
        bridge="mvm-net1",
        ipv4_gateway="10.20.0.1",
    )

    with patch.object(MVMDatabase, "list_networks", return_value=[net1]):
        existing_networks = list_networks()

    # Should raise error on overlap
    with pytest.raises(NetworkError, match="overlaps with network"):
        validate_no_subnet_overlap("10.20.0.0/23", existing_networks)

    # Should not raise if excluding self
    validate_no_subnet_overlap("10.20.0.0/24", existing_networks, exclude_name="net1")

    # Should not raise on non-overlapping
    validate_no_subnet_overlap("10.20.1.0/24", existing_networks)


class TestSetDefaultNetwork:
    """Tests for set_default_network."""

    def test_set_default_network_success(self, mock_cache_dir: Path):
        """Test setting a network as default."""
        db_net = DBNetwork(
            id="net-mynet-123",
            name="mynet",
            subnet="10.20.1.0/24",
            bridge="mvm-mynet",
            ipv4_gateway="10.20.1.1",
        )

        with patch.object(MVMDatabase, "get_network_by_name", return_value=db_net):
            with patch.object(MVMDatabase, "set_default_network") as mock_set:
                set_default_network("mynet")
                mock_set.assert_called_once_with("net-mynet-123")

    def test_set_default_network_not_found(self, mock_cache_dir: Path):
        """Test setting nonexistent network as default raises error."""
        with patch.object(MVMDatabase, "get_network_by_name", return_value=None):
            with pytest.raises(NetworkError, match="does not exist"):
                set_default_network("nonexistent")


class TestNatGatewaysField:
    """Tests for nat_gateways field in NetworkConfig."""

    def test_network_entry_to_config_with_nat_gateways(self):
        """NetworkConfig should include nat_gateways when present in entry."""
        entry = {
            "subnet": "10.20.1.0/24",
            "ipv4_gateway": "10.20.1.1",
            "bridge": "mvm-testnet",
            "nat_enabled": True,
            "nat_gateways": ["eth0", "eth1"],
            "created_at": "2026-01-01T00:00:00Z",
            "is_default": 0,
        }
        config = network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.nat_gateways == ["eth0", "eth1"]

    def test_network_entry_to_config_without_nat_gateways(self):
        """NetworkConfig should have nat_gateways=[] when not in entry."""
        entry = {
            "subnet": "10.20.1.0/24",
            "ipv4_gateway": "10.20.1.1",
            "bridge": "mvm-testnet",
            "nat_enabled": True,
            "created_at": "2026-01-01T00:00:00Z",
            "is_default": 0,
        }
        config = network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.nat_gateways == []

    def test_network_entry_to_config_invalid_nat_gateways_type(self):
        """NetworkConfig should set nat_gateways=[] for invalid types."""
        entry = {
            "subnet": "10.20.1.0/24",
            "ipv4_gateway": "10.20.1.1",
            "bridge": "mvm-testnet",
            "nat_enabled": True,
            "nat_gateways": "eth0",  # Should be list, not string
            "created_at": "2026-01-01T00:00:00Z",
            "is_default": 0,
        }
        config = network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.nat_gateways == []

    def test_network_entry_to_config_invalid_gateway_in_list(self):
        """NetworkConfig should skip invalid gateways in list."""
        entry = {
            "subnet": "10.20.1.0/24",
            "ipv4_gateway": "10.20.1.1",
            "bridge": "mvm-testnet",
            "nat_enabled": True,
            "nat_gateways": ["eth0", 12345, "eth1"],  # 12345 is invalid
            "created_at": "2026-01-01T00:00:00Z",
            "is_default": 0,
        }
        config = network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.nat_gateways == ["eth0", "eth1"]


class TestRestoreNetworks:
    """Tests for restore_networks function."""

    def test_restore_networks_empty(self, mock_cache_dir: Path):
        """restore_networks should return empty list when no networks exist."""
        with patch("mvmctl.api.network.list_networks", return_value=[]):
            result = restore_networks()
            assert result == []

    def test_restore_networks_existing_bridge(self, mock_cache_dir: Path):
        """restore_networks should skip networks with existing bridges."""
        from mvmctl.models.network import NetworkConfig

        config = NetworkConfig(
            name="testnet",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-testnet",
            nat_enabled=True,
            nat_gateways=["eth0"],
        )

        with patch("mvmctl.api.network.list_networks", return_value=[config]):
            with patch("mvmctl.api.network.bridge_exists", return_value=True):
                result = restore_networks()
                assert len(result) == 1
                assert "bridge already exists" in result[0]

    def test_restore_networks_creates_bridge(self, mock_cache_dir: Path):
        """restore_networks should create missing bridges."""
        from mvmctl.models.network import NetworkConfig

        config = NetworkConfig(
            name="testnet",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-testnet",
            nat_enabled=False,
        )

        with patch("mvmctl.api.network.list_networks", return_value=[config]):
            with patch("mvmctl.api.network.bridge_exists", return_value=False):
                with patch("mvmctl.api.network.network_core.setup_bridge") as mock_setup:
                    with patch("mvmctl.core.metadata.update_network_entry"):
                        result = restore_networks()
                        mock_setup.assert_called_once()
                        assert len(result) == 1
                        assert "created bridge" in result[0]

    def test_restore_networks_validates_interface(self, mock_cache_dir: Path):
        """restore_networks should validate stored interface."""
        from mvmctl.models.network import NetworkConfig

        config = NetworkConfig(
            name="testnet",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-testnet",
            nat_enabled=True,
            nat_gateways=["eth0"],
        )

        with patch("mvmctl.api.network.list_networks", return_value=[config]):
            with patch("mvmctl.api.network.bridge_exists", return_value=False):
                with patch("mvmctl.api.network.network_core.setup_bridge"):
                    with patch(
                        "mvmctl.utils.network.validate_network_interface", return_value=True
                    ):
                        with patch("mvmctl.api.network.network_core.setup_nat") as mock_nat:
                            with patch("mvmctl.core.metadata.update_network_entry"):
                                result = restore_networks()
                                mock_nat.assert_called_once()
                                assert "NAT configured" in result[1]

    def test_restore_networks_fallback_to_default_interface(self, mock_cache_dir: Path):
        """restore_networks should fallback to default interface if stored invalid."""
        from mvmctl.models.network import NetworkConfig

        config = NetworkConfig(
            name="testnet",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-testnet",
            nat_enabled=True,
            nat_gateways=["invalid0"],
        )

        with patch("mvmctl.api.network.list_networks", return_value=[config]):
            with patch("mvmctl.api.network.bridge_exists", return_value=False):
                with patch("mvmctl.api.network.network_core.setup_bridge"):
                    with patch("mvmctl.utils.network.validate_network_interface") as mock_validate:
                        mock_validate.return_value = True
                        with patch(
                            "mvmctl.core.network.get_default_interface", return_value="eth0"
                        ):
                            with patch("mvmctl.api.network.network_core.setup_nat") as mock_nat:
                                with patch("mvmctl.core.metadata.update_network_entry"):
                                    result = restore_networks()
                                    mock_nat.assert_called_once()
                                    assert "NAT configured" in result[1]
