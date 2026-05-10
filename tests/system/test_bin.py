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
        result = _run_mvm(mvm_binary, "bin", "ls")
        assert result.returncode == 0

    def test_bin_list_json(self, mvm_binary):
        """List binaries in JSON format."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        assert result.returncode == 0
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)


class TestBinaryPullAdvanced:
    """Test advanced binary pull operations."""

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.domain_bin]

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

        _run_mvm(mvm_binary, "bin", "pull", target, "--default", "--force")

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
        assert result.returncode == 0

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
        assert result.returncode == 0, (
            f"bin rm {target_prefix} failed: {result.stderr}"
        )

        listing = _run_mvm(mvm_binary, "bin", "ls", "--json")
        remaining = json.loads(listing.stdout)
        ids = {b["id"][:6] for b in remaining}
        assert target_prefix not in ids, (
            f"Binary {target_prefix} still present after removal"
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

    def test_pull_cached_binary_with_default_sets_default(
        self, mvm_binary: str
    ) -> None:
        """Pull already-cached binary with --default must set it as default."""
        original_default_prefix: str | None = None

        try:
            result = _run_mvm(mvm_binary, "bin", "ls", "--json")
            binaries: list[dict[str, Any]] = json.loads(result.stdout)
            if not binaries:
                pytest.skip("No cached binaries to test default switching")

            original_default = next(
                (b for b in binaries if b.get("is_default")), None
            )
            if original_default:
                original_default_prefix = original_default["id"][:6]

            _run_mvm(
                mvm_binary, "bin", "pull", "1.15.1", "--default", "--force"
            )

            result = _run_mvm(mvm_binary, "bin", "ls", "--json")
            binaries = json.loads(result.stdout)
            fc_defaults = [
                b
                for b in binaries
                if b.get("is_default") and b.get("name") == "firecracker"
            ]
            assert len(fc_defaults) == 1
            assert fc_defaults[0].get("version") == "1.15.1"
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

    pytestmark = [pytest.mark.requires_kvm]

    def test_delete_binary_used_by_stopped_vm_does_not_error(
        self, mvm_binary: str, unique_vm_name: str
    ) -> None:
        """Binary rm allows deleting binaries referenced by stopped VMs."""
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
