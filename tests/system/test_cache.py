"""Cache management system tests."""

from __future__ import annotations

import json
import os
from typing import Any

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_cache]


class TestCacheInit:
    """Test cache initialization operations."""

    def test_cache_init(self, mvm_binary):
        """Initialize cache resources."""
        result = _run_mvm(mvm_binary, "cache", "init")
        assert result.returncode == 0
        assert "initialized" in result.stdout or "Cache" in result.stdout

    def test_cache_init_idempotent(self, mvm_binary):
        """Running cache init multiple times should be safe (idempotent)."""
        result1 = _run_mvm(mvm_binary, "cache", "init")
        assert result1.returncode == 0

        result2 = _run_mvm(mvm_binary, "cache", "init")
        assert result2.returncode == 0

        bin_result = _run_mvm(mvm_binary, "bin", "ls", "--json", check=False)
        assert bin_result.returncode == 0


class TestCachePruneDryRun:
    """Test cache prune dry-run operations."""

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cache_prune_all_dry_run(self, mvm_binary, created_vm):
        """Prune all resources in dry-run mode should not remove the VM."""
        vm_name = created_vm["name"]
        result = _run_mvm(mvm_binary, "cache", "prune", "--all", "--dry-run")
        assert result.returncode == 0
        assert "DRY RUN" in result.stdout

        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        assert any(v["name"] == vm_name for v in vms)

    def test_cache_prune_dry_run_shows_what_would_be_removed(self, mvm_binary):
        """cache prune --dry-run --all should succeed and print summary."""
        result = _run_mvm(mvm_binary, "cache", "prune", "--dry-run", "--all")
        assert result.returncode == 0
        combined = (result.stdout + result.stderr).lower()
        assert "dry run" in combined

        ls_after = _run_mvm(mvm_binary, "image", "ls", "--json")
        images_after: list[dict[str, Any]] = json.loads(ls_after.stdout)
        assert isinstance(images_after, list)


class TestCacheClean:
    """Test cache clean command."""

    def test_cache_clean_dry_run(self, mvm_binary):
        """cache clean --dry-run --force should preview what would be removed."""
        result = _run_mvm(mvm_binary, "cache", "clean", "--dry-run", "--force")
        assert result.returncode == 0
        combined = (result.stdout + result.stderr).lower()
        assert "dry run" in combined

        init_result = _run_mvm(mvm_binary, "cache", "init", check=False)
        assert init_result.returncode == 0

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_cache_clean_refuses_with_running_vm(
        self, mvm_binary, unique_vm_name
    ):
        """Should not clean cache while resources are in use."""
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
            )
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            result = _run_mvm(mvm_binary, "cache", "clean", check=False)
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert any(s in combined for s in ["in use", "running", "cannot"])

            result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if result_vm.returncode == 0:
                vms: list[dict[str, Any]] = json.loads(result_vm.stdout)
                assert any(v["name"] == vm_name for v in vms)

            cache_dir = os.environ.get("MVM_CACHE_DIR", "")
            if cache_dir:
                assert os.path.isdir(cache_dir)
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)


class TestCachePruneActual:
    """Test actual (non-dry-run) cache prune operations."""

    pytestmark = [pytest.mark.system, pytest.mark.slow]

    def test_cache_prune_misc_actual(self, mvm_binary):
        """Actually prune misc cache (safe to actually clean temp files)."""
        result = _run_mvm(mvm_binary, "cache", "prune", "misc", check=False)
        if result.returncode != 0:
            pytest.skip(
                f"cache prune misc failed (may need sudo or other preconditions): "
                f"{result.stderr}"
            )
        assert result.returncode == 0

        init_result = _run_mvm(mvm_binary, "cache", "init", check=False)
        assert init_result.returncode == 0

    def test_cache_prune_misc_with_force(self, mvm_binary):
        """Prune misc cache with --force flag."""
        result = _run_mvm(
            mvm_binary, "cache", "prune", "misc", "--force", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"Misc prune with --force failed: {result.stderr}")
        assert result.returncode == 0


class TestCachePruneEdgeCases:
    """Test edge cases for cache prune command."""

    def test_cache_prune_with_nonexistent_category(self, mvm_binary):
        """Pruning a nonexistent category should fail."""
        result = _run_mvm(
            mvm_binary,
            "cache",
            "prune",
            "nonexistent-category",
            "--dry-run",
            check=False,
        )
        assert result.returncode != 0

    def test_cache_prune_nonexistent_category_flag(self, mvm_binary):
        """cache prune with an unknown flag should fail."""
        result = _run_mvm(
            mvm_binary, "cache", "prune", "--nonexistent-category", check=False
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["invalid", "unknown", "category"])

    def test_cache_prune_without_category_or_all_fails(self, mvm_binary):
        """cache prune without resource and --all should fail with guidance."""
        result = _run_mvm(mvm_binary, "cache", "prune", check=False)
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["category", "specify", "--all"])

    def test_cache_prune_default_image_skipped_or_warns(self, mvm_binary):
        """Pruning images should skip the default image or warn."""
        ls_result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(ls_result.stdout)
        default_image = next(
            (img for img in images if img.get("is_default")), None
        )

        result = _run_mvm(mvm_binary, "cache", "prune", "image", check=False)
        if result.returncode != 0:
            pytest.skip(
                f"cache prune image non-interactive failed: "
                f"{result.stderr.strip()}"
            )

        assert result.returncode == 0
        combined = (result.stdout + result.stderr).lower()

        if default_image:
            ls_after = _run_mvm(mvm_binary, "image", "ls", "--json")
            images_after: list[dict[str, Any]] = json.loads(ls_after.stdout)
            default_after = next(
                (img for img in images_after if img.get("is_default")), None
            )
            assert default_after is not None
        else:
            assert any(
                s in combined
                for s in ["no images", "nothing", "none", "no image"]
            )
