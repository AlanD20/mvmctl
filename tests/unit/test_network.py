"""Tests for core/network.py."""

import ipaddress
import re
import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.network import (
    _run_ip_batch,
    add_iptables_forward_rules,
    allocate_ip,
    bridge_exists,
    chain_exists,
    create_tap,
    delete_tap,
    generate_mac,
    get_default_interface,
    get_tap_devices,
    list_tuntap_devices,
    remove_iptables_forward_rules,
    setup_bridge,
    setup_mvm_chains,
    setup_nat,
    tap_exists,
    teardown_bridge,
    teardown_mvm_chains,
    teardown_nat,
)
from mvmctl.exceptions import NetworkError

# ---------------------------------------------------------------------------
# bridge_exists
# ---------------------------------------------------------------------------


def test_bridge_exists_true():
    """bridge_exists should return True when the ip command exits with code 0."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = bridge_exists("fc-br0")
        assert result is True
        mock_run.assert_called_once()


def test_bridge_exists_false():
    """bridge_exists should return False when the ip command exits with a non-zero code."""
    mock_result = MagicMock()
    mock_result.returncode = 1
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        result = bridge_exists("fc-br0")
        assert result is False


# ---------------------------------------------------------------------------
# generate_mac
# ---------------------------------------------------------------------------


def test_generate_mac_format():
    """generate_mac should return a MAC address matching the expected 02:FC:xx:xx:xx:xx format."""
    mac = generate_mac()
    pattern = r"^02:FC:[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}$"
    assert re.match(pattern, mac), f"MAC {mac!r} does not match expected format"


def test_generate_mac_uniqueness():
    """generate_mac should produce unique addresses across 100 consecutive calls."""
    macs = {generate_mac() for _ in range(100)}
    assert len(macs) == 100, "Expected 100 unique MAC addresses"


# ---------------------------------------------------------------------------
# allocate_ip
# ---------------------------------------------------------------------------


def test_allocate_ip_basic():
    """allocate_ip should return the first available host address in the subnet."""
    ip = allocate_ip([], "10.20.0.0/24", "10.20.0.1")
    assert ip == "10.20.0.2"


def test_allocate_ip_skips_gateway():
    """allocate_ip should skip the gateway IP and return the next available address."""
    ip = allocate_ip([], "10.20.0.0/24", "10.20.0.1")
    assert ip != "10.20.0.1"


def test_allocate_ip_skips_existing():
    """allocate_ip should skip already-allocated IPs and return the next free address."""
    ip = allocate_ip(["10.20.0.2", "10.20.0.3"], "10.20.0.0/24", "10.20.0.1")
    assert ip == "10.20.0.4"


def test_allocate_ip_exhausted():
    """allocate_ip should raise NetworkError when all addresses in the subnet are taken."""
    network = ipaddress.IPv4Network("10.20.0.0/24", strict=False)
    gateway = "10.20.0.1"
    # All hosts except the gateway
    all_ips = [str(h) for h in network.hosts() if str(h) != gateway]
    with pytest.raises(NetworkError):
        allocate_ip(all_ips, "10.20.0.0/24", gateway)


# ---------------------------------------------------------------------------
# get_tap_devices
# ---------------------------------------------------------------------------


def test_get_tap_devices_empty():
    """get_tap_devices should return an empty list when no TAP devices are attached to the bridge."""
    mock_result = MagicMock()
    mock_result.returncode = 1
    mock_result.stdout = ""
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        result = get_tap_devices("fc-br0")
        assert result == []


def test_get_tap_devices_parses():
    """get_tap_devices should parse interface names from ip link show output correctly."""
    # Sample output of `ip link show master fc-br0` with 2 TAP devices
    sample_output = (
        "3: fc-vm1-0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 master fc-br0\n"
        "    link/ether aa:bb:cc:dd:ee:ff brd ff:ff:ff:ff:ff:ff\n"
        "5: fc-vm2-0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 master fc-br0\n"
        "    link/ether 11:22:33:44:55:66 brd ff:ff:ff:ff:ff:ff\n"
    )
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = sample_output
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        result = get_tap_devices("fc-br0")
        assert result == ["fc-vm1-0", "fc-vm2-0"]


# ---------------------------------------------------------------------------
# get_default_interface
# ---------------------------------------------------------------------------


def test_get_default_interface_found():
    """get_default_interface should return the interface name from the default route."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = "default via 10.0.0.1 dev eth0 proto dhcp src 10.0.0.5 metric 100\n"
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        result = get_default_interface()
        assert result == "eth0"


def test_get_default_interface_not_found():
    """get_default_interface should raise NetworkError when no default route is found."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = ""
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        with pytest.raises(NetworkError):
            get_default_interface()


def test_get_default_interface_error_message_sanitized():
    with patch(
        "mvmctl.core.network.subprocess.run",
        side_effect=subprocess.CalledProcessError(
            1,
            ["ip", "route", "show", "default"],
            stderr="sensitive details for /etc/iproute2/rt_tables",
        ),
    ):
        with pytest.raises(NetworkError) as exc_info:
            get_default_interface()

    error_str = str(exc_info.value)
    assert error_str == "Failed to determine default network interface"
    assert "route show default" not in error_str
    assert "/etc/iproute2/rt_tables" not in error_str


# ---------------------------------------------------------------------------
# setup_bridge
# ---------------------------------------------------------------------------


def test_setup_bridge_already_exists():
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.bridge_exists", return_value=True):
        with patch("mvmctl.core.network._bridge_has_ip", return_value=True):
            with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
                with patch.object(Path, "write_text") as mock_write:
                    with patch("mvmctl.utils.process.os.getuid", return_value=0):
                        setup_bridge("fc-br0", "10.20.0.1/24")

                    assert mock_run.call_count == 1
                    assert mock_run.call_args.kwargs["input"] == "link set fc-br0 up\n"
                    mock_write.assert_called_once_with("1\n")


def test_setup_bridge_existing_bridge_missing_ip_reconciles():
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.bridge_exists", return_value=True):
        with patch("mvmctl.core.network._bridge_has_ip", return_value=False):
            with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
                with patch.object(Path, "write_text") as mock_write:
                    with patch("mvmctl.utils.process.os.getuid", return_value=0):
                        setup_bridge("fc-br0", "10.20.0.1/24")

                    assert mock_run.call_count == 1
                    assert mock_run.call_args.kwargs["input"] == (
                        "addr add 10.20.0.1/24 dev fc-br0\nlink set fc-br0 up\n"
                    )
                    mock_write.assert_called_once_with("1\n")


def test_setup_bridge_success():
    """setup_bridge should create the bridge and enable IP forwarding when it does not exist."""
    with patch("mvmctl.core.network.bridge_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch.object(Path, "write_text") as mock_write:
                with patch(
                    "mvmctl.utils.process.os.getuid", return_value=0
                ):  # Run as root to skip sudo
                    setup_bridge("fc-br0", "10.20.0.1/24")
                    # 1 call for ip -batch (bridge setup)
                    assert mock_run.call_count == 1
                    mock_write.assert_called_once_with("1\n")
                    assert mock_run.call_args_list[0].kwargs["input"] == (
                        "link add name fc-br0 type bridge\n"
                        "addr add 10.20.0.1/24 dev fc-br0\n"
                        "link set fc-br0 up\n"
                    )


def test_setup_bridge_create_fails():
    """setup_bridge should raise NetworkError when the ip command fails to create the bridge."""
    with patch("mvmctl.core.network.bridge_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch(
            "mvmctl.core.network.subprocess.run",
            side_effect=[
                subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to setup bridge"):
                setup_bridge("fc-br0", "10.20.0.1/24")


def test_setup_bridge_ip_forward_fails():
    with patch("mvmctl.core.network.bridge_exists", return_value=False):

        def _run_side_effect(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            cmd_flat = cmd if isinstance(cmd, list) else list(cmd)
            if "sysctl" in str(cmd_flat):
                raise subprocess.CalledProcessError(1, cmd_flat)
            result = MagicMock()
            result.returncode = 0
            return result

        with patch("mvmctl.core.network.subprocess.run", side_effect=_run_side_effect):
            with patch.object(Path, "write_text", side_effect=OSError("Permission denied")):
                with pytest.raises(
                    NetworkError, match="Failed to enable IP forwarding"
                ) as exc_info:
                    setup_bridge("fc-br0", "10.20.0.1/24")

    assert "/proc/sys/net/ipv4/ip_forward" not in str(exc_info.value)


# ---------------------------------------------------------------------------
# teardown_bridge
# ---------------------------------------------------------------------------


def test_teardown_bridge_success():
    """teardown_bridge should call subprocess once to bring down and delete the bridge."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        teardown_bridge("fc-br0")
        assert mock_run.call_count == 1


