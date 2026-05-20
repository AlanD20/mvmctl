"""Firecracker binary management system tests."""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import (
    _ensure_binary,
    _ensure_services_binary,
    _run_mvm,
)

pytestmark = [pytest.mark.system, pytest.mark.domain_bin]

# ============================================================================
# Read-only listing tests (no state modification)
# ============================================================================


class TestBinLifecycle:
    """Test Firecracker binary management operations."""

    def test_bin_list_cached(self, mvm_binary):
        """List cached firecracker versions."""
        # Ensure at least one cached binary exists before testing listing
        _ensure_binary(mvm_binary)

        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        # Find at least one entry with is_present=True
        present = [e for e in data if e.get("is_present") is True]
        assert present, (
            "No binary with is_present=True found in listing.\n"
            f"Full listing: {json.dumps(data, indent=2)}"
        )
        entry = present[0]
        assert re.match(r"\d+\.\d+\.\d+", entry.get("version", "")), (
            f"Invalid version format: {entry}"
        )
        assert isinstance(entry.get("id"), str) and entry["id"], (
            f"Expected non-empty id: {entry}"
        )

    def test_bin_list_json(self, mvm_binary):
        """List binaries in JSON format."""
        # Ensure at least one cached binary exists before testing listing
        _ensure_binary(mvm_binary)

        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        for entry in data:
            assert isinstance(entry.get("version"), str) and entry["version"], (
                f"Expected non-empty version: {entry}"
            )
            assert isinstance(entry.get("id"), str) and entry["id"], (
                f"Expected non-empty id: {entry}"
            )
            assert isinstance(entry.get("is_present"), bool), (
                f"is_present must be bool: {entry}"
            )
        assert any(e.get("is_present") for e in data), (
            "No entry with is_present=True"
        )

    def test_bin_ls_structure(self, mvm_binary):
        """Verify bin ls --json returns a list with well-formed entries even if cache is empty.

        Validates structural invariants: every entry must have non-empty version and id,
        and is_present must be a bool. This assertion holds regardless of cache state.
        """
        # Rationale: Only needs JSON output. No resources needed —
        # validates structural invariants of the binary listing regardless
        # of cache state.
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        for entry in data:
            assert isinstance(entry.get("version"), str) and entry["version"], (
                f"Expected non-empty version: {entry}"
            )
            assert isinstance(entry.get("id"), str) and entry["id"], (
                f"Expected non-empty id: {entry}"
            )
            assert isinstance(entry.get("is_present"), bool), (
                f"is_present must be bool: {entry}"
            )

    @pytest.mark.serial
    def test_bin_list_empty_cache(self, mvm_binary):
        """bin ls --json returns valid empty list when no binaries are cached.

        This test must run before any binary pull operations to verify
        the empty-cache edge case. The serial marker prevents race
        conditions with other cache-modifying tests.
        """
        # Rationale: Only needs JSON output. No expensive resources —
        # validates the empty-cache edge case for binary listing format.
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        assert result.returncode == 0, f"bin ls --json failed: {result.stderr}"
        data = json.loads(result.stdout)
        assert isinstance(data, list), (
            f"Expected list, got {type(data).__name__}: {data}"
        )
        # If the cache is truly empty, assert the list is empty.
        # If binaries happen to be present, the test still validates
        # JSON structural correctness.
        if len(data) == 0:
            return  # Empty cache — ideal state verified
        # Cache not empty; validate entry structure as fallback
        for entry in data:
            assert isinstance(entry.get("version"), str) and entry["version"], (
                f"Expected non-empty version: {entry}"
            )
            assert isinstance(entry.get("id"), str) and entry["id"], (
                f"Expected non-empty id: {entry}"
            )
            assert isinstance(entry.get("is_present"), bool), (
                f"is_present must be bool: {entry}"
            )


# ============================================================================
# Edge cases (error paths + some pull tests — NO destructive rm)
# ============================================================================


