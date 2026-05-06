"""Tests for utils/network.py — NetworkUtils."""

from __future__ import annotations

import subprocess
from unittest.mock import MagicMock

import pytest

from mvmctl.exceptions import NetworkError
from mvmctl.utils.network import NetworkUtils


class TestSubnetMath:
    """Tests for subnet math / computation methods."""

    def test_compute_subnet_mask(self):
        result = NetworkUtils.compute_subnet_mask("10.0.0.0/24")
        assert result == "255.255.255.0"

    def test_compute_subnet_mask_slash16(self):
        result = NetworkUtils.compute_subnet_mask("172.16.0.0/16")
        assert result == "255.255.0.0"

    def test_compute_subnet_mask_slash28(self):
        result = NetworkUtils.compute_subnet_mask("192.168.1.0/28")
        assert result == "255.255.255.240"

    def test_compute_prefix_length(self):
        result = NetworkUtils.compute_prefix_length("10.0.0.0/24")
        assert result == 24

    def test_compute_prefix_length_slash16(self):
        result = NetworkUtils.compute_prefix_length("172.16.0.0/16")
        assert result == 16

    def test_compute_prefix_length_slash28(self):
        result = NetworkUtils.compute_prefix_length("192.168.1.0/28")
        assert result == 28

    def test_compute_ipv4_gateway_normal(self):
        result = NetworkUtils.compute_ipv4_gateway("10.0.0.0/24")
        assert result == "10.0.0.1"

    def test_compute_ipv4_gateway_slash28(self):
        result = NetworkUtils.compute_ipv4_gateway("192.168.1.0/28")
        assert result == "192.168.1.1"

    def test_compute_ipv4_gateway_slash31(self):
        result = NetworkUtils.compute_ipv4_gateway("10.0.0.0/31")
        assert result == "10.0.0.1"

    def test_compute_ipv4_gateway_slash31_second(self):
        result = NetworkUtils.compute_ipv4_gateway("172.16.0.0/31")
        assert result == "172.16.0.1"

    def test_compute_bridge_address(self):
        result = NetworkUtils.compute_bridge_address(
            "172.29.0.1", "172.29.0.0/28"
        )
        assert result == "172.29.0.1/28"

    def test_compute_bridge_address_slash24(self):
        result = NetworkUtils.compute_bridge_address("10.0.0.1", "10.0.0.0/24")
        assert result == "10.0.0.1/24"

    def test_compute_bridge_name(self):
        result = NetworkUtils.compute_bridge_name("mynet")
        assert result == "mvm-mynet"

    def test_compute_bridge_name_different(self):
        result = NetworkUtils.compute_bridge_name("default")
        assert result == "mvm-default"


class TestNamingGeneration:
    """Tests for naming and generation methods."""

    def test_generate_mac_format(self):
        mac = NetworkUtils.generate_mac("02:42")
        assert mac.startswith("02:42:")
        assert mac.count(":") == 5
        parts = mac.split(":")
        assert all(len(p) == 2 for p in parts)
        assert all(all(c in "0123456789ABCDEF" for c in p) for p in parts)

    def test_generate_mac_randomness(self):
        mac1 = NetworkUtils.generate_mac("02:42")
        mac2 = NetworkUtils.generate_mac("02:42")
        assert mac1 != mac2

    def test_generate_tap_name_format(self):
        tap = NetworkUtils.generate_tap_name("default", "vm1")
        assert tap.startswith("mvm-")
        assert len(tap) > len("mvm-")

    def test_generate_tap_name_randomness(self):
        tap1 = NetworkUtils.generate_tap_name("default", "vm1")
        tap2 = NetworkUtils.generate_tap_name("default", "vm2")
        assert tap1 != tap2


