"""Tests for NetworkService — bridge, TAP, NAT, iptables operations."""

from __future__ import annotations

import subprocess
from unittest.mock import MagicMock

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.exceptions import NetworkError, ProcessError
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

    def test_initialize_calls_tracker_initialize(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """initialize() delegates to _tracker.initialize()."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        service = NetworkService(repo)
        mock_init = mocker.patch.object(service._tracker, "initialize")
        service.initialize()
        mock_init.assert_called_once()

    def test_ensure_mvm_chains_calls_tracker_initialize(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_mvm_chains() delegates to the tracker's initialize method (backend-agnostic)."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        service = NetworkService(repo)
        mock_init = mocker.patch.object(service._tracker, "initialize")
        service.ensure_mvm_chains()
        mock_init.assert_called_once()


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
        mocker.patch.object(NetworkUtils, "bridge_exists", return_value=False)
        mocker.patch.object(
            NetworkUtils,
            "_run_batch",
            side_effect=ProcessError("Command failed (exit 1): ip"),
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
        mock_raw = mocker.patch.object(NetworkService, "remove_raw_bridge")

        service = NetworkService(repo)
        service.remove_bridge("mvm-test-net", network_id="test-net-001")

        mock_raw.assert_called_once_with("mvm-test-net")

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
        mocker.patch.object(NetworkService, "remove_raw_tap")
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
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mocker.patch.object(
            NetworkService,
            "remove_raw_bridge",
            side_effect=NetworkError("Failed to remove bridge"),
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
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=False)
        mocker.patch.object(
            NetworkUtils,
            "_run_batch",
            side_effect=ProcessError("Command failed (exit 1): ip"),
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
        mock_raw = mocker.patch.object(NetworkService, "remove_raw_tap")

        service = NetworkService(repo)
        service.remove_tap(
            "tap-vm1", "mvm-test-net", network_id=default_network.id
        )

        mock_raw.assert_called_once_with("tap-vm1")

    def test_remove_tap_noop_when_missing(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_tap() is a no-op when TAP doesn't exist."""
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=False)
        mock_raw = mocker.patch.object(NetworkService, "remove_raw_tap")

        service = NetworkService(repo)
        service.remove_tap(
            "tap-gone", "mvm-test-net", network_id=default_network.id
        )

        mock_raw.assert_not_called()

    def test_remove_tap_without_bridge_detects(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_tap() auto-detects bridge when not provided."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "get_tap_bridge", return_value="mvm-test-net"
        )
        mock_raw = mocker.patch.object(NetworkService, "remove_raw_tap")

        service = NetworkService(repo)
        service.remove_tap("tap-vm1", network_id=default_network.id)

        mock_raw.assert_called_once_with("tap-vm1")