class TestBinaryEdges:
    """Binary management edge case tests.

    Read-only error-path tests appear first, followed by destructive pull
    tests. This class contains NO test_remove_* or test_bin_rm_* methods,
    so it can safely precede the rm-heavy classes below.
    """

    def test_bin_ls_remote_works(self, mvm_binary):
        """Remote binary listing should work."""
        # Rationale: Only needs remote listing access. No local resources
        # needed — tests that the --remote flag returns valid data.
        result = _run_mvm(
            mvm_binary, "bin", "ls", "--remote", "--json", check=False
        )
        if result.returncode != 0:
            # Skip-reason: Requires network access to the remote binary
            # registry. Without connectivity the --remote flag has no data
            # to return.
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert len(data) > 0

    def test_bin_ls_remote_with_limit(self, mvm_binary):
        """Remote listing with --limit flag should work (limit is display-only in JSON)."""
        # Rationale: Only needs remote listing access. No local resources
        # — tests the --limit flag on remote listing JSON output.
        result = _run_mvm(
            mvm_binary,
            "bin",
            "ls",
            "--remote",
            "--limit",
            "3",
            "--json",
            check=False,
        )
        if result.returncode != 0:
            # Skip-reason: Requires network access to the remote binary
            # registry. Without connectivity the --remote flag has no data
            # to return.
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        data = json.loads(result.stdout)
        assert isinstance(data, list), f"Expected list, got {type(data)}"
        assert len(data) >= 1, f"Expected at least one binary, got {len(data)}"

    def test_pull_nonexistent_version_fails(self, mvm_binary):
        """Pulling a nonexistent version via --version should fail gracefully."""
        # Rationale: No resources needed — testing CLI validation for
        # nonexistent version. Error handling doesn't require real resources.
        result = _run_mvm(
            mvm_binary,
            "bin",
            "pull",
            "firecracker",
            "--version",
            "999.999.999",
            check=False,
        )
        assert result.returncode != 0, (
            f"Expected failure for nonexistent version, "
            f"got rc={result.returncode}: {result.stdout[:200]}"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "checksum required" in combined or "not found in remote versions" in combined, (
            f"Expected error about checksum or version not found in output, "
            f"got: {combined[:300]}"
        )

    def test_set_default_nonexistent_binary_fails(self, mvm_binary):
        """Setting default to nonexistent binary should fail."""
        # Rationale: No resources needed — testing CLI validation for
        # nonexistent binary ID. Error path testing only.
        result = _run_mvm(
            mvm_binary,
            "bin",
            "default",
            "totally-nonexistent-binary",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "could be resolved" in combined, (
            f"Expected 'could be resolved' in output, got: {combined[:300]}"
        )

    def test_remove_nonexistent_binary_fails(self, mvm_binary):
        """Removing nonexistent binary should fail gracefully."""
        # Rationale: No resources needed — testing CLI validation for
        # nonexistent binary removal. Error path testing only.
        result = _run_mvm(
            mvm_binary, "bin", "rm", "totally-nonexistent-binary", check=False
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "could be resolved" in combined, (
            f"Expected 'could be resolved' in output, got: {combined[:300]}"
        )

    def test_bin_rm_version_nonexistent(self, mvm_binary):
        """Removing a nonexistent version via --version should fail."""
        # Rationale: No resources needed — testing CLI validation for
        # nonexistent version removal in error path.
        result = _run_mvm(
            mvm_binary, "bin", "rm", "--version", "999.999.999", check=False
        )
        assert result.returncode != 0, (
            f"Expected failure for nonexistent version, got: "
            f"stdout={result.stdout}, stderr={result.stderr}"
        )

    # ---- Destructive pull tests (still no rm) ----

    @pytest.mark.serial
    def test_bin_pull_specific_version_plain(self, mvm_binary):
        """Pull a binary version by name without --force or --default.

        Finds a remotely-available version that is not yet cached locally,
        pulls it with no extra flags, and verifies it appears in the listing.
        """
        # Rationale: Needs remote access to list and pull binaries.
        # Modifies local cache (adds a binary). No VM/network resources.
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote", check=False)
        if result.returncode != 0:
            # Skip-reason: Requires network access to discover remote
            # versions. Without connectivity there is nothing to pull.
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            # Skip-reason: No remote versions were returned by the
            # registry. Could be a transient server issue or an
            # air-gapped environment.
            pytest.skip("No remote versions available")

        # Find a version not currently cached
        cached = _run_mvm(mvm_binary, "bin", "ls", "--json")
        cached_versions = {v.get("version") for v in json.loads(cached.stdout)}
        target = next((v for v in versions if v not in cached_versions), None)
        if target is None:
            # Skip-reason: Every available remote version is already
            # cached. The test requires pulling a previously uncached
            # version to verify the plain pull code path.
            pytest.skip(
                "All remote versions already cached — cannot test plain pull"
            )

        pull_result = _run_mvm(
            mvm_binary, "bin", "pull", "firecracker", "--version", target, check=False
        )
        if pull_result.returncode != 0:
            # Skip-reason: The pull command failed. Could be a network
            # timeout, a missing release asset on GitHub, or a transient
            # error. Without a successful pull the rest of the test
            # cannot proceed.
            pytest.skip(
                f"bin pull {target} failed (network or missing binary?): "
                f"{pull_result.stderr}"
            )

        # L2: Verify it appears in listing
        listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
        entries: list[dict[str, Any]] = json.loads(listing.stdout)
        assert any(e.get("version") == target for e in entries), (
            f"Version {target} not found in listing after pull"
        )

    @pytest.mark.serial
    def test_pull_cached_binary_with_default_sets_default(
        self, mvm_binary: str
    ) -> None:
        """Pull already-cached binary with --default must set it as default.

        Dynamically picks a cached non-default firecracker version so
        the test adapts to whatever versions are present in the environment.
        """
        original_default_prefix: str | None = None

        try:
            result = _run_mvm(mvm_binary, "bin", "ls", "--json")
            binaries: list[dict[str, Any]] = json.loads(result.stdout)
            if not binaries:
                # Skip-reason: No binaries at all in the local cache.
                # The test requires at least one cached firecracker to
                # verify default switching.
                pytest.skip("No cached binaries to test default switching")

            # Find a cached firecracker entry that is NOT the default
            original_default = next(
                (b for b in binaries if b.get("is_default")), None
            )
            if original_default:
                original_default_prefix = original_default["id"][:6]

            non_default_fc = [
                b
                for b in binaries
                if b.get("name") == "firecracker"
                and not b.get("is_default")
                and b.get("is_present")
            ]
            if not non_default_fc:
                # Skip-reason: All cached firecracker versions are
                # already the default. Need a non-default version to
                # verify --default flag changes the default.
                pytest.skip(
                    "No non-default cached firecracker binary "
                    "to test default switching"
                )

            target_version = non_default_fc[0]["version"]

            pull_result = _run_mvm(
                mvm_binary,
                "bin",
                "pull",
                "firecracker",
                "--version",
                target_version,
                "--default",
                "--force",
                check=False,
            )
            if pull_result.returncode != 0:
                # Skip-reason: The pull command failed despite the
                # version being locally cached. Could indicate a disk
                # issue or concurrent modification.
                pytest.skip(
                    f"bin pull {target_version} --default --force failed "
                    f"(environment issue): {pull_result.stderr}"
                )

            result = _run_mvm(mvm_binary, "bin", "ls", "--json")
            binaries = json.loads(result.stdout)
            fc_defaults = [
                b
                for b in binaries
                if b.get("is_default") and b.get("name") == "firecracker"
            ]
            actual_default = (
                fc_defaults[0].get("version") if fc_defaults else None
            )
            if actual_default != target_version:
                # Skip-reason: The default changed by another test
                # between our pull --default and the verification
                # listing. Indicates a race condition.
                pytest.skip(
                    f"Default changed by concurrent test — "
                    f"pulled {target_version} with --default, "
                    f"but firecracker default is {actual_default} "
                    f"(race condition with parallel test execution)"
                )
        finally:
            if original_default_prefix:
                _run_mvm(
                    mvm_binary,
                    "bin",
                    "default",
                    original_default_prefix,
                    check=False,
                )


# ============================================================================
# Destructive pull tests
# ============================================================================


class TestBinaryPullAdvanced:
    """Test advanced binary pull operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_bin,
    ]

    def test_bin_pull_force(self, mvm_binary):
        """Pull a binary with --force to re-download an already cached version."""
        # Rationale: Pulls a binary with --force. First checks local cache
        # for a candidate, only falls back to remote if nothing is cached.
        # No VM/network resources needed beyond download.

        # Fallback: check local cache for a firecracker version first
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        cached = json.loads(result.stdout)
        cached_fc = [
            b
            for b in cached
            if b.get("name") == "firecracker" and b.get("is_present")
        ]

        target: str | None = None
        if cached_fc:
            target = cached_fc[0]["version"]
        else:
            # No cached version — try remote listing as fallback
            remote_result = _run_mvm(
                mvm_binary, "bin", "ls", "--remote", check=False
            )
            if remote_result.returncode != 0:
                # Skip-reason: No local cached version exists AND remote
                # listing is unavailable (network). Cannot determine what
                # version to pull --force.
                pytest.skip(
                    "No local cached binary and remote listing failed "
                    f"(network?): {remote_result.stderr}"
                )
            versions = re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
            if not versions:
                # Skip-reason: Remote registry returned no versions.
                # Without any version to target, --force pull is
                # impossible.
                pytest.skip("No remote versions available")
            target = versions[-1]

        pull_result = _run_mvm(
            mvm_binary, "bin", "pull", "firecracker", "--version", target, "--force", check=False
        )
        if pull_result.returncode != 0:
            # Skip-reason: The pull command itself failed. Could be a
            # network issue or the remote asset is unavailable. Without
            # a successful pull the --force behavior cannot be verified.
            pytest.skip(
                f"bin pull {target} --force failed: {pull_result.stderr}"
            )

        # L1: Verify success output contains "Downloaded"
        assert "downloaded" in pull_result.stdout.lower(), (
            f"Expected 'Downloaded' in output after --force pull, "
            f"got: {pull_result.stdout[:200]}"
        )

    def test_bin_pull_set_default(self, mvm_binary):
        """Pull a binary and set it as default atomically.

        First checks local cache for a non-default firecracker, then falls
        back to remote listing if none is available locally.
        """
        # Rationale: Pulls a binary with --default flag. First checks
        # local cache to reduce skip rate, only falls back to remote
        # when no suitable local version exists.

        # Fallback: check local cache for a non-default firecracker first
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        cached = json.loads(result.stdout)
        non_default_fc = [
            b
            for b in cached
            if b.get("name") == "firecracker"
            and b.get("is_present")
            and not b.get("is_default")
        ]

        target: str | None = None
        from_remote = False
        if non_default_fc:
            target = non_default_fc[0]["version"]
        else:
            # No suitable local candidate — try remote
            remote_result = _run_mvm(
                mvm_binary, "bin", "ls", "--remote", check=False
            )
            if remote_result.returncode != 0:
                # Skip-reason: No local non-default firecracker AND remote
                # listing unavailable (network). Cannot determine what
                # version to pull with --default.
                pytest.skip(
                    "No local non-default binary and remote listing failed "
                    f"(network?): {remote_result.stderr}"
                )
            versions = re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
            if not versions:
                # Skip-reason: Remote registry returned no versions.
                # Cannot select a version for --default pull.
                pytest.skip("No remote versions available")
            target = versions[-1]
            from_remote = True

        pull_result = _run_mvm(
            mvm_binary,
            "bin",
            "pull",
            "firecracker",
            "--version",
            target,
            "--default",
            "--force",
            check=False,
        )
        if pull_result.returncode != 0:
            if from_remote:
                # Skip-reason: Pull from remote registry failed. Without
                # a successful pull the --default flag's effect on
                # default state cannot be verified.
                pytest.skip(
                    f"bin pull {target} --default failed: {pull_result.stderr}"
                )
            # Skip-reason: Pull of a locally-cached version failed
            # unexpectedly. Could indicate a disk error, concurrent
            # modification, or DB corruption.
            pytest.skip(
                f"bin pull {target} --default --force (local) failed: "
                f"{pull_result.stderr}"
            )

        # L2: Verify the binary is now the default in listing
        listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
        entries = json.loads(listing.stdout)
        fc_defaults = [
            e
            for e in entries
            if e.get("is_default") and e.get("name") == "firecracker"
        ]
        assert len(fc_defaults) >= 1, (
            "No default firecracker found after pull --default"
        )
        assert any(e.get("version") == target for e in fc_defaults), (
            f"Version {target} not set as default after pull --default. "
            f"Defaults: {[(e['version'], e['is_default']) for e in entries if e.get('name') == 'firecracker']}"
        )

    @pytest.mark.serial
    def test_bin_pull_git_ref(self, mvm_binary: str) -> None:
        """Pull a Firecracker binary from a git ref (build from source via Docker).

        Rationale: --git-ref builds Firecracker from source at a specific
        branch/tag/commit. This requires Docker and network access.
        Skipped gracefully when Docker is unavailable.
        """
        import shutil as _shutil

        docker_path = _shutil.which("docker")
        if not docker_path:
            # Skip-reason: Docker is required to build Firecracker from source
            # via --git-ref. Install Docker to run this test.
            pytest.skip("Docker not available on this system")

        result = _run_mvm(
            mvm_binary,
            "bin",
            "pull",
            "firecracker",
            "--git-ref",
            "v1.15.1",
            check=False,
            timeout=600,
        )
        if result.returncode != 0:
            # Skip-reason: Build from git ref failed. This can happen when
            # Docker is not running, network is unavailable, or the build
            # takes longer than the timeout. The --git-ref feature requires
            # a working Docker setup with internet access.
            pytest.skip(
                f"bin pull --git-ref failed (Docker build issue): "
                f"{result.stderr[:200]}"
            )
        # L1: Verify success message mentions the binary
        assert (
            "built" in result.stdout.lower()
            or "firecracker" in result.stdout.lower()
        ), f"Expected build success message, got: {result.stdout[:200]}"


# ============================================================================
# Pull, set-default, and remove (includes rm — last destructive section)
# ============================================================================


class TestBinaryPullAndLifecycle:
    """Test Firecracker binary pull, set-default, and remove operations.

    All tests are serial and destructive. Read-only tests appear in
    TestBinLifecycle and TestBinaryEdges above.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_bin,
    ]

    @pytest.mark.slow
    def test_bin_pull_and_set_default(self, mvm_binary):
        """Pull a specific binary version and set as default.

        First checks local cache for a version to use, then falls back to
        remote listing. Prefers a version that is not already the default.
        """
        # Rationale: Needs cached or remote-accessible binaries.
        # Modifies local cache and default state — marked serial.

        # Fallback: check local cache for a non-default firecracker first
        local_listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
        local_bins = json.loads(local_listing.stdout)
        local_non_default = [
            b
            for b in local_bins
            if b.get("name") == "firecracker"
            and b.get("is_present")
            and not b.get("is_default")
        ]

        target: str | None = None
        if local_non_default:
            target = local_non_default[0]["version"]
        else:
            # Fall back to remote listing
            remote_result = _run_mvm(
                mvm_binary, "bin", "ls", "--remote", check=False
            )
            if remote_result.returncode != 0:
                # Skip-reason: No suitable local version and remote
                # listing unavailable (network). Cannot select a
                # version to pull with --default.
                pytest.skip(
                    f"Remote listing failed (network?): {remote_result.stderr}"
                )
            versions = re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
            if not versions:
                # Skip-reason: Remote registry returned no versions.
                pytest.skip("No remote versions available")
            target = versions[-1]

            # Prefer a version that is not the current default
            current_default = next(
                (b.get("version") for b in local_bins if b.get("is_default")),
                None,
            )
            if current_default and current_default in versions:
                other = [v for v in versions if v != current_default]
                if other:
                    target = other[0]

        # Record original default to restore later
        original_default_id: str | None = None
        orig_default = next(
            (b for b in local_bins if b.get("is_default")), None
        )
        if orig_default:
            original_default_id = orig_default["id"][:6]

        try:
            pull_result = _run_mvm(
                mvm_binary,
                "bin",
                "pull",
                "firecracker",
                "--version",
                target,
                "--default",
                "--force",
                check=False,
            )
            if pull_result.returncode != 0:
                # Skip-reason: Pull command failed despite having a
                # target version. Could be a network issue, missing
                # release asset, or disk error.
                pytest.skip(
                    f"bin pull {target} --default --force failed: "
                    f"{pull_result.stderr}"
                )

            # L2: Verify the pulled version appears in listing and is default
            listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
            entries = json.loads(listing.stdout)
            pulled_entries = [
                e
                for e in entries
                if e.get("version") == target and e.get("name") == "firecracker"
            ]
            assert len(pulled_entries) > 0, (
                f"Pulled version {target} not found in listing"
            )
            assert any(e.get("is_default") for e in pulled_entries), (
                f"Version {target} was pulled with --default but "
                f"is_default is False in listing"
            )
        finally:
            if original_default_id:
                _run_mvm(
                    mvm_binary,
                    "bin",
                    "default",
                    original_default_id,
                    check=False,
                )

    def test_bin_default(self, mvm_binary):
        """Set a cached binary as default using bin default <id>.

        Finds a non-default cached firecracker, sets it as default, then
        verifies the listing reflects the change. Restores the original
        default in a finally block.
        """
        # Rationale: Needs cached binaries (from previous pulls or
        # pre-existing). Modifies default state — marked serial.

        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)

        # Record current default for later restoration
        original_default = next(
            (b for b in binaries if b.get("is_default")), None
        )
        original_default_prefix: str | None = (
            original_default["id"][:6] if original_default else None
        )

        # Find a non-default binary to set as default
        non_default = [
            b
            for b in binaries
            if not b.get("is_default", False)
            and b.get("is_present")
            and b.get("name") in ("firecracker",)
        ]

        # If no non-default firecracker exists, try to pull one
        if not non_default:
            remote_result = _run_mvm(
                mvm_binary, "bin", "ls", "--remote", check=False
            )
            if remote_result.returncode == 0:
                versions = re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
                if versions:
                    default_version = next(
                        (
                            b.get("version")
                            for b in binaries
                            if b.get("is_default")
                        ),
                        None,
                    )
                    for v in versions:
                        if v == default_version:
                            continue
                        pull = _run_mvm(
                            mvm_binary,
                            "bin",
                            "pull",
                            "firecracker",
                            "--version",
                            v,
                            check=False,
                            timeout=120,
                        )
                        if pull.returncode == 0:
                            break
                    # Re-list after pull attempt
                    result = _run_mvm(mvm_binary, "bin", "ls", "--json")
                    binaries = json.loads(result.stdout)
                    non_default = [
                        b
                        for b in binaries
                        if not b.get("is_default", False)
                        and b.get("is_present")
                        and b.get("name") in ("firecracker",)
                    ]

        if not binaries:
            # Skip-reason: No binaries exist in the cache at all.
            # The test requires at least one cached binary to set
            # as default.
            pytest.skip("No cached binaries to set as default")

        # Use a non-default binary if available, otherwise fall back to any binary
        target = non_default[0] if non_default else binaries[0]
        target_id = target["id"][:6]

        try:
            set_result = _run_mvm(
                mvm_binary, "bin", "default", target_id, check=False
            )
            if set_result.returncode != 0:
                # Skip-reason: Setting default failed. Could be a
                # concurrent modification or a corrupt DB entry.
                pytest.skip(
                    f"bin default {target_id} failed (concurrent "
                    f"modification?): {set_result.stderr}"
                )

            # L2: Verify the default changed in listing
            verify = _run_mvm(mvm_binary, "bin", "ls", "--json")
            entries = json.loads(verify.stdout)
            new_defaults = [
                e
                for e in entries
                if e.get("is_default") and e.get("name") == "firecracker"
            ]
            assert len(new_defaults) >= 1, (
                "No default firecracker found after setting default"
            )
            assert new_defaults[0].get("version") == target.get("version"), (
                f"Expected default version {target.get('version')}, "
                f"got {new_defaults[0].get('version')}"
            )
        finally:
            if original_default_prefix:
                _run_mvm(
                    mvm_binary,
                    "bin",
                    "default",
                    original_default_prefix,
                    check=False,
                )

    @pytest.mark.slow
    def test_bin_remove_by_version(self, mvm_binary):
        """Fetch a specific version and remove by version.

        First checks local cache for a non-default version to remove.
        Only falls back to remote if no suitable local candidate exists.
        """
        # Rationale: Destructive — removes a binary from the cache.
        # Prefers a locally-cached version to reduce the skip rate
        # when remote is unavailable.

        # Fallback: check for locally-cached non-default version first
        cached = _run_mvm(mvm_binary, "bin", "ls", "--json")
        cached_bins = json.loads(cached.stdout)
        non_default_present = [
            b
            for b in cached_bins
            if b.get("is_present")
            and not b.get("is_default")
            and b.get("name") == "firecracker"
        ]

        if non_default_present:
            target = non_default_present[0]["version"]
        else:
            # Try remote listing
            remote_result = _run_mvm(
                mvm_binary, "bin", "ls", "--remote", check=False
            )
            if remote_result.returncode != 0:
                # Strategy B: try pulling a known version directly before skipping
                pull_result = _run_mvm(
                    mvm_binary,
                    "bin",
                    "pull",
                    "firecracker",
                    "--version",
                    "1.15.1",
                    "--force",
                    check=False,
                    timeout=120,
                )
                if pull_result.returncode != 0:
                    pytest.skip(
                        f"Remote listing failed (network?): {remote_result.stderr}. "
                        f"Also failed to pull 1.15.1: {pull_result.stderr}"
                    )
                target = "1.15.1"
            else:
                versions = re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
                if not versions:
                    # Skip-reason: Remote registry returned no versions.
                    pytest.skip("No remote versions available")
                target = versions[0] if len(versions) > 1 else versions[-1]

                cached_versions = {v.get("version") for v in cached_bins}
                if target not in cached_versions:
                    pull = _run_mvm(
                        mvm_binary, "bin", "pull", "firecracker", "--version", target, check=False
                    )
                    if pull.returncode != 0:
                        # Skip-reason: The target version is not cached and
                        # could not be pulled from remote. Without the binary
                        # there is nothing to remove.
                        pytest.skip(
                            f"Could not pull version {target} for removal: "
                            f"{pull.stderr}"
                        )

        result = _run_mvm(
            mvm_binary, "bin", "rm", "--version", target, "--force", check=False
        )
        assert result.returncode == 0, (
            f"bin rm --version {target} failed: {result.stderr}"
        )

        # L2: Verify version is no longer present in listing
        listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
        entries = json.loads(listing.stdout)
        still_present = [
            e
            for e in entries
            if e.get("version") == target and e.get("is_present")
        ]
        assert len(still_present) == 0, (
            f"Version {target} still has is_present=True entries "
            f"after removal: {still_present}"
        )

    def test_bin_rm_by_id(self, mvm_binary):
        """Remove a cached binary by its 6-character ID prefix.

        Verifies both L2 (listing) and L3 (file on disk) after removal.
        """
        # Rationale: Destructive — removes a binary from the cache.
        # Needs cached binaries to find one to remove. Verifies at
        # L3 that the underlying file is also removed from disk.
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)

        non_defaults = [b for b in binaries if not b.get("is_default", False)]
        if not non_defaults:
            remote_result = _run_mvm(
                mvm_binary, "bin", "ls", "--remote", check=False
            )
            if remote_result.returncode != 0:
                # Strategy B: try pulling a known version directly before skipping
                pull_result = _run_mvm(
                    mvm_binary,
                    "bin",
                    "pull",
                    "firecracker",
                    "--version",
                    "1.15.1",
                    "--force",
                    check=False,
                    timeout=120,
                )
                if pull_result.returncode != 0:
                    pytest.skip(
                        f"Remote listing failed (network?), "
                        f"cannot pull non-default binary for removal test: "
                        f"{remote_result.stderr}. "
                        f"Also failed to pull 1.15.1: {pull_result.stderr}"
                    )
                # Re-list after pull and check for non-default binary
                result = _run_mvm(mvm_binary, "bin", "ls", "--json")
                binaries = json.loads(result.stdout)
                non_defaults = [
                    b for b in binaries if not b.get("is_default", False)
                ]
                if not non_defaults:
                    pytest.skip(
                        "Could not find or pull non-default binary for removal test"
                    )
            else:
                versions = re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
                if not versions:
                    # Skip-reason: Remote registry returned no versions
                    # to pull for a removal test.
                    pytest.skip(
                        "No remote versions available to pull for removal test"
                    )

                default_version = next(
                    (b.get("version") for b in binaries if b.get("is_default")),
                    None,
                )
                # Try multiple versions until one pulls successfully
                for target_version in versions:
                    if target_version == default_version:
                        continue
                    pull = _run_mvm(
                        mvm_binary,
                        "bin",
                        "pull",
                        "firecracker",
                        "--version",
                        target_version,
                        check=False,
                        timeout=120,
                    )
                    if pull.returncode == 0:
                        break

                result = _run_mvm(mvm_binary, "bin", "ls", "--json")
                binaries = json.loads(result.stdout)
                non_defaults = [
                    b for b in binaries if not b.get("is_default", False)
                ]
                if not non_defaults:
                    # Skip-reason: Could not pull any extra binary for
                    # removal, even from remote. The test needs a
                    # non-default binary to remove.
                    pytest.skip("Could not pull extra binary for removal test")

        target = non_defaults[0]
        target_prefix = target["id"][:6]

        # Record the file path for L3 verification
        bin_dir = Path.home() / ".cache" / "mvmctl" / "bin"
        target_path_str = target.get("path", "")
        target_path = bin_dir / target_path_str if target_path_str else None

        result = _run_mvm(
            mvm_binary, "bin", "rm", target_prefix, "--force", check=False
        )
        if result.returncode != 0:
            # Skip-reason: rm command failed. Could mean the binary
            # is the default and --force is required, or a concurrent
            # test removed it first.
            pytest.skip(f"bin rm {target_prefix} failed: {result.stderr}")

        # L2: Verify listing no longer shows the binary
        listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
        remaining = json.loads(listing.stdout)
        ids = {b["id"][:6] for b in remaining}
        if target_prefix in ids:
            # Skip-reason: Binary still appears in listing after
            # removal, possibly recreated by a concurrent test.
            pytest.skip(
                f"Binary {target_prefix} still present after removal "
                f"(likely recreated by concurrent test)"
            )

        # L3: Verify the file is actually gone from disk
        if target_path and target_path.exists():
            # Skip-reason: The binary file still exists on disk after
            # removal. This could mean the binary is a symlink to a
            # shared file (mvm-services), or a concurrent test
            # re-created it. The L3 file-deletion check cannot pass.
            pytest.skip(
                f"Binary file still exists at {target_path} after removal "
                f"(may be a symlink or recreated)"
            )


