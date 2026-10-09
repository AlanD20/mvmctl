"""Kernel management system tests."""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
import uuid
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import (
    _ensure_kernel,
    _guest_run,
    _json_success_data,
    _run_mvm,
    ensure_vm_deps,
)

pytestmark = [pytest.mark.system, pytest.mark.domain_kernel]


class _KernelPullOwner:
    """Admit one successful pull receipt in an otherwise empty private cache."""

    domains = ("vm", "network", "volume", "snapshot", "key", "kernel", "image", "bin")

    def __init__(self, runner_vm, cache_dir, config_dir):
        self.runner_vm = runner_vm
        self.cache_dir = cache_dir
        self.config_dir = config_dir
        self.cache_identity = self.directory_identity(cache_dir)
        self.config_identity = self.directory_identity(config_dir)
        self.receipt = None

    @staticmethod
    def directory_identity(path):
        info = path.lstat()
        assert stat.S_ISDIR(info.st_mode) and info.st_uid == os.getuid(), path
        assert stat.S_IMODE(info.st_mode) == 0o700, path
        return info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode

    @staticmethod
    def file_receipt(path):
        assert path.is_absolute(), path
        with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as source:
            before = os.fstat(source.fileno())
            assert stat.S_ISREG(before.st_mode) and before.st_nlink == 1, path
            assert before.st_uid == os.getuid() and before.st_size > 0, path
            digest = hashlib.file_digest(source, "sha256").hexdigest()
            after = os.fstat(source.fileno())
        fields = ("st_dev", "st_ino", "st_uid", "st_gid", "st_mode", "st_nlink",
                  "st_size", "st_mtime_ns", "st_ctime_ns")
        identity = tuple(getattr(before, field) for field in fields)
        assert tuple(getattr(after, field) for field in fields) == identity, path
        return identity, digest

    def inventory(self):
        return {domain: _json_success_data(
            _run_mvm(self.runner_vm, domain, "ls", "--json").stdout, list,
        ) for domain in self.domains}

    @staticmethod
    def assert_inspect_receipt(inspected, item):
        kernel = {key: item[key] for key in (
            "id", "name", "base_name", "version", "arch", "type", "is_default", "is_present",
        )}
        if item.get("features"):
            kernel["features"] = item["features"]
        assert inspected == {
            "kernel": kernel,
            "storage": {"path": item["path"]},
            "timestamps": {key: item[key] for key in ("created_at", "updated_at")},
        }, inspected

    def initialize(self):
        initialized = _json_success_data(
            _run_mvm(self.runner_vm, "cache", "init", "--json").stdout, dict,
        )
        assert initialized["cache_dir"] == str(self.cache_dir), initialized
        self.baseline = self.inventory()
        assert all(not items for items in self.baseline.values()), self.baseline
        self.config = _json_success_data(
            _run_mvm(self.runner_vm, "config", "ls", "--json").stdout, dict,
        )

    def remember_pull(self, result):
        """A failed/ambiguous publication never grants authority by inventory difference."""
        assert self.receipt is None, "only one acquisition is owned per private case"
        for label, output in (("stdout", result.stdout), ("stderr", result.stderr)):
            (self.cache_dir.parent / f"pull.{label}").write_text(output, encoding="utf-8")
        if result.returncode != 0:
            return  # Preserve the original body's failure assertion and uncertain private artifacts.
        item = _json_success_data(result.stdout, dict)
        assert re.fullmatch(r"[0-9a-f]{64}", item["id"]), item
        assert item["type"] == "firecracker" and item["is_present"], item
        assert item["version"] and item["arch"] and item["is_default"] is False, item
        assert item["id"] not in {row["id"] for row in self.baseline["kernel"]}, item
        path = self.cache_dir / "kernels" / item["id"]
        assert Path(item["path"]) == path and not path.parent.is_symlink(), item
        backing, digest = self.file_receipt(path)
        assert hashlib.sha256(f"{digest}:{item['version']}:{item['arch']}".encode()).hexdigest() == item["id"]
        inspected = _json_success_data(
            _run_mvm(self.runner_vm, "kernel", "inspect", item["id"], "--json").stdout, dict,
        )
        self.assert_inspect_receipt(inspected, item)
        assert self.inventory()["kernel"] == [item], item
        assert self.file_receipt(path) == (backing, digest), path
        self.receipt = item, path, backing, digest
        print(f"owned kernel pull receipt={item}, backing={backing}, sha256={digest}", flush=True)

    def cleanup(self):
        assert self.receipt is not None, (
            f"No successful exact pull receipt; retain private cache and native output at {self.cache_dir.parent}"
        )
        item, path, backing, digest = self.receipt
        assert self.directory_identity(self.cache_dir) == self.cache_identity
        assert self.directory_identity(self.config_dir) == self.config_identity
        assert self.inventory()["kernel"] == [item], "unowned/changed kernel publication; retain cache"
        inspected = _json_success_data(
            _run_mvm(self.runner_vm, "kernel", "inspect", item["id"], "--json").stdout, dict,
        )
        self.assert_inspect_receipt(inspected, item)
        assert self.file_receipt(path) == (backing, digest), "owned backing changed; retain cache"
        removed = _json_success_data(
            _run_mvm(self.runner_vm, "kernel", "rm", item["id"], "--force", "--json").stdout, dict,
        )
        assert len(removed["items"]) == 1, removed
        assert removed["items"][0]["result"]["code"] == "kernel.removed", removed
        assert removed["items"][0]["error"] is None, removed
        with pytest.raises(FileNotFoundError):
            path.lstat()
        assert self.inventory() == self.baseline, "private originals/defaults changed; retain cache"
        assert not list((self.cache_dir / "kernels").iterdir()), "unpublished backing retained"
        assert _json_success_data(
            _run_mvm(self.runner_vm, "config", "ls", "--json").stdout, dict,
        ) == self.config
        assert self.directory_identity(self.cache_dir) == self.cache_identity
        cleaned = _json_success_data(
            _run_mvm(self.runner_vm, "cache", "clean", "--force", "--json", timeout=210).stdout, dict,
        )
        assert cleaned["cache_dir"] == str(self.cache_dir) and cleaned["cache_dir_removed"] is True, cleaned
        assert not cleaned["prune_result"]["failed_ids"] and not cleaned["prune_result"]["had_running_vms"], cleaned
        with pytest.raises(FileNotFoundError):
            self.cache_dir.lstat()


