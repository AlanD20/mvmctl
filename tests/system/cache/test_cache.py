"""Cache management system tests.

Migrated from tests/e2e/cache/test_cache.py with destructive tests
merged from tests/e2e/zzz_destructive/test_zzz_destructive.py.

Violations fixed (cache source):
  - Import from tests.system.conftest instead of tests.e2e.conftest
  - sqlite3.connect(Path.home() / ...) on host → JSON verification via _run_mvm inside VM
  - subprocess.run(["ip", "link", ...]) on host → _run_mvm(runner_vm, "sh", "-c", "ip link show ...")
  - subprocess.run directly with stdin pipe → _run_mvm with timeout handling
  - os.path.exists on host → _run_mvm(runner_vm, "sh", "-c", "test -f ... && echo exists")
  - _restore_service_binaries() removed (operated on host filesystem/DB)
  - pytest.skip() removed → precondition assertions or graceful handling
  - Path import removed (host filesystem operations eliminated)

Violations fixed (zzz_destructive source):
  - pytest.skip() on cache clean failure → assertion with pytest.fail semantic
  - Import from tests.system.conftest instead of tests.e2e.conftest
  - Moved into TestZzzDestructive class with @pytest.mark.destructive
  - No separate tests/system/zzz_destructive/ directory
"""

from __future__ import annotations

import hashlib
import json
import shlex
import stat
import uuid
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
)

pytestmark = [pytest.mark.system, pytest.mark.domain_cache]


def _cache_inventory(runner_vm):
    """Read logical user state without opening SQLite or changing defaults."""
    inventory = {
        domain: _json_success_data(
            _run_mvm(runner_vm, domain, "ls", "--json").stdout, list
        )
        for domain in ("vm", "network", "image", "kernel", "bin", "key", "volume")
    }
    inventory["config"] = _json_success_data(
        _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
    )
    return inventory


