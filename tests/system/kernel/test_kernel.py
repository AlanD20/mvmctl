"""Kernel management system tests."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shlex
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
        self.output_dir = cache_dir.parent

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
            (self.output_dir / f"pull.{label}").write_text(output, encoding="utf-8")
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

    def retire_kernel(self):
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
        result = _run_mvm(self.runner_vm, "kernel", "rm", item["id"], "--force", "--json", check=False)
        self.save_native("retirement", result)
        assert result.returncode == 0, result.stderr
        removed = _json_success_data(result.stdout, dict)
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
        self.receipt = None

    def cleanup_cache(self):
        assert self.receipt is None, "live or uncertain publication; retain cache"
        assert self.directory_identity(self.cache_dir) == self.cache_identity
        assert self.directory_identity(self.config_dir) == self.config_identity
        assert self.inventory() == self.baseline, "private state changed; retain cache"
        assert _json_success_data(
            _run_mvm(self.runner_vm, "config", "ls", "--json").stdout, dict,
        ) == self.config
        result = _run_mvm(self.runner_vm, "cache", "clean", "--force", "--json", timeout=210, check=False)
        self.save_native("cache-clean", result)
        assert result.returncode == 0, result.stderr
        cleaned = _json_success_data(result.stdout, dict)
        assert cleaned["cache_dir"] == str(self.cache_dir) and cleaned["cache_dir_removed"] is True, cleaned
        assert not cleaned["prune_result"]["failed_ids"] and not cleaned["prune_result"]["had_running_vms"], cleaned
        with pytest.raises(FileNotFoundError):
            self.cache_dir.lstat()

    def save_native(self, label, result):
        for stream in ("stdout", "stderr"):
            (self.output_dir / f"{label}.{stream}").write_text(getattr(result, stream), encoding="utf-8")
        (self.output_dir / f"{label}.exit").write_text(str(result.returncode), encoding="utf-8")


class _KernelPullSequence(_KernelPullOwner):
    """One immutable-kernel store, exact case receipts, one terminal cache retirement."""

    selectors = {
        "TestKernelPullWithVersion::test_kernel_pull_with_version": "v1.15",
        "TestKernelPullWithVersion::test_kernel_pull_with_specific_version": "v1.14",
        "TestKernelPullArch::test_kernel_pull_with_arch": "v1.15",
    }

    def __init__(self, runner_vm, cache_dir, config_dir, output_dir, selected):
        super().__init__(runner_vm, cache_dir, config_dir)
        assert selected and len(selected) == len(set(selected))
        assert set(selected) <= self.selectors.keys(), selected
        self.selected = tuple(selected)
        self.completed = []
        self.active = None
        self.aborted = False
        self.output_root = output_dir

    @classmethod
    def retained_paths(cls, receipt_path, expected_sha256):
        """Admit an explicit reviewed handoff, never resources discovered by difference."""
        raw = receipt_path.read_bytes()
        assert hashlib.sha256(raw).hexdigest() == expected_sha256, "retained handoff digest mismatch"
        handoff = json.loads(raw)
        assert set(handoff) == {
            "schema_version", "native_handle", "parent", "cache", "config", "protected",
            "terminal", "failure", "pull", "publication_sha256", "config_files",
        }, handoff
        assert handoff["schema_version"] == 1 and handoff["native_handle"] == 84838, handoff
        documents = {}
        for label in ("terminal", "failure", "pull"):
            reference = handoff[label]
            payload = Path(reference["path"]).read_bytes()
            assert hashlib.sha256(payload).hexdigest() == reference["sha256"], label
            documents[label] = payload
        terminal = json.loads(documents["terminal"])
        assert terminal["fixture_baseline_preserved"] is True and not terminal["preservation_errors"]
        assert terminal["passed"] is False
        commands = [command for command in terminal["commands"]
                    if command["label"] == "kernel-acquisition-v1.15"]
        assert len(commands) == 1 and commands[0]["native_exit"] == 1, terminal
        failure = json.loads(documents["failure"])
        assert failure["no_mutation"] is True and failure["published_kernel_absent"] is True
        assert documents["pull"].decode() == failure["pull_stdout"]
        assert handoff["pull"]["sha256"] == failure["pull_stdout_sha256"]
        item = _json_success_data(documents["pull"].decode(), dict)
        digest = handoff["publication_sha256"]
        assert re.fullmatch(r"[0-9a-f]{64}", digest), digest
        assert hashlib.sha256(f"{digest}:{item['version']}:{item['arch']}".encode()).hexdigest() == item["id"]
        paths = {label: Path(handoff[label]["path"]) for label in ("parent", "cache", "config", "protected")}
        assert all(path.is_absolute() for path in paths.values()), paths
        assert paths["cache"] == paths["parent"] / "pull-cache"
        assert paths["config"] == paths["parent"] / "pull-config"
        assert paths["protected"] == paths["cache"] / ".mvm-protected"
        assert Path(item["path"]) == paths["cache"] / "kernels" / item["id"]
        assert Path(handoff["pull"]["path"]) == paths["parent"] / "pull.stdout"
        for label in ("parent", "cache", "config"):
            assert cls.directory_identity(paths[label]) == tuple(handoff[label]["identity"]), label
        for label in ("parent", "cache", "protected"):
            observed = failure["paths"][str(paths[label])]
            historical = tuple(observed[key] for key in ("st_dev", "st_ino", "st_uid", "st_gid", "st_mode"))
            assert historical == tuple(handoff[label]["identity"]), label
        protected = paths["protected"].lstat()
        assert (protected.st_dev, protected.st_ino, protected.st_uid,
                protected.st_gid, protected.st_mode) == tuple(handoff["protected"]["identity"])
        assert stat.S_ISDIR(protected.st_mode) and protected.st_uid == 0
        assert stat.S_IMODE(protected.st_mode) == 0o700
        with pytest.raises(FileNotFoundError):
            Path(item["path"]).lstat()
        cls.assert_config_files(paths["config"], handoff["config_files"])
        return paths["cache"], paths["config"]

    @staticmethod
    def config_files(config_dir):
        actual = {}
        for path in config_dir.rglob("*"):
            assert not path.is_symlink(), path
            if path.is_dir():
                actual[str(path.relative_to(config_dir))] = {
                    "identity": list(_KernelPullOwner.directory_identity(path)),
                }
            else:
                identity, digest = _KernelPullOwner.file_receipt(path)
                actual[str(path.relative_to(config_dir))] = {"identity": list(identity), "sha256": digest}
        return actual

    @classmethod
    def assert_config_files(cls, config_dir, expected):
        assert cls.config_files(config_dir) == expected, "retained config changed"

    def retained_initialization_state(self):
        paths = {"parent": self.cache_dir.parent, "cache": self.cache_dir,
                 "config": self.config_dir, "protected": self.cache_dir / ".mvm-protected"}
        fields = ("st_dev", "st_ino", "st_uid", "st_gid", "st_mode")
        return {
            "identities": {label: [getattr(path.lstat(), field) for field in fields]
                           for label, path in paths.items()},
            "config_files": self.config_files(self.config_dir),
            "cache_entries": {path.name: [getattr(path.lstat(), field) for field in fields]
                              for path in sorted(self.cache_dir.iterdir())},
        }

    def initialize_retained(self, receipt_path, expected_sha256):
        # Normal init creates the missing mvmdb.db/schema; it does not rebind or adopt this store.
        assert self.retained_paths(receipt_path, expected_sha256) == (self.cache_dir, self.config_dir)
        assert self.directory_identity(self.cache_dir) == self.cache_identity
        assert self.directory_identity(self.config_dir) == self.config_identity
        before = self.retained_initialization_state()
        (self.output_root / "retained-init.before.json").write_text(json.dumps(before), encoding="utf-8")
        result = _run_mvm(self.runner_vm, "cache", "init", "--json", check=False)
        for stream in ("stdout", "stderr"):
            (self.output_root / f"retained-init.{stream}").write_text(getattr(result, stream), encoding="utf-8")
        (self.output_root / "retained-init.exit").write_text(str(result.returncode), encoding="utf-8")
        after = self.retained_initialization_state()
        added = sorted(after["cache_entries"].keys() - before["cache_entries"].keys())
        (self.output_root / "retained-init.after.json").write_text(
            json.dumps({**after, "added_cache_entries": added}), encoding="utf-8",
        )
        assert result.returncode == 0, result.stderr
        initialized = _json_success_data(result.stdout, dict)
        assert initialized["cache_dir"] == str(self.cache_dir), initialized
        assert after["identities"] == before["identities"], "retained store identity changed during init"
        assert after["config_files"] == before["config_files"], "retained config changed during init"
        for name, identity in before["cache_entries"].items():
            assert after["cache_entries"].get(name) == identity, ("retained cache entry changed", name)
        # Closed current init outputs: audit writer plus DB snapshots before schema migrations 2–8.
        directories = {"tmp", "vms", "images", "kernels", "bin", "logs"}
        regular_files = {
            "mvmdb.db", "mvmdb.db-wal", "mvmdb.db-shm", "audit.log", "audit.log.lock",
            "mvmdb.db.v2.snap", "mvmdb.db.v3.snap", "mvmdb.db.v4.snap", "mvmdb.db.v5.snap",
            "mvmdb.db.v6.snap", "mvmdb.db.v7.snap", "mvmdb.db.v8.snap",
        }
        assert set(added) <= directories | regular_files, ("unexpected cache init entries", added)
        for name in added:
            identity = after["cache_entries"][name]
            assert identity[2] == os.getuid(), (name, identity)
            assert (stat.S_ISDIR if name in directories else stat.S_ISREG)(identity[4]), (name, identity)
        self.file_receipt(self.cache_dir / "mvmdb.db")
        assert self.retained_paths(receipt_path, expected_sha256) == (self.cache_dir, self.config_dir)
        self.baseline = self.inventory()
        assert all(not items for items in self.baseline.values()), self.baseline
        self.config = _json_success_data(
            _run_mvm(self.runner_vm, "config", "ls", "--json").stdout, dict,
        )

    def select_cache(self, environment):
        environment.setenv("MVM_CACHE_DIR", str(self.cache_dir))
        environment.setenv("MVM_CONFIG_DIR", str(self.config_dir))
        environment.setenv("MVM_TEMP_DIR", str(self.cache_dir / "tmp"))
        environment.setenv("TMPDIR", str(self.cache_dir / "tmp"))
        environment.delenv("MVM_WARM_POOL", raising=False)

    def begin_case(self, selector):
        assert not self.aborted and self.active is None and self.receipt is None, "sequence retained/active"
        assert selector == self.selected[len(self.completed)], "unexpected sequence order"
        self.active = selector
        self.output_dir = self.output_root / selector.replace("::", "-")
        self.output_dir.mkdir(mode=0o700)

    def finish_case(self, failed):
        assert self.active is not None
        if failed:
            self.aborted = True
            raise AssertionError(f"Failed acquisition case; retain sequence at {self.cache_dir}")
        try:
            self.retire_kernel()
        except BaseException:
            self.aborted = True
            raise
        self.completed.append(self.active)
        self.active = None

    def cleanup(self):
        assert not self.aborted and self.active is None, "failed sequence retained"
        assert tuple(self.completed) == self.selected, "incomplete sequence retained"
        try:
            self.cleanup_cache()
        except BaseException:
            self.aborted = True
            raise


@pytest.fixture(scope="module")
def kernel_pull_sequence(runner_vm, tmp_path_factory, request):
    """Retain one store between cases; preserve shared originals on every exit."""
    selected = [item.nodeid.split("test_kernel.py::", 1)[1] for item in request.session.items
                if item.path == request.node.path and
                item.nodeid.split("test_kernel.py::", 1)[-1] in _KernelPullSequence.selectors]
    assert request.config.option.maxfail == 1, "kernel acquisition sequences require native pytest -x"
    baseline = {domain: _json_success_data(
        _run_mvm(runner_vm, domain, "ls", "--json").stdout, list,
    ) for domain in _KernelPullOwner.domains}
    config = _json_success_data(_run_mvm(runner_vm, "config", "ls", "--json").stdout, dict)
    sources = {Path(item["path"]): _KernelPullOwner.file_receipt(Path(item["path"]))
               for item in baseline["kernel"] if item["is_present"]}
    tmp_path = tmp_path_factory.mktemp("kernel-pull-sequence")
    tmp_path.chmod(0o700)
    receipt_path = os.environ.get("MVM_TEST_KERNEL_PULL_RETAINED_RECEIPT")
    receipt_digest = os.environ.get("MVM_TEST_KERNEL_PULL_RETAINED_SHA256")
    assert bool(receipt_path) == bool(receipt_digest), "retained handoff needs path and SHA256"
    if receipt_path:
        cache_dir, config_dir = _KernelPullSequence.retained_paths(Path(receipt_path), receipt_digest)
    else:
        cache_dir, config_dir = tmp_path / "pull-cache", tmp_path / "pull-config"
        cache_dir.mkdir(mode=0o700)
        config_dir.mkdir(mode=0o700)
    failures = []
    owner = _KernelPullSequence(runner_vm, cache_dir, config_dir, tmp_path, selected)
    try:
        try:
            with pytest.MonkeyPatch.context() as selection:
                owner.select_cache(selection)
                if receipt_path:
                    owner.initialize_retained(Path(receipt_path), receipt_digest)
                else:
                    owner.initialize()
            yield owner
        finally:
            try:
                with pytest.MonkeyPatch.context() as selection:
                    owner.select_cache(selection)
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


@pytest.fixture
def kernel_pull_owner(kernel_pull_sequence, request, monkeypatch):
    """Select only the authentic mirror for this original, unchanged case body."""
    owner = kernel_pull_sequence
    selector = request.node.nodeid.split("test_kernel.py::", 1)[1]
    version = owner.selectors[selector]
    mirror = os.environ.get("MVM_ASSET_MIRROR")
    if version == "v1.14":
        mirror = os.environ.get("MVM_TEST_KERNEL_V114_MIRROR")
        shared = os.environ.get("MVM_ASSET_MIRROR")
        assert mirror and shared and not os.path.samefile(mirror, shared), "v1.14 needs its authentic private mirror"
    assert mirror and Path(mirror).is_absolute() and not Path(mirror).is_symlink(), mirror
    assert Path(mirror).is_dir(), mirror
    owner.select_cache(monkeypatch)
    monkeypatch.setenv("MVM_ASSET_MIRROR", mirror)
    owner.begin_case(selector)
    failures_before = request.session.testsfailed
    try:
        yield owner
    finally:
        owner.finish_case(request.session.testsfailed != failures_before)


class _OfficialKernelPullSequence(_KernelPullOwner):
    """Closed official cases share one cache; failed evidence is never adopted or removed."""

    selectors = {
        "TestKernelLifecycle::test_kernel_pull": False,
        "TestKernelBuild::test_kernel_build_with_custom_config": False,
        "TestKernelBuild::test_kernel_clean_rebuild": False,
        "TestKernelBuild::test_kernel_pull_with_features": False,
        "TestKernelBuild::test_kernel_pull_with_jobs_flag": False,
        "TestKernelBuild::test_kernel_pull_with_keep_build_dir": False,
        "TestKernelRemoveAndPull::test_kernel_pull_with_set_default": True,
        "TestKernelPullAdvancedFlags::test_kernel_pull_with_arch_flag": False,
    }

    def __init__(self, runner_vm, cache_dir, config_dir, output_dir, selected):
        super().__init__(runner_vm, cache_dir, config_dir)
        assert selected and len(selected) == len(set(selected))
        assert set(selected) <= self.selectors.keys(), selected
        self.selected = tuple(selected)
        self.completed = []
        self.active = None
        self.aborted = False
        self.output_root = output_dir
        self.build_logs = {}
        self.borrowed_release = None

    def root_release_receipt(self):
        """Read only the already trusted, fixed qualification pair and manifest."""
        script = r'''
import hashlib,json,os,stat
from pathlib import Path
base=Path('/var/lib/mvmctl/binaries')
slot=base/'x86_64'/'1.16.2'
result={'directories':{},'files':{}}
for path in (base,base/'x86_64',slot):
    info=path.lstat()
    assert stat.S_ISDIR(info.st_mode) and info.st_uid==info.st_gid==0
    assert not info.st_mode & 0o022
    result['directories'][str(path)]=[info.st_dev,info.st_ino,info.st_uid,info.st_gid,info.st_mode]
for name in ('manifest.json','firecracker','jailer'):
    path=slot/name
    with os.fdopen(os.open(path,os.O_RDONLY|os.O_NOFOLLOW),'rb') as source:
        before=os.fstat(source.fileno())
        assert stat.S_ISREG(before.st_mode) and before.st_uid==before.st_gid==0
        assert before.st_nlink==1 and before.st_size>0 and not before.st_mode & 0o022
        if name!='manifest.json': assert before.st_mode & 0o111
        payload=source.read()
        after=os.fstat(source.fileno())
    fields=('st_dev','st_ino','st_uid','st_gid','st_mode','st_nlink','st_size','st_mtime_ns','st_ctime_ns')
    identity=[getattr(before,key) for key in fields]
    assert identity==[getattr(after,key) for key in fields]
    result['files'][name]={'identity':identity,'sha256':hashlib.sha256(payload).hexdigest()}
    if name=='manifest.json': result['manifest']=json.loads(payload)
print(json.dumps(result,sort_keys=True))
'''
        return json.loads(_guest_run(
            self.runner_vm, "sudo -n python3 -c " + shlex.quote(script),
        ).stdout)

    def prepare_borrowed_release(self, shared_binaries):
        """Refuse absent/untrusted prerequisites before any private admission effect."""
        try:
            assert os.uname().machine == "x86_64"
            pair = [item for item in shared_binaries if item["is_default"]]
            assert len(pair) == 2 and {item["type"] for item in pair} == {"firecracker", "jailer"}, pair
            root = self.root_release_receipt()
            manifest = root["manifest"]
            assert manifest["schema_version"] == 1 and manifest["release"] == {
                "version": "1.16.2", "architecture": "x86_64",
            }, manifest
            for item in pair:
                name = item["type"]
                member = root["files"][name]
                assert item["version"] == "1.16.2" and item["architecture"] == "x86_64", item
                assert item["is_default"] is True and item["is_present"] is True, item
                assert item["path"] == f"/var/lib/mvmctl/binaries/x86_64/1.16.2/{name}", item
                assert manifest[name] == {"sha256": member["sha256"], "size_bytes": member["identity"][6]}
                expected_id = hashlib.sha256(
                    f"mvmctl:binary-release:v1:{name}:x86_64:1.16.2:{member['sha256']}".encode(),
                ).hexdigest()
                assert item["id"] == expected_id, item
            mirror = Path(os.environ["MVM_ASSET_MIRROR"])
            assert mirror.is_absolute() and mirror.is_dir() and not mirror.is_symlink(), mirror
            archive = mirror / "firecracker-v1.16.2-x86_64.tgz"
            archive_receipt = self.file_receipt(archive)
            assert archive_receipt[1] == manifest["archive_sha256"], archive
            self.borrowed_release = pair, root, archive, archive_receipt
            (self.output_root / "borrowed-release.before.json").write_text(json.dumps({
                "shared_pair": pair, "root": root, "archive": str(archive),
                "archive_receipt": archive_receipt,
            }), encoding="utf-8")
        except BaseException:
            self.aborted = True
            raise

    def assert_borrowed_release(self):
        assert self.borrowed_release is not None, "borrowed release was never admitted"
        _, root, archive, archive_receipt = self.borrowed_release
        assert self.root_release_receipt() == root, "borrowed root release changed; retain sequence"
        assert self.file_receipt(archive) == archive_receipt, "borrowed archive changed; retain sequence"

    def initialize(self):
        try:
            self.output_dir = self.output_root
            assert self.borrowed_release is not None, "prepare borrowed release before selecting private cache"
            self.assert_borrowed_release()
            super().initialize()  # Preserve the original exact operator cache-init command.
            result = _run_mvm(self.runner_vm, "bin", "pull", "1.16.2", "--json", timeout=300, check=False)
            self.save_native("borrowed-release-pull", result)
            assert result.returncode == 0, result.stdout
            admitted = _json_success_data(result.stdout, list)
            shared_pair = self.borrowed_release[0]
            # Projection timestamps are fresh in this private DB; root identity is not.
            without_timestamps = lambda item: {key: value for key, value in item.items()
                                               if key not in ("created_at", "updated_at")}
            assert sorted(map(without_timestamps, admitted), key=lambda item: item["id"]) == sorted(
                map(without_timestamps, shared_pair), key=lambda item: item["id"],
            ), admitted
            assert len(admitted) == 2 and all(item["is_default"] is True for item in admitted), admitted
            assert all(isinstance(item.get(key), str) and item[key] for item in admitted
                       for key in ("created_at", "updated_at")), admitted
            inventory = self.inventory()
            assert sorted(inventory["bin"], key=lambda item: item["id"]) == sorted(
                admitted, key=lambda item: item["id"],
            ), inventory
            assert {**inventory, "bin": []} == self.baseline, inventory
            self.assert_borrowed_release()
            self.baseline = inventory
            (self.output_root / "borrowed-release.admitted.json").write_text(json.dumps({
                "pull": admitted, "inventory": inventory, "root": self.borrowed_release[1],
                "archive_receipt": self.borrowed_release[3],
                "upstream_checksum_metadata_read": True, "archive_download": False,
            }), encoding="utf-8")
        except BaseException:
            self.aborted = True
            raise

    def select_cache(self, environment):
        environment.setenv("MVM_CACHE_DIR", str(self.cache_dir))
        environment.setenv("MVM_CONFIG_DIR", str(self.config_dir))
        environment.setenv("MVM_TEMP_DIR", str(self.cache_dir / "tmp"))
        environment.setenv("TMPDIR", str(self.cache_dir / "tmp"))
        environment.delenv("MVM_WARM_POOL", raising=False)

    def begin_case(self, selector):
        assert not self.aborted and self.active is None and self.receipt is None, "sequence retained/active"
        assert selector == self.selected[len(self.completed)], "unexpected official sequence order"
        assert self.directory_identity(self.cache_dir) == self.cache_identity
        assert self.directory_identity(self.config_dir) == self.config_identity
        assert self.inventory() == self.baseline, "private state changed; retain cache"
        self.assert_build_logs()
        self.active = selector
        self.output_dir = self.output_root / selector.replace("::", "-")
        self.output_dir.mkdir(mode=0o700)

    def remember_pull(self, result):
        assert self.active is not None and self.receipt is None
        self.save_native("pull", result)
        if result.returncode != 0:
            return  # Never recover a failed publication by inventory difference.
        item = _json_success_data(result.stdout, dict)
        assert re.fullmatch(r"[0-9a-f]{64}", item["id"]), item
        assert item["type"] == "official" and item["is_present"], item
        assert item["version"] and item["arch"] and item["is_default"] is self.selectors[self.active], item
        assert item["id"] not in {row["id"] for row in self.baseline["kernel"]}, item
        path = self.cache_dir / "kernels" / item["id"]
        assert Path(item["path"]) == path and not path.parent.is_symlink(), item
        backing, digest = self.file_receipt(path)
        assert hashlib.sha256(f"{digest}:{item['version']}:{item['arch']}".encode()).hexdigest() == item["id"]
        inventory = self.inventory()
        assert len(inventory["kernel"]) == 1, inventory
        observed = inventory["kernel"][0]
        expected = item
        if self.selectors[self.active]:
            # SetDefault updates the repository timestamp; never accept a stale default boolean.
            assert observed["is_default"] is True and isinstance(observed["updated_at"], str)
            assert observed["updated_at"], observed
            expected = {**item, "updated_at": observed["updated_at"]}
        assert observed == expected, (item, observed)
        assert inventory == {**self.baseline, "kernel": [observed]}, inventory
        inspected = _json_success_data(
            _run_mvm(self.runner_vm, "kernel", "inspect", item["id"], "--json").stdout, dict,
        )
        self.assert_inspect_receipt(inspected, observed)
        assert self.file_receipt(path) == (backing, digest), path
        (self.output_dir / "publication.json").write_text(json.dumps({
            "pull": item, "listed": observed, "inspected": inspected,
            "backing_identity": backing, "sha256": digest,
        }, sort_keys=True), encoding="utf-8")
        self.remember_build_logs(path, result)
        self.receipt = observed, path, backing, digest

    def assert_build_logs(self, publication=None):
        directory = self.cache_dir / "kernels"
        assert not directory.is_symlink(), directory
        actual = set(directory.iterdir()) if directory.exists() else set()
        assert actual == set(self.build_logs) | ({publication} if publication else set()), actual
        for path, receipt in self.build_logs.items():
            assert self.file_receipt(path) == receipt, "owned build log changed; retain cache"

    def remember_build_logs(self, publication, result):
        # BuildKernel writes one .mvm-kernel-stage-*.build.log beside its staging output.
        # These are caller-owned auxiliary files in our initially empty exclusive directory,
        # not authority to adopt any public/protected kernel publication.
        directory = publication.parent
        assert not directory.is_symlink(), directory
        for path, receipt in self.build_logs.items():
            assert self.file_receipt(path) == receipt, "prior build log changed; retain cache"
        new = set(directory.iterdir()) - set(self.build_logs) - {publication}
        assert len(new) <= 1, "unknown producer artifacts; retain cache"
        references = []
        for path in new:
            assert re.fullmatch(r"\.mvm-kernel-stage-[A-Za-z0-9]+\.build\.log", path.name), path
            receipt = self.file_receipt(path)
            assert receipt[0][6] <= 64 * 1024 * 1024, "build log exceeds retained output budget"
            with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as source:
                payload = source.read(64 * 1024 * 1024 + 1)
            assert hashlib.sha256(payload).hexdigest() == receipt[1]
            assert self.file_receipt(path) == receipt
            destination = self.output_dir / "compiler.build.log"
            destination.write_bytes(payload)
            self.build_logs[path] = receipt
            references.append({"path": str(path), "retained_path": str(destination),
                               "identity": receipt[0], "sha256": receipt[1]})
        (self.output_dir / "build-evidence.json").write_text(json.dumps({
            "logs": references,
            "cli_reports_cached_build": "Using cached kernel build (config hash match)" in result.stderr,
            "jobs_or_keep_flags_are_not_compiler_execution_proof": True,
        }, sort_keys=True), encoding="utf-8")
        self.assert_build_logs(publication)

    def retire_kernel(self):
        assert self.receipt is not None, "No successful exact official receipt; retain cache"
        item, path, backing, digest = self.receipt
        assert self.directory_identity(self.cache_dir) == self.cache_identity
        assert self.directory_identity(self.config_dir) == self.config_identity
        assert self.inventory() == {**self.baseline, "kernel": [item]}, "private publication changed"
        inspected = _json_success_data(
            _run_mvm(self.runner_vm, "kernel", "inspect", item["id"], "--json").stdout, dict,
        )
        self.assert_inspect_receipt(inspected, item)
        assert self.file_receipt(path) == (backing, digest), "owned backing changed; retain cache"
        self.assert_build_logs(path)
        result = _run_mvm(self.runner_vm, "kernel", "rm", item["id"], "--force", "--json", check=False)
        self.save_native("retirement", result)
        assert result.returncode == 0, result.stderr
        removed = _json_success_data(result.stdout, dict)
        assert len(removed["items"]) == 1, removed
        assert removed["items"][0]["result"]["code"] == "kernel.removed", removed
        assert removed["items"][0]["error"] is None, removed
        with pytest.raises(FileNotFoundError):
            path.lstat()
        assert self.inventory() == self.baseline, "private defaults/state changed; retain cache"
        assert _json_success_data(
            _run_mvm(self.runner_vm, "config", "ls", "--json").stdout, dict,
        ) == self.config
        self.assert_build_logs()
        assert self.directory_identity(self.cache_dir) == self.cache_identity
        self.receipt = None

    def finish_case(self, failed):
        assert self.active is not None
        if failed:
            self.aborted = True
            raise AssertionError(f"Failed official case; retain sequence at {self.cache_dir}")
        try:
            self.retire_kernel()
        except BaseException:
            self.aborted = True
            raise
        self.completed.append(self.active)
        self.active = None

    def cleanup(self):
        assert not self.aborted and self.active is None, "failed official sequence retained"
        assert tuple(self.completed) == self.selected, "incomplete official sequence retained"
        try:
            self.assert_borrowed_release()
            self.assert_build_logs()
            self.cleanup_cache()  # Only typed terminal cleanup removes original owned logs/build trees.
            self.assert_borrowed_release()  # Borrowed default pair is never removed by cache cleanup.
        except BaseException:
            self.aborted = True
            raise


@pytest.fixture(scope="module")
def official_kernel_pull_sequence(runner_vm, tmp_path_factory, request):
    selected = [item.nodeid.split("test_kernel.py::", 1)[1] for item in request.session.items
                if item.path == request.node.path and
                item.nodeid.split("test_kernel.py::", 1)[-1] in _OfficialKernelPullSequence.selectors]
    assert request.config.option.maxfail == 1, "official acquisition sequences require native pytest -x"
    baseline = {domain: _json_success_data(
        _run_mvm(runner_vm, domain, "ls", "--json").stdout, list,
    ) for domain in _KernelPullOwner.domains}
    config = _json_success_data(_run_mvm(runner_vm, "config", "ls", "--json").stdout, dict)
    sources = {Path(item["path"]): _KernelPullOwner.file_receipt(Path(item["path"]))
               for item in baseline["kernel"] if item["is_present"]}
    tmp_path = tmp_path_factory.mktemp("official-kernel-pull-sequence")
    tmp_path.chmod(0o700)
    cache, private_config = tmp_path / "pull-cache", tmp_path / "pull-config"
    cache.mkdir(mode=0o700)
    private_config.mkdir(mode=0o700)
    owner = _OfficialKernelPullSequence(runner_vm, cache, private_config, tmp_path, selected)
    failures = []
    try:
        try:
            owner.prepare_borrowed_release(baseline["bin"])
            with pytest.MonkeyPatch.context() as selection:
                owner.select_cache(selection)
                owner.initialize()
            yield owner
        finally:
            try:
                with pytest.MonkeyPatch.context() as selection:
                    owner.select_cache(selection)
                    owner.cleanup()
            except Exception as error:
                failures.append(f"official cleanup retained {cache}: {error!r}")
    finally:
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
                failures.append(f"shared original kernel changed: {path}: {error!r}")
        if owner.borrowed_release is not None:
            try:
                owner.assert_borrowed_release()
            except Exception as error:
                failures.append(f"borrowed root pair/archive changed: {error!r}")
        assert not failures, failures


@pytest.fixture
def official_kernel_pull_owner(official_kernel_pull_sequence, request, monkeypatch):
    owner = official_kernel_pull_sequence
    mirror = os.environ.get("MVM_ASSET_MIRROR")
    assert mirror and Path(mirror).is_absolute() and Path(mirror).is_dir()
    assert not Path(mirror).is_symlink(), mirror
    owner.select_cache(monkeypatch)
    owner.begin_case(request.node.nodeid.split("test_kernel.py::", 1)[1])
    failures_before = request.session.testsfailed
    try:
        yield owner
    finally:
        owner.finish_case(request.session.testsfailed != failures_before)


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
    def test_kernel_pull(self, runner_vm, official_kernel_pull_owner):
        """Pull official kernel."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--json",
            check=False,
            timeout=300,
        )
        official_kernel_pull_owner.remember_pull(result)
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

    # 1800s CLI + 30s helper grace + 570s bounded receipt/readback/teardown budget.
    @pytest.mark.timeout(2400)
    def test_kernel_build_with_custom_config(self, runner_vm, official_kernel_pull_owner):
        """Build official kernel with custom config fragment.

        Config fragment created inside the test VM.
        """
        config_path = official_kernel_pull_owner.output_dir / "custom-fragment.conf"
        config_path.write_text("CONFIG_NET=y\nCONFIG_INET=y\n", encoding="utf-8")

        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--config",
            str(config_path),
            "--jobs",
            "4",
            "--keep-build-dir",
            "--json",
            check=False,
            timeout=1800,
        )
        official_kernel_pull_owner.remember_pull(result)
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

    @pytest.mark.timeout(2400)
    def test_kernel_clean_rebuild(self, runner_vm, official_kernel_pull_owner):
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
            "--json",
            check=False,
            timeout=1800,
        )
        official_kernel_pull_owner.remember_pull(result)
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
    @pytest.mark.timeout(2400)
    def test_kernel_pull_with_features(self, runner_vm, official_kernel_pull_owner):
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
            "--json",
            check=False,
            timeout=1800,
        )
        official_kernel_pull_owner.remember_pull(result)
        assert result.returncode == 0, (
            f"Kernel pull with --features failed: {result.stderr.strip()}"
        )

    @pytest.mark.kernel_build
    def test_kernel_pull_with_jobs_flag(self, runner_vm, official_kernel_pull_owner):
        """Pull official kernel with --jobs flag to control parallelism."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--jobs",
            "2",
            "--json",
            check=False,
            timeout=1800,
        )
        official_kernel_pull_owner.remember_pull(result)
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
    def test_kernel_pull_with_keep_build_dir(self, runner_vm, official_kernel_pull_owner):
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
            "--json",
            check=False,
            timeout=1800,
        )
        official_kernel_pull_owner.remember_pull(result)
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
    def test_kernel_pull_with_set_default(self, runner_vm, official_kernel_pull_owner):
        """Pull official kernel and set as default in one command."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--default",
            "--json",
            check=False,
            timeout=300,
        )
        official_kernel_pull_owner.remember_pull(result)
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

    def test_kernel_pull_with_arch_flag(self, runner_vm, official_kernel_pull_owner):
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
            "--json",
            check=False,
            timeout=600,
        )
        official_kernel_pull_owner.remember_pull(result)
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
