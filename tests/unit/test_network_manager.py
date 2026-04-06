"""Tests for network_manager.py — metadata-based network storage."""

from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.api.metadata import get_default_network_entry
from mvmctl.core.metadata import update_network_entry
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.network_manager import (
    NetworkConfig,
    _bridge_name_for,
    _ipv4_gateway_for_subnet,
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
        config = _network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.name == "testnet"
        assert config.subnet == "10.20.1.0/24"
        assert config.ipv4_gateway == "10.20.1.1"
        assert config.bridge == "mvm-testnet"
        assert config.nat_enabled is True
        assert config.is_default is False

    def test_network_entry_to_config_empty_entry(self):
        assert _network_entry_to_config("testnet", {}) is None

    def test_network_entry_to_config_missing_required_fields(self):
        # Missing gateway
        entry = {"subnet": "10.20.1.0/24", "bridge": "mvm-testnet"}
        assert _network_entry_to_config("testnet", entry) is None

        # Missing subnet
        entry = {"ipv4_gateway": "10.20.1.1", "bridge": "mvm-testnet"}
        assert _network_entry_to_config("testnet", entry) is None

        # Missing bridge
        entry = {"subnet": "10.20.1.0/24", "ipv4_gateway": "10.20.1.1"}
        assert _network_entry_to_config("testnet", entry) is None

    def test_network_entry_to_config_is_default_flag(self):
        entry = {
            "subnet": "10.20.1.0/24",
            "ipv4_gateway": "10.20.1.1",
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
                {"vm_id": "vm1", "ipv4": "10.20.1.2"},
                {"vm_id": "vm2", "ipv4": "10.20.1.3"},
            ]
        }
        leases = _leases_from_entry(entry)
        assert len(leases) == 2
        assert leases[0].vm_id == "vm1"
        assert leases[0].ipv4 == "10.20.1.2"
        assert leases[1].vm_id == "vm2"
        assert leases[1].ipv4 == "10.20.1.3"

    def test_leases_from_entry_empty(self):
        assert _leases_from_entry({}) == []
        assert _leases_from_entry({"leases": []}) == []

    def test_leases_from_entry_invalid_format(self):
        # leases is not a list
        assert _leases_from_entry({"leases": "invalid"}) == []
        # Missing required fields
        assert _leases_from_entry({"leases": [{"vm_id": "vm1"}]}) == []
        assert _leases_from_entry({"leases": [{"ipv4": "10.20.1.2"}]}) == []


def _add_network_to_metadata(cache_dir: Path, name: str, **fields) -> None:
    """Add a network to metadata with optional leases.

    Note: This helper writes to JSON files directly since core layer
    no longer queries the database.
    """
    import json

    defaults = {
        "subnet": "10.20.1.0/24",
        "ipv4_gateway": "10.20.1.1",
        "bridge": f"mvm-{name}",
        "nat_enabled": True,
        "created_at": "2026-01-01T00:00:00Z",
        "leases": [],
        "bridge_active": True,
    }
    defaults.update(fields)

    # Write network config to JSON file directly
    networks_dir = cache_dir / "networks"
    networks_dir.mkdir(parents=True, exist_ok=True)
    network_dir = networks_dir / name
    network_dir.mkdir(parents=True, exist_ok=True)

    config_path = network_dir / "config.json"
    config_data = {
        "subnet": defaults["subnet"],
        "ipv4_gateway": defaults["ipv4_gateway"],
        "bridge": defaults["bridge"],
        "nat_enabled": defaults["nat_enabled"],
        "created_at": defaults["created_at"],
        "bridge_active": defaults["bridge_active"],
    }
    config_path.write_text(json.dumps(config_data))

    # Write leases to separate JSON file
    leases_path = network_dir / "leases.json"
    leases_path.write_text(json.dumps(defaults["leases"]))

    # Set as default if requested
    if defaults.get("is_default"):
        default_path = networks_dir / "default_network.json"
        default_path.write_text(json.dumps({"name": name}))


def test_list_networks(mock_cache_dir: Path):
    assert list_networks() == []

    _add_network_to_metadata(
        mock_cache_dir, "net1", subnet="10.20.1.0/24", ipv4_gateway="10.20.1.1", bridge="mvm-net1"
    )
    _add_network_to_metadata(
        mock_cache_dir, "net2", subnet="10.20.2.0/24", ipv4_gateway="10.20.2.1", bridge="mvm-net2"
    )

    networks = list_networks()
    assert len(networks) == 2
    names = {n.name for n in networks}
    assert names == {"net1", "net2"}