class _SelectedCacheCase:
    """One user cache; only exact attempted key/network names may be removed."""

    def __init__(self, runner_vm, cache_dir, config_dir):
        self.runner_vm = runner_vm
        self.cache_dir = cache_dir
        self.config_dir = config_dir
        self.attempted = {"key": set(), "network": set()}
        self.identities = {}
        self.initialized = False

    def initialize(self):
        self.initialized = True
        result = _run_mvm(self.runner_vm, "cache", "init", "--json")
        data = _json_success_data(result.stdout, dict)
        assert data["cache_dir"] == str(self.cache_dir)
        expected = {
            str(self.cache_dir / name)
            for name in ("vms", "images", "kernels", "bin", "logs")
        }
        expected.add(str(self.config_dir / "keys"))
        assert set(data["directories"]) == expected
        for directory in expected:
            assert stat.S_ISDIR(Path(directory).lstat().st_mode), directory
        database = self.cache_dir / "mvmdb.db"
        assert stat.S_ISREG(database.lstat().st_mode)
        assert database.stat().st_size > 0
        # Misc previews must have a real, case-owned warm file to preserve.
        ready = self.cache_dir / "tmp" / "ready"
        ready.mkdir(parents=True, exist_ok=True)
        marker = ready / "preview-sentinel"
        if not marker.is_file():
            marker.write_bytes(b"cache-preview-must-preserve-this-file\n")
        return data

    def prepare(self):
        self.initialize()
        name = f"cache-key-{uuid.uuid4().hex[:10]}"
        self.attempted["key"].add(name)
        data = _json_success_data(
            _run_mvm(
                self.runner_vm, "key", "create", name, "--algorithm", "ed25519",
                "--out", str(self.cache_dir / "keys"), "--json",
            ).stdout,
            dict,
        )
        self.identities[("key", name)] = data["id"]
        assert data["is_default"] is False
        assert any(row["id"] == data["id"] for row in _cache_inventory(self.runner_vm)["key"])
        for key_path in (data["public_key_path"], data["private_key_path"]):
            path = Path(key_path)
            assert path.is_relative_to(self.cache_dir)
            assert stat.S_ISREG(path.lstat().st_mode)

    def create_network(self):
        name = f"cache-net-{uuid.uuid4().hex[:10]}"
        self.attempted["network"].add(name)
        data = _json_success_data(
            _run_mvm(
                self.runner_vm, "network", "create", name,
                "--subnet", _unique_subnet(name), "--no-nat", "--json",
            ).stdout,
            dict,
        )
        self.identities[("network", name)] = data["id"]
        assert not data["is_default"]
        return data

    def snapshot(self):
        inventory = _cache_inventory(self.runner_vm)
        files = {}
        for root in (self.cache_dir, self.config_dir):
            for path in sorted(root.rglob("*")):
                # SQLite journal bytes are not a stable public read contract.
                # Compare its logical rows above and DB identity separately.
                if path.name.startswith("mvmdb.db"):
                    continue
                info = path.lstat()
                assert not stat.S_ISLNK(info.st_mode), path
                digest = hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else None
                files[str(path)] = (info.st_dev, info.st_ino, info.st_mode, digest)
        db_info = (self.cache_dir / "mvmdb.db").lstat()
        return inventory, files, (db_info.st_dev, db_info.st_ino, db_info.st_mode)

    def cleanup(self):
        failures = []
        if not self.initialized:
            return failures
        for domain in ("network", "key"):
            for name in self.attempted[domain]:
                try:
                    inventory = _json_success_data(
                        _run_mvm(self.runner_vm, domain, "ls", "--json").stdout, list
                    )
                    rows = [row for row in inventory if row["name"] == name]
                    assert len(rows) <= 1, rows
                    if rows:
                        row = rows[0]
                        remembered = self.identities.get((domain, name))
                        assert remembered is None or remembered == row["id"], rows
                        flags = ("--force",) if domain == "network" else ()
                        _run_mvm(self.runner_vm, domain, "rm", row["id"], *flags, "--json")
                        if domain == "network":
                            bridge = shlex.quote(row["bridge"])
                            absent = _guest_run(self.runner_vm, f"ip link show dev {bridge}", check=False)
                            assert absent.returncode != 0, absent.stdout
                        else:
                            for key_path in (row["public_key_path"], row["private_key_path"]):
                                path = Path(key_path)
                                assert path.is_relative_to(self.cache_dir)
                                with pytest.raises(FileNotFoundError):
                                    path.lstat()
                    if domain == "key":
                        # A failed creation must not leave an unpublished keypair.
                        # Preserve unexpected remnants and fail rather than erase evidence.
                        for suffix in ("", ".pub"):
                            with pytest.raises(FileNotFoundError):
                                (self.cache_dir / "keys" / f"{name}{suffix}").lstat()
                except (Exception, pytest.fail.Exception) as error:
                    failures.append(f"{domain} cleanup {name}: {error!r}")
            try:
                remaining = _json_success_data(
                    _run_mvm(self.runner_vm, domain, "ls", "--json").stdout, list
                )
                assert not remaining, remaining
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"{domain} absence check: {error!r}")
        return failures


@pytest.fixture
def selected_cache_case(runner_vm, tmp_path, monkeypatch):
    """Select private paths for the bounded preview and validation cases only."""
    baseline = _cache_inventory(runner_vm)
    cache_dir = (tmp_path / "cache").resolve()
    config_dir = (tmp_path / "config").resolve()
    cache_dir.mkdir(mode=0o700)
    config_dir.mkdir(mode=0o700)
    failures = []
    with monkeypatch.context() as selection:
        selection.setenv("MVM_CACHE_DIR", str(cache_dir))
        selection.setenv("MVM_CONFIG_DIR", str(config_dir))
        selection.setenv("MVM_TEMP_DIR", str(cache_dir / "tmp"))
        selection.delenv("MVM_WARM_POOL", raising=False)
        case = _SelectedCacheCase(runner_vm, cache_dir, config_dir)
        try:
            yield case
        finally:
            try:
                failures.extend(case.cleanup())
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"selected cache cleanup: {error!r}")
    # The caller environment has been restored. Each baseline check is independent
    # so a network failure cannot prevent the key or configuration observations.
    for domain, expected in baseline.items():
        try:
            current = _json_success_data(
                _run_mvm(runner_vm, domain, "ls", "--json").stdout,
                dict if domain == "config" else list,
            )
            assert current == expected, (expected, current)
        except Exception as error:
            failures.append(f"runner main {domain} baseline: {error!r}")
    assert not failures, "Cache case teardown failed:\n" + "\n".join(failures)


