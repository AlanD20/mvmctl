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
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = bridge_exists("fc-br0")
        assert result is True
        mock_run.assert_called_once()


def test_bridge_exists_false():
    mock_result = MagicMock()
    mock_result.returncode = 1
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
        result = bridge_exists("fc-br0")
        assert result is False


# ---------------------------------------------------------------------------
# generate_mac
# ---------------------------------------------------------------------------


def test_generate_mac_format():
    mac = generate_mac()
    pattern = r"^02:FC:[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}$"
    assert re.match(pattern, mac), f"MAC {mac!r} does not match expected format"


def test_generate_mac_uniqueness():
    macs = {generate_mac() for _ in range(100)}
    assert len(macs) == 100, "Expected 100 unique MAC addresses"


# ---------------------------------------------------------------------------
# allocate_ip
# ---------------------------------------------------------------------------


def test_allocate_ip_basic():
    ip = allocate_ip([], "10.20.0.0/24", "10.20.0.1")
    assert ip == "10.20.0.2"


def test_allocate_ip_skips_gateway():
    ip = allocate_ip([], "10.20.0.0/24", "10.20.0.1")
    assert ip != "10.20.0.1"


def test_allocate_ip_skips_existing():
    ip = allocate_ip(["10.20.0.2", "10.20.0.3"], "10.20.0.0/24", "10.20.0.1")
    assert ip == "10.20.0.4"


def test_allocate_ip_exhausted():
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
    mock_result = MagicMock()
    mock_result.returncode = 1
    mock_result.stdout = ""
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
        result = get_tap_devices("fc-br0")
        assert result == []


def test_get_tap_devices_parses():
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
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = "default via 10.0.0.1 dev eth0 proto dhcp src 10.0.0.5 metric 100\n"
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
        result = get_default_interface()
        assert result == "eth0"


def test_get_default_interface_not_found():
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
    with patch("fcm.core.network.bridge_exists", return_value=True):
        with patch("fcm.core.network.subprocess.run") as mock_run:
            setup_bridge("fc-br0", "10.20.0.1/24")
            mock_run.assert_not_called()