def test_get_network(mock_cache_dir: Path):
    assert get_network("nonexistent") is None

    _add_network_to_metadata(
        mock_cache_dir,
        "testnet",
        subnet="10.20.1.0/24",
        ipv4_gateway="10.20.1.1",
        bridge="mvm-testnet",
    )

    config = get_network("testnet")
    assert config is not None
    assert config.name == "testnet"
    assert config.subnet == "10.20.1.0/24"


def test_get_network_leases(mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir,
        "testnet",
        leases=[{"vm_id": "vm1", "ipv4": "10.20.1.2"}],
    )

    leases = get_network_leases("testnet")
    assert len(leases) == 1
    assert leases[0].vm_id == "vm1"
    assert leases[0].ipv4 == "10.20.1.2"


def test_get_network_leases_empty(mock_cache_dir: Path):
    # Network doesn't exist — returns empty list
    leases = get_network_leases("nonexistent")
    assert leases == []


@patch("mvmctl.core.network_manager.setup_nat")
@patch("mvmctl.core.network_manager.setup_bridge")
@patch("mvmctl.utils.network.list_network_interfaces", return_value=["eth0"])
def test_create_network_success(
    mock_interfaces, mock_setup_bridge, mock_setup_nat, mock_cache_dir: Path
):
    config = create_network(name="mynet", subnet="10.20.0.0/24")
    assert config.name == "mynet"
    assert config.subnet == "10.20.0.0/24"
    assert config.ipv4_gateway == "10.20.0.1"

    # Verify persistence in metadata
    assert get_network("mynet") is not None
    mock_setup_bridge.assert_called_once_with("mvm-mynet", ipv4_gateway_subnet="10.20.0.1/24")
    mock_setup_nat.assert_called_once_with("mvm-mynet", nat_gateways=None, subnet="10.20.0.0/24")


def test_create_network_already_exists(mock_cache_dir: Path):
    _add_network_to_metadata(mock_cache_dir, "mynet")

    with pytest.raises(NetworkError, match="already exists"):
        create_network(name="mynet", subnet="10.20.1.0/24")


@patch("mvmctl.core.network_manager.setup_bridge")
@patch("mvmctl.core.network_manager.teardown_bridge")
def test_create_network_setup_failure(
    mock_teardown_bridge, mock_setup_bridge, mock_cache_dir: Path
):
    mock_setup_bridge.side_effect = NetworkError("Failed to setup bridge")

    with pytest.raises(NetworkError, match="Failed to setup bridge"):
        create_network(name="mynet", subnet="10.20.0.0/24")

    mock_teardown_bridge.assert_called_once()
    assert get_network("mynet") is None


@patch("mvmctl.core.network_manager.teardown_bridge")
@patch("mvmctl.core.network_manager.teardown_nat")
def test_remove_network(mock_teardown_nat, mock_teardown_bridge, mock_cache_dir: Path):
    _add_network_to_metadata(mock_cache_dir, "mynet", leases=[])

    remove_network("mynet")

    mock_teardown_nat.assert_called_once_with(bridge="mvm-mynet", force=True, subnet="10.20.1.0/24")
    mock_teardown_bridge.assert_called_once_with("mvm-mynet")
    assert get_network("mynet") is None


def test_remove_network_not_found():
    with pytest.raises(NetworkError, match="not found"):
        remove_network("nonexistent")


def test_remove_network_with_vms(mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir, "mynet", leases=[{"vm_id": "vm1", "ipv4": "10.20.1.2"}]
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
        mock_cache_dir, "mynet", leases=[{"vm_id": "vm1", "ipv4": "10.20.1.2"}]
    )

    info = inspect_network("mynet")
    assert info["name"] == "mynet"
    assert info["bridge_exists"] is True
    vms = info["vms"]
    assert isinstance(vms, list)
    assert len(vms) == 1
    assert vms[0]["vm_id"] == "vm1"
    assert vms[0]["ipv4"] == "10.20.1.2"
    assert "status" in vms[0]
    assert "pid" in vms[0]
    assert "api_socket_path" in vms[0]


def test_inspect_network_not_found():
    with pytest.raises(NetworkError, match="not found"):
        inspect_network("nonexistent")


