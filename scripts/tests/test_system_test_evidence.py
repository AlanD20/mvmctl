"""Hermetic QA report recording and read-only finalization behavior."""

from __future__ import annotations

import hashlib
import importlib.util
import json
import os
import subprocess
import sys
import xml.etree.ElementTree as ET
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
        selected = row.get("nodes", row["files"])
        nodes = (
            selected
            if "nodes" in row
            else [path + "::test_fixture" for path in selected]
        )
        report = capture.domain(domain, row["tier"], selected)
        runtime = {
            path: digest
            for path, digest in source["payload"].items()
            if path.startswith("tests/system/")
            or path
            in ("scripts/system_test_evidence.py", "scripts/system_test_outcomes.py")
        }
        report.record_runtime_source(json.dumps(runtime).encode(), runtime)
        report.record_pytest(
            subprocess.CompletedProcess(
                ["pytest", *row["files"]],
                native,
                stdout="literal pytest output\n",
                stderr="literal stderr\n",
            ),
            outcomes
            if outcomes is not None
            else _complete_pytest_outcome_report(collected=len(nodes)),
        )
        report.record_nodes(_node_report(nodes))
        report.record_junit(_junit(evidence, nodes))
        report.report_cleanup = report_cleanup
        report.runner_cleanup = runner_cleanup if row["tier"] in (1, 2) else None
        result = dict(domain=domain, passed=True, output="literal pytest output\n")
        report.close(result)
        results.append(result)
    capture.close(results, source)
    return task, binding


def _node_report(nodes, *, collect_only=False, native=0):
    return json.dumps(
        dict(
            schema_version=1,
            collect_only=collect_only,
            collected=nodes,
            phases={}
            if collect_only
            else {
                node: {
                    phase: dict(outcome="passed", wasxfail=False)
                    for phase in ("setup", "call", "teardown")
                }
                for node in nodes
            },
            collection_errors=0,
            deselected=0,
            exit_status=native,
        )
    ).encode()


def _junit(evidence, nodes):
    suite = ET.Element(
        "testsuite", tests=str(len(nodes)), failures="0", errors="0", skipped="0"
    )
    for node in nodes:
        classname, name = evidence._junit_identity(node)
        ET.SubElement(suite, "testcase", classname=classname, name=name)
    return ET.tostring(suite)


