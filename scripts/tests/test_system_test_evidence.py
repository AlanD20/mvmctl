"""Hermetic QA report recording and read-only finalization behavior."""

from __future__ import annotations

import hashlib
import io
import importlib.util
import json
import os
import subprocess
import sys
import tarfile
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


@pytest.mark.parametrize("candidate", [
    {"sha256": "b" * 64, "version": "mvm 0.3.0-rc.kernel-lineage"},
    {"sha256": "507110b052a3cca0e86e54a689b6101e89ab3d4110c9f500c95e943f385e7a2b",
     "version": "mvm 0.3.0-rc.different-version"},
])
def test_rc030_carry_refuses_new_candidate_before_reading_record(evidence, tmp_path, candidate):
    with pytest.raises(RuntimeError, match="candidate507 only"):
        evidence.verify_rc030_candidate507_carry(
            tmp_path / "absent-record.json", {}, candidate, {},
        )


@pytest.mark.parametrize("milestone", ["f50", "f905", "kernel22"])
def test_rc030_source_pin_rejects_changed_python_with_original_revision(
    evidence, monkeypatch, tmp_path, milestone,
):
    revision, tree, _digest = evidence.RC030_SOURCES[milestone]
    source = dict(revision=revision, tree=tree, payload={
        "tests/system/conftest.py": "0" * 64,
    })
    monkeypatch.setattr(evidence, "source_identity", lambda _root: source)
    with pytest.raises(RuntimeError, match="exhaustive Python delta changed"):
        evidence._rc030_source(tmp_path, milestone)


