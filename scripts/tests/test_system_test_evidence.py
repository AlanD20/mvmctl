"""Hermetic QA report recording and read-only finalization behavior."""

from __future__ import annotations

import hashlib
import importlib.util
import json
import subprocess
import sys
from pathlib import Path

import pytest

from test_run_system_tests import (
    _complete_pytest_outcome_report,
    _host_reboot_fixture,
)


@pytest.fixture
def orchestrator(monkeypatch):
    script = Path(__file__).parents[1] / "run-system-tests.py"
    monkeypatch.syspath_prepend(str(script.parent))
    spec = importlib.util.spec_from_file_location(
        "mvmctl_evidence_test_orchestrator", script
    )
    module = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, spec.name, module)
    spec.loader.exec_module(module)
    return module


@pytest.fixture
def evidence(orchestrator):
    import system_test_evidence

    return system_test_evidence


def _binding(source, registry):
    candidate = dict(sha256="a" * 64, version="mvm 0.3.0-rc.fixture")
    return dict(
        schema_version=1,
        host="mvmctl-030-test",
        boot_id="11111111-1111-4111-8111-111111111111",
        source=source,
        candidate=candidate,
        controller=dict(
            path="/usr/local/bin/mvm",
            command=["/usr/local/bin/mvm"],
            sha256=candidate["sha256"],
            identity=[1, 2, 0, 0, 0o755, 1, 100, 3, 4],
        ),
        registry=registry,
        selection={
            name: row for name, row in registry.items() if name != "host_reboot"
        },
    )


def _record(
    evidence,
    tmp_path,
    source,
    registry,
    *,
    native=0,
    outcomes=None,
    report_cleanup=True,
    runner_cleanup=True,
    selection=None,
):
    task = tmp_path / "matrix"
    task.mkdir(mode=0o700)
    binding = _binding(source, registry)
    if selection is not None:
        binding["selection"] = selection
    capture = evidence.MatrixEvidence(task, binding)
    results = []
    for domain, row in binding["selection"].items():
        report = capture.domain(domain, row["tier"], row["files"])
        runtime = {
            path: digest
            for path, digest in source["payload"].items()
            if path.startswith("tests/system/")
        }
        report.record_runtime_source(json.dumps(runtime).encode(), runtime)
        report.record_pytest(
            subprocess.CompletedProcess(
                ["pytest", *row["files"]],
                native,
                stdout="literal pytest output\n",
                stderr="literal stderr\n",
            ),
            outcomes if outcomes is not None else _complete_pytest_outcome_report(),
        )
        report.report_cleanup = report_cleanup
        report.runner_cleanup = runner_cleanup if row["tier"] in (1, 2) else None
        result = dict(domain=domain, passed=True, output="literal pytest output\n")
        report.close(result)
        results.append(result)
    capture.close(results, source)
    return task, binding


@pytest.fixture
def inputs():
    return (
        dict(
            revision="f" * 40,
            tree="e" * 40,
            payload={"tests/system/one/test_one.py": "b" * 64},
        ),
        {
            "one": dict(tier=2, files=["tests/system/one/test_one.py"]),
            "host_reboot": dict(
                tier=2, files=["tests/system/host/test_host_reboot.py"]
            ),
        },
    )


def test_recorded_reports_verify_without_overwrite(evidence, tmp_path, inputs):
    source, registry = inputs
    task, binding = _record(evidence, tmp_path, source, registry)
    result = evidence.verify_matrix_task(task, source, binding["candidate"], registry)
    assert result["domains"] == {"one": 1}
    assert (task / "one.stdout").read_bytes() == b"literal pytest output\n"
    assert json.loads((task / "one.native.json").read_bytes()) == {"returncode": 0}
    with pytest.raises(RuntimeError, match="empty"):
        evidence.MatrixEvidence(task, binding)


@pytest.mark.parametrize(
    "fault",
    [
        "missing",
        "mutated",
        "source",
        "controller",
        "native",
        "outcomes",
        "report-cleanup",
        "runner-cleanup",
        "runtime-source",
        "symlink",
    ],
)
def test_matrix_refuses_incomplete_or_changed_proof(evidence, tmp_path, inputs, fault):
    source, registry = inputs
    outcomes = json.loads(_complete_pytest_outcome_report())
    if fault == "outcomes":
        outcomes.update(passed=0, skipped=1)
    task, binding = _record(
        evidence,
        tmp_path,
        source,
        registry,
        native=1 if fault == "native" else 0,
        outcomes=json.dumps(outcomes).encode(),
        report_cleanup=fault != "report-cleanup",
        runner_cleanup=fault != "runner-cleanup",
    )
    if fault == "missing":
        (task / "one.native.json").unlink()
    elif fault == "mutated":
        (task / "one.stdout").write_bytes(b"replaced output")
    elif fault in ("source", "controller"):
        changed = json.loads((task / "binding.json").read_bytes())
        if fault == "source":
            changed["source"]["revision"] = "c" * 40
        else:
            changed["controller"]["path"] = "/home/www/.local/bin/mvm"
        (task / "binding.json").write_text(json.dumps(changed))
    elif fault == "runtime-source":
        (task / "one.runtime-source.json").write_bytes(b"{}")
    elif fault == "symlink":
        target = tmp_path / "replacement"
        target.write_bytes((task / "one.outcomes.json").read_bytes())
        (task / "one.outcomes.json").unlink()
        (task / "one.outcomes.json").symlink_to(target)
    with pytest.raises((RuntimeError, OSError)):
        evidence.verify_matrix_task(task, source, binding["candidate"], registry)


