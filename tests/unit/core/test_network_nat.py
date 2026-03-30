"""Unit tests for NAT/POSTROUTING rules in network module."""

from unittest.mock import MagicMock, call

import pytest
from pytest_mock import MockerFixture

from mvmctl.core.network import (
    _detect_cidr_for_bridge,
    setup_nat,
    teardown_nat,
)
from mvmctl.exceptions import NetworkError


class TestSetupNat:
    """Tests for setup_nat function."""

    def test_setup_nat_with_cidr(self, mocker: MockerFixture):
        """Verify -s CIDR is added to MASQUERADE rule."""
        # Mock get_default_interface
        mocker.patch("mvmctl.core.network.get_default_interface", return_value="eth0")
        # Mock setup_mvm_chains
        mocker.patch("mvmctl.core.network.setup_mvm_chains")
        # Mock _iptables_rule_exists to return False (rule doesn't exist)
        mocker.patch("mvmctl.core.network._iptables_rule_exists", return_value=False)
        # Mock subprocess.run for the iptables command
        mock_run = mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        
        setup_nat(bridge="mvm-test", cidr="10.0.0.0/24")
        
        # Verify subprocess.run was called with the MASQUERADE rule containing -s CIDR
        calls = mock_run.call_args_list
        masquerade_calls = [
            c for c in calls 
            if "MASQUERADE" in str(c) and "-s" in str(c)
        ]
        assert len(masquerade_calls) > 0
        # Check the CIDR is in the command
        assert any("10.0.0.0/24" in str(c) for c in masquerade_calls)

    def test_setup_nat_with_internet_iface(self, mocker: MockerFixture):
        """Verify interface is used correctly."""
        # Mock setup_mvm_chains
        mocker.patch("mvmctl.core.network.setup_mvm_chains")
        # Mock _iptables_rule_exists to return False
        mocker.patch("mvmctl.core.network._iptables_rule_exists", return_value=False)
        # Mock subprocess.run
        mock_run = mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        
        setup_nat(bridge="mvm-test", internet_iface="eth1")
        
        # Verify -o eth1 appears in MASQUERADE rule
        calls = mock_run.call_args_list
        masquerade_calls = [c for c in calls if "MASQUERADE" in str(c)]
        assert len(masquerade_calls) > 0
        assert any("-o" in str(c) and "eth1" in str(c) for c in masquerade_calls)

    def test_setup_nat_with_comment(self, mocker: MockerFixture):
        """Verify comment is added to rule."""
        # Mock get_default_interface
        mocker.patch("mvmctl.core.network.get_default_interface", return_value="eth0")
        # Mock setup_mvm_chains
        mocker.patch("mvmctl.core.network.setup_mvm_chains")
        # Mock _iptables_rule_exists to return False
        mocker.patch("mvmctl.core.network._iptables_rule_exists", return_value=False)
        # Mock subprocess.run
        mock_run = mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        
        setup_nat(bridge="mvm-test")
        
        # Verify comment appears in the rule
        calls = mock_run.call_args_list
        comment_calls = [c for c in calls if "--comment" in str(c)]
        assert len(comment_calls) > 0
        assert any("mvm-nat:mvm-test" in str(c) for c in comment_calls)


class TestTeardownNat:
    """Tests for teardown_nat function."""

    def test_teardown_nat_with_cidr(self, mocker: MockerFixture):
        """Verify source-filtered rules are removed."""
        # Mock get_tap_devices to return empty (no TAPs attached)
        mocker.patch("mvmctl.core.network.get_tap_devices", return_value=[])
        # Mock get_default_interface
        mocker.patch("mvmctl.core.network.get_default_interface", return_value="eth0")
        # Mock chain_exists to return True
        mocker.patch("mvmctl.core.network.chain_exists", return_value=True)
        # Mock _detect_cidr_for_bridge
        mocker.patch("mvmctl.core.network._detect_cidr_for_bridge", return_value="10.0.0.0/24")
        # Mock subprocess.run
        mock_run = mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        
        teardown_nat(bridge="mvm-test", force=True)
        
        # Verify -s CIDR appears in delete command
        calls = mock_run.call_args_list
        delete_calls = [c for c in calls if "-D" in str(c) and "POSTROUTING" in str(c)]
        assert len(delete_calls) > 0
        assert any("-s" in str(c) and "10.0.0.0/24" in str(c) for c in delete_calls)

    def test_detect_cidr_for_bridge(self, mocker: MockerFixture):
        """Test CIDR detection from existing rules."""
        # Mock subprocess.run to return iptables list output with MASQUERADE rule
        mock_result = MagicMock()
        mock_result.returncode = 0
        mock_result.stdout = """Chain MVM-POSTROUTING (1 references)
num   packets   bytes target     prot opt in     out     source               destination
1        0        0 MASQUERADE  all  --  *      eth0    10.0.0.0/24           0.0.0.0/0           /* mvm-nat:mvm-test */
"""
        mocker.patch("subprocess.run", return_value=mock_result)
        
        result = _detect_cidr_for_bridge("mvm-test")
        
        assert result == "10.0.0.0/24"
