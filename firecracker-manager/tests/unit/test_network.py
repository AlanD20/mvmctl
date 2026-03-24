"""Tests for core/network.py."""

import re
import subprocess
import ipaddress
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from fcm.core.network import (
    add_iptables_forward_rules,
    allocate_ip,
    bridge_exists,
    create_tap,
    delete_tap,
    generate_mac,
    get_default_interface,
    get_tap_devices,
    remove_iptables_forward_rules,
    setup_bridge,
    setup_nat,
    tap_exists,
    teardown_bridge,
    teardown_nat,
)
from fcm.exceptions import NetworkError


# ---------------------------------------------------------------------------
# bridge_exists
# ---------------------------------------------------------------------------


def test_bridge_exists_true():
    """bridge_exists should return True when the ip command exits with code 0."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = bridge_exists("fc-br0")
        assert result is True
        mock_run.assert_called_once()


def test_bridge_exists_false():
    """bridge_exists should return False when the ip command exits with a non-zero code."""
    mock_result = MagicMock()
    mock_result.returncode = 1
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
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
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
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
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
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
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
        result = get_default_interface()
        assert result == "eth0"


def test_get_default_interface_not_found():
    """get_default_interface should raise NetworkError when no default route is found."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = ""
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
        with pytest.raises(NetworkError):
            get_default_interface()


# ---------------------------------------------------------------------------
# setup_bridge
# ---------------------------------------------------------------------------


def test_setup_bridge_already_exists():
    """setup_bridge should skip creation and not call subprocess when the bridge already exists."""
    with patch("fcm.core.network.bridge_exists", return_value=True):
        with patch("fcm.core.network.subprocess.run") as mock_run:
            setup_bridge("fc-br0", "10.20.0.1/24")
            mock_run.assert_not_called()