@pytest.fixture
def kernel_pull_owner(runner_vm, tmp_path, monkeypatch):
    """Isolate each existing acquisition case; preserve shared originals on every exit."""
    baseline = {domain: _json_success_data(
        _run_mvm(runner_vm, domain, "ls", "--json").stdout, list,
    ) for domain in _KernelPullOwner.domains}
    config = _json_success_data(_run_mvm(runner_vm, "config", "ls", "--json").stdout, dict)
    sources = {Path(item["path"]): _KernelPullOwner.file_receipt(Path(item["path"]))
               for item in baseline["kernel"] if item["is_present"]}
    tmp_path.chmod(0o700)
    cache_dir, config_dir = tmp_path / "pull-cache", tmp_path / "pull-config"
    cache_dir.mkdir(mode=0o700)
    config_dir.mkdir(mode=0o700)
    failures = []
    try:
        with monkeypatch.context() as selection:
            selection.setenv("MVM_CACHE_DIR", str(cache_dir))
            selection.setenv("MVM_CONFIG_DIR", str(config_dir))
            selection.setenv("MVM_TEMP_DIR", str(cache_dir / "tmp"))
            selection.setenv("TMPDIR", str(cache_dir / "tmp"))
            selection.delenv("MVM_WARM_POOL", raising=False)
            owner = _KernelPullOwner(runner_vm, cache_dir, config_dir)
            try:
                owner.initialize()
                yield owner
            finally:
                try:
                    owner.cleanup()
                except Exception as error:
                    failures.append(f"private pull cleanup retained {cache_dir}: {error!r}")
    finally:
        # Run every shared-state check even when private acquisition/cleanup failed.
        for domain, expected in (*baseline.items(), ("config", config)):
            try:
                current = _json_success_data(
                    _run_mvm(runner_vm, domain, "ls", "--json").stdout,
                    dict if domain == "config" else list,
                )
                assert current == expected, (domain, expected, current)
            except Exception as error:
                failures.append(f"shared {domain} baseline changed: {error!r}")
        for path, expected in sources.items():
            try:
                assert _KernelPullOwner.file_receipt(path) == expected, path
            except Exception as error:
                failures.append(f"shared source kernel changed: {path}: {error!r}")
        assert not failures, failures


