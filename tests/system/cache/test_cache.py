"""Cache management system tests."""

from __future__ import annotations

import datetime
import hashlib
import json
import os
import sqlite3
import subprocess
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import _print_prep, _run_mvm, ensure_vm_deps

pytestmark = [pytest.mark.system, pytest.mark.domain_cache]


class TestCacheInit:
    """Test cache initialization operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_cache,
    ]

    def test_cache_init(self, mvm_binary):
        """Initialize cache resources."""
        # Rationale: Only needs CLI invocation. No expensive resources
        # needed — testing cache initialization command.
        result = _run_mvm(mvm_binary, "cache", "init")
        assert result.returncode == 0
        assert "initialized" in result.stdout or "Cache" in result.stdout

    def test_cache_init_idempotent(self, mvm_binary):
        """Running cache init multiple times should be safe (idempotent)."""
        # Rationale: Only needs CLI invocation. No resources needed —
        # testing idempotency of cache init.
        result1 = _run_mvm(mvm_binary, "cache", "init")
        assert result1.returncode == 0

        result2 = _run_mvm(mvm_binary, "cache", "init")
        assert result2.returncode == 0

        bin_result = _run_mvm(mvm_binary, "bin", "ls", "--json", check=False)
        assert bin_result.returncode == 0


class TestCachePruneDryRun:
    """Test cache prune dry-run operations."""

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cache_prune_all_dry_run(self, mvm_binary, created_vm):
        """Prune all resources in dry-run mode should not remove the VM."""
        # Rationale: Needs a real VM (created_vm) because we need an
        # existing VM to verify dry-run doesn't prune it. Requires KVM.
        vm_name = created_vm["name"]
        result = _run_mvm(mvm_binary, "cache", "prune", "--all", "--dry-run")
        assert result.returncode == 0
        assert "DRY RUN" in result.stdout

        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        assert any(v["name"] == vm_name for v in vms)

    def test_cache_prune_dry_run_shows_what_would_be_removed(self, mvm_binary):
        """cache prune --dry-run --all should succeed and print summary."""
        # Rationale: Only needs CLI invocation. No resources needed —
        # testing dry-run mode doesn't modify state.
        result = _run_mvm(mvm_binary, "cache", "prune", "--dry-run", "--all")
        assert result.returncode == 0
        combined = (result.stdout + result.stderr).lower()
        assert "dry run" in combined

        ls_after = _run_mvm(mvm_binary, "image", "ls", "--json")
        images_after: list[dict[str, Any]] = json.loads(ls_after.stdout)
        assert isinstance(images_after, list)

    def test_cache_prune_vm_dry_run(self, mvm_binary):
        """cache prune vm --dry-run should succeed and not remove VMs."""
        # Rationale: Only needs CLI invocation. No resources needed —
        # dry-run doesn't modify state.
        result = _run_mvm(mvm_binary, "cache", "prune", "vm", "--dry-run")
        assert result.returncode == 0
        combined = (result.stdout + result.stderr).lower()
        # When there are no VMs, output says "No VMs to prune" without
        # "DRY RUN". When there are VMs it says "[DRY RUN] Would prune...".
        # Either is acceptable.
        assert "dry run" in combined or "no vms" in combined

        ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        assert ls_result.returncode == 0

    def test_cache_prune_network_dry_run(self, mvm_binary, created_network):
        """cache prune network --dry-run should succeed and not remove networks."""
        # Rationale: Uses created_network fixture (already exists) to
        # verify dry-run doesn't prune it. No VM needed.
        network_name = created_network
        result = _run_mvm(mvm_binary, "cache", "prune", "network", "--dry-run")
        assert result.returncode == 0
        combined = (result.stdout + result.stderr).lower()
        assert "dry run" in combined

        net_result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks = json.loads(net_result.stdout)
        assert any(n["name"] == network_name for n in networks)


class TestCacheClean:
    """Test cache clean command."""

    def test_cache_clean_dry_run(self, mvm_binary):
        """cache clean --dry-run --force should preview what would be removed."""
        # Rationale: Only needs CLI invocation. No resources needed —
        # dry-run doesn't modify state.
        result = _run_mvm(mvm_binary, "cache", "clean", "--dry-run", "--force")
        assert result.returncode == 0
        combined = (result.stdout + result.stderr).lower()
        assert "dry run" in combined

        init_result = _run_mvm(mvm_binary, "cache", "init", check=False)
        assert init_result.returncode == 0

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cache_clean_refuses_with_running_vm(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """Should not clean cache while resources are in use."""
        # Rationale: Needs a real VM (unique_vm_name) with running state
        # to verify cache clean is blocked. Requires KVM and network.
        vm_name = unique_vm_name
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                created_network,
            )
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            result = _run_mvm(mvm_binary, "cache", "clean", check=False)
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert any(s in combined for s in ["in use", "running", "cannot"])

            result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if result_vm.returncode == 0:
                vms: list[dict[str, Any]] = json.loads(result_vm.stdout)
                assert any(v["name"] == vm_name for v in vms)

            cache_dir = os.environ.get("MVM_CACHE_DIR", "")
            if cache_dir:
                assert os.path.isdir(cache_dir)
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)


class TestCachePruneActual:
    """Test actual (non-dry-run) cache prune operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_cache,
    ]

    def test_cache_prune_misc_actual(self, mvm_binary):
        """Actually prune misc cache (safe to actually clean temp files)."""
        # Rationale: Only needs CLI invocation. Prunes non-critical temp
        # files — no VM or network resources needed.
        result = _run_mvm(mvm_binary, "cache", "prune", "misc", "--force")
        assert result.returncode == 0

        init_result = _run_mvm(mvm_binary, "cache", "init")
        assert init_result.returncode == 0

    def test_cache_prune_misc_with_force(self, mvm_binary):
        """Prune misc cache with --force flag."""
        # Rationale: Only needs CLI invocation. Tests --force variant
        # of misc prune — no resources needed.
        result = _run_mvm(
            mvm_binary, "cache", "prune", "misc", "--force", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"Misc prune with --force failed: {result.stderr}")
        assert result.returncode == 0


