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
    chain_exists,
    create_tap,
    delete_tap,
    generate_mac,
    get_default_interface,
    _run_ip_batch,
    get_tap_devices,
    remove_iptables_forward_rules,
    setup_bridge,
    setup_fcm_chains,
    setup_nat,
    tap_exists,
    teardown_bridge,
    teardown_fcm_chains,
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


def test_get_default_interface_error_message_sanitized():
    with patch(
        "fcm.core.network.subprocess.run",
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
                with patch(
                    "fcm.core.network.os.getuid", return_value=0
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
    """setup_nat should apply rules via iptables-restore (idempotent via --noflush)."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            with patch("fcm.core.network.setup_fcm_chains"):
                setup_nat("fc-br0", "eth0")
                # Single iptables-restore call for all rules
                assert mock_run.call_count == 1
                args = mock_run.call_args[0][0]
                assert "iptables-restore" in args


def test_setup_nat_no_rules_exist():
    """setup_nat should apply all rules in a single iptables-restore call."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch(
        "fcm.core.network.subprocess.run",
        return_value=mock_result,
    ) as mock_run:
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            with patch("fcm.core.network.setup_fcm_chains"):
                setup_nat("fc-br0", "eth0")
                # Single iptables-restore call for all rules
                assert mock_run.call_count == 1
                args = mock_run.call_args[0][0]
                assert "iptables-restore" in args


def test_setup_nat_masquerade_add_fails():
    """setup_nat should raise NetworkError when iptables-restore fails."""
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=subprocess.CalledProcessError(1, ["iptables-restore", "--noflush"]),
    ):
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            with patch("fcm.core.network.setup_fcm_chains"):
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
                with patch("fcm.core.network.chain_exists", return_value=True):
                    teardown_nat("fc-br0", force=True)
                    # 3 rule deletions = 3 calls
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
                with patch("fcm.core.network.chain_exists", return_value=True):
                    teardown_nat("fc-br0", force=False)
                    # 3 rule deletions = 3 calls
                    assert mock_run.call_count == 3


def test_teardown_nat_removes_forward_rules_for_correct_bridge():
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        mock_result = MagicMock()
        mock_result.returncode = 0
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("fcm.core.network.get_default_interface", return_value="wlan0"):
                with patch("fcm.core.network.chain_exists", return_value=True):
                    teardown_nat("fcm-default", force=True)
        calls = [str(c) for c in mock_run.call_args_list]
        assert any("MASQUERADE" in c for c in calls)
        assert any("fcm-default" in c and "wlan0" in c for c in calls)


def test_teardown_nat_called_process_error():
    """teardown_nat should raise NetworkError when the iptables deletion command fails."""
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        with patch("fcm.core.network.chain_exists", return_value=True):
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
            assert mock_run.call_args.kwargs["input"] == (
                "tuntap add dev fc-vm1-0 mode tap\n"
                "link set fc-vm1-0 master fc-br0\n"
                "link set fc-vm1-0 up\n"
            )


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
            assert mock_run.call_args.kwargs["input"] == (
                "link set fc-vm1-0 down\nlink delete fc-vm1-0\n"
            )


def test_run_ip_batch_uses_ip_batch_mode():
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("fcm.core.network.os.getuid", return_value=0):
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
    with patch("fcm.core.network.tap_exists", return_value=True):
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["ip", "-batch", "-"]),
        ):
            with pytest.raises(NetworkError, match="Failed to delete TAP"):  # Updated match
                delete_tap("fc-vm1-0")


