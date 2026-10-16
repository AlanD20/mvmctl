"""Pure QA ownership-state regressions: no installed CLI or acquisition bodies."""

from __future__ import annotations

import hashlib
import importlib.util
import json
import os
from pathlib import Path
from types import SimpleNamespace

import pytest


@pytest.fixture
def kernel_module(monkeypatch):
    source = Path(__file__).parents[2] / "tests/system/kernel/test_kernel.py"
    spec = importlib.util.spec_from_file_location("kernel_pull_sequence_under_test", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)

    def no_cli(*args, **kwargs):
        raise AssertionError("pure sequence tests must not invoke the installed CLI")

    monkeypatch.setattr(module, "_run_mvm", no_cli)
    return module


@pytest.fixture
def sequence(kernel_module, tmp_path):
    tmp_path.chmod(0o700)
    cache, config, evidence = (tmp_path / name for name in ("pull-cache", "pull-config", "evidence"))
    for path in (cache, config, evidence):
        path.mkdir(mode=0o700)
    owner = kernel_module._KernelPullSequence(
        "mvmctl-030-test", cache, config, evidence,
        list(kernel_module._KernelPullSequence.selectors),
    )
    owner.baseline = {domain: [] for domain in owner.domains}
    owner.config = {}
    return owner


def test_three_successes_retire_between_cases_then_clean_once(sequence, monkeypatch):
    effects = []

    def retire():
        assert sequence.receipt is not None
        effects.append(("retire", sequence.active, sequence.cache_identity))
        sequence.receipt = None

    monkeypatch.setattr(sequence, "retire_kernel", retire)
    monkeypatch.setattr(sequence, "cleanup_cache", lambda: effects.append(("clean", sequence.cache_identity)))
    with pytest.raises(AssertionError, match="incomplete"):
        sequence.cleanup()
    for selector in sequence.selected:
        sequence.begin_case(selector)
        sequence.receipt = object()
        sequence.finish_case(False)
        assert sequence.receipt is None and sequence.cache_dir.exists()
    sequence.cleanup()
    assert [effect[0] for effect in effects] == ["retire", "retire", "retire", "clean"]
    assert all(effect[-1] == sequence.cache_identity for effect in effects)


def test_failed_body_retains_publication_and_refuses_next_case_and_cleanup(sequence, monkeypatch):
    effects = []
    monkeypatch.setattr(sequence, "retire_kernel", lambda: effects.append("retire"))
    monkeypatch.setattr(sequence, "cleanup_cache", lambda: effects.append("clean"))
    sequence.begin_case(sequence.selected[0])
    publication = sequence.receipt = object()
    with pytest.raises(AssertionError, match="Failed acquisition"):
        sequence.finish_case(True)
    with pytest.raises(AssertionError, match="retained/active"):
        sequence.begin_case(sequence.selected[1])
    with pytest.raises(AssertionError, match="failed sequence"):
        sequence.cleanup()
    assert sequence.receipt is publication and sequence.cache_dir.exists() and effects == []


def test_failed_retirement_never_retries_or_cleans(sequence, monkeypatch):
    effects = []

    def refuse():
        effects.append("retire")
        raise RuntimeError("uncertain native retirement")

    monkeypatch.setattr(sequence, "retire_kernel", refuse)
    monkeypatch.setattr(sequence, "cleanup_cache", lambda: effects.append("clean"))
    sequence.begin_case(sequence.selected[0])
    sequence.receipt = object()
    with pytest.raises(RuntimeError, match="uncertain"):
        sequence.finish_case(False)
    with pytest.raises(AssertionError, match="failed sequence"):
        sequence.cleanup()
    assert sequence.aborted and not sequence.completed and effects == ["retire"]


