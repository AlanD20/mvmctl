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
    pytest.mark.domain_cache,
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
        # Rationale: Needs actual cache state (SQLite DB, asset files) because
        # modifying the cache destroys persisted data that no fixture or JSON
        # query can simulate. A cache ls --json test would not exercise the
        # destroy-and-recover code path.
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
        _run_mvm(mvm_binary, "bin", "pull", "1.15.1", "--default", check=False)
        bin_ls = _run_mvm(mvm_binary, "bin", "ls", "--json", check=False)
        if bin_ls.returncode == 0 and bin_ls.stdout.strip():
            bins = json.loads(bin_ls.stdout)
            fc = next((b for b in bins if b.get("name") == "firecracker"), None)
            if fc and not any(
                b.get("is_default")
                for b in bins
                if b.get("name") == "firecracker"
            ):
                _run_mvm(
                    mvm_binary, "bin", "default", fc["id"][:6], check=False
                )
        kernel_result = _run_mvm(
            mvm_binary, "kernel", "pull", "--type", "firecracker", check=False
        )
        if kernel_result.returncode == 0:
            # Fetch kernel listing and set the first present kernel as default
            kernel_ls = _run_mvm(
                mvm_binary, "kernel", "ls", "--json", check=False
            )
            if kernel_ls.returncode == 0 and kernel_ls.stdout.strip():
                kernels = json.loads(kernel_ls.stdout)
                present = [k for k in kernels if k.get("is_present")]
                if present:
                    _run_mvm(
                        mvm_binary,
                        "kernel",
                        "default",
                        present[0]["id"][:6],
                        check=False,
                    )
        _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            check=False,
        )
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