def test_add_iptables_forward_rules_already_exist():
    """add_iptables_forward_rules should apply rules via iptables-restore (idempotent)."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("fcm.core.network.setup_fcm_chains"):
            add_iptables_forward_rules("fc-vm1-0", "fc-br0")
            # Single iptables-restore call for all rules
            assert mock_run.call_count == 1
            args = mock_run.call_args[0][0]
            assert "iptables-restore" in args


def test_add_iptables_forward_rules_add_success():
    """add_iptables_forward_rules should apply all rules in a single iptables-restore call."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch(
        "fcm.core.network.subprocess.run",
        return_value=mock_result,
    ) as mock_run:
        with patch("fcm.core.network.setup_fcm_chains"):
            add_iptables_forward_rules("fc-vm1-0", "fc-br0")
            # Single iptables-restore call for all rules
            assert mock_run.call_count == 1
            args = mock_run.call_args[0][0]
            assert "iptables-restore" in args


def test_add_iptables_forward_rules_bridge_to_tap_fails():
    """add_iptables_forward_rules should raise NetworkError when iptables-restore fails."""
    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=subprocess.CalledProcessError(1, ["iptables-restore", "--noflush"]),
    ):
        with patch("fcm.core.network.setup_fcm_chains"):
            with pytest.raises(NetworkError, match="Failed to add FORWARD rules"):
                add_iptables_forward_rules("fc-vm1-0", "fc-br0")


def test_remove_iptables_forward_rules_success():
    """remove_iptables_forward_rules should call subprocess twice to delete FORWARD rules."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("fcm.core.network.chain_exists", return_value=True):
            remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
            # 2 rule deletions = 2 calls
            assert mock_run.call_count == 2


def test_remove_iptables_forward_rules_already_absent():
    """remove_iptables_forward_rules should not raise when the rules are already absent."""
    with patch("fcm.core.network.chain_exists", return_value=True):
        with patch(
            "fcm.core.network.subprocess.run",
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
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = chain_exists("FCM-FORWARD", "filter")
        assert result is True
        mock_run.assert_called_once()
        args = mock_run.call_args[0][0]
        assert "-L" in args
        assert "FCM-FORWARD" in args


def test_chain_exists_false():
    """chain_exists should return False when the iptables chain does not exist."""
    mock_result = MagicMock()
    mock_result.returncode = 1
    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
        result = chain_exists("FCM-FORWARD", "filter")
        assert result is False


def test_chain_exists_nat_table():
    """chain_exists should work with nat table."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        result = chain_exists("FCM-POSTROUTING", "nat")
        assert result is True
        args = mock_run.call_args[0][0]
        assert "-t" in args
        assert "nat" in args


# ---------------------------------------------------------------------------
# setup_fcm_chains
# ---------------------------------------------------------------------------


def test_setup_fcm_chains_creates_both_chains():
    """setup_fcm_chains should create both FORWARD and POSTROUTING chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.chain_exists", return_value=False):
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("fcm.core.network._iptables_rule_exists", return_value=False):
                setup_fcm_chains()
                # 2 chain creations + 2 jump rule additions = 4 calls
                assert mock_run.call_count == 4


def test_setup_fcm_chains_idempotent():
    """setup_fcm_chains should not recreate chains that already exist."""
    with patch("fcm.core.network.chain_exists", return_value=True):
        with patch("fcm.core.network._iptables_rule_exists", return_value=True):
            with patch("fcm.core.network.subprocess.run") as mock_run:
                setup_fcm_chains()
                mock_run.assert_not_called()


def test_setup_fcm_chains_adds_jump_rules():
    """setup_fcm_chains should add jump rules from built-in chains to FCM chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.chain_exists", return_value=True):
        with patch("fcm.core.network._iptables_rule_exists", return_value=False):
            with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
                setup_fcm_chains()
                # 2 jump rules (FORWARD -> FCM-FORWARD, POSTROUTING -> FCM-POSTROUTING)
                assert mock_run.call_count == 2


def test_setup_fcm_chains_raises_on_failure():
    """setup_fcm_chains should raise NetworkError when chain creation fails."""
    with patch("fcm.core.network.chain_exists", return_value=False):
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, ["iptables", "-N"]),
        ):
            with pytest.raises(NetworkError, match="Failed to create FCM-FORWARD chain"):
                setup_fcm_chains()


# ---------------------------------------------------------------------------
# teardown_fcm_chains
# ---------------------------------------------------------------------------