def test_rc030_helper_proof_refuses_caller_resealed_archive(evidence, tmp_path):
    archive = tmp_path / "forged-helper.tar"
    archive.write_bytes(b"caller created helper proof")
    declared = dict(
        archive_path=str(archive),
        archive_sha256=hashlib.sha256(archive.read_bytes()).hexdigest(),
        baselines={},
    )
    with pytest.raises(RuntimeError, match="original/fixed 7\\+59 helper proof changed"):
        evidence._rc030_helper_proof(declared)


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
def rc030_packet(evidence, orchestrator, monkeypatch, tmp_path):
    """Small sealed-report model; actual artifact pins have separate refusal tests."""
    cp = "tests/system/cp/test_cp.py"
    groups = {
        "cp": (cp, 15),
        "snapshot_load": ("tests/system/vm/test_vm_snapshot_load.py", 8),
        "snapshot_rootfs": ("tests/system/vm/test_vm_snapshot_rootfs_independence.py", 6),
        "fresh_env": ("tests/system/vm/test_vm_fresh_env.py", 4),
        "other": ("tests/system/other/test_other.py", 426),
        "images": ("tests/system/images/test_images.py", 44),
        "kernel_build": ("tests/system/kernel/test_kernel.py", 22),
    }
    registry = {name: dict(tier=3, files=[path]) for name, (path, _) in groups.items()}
    registry["host_reboot"] = dict(tier=2, files=[orchestrator._HOST_REBOOT_FILE])
    nodes = {name: [f"{path}::TestFixture::test_case_{index}" for index in range(count)]
             for name, (path, count) in groups.items()}
    nodes["cp"][-1] = cp + "::TestCpMultiSource::test_cp_multi_source_rejects_non_vm_dest"
    nodes["kernel_build"][-1] = (
        "tests/system/kernel/test_kernel.py::TestKernelStoppedVMDeletion::"
        "test_delete_kernel_used_by_stopped_vm_is_denied"
    )
    all_nodes = [node for values in nodes.values() for node in values] + sorted(evidence.HOST_NODES)
    roots, sources = {}, {}
    for milestone, (revision, tree, _) in evidence.RC030_SOURCES.items():
        root = tmp_path / milestone
        root.mkdir(mode=0o700)
        roots[milestone] = root
        payload = {path: "a" * 64 for path in (
            *[path for path, _ in groups.values()], *orchestrator._HOST_REBOOT_SOURCE_FILES,
        )}
        payload["tests/system/conftest.py"] = "b" * 64
        sources[milestone] = dict(revision=revision, tree=tree, payload=payload)
    monkeypatch.setattr(evidence, "_rc030_source", lambda root, milestone: sources[milestone])
    monkeypatch.setattr(evidence, "_rc030_production_inputs", lambda root: {"go.mod": "a" * 64})
    original_binding = _binding

    def binding(source, registered):
        value = original_binding(source, registered)
        value["candidate"] = dict(evidence.RC030_CANDIDATE)
        value["controller"]["sha256"] = evidence.RC030_CANDIDATE["sha256"]
        return value

    monkeypatch.setattr(sys.modules[__name__], "_binding", binding)
    matrices, collections = {}, {}
    selections = {"f50": set(groups) - {"images", "kernel_build"},
                  "f905": {"images"}, "kernel22": {"kernel_build"}}
    for milestone, root in roots.items():
        collections[milestone] = _collection(evidence, root, sources[milestone], registry, nodes=all_nodes)
        selection = {name: {**registry[name], "nodes": nodes[name]} for name in selections[milestone]}
        matrices[milestone], _ = _record(evidence, root, sources[milestone], registry, selection=selection)
    seals = {name: hashlib.sha256((task / "terminal.json").read_bytes()).hexdigest()
             for name, task in matrices.items()}
    monkeypatch.setattr(evidence, "RC030_F50_MATRIX", {seals["f50"]: 459})
    monkeypatch.setattr(evidence, "RC030_IMAGES_TERMINAL", seals["f905"])
    monkeypatch.setattr(evidence, "RC030_KERNEL_TERMINAL", seals["kernel22"])
    monkeypatch.setattr(evidence, "RC030_TARGET_COLLECTION", hashlib.sha256(
        (collections["kernel22"] / "terminal.json").read_bytes()
    ).hexdigest())

    task, envelope, calls, phases, state = _host_reboot_fixture(orchestrator, monkeypatch, roots["f50"])
    monkeypatch.setattr(orchestrator, "_REPO_ROOT", roots["f50"])
    monkeypatch.setattr(orchestrator, "_get_mvm_version",
                        lambda: evidence.RC030_CANDIDATE["version"].removeprefix("mvm "))

    def digest(path):
        path = Path(path)
        if path == Path(orchestrator.MVM_CANDIDATE_BINARY):
            return evidence.RC030_CANDIDATE["sha256"]
        for name, root in roots.items():
            if path.is_relative_to(root):
                return sources[name]["payload"].get(str(path.relative_to(root)), "a" * 64)
        return "a" * 64

    def git(arguments, **kwargs):
        root = Path(arguments[2])
        name = next(name for name, path in roots.items() if path == root)
        return sources[name]["revision"] + "\n" if arguments[-1] == "HEAD" else ""

    monkeypatch.setattr(orchestrator, "_sha256_runner_input", digest)
    monkeypatch.setattr(orchestrator.subprocess, "check_output", git)
    assert orchestrator.run_host_reboot_phase("pre", task, None)["phase_complete"]
    assert orchestrator.run_host_reboot_phase("post", task, envelope)["passed"]
    reboot_seal = hashlib.sha256((task / "post.terminal.json").read_bytes()).hexdigest()
    monkeypatch.setattr(evidence, "RC030_REBOOT_TERMINAL", reboot_seal)
    monkeypatch.setattr(orchestrator, "_REPO_ROOT", roots["kernel22"])
    monkeypatch.setattr(evidence, "source_identity", lambda root: next(
        source for name, source in sources.items() if roots[name] == root
    ))
    monkeypatch.setattr(orchestrator, "_validate_system_test_registry", lambda parser: None)
    monkeypatch.setattr(orchestrator, "_evidence_registry", lambda: registry)

    def forbidden(*args, **kwargs):
        raise AssertionError("carry verification must not query or mutate a runner")

    for name in ("mvm", "_host_reboot_probe", "_host_reboot_outer_identity", "_build_mvm_binary", "run_prepare"):
        monkeypatch.setattr(orchestrator, name, forbidden)
    before = roots["f50"] / "before.json"
    before.write_text(json.dumps({"public": {"config": {"settings": {"guestfs_enabled": {
        "default": False, "override": False, "type": "bool",
    }}}}}))
    before_seal = hashlib.sha256(before.read_bytes()).hexdigest()
    monkeypatch.setattr(evidence, "RC030_FALSE_BASELINES", {seals["f50"]: before_seal})
    stream = io.BytesIO()
    with tarfile.open(fileobj=stream, mode="w") as archive:
        members = {"reports/original-regression.json": b'{"native_exit":1}'}
        for label, count in (("fixed-focused", 7), ("affected-owner", 59)):
            members[f"reports/{label}.json"] = b'{"native_exit":0}'
            members[f"reports/{label}.xml"] = _junit(evidence, [
                f"tests/system/proof/test_proof.py::TestProof::test_{index}" for index in range(count)
            ])
        for name, raw in members.items():
            info = tarfile.TarInfo(name)
            info.size = len(raw)
            archive.addfile(info, io.BytesIO(raw))
    helper = tmp_path / "helper.tar"
    helper.write_bytes(stream.getvalue())
    helper_seal = hashlib.sha256(helper.read_bytes()).hexdigest()
    monkeypatch.setattr(evidence, "RC030_HELPER_ARCHIVE", helper_seal)
    declaration = dict(
        schema_version=1, profile="rc030-candidate507-oct14",
        target=dict(source_path=str(roots["kernel22"]), collection_task=str(collections["kernel22"]),
                    terminal_sha256=hashlib.sha256((collections["kernel22"] / "terminal.json").read_bytes()).hexdigest()),
        origins={name: dict(source_path=str(root), collection_task=str(collections[name]),
                            matrix_tasks=[dict(path=str(matrices[name]), terminal_sha256=seals[name])])
                 for name, root in roots.items()},
        delta_proof=dict(archive_path=str(helper), archive_sha256=helper_seal,
                         baselines={seals["f50"]: dict(path=str(before), sha256=before_seal)}),
    )
    declaration["origins"]["f50"].update(reboot_task=str(task), post_terminal_sha256=reboot_seal)
    record = tmp_path / "carry.json"
    record.write_text(json.dumps(declaration))
    original_paths = [task / "task.json", task / "post.terminal.json",
                      *[matrix / "binding.json" for matrix in matrices.values()]]
    return dict(record=record, declaration=declaration, matrices=matrices, collections=collections,
                roots=roots, sources=sources, registry=registry, reboot=task, before=before,
                original={path: path.read_bytes() for path in original_paths},
                calls=calls, phases=phases, nodes=nodes, state=state)