def test_failed_pull_keeps_native_evidence_without_inventory_adoption(sequence):
    sequence.begin_case(sequence.selected[0])
    sequence.remember_pull(SimpleNamespace(returncode=1, stdout="partial publication", stderr="native failure"))
    assert sequence.receipt is None
    assert (sequence.output_dir / "pull.stdout").read_text() == "partial publication"
    assert (sequence.output_dir / "pull.stderr").read_text() == "native failure"
    with pytest.raises(AssertionError, match="No successful exact"):
        sequence.finish_case(False)
    assert sequence.aborted and sequence.cache_dir.exists()


def test_changed_owned_backing_refuses_typed_removal(sequence, kernel_module, monkeypatch):
    path = sequence.cache_dir / "kernels" / ("a" * 64)
    path.parent.mkdir(mode=0o700)
    path.write_bytes(b"original kernel")
    item = dict(id="a" * 64, name="kernel", base_name="vmlinux", version="6.1", arch="x86_64",
                type="firecracker", is_default=False, is_present=True, path=str(path),
                created_at="before", updated_at="before")
    backing, digest = sequence.file_receipt(path)
    sequence.receipt = item, path, backing, digest
    path.write_bytes(b"changed kernel")
    monkeypatch.setattr(sequence, "inventory", lambda: {**sequence.baseline, "kernel": [item]})
    calls = []

    def inspect_only(_runner, *args, **kwargs):
        calls.append(args)
        assert args[:2] == ("kernel", "inspect")
        inspected = dict(kernel={key: item[key] for key in (
            "id", "name", "base_name", "version", "arch", "type", "is_default", "is_present")},
            storage={"path": str(path)}, timestamps={"created_at": "before", "updated_at": "before"})
        return SimpleNamespace(stdout=json.dumps(dict(schema_version=1, status="success", data=inspected, error=None)))

    monkeypatch.setattr(kernel_module, "_run_mvm", inspect_only)
    with pytest.raises(AssertionError, match="backing changed"):
        sequence.retire_kernel()
    assert len(calls) == 1 and path.read_bytes() == b"changed kernel" and sequence.receipt is not None


def test_failed_terminal_cache_cleanup_keeps_native_receipts(sequence, kernel_module, monkeypatch):
    monkeypatch.setattr(sequence, "inventory", lambda: sequence.baseline)

    def native(_runner, *args, **kwargs):
        if args[:2] == ("config", "ls"):
            return SimpleNamespace(stdout=json.dumps(dict(schema_version=1, status="success", data={}, error=None)))
        assert args == ("cache", "clean", "--force", "--json") and kwargs == dict(timeout=210, check=False)
        return SimpleNamespace(returncode=1, stdout="uncertain cleanup", stderr="protected refusal")

    monkeypatch.setattr(kernel_module, "_run_mvm", native)
    with pytest.raises(AssertionError, match="protected refusal"):
        sequence.cleanup_cache()
    assert sequence.cache_dir.exists()
    assert (sequence.output_dir / "cache-clean.exit").read_text() == "1"
    assert (sequence.output_dir / "cache-clean.stdout").read_text() == "uncertain cleanup"


def test_case_fixture_selects_distinct_mirrors_and_observes_native_call_failure(
    sequence, kernel_module, monkeypatch, tmp_path,
):
    shared, v114 = tmp_path / "shared-mirror", tmp_path / "v114-mirror"
    shared.mkdir()
    v114.mkdir()
    monkeypatch.setenv("MVM_ASSET_MIRROR", str(shared))
    monkeypatch.setenv("MVM_TEST_KERNEL_V114_MIRROR", str(v114))
    retired = []
    monkeypatch.setattr(sequence, "retire_kernel", lambda: retired.append(sequence.active))
    session = SimpleNamespace(testsfailed=0)
    first, second = sequence.selected[:2]
    for selector, expected in ((first, shared), (second, v114)):
        request = SimpleNamespace(node=SimpleNamespace(nodeid="tests/system/kernel/test_kernel.py::" + selector),
                                  session=session)
        with monkeypatch.context() as case_environment:
            fixture = kernel_module.kernel_pull_owner.__wrapped__(sequence, request, case_environment)
            assert next(fixture) is sequence
            assert os.environ["MVM_ASSET_MIRROR"] == str(expected)
            if selector == first:
                with pytest.raises(StopIteration):
                    next(fixture)
            else:
                session.testsfailed += 1
                with pytest.raises(AssertionError, match="Failed acquisition"):
                    next(fixture)
        assert os.environ["MVM_ASSET_MIRROR"] == str(shared)
    assert retired == [first] and sequence.aborted