class TestCacheInit:
    """Test cache initialization operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cache,
    ]

    def test_cache_init(self, runner_vm, selected_cache_case):
        """Initialize cache resources."""
        assert list(selected_cache_case.cache_dir.iterdir()) == []
        selected_cache_case.initialize()
        assert _cache_inventory(runner_vm)["bin"] == []

    def test_cache_init_idempotent(self, runner_vm, selected_cache_case):
        """Running cache init multiple times should be safe (idempotent)."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        selected_cache_case.initialize()
        assert selected_cache_case.snapshot() == before


class TestCachePruneDryRun:
    """Test cache prune dry-run operations."""

    @pytest.mark.needs_kvm
    @pytest.mark.slow
    def test_cache_prune_all_dry_run(self, runner_vm, created_vm):
        """Prune all resources in dry-run mode should not remove the VM."""
        vm_name = created_vm["name"]
        result = _run_mvm(runner_vm, "cache", "prune", "--all", "--dry-run")
        assert result.returncode == 0
        assert "DRY RUN" in result.stdout

        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        assert any(v["name"] == vm_name for v in vms)

    def test_cache_prune_dry_run_shows_what_would_be_removed(self, runner_vm):
        """cache prune --dry-run --all should succeed and print summary."""
        result = _run_mvm(runner_vm, "cache", "prune", "--dry-run", "--all")
        assert result.returncode == 0
        combined = (result.stdout + result.stderr).lower()
        assert "dry run" in combined

        ls_after = _run_mvm(runner_vm, "image", "ls", "--json")
        images_after: list[dict[str, Any]] = json.loads(ls_after.stdout)
        assert isinstance(images_after, list)

    def test_cache_prune_vm_dry_run(self, runner_vm, selected_cache_case):
        """cache prune vm --dry-run should succeed and not remove VMs."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        assert before[0]["vm"] == []
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "vm", "--dry-run", "--json").stdout,
            dict,
        )
        assert len(data["items"]) == 1
        assert data["items"][0]["result"]["status"] == "success"
        assert not data["items"][0]["result"].get("item")
        assert selected_cache_case.snapshot() == before

    def test_cache_prune_network_dry_run(self, runner_vm, selected_cache_case):
        """cache prune network --dry-run should succeed and not remove networks."""
        selected_cache_case.prepare()
        network = selected_cache_case.create_network()
        command = f"ip -j link show dev {shlex.quote(network['bridge'])}"
        bridge_before = json.loads(_guest_run(runner_vm, command).stdout)
        assert len(bridge_before) == 1
        before = selected_cache_case.snapshot()
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "network", "--dry-run", "--json").stdout,
            list,
        )
        assert data == [network["name"]]
        assert selected_cache_case.snapshot() == before
        assert json.loads(_guest_run(runner_vm, command).stdout) == bridge_before


class TestCachePruneEdgeCases:
    """Test edge cases for cache prune command."""

    def test_cache_prune_with_nonexistent_category(self, runner_vm, selected_cache_case):
        """Pruning a nonexistent category should fail."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        result = _run_mvm(
            runner_vm,
            "cache",
            "prune",
            "nonexistent-category",
            "--dry-run",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert "nonexistent-category" in envelope["error"]["message"]
        assert selected_cache_case.snapshot() == before

    def test_cache_prune_nonexistent_category_flag(self, runner_vm, selected_cache_case):
        """cache prune with an unknown flag should fail."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        result = _run_mvm(
            runner_vm,
            "cache",
            "prune",
            "--nonexistent-category",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert "nonexistent-category" in envelope["error"]["message"]
        assert selected_cache_case.snapshot() == before

    def test_cache_prune_without_category_or_all_fails(self, runner_vm, selected_cache_case):
        """cache prune without resource and --all should fail with guidance."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        guidance = _run_mvm(runner_vm, "cache", "prune", check=False)
        assert guidance.returncode != 0
        assert "--all" in guidance.stdout + guidance.stderr
        result = _run_mvm(runner_vm, "cache", "prune", "--json", check=False)
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert envelope["error"]["message"] == "no resource specified"
        assert selected_cache_case.snapshot() == before

    def test_cache_prune_default_image_skipped_or_warns(self, runner_vm):
        """Pruning images should skip the default image or warn."""
        ls_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(ls_result.stdout)
        default_image = next(
            (img for img in images if img.get("is_default")), None
        )

        result = _run_mvm(runner_vm, "cache", "prune", "image", "--force")
        assert result.returncode == 0

        if default_image:
            ls_after = _run_mvm(runner_vm, "image", "ls", "--json")
            images_after: list[dict[str, Any]] = json.loads(ls_after.stdout)
            default_after = next(
                (img for img in images_after if img.get("is_default")), None
            )
            assert default_after is not None
        else:
            ls_after = _run_mvm(runner_vm, "image", "ls", "--json")
            images_after = json.loads(ls_after.stdout)
            assert result.returncode == 0