def test_teardown_fcm_chains_removes_both_chains():
    """teardown_fcm_chains should remove both FORWARD and POSTROUTING chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.chain_exists", return_value=True):
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            teardown_fcm_chains()
            # 2 jump removals + 2 flushes + 2 deletions = 6 calls
            assert mock_run.call_count == 6


def test_teardown_fcm_chains_safe_when_missing():
    """teardown_fcm_chains should be safe when chains don't exist."""
    with patch("fcm.core.network.chain_exists", return_value=False):
        with patch("fcm.core.network.subprocess.run") as mock_run:
            teardown_fcm_chains()
            mock_run.assert_not_called()


def test_teardown_fcm_chains_raises_on_flush_failure():
    """teardown_fcm_chains should raise NetworkError when chain flush fails."""
    with patch("fcm.core.network.chain_exists", return_value=True):
        mock_success = MagicMock()
        mock_success.returncode = 0
        with patch(
            "fcm.core.network.subprocess.run",
            side_effect=[
                # Jump removal from FORWARD (check=False, ignored)
                mock_success,
                # Flush FCM-FORWARD chain - fails
                subprocess.CalledProcessError(1, ["iptables", "-F"]),
            ],
        ):
            with pytest.raises(NetworkError, match="Failed to remove FCM-FORWARD chain"):
                teardown_fcm_chains()


# ---------------------------------------------------------------------------
# setup_nat with FCM chains
# ---------------------------------------------------------------------------


def test_setup_nat_calls_setup_fcm_chains():
    """setup_nat should call setup_fcm_chains to ensure chains exist."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.setup_fcm_chains") as mock_setup_chains:
        with patch("fcm.core.network.subprocess.run", return_value=mock_result):
            with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                with patch("fcm.core.network._iptables_rule_exists", return_value=True):
                    setup_nat("fc-br0", "eth0")
                    mock_setup_chains.assert_called_once()


def test_setup_nat_adds_rules_to_fcm_chains():
    """setup_nat should add rules to FCM chains, not built-in chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.setup_fcm_chains"):
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                with patch("fcm.core.network._iptables_rule_exists", return_value=False):
                    setup_nat("fc-br0", "eth0")
                    # Check that rules are added to FCM chains
                    calls = [str(c) for c in mock_run.call_args_list]
                    assert any("FCM-POSTROUTING" in c for c in calls)
                    assert any("FCM-FORWARD" in c for c in calls)


# ---------------------------------------------------------------------------
# teardown_nat with FCM chains
# ---------------------------------------------------------------------------


def test_teardown_nat_skips_when_chains_missing():
    """teardown_nat should skip when FCM chains don't exist."""
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        with patch("fcm.core.network.chain_exists", return_value=False):
            with patch("fcm.core.network.subprocess.run") as mock_run:
                with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                    teardown_nat("fc-br0", force=True)
                    mock_run.assert_not_called()


def test_teardown_nat_removes_rules_from_fcm_chains():
    """teardown_nat should remove rules from FCM chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.get_tap_devices", return_value=[]):
        with patch("fcm.core.network.chain_exists", return_value=True):
            with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
                with patch("fcm.core.network.get_default_interface", return_value="eth0"):
                    teardown_nat("fc-br0", force=True)
                    calls = [str(c) for c in mock_run.call_args_list]
                    assert any("FCM-POSTROUTING" in c for c in calls)
                    assert any("FCM-FORWARD" in c for c in calls)


# ---------------------------------------------------------------------------
# add_iptables_forward_rules with FCM chains
# ---------------------------------------------------------------------------


def test_add_iptables_forward_rules_calls_setup_chains():
    """add_iptables_forward_rules should call setup_fcm_chains."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.setup_fcm_chains") as mock_setup_chains:
        with patch("fcm.core.network.subprocess.run", return_value=mock_result):
            with patch("fcm.core.network._iptables_rule_exists", return_value=True):
                add_iptables_forward_rules("fc-vm1-0", "fc-br0")
                mock_setup_chains.assert_called_once()


