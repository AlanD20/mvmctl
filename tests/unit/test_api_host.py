"""Tests for api/host.py."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.api.host import (
    check_kvm_access,
    check_privileges,
    check_privileges_interactive,
    check_required_binaries,
    clean_host,
    clean_ready_pool,
    default_cache_dir,
    escalate_and_init_host,
    get_host_state,
    get_ip_forward_status,
    get_ready_pool_dir,
    get_vm_manager,
    init_host,
    prune_host,
    reset_host,
    restore_host,
)
from mvmctl.exceptions import HostError


class TestDefaultCacheDir:
    """Tests for default_cache_dir()."""

    def test_returns_get_cache_dir(self):
        """default_cache_dir should return get_cache_dir() result."""
        with patch("mvmctl.api.host.get_cache_dir") as mock_get:
            mock_get.return_value = Path("/tmp/test-cache")
            result = default_cache_dir()
            assert result == Path("/tmp/test-cache")
            mock_get.assert_called_once()


class TestEscalateAndInitHost:
    """Tests for escalate_and_init_host()."""

    def test_success_no_escalation(self):
        """Should return changes when init_host succeeds without escalation."""
        with patch("mvmctl.api.host.get_cache_dir") as mock_cache:
            with patch("mvmctl.api.host.init_host") as mock_init:
                mock_cache.return_value = Path("/tmp/cache")
                mock_init.return_value = ["created bridge"]
                result = escalate_and_init_host()
                assert result == ["created bridge"]
                mock_init.assert_called_once_with(Path("/tmp/cache"))

    def test_escalates_to_sudo_on_privilege_error(self):
        """Should escalate to sudo when init_host raises privilege error."""
        with patch("mvmctl.api.host.get_cache_dir") as mock_cache:
            with patch("mvmctl.api.host.init_host") as mock_init:
                with patch("mvmctl.api.host.subprocess.run") as mock_run:
                    with patch.dict("os.environ", {}, clear=True):
                        mock_cache.return_value = Path("/tmp/cache")
                        mock_init.side_effect = HostError("Root privileges required")
                        mock_run.return_value = MagicMock(returncode=0)

                        with pytest.raises(SystemExit):
                            escalate_and_init_host(["mvm", "host", "init"])

                        mock_run.assert_called_once()
                        call_env = mock_run.call_args[1]["env"]
                        assert call_env["MVM_SUDO_RESTART"] == "1"

    def test_recursive_sudo_detection(self):
        """Should raise HostError on recursive sudo restart."""
        with patch("mvmctl.api.host.get_cache_dir"):
            with patch("mvmctl.api.host.init_host") as mock_init:
                with patch.dict("os.environ", {"MVM_SUDO_RESTART": "1"}):
                    mock_init.side_effect = HostError("Root privileges required")
                    with pytest.raises(HostError, match="Recursive sudo restart"):
                        escalate_and_init_host()

    def test_non_privilege_error_reraises(self):
        """Should re-raise non-privilege HostErrors without escalation."""
        with patch("mvmctl.api.host.get_cache_dir"):
            with patch("mvmctl.api.host.init_host") as mock_init:
                mock_init.side_effect = HostError("Some other error")
                with pytest.raises(HostError, match="Some other error"):
                    escalate_and_init_host()

    def test_sudo_not_found(self):
        """Should raise HostError when sudo binary is missing."""
        with patch("mvmctl.api.host.get_cache_dir"):
            with patch("mvmctl.api.host.init_host") as mock_init:
                with patch("mvmctl.api.host.subprocess.run") as mock_run:
                    with patch.dict("os.environ", {}, clear=True):
                        mock_init.side_effect = HostError("Root privileges required")
                        mock_run.side_effect = FileNotFoundError("sudo not found")
                        with pytest.raises(HostError, match="sudo command not found"):
                            escalate_and_init_host()

    def test_uses_sys_argv_when_no_args(self):
        """Should use sys.argv when argv is None."""
        with patch("mvmctl.api.host.get_cache_dir"):
            with patch("mvmctl.api.host.init_host") as mock_init:
                with patch("mvmctl.api.host.subprocess.run") as mock_run:
                    with patch("mvmctl.api.host.sys.argv", ["mvm", "host", "init"]):
                        with patch.dict("os.environ", {}, clear=True):
                            mock_init.side_effect = HostError("Root privileges required")
                            mock_run.return_value = MagicMock(returncode=0)
                            with pytest.raises(SystemExit):
                                escalate_and_init_host()
                            cmd = mock_run.call_args[0][0]
                            assert cmd == ["sudo", "mvm", "host", "init"]


class TestCleanHost:
    """Tests for clean_host()."""

    def test_default_cache_dir(self):
        """Should use get_cache_dir() when cache_dir is None."""
        with patch("mvmctl.api.host.get_cache_dir") as mock_get:
            with patch("mvmctl.api.host._clean_host") as mock_clean:
                mock_get.return_value = Path("/tmp/cache")
                mock_clean.return_value = ["removed bridge"]
                result = clean_host()
                assert result == ["removed bridge"]
                mock_clean.assert_called_once_with(Path("/tmp/cache"))

    def test_explicit_cache_dir(self):
        """Should use provided cache_dir."""
        with patch("mvmctl.api.host._clean_host") as mock_clean:
            mock_clean.return_value = []
            result = clean_host(Path("/custom/cache"))
            assert result == []
            mock_clean.assert_called_once_with(Path("/custom/cache"))


class TestResetHost:
    """Tests for reset_host()."""

    def test_default_cache_dir(self):
        """Should use get_cache_dir() when cache_dir is None."""
        with patch("mvmctl.api.host.get_cache_dir") as mock_get:
            with patch("mvmctl.api.host._reset_host") as mock_reset:
                mock_get.return_value = Path("/tmp/cache")
                mock_reset.return_value = ["reverted sysctl"]
                result = reset_host()
                assert result == ["reverted sysctl"]

    def test_explicit_cache_dir(self):
        """Should use provided cache_dir."""
        with patch("mvmctl.api.host._reset_host") as mock_reset:
            mock_reset.return_value = []
            reset_host(Path("/custom/cache"))
            mock_reset.assert_called_once_with(Path("/custom/cache"))


class TestPruneHost:
    """Tests for prune_host()."""

    def test_default_cache_dir(self):
        """Should use get_cache_dir() when cache_dir is None."""
        with patch("mvmctl.api.host.get_cache_dir") as mock_get:
            with patch("mvmctl.api.host._prune_host") as mock_prune:
                mock_get.return_value = Path("/tmp/cache")
                mock_prune.return_value = ["torn down bridge"]
                result = prune_host()
                assert result == ["torn down bridge"]

    def test_explicit_cache_dir(self):
        """Should use provided cache_dir."""
        with patch("mvmctl.api.host._prune_host") as mock_prune:
            mock_prune.return_value = []
            prune_host(Path("/custom/cache"))
            mock_prune.assert_called_once_with(Path("/custom/cache"))


class TestCleanReadyPool:
    """Tests for clean_ready_pool()."""

    def test_delegates_to_core(self):
        """Should delegate to _clean_ready_pool."""
        with patch("mvmctl.api.host._clean_ready_pool") as mock_clean:
            mock_clean.return_value = 5
            result = clean_ready_pool()
            assert result == 5
            mock_clean.assert_called_once()


class TestGetReadyPoolDir:
    """Tests for get_ready_pool_dir()."""

    def test_delegates_to_core(self):
        """Should delegate to _get_ready_pool_dir."""
        with patch("mvmctl.api.host._get_ready_pool_dir") as mock_get:
            mock_get.return_value = Path("/tmp/ready-pool")
            result = get_ready_pool_dir()
            assert result == Path("/tmp/ready-pool")
            mock_get.assert_called_once()


class TestReExports:
    """Tests that re-exported core functions are accessible."""

    def test_check_kvm_access_is_callable(self):
        assert callable(check_kvm_access)

    def test_check_privileges_is_callable(self):
        assert callable(check_privileges)

    def test_check_privileges_interactive_is_callable(self):
        assert callable(check_privileges_interactive)

    def test_check_required_binaries_is_callable(self):
        assert callable(check_required_binaries)

    def test_get_host_state_is_callable(self):
        assert callable(get_host_state)

    def test_get_ip_forward_status_is_callable(self):
        assert callable(get_ip_forward_status)

    def test_init_host_is_callable(self):
        assert callable(init_host)

    def test_restore_host_is_callable(self):
        assert callable(restore_host)

    def test_get_vm_manager_is_callable(self):
        assert callable(get_vm_manager)