class TestIPAllocation:
    """Tests for allocate_next_ip."""

    def test_allocate_next_ip_basic(self):
        result = NetworkUtils.allocate_next_ip(
            ["10.0.0.1", "10.0.0.2"], "10.0.0.0/24"
        )
        assert result == "10.0.0.3"

    def test_allocate_next_ip_skips_gateway(self):
        result = NetworkUtils.allocate_next_ip(
            [], "10.0.0.0/24", gateway="10.0.0.1"
        )
        assert result == "10.0.0.2"

    def test_allocate_next_ip_first_host(self):
        result = NetworkUtils.allocate_next_ip([], "10.0.0.0/29")
        assert result == "10.0.0.1"

    def test_allocate_next_ip_skips_gateway_and_existing(self):
        result = NetworkUtils.allocate_next_ip(
            ["10.0.0.2"], "10.0.0.0/24", gateway="10.0.0.1"
        )
        assert result == "10.0.0.3"

    def test_allocate_next_ip_no_available(self):
        with pytest.raises(NetworkError, match="No available IPs"):
            NetworkUtils.allocate_next_ip(
                ["10.0.0.1", "10.0.0.2"], "10.0.0.0/30"
            )

    def test_allocate_next_ip_slash30_full(self):
        with pytest.raises(NetworkError, match="No available IPs"):
            NetworkUtils.allocate_next_ip(
                ["10.0.0.1", "10.0.0.2"],
                "10.0.0.0/30",
                gateway="10.0.0.1",
            )


class TestGetPhysicalInterfaces:
    """Tests for get_physical_interfaces."""

    def test_returns_physical_interfaces(self, mocker):
        entry1 = MagicMock()
        entry1.name = "eth0"
        entry2 = MagicMock()
        entry2.name = "ens33"
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = True
        mock_path.return_value.iterdir.return_value = [entry1, entry2]

        result = NetworkUtils.get_physical_interfaces()
        assert result == ["ens33", "eth0"]

    def test_excludes_loopback_and_virtual(self, mocker):
        entry1 = MagicMock()
        entry1.name = "eth0"
        entry2 = MagicMock()
        entry2.name = "lo"
        entry3 = MagicMock()
        entry3.name = "docker0"
        entry4 = MagicMock()
        entry4.name = "vethabc123"
        entry5 = MagicMock()
        entry5.name = "tap0"
        entry6 = MagicMock()
        entry6.name = "mvm-br0"
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = True
        mock_path.return_value.iterdir.return_value = [
            entry1,
            entry2,
            entry3,
            entry4,
            entry5,
            entry6,
        ]

        result = NetworkUtils.get_physical_interfaces()
        assert result == ["eth0"]

    def test_no_sys_class_net(self, mocker):
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = False

        with pytest.raises(
            NetworkError, match="Unable to access /sys/class/net"
        ):
            NetworkUtils.get_physical_interfaces()

    def test_oserror_handled(self, mocker):
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = True
        mock_path.return_value.iterdir.side_effect = OSError(
            "permission denied"
        )

        with pytest.raises(
            NetworkError, match="Failed to list network interfaces"
        ):
            NetworkUtils.get_physical_interfaces()