def _collection(evidence, tmp_path, source, registry, *, nodes=None):
    task = tmp_path / "collection"
    task.mkdir(mode=0o700)
    binding = _binding(source, registry)
    binding["selection"] = registry
    capture = evidence.MatrixEvidence(task, binding)
    if nodes is None:
        nodes = [
            path + "::test_fixture"
            for domain, row in registry.items()
            if domain != "host_reboot"
            for path in row["files"]
        ]
        if "host_reboot" in registry:
            nodes.extend(sorted(evidence.HOST_NODES))
    evidence._write(
        task, "collection-nodes.json", _node_report(nodes, collect_only=True)
    )
    evidence._write(task, "collection-native.json", b'{"returncode":0}')
    evidence._write(task, "collection-stdout", b"literal complete collection")
    evidence._write(task, "collection-stderr", b"")
    capture.close([dict(domain="full_collection", passed=True)], source)
    return task


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
    collection = _collection(evidence, tmp_path, source, registry)
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
                orchestrator, [matrix], task, collection_task=collection
            )
    else:
        result = evidence.finalize_named_runner_release_evidence(
            orchestrator, [matrix], task, collection_task=collection
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
        kwargs.setdefault("dir", tmp_path)
        created = original_directory(**kwargs)
        if cleanup_failure:

            def fail():
                raise RuntimeError("literal cleanup fault")

            created.cleanup = fail
        return created

    monkeypatch.setattr(orchestrator.tempfile, "TemporaryDirectory", directory)

    def pytest_process(_files, *, timeout, extra_env, node_report):
        Path(extra_env[orchestrator.PYTEST_OUTCOME_REPORT_ENV]).write_bytes(
            _complete_pytest_outcome_report()
        )
        nodes = [_files[0] + "::test_fixture"]
        node_report.write_bytes(_node_report(nodes))
        (node_report.parent / "pytest.xml").write_bytes(_junit(evidence, nodes))
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


@pytest.mark.parametrize(
    "fault", [None, "missing-node", "unknown-file", "native", "mutated", "host-node"]
)
def test_full_collection_receipt_defines_all_registry_files(
    evidence, tmp_path, inputs, fault
):
    source, registry = inputs
    nodes = ["tests/system/one/test_one.py::test_fixture", *sorted(evidence.HOST_NODES)]
    if fault == "missing-node":
        nodes = sorted(evidence.HOST_NODES)
    elif fault == "unknown-file":
        nodes.append("tests/system/unknown/test_other.py::test_extra")
    elif fault == "host-node":
        nodes[-1] += "_changed"
    task = _collection(evidence, tmp_path, source, registry, nodes=nodes)
    if fault == "native":
        (task / "collection-native.json").write_bytes(b'{"returncode":1}')
    elif fault == "mutated":
        (task / "collection-stdout").write_bytes(b"different collection")
    if fault:
        with pytest.raises(RuntimeError):
            evidence.verify_collection_task(
                task, source, _binding(source, registry)["candidate"], registry
            )
    else:
        verified = evidence.verify_collection_task(
            task, source, _binding(source, registry)["candidate"], registry
        )
        assert set(verified["nodes"]["one"]) == {nodes[0]}
        assert set(verified["nodes"]["host_reboot"]) == evidence.HOST_NODES


@pytest.mark.parametrize(
    "fault",
    [
        None,
        "missing",
        "duplicate",
        "unknown",
        "already-covered",
        "seal",
        "summary-only",
    ],
)
def test_declared_node_remainder_subtracts_only_verified_exact_nodes(
    evidence, tmp_path, inputs, fault
):
    source, registry = inputs
    first = "tests/system/one/test_one.py::test_first"
    second = "tests/system/one/test_one.py::test_second"
    collection = dict(
        nodes={"one": [first, second], "host_reboot": sorted(evidence.HOST_NODES)},
        terminal_sha256="f" * 64,
    )
    previous = dict(domains={"one": 1}, nodes={"one": [first]})
    declared = dict(
        schema_version=1, collection_terminal_sha256="f" * 64, nodes=[second]
    )
    if fault == "missing":
        declared["nodes"] = []
    elif fault == "duplicate":
        declared["nodes"] = [second, second]
    elif fault == "unknown":
        declared["nodes"] = [second, "tests/system/one/test_one.py::test_other"]
    elif fault == "already-covered":
        declared["nodes"] = [first, second]
    elif fault == "seal":
        declared["collection_terminal_sha256"] = "e" * 64
    elif fault == "summary-only":
        previous["nodes"] = {}
    manifest = tmp_path / "selected.json"
    manifest.write_text(json.dumps(declared))
    if fault:
        with pytest.raises(RuntimeError):
            evidence.selected_node_remainder(
                manifest, collection, [previous], [], {"one"}
            )
    else:
        assert evidence.selected_node_remainder(
            manifest, collection, [previous], [], {"one"}
        ) == {"one": [second]}
        assert evidence.accounted_nodes(collection, [previous], [second]) == {
            first,
            second,
        }
        with pytest.raises(RuntimeError, match="duplicate"):
            evidence.accounted_nodes(collection, [previous], [first])


@pytest.mark.parametrize(
    "fault",
    [
        None,
        "count",
        "phase",
        "xfail",
        "native",
        "deselected",
        "collect-executed",
        "duplicate",
    ],
)
def test_actual_node_report_requires_complete_matching_phases(evidence, fault):
    nodes = ["tests/system/one/test_one.py::test_fixture"]
    raw = json.loads(_node_report(nodes))
    native = 0
    collect_only = False
    if fault == "phase":
        raw["phases"][nodes[0]].pop("teardown")
    elif fault == "xfail":
        raw["phases"][nodes[0]]["call"]["wasxfail"] = True
    elif fault == "native":
        native = 1
    elif fault == "deselected":
        raw["deselected"] = 1
    elif fault == "collect-executed":
        raw["collect_only"] = collect_only = True
    elif fault == "duplicate":
        raw["collected"].append(nodes[0])
    outcome = evidence.require_complete_pytest_outcomes(
        _complete_pytest_outcome_report(2 if fault == "count" else 1),
        process_returncode=0,
    )
    if fault:
        with pytest.raises(RuntimeError):
            evidence.require_node_report(
                json.dumps(raw).encode(),
                native=native,
                outcome=outcome,
                collect_only=collect_only,
            )
    else:
        assert (
            evidence.require_node_report(
                json.dumps(raw).encode(), native=0, outcome=outcome
            )
            == nodes
        )


@pytest.mark.parametrize(
    "fault", [None, "candidate", "reachable", "report", "junit", "cleanup"]
)
def test_named_official_carry_reads_original_sealed_proof(
    evidence, monkeypatch, tmp_path, fault
):
    task = tmp_path / "official"
    reports = task / "runtime-reports"
    reports.mkdir(parents=True)
    nodes = [
        "tests/system/kernel/test_kernel.py::TestKernelBuild::test_case_" + str(index)
        for index in range(8)
    ]
    source = dict(payload={"tests/system/kernel/test_kernel.py": "a" * 64})
    candidate = dict(evidence.OFFICIAL_KERNEL_CANDIDATE)
    terminal = dict(
        passed=True,
        failure=None,
        case_native_exit=0,
        fixture_baseline_preserved=True,
        preservation_errors=[],
        acquisition_results=[
            dict(nodes=nodes, typed_kernel_retirements=8, typed_terminal_cache_clean=1)
        ],
    )
    if fault == "junit":
        xml_nodes = nodes[:-1] + [nodes[0]]
    else:
        xml_nodes = nodes
    if fault == "cleanup":
        terminal["acquisition_results"][0]["typed_kernel_retirements"] = 7
    raw = {
        "official-eight.json": _complete_pytest_outcome_report(8),
        "official-eight.xml": _junit(evidence, xml_nodes),
        "terminal.json": json.dumps(terminal).encode(),
    }
    for name, body in raw.items():
        (reports / name).write_bytes(body)
    monkeypatch.setattr(
        evidence,
        "OFFICIAL_KERNEL_SEALS",
        {name: hashlib.sha256(body).hexdigest() for name, body in raw.items()},
    )
    monkeypatch.setattr(evidence, "OFFICIAL_KERNEL_INPUTS", dict(source["payload"]))
    if fault == "candidate":
        candidate["sha256"] = "c" * 64
    elif fault == "reachable":
        source["payload"]["tests/system/kernel/test_kernel.py"] = "c" * 64
    elif fault == "report":
        (reports / "terminal.json").write_bytes(b"{}")
    if fault:
        with pytest.raises(RuntimeError):
            evidence.verify_official_kernel_carry(task, source, candidate)
    else:
        assert evidence.verify_official_kernel_carry(task, source, candidate) == nodes


@pytest.mark.parametrize("mode", ["collect", "selected", "failed"])
def test_node_plugin_records_actual_pytest_items_and_native_outcomes(
    evidence, tmp_path, mode
):
    root = tmp_path / "source"
    file = root / "tests/system/sample/test_sample.py"
    file.parent.mkdir(parents=True)
    file.write_text(
        "def test_first():\n    assert True\ndef test_second():\n    assert False\n"
    )
    reports = tmp_path / "reports"
    reports.mkdir(mode=0o700)
    scripts = Path(__file__).parents[1]
    env = {
        **os.environ,
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTEST_DISABLE_PLUGIN_AUTOLOAD": "1",
        "PYTHONPATH": str(scripts.parent) + ":" + str(scripts),
        evidence.NODE_REPORT_ENV: str(reports / "nodes.json"),
    }
    command = [
        sys.executable,
        "-B",
        "-m",
        "pytest",
        "-p",
        "system_test_evidence",
        "-p",
        "no:cacheprovider",
        "--rootdir=" + str(root),
        "-q",
    ]
    if mode == "collect":
        command.extend(["--collect-only", str(file)])
    else:
        env["MVM_PYTEST_OUTCOME_REPORT"] = str(reports / "outcomes.json")
        command.extend(
            [
                "-p",
                "tests.system.conftest",
                "--junitxml=" + str(reports / "pytest.xml"),
                str(file) + ("::test_first" if mode == "selected" else ""),
            ]
        )
    result = subprocess.run(
        command, env=env, cwd=root, capture_output=True, text=True, timeout=30
    )
    raw = (reports / "nodes.json").read_bytes()
    if mode == "failed":
        assert result.returncode == 1, result.stdout + result.stderr
        assert (
            json.loads(raw)["phases"][
                "tests/system/sample/test_sample.py::test_second"
            ]["call"]["outcome"]
            == "failed"
        )
        with pytest.raises(RuntimeError):
            evidence.require_node_report(raw, native=result.returncode)
    else:
        assert result.returncode == 0, result.stdout + result.stderr
        nodes = evidence.require_node_report(
            raw, native=0, collect_only=mode == "collect"
        )
        assert len(nodes) == (2 if mode == "collect" else 1)
        if mode == "selected":
            strict = evidence.require_complete_pytest_outcomes(
                (reports / "outcomes.json").read_bytes(), process_returncode=0
            )
            assert strict.collected == 1 and nodes == [
                "tests/system/sample/test_sample.py::test_first"
            ]


@pytest.mark.parametrize(
    "fault", [None, "duplicate", "missing-nodes", "junit-identity", "phase"]
)
def test_split_domain_receipts_cover_nodes_without_replaying_whole_file(
    evidence, tmp_path, inputs, fault
):
    source, registry = inputs
    first = "tests/system/one/test_one.py::test_first"
    second = "tests/system/one/test_one.py::test_second"
    reports = []
    for index, node in enumerate((first, first if fault == "duplicate" else second)):
        parent = tmp_path / str(index)
        parent.mkdir()
        matrix, binding = _record(
            evidence,
            parent,
            source,
            registry,
            selection={"one": {**registry["one"], "nodes": [node]}},
        )
        if index == 1 and fault in ("missing-nodes", "junit-identity", "phase"):
            if fault == "missing-nodes":
                (matrix / "one.nodes.json").unlink()
            elif fault == "junit-identity":
                (matrix / "one.xml").write_bytes(_junit(evidence, [first]))
            elif fault == "phase":
                raw = json.loads((matrix / "one.nodes.json").read_bytes())
                raw["phases"][node].pop("call")
                (matrix / "one.nodes.json").write_text(json.dumps(raw))
            # Even a newly sealed terminal cannot turn inconsistent literal data into a pass.
            terminal = json.loads((matrix / "terminal.json").read_bytes())
            terminal["proof_sha256"] = {
                name: hashlib.sha256((matrix / name).read_bytes()).hexdigest()
                for name in terminal["proof_sha256"]
                if (matrix / name).exists()
            }
            (matrix / "terminal.json").write_text(json.dumps(terminal))
            with pytest.raises((RuntimeError, FileNotFoundError)):
                evidence.verify_matrix_task(
                    matrix, source, binding["candidate"], registry
                )
            return
        reports.append(
            evidence.verify_matrix_task(matrix, source, binding["candidate"], registry)
        )
    collection = dict(nodes={"one": [first, second]})
    if fault == "duplicate":
        with pytest.raises(RuntimeError, match="duplicate"):
            evidence.accounted_nodes(collection, reports)
    else:
        assert evidence.accounted_nodes(collection, reports) == {first, second}
        assert [report["domains"] for report in reports] == [{"one": 1}, {"one": 1}]


@pytest.mark.parametrize("runtime_changed", [False, True])
def test_remote_selected_producer_retains_node_reports_and_checked_owned_cleanup(
    evidence, orchestrator, monkeypatch, tmp_path, inputs, runtime_changed
):
    source, registry = inputs
    selected = "tests/system/one/test_one.py::test_fixture"
    source["payload"].update(
        {
            "scripts/system_test_evidence.py": "c" * 64,
            "scripts/system_test_outcomes.py": "d" * 64,
        }
    )
    task = tmp_path / "capture"
    task.mkdir(mode=0o700)
    binding = _binding(source, registry)
    binding["selection"]["one"]["nodes"] = [selected]
    record = evidence.MatrixEvidence(task, binding)
    capture = record.domain("one", 2, [selected])
    calls = []

    def mvm(*argv, **kwargs):
        calls.append(argv)
        command = argv[-1]
        stdout = ""
        if argv[0] == "exec" and "import hashlib,json" in command:
            runtime = dict(source["payload"])
            if runtime_changed:
                runtime["scripts/system_test_evidence.py"] = "0" * 64
            stdout = json.dumps(runtime)
        elif argv[0] == "exec" and "-m pytest" in command:
            assert (
                selected in command
                and "-k " not in command
                and "--deselect" not in command
            )
            stdout = "one actual selected pytest test"
        elif argv[0] == "exec" and command.startswith("head -c"):
            if command.endswith("/nodes.json"):
                stdout = _node_report([selected]).decode()
            elif command.endswith("/pytest.xml"):
                stdout = _junit(evidence, [selected]).decode()
            else:
                stdout = _complete_pytest_outcome_report().decode()
        return subprocess.CompletedProcess(argv, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator, "mvm", mvm)
    if runtime_changed:
        with pytest.raises(RuntimeError, match="runtime source differs"):
            orchestrator._run_remote_pytest(
                "t2-one-fixture", [selected], timeout=60, evidence=capture
            )
        assert not any(call[0] == "exec" and "-m pytest" in call[-1] for call in calls)
        assert capture.report_cleanup is True
        return
    result, failure = orchestrator._run_remote_pytest(
        "t2-one-fixture", [selected], timeout=60, evidence=capture
    )
    assert result.returncode == 0 and failure is None and capture.report_cleanup is True
    assert evidence.require_node_report(
        (task / "one.nodes.json").read_bytes(), native=0
    ) == [selected]
    assert len([call for call in calls if call[0] == "cp"]) == 2
    assert (
        "test ! -e /home/runner/.cache/mvmctl-node-evidence-t2-one-fixture/nodes.json"
        in calls[-1][-1]
    )
    assert any(
        call[0] == "exec"
        and "TMPDIR=/home/runner/.cache/mvmctl-node-evidence-t2-one-fixture/tmp"
        in call[-1]
        for call in calls
    )


def test_collection_operation_observes_items_without_running_fixture_effects(
    evidence, orchestrator, monkeypatch, tmp_path
):
    root = tmp_path / "source"
    file = root / "tests/system/one/test_one.py"
    file.parent.mkdir(parents=True)
    file.write_text(
        "def test_fixture():\n    raise AssertionError('collection must never run this')\n"
    )
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("system_test_evidence.py", "system_test_outcomes.py"):
        (scripts / name).write_bytes((Path(__file__).parents[1] / name).read_bytes())
    source = dict(
        revision="f" * 40,
        tree="e" * 40,
        payload={
            "tests/system/one/test_one.py": hashlib.sha256(
                file.read_bytes()
            ).hexdigest()
        },
    )
    registry = {"one": dict(tier=2, files=["tests/system/one/test_one.py"])}
    binding = _binding(source, registry)
    monkeypatch.chdir(root)
    monkeypatch.setattr(evidence.socket, "gethostname", lambda: "mvmctl-030-test")
    for key in ("MVM_TEST_VM", "MVM_TEST_QUALIFICATION_HOST"):
        monkeypatch.setenv(key, "mvmctl-030-test")
    monkeypatch.setattr(orchestrator, "_REPO_ROOT", root)
    monkeypatch.setattr(
        orchestrator, "verify_release_binary_identity", lambda **_kwargs: None
    )
    monkeypatch.setattr(orchestrator, "_evidence_registry", lambda: registry)
    monkeypatch.setattr(
        orchestrator, "_host_reboot_controller", lambda: binding["controller"]
    )
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0-rc.fixture")
    monkeypatch.setattr(
        orchestrator,
        "_sha256_runner_input",
        lambda _path: binding["candidate"]["sha256"],
    )
    monkeypatch.setattr(evidence, "source_identity", lambda _root: source)

    def forbidden(*_args, **_kwargs):
        raise AssertionError("collection must not run lifecycle effects")

    monkeypatch.setattr(orchestrator, "mvm", forbidden)
    task = tmp_path / "collection"
    task.mkdir(mode=0o700)
    evidence.collect_named_runner_nodes(orchestrator, task)
    verified = evidence.verify_collection_task(
        task, source, binding["candidate"], registry
    )
    assert verified["nodes"] == {"one": ["tests/system/one/test_one.py::test_fixture"]}