def test_setup_bridge_success():
    with patch("fcm.core.network.bridge_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch.object(Path, "write_text") as mock_write:
                setup_bridge("fc-br0", "10.20.0.1/24")
                assert mock_run.call_count == 3
                mock_write.assert_called_once_with("1\n")


def test_setup_bridge_create_fails():
    with patch("fcm.core.network.bridge_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=[
                subprocess.CalledProcessError(1, ["ip", "link", "add"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to create bridge"):
                setup_bridge("fc-br0", "10.20.0.1/24")


def test_setup_bridge_ip_fails():
    with patch("fcm.core.network.bridge_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=[
                mock_result,
                subprocess.CalledProcessError(1, ["ip", "addr", "add"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to assign IP"):
                setup_bridge("fc-br0", "10.20.0.1/24")


def test_setup_bridge_up_fails():
    with patch("fcm.core.network.bridge_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=[
                mock_result,
                mock_result,
                subprocess.CalledProcessError(1, ["ip", "link", "set"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to bring up bridge"):
                setup_bridge("fc-br0", "10.20.0.1/24")


def test_setup_bridge_ip_forward_fails():
    with patch("fcm.core.network.bridge_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result):
            with patch.object(Path, "write_text", side_effect=OSError("Permission denied")):
                with pytest.raises(NetworkError, match="Failed to enable IP forwarding"):
                    setup_bridge("fc-br0", "10.20.0.1/24")


# ---------------------------------------------------------------------------
# teardown_bridge
# ---------------------------------------------------------------------------


def test_teardown_bridge_success():
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        teardown_bridge("fc-br0")
        assert mock_run.call_count == 2


def test_teardown_bridge_down_fails():
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=subprocess.CalledProcessError(1, ["ip", "link", "set"]),
    ):
        with pytest.raises(NetworkError, match="Failed to bring down bridge"):
            teardown_bridge("fc-br0")


def test_teardown_bridge_delete_fails():
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            mock_result,
            subprocess.CalledProcessError(1, ["ip", "link", "delete"]),
        ],
    ):
        with pytest.raises(NetworkError, match="Failed to delete bridge"):
            teardown_bridge("fc-br0")


# ---------------------------------------------------------------------------
# setup_nat
# ---------------------------------------------------------------------------


def test_setup_nat_all_rules_exist():
    mock_check = MagicMock()
    mock_check.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_check) as mock_run:
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            setup_nat("fc-br0", "eth0")
            # 3 check calls, no add calls
            assert mock_run.call_count == 3


def test_setup_nat_no_rules_exist():
    mock_check = MagicMock()
    mock_check.returncode = 1
    mock_add = MagicMock()
    mock_add.returncode = 0
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[mock_check, mock_add, mock_check, mock_add, mock_check, mock_add],
    ) as mock_run:
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            setup_nat("fc-br0", "eth0")
            # 3 checks + 3 adds = 6 calls
            assert mock_run.call_count == 6


def test_setup_nat_masquerade_add_fails():
    mock_check = MagicMock()
    mock_check.returncode = 1
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            mock_check,
            subprocess.CalledProcessError(1, ["iptables", "-t", "nat", "-A"]),
        ],
    ):
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            with pytest.raises(NetworkError, match="Failed to add MASQUERADE rule"):
                setup_nat("fc-br0", "eth0")


def test_setup_nat_forward_bridge_to_host_fails():
    mock_check = MagicMock()
    mock_check.returncode = 1
    mock_add = MagicMock()
    mock_add.returncode = 0
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            mock_check,
            mock_add,
            mock_check,
            subprocess.CalledProcessError(1, ["iptables", "-A", "FORWARD"]),
        ],
    ):
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            with pytest.raises(NetworkError, match="Failed to add FORWARD rule bridge"):
                setup_nat("fc-br0", "eth0")


def test_setup_nat_forward_host_to_bridge_fails():
    mock_check = MagicMock()
    mock_check.returncode = 1
    mock_add = MagicMock()
    mock_add.returncode = 0
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            mock_check,
            mock_add,
            mock_check,
            mock_add,
            mock_check,
            subprocess.CalledProcessError(1, ["iptables", "-A", "FORWARD"]),
        ],
    ):
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            with pytest.raises(NetworkError, match="Failed to add FORWARD rule host"):
                setup_nat("fc-br0", "eth0")


def test_setup_nat_auto_detect_interface():
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
    with patch("fcm.core.network.get_tap_devices", return_value=["tap0"]):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                teardown_nat("fc-br0", force=True)
                mock_run.assert_called_once()


def test_teardown_nat_tap_devices_present_skips():
    with patch("fcm.core.network.get_tap_devices", return_value=["tap0"]) as mock_get_taps:
        with patch("fcm.core.network.subprocess.run") as mock_run:
            teardown_nat("fc-br0", force=False)
            mock_get_taps.assert_called_once_with("fc-br0")
            mock_run.assert_not_called()


def test_teardown_nat_no_taps_removes():
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                teardown_nat("fc-br0", force=False)
                mock_run.assert_called_once()


def test_teardown_nat_called_process_error():
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["iptables", "-t", "nat", "-D"]),
        ):
            with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                with pytest.raises(NetworkError, match="Failed to remove MASQUERADE rule"):
                    teardown_nat("fc-br0", force=True)


def test_teardown_nat_get_default_interface_fails_gracefully():
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
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = tap_exists("fc-vm1-0")
        assert result is True
        mock_run.assert_called_once()


def test_tap_exists_false():
    mock_result = MagicMock()
    mock_result.returncode = 1
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
        result = tap_exists("fc-vm1-0")
        assert result is False


# ---------------------------------------------------------------------------
# create_tap
# ---------------------------------------------------------------------------


def test_create_tap_already_exists():
    with patch("fcm.core.network.tap_exists", return_value=True):
        with pytest.raises(NetworkError, match="TAP device .* already exists"):
            create_tap("fc-vm1-0", "fc-br0")


def test_create_tap_success():
    with patch("fcm.core.network.tap_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            create_tap("fc-vm1-0", "fc-br0")
            assert mock_run.call_count == 3


def test_create_tap_create_fails():
    with patch("fcm.core.network.tap_exists", return_value=False):
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["ip", "tuntap", "add"]),
        ):
            with pytest.raises(NetworkError, match="Failed to create TAP"):
                create_tap("fc-vm1-0", "fc-br0")