def test_setup_bridge_success():
    """setup_bridge should create the bridge and enable IP forwarding when it does not exist."""
    with patch("fcm.core.network.bridge_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch.object(Path, "write_text") as mock_write:
                setup_bridge("fc-br0", "10.20.0.1/24")
                assert mock_run.call_count == 1
                mock_write.assert_called_once_with("1\n")


def test_setup_bridge_create_fails():
    """setup_bridge should raise NetworkError when the ip command fails to create the bridge."""
    with patch("fcm.core.network.bridge_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=[
                subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to setup bridge"):
                setup_bridge("fc-br0", "10.20.0.1/24")


def test_setup_bridge_ip_forward_fails():
    with patch("fcm.core.network.bridge_exists", return_value=False):

        def _run_side_effect(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            cmd_flat = cmd if isinstance(cmd, list) else list(cmd)
            if "sysctl" in str(cmd_flat):
                raise subprocess.CalledProcessError(1, cmd_flat)
            result = MagicMock()
            result.returncode = 0
            return result

        with patch("fcm.core.network.subprocess.run", side_effect=_run_side_effect):
            with patch.object(Path, "write_text", side_effect=OSError("Permission denied")):
                with pytest.raises(NetworkError, match="Failed to enable IP forwarding"):
                    setup_bridge("fc-br0", "10.20.0.1/24")


# ---------------------------------------------------------------------------
# teardown_bridge
# ---------------------------------------------------------------------------


def test_teardown_bridge_success():
    """teardown_bridge should call subprocess once to bring down and delete the bridge."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        teardown_bridge("fc-br0")
        assert mock_run.call_count == 1


def test_teardown_bridge_down_fails():
    """teardown_bridge should raise NetworkError when the ip command fails."""
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
    ):
        with pytest.raises(NetworkError, match="Failed to teardown bridge"):  # Updated match
            teardown_bridge("fc-br0")


def test_setup_nat_all_rules_exist():
    """setup_nat should not add rules when all iptables rules already exist."""
    mock_check = MagicMock()
    mock_check.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_check) as mock_run:
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            setup_nat("fc-br0", "eth0")
            # 3 check calls (one per _ensure_iptables_rule), no add calls
            assert mock_run.call_count == 3


def test_setup_nat_no_rules_exist():
    """setup_nat should add iptables rules when none currently exist."""
    mock_result = MagicMock()
    mock_result.returncode = 1  # check says rule doesn't exist
    with patch(
        "fcm.core.network.subprocess.run",
        return_value=mock_result,
    ) as mock_run:
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            setup_nat("fc-br0", "eth0")
            # 3 checks + 3 adds = 6 calls
            assert mock_run.call_count == 6


def test_setup_nat_masquerade_add_fails():
    """setup_nat should raise NetworkError when the iptables MASQUERADE rule cannot be added."""
    check_result = MagicMock()
    check_result.returncode = 1
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            check_result,
            subprocess.CalledProcessError(1, ["iptables", "-t", "nat", "-A"]),
        ],
    ):
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            with pytest.raises(NetworkError, match="Failed to setup NAT"):
                setup_nat("fc-br0", "eth0")


def test_setup_nat_auto_detect_interface():
    """setup_nat should auto-detect the default interface when none is supplied."""
    mock_check = MagicMock()
    mock_check.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_check):
        with patch("fcm.core.network.get_default_interface", return_value="eth0") as mock_get_iface:
            setup_nat("fc-br0")
            mock_get_iface.assert_called_once()


# ---------------------------------------------------------------------------
# teardown_nat
# ---------------------------------------------------------------------------


def test_teardown_nat_force_true_removes():
    """teardown_nat with force=True should remove MASQUERADE + FORWARD rules."""
    with patch("fcm.core.network.get_tap_devices", return_value=["tap0"]):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                teardown_nat("fc-br0", force=True)
                assert mock_run.call_count == 3


def test_teardown_nat_tap_devices_present_skips():
    """teardown_nat with force=False should skip removal when TAP devices are still present."""
    with patch("fcm.core.network.get_tap_devices", return_value=["tap0"]) as mock_get_taps:
        with patch("fcm.core.network.subprocess.run") as mock_run:
            teardown_nat("fc-br0", force=False)
            mock_get_taps.assert_called_once_with("fc-br0")
            mock_run.assert_not_called()


def test_teardown_nat_no_taps_removes():
    """teardown_nat should remove MASQUERADE + bridge FORWARD rules when no TAPs remain."""
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                teardown_nat("fc-br0", force=False)
                assert mock_run.call_count == 3


def test_teardown_nat_removes_forward_rules_for_correct_bridge():
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("fcm.core.network.get_default_interface", return_value="wlan0"):
                teardown_nat("fcm-default", force=True)
        calls = [str(c) for c in mock_run.call_args_list]
        assert any("MASQUERADE" in c for c in calls)
        assert any("fcm-default" in c and "wlan0" in c for c in calls)


def test_teardown_nat_called_process_error():
    """teardown_nat should raise NetworkError when the iptables deletion command fails."""
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["iptables", "-t", "nat", "-D"]),
        ):
            with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                with pytest.raises(NetworkError, match="Failed to remove MASQUERADE rule"):
                    teardown_nat("fc-br0", force=True)


def test_teardown_nat_get_default_interface_fails_gracefully():
    """teardown_nat should silently skip removal when the default interface cannot be determined."""
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        with patch(
            "fcm.core.network.get_default_interface",
            side_effect=NetworkError("No default interface"),
        ):
            with patch("fcm.core.network.subprocess.run") as mock_run:
                teardown_nat("fc-br0", force=True)
                mock_run.assert_not_called()


# ---------------------------------------------------------------------------
# tap_exists
# ---------------------------------------------------------------------------


def test_tap_exists_true():
    """tap_exists should return True when the ip command exits with code 0."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = tap_exists("fc-vm1-0")
        assert result is True
        mock_run.assert_called_once()


def test_tap_exists_false():
    """tap_exists should return False when the ip command exits with a non-zero code."""
    mock_result = MagicMock()
    mock_result.returncode = 1
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
        result = tap_exists("fc-vm1-0")
        assert result is False


# ---------------------------------------------------------------------------
# create_tap
# ---------------------------------------------------------------------------


def test_create_tap_already_exists():
    """create_tap should raise NetworkError when the TAP device already exists."""
    with patch("fcm.core.network.tap_exists", return_value=True):
        with pytest.raises(NetworkError, match="TAP device .* already exists"):
            create_tap("fc-vm1-0", "fc-br0")


def test_create_tap_success():
    """create_tap should call subprocess once to create and attach the TAP device to the bridge."""
    with patch("fcm.core.network.tap_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            create_tap("fc-vm1-0", "fc-br0")
            assert mock_run.call_count == 1


def test_create_tap_create_fails():
    """create_tap should raise NetworkError when the ip command fails to create the TAP device."""
    with patch("fcm.core.network.tap_exists", return_value=False):
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
        ):
            with pytest.raises(NetworkError, match="Failed to create TAP"):  # Updated match
                create_tap("fc-vm1-0", "fc-br0")


def test_delete_tap_does_not_exist():
    """delete_tap should be a no-op when the TAP device does not exist."""
    with patch("fcm.core.network.tap_exists", return_value=False):
        with patch("fcm.core.network.subprocess.run") as mock_run:
            delete_tap("fc-vm1-0")
            mock_run.assert_not_called()


def test_delete_tap_success():
    """delete_tap should call subprocess once to delete an existing TAP device."""
    with patch("fcm.core.network.tap_exists", return_value=True):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            delete_tap("fc-vm1-0")
            assert mock_run.call_count == 1


def test_delete_tap_down_fails():
    """delete_tap should raise NetworkError when the ip command fails to delete the TAP device."""
    with patch("fcm.core.network.tap_exists", return_value=True):
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
        ):
            with pytest.raises(NetworkError, match="Failed to delete TAP"):  # Updated match
                delete_tap("fc-vm1-0")


def test_add_iptables_forward_rules_already_exist():
    """add_iptables_forward_rules should not add rules when they already exist."""
    mock_check = MagicMock()
    mock_check.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_check) as mock_run:
        add_iptables_forward_rules("fc-vm1-0", "fc-br0")
        assert mock_run.call_count == 2


def test_add_iptables_forward_rules_add_success():
    """add_iptables_forward_rules should add rules when they are absent."""
    mock_result = MagicMock()
    mock_result.returncode = 1  # check says rule doesn't exist
    with patch(
        "fcm.core.network.subprocess.run",
        return_value=mock_result,
    ) as mock_run:
        add_iptables_forward_rules("fc-vm1-0", "fc-br0")
        # 2 checks + 2 adds = 4 calls
        assert mock_run.call_count == 4


def test_add_iptables_forward_rules_bridge_to_tap_fails():
    """add_iptables_forward_rules should raise NetworkError when the iptables command fails."""
    check_result = MagicMock()
    check_result.returncode = 1
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            check_result,
            subprocess.CalledProcessError(1, ["iptables", "-A", "FORWARD"]),
        ],
    ):
        with pytest.raises(NetworkError, match="Failed to add FORWARD rules"):
            add_iptables_forward_rules("fc-vm1-0", "fc-br0")


def test_remove_iptables_forward_rules_success():
    """remove_iptables_forward_rules should call subprocess twice to delete FORWARD rules."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
        assert mock_run.call_count == 2


def test_remove_iptables_forward_rules_already_absent():
    """remove_iptables_forward_rules should not raise when the rules are already absent."""
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            subprocess.CompletedProcess(["iptables", "-D", "FORWARD"], returncode=1),
            subprocess.CompletedProcess(["iptables", "-D", "FORWARD"], returncode=1),
        ],
    ) as mock_run:
        remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
        assert mock_run.call_count == 2