def test_teardown_bridge_down_fails():
    """teardown_bridge should raise NetworkError when the ip command fails."""
    with patch(
        "mvmctl.core.network.subprocess.run",
        side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
    ):
        with pytest.raises(NetworkError, match="Failed to teardown bridge"):  # Updated match
            teardown_bridge("fc-br0")


def test_setup_nat_all_rules_exist():
    """setup_nat should check each rule existence and skip existing ones."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
            with patch("mvmctl.core.network.setup_mvm_chains"):
                with patch("mvmctl.core.network.chain_exists", return_value=False):
                    setup_nat("fc-br0", "eth0")
                    # 3 calls: check MASQUERADE, check FORWARD out, check FORWARD in
                    # All return 0 (exist), so no add calls
                    assert mock_run.call_count == 3
                # Verify all calls use iptables -C (check)
                for call in mock_run.call_args_list:
                    assert "-C" in call[0][0]


def test_setup_nat_no_rules_exist():
    """setup_nat should add all rules when none exist."""
    # First 3 calls (checks) return 1 (rule doesn't exist), next 3 calls (adds) succeed
    mock_results = [
        MagicMock(returncode=1),  # MASQUERADE check - not exists
        MagicMock(returncode=0),  # MASQUERADE add - success
        MagicMock(returncode=1),  # FORWARD out check - not exists
        MagicMock(returncode=0),  # FORWARD out add - success
        MagicMock(returncode=1),  # FORWARD in check - not exists
        MagicMock(returncode=0),  # FORWARD in add - success
    ]
    with patch(
        "mvmctl.core.network.subprocess.run",
        side_effect=mock_results,
    ) as mock_run:
        with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
            with patch("mvmctl.core.network.setup_mvm_chains"):
                with patch("mvmctl.core.network.chain_exists", return_value=False):
                    setup_nat("fc-br0", "eth0")
                    # 6 calls: 3 checks + 3 adds
                    assert mock_run.call_count == 6


def test_setup_nat_masquerade_add_fails():
    """setup_nat should raise NetworkError when iptables add fails."""
    # Check returns 1 (not exists), add fails
    mock_results = [
        MagicMock(returncode=1),  # MASQUERADE check - not exists
        subprocess.CalledProcessError(1, ["iptables", "-A"]),  # MASQUERADE add - fails
    ]
    with patch(
        "mvmctl.core.network.subprocess.run",
        side_effect=mock_results,
    ):
        with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
            with patch("mvmctl.core.network.setup_mvm_chains"):
                with patch("mvmctl.core.network.chain_exists", return_value=False):
                    with pytest.raises(NetworkError, match="Failed to add MASQUERADE"):
                        setup_nat("fc-br0", "eth0")


def test_setup_nat_auto_detect_interface():
    """setup_nat should auto-detect the default interface when none is supplied."""
    mock_check = MagicMock()
    mock_check.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_check):
        with patch(
            "mvmctl.core.network.get_default_interface", return_value="eth0"
        ) as mock_get_iface:
            setup_nat("fc-br0")
            mock_get_iface.assert_called_once()


# ---------------------------------------------------------------------------
# teardown_nat
# ---------------------------------------------------------------------------


def test_teardown_nat_force_true_removes():
    """teardown_nat with force=True should remove MASQUERADE + FORWARD rules."""
    with patch("mvmctl.core.network.get_tap_devices", return_value=["tap0"]):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
                with patch("mvmctl.core.network.chain_exists", return_value=True):
                    with patch("mvmctl.core.network._detect_cidr_for_bridge", return_value=None):
                        teardown_nat("fc-br0", force=True)
                        # 3 rule deletions = 3 calls (CIDR detection returns None, no extra call)
                        assert mock_run.call_count == 3


def test_teardown_nat_tap_devices_present_skips():
    """teardown_nat with force=False should skip removal when TAP devices are still present."""
    with patch("mvmctl.core.network.get_tap_devices", return_value=["tap0"]) as mock_get_taps:
        with patch("mvmctl.core.network.subprocess.run") as mock_run:
            teardown_nat("fc-br0", force=False)
            mock_get_taps.assert_called_once_with("fc-br0")
            mock_run.assert_not_called()


def test_teardown_nat_no_taps_removes():
    """teardown_nat should remove MASQUERADE + bridge FORWARD rules when no TAPs remain."""
    with patch("mvmctl.core.network.get_tap_devices", return_value=[]):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
                with patch("mvmctl.core.network.chain_exists", return_value=True):
                    with patch("mvmctl.core.network._detect_cidr_for_bridge", return_value=None):
                        teardown_nat("fc-br0", force=False)
                        # 3 rule deletions = 3 calls (CIDR detection returns None, no extra call)
                        assert mock_run.call_count == 3


def test_teardown_nat_removes_forward_rules_for_correct_bridge():
    with patch("mvmctl.core.network.get_tap_devices", return_value=[]):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("mvmctl.core.network.get_default_interface", return_value="wlan0"):
                with patch("mvmctl.core.network.chain_exists", return_value=True):
                    teardown_nat("mvm-default", force=True)
        calls = [str(c) for c in mock_run.call_args_list]
        assert any("MASQUERADE" in c for c in calls)
        assert any("mvm-default" in c and "wlan0" in c for c in calls)


def test_teardown_nat_called_process_error():
    """teardown_nat should raise NetworkError when the iptables deletion command fails."""
    with patch("mvmctl.core.network.get_tap_devices", return_value=[]):
        with patch("mvmctl.core.network.chain_exists", return_value=True):
            with patch(
                "mvmctl.core.network.subprocess.run",
                side_effect=subprocess.CalledProcessError(1, ["iptables", "-t", "nat", "-D"]),
            ):
                with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
                    with pytest.raises(NetworkError, match="Failed to remove MASQUERADE rule"):
                        teardown_nat("fc-br0", force=True)


def test_teardown_nat_get_default_interface_fails_gracefully():
    """teardown_nat should silently skip removal when the default interface cannot be determined."""
    with patch("mvmctl.core.network.get_tap_devices", return_value=[]):
        with patch(
            "mvmctl.core.network.get_default_interface",
            side_effect=NetworkError("No default interface"),
        ):
            with patch("mvmctl.core.network.subprocess.run") as mock_run:
                teardown_nat("fc-br0", force=True)
                mock_run.assert_not_called()


# ---------------------------------------------------------------------------
# tap_exists
# ---------------------------------------------------------------------------


def test_tap_exists_true():
    """tap_exists should return True when the ip command exits with code 0."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = tap_exists("fc-vm1-0")
        assert result is True
        mock_run.assert_called_once()


