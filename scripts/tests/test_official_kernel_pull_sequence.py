"""Pure official QA-owner regressions; never invoke installed CLI or kernel builds."""

from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace

import pytest


def success(data):
    return SimpleNamespace(returncode=0, stdout=json.dumps({
        "schema_version": 1, "status": "success", "data": data, "error": None,
    }), stderr="")


@pytest.fixture
def kernel_module(monkeypatch):
    source = Path(__file__).parents[2] / "tests/system/kernel/test_kernel.py"
    spec = importlib.util.spec_from_file_location("official_kernel_sequence_under_test", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)

    def no_cli(*args, **kwargs):
        raise AssertionError("pure owner checks must not invoke installed CLI")

    monkeypatch.setattr(module, "_run_mvm", no_cli)
    return module


@pytest.fixture
def sequence(kernel_module, tmp_path):
    tmp_path.chmod(0o700)
    cache, config, evidence = (tmp_path / name for name in ("cache", "config", "evidence"))
    for path in (cache, config, evidence):
        path.mkdir(mode=0o700)
    (cache / "kernels").mkdir(mode=0o700)
    owner = kernel_module._OfficialKernelPullSequence(
        "mvmctl-030-test", cache, config, evidence,
        list(kernel_module._OfficialKernelPullSequence.selectors),
    )
    owner.baseline = {domain: [] for domain in owner.domains}
    owner.config = {}
    return owner


def begin(sequence, monkeypatch, selector):
    sequence.selected = (selector,)
    monkeypatch.setattr(sequence, "inventory", lambda: sequence.baseline)
    sequence.begin_case(selector)


def publication(sequence, *, default=False):
    payload = b"official kernel payload"
    digest = hashlib.sha256(payload).hexdigest()
    kernel_id = hashlib.sha256(f"{digest}:6.19.9:x86_64".encode()).hexdigest()
    path = sequence.cache_dir / "kernels" / kernel_id
    path.write_bytes(payload)
    item = dict(id=kernel_id, name="vmlinux-official-6.19.9-x86_64", base_name="vmlinux-official",
                version="6.19.9", arch="x86_64", type="official", path=str(path),
                is_default=default, is_present=True, created_at="2026-10-10T00:00:00Z",
                updated_at="2026-10-10T00:00:00Z")
    return item, path


def expose(sequence, kernel_module, monkeypatch, item):
    monkeypatch.setattr(sequence, "inventory", lambda: {**sequence.baseline, "kernel": [item]})

    def inspect(_runner, *args, **kwargs):
        assert args == ("kernel", "inspect", item["id"], "--json")
        info = {key: item[key] for key in (
            "id", "name", "base_name", "version", "arch", "type", "is_default", "is_present",
        )}
        if item.get("features"):
            info["features"] = item["features"]
        return success(dict(kernel=info, storage={"path": item["path"]},
                            timestamps={key: item[key] for key in ("created_at", "updated_at")}))

    monkeypatch.setattr(kernel_module, "_run_mvm", inspect)


def test_stale_default_pull_boolean_is_refused_without_inventory_adoption(sequence, monkeypatch):
    begin(sequence, monkeypatch, "TestKernelRemoveAndPull::test_kernel_pull_with_set_default")
    item, path = publication(sequence, default=False)
    with pytest.raises(AssertionError):
        sequence.remember_pull(success(item))
    assert sequence.receipt is None and path.exists()
    assert json.loads((sequence.output_dir / "pull.stdout").read_text())["data"] == item


def test_default_timestamp_delta_keeps_raw_maps_and_exact_inspection(
    sequence, kernel_module, monkeypatch,
):
    begin(sequence, monkeypatch, "TestKernelRemoveAndPull::test_kernel_pull_with_set_default")
    item, path = publication(sequence, default=True)
    listed = {**item, "updated_at": "2026-10-10T00:00:01Z"}
    expose(sequence, kernel_module, monkeypatch, listed)
    sequence.remember_pull(success(item))
    assert sequence.receipt[0] == listed and sequence.receipt[1] == path
    evidence = json.loads((sequence.output_dir / "publication.json").read_text())
    assert evidence["pull"] == item and evidence["listed"] == listed
    assert evidence["inspected"]["timestamps"]["updated_at"] == listed["updated_at"]


@pytest.mark.parametrize("drift", [{"name": "other"}, {"created_at": "other"}, {"features": ["kvm"]}])
def test_default_admission_does_not_normalize_other_fields(
    sequence, kernel_module, monkeypatch, drift,
):
    begin(sequence, monkeypatch, "TestKernelRemoveAndPull::test_kernel_pull_with_set_default")
    item, path = publication(sequence, default=True)
    expose(sequence, kernel_module, monkeypatch, {**item, **drift})
    with pytest.raises(AssertionError):
        sequence.remember_pull(success(item))
    assert sequence.receipt is None and path.exists()


