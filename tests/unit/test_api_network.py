"""Tests for network API module."""

from unittest.mock import MagicMock, patch

import mvmctl.api.network as network_api
from mvmctl.api.network import create_network, remove_network
from mvmctl.models.network import NetworkConfig, NetworkLease


@patch("mvmctl.api.network.list_network_interfaces", return_value=["eth0"])
@patch("mvmctl.api.network.check_privileges_interactive")
@patch("mvmctl.api.network._create_network")
def test_create_network_with_privileges(mock_create, mock_check_priv, mock_interfaces):
    """Test create_network calls privilege check and delegates."""
    mock_config = MagicMock(spec=NetworkConfig)
    mock_create.return_value = mock_config

    result = create_network("test-net", subnet="10.0.0.0/24", ipv4_gateway="10.0.0.1", nat=True)

    mock_check_priv.assert_called_once_with("/usr/sbin/ip", "create network 'test-net'")
    mock_create.assert_called_once_with(
        "test-net", subnet="10.0.0.0/24", ipv4_gateway="10.0.0.1", nat=True, nat_gateways=None
    )
    assert result == mock_config


@patch("mvmctl.api.network.check_privileges_interactive")
@patch("mvmctl.api.network._remove_network")
def test_remove_network_with_privileges(mock_remove, mock_check_priv):
    """Test remove_network calls privilege check and delegates."""
    remove_network("test-net")

    mock_check_priv.assert_called_once_with("/usr/sbin/ip", "remove network 'test-net'")
    mock_remove.assert_called_once_with("test-net")


@patch("mvmctl.api.network.list_networks")
def test_list_networks_delegates(mock_list):
    """Test list_networks delegates to core."""
    mock_networks = [MagicMock(spec=NetworkConfig), MagicMock(spec=NetworkConfig)]
    mock_list.return_value = mock_networks

    result = network_api.list_networks()

    assert result == mock_networks


@patch("mvmctl.api.network.get_network")
def test_get_network_delegates(mock_get):
    """Test get_network delegates to core."""
    mock_config = MagicMock(spec=NetworkConfig)
    mock_get.return_value = mock_config

    result = network_api.get_network("test-net")

    mock_get.assert_called_once_with("test-net")
    assert result == mock_config


@patch("mvmctl.api.network.get_network_leases")
def test_get_network_leases_delegates(mock_get_leases):
    """Test get_network_leases delegates to core."""
    mock_leases = [MagicMock(spec=NetworkLease), MagicMock(spec=NetworkLease)]
    mock_get_leases.return_value = mock_leases

    result = network_api.get_network_leases("test-net")

    mock_get_leases.assert_called_once_with("test-net")
    assert result == mock_leases


@patch("mvmctl.api.network.inspect_network")
def test_inspect_network_delegates(mock_inspect):
    """Test inspect_network delegates to core."""
    mock_info = {"name": "test-net", "bridge": "mvm-test-net"}
    mock_inspect.return_value = mock_info

    result = network_api.inspect_network("test-net")

    mock_inspect.assert_called_once_with("test-net")
    assert result == mock_info


@patch("mvmctl.api.network.allocate_network_ip")
def test_allocate_network_ip_delegates(mock_allocate):
    """Test allocate_network_ip delegates to core."""
    mock_allocate.return_value = "10.0.0.5"

    result = network_api.allocate_network_ip("test-net", "vm1")

    mock_allocate.assert_called_once_with("test-net", "vm1")
    assert result == "10.0.0.5"


@patch("mvmctl.api.network.release_network_ip")
def test_release_network_ip_delegates(mock_release):
    """Test release_network_ip delegates to core."""
    network_api.release_network_ip("test-net", "10.0.0.5")

    mock_release.assert_called_once_with("test-net", "10.0.0.5")


@patch("mvmctl.api.network.ensure_default_network")
def test_ensure_default_network_delegates(mock_ensure):
    """Test ensure_default_network delegates to core."""
    mock_ensure.return_value = MagicMock(spec=NetworkConfig)

    result = network_api.ensure_default_network()

    assert result == mock_ensure.return_value


@patch("mvmctl.api.network.get_iptables_rules_for_bridge")
def test_get_iptables_rules_for_bridge_delegates(mock_get_rules):
    """Test get_iptables_rules_for_bridge delegates to core."""
    mock_rules = ["-A FORWARD -i mvm-br0 -j ACCEPT"]
    mock_get_rules.return_value = mock_rules

    result = network_api.get_iptables_rules_for_bridge("mvm-br0")

    mock_get_rules.assert_called_once_with("mvm-br0")
    assert result == mock_rules
