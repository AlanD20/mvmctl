"""Kernel management system tests."""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.slow]


class TestKernelLifecycle:
    """Test kernel CRUD operations."""

    def test_kernel_list_empty(self, mvm_binary):
        """List kernels when none are cached."""
        result = _run_mvm(mvm_binary, "kernel", "ls")
        assert result.returncode == 0

    def test_kernel_fetch(self, mvm_binary):
        """Fetch official kernel."""
        from tests.system.conftest import _skip_if_parallel

        _skip_if_parallel()

        result = _run_mvm(mvm_binary, "kernel", "fetch", "--type", "official")
        assert result.returncode == 0

    def test_kernel_list_json(self, mvm_binary):
        """List kernels in JSON format."""
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, list)

    def test_kernel_set_default(self, mvm_binary):
        """Set kernel as default (uses the one fetched in test_kernel_fetch)."""
        from tests.system.conftest import _skip_if_parallel

        _skip_if_parallel()

        # Get kernel ID
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels = json.loads(result.stdout)
        if not kernels:
            pytest.skip("No kernel to set as default")
        kernel_id = kernels[0]["id"]
        result = _run_mvm(mvm_binary, "kernel", "set-default", kernel_id[:6])
        assert result.returncode == 0


class TestKernelRemoveAndFetch:
    """Test kernel removal and fetch with set-default."""

    def test_kernel_fetch_with_set_default(self, mvm_binary):
        """Fetch official kernel and set as default in one command."""
        from tests.system.conftest import _skip_if_parallel

        _skip_if_parallel()

        result = _run_mvm(
            mvm_binary,
            "kernel",
            "fetch",
            "--type",
            "official",
            "--set-default",
        )
        assert result.returncode == 0

    def test_kernel_remove(self, mvm_binary):
        """Fetch a kernel then remove it."""
        from tests.system.conftest import _skip_if_parallel

        _skip_if_parallel()

        # Get existing kernels
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        existing = json.loads(result.stdout)

        if not existing:
            # Fetch one first
            _run_mvm(mvm_binary, "kernel", "fetch", "--type", "official")
            result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
            existing = json.loads(result.stdout)

        if not existing:
            pytest.skip("No kernel available to remove")

        kernel_id = existing[0]["id"][:6]

        # Remove the kernel
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "rm",
            kernel_id,
            check=False,
        )
        assert result.returncode == 0

        # Verify gone
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        remaining = json.loads(result.stdout)
        assert not any(k["id"].startswith(kernel_id) for k in remaining)

        # Re-fetch so other tests aren't broken
        _run_mvm(
            mvm_binary,
            "kernel",
            "fetch",
            "--type",
            "official",
            "--set-default",
            check=False,
        )