def test_rc030_complete_join_keeps_original_bindings_and_provenance(evidence, orchestrator, rc030_packet):
    packet = rc030_packet
    calls = list(packet["calls"])
    result = evidence.finalize_named_runner_release_evidence(
        orchestrator, [], packet["reboot"], collection_task=packet["collections"]["kernel22"],
        rc030_source_carry=packet["record"],
    )
    assert result["passed"] and len(result["carried_nodes"]) == 503 and len(result["executed_nodes"]) == 22
    assert result["qa_source_carry"]["affected_nodes"] == []
    assert {entry["source"]["revision"] for entry in result["qa_source_carry"]["provenance"]} == {
        source["revision"] for source in packet["sources"].values()
    }
    assert result["host_reboot"]["original_source"] == packet["sources"]["f50"]
    assert packet["calls"] == calls and packet["phases"] == ["pre", "post"]
    assert {path: path.read_bytes() for path in packet["original"]} == packet["original"]


@pytest.mark.parametrize("pending", ["kernel22", "helper"])
def test_rc030_pending_proof_withholds_exact32_and_selected_remainder(
    evidence, rc030_packet, pending,
):
    packet = rc030_packet
    declared = packet["declaration"]
    if pending == "kernel22":
        declared["origins"]["kernel22"]["matrix_tasks"] = []
    else:
        declared["delta_proof"] = None
    packet["record"].write_text(json.dumps(declared))
    checked = evidence.verify_rc030_candidate507_carry(
        packet["record"], packet["sources"]["kernel22"], evidence.RC030_CANDIDATE, packet["registry"],
    )
    assert len(checked.affected_nodes) == 32 and len(checked.carried_nodes) == 471
    assert not checked.reboot_admissible
    selection = packet["record"].parent / "selected.json"
    expected = packet["nodes"]["kernel_build"] if pending == "kernel22" else list(checked.affected_nodes)
    domains = {"kernel_build"} if pending == "kernel22" else {"cp", "snapshot_load", "snapshot_rootfs", "fresh_env"}
    selection.write_text(json.dumps(dict(schema_version=1,
        collection_terminal_sha256=checked.collection["terminal_sha256"], nodes=expected)))
    selected = evidence.selected_node_remainder(
        selection, checked.collection, list(checked.target_reports), list(checked.carried_nodes), domains,
    )
    assert {node for values in selected.values() for node in values} == set(expected)