@patch("mvmctl.core.network_manager.allocate_ip")
def test_allocate_network_ip(mock_allocate_ip, mock_cache_dir: Path):
    from mvmctl.db.models import VMInstance as DBVMInstance

    _add_network_to_metadata(mock_cache_dir, "mynet", leases=[])

    db = MVMDatabase()
    db.upsert_vm(
        DBVMInstance(
            id="vm1",
            name="vm1",
            status="stopped",
            created_at="2026-01-01T00:00:00Z",
            updated_at="2026-01-01T00:00:00Z",
        )
    )

    mock_allocate_ip.return_value = "10.20.1.2"

    ip = allocate_network_ip("mynet", "vm1")
    assert ip == "10.20.1.2"

    mock_allocate_ip.assert_called_once_with(["10.20.1.1"], subnet="10.20.1.0/24")

    leases = get_network_leases("mynet")
    assert len(leases) == 1
    assert leases[0].vm_id == "vm1"
    assert leases[0].ipv4 == "10.20.1.2"


def test_allocate_network_ip_not_found():
    with pytest.raises(NetworkError, match="not found"):
        allocate_network_ip("nonexistent", "vm1")


def test_release_network_ip(mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir,
        "mynet",
        leases=[
            {"vm_id": "vm1", "ipv4": "10.20.1.2"},
            {"vm_id": "vm2", "ipv4": "10.20.1.3"},
        ],
    )

    release_network_ip("mynet", "vm1")

    leases = get_network_leases("mynet")
    assert len(leases) == 1
    assert leases[0].vm_id == "vm2"


@patch("mvmctl.utils.network.get_default_interface")
@patch("mvmctl.core.network_manager.create_network")
@patch("mvmctl.core.network_manager.set_default_network")
def test_ensure_default_network_creates_when_missing(
    mock_set_default, mock_create_network, mock_get_default_iface, mock_cache_dir: Path
):
    # Doesn't exist, will be created
    mock_get_default_iface.return_value = "wlo1"
    mock_create_network.return_value = NetworkConfig(
        "default", "172.35.0.0/24", "172.35.0.1", "mvm-default"
    )
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
        patch("mvmctl.core.network_manager.setup_bridge"),
        patch("mvmctl.core.network_manager.setup_nat"),
        patch("mvmctl.utils.network.list_network_interfaces", return_value=["eth0"]),
    ):
        config = create_network(name="default", subnet="10.20.0.0/24")

    assert config.name == "default"
    # Host state should NOT be marked - only ensure_default_network does that now
    state = db.get_host_state()
    assert state is None or bool(state.default_network_created) is False


@patch("mvmctl.utils.network.bridge_exists", return_value=True)
@patch("mvmctl.core.network.setup_nat")
@patch("mvmctl.core.network.setup_mvm_chains", return_value=True)
def test_ensure_default_network_returns_existing(
    mock_setup_chains, mock_setup_nat, mock_bridge_exists, mock_cache_dir: Path
):
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        subnet="172.35.0.0/24",
        ipv4_gateway="172.35.0.1",
        bridge="mvm-default",
    )

    config = ensure_default_network()
    assert config is not None
    assert config.name == "default"


def test_ensure_default_network_creates_default_network_metadata(
    mock_cache_dir: Path, monkeypatch: pytest.MonkeyPatch
):
    """Test that ensure_default_network creates default network in JSON metadata."""
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        subnet="172.35.0.0/24",
        ipv4_gateway="172.35.0.1",
        bridge="mvm-default",
    )

    db = MVMDatabase()
    from mvmctl.db.models import Network

    network = Network(
        id="net-default-123",
        name="default",
        subnet="172.35.0.0/24",
        ipv4_gateway="172.35.0.1",
        bridge="mvm-default",
        nat_enabled=True,
    )
    db.upsert_network(network)
    db.set_default_network("net-default-123")

    with (
        patch("mvmctl.utils.network.bridge_exists", return_value=True),
        patch("mvmctl.core.network.setup_nat"),
        patch("mvmctl.core.network.setup_mvm_chains", return_value=True),
    ):
        config = ensure_default_network()

    assert config is not None
    default_entry = get_default_network_entry(mock_cache_dir)
    assert default_entry is not None
    assert default_entry[0] == "default"


def test_ensure_default_network_sets_default_when_none_exists(mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        subnet="172.35.0.0/24",
        ipv4_gateway="172.35.0.1",
        bridge="mvm-default",
    )

    db = MVMDatabase()
    from mvmctl.db.models import Network

    network = Network(
        id="net-default-456",
        name="default",
        subnet="172.35.0.0/24",
        ipv4_gateway="172.35.0.1",
        bridge="mvm-default",
        nat_enabled=True,
    )
    db.upsert_network(network)
    db.set_default_network("net-default-456")

    with (
        patch("mvmctl.utils.network.bridge_exists", return_value=True),
        patch("mvmctl.core.network.setup_nat"),
        patch("mvmctl.core.network.setup_mvm_chains", return_value=True),
    ):
        config = ensure_default_network()

    assert config is not None
    default_entry = get_default_network_entry(mock_cache_dir)
    assert default_entry is not None
    assert default_entry[0] == "default"