def test_tap_exists_false():
    """tap_exists should return False when the ip command exits with a non-zero code."""
    mock_result = MagicMock()
    mock_result.returncode = 1
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        result = tap_exists("fc-vm1-0")
        assert result is False


# ---------------------------------------------------------------------------
# create_tap
# ---------------------------------------------------------------------------


def test_create_tap_already_exists():
    """create_tap should raise NetworkError when the TAP device already exists."""
    with patch("mvmctl.core.network.tap_exists", return_value=True):
        with pytest.raises(NetworkError, match="TAP device .* already exists"):
            create_tap("fc-vm1-0", "fc-br0")


def test_create_tap_success():
    """create_tap should call subprocess once to create and attach the TAP device to the bridge."""
    with patch("mvmctl.core.network.tap_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            create_tap("fc-vm1-0", "fc-br0")
            assert mock_run.call_count == 1
            assert mock_run.call_args.kwargs["input"] == (
                "tuntap add dev fc-vm1-0 mode tap\n"
                "link set fc-vm1-0 master fc-br0\n"
                "link set fc-vm1-0 up\n"
            )


def test_create_tap_create_fails():
    """create_tap should raise NetworkError when the ip command fails to create the TAP device."""
    with patch("mvmctl.core.network.tap_exists", return_value=False):
        with patch(
            "mvmctl.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
        ):
            with pytest.raises(NetworkError, match="Failed to create TAP"):  # Updated match
                create_tap("fc-vm1-0", "fc-br0")


def test_delete_tap_does_not_exist():
    """delete_tap should be a no-op when the TAP device does not exist."""
    with patch("mvmctl.core.network.tap_exists", return_value=False):
        with patch("mvmctl.core.network.subprocess.run") as mock_run:
            delete_tap("fc-vm1-0")
            mock_run.assert_not_called()


def test_delete_tap_success():
    """delete_tap should call subprocess once to delete an existing TAP device."""
    with patch("mvmctl.core.network.tap_exists", return_value=True):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            delete_tap("fc-vm1-0")
            assert mock_run.call_count == 1
            assert mock_run.call_args.kwargs["input"] == (
                "link set fc-vm1-0 down\nlink delete fc-vm1-0\n"
            )


def test_run_ip_batch_uses_ip_batch_mode():
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("mvmctl.utils.process.os.getuid", return_value=0):
            _run_ip_batch(["link set tap0 up", "link delete tap0"])

    mock_run.assert_called_once_with(
        ["ip", "-batch", "-"],
        input="link set tap0 up\nlink delete tap0\n",
        text=True,
        check=True,
        capture_output=True,
    )


def test_delete_tap_down_fails():
    """delete_tap should raise NetworkError when the ip command fails to delete the TAP device."""
    with patch("mvmctl.core.network.tap_exists", return_value=True):
        with patch(
            "mvmctl.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
        ):
            with pytest.raises(NetworkError, match="Failed to delete TAP"):  # Updated match
                delete_tap("fc-vm1-0")


def test_add_iptables_forward_rules_already_exist():
    """add_iptables_forward_rules should check each rule and skip existing ones."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("mvmctl.core.network.setup_mvm_chains"):
            with patch("mvmctl.core.network.chain_exists", return_value=False):
                add_iptables_forward_rules("fc-vm1-0", "fc-br0")
                # 2 calls: check bridge→TAP, check TAP→bridge
                assert mock_run.call_count == 2
            # Verify all calls use iptables -C (check)
            for call in mock_run.call_args_list:
                assert "-C" in call[0][0]


def test_add_iptables_forward_rules_add_success():
    """add_iptables_forward_rules should add all rules when none exist."""
    # First 2 calls (checks) return 1 (rule doesn't exist), next 2 calls (adds) succeed
    mock_results = [
        MagicMock(returncode=1),  # bridge→TAP check - not exists
        MagicMock(returncode=0),  # bridge→TAP add - success
        MagicMock(returncode=1),  # TAP→bridge check - not exists
        MagicMock(returncode=0),  # TAP→bridge add - success
    ]
    with patch(
        "mvmctl.core.network.subprocess.run",
        side_effect=mock_results,
    ) as mock_run:
        with patch("mvmctl.core.network.setup_mvm_chains"):
            with patch("mvmctl.core.network.chain_exists", return_value=False):
                add_iptables_forward_rules("fc-vm1-0", "fc-br0")
                # 4 calls: 2 checks + 2 adds
                assert mock_run.call_count == 4


def test_add_iptables_forward_rules_bridge_to_tap_fails():
    """add_iptables_forward_rules should raise NetworkError when iptables add fails."""
    # Check returns 1 (not exists), add fails
    mock_results = [
        MagicMock(returncode=1),  # bridge→TAP check - not exists
        subprocess.CalledProcessError(1, ["iptables", "-A"]),  # bridge→TAP add - fails
    ]
    with patch(
        "mvmctl.core.network.subprocess.run",
        side_effect=mock_results,
    ):
        with patch("mvmctl.core.network.setup_mvm_chains"):
            with patch("mvmctl.core.network.chain_exists", return_value=False):
                with pytest.raises(NetworkError, match="Failed to add FORWARD rule"):
                    add_iptables_forward_rules("fc-vm1-0", "fc-br0")


def test_remove_iptables_forward_rules_success():
    """remove_iptables_forward_rules should call subprocess twice to delete FORWARD rules."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("mvmctl.core.network.chain_exists", return_value=True):
            remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
            # 2 rule deletions = 2 calls
            assert mock_run.call_count == 2


def test_remove_iptables_forward_rules_already_absent():
    """remove_iptables_forward_rules should not raise when the rules are already absent."""
    with patch("mvmctl.core.network.chain_exists", return_value=True):
        with patch(
            "mvmctl.core.network.subprocess.run",
            side_effect=[
                subprocess.CompletedProcess(["iptables", "-D", "FORWARD"], returncode=1),
                subprocess.CompletedProcess(["iptables", "-D", "FORWARD"], returncode=1),
            ],
        ) as mock_run:
            remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
            # 2 delete attempts = 2 calls
            assert mock_run.call_count == 2


# ---------------------------------------------------------------------------
# chain_exists
# ---------------------------------------------------------------------------


def test_chain_exists_true():
    """chain_exists should return True when the iptables chain exists."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = chain_exists("MVM-FORWARD", "filter")
        assert result is True
        mock_run.assert_called_once()
        args = mock_run.call_args[0][0]
        assert "-L" in args
        assert "MVM-FORWARD" in args


def test_chain_exists_false():
    """chain_exists should return False when the iptables chain does not exist."""
    mock_result = MagicMock()
    mock_result.returncode = 1
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        result = chain_exists("MVM-FORWARD", "filter")
        assert result is False


def test_chain_exists_nat_table():
    """chain_exists should work with nat table."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = chain_exists("MVM-POSTROUTING", "nat")
        assert result is True
        args = mock_run.call_args[0][0]
        assert "-t" in args
        assert "nat" in args


# ---------------------------------------------------------------------------
# setup_mvm_chains
# ---------------------------------------------------------------------------


def test_setup_mvm_chains_creates_both_chains():
    """setup_mvm_chains should create all MVM iptables chains (FORWARD, POSTROUTING, NOCLOUD-INPUT)."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.chain_exists", return_value=False):
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("mvmctl.core.network._iptables_rule_exists", return_value=False):
                setup_mvm_chains()
                assert mock_run.call_count == 9  # 3 chains × (1 create + 2 jump rule ops)


def test_setup_mvm_chains_idempotent():
    """setup_mvm_chains should not recreate chains that already exist."""
    with patch("mvmctl.core.network.chain_exists", return_value=True):
        with patch("mvmctl.core.network._iptables_rule_exists", return_value=True):
            with patch("mvmctl.core.network.subprocess.run") as mock_run:
                already_existed = setup_mvm_chains()
                assert mock_run.call_count == 3  # 3 chains, just delete old jump rules
                assert already_existed is True


def test_setup_mvm_chains_adds_jump_rules():
    """setup_mvm_chains should add jump rules from built-in chains to MVM chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.chain_exists", return_value=True):
        with patch("mvmctl.core.network._iptables_rule_exists", return_value=False):
            with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
                setup_mvm_chains()
                assert mock_run.call_count == 6  # 3 chains × 2 (delete + insert jump rule)


def test_setup_mvm_chains_inserts_forward_jump_at_top_priority():
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.chain_exists", return_value=True):
        with patch("mvmctl.core.network._iptables_rule_exists", return_value=False):
            with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
                setup_mvm_chains()
                commands = [call.args[0] for call in mock_run.call_args_list]
                assert [
                    "sudo",
                    "iptables",
                    "-t",
                    "filter",
                    "-I",
                    "FORWARD",
                    "1",
                    "-j",
                    "MVM-FORWARD",
                ] in commands


def test_setup_mvm_chains_returns_false_when_created():
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.chain_exists", return_value=False):
        with patch("mvmctl.core.network._iptables_rule_exists", return_value=False):
            with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
                already_existed = setup_mvm_chains()
                assert already_existed is False


def test_setup_mvm_chains_raises_on_failure():
    """setup_mvm_chains should raise NetworkError when chain creation fails."""
    with patch("mvmctl.core.network.chain_exists", return_value=False):
        with patch(
            "mvmctl.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["iptables", "-N"]),
        ):
            with pytest.raises(NetworkError, match="Failed to create MVM-FORWARD chain"):
                setup_mvm_chains()


# ---------------------------------------------------------------------------
# teardown_mvm_chains
# ---------------------------------------------------------------------------


def test_teardown_mvm_chains_removes_both_chains():
    """teardown_mvm_chains should remove both FORWARD and POSTROUTING chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.chain_exists", return_value=True):
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            teardown_mvm_chains()
            # 2 jump removals + 2 flushes + 2 deletions = 6 calls
            assert mock_run.call_count == 6


def test_teardown_mvm_chains_safe_when_missing():
    """teardown_mvm_chains should be safe when chains don't exist."""
    with patch("mvmctl.core.network.chain_exists", return_value=False):
        with patch("mvmctl.core.network.subprocess.run") as mock_run:
            teardown_mvm_chains()
            mock_run.assert_not_called()


def test_teardown_mvm_chains_raises_on_flush_failure():
    """teardown_mvm_chains should raise NetworkError when chain flush fails."""
    with patch("mvmctl.core.network.chain_exists", return_value=True):
        mock_success = MagicMock()
        mock_success.returncode = 0
        with patch(
            "mvmctl.core.network.subprocess.run",
            side_effect=[
                # Jump removal from FORWARD (check=False, ignored)
                mock_success,
                # Flush MVM-FORWARD chain - fails
                subprocess.CalledProcessError(1, ["iptables", "-F"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to remove MVM-FORWARD chain"):
                teardown_mvm_chains()


# ---------------------------------------------------------------------------
# setup_nat with MVM chains
# ---------------------------------------------------------------------------


def test_setup_nat_calls_setup_mvm_chains():
    """setup_nat should call setup_mvm_chains to ensure chains exist."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.setup_mvm_chains") as mock_setup_chains:
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
            with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
                with patch("mvmctl.core.network._iptables_rule_exists", return_value=True):
                    setup_nat("fc-br0", "eth0")
                    mock_setup_chains.assert_called_once()


def test_setup_nat_adds_rules_to_mvm_chains():
    """setup_nat should add rules to MVM chains, not built-in chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.setup_mvm_chains"):
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
                with patch("mvmctl.core.network._iptables_rule_exists", return_value=False):
                    setup_nat("fc-br0", "eth0")
                    # Check that rules are added to MVM chains
                    calls = [str(c) for c in mock_run.call_args_list]
                    assert any("MVM-POSTROUTING" in c for c in calls)
                    assert any("MVM-FORWARD" in c for c in calls)


# ---------------------------------------------------------------------------
# teardown_nat with MVM chains
# ---------------------------------------------------------------------------


def test_teardown_nat_skips_when_chains_missing():
    """teardown_nat should skip when MVM chains don't exist."""
    with patch("mvmctl.core.network.get_tap_devices", return_value=[]):
        with patch("mvmctl.core.network.chain_exists", return_value=False):
            with patch("mvmctl.core.network.subprocess.run") as mock_run:
                with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
                    teardown_nat("fc-br0", force=True)
                    mock_run.assert_not_called()


def test_teardown_nat_removes_rules_from_mvm_chains():
    """teardown_nat should remove rules from MVM chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.get_tap_devices", return_value=[]):
        with patch("mvmctl.core.network.chain_exists", return_value=True):
            with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
                with patch("mvmctl.core.network.get_default_interface", return_value="eth0"):
                    teardown_nat("fc-br0", force=True)
                    calls = [str(c) for c in mock_run.call_args_list]
                    assert any("MVM-POSTROUTING" in c for c in calls)
                    assert any("MVM-FORWARD" in c for c in calls)


# ---------------------------------------------------------------------------
# add_iptables_forward_rules with MVM chains
# ---------------------------------------------------------------------------


def test_add_iptables_forward_rules_calls_setup_chains():
    """add_iptables_forward_rules should call setup_mvm_chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.setup_mvm_chains") as mock_setup_chains:
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
            with patch("mvmctl.core.network._iptables_rule_exists", return_value=True):
                add_iptables_forward_rules("fc-vm1-0", "fc-br0")
                mock_setup_chains.assert_called_once()


def test_add_iptables_forward_rules_uses_mvm_chain():
    """add_iptables_forward_rules should add rules to MVM-FORWARD chain."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.setup_mvm_chains"):
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("mvmctl.core.network._iptables_rule_exists", return_value=False):
                add_iptables_forward_rules("fc-vm1-0", "fc-br0")
                calls = [str(c) for c in mock_run.call_args_list]
                assert all("MVM-FORWARD" in c for c in calls)


# ---------------------------------------------------------------------------
# remove_iptables_forward_rules with MVM chains
# ---------------------------------------------------------------------------


def test_remove_iptables_forward_rules_skips_when_chain_missing():
    """remove_iptables_forward_rules should skip when MVM chain doesn't exist."""
    with patch("mvmctl.core.network.chain_exists", return_value=False):
        with patch("mvmctl.core.network.subprocess.run") as mock_run:
            remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
            mock_run.assert_not_called()


def test_remove_iptables_forward_rules_uses_mvm_chain():
    """remove_iptables_forward_rules should remove rules from MVM-FORWARD chain."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("mvmctl.core.network.chain_exists", return_value=True):
        with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
            remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
            calls = [str(c) for c in mock_run.call_args_list]
            assert all("MVM-FORWARD" in c for c in calls)


def test_list_tuntap_devices_returns_all_names():
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = (
        "3: mvm-def-vm0-aaa: <BROADCAST> mtu 1500\n4: mvm-orphan: <BROADCAST> mtu 1500\n"
    )
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        devices = list_tuntap_devices()
        assert devices == ["mvm-def-vm0-aaa", "mvm-orphan"]


def test_list_tuntap_devices_handles_command_failure():
    mock_result = MagicMock()
    mock_result.returncode = 1
    mock_result.stdout = ""
    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        assert list_tuntap_devices() == []


# ---------------------------------------------------------------------------
# Sudo credential caching (Issue #10)
# ---------------------------------------------------------------------------


def test_sudo_credentials_cached_after_validation():
    """Sudo credentials should be cached after successful validation."""
    import mvmctl.utils.process as process_module

    process_module._SUDO_CREDENTIALS_VALID = False
    process_module._SUDO_CACHE_TIMESTAMP = 0.0

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.utils.process.subprocess.run", return_value=mock_result):
        result = process_module._validate_sudo_credentials()
        assert result is True
        assert process_module._is_sudo_cached() is True

    process_module._SUDO_CREDENTIALS_VALID = False
    process_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_cache_reduces_validation_calls():
    """Multiple calls should only trigger one sudo validation."""
    import mvmctl.utils.process as process_module

    process_module._SUDO_CREDENTIALS_VALID = False
    process_module._SUDO_CACHE_TIMESTAMP = 0.0

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.utils.process.subprocess.run", return_value=mock_result) as mock_run:
        process_module._validate_sudo_credentials()
        assert mock_run.call_count == 1

        mock_run.reset_mock()
        process_module._validate_sudo_credentials()
        assert mock_run.call_count == 0

    process_module._SUDO_CREDENTIALS_VALID = False
    process_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_cache_expires_after_ttl():
    """Sudo credentials should expire after TTL period."""
    import mvmctl.utils.process as process_module

    process_module._SUDO_CREDENTIALS_VALID = True
    process_module._SUDO_CACHE_TIMESTAMP = 0.0

    assert process_module._is_sudo_cached() is False

    process_module._SUDO_CREDENTIALS_VALID = False
    process_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_uses_sudo_n_for_non_interactive_check():
    """Sudo validation should use 'sudo -n true' for non-interactive check."""
    import mvmctl.utils.process as process_module

    process_module._SUDO_CREDENTIALS_VALID = False
    process_module._SUDO_CACHE_TIMESTAMP = 0.0

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.utils.process.subprocess.run", return_value=mock_result) as mock_run:
        process_module._validate_sudo_credentials()

        calls = mock_run.call_args_list
        assert len(calls) == 1
        assert calls[0][0][0] == ["sudo", "-n", "true"]

    process_module._SUDO_CREDENTIALS_VALID = False
    process_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_uses_sudo_v_when_not_cached():
    """Sudo validation should use 'sudo -v' when credentials are not cached."""
    import mvmctl.utils.process as process_module

    process_module._SUDO_CREDENTIALS_VALID = False
    process_module._SUDO_CACHE_TIMESTAMP = 0.0

    not_cached_result = MagicMock(returncode=1)
    cached_result = MagicMock(returncode=0)

    with patch(
        "mvmctl.utils.process.subprocess.run",
        side_effect=[not_cached_result, cached_result],
    ) as mock_run:
        process_module._validate_sudo_credentials()

        calls = mock_run.call_args_list
        assert len(calls) == 2
        assert calls[0][0][0] == ["sudo", "-n", "true"]
        assert calls[1][0][0] == ["sudo", "-v"]

    process_module._SUDO_CREDENTIALS_VALID = False
    process_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_anti_recursion_protection():
    """Sudo validation should have anti-recursion protection."""
    import mvmctl.utils.process as process_module

    original_state = process_module._SUDO_VALIDATION_IN_PROGRESS
    process_module._SUDO_VALIDATION_IN_PROGRESS = True

    try:
        result = process_module._validate_sudo_credentials()
        assert result is False
    finally:
        process_module._SUDO_VALIDATION_IN_PROGRESS = original_state


@pytest.mark.real_mvm_group_check
def test_privileged_cmd_raises_when_not_in_mvm_group():
    """_privileged_cmd should raise PrivilegeError when user is not in mvm group."""
    import grp
    import pwd

    from mvmctl.exceptions import PrivilegeError
    from mvmctl.utils.process import privileged_cmd as _privileged_cmd

    mock_group = MagicMock()
    mock_group.gr_gid = 1001
    mock_group.gr_mem = ["otheruser"]

    mock_passwd = MagicMock()
    mock_passwd.pw_name = "testuser"
    mock_passwd.pw_gid = 1000

    with patch("mvmctl.utils.process.os.getuid", return_value=1000):
        with patch.object(grp, "getgrnam", return_value=mock_group):
            with patch.object(pwd, "getpwuid", return_value=mock_passwd):
                with pytest.raises(PrivilegeError, match="not in the 'mvm' group"):
                    _privileged_cmd(["ip", "link", "show"])


@pytest.mark.real_mvm_group_check
def test_privileged_cmd_succeeds_when_in_mvm_group():
    """_privileged_cmd should return sudo command when user is in mvm group."""
    import grp
    import pwd

    from mvmctl.utils.process import privileged_cmd as _privileged_cmd

    mock_group = MagicMock()
    mock_group.gr_gid = 1001
    mock_group.gr_mem = ["testuser"]

    mock_passwd = MagicMock()
    mock_passwd.pw_name = "testuser"
    mock_passwd.pw_gid = 1000

    with patch("mvmctl.utils.process.os.getuid", return_value=1000):
        with patch.object(grp, "getgrnam", return_value=mock_group):
            with patch.object(pwd, "getpwuid", return_value=mock_passwd):
                with patch("mvmctl.utils.process.os.getgroups", return_value=[1001]):
                    with patch("mvmctl.utils.process.os.getgid", return_value=1000):
                        with patch("mvmctl.utils.process.os.getegid", return_value=1000):
                            result = _privileged_cmd(["ip", "link", "show"])
                            assert result == ["sudo", "ip", "link", "show"]


def test_privileged_cmd_skips_sudo_when_root():
    """_privileged_cmd should not add sudo when running as root."""
    from mvmctl.utils.process import privileged_cmd as _privileged_cmd

    with patch("mvmctl.utils.process.os.getuid", return_value=0):
        with patch("mvmctl.utils.process.subprocess.run") as mock_run:
            result = _privileged_cmd(["ip", "link", "show"])
            assert result == ["ip", "link", "show"]
            mock_run.assert_not_called()


@pytest.mark.real_mvm_group_check
def test_require_mvm_group_raises_when_group_not_found():
    """_require_mvm_group_membership should raise PrivilegeError when mvm group doesn't exist."""
    import grp

    from mvmctl.exceptions import PrivilegeError
    from mvmctl.utils.process import require_mvm_group_membership as _require_mvm_group_membership

    with patch.object(grp, "getgrnam", side_effect=KeyError("mvm")):
        with pytest.raises(PrivilegeError, match="does not exist"):
            _require_mvm_group_membership()


@pytest.mark.real_mvm_group_check
def test_require_mvm_group_raises_when_not_member():
    """_require_mvm_group_membership should raise PrivilegeError when user is not in group."""
    import grp
    import pwd

    from mvmctl.exceptions import PrivilegeError
    from mvmctl.utils.process import require_mvm_group_membership as _require_mvm_group_membership

    mock_group = MagicMock()
    mock_group.gr_gid = 1001
    mock_group.gr_mem = ["otheruser"]

    mock_passwd = MagicMock()
    mock_passwd.pw_name = "testuser"
    mock_passwd.pw_gid = 1000

    with patch.object(grp, "getgrnam", return_value=mock_group):
        with patch.object(pwd, "getpwuid", return_value=mock_passwd):
            with patch("mvmctl.utils.process.os.getuid", return_value=1000):
                with pytest.raises(PrivilegeError, match="not in the 'mvm' group"):
                    _require_mvm_group_membership()


@pytest.mark.real_mvm_group_check
def test_require_mvm_group_raises_when_session_not_active():
    """_require_mvm_group_membership should raise PrivilegeError when group not active in session."""
    import grp
    import pwd

    from mvmctl.exceptions import PrivilegeError
    from mvmctl.utils.process import require_mvm_group_membership as _require_mvm_group_membership

    mock_group = MagicMock()
    mock_group.gr_gid = 1001
    mock_group.gr_mem = ["testuser"]

    mock_passwd = MagicMock()
    mock_passwd.pw_name = "testuser"
    mock_passwd.pw_gid = 1000

    with patch.object(grp, "getgrnam", return_value=mock_group):
        with patch.object(pwd, "getpwuid", return_value=mock_passwd):
            with patch("mvmctl.utils.process.os.getuid", return_value=1000):
                with patch("mvmctl.utils.process.os.getgroups", return_value=[]):
                    with patch("mvmctl.utils.process.os.getgid", return_value=1000):
                        with patch("mvmctl.utils.process.os.getegid", return_value=1000):
                            with pytest.raises(
                                PrivilegeError, match="does not have the group active"
                            ):
                                _require_mvm_group_membership()


# ---------------------------------------------------------------------------
# iptables-restore batching (Issue #8)
# ---------------------------------------------------------------------------


@patch("mvmctl.core.network.chain_exists", return_value=False)
def test_build_iptables_restore_input_single_table(mock_chain_exists):
    """_build_iptables_restore_input should format single table rules correctly."""
    from mvmctl.core.network import _build_iptables_restore_input

    rules = [
        {"table": "filter", "chain": "MVM-FORWARD", "rule": "-i eth0 -j ACCEPT"},
        {"table": "filter", "chain": "MVM-FORWARD", "rule": "-o eth0 -j ACCEPT"},
    ]
    result = _build_iptables_restore_input(rules)

    assert "*filter" in result
    assert ":MVM-FORWARD - [0:0]" in result
    assert "-A MVM-FORWARD -i eth0 -j ACCEPT" in result
    assert "-A MVM-FORWARD -o eth0 -j ACCEPT" in result
    assert "COMMIT" in result


@patch("mvmctl.core.network.chain_exists", return_value=False)
def test_build_iptables_restore_input_multiple_tables(mock_chain_exists):
    """_build_iptables_restore_input should group rules by table."""
    from mvmctl.core.network import _build_iptables_restore_input

    rules = [
        {"table": "nat", "chain": "MVM-POSTROUTING", "rule": "-o eth0 -j MASQUERADE"},
        {"table": "filter", "chain": "MVM-FORWARD", "rule": "-i eth0 -j ACCEPT"},
    ]
    result = _build_iptables_restore_input(rules)

    assert "*nat" in result
    assert "*filter" in result
    assert ":MVM-POSTROUTING - [0:0]" in result
    assert ":MVM-FORWARD - [0:0]" in result
    assert result.count("COMMIT") == 2


@patch("mvmctl.core.network.chain_exists", return_value=False)
def test_build_iptables_restore_input_default_table(mock_chain_exists):
    """_build_iptables_restore_input should default to filter table."""
    from mvmctl.core.network import _build_iptables_restore_input

    rules = [{"chain": "MVM-FORWARD", "rule": "-i eth0 -j ACCEPT"}]
    result = _build_iptables_restore_input(rules)

    assert "*filter" in result


def test_build_iptables_restore_input_empty_rules():
    """_build_iptables_restore_input should return empty string for empty rules."""
    from mvmctl.core.network import _build_iptables_restore_input

    result = _build_iptables_restore_input([])
    assert result == "\n"


def test_apply_iptables_rules_batch_success():
    """_apply_iptables_rules_batch should call iptables-restore with correct input."""
    from mvmctl.core.network import _apply_iptables_rules_batch

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("mvmctl.core.network.chain_exists", return_value=False):
            rules = [
                {"table": "filter", "chain": "MVM-FORWARD", "rule": "-i eth0 -j ACCEPT"},
            ]
            _apply_iptables_rules_batch(rules)

        assert mock_run.call_count == 1
        args = mock_run.call_args[0][0]
        assert "iptables-restore" in args
        assert "--noflush" in args


def test_apply_iptables_rules_batch_empty_rules():
    """_apply_iptables_rules_batch should be a no-op for empty rules list."""
    from mvmctl.core.network import _apply_iptables_rules_batch

    with patch("mvmctl.core.network.subprocess.run") as mock_run:
        _apply_iptables_rules_batch([])
        mock_run.assert_not_called()


def test_apply_iptables_rules_batch_failure():
    """_apply_iptables_rules_batch should raise NetworkError on failure."""
    from mvmctl.core.network import _apply_iptables_rules_batch

    with patch(
        "mvmctl.core.network.subprocess.run",
        side_effect=subprocess.CalledProcessError(1, ["iptables-restore"]),
    ):
        with patch("mvmctl.core.network.chain_exists", return_value=False):
            rules = [{"table": "filter", "chain": "MVM-FORWARD", "rule": "-i eth0 -j ACCEPT"}]
            with pytest.raises(NetworkError, match="Failed to apply iptables rules"):
                _apply_iptables_rules_batch(rules)


# ---------------------------------------------------------------------------
# Issue #20: Subprocess Buffering - Standardize ip -batch usage
# ---------------------------------------------------------------------------


def test_setup_bridge_uses_ip_batch_mode():
    """setup_bridge should use ip -batch for atomic bridge creation."""
    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("mvmctl.core.network.bridge_exists", return_value=False):
            setup_bridge("fc-br0", "10.20.0.1/24")

            batch_call = mock_run.call_args_list[0]

            # Should use ip -batch
            args = batch_call[0][0]
            assert "ip" in args
            assert "-batch" in args
            assert "-" in args

            # Verify batch input contains bridge commands
            call_kwargs = batch_call[1]
            input_data = call_kwargs.get("input", "")
            assert "link add name fc-br0 type bridge" in input_data
            assert "addr add" in input_data
            assert "link set fc-br0 up" in input_data


def test_teardown_bridge_uses_ip_batch_mode():
    """teardown_bridge should use ip -batch for atomic bridge removal."""
    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        teardown_bridge("fc-br0")

        # Should use ip -batch
        args = mock_run.call_args[0][0]
        assert "ip" in args
        assert "-batch" in args
        assert "-" in args

        # Verify batch input contains bridge commands
        call_kwargs = mock_run.call_args[1]
        input_data = call_kwargs.get("input", "")
        assert "link set fc-br0 down" in input_data
        assert "link delete fc-br0 type bridge" in input_data


def test_create_tap_uses_ip_batch_mode():
    """create_tap should use ip -batch for atomic TAP creation."""
    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("mvmctl.core.network.tap_exists", return_value=False):
            create_tap("fc-tap0", "fc-br0")

            # Should use ip -batch
            args = mock_run.call_args[0][0]
            assert "ip" in args
            assert "-batch" in args
            assert "-" in args

            # Verify batch input contains TAP commands
            call_kwargs = mock_run.call_args[1]
            input_data = call_kwargs.get("input", "")
            assert "tuntap add dev fc-tap0 mode tap" in input_data
            assert "link set fc-tap0 master fc-br0" in input_data
            assert "link set fc-tap0 up" in input_data


def test_delete_tap_uses_ip_batch_mode():
    """delete_tap should use ip -batch for atomic TAP removal."""
    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("mvmctl.core.network.tap_exists", return_value=True):
            delete_tap("fc-tap0")

            # Should use ip -batch
            args = mock_run.call_args[0][0]
            assert "ip" in args
            assert "-batch" in args
            assert "-" in args

            # Verify batch input contains TAP commands
            call_kwargs = mock_run.call_args[1]
            input_data = call_kwargs.get("input", "")
            assert "link set fc-tap0 down" in input_data
            assert "link delete fc-tap0" in input_data


def test_ip_batch_mode_is_standardized():
    """Verify that all bridge/TAP operations use ip -batch consistently."""
    # This test documents the standardization requirement from Issue #20
    # All network operations that modify state should use ip -batch
    import inspect

    from mvmctl.core.network import (
        create_tap,
        delete_tap,
        setup_bridge,
        teardown_bridge,
    )

    # Get source code of each function
    for func in [setup_bridge, teardown_bridge, create_tap, delete_tap]:
        source = inspect.getsource(func)
        # Each function should use ip -batch directly or via _run_ip_batch helper
        assert "-batch" in source or "_run_ip_batch" in source, (
            f"{func.__name__} should use ip -batch or _run_ip_batch"
        )


# ---------------------------------------------------------------------------
# Issue #8: iptables chain fix - only declare chains that don't exist
# ---------------------------------------------------------------------------


def test_build_iptables_restore_input_declares_new_chain():
    """_build_iptables_restore_input should declare chain when chain_exists returns False."""
    from mvmctl.core.network import _build_iptables_restore_input

    with patch("mvmctl.core.network.chain_exists", return_value=False):
        rules = [
            {"table": "filter", "chain": "MVM-FORWARD", "rule": "-i br0 -o tap0 -j ACCEPT"},
        ]
        result = _build_iptables_restore_input(rules)

        assert ":MVM-FORWARD - [0:0]" in result
        assert "-A MVM-FORWARD -i br0 -o tap0 -j ACCEPT" in result


def test_build_iptables_restore_input_skips_existing_chain():
    """_build_iptables_restore_input should NOT declare chain when chain_exists returns True."""
    from mvmctl.core.network import _build_iptables_restore_input

    with patch("mvmctl.core.network.chain_exists", return_value=True):
        rules = [
            {"table": "filter", "chain": "MVM-FORWARD", "rule": "-i br0 -o tap0 -j ACCEPT"},
        ]
        result = _build_iptables_restore_input(rules)

        assert ":MVM-FORWARD - [0:0]" not in result
        assert "-A MVM-FORWARD -i br0 -o tap0 -j ACCEPT" in result


def test_build_iptables_restore_input_mixed_chains():
    """_build_iptables_restore_input should only declare chains where chain_exists returns False."""
    from mvmctl.core.network import _build_iptables_restore_input

    def chain_exists_side_effect(chain, table="filter"):
        # MVM-POSTROUTING doesn't exist, MVM-FORWARD exists
        if chain == "MVM-POSTROUTING":
            return False
        return True

    with patch("mvmctl.core.network.chain_exists", side_effect=chain_exists_side_effect):
        rules = [
            {"table": "nat", "chain": "MVM-POSTROUTING", "rule": "-o eth0 -j MASQUERADE"},
            {"table": "filter", "chain": "MVM-FORWARD", "rule": "-i br0 -o tap0 -j ACCEPT"},
        ]
        result = _build_iptables_restore_input(rules)

        # MVM-POSTROUTING should be declared (chain didn't exist)
        assert ":MVM-POSTROUTING - [0:0]" in result
        # MVM-FORWARD should NOT be declared (chain already existed)
        assert ":MVM-FORWARD - [0:0]" not in result
        # But both rules should still be present
        assert "-A MVM-POSTROUTING -o eth0 -j MASQUERADE" in result
        assert "-A MVM-FORWARD -i br0 -o tap0 -j ACCEPT" in result


def test_apply_iptables_rules_batch_logs_input():
    """_apply_iptables_rules_batch should log the restore input via logger.debug."""
    from mvmctl.core.network import _apply_iptables_rules_batch

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
        with patch("mvmctl.core.network.logger") as mock_logger:
            rules = [
                {"table": "filter", "chain": "MVM-FORWARD", "rule": "-i br0 -o tap0 -j ACCEPT"},
            ]
            _apply_iptables_rules_batch(rules)

            assert mock_logger.debug.called
            first_call_args = mock_logger.debug.call_args_list[0]
            call_str = str(first_call_args)
            assert "*filter" in call_str


# ---------------------------------------------------------------------------
# validate_network_interface
# ---------------------------------------------------------------------------


class TestValidateNetworkInterface:
    """Tests for validate_network_interface function."""

    def test_reject_loopback(self):
        """validate_network_interface should reject loopback interface."""
        from mvmctl.core.network import validate_network_interface
        from mvmctl.exceptions import NetworkError

        with pytest.raises(NetworkError) as exc_info:
            validate_network_interface("lo")
        assert "loopback" in str(exc_info.value).lower()

    def test_reject_nonexistent_interface(self):
        """validate_network_interface should reject non-existent interface."""
        from mvmctl.core.network import validate_network_interface
        from mvmctl.exceptions import NetworkError

        with patch("pathlib.Path.exists", return_value=False):
            with pytest.raises(NetworkError) as exc_info:
                validate_network_interface("nonexistent0")
            assert "does not exist" in str(exc_info.value)

    def test_reject_down_interface(self):
        """validate_network_interface should reject interface that is down."""
        from mvmctl.core.network import validate_network_interface
        from mvmctl.exceptions import NetworkError

        with patch("pathlib.Path.exists", return_value=True):
            with patch("pathlib.Path.__truediv__") as mock_div:
                mock_div.return_value = MagicMock(
                    read_text=lambda: "down\n",
                )
                with pytest.raises(NetworkError) as exc_info:
                    validate_network_interface("eth0")
                assert "down" in str(exc_info.value).lower()

    def test_reject_interface_without_ip(self):
        """validate_network_interface should reject interface without IP address."""
        from mvmctl.core.network import validate_network_interface
        from mvmctl.exceptions import NetworkError

        mock_result = MagicMock()
        mock_result.returncode = 0
        mock_result.stdout = ""

        with patch("pathlib.Path.exists", return_value=True):
            with patch("pathlib.Path.__truediv__") as mock_div:
                mock_div.return_value = MagicMock(
                    read_text=lambda: "up\n",
                )
                with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
                    with pytest.raises(NetworkError) as exc_info:
                        validate_network_interface("eth0")
                    assert "ipv4 address" in str(exc_info.value).lower()

    def test_accept_valid_interface(self):
        """validate_network_interface should return True for valid interface."""
        from mvmctl.core.network import validate_network_interface

        mock_result = MagicMock()
        mock_result.returncode = 0
        mock_result.stdout = "2: eth0: <BROADCAST,MULTICAST,UP> inet 192.168.1.100/24"

        with patch("pathlib.Path.exists", return_value=True):
            with patch("pathlib.Path.__truediv__") as mock_div:
                mock_div.return_value = MagicMock(
                    read_text=lambda: "up\n",
                )
                with patch("mvmctl.core.network.subprocess.run", return_value=mock_result):
                    result = validate_network_interface("eth0")
                    assert result is True

    def test_ip_command_not_found(self):
        """validate_network_interface should raise NetworkError if ip command missing."""
        from mvmctl.core.network import validate_network_interface
        from mvmctl.exceptions import NetworkError

        with patch("pathlib.Path.exists", return_value=True):
            with patch("pathlib.Path.__truediv__") as mock_div:
                mock_div.return_value = MagicMock(
                    read_text=lambda: "up\n",
                )
                with patch("mvmctl.core.network.subprocess.run", side_effect=FileNotFoundError):
                    with pytest.raises(NetworkError) as exc_info:
                        validate_network_interface("eth0")
                    assert "ip" in str(exc_info.value).lower()