def test_rc030_selected_driver_records_only_unproved_kernel22(
    evidence, orchestrator, monkeypatch, rc030_packet,
):
    packet = rc030_packet
    packet["declaration"]["origins"]["kernel22"]["matrix_tasks"] = []
    packet["record"].write_text(json.dumps(packet["declaration"]))
    selection = packet["record"].parent / "selected.json"
    selection.write_text(json.dumps(dict(schema_version=1,
        collection_terminal_sha256=packet["declaration"]["target"]["terminal_sha256"],
        nodes=packet["nodes"]["kernel_build"])))
    task = packet["record"].parent / "selected-matrix"
    task.mkdir(mode=0o700)
    args = orchestrator._build_parser().parse_args([
        "kernel_build", "--host-direct", "--record-evidence", str(task),
        "--collection-task", str(packet["collections"]["kernel22"]),
        "--selected-node-file", str(selection), "--rc030-qa-source-carry", str(packet["record"]),
    ])
    monkeypatch.setattr(orchestrator, "source_identity", evidence.source_identity)
    monkeypatch.setattr(orchestrator, "verify_release_binary_identity", lambda **kwargs: None)
    monkeypatch.setattr(orchestrator, "_require_release_host_without_active_vms", lambda: None)
    capture = orchestrator._start_matrix_evidence(task, args)
    assert capture.binding["selection"] == {
        "kernel_build": {**packet["registry"]["kernel_build"], "nodes": packet["nodes"]["kernel_build"]},
    }
    assert capture.binding["source"] == packet["sources"]["kernel22"]


@pytest.mark.parametrize("changed", [
    "tests/system/conftest.py",
    "tests/system/network/test_network.py",
])
def test_rc030_reboot_carry_refuses_changed_shared_inputs(evidence, orchestrator, rc030_packet, changed):
    packet = rc030_packet
    packet["sources"]["kernel22"]["payload"][changed] = "d" * 64
    with pytest.raises(RuntimeError, match="reboot assertions or staging inputs changed") as error:
        orchestrator.require_completed_rc030_candidate507_host_reboot(
            packet["reboot"], packet["roots"]["f50"],
        )
    assert str(error.value).endswith(changed)


