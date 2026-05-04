"""Cache management system tests."""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system]


class TestCacheCommands:
    """Test cache management operations."""

    def test_cache_init(self, mvm_binary):
        """Initialize cache resources."""
        result = _run_mvm(mvm_binary, "cache", "init")
        assert result.returncode == 0
        assert "initialized" in result.stdout or "Cache" in result.stdout

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cache_prune_vm_dry_run(self, mvm_binary, created_vm):
        """Prune VMs in dry-run mode should not remove the VM."""
        vm_name = created_vm["name"]
        # Stop the VM so it appears as prunable
        _run_mvm(mvm_binary, "vm", "stop", vm_name, check=False)
        result = _run_mvm(mvm_binary, "cache", "prune", "vm", "--dry-run")
        assert result.returncode == 0
        assert "DRY RUN" in result.stdout or vm_name in result.stdout

        # Verify VM still exists
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        assert any(v["name"] == vm_name for v in vms)

    def test_cache_prune_network_dry_run(self, mvm_binary, created_network):
        """Prune networks in dry-run mode should not remove the network."""
        network_name = created_network
        result = _run_mvm(mvm_binary, "cache", "prune", "network", "--dry-run")
        assert result.returncode == 0
        assert "DRY RUN" in result.stdout

        # Verify network still exists
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks = json.loads(result.stdout)
        assert any(n["name"] == network_name for n in networks)

    def test_cache_prune_image_dry_run(self, mvm_binary):
        """Prune images in dry-run mode."""
        result = _run_mvm(mvm_binary, "cache", "prune", "image", "--dry-run")
        assert result.returncode == 0

    def test_cache_prune_kernel_dry_run(self, mvm_binary):
        """Prune kernels in dry-run mode."""
        result = _run_mvm(mvm_binary, "cache", "prune", "kernel", "--dry-run")
        assert result.returncode == 0

    def test_cache_prune_binary_dry_run(self, mvm_binary):
        """Prune binaries in dry-run mode."""
        result = _run_mvm(mvm_binary, "cache", "prune", "binary", "--dry-run")
        assert result.returncode == 0

    def test_cache_prune_misc_dry_run(self, mvm_binary):
        """Prune misc cache in dry-run mode."""
        result = _run_mvm(mvm_binary, "cache", "prune", "misc", "--dry-run")
        assert result.returncode == 0

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cache_prune_all_dry_run(self, mvm_binary, created_vm):
        """Prune all resources in dry-run mode should not remove the VM."""
        vm_name = created_vm["name"]
        result = _run_mvm(mvm_binary, "cache", "prune", "--all", "--dry-run")
        assert result.returncode == 0
        assert "DRY RUN" in result.stdout

        # Verify VM still exists
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        assert any(v["name"] == vm_name for v in vms)


class TestCacheClean:
    """Test mvm cache clean command."""

    def test_cache_clean_dry_run(self, mvm_binary):
        """cache clean --dry-run --force should preview what would be removed."""
        result = _run_mvm(mvm_binary, "cache", "clean", "--dry-run", "--force")
        assert result.returncode == 0
        assert "DRY RUN" in result.stdout