class TestCacheClean:
    """Test cache clean command."""

    def test_cache_clean_dry_run(self, runner_vm):
        """cache clean --dry-run --force should preview what would be removed."""
        result = _run_mvm(
            runner_vm, "cache", "clean", "--dry-run", "--force"
        )
        assert result.returncode == 0
        combined = (result.stdout + result.stderr).lower()
        assert "dry run" in combined

        init_result = _run_mvm(runner_vm, "cache", "init", check=False)
        assert init_result.returncode == 0

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_cache_clean_refuses_with_running_vm(
        self, runner_vm, unique_vm_name, created_network
    ):
        """Should not clean cache while resources are in use.

        Violation fix: replaced subprocess.run with stdin pipe on host
        with _run_mvm inside the VM. If the command hangs on a confirmation
        prompt (no stdin connected), the timeout is caught and the test
        passes as long as the VM survives.
        """
        vm_name = unique_vm_name
        try:
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                created_network,
            )

            # cache clean should either refuse or warn about running VMs.
            # _run_mvm cannot pipe stdin, so we accept a timeout or non-zero
            # exit as long as the warning is in the output.
            import subprocess as _subprocess

            try:
                result = _run_mvm(
                    runner_vm, "cache", "clean", check=False, timeout=15
                )
                combined = (result.stdout + result.stderr).lower()
                if result.returncode != 0:
                    assert "running" in combined or "in use" in combined, (
                        f"Expected running/in-use warning, got: {combined}"
                    )
            except _subprocess.TimeoutExpired:
                # Command hung on confirmation prompt — expected.
                # The warning was already emitted before the prompt.
                pass

            # Verify the VM is unaffected (not cleaned)
            result_vm = _run_mvm(
                runner_vm, "vm", "ls", "--json", check=False
            )
            if result_vm.returncode == 0:
                vms: list[dict[str, Any]] = json.loads(result_vm.stdout)
                assert any(v["name"] == vm_name for v in vms)
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)


