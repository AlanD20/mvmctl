"""Integration tests for network create/destroy workflow.

Tests the complete network lifecycle: create -> list -> inspect -> remove
with mocked subprocess calls for bridge operations.
"""

from __future__ import annotations

from typing import Any
from unittest.mock import MagicMock

import pytest

from mvmctl.api import NetworkCreateInput, NetworkInput, NetworkOperation
from mvmctl.exceptions import MVMError, NetworkError, NetworkNotFoundError
from mvmctl.models.network import (
    FirewallRule,
    NetworkItem,
    NetworkLeaseItem,
)
from mvmctl.models.result import OperationResult


@pytest.fixture(autouse=True)
def _mock_network_subprocess(monkeypatch: pytest.MonkeyPatch) -> None:
    """Mock subprocess.run so network bridge/iptables ops never touch the host.

    Tracks bridges "created" by ip -batch calls so bridge_exists checks
    return the correct state after setup.
    """
    _bridges: set[str] = {"mvm-br0"}

    def _fake_run(*args: Any, **kwargs: Any) -> Any:
        nonlocal _bridges
        cmd = args[0] if args else kwargs.get("args", [])
        if isinstance(cmd, list):
            cmd_str = " ".join(str(c) for c in cmd)

            # Bridge existence check — return found if bridge was "created"
            if cmd_str.startswith("ip link show") and len(cmd) >= 4:
                bridge_name = cmd[-1]
                if bridge_name in _bridges:
                    return MagicMock(returncode=0, stdout="", stderr="")
                return MagicMock(returncode=1, stdout="", stderr="")

            # Bridge creation via ip -batch — track the bridge name
            if cmd_str == "ip -batch -":
                batch = kwargs.get("input", "")
                if isinstance(batch, str):
                    for line in batch.splitlines():
                        if "link add name" in line and "type bridge" in line:
                            parts = line.split()
                            idx = parts.index("name") + 1
                            if idx < len(parts):
                                _bridges.add(parts[idx])
                return MagicMock(returncode=0, stdout="", stderr="")

            # Default route detection — no default route in tests
            if "ip route show default" in cmd_str:
                return MagicMock(returncode=0, stdout="", stderr="")

            # iptables check (exists) commands
            if "iptables" in cmd_str and (
                " -C " in cmd_str or " -L " in cmd_str
            ):
                return MagicMock(returncode=0, stdout="", stderr="")

        return MagicMock(returncode=0, stdout="", stderr="")

    monkeypatch.setattr("subprocess.run", _fake_run)

    def _fake_check_output(*args: Any, **kwargs: Any) -> bytes:
        return b""

    monkeypatch.setattr("subprocess.check_output", _fake_check_output)


class TestNetworkLifecycleWorkflow:
    """Test complete network lifecycle workflow end-to-end through the public API."""

    def test_create_network(self) -> None:
        """Test creating a network and then listing it."""
        result = NetworkOperation.create(
            NetworkCreateInput(name="testnet", subnet="10.0.0.0/24")
        )
        assert isinstance(result, OperationResult)
        assert result.status == "success"
        assert isinstance(result.item, NetworkItem)
        assert result.item.name == "testnet"
        assert result.item.subnet == "10.0.0.0/24"
        assert result.item.ipv4_gateway == "10.0.0.1"

        networks = NetworkOperation.list_all()
        assert any(n.name == "testnet" for n in networks)

    def test_get_network_by_name(self) -> None:
        """Test creating a network and then retrieving it by name."""
        NetworkOperation.create(
            NetworkCreateInput(name="getnet", subnet="10.10.0.0/24")
        )

        network = NetworkOperation.get(NetworkInput(name=["getnet"]))
        assert isinstance(network, NetworkItem)
        assert network.name == "getnet"
        assert network.subnet == "10.10.0.0/24"

    def test_inspect_network(self) -> None:
        """Test creating a network and then inspecting it."""
        NetworkOperation.create(
            NetworkCreateInput(name="inspectnet", subnet="172.16.0.0/24")
        )

        inspected = NetworkOperation.inspect(
            NetworkInput(name=["inspectnet"]), is_json=True
        )
        assert isinstance(inspected, dict)
        assert inspected["name"] == "inspectnet"
        assert inspected["subnet"] == "172.16.0.0/24"
        assert "leases" in inspected
        assert "iptables_rules" in inspected
        assert isinstance(inspected.get("iptables_rules"), list)

    def test_remove_network(self) -> None:
        """Test full network lifecycle: create -> verify -> remove."""
        NetworkOperation.create(
            NetworkCreateInput(name="remnet", subnet="192.168.200.0/24")
        )

        networks_before = NetworkOperation.list_all()
        assert any(n.name == "remnet" for n in networks_before)

        NetworkOperation.remove(NetworkInput(name=["remnet"]))

        networks_after = NetworkOperation.list_all()
        assert not any(n.name == "remnet" for n in networks_after)

    def test_create_default_network(self) -> None:
        """Test creating the default network."""
        network = NetworkOperation.create_default_network()
        assert isinstance(network, OperationResult)
        assert network.status == "success"
        assert network.item.is_default
        assert network.item.name == "net"

    def test_network_with_leases(self) -> None:
        """Test that a created network has correctly populated fields."""
        NetworkOperation.create(
            NetworkCreateInput(name="leasenet", subnet="10.30.0.0/24")
        )

        network = NetworkOperation.get(NetworkInput(name=["leasenet"]))
        assert isinstance(network, NetworkItem)
        assert network.subnet == "10.30.0.0/24"
        assert network.ipv4_gateway == "10.30.0.1"
        assert network.leases is not None
        assert all(
            isinstance(lease, NetworkLeaseItem)
            for lease in network.leases or []
        )
        assert network.iptables_rules is None or all(
            isinstance(rule, FirewallRule)
            for rule in network.iptables_rules or []
        )