class TestDetectOutboundInterface:
    """Tests for detect_outbound_interface."""

    def test_returns_interface(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="default via 10.0.0.1 dev eth0 proto static\n",
            stderr="",
        )

        result = NetworkUtils.detect_outbound_interface()
        assert result == "eth0"

    def test_returns_none_when_no_default_route(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="",
            stderr="",
        )

        result = NetworkUtils.detect_outbound_interface()
        assert result is None

    def test_returns_none_when_called_process_error(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.side_effect = subprocess.CalledProcessError(1, ["ip", "route"])

        result = NetworkUtils.detect_outbound_interface()
        assert result is None

    def test_parses_dev_from_middle(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="default via 192.168.1.1 dev wlp2s0 proto dhcp metric 600\n",
            stderr="",
        )

        result = NetworkUtils.detect_outbound_interface()
        assert result == "wlp2s0"


class TestBridgeExists:
    """Tests for bridge_exists."""

    def test_returns_true(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=0)

        result = NetworkUtils.bridge_exists("br0")
        assert result is True
        mock_run.assert_called_once_with(
            ["ip", "link", "show", "br0"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )

    def test_returns_false(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=1)

        result = NetworkUtils.bridge_exists("nonexistent")
        assert result is False


class TestTapExists:
    """Tests for tap_exists."""

    def test_returns_true(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=0)

        result = NetworkUtils.tap_exists("tap0")
        assert result is True
        mock_run.assert_called_once_with(
            ["ip", "link", "show", "tap0"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )

    def test_returns_false(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=1)

        result = NetworkUtils.tap_exists("nonexistent")
        assert result is False


class TestChainExists:
    """Tests for chain_exists."""

    def test_returns_true(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=0)

        result = NetworkUtils.chain_exists("MVM-FORWARD")
        assert result is True
        mock_run.assert_called_once_with(
            ["iptables", "-t", "filter", "-L", "MVM-FORWARD", "-n"],
            capture_output=True,
            check=False,
        )

    def test_returns_false(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=1)

        result = NetworkUtils.chain_exists("NONEXISTENT")
        assert result is False

    def test_custom_table(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=0)

        result = NetworkUtils.chain_exists("MVM-POSTROUTING", table="nat")
        assert result is True
        mock_run.assert_called_once_with(
            ["iptables", "-t", "nat", "-L", "MVM-POSTROUTING", "-n"],
            capture_output=True,
            check=False,
        )


class TestGetTuntapDevices:
    """Tests for get_tuntap_devices."""

    def test_returns_devices(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=(
                "3: tap0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 "
                "qdisc pfifo_fast state UNKNOWN mode DEFAULT group default qlen 1000\n"
                "4: tap1: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 "
                "qdisc pfifo_fast state UNKNOWN mode DEFAULT group default qlen 1000\n"
            ),
            stderr="",
        )

        result = NetworkUtils.get_tuntap_devices()
        assert result == ["tap0", "tap1"]

    def test_returns_empty_when_no_devices(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="",
            stderr="",
        )

        result = NetworkUtils.get_tuntap_devices()
        assert result == []

    def test_returns_empty_on_failure(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=2, stdout="", stderr="")

        result = NetworkUtils.get_tuntap_devices()
        assert result == []


class TestGetBridges:
    """Tests for get_bridges."""

    def test_returns_bridges(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=(
                "1: br0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 "
                "qdisc noqueue state UP mode DEFAULT group default qlen 1000\n"
                "2: mvm-mynet: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 "
                "qdisc noqueue state UP mode DEFAULT group default qlen 1000\n"
            ),
            stderr="",
        )

        result = NetworkUtils.get_bridges()
        assert result == ["br0", "mvm-mynet"]

    def test_returns_empty_when_no_bridges(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="",
            stderr="",
        )

        result = NetworkUtils.get_bridges()
        assert result == []

    def test_returns_empty_on_failure(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=2, stdout="", stderr="")

        result = NetworkUtils.get_bridges()
        assert result == []


class TestGetBridgeTaps:
    """Tests for get_bridge_taps."""

    def test_returns_taps(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=(
                "3: tap0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 "
                "master mvm-bridge state UNKNOWN mode DEFAULT group default qlen 1000\n"
                "4: tap1: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 "
                "master mvm-bridge state UNKNOWN mode DEFAULT group default qlen 1000\n"
            ),
            stderr="",
        )

        result = NetworkUtils.get_bridge_taps("mvm-bridge")
        assert result == ["tap0", "tap1"]

    def test_returns_empty_when_no_taps(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="",
            stderr="",
        )

        result = NetworkUtils.get_bridge_taps("mvm-bridge")
        assert result == []

    def test_returns_empty_on_failure(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=1, stdout="", stderr="")

        result = NetworkUtils.get_bridge_taps("mvm-bridge")
        assert result == []


class TestGetTapBridge:
    """Tests for get_tap_bridge."""

    def test_returns_bridge(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=(
                "3: tap0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 "
                "master br0 state UNKNOWN mode DEFAULT group default qlen 1000\n"
            ),
            stderr="",
        )

        result = NetworkUtils.get_tap_bridge("tap0")
        assert result == "br0"

    def test_returns_none_when_not_attached(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=(
                "3: tap0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 "
                "state UNKNOWN mode DEFAULT group default qlen 1000\n"
            ),
            stderr="",
        )

        result = NetworkUtils.get_tap_bridge("tap0")
        assert result is None

    def test_returns_none_on_called_process_error(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.side_effect = subprocess.CalledProcessError(1, ["ip", "link"])

        result = NetworkUtils.get_tap_bridge("nonexistent")
        assert result is None


class TestBridgeHasSubnet:
    """Tests for bridge_has_subnet."""

    def test_returns_true(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="2: br0    inet 10.0.0.1/24 scope global br0       valid_lft forever preferred_lft forever\n",
            stderr="",
        )

        result = NetworkUtils.bridge_has_subnet("br0", "10.0.0.1/24")
        assert result is True

    def test_returns_false_when_subnet_not_found(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="2: br0    inet 172.16.0.1/16 scope global br0\n",
            stderr="",
        )

        result = NetworkUtils.bridge_has_subnet("br0", "10.0.0.0/24")
        assert result is False

    def test_returns_false_on_failure(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=1, stdout="", stderr="")

        result = NetworkUtils.bridge_has_subnet("br0", "10.0.0.0/24")
        assert result is False


class TestEnsureInterfaceReady:
    """Tests for ensure_interface_ready."""

    def test_ready(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="2: eth0    inet 10.0.0.5/24 scope global eth0\n",
            stderr="",
        )
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = True
        operstate_mock = mock_path.return_value.__truediv__.return_value
        operstate_mock.read_text.return_value = "up\n"

        result = NetworkUtils.ensure_interface_ready("eth0")
        assert result is True

    def test_raises_for_loopback(self, mocker):
        with pytest.raises(
            NetworkError,
            match="Loopback interface 'lo' cannot be used for NAT",
        ):
            NetworkUtils.ensure_interface_ready("lo")

    def test_raises_when_interface_does_not_exist(self, mocker):
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = False

        with pytest.raises(
            NetworkError,
            match="Interface 'eth0' does not exist",
        ):
            NetworkUtils.ensure_interface_ready("eth0")

    def test_raises_when_interface_is_down(self, mocker):
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = True
        operstate_mock = mock_path.return_value.__truediv__.return_value
        operstate_mock.read_text.return_value = "down\n"

        with pytest.raises(
            NetworkError,
            match="Interface 'eth0' is down",
        ):
            NetworkUtils.ensure_interface_ready("eth0")

    def test_raises_when_operstate_oserror(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="2: eth0    inet 10.0.0.5/24 scope global eth0\n",
            stderr="",
        )
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = True
        operstate_mock = mock_path.return_value.__truediv__.return_value
        operstate_mock.read_text.side_effect = OSError()

        result = NetworkUtils.ensure_interface_ready("eth0")
        assert result is True

    def test_raises_when_no_ipv4(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="",
            stderr="",
        )
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = True
        operstate_mock = mock_path.return_value.__truediv__.return_value
        operstate_mock.read_text.return_value = "up\n"

        with pytest.raises(
            NetworkError,
            match="Interface 'eth0' has no IPv4 address",
        ):
            NetworkUtils.ensure_interface_ready("eth0")

    def test_raises_when_ip_command_fails(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(
            returncode=1,
            stdout="",
            stderr="",
        )
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = True
        operstate_mock = mock_path.return_value.__truediv__.return_value
        operstate_mock.read_text.return_value = "up\n"

        with pytest.raises(
            NetworkError,
            match="Interface 'eth0' has no IPv4 address",
        ):
            NetworkUtils.ensure_interface_ready("eth0")

    def test_raises_when_ip_command_not_found(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.side_effect = FileNotFoundError()
        mock_path = mocker.patch("mvmctl.utils.network.Path")
        mock_path.return_value.exists.return_value = True
        operstate_mock = mock_path.return_value.__truediv__.return_value
        operstate_mock.read_text.return_value = "up\n"

        with pytest.raises(
            NetworkError,
            match="'ip' command not found",
        ):
            NetworkUtils.ensure_interface_ready("eth0")


class TestDetectIptablesBackendConflict:
    """Tests for detect_iptables_backend_conflict."""

    def test_no_rules_no_conflict(self, mocker):
        mocker.patch(
            "mvmctl.utils._system.privileged_cmd",
            side_effect=lambda cmd: cmd,
        )
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        no_rules_output = (
            "Chain INPUT (policy ACCEPT 0 packets, 0 bytes)\n"
            "    pkts bytes target     prot opt in     out     source               destination\n"
            "       0     0 ACCEPT     all  --  *      *       0.0.0.0/0            0.0.0.0/0\n"
        )
        mock_run.side_effect = [
            MagicMock(returncode=0, stdout="", stderr="nf_tables"),
            MagicMock(returncode=0, stdout=no_rules_output, stderr=""),
            MagicMock(returncode=0, stdout=no_rules_output, stderr=""),
        ]

        has_conflict, diagnosis = (
            NetworkUtils.detect_iptables_backend_conflict()
        )
        assert has_conflict is False
        assert "legacy active: False" in diagnosis
        assert "nft active: False" in diagnosis
        assert "nft" in diagnosis

    def test_legacy_active_only(self, mocker):
        mocker.patch(
            "mvmctl.utils._system.privileged_cmd",
            side_effect=lambda cmd: cmd,
        )
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        rules_with_traffic = (
            "Chain INPUT (policy ACCEPT 0 packets, 0 bytes)\n"
            "    pkts bytes target     prot opt in     out     source               destination\n"
            "   12345  6789 ACCEPT     all  --  *      *       0.0.0.0/0            0.0.0.0/0\n"
        )
        no_rules_output = (
            "Chain INPUT (policy ACCEPT 0 packets, 0 bytes)\n"
            "    pkts bytes target     prot opt in     out     source               destination\n"
            "       0     0 ACCEPT     all  --  *      *       0.0.0.0/0            0.0.0.0/0\n"
        )
        mock_run.side_effect = [
            MagicMock(returncode=0, stdout="", stderr="legacy"),
            MagicMock(returncode=0, stdout=rules_with_traffic, stderr=""),
            MagicMock(returncode=0, stdout=no_rules_output, stderr=""),
        ]

        has_conflict, diagnosis = (
            NetworkUtils.detect_iptables_backend_conflict()
        )
        assert has_conflict is False
        assert "legacy active: True" in diagnosis
        assert "nft active: False" in diagnosis
        assert "legacy" in diagnosis

    def test_both_active_conflict(self, mocker):
        mocker.patch(
            "mvmctl.utils._system.privileged_cmd",
            side_effect=lambda cmd: cmd,
        )
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        rules_with_traffic = (
            "Chain INPUT (policy ACCEPT 0 packets, 0 bytes)\n"
            "    pkts bytes target     prot opt in     out     source               destination\n"
            "   12345  6789 ACCEPT     all  --  *      *       0.0.0.0/0            0.0.0.0/0\n"
        )
        mock_run.side_effect = [
            MagicMock(returncode=0, stdout="", stderr="nf_tables"),
            MagicMock(returncode=0, stdout=rules_with_traffic, stderr=""),
            MagicMock(returncode=0, stdout=rules_with_traffic, stderr=""),
        ]

        has_conflict, diagnosis = (
            NetworkUtils.detect_iptables_backend_conflict()
        )
        assert has_conflict is True
        assert "legacy active: True" in diagnosis
        assert "nft active: True" in diagnosis

    def test_exception_during_legacy_check(self, mocker):
        mocker.patch(
            "mvmctl.utils._system.privileged_cmd",
            side_effect=lambda cmd: cmd,
        )
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        no_rules_output = (
            "Chain INPUT (policy ACCEPT 0 packets, 0 bytes)\n"
            "    pkts bytes target     prot opt in     out     source               destination\n"
            "       0     0 ACCEPT     all  --  *      *       0.0.0.0/0            0.0.0.0/0\n"
        )
        mock_run.side_effect = [
            MagicMock(returncode=0, stdout="", stderr="nf_tables"),
            subprocess.CalledProcessError(1, ["iptables-legacy"]),
            MagicMock(returncode=0, stdout=no_rules_output, stderr=""),
        ]

        has_conflict, diagnosis = (
            NetworkUtils.detect_iptables_backend_conflict()
        )
        assert has_conflict is False
        assert "legacy active: False" in diagnosis
        assert "nft active: False" in diagnosis

    def test_exception_during_nft_check(self, mocker):
        mocker.patch(
            "mvmctl.utils._system.privileged_cmd",
            side_effect=lambda cmd: cmd,
        )
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        rules_with_traffic = (
            "Chain INPUT (policy ACCEPT 0 packets, 0 bytes)\n"
            "    pkts bytes target     prot opt in     out     source               destination\n"
            "   12345  6789 ACCEPT     all  --  *      *       0.0.0.0/0            0.0.0.0/0\n"
        )
        mock_run.side_effect = [
            MagicMock(returncode=0, stdout="", stderr="nf_tables"),
            MagicMock(returncode=0, stdout=rules_with_traffic, stderr=""),
            subprocess.CalledProcessError(1, ["iptables"]),
        ]

        has_conflict, diagnosis = (
            NetworkUtils.detect_iptables_backend_conflict()
        )
        assert has_conflict is False
        assert "legacy active: True" in diagnosis
        assert "nft active: False" in diagnosis


class TestStripTapRules:
    """Tests for strip_tap_rules."""

    def test_strips_tap_rules(self, mocker):
        mocker.patch.object(
            NetworkUtils,
            "get_tuntap_devices",
            return_value=["tap0", "tap1"],
        )
        rules = (
            "*filter\n"
            ":INPUT ACCEPT [0:0]\n"
            "-A FORWARD -i tap0 -j ACCEPT\n"
            "-A FORWARD -o tap0 -j ACCEPT\n"
            "-A FORWARD -i tap1 -j ACCEPT\n"
            "-A FORWARD -o eth0 -j ACCEPT\n"
            "COMMIT\n"
        )

        result = NetworkUtils.strip_tap_rules(rules)
        assert "-i tap0" not in result
        assert "-o tap0" not in result
        assert "-i tap1" not in result
        assert "-o eth0" in result
        assert "*filter" in result
        assert "COMMIT" in result

    def test_no_tap_rules_unchanged(self, mocker):
        mocker.patch.object(
            NetworkUtils,
            "get_tuntap_devices",
            return_value=["tap0"],
        )
        rules = (
            "*filter\n"
            ":INPUT ACCEPT [0:0]\n"
            "-A FORWARD -i eth0 -j ACCEPT\n"
            "COMMIT\n"
        )

        result = NetworkUtils.strip_tap_rules(rules)
        assert result == rules

    def test_empty_rules(self, mocker):
        mocker.patch.object(
            NetworkUtils,
            "get_tuntap_devices",
            return_value=["tap0"],
        )

        result = NetworkUtils.strip_tap_rules("")
        assert result == ""

    def test_no_tap_devices_returns_unchanged(self, mocker):
        mocker.patch.object(
            NetworkUtils,
            "get_tuntap_devices",
            return_value=[],
        )
        rules = "*filter\n-A FORWARD -i tap0 -j ACCEPT\nCOMMIT\n"

        result = NetworkUtils.strip_tap_rules(rules)
        assert result == rules


class TestRunBatch:
    """Tests for _run_batch."""

    def test_runs_batch_successfully(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

        NetworkUtils._run_batch(["cmd1", "cmd2"])

        mock_run.assert_called_once_with(
            ["sudo", "ip", "-batch", "-"],
            input="cmd1\ncmd2\n",
            text=True,
            check=True,
            capture_output=True,
        )

    def test_raises_on_failure(self, mocker):
        mock_run = mocker.patch("mvmctl.utils.network.subprocess.run")
        mock_run.side_effect = subprocess.CalledProcessError(
            1, ["ip", "-batch", "-"]
        )

        with pytest.raises(subprocess.CalledProcessError):
            NetworkUtils._run_batch(["failing_cmd"])