def test_real_build_log_is_retained_and_revalidated_without_claiming_flag_coverage(
    sequence, kernel_module, monkeypatch,
):
    begin(sequence, monkeypatch, sequence.selected[0])
    item, _path = publication(sequence)
    log = sequence.cache_dir / "kernels/.mvm-kernel-stage-123.build.log"
    log.write_bytes(b"CC kernel.o\nLD vmlinux\n")
    expose(sequence, kernel_module, monkeypatch, item)
    sequence.remember_pull(success(item))
    assert (sequence.output_dir / "compiler.build.log").read_bytes() == log.read_bytes()
    evidence = json.loads((sequence.output_dir / "build-evidence.json").read_text())
    assert evidence["logs"][0]["sha256"] == hashlib.sha256(log.read_bytes()).hexdigest()
    assert evidence["jobs_or_keep_flags_are_not_compiler_execution_proof"] is True
    log.write_bytes(b"changed build log")
    with pytest.raises(AssertionError, match="build log changed"):
        sequence.retire_kernel()
    assert sequence.receipt is not None and log.exists()


def test_cache_hit_is_recorded_without_fabricating_compiler_log(sequence, kernel_module, monkeypatch):
    begin(sequence, monkeypatch, sequence.selected[0])
    item, _path = publication(sequence)
    expose(sequence, kernel_module, monkeypatch, item)
    result = success(item)
    result.stderr = "Using cached kernel build (config hash match)"
    sequence.remember_pull(result)
    evidence = json.loads((sequence.output_dir / "build-evidence.json").read_text())
    assert evidence["cli_reports_cached_build"] is True and evidence["logs"] == []
    assert not (sequence.output_dir / "compiler.build.log").exists()


def test_unknown_auxiliary_file_refuses_publication_ownership(sequence, kernel_module, monkeypatch):
    begin(sequence, monkeypatch, sequence.selected[0])
    item, path = publication(sequence)
    unknown = sequence.cache_dir / "kernels/not-a-build-log"
    unknown.write_bytes(b"unknown artifact")
    expose(sequence, kernel_module, monkeypatch, item)
    with pytest.raises(AssertionError):
        sequence.remember_pull(success(item))
    assert sequence.receipt is None and path.exists() and unknown.exists()
    with pytest.raises(AssertionError, match="No successful exact"):
        sequence.finish_case(False)
    assert sequence.aborted


def test_failed_pull_saves_native_exit_but_never_adopts_or_cleans(sequence, monkeypatch):
    begin(sequence, monkeypatch, sequence.selected[0])
    sequence.remember_pull(SimpleNamespace(returncode=1, stdout="partial", stderr="native failure"))
    assert sequence.receipt is None
    assert (sequence.output_dir / "pull.exit").read_text() == "1"
    with pytest.raises(AssertionError, match="Failed official"):
        sequence.finish_case(True)
    with pytest.raises(AssertionError, match="failed official"):
        sequence.cleanup()
    assert sequence.cache_dir.exists()


def test_same_cache_retires_per_case_and_cleans_once_only_after_complete_sequence(sequence, monkeypatch):
    effects = []
    monkeypatch.setattr(sequence, "inventory", lambda: sequence.baseline)

    def retire():
        assert sequence.receipt is not None
        effects.append(("retire", sequence.active, sequence.cache_identity))
        sequence.receipt = None

    monkeypatch.setattr(sequence, "retire_kernel", retire)
    monkeypatch.setattr(sequence, "cleanup_cache", lambda: effects.append(("clean", sequence.cache_identity)))
    for selector in sequence.selected:
        sequence.begin_case(selector)
        sequence.receipt = object()
        sequence.finish_case(False)
    sequence.cleanup()
    assert len(effects) == 9 and [effect[0] for effect in effects] == ["retire"] * 8 + ["clean"]
    assert all(effect[-1] == sequence.cache_identity for effect in effects)


def test_failed_retirement_retains_active_case_and_refuses_next_or_final_cleanup(sequence, monkeypatch):
    monkeypatch.setattr(sequence, "inventory", lambda: sequence.baseline)
    sequence.begin_case(sequence.selected[0])
    receipt = sequence.receipt = object()
    effects = []

    def refuse():
        effects.append("retire")
        raise RuntimeError("uncertain retirement")

    monkeypatch.setattr(sequence, "retire_kernel", refuse)
    monkeypatch.setattr(sequence, "cleanup_cache", lambda: effects.append("clean"))
    with pytest.raises(RuntimeError, match="uncertain"):
        sequence.finish_case(False)
    with pytest.raises(AssertionError, match="retained/active"):
        sequence.begin_case(sequence.selected[1])
    with pytest.raises(AssertionError, match="failed official"):
        sequence.cleanup()
    assert sequence.receipt is receipt and sequence.completed == [] and effects == ["retire"]


def test_failed_cache_cleanup_keeps_native_receipts_and_known_logs(sequence, kernel_module, monkeypatch):
    sequence.completed = list(sequence.selected)
    monkeypatch.setattr(sequence, "inventory", lambda: sequence.baseline)
    log = sequence.cache_dir / "kernels/.mvm-kernel-stage-456.build.log"
    log.write_bytes(b"compiler evidence")
    sequence.build_logs[log] = sequence.file_receipt(log)

    def native(_runner, *args, **kwargs):
        if args == ("config", "ls", "--json"):
            return success({})
        assert args == ("cache", "clean", "--force", "--json")
        return SimpleNamespace(returncode=1, stdout="partial cleanup", stderr="root refusal")

    monkeypatch.setattr(kernel_module, "_run_mvm", native)
    with pytest.raises(AssertionError, match="root refusal"):
        sequence.cleanup()
    assert sequence.aborted and sequence.cache_dir.exists() and log.exists()
    assert (sequence.output_dir / "cache-clean.exit").read_text() == "1"