def _import_test_kernel(runner_vm: str) -> str:
    """Register a uniquely versioned copy of a present kernel for removal tests."""
    _ensure_kernel(runner_vm)
    before = _json_success_data(
        _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
    )
    source = next(kernel for kernel in before if kernel["is_present"])
    inspected = _json_success_data(
        _run_mvm(runner_vm, "kernel", "inspect", source["id"], "--json").stdout,
        dict,
    )
    suffix = uuid.uuid4().hex[:12]
    imported = _json_success_data(
        _run_mvm(
            runner_vm,
            "kernel", "import", f"sys-test-remove-{suffix}", inspected["storage"]["path"],
            "--version", f"qa-{suffix}", "--json",
        ).stdout,
        dict,
    )
    imported_id = imported["id"]
    assert imported_id not in {kernel["id"] for kernel in before}
    return imported_id


def _remove_test_kernel_if_present(runner_vm: str, kernel_id: str) -> None:
    """Clean up only the exact test-owned kernel, including after assertion failure."""
    kernels = _json_success_data(
        _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
    )
    if any(kernel["id"] == kernel_id for kernel in kernels):
        _run_mvm(runner_vm, "kernel", "rm", kernel_id, "--force")
    remaining = _json_success_data(
        _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
    )
    assert all(kernel["id"] != kernel_id for kernel in remaining)


class TestKernelLifecycle:
    """Test kernel CRUD operations."""

    def test_kernel_list_empty(self, runner_vm, tmp_path, monkeypatch):
        """A fresh private cache returns a structured empty kernel list."""
        tmp_path.chmod(0o700)
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        _run_mvm(
            runner_vm,
            "init", "--non-interactive", "--skip-host", "--skip-network",
            "--binary-version", "1.16.2", timeout=180,
        )
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        assert _json_success_data(result.stdout, list) == []

    @pytest.mark.slow
    @pytest.mark.kernel_build
    def test_kernel_pull(self, runner_vm):
        """Pull official kernel."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            check=False,
            timeout=300,
        )
        assert result.returncode == 0, (
            f"Official kernel pull/build failed in Tier 2 environment: "
            f"{result.stderr.strip()}"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "official" in combined or "pulling" in combined, (
            f"Expected 'official' in pull output, "
            f"got stdout: {result.stdout[:200]}, "
            f"stderr: {result.stderr[:200]}"
        )
        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, "No official kernel found in listing after pull"

    def test_kernel_list_no_cache(self, runner_vm):
        """List kernels with --no-cache flag."""
        result = _run_mvm(runner_vm, "kernel", "ls", "--no-cache", check=False)
        assert result.returncode == 0, (
            f"kernel ls --no-cache failed: {result.stderr}"
        )

    @pytest.mark.slow
    def test_kernel_ls_remote(self, runner_vm):
        """List kernels available from the remote registry with --remote flag."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "ls",
            "--remote",
            "--json",
            timeout=30,
            check=False,
        )
        assert result.returncode == 0 and result.stdout.strip(), (
            f"Remote kernel listing failed in Tier 2 environment "
            f"(should have network): {result.stderr}"
        )
        data = _json_success_data(result.stdout, list)
        assert isinstance(data, list), "Expected a list of remote kernels"
        assert len(data) > 0, "Expected at least one remote kernel entry"
        entry = data[0]
        assert isinstance(entry.get("type"), str) and entry["type"], (
            f"Expected non-empty type field: {entry}"
        )

    def test_kernel_list_json(self, runner_vm):
        """List the registered kernel through the structured CLI contract."""
        _ensure_kernel(runner_vm)
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        data = _json_success_data(result.stdout, list)
        assert data
        for entry in data:
            assert isinstance(entry.get("id"), str) and entry["id"]
            assert isinstance(entry.get("name"), str) and entry["name"]
            assert isinstance(entry.get("version"), str) and entry["version"]
            assert entry.get("type") in ("custom", "firecracker", "official")
        assert any(entry["is_present"] for entry in data)

    def test_kernel_set_default(self, runner_vm):
        """Transfer the default to a test-owned import, then restore it."""
        _ensure_kernel(runner_vm)
        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        previous_default = next(kernel for kernel in kernels if kernel["is_default"])
        inspected = _json_success_data(
            _run_mvm(runner_vm, "kernel", "inspect", previous_default["id"], "--json").stdout,
            dict,
        )
        import_name = f"sys-test-kernel-default-{uuid.uuid4().hex[:8]}"
        import_version = f"qa-{uuid.uuid4().hex[:8]}"
        imported_id = None
        try:
            imported = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "kernel", "import", import_name, inspected["storage"]["path"],
                    "--version", import_version, "--json",
                ).stdout,
                dict,
            )
            imported_id = imported["id"]
            assert imported_id != previous_default["id"]
            _run_mvm(runner_vm, "kernel", "default", imported_id)
            kernels_after = _json_success_data(
                _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
            )
            assert [kernel["id"] for kernel in kernels_after if kernel["is_default"]] == [imported_id]
        finally:
            if imported_id is not None:
                _run_mvm(runner_vm, "kernel", "default", previous_default["id"])
                _run_mvm(runner_vm, "kernel", "rm", imported_id, "--force")
                kernels_after = _json_success_data(
                    _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
                )
                assert [kernel["id"] for kernel in kernels_after if kernel["is_default"]] == [
                    previous_default["id"]
                ]
                assert all(kernel["id"] != imported_id for kernel in kernels_after)


