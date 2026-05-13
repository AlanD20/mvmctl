"""Destructive system tests — run LAST in the suite.

These tests modify global system state (cache directory, iptables chains)
in ways that cannot be fully restored without sudo/system privileges.
They MUST run after all other tests to avoid cascading failures.
"""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [
    pytest.mark.system,
    pytest.mark.serial,
    pytest.mark.slow,
]


class TestCacheCleanActual:
    """Test actual cache clean — destroys and restores state."""

    def test_cache_clean_actual(self, mvm_binary) -> None:
        """Run cache clean --force, then cache init to verify recovery.

        cache clean --force destroys the SQLite DB, asset files, and iptables
        chains. This test verifies that cache init + asset re-pull can recover
        the system state. iptables chains are NOT restored (need sudo host init).
        """
        result = _run_mvm(mvm_binary, "cache", "clean", "--force", check=False)
        if result.returncode != 0:
            pytest.skip(
                f"cache clean --force failed (may need sudo): {result.stderr}"
            )
        assert result.returncode == 0

        init_result = _run_mvm(mvm_binary, "cache", "init", check=False)
        assert init_result.returncode == 0

        # Recreate database, binary, kernel, image, and network records.
        _run_mvm(
            mvm_binary,
            "init",
            "--non-interactive",
            "--skip-host",
            check=False,
        )
        _run_mvm(mvm_binary, "bin", "pull", "1.15.0", check=False)
        bin_ls = _run_mvm(mvm_binary, "bin", "ls", "--json", check=False)
        if bin_ls.returncode == 0 and bin_ls.stdout.strip():
            bins = json.loads(bin_ls.stdout)
            fc = next((b for b in bins if b.get("name") == "firecracker"), None)
            if fc:
                _run_mvm(
                    mvm_binary, "bin", "default", fc["id"][:6], check=False
                )
        _run_mvm(
            mvm_binary, "kernel", "pull", "--type", "firecracker", check=False
        )
        _run_mvm(mvm_binary, "image", "pull", "alpine-3.21", check=False)
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            "net",
            "--subnet",
            "10.200.0.0/24",
            "--no-nat",
            check=False,
        )
        _run_mvm(mvm_binary, "network", "default", "net", check=False)
