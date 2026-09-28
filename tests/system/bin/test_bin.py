"""Firecracker binary management system tests."""

from __future__ import annotations

import json
import os
import platform
import re
import stat
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import (
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
)

pytestmark = [pytest.mark.system, pytest.mark.domain_bin]


def _installed_pair(binaries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Require the preinstalled launch pair without repairing runner defaults."""
    pair = [item for item in binaries if item["is_default"]]
    assert len(pair) == 2, pair
    assert {item["type"] for item in pair} == {"firecracker", "jailer"}
    for item in pair:
        assert item["version"] == "1.16.2", item
        assert item["architecture"] == platform.machine(), item
        assert item["is_present"] is True, item
        assert re.fullmatch(r"[0-9a-f]{64}", item["id"]), item
        assert item["path"] == (
            f"/var/lib/mvmctl/binaries/{item['architecture']}/1.16.2/{item['type']}"
        ), item
    return pair


def _binary_identity(binaries: list[dict[str, Any]]) -> set[tuple]:
    """Compare release identities and defaults while allowing projection timestamps to refresh."""
    return {
        (item["id"], item["type"], item["architecture"], item["version"],
         item["path"], item["is_default"], item["is_present"])
        for item in binaries
    }


def _file_identity(path: Path) -> tuple:
    """Record exact regular files rather than following a substituted fixture leaf."""
    info = path.lstat()
    assert stat.S_ISREG(info.st_mode) and info.st_nlink == 1, path
    return (info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode,
            info.st_mtime_ns, info.st_ctime_ns, info.st_size)


# ============================================================================
# Read-only listing tests (no state modification)
# ============================================================================


class TestBinLifecycle:
    """Test Firecracker binary management operations."""

    def test_bin_list_cached(self, runner_vm):
        """List cached firecracker versions."""
        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        data: list[dict[str, Any]] = _json_success_data(result.stdout, list)
        for entry in data:
            assert isinstance(entry, dict), entry
            assert isinstance(entry.get("version"), str) and entry["version"], entry
            assert isinstance(entry.get("id"), str) and entry["id"], entry
            assert isinstance(entry.get("is_present"), bool), entry
        _installed_pair(data)


# ============================================================================
# Edge cases (error paths and mirrored pull; no present-release removal)
# ============================================================================


class TestBinaryEdges:
    """Binary management edge case tests.

    The exact absent-slot removal is a typed no-op; pull cases use mirrored
    assets. Present-release removal is exercised separately below.
    """

    def test_bin_ls_remote_limit_applies_to_json(self, runner_vm: str) -> None:
        """Remote release metadata is structured, and --limit bounds the JSON data."""
        default = _json_success_data(
            _run_mvm(runner_vm, "bin", "ls", "--remote", "--json", timeout=60).stdout,
            list,
        )
        assert len(default) > 3, "remote source must expose enough releases to prove --limit"
        limited = _json_success_data(
            _run_mvm(
                runner_vm, "bin", "ls", "--remote", "--limit", "3", "--json", timeout=60,
            ).stdout,
            list,
        )
        assert len(limited) == 3
        assert [item["version"] for item in limited] == [item["version"] for item in default[:3]]
        for item in limited:
            assert re.fullmatch(r"\d+\.\d+\.\d+", item["version"])
            assert item["type"] == "bin"
            assert item["name"] == "firecracker"

    def test_bin_pull_invalid_version_rejected_before_effects(self, runner_vm: str) -> None:
        """Malformed exact selectors fail locally without contacting the asset source."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        rejected = _run_mvm(runner_vm, "bin", "pull", "not-a-version", "--json", check=False)
        assert rejected.returncode != 0
        envelope = json.loads(rejected.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["error"]["code"] == "binary.pull_failed"
        assert "invalid version spec" in envelope["error"]["message"].lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

    def test_bin_default_missing_id_rejected_before_effects(self, runner_vm: str) -> None:
        """A missing binary ID leaves both default projections unchanged."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        rejected = _run_mvm(
            runner_vm, "bin", "default", "totally-nonexistent-binary", "--json", check=False,
        )
        assert rejected.returncode != 0
        envelope = json.loads(rejected.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["error"]["code"] == "binary.default_set_failed"
        assert "could be resolved" in envelope["error"]["message"].lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

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
        prior_pair = _installed_pair(before)
        version = "1.16.2"
        prior_ids = {item["type"]: item["id"] for item in prior_pair}
        mirror = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser()
        archive = mirror / f"firecracker-v{version}-{platform.machine()}.tgz"
        archive_identity = _file_identity(archive)
        assert archive_identity[-1] > 0, archive

        pulled = _json_success_data(
            _run_mvm(runner_vm, "bin", "pull", version, "--json", timeout=300).stdout,
            list,
        )
        assert len(pulled) == 2
        assert {item["type"] for item in pulled} == {"firecracker", "jailer"}
        assert {item["version"] for item in pulled} == {version}
        assert all(item["is_present"] for item in pulled)
        assert {item["type"]: item["id"] for item in pulled} == prior_ids
        assert _binary_identity(pulled) == _binary_identity(prior_pair)
        assert _file_identity(archive) == archive_identity
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert _binary_identity(after) == _binary_identity(before)

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
    @pytest.mark.destructive
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
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        pair = _installed_pair(before)
        version = "1.16.2"
        mirror = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser()
        archive = mirror / f"firecracker-v{version}-{platform.machine()}.tgz"
        archive_identity = _file_identity(archive)
        assert archive_identity[-1] > 0, archive
        guestfs = _json_success_data(
            _run_mvm(runner_vm, "config", "get", "settings", "guestfs_enabled", "--json").stdout,
            bool,
        )
        assert guestfs is False, "live release fixture requires the prepared LoopMount runner"
        kernels = _json_success_data(_run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list)
        kernel = [item for item in kernels if item["is_default"] and item["is_present"]]
        assert len(kernel) == 1, kernels
        images = _json_success_data(_run_mvm(runner_vm, "image", "ls", "--json").stdout, list)
        image = [item for item in images if item["type"] == "alpine"
                 and item["version"] == "3.23" and item["is_present"]]
        assert len(image) == 1, images
        initial_vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        assert all(item["name"] != vm_name for item in initial_vms), initial_vms
        created = None
        files: dict[Path, tuple] = {}
        directory_identity = None

        try:
            receipt = _json_success_data(_run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                image[0]["id"],
                "--kernel",
                kernel[0]["id"],
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
                "--json",
                timeout=180,
            ).stdout, list)
            assert len(receipt) == 1, receipt
            created = receipt[0]
            assert created["name"] == vm_name, created
            assert re.fullmatch(r"[0-9a-f]{32}", created["id"]), created
            assert all(item["id"] != created["id"] for item in initial_vms), created
            assert created["created_at"], created
            cache = Path(os.environ.get("MVM_CACHE_DIR", "~/.cache/mvmctl")).expanduser()
            vm_dir = cache / "vms" / created["id"]
            directory = vm_dir.lstat()
            assert stat.S_ISDIR(directory.st_mode) and directory.st_uid == os.getuid(), vm_dir
            directory_identity = (directory.st_dev, directory.st_ino, directory.st_uid, directory.st_mode)
            for field, leaf in (("config_path", "firecracker.json"), ("rootfs_path", "rootfs.img")):
                path = Path(created[field])
                assert path == vm_dir / leaf, created
                identity = _file_identity(path)
                assert identity[2] == os.getuid(), path
                files[path] = identity
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(item for item in vms if item["id"] == created["id"])
            assert vm["status"] == "running"
            assert {vm["binary_id"], vm["jailer_binary_id"]} == {item["id"] for item in pair}
            process_identity = (vm["pid"], vm["process_start_time"])
            assert process_identity[0] > 0 and process_identity[1] > 0, vm
            assert _run_mvm(
                runner_vm, "exec", created["id"], "--", "printf bin-live-pair-ok",
            ).stdout == "bin-live-pair-ok"

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

            pulled = _json_success_data(
                _run_mvm(
                    runner_vm, "bin", "pull", version, "--force", "--json", timeout=300,
                ).stdout,
                list,
            )
            assert {item["id"] for item in pulled} == {item["id"] for item in pair}
            assert {item["type"] for item in pulled} == {"firecracker", "jailer"}
            assert _binary_identity(pulled) == _binary_identity(pair)
            assert _file_identity(archive) == archive_identity

            after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
            assert _binary_identity(after) == _binary_identity(before)
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(item for item in vms if item["id"] == created["id"])
            assert vm["status"] == "running"
            assert (vm["pid"], vm["process_start_time"]) == process_identity
            assert _run_mvm(
                runner_vm, "exec", created["id"], "--", "printf bin-live-pair-ok",
            ).stdout == "bin-live-pair-ok"
        finally:
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if created is not None:
                matches = [item for item in vms if item["id"] == created["id"]]
                assert len(matches) == 1, (created, matches)
                current = matches[0]
                for field in ("name", "created_at", "config_path", "rootfs_path", "binary_id", "jailer_binary_id"):
                    assert current[field] == created[field], (field, created, current)
                directory = Path(current["config_path"]).parent.lstat()
                assert (directory.st_dev, directory.st_ino, directory.st_uid,
                        directory.st_mode) == directory_identity
                assert len(files) == 2, "incomplete file receipt; preserve fixture for explicit recovery"
                for path, identity in files.items():
                    assert _file_identity(path) == identity, path
                _run_mvm(runner_vm, "vm", "rm", created["id"], "--force", timeout=120)
                for path in (*files, Path(current["config_path"]).parent):
                    with pytest.raises(FileNotFoundError):
                        path.lstat()
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert all(item["name"] != vm_name for item in vms)
            if created is not None:
                assert all(item["id"] != created["id"] for item in vms)