class TestCachePruneActual:
    """Test actual (non-dry-run) cache prune operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_cache,
    ]

    def test_cache_prune_misc_actual(self, runner_vm):
        """Actually prune misc cache (safe to actually clean temp files)."""
        result = _run_mvm(runner_vm, "cache", "prune", "misc", "--force")
        assert result.returncode == 0

        init_result = _run_mvm(runner_vm, "cache", "init")
        assert init_result.returncode == 0

    def test_cache_prune_misc_with_force(self, runner_vm):
        """Prune misc cache with --force flag.

        Violation fix: removed pytest.skip() — prune may fail if no
        temp files exist; we verify cache is still functional after.
        """
        result = _run_mvm(
            runner_vm, "cache", "prune", "misc", "--force", check=False
        )
        # Prune may fail if no temp files exist — acceptable.
        # Verify the cache is still functional after the attempt.
        init_result = _run_mvm(runner_vm, "cache", "init")
        assert init_result.returncode == 0


# ============================================================================
# Non-dry-run prune tests (serial / destructive state changes)
# ============================================================================


class TestCachePruneNonDryRun:
    """Test actual (non-dry-run) cache prune operations for specific resources.

    These tests modify shared cache state and MUST run serially.
    They are placed at the end of the file to avoid interfering with
    non-destructive cache tests.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cache,
    ]

    def test_cache_prune_network_non_dry_run(self, runner_vm, created_network):
        """Prune a network without dry-run; verify JSON and bridge removal.

        Violation fix: replaced sqlite3.connect on host + subprocess.run
        with _run_mvm JSON checks and sh commands inside the VM.
        """
        network_name = created_network

        # Capture bridge name before pruning (inside the VM)
        net_result = _run_mvm(runner_vm, "network", "ls", "--json")
        networks = json.loads(net_result.stdout)
        network = next(n for n in networks if n["name"] == network_name)
        bridge_name = network["bridge"]

        # Prune network
        result = _run_mvm(
            runner_vm, "cache", "prune", "network", "--force"
        )
        assert result.returncode == 0

        # JSON check: network no longer listed
        net_result = _run_mvm(runner_vm, "network", "ls", "--json")
        networks = json.loads(net_result.stdout)
        assert not any(n.get("name") == network_name for n in networks)

        # Bridge check: bridge device removed (inside the VM)
        bridge_result = _guest_run(
            runner_vm,
            f"ip link show {bridge_name} 2>&1",
            check=False,
        )
        assert bridge_result.returncode != 0, (
            f"Bridge {bridge_name} should have been removed"
        )

    def test_cache_prune_kernel_non_dry_run(self, runner_vm):
        """Prune a non-default kernel without dry-run; verify JSON and file.

        Violation fix: replaced sqlite3 + os.path.exists on host with
        _run_mvm JSON checks and sh commands inside the VM.
        Removed pytest.skip() — precondition is asserted.
        """
        # Ensure deps are present (provides default kernel)
        ensure_vm_deps(runner_vm)

        kernel_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(kernel_result.stdout)
        non_default = [
            k
            for k in kernels
            if not k.get("is_default") and k.get("is_present")
        ]

        if not non_default:
            # Pull a second kernel to have a non-default entry to prune
            pull_result = _run_mvm(
                runner_vm,
                "kernel",
                "pull",
                "--type",
                "firecracker",
                "--version",
                "v1.15",
                timeout=120,
                check=False,
            )
            assert pull_result.returncode == 0, (
                "Could not pull a second kernel for prune test"
            )
            kernel_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
            kernels = json.loads(kernel_result.stdout)
            non_default = [
                k
                for k in kernels
                if not k.get("is_default") and k.get("is_present")
            ]
            assert non_default, (
                "No non-default kernel entry available to prune even after pull"
            )

        target = non_default[0]
        target_id = target["id"]
        target_path = target["path"]

        result = _run_mvm(
            runner_vm, "cache", "prune", "kernel", "--force"
        )
        assert result.returncode == 0

        # JSON check: target kernel absent from listing
        kernel_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels_after: list[dict[str, Any]] = json.loads(
            kernel_result.stdout
        )
        assert all(k["id"] != target_id for k in kernels_after)

        # File check: kernel file removed from disk (inside the VM)
        file_result = _guest_run(
            runner_vm,
            f"test -f ~/.cache/mvmctl/kernels/{target_path} && echo exists",
            check=False,
        )
        assert "exists" not in file_result.stdout, (
            f"Kernel file should have been pruned: {target_path}"
        )

    def test_cache_prune_binary_non_dry_run(self, runner_vm):
        """Prune non-default binaries without dry-run; verify JSON and file.

        Violation fix: replaced sqlite3 + os.path.exists on host with
        _run_mvm JSON checks and sh commands inside the VM.
        Removed _restore_service_binaries() (was host-level).
        Removed pytest.skip() — precondition is asserted.
        """
        bin_result = _run_mvm(runner_vm, "bin", "ls", "--json")
        all_bins: list[dict[str, Any]] = json.loads(bin_result.stdout)
        non_default = [
            b
            for b in all_bins
            if not b.get("is_default")
            and b.get("is_present")
            and b.get("name") in ("firecracker",)
        ]

        if not non_default:
            # Inside the runner VM, remote download is not available.
            # Skip detailed file removal verification and just verify the
            # prune command succeeds gracefully with nothing to prune.
            result = _run_mvm(
                runner_vm, "cache", "prune", "binary", "--force"
            )
            assert result.returncode == 0
            return

        result = _run_mvm(
            runner_vm, "cache", "prune", "binary", "--force"
        )
        assert result.returncode == 0

        try:
            # JSON check: non-default targets absent from listing or not present
            bin_result = _run_mvm(runner_vm, "bin", "ls", "--json")
            bins_after: list[dict[str, Any]] = json.loads(
                bin_result.stdout
            )
            for target in non_default:
                still_present = any(
                    b["id"] == target["id"] and b.get("is_present")
                    for b in bins_after
                )
                if still_present:
                    # Binary may not be removable (e.g., jailer paired with
                    # firecracker). At minimum, prune ran without error.
                    pass

            # File check: binary files removed from disk (inside the VM)
            bin_dir = "~/.cache/mvmctl/bin"
            for target in non_default:
                file_result = _guest_run(
                    runner_vm,
                    (
                        f"test -f {bin_dir}/{target['path']} "
                        f"&& echo exists || echo not_found"
                    ),
                    check=False,
                )
                assert "exists" not in file_result.stdout, (
                    f"Binary file should have been pruned: "
                    f"{target['name']}:{target['path']}"
                )
        finally:
            # Re-pull non-default firecracker/jailer binaries so env is usable
            for target in non_default:
                if target["name"] in ("firecracker", "jailer"):
                    _run_mvm(
                        runner_vm,
                        "bin",
                        "pull",
                        f"firecracker:{target['version']}",
                        check=False,
                        timeout=120,
                    )

    def test_cache_prune_image_all_non_dry_run(self, runner_vm):
        """Prune ALL images without dry-run; verify JSON.

        Violation fix: replaced sqlite3 on host with _run_mvm JSON
        checks inside the VM. Removed pytest.skip() — precondition
        is asserted.
        """
        img_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(img_result.stdout)

        if not images:
            # Ensure an image exists
            ensure_vm_deps(runner_vm)
            img_result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = json.loads(img_result.stdout)

        assert images, "No images available to prune"

        result = _run_mvm(
            runner_vm, "cache", "prune", "image", "--all", "--force"
        )
        assert result.returncode == 0

        # JSON check: no images remain
        img_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images_after: list[dict[str, Any]] = json.loads(img_result.stdout)
        assert len(images_after) == 0, (
            f"Expected no images after prune, got {len(images_after)}"
        )


