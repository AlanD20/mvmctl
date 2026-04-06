"""Tests for api/host.py."""

from pathlib import Path
from unittest.mock import patch

from mvmctl.api.host import (
    check_kvm_access,
    check_privileges,
    check_privileges_interactive,
    check_required_binaries,
    clean_host,
    clean_ready_pool,
    get_host_state,
    get_ip_forward_status,
    get_ready_pool_dir,
    get_vm_manager,
    init_host,
    prune_host,
    reset_host,
    restore_host,
)


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
                # Verify _clean_host was called with cache_dir and db
                assert mock_clean.call_count == 1
                call_args = mock_clean.call_args[0]
                assert call_args[0] == Path("/tmp/cache")
                # Second argument should be an MVMDatabase instance
                from mvmctl.core.mvm_db import MVMDatabase

                assert isinstance(call_args[1], MVMDatabase)

    def test_explicit_cache_dir(self):
        """Should use provided cache_dir."""
        with patch("mvmctl.api.host._clean_host") as mock_clean:
            mock_clean.return_value = []
            result = clean_host(Path("/custom/cache"))
            assert result == []
            # Verify _clean_host was called with cache_dir and db
            assert mock_clean.call_count == 1
            call_args = mock_clean.call_args[0]
            assert call_args[0] == Path("/custom/cache")
            # Second argument should be an MVMDatabase instance
            from mvmctl.core.mvm_db import MVMDatabase

            assert isinstance(call_args[1], MVMDatabase)


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
                # Verify _reset_host was called with cache_dir and db
                assert mock_reset.call_count == 1
                call_args = mock_reset.call_args[0]
                assert call_args[0] == Path("/tmp/cache")
                # Second argument should be an MVMDatabase instance
                from mvmctl.core.mvm_db import MVMDatabase

                assert isinstance(call_args[1], MVMDatabase)

    def test_explicit_cache_dir(self):
        """Should use provided cache_dir."""
        with patch("mvmctl.api.host._reset_host") as mock_reset:
            mock_reset.return_value = []
            reset_host(Path("/custom/cache"))
            # Verify _reset_host was called with cache_dir and db
            assert mock_reset.call_count == 1
            call_args = mock_reset.call_args[0]
            assert call_args[0] == Path("/custom/cache")
            # Second argument should be an MVMDatabase instance
            from mvmctl.core.mvm_db import MVMDatabase

            assert isinstance(call_args[1], MVMDatabase)


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
                # Verify _prune_host was called with cache_dir and db
                assert mock_prune.call_count == 1
                call_args = mock_prune.call_args[0]
                assert call_args[0] == Path("/tmp/cache")
                # Second argument should be an MVMDatabase instance
                from mvmctl.core.mvm_db import MVMDatabase

                assert isinstance(call_args[1], MVMDatabase)

    def test_explicit_cache_dir(self):
        """Should use provided cache_dir."""
        with patch("mvmctl.api.host._prune_host") as mock_prune:
            mock_prune.return_value = []
            prune_host(Path("/custom/cache"))
            # Verify _prune_host was called with cache_dir and db
            assert mock_prune.call_count == 1
            call_args = mock_prune.call_args[0]
            assert call_args[0] == Path("/custom/cache")
            # Second argument should be an MVMDatabase instance
            from mvmctl.core.mvm_db import MVMDatabase

            assert isinstance(call_args[1], MVMDatabase)


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
