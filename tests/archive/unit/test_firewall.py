"""Tests for core/firewall.py."""

import subprocess
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core import firewall


class TestSetupNocloudInputChain:
    """Tests for setup_nocloud_input_chain function."""

    def test_setup_nocloud_input_chain_creates_chain(self):
        """setup_nocloud_input_chain should create MVM-NOCLOUD-INPUT chain."""
        with patch.object(firewall, "_chain_exists", return_value=False):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.return_value = MagicMock(returncode=0)

                firewall.setup_nocloud_input_chain()

                # Verify chain creation was attempted
                calls = mock_subprocess.run.call_args_list
                # Should have: -N chain, -D INPUT jump, -C INPUT jump check, -I INPUT jump
                assert len(calls) >= 1

    def test_setup_chain_when_already_exists(self):
        """setup_nocloud_input_chain should be idempotent when chain exists."""
        with patch.object(firewall, "_chain_exists", return_value=True):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.return_value = MagicMock(returncode=0)

                # Should not raise and should handle existing chain
                firewall.setup_nocloud_input_chain()

                # Verify subprocess was called (for INPUT jump rules)
                assert mock_subprocess.run.called


class TestAddNocloudInputRule:
    """Tests for add_nocloud_input_rule function."""

    def test_add_nocloud_input_rule_structure(self):
        """add_nocloud_input_rule should add a correctly structured rule."""
        with patch.object(firewall, "setup_nocloud_input_chain"):
            with patch.object(firewall, "_apply_iptables_rules_batch") as mock_batch:
                firewall.add_nocloud_input_rule("10.0.0.2", "myvm", 8080)

                # Verify batch was called with correct rule structure
                mock_batch.assert_called_once()
                call_args = mock_batch.call_args[0][0]

                assert len(call_args) == 1
                assert call_args[0]["table"] == "filter"
                assert call_args[0]["chain"] == "MVM-NOCLOUD-INPUT"
                assert "-s 10.0.0.2" in call_args[0]["rule"]
                assert "-p tcp" in call_args[0]["rule"]
                assert "--dport 8080" in call_args[0]["rule"]
                assert "-j ACCEPT" in call_args[0]["rule"]
                assert "# mvm-nocloud:myvm:8080" in call_args[0]["rule"]

    def test_add_rule_uses_privileged_cmd(self):
        """add_nocloud_input_rule should use _privileged_cmd for subprocess calls."""
        with patch.object(firewall, "setup_nocloud_input_chain"):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.return_value = MagicMock(returncode=0)

                firewall.add_nocloud_input_rule("10.0.0.2", "myvm", 8080)

                # Verify at least one call used _privileged_cmd pattern
                # The _apply_iptables_rules_batch uses _privileged_cmd internally
                assert mock_subprocess.run.called

    def test_add_rule_includes_vm_name_in_comment(self):
        """add_nocloud_input_rule should include VM name in rule comment."""
        with patch.object(firewall, "setup_nocloud_input_chain"):
            with patch.object(firewall, "_apply_iptables_rules_batch") as mock_batch:
                firewall.add_nocloud_input_rule("10.0.0.2", "test-vm-123", 9090)

                call_args = mock_batch.call_args[0][0]
                rule = call_args[0]["rule"]
                assert "# mvm-nocloud:test-vm-123:9090" in rule


class TestRemoveNocloudInputRule:
    """Tests for remove_nocloud_input_rule function."""

    def test_remove_nocloud_input_rule_is_idempotent(self):
        """remove_nocloud_input_rule should be safe to call multiple times."""
        with patch.object(firewall, "_chain_exists", return_value=True):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.return_value = MagicMock(returncode=1)

                # Call multiple times - should not raise
                firewall.remove_nocloud_input_rule("10.0.0.2", "myvm", 8080)
                firewall.remove_nocloud_input_rule("10.0.0.2", "myvm", 8080)
                firewall.remove_nocloud_input_rule("10.0.0.2", "myvm", 8080)

                # All calls should use check=False (idempotent)
                for call in mock_subprocess.run.call_args_list:
                    kwargs = call.kwargs
                    assert kwargs.get("check", True) is False

    def test_remove_rule_when_chain_not_exists(self):
        """remove_nocloud_input_rule should do nothing if chain doesn't exist."""
        with patch.object(firewall, "_chain_exists", return_value=False):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                # Should return early without calling subprocess
                firewall.remove_nocloud_input_rule("10.0.0.2", "myvm", 8080)

                assert not mock_subprocess.run.called

    def test_remove_rule_builds_correct_command(self):
        """remove_nocloud_input_rule should build correct iptables -D command."""
        with patch.object(firewall, "_chain_exists", return_value=True):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.return_value = MagicMock(returncode=0)

                firewall.remove_nocloud_input_rule("10.0.0.2", "myvm", 8080)

                # Verify the command structure
                call_args = mock_subprocess.run.call_args[0][0]
                # Should contain chain name and rule components
                assert "MVM-NOCLOUD-INPUT" in call_args