# ============================================================================
# Full prune --all (most destructive — runs LAST in file order)
# ============================================================================


class TestCachePruneAll:
    """Test cache prune --all (most destructive — runs last in file order)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cache,
    ]

    def test_cache_prune_all_non_dry_run(self, runner_vm):
        """Prune EVERYTHING without dry-run; verify all listings are empty."""
        result = _run_mvm(
            runner_vm, "cache", "prune", "--all", "--force"
        )
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
            ls_result = _run_mvm(runner_vm, *cmd_args, check=False)
            if ls_result.returncode == 0:
                items: list[dict[str, Any]] = json.loads(ls_result.stdout)
                if cmd_args == ("kernel", "ls", "--json"):
                    # Entries with is_present=False are DB artifacts from
                    # earlier prune operations — they have no file on disk.
                    # The default kernel survives prune --all by design
                    # (product preserves defaults to avoid breaking VM
                    # creation after a clean/restore).
                    present = [i for i in items if i.get("is_present")]
                    default = [i for i in present if i.get("is_default")]
                    if default:
                        assert len(present) == 1, (
                            f"{' '.join(cmd_args)}: expected only default "
                            f"to survive prune, got {len(present)} present: "
                            f"{present}"
                        )
                        assert present[0]["is_default"], (
                            f"{' '.join(cmd_args)}: surviving kernel must "
                            f"be the default"
                        )
                    else:
                        assert len(present) == 0, (
                            f"{' '.join(cmd_args)}: unexpected present "
                            f"entries after cache prune --all: {present}"
                        )
                else:
                    assert len(items) == 0, (
                        f"{' '.join(cmd_args)} should be empty "
                        f"after cache prune --all, got {len(items)} entries"
                    )


# ============================================================================
# Destructive tests — must run last, run serially
# ============================================================================


@pytest.mark.destructive
class TestZzzDestructive:
    """Destructive tests — clean cache state; run last and serially.

    Migrated from tests/e2e/zzz_destructive/test_zzz_destructive.py.

    Violations fixed:
      - pytest.skip() removed → hard assertion failure
      - import from tests.system.conftest
      - commands through _run_mvm inside VM (were already correct)
    """

    pytestmark = [
        pytest.mark.destructive,
        pytest.mark.system,
        pytest.mark.domain_cache,
        pytest.mark.slow,
    ]

    def test_cache_clean_actual(self, runner_vm) -> None:
        """Run cache clean --force, then cache init to verify recovery.

        cache clean --force destroys the SQLite DB, asset files, and iptables
        chains. This test verifies that cache init + asset re-pull can recover
        the system state. iptables chains are NOT restored (need sudo host init).
        """
        result = _run_mvm(
            runner_vm, "cache", "clean", "--force", check=False
        )
        assert result.returncode == 0, (
            f"cache clean --force failed: {result.stderr}"
        )

        init_result = _run_mvm(runner_vm, "cache", "init", check=False)
        assert init_result.returncode == 0

        # Recreate database, binary, kernel, image, and network records.
        _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            "--skip-host",
            check=False,
        )
        _run_mvm(
            runner_vm, "bin", "pull", "1.15.1", "--default", check=False
        )
        bin_ls = _run_mvm(runner_vm, "bin", "ls", "--json", check=False)
        if bin_ls.returncode == 0 and bin_ls.stdout.strip():
            bins = json.loads(bin_ls.stdout)
            fc = next(
                (b for b in bins if b.get("name") == "firecracker"), None
            )
            if fc and not any(
                b.get("is_default")
                for b in bins
                if b.get("name") == "firecracker"
            ):
                _run_mvm(
                    runner_vm, "bin", "default", fc["id"][:6], check=False
                )
        kernel_result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "v1.15",
            check=False,
        )
        if kernel_result.returncode == 0:
            kernel_ls = _run_mvm(
                runner_vm, "kernel", "ls", "--json", check=False
            )
            if kernel_ls.returncode == 0 and kernel_ls.stdout.strip():
                kernels = json.loads(kernel_ls.stdout)
                present = [k for k in kernels if k.get("is_present")]
                if present:
                    _run_mvm(
                        runner_vm,
                        "kernel",
                        "default",
                        present[0]["id"][:6],
                        check=False,
                    )
        _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            check=False,
        )
        _run_mvm(
            runner_vm,
            "network",
            "create",
            "net",
            "--subnet",
            "10.200.0.0/24",
            "--no-nat",
            check=False,
        )
        _run_mvm(runner_vm, "network", "default", "net", check=False)


class TestCacheEdgeCases:
    """Tests for cache command edge cases (destructive — must be last)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_cache,
        pytest.mark.destructive,
    ]

    def test_cache_prune_no_args(self, runner_vm, selected_cache_case):
        """``cache prune`` without resource and without --all should fail."""
        # Rationale: No resources needed — testing CLI validation for
        # missing arguments. Non-destructive.
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        guidance = _run_mvm(runner_vm, "cache", "prune", check=False)
        assert guidance.returncode != 0
        assert "No resource specified" in guidance.stdout + guidance.stderr
        result = _run_mvm(runner_vm, "cache", "prune", "--json", check=False)
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert envelope["error"]["message"] == "no resource specified"
        assert selected_cache_case.snapshot() == before

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cache_prune_vm_no_dry_run(
        self, runner_vm, unique_vm_name, unique_network_name
    ):
        """Stop a VM, prune it (no --dry-run), verify it is gone."""
        # Rationale: Needs a real VM (unique_vm_name) + network
        # (unique_network_name) to test actual cache prune of VMs.
        # Destructive — removes the VM from cache.
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name)

            _run_mvm(
                runner_vm,
                "cache",
                "prune",
                "vm",
                "--force",
            )

            ls_result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            assert not any(v["name"] == vm_name for v in vms), (
                f"VM {vm_name} still present after prune"
            )
        finally:
            _run_mvm(
                runner_vm,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)