def test_ensure_default_network_preserves_existing_other_default(mock_cache_dir: Path):
    _add_network_to_metadata(
        mock_cache_dir,
        "custom",
        subnet="10.20.0.0/24",
        ipv4_gateway="10.20.0.1",
        bridge="mvm-custom",
        is_default=True,
    )
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        subnet="172.35.0.0/24",
        ipv4_gateway="172.35.0.1",
        bridge="mvm-default",
        is_default=False,
    )

    db = MVMDatabase()
    from mvmctl.db.models import Network

    custom_network = Network(
        id="net-custom-123",
        name="custom",
        subnet="10.20.0.0/24",
        ipv4_gateway="10.20.0.1",
        bridge="mvm-custom",
        nat_enabled=True,
    )
    db.upsert_network(custom_network)
    db.set_default_network("net-custom-123")

    with (
        patch("mvmctl.utils.network.bridge_exists", return_value=True),
        patch("mvmctl.core.network.setup_nat"),
        patch("mvmctl.core.network.setup_mvm_chains", return_value=True),
    ):
        config = ensure_default_network()

    assert config is not None
    default_entry = get_default_network_entry(mock_cache_dir)
    assert default_entry is not None
    assert default_entry[0] == "custom"


@patch("mvmctl.utils.network.bridge_exists", return_value=False)
@patch("mvmctl.core.network.setup_bridge")
@patch("mvmctl.core.network.setup_nat")
@patch("mvmctl.core.network.setup_mvm_chains", return_value=True)
@patch("mvmctl.utils.network.get_default_interface", return_value="eth0")
def test_ensure_default_network_recreates_missing_bridge(
    mock_get_iface,
    mock_setup_chains,
    mock_setup_nat,
    mock_setup_bridge,
    mock_bridge_exists,
    mock_cache_dir: Path,
):
    """When metadata exists but bridge is missing, setup_bridge should be called."""
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        subnet="172.35.0.0/24",
        ipv4_gateway="172.35.0.1",
        bridge="mvm-default",
        nat_enabled=True,
    )

    config = ensure_default_network()
    assert config is not None
    assert config.name == "default"
    mock_setup_bridge.assert_called_once_with("mvm-default", ipv4_gateway_subnet="172.35.0.1/24")
    mock_setup_nat.assert_called_once_with(
        "mvm-default", nat_gateways=["eth0"], subnet="172.35.0.0/24"
    )


@patch("mvmctl.utils.network.bridge_exists", return_value=True)
@patch("mvmctl.core.network.setup_bridge")
@patch("mvmctl.core.network.setup_nat")
@patch("mvmctl.core.network.setup_mvm_chains", return_value=False)
@patch("mvmctl.utils.network.get_default_interface", return_value="eth0")
def test_ensure_default_network_recreates_missing_chains(
    mock_get_iface,
    mock_setup_chains,
    mock_setup_nat,
    mock_setup_bridge,
    mock_bridge_exists,
    mock_cache_dir: Path,
):
    """When bridge exists but chains were just created, NAT should be set up."""
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        subnet="172.35.0.0/24",
        ipv4_gateway="172.35.0.1",
        bridge="mvm-default",
        nat_enabled=True,
    )

    config = ensure_default_network()
    assert config is not None
    assert config.name == "default"
    mock_setup_bridge.assert_not_called()
    mock_setup_nat.assert_called_once_with(
        "mvm-default", nat_gateways=["eth0"], subnet="172.35.0.0/24"
    )