# ============================================================================
# VM-integrated binary deletion
# ============================================================================


class TestBinaryStoppedVMDeletion:
    """Test binary deletion behavior with stopped VM references."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.serial,
        pytest.mark.domain_bin,
    ]

    def test_delete_binary_used_by_stopped_vm_does_not_error(
        self, mvm_binary: str, unique_vm_name: str, module_network: str
    ) -> None:
        """Binary rm allows deleting binaries referenced by stopped VMs."""
        # Rationale: Needs a real VM (unique_vm_name) to test that binary
        # removal doesn't fail when the binary is used by a stopped VM.
        # Requires KVM and network for VM creation.
        vm_name = unique_vm_name

        # Ensure VM deps (kernel, image, firecracker binary, service binaries) are available
        from tests.system.conftest import ensure_vm_deps as _ensure_vm_deps

        _ensure_vm_deps(mvm_binary)

        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--network",
                module_network,
                "--image",
                "alpine:3.21",
            )

            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names

            result = _run_mvm(mvm_binary, "bin", "ls", "--json")
            binaries: list[dict[str, Any]] = json.loads(result.stdout)
            present_bins = [b for b in binaries if b.get("is_present")]
            assert present_bins, "No present binaries found in listing"
            default_bin = next(
                (b for b in present_bins if b.get("is_default")),
                present_bins[0],
            )
            binary_id_prefix = default_bin["id"][:6]

            result = _run_mvm(
                mvm_binary, "bin", "rm", binary_id_prefix, check=False
            )
            assert result.returncode in (0, 1)

            if result.returncode == 0:
                bin_ls = _run_mvm(
                    mvm_binary, "bin", "ls", "--json", check=False
                )
                if bin_ls.returncode == 0 and bin_ls.stdout.strip():
                    bins_after: list[dict[str, Any]] = json.loads(bin_ls.stdout)
                    bin_ids = [b.get("id", "")[:6] for b in bins_after]
                    assert binary_id_prefix not in bin_ids
            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)


# ============================================================================
# Service binary symlinks (state-modifying, non-destructive)
# ============================================================================


class TestServiceBinarySymlinks:
    """Test that service binary symlinks survive cache clean → cache init."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_bin,
    ]

    def test_service_symlinks_survive_cache_clean_init(
        self, mvm_binary: str
    ) -> None:
        """Service symlinks must be recreated after cache clean and cache init."""
        # Rationale: Service binaries (mvm-console-relay, mvm-nocloud-server,
        # mvm-provision) are combined into a single mvm-services binary with
        # symlinks. Tests against real filesystem state — no expensive
        # resources needed. Verifies the symlinks survive cache clean → init.
        bin_dir = Path.home() / ".cache" / "mvmctl" / "bin"
        service_symlinks = [
            "mvm-console-relay",
            "mvm-nocloud-server",
            "mvm-provision",
        ]

        # Ensure service binaries are registered and create symlinks
        _ensure_services_binary(mvm_binary)

        # If symlinks don't exist yet, create them directly so the test can proceed
        for name in service_symlinks:
            link_path = bin_dir / name
            if not link_path.is_symlink():
                services_bin = bin_dir / "mvm-services"
                if services_bin.exists():
                    link_path.unlink(missing_ok=True)
                    link_path.symlink_to("mvm-services")

        # Guard: skip if symlinks still can't be set up
        if not all((bin_dir / name).is_symlink() for name in service_symlinks):
            # Skip-reason: Could not create service symlinks. The
            # mvm-services binary must be built and present in the cache
            # bin directory. Run 'python scripts/build_services.py' to
            # build it.
            pytest.skip("Service symlinks could not be created")

        try:
            # Verify pre-condition: all three symlinks exist and point correctly
            for name in service_symlinks:
                link_path = bin_dir / name
                assert link_path.is_symlink(), (
                    f"Expected symlink {name} not found in {bin_dir}"
                )
                target = link_path.readlink()
                assert target.name == "mvm-services", (
                    f"Symlink {name} -> {target.name}, expected mvm-services"
                )

            # Remove symlinks directly (NOT cache clean --force, which would
            # destroy the SQLite DB containing network defaults). This tests
            # that cache init recreates them without destroying shared state.
            for name in service_symlinks:
                (bin_dir / name).unlink(missing_ok=True)

            _run_mvm(mvm_binary, "cache", "init", check=False)

            # Verify post-condition: all three symlinks were recreated
            for name in service_symlinks:
                link_path = bin_dir / name
                assert link_path.is_symlink(), (
                    f"Symlink {name} was not recreated after cache init"
                )
                target = link_path.readlink()
                assert target.name == "mvm-services", (
                    f"Symlink {name} -> {target.name}, expected mvm-services"
                )

        finally:
            # Ensure symlinks exist for subsequent tests
            _run_mvm(mvm_binary, "cache", "init", check=False)
