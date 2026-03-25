"""Tests for cli/network.py."""

from unittest.mock import patch

from typer.testing import CliRunner

from mvmctl.cli.network import app
from mvmctl.core.network_manager import NetworkConfig
from mvmctl.exceptions import NetworkError

runner = CliRunner()

_FAKE_NET = NetworkConfig(
    name="testnet",
    cidr="192.168.100.0/24",
    gateway="192.168.100.1",
    bridge="fcm-testnet",
    nat_enabled=True,
    created_at="2024-01-01T00:00:00+00:00",
)


# ---------------------------------------------------------------------------
# network ls
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.list_networks", return_value=[])
def test_ls_empty(mock_list):
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "No networks found" in result.output


@patch("mvmctl.cli.network.list_networks", return_value=[_FAKE_NET])
@patch("mvmctl.cli.network.get_network_leases", return_value=[])
def test_ls_with_networks(mock_leases, mock_list):
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "testnet" in result.output


@patch("mvmctl.cli.network.list_networks", return_value=[_FAKE_NET])
@patch("mvmctl.cli.network.get_network_leases", return_value=[])
def test_ls_json(mock_leases, mock_list):
    result = runner.invoke(app, ["ls", "--json"])
    assert result.exit_code == 0
    assert '"testnet"' in result.output


# ---------------------------------------------------------------------------
# network create
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.create_network", return_value=_FAKE_NET)
def test_create_success(mock_create):
    result = runner.invoke(app, ["create", "testnet", "--cidr", "192.168.100.0/24"])
    assert result.exit_code == 0
    assert "created" in result.output.lower()
    mock_create.assert_called_once_with(
        name="testnet",
        cidr="192.168.100.0/24",
        gateway=None,
        nat=True,
    )


@patch("mvmctl.cli.network.create_network", side_effect=NetworkError("already exists"))
def test_create_error(mock_create):
    result = runner.invoke(app, ["create", "testnet", "--cidr", "192.168.100.0/24"])
    assert result.exit_code == 1
    assert "already exists" in result.output.lower()


def test_create_missing_cidr():
    result = runner.invoke(app, ["create", "testnet"])
    assert result.exit_code == 1
    assert "--cidr" in result.output


# ---------------------------------------------------------------------------
# network remove
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.remove_network")
def test_remove_success(mock_remove):
    result = runner.invoke(app, ["remove", "testnet", "--force"])
    assert result.exit_code == 0
    assert "removed" in result.output.lower()
    mock_remove.assert_called_once_with("testnet")


@patch("mvmctl.cli.network.remove_network", side_effect=NetworkError("not found"))
def test_remove_error(mock_remove):
    result = runner.invoke(app, ["remove", "testnet", "--force"])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


@patch("mvmctl.cli.network.remove_network")
def test_rm_alias(mock_remove):
    result = runner.invoke(app, ["rm", "testnet", "--force"])
    assert result.exit_code == 0
    mock_remove.assert_called_once_with("testnet")


# ---------------------------------------------------------------------------
# network inspect
# ---------------------------------------------------------------------------

_FAKE_INSPECT = {
    "name": "testnet",
    "cidr": "192.168.100.0/24",
    "gateway": "192.168.100.1",
    "bridge": "fcm-testnet",
    "nat_enabled": True,
    "bridge_exists": False,
    "created_at": "2024-01-01T00:00:00+00:00",
    "vms": [],
}


@patch("mvmctl.cli.network.inspect_network", return_value=_FAKE_INSPECT)
@patch("mvmctl.cli.network.get_iptables_rules_for_bridge", return_value=[])
def test_inspect_success(mock_rules, mock_inspect):
    result = runner.invoke(app, ["inspect", "testnet"])
    assert result.exit_code == 0
    assert "testnet" in result.output


@patch("mvmctl.cli.network.inspect_network", return_value=_FAKE_INSPECT)
@patch("mvmctl.cli.network.get_iptables_rules_for_bridge", return_value=[])
def test_inspect_json(mock_rules, mock_inspect):
    result = runner.invoke(app, ["inspect", "testnet", "--json"])
    assert result.exit_code == 0
    assert '"testnet"' in result.output


@patch("mvmctl.cli.network.inspect_network", side_effect=NetworkError("not found"))
@patch("mvmctl.cli.network.get_iptables_rules_for_bridge", return_value=[])
def test_inspect_error(mock_rules, mock_inspect):
    result = runner.invoke(app, ["inspect", "testnet"])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


# ---------------------------------------------------------------------------
# help subcommand at subcommand level (Phase 4 §5)
# ---------------------------------------------------------------------------


def test_create_help_arg_shows_help():
    result = runner.invoke(app, ["create", "help"])
    assert result.exit_code == 0
    assert "Usage" in result.output


def test_create_no_args_shows_help():
    result = runner.invoke(app, ["create"])
    assert "Usage" in result.output


def test_remove_help_arg_shows_help():
    result = runner.invoke(app, ["remove", "help"])
    assert result.exit_code == 0
    assert "Usage" in result.output


def test_inspect_help_arg_shows_help():
    result = runner.invoke(app, ["inspect", "help"])
    assert result.exit_code == 0
    assert "Usage" in result.output


# ---------------------------------------------------------------------------
# S-H1: Entity name validation on network commands
# ---------------------------------------------------------------------------


def test_create_rejects_invalid_name():
    result = runner.invoke(app, ["create", "../evil", "--cidr", "192.168.1.0/24"])
    assert result.exit_code != 0
    assert isinstance(result.exception, Exception)
    assert "Invalid network name" in str(result.exception)


def test_remove_rejects_invalid_name():
    """Uppercase network name should be rejected."""
    result = runner.invoke(app, ["remove", "UPPER", "--force"])
    assert result.exit_code == 1


def test_inspect_rejects_invalid_name():
    """Network name with semicolon should be rejected."""
    result = runner.invoke(app, ["inspect", "bad;name"])
    assert result.exit_code == 1
