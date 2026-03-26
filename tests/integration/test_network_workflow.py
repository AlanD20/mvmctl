"""Integration tests for network create/destroy workflow.

Tests the complete network lifecycle: create -> list -> inspect -> remove
with mocked subprocess calls for bridge operations.
"""

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from mvmctl.cli.network import app as network_app
from mvmctl.core.network_manager import NetworkConfig
from mvmctl.exceptions import NetworkError

runner = CliRunner()


def _make_network(name: str = "testnet", cidr: str = "192.168.100.0/24") -> NetworkConfig:
    """Create a sample NetworkConfig for testing."""
    return NetworkConfig(
        name=name,
        cidr=cidr,
        gateway="192.168.100.1",
        bridge=f"mvm-{name}",
        nat_enabled=True,
        created_at="2024-01-01T00:00:00+00:00",
    )


class TestNetworkLifecycleWorkflow:
    """Test complete network lifecycle workflow end-to-end."""

    @patch("mvmctl.api.network.check_privileges")
    @patch("mvmctl.cli.network.create_network")
    @patch("mvmctl.cli.network.list_networks")
    def test_create_and_list_network(self, mock_list, mock_create, mock_check_priv):
        """Test creating a network and then listing it."""
        mock_check_priv.return_value = None

        network = _make_network("integration-net", "10.50.0.0/24")
        mock_create.return_value = network
        mock_list.return_value = [network]

        result = runner.invoke(
            network_app,
            ["create", "integration-net", "--cidr", "10.50.0.0/24"],
        )
        assert result.exit_code == 0
        assert "created" in result.output.lower()
        mock_create.assert_called_once()

        result = runner.invoke(network_app, ["ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) == 1
        assert data[0]["name"] == "integration-net"

    @patch("mvmctl.api.network.check_privileges")
    @patch("mvmctl.cli.network.create_network")
    @patch("mvmctl.cli.network.inspect_network")
    @patch("mvmctl.cli.network.get_iptables_rules_for_bridge")
    def test_create_and_inspect_network(
        self, mock_rules, mock_inspect, mock_create, mock_check_priv
    ):
        """Test creating a network and then inspecting it."""
        mock_check_priv.return_value = None

        network = _make_network("inspect-net", "172.16.0.0/24")
        mock_create.return_value = network
        mock_inspect.return_value = {
            "name": "inspect-net",
            "cidr": "172.16.0.0/24",
            "gateway": "172.16.0.1",
            "bridge": "mvm-inspect-net",
            "nat_enabled": True,
            "bridge_exists": True,
            "created_at": "2024-01-01T00:00:00+00:00",
            "vms": [],
        }
        mock_rules.return_value = []

        result = runner.invoke(
            network_app,
            ["create", "inspect-net", "--cidr", "172.16.0.0/24"],
        )
        assert result.exit_code == 0

        result = runner.invoke(network_app, ["inspect", "inspect-net"])
        assert result.exit_code == 0
        assert "inspect-net" in result.output
        mock_inspect.assert_called_once_with("inspect-net")

    @patch("mvmctl.api.network.check_privileges")
    @patch("mvmctl.cli.network.create_network")
    @patch("mvmctl.cli.network.remove_network")
    @patch("mvmctl.cli.network.list_networks")
    def test_full_network_lifecycle(self, mock_list, mock_remove, mock_create, mock_check_priv):
        """Test full network lifecycle: create -> verify -> remove."""
        mock_check_priv.return_value = None

        network = _make_network("lifecycle-net", "192.168.200.0/24")
        mock_create.return_value = network
        mock_list.return_value = [network]
        mock_remove.return_value = None

        result = runner.invoke(
            network_app,
            ["create", "lifecycle-net", "--cidr", "192.168.200.0/24"],
        )
        assert result.exit_code == 0
        assert "lifecycle-net" in result.output

        result = runner.invoke(network_app, ["ls"])
        assert result.exit_code == 0
        assert "lifecycle-net" in result.output

        mock_list.return_value = []
        result = runner.invoke(network_app, ["remove", "lifecycle-net", "--force"])
        assert result.exit_code == 0
        assert "removed" in result.output.lower()
        mock_remove.assert_called_once_with("lifecycle-net")

    @patch("mvmctl.api.network.check_privileges")
    @patch("mvmctl.cli.network.create_network")
    def test_create_network_without_nat(self, mock_create, mock_check_priv):
        """Test creating a network without NAT."""
        mock_check_priv.return_value = None

        network = _make_network("no-nat-net", "10.100.0.0/24")
        network.nat_enabled = False
        mock_create.return_value = network

        result = runner.invoke(
            network_app,
            ["create", "no-nat-net", "--cidr", "10.100.0.0/24", "--no-nat"],
        )
        assert result.exit_code == 0

        call_kwargs = mock_create.call_args.kwargs
        assert call_kwargs.get("nat") is False

    @patch("mvmctl.api.network.check_privileges")
    @patch("mvmctl.cli.network.create_network")
    def test_create_network_with_custom_gateway(self, mock_create, mock_check_priv):
        """Test creating a network with a custom gateway."""
        mock_check_priv.return_value = None

        network = _make_network("custom-gw-net", "10.99.0.0/24")
        network.gateway = "10.99.0.254"
        mock_create.return_value = network

        result = runner.invoke(
            network_app,
            [
                "create",
                "custom-gw-net",
                "--cidr",
                "10.99.0.0/24",
                "--gateway",
                "10.99.0.254",
            ],
        )
        assert result.exit_code == 0

        call_kwargs = mock_create.call_args.kwargs
        assert call_kwargs.get("gateway") == "10.99.0.254"


class TestNetworkWorkflowEdgeCases:
    """Test edge cases in network workflow."""

    @patch("mvmctl.api.network.check_privileges")
    @patch("mvmctl.cli.network.create_network")
    def test_create_duplicate_network(self, mock_create, mock_check_priv):
        """Test attempting to create a network that already exists."""
        mock_check_priv.return_value = None
        mock_create.side_effect = NetworkError("Network 'duplicate-net' already exists")

        result = runner.invoke(
            network_app,
            ["create", "duplicate-net", "--cidr", "10.88.0.0/24"],
        )
        assert result.exit_code == 1
        assert "already exists" in result.output.lower()

    @patch("mvmctl.api.network.check_privileges")
    @patch("mvmctl.cli.network.remove_network")
    def test_remove_nonexistent_network(self, mock_remove, mock_check_priv):
        """Test attempting to remove a network that doesn't exist."""
        mock_check_priv.return_value = None
        mock_remove.side_effect = NetworkError("Network 'missing-net' not found")

        result = runner.invoke(network_app, ["remove", "missing-net", "--force"])
        assert result.exit_code == 1
        assert "not found" in result.output.lower()

    @patch("mvmctl.api.network.check_privileges")
    @patch("mvmctl.cli.network.inspect_network")
    @patch("mvmctl.cli.network.get_iptables_rules_for_bridge")
    def test_inspect_nonexistent_network(self, mock_rules, mock_inspect, mock_check_priv):
        """Test attempting to inspect a network that doesn't exist."""
        mock_check_priv.return_value = None
        mock_inspect.side_effect = NetworkError("Network 'unknown-net' not found")
        mock_rules.return_value = []

        result = runner.invoke(network_app, ["inspect", "unknown-net"])
        assert result.exit_code == 1
        assert "not found" in result.output.lower()

    def test_create_network_missing_cidr(self):
        """Test that creating a network without CIDR fails."""
        result = runner.invoke(network_app, ["create", "invalid-net"])
        assert result.exit_code != 0

    @patch("mvmctl.api.network.check_privileges")
    @patch("mvmctl.cli.network.create_network")
    def test_create_network_with_invalid_cidr(self, mock_create, mock_check_priv):
        """Test creating a network with an invalid CIDR."""
        mock_check_priv.return_value = None
        mock_create.side_effect = NetworkError("Invalid CIDR format: not-a-cidr")

        result = runner.invoke(
            network_app,
            ["create", "invalid-cidr", "--cidr", "not-a-cidr"],
        )
        assert result.exit_code == 1


class TestNetworkWithSubprocessMocking:
    """Test network workflows with mocked subprocess calls."""

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.core.network.subprocess.run")
    @patch("mvmctl.api.network.check_privileges")
    def test_network_create_with_bridge_setup(
        self, mock_check_priv, mock_run, mock_require_group, mock_cache_dir
    ):
        """Test network creation with mocked bridge setup commands."""
        from mvmctl.core.network_manager import create_network, get_network

        mock_check_priv.return_value = None
        mock_require_group.return_value = None
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

        with patch("mvmctl.core.network_manager._validate_subnet_no_overlap"):
            with patch("mvmctl.core.network_manager.setup_nat"):
                result = create_network("subprocess-net", cidr="10.77.0.0/24")

        assert result.name == "subprocess-net"
        assert result.cidr == "10.77.0.0/24"
        # Verify persisted in metadata
        assert get_network("subprocess-net") is not None

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.core.network.subprocess.run")
    @patch("mvmctl.api.network.check_privileges")
    def test_network_remove_with_bridge_teardown(
        self, mock_check_priv, mock_run, mock_require_group, mock_cache_dir
    ):
        """Test network removal with mocked bridge teardown commands."""
        from mvmctl.core.metadata import update_network_entry
        from mvmctl.core.network_manager import get_network, remove_network

        mock_check_priv.return_value = None
        mock_require_group.return_value = None
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

        # Add network to metadata first
        update_network_entry(
            mock_cache_dir,
            "teardown-net",
            cidr="10.66.0.0/24",
            gateway="10.66.0.1",
            bridge="mvm-teardown-n",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00+00:00",
            leases=[],
            bridge_active=True,
        )

        remove_network("teardown-net")

        mock_run.assert_called()
        # Verify removed from metadata
        assert get_network("teardown-net") is None