@pytest.mark.parametrize("fault", ["controller", "original-revision"])
def test_rc030_historical_reboot_reader_keeps_original_authority_checks(
    evidence, orchestrator, monkeypatch, rc030_packet, fault,
):
    packet = rc030_packet
    if fault == "controller":
        packet["state"]["controller"]["identity"][1] += 1
    else:
        monkeypatch.setattr(orchestrator.subprocess, "check_output",
                            lambda arguments, **kwargs: "0" * 40 + "\n" if arguments[-1] == "HEAD" else "")
    with pytest.raises(RuntimeError, match="PRE binding"):
        orchestrator.require_completed_rc030_candidate507_host_reboot(
            packet["reboot"], packet["roots"]["f50"],
        )


@pytest.mark.parametrize("fault", ["production", "kernel-teardown", "False-baseline", "retarget", "missing22"])
def test_rc030_join_refuses_unproved_or_changed_inputs(
    evidence, orchestrator, monkeypatch, rc030_packet, fault,
):
    packet = rc030_packet
    if fault == "production":
        monkeypatch.setattr(evidence, "_rc030_production_inputs", lambda root: {
            "go.mod": "b" * 64 if root == packet["roots"]["f905"] else "a" * 64,
        })
    elif fault == "kernel-teardown":
        path = packet["matrices"]["kernel22"] / "kernel_build.nodes.json"
        report = json.loads(path.read_bytes())
        next(iter(report["phases"].values()))["teardown"]["outcome"] = "failed"
        path.write_text(json.dumps(report))
    elif fault == "False-baseline":
        packet["before"].write_text('{"public":{"config":{"settings":{"guestfs_enabled":{"override":null}}}}}')
    elif fault == "retarget":
        path = packet["matrices"]["f50"] / "binding.json"
        binding = json.loads(path.read_bytes())
        binding["source"] = packet["sources"]["kernel22"]
        path.write_text(json.dumps(binding))
    else:
        packet["declaration"]["origins"]["kernel22"]["matrix_tasks"] = []
        packet["record"].write_text(json.dumps(packet["declaration"]))
    with pytest.raises(RuntimeError):
        evidence.finalize_named_runner_release_evidence(
            orchestrator, [], packet["reboot"], collection_task=packet["collections"]["kernel22"],
            rc030_source_carry=packet["record"],
        )