def test_create_tap_attach_fails():
    with patch("fcm.core.network.tap_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=[
                mock_result,
                subprocess.CalledProcessError(1, ["ip", "link", "set"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to attach TAP"):
                create_tap("fc-vm1-0", "fc-br0")


def test_create_tap_up_fails():
    with patch("fcm.core.network.tap_exists", return_value=False):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=[
                mock_result,
                mock_result,
                subprocess.CalledProcessError(1, ["ip", "link", "set"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to bring up TAP"):
                create_tap("fc-vm1-0", "fc-br0")


# ---------------------------------------------------------------------------
# delete_tap
# ---------------------------------------------------------------------------


def test_delete_tap_does_not_exist():
    with patch("fcm.core.network.tap_exists", return_value=False):
        with patch("fcm.core.network.subprocess.run") as mock_run:
            delete_tap("fc-vm1-0")
            mock_run.assert_not_called()


def test_delete_tap_success():
    with patch("fcm.core.network.tap_exists", return_value=True):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            delete_tap("fc-vm1-0")
            assert mock_run.call_count == 2


def test_delete_tap_down_fails():
    with patch("fcm.core.network.tap_exists", return_value=True):
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["ip", "link", "set"]),
        ):
            with pytest.raises(NetworkError, match="Failed to bring down TAP"):
                delete_tap("fc-vm1-0")


def test_delete_tap_delete_fails():
    with patch("fcm.core.network.tap_exists", return_value=True):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=[
                mock_result,
                subprocess.CalledProcessError(1, ["ip", "link", "delete"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to delete TAP"):
                delete_tap("fc-vm1-0")


# ---------------------------------------------------------------------------
# add_iptables_forward_rules
# ---------------------------------------------------------------------------


def test_add_iptables_forward_rules_already_exist():
    mock_check = MagicMock()
    mock_check.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_check) as mock_run:
        add_iptables_forward_rules("fc-vm1-0", "fc-br0")
        # 2 checks, no adds
        assert mock_run.call_count == 2


def test_add_iptables_forward_rules_add_success():
    mock_check = MagicMock()
    mock_check.returncode = 1
    mock_add = MagicMock()
    mock_add.returncode = 0
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[mock_check, mock_add, mock_check, mock_add],
    ) as mock_run:
        add_iptables_forward_rules("fc-vm1-0", "fc-br0")
        # 2 checks + 2 adds = 4 calls
        assert mock_run.call_count == 4


def test_add_iptables_forward_rules_bridge_to_tap_fails():
    mock_check = MagicMock()
    mock_check.returncode = 1
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            mock_check,
            subprocess.CalledProcessError(1, ["iptables", "-A", "FORWARD"]),
        ],
    ):
        with pytest.raises(NetworkError, match="Failed to add FORWARD rule"):
            add_iptables_forward_rules("fc-vm1-0", "fc-br0")


def test_add_iptables_forward_rules_tap_to_bridge_fails():
    mock_check = MagicMock()
    mock_check.returncode = 1
    mock_add = MagicMock()
    mock_add.returncode = 0
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            mock_check,
            mock_add,
            mock_check,
            subprocess.CalledProcessError(1, ["iptables", "-A", "FORWARD"]),
        ],
    ):
        with pytest.raises(NetworkError, match="Failed to add FORWARD rule"):
            add_iptables_forward_rules("fc-vm1-0", "fc-br0")


# ---------------------------------------------------------------------------
# remove_iptables_forward_rules
# ---------------------------------------------------------------------------


def test_remove_iptables_forward_rules_success():
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
        assert mock_run.call_count == 2


def test_remove_iptables_forward_rules_already_absent():
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[
            subprocess.CompletedProcess(["iptables", "-D", "FORWARD"], returncode=1),
            subprocess.CompletedProcess(["iptables", "-D", "FORWARD"], returncode=1),
        ],
    ) as mock_run:
        remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
        # Should not raise, just log
        assert mock_run.call_count == 2
