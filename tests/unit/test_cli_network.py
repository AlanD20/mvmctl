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
    bridge="mvm-testnet",
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
    """Remove without --force - proceeds immediately."""
    result = runner.invoke(app, ["remove", "testnet"])
    assert result.exit_code == 0
    assert "removed" in result.output.lower()
    mock_remove.assert_called_once_with("testnet")


@patch("mvmctl.cli.network.remove_network", side_effect=NetworkError("not found"))
def test_remove_error(mock_remove):
    result = runner.invoke(app, ["remove", "testnet"])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


@patch("mvmctl.cli.network.remove_network")
def test_rm_alias(mock_remove):
    """Rm alias without --force - proceeds immediately."""
    result = runner.invoke(app, ["rm", "testnet"])
    assert result.exit_code == 0
    mock_remove.assert_called_once_with("testnet")


# ---------------------------------------------------------------------------
# network inspect
# ---------------------------------------------------------------------------

_FAKE_INSPECT = {
    "name": "testnet",
    "cidr": "192.168.100.0/24",
    "gateway": "192.168.100.1",
    "bridge": "mvm-testnet",
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
    result = runner.invoke(app, ["remove", "UPPER"])
    assert result.exit_code == 1


def test_inspect_rejects_invalid_name():
    """Network name with semicolon should be rejected."""
    result = runner.invoke(app, ["inspect", "bad;name"])
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# State Validation X marks (Phase 4)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.list_networks")
@patch("mvmctl.cli.network.get_network_leases")
def test_network_ls_shows_x_mark_for_missing_bridge(mock_leases, mock_list, mocker):
    """Verify X prefix when bridge interface missing."""
    # Mock NetworkConfig with bridge
    net = NetworkConfig(
        name="testnet",
        cidr="192.168.100.0/24",
        gateway="192.168.100.1",
        bridge="mvm-testnet",
        nat_enabled=True,
        created_at="2024-01-01T00:00:00+00:00",
    )
    mock_list.return_value = [net]
    mock_leases.return_value = []

    # Mock bridge_exists check -> False
    mocker.patch("mvmctl.cli.network.is_bridge_alive", return_value=False)

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "X " prefix in output
    assert "X " in result.output


@patch("mvmctl.cli.network.list_networks")
@patch("mvmctl.cli.network.get_network_leases")
def test_network_ls_no_x_mark_for_existing_bridge(mock_leases, mock_list, mocker):
    """Verify no X prefix when bridge exists."""
    # Mock NetworkConfig with bridge
    net = NetworkConfig(
        name="testnet",
        cidr="192.168.100.0/24",
        gateway="192.168.100.1",
        bridge="mvm-testnet",
        nat_enabled=True,
        created_at="2024-01-01T00:00:00+00:00",
    )
    mock_list.return_value = [net]
    mock_leases.return_value = []

    # Mock bridge_exists check -> True
    mocker.patch("mvmctl.cli.network.is_bridge_alive", return_value=True)

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify no "X " prefix for existing bridge
    lines = result.output.split("\n")
    for line in lines:
        if "testnet" in line and "Name" not in line:
            assert not line.startswith("X ")


# ---------------------------------------------------------------------------
# Default prefix tests (Phase 4)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.list_networks")
@patch("mvmctl.cli.network.get_network_leases")
def test_network_ls_shows_default_prefix(mock_leases, mock_list):
    """Verify * prefix shown for default network."""
    # Mock list_networks returning network with is_default=True
    net = NetworkConfig(
        name="default",
        cidr="10.0.0.0/24",
        gateway="10.0.0.1",
        bridge="mvm-default",
        nat_enabled=True,
        created_at="2024-01-01T00:00:00+00:00",
        is_default=True,
    )
    mock_list.return_value = [net]
    mock_leases.return_value = []

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "* " prefix in output
    assert "* " in result.output


@patch("mvmctl.cli.network.list_networks")
@patch("mvmctl.cli.network.get_network_leases")
def test_network_ls_no_prefix_for_non_default(mock_leases, mock_list):
    """Verify no * prefix for non-default network."""
    # Mock list_networks returning network with is_default=False
    net = NetworkConfig(
        name="custom",
        cidr="192.168.1.0/24",
        gateway="192.168.1.1",
        bridge="mvm-custom",
        nat_enabled=True,
        created_at="2024-01-01T00:00:00+00:00",
        is_default=False,
    )
    mock_list.return_value = [net]
    mock_leases.return_value = []

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify no "* " prefix for non-default network
    lines = result.output.split("\n")
    for line in lines:
        if "custom" in line and "Name" not in line:
            assert not line.startswith("* ")
