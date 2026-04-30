"""Firecracker binary management system tests."""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm

pytestmark = pytest.mark.system


class TestBinLifecycle:
    """Test Firecracker binary management operations."""

    def test_bin_list_cached(self, mvm_binary):
        """List cached firecracker versions."""
        result = _run_mvm(mvm_binary, "bin", "ls")
        assert result.returncode == 0

    def test_bin_list_json(self, mvm_binary):
        """List binaries in JSON format."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, list)

    def test_bin_list_remote(self, mvm_binary):
        """List available remote versions."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote")
        assert result.returncode == 0


class TestBinaryFetchAndLifecycle:
    """Test Firecracker binary fetch, set-default, and remove operations."""

    @pytest.mark.slow
    def test_bin_fetch_and_set_default(self, mvm_binary):
        """Fetch a specific binary version and set as default."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote")
        import re

        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")
        target = versions[-1]  # Latest version

        result = _run_mvm(
            mvm_binary,
            "bin",
            "fetch",
            target,
            "--set-default",
            check=False,
        )
        assert result.returncode == 0, (
            f"bin fetch {target} failed: {result.stderr}"
        )

    @pytest.mark.slow
    def test_bin_remove_by_version(self, mvm_binary):
        """Fetch a specific version and remove by version."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote")
        import re

        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")

        # Pick a version that's not the latest (to avoid removing the default)
        target = versions[0] if len(versions) > 1 else versions[-1]

        # Fetch it
        _run_mvm(mvm_binary, "bin", "fetch", target, check=False)

        # Remove by version
        result = _run_mvm(
            mvm_binary,
            "bin",
            "rm",
            "--version",
            target,
            "--force",
            check=False,
        )
        assert result.returncode == 0, (
            f"bin rm --version {target} failed: {result.stderr}"
        )