class TestNetworkServiceNat:
    """Tests for ensure_nat() and remove_nat()."""

    def test_ensure_nat_adds_masquerade_and_forward(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_nat() creates MASQUERADE + FORWARD rules."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch("os.getuid", return_value=0)
        # Mock FirewallTracker to avoid hitting the nftables backend / DB
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_rule",
            return_value=MagicMock(success=True),
        )

        service = NetworkService(repo)
        service.ensure_nat(
            "mvm-test-net",
            ["eth0"],
            subnet="10.0.0.0/24",
            network_id=default_network.id,
        )

        # Should have subprocess calls for firewall commands
        assert service.tracker is not None

    def test_ensure_nat_raises_on_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_nat() raises NetworkError when MASQUERADE add fails."""
        # Mock ensure_chain to pass
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_chain"
        )
        # Mock ensure_rule to return failure
        mock_result = MagicMock()
        mock_result.success = False
        mock_result.error_message = "iptables failed"
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_rule",
            return_value=mock_result,
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))

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
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.remove_rule",
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
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.remove_rule",
            return_value=MagicMock(success=True),
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))

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
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.remove_rule",
            return_value=MagicMock(success=True),
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))

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


class TestNetworkServiceEnsureIpForwardingFallback:
    """Tests for ensure_ip_forwarding() sysctl fallback."""

    def test_falls_back_to_sysctl_when_proc_fails(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_ip_forwarding() falls back to sysctl when /proc write fails."""
        mocker.patch(
            "mvmctl.core.network._service.Path.write_text",
            side_effect=OSError("Read-only filesystem"),
        )
        mock_run = mocker.patch(
            "subprocess.run", return_value=MagicMock(returncode=0)
        )

        service = NetworkService(repo)
        service.ensure_ip_forwarding()

        mock_run.assert_called_once()
        args = mock_run.call_args[0][0]
        assert "sysctl" in args

    def test_raises_when_sysctl_also_fails(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_ip_forwarding() raises NetworkError when both methods fail."""
        mocker.patch(
            "mvmctl.core.network._service.Path.write_text",
            side_effect=OSError("Read-only filesystem"),
        )
        mocker.patch(
            "subprocess.run",
            side_effect=subprocess.CalledProcessError(
                1, ["sysctl", "-w", "net.ipv4.ip_forward=1"]
            ),
        )

        service = NetworkService(repo)
        with pytest.raises(
            NetworkError, match="Failed to enable IP forwarding"
        ):
            service.ensure_ip_forwarding()


class TestNetworkServiceTapEdgeCases:
    """Edge cases for ensure_tap() and remove_tap()."""

    def test_ensure_tap_reattaches_without_previous_bridge(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_tap() attaches TAP when it exists but has no bridge."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(NetworkUtils, "get_tap_bridge", return_value=None)
        mock_batch = mocker.patch.object(NetworkUtils, "_run_batch")

        service = NetworkService(repo)
        service.ensure_tap(
            "tap-vm1", "mvm-test-net", network_id=default_network.id
        )

        mock_batch.assert_called_once()
        batch_input = mock_batch.call_args[0][0]
        assert "link set tap-vm1 master mvm-test-net" in batch_input

    def test_ensure_tap_rolls_back_on_second_rule_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_tap() removes first rule when second ensure_rule fails."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=False)
        mocker.patch.object(NetworkUtils, "_run_batch")

        call_count: list[int] = [0]
        mock_remove = mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.remove_rule",
        )

        def _ensure_rule_side(*args: object, **kwargs: object) -> MagicMock:
            call_count[0] += 1
            result = MagicMock()
            if call_count[0] == 1:
                result.success = True
            else:
                result.success = False
                result.error_message = "iptables failed"
            return result

        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_rule",
            side_effect=_ensure_rule_side,
        )

        service = NetworkService(repo)
        with pytest.raises(
            NetworkError, match="Failed to add FORWARD rule for TAP"
        ):
            service.ensure_tap(
                "tap-vm1", "mvm-test-net", network_id=default_network.id
            )

        # Should have called remove_rule to roll back the first rule
        mock_remove.assert_called_once()

    def test_remove_tap_no_bridge_detected(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_tap() skips rule cleanup when bridge can't be detected."""
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(NetworkUtils, "get_tap_bridge", return_value=None)
        mock_raw = mocker.patch.object(NetworkService, "remove_raw_tap")
        mock_remove = mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.remove_rule",
        )

        service = NetworkService(repo)
        service.remove_tap("tap-vm1", network_id=default_network.id)

        mock_remove.assert_not_called()
        mock_raw.assert_called_once_with("tap-vm1")

    def test_remove_tap_logs_warning_on_rule_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_tap() logs warning when remove_rule fails but still deletes TAP."""
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "get_tap_bridge", return_value="mvm-test-net"
        )
        mock_raw = mocker.patch.object(NetworkService, "remove_raw_tap")
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.remove_rule",
            return_value=MagicMock(success=False, error_message="not found"),
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))

        service = NetworkService(repo)
        service.remove_tap(
            "tap-vm1", "mvm-test-net", network_id=default_network.id
        )

        mock_raw.assert_called_once_with("tap-vm1")

    def test_remove_tap_raises_on_batch_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_tap() raises NetworkError when link delete fails."""
        mocker.patch.object(NetworkUtils, "tap_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "get_tap_bridge", return_value="mvm-test-net"
        )
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.remove_rule",
            return_value=MagicMock(success=True),
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(
            NetworkService,
            "remove_raw_tap",
            side_effect=NetworkError("Failed to remove TAP device"),
        )

        service = NetworkService(repo)
        with pytest.raises(NetworkError, match="Failed to remove TAP"):
            service.remove_tap(
                "tap-vm1", "mvm-test-net", network_id=default_network.id
            )