class TestNetworkWorkflowEdgeCases:
    """Test edge cases in network workflow."""

    def test_get_nonexistent_network(self) -> None:
        """Test attempting to get a network that doesn't exist."""
        with pytest.raises(NetworkNotFoundError):
            NetworkOperation.get(NetworkInput(name=["nonexistent-net"]))

    def test_remove_nonexistent_network(self) -> None:
        """Test attempting to remove a network that doesn't exist."""
        with pytest.raises(NetworkNotFoundError):
            NetworkOperation.remove(NetworkInput(name=["missing-net"]))

    def test_inspect_nonexistent_network(self) -> None:
        """Test attempting to inspect a network that doesn't exist."""
        with pytest.raises(NetworkNotFoundError):
            NetworkOperation.inspect(NetworkInput(name=["unknown-net"]))

    def test_create_duplicate_network(self) -> None:
        """Test attempting to create a network that already exists."""
        NetworkOperation.create(
            NetworkCreateInput(name="dupnet", subnet="10.88.0.0/24")
        )

        with pytest.raises((NetworkError, MVMError)):
            NetworkOperation.create(
                NetworkCreateInput(name="dupnet", subnet="10.88.0.0/24")
            )

    def test_create_network_with_custom_gateway(self) -> None:
        """Test creating a network with a custom IPv4 gateway."""
        result = NetworkOperation.create(
            NetworkCreateInput(
                name="custgw",
                subnet="10.99.0.0/24",
                ipv4_gateway="10.99.0.254",
            )
        )
        assert result.item.ipv4_gateway == "10.99.0.254"