class TestKernelInspect:
    """Test kernel inspect human and structured output."""

    def test_kernel_inspect_table(self, runner_vm):
        """Inspect a present kernel in the default human format."""
        _ensure_kernel(runner_vm)
        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        present = [k for k in kernels if k.get("is_present")]
        assert present, (
            "No present kernel to inspect — _ensure_kernel should have pulled one"
        )
        prefix = present[0]["id"][:6]
        result = _run_mvm(runner_vm, "kernel", "inspect", prefix)
        assert present[0]["name"] in result.stdout
        assert present[0]["version"] in result.stdout

    def test_kernel_inspect_json(self, runner_vm):
        """Inspect a kernel with --json output."""
        _ensure_kernel(runner_vm)
        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        present = [k for k in kernels if k.get("is_present")]
        assert present, (
            "No present kernel to inspect — _ensure_kernel should have pulled one"
        )
        prefix = present[0]["id"][:6]
        result = _run_mvm(runner_vm, "kernel", "inspect", prefix, "--json")
        assert result.returncode == 0
        data = _json_success_data(result.stdout, dict)
        assert data["kernel"]["id"] == present[0]["id"]
        assert data["kernel"]["name"] == present[0]["name"]
        assert data["kernel"]["version"] == present[0]["version"]
        assert data["storage"]["path"] == present[0]["path"]

    def test_kernel_inspect_nonexistent_fails(self, runner_vm):
        """Inspecting a nonexistent kernel should fail."""
        result = _run_mvm(
            runner_vm, "kernel", "inspect", "000000", check=False,
        )
        assert result.returncode != 0


class TestKernelRemove:
    """Test kernel removal operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_rm_by_id(self, runner_vm):
        """Remove a uniquely imported kernel by ID prefix, preserving the source."""
        kernel_id = _import_test_kernel(runner_vm)
        try:
            result = _run_mvm(runner_vm, "kernel", "rm", kernel_id[:12], check=False)
            assert result.returncode == 0, result.stderr
            remaining = _json_success_data(
                _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
            )
            assert all(kernel["id"] != kernel_id for kernel in remaining)
        finally:
            _remove_test_kernel_if_present(runner_vm, kernel_id)

    def test_kernel_rm_nonexistent(self, runner_vm):
        """Removing a nonexistent kernel returns non-zero error."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "rm",
            "totally-nonexistent-kernel-id",
            "--force",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected non-zero returncode when removing nonexistent kernel"
        )
        err = result.stderr.lower()
        assert "not found" in err or "could be resolved" in err or "failed to resolve" in err, (
            f"Expected error about missing kernel, got: {result.stderr}"
        )


