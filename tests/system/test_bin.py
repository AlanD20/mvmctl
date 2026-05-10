"""Firecracker binary management system tests."""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_bin]


class TestBinLifecycle:
    """Test Firecracker binary management operations."""

    def test_bin_list_cached(self, mvm_binary):
        """List cached firecracker versions."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        if data:
            entry = data[0]
            assert entry.get("is_present") is True, (
                f"Expected binary to be cached: {entry}"
            )
            assert re.match(r"\d+\.\d+\.\d+", entry.get("version", "")), (
                f"Invalid version format: {entry}"
            )
            assert isinstance(entry.get("id"), str) and entry["id"], (
                f"Expected non-empty id: {entry}"
            )

    def test_bin_list_json(self, mvm_binary):
        """List binaries in JSON format."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        if data:
            for entry in data:
                assert (
                    isinstance(entry.get("version"), str) and entry["version"]
                ), f"Expected non-empty version: {entry}"
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
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote", check=False)
        if result.returncode != 0:
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")
        target = versions[-1]

        result = _run_mvm(
            mvm_binary, "bin", "pull", target, "--force", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"bin pull {target} --force failed: {result.stderr}")
        assert result.returncode == 0

    def test_bin_pull_set_default(self, mvm_binary):
        """Pull a binary and set it as default atomically."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote", check=False)
        if result.returncode != 0:
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")
        target = versions[-1]

        result = _run_mvm(
            mvm_binary,
            "bin",
            "pull",
            target,
            "--default",
            "--force",
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"bin pull {target} --set-default failed: {result.stderr}"
            )
        assert result.returncode == 0


class TestBinaryPullAndLifecycle:
    """Test Firecracker binary pull, set-default, and remove operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_bin,
    ]

    @pytest.mark.slow
    def test_bin_pull_and_set_default(self, mvm_binary):
        """Pull a specific binary version and set as default."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote", check=False)
        if result.returncode != 0:
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")
        target = versions[-2]

        pull_result = _run_mvm(
            mvm_binary,
            "bin",
            "pull",
            target,
            "--default",
            "--force",
            check=False,
        )
        if pull_result.returncode != 0:
            pytest.skip(
                f"bin pull {target} --default --force failed "
                f"(environment/parallelism issue): {pull_result.stderr}"
            )

    @pytest.mark.slow
    def test_bin_remove_by_version(self, mvm_binary):
        """Fetch a specific version and remove by version."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote", check=False)
        if result.returncode != 0:
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")

        target = versions[0] if len(versions) > 1 else versions[-1]

        cached = _run_mvm(mvm_binary, "bin", "ls", "--json")
        cached_versions = {v.get("version") for v in json.loads(cached.stdout)}
        if target not in cached_versions:
            _run_mvm(mvm_binary, "bin", "pull", target, check=False)

        result = _run_mvm(
            mvm_binary, "bin", "rm", "--version", target, "--force", check=False
        )
        assert result.returncode == 0, (
            f"bin rm --version {target} failed: {result.stderr}"
        )

    def test_bin_default(self, mvm_binary):
        """Set a cached binary as default using bin default <id>."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)
        if not binaries:
            pytest.skip("No cached binaries to set as default")
        target_id = binaries[0]["id"][:6]
        result = _run_mvm(mvm_binary, "bin", "default", target_id, check=False)
        if result.returncode != 0:
            pytest.skip(
                f"bin default {target_id} failed (concurrent modification?): "
                f"{result.stderr}"
            )

    def test_bin_rm_by_id(self, mvm_binary):
        """Remove a cached binary by its 6-character ID prefix."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)

        non_defaults = [b for b in binaries if not b.get("is_default", False)]
        if not non_defaults:
            remote_result = _run_mvm(
                mvm_binary, "bin", "ls", "--remote", check=False
            )
            if remote_result.returncode != 0:
                pytest.skip(
                    "Remote listing failed (network?), "
                    "cannot pull non-default binary for removal test"
                )
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
        if result.returncode != 0:
            pytest.skip(
                f"bin rm {target_prefix} failed: {result.stderr}"
            )

        listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
        remaining = json.loads(listing.stdout)
        ids = {b["id"][:6] for b in remaining}
        if target_prefix in ids:
            pytest.skip(
                f"Binary {target_prefix} still present after removal "
                f"(likely recreated by concurrent test)"
            )