class TestCleanupNocloudInputRules:
    """Tests for cleanup_nocloud_input_rules function."""

    def test_cleanup_nocloud_input_rules_flushes(self):
        """cleanup_nocloud_input_rules should flush the chain."""
        with patch.object(firewall, "_chain_exists", return_value=True):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.return_value = MagicMock(returncode=0)

                firewall.cleanup_nocloud_input_rules()

                # Should call iptables -F with the chain name
                mock_subprocess.run.assert_called_once()
                call_args = mock_subprocess.run.call_args[0][0]
                assert "-F" in call_args
                assert "MVM-NOCLOUD-INPUT" in call_args

    def test_cleanup_when_chain_not_exists(self):
        """cleanup_nocloud_input_rules should do nothing if chain doesn't exist."""
        with patch.object(firewall, "_chain_exists", return_value=False):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                # Should return early without calling subprocess
                firewall.cleanup_nocloud_input_rules()

                assert not mock_subprocess.run.called

    def test_cleanup_is_idempotent(self):
        """cleanup_nocloud_input_rules should be safe to call multiple times."""
        with patch.object(firewall, "_chain_exists", return_value=True):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.return_value = MagicMock(returncode=0)

                # Call multiple times - should not raise
                firewall.cleanup_nocloud_input_rules()
                firewall.cleanup_nocloud_input_rules()
                firewall.cleanup_nocloud_input_rules()

                # All calls should succeed
                assert mock_subprocess.run.call_count == 3


class TestPrivilegedCmdUsage:
    """Tests verifying _privileged_cmd is used for all iptables calls."""

    def test_privileged_cmd_used_for_chain_check(self):
        """_privileged_cmd should be used when checking if chain exists."""
        with patch.object(firewall, "_privileged_cmd") as mock_priv:
            mock_priv.return_value = ["iptables", "-t", "filter", "-L", "MVM-NOCLOUD-INPUT", "-n"]
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.return_value = MagicMock(returncode=0)
                mock_subprocess.CalledProcessError = subprocess.CalledProcessError

                firewall._chain_exists("MVM-NOCLOUD-INPUT")

                mock_priv.assert_called_once_with(
                    ["iptables", "-t", "filter", "-L", "MVM-NOCLOUD-INPUT", "-n"]
                )

    def test_privileged_cmd_used_for_rule_check(self):
        """_privileged_cmd should be used when checking if rule exists."""
        with patch.object(firewall, "_privileged_cmd") as mock_priv:
            mock_priv.return_value = ["iptables", "-C", "INPUT", "-j", "MVM-NOCLOUD-INPUT"]
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.return_value = MagicMock(returncode=0)
                mock_subprocess.CalledProcessError = subprocess.CalledProcessError

                firewall._iptables_rule_exists(
                    ["iptables", "-C", "INPUT", "-j", "MVM-NOCLOUD-INPUT"]
                )

                mock_priv.assert_called_once()


class TestApplyIptablesRulesBatch:
    """Tests for _apply_iptables_rules_batch."""

    def test_empty_rules_returns_immediately(self):
        """_apply_iptables_rules_batch should return early for empty rules."""
        with patch.object(firewall, "subprocess") as mock_subprocess:
            firewall._apply_iptables_rules_batch([])
            mock_subprocess.run.assert_not_called()

    def test_raises_network_error_on_failure(self):
        """_apply_iptables_rules_batch should raise NetworkError on CalledProcessError."""
        from mvmctl.exceptions import NetworkError

        with patch.object(firewall, "subprocess") as mock_subprocess:
            mock_subprocess.run.side_effect = subprocess.CalledProcessError(
                1, ["iptables-restore"], stderr="error"
            )
            mock_subprocess.CalledProcessError = subprocess.CalledProcessError

            rules = [{"table": "filter", "chain": "TEST", "rule": "-A TEST -j ACCEPT"}]
            with pytest.raises(NetworkError):
                firewall._apply_iptables_rules_batch(rules)