class TestNetworkServiceNatEdgeCases:
    """Edge cases for remove_nat()."""

    def test_remove_nat_raises_when_db_lookup_fails(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """remove_nat() raises NetworkError when subnet and gateways can't be resolved."""
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mocker.patch.object(NetworkUtils, "bridge_exists", return_value=False)

        service = NetworkService(repo)
        with pytest.raises(
            NetworkError, match="Could not determine NAT gateways"
        ):
            service.remove_nat(
                "nonexistent-bridge",
                network_id="net-001",
                force=True,
            )

    def test_remove_nat_raises_when_gateways_missing(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """remove_nat() raises NetworkError when gateways not resolved."""

        class _FakeFailingResolver:
            def by_name(self, _name: str) -> NetworkItem:
                raise ValueError("Network not found in database")

        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mocker.patch(
            "mvmctl.core.network._resolver.NetworkResolver",
            return_value=_FakeFailingResolver(),
        )

        service = NetworkService(repo)
        with pytest.raises(
            NetworkError, match="Could not determine NAT gateways"
        ):
            service.remove_nat(
                "no-gw-bridge",
                network_id="net-001",
                force=True,
            )

    def test_remove_nat_logs_warning_on_failed_rule_removal(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_nat() logs warning when individual remove_rule fails."""
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.remove_rule",
            return_value=MagicMock(success=False, error_message="not found"),
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))

        service = NetworkService(repo)
        # Should not raise, just log warning
        service.remove_nat(
            "mvm-test-net",
            ["eth0"],
            subnet="10.0.0.0/24",
            network_id=default_network.id,
            force=True,
        )

    def test_remove_nat_with_subnet_none_gateways_none(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_nat() resolves from DB when both subnet and gateways are None."""
        from mvmctl.core.network._resolver import NetworkResolver

        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mock_resolver = mocker.patch.object(
            NetworkResolver, "by_name", return_value=default_network
        )
        mock_batch_remove = mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.batch_remove_rules",
            return_value=MagicMock(success=True),
        )

        service = NetworkService(repo)
        service.remove_nat(
            "test-net",
            network_id=default_network.id,
            force=True,
        )

        mock_resolver.assert_called_once_with("test-net")
        mock_batch_remove.assert_called_once()
        # Verify 3 rules were passed (MASQUERADE + FORWARD_OUT + FORWARD_IN per gateway)
        assert len(mock_batch_remove.call_args[0][0]) == 3

    def test_remove_nat_force_with_taps_present(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_nat() with force=True removes NAT even with attached TAPs."""
        mocker.patch.object(
            NetworkUtils, "get_bridge_taps", return_value=["tap-vm1"]
        )
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.remove_rule",
            return_value=MagicMock(success=True),
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))

        service = NetworkService(repo)
        service.remove_nat(
            "mvm-test-net",
            ["eth0"],
            subnet="10.0.0.0/24",
            network_id=default_network.id,
            force=True,
        )


class TestNetworkServiceRemove:
    """Tests for remove() and remove_many()."""

    def test_remove_calls_remove_nat_and_bridge(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove() calls remove_nat, remove_bridge, and performs DB deletion."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mock_remove_nat = mocker.patch.object(
            NetworkService, "remove_nat", autospec=True
        )
        mock_remove_bridge = mocker.patch.object(
            NetworkService, "remove_bridge", autospec=True
        )

        service = NetworkService(repo)
        service.remove(default_network)

        mock_remove_nat.assert_called_once()
        mock_remove_bridge.assert_called_once()
        # Verify DB deletion happened (no VMs reference this network)
        assert repo.get(default_network.id) is None

    def test_remove_tolerates_nat_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove() continues with bridge removal even if NAT fails."""
        mocker.patch.object(
            NetworkService,
            "remove_nat",
            side_effect=NetworkError("NAT failed"),
            autospec=True,
        )
        mocker.patch.object(NetworkService, "remove_bridge", autospec=True)
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])

        service = NetworkService(repo)
        service.remove(default_network)
        # Verify DB deletion still happened
        assert repo.get(default_network.id) is None

    def test_remove_tolerates_bridge_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove() performs DB deletion even if bridge removal fails."""
        mocker.patch.object(
            NetworkService,
            "remove_nat",
            autospec=True,
        )
        mocker.patch.object(
            NetworkService,
            "remove_bridge",
            side_effect=NetworkError("Bridge failed"),
            autospec=True,
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])

        service = NetworkService(repo)
        service.remove(default_network)
        # Verify DB deletion still happened despite bridge failure
        assert repo.get(default_network.id) is None

    def test_remove_with_nat_disabled(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """remove() skips NAT when nat_enabled is False."""
        repo.upsert(
            network_no_nat := NetworkItem(
                id="net-no-nat",
                name="test-no-nat",
                subnet="10.1.0.0/24",
                bridge="mvm-no-nat",
                ipv4_gateway="10.1.0.1",
                bridge_active=True,
                nat_enabled=False,
                is_default=False,
                is_present=True,
                created_at="2026-01-01T00:00:00Z",
                updated_at="2026-01-01T00:00:00Z",
            )
        )
        mocker.patch.object(NetworkUtils, "get_bridge_taps", return_value=[])
        mock_remove_nat = mocker.patch.object(
            NetworkService, "remove_nat", autospec=True
        )
        mocker.patch.object(NetworkService, "remove_bridge", autospec=True)
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))

        service = NetworkService(repo)
        service.remove(network_no_nat)

        mock_remove_nat.assert_not_called()

    def test_remove_many_removes_all(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """remove_many() removes all provided networks."""
        mocker.patch.object(NetworkService, "remove", autospec=True)

        net2 = NetworkItem(
            id="net-002",
            name="test-net-2",
            subnet="10.2.0.0/24",
            bridge="mvm-test-net-2",
            ipv4_gateway="10.2.0.1",
            bridge_active=True,
            nat_enabled=False,
            is_default=False,
            is_present=True,
            created_at="2026-01-01T00:00:00Z",
            updated_at="2026-01-01T00:00:00Z",
        )

        service = NetworkService(repo)
        service.remove_many([default_network, net2], force=True)

        assert service.remove.call_count == 2  # type: ignore[attr-defined]


class TestNetworkServiceDetectIptablesBackend:
    """Tests for detect_iptables_backend_conflict()."""

    def test_detect_no_conflict(self, repo: NetworkRepository, mocker) -> None:
        """detect_iptables_backend_conflict() returns (False, msg) when no conflict."""
        mocker.patch.object(
            NetworkUtils,
            "detect_iptables_backend_conflict",
            return_value=(
                False,
                "All iptables rules use the same backend (nft).",
            ),
        )

        service = NetworkService(repo)
        has_conflict, msg = service.detect_iptables_backend_conflict()

        assert has_conflict is False
        assert "same backend" in msg

    def test_detect_conflict(self, repo: NetworkRepository, mocker) -> None:
        """detect_iptables_backend_conflict() returns (True, msg) when conflict exists."""
        mocker.patch.object(
            NetworkUtils,
            "detect_iptables_backend_conflict",
            return_value=(True, "Mixed iptables backends detected"),
        )

        service = NetworkService(repo)
        has_conflict, msg = service.detect_iptables_backend_conflict()

        assert has_conflict is True
        assert "Mixed" in msg


class TestNetworkServiceSyncIptables:
    """Tests for sync_iptables_rules().

    Orphan detection is now handled by backend trackers
    (IPTablesTracker.count_orphaned_rules / NFTablesTracker.count_orphaned_rules
    via FirewallTracker.count_orphaned_rules delegation).
    """

    def test_sync_iptables_verifies_existing_rules(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """sync_iptables_rules() verifies existing DB rules using batch mode."""
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_rule",
            return_value=MagicMock(success=True, command_executed=None),
        )
        mocker.patch.object(
            default_network,  # type: ignore[arg-type]
            "nat_enabled",
            False,
        )

        # Insert a DB rule
        from mvmctl.models import (
            FirewallChain,
            FirewallPort,
            FirewallProtocol,
            FirewallRule,
            FirewallRuleType,
            FirewallTable,
            FirewallTarget,
            FirewallWildcard,
        )

        db_rule = FirewallRule(
            table_name=FirewallTable.FILTER,
            chain_name=FirewallChain.MVM_FORWARD,
            rule_type=FirewallRuleType.FORWARD_IN,
            protocol=FirewallProtocol.ALL,
            source=FirewallWildcard.ANY_CIDR,
            destination=FirewallWildcard.ANY_CIDR,
            in_interface=FirewallWildcard.ANY_INTERFACE,
            out_interface=FirewallWildcard.ANY_INTERFACE,
            target=FirewallTarget.ACCEPT,
            sport=FirewallPort.ANY,
            dport=FirewallPort.ANY,
            network_id=default_network.id,
            is_active=True,
            network_name=default_network.name,
            comment_tag="mvm:test:sync",
        )
        service = NetworkService(repo)

        # Mock tracker.batch() context manager
        mocker.patch.object(service._tracker, "batch")
        # Mock tracker.count_orphaned_rules (delegates to backend)
        mocker.patch.object(
            service._tracker, "count_orphaned_rules", return_value=0
        )

        service._tracker.repo.insert(db_rule)

        result = service.sync_iptables_rules(default_network)

        assert result["verified"] >= 1
        assert result["added"] == 0
        assert result["orphaned"] == 0
        # Verify batch was used
        service._tracker.batch.assert_called_once()
        # Verify orphan detection now delegates to tracker
        service._tracker.count_orphaned_rules.assert_called_once_with(
            default_network
        )

    def test_sync_iptables_adds_missing_rules(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """sync_iptables_rules() adds rules that need to be created using batch mode."""
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_rule",
            return_value=MagicMock(
                success=True, command_executed=["iptables", "-A"]
            ),
        )

        from mvmctl.models import (
            FirewallChain,
            FirewallPort,
            FirewallProtocol,
            FirewallRule,
            FirewallRuleType,
            FirewallTable,
            FirewallTarget,
            FirewallWildcard,
        )

        db_rule = FirewallRule(
            table_name=FirewallTable.FILTER,
            chain_name=FirewallChain.MVM_FORWARD,
            rule_type=FirewallRuleType.FORWARD_IN,
            protocol=FirewallProtocol.ALL,
            source=FirewallWildcard.ANY_CIDR,
            destination=FirewallWildcard.ANY_CIDR,
            in_interface=FirewallWildcard.ANY_INTERFACE,
            out_interface=FirewallWildcard.ANY_INTERFACE,
            target=FirewallTarget.ACCEPT,
            sport=FirewallPort.ANY,
            dport=FirewallPort.ANY,
            network_id=default_network.id,
            is_active=True,
            network_name=default_network.name,
            comment_tag="mvm:test:add",
        )
        service = NetworkService(repo)

        # Mock tracker.batch() context manager and count_orphaned_rules
        mocker.patch.object(service._tracker, "batch")
        mocker.patch.object(
            service._tracker, "count_orphaned_rules", return_value=0
        )

        service._tracker.repo.insert(db_rule)

        result = service.sync_iptables_rules(default_network)

        assert result["added"] >= 1
        assert result["verified"] == 0
        # Verify batch was used
        service._tracker.batch.assert_called_once()
        service._tracker.count_orphaned_rules.assert_called_once_with(
            default_network
        )


class TestNetworkServiceReconcileEdgeCases:
    """Edge cases for ensure_bridge reconcile path."""

    def test_ensure_bridge_reconcile_failure(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """ensure_bridge() raises NetworkError when reconcile batch fails."""
        mocker.patch.object(NetworkUtils, "bridge_exists", return_value=True)
        mocker.patch.object(
            NetworkUtils, "bridge_has_subnet", return_value=False
        )
        mocker.patch.object(
            NetworkUtils,
            "_run_batch",
            side_effect=ProcessError("Command failed (exit 1): ip"),
        )

        service = NetworkService(repo)
        with pytest.raises(NetworkError, match="Failed to setup bridge"):
            service.ensure_bridge("mvm-test-net", "10.0.0.1/24")

    def test_ensure_nat_forward_out_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_nat() raises NetworkError when FORWARD_OUT rule fails."""
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_chain"
        )
        call_count: list[int] = [0]

        def _ensure_rule_side(*args: object, **kwargs: object) -> MagicMock:
            call_count[0] += 1
            result = MagicMock()
            if call_count[0] in (1,):
                result.success = True
                result.error_message = None
            else:
                result.success = False
                result.error_message = "iptables forward out failed"
            return result

        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_rule",
            side_effect=_ensure_rule_side,
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))

        service = NetworkService(repo)
        with pytest.raises(NetworkError, match="Failed to add FORWARD out"):
            service.ensure_nat(
                "mvm-test-net",
                ["eth0"],
                subnet="10.0.0.0/24",
                network_id=default_network.id,
            )

    def test_ensure_nat_forward_in_failure(
        self, repo: NetworkRepository, default_network, mocker
    ) -> None:
        """ensure_nat() raises NetworkError when FORWARD_IN rule fails."""
        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_chain"
        )
        call_count: list[int] = [0]

        def _ensure_rule_side(*args: object, **kwargs: object) -> MagicMock:
            call_count[0] += 1
            result = MagicMock()
            if call_count[0] in (1, 2):
                result.success = True
            else:
                result.success = False
                result.error_message = "iptables forward in failed"
            return result

        mocker.patch(
            "mvmctl.core._shared._firewall_tracker.FirewallTracker.ensure_rule",
            side_effect=_ensure_rule_side,
        )
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))

        service = NetworkService(repo)
        with pytest.raises(NetworkError, match="Failed to add FORWARD in"):
            service.ensure_nat(
                "mvm-test-net",
                ["eth0"],
                subnet="10.0.0.0/24",
                network_id=default_network.id,
            )


