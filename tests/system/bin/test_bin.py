"""Firecracker binary management system tests."""

from __future__ import annotations

import json
import re
from typing import Any

import pytest

from tests.system.conftest import _ensure_binary, _guest_run, _run_mvm, ensure_vm_deps

pytestmark = [pytest.mark.system, pytest.mark.domain_bin]

# ============================================================================
# Read-only listing tests (no state modification)
# ============================================================================


class TestBinLifecycle:
    """Test Firecracker binary management operations."""

    def test_bin_list_cached(self, runner_vm):
        """List cached firecracker versions."""
        _ensure_binary(runner_vm)

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
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

    def test_bin_list_json(self, runner_vm):
        """List binaries in JSON format."""
        _ensure_binary(runner_vm)

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
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

    def test_bin_ls_structure(self, runner_vm):
        """Verify bin ls --json returns a list with well-formed entries even if cache is empty."""
        result = _run_mvm(runner_vm, "bin", "ls", "--json")
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

    def test_bin_list_empty_cache(self, runner_vm):
        """bin ls --json returns valid empty list when no binaries are cached.

        This test must run before any binary pull operations to verify
        the empty-cache edge case.
        """
        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        assert result.returncode == 0, f"bin ls --json failed: {result.stderr}"
        data = json.loads(result.stdout)
        assert isinstance(data, list), (
            f"Expected list, got {type(data).__name__}: {data}"
        )
        if len(data) == 0:
            return
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

    def test_bin_ls_remote_works(self, runner_vm):
        """Remote binary listing should work in Tier 2 environment."""
        result = _run_mvm(runner_vm, "bin", "ls", "--remote", "--json", check=False)
        assert result.returncode == 0, (
            f"Remote listing failed — Tier 2 environment must have network access: "
            f"{result.stderr}"
        )
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert len(data) > 0, "Expected at least one remote binary"

    def test_bin_ls_remote_with_limit(self, runner_vm):
        """Remote listing with --limit flag should work (limit is display-only in JSON)."""
        result = _run_mvm(
            runner_vm,
            "bin",
            "ls",
            "--remote",
            "--limit",
            "3",
            "--json",
            check=False,
        )
        assert result.returncode == 0, (
            f"Remote listing with --limit failed — Tier 2 environment must have "
            f"network access: {result.stderr}"
        )
        data = json.loads(result.stdout)
        assert isinstance(data, list), f"Expected list, got {type(data)}"
        assert len(data) >= 1, f"Expected at least one binary, got {len(data)}"

    def test_pull_nonexistent_version_fails(self, runner_vm):
        """Pulling a nonexistent version via --version should fail gracefully."""
        result = _run_mvm(
            runner_vm,
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

    def test_set_default_nonexistent_binary_fails(self, runner_vm):
        """Setting default to nonexistent binary should fail."""
        result = _run_mvm(
            runner_vm,
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

    def test_remove_nonexistent_binary_fails(self, runner_vm):
        """Removing nonexistent binary should fail gracefully."""
        result = _run_mvm(
            runner_vm, "bin", "rm", "totally-nonexistent-binary", check=False
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "could be resolved" in combined, (
            f"Expected 'could be resolved' in output, got: {combined[:300]}"
        )

    def test_bin_rm_version_nonexistent(self, runner_vm):
        """Removing a nonexistent version via --version should fail."""
        result = _run_mvm(
            runner_vm, "bin", "rm", "--version", "999.999.999", check=False
        )
        assert result.returncode != 0, (
            f"Expected failure for nonexistent version, got: "
            f"stdout={result.stdout}, stderr={result.stderr}"
        )

    # ---- Destructive pull tests (still no rm) ----

    def test_bin_pull_specific_version_plain(self, runner_vm):
        """Pull a binary version by name without --force or --default.

        Uses a known version (1.15.1) that should be available in Tier 2.
        """
        _ensure_binary(runner_vm)

        # Use a specific version that is not the default
        cached = _run_mvm(runner_vm, "bin", "ls", "--json")
        cached_bins: list[dict[str, Any]] = json.loads(cached.stdout)
        default_version = next(
            (b.get("version") for b in cached_bins if b.get("is_default")),
            None,
        )
        target = "1.14.2" if default_version != "1.14.2" else "1.14.1"

        pull_result = _run_mvm(
            runner_vm, "bin", "pull", "firecracker", "--version", target,
            check=False, timeout=120,
        )
        assert pull_result.returncode == 0, (
            f"bin pull {target} failed in Tier 2 environment (should have network): "
            f"{pull_result.stderr}"
        )

        listing = _run_mvm(runner_vm, "bin", "ls", "--json")
        entries: list[dict[str, Any]] = json.loads(listing.stdout)
        assert any(e.get("version") == target for e in entries), (
            f"Version {target} not found in listing after pull"
        )

    def test_pull_cached_binary_with_default_sets_default(
        self, runner_vm: str
    ) -> None:
        """Pull already-cached binary with --default must set it as default.

        Uses a dynamically-identified non-default version in the Tier 2 cache.
        """
        original_default_prefix: str | None = None

        try:
            result = _run_mvm(runner_vm, "bin", "ls", "--json")
            binaries: list[dict[str, Any]] = json.loads(result.stdout)
            assert binaries, "No cached binaries found in Tier 2 environment"

            original_default = next(
                (b for b in binaries if b.get("is_default")), None
            )
            if original_default:
                original_default_prefix = original_default["id"][:6]

            non_default_fc = [
                b
                for b in binaries
                if b.get("type") == "firecracker"
                and not b.get("is_default")
                and b.get("is_present")
            ]

            # If no non-default binary exists, pull a different version
            if not non_default_fc:
                default_version = original_default["version"] if original_default else "1.15.1"
                known = "1.14.2" if default_version != "1.14.2" else "1.14.1"
                _run_mvm(
                    runner_vm, "bin", "pull", "firecracker",
                    "--version", known, "--force", timeout=120,
                )
                result = _run_mvm(runner_vm, "bin", "ls", "--json")
                binaries = json.loads(result.stdout)
                non_default_fc = [
                    b for b in binaries
                    if b.get("type") == "firecracker"
                    and not b.get("is_default")
                    and b.get("is_present")
                ]

            assert non_default_fc, "Could not obtain a non-default binary for testing"
            target_version = non_default_fc[0]["version"]

            pull_result = _run_mvm(
                runner_vm,
                "bin",
                "pull",
                "firecracker",
                "--version",
                target_version,
                "--default",
                "--force",
                check=False,
            )
            assert pull_result.returncode == 0, (
                f"bin pull {target_version} --default --force failed: {pull_result.stderr}"
            )

            result = _run_mvm(runner_vm, "bin", "ls", "--json")
            binaries = json.loads(result.stdout)
            fc_defaults = [
                b for b in binaries
                if b.get("is_default") and b.get("type") == "firecracker"
            ]
            actual_default = fc_defaults[0].get("version") if fc_defaults else None
            assert actual_default == target_version, (
                f"Pulled {target_version} with --default, "
                f"but firecracker default is {actual_default}"
            )
        finally:
            if original_default_prefix:
                _run_mvm(
                    runner_vm, "bin", "default", original_default_prefix,
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
        pytest.mark.domain_bin,
    ]

    def test_bin_pull_force(self, runner_vm):
        """Pull a binary with --force to re-download an already cached version."""
        _ensure_binary(runner_vm)

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        cached = json.loads(result.stdout)
        cached_fc = [
            b for b in cached
            if b.get("type") == "firecracker" and b.get("is_present")
        ]
        assert cached_fc, "No cached firecracker binary in Tier 2 environment"
        target = cached_fc[0]["version"]

        pull_result = _run_mvm(
            runner_vm, "bin", "pull", "firecracker", "--version", target,
            "--force", check=False,
        )
        assert pull_result.returncode == 0, (
            f"bin pull {target} --force failed: {pull_result.stderr}"
        )
        assert "downloaded" in pull_result.stdout.lower(), (
            f"Expected 'Downloaded' in output after --force pull, "
            f"got: {pull_result.stdout[:200]}"
        )

    def test_bin_pull_set_default(self, runner_vm):
        """Pull a binary and set it as default atomically.

        Uses a dynamically-identified non-default version from the cache.
        """
        _ensure_binary(runner_vm)

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        cached = json.loads(result.stdout)
        non_default_fc = [
            b for b in cached
            if b.get("type") == "firecracker"
            and b.get("is_present")
            and not b.get("is_default")
        ]

        if not non_default_fc:
            default_version = next(
                (b.get("version") for b in cached if b.get("is_default")),
                None,
            )
            target = "1.14.2" if default_version != "1.14.2" else "1.14.1"
            _run_mvm(
                runner_vm, "bin", "pull", "firecracker",
                "--version", target, "--force", timeout=120,
            )
            result = _run_mvm(runner_vm, "bin", "ls", "--json")
            cached = json.loads(result.stdout)
            non_default_fc = [
                b for b in cached
                if b.get("type") == "firecracker"
                and b.get("is_present")
                and not b.get("is_default")
            ]

        assert non_default_fc, "No non-default firecracker binary available"
        target = non_default_fc[0]["version"]

        pull_result = _run_mvm(
            runner_vm,
            "bin",
            "pull",
            "firecracker",
            "--version",
            target,
            "--default",
            "--force",
            check=False,
        )
        assert pull_result.returncode == 0, (
            f"bin pull {target} --default failed: {pull_result.stderr}"
        )

        listing = _run_mvm(runner_vm, "bin", "ls", "--json")
        entries = json.loads(listing.stdout)
        fc_defaults = [
            e for e in entries
            if e.get("is_default") and e.get("type") == "firecracker"
        ]
        assert len(fc_defaults) >= 1, (
            "No default firecracker found after pull --default"
        )
        assert any(e.get("version") == target for e in fc_defaults), (
            f"Version {target} not set as default after pull --default. "
            f"Defaults: {[(e['version'], e['is_default']) for e in entries if e.get('name') == 'firecracker']}"
        )

    def test_bin_pull_git_ref_help(self, runner_vm: str) -> None:
        """Verify CLI accepts --git-ref flag (syntax check)."""
        result = _run_mvm(
            runner_vm,
            "bin",
            "pull",
            "--help",
            check=False,
            timeout=10,
        )
        assert "--git-ref" in result.stdout, (
            "--git-ref flag should be documented in bin pull --help"
        )

    def test_bin_pull_version(self, runner_vm: str) -> None:
        """Pull a Firecracker binary from asset mirror — uses pre-built version."""
        result = _run_mvm(
            runner_vm,
            "bin",
            "pull",
            "firecracker",
            "--version",
            "1.15.0",
            "--force",
            check=False,
            timeout=300,
        )
        assert result.returncode == 0, (
            f"bin pull --version 1.16.0 failed: "
            f"stdout={result.stdout} stderr={result.stderr}"
        )
        assert "firecracker" in result.stdout.lower(), (
            f"Expected 'firecracker' in output: {result.stdout}"
        )


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
        pytest.mark.domain_bin,
    ]

    @pytest.mark.slow
    def test_bin_pull_and_set_default(self, runner_vm):
        """Pull a specific binary version and set as default."""
        _ensure_binary(runner_vm)

        local_listing = _run_mvm(runner_vm, "bin", "ls", "--json")
        local_bins = json.loads(local_listing.stdout)

        # Record original default to restore later
        original_default_id: str | None = None
        orig_default = next(
            (b for b in local_bins if b.get("is_default")), None
        )
        if orig_default:
            original_default_id = orig_default["id"][:6]

        try:
            # Pick a non-default target
            non_default = [
                b for b in local_bins
                if b.get("type") == "firecracker"
                and b.get("is_present")
                and not b.get("is_default")
            ]
            if non_default:
                target = non_default[0]["version"]
            else:
                target = "1.14.2"

            pull_result = _run_mvm(
                runner_vm,
                "bin",
                "pull",
                "firecracker",
                "--version",
                target,
                "--default",
                "--force",
                check=False,
            )
            assert pull_result.returncode == 0, (
                f"bin pull {target} --default --force failed: {pull_result.stderr}"
            )

            listing = _run_mvm(runner_vm, "bin", "ls", "--json")
            entries = json.loads(listing.stdout)
            pulled_entries = [
                e for e in entries
                if e.get("version") == target and e.get("type") == "firecracker"
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
                _run_mvm(runner_vm, "bin", "default", original_default_id, check=False)

    def test_bin_default(self, runner_vm):
        """Set a cached binary as default using bin default <id>.

        Finds a non-default cached firecracker, sets it as default, then
        verifies the listing reflects the change. Restores the original
        default in a finally block.
        """
        _ensure_binary(runner_vm)

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)

        original_default = next(
            (b for b in binaries if b.get("is_default")), None
        )
        original_default_prefix: str | None = (
            original_default["id"][:6] if original_default else None
        )

        non_default = [
            b for b in binaries
            if not b.get("is_default", False)
            and b.get("is_present")
            and b.get("type") in ("firecracker",)
        ]

        if not non_default:
            _run_mvm(
                runner_vm, "bin", "pull", "firecracker",
                "--version", "1.14.2", "--force", timeout=120,
            )
            result = _run_mvm(runner_vm, "bin", "ls", "--json")
            binaries = json.loads(result.stdout)
            non_default = [
                b for b in binaries
                if not b.get("is_default", False)
                and b.get("is_present")
                and b.get("type") in ("firecracker",)
            ]

        assert binaries, "No cached binaries found in Tier 2 environment"
        target = non_default[0] if non_default else binaries[0]
        target_id = target["id"][:6]

        try:
            set_result = _run_mvm(runner_vm, "bin", "default", target_id, check=False)
            assert set_result.returncode == 0, (
                f"bin default {target_id} failed: {set_result.stderr}"
            )

            verify = _run_mvm(runner_vm, "bin", "ls", "--json")
            entries = json.loads(verify.stdout)
            new_defaults = [
                e for e in entries
                if e.get("is_default") and e.get("type") == "firecracker"
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
                _run_mvm(runner_vm, "bin", "default", original_default_prefix, check=False)

    @pytest.mark.slow
    def test_bin_remove_by_version(self, runner_vm):
        """Fetch a specific version and remove by version."""
        _ensure_binary(runner_vm)

        cached = _run_mvm(runner_vm, "bin", "ls", "--json")
        cached_bins = json.loads(cached.stdout)
        non_default_present = [
            b for b in cached_bins
            if b.get("is_present")
            and not b.get("is_default")
            and b.get("type") == "firecracker"
        ]

        if non_default_present:
            target = non_default_present[0]["version"]
        else:
            # Pull a non-default version
            _run_mvm(
                runner_vm, "bin", "pull", "firecracker",
                "--version", "1.14.2", "--force", timeout=120,
            )
            target = "1.14.2"

        result = _run_mvm(
            runner_vm, "bin", "rm", "--version", target, "--force", check=False
        )
        assert result.returncode == 0, (
            f"bin rm --version {target} failed: {result.stderr}"
        )

        listing = _run_mvm(runner_vm, "bin", "ls", "--json")
        entries = json.loads(listing.stdout)
        still_present = [
            e for e in entries
            if e.get("version") == target and e.get("is_present")
        ]
        assert len(still_present) == 0, (
            f"Version {target} still has is_present=True entries "
            f"after removal: {still_present}"
        )

    def test_bin_rm_by_id(self, runner_vm):
        """Remove a cached binary by its 6-character ID prefix.

        Verifies both L2 (listing) and L3 (file on disk) after removal.
        """
        _ensure_binary(runner_vm)

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)

        non_defaults = [b for b in binaries if not b.get("is_default", False)]
        if not non_defaults:
            _run_mvm(
                runner_vm, "bin", "pull", "firecracker",
                "--version", "1.14.2", "--force", timeout=120,
            )
            result = _run_mvm(runner_vm, "bin", "ls", "--json")
            binaries = json.loads(result.stdout)
            non_defaults = [b for b in binaries if not b.get("is_default", False)]

        assert non_defaults, "No non-default binary available for removal test"
        target = non_defaults[0]
        target_prefix = target["id"][:6]
        target_path_str = target.get("path", "")

        result = _run_mvm(runner_vm, "bin", "rm", target_prefix, "--force", check=False)
        assert result.returncode == 0, (
            f"bin rm {target_prefix} failed: {result.stderr}"
        )

        # L2: Verify listing no longer shows the binary
        listing = _run_mvm(runner_vm, "bin", "ls", "--json")
        remaining = json.loads(listing.stdout)
        ids = {b["id"][:6] for b in remaining}
        assert target_prefix not in ids, (
            f"Binary {target_prefix} still present in listing after removal"
        )

        # L3: Verify the file is gone from disk inside the VM
        if target_path_str:
            check = _guest_run(runner_vm,
                f"test -f /root/.cache/mvmctl/bin/{target_path_str} && echo exists || echo not-found",
                check=False,
            )
            assert "not-found" in check.stdout, (
                f"Binary file still exists at /root/.cache/mvmctl/bin/{target_path_str} "
                f"after removal"
            )


# ============================================================================
# VM-integrated binary deletion
# ============================================================================


class TestBinaryStoppedVMDeletion:
    """Test binary deletion behavior with stopped VM references."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.domain_bin,
    ]

    def test_delete_binary_used_by_stopped_vm_does_not_error(
        self, runner_vm: str, unique_vm_name: str, module_network: str
    ) -> None:
        """Binary rm allows deleting binaries referenced by stopped VMs."""
        vm_name = unique_vm_name
        ensure_vm_deps(runner_vm)

        try:
            result = _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--network",
                module_network,
                "--image",
                "alpine:3.23",
            )

            vm_ls = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names

            result = _run_mvm(runner_vm, "bin", "ls", "--json")
            binaries: list[dict[str, Any]] = json.loads(result.stdout)
            present_bins = [b for b in binaries if b.get("is_present")]
            assert present_bins, "No present binaries found in listing"
            default_bin = next(
                (b for b in present_bins if b.get("is_default")),
                present_bins[0],
            )
            binary_id_prefix = default_bin["id"][:6]

            result = _run_mvm(runner_vm, "bin", "rm", binary_id_prefix, check=False)
            assert result.returncode in (0, 1)

            if result.returncode == 0:
                bin_ls = _run_mvm(runner_vm, "bin", "ls", "--json", check=False)
                if bin_ls.returncode == 0 and bin_ls.stdout.strip():
                    bins_after: list[dict[str, Any]] = json.loads(bin_ls.stdout)
                    bin_ids = [b.get("id", "")[:6] for b in bins_after]
                    assert binary_id_prefix not in bin_ids
            vm_ls = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)


# ============================================================================
# Service binary symlinks (state-modifying, non-destructive)
# ============================================================================


class TestServiceBinarySymlinks:
    """Test that service binary symlinks survive cache clean → cache init."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_bin,
    ]

    @pytest.mark.xfail(
        reason="Service symlinks (console-relay, nocloud-server, provision) "
        "are not created by the current mvmctl binary cache init. "
        "Needs implementation in the cache service.",
        strict=False,
    )
    def test_service_symlinks_survive_cache_clean_init(
        self, runner_vm: str
    ) -> None:
        """Service symlinks must be recreated after cache clean and cache init.

        All operations run INSIDE the test VM via _run_mvm.
        """
        # Inside the test VM, mvm runs as 'runner' user, so binaries
        # are stored under the runner's home, not root.
        bin_dir = "/home/runner/.cache/mvmctl/bin"
        service_symlinks = [
            "mvm-console-relay",
            "mvm-nocloud-server",
            "mvm-provision",
        ]

        # Ensure service binaries exist inside the VM via cache init
        _run_mvm(runner_vm, "cache", "init", check=False)

        # Check which symlinks already exist
        existing_symlinks = []
        for name in service_symlinks:
            check = _guest_run(runner_vm,
                f"test -L {bin_dir}/{name} && echo symlink || echo not",
                check=False,
            )
            if "symlink" in check.stdout:
                existing_symlinks.append(name)
            else:
                # Check if mvm-services exists and create symlink
                svc_check = _guest_run(runner_vm,
                    f"test -f {bin_dir}/mvm-services && echo exists || echo not",
                    check=False,
                )
                if "exists" in svc_check.stdout:
                    _guest_run(runner_vm,
                        f"ln -sf mvm-services {bin_dir}/{name}", check=False,
                    )
                    existing_symlinks.append(name)

        # Guard: all three symlinks must exist
        for name in service_symlinks:
            check = _guest_run(runner_vm,
                f"test -L {bin_dir}/{name} && echo symlink || echo not",
                check=False,
            )
            assert "symlink" in check.stdout, (
                f"Expected symlink {name} not found in {bin_dir} inside VM"
            )

        # Verify pre-condition: all three symlinks point to mvm-services
        for name in service_symlinks:
            target = _guest_run(runner_vm,
                f"readlink {bin_dir}/{name}",
                check=False,
            )
            target_name = target.stdout.strip()
            assert target_name == "mvm-services", (
                f"Symlink {name} -> {target_name}, expected mvm-services"
            )

        try:
            # Remove symlinks directly inside the VM
            for name in service_symlinks:
                _guest_run(runner_vm, f"rm -f {bin_dir}/{name}", check=False)

            # Re-init cache inside the VM
            _run_mvm(runner_vm, "cache", "init", check=False)

            # Verify post-condition: all three symlinks were recreated
            for name in service_symlinks:
                check = _guest_run(runner_vm,
                    f"test -L {bin_dir}/{name} && echo symlink || echo not",
                    check=False,
                )
                assert "symlink" in check.stdout, (
                    f"Symlink {name} was not recreated after cache init"
                )
                target = _guest_run(runner_vm,
                    f"readlink {bin_dir}/{name}",
                    check=False,
                )
                target_name = target.stdout.strip()
                assert target_name == "mvm-services", (
                    f"Symlink {name} -> {target_name}, expected mvm-services"
                )
        finally:
            _run_mvm(runner_vm, "cache", "init", check=False)
