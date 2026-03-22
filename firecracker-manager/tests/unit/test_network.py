"""Tests for core/network.py."""

import re
import ipaddress
from unittest.mock import MagicMock, patch

import pytest

from fcm.core.network import (
    allocate_ip,
    bridge_exists,
    generate_mac,
    get_default_interface,
    get_tap_devices,
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