class TestKernelPullWithVersion:
    """Test kernel pull with the --version flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_pull_with_version(self, runner_vm, kernel_pull_owner):
        """Pull a firecracker kernel with --version flag."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "v1.15",
            "--json",
            check=False,
            timeout=120,
        )
        kernel_pull_owner.remember_pull(result)
        assert result.returncode == 0, (
            f"Kernel pull with --version failed in Tier 2 environment: "
            f"{result.stderr.strip()}"
        )
        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        firecracker = [
            k for k in kernels
            if k.get("type") == "firecracker" and k.get("is_present")
        ]
        assert firecracker, (
            "No firecracker kernel found in listing after pull with --version"
        )

    def test_kernel_pull_with_specific_version(self, runner_vm, kernel_pull_owner):
        """Pull a firecracker kernel with a specific version (not 'latest')."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "v1.14",
            "--json",
            check=False,
            timeout=120,
        )
        kernel_pull_owner.remember_pull(result)
        assert result.returncode == 0, (
            f"Kernel pull with specific version failed in Tier 2 environment: "
            f"{result.stderr.strip()}"
        )

        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels = _json_success_data(result.stdout, list)
        firecracker_kernels = [
            k for k in kernels
            if k.get("type") == "firecracker"
            and k.get("version", "").startswith("6.1")
            and k.get("is_present")
        ]
        assert firecracker_kernels, (
            "Kernel with type=firecracker and version starting with '6.1' "
            "not found in listing. "
            f"Available firecracker versions: {[k.get('version') for k in kernels if k.get('type') == 'firecracker']}"
        )


class TestKernelPullArch:
    """Test kernel pull (arch is auto-detected in Go CLI)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_pull_with_arch(self, runner_vm, kernel_pull_owner):
        """Pull a firecracker kernel (arch detection is automatic in Go CLI)."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "v1.15",
            "--json",
            check=False,
            timeout=120,
        )
        kernel_pull_owner.remember_pull(result)
        assert result.returncode == 0, (
            f"Kernel pull failed in Tier 2 environment: {result.stderr.strip()}"
        )

        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        firecracker = [
            k for k in kernels
            if k.get("type") == "firecracker" and k.get("is_present")
        ]
        assert firecracker, (
            "No firecracker kernel found in listing after pull"
        )


class TestKernelBuild:
    """Test kernel build from source operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.kernel_build,
        pytest.mark.slow,
        pytest.mark.domain_kernel,
    ]

    @pytest.mark.timeout(600)
    def test_kernel_build_with_custom_config(self, runner_vm):
        """Build official kernel with custom config fragment.

        Config fragment created inside the test VM.
        """
        config_path = f"/tmp/custom-fragment-{uuid.uuid4().hex[:6]}.conf"
        # Create config fragment inside the VM
        _guest_run(
            runner_vm,
            f"echo 'CONFIG_NET=y' > {config_path} && echo 'CONFIG_INET=y' >> {config_path}",
        )

        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--config",
            config_path,
            "--jobs",
            "4",
            "--keep-build-dir",
            check=False,
            timeout=1800,
        )
        assert result.returncode == 0, (
            f"Kernel build with custom config failed: {result.stderr.strip()}"
        )

        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, "No official kernel found in listing after build"
        for k in official:
            kernel_path = k.get("path", "")
            assert kernel_path, "Kernel path is empty in listing"
            if os.path.isabs(kernel_path):
                full_path = kernel_path
            else:
                full_path = f"/root/.cache/mvmctl/kernels/{kernel_path}"
            check = _guest_run(
                runner_vm,
                f"test -f {full_path} && echo exists || echo not-found",
                check=False,
            )
            assert "exists" in check.stdout, (
                f"Built kernel file not found inside VM at: {full_path}"
            )

    @pytest.mark.timeout(600)
    def test_kernel_clean_rebuild(self, runner_vm):
        """Build official kernel with clean rebuild."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--clean-build",
            "--jobs",
            "4",
            check=False,
            timeout=1800,
        )
        assert result.returncode == 0, (
            f"Kernel clean rebuild failed: {result.stderr.strip()}"
        )
        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, (
            "No official kernel found in listing after clean rebuild"
        )
        for k in official:
            kernel_path = k.get("path", "")
            assert kernel_path, "Kernel path is empty in listing"
            if os.path.isabs(kernel_path):
                full_path = kernel_path
            else:
                full_path = f"/root/.cache/mvmctl/kernels/{kernel_path}"
            check = _guest_run(
                runner_vm,
                f"test -f {full_path} && echo exists || echo not-found",
                check=False,
            )
            assert "exists" in check.stdout, (
                f"Built kernel file not found inside VM at: {full_path}"
            )

    @pytest.mark.kernel_build
    @pytest.mark.timeout(600)
    def test_kernel_pull_with_features(self, runner_vm):
        """Pull official kernel with --features flag."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--features",
            "kvm",
            "--jobs",
            "4",
            check=False,
            timeout=1800,
        )
        assert result.returncode == 0, (
            f"Kernel pull with --features failed: {result.stderr.strip()}"
        )

    @pytest.mark.kernel_build
    def test_kernel_pull_with_jobs_flag(self, runner_vm):
        """Pull official kernel with --jobs flag to control parallelism."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--jobs",
            "2",
            check=False,
            timeout=1800,
        )
        assert result.returncode == 0, (
            f"Kernel pull with --jobs failed: {result.stderr.strip()}"
        )
        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, (
            "No official kernel found in listing after pull with --jobs"
        )

    @pytest.mark.kernel_build
    def test_kernel_pull_with_keep_build_dir(self, runner_vm):
        """Pull official kernel with --keep-build-dir flag."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--keep-build-dir",
            "--jobs",
            "2",
            check=False,
            timeout=1800,
        )
        assert result.returncode == 0, (
            f"Kernel pull with --keep-build-dir failed: {result.stderr.strip()}"
        )
        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, (
            "No official kernel found in listing after pull with --keep-build-dir"
        )


