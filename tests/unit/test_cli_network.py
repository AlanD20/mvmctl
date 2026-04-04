"""Tests for cli/network.py."""

from unittest.mock import patch

from typer.testing import CliRunner

from mvmctl.cli.network import app
from mvmctl.models.network import NetworkConfig
from mvmctl.exceptions import MVMError, NetworkError

runner = CliRunner()

_FAKE_NET = NetworkConfig(
    name="testnet",
    subnet="192.168.100.0/24",
    ipv4_gateway="192.168.100.1",
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
    # Verify column header is "Network" not "SUBNET"
    assert "Network" in result.output


@patch("mvmctl.cli.network.list_networks", return_value=[_FAKE_NET])
@patch("mvmctl.cli.network.get_network_leases", return_value=[])
def test_ls_json(mock_leases, mock_list):
    result = runner.invoke(app, ["ls", "--json"])
    assert result.exit_code == 0
    assert '"testnet"' in result.output


# ---------------------------------------------------------------------------
# network create
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0"])
@patch("mvmctl.cli.network.create_network", return_value=_FAKE_NET)
def test_create_success(mock_create, mock_interfaces):
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 0
    assert "created" in result.output.lower()
    mock_create.assert_called_once_with(
        name="testnet",
        subnet="192.168.100.0/24",
        ipv4_gateway=None,
        nat=True,
        nat_gateways=["eth0"],
    )


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0"])
@patch("mvmctl.cli.network.create_network", side_effect=NetworkError("already exists"))
def test_create_error(mock_create, mock_interfaces):
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 1
    assert "already exists" in result.output.lower()


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0"])
@patch(
    "mvmctl.cli.network.create_network",
    side_effect=NetworkError(
        "Subnet 192.168.100.0/24 overlaps with network 'default' (192.168.100.0/24)"
    ),
)
def test_create_error_subnet_overlap(mock_create, mock_interfaces):
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 1
    assert "overlaps" in result.output.lower()


def test_create_missing_subnet():
    result = runner.invoke(app, ["create", "testnet"])
    assert result.exit_code == 1
    assert "--subnet" in result.output


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
    "subnet": "192.168.100.0/24",
    "ipv4_gateway": "192.168.100.1",
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
    result = runner.invoke(app, ["create", "../evil", "--subnet", "192.168.1.0/24"])
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
        subnet="192.168.100.0/24",
        ipv4_gateway="192.168.100.1",
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
        subnet="192.168.100.0/24",
        ipv4_gateway="192.168.100.1",
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
@patch("mvmctl.cli.network.is_bridge_alive")
def test_network_ls_shows_default_prefix(mock_bridge_alive, mock_leases, mock_list):
    """Verify * prefix shown for default network."""
    # Mock list_networks returning network with is_default=True
    net = NetworkConfig(
        name="default",
        subnet="10.0.0.0/24",
        ipv4_gateway="10.0.0.1",
        bridge="mvm-default",
        nat_enabled=True,
        created_at="2024-01-01T00:00:00+00:00",
        is_default=True,
    )
    mock_list.return_value = [net]
    mock_leases.return_value = []
    mock_bridge_alive.return_value = True

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "* " prefix (default marker) in output
    assert "* default" in result.output


@patch("mvmctl.cli.network.list_networks")
@patch("mvmctl.cli.network.get_network_leases")
@patch("mvmctl.cli.network.is_bridge_alive")
def test_network_ls_default_prefix_takes_priority_over_missing_bridge(
    mock_bridge_alive, mock_leases, mock_list
):
    """Verify * prefix takes priority over X when default network's bridge is missing."""
    # Mock list_networks returning default network with missing bridge
    net = NetworkConfig(
        name="default",
        subnet="10.0.0.0/24",
        ipv4_gateway="10.0.0.1",
        bridge="mvm-default",
        nat_enabled=True,
        created_at="2024-01-01T00:00:00+00:00",
        is_default=True,
    )
    mock_list.return_value = [net]
    mock_leases.return_value = []
    mock_bridge_alive.return_value = False  # Bridge is missing

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "* default" is shown, not "*X default"
    assert "* default" in result.output
    # Verify "*X" is NOT in output (X should not be leading)
    assert "*X" not in result.output


@patch("mvmctl.cli.network.list_networks")
@patch("mvmctl.cli.network.get_network_leases")
def test_network_ls_no_prefix_for_non_default(mock_leases, mock_list):
    """Verify no * prefix for non-default network."""
    # Mock list_networks returning network with is_default=False
    net = NetworkConfig(
        name="custom",
        subnet="192.168.1.0/24",
        ipv4_gateway="192.168.1.1",
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


# ---------------------------------------------------------------------------
# network help subcommand (covers lines 49-50)
# ---------------------------------------------------------------------------


def test_network_help_subcommand():
    """Help subcommand echoes help text and exits 0."""
    result = runner.invoke(app, ["help"])
    assert result.exit_code == 0


# ---------------------------------------------------------------------------
# network set-default NetworkError branch (covers lines 119-121)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.set_default_network", side_effect=NetworkError("no such network"))
def test_set_default_network_error(mock_set):
    result = runner.invoke(app, ["set-default", "missing"])
    assert result.exit_code == 1
    assert "no such network" in result.output.lower()


# ---------------------------------------------------------------------------
# network create — ipv4_gateway validation error (covers lines 160-162)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0"])
@patch("mvmctl.cli.network.validate_ipv4_address", side_effect=MVMError("bad address"))
def test_create_invalid_ipv4_gateway(mock_validate, mock_ifaces):
    result = runner.invoke(
        app, ["create", "testnet", "--subnet", "192.168.100.0/24", "--ipv4-gateway", "not-an-ip"]
    )
    assert result.exit_code == 1
    assert "invalid ipv4 gateway" in result.output.lower()


# ---------------------------------------------------------------------------
# network create — no interfaces found (covers lines 166-168)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.list_network_interfaces", return_value=[])
def test_create_no_interfaces(mock_ifaces):
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 1
    assert "no network interfaces" in result.output.lower()


# ---------------------------------------------------------------------------
# network create — multiple interfaces, user selects one (covers lines 172-192)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0", "wlan0"])
@patch("mvmctl.cli.network.create_network", return_value=_FAKE_NET)
@patch("mvmctl.cli.network.Prompt.ask", return_value="1")
def test_create_multiple_interfaces_select_first(mock_prompt, mock_create, mock_ifaces):
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 0
    mock_create.assert_called_once_with(
        name="testnet",
        subnet="192.168.100.0/24",
        ipv4_gateway=None,
        nat=True,
        nat_gateways=["eth0"],
    )


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0", "wlan0"])
@patch("mvmctl.cli.network.Prompt.ask", return_value="99")
def test_create_multiple_interfaces_invalid_index(mock_prompt, mock_ifaces):
    """All selected indices out of range → error."""
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 1
    assert "no valid interface indices" in result.output.lower()


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0", "wlan0"])
@patch("mvmctl.cli.network.Prompt.ask", return_value="notanumber")
def test_create_multiple_interfaces_non_integer_selection(mock_prompt, mock_ifaces):
    """Non-integer input → ValueError → error message."""
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 1
    assert "invalid interface selection" in result.output.lower()


# ---------------------------------------------------------------------------
# network create — validate_nat_gateways raises MVMError (covers lines 197-199)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.validate_nat_gateways", side_effect=MVMError("bad gateway"))
def test_create_invalid_nat_gateways(mock_validate):
    result = runner.invoke(
        app, ["create", "testnet", "--subnet", "192.168.100.0/24", "--nat-gateways", "bad0"]
    )
    assert result.exit_code == 1
    assert "invalid nat gateways" in result.output.lower()


# ---------------------------------------------------------------------------
# network create NetworkError branches (covers lines 226-231)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0"])
@patch(
    "mvmctl.cli.network.create_network",
    side_effect=NetworkError("bridge mvm-x conflicts with existing bridge"),
)
def test_create_error_bridge_conflicts(mock_create, mock_ifaces):
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 1
    assert "different network name" in result.output.lower()


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0"])
@patch(
    "mvmctl.cli.network.create_network",
    side_effect=NetworkError("privilege denied"),
)
def test_create_error_privilege(mock_create, mock_ifaces):
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 1
    assert "sudo mvm host init" in result.output


# ---------------------------------------------------------------------------
# network create — nat_gateways printed on success (covers line 240)
# ---------------------------------------------------------------------------

_FAKE_NET_WITH_GATEWAYS = NetworkConfig(
    name="testnet",
    subnet="192.168.100.0/24",
    ipv4_gateway="192.168.100.1",
    bridge="mvm-testnet",
    nat_enabled=True,
    nat_gateways=["eth0"],
    created_at="2024-01-01T00:00:00+00:00",
)


@patch("mvmctl.cli.network.list_network_interfaces", return_value=["eth0"])
@patch("mvmctl.cli.network.create_network", return_value=_FAKE_NET_WITH_GATEWAYS)
def test_create_success_prints_nat_gateways(mock_create, mock_ifaces):
    result = runner.invoke(app, ["create", "testnet", "--subnet", "192.168.100.0/24"])
    assert result.exit_code == 0
    assert "nat gateways" in result.output.lower()
    assert "eth0" in result.output


# ---------------------------------------------------------------------------
# network inspect — NAT section with iptables rules (covers lines 317-333)
# ---------------------------------------------------------------------------

_FAKE_INSPECT_NAT = {
    "name": "testnet",
    "subnet": "192.168.100.0/24",
    "ipv4_gateway": "192.168.100.1",
    "bridge": "mvm-testnet",
    "nat_enabled": True,
    "bridge_exists": True,
    "created_at": "2024-01-01T00:00:00+00:00",
    "vms": [],
}


@patch("mvmctl.cli.network.inspect_network", return_value=_FAKE_INSPECT_NAT)
@patch(
    "mvmctl.cli.network.get_iptables_rules_for_bridge",
    return_value=["-A POSTROUTING -o eth0 -j MASQUERADE"],
)
def test_inspect_nat_section_with_iptables_rule(mock_rules, mock_inspect):
    """NAT CONFIG section printed and interface extracted from iptables rule."""
    result = runner.invoke(app, ["inspect", "testnet"])
    assert result.exit_code == 0
    assert "NAT CONFIG" in result.output
    assert "eth0" in result.output


@patch("mvmctl.cli.network.inspect_network", return_value=_FAKE_INSPECT_NAT)
@patch("mvmctl.cli.network.get_iptables_rules_for_bridge", return_value=[])
def test_inspect_nat_section_no_iptables_rules(mock_rules, mock_inspect):
    """NAT CONFIG section printed even when no iptables rules found."""
    result = runner.invoke(app, ["inspect", "testnet"])
    assert result.exit_code == 0
    assert "NAT CONFIG" in result.output


# ---------------------------------------------------------------------------
# network inspect — VMs section (covers lines 336-339)
# ---------------------------------------------------------------------------

_FAKE_INSPECT_WITH_VMS = {
    "name": "testnet",
    "subnet": "192.168.100.0/24",
    "ipv4_gateway": "192.168.100.1",
    "bridge": "mvm-testnet",
    "nat_enabled": False,
    "bridge_exists": True,
    "created_at": "2024-01-01T00:00:00+00:00",
    "vms": [{"vm_id": "abc123", "ipv4": "192.168.100.2"}],
}


@patch("mvmctl.cli.network.inspect_network", return_value=_FAKE_INSPECT_WITH_VMS)
@patch("mvmctl.cli.network.get_iptables_rules_for_bridge", return_value=[])
def test_inspect_shows_vms_section(mock_rules, mock_inspect):
    """VMS section printed when vms list is non-empty."""
    result = runner.invoke(app, ["inspect", "testnet"])
    assert result.exit_code == 0
    assert "VMS" in result.output
    assert "abc123" in result.output


@patch("mvmctl.cli.network.set_default_network")
def test_set_default_network_success(mock_set):
    result = runner.invoke(app, ["set-default", "mynet"])
    assert result.exit_code == 0
    assert "mynet" in result.output
    mock_set.assert_called_once_with("mynet")