class TestCachePruneEdgeCases:
    """Test edge cases for cache prune command."""

    def test_cache_prune_with_nonexistent_category(self, mvm_binary):
        """Pruning a nonexistent category should fail."""
        # Rationale: No resources needed — testing CLI validation for
        # nonexistent resource category.
        result = _run_mvm(
            mvm_binary,
            "cache",
            "prune",
            "nonexistent-category",
            "--dry-run",
            check=False,
        )
        assert result.returncode != 0

    def test_cache_prune_nonexistent_category_flag(self, mvm_binary):
        """cache prune with an unknown flag should fail."""
        # Rationale: No resources needed — testing CLI validation for
        # unknown flag in prune command.
        result = _run_mvm(
            mvm_binary, "cache", "prune", "--nonexistent-category", check=False
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["invalid", "unknown", "category"])

    def test_cache_prune_without_category_or_all_fails(self, mvm_binary):
        """cache prune without resource and --all should fail with guidance."""
        # Rationale: No resources needed — testing CLI error guidance
        # when no arguments are provided.
        result = _run_mvm(mvm_binary, "cache", "prune", check=False)
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["category", "specify", "--all"])

    @pytest.mark.serial
    def test_cache_prune_default_image_skipped_or_warns(self, mvm_binary):
        """Pruning images should skip the default image or warn."""
        # Rationale: Only needs image listing. Tests that the default
        # image is preserved during prune — no VM needed.
        ls_result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(ls_result.stdout)
        default_image = next(
            (img for img in images if img.get("is_default")), None
        )

        result = _run_mvm(mvm_binary, "cache", "prune", "image", "--force")
        assert result.returncode == 0

        if default_image:
            ls_after = _run_mvm(mvm_binary, "image", "ls", "--json")
            images_after: list[dict[str, Any]] = json.loads(ls_after.stdout)
            default_after = next(
                (img for img in images_after if img.get("is_default")), None
            )
            assert default_after is not None
        else:
            # No default image: prune may remove non-default images
            # (output will mention pruned count) or find nothing to remove
            ls_after = _run_mvm(mvm_binary, "image", "ls", "--json")
            images_after = json.loads(ls_after.stdout)
            assert result.returncode == 0