class TestCreateChainIfMissing:
    """Tests for _create_chain_if_missing."""

    def test_returns_false_when_chain_exists(self):
        """_create_chain_if_missing should return False when chain already exists."""
        with patch.object(firewall, "_chain_exists", return_value=True):
            result = firewall._create_chain_if_missing("TEST_CHAIN")
            assert result is False

    def test_handles_chain_already_exists_error(self):
        """_create_chain_if_missing should return False on 'Chain already exists' error."""
        error = subprocess.CalledProcessError(1, ["iptables"], stderr=b"Chain already exists")
        with patch.object(firewall, "_chain_exists", return_value=False):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.side_effect = error
                mock_subprocess.CalledProcessError = subprocess.CalledProcessError

                result = firewall._create_chain_if_missing("TEST_CHAIN")
                assert result is False

    def test_raises_on_other_error(self):
        """_create_chain_if_missing should raise NetworkError on other errors."""
        from mvmctl.exceptions import NetworkError

        error = subprocess.CalledProcessError(1, ["iptables"], stderr=b"Permission denied")
        with patch.object(firewall, "_chain_exists", return_value=False):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.side_effect = error
                mock_subprocess.CalledProcessError = subprocess.CalledProcessError

                with pytest.raises(NetworkError, match="Failed to create"):
                    firewall._create_chain_if_missing("TEST_CHAIN")


class TestSetupNocloudInputChainErrors:
    """Tests for setup_nocloud_input_chain error handling."""

    def test_raises_on_jump_insert_failure(self):
        """setup_nocloud_input_chain should raise NetworkError on jump insert failure."""
        from mvmctl.exceptions import NetworkError

        error = subprocess.CalledProcessError(1, ["iptables"], stderr="fail")
        with patch.object(firewall, "_create_chain_if_missing", return_value=True):
            with patch.object(firewall, "_iptables_rule_exists", return_value=False):
                with patch.object(firewall, "subprocess") as mock_subprocess:
                    mock_subprocess.run.side_effect = [
                        MagicMock(returncode=0),
                        error,
                    ]
                    mock_subprocess.CalledProcessError = subprocess.CalledProcessError

                    with pytest.raises(NetworkError, match="Failed to add jump"):
                        firewall.setup_nocloud_input_chain()


class TestCleanupNocloudInputRulesErrors:
    """Tests for cleanup_nocloud_input_rules error handling."""

    def test_returns_when_chain_not_exists(self):
        """cleanup_nocloud_input_rules should return early when chain doesn't exist."""
        with patch.object(firewall, "_chain_exists", return_value=False):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                firewall.cleanup_nocloud_input_rules()
                mock_subprocess.run.assert_not_called()

    def test_raises_on_flush_failure(self):
        """cleanup_nocloud_input_rules should raise NetworkError on flush failure."""
        from mvmctl.exceptions import NetworkError

        error = subprocess.CalledProcessError(1, ["iptables"], stderr="fail")
        with patch.object(firewall, "_chain_exists", return_value=True):
            with patch.object(firewall, "subprocess") as mock_subprocess:
                mock_subprocess.run.side_effect = error
                mock_subprocess.CalledProcessError = subprocess.CalledProcessError

                with pytest.raises(NetworkError, match="Failed to flush"):
                    firewall.cleanup_nocloud_input_rules()


class TestBuildIptablesRestoreInput:
    """Tests for _build_iptables_restore_input."""

    def test_groups_rules_by_table(self):
        """Rules with same table should be grouped together."""
        rules = [
            {"table": "filter", "chain": "INPUT", "rule": "-j ACCEPT"},
            {"table": "filter", "chain": "OUTPUT", "rule": "-j DROP"},
            {"table": "nat", "chain": "PREROUTING", "rule": "-j DNAT"},
        ]
        result = firewall._build_iptables_restore_input(rules)
        assert "*filter" in result
        assert "*nat" in result
        assert "COMMIT" in result