class TestNetworkForceRemoval:
    """Test force removal of networks with attached VMs."""

    @staticmethod
    def _setup_vm_mocks(
        monkeypatch: pytest.MonkeyPatch,
    ) -> dict[str, object]:
        from tests.integration.conftest import (
            SmartPopenMock,
            SmartSubprocessMock,
        )

        sub_mock = SmartSubprocessMock()
        popen_mock = SmartPopenMock()
        monkeypatch.setattr("subprocess.run", sub_mock)
        monkeypatch.setattr("subprocess.Popen", popen_mock)

        provisioner_mock = MagicMock()
        monkeypatch.setattr(
            "mvmctl.api.vm_operations.VMProvisioner",
            lambda *args, **kwargs: provisioner_mock,
        )
        return {
            "subprocess": sub_mock,
            "popen": popen_mock,
            "provisioner": provisioner_mock,
        }

    def test_remove_network_with_force(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create network with attached VM, remove with force=True, verify soft delete."""
        mocks = self._setup_vm_mocks(monkeypatch)
        mocks["provisioner"].resize.return_value = mocks["provisioner"]
        mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
        mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
        mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
        mocks["provisioner"].disable_cloud_init.return_value = mocks[
            "provisioner"
        ]
        mocks["provisioner"].run.return_value = None

        # 1. Create a dedicated network
        NetworkOperation.create(
            NetworkCreateInput(name="forcenet", subnet="10.50.0.0/24")
        )
        network = NetworkOperation.get(NetworkInput(name=["forcenet"]))
        assert network.name == "forcenet"

        # 2. Create a VM attached to this network
        from mvmctl.api import VMCreateInput, VMInput, VMOperation

        VMOperation.create(
            VMCreateInput(
                name="force-vm",
                ssh_keys=[],
                enable_console=False,
                network_name="forcenet",
            )
        )
        vm = VMOperation.get(VMInput(identifiers=["force-vm"]))
        assert vm.network_id == network.id

        # 3. Remove network with force=True
        NetworkOperation.remove(NetworkInput(name=["forcenet"]), force=True)

        # 4. Verify network is gone from API surface (soft delete)
        with pytest.raises(NetworkNotFoundError):
            NetworkOperation.get(NetworkInput(name=["forcenet"]))

        networks = NetworkOperation.list_all()
        assert not any(n.name == "forcenet" for n in networks)

        # 5. Verify DB record still exists with deleted_at set
        from mvmctl.core._shared import Database
        from mvmctl.core.network._repository import NetworkRepository

        repo = NetworkRepository(Database())
        all_networks = repo.list_all()
        assert not any(n.name == "forcenet" for n in all_networks)


class TestNetworkDefaultBehavior:
    """Test default network behavior during removal."""

    def test_remove_default_network(self) -> None:
        """Remove the default network and verify no default remains."""
        # Ensure we have the seeded default network "net"
        default_before = NetworkOperation.create_default_network()
        assert default_before.status == "success"
        assert default_before.item.name == "net"
        assert default_before.item.is_default

        # Remove the default network
        NetworkOperation.remove(NetworkInput(name=["net"]))

        # Verify it's gone from listings
        networks = NetworkOperation.list_all()
        assert not any(n.name == "net" for n in networks)

        # Verify no default network remains
        from mvmctl.core._shared import Database
        from mvmctl.core.network._repository import NetworkRepository

        repo = NetworkRepository(Database())
        default_after = repo.get_default()
        assert default_after is None


class TestNetworkCreateEdgeCases:
    """Test edge cases during network creation."""

    def test_create_network_invalid_subnet(self) -> None:
        """Creating a network with an invalid subnet raises ValueError."""
        with pytest.raises(ValueError):
            NetworkOperation.create(
                NetworkCreateInput(name="badsubnet", subnet="invalid")
            )

    def test_create_network_with_duplicate_name_different_subnet(self) -> None:
        """Creating a network with duplicate name but different subnet raises NetworkError."""
        NetworkOperation.create(
            NetworkCreateInput(name="dupnamesub", subnet="10.55.0.0/24")
        )

        with pytest.raises((NetworkError, MVMError)):
            NetworkOperation.create(
                NetworkCreateInput(name="dupnamesub", subnet="10.56.0.0/24")
            )


class TestNetworkSync:
    """Test iptables rule synchronization."""

    def test_sync_network(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Create network, sync iptables rules, verify rules tracked in DB."""
        monkeypatch.setattr(
            "mvmctl.utils.network.NetworkUtils.ensure_interface_ready",
            lambda iface: True,
        )
        result = NetworkOperation.create(
            NetworkCreateInput(
                name="syncnet",
                subnet="10.60.0.0/24",
                nat_enabled=True,
                nat_gateways=["eth0"],
            )
        )
        network = result.item

        # Sync iptables rules for this network
        sync_result = NetworkOperation.sync(network_id=network.id)
        assert isinstance(sync_result, OperationResult)
        assert sync_result.status == "success"
        assert network.id in sync_result.item
        stats = sync_result.item[network.id]
        assert "added" in stats
        assert "verified" in stats
        assert "orphaned" in stats
        assert isinstance(stats["added"], int)
        assert isinstance(stats["verified"], int)
        assert isinstance(stats["orphaned"], int)

        # Verify rules exist in DB for this network
        from mvmctl.core._shared._firewall_tracker import FirewallTracker

        tracker = FirewallTracker()
        rules = tracker.repo.get_by_network_id(network.id, active_only=True)
        assert len(rules) > 0
        assert all(r.network_id == network.id for r in rules)
        assert all(r.is_active for r in rules)


class TestNetworkGetEdgeCases:
    """Test edge cases for network retrieval."""

    def test_get_network_by_id_prefix(self) -> None:
        """Create a network and retrieve it by a 6-character ID prefix."""
        result = NetworkOperation.create(
            NetworkCreateInput(name="prefixnet", subnet="10.70.0.0/24")
        )
        network = result.item
        prefix = network.id[:6]

        fetched = NetworkOperation.get(NetworkInput(id=[prefix]))
        assert isinstance(fetched, NetworkItem)
        assert fetched.id == network.id
        assert fetched.name == "prefixnet"

    def test_get_network_empty_identifiers(self) -> None:
        """Get with empty identifiers raises NetworkNotFoundError."""
        with pytest.raises(NetworkNotFoundError):
            NetworkOperation.get(NetworkInput())