@pytest.fixture
def candidate565_packet(evidence, monkeypatch, tmp_path):
    """Real sealed reports model the closed 344+34+118+8+22 ownership split.

    Only artifact constants/source observations are substituted. The original
    strict matrix/collection readers and exact-node accounting remain real.
    """
    counts = dict(original=344, startup=34, remaining=118, nested=8, kernel=22)
    domains = {
        name: "kernel_build" if name == "kernel" else "ordinary" for name in counts
    }
    registry = {
        domain: dict(tier=3, files=[f"tests/system/{domain}/test_{domain}.py"])
        for domain in domains.values()
    }
    registry["host_reboot"] = dict(
        tier=2, files=["tests/system/host/test_host_reboot.py"]
    )
    nodes = {
        name: [
            f"{registry[domain]['files'][0]}::test_{name}_{i}"
            for i in range(counts[name])
        ]
        for name, domain in domains.items()
    }
    all_nodes = [node for values in nodes.values() for node in values] + sorted(
        evidence.HOST_NODES
    )
    roots, sources, matrices, collections = {}, {}, {}, {}
    original_binding = _binding

    def binding(source, registered):
        value = original_binding(source, registered)
        value["candidate"] = dict(evidence.CANDIDATE565)
        value["controller"]["sha256"] = evidence.CANDIDATE565["sha256"]
        return value

    monkeypatch.setattr(sys.modules[__name__], "_binding", binding)
    for name, (revision, tree, _) in evidence.CANDIDATE565_SOURCES.items():
        root = tmp_path / name
        root.mkdir(mode=0o700)
        roots[name] = root
        sources[name] = dict(
            revision=revision,
            tree=tree,
            payload={
                path: "a" * 64 for row in registry.values() for path in row["files"]
            },
        )
        if name == "release_docs":
            continue
        collections[name] = _collection(
            evidence, root, sources[name], registry, nodes=all_nodes
        )
        domain = domains[name]
        matrices[name], _ = _record(
            evidence,
            root,
            sources[name],
            registry,
            selection={
                domain: {**registry[domain], "nodes": nodes[name]},
            },
        )
    seals = {
        name: hashlib.sha256((task / "terminal.json").read_bytes()).hexdigest()
        for name, task in matrices.items()
    }
    monkeypatch.setattr(
        evidence,
        "CANDIDATE565_COLLECTIONS",
        {
            name: hashlib.sha256((task / "terminal.json").read_bytes()).hexdigest()
            for name, task in collections.items()
        },
    )
    monkeypatch.setattr(
        evidence,
        "CANDIDATE565_ACCEPTED",
        {name: {seals[name]: counts[name]} for name in counts if name != "kernel"},
    )
    monkeypatch.setattr(
        evidence, "_candidate565_source", lambda root, name: sources[name]
    )
    monkeypatch.setattr(
        evidence, "_rc030_production_inputs", lambda root: {"go.mod": "a" * 64}
    )
    monkeypatch.setattr(
        evidence, "_candidate565_relevant_equalities", lambda roots: None
    )
    # Git bytes are an external observation, not the admission policy under test.
    monkeypatch.setattr(
        evidence,
        "CANDIDATE565_DELTA",
        (("original", "kernel", hashlib.sha256(b"reviewed delta").hexdigest()),),
    )
    monkeypatch.setattr(
        evidence.subprocess, "check_output", lambda *args, **kwargs: b"reviewed delta"
    )
    declared = dict(
        schema_version=1,
        profile=evidence.CANDIDATE565_PROFILE,
        release_source_path=str(roots["release_docs"]),
        sources={
            name: dict(
                source_path=str(roots[name]),
                collection_task=str(collections[name]),
                matrix_tasks=[
                    dict(path=str(matrices[name]), terminal_sha256=seals[name])
                ],
            )
            for name in counts
        },
    )
    record = tmp_path / "candidate565.json"
    record.write_text(json.dumps(declared))
    return dict(
        record=record,
        declared=declared,
        roots=roots,
        sources=sources,
        matrices=matrices,
        collections=collections,
        registry=registry,
        nodes=nodes,
    )


def test_candidate565_join_preserves_original_receipts_and_node_provenance(
    evidence, candidate565_packet
):
    packet = candidate565_packet
    files = {
        path: path.read_bytes()
        for task in packet["matrices"].values()
        for path in task.iterdir()
    }
    joined = evidence.verify_candidate565_join(
        packet["record"],
        packet["sources"]["kernel"],
        evidence.CANDIDATE565,
        packet["registry"],
    )
    assert {
        entry["source"]["revision"]: set(entry["nodes"])
        for entry in joined["provenance"]
    } == {
        packet["sources"][name]["revision"]: set(nodes)
        for name, nodes in packet["nodes"].items()
    }
    assert {
        entry["source"]["revision"]: entry["classification"]
        for entry in joined["provenance"]
    } == {
        packet["sources"][name]["revision"]: "executed-at-target"
        if name == "kernel"
        else "checked-original-source-carry"
        for name in packet["nodes"]
    }
    assert {path: path.read_bytes() for path in files} == files


