"""Tests for CLI network commands."""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.exceptions import NetworkError
from mvmctl.main import app
from mvmctl.models import NetworkItem, NetworkLeaseItem

runner = CliRunner()


def _make_network(
    name: str = "testnet",
    subnet: str = "192.168.100.0/24",
    bridge: str = "mvm-testnet",
    is_default: bool = False,
    is_present: bool = True,
    **kwargs,
) -> NetworkItem:
    return NetworkItem(
        id=f"net-{name}-" + "x" * 55,
        name=name,
        subnet=subnet,
        bridge=bridge,
        ipv4_gateway="192.168.100.1",
        bridge_active=is_present,
        nat_enabled=True,
        is_default=is_default,
        is_present=is_present,
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
        leases=[],
        **kwargs,
    )


class TestNetworkLs:
    """Tests for 'network ls' command."""

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_ls_empty(self, mock_net_op):
        mock_net_op.list_all.return_value = []
        result = runner.invoke(app, ["network", "ls"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_ls_with_networks(self, mock_net_op):
        mock_net_op.list_all.return_value = [
            _make_network("testnet"),
            _make_network("default", is_default=True),
        ]
        result = runner.invoke(app, ["network", "ls"])
        assert result.exit_code == 0
        assert "testnet" in result.output
        assert "default" in result.output

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_ls_json(self, mock_net_op):
        mock_net_op.to_json.return_value = [
            {
                "name": "testnet",
                "vm_count": 0,
                "leases": [],
                "iptables_rules": [],
            }
        ]
        result = runner.invoke(app, ["network", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) == 1
        assert data[0]["name"] == "testnet"

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_ls_with_leases(self, mock_net_op):
        net = _make_network("testnet")
        net.leases = [
            NetworkLeaseItem(
                network_id=net.id,
                ipv4="192.168.100.2",
                leased_at="2026-01-01T12:00:00",
            ),
        ]
        mock_net_op.list_all.return_value = [net]
        result = runner.invoke(app, ["network", "ls"])
        assert result.exit_code == 0

    def test_ls_help(self):
        result = runner.invoke(app, ["network", "ls", "--help"])
        assert result.exit_code == 0


class TestNetworkCreate:
    """Tests for 'network create' command."""

    @patch("mvmctl.cli.network.NetworkOperation")
    @patch("mvmctl.cli.network.NetworkUtils.get_physical_interfaces")
    def test_create_success(self, mock_ifaces, mock_net_op):
        mock_ifaces.return_value = ["eth0"]
        from mvmctl.models.result import OperationResult

        mock_net_op.create.return_value = MagicMock(
            spec=OperationResult,
            status="success",
            item=_make_network("testnet"),
        )
        result = runner.invoke(
            app,
            [
                "network",
                "create",
                "testnet",
                "--subnet",
                "192.168.100.0/24",
            ],
        )
        assert result.exit_code == 0
        assert "created" in result.output.lower()

    @patch("mvmctl.cli.network.NetworkOperation")
    @patch("mvmctl.cli.network.NetworkUtils.get_physical_interfaces")
    def test_create_missing_subnet(self, mock_ifaces, mock_net_op):
        mock_ifaces.return_value = ["eth0"]
        result = runner.invoke(app, ["network", "create", "testnet"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_create_missing_name(self, mock_net_op):
        result = runner.invoke(app, ["network", "create"])
        assert result.exit_code == 1  # Shows help with error when no args

    def test_create_help(self):
        result = runner.invoke(app, ["network", "create", "--help"])
        assert result.exit_code == 0


class TestNetworkRemove:
    """Tests for 'network rm' command."""

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_rm_success(self, mock_net_op):
        mock_net_op.remove.return_value = MagicMock(status="success")
        result = runner.invoke(app, ["network", "rm", "testnet"])
        assert result.exit_code == 0
        assert "removed" in result.output.lower()

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_rm_multiple(self, mock_net_op):
        mock_net_op.remove.return_value = MagicMock(status="success")
        result = runner.invoke(app, ["network", "rm", "net1", "net2"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_rm_no_name(self, mock_net_op):
        result = runner.invoke(app, ["network", "rm"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_rm_api_error(self, mock_net_op):
        mock_net_op.remove.side_effect = NetworkError("not found")
        result = runner.invoke(app, ["network", "rm", "nonexistent"])
        assert result.exit_code == 1

    def test_rm_alias(self):
        """Verify 'network rm' exists (not just 'remove')."""
        result = runner.invoke(app, ["network", "rm", "--help"])
        assert result.exit_code == 0


class TestNetworkInspect:
    """Tests for 'network inspect' command."""

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_inspect_success(self, mock_net_op):
        mock_net_op.inspect.return_value = _make_network("testnet")
        result = runner.invoke(app, ["network", "inspect", "testnet"])
        assert result.exit_code == 0
        assert "testnet" in result.output
        assert "192.168.100.0/24" in result.output

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_inspect_json(self, mock_net_op):
        mock_net_op.inspect.return_value = {
            "name": "testnet",
            "subnet": "192.168.100.0/24",
        }
        result = runner.invoke(app, ["network", "inspect", "testnet", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["name"] == "testnet"

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_inspect_not_found(self, mock_net_op):
        mock_net_op.inspect.side_effect = NetworkError("not found")
        result = runner.invoke(app, ["network", "inspect", "nonexistent"])
        assert result.exit_code == 1

    def test_inspect_help(self):
        result = runner.invoke(app, ["network", "inspect", "--help"])
        assert result.exit_code == 0


class TestNetworkSetDefault:
    """Tests for 'network set-default' command."""

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_set_default_success(self, mock_net_op):
        mock_net_op.set_default.return_value = MagicMock(status="success")
        result = runner.invoke(app, ["network", "default", "mynet"])
        assert result.exit_code == 0
        assert "mynet" in result.output

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_set_default_no_args(self, mock_net_op):
        result = runner.invoke(app, ["network", "default"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_set_default_api_error(self, mock_net_op):
        mock_net_op.set_default.side_effect = NetworkError("no such network")
        result = runner.invoke(app, ["network", "default", "missing"])
        assert result.exit_code == 1


class TestNetworkSync:
    """Tests for 'network sync' command."""

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_sync_all(self, mock_net_op):
        test_id = "a" * 64
        mock_net_op.list_all.return_value = [
            NetworkItem(
                id=test_id,
                name="testnet",
                subnet="192.168.100.0/24",
                bridge="mvm-testnet",
                ipv4_gateway="192.168.100.1",
                bridge_active=True,
                nat_enabled=True,
                is_default=False,
                is_present=True,
                created_at="2026-01-01T12:00:00+00:00",
                updated_at="2026-01-01T12:00:00+00:00",
                leases=[],
            )
        ]
        mock_net_op.sync.return_value = MagicMock(
            status="success",
            item={test_id: {"verified": 5, "added": 2, "orphaned": 1}},
        )
        result = runner.invoke(app, ["network", "sync"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.network.NetworkOperation")
    def test_sync_json(self, mock_net_op):
        test_id = "b" * 64
        mock_net_op.list_all.return_value = [
            NetworkItem(
                id=test_id,
                name="testnet",
                subnet="192.168.100.0/24",
                bridge="mvm-testnet",
                ipv4_gateway="192.168.100.1",
                bridge_active=True,
                nat_enabled=True,
                is_default=False,
                is_present=True,
                created_at="2026-01-01T12:00:00+00:00",
                updated_at="2026-01-01T12:00:00+00:00",
                leases=[],
            )
        ]
        mock_net_op.sync.return_value = MagicMock(
            status="success",
            item={test_id: {"verified": 5, "added": 2, "orphaned": 1}},
        )
        result = runner.invoke(app, ["network", "sync", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert test_id in data


class TestNetworkHelp:
    """Tests for network command group help."""

    def test_network_help(self):
        result = runner.invoke(app, ["network", "--help"])
        assert result.exit_code == 0
        assert "Network management" in result.output

    def test_network_help_command(self):
        result = runner.invoke(app, ["network", "help"])
        assert result.exit_code == 0