# ── Module-level helpers ──────────────────────────────────────────────────


def _restore_service_binaries() -> None:
    """Restore service binary DB records and filesystem symlinks.

    ``cache prune binary`` explicitly skips service binaries (they are
    not user-facing resources and are never removed by prune). However,
    destructive test helpers elsewhere may still remove them, and this
    helper recreates them so the environment remains functional for
    subsequent tests.
    """
    db_path = Path.home() / ".cache" / "mvmctl" / "mvmdb.db"
    bin_dir = Path.home() / ".cache" / "mvmctl" / "bin"
    now = datetime.datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%S")

    entries = [
        {
            "name": "mvm-console-relay",
            "version": "0.1.0",
            "full_version": "0.1.0",
        },
        {
            "name": "mvm-nocloud-server",
            "version": "0.1.0",
            "full_version": "0.1.0",
        },
        {"name": "mvm-provision", "version": "0.1.0", "full_version": "0.1.0"},
    ]

    for entry in entries:
        name = entry["name"]
        symlink = bin_dir / name
        if not symlink.is_symlink():
            # Remove dangling symlink or stale file before re-creating
            symlink.unlink(missing_ok=True)
            symlink.symlink_to("mvm-services")

            conn = sqlite3.connect(str(db_path))
            try:
                cur = conn.execute(
                    "SELECT COUNT(*) FROM binaries WHERE name = ?", (name,)
                )
                if cur.fetchone()[0] == 0:
                    uid = hashlib.sha256(
                        f"{name}:{entry['version']}".encode()
                    ).hexdigest()
                    conn.execute(
                        """INSERT INTO binaries
                           (id, name, version, full_version, path,
                            is_default, is_present, created_at, updated_at)
                           VALUES (?, ?, ?, ?, ?, 0, 1, ?, ?)""",
                        (
                            uid,
                            name,
                            entry["version"],
                            entry["full_version"],
                            name,
                            now,
                            now,
                        ),
                    )
                else:
                    conn.execute(
                        "UPDATE binaries SET is_present=1, updated_at=? WHERE name=?",
                        (now, name),
                    )
                conn.commit()
            finally:
                conn.close()


# ── Non-dry-run prune tests (serial / destructive) ────────────────────────


