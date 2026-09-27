"""Firecracker binary management system tests."""

from __future__ import annotations

import json
import os
import platform
import re
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import (
    _ensure_binary,
    _guest_run,
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
    ensure_vm_deps,
)

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
        data: list[dict[str, Any]] = _json_success_data(result.stdout, list)
        for entry in data:
            assert isinstance(entry, dict), entry
            assert isinstance(entry.get("version"), str) and entry["version"], entry
            assert isinstance(entry.get("id"), str) and entry["id"], entry
            assert isinstance(entry.get("is_present"), bool), entry
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

    def test_bin_rm_removed_version_flag_rejected_before_effects(self, runner_vm: str) -> None:
        """A removed selector cannot alter any trusted release."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        rejected = _run_mvm(
            runner_vm, "bin", "rm", "--version", "999.999.999", check=False,
        )
        assert rejected.returncode != 0
        assert "unknown flag: --version" in (rejected.stdout + rejected.stderr).lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

    def test_bin_rm_exact_absent_version_is_skipped(self, runner_vm: str) -> None:
        """Root-authoritative removal reports a typed no-op for an absent exact slot."""
        version = "999.999.999"
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert all(item["version"] != version for item in before)
        data = _json_success_data(
            _run_mvm(runner_vm, "bin", "rm", version, "--json").stdout,
            dict,
        )
        assert len(data["items"]) == 1
        item = data["items"][0]
        assert item["error"] is None
        assert item["result"]["status"] == "skipped"
        assert item["result"]["code"] == "binary.remove_absent"
        assert item["result"]["item"] == version
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

    # ---- Destructive pull tests (still no rm) ----

    def test_bin_pull_present_exact_version_from_mirror(self, runner_vm: str) -> None:
        """An exact mirrored release can be re-admitted without changing its pair or default."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        default = next(
            (item for item in before if item["type"] == "firecracker" and item["is_default"]),
            None,
        )
        assert default is not None and default["is_present"]
        version = default["version"]
        prior_pair = [
            item for item in before
            if item["version"] == version and item["type"] in ("firecracker", "jailer")
        ]
        assert len(prior_pair) == 2 and all(item["is_present"] for item in prior_pair)
        prior_ids = {item["type"]: item["id"] for item in prior_pair}
        mirror = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser()
        archive = mirror / f"firecracker-v{version}-{platform.machine()}.tgz"
        assert archive.is_file(), f"binary pull test requires mirrored {archive.name}"

        pulled = _json_success_data(
            _run_mvm(runner_vm, "bin", "pull", version, "--json", timeout=300).stdout,
            list,
        )
        assert len(pulled) == 2
        assert {item["type"] for item in pulled} == {"firecracker", "jailer"}
        assert {item["version"] for item in pulled} == {version}
        assert all(item["is_present"] for item in pulled)
        assert {item["type"]: item["id"] for item in pulled} == prior_ids
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        for item in pulled:
            assert any(
                entry["id"] == item["id"] and entry["type"] == item["type"]
                and entry["version"] == version and entry["is_present"]
                for entry in after
            )
        assert {
            (item["type"], item["version"]) for item in after if item["is_default"]
        } == {("firecracker", version), ("jailer", version)}

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

    def test_bin_pull_removed_git_ref_rejected_before_effects(self, runner_vm: str) -> None:
        """The removed source-build selector must not change trusted-release state."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        rejected = _run_mvm(
            runner_vm, "bin", "pull", "1.16.0", "--git-ref", "main", check=False,
        )
        assert rejected.returncode != 0
        assert "unknown flag: --git-ref" in (rejected.stdout + rejected.stderr).lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

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
    def test_bin_pull_and_set_default(self, runner_vm: str) -> None:
        """Install an absent mirrored release, transfer defaults, then remove the test-owned slot."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        defaults = {item["type"]: item for item in before if item["is_default"]}
        assert set(defaults) == {"firecracker", "jailer"}
        assert defaults["firecracker"]["version"] == defaults["jailer"]["version"]
        mirror = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser()
        target = next(
            (
                version
                for version in ("1.16.1", "1.15.1")
                if all(item["version"] != version for item in before)
                and (mirror / f"firecracker-v{version}-{platform.machine()}.tgz").is_file()
            ),
            None,
        )
        assert target is not None, "test requires a mirrored release absent from this isolated runner"

        attempted = False
        installed = False
        try:
            attempted = True
            pulled = _json_success_data(
                _run_mvm(runner_vm, "bin", "pull", target, "--default", "--json", timeout=300).stdout,
                list,
            )
            installed = True
            assert len(pulled) == 2
            assert {item["type"] for item in pulled} == {"firecracker", "jailer"}
            assert {item["version"] for item in pulled} == {target}
            assert all(item["is_present"] for item in pulled)
            listing = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
            assert {
                (item["type"], item["version"], item["id"])
                for item in listing
                if item["is_default"]
            } == {(item["type"], target, item["id"]) for item in pulled}
        finally:
            restore_failures: list[str] = []
            for item in defaults.values():
                restored = _run_mvm(runner_vm, "bin", "default", item["id"], check=False)
                if restored.returncode != 0:
                    restore_failures.append(f"{item['type']}: {restored.stderr}")
            if attempted:
                removed = _json_success_data(
                    _run_mvm(runner_vm, "bin", "rm", target, "--force", "--json").stdout,
                    dict,
                )
                assert len(removed["items"]) == 1
                result = removed["items"][0]
                assert result["error"] is None
                assert result["result"]["item"] == target
                if installed:
                    assert result["result"]["status"] == "success"
                    assert result["result"]["code"] == "binary.removed"
                else:
                    assert result["result"]["status"] in ("success", "skipped")
            assert not restore_failures, restore_failures
            after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
            assert {
                (item["type"], item["version"], item["id"], item["is_default"])
                for item in after
            } == {
                (item["type"], item["version"], item["id"], item["is_default"])
                for item in before
            }

    def test_bin_rm_rejects_binary_id_without_effects(self, runner_vm: str) -> None:
        """The exact-version removal API must not interpret a display ID as a release slot."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        selected = next(
            item for item in before
            if item["type"] == "firecracker" and item["is_default"]
        )
        rejected = _run_mvm(runner_vm, "bin", "rm", selected["id"], check=False)
        assert rejected.returncode != 0
        assert "requires exact versions" in (rejected.stdout + rejected.stderr).lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before


# ============================================================================
# VM-integrated binary deletion
# ============================================================================


class TestBinaryLiveVMReleaseLease:
    """A running VM's trusted release cannot be removed."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_bin,
        pytest.mark.tier2,
        pytest.mark.needs_kvm,
    ]

    def test_force_remove_denied_and_identical_pull_keeps_live_release(
        self, runner_vm: str, unique_vm_name: str
    ) -> None:
        """Removal is denied; identical force-pull leaves the live pair usable."""
        vm_name = unique_vm_name
        ensure_vm_deps(runner_vm)

        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--no-network",
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--no-pci",
                "-s",
                "512M",
                "--mem",
                "256M",
                "--vcpu",
                "1",
                timeout=180,
            )
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(item for item in vms if item["name"] == vm_name)
            assert vm["status"] == "running"

            before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
            pair = [
                item for item in before
                if item["id"] in {vm["binary_id"], vm["jailer_binary_id"]}
            ]
            assert len(pair) == 2
            assert {item["type"] for item in pair} == {"firecracker", "jailer"}
            assert len({item["version"] for item in pair}) == 1
            assert all(item["is_present"] for item in pair)
            version = pair[0]["version"]

            denied = _run_mvm(
                runner_vm, "bin", "rm", version, "--force", "--json", check=False,
            )
            assert denied.returncode != 0
            envelope = json.loads(denied.stdout)
            assert envelope["schema_version"] == 1
            assert envelope["status"] == "error"
            assert len(envelope["data"]["items"]) == 1
            rejection = envelope["data"]["items"][0]
            assert rejection["result"]["item"] == version
            assert rejection["error"]["code"] == "vm.state.invalid"
            assert rejection["error"]["class"] == 2  # errs.ClassConflict
            assert "referenced by live privileged VM authority" in rejection["error"]["message"]

            mirror = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser()
            archive = mirror / f"firecracker-v{version}-{platform.machine()}.tgz"
            assert archive.is_file(), f"identical force-pull requires mirrored {archive.name}"
            pulled = _json_success_data(
                _run_mvm(
                    runner_vm, "bin", "pull", version, "--force", "--json", timeout=300,
                ).stdout,
                list,
            )
            assert {item["id"] for item in pulled} == {item["id"] for item in pair}
            assert {item["type"] for item in pulled} == {"firecracker", "jailer"}

            after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
            assert {
                (item["id"], item["type"], item["version"], item["path"],
                 item["is_default"], item["is_present"])
                for item in after
            } == {
                (item["id"], item["type"], item["version"], item["path"],
                 item["is_default"], item["is_present"])
                for item in before
            }
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert next(item for item in vms if item["name"] == vm_name)["status"] == "running"
        finally:
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if any(item["name"] == vm_name for item in vms):
                _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", timeout=120)
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert all(item["name"] != vm_name for item in vms)


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