@pytest.mark.parametrize("fault", ["overlap", "outside-collection"])
def test_candidate565_join_refuses_resealed_overlap_or_gap(
    evidence, candidate565_packet, tmp_path, fault
):
    packet = candidate565_packet
    nodes = list(packet["nodes"]["startup"])
    nodes[0] = (
        packet["nodes"]["original"][0]
        if fault == "overlap"
        else "tests/system/ordinary/test_ordinary.py::test_uncollected"
    )
    fresh = tmp_path / "resealed"
    fresh.mkdir(mode=0o700)
    task, _ = _record(
        evidence,
        fresh,
        packet["sources"]["startup"],
        packet["registry"],
        selection={
            "ordinary": {**packet["registry"]["ordinary"], "nodes": nodes},
        },
    )
    seal = hashlib.sha256((task / "terminal.json").read_bytes()).hexdigest()
    # A new all-pass, internally sealed receipt still cannot double-credit a
    # node or fill an unproved registered node with a different node.
    evidence.CANDIDATE565_ACCEPTED["startup"] = {seal: 34}
    packet["declared"]["sources"]["startup"]["matrix_tasks"] = [
        dict(path=str(task), terminal_sha256=seal)
    ]
    packet["record"].write_text(json.dumps(packet["declared"]))
    reason = "duplicate executed" if fault == "overlap" else "outside full registered"
    with pytest.raises(RuntimeError, match=reason):
        evidence.verify_candidate565_join(
            packet["record"],
            packet["sources"]["kernel"],
            evidence.CANDIDATE565,
            packet["registry"],
        )


@pytest.mark.parametrize(
    "fault",
    [
        "missing-kernel",
        "failed-teardown",
        "retarget",
        "seal",
        "registry",
        "delta",
        "production",
    ],
)
def test_candidate565_join_rejects_incomplete_changed_or_unproved_inputs(
    evidence,
    candidate565_packet,
    monkeypatch,
    fault,
):
    packet = candidate565_packet
    if fault == "missing-kernel":
        packet["declared"]["sources"]["kernel"]["matrix_tasks"] = []
        packet["record"].write_text(json.dumps(packet["declared"]))
    elif fault == "failed-teardown":
        path = packet["matrices"]["kernel"] / "kernel_build.nodes.json"
        value = json.loads(path.read_bytes())
        next(iter(value["phases"].values()))["teardown"]["outcome"] = "failed"
        path.write_text(json.dumps(value))
    elif fault == "retarget":
        path = packet["matrices"]["original"] / "binding.json"
        value = json.loads(path.read_bytes())
        value["source"] = packet["sources"]["kernel"]
        path.write_text(json.dumps(value))
    elif fault == "seal":
        packet["declared"]["sources"]["original"]["matrix_tasks"][0][
            "terminal_sha256"
        ] = "0" * 64
        packet["record"].write_text(json.dumps(packet["declared"]))
    elif fault == "registry":
        packet["registry"]["unproved"] = dict(
            tier=1, files=["tests/system/new/test_new.py"]
        )
    elif fault == "delta":
        monkeypatch.setattr(
            evidence.subprocess,
            "check_output",
            lambda *args, **kwargs: b"different delta",
        )
    else:
        monkeypatch.setattr(
            evidence,
            "_rc030_production_inputs",
            lambda root: {
                "go.mod": "b" * 64 if root == packet["roots"]["startup"] else "a" * 64,
            },
        )
    with pytest.raises(RuntimeError):
        evidence.verify_candidate565_join(
            packet["record"],
            packet["sources"]["kernel"],
            evidence.CANDIDATE565,
            packet["registry"],
        )


@pytest.mark.parametrize(
    "milestone",
    ["original", "startup", "remaining", "nested", "kernel", "release_docs"],
)
def test_candidate565_exact_source_rejects_changed_shared_payload(
    evidence, monkeypatch, tmp_path, milestone
):
    revision, tree, _ = evidence.CANDIDATE565_SOURCES[milestone]
    monkeypatch.setattr(
        evidence,
        "source_identity",
        lambda root: dict(
            revision=revision,
            tree=tree,
            payload={"tests/system/conftest.py": "0" * 64},
        ),
    )
    with pytest.raises(RuntimeError, match="exact source/tree/payload changed"):
        evidence._candidate565_source(tmp_path, milestone)


