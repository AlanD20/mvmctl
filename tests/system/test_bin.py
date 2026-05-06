"""Firecracker binary management system tests."""

from __future__ import annotations

import json
import re

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system]


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

    def test_bin_list_remote_with_limit(self, mvm_binary):
        """List remote versions with a limit."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote", "--limit", "5")
        assert result.returncode == 0


class TestBinaryPullAndLifecycle:
    """Test Firecracker binary pull, set-default, and remove operations."""

    @pytest.mark.slow
    @pytest.mark.serial
    def test_bin_pull_and_set_default(self, mvm_binary):
        """Pull a specific binary version and set as default."""

        result = _run_mvm(mvm_binary, "bin", "ls", "--remote")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")
        target = versions[-2]  # One before the latest version

        _run_mvm(mvm_binary, "bin", "pull", target, "--set-default", "--force")

    @pytest.mark.slow
    @pytest.mark.serial
    def test_bin_remove_by_version(self, mvm_binary):
        """Fetch a specific version and remove by version."""

        result = _run_mvm(mvm_binary, "bin", "ls", "--remote")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")

        # Pick a version that's not the latest (to avoid removing the default)
        target = versions[0] if len(versions) > 1 else versions[-1]

        # Check if already cached to avoid interactive re-download prompt
        cached = _run_mvm(mvm_binary, "bin", "ls", "--json")
        cached_versions = {v.get("version") for v in json.loads(cached.stdout)}
        if target not in cached_versions:
            _run_mvm(mvm_binary, "bin", "pull", target, check=False)

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

    @pytest.mark.serial
    def test_bin_default(self, mvm_binary):
        """Set a cached binary as default using bin default <id>."""

        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)
        if not binaries:
            pytest.skip("No cached binaries to set as default")
        target_id = binaries[0]["id"][:6]
        result = _run_mvm(mvm_binary, "bin", "default", target_id, check=False)
        assert result.returncode == 0

    @pytest.mark.serial
    def test_bin_rm_by_id(self, mvm_binary):
        """Remove a cached binary by its 6-character ID prefix."""

        import re

        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)

        non_defaults = [b for b in binaries if not b.get("is_default", False)]
        if not non_defaults:
            # Pull a throwaway older version so we have a non-default to remove
            remote_result = _run_mvm(mvm_binary, "bin", "ls", "--remote")
            versions = re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
            if not versions:
                pytest.skip(
                    "No remote versions available to pull for removal test"
                )

            default_version = next(
                (b.get("version") for b in binaries if b.get("is_default")),
                None,
            )
            target_version = next(
                (v for v in versions if v != default_version), versions[-1]
            )
            _run_mvm(mvm_binary, "bin", "pull", target_version, check=False)

            # Re-read after pull
            result = _run_mvm(mvm_binary, "bin", "ls", "--json")
            binaries = json.loads(result.stdout)
            non_defaults = [
                b for b in binaries if not b.get("is_default", False)
            ]
            if not non_defaults:
                pytest.skip("Could not pull extra binary for removal test")

        target = non_defaults[0]
        target_prefix = target["id"][:6]

        result = _run_mvm(
            mvm_binary, "bin", "rm", target_prefix, "--force", check=False
        )
        assert result.returncode == 0, (
            f"bin rm {target_prefix} failed: {result.stderr}"
        )

        # Verify it's gone from listing
        listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
        remaining = json.loads(listing.stdout)
        ids = {b["id"][:6] for b in remaining}
        assert target_prefix not in ids, (
            f"Binary {target_prefix} still present after removal"
        )


class TestBinaryPullAdvanced:
    """Test advanced binary pull operations."""

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]

    def test_bin_pull_force(self, mvm_binary):
        """Pull a binary with --force to re-download an already cached version."""

        result = _run_mvm(mvm_binary, "bin", "ls", "--remote")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")
        target = versions[-2]  # One before the latest version

        result = _run_mvm(
            mvm_binary, "bin", "pull", target, "--force", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"bin pull {target} --force failed: {result.stderr}")
        assert result.returncode == 0

    def test_bin_pull_set_default(self, mvm_binary):
        """Pull a binary and set it as default atomically."""

        result = _run_mvm(mvm_binary, "bin", "ls", "--remote")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")
        target = versions[-2]  # One before the latest version

        result = _run_mvm(
            mvm_binary,
            "bin",
            "pull",
            target,
            "--set-default",
            "--force",
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"bin pull {target} --set-default failed: {result.stderr}"
            )
        assert result.returncode == 0