def test_add_iptables_forward_rules_uses_fcm_chain():
    """add_iptables_forward_rules should add rules to FCM-FORWARD chain."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.setup_fcm_chains"):
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            with patch("fcm.core.network._iptables_rule_exists", return_value=False):
                add_iptables_forward_rules("fc-vm1-0", "fc-br0")
                calls = [str(c) for c in mock_run.call_args_list]
                assert all("FCM-FORWARD" in c for c in calls)


# ---------------------------------------------------------------------------
# remove_iptables_forward_rules with FCM chains
# ---------------------------------------------------------------------------


def test_remove_iptables_forward_rules_skips_when_chain_missing():
    """remove_iptables_forward_rules should skip when FCM chain doesn't exist."""
    with patch("fcm.core.network.chain_exists", return_value=False):
        with patch("fcm.core.network.subprocess.run") as mock_run:
            remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
            mock_run.assert_not_called()


def test_remove_iptables_forward_rules_uses_fcm_chain():
    """remove_iptables_forward_rules should remove rules from FCM-FORWARD chain."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    with patch("fcm.core.network.chain_exists", return_value=True):
        with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
            remove_iptables_forward_rules("fc-vm1-0", "fc-br0")
            calls = [str(c) for c in mock_run.call_args_list]
            assert all("FCM-FORWARD" in c for c in calls)


# ---------------------------------------------------------------------------
# Sudo credential caching (Issue #10)
# ---------------------------------------------------------------------------


def test_sudo_credentials_cached_after_validation():
    """Sudo credentials should be cached after successful validation."""
    import fcm.core.network as network_module

    network_module._SUDO_CREDENTIALS_VALID = False
    network_module._SUDO_CACHE_TIMESTAMP = 0.0

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("fcm.core.network.subprocess.run", return_value=mock_result):
        result = network_module._validate_sudo_credentials()
        assert result is True
        assert network_module._is_sudo_cached() is True

    network_module._SUDO_CREDENTIALS_VALID = False
    network_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_cache_reduces_validation_calls():
    """Multiple calls should only trigger one sudo validation."""
    import fcm.core.network as network_module

    network_module._SUDO_CREDENTIALS_VALID = False
    network_module._SUDO_CACHE_TIMESTAMP = 0.0

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        network_module._validate_sudo_credentials()
        assert mock_run.call_count == 1

        mock_run.reset_mock()
        network_module._validate_sudo_credentials()
        assert mock_run.call_count == 0

    network_module._SUDO_CREDENTIALS_VALID = False
    network_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_cache_expires_after_ttl():
    """Sudo credentials should expire after TTL period."""
    import fcm.core.network as network_module

    network_module._SUDO_CREDENTIALS_VALID = True
    network_module._SUDO_CACHE_TIMESTAMP = 0.0

    assert network_module._is_sudo_cached() is False

    network_module._SUDO_CREDENTIALS_VALID = False
    network_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_uses_sudo_n_for_non_interactive_check():
    """Sudo validation should use 'sudo -n true' for non-interactive check."""
    import fcm.core.network as network_module

    network_module._SUDO_CREDENTIALS_VALID = False
    network_module._SUDO_CACHE_TIMESTAMP = 0.0

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        network_module._validate_sudo_credentials()

        calls = mock_run.call_args_list
        assert len(calls) == 1
        assert calls[0][0][0] == ["sudo", "-n", "true"]

    network_module._SUDO_CREDENTIALS_VALID = False
    network_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_uses_sudo_v_when_not_cached():
    """Sudo validation should use 'sudo -v' when credentials are not cached."""
    import fcm.core.network as network_module

    network_module._SUDO_CREDENTIALS_VALID = False
    network_module._SUDO_CACHE_TIMESTAMP = 0.0

    not_cached_result = MagicMock(returncode=1)
    cached_result = MagicMock(returncode=0)

    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=[not_cached_result, cached_result],
    ) as mock_run:
        network_module._validate_sudo_credentials()

        calls = mock_run.call_args_list
        assert len(calls) == 2
        assert calls[0][0][0] == ["sudo", "-n", "true"]
        assert calls[1][0][0] == ["sudo", "-v"]

    network_module._SUDO_CREDENTIALS_VALID = False
    network_module._SUDO_CACHE_TIMESTAMP = 0.0


def test_sudo_anti_recursion_protection():
    """Sudo validation should have anti-recursion protection."""
    import fcm.core.network as network_module

    original_state = network_module._SUDO_VALIDATION_IN_PROGRESS
    network_module._SUDO_VALIDATION_IN_PROGRESS = True

    try:
        result = network_module._validate_sudo_credentials()
        assert result is False
    finally:
        network_module._SUDO_VALIDATION_IN_PROGRESS = original_state


def test_privileged_cmd_triggers_validation():
    """_privileged_cmd should trigger credential validation when not root."""
    import fcm.core.network as network_module

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("fcm.core.network.os.getuid", return_value=1000):
        with patch("fcm.core.network.subprocess.run", return_value=mock_result):
            result = network_module._privileged_cmd(["ip", "link", "show"])
            assert result == ["sudo", "ip", "link", "show"]


def test_privileged_cmd_skips_sudo_when_root():
    """_privileged_cmd should not add sudo when running as root."""
    import fcm.core.network as network_module

    with patch("fcm.core.network.os.getuid", return_value=0):
        with patch("fcm.core.network.subprocess.run") as mock_run:
            result = network_module._privileged_cmd(["ip", "link", "show"])
            assert result == ["ip", "link", "show"]
            mock_run.assert_not_called()


# ---------------------------------------------------------------------------
# iptables-restore batching (Issue #8)
# ---------------------------------------------------------------------------


def test_build_iptables_restore_input_single_table():
    """_build_iptables_restore_input should format single table rules correctly."""
    from fcm.core.network import _build_iptables_restore_input

    rules = [
        {"table": "filter", "chain": "FCM-FORWARD", "rule": "-i eth0 -j ACCEPT"},
        {"table": "filter", "chain": "FCM-FORWARD", "rule": "-o eth0 -j ACCEPT"},
    ]
    result = _build_iptables_restore_input(rules)

    assert "*filter" in result
    assert ":FCM-FORWARD - [0:0]" in result
    assert "-A FCM-FORWARD -i eth0 -j ACCEPT" in result
    assert "-A FCM-FORWARD -o eth0 -j ACCEPT" in result
    assert "COMMIT" in result


def test_build_iptables_restore_input_multiple_tables():
    """_build_iptables_restore_input should group rules by table."""
    from fcm.core.network import _build_iptables_restore_input

    rules = [
        {"table": "nat", "chain": "FCM-POSTROUTING", "rule": "-o eth0 -j MASQUERADE"},
        {"table": "filter", "chain": "FCM-FORWARD", "rule": "-i eth0 -j ACCEPT"},
    ]
    result = _build_iptables_restore_input(rules)

    assert "*nat" in result
    assert "*filter" in result
    assert ":FCM-POSTROUTING - [0:0]" in result
    assert ":FCM-FORWARD - [0:0]" in result
    assert result.count("COMMIT") == 2


def test_build_iptables_restore_input_default_table():
    """_build_iptables_restore_input should default to filter table."""
    from fcm.core.network import _build_iptables_restore_input

    rules = [{"chain": "FCM-FORWARD", "rule": "-i eth0 -j ACCEPT"}]
    result = _build_iptables_restore_input(rules)

    assert "*filter" in result


def test_build_iptables_restore_input_empty_rules():
    """_build_iptables_restore_input should return empty string for empty rules."""
    from fcm.core.network import _build_iptables_restore_input

    result = _build_iptables_restore_input([])
    assert result == "\n"


def test_apply_iptables_rules_batch_success():
    """_apply_iptables_rules_batch should call iptables-restore with correct input."""
    from fcm.core.network import _apply_iptables_rules_batch

    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        rules = [
            {"table": "filter", "chain": "FCM-FORWARD", "rule": "-i eth0 -j ACCEPT"},
        ]
        _apply_iptables_rules_batch(rules)

        assert mock_run.call_count == 1
        args = mock_run.call_args[0][0]
        assert "iptables-restore" in args
        assert "--noflush" in args


def test_apply_iptables_rules_batch_empty_rules():
    """_apply_iptables_rules_batch should be a no-op for empty rules list."""
    from fcm.core.network import _apply_iptables_rules_batch

    with patch("fcm.core.network.subprocess.run") as mock_run:
        _apply_iptables_rules_batch([])
        mock_run.assert_not_called()


def test_apply_iptables_rules_batch_failure():
    """_apply_iptables_rules_batch should raise NetworkError on failure."""
    from fcm.core.network import _apply_iptables_rules_batch

    with patch(
        "fcm.core.network.subprocess.run",
        side_effect=subprocess.CalledProcessError(1, ["iptables-restore"]),
    ):
        rules = [{"table": "filter", "chain": "FCM-FORWARD", "rule": "-i eth0 -j ACCEPT"}]
        with pytest.raises(NetworkError, match="Failed to apply iptables rules"):
            _apply_iptables_rules_batch(rules)


def test_setup_nat_uses_batch_mode():
    """setup_nat should use iptables-restore for atomic rule application."""
    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("fcm.core.network.get_default_interface", return_value="eth0"):
            with patch("fcm.core.network.setup_fcm_chains"):
                setup_nat("fc-br0", "eth0")

                # Should be exactly 1 call to iptables-restore
                assert mock_run.call_count == 1
                args = mock_run.call_args[0][0]
                assert "iptables-restore" in args
                assert "--noflush" in args

                # Verify the input contains all 3 rules
                call_kwargs = mock_run.call_args[1]
                input_data = call_kwargs.get("input", "")
                assert "MASQUERADE" in input_data
                assert "FCM-FORWARD" in input_data
                assert "FCM-POSTROUTING" in input_data


def test_add_iptables_forward_rules_uses_batch_mode():
    """add_iptables_forward_rules should use iptables-restore for atomic application."""
    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("fcm.core.network.setup_fcm_chains"):
            add_iptables_forward_rules("fc-vm1-0", "fc-br0")

            # Should be exactly 1 call to iptables-restore
            assert mock_run.call_count == 1
            args = mock_run.call_args[0][0]
            assert "iptables-restore" in args

            # Verify the input contains both rules
            call_kwargs = mock_run.call_args[1]
            input_data = call_kwargs.get("input", "")
            assert "fc-vm1-0" in input_data
            assert "fc-br0" in input_data
            assert input_data.count("ACCEPT") == 2


# ---------------------------------------------------------------------------
# Issue #20: Subprocess Buffering - Standardize ip -batch usage
# ---------------------------------------------------------------------------


def test_setup_bridge_uses_ip_batch_mode():
    """setup_bridge should use ip -batch for atomic bridge creation."""
    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("fcm.core.network.bridge_exists", return_value=False):
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

    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
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

    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("fcm.core.network.tap_exists", return_value=False):
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

    with patch("fcm.core.network.subprocess.run", return_value=mock_result) as mock_run:
        with patch("fcm.core.network.tap_exists", return_value=True):
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
    from fcm.core.network import (
        setup_bridge,
        teardown_bridge,
        create_tap,
        delete_tap,
        _run_ip_batch,
    )

    # Get source code of each function
    for func in [setup_bridge, teardown_bridge, create_tap, delete_tap]:
        source = inspect.getsource(func)
        # Each function should use ip -batch directly or via _run_ip_batch helper
        assert "-batch" in source or "_run_ip_batch" in source, (
            f"{func.__name__} should use ip -batch or _run_ip_batch"
        )