@patch("mvmctl.utils.network.bridge_exists", return_value=True)
@patch("mvmctl.core.network.setup_bridge")
@patch("mvmctl.core.network.setup_nat")
@patch("mvmctl.core.network.setup_mvm_chains", return_value=True)
@patch("mvmctl.utils.network._iptables_rule_exists", return_value=True)
def test_ensure_default_network_idempotent_when_all_exists(
    mock_rule_exists,
    mock_setup_chains,
    mock_setup_nat,
    mock_setup_bridge,
    mock_bridge_exists,
    mock_cache_dir: Path,
):
    """When both metadata and resources exist, no setup functions should be called."""
    _add_network_to_metadata(
        mock_cache_dir,
        "default",
        subnet="172.35.0.0/24",
        ipv4_gateway="172.35.0.1",
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
        mock_cache_dir, "net1", subnet="10.20.0.0/24", ipv4_gateway="10.20.0.1", bridge="mvm-net1"
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

        get_network("mynet")

        import json

        default_path = mock_cache_dir / "networks" / "default_network.json"
        assert default_path.exists()
        data = json.loads(default_path.read_text())
        assert data.get("name") == "mynet"

    def test_set_default_network_not_found(self, mock_cache_dir: Path):
        from mvmctl.core.network_manager import set_default_network

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
        config = _network_entry_to_config("testnet", entry)
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
        config = _network_entry_to_config("testnet", entry)
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
        config = _network_entry_to_config("testnet", entry)
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
        config = _network_entry_to_config("testnet", entry)
        assert config is not None
        assert config.nat_gateways == ["eth0", "eth1"]


class TestRestoreNetworks:
    """Tests for restore_networks function."""

    def test_restore_networks_empty(self, mock_cache_dir: Path):
        """restore_networks should return empty list when no networks exist."""
        from mvmctl.core.network_manager import restore_networks

        with patch("mvmctl.core.network_manager.list_networks", return_value=[]):
            result = restore_networks()
            assert result == []

    def test_restore_networks_existing_bridge(self, mock_cache_dir: Path):
        """restore_networks should skip networks with existing bridges."""
        from mvmctl.core.network_manager import restore_networks
        from mvmctl.models.network import NetworkConfig

        config = NetworkConfig(
            name="testnet",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-testnet",
            nat_enabled=True,
            nat_gateways=["eth0"],
        )

        with patch("mvmctl.core.network_manager.list_networks", return_value=[config]):
            with patch("mvmctl.utils.network.bridge_exists", return_value=True):
                result = restore_networks()
                assert len(result) == 1
                assert "bridge already exists" in result[0]

    def test_restore_networks_creates_bridge(self, mock_cache_dir: Path):
        """restore_networks should create missing bridges."""
        from mvmctl.core.network_manager import restore_networks
        from mvmctl.models.network import NetworkConfig

        config = NetworkConfig(
            name="testnet",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-testnet",
            nat_enabled=False,
        )

        with patch("mvmctl.core.network_manager.list_networks", return_value=[config]):
            with patch("mvmctl.utils.network.bridge_exists", return_value=False):
                with patch("mvmctl.core.network.setup_bridge") as mock_setup:
                    with patch("mvmctl.core.network_manager.update_network_entry"):
                        result = restore_networks()
                        mock_setup.assert_called_once()
                        assert len(result) == 1
                        assert "created bridge" in result[0]

    def test_restore_networks_validates_interface(self, mock_cache_dir: Path):
        """restore_networks should validate stored interface."""
        from mvmctl.core.network_manager import restore_networks
        from mvmctl.models.network import NetworkConfig

        config = NetworkConfig(
            name="testnet",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-testnet",
            nat_enabled=True,
            nat_gateways=["eth0"],
        )

        with patch("mvmctl.core.network_manager.list_networks", return_value=[config]):
            with patch("mvmctl.utils.network.bridge_exists", return_value=False):
                with patch("mvmctl.core.network.setup_bridge"):
                    with patch(
                        "mvmctl.utils.network.validate_network_interface", return_value=True
                    ):
                        with patch("mvmctl.core.network.setup_nat") as mock_nat:
                            with patch("mvmctl.core.network_manager.update_network_entry"):
                                result = restore_networks()
                                mock_nat.assert_called_once()
                                assert "NAT configured" in result[1]

    def test_restore_networks_fallback_to_default_interface(self, mock_cache_dir: Path):
        """restore_networks should fallback to default interface if stored invalid."""
        from mvmctl.core.network_manager import restore_networks
        from mvmctl.models.network import NetworkConfig

        config = NetworkConfig(
            name="testnet",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-testnet",
            nat_enabled=True,
            nat_gateways=["invalid0"],
        )

        with patch("mvmctl.core.network_manager.list_networks", return_value=[config]):
            with patch("mvmctl.utils.network.bridge_exists", return_value=False):
                with patch("mvmctl.core.network.setup_bridge"):
                    with patch("mvmctl.utils.network.validate_network_interface") as mock_validate:
                        mock_validate.return_value = True
                        with patch(
                            "mvmctl.core.network.get_default_interface", return_value="eth0"
                        ):
                            with patch("mvmctl.core.network.setup_nat") as mock_nat:
                                with patch("mvmctl.core.network_manager.update_network_entry"):
                                    result = restore_networks()
                                    mock_nat.assert_called_once()
                                    assert "NAT configured" in result[1]