def test_v114_alias_to_shared_mirror_rejected_before_case_start(sequence, kernel_module, monkeypatch, tmp_path):
    shared = tmp_path / "shared-mirror"
    shared.mkdir()
    alias = tmp_path / "mirror-alias"
    alias.symlink_to(shared)
    monkeypatch.setenv("MVM_ASSET_MIRROR", str(shared))
    monkeypatch.setenv("MVM_TEST_KERNEL_V114_MIRROR", str(alias))
    selector = sequence.selected[1]
    request = SimpleNamespace(node=SimpleNamespace(nodeid="tests/system/kernel/test_kernel.py::" + selector))
    fixture = kernel_module.kernel_pull_owner.__wrapped__(sequence, request, monkeypatch)
    with pytest.raises(AssertionError, match="authentic private mirror"):
        next(fixture)
    assert sequence.active is None and sequence.output_dir == sequence.cache_dir.parent


@pytest.fixture
def retained_handoff(sequence, kernel_module, tmp_path, monkeypatch):
    # Synthetic private metadata for admission only; no real root object is created or adopted.
    protected = sequence.cache_dir / ".mvm-protected"
    protected.mkdir(mode=0o700)
    original_lstat = Path.lstat

    def protected_metadata(path):
        info = original_lstat(path)
        if path == protected:
            return SimpleNamespace(st_dev=info.st_dev, st_ino=info.st_ino, st_uid=0,
                                   st_gid=0, st_mode=info.st_mode)
        return info

    monkeypatch.setattr(Path, "lstat", protected_metadata)
    identities = {label: dict(path=str(path), identity=list(kernel_module._KernelPullOwner.directory_identity(path)))
                  for label, path in (("parent", sequence.cache_dir.parent), ("cache", sequence.cache_dir),
                                      ("config", sequence.config_dir))}
    info = protected.lstat()
    identities["protected"] = dict(path=str(protected), identity=[info.st_dev, info.st_ino, 0, 0, info.st_mode])
    digest = "b" * 64
    identifier = hashlib.sha256(f"{digest}:6.1.155:x86_64".encode()).hexdigest()
    item = dict(id=identifier, version="6.1.155", arch="x86_64", path=str(sequence.cache_dir / "kernels" / identifier))
    pull = json.dumps(dict(schema_version=1, status="success", data=item, error=None))
    documents = {
        "pull": pull,
        "terminal": json.dumps(dict(fixture_baseline_preserved=True, preservation_errors=[], passed=False,
                                    commands=[dict(label="kernel-acquisition-v1.15", native_exit=1)])),
        "failure": json.dumps(dict(no_mutation=True, published_kernel_absent=True, pull_stdout=pull,
                                   pull_stdout_sha256=hashlib.sha256(pull.encode()).hexdigest(),
                                   paths={row["path"]: dict(zip(
                                       ("st_dev", "st_ino", "st_uid", "st_gid", "st_mode"), row["identity"]))
                                       for label, row in identities.items() if label != "config"})),
    }
    references = {}
    for label, raw in documents.items():
        path = sequence.cache_dir.parent / ("pull.stdout" if label == "pull" else f"{label}.json")
        path.write_text(raw)
        references[label] = dict(path=str(path), sha256=hashlib.sha256(raw.encode()).hexdigest())
    handoff = dict(schema_version=1, native_handle=84838, **identities, **references,
                   publication_sha256=digest, config_files={})
    path = tmp_path / "handoff.json"
    path.write_text(json.dumps(handoff))
    return path, hashlib.sha256(path.read_bytes()).hexdigest(), handoff