@pytest.mark.parametrize("fault", [None, "coverage", "outer-controller", "source"])
def test_finalization_joins_current_matrix_and_unchanged_outer_reboot_reader(
    evidence,
    orchestrator,
    monkeypatch,
    tmp_path,
    fault,
):
    task, envelope, calls, phases, state = _host_reboot_fixture(
        orchestrator, monkeypatch, tmp_path
    )
    assert orchestrator.run_host_reboot_phase("pre", task, None)["phase_complete"]
    assert orchestrator.run_host_reboot_phase("post", task, envelope)["passed"]
    source = dict(
        revision="f" * 40,
        tree="e" * 40,
        payload={path: "a" * 64 for path in orchestrator._HOST_REBOOT_SOURCE_FILES},
    )
    source["payload"]["tests/system/one/test_one.py"] = "a" * 64
    registry = {
        "one": dict(tier=2, files=["tests/system/one/test_one.py"]),
        "host_reboot": dict(tier=2, files=[orchestrator._HOST_REBOOT_FILE]),
    }
    monkeypatch.setattr(evidence, "source_identity", lambda _root: source)
    monkeypatch.setattr(
        orchestrator, "_validate_system_test_registry", lambda _parser: None
    )
    monkeypatch.setattr(orchestrator, "_evidence_registry", lambda: registry)
    matrix, _ = _record(evidence, tmp_path, source, registry)
    original_calls = list(calls)

    def forbidden(*_args, **_kwargs):
        raise AssertionError("finalization must not query or mutate the runner")

    for helper in (
        "mvm",
        "_host_reboot_probe",
        "_host_reboot_outer_identity",
        "_build_mvm_binary",
        "run_prepare",
    ):
        monkeypatch.setattr(orchestrator, helper, forbidden)
    if fault == "coverage":
        registry["missing"] = dict(
            tier=3, files=["tests/system/missing/test_missing.py"]
        )
        # Retain a truthful larger registry with a partial selection, not a forged PASS for the missing domain.
        binding = json.loads((matrix / "binding.json").read_bytes())
        binding["registry"] = registry
        (matrix / "binding.json").write_text(json.dumps(binding))
        terminal = json.loads((matrix / "terminal.json").read_bytes())
        terminal["proof_sha256"]["binding.json"] = hashlib.sha256(
            (matrix / "binding.json").read_bytes()
        ).hexdigest()
        (matrix / "terminal.json").write_text(json.dumps(terminal))
    elif fault == "outer-controller":
        state["controller"]["identity"][1] = 999
    elif fault == "source":
        source["revision"] = "d" * 40
    if fault:
        with pytest.raises(RuntimeError):
            evidence.finalize_named_runner_release_evidence(
                orchestrator, [matrix], task
            )
    else:
        result = evidence.finalize_named_runner_release_evidence(
            orchestrator, [matrix], task
        )
        assert result["passed"] is True and result["domains"] == {"one": 1}
        assert result["host_reboot"]["passed"] is True
    assert calls == original_calls and phases == ["pre", "post"]


@pytest.mark.parametrize("cleanup_failure", [False, True])
def test_local_recording_retains_literal_reports_before_checked_cleanup(
    evidence,
    orchestrator,
    monkeypatch,
    tmp_path,
    inputs,
    cleanup_failure,
):
    source, registry = inputs
    source["payload"] = {}
    task = tmp_path / "capture"
    task.mkdir(mode=0o700)
    record = evidence.MatrixEvidence(task, _binding(source, registry))
    capture = record.domain("one", 3, registry["one"]["files"])
    original_directory = orchestrator.tempfile.TemporaryDirectory

    def directory(**kwargs):
        created = original_directory(dir=tmp_path, **kwargs)
        if cleanup_failure:

            def fail():
                raise RuntimeError("literal cleanup fault")

            created.cleanup = fail
        return created

    monkeypatch.setattr(orchestrator.tempfile, "TemporaryDirectory", directory)

    def pytest_process(_files, *, timeout, extra_env):
        Path(extra_env[orchestrator.PYTEST_OUTCOME_REPORT_ENV]).write_bytes(
            _complete_pytest_outcome_report()
        )
        return subprocess.CompletedProcess(
            ["pytest"], 0, stdout="real retained bytes", stderr=""
        )

    monkeypatch.setattr(orchestrator, "_run_pytest", pytest_process)
    result, reason = orchestrator._run_local_pytest(
        "one", registry["one"]["files"], timeout=60, evidence=capture
    )
    assert result.returncode == 0
    assert capture.report_cleanup is not cleanup_failure
    assert bool(reason) is cleanup_failure
    assert (task / "one.stdout").read_bytes() == b"real retained bytes"
    assert (
        task / "one.outcomes.json"
    ).read_bytes() == _complete_pytest_outcome_report()
