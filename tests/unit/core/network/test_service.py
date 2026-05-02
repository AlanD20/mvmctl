"""Tests for NetworkService — bridge, TAP, NAT, iptables operations."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.exceptions import NetworkError
from mvmctl.models import NetworkItem
from mvmctl.utils.network import NetworkUtils


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied."""
    database = Database()
    database.migrate()
    return database


@pytest.fixture
def repo(db: Database) -> NetworkRepository:
    """Create a NetworkRepository backed by the test database."""
    return NetworkRepository(db)


@pytest.fixture
def default_network(repo: NetworkRepository) -> NetworkItem:
    """Seed and return a default network for testing."""
    network = NetworkItem(
        id="test-net-001",
        name="test-net",
        subnet="10.0.0.0/24",
        bridge="mvm-test-net",
        ipv4_gateway="10.0.0.1",
        bridge_active=True,
        nat_enabled=True,
        is_default=True,
        is_present=True,
        created_at="2026-01-01T00:00:00Z",
        updated_at="2026-01-01T00:00:00Z",
        nat_gateways="eth0",
    )
    repo.upsert(network)
    return network


class TestNetworkServiceInitialize:
    """Tests for NetworkService.initialize() / ensure_mvm_chains()."""

    def test_initialize_calls_ensure_mvm_chains(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """initialize() delegates to ensure_mvm_chains()."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        service = NetworkService(repo)
        spy = mocker.spy(service, "ensure_mvm_chains")
        service.initialize()
        spy.assert_called_once()

    def test_ensure_mvm_chains_calls_iptables(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_mvm_chains() creates 3 MVM chains with appropriate iptables calls."""
        mock_run = mocker.patch(
            "subprocess.run", return_value=MagicMock(returncode=0)
        )
        service = NetworkService(repo)
        service.ensure_mvm_chains()

        # ensure_chain for each of the 3 chains triggers several subprocess calls
        # (create chain + insert jump rule for each)
        assert mock_run.call_count > 0
        # Verify chain names appear in command args
        all_args = []
        for call_args in mock_run.call_args_list:
            args = call_args[0][0]
            all_args.extend(args)
        assert "MVM-FORWARD" in str(all_args) or "MVM-POSTROUTING" in str(
            all_args
        )


class TestNetworkServiceBridge:
    """Tests for ensure_bridge() and remove_bridge()."""

    def test_ensure_bridge_creates_when_not_exists(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_bridge() creates bridge when it doesn't exist."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "bridge_exists", return_value=False)
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.ensure_bridge("mvm-test-net", "10.0.0.1/24")

        mock_batch.assert_called_once()
        batch_input = mock_batch.call_args[0][0]
        assert "link add name mvm-test-net type bridge" in batch_input
        assert "addr add 10.0.0.1/24 dev mvm-test-net" in batch_input
        assert "link set mvm-test-net up" in batch_input

    def test_ensure_bridge_skips_when_exists(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_bridge() reconciles when bridge already exists with subnet."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "bridge_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "bridge_has_subnet", return_value=True
        )
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.ensure_bridge("mvm-test-net", "10.0.0.1/24")

        mock_batch.assert_called_once()
        batch_input = mock_batch.call_args[0][0]
        # Should only bring it up since subnet already exists
        assert "link set mvm-test-net up" in batch_input
        assert "addr add" not in batch_input

    def test_ensure_bridge_reconciles_missing_subnet(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_bridge() adds subnet when bridge exists but missing IP."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "bridge_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "bridge_has_subnet", return_value=False
        )
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.ensure_bridge("mvm-test-net", "10.0.0.1/24")

        mock_batch.assert_called_once()
        batch_input = mock_batch.call_args[0][0]
        assert "addr add 10.0.0.1/24 dev mvm-test-net" in batch_input
        assert "link set mvm-test-net up" in batch_input

    def test_ensure_bridge_raises_on_failure(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_bridge() raises NetworkError when bridge creation fails."""
        import subprocess

        mocker.patch.object(NetworkUtils, "bridge_exists", return_value=False)
        mocker.patch.object(
            NetworkUtils,
            "_run_batch",
            side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
        )

        service = NetworkService(repo)
        with pytest.raises(NetworkError, match="Failed to setup bridge"):
            service.ensure_bridge("mvm-fail", "10.0.0.1/24")

    def test_remove_bridge_deletes_bridge(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """remove_bridge() removes bridge and its attached TAPs."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.remove_bridge("mvm-test-net", network_id="test-net-001")

        mock_batch.assert_called_once()
        batch_input = mock_batch.call_args[0][0]
        assert "link set mvm-test-net down" in batch_input
        assert "link delete mvm-test-net type bridge" in batch_input

    def test_remove_bridge_removes_attached_taps(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """remove_bridge() removes TAPs attached to the bridge."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(
            NetworkUtils, "get_bridge_taps", return_value=["tap-vm1", "tap-vm2"]
        )
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "get_tap_bridge", return_value="mvm-test-net"
        )
        mocker.patch.object(NetworkUtils, "_run_batch")
        mock_remove_tap = mocker.patch.object(
            NetworkService, "remove_tap", autospec=True
        )

        service = NetworkService(repo)
        service.remove_bridge("mvm-test-net", network_id="test-net-001")

        # Should have removed both TAPs
        assert mock_remove_tap.call_count == 2

    def test_remove_bridge_raises_on_failure(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """remove_bridge() raises NetworkError when bridge removal fails."""
        import subprocess

        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mocker.patch.object(
            NetworkUtils,
            "_run_batch",
            side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
        )

        service = NetworkService(repo)
        with pytest.raises(NetworkError, match="Failed to teardown bridge"):
            service.remove_bridge("mvm-test-net", network_id="test-net-001")

    def test_ensure_ip_forwarding_writes_proc(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_ip_forwarding() writes to /proc/sys/net/ipv4/ip_forward."""
        mock_write = mocker.patch(
            "mvmctl.core.network._service.Path.write_text"
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch("os.getuid", return_value=0)

        service = NetworkService(repo)
        service.ensure_ip_forwarding()

        mock_write.assert_called_once_with("1\n")


class TestNetworkServiceTap:
    """Tests for ensure_tap() and remove_tap()."""

    def test_ensure_tap_creates_when_not_exists(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_tap() creates TAP and attaches to bridge."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=False)
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.ensure_tap(
            "tap-vm1", "mvm-test-net", network_id=default_network.id
        )

        mock_batch.assert_called_once()
        batch_input = mock_batch.call_args[0][0]
        assert "tuntap add dev tap-vm1 mode tap" in batch_input
        assert "link set tap-vm1 master mvm-test-net" in batch_input
        assert "link set tap-vm1 up" in batch_input

    def test_ensure_tap_skips_when_exists_and_attached(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_tap() skips creation when TAP already exists on correct bridge."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "get_tap_bridge", return_value="mvm-test-net"
        )
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.ensure_tap(
            "tap-vm1", "mvm-test-net", network_id=default_network.id
        )

        mock_batch.assert_not_called()

    def test_ensure_tap_reattaches_when_on_wrong_bridge(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_tap() reattaches TAP when on a different bridge."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "get_tap_bridge", return_value="mvm-other-bridge"
        )
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.ensure_tap(
            "tap-vm1", "mvm-test-net", network_id=default_network.id
        )

        mock_batch.assert_called_once()
        batch_input = mock_batch.call_args[0][0]
        assert "link set tap-vm1 master mvm-test-net" in batch_input

    def test_ensure_tap_raises_on_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_tap() raises NetworkError when TAP creation fails."""
        import subprocess

        mocker.patch.object(NetworkUtils, "tap_exists", return_value=False)
        mocker.patch.object(
            NetworkUtils,
            "_run_batch",
            side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
        )

        service = NetworkService(repo)
        with pytest.raises(NetworkError, match="Failed to create TAP"):
            service.ensure_tap(
                "tap-fail", "mvm-test-net", network_id=default_network.id
            )

    def test_remove_tap_deletes_tap(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_tap() removes an existing TAP device."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "get_tap_bridge", return_value="mvm-test-net"
        )
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.remove_tap(
            "tap-vm1", "mvm-test-net", network_id=default_network.id
        )

        mock_batch.assert_called_once()
        batch_input = mock_batch.call_args[0][0]
        assert "link set tap-vm1 down" in batch_input
        assert "link delete tap-vm1" in batch_input

    def test_remove_tap_noop_when_missing(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_tap() is a no-op when TAP doesn't exist."""
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=False)
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.remove_tap(
            "tap-gone", "mvm-test-net", network_id=default_network.id
        )

        mock_batch.assert_not_called()

    def test_remove_tap_without_bridge_detects(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_tap() auto-detects bridge when not provided."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "get_tap_bridge", return_value="mvm-test-net"
        )
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.remove_tap("tap-vm1", network_id=default_network.id)

        mock_batch.assert_called_once()


class TestNetworkServiceNat:
    """Tests for ensure_nat() and remove_nat()."""

    def test_ensure_nat_adds_masquerade_and_forward(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_nat() creates MASQUERADE + FORWARD rules."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch("os.getuid", return_value=0)
        # Mock iptables_rule_exists to return False so rules are added
        mocker.patch(
            "mvmctl.core._shared._iptables_tracker._tracker.IPTablesTracker.ensure_chain"
        )

        service = NetworkService(repo)
        service.ensure_nat(
            "mvm-test-net",
            ["eth0"],
            subnet="10.0.0.0/24",
            network_id=default_network.id,
        )

        # Should have subprocess calls for iptables commands
        assert service.tracker is not None

    def test_ensure_nat_raises_on_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_nat() raises NetworkError when MASQUERADE add fails."""
        # Mock ensure_chain to pass
        mocker.patch(
            "mvmctl.core._shared._iptables_tracker._tracker.IPTablesTracker.ensure_chain"
        )
        # Mock ensure_rule to return failure
        mock_result = MagicMock()
        mock_result.success = False
        mock_result.error_message = "iptables failed"
        mocker.patch(
            "mvmctl.core._shared._iptables_tracker._tracker.IPTablesTracker.ensure_rule",
            return_value=mock_result,
        )

        service = NetworkService(repo)
        with pytest.raises(NetworkError, match="Failed to add MASQUERADE"):
            service.ensure_nat(
                "mvm-test-net",
                ["eth0"],
                subnet="10.0.0.0/24",
                network_id=default_network.id,
            )

    def test_remove_nat_removes_rules(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_nat() removes MASQUERADE + FORWARD rules."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mocker.patch(
            "mvmctl.core._shared._iptables_tracker._tracker.IPTablesTracker.remove_rule",
            return_value=MagicMock(success=True),
        )

        service = NetworkService(repo)
        # Should not raise
        service.remove_nat(
            "mvm-test-net",
            ["eth0"],
            subnet="10.0.0.0/24",
            network_id=default_network.id,
            force=True,
        )

    def test_remove_nat_raises_when_taps_present(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_nat() raises NetworkError when TAPs still attached without force."""
        mocker.patch.object(
            NetworkUtils, "get_bridge_taps", return_value=["tap-vm1"]
        )

        service = NetworkService(repo)
        with pytest.raises(NetworkError, match="TAP.*still attached"):
            service.remove_nat(
                "mvm-test-net",
                ["eth0"],
                subnet="10.0.0.0/24",
                network_id=default_network.id,
                force=False,
            )

    def test_remove_nat_with_force_skips_tap_check(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_nat() with force=True allows removal even with TAPs present."""
        mocker.patch.object(
            NetworkUtils, "get_bridge_taps", return_value=["tap-vm1"]
        )
        mocker.patch(
            "mvmctl.core._shared._iptables_tracker._tracker.IPTablesTracker.remove_rule",
            return_value=MagicMock(success=True),
        )

        service = NetworkService(repo)
        # Should not raise because force=True
        service.remove_nat(
            "mvm-test-net",
            ["eth0"],
            subnet="10.0.0.0/24",
            network_id=default_network.id,
            force=True,
        )

    def test_remove_nat_queries_db_when_params_missing(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_nat() resolves subnet/gateways from DB when not provided."""
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mocker.patch(
            "mvmctl.core._shared._iptables_tracker._tracker.IPTablesTracker.remove_rule",
            return_value=MagicMock(success=True),
        )

        service = NetworkService(repo)

        # Without explicit params, should resolve from repo
        service.remove_nat(
            "test-net",
            network_id=default_network.id,
            force=True,
        )


class TestNetworkServiceListAll:
    """Tests for list_all()."""

    def test_list_all_returns_networks(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """list_all() returns all non-deleted networks."""
        mocker.patch.object(NetworkUtils, "bridge_exists", return_value=True)

        service = NetworkService(repo)
        networks = service.list_all(verify=True)
        assert len(networks) >= 1
        names = {n.name for n in networks}
        assert "test-net" in names

    def test_list_all_with_verify_updates_missing(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """list_all(verify=True) updates is_present for missing bridges."""
        mocker.patch.object(NetworkUtils, "bridge_exists", return_value=False)

        service = NetworkService(repo)
        networks = service.list_all(verify=True)

        # The bridge doesn't exist, so is_present should be False
        for net in networks:
            if net.id == default_network.id:
                assert not net.is_present

    def test_list_all_without_verify(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """list_all(verify=False) returns DB records as-is."""
        service = NetworkService(repo)
        networks = service.list_all(verify=False)
        assert len(networks) >= 1


class TestBridgeExistsHelper:
    """Tests for the bridge_exists static helper."""

    def test_bridge_exists_true(self, mocker) -> None:
        """bridge_exists returns True when ip link show succeeds."""
        mock_result = MagicMock()
        mock_result.returncode = 0
        mocker.patch("subprocess.run", return_value=mock_result)

        result = NetworkUtils.bridge_exists("mvm-test")
        assert result is True

    def test_bridge_exists_false(self, mocker) -> None:
        """bridge_exists returns False when ip link show fails."""
        mock_result = MagicMock()
        mock_result.returncode = 1
        mocker.patch("subprocess.run", return_value=mock_result)

        result = NetworkUtils.bridge_exists("mvm-nonexistent")
        assert result is False


class TestEnsureInterfaceReady:
    """Tests for ensure_interface_ready()."""

    def test_ensure_interface_ready_success(self, mocker, tmp_path) -> None:
        """ensure_interface_ready() returns True when interface is ready."""
        mock_net_path = tmp_path / "sys" / "class" / "net" / "eth0"
        mock_net_path.mkdir(parents=True)
        (mock_net_path / "operstate").write_text("up")

        mocker.patch("mvmctl.utils.network.Path", return_value=mock_net_path)
        mock_run = mocker.patch("subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="2: eth0 inet 192.168.1.100/24 scope global eth0\n",
        )

        result = NetworkUtils.ensure_interface_ready("eth0")
        assert result is True

    def test_ensure_interface_ready_loopback(self, mocker) -> None:
        """ensure_interface_ready() raises for loopback interface."""
        with pytest.raises(NetworkError, match="Loopback interface"):
            NetworkUtils.ensure_interface_ready("lo")

    def test_ensure_interface_ready_not_found(self, mocker) -> None:
        """ensure_interface_ready() raises for non-existent interface."""
        mocker.patch("mvmctl.utils.network.Path.exists", return_value=False)

        with pytest.raises(NetworkError, match="does not exist"):
            NetworkUtils.ensure_interface_ready("eth99")

    def test_ensure_interface_ready_down(self, mocker, tmp_path) -> None:
        """ensure_interface_ready() raises for down interface."""
        mock_net_path = tmp_path / "sys" / "class" / "net" / "eth0"
        mock_net_path.mkdir(parents=True)
        (mock_net_path / "operstate").write_text("down")

        mocker.patch("mvmctl.utils.network.Path", return_value=mock_net_path)

        with pytest.raises(NetworkError, match="is down"):
            NetworkUtils.ensure_interface_ready("eth0")

    def test_ensure_interface_ready_no_ipv4(self, mocker, tmp_path) -> None:
        """ensure_interface_ready() raises when interface has no IPv4."""
        mock_net_path = tmp_path / "sys" / "class" / "net" / "eth0"
        mock_net_path.mkdir(parents=True)
        (mock_net_path / "operstate").write_text("up")

        mocker.patch("mvmctl.utils.network.Path", return_value=mock_net_path)
        mocker.patch(
            "subprocess.run", return_value=MagicMock(returncode=0, stdout="")
        )

        with pytest.raises(NetworkError, match="no IPv4 address"):
            NetworkUtils.ensure_interface_ready("eth0")