def test_retained_cache_admitted_only_from_pinned_native_and_identity_receipts(kernel_module, retained_handoff, sequence):
    path, digest, _ = retained_handoff
    assert kernel_module._KernelPullSequence.retained_paths(path, digest) == (sequence.cache_dir, sequence.config_dir)
    with pytest.raises(AssertionError, match="digest mismatch"):
        kernel_module._KernelPullSequence.retained_paths(path, "0" * 64)


@pytest.mark.parametrize("change", ["cache_identity", "failed_native", "changed_config", "publication_digest"])
def test_retained_admission_rejects_changed_authority_without_fresh_fallback(
    kernel_module, retained_handoff, sequence, change,
):
    path, _, handoff = retained_handoff
    if change == "cache_identity":
        handoff["cache"]["identity"][1] += 1
    elif change == "failed_native":
        terminal_path = Path(handoff["terminal"]["path"])
        terminal = json.loads(terminal_path.read_text())
        terminal["commands"][0]["native_exit"] = 0
        terminal_path.write_text(json.dumps(terminal))
        handoff["terminal"]["sha256"] = hashlib.sha256(terminal_path.read_bytes()).hexdigest()
    elif change == "changed_config":
        (sequence.config_dir / "unexpected").write_text("changed")
    else:
        handoff["publication_sha256"] = "c" * 64
    path.write_text(json.dumps(handoff))
    with pytest.raises(AssertionError):
        kernel_module._KernelPullSequence.retained_paths(path, hashlib.sha256(path.read_bytes()).hexdigest())
    assert sequence.cache_dir.exists() and not sequence.completed


@pytest.fixture
def retained_initialization_handoff(retained_handoff, sequence):
    path, _, handoff = retained_handoff
    keys = sequence.config_dir / "keys"
    keys.mkdir(mode=0o700)
    historical_key = keys / "historical-key"
    historical_key.write_bytes(b"historical private key")
    handoff["config_files"] = sequence.config_files(sequence.config_dir)
    path.write_text(json.dumps(handoff))
    return path, hashlib.sha256(path.read_bytes()).hexdigest(), historical_key


def test_retained_normal_init_precedes_inventory_and_preserves_historical_authority(
    kernel_module, retained_initialization_handoff, sequence, monkeypatch,
):
    path, digest, historical_key = retained_initialization_handoff
    original_key = sequence.file_receipt(historical_key)
    calls = []

    def native(_runner, *args, **kwargs):
        calls.append(args)
        if args == ("cache", "init", "--json"):
            assert kwargs == dict(check=False)
            assert not (sequence.cache_dir / "mvmdb.db").exists()
            for name in ("tmp", "vms", "images", "kernels", "bin", "logs"):
                (sequence.cache_dir / name).mkdir(mode=0o700)
            (sequence.cache_dir / "mvmdb.db").write_bytes(b"initialized private schema")
            for name in ("audit.log", "audit.log.lock", "mvmdb.db.v2.snap", "mvmdb.db.v3.snap",
                         "mvmdb.db.v4.snap", "mvmdb.db.v5.snap", "mvmdb.db.v6.snap",
                         "mvmdb.db.v7.snap", "mvmdb.db.v8.snap"):
                (sequence.cache_dir / name).write_bytes(b"normal init audit or migration snapshot")
            data = dict(cache_dir=str(sequence.cache_dir))
        else:
            assert (sequence.cache_dir / "mvmdb.db").is_file()
            assert args[1:] == ("ls", "--json")
            data = {} if args[0] == "config" else []
        return SimpleNamespace(returncode=0, stderr="", stdout=json.dumps(
            dict(schema_version=1, status="success", data=data, error=None),
        ))

    monkeypatch.setattr(kernel_module, "_run_mvm", native)
    sequence.initialize_retained(path, digest)
    assert calls == [("cache", "init", "--json"),
                     *((domain, "ls", "--json") for domain in sequence.domains), ("config", "ls", "--json")]
    before = json.loads((sequence.output_root / "retained-init.before.json").read_text())
    after = json.loads((sequence.output_root / "retained-init.after.json").read_text())
    assert after["identities"] == before["identities"]
    assert after["config_files"] == before["config_files"]
    assert after["added_cache_entries"] == [
        "audit.log", "audit.log.lock", "bin", "images", "kernels", "logs", "mvmdb.db",
        "mvmdb.db.v2.snap", "mvmdb.db.v3.snap", "mvmdb.db.v4.snap", "mvmdb.db.v5.snap",
        "mvmdb.db.v6.snap", "mvmdb.db.v7.snap", "mvmdb.db.v8.snap", "tmp", "vms",
    ]
    assert sequence.file_receipt(historical_key) == original_key
    assert sequence.inventory() == sequence.baseline
    assert (sequence.output_root / "retained-init.exit").read_text() == "0"
    assert not (sequence.cache_dir.parent / "retained-init.stdout").exists()