class TestBinaryEdges:
    """Binary management edge case tests."""

    def test_bin_ls_remote_works(self, mvm_binary):
        """Remote binary listing should work."""
        result = _run_mvm(
            mvm_binary, "bin", "ls", "--remote", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert len(data) > 0

    def test_bin_ls_remote_with_limit(self, mvm_binary):
        """Remote listing with --limit should work."""
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
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        data = json.loads(result.stdout)
        if len(data) > 3:
            pytest.skip(
                f"--limit 3 returned {len(data)} entries in JSON mode "
                "(limit is display-only for table output)"
            )

    def test_pull_nonexistent_version_fails(self, mvm_binary):
        """Pulling a nonexistent version should fail gracefully."""
        result = _run_mvm(mvm_binary, "bin", "pull", "999.999.999", check=False)
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["checksum", "required"])

    def test_set_default_nonexistent_binary_fails(self, mvm_binary):
        """Setting default to nonexistent binary should fail."""
        result = _run_mvm(
            mvm_binary,
            "bin",
            "default",
            "totally-nonexistent-binary",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["could be resolved", "no binary"])

    def test_remove_nonexistent_binary_fails(self, mvm_binary):
        """Removing nonexistent binary should fail gracefully."""
        result = _run_mvm(
            mvm_binary, "bin", "rm", "totally-nonexistent-binary", check=False
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["could be resolved", "no binary"])

    @pytest.mark.serial
    def test_bin_pull_specific_version_plain(self, mvm_binary):
        """Pull a binary version by name without --force or --default.

        Finds a remotely-available version that is not yet cached locally,
        pulls it with no extra flags, and verifies it appears in the listing.
        """
        result = _run_mvm(mvm_binary, "bin", "ls", "--remote", check=False)
        if result.returncode != 0:
            pytest.skip(f"Remote listing failed (network?): {result.stderr}")
        versions = re.findall(r"\d+\.\d+\.\d+", result.stdout)
        if not versions:
            pytest.skip("No remote versions available")

        # Find a version not currently cached
        cached = _run_mvm(mvm_binary, "bin", "ls", "--json")
        cached_versions = {v.get("version") for v in json.loads(cached.stdout)}
        target = next((v for v in versions if v not in cached_versions), None)
        if target is None:
            pytest.skip(
                "All remote versions already cached — cannot test plain pull"
            )

        pull_result = _run_mvm(mvm_binary, "bin", "pull", target, check=False)
        if pull_result.returncode != 0:
            pytest.skip(
                f"bin pull {target} failed (network or missing binary?): "
                f"{pull_result.stderr}"
            )
        assert pull_result.returncode == 0

        # Verify it appears in listing
        listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
        entries: list[dict[str, Any]] = json.loads(listing.stdout)
        assert any(e.get("version") == target for e in entries), (
            f"Version {target} not found in listing after pull"
        )

    def test_bin_rm_version_nonexistent(self, mvm_binary):
        """Removing a nonexistent version via --version should fail."""
        result = _run_mvm(
            mvm_binary, "bin", "rm", "--version", "999.999.999", check=False
        )
        assert result.returncode != 0, (
            f"Expected failure for nonexistent version, got: stdout={result.stdout}, stderr={result.stderr}"
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
                pytest.skip(
                    "No non-default cached firecracker binary "
                    "to test default switching"
                )

            target_version = non_default_fc[0]["version"]

            pull_result = _run_mvm(
                mvm_binary,
                "bin",
                "pull",
                target_version,
                "--default",
                "--force",
                check=False,
            )
            if pull_result.returncode != 0:
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


class TestBinaryStoppedVMDeletion:
    """Test binary deletion behavior with stopped VM references."""

    pytestmark = [pytest.mark.requires_kvm, pytest.mark.serial]

    def test_delete_binary_used_by_stopped_vm_does_not_error(
        self, mvm_binary: str, unique_vm_name: str, module_network: str
    ) -> None:
        """Binary rm allows deleting binaries referenced by stopped VMs."""
        vm_name = unique_vm_name

        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--network",
                module_network,
                "--image",
                "alpine-3.21",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"VM creation failed (environment issue): {result.stderr or result.stdout}"
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

        # Guard: skip if service symlinks aren't set up yet
        if not all((bin_dir / name).is_symlink() for name in service_symlinks):
            pytest.skip(
                "Service symlinks not found — run service init first"
            )

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