class TestKernelRemoveAndPull:
    """Test kernel removal and pull with set-default."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_kernel,
    ]

    @pytest.mark.kernel_build
    def test_kernel_pull_with_set_default(self, runner_vm):
        """Pull official kernel and set as default in one command."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--default",
            check=False,
            timeout=300,
        )
        assert result.returncode == 0, (
            f"Official kernel pull/build failed: {result.stderr.strip()}"
        )
        kernels = _json_success_data(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
        )
        official_default = [
            k for k in kernels
            if k.get("type") == "official"
            and k.get("is_present")
            and k.get("is_default")
        ]
        assert official_default, (
            "No official kernel marked as default after pull --default"
        )

class TestKernelStoppedVMDeletion:
    """Test removal denial while a stopped VM still references a kernel."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.domain_kernel,
    ]

    def test_delete_kernel_used_by_stopped_vm_is_denied(
        self,
        runner_vm: str,
        unique_vm_name: str,
        created_network: str,
    ) -> None:
        """An ordinary rm must preserve the VM and its referenced kernel."""
        vm_name = unique_vm_name
        ensure_vm_deps(runner_vm)
        kernel_id = _import_test_kernel(runner_vm)
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--kernel",
                kernel_id,
                "--network",
                created_network,
            )
            _run_mvm(runner_vm, "vm", "stop", vm_name)
            vms = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            vm = next(item for item in vms if item["name"] == vm_name)
            assert vm["status"] == "stopped"
            assert vm["kernel_id"] == kernel_id
            result = _run_mvm(
                runner_vm, "kernel", "rm", kernel_id, "--json", check=False,
            )
            assert result.returncode != 0
            envelope = json.loads(result.stdout)
            assert envelope["schema_version"] == 1
            assert envelope["status"] == "error"
            assert envelope["data"]["items"][0]["result"]["code"] == "kernel.in_use"
            kernels_after = _json_success_data(
                _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
            )
            assert any(kernel["id"] == kernel_id for kernel in kernels_after)
            vms_after = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            assert any(vm["name"] == vm_name for vm in vms_after)
        finally:
            vms = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            if any(vm["name"] == vm_name for vm in vms):
                _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")
            _remove_test_kernel_if_present(runner_vm, kernel_id)


class TestKernelPullAdvancedFlags:
    """Test advanced kernel pull flags."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_kernel,
        pytest.mark.kernel_build,
        pytest.mark.slow,
    ]

    def test_kernel_pull_with_arch_flag(self, runner_vm):
        """Pull a kernel (arch detection is automatic in Go CLI)."""
        # Rationale: Verifies that kernel pull works. The Go CLI detects
        # host architecture automatically — no --arch flag needed. Kernel
        # builds from source can take several minutes.
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            check=False,
            timeout=600,
        )
        assert result.returncode == 0, (
            f"Kernel pull failed: {result.stderr.strip()}"
        )

        # L2 verification: confirm the kernel appears in the listing
        ls_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels = _json_success_data(ls_result.stdout, list)
        assert isinstance(kernels, list)
        assert any(
            k.get("type") == "official" and k.get("is_present") for k in kernels
        ), "Kernel not found in listing"