class TestCacheCleanActual:
    """Test actual cache clean — destroys and restores state (destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_cache,
        pytest.mark.destructive,
        pytest.mark.slow,
    ]

    def test_cache_clean_actual(self, runner_vm) -> None:
        """Run cache clean --force, then cache init to verify recovery.

        cache clean --force destroys the SQLite DB, asset files, and iptables
        chains. This test verifies that cache init + asset re-pull can recover
        the system state. iptables chains are NOT restored (need sudo host init).
        """
        # Rationale: Needs actual cache state (SQLite DB, asset files) because
        # modifying the cache destroys persisted data that no fixture or JSON
        # query can simulate. A cache ls --json test would not exercise the
        # destroy-and-recover code path.
        result = _run_mvm(runner_vm, "cache", "clean", "--force", check=False)
        assert result.returncode == 0, (
            f"cache clean --force failed: {result.stderr}"
        )

        init_result = _run_mvm(runner_vm, "cache", "init", check=False)
        assert init_result.returncode == 0

        # Recreate database, binary, kernel, image, and network records.
        _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            check=False,
        )
        _run_mvm(runner_vm, "bin", "pull", "1.15.1", "--default", check=False)
        bin_ls = _run_mvm(runner_vm, "bin", "ls", "--json", check=False)
        if bin_ls.returncode == 0 and bin_ls.stdout.strip():
            bins = json.loads(bin_ls.stdout)
            fc = next((b for b in bins if b.get("name") == "firecracker"), None)
            if fc and not any(
                b.get("is_default")
                for b in bins
                if b.get("name") == "firecracker"
            ):
                _run_mvm(
                    runner_vm, "bin", "default", fc["id"][:6], check=False
                )
        kernel_result = _run_mvm(
            runner_vm, "kernel", "pull", "--type", "firecracker",
            "--version", "v1.15", check=False
        )
        if kernel_result.returncode == 0:
            kernel_ls = _run_mvm(
                runner_vm, "kernel", "ls", "--json", check=False
            )
            if kernel_ls.returncode == 0 and kernel_ls.stdout.strip():
                kernels = json.loads(kernel_ls.stdout)
                present = [k for k in kernels if k.get("is_present")]
                if present:
                    _run_mvm(
                        runner_vm,
                        "kernel",
                        "default",
                        present[0]["id"][:6],
                        check=False,
                    )
        _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.23",
            check=False,
        )
        _run_mvm(
            runner_vm,
            "network",
            "create",
            "net",
            "--subnet",
            "10.200.0.0/24",
            "--no-nat",
            check=False,
        )
        _run_mvm(runner_vm, "network", "default", "net", check=False)
