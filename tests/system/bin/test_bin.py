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
# Edge cases (error paths and mirrored pull; no present-release removal)
# ============================================================================


class TestBinaryEdges:
    """Binary management edge case tests.

    The exact absent-slot removal is a typed no-op; pull cases use mirrored
    assets. Present-release removal is exercised separately below.
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