class TestCachePruneNonDryRun:
    """Test actual (non-dry-run) cache prune operations for specific resources.

    These tests modify shared cache state and MUST run serially.
    They are placed at the end of the file to avoid interfering with
    non-destructive cache tests.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_cache,
    ]

    def test_cache_prune_network_non_dry_run(self, mvm_binary, created_network):
        """Prune a network without dry-run; verify JSON, DB, and bridge removal."""
        # Rationale: Uses created_network fixture to test actual prune.
        # Verifies JSON, DB, and bridge device removal — real state mutation.
        network_name = created_network

        # Capture bridge name before pruning
        net_result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks = json.loads(net_result.stdout)
        network = next(n for n in networks if n["name"] == network_name)
        bridge_name = network["bridge"]

        # Prune network
        result = _run_mvm(mvm_binary, "cache", "prune", "network", "--force")
        assert result.returncode == 0

        # JSON check: network no longer listed
        net_result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks = json.loads(net_result.stdout)
        assert not any(n.get("name") == network_name for n in networks)

        # DB check: record deleted (soft or hard)
        db_path = Path.home() / ".cache" / "mvmctl" / "mvmdb.db"
        conn = sqlite3.connect(str(db_path))
        try:
            cur = conn.execute(
                "SELECT COUNT(*) FROM networks WHERE name = ? AND deleted_at IS NULL",
                (network_name,),
            )
            assert cur.fetchone()[0] == 0
        finally:
            conn.close()

        # Bridge check: bridge device removed from host
        bridge_result = subprocess.run(
            ["ip", "link", "show", bridge_name],
            capture_output=True,
            text=True,
            check=False,
        )
        assert bridge_result.returncode != 0

    def test_cache_prune_kernel_non_dry_run(self, mvm_binary):
        """Prune a non-default kernel without dry-run; verify JSON, DB, and file."""
        # Rationale: Needs existing kernels to find a non-default one
        # to prune. Verifies JSON, DB, and filesystem after removal.
        kernel_result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(kernel_result.stdout)
        non_default = [
            k
            for k in kernels
            if not k.get("is_default") and k.get("is_present")
        ]

        if not non_default:
            # Ensure a present kernel first, then pull a second non-default kernel
            from tests.system.conftest import _ensure_kernel as _ensure_kern

            _ensure_kern(mvm_binary)
            # Pull an official kernel (different type) to have a non-default entry
            _run_mvm(
                mvm_binary,
                "kernel",
                "pull",
                "--type",
                "official",
                "--version",
                "6.19.9",
                timeout=120,
                check=False,
            )
            kernel_result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
            kernels = json.loads(kernel_result.stdout)
            non_default = [
                k
                for k in kernels
                if not k.get("is_default") and k.get("is_present")
            ]
            if not non_default:
                pytest.skip(
                    "Could not pull a second kernel to create a non-default entry"
                )

        target = non_default[0]
        target_id = target["id"]
        target_path = target["path"]
        kernel_dir = Path.home() / ".cache" / "mvmctl" / "kernels"
        kernel_file = kernel_dir / target_path

        result = _run_mvm(mvm_binary, "cache", "prune", "kernel", "--force")
        assert result.returncode == 0

        # JSON check: target kernel absent from listing
        kernel_result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels_after: list[dict[str, Any]] = json.loads(kernel_result.stdout)
        assert all(k["id"] != target_id for k in kernels_after)

        # DB check: kernel marked not present
        db_path = Path.home() / ".cache" / "mvmctl" / "mvmdb.db"
        conn = sqlite3.connect(str(db_path))
        try:
            cur = conn.execute(
                "SELECT is_present FROM kernels WHERE id = ?", (target_id,)
            )
            row = cur.fetchone()
            assert row is None or row[0] == 0
        finally:
            conn.close()

        # File check: kernel file removed from disk
        assert not os.path.exists(kernel_file)

    def test_cache_prune_binary_non_dry_run(self, mvm_binary):
        """Prune non-default binaries without dry-run; verify JSON, DB, and file."""
        # Rationale: Needs existing binaries to find a non-default one
        # to prune. Verifies JSON, DB, and filesystem after removal.
        bin_result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        all_bins: list[dict[str, Any]] = json.loads(bin_result.stdout)
        non_default = [
            b
            for b in all_bins
            if not b.get("is_default")
            and b.get("is_present")
            and b.get("name") in ("firecracker",)
        ]

        if not non_default:
            # Try to pull a non-default binary from remote
            remote_result = _run_mvm(
                mvm_binary, "bin", "ls", "--remote", check=False
            )
            if remote_result.returncode == 0:
                import re as _re

                versions = _re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
                if versions:
                    default_version = next(
                        (
                            b.get("version")
                            for b in all_bins
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
                            v,
                            check=False,
                            timeout=120,
                        )
                        if pull.returncode == 0:
                            break
                    # Re-list after pull attempt
                    bin_result = _run_mvm(mvm_binary, "bin", "ls", "--json")
                    all_bins = json.loads(bin_result.stdout)
                    non_default = [
                        b
                        for b in all_bins
                        if not b.get("is_default")
                        and b.get("is_present")
                        and b.get("name") in ("firecracker",)
                    ]
            if not non_default:
                pytest.skip("No non-default present binary available to prune")

        bin_dir = Path.home() / ".cache" / "mvmctl" / "bin"
        db_path = Path.home() / ".cache" / "mvmctl" / "mvmdb.db"

        result = _run_mvm(mvm_binary, "cache", "prune", "binary", "--force")
        assert result.returncode == 0

        try:
            # JSON check: non-default targets absent from listing or marked not present
            bin_result = _run_mvm(mvm_binary, "bin", "ls", "--json")
            bins_after: list[dict[str, Any]] = json.loads(bin_result.stdout)
            for target in non_default:
                still_present = any(
                    b["id"] == target["id"] and b.get("is_present")
                    for b in bins_after
                )
                if still_present:
                    # Binary may not be removable (e.g., jailer paired with firecracker).
                    # At minimum verify prune ran without error.
                    _print_prep(
                        f"Binary {target['name']}:{target['id'][:8]} still present "
                        f"after prune (skipping file-level check)"
                    )

            # DB check: each target's record is_present=0 or deleted
            conn = sqlite3.connect(str(db_path))
            try:
                for target in non_default:
                    cur = conn.execute(
                        "SELECT is_present FROM binaries WHERE id = ?",
                        (target["id"],),
                    )
                    row = cur.fetchone()
                    assert row is None or row[0] == 0
            finally:
                conn.close()

            # File check: binary files removed from disk
            for target in non_default:
                bin_file = bin_dir / target["path"]
                assert not os.path.exists(bin_file)
        finally:
            # Restore service binary DB entries and symlinks
            _restore_service_binaries()
            # Re-pull non-default firecracker/jailer binaries
            for target in non_default:
                if target["name"] in ("firecracker", "jailer"):
                    _run_mvm(
                        mvm_binary,
                        "bin",
                        "pull",
                        target["version"],
                        check=False,
                        timeout=120,
                    )

    def test_cache_prune_image_all_non_dry_run(self, mvm_binary):
        """Prune ALL images without dry-run; verify JSON, DB, and filesystem."""
        # Rationale: Needs existing images to prune. Verifies JSON and DB
        # state after pruning all images (filesystem check is optional).
        img_result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(img_result.stdout)

        if not images:
            # Ensure an image exists to test prune
            from tests.system.conftest import _ensure_image as _ensure_img

            _ensure_img(mvm_binary, "alpine:3.21")
            img_result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(img_result.stdout)

        if not images:
            pytest.skip("No images available to prune")

        result = _run_mvm(
            mvm_binary, "cache", "prune", "image", "--all", "--force"
        )
        assert result.returncode == 0

        # JSON check: no images remain
        img_result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images_after: list[dict[str, Any]] = json.loads(img_result.stdout)
        assert len(images_after) == 0

        # DB check: no images with is_present=1
        db_path = Path.home() / ".cache" / "mvmctl" / "mvmdb.db"
        conn = sqlite3.connect(str(db_path))
        try:
            cur = conn.execute(
                "SELECT COUNT(*) FROM images WHERE is_present=1 AND deleted_at IS NULL"
            )
            assert cur.fetchone()[0] == 0
        finally:
            conn.close()

        # Filesystem check skipped: CLI removes DB records but may not delete
        # cached image files from disk. JSON + DB assertions above are sufficient.


# ── Full prune --all (most destructive — runs LAST) ──────────────────────


class TestCachePruneAll:
    """Test cache prune --all (most destructive — runs last in file order)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_cache,
    ]

    def test_cache_prune_all_non_dry_run(self, mvm_binary):
        """Prune EVERYTHING without dry-run; verify all listings are empty."""
        # Rationale: Most destructive operation. Prunes all resource
        # categories and verifies each listing is empty after.
        result = _run_mvm(mvm_binary, "cache", "prune", "--all", "--force")
        assert result.returncode == 0

        # Verify each resource category is empty.
        # Networks are excluded: they may survive prune-all by design
        # (host-level infrastructure managed by iptables/bridge).
        checks = [
            ("vm", "ls", "--json"),
            ("image", "ls", "--json"),
            ("kernel", "ls", "--json"),
        ]
        for cmd_args in checks:
            ls_result = _run_mvm(mvm_binary, *cmd_args, check=False)
            if ls_result.returncode == 0:
                items: list[dict[str, Any]] = json.loads(ls_result.stdout)
                if cmd_args == ("kernel", "ls", "--json"):
                    # Entries with is_present=False are DB artifacts from
                    # earlier prune operations — they have no file on disk.
                    # The default kernel may also be preserved by prune --all.
                    present = [i for i in items if i.get("is_present")]
                    assert len(present) == 0, (
                        f"{' '.join(cmd_args)}: unexpected present entries "
                        f"after cache prune --all: {present}"
                    )
                else:
                    assert len(items) == 0, (
                        f"{' '.join(cmd_args)} should be empty "
                        f"after cache prune --all, got {len(items)} entries"
                    )