@pytest.mark.parametrize("fault", ["native", "returned_path", "parent", "cache", "config", "protected",
                                 "historical_key", "unexpected_entry", "new_file_uid", "new_file_type",
                                 "unexpected_snapshot"])
def test_retained_init_failure_or_changed_authority_retains_evidence_without_inventory_or_fallback(
    kernel_module, retained_initialization_handoff, sequence, monkeypatch, fault,
):
    path, digest, historical_key = retained_initialization_handoff
    calls = []

    def native(_runner, *args, **kwargs):
        calls.append(args)
        assert args == ("cache", "init", "--json") and kwargs == dict(check=False)
        (sequence.cache_dir / "mvmdb.db").write_bytes(b"initialized private schema")
        if fault in ("parent", "cache", "config", "protected"):
            changed = {"parent": sequence.cache_dir.parent, "cache": sequence.cache_dir,
                       "config": sequence.config_dir, "protected": sequence.cache_dir / ".mvm-protected"}[fault]
            changed.chmod(0o750)
        elif fault == "historical_key":
            historical_key.write_bytes(b"changed historical key")
        elif fault == "unexpected_entry":
            (sequence.cache_dir / "unowned").write_bytes(b"unexpected publication")
        elif fault == "unexpected_snapshot":
            (sequence.cache_dir / "mvmdb.db.v9.snap").write_bytes(b"unknown schema snapshot")
        elif fault == "new_file_type":
            (sequence.cache_dir / "mvmdb.db.v2.snap").mkdir(mode=0o700)
        elif fault == "new_file_uid":
            audit = sequence.cache_dir / "audit.log.lock"
            audit.write_bytes(b"foreign-owned audit lock")
            original_lstat = Path.lstat

            def foreign_audit_owner(candidate):
                info = original_lstat(candidate)
                if candidate == audit:
                    return SimpleNamespace(st_dev=info.st_dev, st_ino=info.st_ino, st_uid=os.getuid() + 1,
                                           st_gid=info.st_gid, st_mode=info.st_mode)
                return info

            monkeypatch.setattr(Path, "lstat", foreign_audit_owner)
        data = dict(cache_dir=str(sequence.cache_dir.parent if fault == "returned_path" else sequence.cache_dir))
        return SimpleNamespace(returncode=1 if fault == "native" else 0, stderr="native init refused",
                               stdout=json.dumps(dict(schema_version=1, status="success", data=data, error=None)))

    monkeypatch.setattr(kernel_module, "_run_mvm", native)
    with pytest.raises(AssertionError):
        sequence.initialize_retained(path, digest)
    assert calls == [("cache", "init", "--json")]
    assert sequence.cache_dir.exists() and historical_key.exists() and not sequence.completed
    assert (sequence.output_root / "retained-init.stdout").is_file()
    assert (sequence.output_root / "retained-init.stderr").read_text() == "native init refused"
    assert (sequence.output_root / "retained-init.exit").read_text() == ("1" if fault == "native" else "0")
    assert (sequence.output_root / "retained-init.before.json").is_file()
    assert (sequence.output_root / "retained-init.after.json").is_file()