@pytest.mark.parametrize("fault", [None, "helper-body", "module-initialization"])
def test_candidate565_shared_seams_check_helpers_and_module_initialization(
    evidence, monkeypatch, tmp_path, fault
):
    path = "test_shared.py"
    roots = {}
    original = "registry = 1\ndef helper():\n    return 2\ndef other():\n    return 3\n"
    for name in ("original", "startup", "remaining", "nested", "kernel"):
        root = tmp_path / name
        root.mkdir(mode=0o700)
        roots[name] = root
        body = original
        if name == "remaining":
            if fault == "helper-body":
                body = body.replace("return 2", "return 4")
            elif fault == "module-initialization":
                body = body.replace("registry = 1", "registry = 4")
            else:
                body = body.replace("return 3", "return 4")
        (root / path).write_text(body)
    monkeypatch.setattr(evidence, "CANDIDATE565_EQUAL_HELPERS", {path: ("helper",)})
    monkeypatch.setattr(evidence, "CANDIDATE565_BODY_ONLY_MODULES", (path,))
    if fault is None:
        evidence._candidate565_relevant_equalities(roots)
    else:
        with pytest.raises(
            RuntimeError, match="reviewed shared helper|reviewed imports/registration"
        ):
            evidence._candidate565_relevant_equalities(roots)


@pytest.mark.parametrize("reboot_complete", [False, True])
def test_candidate565_finalizer_requires_reboot_closure_and_reports_original_sources(
    evidence,
    orchestrator,
    monkeypatch,
    candidate565_packet,
    reboot_complete,
):
    packet = candidate565_packet
    source = packet["sources"]["kernel"]
    monkeypatch.setattr(orchestrator, "_REPO_ROOT", packet["roots"]["kernel"])
    monkeypatch.setattr(
        orchestrator, "_validate_system_test_registry", lambda parser: None
    )
    monkeypatch.setattr(orchestrator, "_evidence_registry", lambda: packet["registry"])
    monkeypatch.setattr(
        orchestrator, "_get_mvm_version", lambda: evidence.CANDIDATE565["version"][4:]
    )
    monkeypatch.setattr(
        orchestrator,
        "_sha256_runner_input",
        lambda path: evidence.CANDIDATE565["sha256"],
    )
    monkeypatch.setattr(evidence, "source_identity", lambda root: source)
    reboot_task = packet["record"].parent / "fresh-reboot"

    def original_reboot_reader(task, root):
        assert task == reboot_task and root == packet["roots"]["kernel"]
        if not reboot_complete:
            raise RuntimeError(
                "host_reboot release proof lacks exact completed native closure"
            )
        return dict(passed=True)

    monkeypatch.setattr(
        orchestrator,
        "_require_completed_host_reboot_for_source",
        original_reboot_reader,
    )
    if not reboot_complete:
        with pytest.raises(RuntimeError, match="completed native closure"):
            evidence.finalize_named_runner_release_evidence(
                orchestrator,
                [],
                reboot_task,
                collection_task=packet["collections"]["kernel"],
                candidate565_source_join=packet["record"],
            )
        return
    result = evidence.finalize_named_runner_release_evidence(
        orchestrator,
        [],
        reboot_task,
        collection_task=packet["collections"]["kernel"],
        candidate565_source_join=packet["record"],
    )
    assert result["passed"] is True
    assert set(result["executed_nodes"]) == set(packet["nodes"]["kernel"])
    assert set(result["carried_nodes"]) == {
        node
        for name, nodes in packet["nodes"].items()
        if name != "kernel"
        for node in nodes
    }
    assert result["host_reboot"]["nodes"] == sorted(evidence.HOST_NODES)
    assert result["host_reboot"]["original_source"] == source
    assert {
        entry["source"]["revision"] for entry in result["qa_source_join"]["provenance"]
    } == {packet["sources"][name]["revision"] for name in packet["nodes"]}


def test_candidate565_join_refuses_other_candidate_before_reading_record(
    evidence, tmp_path
):
    with pytest.raises(RuntimeError, match="exact binary and version"):
        evidence.verify_candidate565_join(
            tmp_path / "absent.json", {}, evidence.RC030_CANDIDATE, {}
        )


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