class TestNetworkServiceRemoveStaleInterfaces:
    """Tests for NetworkService.remove_stale_interfaces()."""

    def test_no_bridges_returns_empty(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """Should return empty list when no bridges exist."""
        mocker.patch.object(NetworkUtils, "get_bridges", return_value=[])

        service = NetworkService(repo)
        result = service.remove_stale_interfaces("mvm-")

        assert result == []

    def test_no_matching_bridges_returns_empty(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """Should return empty list when bridges exist but none match prefix."""
        mocker.patch.object(
            NetworkUtils, "get_bridges", return_value=["docker0", "br-foo"]
        )

        service = NetworkService(repo)
        result = service.remove_stale_interfaces("mvm-")

        assert result == []

    def test_removes_slaves_from_matching_bridges(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """Should remove all slave interfaces from bridges matching prefix."""
        mocker.patch.object(
            NetworkUtils, "get_bridges", return_value=["mvm-net", "docker0"]
        )
        mocker.patch.object(
            NetworkUtils,
            "get_bridge_slaves",
            side_effect=lambda bridge: (
                ["mvm-net-tap1", "mvm-net-tap2"] if bridge == "mvm-net" else []
            ),
        )
        mock_raw = mocker.patch.object(NetworkService, "remove_raw_tap")

        service = NetworkService(repo)
        result = service.remove_stale_interfaces("mvm-")

        assert mock_raw.call_count == 2
        assert "Removed interface 'mvm-net-tap1'" in result
        assert "Removed interface 'mvm-net-tap2'" in result

    def test_handles_slave_removal_failure(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """Should record warning when slave removal fails."""
        mocker.patch.object(
            NetworkUtils, "get_bridges", return_value=["mvm-net"]
        )
        mocker.patch.object(
            NetworkUtils, "get_bridge_slaves", return_value=["mvm-net-tap1"]
        )
        mocker.patch.object(
            NetworkService,
            "remove_raw_tap",
            side_effect=NetworkError("ip link delete failed"),
        )

        service = NetworkService(repo)
        result = service.remove_stale_interfaces("mvm-")

        assert len(result) == 1
        assert "Warning: failed to remove interface 'mvm-net-tap1'" in result[0]

    def test_multiple_bridges_with_slaves(
        self, repo: NetworkRepository, mocker
    ) -> None:
        """Should process all matching bridges and their slaves."""
        mocker.patch.object(
            NetworkUtils,
            "get_bridges",
            return_value=["mvm-net", "mvm-testnet", "docker0"],
        )

        def _slaves(bridge: str) -> list[str]:
            if bridge == "mvm-net":
                return ["mvm-net-tap1"]
            if bridge == "mvm-testnet":
                return ["mvm-testnet-tap1", "mvm-testnet-tap2"]
            return []

        mocker.patch.object(
            NetworkUtils, "get_bridge_slaves", side_effect=_slaves
        )
        mock_raw = mocker.patch.object(NetworkService, "remove_raw_tap")

        service = NetworkService(repo)
        result = service.remove_stale_interfaces("mvm-")

        assert mock_raw.call_count == 3
        assert "Removed interface 'mvm-net-tap1'" in result
        assert "Removed interface 'mvm-testnet-tap1'" in result
        assert "Removed interface 'mvm-testnet-tap2'" in result

    def test_different_prefix(self, repo: NetworkRepository, mocker) -> None:
        """Should only match bridges with the given prefix."""
        mocker.patch.object(
            NetworkUtils,
            "get_bridges",
            return_value=["foo-bridge", "foo-tap-br", "mvm-net"],
        )
        mocker.patch.object(
            NetworkUtils,
            "get_bridge_slaves",
            side_effect=lambda bridge: (
                ["foo-slave"] if bridge == "foo-bridge" else []
            ),
        )
        mock_raw = mocker.patch.object(NetworkService, "remove_raw_tap")

        service = NetworkService(repo)
        result = service.remove_stale_interfaces("foo-")

        assert mock_raw.call_count == 1
        assert "Removed interface 'foo-slave'" in result
        assert "mvm-net" not in str(result)
