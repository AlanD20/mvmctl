from __future__ import annotations

import argparse
import ast
import concurrent.futures
import hashlib
import importlib.util
import json
import os
import select
import shlex
import shutil
import signal
import struct
import subprocess
import sys
import threading
import time
from collections.abc import Callable
from pathlib import Path
from types import ModuleType, SimpleNamespace

import pytest


@pytest.fixture
def orchestrator(monkeypatch: pytest.MonkeyPatch) -> ModuleType:
    script = Path(__file__).parents[1] / "run-system-tests.py"
    monkeypatch.syspath_prepend(str(script.parent))
    module_name = "mvmctl_run_system_tests"
    spec = importlib.util.spec_from_file_location(module_name, script)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


def _assert_selection_rejected_before_side_effects(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
    expected_error: str,
) -> None:
    calls: list[str] = []

    def unexpected(name: str):
        def fail(*_args: object, **_kwargs: object) -> None:
            calls.append(name)
            raise AssertionError(f"{name} must not run for an invalid selection")

        return fail

    for helper in (
        "validate_release_build_paths",
        "_resolve_candidate_build_version",
        "_build_mvm_binary",
        "verify_release_binary_identity",
        "_require_release_host_without_active_vms",
        "_require_distinct_candidate_controller",
        "_get_mvm_version",
        "run_prepare",
        "ensure_shared_volume",
        "ensure_test_network",
    ):
        monkeypatch.setattr(orchestrator, helper, unexpected(helper), raising=False)
    monkeypatch.setattr(orchestrator.shutil, "which", unexpected("which"))
    monkeypatch.setattr(orchestrator.sys, "argv", ["run-system-tests.py", *selection])

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 2
    assert calls == []
    assert expected_error in capsys.readouterr().err


def _configure_test_registry(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    repo_root: Path,
    tier1: dict[str, list[str]],
    tier2: dict[str, list[str]] | None = None,
    tier3: dict[str, list[str]] | None = None,
) -> None:
    monkeypatch.setattr(orchestrator, "_REPO_ROOT", repo_root)
    monkeypatch.setattr(orchestrator, "TIER1_DOMAINS", tier1)
    monkeypatch.setattr(orchestrator, "TIER2_DOMAINS", tier2 or {})
    monkeypatch.setattr(orchestrator, "TIER3_DOMAINS", tier3 or {})
    monkeypatch.setattr(orchestrator, "HOST_REBOOT_PHASE_DOMAINS", {})


def _write_system_test(repo_root: Path, relative_path: str) -> None:
    test_file = repo_root / relative_path
    test_file.parent.mkdir(parents=True, exist_ok=True)
    test_file.write_text("def test_registered():\n    pass\n", encoding="utf-8")


def _complete_pytest_outcome_report(collected: int = 1) -> bytes:
    return json.dumps(
        {
            "schema_version": 1,
            "collected": collected,
            "passed": collected,
            "failed": 0,
            "errors": 0,
            "collection_errors": 0,
            "deselected": 0,
            "skipped": 0,
            "xfailed": 0,
            "xpassed": 0,
            "exit_status": 0,
        }
    ).encode()


def _stub_release_host_vm_list(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    *,
    stdout: str | None,
    returncode: int = 0,
    data_payload: bool = False,
) -> list[tuple[tuple[str, ...], dict[str, object]]]:
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []
    monkeypatch.setattr(orchestrator, "MVM_BINARY", "/usr/local/bin/mvm")
    if data_payload and stdout is not None:
        stdout = (
            '{"schema_version":1,"status":"success","data":'
            + stdout + ',"error":null}'
        )

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(
            args,
            returncode,
            stdout=stdout,
            stderr="synthetic stderr" if returncode else "",
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    return calls


def test_default_outer_controller_is_the_canonical_system_binary(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("MVM_BINARY", raising=False)
    script = Path(__file__).parents[1] / "run-system-tests.py"
    monkeypatch.syspath_prepend(str(script.parent))
    module_name = "mvmctl_run_system_tests_default_controller"
    spec = importlib.util.spec_from_file_location(module_name, script)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, module_name, module)

    spec.loader.exec_module(module)

    assert module.MVM_BINARY == "/usr/local/bin/mvm"


def _host_reboot_fixture(orchestrator, monkeypatch, tmp_path):
    """Closed phase simulator: lifecycle is recorded, never executed."""
    task = tmp_path / "host-reboot"
    task.mkdir(mode=0o700)
    before_boot, after_boot = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
    outer = dict(runner_id="1" * 32, created_at="2026-10-10T00:00:00Z", pid=123, start_ticks=456,
                 guest_mem_size_mib=6144, memory_high="8589934592", memory_max="8589934592")
    calls, phases = [], []
    controller = dict(path="/home/www/.local/bin/mvm", command=["/home/www/.local/bin/mvm"],
                      identity=[1, 2, 1000, 1000, 0o755, 1, 21586222, 3, 4], sha256="c" * 64)
    state = dict(pre=None, cleanup=True, outer=outer, probes=0, controller=controller)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", controller["path"])
    monkeypatch.setattr(orchestrator, "_host_reboot_controller", lambda: json.loads(json.dumps(state["controller"])))
    monkeypatch.setattr(orchestrator, "_require_distinct_candidate_controller", lambda: None)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_ID", "2" * 64)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_SHA256", "3" * 64)
    monkeypatch.setattr(orchestrator, "_sha256_runner_input", lambda path: "a" * 64)
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0-rc.fixture")
    def git(args, **kwargs):
        return "f" * 40 + "\n" if args[-1] == "HEAD" else ""
    monkeypatch.setattr(orchestrator.subprocess, "check_output", git)
    monkeypatch.setattr(orchestrator, "_host_reboot_outer_identity", lambda: dict(state["outer"]))
    def probe(bound):
        state["probes"] += 1
        return before_boot if state["probes"] == 1 else after_boot
    monkeypatch.setattr(orchestrator, "_host_reboot_probe", probe)
    def mvm(*args, **kwargs):
        calls.append(args)
        if args[0] == "ssh" and "sha256sum --" in args[-1]:
            paths = shlex.split(args[-1])[2:]
            lines = []
            for path in paths:
                relative = path.split("/source/", 1)[1]
                digest = ("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
                          if state.get("source_lost") and relative == orchestrator._HOST_REBOOT_FILE
                          else orchestrator._sha256_runner_input(orchestrator._REPO_ROOT / relative))
                lines.append(f"{digest}  {path}")
            return subprocess.CompletedProcess(args, 0, stdout="\n".join(lines) + "\n", stderr="")
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")
    monkeypatch.setattr(orchestrator, "mvm", mvm)
    monkeypatch.setattr(orchestrator, "_host_reboot_stage_source", lambda bound: None)
    def phase(bound, selected, directory):
        phases.append(selected)
        physical = {key: [] for key in ("image", "kernel", "bin", "volume", "key", "snapshot", "config", "traffic", "exec")}
        if selected == "pre":
            receipt = dict(boot_id=before_boot, source=dict(revision=bound["source_revision"],
                test_sha256=bound["test_sha256"], payload=bound["source_payload"]),
                candidate=dict(sha256=bound["candidate_sha256"], version=bound["candidate_version"]), vm=dict(id="4" * 32), physical=physical)
            raw = json.dumps(receipt).encode()
            state["pre"] = raw
        else:
            pre = json.loads(state["pre"])
            raw = json.dumps(dict(schema_version=1, host_boot_id=after_boot, owned_cleanup=state["cleanup"],
                vm_id=pre["vm"]["id"], source=pre["source"], candidate=pre["candidate"],
                physical_before=physical, physical_after_boot=physical, physical_after=physical)).encode()
        orchestrator._host_reboot_write(directory, f"{selected}.json", raw)
        orchestrator._host_reboot_write(directory, f"{selected}.outcomes.json", _complete_pytest_outcome_report())
        for suffix, data in (("stdout", b"literal pytest"), ("stderr", b""),
            ("native.json", json.dumps(dict(phase=selected, returncode=0)).encode()),
            ("xml", f'<testsuites><testsuite tests="1" failures="0" errors="0" skipped="0"><testcase classname="tests.system.host.test_host_reboot" name="test_host_reboot_{selected}"/></testsuite></testsuites>'.encode())):
            orchestrator._host_reboot_write(directory, f"{selected}.{suffix}", data)
        return raw
    monkeypatch.setattr(orchestrator, "_host_reboot_pytest", phase)
    def fetch(bound, name):
        if name == "preservation.json":
            pre = json.loads(state["pre"])
            return json.dumps(dict(before=pre["physical"], after_boot=pre["physical"], final=pre["physical"])).encode()
        return state["pre"]
    monkeypatch.setattr(orchestrator, "_host_reboot_fetch", fetch)
    envelope = tmp_path / "envelope.json"
    payload = dict(schema_version=1, runner="mvmctl-030-test", runner_id=outer["runner_id"], host_boot_id=after_boot,
                   outer_pid=outer["pid"], outer_start_ticks=outer["start_ticks"], memory_high=8589934592,
                   memory_max=8589934592, guest_mem_size_mib=6144)
    envelope.write_text(json.dumps(payload))
    envelope.chmod(0o600)
    return task, envelope, calls, phases, state


@pytest.mark.parametrize("memory_high,memory_max", [
    (8589934592, 8589934592), (6576668672, 6576668672),
    (6576668672, "max"), ("max", 6576668672), ("max", "max"),
], ids=["retained-8g", "observed-6.125g", "observed-max-limit", "observed-max-high", "observed-both-max"])
def test_host_reboot_pre_retains_proof_but_never_claims_journey_success(
    orchestrator, monkeypatch, tmp_path, memory_high, memory_max,
):
    stage_source = orchestrator._host_reboot_stage_source
    source_hash = orchestrator._sha256_runner_input
    task, envelope, calls, phases, state = _host_reboot_fixture(orchestrator, monkeypatch, tmp_path)
    monkeypatch.setattr(orchestrator, "_host_reboot_stage_source", stage_source)
    monkeypatch.setattr(orchestrator, "_sha256_runner_input",
                        lambda path: source_hash(path) if path.is_file() else "a" * 64)
    pre = orchestrator.run_host_reboot_phase("pre", task, None)
    assert pre["phase_complete"] is True and pre["passed"] is False and pre["release_verdict"] == "INCOMPLETE"
    assert phases == ["pre"]
    assert [call for call in calls if call[:2] == ("vm", "reboot")] == [("vm", "reboot", "mvmctl-030-test", "--force")]
    assert all(call[:2] not in (("vm", "create"), ("vm", "rm"), ("host", "reset")) for call in calls)
    assert (task / "pre.json").read_bytes() == state["pre"]
    assert json.loads((task / "task.json").read_bytes())["after_boot"] != json.loads(state["pre"])["boot_id"]
    assert json.loads((task / "task.json").read_bytes())["controller"] == state["controller"]
    # POST accepts truthful current controls, not a required setup value or the PRE envelope.
    state["outer"].update(memory_high=str(memory_high), memory_max=str(memory_max))
    payload = json.loads(envelope.read_bytes())
    payload.update(memory_high=memory_high, memory_max=memory_max)
    envelope.write_text(json.dumps(payload))
    post = orchestrator.run_host_reboot_phase("post", task, envelope)
    assert post["passed"] is True and post["release_verdict"] == "PASS" and phases == ["pre", "post"]
    assert (task / "admin-envelope.json").read_bytes() == envelope.read_bytes()
    assert len([call for call in calls if call[:2] == ("vm", "reboot")]) == 1
    def no_live_query(*args, **kwargs):
        raise AssertionError("historical full-release input cannot query runner liveness")
    monkeypatch.setattr(orchestrator, "mvm", no_live_query)
    monkeypatch.setattr(orchestrator, "_host_reboot_probe", no_live_query)
    monkeypatch.setattr(orchestrator, "_host_reboot_outer_identity", no_live_query)
    assert orchestrator.require_completed_host_reboot(task)["passed"] is True
    assert phases == ["pre", "post"] and len([call for call in calls if call[:2] == ("vm", "reboot")]) == 1
    # Explicit POST execution still requires live authority, unlike receipt consumption.
    monkeypatch.setattr(orchestrator, "_host_reboot_probe", lambda bound: json.loads((task / "task.json").read_bytes())["after_boot"])
    monkeypatch.setattr(orchestrator, "_host_reboot_outer_identity", lambda: dict(state["outer"]))
    replay = orchestrator.run_host_reboot_phase("post", task, envelope)
    assert replay["passed"] is False and phases == ["pre", "post"]


@pytest.mark.parametrize("field,value", [("host_boot_id", "11111111-1111-4111-8111-111111111111"),
    ("outer_pid", 124), ("runner_id", "9" * 32), ("memory_high", 6576668672),
    ("memory_max", 6576668672), ("schema_version", True),
    ("memory_high", "max"), ("memory_max", "max"),
    ("memory_high", "8589934592"), ("memory_max", "8589934592"),
    ("memory_high", True), ("memory_max", True),
    ("memory_high", 8589934592.0), ("memory_max", 8589934592.0),
    ("memory_max", "MAX"), ("memory_max", None), ("unexpected", 1),
    (None, "memory_high"), (None, "memory_max")])
def test_host_reboot_post_refuses_wrong_external_admin_authority_before_body(
    orchestrator, monkeypatch, tmp_path, field, value,
):
    task, envelope, calls, phases, _ = _host_reboot_fixture(orchestrator, monkeypatch, tmp_path)
    assert orchestrator.run_host_reboot_phase("pre", task, None)["phase_complete"]
    payload = json.loads(envelope.read_bytes())
    if field is None:
        payload.pop(value)
    else:
        payload[field] = value
    envelope.write_text(json.dumps(payload))
    result = orchestrator.run_host_reboot_phase("post", task, envelope)
    assert not result["passed"] and not result["phase_complete"] and phases == ["pre"]
    assert "external administrator" in result["output"]
    assert not (task / "admin-envelope.json").exists()


@pytest.mark.parametrize("failure,value", [
    ("actual-controls", "6576668672"), ("actual-controls", "max"),
    ("invalid-controls", "MAX"), ("invalid-controls", "01"), ("invalid-controls", "-1"),
    ("invalid-controls", 8589934592), ("invalid-controls", True), ("invalid-controls", None),
    ("pre-tampered", None), ("cleanup-unproven", None),
])
def test_host_reboot_never_aggregates_unproven_control_pre_or_cleanup(
    orchestrator, monkeypatch, tmp_path, failure, value,
):
    task, envelope, calls, phases, state = _host_reboot_fixture(orchestrator, monkeypatch, tmp_path)
    assert orchestrator.run_host_reboot_phase("pre", task, None)["phase_complete"]
    if failure == "actual-controls":
        state["outer"]["memory_max"] = value
    elif failure == "invalid-controls":
        state["outer"]["memory_max"] = value
        payload = json.loads(envelope.read_bytes())
        payload["memory_max"] = value
        envelope.write_text(json.dumps(payload))
    elif failure == "pre-tampered":
        (task / "pre.json").write_bytes(b"{}")
    else:
        state["cleanup"] = False
    result = orchestrator.run_host_reboot_phase("post", task, envelope)
    assert not result["passed"] and result["release_verdict"] == "INCOMPLETE"
    assert phases == (["pre", "post"] if failure == "cleanup-unproven" else ["pre"])
    assert len([call for call in calls if call[:2] == ("vm", "reboot")]) == 1


@pytest.mark.parametrize("failure", ["strict-report", "ssh-mkdir", "ssh-source", "source-changed", "sync-failure"])
def test_host_reboot_stops_before_lifecycle_after_failed_pre(orchestrator, monkeypatch, tmp_path, failure):
    stage_source = orchestrator._host_reboot_stage_source
    source_hash = orchestrator._sha256_runner_input
    task, _, calls, _, _ = _host_reboot_fixture(orchestrator, monkeypatch, tmp_path)
    def failed(*args):
        raise RuntimeError("strict PRE report failed")
    expected = "strict PRE report failed"
    if failure == "strict-report":
        monkeypatch.setattr(orchestrator, "_host_reboot_pytest", failed)
    else:
        monkeypatch.setattr(orchestrator, "_sha256_runner_input",
                            lambda path: source_hash(path) if path.is_file() else "a" * 64)
        def stage(bound):
            if failure == "source-changed":
                bound["source_payload"][orchestrator._HOST_REBOOT_SOURCE_FILES[0]] = "0" * 64
            stage_source(bound)
        monkeypatch.setattr(orchestrator, "_host_reboot_stage_source", stage)
        def transport(*args, **kwargs):
            calls.append(args)
            assert args[0] == "ssh", "no vsock fallback or lifecycle after transport failure"
            if ((failure == "ssh-mkdir" and "mkdir -m 700" in args[-1])
                    or (failure == "ssh-source" and "python3 -c" in args[-1])
                    or (failure == "sync-failure" and "sync -f --" in args[-1])):
                raise RuntimeError("SSH durability barrier failed" if failure == "sync-failure"
                                   else "SSH setup denied: literal diagnostic")
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")
        monkeypatch.setattr(orchestrator, "mvm", transport)
        expected = "source changed before staging" if failure == "source-changed" else "SSH setup denied"
        if failure == "sync-failure":
            expected = "SSH durability barrier failed"
    result = orchestrator.run_host_reboot_phase("pre", task, None)
    assert not result["phase_complete"] and not result["passed"]
    assert not any(call[:2] == ("vm", "reboot") for call in calls)
    assert expected in (task / "pre.terminal.json").read_text()
    assert not (task / "task.json").exists()
    if failure == "sync-failure":
        assert (task / "pre.json").exists() and (task / "pre.outcomes.json").exists()
        assert json.loads((task / "pre.native.json").read_bytes()) == dict(phase="pre", returncode=0)


@pytest.mark.parametrize("failure", ["candidate", "source-after-reconnect", "source-before-post"])
def test_host_reboot_stops_on_identity_failure_instead_of_retrying(orchestrator, monkeypatch, tmp_path, failure):
    actual_probe = orchestrator._host_reboot_probe
    task, envelope, calls, phases, state = _host_reboot_fixture(orchestrator, monkeypatch, tmp_path)
    if failure != "candidate":
        state["source_lost"] = failure == "source-after-reconnect"
        pre = orchestrator.run_host_reboot_phase("pre", task, None)
        if failure == "source-before-post":
            assert pre["phase_complete"] is True
            state["source_lost"] = True
            result = orchestrator.run_host_reboot_phase("post", task, envelope)
        else:
            result = pre
            assert not (task / "task.json").exists()
        assert not result["phase_complete"] and not result["passed"]
        assert "staged source changed across reboot" in result["output"]
        assert phases == ["pre"] and len([call for call in calls if call[:2] == ("vm", "reboot")]) == 1
        assert not (task / "post.json").exists()
        return
    probes = []
    def probe(bound):
        probes.append(bound)
        if len(probes) == 1:
            return "11111111-1111-4111-8111-111111111111"
        return actual_probe(bound)
    monkeypatch.setattr(orchestrator, "_host_reboot_probe", probe)
    def transport(*args, **kwargs):
        calls.append(args)
        if args[:2] == ("vm", "reboot") or "mkdir -m 700" in args[-1]:
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")
        assert args[0] == "ssh", "candidate mismatch must not invoke an exec fallback"
        return subprocess.CompletedProcess(args, 0,
            stdout="22222222-2222-4222-8222-222222222222\n" + "0" * 64 + "  /usr/local/bin/mvm\nmvm 0.3.0-rc.fixture\n", stderr="")
    monkeypatch.setattr(orchestrator, "mvm", transport)
    result = orchestrator.run_host_reboot_phase("pre", task, None)
    assert not result["phase_complete"] and len(probes) == 2
    assert "installed candidate changed" in result["output"] and not (task / "task.json").exists()


def test_host_reboot_is_not_executed_by_ordinary_tier2(orchestrator, monkeypatch):
    def unexpected(*args, **kwargs):
        raise AssertionError("ordinary T2 cannot mutate the named runner")
    monkeypatch.setattr(orchestrator, "mvm", unexpected)
    monkeypatch.setattr(orchestrator, "provision_t2", unexpected)
    assert "host_reboot" not in orchestrator.TIER2_DOMAINS
    assert orchestrator.HOST_REBOOT_PHASE_DOMAINS == {"host_reboot": ["tests/system/host/test_host_reboot.py"]}
    result = orchestrator.run_tier2_domain("host_reboot", ["tests/system/host/test_host_reboot.py"], "0.3.0")
    assert not result["passed"] and "no phase executed" in result["output"]


@pytest.mark.parametrize("field", ["path", "identity", "sha256"])
@pytest.mark.parametrize("checkpoint", ["pre-lifecycle", "post-body"])
def test_host_reboot_refuses_changed_configured_controller(orchestrator, monkeypatch, tmp_path, field, checkpoint):
    task, envelope, calls, phases, state = _host_reboot_fixture(orchestrator, monkeypatch, tmp_path)
    def change():
        state["controller"][field] = ([1, 999, 1000, 1000, 0o755, 1, 21586222, 3, 4] if field == "identity"
                                     else "/home/www/other-mvm" if field == "path" else "d" * 64)
    if checkpoint == "pre-lifecycle":
        original = orchestrator._host_reboot_pytest
        def phase(*args):
            raw = original(*args)
            change()
            return raw
        monkeypatch.setattr(orchestrator, "_host_reboot_pytest", phase)
        result = orchestrator.run_host_reboot_phase("pre", task, None)
        assert not any(call[:2] == ("vm", "reboot") for call in calls)
    else:
        assert orchestrator.run_host_reboot_phase("pre", task, None)["phase_complete"]
        change()
        result = orchestrator.run_host_reboot_phase("post", task, envelope)
        assert phases == ["pre"]
    assert not result["passed"] and not result["phase_complete"]


def test_host_reboot_pins_resolved_configured_controller_and_refuses_same_bytes_replacement(orchestrator, monkeypatch, tmp_path):
    pin_controller = orchestrator._host_reboot_controller
    task, envelope, calls, phases, _ = _host_reboot_fixture(orchestrator, monkeypatch, tmp_path)
    installed = tmp_path / ".local" / "bin" / "mvm"
    installed.parent.mkdir(parents=True)
    installed.write_bytes(b"unchanged installed outer controller")
    installed.chmod(0o755)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(installed))
    monkeypatch.setattr(orchestrator, "_host_reboot_controller", pin_controller)
    assert orchestrator.run_host_reboot_phase("pre", task, None)["phase_complete"]
    bound = json.loads((task / "task.json").read_bytes())
    assert bound["controller"]["path"] == str(installed.resolve())
    assert bound["controller"]["identity"][1] == installed.stat().st_ino
    replacement = installed.parent / "replacement"
    replacement.write_bytes(installed.read_bytes())
    replacement.chmod(0o755)
    replacement.replace(installed)
    assert installed.stat().st_ino != bound["controller"]["identity"][1]
    result = orchestrator.run_host_reboot_phase("post", task, envelope)
    assert not result["passed"] and phases == ["pre"]
    assert len([call for call in calls if call[:2] == ("vm", "reboot")]) == 1


@pytest.mark.parametrize("fault", ["missing", "malformed", "wrong-boot", "source", "controller", "control", "cleanup", "policy", "native", "skipped", "wrong-junit"])
def test_host_reboot_full_release_refuses_incomplete_or_stale_closed_proof(orchestrator, monkeypatch, tmp_path, fault):
    task, envelope, calls, phases, state = _host_reboot_fixture(orchestrator, monkeypatch, tmp_path)
    assert orchestrator.run_host_reboot_phase("pre", task, None)["phase_complete"]
    assert orchestrator.run_host_reboot_phase("post", task, envelope)["passed"]
    before_calls = list(calls)
    def replace(name, mutate):
        path = task / name
        value = json.loads(path.read_bytes())
        mutate(value)
        path.write_text(json.dumps(value))
    if fault == "missing":
        (task / "post.native.json").unlink()
    elif fault == "malformed":
        (task / "post.terminal.json").write_bytes(b"{}")
    elif fault == "wrong-boot":
        replace("post-authority.json", lambda value: value.update(host_boot_id="33333333-3333-4333-8333-333333333333"))
    elif fault == "source":
        replace("task.json", lambda value: value.update(source_revision="e" * 40))
    elif fault == "controller":
        state["controller"]["identity"][1] = 999
    elif fault == "control":
        replace("post-authority.json", lambda value: value["outer"].update(memory_high="6576668672"))
    elif fault == "cleanup":
        replace("post.json", lambda value: value.update(owned_cleanup=False))
    elif fault == "policy":
        replace("post.json", lambda value: value["physical_after"].update(exec=[{"id": "changed-policy"}]))
        replace("preservation.json", lambda value: value["final"].update(exec=[{"id": "changed-policy"}]))
    elif fault == "native":
        replace("post.native.json", lambda value: value.update(returncode=1))
    elif fault == "skipped":
        replace("post.outcomes.json", lambda value: value.update(passed=0, skipped=1))
    elif fault == "wrong-junit":
        path = task / "post.xml"
        path.write_bytes(path.read_bytes().replace(b"test_host_reboot_post", b"test_host_reboot_pre"))
    if fault not in ("missing", "malformed"):
        replace("post.terminal.json", lambda value: value.update(proof_sha256=orchestrator._host_reboot_proof_hashes(task)))
    with pytest.raises((RuntimeError, FileNotFoundError, KeyError)):
        orchestrator.require_completed_host_reboot(task)
    assert phases == ["pre", "post"] and calls == before_calls


@pytest.mark.parametrize("selection,release", [(["--tier", "2"], False), (["--all", "--host-direct"], False),
    (["--release-qualification", "--all", "--host-direct", "--rebuild", "--candidate-version", "0.3.0",
      "--host-reboot-task", "/tmp/completed-host-reboot"], True)])
def test_host_reboot_batch_selection_and_full_release_consume_only_explicit_completed_proof(orchestrator, monkeypatch, tmp_path, capsys, selection, release):
    candidate = tmp_path / "candidate-mvm"
    candidate.write_bytes(b"candidate")
    candidate.chmod(0o755)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", candidate)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    monkeypatch.setattr(orchestrator.sys, "argv", ["run-system-tests.py", *selection])
    monkeypatch.setattr(orchestrator.shutil, "which", lambda binary: "/usr/local/bin/mvm")
    monkeypatch.setattr(orchestrator, "_resolve_imported_runner_kernel_binding", lambda: None)
    monkeypatch.setattr(orchestrator, "_require_distinct_candidate_controller", lambda: None)
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0")
    monkeypatch.setattr(orchestrator, "ensure_shared_volume", lambda **kwargs: None)
    monkeypatch.setattr(orchestrator, "ensure_test_network", lambda: None)
    monkeypatch.setattr(orchestrator, "validate_release_build_paths", lambda **kwargs: None)
    monkeypatch.setattr(orchestrator, "_resolve_candidate_build_version", lambda version: version)
    monkeypatch.setattr(orchestrator, "_build_mvm_binary", lambda version: None)
    monkeypatch.setattr(orchestrator, "verify_release_binary_identity", lambda **kwargs: None)
    monkeypatch.setattr(orchestrator, "_require_release_host_without_active_vms", lambda: None)
    events, executed, tier3_timeouts = [], [], []
    monkeypatch.setattr(orchestrator, "run_prepare", lambda **kwargs: events.append("prepare"))
    def batch(domains, tier, *args, **kwargs):
        executed.extend(domains)
        return [dict(domain=domain, tier=tier, passed=True, output="mock batch") for domain in domains]
    monkeypatch.setattr(orchestrator, "run_domains", batch)
    def run_tier3(domain, files, *, timeout):
        assert files == orchestrator.TIER3_DOMAINS[domain]
        tier3_timeouts.append((domain, timeout))
        return dict(domain=domain, tier=3, passed=True, output="mock batch")
    monkeypatch.setattr(orchestrator, "run_tier3_domain", run_tier3)
    monkeypatch.setattr(orchestrator, "run_host_reboot_phase", lambda *args: pytest.fail("batch cannot reboot"))
    def consume(task):
        assert release and task == Path("/tmp/completed-host-reboot")
        events.append("consume-closed-proof")
        return dict(domain="host_reboot", tier=2, passed=True, output="historical closed proof")
    monkeypatch.setattr(orchestrator, "require_completed_host_reboot", consume)
    orchestrator.main()
    assert executed and "host_reboot" not in executed
    expected_tier3_timeouts = [
        (domain, 23400 if domain == "kernel_build" else 600)
        for domain in sorted(orchestrator.TIER3_DOMAINS)
    ] if "--all" in selection else []
    assert tier3_timeouts == expected_tier3_timeouts
    output = capsys.readouterr().out
    assert "0 failed" in output
    if release:
        assert events == ["consume-closed-proof", "prepare", "consume-closed-proof"]
        assert "[PASS] Tier 2 host_reboot" in output and "RELEASE VERDICT INCOMPLETE" not in output
    else:
        assert events == [] and "BATCH RESULTS ONLY / RELEASE VERDICT INCOMPLETE" in output


def test_host_reboot_full_release_requires_completed_task_before_effects(orchestrator, monkeypatch, capsys):
    _assert_selection_rejected_before_side_effects(orchestrator, monkeypatch, capsys,
        ["--release-qualification", "--all", "--host-direct", "--rebuild", "--candidate-version", "0.3.0"],
        "requires --host-reboot-task with completed strict POST evidence")


@pytest.mark.parametrize("fault", [None, "snapshot-after_boot", "traffic-after_boot", "exec-after_boot",
                                   "snapshot-final", "traffic-final", "exec-final"])
def test_host_reboot_guest_preservation_refuses_persistent_changes_but_allows_boot_physical_changes(monkeypatch, tmp_path, fault):
    # Execute only the pure preservation assertion, not a registered guest phase body.
    root = Path(__file__).parents[2]
    monkeypatch.syspath_prepend(str(root))
    from tests.system.host.test_host_reboot import _preserve_persistent_inventory
    before = {key: [] for key in ("image", "kernel", "bin", "volume", "key", "snapshot", "config", "traffic", "exec")}
    before.update(namespace=[1, 2], links=[{"ifindex": 3}], addresses=[], routes=[], firewall=[], namespace_mounts={})
    after_boot, final = json.loads(json.dumps(before)), json.loads(json.dumps(before))
    after_boot.update(namespace=[5, 6], links=[{"ifindex": 7}])
    final.update(namespace=[5, 6], links=[{"ifindex": 8}])
    if fault:
        domain, point = fault.split("-")
        (after_boot if point == "after_boot" else final)[domain] = [{"id": "changed"}]
        with pytest.raises(AssertionError, match=domain):
            _preserve_persistent_inventory(tmp_path, before, after_boot, final)
    else:
        _preserve_persistent_inventory(tmp_path, before, after_boot, final)
    assert json.loads((tmp_path / "preservation.json").read_bytes()) == dict(before=before, after_boot=after_boot, final=final)


@pytest.mark.parametrize("selection", [
    ["host", "--host-reboot-phase", "pre", "--host-reboot-task", "/tmp/task"],
    ["host_reboot", "--host-reboot-phase", "post", "--host-reboot-task", "/tmp/task"],
    ["host_reboot", "--host-reboot-phase", "pre", "--host-reboot-task", "/tmp/task", "--push"],
    ["host_reboot"],
])
def test_host_reboot_mixed_or_incomplete_selection_fails_before_side_effects(
    orchestrator, monkeypatch, capsys, selection,
):
    _assert_selection_rejected_before_side_effects(orchestrator, monkeypatch, capsys, selection,
        "host_reboot requires only its exact domain")


@pytest.mark.parametrize("fault", ["native-failure", "skipped", "wrong-junit", "two-items", "ssh-report-unavailable"])
def test_host_reboot_retains_literal_reports_and_rejects_incomplete_phase(
    orchestrator, monkeypatch, tmp_path, fault,
):
    actual_fetch = orchestrator._host_reboot_fetch
    task = tmp_path / "phase"
    task.mkdir(mode=0o700)
    bound = dict(guest_task="/home/runner/.cache/mvmctl-host-reboot-" + "1" * 32,
        source_revision="f" * 40, candidate_sha256="a" * 64, candidate_version="mvm 0.3.0",
        kernel_id="b" * 64, kernel_sha256="c" * 64)
    calls = []
    def mvm(*args, **kwargs):
        calls.append(args)
        assert args[0] == "ssh", "phase execution/report retrieval cannot fall back to outer exec"
        if "head -c" in args[-1]:
            raise RuntimeError("SSH report unavailable: literal diagnostic")
        return subprocess.CompletedProcess(args, 1 if fault == "native-failure" else 0, stdout="literal pytest", stderr="literal diagnostic")
    monkeypatch.setattr(orchestrator, "mvm", mvm)
    outcomes = json.loads(_complete_pytest_outcome_report(2 if fault == "two-items" else 1))
    if fault == "skipped":
        outcomes.update(passed=0, skipped=1)
    method = "test_host_reboot_post" if fault == "wrong-junit" else "test_host_reboot_pre"
    xml = f'<testsuites><testsuite tests="1" failures="0" errors="0" skipped="0"><testcase classname="tests.system.host.test_host_reboot" name="{method}"/></testsuite></testsuites>'.encode()
    def fetch(bound, name):
        if fault == "ssh-report-unavailable":
            return actual_fetch(bound, name)
        return xml if name.endswith(".xml") else json.dumps(outcomes).encode()
    monkeypatch.setattr(orchestrator, "_host_reboot_fetch", fetch)
    with pytest.raises(RuntimeError) as failure:
        orchestrator._host_reboot_pytest(bound, "pre", task)
    assert (task / "pre.stdout").read_bytes() == b"literal pytest"
    assert (task / "pre.stderr").read_bytes() == b"literal diagnostic"
    assert json.loads((task / "pre.native.json").read_bytes()) == dict(phase="pre", returncode=1 if fault == "native-failure" else 0)
    if fault == "ssh-report-unavailable":
        assert "SSH report unavailable: literal diagnostic" in str(failure.value)
        assert not (task / "pre.outcomes.json").exists() and not (task / "pre.xml").exists()
    else:
        assert (task / "pre.outcomes.json").read_bytes() == json.dumps(outcomes).encode()
        assert (task / "pre.xml").read_bytes() == xml
    assert len(calls) == (2 if fault == "ssh-report-unavailable" else 1) and calls[0][1] == "mvmctl-030-test"
    assert "::test_host_reboot_pre" in calls[0][-1] and "MVM_CONFIG_DIR=/home/runner/.config/mvmctl-030-schema6" in calls[0][-1]
    assert "--rootdir=." in calls[0][-1]


def test_registry_uses_exec_tier2_and_one_canonical_fresh_env_domain(
    orchestrator: ModuleType,
) -> None:
    assert orchestrator.TIER2_DOMAINS["exec"] == [
        "tests/system/exec/test_exec.py",
        "tests/system/exec/test_vm_to_vm_exec.py",
    ]
    assert orchestrator.TIER3_DOMAINS["fresh_env"] == [
        "tests/system/vm/test_vm_fresh_env.py"
    ]
    assert "nested_virt" not in orchestrator.TIER3_DOMAINS


def test_duplicate_registry_domain_is_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    orchestrator.TIER2_DOMAINS["cli"] = ["tests/system/cli/test_cli.py"]

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        "duplicate system-test domains: cli",
    )


def test_empty_registry_domains_are_rejected_in_deterministic_order(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    orchestrator.TIER1_DOMAINS["cli"].clear()
    orchestrator.TIER2_DOMAINS["volume"].clear()

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        "empty system-test domains: cli, volume",
    )


def test_duplicate_registered_file_is_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    registered = "tests/system/cli/test_cli.py"
    _write_system_test(tmp_path, registered)
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"cli": [registered]},
        {"cli_alias": [registered]},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        f"duplicate registered system-test files: {registered}",
    )


@pytest.mark.parametrize(
    "registered",
    [
        "tests/system/cli/helper.py",
        "outside/test_escape.py",
        "/tmp/test_absolute.py",
        "tests/system/cli/../cli/test_cli.py",
        "tests/system/../../outside/test_escape.py",
    ],
    ids=["non-test", "out-of-tree", "absolute", "non-canonical", "escaping"],
)
def test_invalid_registered_paths_are_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
    registered: str,
) -> None:
    if not Path(registered).is_absolute():
        _write_system_test(tmp_path, registered)
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"invalid": [registered]},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        f"invalid system-test paths: {registered}",
    )


def test_missing_registered_files_are_rejected_in_deterministic_order(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    missing = [
        "tests/system/zeta/test_zeta.py",
        "tests/system/alpha/test_alpha.py",
    ]
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"missing": missing},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        "missing system-test files: "
        "tests/system/alpha/test_alpha.py, tests/system/zeta/test_zeta.py",
    )


@pytest.mark.parametrize("kind", ["directory", "symlink"])
def test_non_regular_registered_files_are_rejected_before_any_probe(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
    kind: str,
) -> None:
    registered = "tests/system/cli/test_cli.py"
    registered_path = tmp_path / registered
    registered_path.parent.mkdir(parents=True)
    if kind == "directory":
        registered_path.mkdir()
    else:
        target = tmp_path / "real_test.py"
        target.write_text("def test_real():\n    pass\n", encoding="utf-8")
        registered_path.symlink_to(target)
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"invalid": [registered]},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        f"non-regular system-test files: {registered}",
    )


def test_unregistered_system_tests_are_rejected_in_deterministic_order(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    registered = "tests/system/cli/test_cli.py"
    _write_system_test(tmp_path, registered)
    _write_system_test(tmp_path, "tests/system/zeta/test_zeta.py")
    _write_system_test(tmp_path, "tests/system/alpha/test_alpha.py")
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"cli": [registered]},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        "unregistered system-test files: "
        "tests/system/alpha/test_alpha.py, tests/system/zeta/test_zeta.py",
    )


def test_tier3_selection_detection_covers_all_request_forms(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    selections = [
        ["--all"],
        ["--tier", "3"],
        ["--tier", "2,3,1"],
        *[[domain] for domain in orchestrator.TIER3_DOMAINS],
    ]

    for selection in selections:
        args = parser.parse_args(selection)
        assert orchestrator._selection_requests_tier3(args), selection


def test_t1_t2_and_unknown_names_do_not_imply_tier3_host_direct(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    selections = [
        ["--tier", "1"],
        ["--tier", "1,2"],
        [next(iter(orchestrator.TIER1_DOMAINS))],
        [next(iter(orchestrator.TIER2_DOMAINS))],
        ["unknown-domain"],
    ]

    for selection in selections:
        args = parser.parse_args(selection)
        assert not orchestrator._selection_requests_tier3(args), selection


@pytest.mark.parametrize(
    "selection",
    [
        ["not-a-domain", "also-unknown"],
        ["not-a-domain", "also-unknown", "--tier", "1"],
    ],
    ids=["domains-only", "with-tier"],
)
def test_unknown_domains_are_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
) -> None:
    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        selection,
        "unknown domains: not-a-domain, also-unknown",
    )


@pytest.mark.parametrize(
    "selection",
    [
        ["--all", "cli"],
        ["--all", "--tier", "1"],
        ["cli", "--tier", "1"],
    ],
    ids=["all-and-domains", "all-and-tier", "domains-and-tier"],
)
def test_ambiguous_test_selectors_are_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
) -> None:
    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        selection,
        "choose exactly one test selector: positional domains, --tier, or --all",
    )


@pytest.mark.parametrize(
    ("selection", "expected_error"),
    [
        (["cli", "cli"], "duplicate domains: cli"),
        (["cli", "config", "cli"], "duplicate domains: cli"),
        (["--tier", "1,1"], "duplicate tiers: 1"),
        (["--tier", "1,2,1"], "duplicate tiers: 1"),
    ],
    ids=["adjacent-domains", "nonadjacent-domains", "adjacent-tiers", "nonadjacent-tiers"],
)
def test_duplicate_test_selectors_are_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
    expected_error: str,
) -> None:
    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        selection,
        expected_error,
    )


def test_unique_tier_selection_preserves_requested_order(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    args = parser.parse_args(["--tier", "2,1,3"])

    orchestrator._validate_test_selection_args(parser, args)

    assert args.tier == [2, 1, 3]


def test_selection_with_no_registered_domains_is_rejected_before_any_probe(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    _configure_test_registry(orchestrator, monkeypatch, tmp_path, {})

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        ["--tier", "2"],
        "test selection resolves to zero tests",
    )


def test_no_arguments_print_help_and_exit_without_any_probe(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setattr(
        orchestrator.shutil,
        "which",
        lambda *_args, **_kwargs: pytest.fail("no-args help must not probe binaries"),
    )
    monkeypatch.setattr(orchestrator.sys, "argv", ["run-system-tests.py"])

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 0
    assert "usage:" in capsys.readouterr().out.lower()


@pytest.mark.parametrize(
    "selection",
    [
        ["--prepare"],
        ["--volume"],
        ["--image"],
        ["--volume", "--image", "--prepare"],
        ["--rebuild", "--candidate-version", "0.3.0"],
    ],
    ids=["prepare", "volume", "image", "combined", "rebuild"],
)
def test_preparation_only_modes_do_not_require_a_test_selector(
    orchestrator: ModuleType,
    selection: list[str],
) -> None:
    parser = orchestrator._build_parser()
    args = parser.parse_args(selection)

    orchestrator._validate_test_selection_args(parser, args)


def test_host_direct_help_is_an_acknowledgment_not_a_clean_host_guarantee(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    args = parser.parse_args(["--host-direct", "--all"])
    help_text = parser.format_help().lower()

    assert args.host_direct is True
    assert "acknowledge" in help_text
    assert "guarantee" not in help_text


@pytest.mark.parametrize(
    "selection",
    [
        ["--all", "--rebuild", "--candidate-version", "0.3.0"],
        ["--tier", "3"],
        ["env"],
    ],
    ids=["all", "tier", "positional-domain"],
)
def test_tier3_without_host_direct_is_rejected_before_any_side_effect(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
) -> None:
    calls: list[str] = []

    def unexpected(name: str):
        def fail(*_args: object, **_kwargs: object) -> None:
            calls.append(name)
            raise AssertionError(f"{name} must not run before host-direct consent")

        return fail

    for helper in (
        "_resolve_candidate_build_version",
        "_build_mvm_binary",
        "_require_release_host_without_active_vms",
        "_require_distinct_candidate_controller",
        "_get_mvm_version",
        "run_prepare",
        "ensure_shared_volume",
        "ensure_test_network",
    ):
        monkeypatch.setattr(orchestrator, helper, unexpected(helper))
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        ["run-system-tests.py", *selection],
    )

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 2
    assert calls == []
    error = capsys.readouterr().err.lower()
    assert "--host-direct" in error
    assert "acknowledge" in error


@pytest.mark.parametrize(
    "selection",
    [
        ["--release-qualification", "--host-direct", "--rebuild", "--candidate-version", "0.3.0"],
        ["--release-qualification", "--all", "--rebuild", "--candidate-version", "0.3.0"],
        ["--release-qualification", "--all", "--host-direct", "--candidate-version", "0.3.0"],
        ["--release-qualification", "--all", "--host-direct", "--rebuild"],
        [
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0",
            "env",
        ],
        [
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0",
            "--tier",
            "1,2,3",
        ],
        [
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0",
            "--skip-volume-check",
        ],
    ],
    ids=[
        "missing-all",
        "missing-host-direct",
        "missing-rebuild",
        "missing-version",
        "positional-domain",
        "tier-filter",
        "skip-volume-check",
    ],
)
def test_invalid_release_qualification_is_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
) -> None:
    calls: list[str] = []

    def unexpected(name: str):
        def fail(*_args: object, **_kwargs: object) -> None:
            calls.append(name)
            raise AssertionError(f"{name} must not run for an invalid release mode")

        return fail

    for helper in (
        "validate_release_build_paths",
        "_resolve_candidate_build_version",
        "_build_mvm_binary",
        "verify_release_binary_identity",
        "_require_release_host_without_active_vms",
        "_require_distinct_candidate_controller",
        "_get_mvm_version",
        "run_prepare",
        "ensure_shared_volume",
        "ensure_test_network",
    ):
        monkeypatch.setattr(orchestrator, helper, unexpected(helper), raising=False)
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        ["run-system-tests.py", *selection],
    )

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 2
    assert calls == []
    assert "--release-qualification requires" in capsys.readouterr().err


def test_release_qualification_parser_accepts_only_the_full_acknowledged_gate(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    args = parser.parse_args(
        [
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0-rc.1",
            "--host-reboot-task", "/tmp/completed-reboot",
        ]
    )

    orchestrator._validate_release_qualification_args(parser, args)

    assert args.release_qualification is True
    assert args.candidate_version == "0.3.0-rc.1"
    assert "release binary identity" in parser.format_help().lower()


def test_release_identity_runs_after_build_and_before_host_resource_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    events: list[str] = []

    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        [
            "run-system-tests.py",
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0",
            "--host-reboot-task", "/tmp/completed-reboot",
        ],
    )
    monkeypatch.setattr(
        orchestrator,
        "validate_release_build_paths",
        lambda **_kwargs: events.append("path-configuration"),
        raising=False,
    )
    monkeypatch.setattr(
        orchestrator,
        "_resolve_candidate_build_version",
        lambda version: events.append(f"resolve-{version}") or version,
    )
    monkeypatch.setattr(
        orchestrator,
        "_build_mvm_binary",
        lambda version: events.append(f"build-{version}"),
    )

    def fail_identity(**kwargs: object) -> None:
        events.append(f"identity-{kwargs['requested_version']}")
        raise RuntimeError("release identity mismatch")

    monkeypatch.setattr(
        orchestrator,
        "verify_release_binary_identity",
        fail_identity,
        raising=False,
    )

    def unexpected_mutation(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("host/resource mutation must follow identity validation")

    for helper in (
        "_require_release_host_without_active_vms",
        "run_prepare",
        "ensure_shared_volume",
        "ensure_test_network",
    ):
        monkeypatch.setattr(orchestrator, helper, unexpected_mutation)

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 1
    assert events == [
        "path-configuration",
        "resolve-0.3.0",
        "build-0.3.0",
        "identity-0.3.0",
    ]
    assert "release identity mismatch" in capsys.readouterr().out


def test_release_host_admission_runs_after_identity_and_before_resource_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    events: list[str] = []
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        [
            "run-system-tests.py",
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0",
            "--host-reboot-task", "/tmp/completed-reboot",
        ],
    )
    monkeypatch.setattr(
        orchestrator,
        "validate_release_build_paths",
        lambda **_kwargs: events.append("path-configuration"),
    )
    monkeypatch.setattr(
        orchestrator,
        "_resolve_candidate_build_version",
        lambda version: events.append(f"resolve-{version}") or version,
    )
    monkeypatch.setattr(
        orchestrator,
        "_build_mvm_binary",
        lambda version: events.append(f"build-{version}"),
    )
    monkeypatch.setattr(
        orchestrator,
        "verify_release_binary_identity",
        lambda **kwargs: events.append(f"identity-{kwargs['requested_version']}"),
    )

    def refuse_active_vm() -> None:
        events.append("host-vm-admission")
        raise RuntimeError("release host has an active VM")

    monkeypatch.setattr(
        orchestrator,
        "_require_release_host_without_active_vms",
        refuse_active_vm,
        raising=False,
    )

    def unexpected(name: str):
        def fail(*_args: object, **_kwargs: object) -> None:
            events.append(name)
            raise AssertionError(f"{name} must follow release host admission")

        return fail

    for helper in (
        "_require_distinct_candidate_controller",
        "_get_mvm_version",
        "run_prepare",
        "ensure_shared_volume",
        "ensure_test_network",
    ):
        monkeypatch.setattr(orchestrator, helper, unexpected(helper))

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 1
    assert events == [
        "path-configuration",
        "resolve-0.3.0",
        "build-0.3.0",
        "identity-0.3.0",
        "host-vm-admission",
    ]
    assert "release host has an active VM" in capsys.readouterr().out


def test_release_host_admission_queries_exact_controller_and_accepts_stopped_vms(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls = _stub_release_host_vm_list(
        orchestrator,
        monkeypatch,
        stdout=json.dumps(
            [
                {
                    "id": "11111111-1111-1111-1111-111111111111",
                    "name": "stopped-vm",
                    "status": "stopped",
                    "pid": 0,
                }
            ]
        ),
        data_payload=True,
    )

    orchestrator._require_release_host_without_active_vms()

    assert calls == [
        (
            ("vm", "ls", "--json"),
            {"check": True, "timeout": 15},
        )
    ]


@pytest.mark.parametrize(
    ("payload", "expected_error"),
    [
        ("[]", "JSON response envelope"),
        ("{}", "incomplete or unknown response fields"),
        ('{"schema_version":2,"status":"success","data":[],"error":null}',
         "unsupported response schema"),
        ('{"schema_version":1,"status":"partial","data":[],"error":null}',
         "not a successful response"),
        ('{"schema_version":1,"status":"success","data":[],"error":{}}',
         "not a successful response"),
        ('{"schema_version":1,"status":"success","data":null,"error":null}',
         "data must be a JSON list"),
        ('{"schema_version":1,"status":"success","data":[],"error":null,"extra":1}',
         "incomplete or unknown response fields"),
    ],
)
def test_release_host_admission_rejects_invalid_cli_envelope(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    payload: str,
    expected_error: str,
) -> None:
    _stub_release_host_vm_list(orchestrator, monkeypatch, stdout=payload)

    with pytest.raises(RuntimeError, match=expected_error):
        orchestrator._require_release_host_without_active_vms()


@pytest.mark.parametrize(
    ("payload", "expected_error"),
    [
        ("{", "malformed JSON"),
        ("{}", "data must be a JSON list"),
        ("[null]", "entry 0 must be an object"),
        ('[{"id":"id","name":"vm"}]', "entry 0 missing fields: status"),
        (
            '[{"id":"id","name":"vm","status":1}]',
            "entry 0 field status must be a string",
        ),
        (
            '[{"id":"id","name":"vm","status":"RUNNING"}]',
            "entry 0 has unknown status",
        ),
        (
            '[{"id":"id","name":"vm","status":"stopped",'
            '"status":"running"}]',
            "duplicate field: status",
        ),
        (
            '[{"id":"id","name":"vm","status":"stopped",'
            '"STATUS":"running"}]',
            "case-fold duplicate fields: STATUS, status",
        ),
        (
            '[{"id":"id","name":"vm-a","status":"stopped"},'
            '{"id":"id","name":"vm-b","status":"stopped"}]',
            "duplicate VM id: id",
        ),
        (
            '[{"id":"id-a","name":"vm","status":"stopped"},'
            '{"id":"id-b","name":"vm","status":"stopped"}]',
            "duplicate VM name: vm",
        ),
        (
            '[{"id":"","name":"vm","status":"stopped"}]',
            "entry 0 field id must not be empty",
        ),
        (
            '[{"id":"id","name":false,"status":"stopped"}]',
            "entry 0 field name must be a string",
        ),
        (
            '[{"id":"id","name":"vm","status":"stopped",'
            '"ambiguous":NaN}]',
            "non-standard JSON value: NaN",
        ),
    ],
    ids=[
        "malformed-json",
        "top-level-object",
        "non-object-entry",
        "missing-status",
        "non-string-status",
        "unknown-status",
        "duplicate-status",
        "casefold-duplicate-status",
        "duplicate-id",
        "duplicate-name",
        "empty-id",
        "non-string-name",
        "nonstandard-constant",
    ],
)
def test_release_host_admission_rejects_ambiguous_vm_list_schema(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    payload: str,
    expected_error: str,
) -> None:
    _stub_release_host_vm_list(
        orchestrator,
        monkeypatch,
        stdout=payload,
        data_payload=True,
    )

    with pytest.raises(RuntimeError, match=expected_error):
        orchestrator._require_release_host_without_active_vms()


@pytest.mark.parametrize(
    ("returncode", "stdout", "expected_error"),
    [
        (1, "[]", "outer controller vm ls --json failed with rc=1"),
        (0, None, "outer controller vm ls --json returned no stdout"),
    ],
    ids=["nonzero", "missing-stdout"],
)
def test_release_host_admission_rejects_command_ambiguity(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    returncode: int,
    stdout: str | None,
    expected_error: str,
) -> None:
    _stub_release_host_vm_list(
        orchestrator,
        monkeypatch,
        stdout=stdout,
        returncode=returncode,
    )

    with pytest.raises(RuntimeError, match=expected_error):
        orchestrator._require_release_host_without_active_vms()


@pytest.mark.parametrize(
    ("limit_name", "limit", "payload", "expected_error"),
    [
        (
            "MAX_RELEASE_HOST_VM_LIST_BYTES",
            16,
            "[]               ",
            "exceeds 16 bytes",
        ),
        (
            "MAX_RELEASE_HOST_VM_LIST_ENTRIES",
            1,
            '[{"id":"id-a","name":"vm-a","status":"stopped"},'
            '{"id":"id-b","name":"vm-b","status":"stopped"}]',
            "exceeds 1 entries",
        ),
        (
            "MAX_RELEASE_HOST_VM_FIELD_LENGTH",
            4,
            '[{"id":"id","name":"abcde","status":"stopped"}]',
            "entry 0 field name exceeds 4 characters",
        ),
    ],
    ids=["bytes", "entries", "field-length"],
)
def test_release_host_admission_enforces_bounded_vm_list(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    limit_name: str,
    limit: int,
    payload: str,
    expected_error: str,
) -> None:
    monkeypatch.setattr(orchestrator, limit_name, limit, raising=False)
    _stub_release_host_vm_list(
        orchestrator,
        monkeypatch,
        stdout=payload,
        data_payload=True,
    )

    with pytest.raises(RuntimeError, match=expected_error):
        orchestrator._require_release_host_without_active_vms()


@pytest.mark.parametrize(
    "status",
    ["starting", "running", "paused", "stopping", "crashed", "error"],
)
def test_release_host_admission_rejects_every_non_stopped_vm_status(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    status: str,
) -> None:
    _stub_release_host_vm_list(
        orchestrator,
        monkeypatch,
        stdout=json.dumps(
            [
                {"id": "stopped-id", "name": "stopped-vm", "status": "stopped"},
                {"id": "unsafe-id", "name": "unsafe-vm", "status": status},
            ]
        ),
        data_payload=True,
    )

    with pytest.raises(RuntimeError, match="not safely stopped"):
        orchestrator._require_release_host_without_active_vms()


def test_release_host_admission_rejects_noncanonical_controller_without_query(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        orchestrator,
        "MVM_BINARY",
        "/usr/local/bin/mvm --wrapper-argument",
    )
    monkeypatch.setattr(
        orchestrator,
        "mvm",
        lambda *_args, **_kwargs: pytest.fail(
            "noncanonical controller must not be invoked"
        ),
    )

    with pytest.raises(RuntimeError, match="exact /usr/local/bin/mvm"):
        orchestrator._require_release_host_without_active_vms()


@pytest.mark.parametrize(
    ("domain", "tier"),
    [("cli", 1), ("jailer", 2)],
    ids=["tier1", "tier2"],
)
def test_focused_tier1_and_tier2_do_not_run_release_host_admission(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    domain: str,
    tier: int,
) -> None:
    candidate = tmp_path / "candidate-mvm"
    candidate.write_bytes(b"candidate")
    candidate.chmod(0o755)
    events: list[str] = []
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        ["run-system-tests.py", domain],
    )
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", candidate)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    monkeypatch.setattr(orchestrator.shutil, "which", lambda _binary: "/usr/local/bin/mvm")
    monkeypatch.setattr(
        orchestrator,
        "_require_release_host_without_active_vms",
        lambda: pytest.fail("focused T1/T2 must not run release host admission"),
    )
    monkeypatch.setattr(
        orchestrator,
        "_require_distinct_candidate_controller",
        lambda: events.append("distinct-paths"),
    )
    monkeypatch.setattr(
        orchestrator,
        "_get_mvm_version",
        lambda: events.append("candidate-version") or "0.3.0",
    )
    monkeypatch.setattr(
        orchestrator,
        "ensure_shared_volume",
        lambda **_kwargs: events.append("shared-volume"),
    )
    monkeypatch.setattr(
        orchestrator,
        "ensure_test_network",
        lambda: events.append("test-network"),
    )
    monkeypatch.setattr(
        orchestrator,
        "run_domains",
        lambda domains, tier, *_args, **_kwargs: events.append(
            f"run-tier-{tier}:{','.join(domains)}"
        ) or [],
    )

    orchestrator.main()

    assert events == [
        "distinct-paths",
        "candidate-version",
        "shared-volume",
        "test-network",
        f"run-tier-{tier}:{domain}",
    ]


def test_guest_product_commands_use_the_exact_installed_cli(
    orchestrator: ModuleType,
) -> None:
    tree = ast.parse(Path(orchestrator.__file__).read_text(encoding="utf-8"))
    unqualified = sorted(
        {
            node.value
            for node in ast.walk(tree)
            if isinstance(node, ast.Constant)
            and isinstance(node.value, str)
            and "MVM_ASSET_MIRROR=/mnt mvm " in node.value
        }
    )

    assert unqualified == []


def test_runner_user_initialization_uses_the_exact_installed_cli(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    orchestrator._initialize_runner_user(
        "base-img-builder",
        timeout=180,
        capture=False,
    )

    assert calls == [
        (
            (
                "exec",
                "base-img-builder",
                "--user",
                "runner",
                "--timeout",
                "10",
                "--",
                "sudo mkdir -p /mnt && sudo mount /dev/vdb /mnt && "
                "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm init --non-interactive "
                "--binary-version 1.17.0",
            ),
            {"timeout": 180, "capture": False},
        )
    ]


@pytest.mark.parametrize(
    "fault",
    ["ready", "missing", "wrong", "absent", "inspection-failed", "alpine-pull-failed"],
)
def test_tier2_provision_registers_assets_and_requires_alpine_default(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    fault: str,
) -> None:
    """Asset registration must establish the default used by image-less VM creation."""
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []
    images = [{"type": "alpine", "version": "3.23", "is_default": True, "is_present": True}]
    if fault == "missing":
        images[0]["is_default"] = False
    elif fault == "wrong":
        images[0]["type"] = "ubuntu"
    elif fault == "absent":
        images[0]["is_present"] = False

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        stdout = ""
        if args[-1] == "/usr/local/bin/mvm image ls --json":
            if fault == "inspection-failed":
                raise RuntimeError("image defaults inspection refused")
            stdout = json.dumps({
                "schema_version": 1, "status": "success", "data": images, "error": None,
            })
        elif fault == "alpine-pull-failed" and "image pull alpine:3.23" in args[-1]:
            raise RuntimeError("Alpine registration refused")
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    for helper in (
        "_ensure_builder_key",
        "_ensure_official_kernel_on_host",
        "_install_system_binary_in_runner",
        "_initialize_system_binary_in_runner",
        "_initialize_runner_user",
    ):
        monkeypatch.setattr(orchestrator, helper, lambda *_args, **_kwargs: None)

    if fault == "ready":
        orchestrator.provision_t2("focused-runner", "0.3.0-rc.test", lambda: None)
    else:
        failure = {
            "inspection-failed": "image defaults inspection refused",
            "alpine-pull-failed": "Alpine registration refused",
        }.get(fault, "exactly one present Alpine 3.23 default image")
        with pytest.raises(RuntimeError, match=failure):
            orchestrator.provision_t2("focused-runner", "0.3.0-rc.test", lambda: None)

    registrations = [
        ("MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm kernel pull --type firecracker --version v1.15 --default", 150),
        ("MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull alpine:3.23 --default", 150),
        ("MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull ubuntu:noble", 360),
        ("MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm bin pull 1.17.0 --default --force", 150),
        ("/usr/local/bin/mvm image ls --json", 30),
    ]
    if fault == "alpine-pull-failed":
        registrations = registrations[:2]
    assert calls[1:] == [
        (
            ("exec", "focused-runner", "--user", "runner", "--timeout", "10", "--", command),
            {"timeout": timeout},
        )
        for command, timeout in registrations
    ]


@pytest.mark.parametrize("provision_name", ["provision_t1", "provision_t2"])
@pytest.mark.parametrize("create_fails", [False, True], ids=["created", "create-refused"])
def test_provision_reports_ownership_only_after_successful_create(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    provision_name: str,
    create_fails: bool,
) -> None:
    events: list[str] = []
    monkeypatch.setattr(orchestrator, "_ensure_builder_key", lambda: None)
    monkeypatch.setattr(orchestrator, "_ensure_official_kernel_on_host", lambda _name: None)

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        assert args[:2] == ("vm", "create")
        assert kwargs["timeout"] == 1800
        events.append("create")
        if create_fails:
            raise RuntimeError("create refused")
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    def stop_after_create(_name: str) -> None:
        events.append("install")
        raise RuntimeError("stop after creation")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(orchestrator, "_install_system_binary_in_runner", stop_after_create)

    with pytest.raises(RuntimeError, match="create refused|stop after creation"):
        getattr(orchestrator, provision_name)(
            "focused-runner", "0.3.0-test", lambda: events.append("owned")
        )

    assert events == (["create"] if create_fails else ["create", "owned", "install"])


def test_streamed_mvm_failure_preserves_command_and_rc_when_output_is_none(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(orchestrator, "MVM_BINARY", "/outer/controller-mvm")

    def failed_stream(
        command: list[str], **kwargs: object
    ) -> SimpleNamespace:
        assert kwargs["stdout"] is None and kwargs["stderr"] is None
        return SimpleNamespace(returncode=23, wait=lambda **_kwargs: 23)

    monkeypatch.setattr(orchestrator.subprocess, "Popen", failed_stream)

    with pytest.raises(RuntimeError) as captured:
        orchestrator.mvm(
            "image",
            "pull",
            "ubuntu-minimal:24.04",
            capture=False,
        )

    message = str(captured.value)
    assert (
        "mvm command failed: /outer/controller-mvm image pull "
        "ubuntu-minimal:24.04"
    ) in message
    assert "rc: 23" in message
    assert "stderr: (streamed; not captured)" in message


def _configure_interruptible_mvm_cli(orchestrator, monkeypatch, tmp_path, body):
    ready = tmp_path / "cli-ready"
    script = (
        "import os,signal,sys,time\nfrom pathlib import Path\n"
        + body
        + f"\nPath({str(ready)!r}).write_text(str(os.getpid()))\n"
        + "print('before-out', flush=True)\nprint('before-err', file=sys.stderr, flush=True)\n"
        + "while True: time.sleep(0.05)\n"
    )
    monkeypatch.setattr(orchestrator, "MVM_BINARY", shlex.join([sys.executable, "-u", "-c", script]))
    popen = subprocess.Popen

    def start_cli(*args, **kwargs):
        process = popen(*args, **kwargs)
        deadline = time.monotonic() + 5
        while not ready.exists() and process.poll() is None and time.monotonic() < deadline:
            time.sleep(0.01)
        assert ready.exists(), "lightweight CLI did not install its signal handler"
        return process

    monkeypatch.setattr(orchestrator.subprocess, "Popen", start_cli)
    return ready


@pytest.mark.parametrize("check", [True, False])
def test_mvm_timeout_interrupts_owned_caller_and_retains_shutdown_streams(
    orchestrator, monkeypatch, tmp_path, check,
):
    ready = _configure_interruptible_mvm_cli(
        orchestrator, monkeypatch, tmp_path,
        "def interrupted(sig, frame):\n"
        "    print('after-out', flush=True)\n"
        "    print('after-err', file=sys.stderr, flush=True)\n"
        "    sys.exit(0)\nsignal.signal(signal.SIGINT, interrupted)\n",
    )
    with pytest.raises(subprocess.TimeoutExpired) as captured:
        orchestrator.mvm("create", timeout=0.1, check=check)
    error = captured.value
    assert error.returncode == 0
    assert error.stdout == "before-out\nafter-out\n"
    assert error.stderr == "before-err\nafter-err\n"
    assert error.cleanup_errors == []
    assert "timed out" in str(error) and "after-err" in str(error)
    with pytest.raises(ChildProcessError):
        os.waitpid(int(ready.read_text()), os.WNOHANG)


def test_mvm_timeout_kills_only_ignored_interrupt_caller_after_grace(
    orchestrator, monkeypatch, tmp_path,
):
    ready = _configure_interruptible_mvm_cli(
        orchestrator, monkeypatch, tmp_path, "signal.signal(signal.SIGINT, signal.SIG_IGN)\n",
    )
    start_cli = orchestrator.subprocess.Popen

    def shorten_waits(*args, **kwargs):
        process = start_cli(*args, **kwargs)
        wait = process.wait
        process.wait = lambda timeout: wait(timeout=min(timeout, 0.1))
        return process

    monkeypatch.setattr(orchestrator.subprocess, "Popen", shorten_waits)
    with pytest.raises(subprocess.TimeoutExpired) as captured:
        orchestrator.mvm("create", timeout=0.1)
    assert captured.value.returncode == -signal.SIGKILL
    assert captured.value.stdout == "before-out\n"
    assert captured.value.stderr == "before-err\n"
    assert captured.value.cleanup_errors == []
    with pytest.raises(ChildProcessError):
        os.waitpid(int(ready.read_text()), os.WNOHANG)


def test_mvm_timeout_keeps_descendant_alive_without_waiting_for_pipe_eof(
    orchestrator, monkeypatch, tmp_path,
):
    descendant = tmp_path / "descendant"
    ready = _configure_interruptible_mvm_cli(
        orchestrator, monkeypatch, tmp_path,
        "def interrupted(sig, frame):\n"
        "    print('after-out', flush=True)\n    sys.exit(0)\n"
        "signal.signal(signal.SIGINT, interrupted)\n"
        "child = os.fork()\n"
        "if child == 0:\n"
        "    signal.signal(signal.SIGINT, signal.SIG_IGN)\n"
        "    time.sleep(10)\n    os._exit(0)\n"
        + f"Path({str(descendant)!r}).write_text(str(child))\n",
    )
    pidfd = None
    try:
        with pytest.raises(subprocess.TimeoutExpired) as captured:
            orchestrator.mvm("create", timeout=0.1)
        assert captured.value.returncode == 0
        assert captured.value.stdout == "before-out\nafter-out\n"
        assert captured.value.stderr == "before-err\n"
        pidfd = os.pidfd_open(int(descendant.read_text()))
        poller = select.poll()
        poller.register(pidfd, select.POLLIN)
        assert poller.poll(0) == [], "caller timeout killed its unrelated descendant"
        with pytest.raises(ChildProcessError):
            os.waitpid(int(ready.read_text()), os.WNOHANG)
    finally:
        if pidfd is None and descendant.exists():
            pidfd = os.pidfd_open(int(descendant.read_text()))
        if pidfd is not None:
            signal.pidfd_send_signal(pidfd, signal.SIGKILL)
            poller = select.poll()
            poller.register(pidfd, select.POLLIN)
            assert poller.poll(2000), "owned lightweight descendant did not exit"
            os.close(pidfd)


def test_mvm_pidfd_failure_preserves_timeout_and_cooperative_output(
    orchestrator, monkeypatch, tmp_path,
):
    _configure_interruptible_mvm_cli(
        orchestrator, monkeypatch, tmp_path,
        "signal.signal(signal.SIGINT, lambda sig, frame: sys.exit(0))\n",
    )

    def refuse_pidfd(_pid):
        raise OSError("synthetic pidfd refusal")

    monkeypatch.setattr(orchestrator.os, "pidfd_open", refuse_pidfd)
    with pytest.raises(subprocess.TimeoutExpired) as captured:
        orchestrator.mvm("create", timeout=0.1)
    error = captured.value
    assert error.returncode == 0
    assert error.stdout == "before-out\n" and error.stderr == "before-err\n"
    assert "synthetic pidfd refusal" in str(error)


def test_mvm_timeout_retains_signal_and_reap_failures_without_claiming_exit(
    orchestrator, monkeypatch,
):
    signals = []

    def refused(*_args, **_kwargs):
        raise OSError("synthetic shutdown refusal")

    def start_cli(command, **kwargs):
        kwargs["stdout"].write("primary-out\n")
        kwargs["stdout"].flush()
        kwargs["stderr"].write("primary-err\n")
        kwargs["stderr"].flush()

        def wait(timeout):
            if not signals:
                raise subprocess.TimeoutExpired(command, timeout)
            raise OSError("synthetic reap failure")

        def send(sig):
            signals.append(sig)
            raise OSError("synthetic signal failure")

        return SimpleNamespace(pid=123, returncode=None, poll=lambda: None, wait=wait, send_signal=send)

    monkeypatch.setattr(orchestrator.subprocess, "Popen", start_cli)
    monkeypatch.setattr(orchestrator.os, "pidfd_open", refused)
    with pytest.raises(subprocess.TimeoutExpired) as captured:
        orchestrator.mvm("create", timeout=0.1)
    assert signals == [signal.SIGINT, signal.SIGKILL]
    assert captured.value.returncode is None
    assert captured.value.stdout == "primary-out\n" and captured.value.stderr == "primary-err\n"
    for message in ("primary-out", "primary-err", "shutdown refusal", "signal failure", "reap failure"):
        assert message in str(captured.value)


def test_mvm_fast_command_preserves_argv_environment_and_needs_no_pidfd(
    orchestrator, monkeypatch,
):
    script = "import json,os,sys; print(json.dumps([sys.argv[1:],os.environ['NO_COLOR'],os.environ['MVM_ASSET_MIRROR']]))"
    monkeypatch.setattr(orchestrator, "MVM_BINARY", shlex.join([sys.executable, "-c", script]))
    monkeypatch.setenv("MVM_ASSET_MIRROR", "/explicit/mirror")

    def unexpected(_pid):
        raise AssertionError("a completed command must not require pidfd acquisition")

    monkeypatch.setattr(orchestrator.os, "pidfd_open", unexpected)
    result = orchestrator.mvm("arg with spaces", "--json", timeout=5)
    assert result.returncode == 0 and result.stderr == ""
    assert json.loads(result.stdout) == [["arg with spaces", "--json"], "1", "/explicit/mirror"]


def test_tier3_domain_requires_complete_local_pytest_outcomes(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    report_directories: list[Path] = []

    def fake_pytest(
        test_files: list[str],
        *,
        timeout: int,
        extra_env: dict[str, str],
    ) -> subprocess.CompletedProcess[str]:
        assert test_files == ["tests/system/env/test_env.py"]
        assert timeout == 600
        assert "MVM_TEST_QUALIFICATION_HOST" not in extra_env
        report_path = Path(extra_env[orchestrator.PYTEST_OUTCOME_REPORT_ENV])
        report_directories.append(report_path.parent)
        report_path.write_bytes(_complete_pytest_outcome_report(collected=2))
        return subprocess.CompletedProcess(
            ["pytest"],
            0,
            stdout="pytest stdout\n",
            stderr="pytest stderr\n",
        )

    monkeypatch.setattr(orchestrator, "_run_pytest", fake_pytest)

    result = orchestrator.run_tier3_domain(
        "env",
        ["tests/system/env/test_env.py"],
    )

    assert result == {
        "domain": "env",
        "tier": 3,
        "passed": True,
        "output": "pytest stdout\npytest stderr\n",
    }
    assert len(report_directories) == 1
    assert report_directories[0].name.startswith("mvmctl-pytest-env-")
    assert not report_directories[0].exists()


@pytest.mark.parametrize(
    ("report_payload", "expected_reason"),
    [
        (None, "outcome report is missing"),
        (b"{", "malformed JSON"),
        (
            json.dumps(
                {
                    "schema_version": 1,
                    "collected": 1,
                    "passed": 0,
                    "failed": 0,
                    "errors": 0,
                    "collection_errors": 0,
                    "deselected": 0,
                    "skipped": 1,
                    "xfailed": 0,
                    "xpassed": 0,
                    "exit_status": 0,
                }
            ).encode(),
            "skipped=1",
        ),
    ],
    ids=["missing", "corrupt", "skip"],
)
def test_tier3_domain_preserves_pytest_output_and_report_failure_reason(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    report_payload: bytes | None,
    expected_reason: str,
) -> None:
    report_directories: list[Path] = []

    def fake_pytest(
        _test_files: list[str],
        *,
        timeout: int,
        extra_env: dict[str, str],
    ) -> subprocess.CompletedProcess[str]:
        assert timeout == 600
        report_path = Path(extra_env[orchestrator.PYTEST_OUTCOME_REPORT_ENV])
        report_directories.append(report_path.parent)
        if report_payload is not None:
            report_path.write_bytes(report_payload)
        return subprocess.CompletedProcess(
            ["pytest"],
            0,
            stdout="useful pytest stdout\n",
            stderr="useful pytest stderr\n",
        )

    monkeypatch.setattr(orchestrator, "_run_pytest", fake_pytest)

    result = orchestrator.run_tier3_domain(
        "env",
        ["tests/system/env/test_env.py"],
    )

    assert result["passed"] is False
    assert "useful pytest stdout" in result["output"]
    assert "useful pytest stderr" in result["output"]
    assert "pytest outcome validation failed" in result["output"]
    assert expected_reason in result["output"]
    assert len(report_directories) == 1
    assert not report_directories[0].exists()


def test_tier3_domain_preserves_pytest_output_when_report_cleanup_fails(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    report_directory = tmp_path / "mvmctl-pytest-env-fixed"
    report_directory.mkdir()

    class FailingTemporaryDirectory:
        name = str(report_directory)

        def __init__(self, *, prefix: str) -> None:
            assert prefix == "mvmctl-pytest-env-"

        def cleanup(self) -> None:
            raise OSError("synthetic local cleanup refusal")

    def fake_pytest(
        _test_files: list[str],
        *,
        timeout: int,
        extra_env: dict[str, str],
    ) -> subprocess.CompletedProcess[str]:
        assert timeout == 600
        Path(extra_env[orchestrator.PYTEST_OUTCOME_REPORT_ENV]).write_bytes(
            _complete_pytest_outcome_report()
        )
        return subprocess.CompletedProcess(
            ["pytest"],
            0,
            stdout="useful pytest stdout\n",
            stderr="useful pytest stderr\n",
        )

    monkeypatch.setattr(
        orchestrator.tempfile,
        "TemporaryDirectory",
        FailingTemporaryDirectory,
    )
    monkeypatch.setattr(orchestrator, "_run_pytest", fake_pytest)

    result = orchestrator.run_tier3_domain(
        "env",
        ["tests/system/env/test_env.py"],
    )

    assert result["passed"] is False
    assert "useful pytest stdout" in result["output"]
    assert "useful pytest stderr" in result["output"]
    assert "local outcome report cleanup failed" in result["output"]
    assert "synthetic local cleanup refusal" in result["output"]


@pytest.mark.parametrize(
    ("runner_name", "provision_name", "tier", "pytest_timeout"),
    [
        ("run_tier1_domain", "provision_t1", 1, 660),
        ("run_tier2_domain", "provision_t2", 2, 960),
    ],
    ids=["tier1", "tier2"],
)
def test_remote_domains_require_retrieved_complete_pytest_outcomes(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    runner_name: str,
    provision_name: str,
    tier: int,
    pytest_timeout: int,
) -> None:
    vm_name = f"t{tier}-cli-fixed"
    monkeypatch.setenv("MVM_TEST_VM", "unrelated-runner")
    monkeypatch.setenv("MVM_TEST_QUALIFICATION_HOST", "unrelated-runner")
    events: list[str] = []
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []
    monkeypatch.setattr(orchestrator, "_unique_name", lambda _prefix: vm_name)
    monkeypatch.setattr(
        orchestrator,
        provision_name,
        lambda provisioned, version, on_created: (
            on_created(), events.append(f"provision:{provisioned}:{version}")
        ),
    )
    monkeypatch.setattr(
        orchestrator,
        "destroy_vm",
        lambda destroyed: events.append(f"destroy:{destroyed}"),
    )

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        command = args[-1]
        if "python3 -m pytest" in command:
            events.append("pytest")
            return subprocess.CompletedProcess(
                args,
                0,
                stdout="pytest stdout\n",
                stderr="pytest stderr\n",
            )
        if command.startswith("head -c "):
            events.append("retrieve")
            return subprocess.CompletedProcess(
                args,
                0,
                stdout=_complete_pytest_outcome_report().decode(),
                stderr="",
            )
        if command.startswith("rm -f -- "):
            events.append("cleanup")
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")
        raise AssertionError(f"unexpected mvm call: {args}")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    runner = getattr(orchestrator, runner_name)
    result = runner(
        "cli",
        ["tests/system/cli/test_cli.py"],
        "0.3.0",
    )

    assert result == {
        "domain": "cli",
        "tier": tier,
        "passed": True,
        "output": "pytest stdout\npytest stderr\n",
    }
    report_path = f"/tmp/mvmctl-pytest-outcomes-{vm_name}.json"
    pytest_args, pytest_kwargs = calls[0]
    assert pytest_args[:2] == ("exec", vm_name)
    pytest_command = shlex.split(pytest_args[-1])
    assert f"MVM_TEST_VM={vm_name}" in pytest_command
    assert f"MVM_TEST_QUALIFICATION_HOST={vm_name}" in pytest_command
    assert f"MVM_PYTEST_OUTCOME_REPORT={report_path}" in pytest_args[-1]
    assert "/tests/system/cli/test_cli.py" in pytest_args[-1]
    assert pytest_kwargs["timeout"] == pytest_timeout
    retrieve_args, retrieve_kwargs = calls[1]
    assert retrieve_args[-1] == f"head -c 4097 -- {report_path}"
    assert retrieve_kwargs["check"] is False
    cleanup_args, cleanup_kwargs = calls[2]
    assert cleanup_args[-1] == (
        f"rm -f -- {report_path} && test ! -e {report_path}"
    )
    assert cleanup_kwargs["check"] is True
    assert events == [
        f"provision:{vm_name}:0.3.0",
        "pytest",
        "retrieve",
        "cleanup",
        f"destroy:{vm_name}",
    ]


@pytest.mark.parametrize("boundary", ["network", "image-import", "vm-crash"])
@pytest.mark.parametrize(
    ("test_vm", "qualification_host"),
    [
        ("t1-network-fixed", None),
        (None, "t1-network-fixed"),
        ("", ""),
        ("t1-network-fixed", "unrelated-runner"),
    ],
    ids=["missing-admission", "missing-runner", "empty", "mismatched"],
)
def test_administrator_faults_require_explicit_matching_qualification_host(
    monkeypatch: pytest.MonkeyPatch,
    boundary: str,
    test_vm: str | None,
    qualification_host: str | None,
) -> None:
    from tests.system.images.test_images import _VMImportReceiverDeath
    from tests.system.network.test_network import TestNetworkSyncMissingBridge
    from tests.system.vm.test_vm_lifecycle import _kill_exact_test_firecracker

    for name, value in (
        ("MVM_TEST_VM", test_vm),
        ("MVM_TEST_QUALIFICATION_HOST", qualification_host),
    ):
        if value is None:
            monkeypatch.delenv(name, raising=False)
        else:
            monkeypatch.setenv(name, value)

    def unexpected_fault(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("administrator fault subprocess started before admission")

    monkeypatch.setattr(subprocess, "run", unexpected_fault)
    monkeypatch.setattr(subprocess, "Popen", unexpected_fault)

    with pytest.raises(
        AssertionError, match="explicit exact qualification-host admission"
    ):
        if boundary == "network":
            TestNetworkSyncMissingBridge().test_sync_refuses_missing_managed_bridge(
                SimpleNamespace(runner_vm="t1-network-fixed")
            )
        elif boundary == "image-import":
            _VMImportReceiverDeath("t1-network-fixed", {})
        else:
            _kill_exact_test_firecracker(1, 1, "0" * 32)


@pytest.mark.parametrize(
    ("report_payload", "retrieve_rc", "cleanup_fails", "expected_reason"),
    [
        ("", 1, False, "outcome report is missing"),
        ("{", 0, False, "malformed JSON"),
        (
            _complete_pytest_outcome_report().decode(),
            0,
            True,
            "remote outcome report cleanup failed",
        ),
    ],
    ids=["missing", "corrupt", "cleanup"],
)
def test_remote_domain_preserves_pytest_output_and_checked_cleanup_failure(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    report_payload: str,
    retrieve_rc: int,
    cleanup_fails: bool,
    expected_reason: str,
) -> None:
    vm_name = "t1-cli-fixed"
    destroyed: list[str] = []
    monkeypatch.setattr(orchestrator, "_unique_name", lambda _prefix: vm_name)
    monkeypatch.setattr(
        orchestrator,
        "provision_t1",
        lambda _vm_name, _version, on_created: on_created(),
    )
    monkeypatch.setattr(orchestrator, "destroy_vm", destroyed.append)

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        command = args[-1]
        if "python3 -m pytest" in command:
            return subprocess.CompletedProcess(
                args,
                0,
                stdout="useful pytest stdout\n",
                stderr="useful pytest stderr\n",
            )
        if command.startswith("head -c "):
            return subprocess.CompletedProcess(
                args,
                retrieve_rc,
                stdout=report_payload,
                stderr=None if retrieve_rc else "",
            )
        if command.startswith("rm -f -- "):
            if cleanup_fails:
                raise RuntimeError("synthetic cleanup refusal")
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")
        raise AssertionError(f"unexpected mvm call: {args}")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    result = orchestrator.run_tier1_domain(
        "cli",
        ["tests/system/cli/test_cli.py"],
        "0.3.0",
    )

    assert result["passed"] is False
    assert "useful pytest stdout" in result["output"]
    assert "useful pytest stderr" in result["output"]
    assert "pytest outcome validation failed" in result["output"]
    assert expected_reason in result["output"]
    assert destroyed == [vm_name]


def test_destroy_vm_requires_success(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    orchestrator.destroy_vm("t1-cli-fixed")

    assert calls == [
        (
            ("vm", "rm", "t1-cli-fixed", "--force"),
            {"timeout": 60, "check": True},
        )
    ]


@pytest.mark.parametrize(
    ("runner_name", "provision_name", "tier"),
    [
        ("run_tier1_domain", "provision_t1", 1),
        ("run_tier2_domain", "provision_t2", 2),
    ],
    ids=["tier1", "tier2"],
)
def test_remote_domain_destruction_failure_overrides_success(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    runner_name: str,
    provision_name: str,
    tier: int,
) -> None:
    vm_name = f"t{tier}-cli-fixed"
    events: list[str] = []
    monkeypatch.setattr(orchestrator, "_unique_name", lambda _prefix: vm_name)
    monkeypatch.setattr(
        orchestrator,
        provision_name,
        lambda provisioned, version, on_created: (
            on_created(), events.append(f"provision:{provisioned}:{version}")
        ),
    )

    def fake_pytest(
        tested_vm: str,
        _test_files: list[str],
        *,
        timeout: int,
    ) -> tuple[subprocess.CompletedProcess[str], None]:
        events.append(f"pytest:{tested_vm}:{timeout}")
        return (
            subprocess.CompletedProcess(
                ["pytest"],
                0,
                stdout="useful pytest stdout\n",
                stderr="",
            ),
            None,
        )

    def fail_destroy(destroyed: str) -> None:
        events.append(f"destroy:{destroyed}")
        raise RuntimeError("synthetic runner destruction refusal")

    monkeypatch.setattr(orchestrator, "_run_remote_pytest", fake_pytest)
    monkeypatch.setattr(orchestrator, "destroy_vm", fail_destroy)

    result = getattr(orchestrator, runner_name)(
        "cli",
        ["tests/system/cli/test_cli.py"],
        "0.3.0",
    )

    assert result["passed"] is False
    assert "useful pytest stdout" in result["output"]
    assert "runner VM destruction failed" in result["output"]
    assert "synthetic runner destruction refusal" in result["output"]
    assert events == [
        f"provision:{vm_name}:0.3.0",
        f"pytest:{vm_name}:{660 if tier == 1 else 960}",
        f"destroy:{vm_name}",
    ]


@pytest.mark.parametrize(
    ("runner_name", "provision_name", "tier"),
    [
        ("run_tier1_domain", "provision_t1", 1),
        ("run_tier2_domain", "provision_t2", 2),
    ],
    ids=["tier1", "tier2"],
)
def test_remote_domain_preserves_primary_failure_when_destruction_fails(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    runner_name: str,
    provision_name: str,
    tier: int,
) -> None:
    vm_name = f"t{tier}-cli-fixed"
    events: list[str] = []
    monkeypatch.setattr(orchestrator, "_unique_name", lambda _prefix: vm_name)
    monkeypatch.setattr(
        orchestrator,
        provision_name,
        lambda provisioned, version, on_created: (
            on_created(), events.append(f"provision:{provisioned}:{version}")
        ),
    )

    def fail_pytest(
        tested_vm: str,
        _test_files: list[str],
        *,
        timeout: int,
    ) -> tuple[subprocess.CompletedProcess[str], str | None]:
        events.append(f"pytest:{tested_vm}:{timeout}")
        raise RuntimeError("synthetic primary pytest failure")

    def fail_destroy(destroyed: str) -> None:
        events.append(f"destroy:{destroyed}")
        raise RuntimeError("synthetic runner destruction refusal")

    monkeypatch.setattr(orchestrator, "_run_remote_pytest", fail_pytest)
    monkeypatch.setattr(orchestrator, "destroy_vm", fail_destroy)

    result = getattr(orchestrator, runner_name)(
        "cli",
        ["tests/system/cli/test_cli.py"],
        "0.3.0",
    )

    assert result["passed"] is False
    assert "synthetic primary pytest failure" in result["output"]
    assert "runner VM destruction failed" in result["output"]
    assert "synthetic runner destruction refusal" in result["output"]
    assert events == [
        f"provision:{vm_name}:0.3.0",
        f"pytest:{vm_name}:{660 if tier == 1 else 960}",
        f"destroy:{vm_name}",
    ]


@pytest.mark.parametrize(
    ("runner_name", "provision_name"),
    [
        ("run_tier1_domain", "provision_t1"),
        ("run_tier2_domain", "provision_t2"),
    ],
    ids=["tier1", "tier2"],
)
@pytest.mark.parametrize("created", [False, True], ids=["before-create", "after-create"])
def test_remote_domain_destroys_only_a_created_runner(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    runner_name: str,
    provision_name: str,
    created: bool,
) -> None:
    destroyed: list[str] = []
    monkeypatch.setattr(orchestrator, "_unique_name", lambda _prefix: "fixed-runner")

    def fail_provision(
        _vm_name: str, _version: str, on_created: Callable[[], None]
    ) -> None:
        if created:
            on_created()
        raise RuntimeError("synthetic provision failure")

    monkeypatch.setattr(orchestrator, provision_name, fail_provision)
    monkeypatch.setattr(orchestrator, "destroy_vm", destroyed.append)

    result = getattr(orchestrator, runner_name)("cli", ["tests/system/cli/test_cli.py"], "0.3.0")

    assert result["passed"] is False
    assert result["output"] == "synthetic provision failure"
    assert destroyed == (["fixed-runner"] if created else [])


def test_tier2_completion_wait_covers_provisioning_pytest_and_cleanup(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    lifecycle_seconds = 4200 + 960 + 180
    worker_started = threading.Event()
    release_worker = threading.Event()
    real_wait = concurrent.futures.wait
    observed_timeouts: list[int] = []

    def runner(domain: str, _files: list[str], _version: str, _push: bool) -> dict[str, object]:
        worker_started.set()
        assert release_worker.wait(timeout=2)
        return {"domain": domain, "tier": 2, "passed": True, "output": "lifecycle cleanup complete"}

    def lifecycle_wait(futures, *, timeout: int, return_when: str):
        observed_timeouts.append(timeout)
        assert return_when == concurrent.futures.FIRST_COMPLETED
        assert worker_started.wait(timeout=2)
        release_worker.set()
        if timeout < lifecycle_seconds:
            return set(), set(futures)
        return real_wait(futures, timeout=2, return_when=return_when)

    monkeypatch.setattr(concurrent.futures, "wait", lifecycle_wait)

    results = orchestrator.run_domains(
        {"vm_lifecycle": ["tests/system/vm/test_vm_lifecycle.py"]},
        2,
        runner,
        1,
        "0.3.0",
    )

    assert observed_timeouts == [6000]
    assert results == [
        {"domain": "vm_lifecycle", "tier": 2, "passed": True, "output": "lifecycle cleanup complete"}
    ]


def test_timed_out_parallel_worker_is_joined_and_late_failures_are_preserved(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    worker_started = threading.Event()
    release_worker = threading.Event()
    worker_finished = threading.Event()
    timeout_reported = threading.Event()
    started_domains: list[str] = []

    def runner(
        domain: str,
        _test_files: list[str],
        _mvm_version: str,
        _push: bool,
    ) -> dict[str, object]:
        started_domains.append(domain)
        worker_started.set()
        assert release_worker.wait(timeout=2)
        worker_finished.set()
        return {
            "domain": domain,
            "tier": 1,
            "passed": False,
            "output": (
                "late primary pytest failure\n"
                "runner VM destruction failed: late cleanup refusal"
            ),
        }

    def force_timeout(
        futures: set[concurrent.futures.Future[dict[str, object]]],
        *,
        timeout: int,
        return_when: str,
    ) -> tuple[set[concurrent.futures.Future[dict[str, object]]], set[concurrent.futures.Future[dict[str, object]]]]:
        assert timeout == 900
        assert return_when == concurrent.futures.FIRST_COMPLETED
        assert worker_started.wait(timeout=2)
        return set(), set(futures)

    def capture_log(message: str) -> None:
        if "[TIMEOUT]" in message:
            timeout_reported.set()

    monkeypatch.setattr(concurrent.futures, "wait", force_timeout)
    monkeypatch.setattr(orchestrator, "log", capture_log)

    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as caller_pool:
        invocation = caller_pool.submit(
            orchestrator.run_domains,
            {"slow": ["slow.py"], "queued": ["queued.py"]},
            1,
            runner,
            1,
            "0.3.0",
        )
        assert timeout_reported.wait(timeout=2)
        assert worker_finished.is_set() is False
        release_worker.set()
        results = invocation.result(timeout=2)

    assert worker_finished.is_set() is True
    assert started_domains == ["slow"]
    by_domain = {result["domain"]: result for result in results}
    assert set(by_domain) == {"queued", "slow"}
    assert by_domain["slow"]["passed"] is False
    assert "Domain timed out after 900s" in by_domain["slow"]["output"]
    assert "late primary pytest failure" in by_domain["slow"]["output"]
    assert "runner VM destruction failed: late cleanup refusal" in by_domain["slow"]["output"]
    assert by_domain["queued"]["passed"] is False
    assert "Domain timed out after 900s" in by_domain["queued"]["output"]
    assert "canceled before it started" in by_domain["queued"]["output"]


def test_timed_out_parallel_worker_preserves_late_exception(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    worker_started = threading.Event()
    release_worker = threading.Event()
    timeout_reported = threading.Event()

    def runner(
        _domain: str,
        _test_files: list[str],
        _mvm_version: str,
        _push: bool,
    ) -> dict[str, object]:
        worker_started.set()
        assert release_worker.wait(timeout=2)
        raise RuntimeError("late worker exception")

    def force_timeout(
        futures: set[concurrent.futures.Future[dict[str, object]]],
        *,
        timeout: int,
        return_when: str,
    ) -> tuple[set[concurrent.futures.Future[dict[str, object]]], set[concurrent.futures.Future[dict[str, object]]]]:
        assert timeout == 900
        assert return_when == concurrent.futures.FIRST_COMPLETED
        assert worker_started.wait(timeout=2)
        return set(), set(futures)

    def capture_log(message: str) -> None:
        if "[TIMEOUT]" in message:
            timeout_reported.set()

    monkeypatch.setattr(concurrent.futures, "wait", force_timeout)
    monkeypatch.setattr(orchestrator, "log", capture_log)

    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as caller_pool:
        invocation = caller_pool.submit(
            orchestrator.run_domains,
            {"slow": ["slow.py"]},
            1,
            runner,
            1,
            "0.3.0",
        )
        assert timeout_reported.wait(timeout=2)
        release_worker.set()
        results = invocation.result(timeout=2)

    assert results == [
        {
            "domain": "slow",
            "tier": 1,
            "passed": False,
            "output": (
                "Domain timed out after 900s\n"
                "Late worker error: late worker exception"
            ),
        }
    ]


def test_builder_assets_are_pulled_in_separate_sequential_execs(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(
            args,
            0,
            stdout="pull completed\n",
            stderr="INFO: root: Using local mirror for download url=https://example.test\n",
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    orchestrator._pull_builder_assets("base-img-builder")

    expected_pull_commands = [
        "MVM_ASSET_MIRROR=/mnt MVM_LOG_LEVEL=INFO "
        "/usr/local/bin/mvm kernel pull "
        "--type firecracker --version v1.15 --default",
        "MVM_ASSET_MIRROR=/mnt MVM_LOG_LEVEL=INFO "
        "/usr/local/bin/mvm image pull alpine:3.23",
        "MVM_ASSET_MIRROR=/mnt MVM_LOG_LEVEL=INFO "
        "/usr/local/bin/mvm image pull "
        "ubuntu-minimal:24.04",
        "MVM_ASSET_MIRROR=/mnt MVM_LOG_LEVEL=INFO "
        "/usr/local/bin/mvm image pull ubuntu "
        "--version 24.04",
    ]
    pull_calls = calls[:-1]
    assert [args[-1] for args, _kwargs in pull_calls] == expected_pull_commands
    assert len(pull_calls) == 4
    assert not any("binary pull firecracker" in args[-1] for args, _ in calls)
    for args, kwargs in pull_calls:
        assert args[:-1] == (
            "exec",
            "base-img-builder",
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
        )
        assert " & " not in args[-1]
        assert "pids=" not in args[-1]
        assert kwargs["capture"] is True
        assert kwargs["check"] is False
        assert kwargs["timeout"] == 600

    verify_args, verify_kwargs = calls[-1]
    assert verify_args[:2] == ("exec", "base-img-builder")
    assert "Verifying cached image integrity" in verify_args[-1]
    assert verify_kwargs == {"timeout": 180, "capture": False}


def test_builder_asset_pull_keeps_dynamic_vm_name_outside_guest_shell(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[str, ...]] = []
    hostile_vm_name = "builder; touch /tmp/runner-command-injection"

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        return subprocess.CompletedProcess(
            args,
            0,
            stdout="",
            stderr="INFO: root: Using local mirror for download url=https://example.test\n",
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    orchestrator._pull_builder_assets(hostile_vm_name)

    for args in calls[:-1]:
        assert args[1] == hostile_vm_name
        assert hostile_vm_name not in args[-1]
        command_tokens = shlex.split(args[-1])
        assert command_tokens[:3] == [
            "MVM_ASSET_MIRROR=/mnt",
            "MVM_LOG_LEVEL=INFO",
            "/usr/local/bin/mvm",
        ]
        assert args[-1] == shlex.join(command_tokens)


def test_builder_asset_pull_rejects_missing_local_mirror_evidence(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(
            args,
            0,
            stdout="pull completed\n",
            stderr="",
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(
        RuntimeError,
        match="did not prove a local mirror artifact read",
    ):
        orchestrator._pull_builder_assets("base-img-builder")


@pytest.mark.parametrize(
    "forbidden_marker",
    [
        "Mirror checksum mismatch, falling back to HTTP download",
        "Copied to asset mirror",
    ],
)
def test_builder_asset_pull_rejects_http_fallback_or_autopopulation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    forbidden_marker: str,
) -> None:
    commands: list[str] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        commands.append(args[-1])
        return subprocess.CompletedProcess(
            args,
            0,
            stdout="pull completed\n",
            stderr=(
                "INFO: root: Using local mirror for download url=https://example.test\n"
                f"WARNING: root: {forbidden_marker} url=https://example.test\n"
            ),
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError) as captured:
        orchestrator._pull_builder_assets("base-img-builder")

    assert forbidden_marker in str(captured.value)
    assert len(commands) == 1


def test_builder_asset_pull_prints_bounded_captured_output(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    huge_stdout = "stdout-start\n" + ("s" * 50_000) + "\nstdout-end\n"
    huge_stderr = (
        "stderr-start\n"
        + ("e" * 50_000)
        + "\nINFO: root: Using local mirror for download url=https://example.test\n"
        + "stderr-end\n"
    )

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(
            args,
            0,
            stdout=huge_stdout,
            stderr=huge_stderr,
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    orchestrator._pull_builder_assets("base-img-builder")

    output = capsys.readouterr().out
    assert "stdout-start" in output
    assert "stderr-end" in output
    assert "builder pull output truncated" in output
    assert len(output) < 40_000


@pytest.mark.parametrize(
    ("returncode", "expected_error"),
    [
        (23, "builder asset pull failed for Firecracker kernel: rc=23"),
        (0, "did not prove a local mirror artifact read"),
    ],
)
def test_builder_asset_pull_handles_missing_captured_streams(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    returncode: int,
    expected_error: str,
) -> None:
    commands: list[str] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        commands.append(args[-1])
        return subprocess.CompletedProcess(
            args,
            returncode,
            stdout=None,
            stderr=None,
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match=expected_error):
        orchestrator._pull_builder_assets("base-img-builder")

    assert len(commands) == 1
    assert "(no captured output)" in capsys.readouterr().out


@pytest.mark.parametrize(
    ("stdout", "stderr"),
    [
        ("Using local mirror for download\n", None),
        (None, "Using local mirror for download\n"),
    ],
)
def test_builder_asset_pull_accepts_mirror_evidence_with_one_missing_stream(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    stdout: str | None,
    stderr: str | None,
) -> None:
    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(
            args,
            0,
            stdout=stdout,
            stderr=stderr,
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    orchestrator._pull_builder_assets("base-img-builder")


def test_builder_asset_pull_preserves_nonzero_failure_output(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(
            args,
            17,
            stdout="partial pull progress\n",
            stderr="download process terminated\n",
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match="rc=17"):
        orchestrator._pull_builder_assets("base-img-builder")

    output = capsys.readouterr().out
    assert "partial pull progress" in output
    assert "download process terminated" in output


def test_builder_asset_pull_stops_after_first_nonzero_result(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        if "ubuntu-minimal:24.04" in args[-1]:
            return subprocess.CompletedProcess(
                args,
                23,
                stdout="partial ubuntu-minimal progress\n",
                stderr="ubuntu-minimal download failed\n",
            )
        return subprocess.CompletedProcess(
            args,
            0,
            stdout="",
            stderr="INFO: root: Using local mirror for download url=https://example.test\n",
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match="rc=23"):
        orchestrator._pull_builder_assets("base-img-builder")

    commands = [args[-1] for args, _kwargs in calls]
    assert commands[-1].endswith("image pull ubuntu-minimal:24.04")
    assert calls[-1][1] == {"timeout": 600, "capture": True, "check": False}
    assert not any("--version 24.04" in command for command in commands)
    assert not any("Verifying cached image integrity" in command for command in commands)
    output = capsys.readouterr().out
    assert "partial ubuntu-minimal progress" in output
    assert "ubuntu-minimal download failed" in output


def test_build_base_image_cleans_builder_when_asset_pull_fails(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    candidate = tmp_path / "mvm-candidate"
    candidate.write_bytes(b"candidate")
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    calls: list[tuple[str, ...]] = []
    dependency_timeouts: list[object] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        if args[0] == "exec" and args[-1].startswith("sudo apt-get update -qq && "):
            dependency_timeouts.append(kwargs["timeout"])
            assert args[args.index("--timeout") + 1] == "10"
        stdout = ""
        if args[:2] == ("image", "ls"):
            stdout = json.dumps({"schema_version": 1, "status": "success", "data": [], "error": None})
        if args[:2] == ("exec", orchestrator.BASE_VM_NAME) and args[-1].startswith("stat -c%s"):
            size = candidate.stat().st_size
            stdout = f"{size}\n{size}\n"
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    original_failure = RuntimeError("asset pull failed")
    initialization_order: list[str] = []

    def record_host_init(_vm_name: str) -> None:
        initialization_order.append("host-init")

    def record_runner_init(
        _vm_name: str,
        *,
        timeout: int,
        capture: bool = True,
    ) -> None:
        assert timeout == 180
        assert capture is False
        initialization_order.append("runner-init-1.17.0")

    def fail_asset_pull(_vm_name: str) -> None:
        initialization_order.append("asset-pulls")
        raise original_failure

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(
        orchestrator, "_install_system_binary_in_runner", lambda _vm_name: None
    )
    monkeypatch.setattr(
        orchestrator, "_initialize_system_binary_in_runner", record_host_init
    )
    monkeypatch.setattr(orchestrator, "_initialize_runner_user", record_runner_init)
    monkeypatch.setattr(orchestrator, "_pull_builder_assets", fail_asset_pull)

    with pytest.raises(RuntimeError) as captured:
        orchestrator._build_base_image("0.3.0-test", rebuild=True)

    assert captured.value is original_failure
    assert dependency_timeouts == [1800]
    assert initialization_order == [
        "host-init",
        "runner-init-1.17.0",
        "asset-pulls",
    ]
    builder_removals = [
        args
        for args in calls
        if args == ("vm", "rm", orchestrator.BASE_VM_NAME, "--force")
    ]
    assert len(builder_removals) == 1
    assert not any(args[:2] == ("image", "import") for args in calls)


def test_build_base_image_does_not_remove_existing_builder_on_create_refusal(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[str, ...]] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        if args[:2] == ("image", "ls"):
            return subprocess.CompletedProcess(
                args, 0,
                stdout=json.dumps({"schema_version": 1, "status": "success", "data": [], "error": None}),
                stderr="",
            )
        if args[:2] == ("vm", "create"):
            raise RuntimeError("builder VM already exists")
        return subprocess.CompletedProcess(args, 1, stdout="", stderr="not found")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match="builder VM already exists"):
        orchestrator._build_base_image("0.3.0-test")

    assert ("vm", "rm", orchestrator.BASE_VM_NAME, "--force") not in calls
    assert not any(args[:2] == ("image", "import") for args in calls)


@pytest.mark.parametrize("imported", [False, True], ids=["stock", "imported"])
@pytest.mark.parametrize(
    "available,rebuild,reuse",
    [
        ("old", False, False),
        ("old", True, False),
        ("exact", False, True),
        ("exact", True, False),
        ("absent", False, False),
        ("missing", False, False),
    ],
)
def test_build_base_image_requires_exact_present_identity(
    orchestrator,
    monkeypatch,
    imported,
    available,
    rebuild,
    reuse,
):
    binding = orchestrator.ImportedRunnerKernelBinding("d" * 64, "e" * 64) if imported else None
    monkeypatch.setattr(orchestrator, "_validate_imported_runner_kernel", lambda _binding: None)
    image_type = orchestrator.BASE_IMAGE_NAME + (f"-kernel-{binding.id}" if binding else "")
    old = {"id": "a" * 64, "type": image_type, "version": "0.3.0-old", "is_present": True}
    older = {**old, "id": "c" * 64, "version": "0.3.0-older"}
    current = {
        "id": "b" * 64, "type": image_type, "version": "0.3.0-current",
        "is_present": available != "absent",
    }
    images = {} if available == "missing" else {old["id"]: old.copy(), older["id"]: older.copy()}
    if available in ("exact", "absent"):
        images[current["id"]] = current.copy()
    removed = []
    builders = []
    builder_reached = RuntimeError("ordinary builder reached")

    def fake_mvm(*args, **kwargs):
        if args[:2] == ("image", "ls"):
            return subprocess.CompletedProcess(
                args, 0,
                stdout=json.dumps({
                    "schema_version": 1, "status": "success",
                    "data": list(images.values()), "error": None,
                }), stderr="",
            )
        if args[:2] == ("image", "inspect"):
            # The public resolver's successful old-version fallback is not exact evidence.
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps({"image": old}), stderr="")
        if args[:2] == ("image", "rm"):
            removed.append(args[2])
            images.pop(args[2])
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")
        if args[:2] == ("vm", "create"):
            builders.append(args[3])  # --writeback precedes the owned builder name.
            raise builder_reached
        raise AssertionError(f"unexpected preparation effect: {args}")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    if reuse:
        assert orchestrator._build_base_image("0.3.0-current", rebuild=rebuild, binding=binding) == "0.3.0-current"
    else:
        with pytest.raises(RuntimeError) as captured:
            orchestrator._build_base_image("0.3.0-current", rebuild=rebuild, binding=binding)
        assert captured.value is builder_reached
    expected_images = {} if available == "missing" else {old["id"]: old, older["id"]: older}
    if available == "absent" or (available == "exact" and not rebuild):
        expected_images[current["id"]] = current
    assert images == expected_images
    assert removed == ([current["id"]] if available == "exact" and rebuild else [])
    assert builders == ([] if reuse else [orchestrator.BASE_VM_NAME])


@pytest.mark.parametrize("rebuild", [False, True], ids=["reuse", "rebuild"])
@pytest.mark.parametrize(
    "reply,returncode,error",
    [
        ("", 0, "invalid JSON"),
        ('{"schema_version":1,"status":"success","data":[],"error":null,"DATA":[]}', 0, "duplicate field"),
        ('{"schema_version":1,"status":"success","data":[],"error":null,"extra":1}', 0, "envelope fields"),
        ('{"schema_version":true,"status":"success","data":[],"error":null}', 0, "unsupported schema"),
        ('{"schema_version":1,"status":"error","data":[],"error":null}', 0, "not successful"),
        ('{"schema_version":1,"status":"success","data":{},"error":null}', 0, "must be a list"),
        ("[]", 0, "envelope fields"),
        ("unavailable", 23, "lookup failed"),
    ],
)
def test_build_base_image_refuses_failed_or_malformed_image_list(
    orchestrator, monkeypatch, rebuild, reply, returncode, error,
):
    effects = []

    def fake_mvm(*args, **kwargs):
        if args[:2] != ("image", "ls"):
            effects.append(args)
        return subprocess.CompletedProcess(args, returncode, stdout=reply, stderr="inventory unavailable")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    with pytest.raises(RuntimeError, match=error):
        orchestrator._build_base_image("0.3.0", rebuild=rebuild)
    assert effects == []


@pytest.mark.parametrize("fault", ["duplicate-version", "duplicate-id", "missing-version", "short-id", "invalid-presence"])
def test_build_base_image_refuses_ambiguous_or_invalid_image_identity(
    orchestrator, monkeypatch, fault,
):
    image = {"id": "a" * 64, "type": orchestrator.BASE_IMAGE_NAME, "version": "0.3.0", "is_present": True}
    images = [image]
    if fault == "duplicate-version":
        images.append({**image, "id": "b" * 64})
    elif fault == "duplicate-id":
        images.append({**image, "version": "0.3.0-old"})
    elif fault == "missing-version":
        del image["version"]
    elif fault == "short-id":
        image["id"] = "a" * 8
    else:
        image["is_present"] = 1
    effects = []

    def fake_mvm(*args, **kwargs):
        if args[:2] != ("image", "ls"):
            effects.append(args)
        return subprocess.CompletedProcess(
            args, 0,
            stdout=json.dumps({"schema_version": 1, "status": "success", "data": images, "error": None}),
            stderr="",
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    with pytest.raises(RuntimeError, match="ambiguous|duplicate image ID|invalid image identity"):
        orchestrator._build_base_image("0.3.0", rebuild=True)
    assert effects == []


@pytest.mark.parametrize("enveloped", [False, True], ids=["legacy", "schema-1"])
@pytest.mark.parametrize("path", ["/cache/volumes/asset-mirror.raw", None, "", 7])
def test_inspect_volume_path_decodes_controller_response(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    enveloped: bool,
    path: object,
) -> None:
    volume = {"status": "available"}
    if path is not None:
        volume["path"] = path
    info = {"volume": volume, "attachment": {}, "disk_info": {}, "timestamps": {}}
    response = (
        {"schema_version": 1, "status": "success", "data": info, "error": None}
        if enveloped
        else info
    )
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout=json.dumps(response))

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    assert orchestrator._inspect_volume_path("asset-mirror") == (
        True,
        path if isinstance(path, str) and path else None,
    )
    assert calls == [
        (
            ("volume", "inspect", "asset-mirror", "--json"),
            {"check": False, "timeout": 30},
        )
    ]


@pytest.mark.parametrize(
    "response, expected_error",
    [
        ("not-json", "invalid JSON"),
        ("[]", "must be an object"),
        ("{}", "must contain a volume object"),
        ('{"volume":[]}', "must contain a volume object"),
        ('{"status":"success","volume":{"path":"/ignored"}}', "incomplete or unknown"),
        (
            '{"schema_version":2,"status":"success","data":{},"error":null}',
            "unsupported schema",
        ),
        (
            '{"schema_version":true,"status":"success","data":{},"error":null}',
            "unsupported schema",
        ),
        (
            '{"schema_version":1,"status":"partial","data":{},"error":null}',
            "not successful",
        ),
        (
            '{"schema_version":1,"status":"error","data":{},"error":null}',
            "not successful",
        ),
        (
            '{"schema_version":1,"status":"success","data":{},"error":{}}',
            "not successful",
        ),
        (
            '{"schema_version":1,"status":"success","data":[],"error":null}',
            "data.*must be an object",
        ),
        (
            '{"schema_version":1,"status":"success","data":{},"error":null}',
            "must contain a volume object",
        ),
        (
            '{"schema_version":1,"status":"success","data":{},"error":null,"extra":1}',
            "incomplete or unknown",
        ),
    ],
)
def test_inspect_volume_path_rejects_malformed_controller_response(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    response: str,
    expected_error: str,
) -> None:
    monkeypatch.setattr(
        orchestrator,
        "mvm",
        lambda *args, **_kwargs: subprocess.CompletedProcess(args, 0, stdout=response),
    )

    with pytest.raises(RuntimeError, match=expected_error):
        orchestrator._inspect_volume_path("asset-mirror")


@pytest.mark.parametrize(
    "response, expected_status",
    [
        ('{"volume":{"status":"attached"}}', "attached"),
        ('{"volume":{}}', "unknown"),
        (
            '{"schema_version":1,"status":"success","data":{"volume":{"status":"available"}},"error":null}',
            "available",
        ),
        (
            '{"schema_version":1,"status":"partial","data":{"volume":{"status":"available"}},"error":null}',
            None,
        ),
    ],
    ids=["legacy-attached", "legacy-missing-status", "schema-1", "refuse-partial"],
)
def test_prepare_decodes_volume_status_and_cleans_up_on_response_refusal(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    response: str,
    expected_status: str | None,
) -> None:
    destroyed: list[str] = []
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0")
    monkeypatch.setattr(orchestrator, "_unique_name", lambda prefix: prefix)
    for helper in (
        "ensure_shared_volume",
        "ensure_test_network",
        "_build_base_image",
        "_initialize_runner_user",
    ):
        monkeypatch.setattr(orchestrator, helper, lambda *_args, **_kwargs: None)
    monkeypatch.setattr(orchestrator, "destroy_vm", destroyed.append)

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        stdout = response if args[:2] == ("volume", "inspect") else ""
        if args[0] == "exec" and "image pull alpine:3.23" in args[-1]:
            stdout = orchestrator.LOCAL_MIRROR_READ_MARKER
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    if expected_status is None:
        with pytest.raises(RuntimeError, match="not successful"):
            orchestrator.run_prepare()
        assert "ALL STEPS PASSED" not in capsys.readouterr().out
    else:
        orchestrator.run_prepare()
        assert f"Volume status: {expected_status}" in capsys.readouterr().out
    assert destroyed == ["prep-t1", "prep-t2"]


_RUNNER_ELF_BUILD_ID = bytes.fromhex("79ee949b4f07aa6b8567741ea49862ab1a4f6b41")


def _minimal_runner_elf(notes: bytes | None = None) -> bytes:
    if notes is None:
        notes = (
            struct.pack("<III", 4, len(_RUNNER_ELF_BUILD_ID), 3)
            + b"GNU\x00"
            + _RUNNER_ELF_BUILD_ID
        )
    header = struct.pack(
        "<16sHHIQQQIHHHHHH",
        b"\x7fELF\x02\x01\x01" + b"\x00" * 9,
        2,
        62,
        1,
        0,
        0,
        64,
        0,
        64,
        0,
        0,
        64,
        2,
        0,
    )
    note_section = struct.pack("<IIQQQQIIQQ", 0, 7, 0, 0, 192, len(notes), 0, 0, 4, 0)
    return header + b"\x00" * 64 + note_section + notes


@pytest.fixture
def imported_runner_kernel(orchestrator, monkeypatch, tmp_path):
    payload = _minimal_runner_elf()
    digest = hashlib.sha256(payload).hexdigest()
    kernel_id = hashlib.sha256(f"{digest}:7.0.11:x86_64".encode()).hexdigest()
    binding = orchestrator.ImportedRunnerKernelBinding(
        kernel_id, digest, build_id=_RUNNER_ELF_BUILD_ID.hex()
    )
    cache = tmp_path / "cache"
    path = cache / "kernels" / kernel_id
    path.parent.mkdir(parents=True)
    path.write_bytes(payload)
    candidate = tmp_path / "candidate"
    candidate.write_bytes(b"candidate bytes")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    info = {
        "kernel": {
            "id": kernel_id,
            "type": "custom",
            "is_present": True,
            "version": "7.0.11",
            "arch": "x86_64",
        },
        "storage": {"path": str(path)},
    }
    return binding, info, path


@pytest.mark.parametrize("fault", [
    None, "source-missing", "source-bytes", "source-metadata", "transfer-missing", "transfer-bytes",
    "import-id", "import-version", "import-arch", "import-default", "inspect-path", "inspect-version",
    "managed-bytes", "changed-default", "removed-existing", "missing-registration",
])
def test_register_imported_runner_kernel_preserves_exact_identity_and_defaults(
    orchestrator, monkeypatch, tmp_path, imported_runner_kernel, fault,
):
    binding, source_info, source = imported_runner_kernel
    if fault == "source-missing":
        source.unlink()
    elif fault == "source-bytes":
        source.write_bytes(b"wrong source")
    elif fault == "source-metadata":
        source_info["kernel"]["arch"] = "aarch64"
    existing = [{"id": "a" * 64, "is_default": True}, {"id": "b" * 64, "is_default": False}]
    rows = [row.copy() for row in existing]
    registered = {"id": binding.id, "type": "custom", "version": binding.version,
                  "arch": binding.arch, "is_present": True, "is_default": False}
    managed_path = f"/home/runner/.cache/mvmctl/kernels/{binding.id}"
    managed = tmp_path / "managed-elf"
    uploads = {}
    calls, proved = [], []

    def envelope(data):
        return json.dumps({"schema_version": 1, "status": "success", "data": data, "error": None})

    def file_identity(vm_name, path, selected):
        assert vm_name == "runner" and selected == binding
        local = managed if path == managed_path else uploads[path]
        actual = orchestrator._imported_runner_elf_identity(local, selected.sha256)
        assert actual == selected.build_id
        proved.append(path)

    def fake_mvm(*args, **kwargs):
        calls.append(args)
        stdout = ""
        if args[:2] == ("kernel", "inspect"):
            stdout = envelope(source_info)
        elif args[0] == "cp":
            assert args[1] == str(source)
            uploaded = args[2].split(":", 1)[1]
            local = tmp_path / "uploaded-elf"
            uploads[uploaded] = local
            if fault != "transfer-missing":
                local.write_bytes(b"wrong transfer" if fault == "transfer-bytes" else source.read_bytes())
        elif args[0] == "exec":
            assert args[args.index("--user") + 1] in {"runner", "root"}
            command = shlex.split(args[-1])
            if command[:3] == ["/usr/local/bin/mvm", "kernel", "ls"]:
                stdout = envelope(rows)
            elif command[:3] == ["/usr/local/bin/mvm", "kernel", "import"]:
                assert args[args.index("--user") + 1] == "runner"
                assert "--default" not in command and "--force" not in command
                assert command[command.index("--version") + 1] == binding.version
                result = registered.copy()
                changed = {"import-id": ("id", "c" * 64), "import-version": ("version", "6.1.155"),
                           "import-arch": ("arch", "aarch64"), "import-default": ("is_default", True)}
                if fault in changed:
                    field, value = changed[fault]
                    result[field] = value
                rows.append(registered.copy())
                managed.write_bytes(b"wrong managed" if fault == "managed-bytes" else source.read_bytes())
                if fault == "changed-default":
                    rows[0]["is_default"] = False
                elif fault == "removed-existing":
                    rows.pop(1)
                elif fault == "missing-registration":
                    rows.pop()
                stdout = envelope(result)
            elif command[:3] == ["/usr/local/bin/mvm", "kernel", "inspect"]:
                info = {"kernel": registered.copy(), "storage": {"path": managed_path}}
                if fault == "inspect-path":
                    info["storage"]["path"] = "/outside-managed-cache"
                elif fault == "inspect-version":
                    info["kernel"]["version"] = "6.1.155"
                stdout = envelope(info)
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(orchestrator, "_runner_kernel_file_identity", file_identity)
    if fault is not None:
        with pytest.raises(RuntimeError):
            orchestrator._register_imported_runner_kernel("runner", binding)
        if fault.startswith("source-"):
            assert calls == [("kernel", "inspect", binding.id, "--json")]
        if fault.startswith("transfer-"):
            assert not any(call[0] == "exec" and "kernel import" in call[-1] for call in calls)
    else:
        orchestrator._register_imported_runner_kernel("runner", binding)
        assert rows == [*existing, registered]
        assert len(proved) == 2 and proved[-1] == managed_path
        assert len([call for call in calls if call[0] == "cp"]) == 1
        # Reuse validates existing bytes and metadata without importing or changing defaults.
        calls.clear()
        proved.clear()
        orchestrator._register_imported_runner_kernel("runner", binding)
        assert proved == [managed_path] and rows == [*existing, registered]
        assert not any(call[0] == "cp" or "kernel import" in call[-1] for call in calls)
    assert not any(call[:2] in {("kernel", "pull"), ("kernel", "default"), ("kernel", "rm")}
                   or (call[0] == "exec" and "kernel rm" in call[-1]) for call in calls)


def test_runner_kernel_file_identity_requires_exact_admitted_build_id(
    orchestrator, monkeypatch, imported_runner_kernel,
):
    binding, _info, _path = imported_runner_kernel
    monkeypatch.setattr(orchestrator, "mvm", lambda *args, **kwargs:
                        subprocess.CompletedProcess(args, 0, stdout="b" * 40 + "\n", stderr=""))
    with pytest.raises(RuntimeError, match="GNU build ID"):
        orchestrator._runner_kernel_file_identity("runner", "/home/runner/owned-upload/vmlinux", binding)


@pytest.mark.parametrize("enveloped", [False, True])
def test_imported_runner_binding_resolves_exact_existing_record(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    enveloped,
):
    binding, info, _path = imported_runner_kernel
    response = (
        {"schema_version": 1, "status": "success", "data": info, "error": None}
        if enveloped
        else info
    )
    calls = []

    def fake_mvm(*args, **_kwargs):
        calls.append(args)
        return subprocess.CompletedProcess(args, 0, stdout=json.dumps(response))

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_ID", binding.id)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_SHA256", binding.sha256)
    assert orchestrator._resolve_imported_runner_kernel_binding() == binding
    assert calls == [("kernel", "inspect", binding.id, "--json")]


@pytest.mark.parametrize(
    "kernel_id,digest",
    [
        (None, "a" * 64),
        ("a" * 64, None),
        ("", "a" * 64),
        ("x", "a" * 64),
        ("A" * 64, "a" * 64),
    ],
)
def test_imported_runner_pair_refused_before_effects(
    orchestrator, monkeypatch, kernel_id, digest
):
    for name, value in (
        ("MVM_RUNNER_KERNEL_ID", kernel_id),
        ("MVM_RUNNER_KERNEL_SHA256", digest),
    ):
        if value is None:
            monkeypatch.delenv(name, raising=False)
        else:
            monkeypatch.setenv(name, value)
    calls = []
    monkeypatch.setattr(orchestrator, "mvm", lambda *args, **kwargs: calls.append(args))
    with pytest.raises(RuntimeError):
        orchestrator._resolve_imported_runner_kernel_binding()
    assert calls == []


@pytest.mark.parametrize(
    "fault",
    [
        "missing",
        "type",
        "present",
        "version",
        "arch",
        "id",
        "digest",
        "id-hash",
        "path",
        "symlink",
        "parent-symlink",
    ],
)
def test_imported_runner_validation_refuses_stale_or_wrong_import_without_fallback(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    fault,
):
    binding, info, path = imported_runner_kernel
    if fault in {"type", "present", "version", "arch", "id"}:
        field = "is_present" if fault == "present" else fault
        info["kernel"][field] = {
            "type": "official",
            "present": False,
            "version": "7.0.12",
            "arch": "aarch64",
            "id": "b" * 64,
        }[fault]
    elif fault == "digest":
        path.write_bytes(b"changed bytes")
    elif fault == "id-hash":
        wrong = "b" * 64
        path.rename(path.parent / wrong)
        info["storage"]["path"] = str(path.parent / wrong)
        info["kernel"]["id"] = wrong
        binding = orchestrator.ImportedRunnerKernelBinding(wrong, binding.sha256)
    elif fault == "path":
        info["storage"]["path"] = str(path.parent / "unrelated")
    elif fault == "symlink":
        retained = path.with_suffix(".retained")
        path.rename(retained)
        path.symlink_to(retained)
    elif fault == "parent-symlink":
        retained = path.parent.with_name("retained-kernels")
        path.parent.rename(retained)
        path.parent.symlink_to(retained, target_is_directory=True)
    calls = []

    def fake_mvm(*args, **kwargs):
        calls.append(args)
        return subprocess.CompletedProcess(
            args, 1 if fault == "missing" else 0, stdout=json.dumps(info)
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(
        orchestrator, "_ensure_builder_key", lambda: calls.append(("key-effect",))
    )
    with pytest.raises(RuntimeError):
        orchestrator.provision_t1("runner", "0.3.0", lambda: None, binding=binding)
    assert calls == [("kernel", "inspect", binding.id, "--json")]


@pytest.mark.parametrize(
    "response",
    [
        "not-json",
        "[]",
        '{"kernel":{},"Kernel":{}}',
        '{"schema_version":true,"status":"success","data":{},"error":null}',
        '{"schema_version":1,"status":"partial","data":{},"error":null}',
        '{"schema_version":1,"status":"success","data":{},"error":null,"extra":1}',
    ],
)
def test_imported_runner_inspection_refuses_ambiguous_json(
    orchestrator, monkeypatch, imported_runner_kernel, response
):
    binding, _info, _path = imported_runner_kernel
    monkeypatch.setattr(
        orchestrator,
        "mvm",
        lambda *args, **kwargs: subprocess.CompletedProcess(args, 0, stdout=response),
    )
    with pytest.raises(RuntimeError):
        orchestrator._validate_imported_runner_kernel(binding)


@pytest.mark.parametrize(
    "fault", ["missing", "digest", "candidate", "extra", "boolean-schema", "envelope"]
)
def test_imported_runner_receipt_rejects_wrong_closed_inputs(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    fault,
):
    binding, _info, _path = imported_runner_kernel
    receipt = orchestrator._runner_base_receipt("0.3.0", binding)
    if fault == "digest":
        receipt["kernel_sha256"] = "b" * 64
    elif fault == "candidate":
        receipt["candidate_sha256"] = "b" * 64
    elif fault == "extra":
        receipt["qualification"] = True
    elif fault == "boolean-schema":
        receipt["receipt_schema"] = True
    elif fault == "envelope":
        receipt = {
            "schema_version": 1,
            "status": "success",
            "data": receipt,
            "error": None,
        }
    monkeypatch.setattr(
        orchestrator,
        "mvm",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args, 0, stdout="" if fault == "missing" else json.dumps(receipt)
        ),
    )
    with pytest.raises(RuntimeError):
        orchestrator._require_runner_base_receipt("runner", "0.3.0", binding)


def test_imported_runner_selects_same_kernel_on_all_five_create_paths(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
):
    binding, info, _path = imported_runner_kernel
    calls = []
    receipt = orchestrator._runner_base_receipt("0.3.0", binding)

    def fake_mvm(*args, **kwargs):
        calls.append(args)
        stdout = ""
        if args[:2] == ("kernel", "inspect"):
            stdout = json.dumps(info)
        elif args[:2] == ("image", "ls"):
            stdout = json.dumps({
                "schema_version": 1, "status": "success", "data": [], "error": None,
            })
        elif args[:2] == ("network", "inspect"):
            return subprocess.CompletedProcess(args, 1, stdout="")
        elif args[:2] == ("image", "inspect"):
            return subprocess.CompletedProcess(args, 1, stdout="")
        elif args[0] == "exec":
            if args[-1].startswith("stat -c%s"):
                size = Path(orchestrator.MVM_CANDIDATE_BINARY).stat().st_size
                stdout = f"{size}\n{size}\n"
            elif args[-1].startswith("sha256sum"):
                stdout = f"{receipt['candidate_sha256']}  /usr/local/bin/mvm\n"
            elif args[-1].startswith("head -c"):
                stdout = json.dumps(receipt)
            elif "image pull alpine:3.23" in args[-1]:
                stdout = orchestrator.LOCAL_MIRROR_READ_MARKER
            elif args[-1] == "/usr/local/bin/mvm image ls --json":
                stdout = json.dumps({
                    "schema_version": 1, "status": "success", "error": None,
                    "data": [{"type": "alpine", "version": "3.23", "is_default": True, "is_present": True}],
                })
        elif args[:2] == ("volume", "inspect"):
            stdout = json.dumps({"volume": {"status": "available"}})
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0")
    monkeypatch.setattr(orchestrator, "_unique_name", lambda prefix: prefix)
    for name in (
        "ensure_shared_volume",
        "_ensure_builder_key",
        "_install_system_binary_in_runner",
        "_initialize_system_binary_in_runner",
        "_initialize_runner_user",
        "_pull_builder_assets",
        "_require_imported_runner_smoke",
        "_register_imported_runner_kernel",
    ):
        monkeypatch.setattr(orchestrator, name, lambda *args, **kwargs: None)
    orchestrator.run_prepare(binding=binding)
    orchestrator.provision_t1("normal-t1", "0.3.0", lambda: None, binding=binding)
    orchestrator.provision_t2("normal-t2", "0.3.0", lambda: None, binding=binding)
    assert [args for args in calls if args[:2] == ("network", "create")] == [
        ("network", "create", "sys-test-net", "--subnet", "10.88.0.0/24")
    ]
    creates = [args for args in calls if args[:2] == ("vm", "create")]
    assert len(creates) == 5
    for args in creates:
        assert args[args.index("--kernel") + 1] == binding.id
        expected_image = (
            "ubuntu-minimal:noble"
            if orchestrator.BASE_VM_NAME in args
            else f"mvm-test-runner-kernel-{binding.id}:0.3.0"
        )
        assert args[args.index("--image") + 1] == expected_image
    assert not any(
        args[:2] in {("kernel", "pull"), ("kernel", "default")} for args in calls
    )
    image_import = next(args for args in calls if args[:2] == ("image", "import"))
    assert image_import[2] == f"mvm-test-runner-kernel-{binding.id}:0.3.0"
    assert "--default" not in image_import
    receipt_write = next(
        args[-1]
        for args in calls
        if args[0] == "exec" and args[-1].startswith("printf")
    )
    assert binding.sha256 in receipt_write
    assert receipt["candidate_sha256"] in receipt_write
    assert orchestrator._runner_image_key("0.3.0") == "mvm-test-runner:0.3.0"


@pytest.mark.parametrize(
    "selection,error",
    [
        (["--image", "cli"], "requires --prepare"),
        (["--volume", "cli"], "requires --prepare"),
        (
            ["--prepare", "--tier", "1,2", "--skip-volume-check"],
            "forbids --skip-volume-check",
        ),
    ],
)
def test_imported_runner_preserves_prepare_and_mirror_boundaries_before_effects(
    orchestrator,
    monkeypatch,
    capsys,
    imported_runner_kernel,
    selection,
    error,
):
    binding, _info, _path = imported_runner_kernel
    monkeypatch.setattr(
        orchestrator, "_resolve_imported_runner_kernel_binding", lambda: binding
    )
    _assert_selection_rejected_before_side_effects(
        orchestrator, monkeypatch, capsys, selection, error
    )


@pytest.mark.parametrize("backing", ["missing", "wrong-keys", "absent"])
def test_imported_cached_admission_refuses_unavailable_exact_image_before_effects(
    orchestrator, monkeypatch, capsys, imported_runner_kernel, backing,
):
    binding, info, _path = imported_runner_kernel
    candidate = Path(orchestrator.MVM_CANDIDATE_BINARY)
    candidate.chmod(0o755)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", candidate)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", sys.executable)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_ID", binding.id)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_SHA256", binding.sha256)
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0")
    image_type = f"mvm-test-runner-kernel-{binding.id}"
    rows = []
    if backing == "wrong-keys":
        rows = [
            {"id": "a" * 64, "type": image_type, "version": "0.2.0", "is_present": True},
            {"id": "b" * 64, "type": orchestrator.BASE_IMAGE_NAME, "version": "0.3.0", "is_present": True},
            {"id": "c" * 64, "type": "mvm-test-runner-kernel-" + "d" * 64, "version": "0.3.0", "is_present": True},
        ]
    elif backing == "absent":
        rows = [{"id": "a" * 64, "type": image_type, "version": "0.3.0", "is_present": False}]
    original_rows = json.dumps(rows, sort_keys=True)
    calls = []
    effects = []

    def unexpected_effect(*args, **kwargs):
        effects.append((args, kwargs))
        raise AssertionError("cached admission must precede preparation and runner effects")

    for name in (
        "_start_matrix_evidence", "run_prepare", "_build_base_image",
        "ensure_shared_volume", "ensure_test_network", "run_domains",
    ):
        monkeypatch.setattr(orchestrator, name, unexpected_effect)

    def fake_mvm(*args, **kwargs):
        calls.append(args)
        if args == ("kernel", "inspect", binding.id, "--json"):
            stdout = json.dumps(info)
        elif args == ("image", "ls", "--json"):
            stdout = json.dumps({"schema_version": 1, "status": "success", "data": rows, "error": None})
        else:
            return unexpected_effect(*args, **kwargs)
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(orchestrator.sys, "argv", ["run-system-tests.py", "cli"])
    with pytest.raises(SystemExit) as captured:
        orchestrator.main()
    assert captured.value.code == 2
    error = capsys.readouterr().err
    assert orchestrator._runner_image_key("0.3.0", binding) in error
    assert "prepare it separately with --prepare or --rebuild" in error
    assert calls == [("kernel", "inspect", binding.id, "--json"), ("image", "ls", "--json")]
    assert effects == []
    assert json.dumps(rows, sort_keys=True) == original_rows


@pytest.mark.parametrize("tier", [1, 2])
def test_imported_cached_execution_runs_one_owned_ready_runner_without_preparation(
    orchestrator, monkeypatch, capsys, tmp_path, imported_runner_kernel, tier,
):
    binding, info, _path = imported_runner_kernel
    candidate = Path(orchestrator.MVM_CANDIDATE_BINARY)
    candidate.chmod(0o755)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", candidate)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", sys.executable)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_ID", binding.id)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_SHA256", binding.sha256)
    test_file = "tests/system/cli/test_cli.py"
    _write_system_test(tmp_path, test_file)
    domains = {"cli": [test_file]}
    _configure_test_registry(
        orchestrator, monkeypatch, tmp_path,
        domains if tier == 1 else {}, domains if tier == 2 else {},
    )
    monkeypatch.setattr(orchestrator, "_unique_name", lambda prefix: "owned-runner")
    registered = []
    monkeypatch.setattr(orchestrator, "_register_imported_runner_kernel",
                        lambda vm_name, selected: registered.append((vm_name, selected)))
    versions = []

    def candidate_version():
        versions.append("0.3.0")
        assert len(versions) == 1, "cached dispatch must reuse the admitted candidate version"
        return "0.3.0"

    monkeypatch.setattr(orchestrator, "_get_mvm_version", candidate_version)
    for name in ("ensure_shared_volume", "ensure_test_network"):
        monkeypatch.setattr(orchestrator, name, lambda *args, **kwargs: None)
    receipt = orchestrator._runner_base_receipt("0.3.0", binding)
    calls = []
    live_vm_ids = {"unrelated-vm"}

    def fake_mvm(*args, **kwargs):
        calls.append(args)
        stdout = ""
        if args[:2] == ("kernel", "inspect"):
            stdout = json.dumps(info)
        elif args[:2] == ("image", "ls"):
            stdout = json.dumps({"schema_version": 1, "status": "success", "error": None,
                "data": [{"id": "a" * 64, "type": f"mvm-test-runner-kernel-{binding.id}",
                          "version": "0.3.0", "is_present": True}]})
        elif args[:2] == ("vm", "create"):
            live_vm_ids.add(args[2])
        elif args[:2] == ("vm", "rm"):
            live_vm_ids.remove(args[2])
        elif args[:2] == ("vm", "inspect"):
            stdout = json.dumps({"assets": {"kernel": {"id": binding.id}}})
        elif args[0] == "exec":
            if args[-1] == f"head -c 4097 {orchestrator.RUNNER_BASE_RECEIPT}":
                stdout = json.dumps(receipt)
            elif args[-1].startswith("python3 -c"):
                stdout = f"Linux 7.0.11 GNU build ID {binding.build_id}\n"
            elif args[-1] == "/usr/local/bin/mvm host status --json":
                stdout = json.dumps({"resources": {"nftables_available": True}})
            elif args[-1] == "/usr/local/bin/mvm image ls --json":
                stdout = json.dumps({"schema_version": 1, "status": "success", "error": None,
                    "data": [{"type": "alpine", "version": "3.23", "is_default": True, "is_present": True}]})
            elif args[-1].startswith("head -c 4097 -- "):
                stdout = _complete_pytest_outcome_report().decode()
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(orchestrator.sys, "argv", ["run-system-tests.py", "--tier", str(tier), "--workers", "1"])
    orchestrator.main()
    assert "RESULTS: 1 passed, 0 failed, 1 total" in capsys.readouterr().out
    creates = [args for args in calls if args[:2] == ("vm", "create")]
    assert len(creates) == 1
    assert creates[0][2] == "owned-runner"
    assert creates[0][creates[0].index("--kernel") + 1] == binding.id
    assert creates[0][creates[0].index("--image") + 1] == orchestrator._runner_image_key("0.3.0", binding)
    assert [args for args in calls if args[:2] == ("vm", "rm")] == [("vm", "rm", "owned-runner", "--force")]
    assert live_vm_ids == {"unrelated-vm"}
    assert not any(args[:2] in {("image", "rm"), ("image", "import"), ("kernel", "pull")} for args in calls)
    init = next(i for i, args in enumerate(calls) if args[-1] == orchestrator.RUNNER_USER_INIT_COMMAND)
    smoke = next(i for i, args in enumerate(calls) if args[-1] == "/usr/local/bin/mvm host status --json")
    pytest_calls = [i for i, args in enumerate(calls) if args[0] == "exec" and "python3 -m pytest" in args[-1]]
    assert len(pytest_calls) == 1
    assert init < smoke < pytest_calls[0]
    assert registered == [("owned-runner", binding)]
    if tier == 2:
        assets = next(i for i, args in enumerate(calls) if "kernel pull --type firecracker" in args[-1])
        assert smoke < assets


@pytest.mark.parametrize("fail_at", ["receipt", "smoke", "second-create"])
def test_imported_prepare_failure_destroys_only_owned_smoke_runner(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    fail_at,
):
    binding, _info, _path = imported_runner_kernel
    destroyed = []
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0")
    monkeypatch.setattr(orchestrator, "_unique_name", lambda prefix: prefix)
    monkeypatch.setattr(orchestrator, "destroy_vm", destroyed.append)
    for name in (
        "_validate_imported_runner_kernel",
        "ensure_shared_volume",
        "ensure_test_network",
        "_build_base_image",
        "_initialize_runner_user",
    ):
        monkeypatch.setattr(orchestrator, name, lambda *args, **kwargs: None)

    def gate(vm_name, *args):
        if fail_at != "second-create":
            raise RuntimeError("binding gate refused")

    monkeypatch.setattr(
        orchestrator,
        "_require_runner_base_receipt",
        gate if fail_at == "receipt" else lambda *a: None,
    )
    monkeypatch.setattr(
        orchestrator,
        "_require_imported_runner_smoke",
        gate if fail_at == "smoke" else lambda *a: None,
    )

    def fake_mvm(*args, **kwargs):
        if args[:3] == ("vm", "create", "prep-t2"):
            raise RuntimeError("second create refused")
        return subprocess.CompletedProcess(args, 0, stdout="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    with pytest.raises(RuntimeError, match="refused"):
        orchestrator.run_prepare(binding=binding)
    assert destroyed == ["prep-t1"]


@pytest.mark.parametrize("tier", [1, 2])
@pytest.mark.parametrize("smoke_fails", [True, False], ids=["primary-and-cleanup", "cleanup-only"])
def test_prepare_keeps_smoke_and_owned_destruction_failure_outputs(
    orchestrator, monkeypatch, imported_runner_kernel, tier, smoke_fails,
):
    binding, _info, _path = imported_runner_kernel
    target = f"prep-t{tier}"
    destroyed = []
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0")
    monkeypatch.setattr(orchestrator, "_unique_name", lambda prefix: prefix)
    for name in (
        "_validate_imported_runner_kernel", "ensure_shared_volume", "ensure_test_network",
        "_build_base_image", "_initialize_runner_user", "_require_runner_base_receipt",
    ):
        monkeypatch.setattr(orchestrator, name, lambda *args, **kwargs: None)

    def smoke(vm_name, _binding):
        if vm_name == target and smoke_fails:
            raise RuntimeError("primary smoke stdout and stderr")

    def destroy(vm_name):
        destroyed.append(vm_name)
        if vm_name == target:
            raise RuntimeError("owned cleanup stdout and stderr")

    monkeypatch.setattr(orchestrator, "_require_imported_runner_smoke", smoke)
    monkeypatch.setattr(orchestrator, "destroy_vm", destroy)
    monkeypatch.setattr(
        orchestrator, "mvm",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args, 0, stdout='{"schema_version":1,"status":"success","data":{"volume":{"id":"' + "a" * 64 + '","name":"asset-mirror","status":"available"}},"error":null}', stderr="",
        ),
    )
    monkeypatch.setattr(orchestrator, "_require_local_mirror_pull", lambda *args: None)
    with pytest.raises(RuntimeError) as captured:
        orchestrator.run_prepare(binding=binding)
    message = str(captured.value)
    assert "owned cleanup stdout and stderr" in message
    assert ("primary smoke stdout and stderr" in message) is smoke_fails
    assert destroyed == (["prep-t1"] if tier == 1 else ["prep-t1", "prep-t2"])


@pytest.mark.parametrize("tier", [1, 2])
@pytest.mark.parametrize("timed_out", [True, False], ids=["timeout", "refusal"])
def test_prepare_failed_create_never_manufactures_destruction_ownership(
    orchestrator, monkeypatch, imported_runner_kernel, tier, timed_out,
):
    binding, _info, _path = imported_runner_kernel
    target = f"prep-t{tier}"
    destroyed = []
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0")
    monkeypatch.setattr(orchestrator, "_unique_name", lambda prefix: prefix)
    monkeypatch.setattr(orchestrator, "destroy_vm", destroyed.append)
    for name in (
        "_validate_imported_runner_kernel", "ensure_shared_volume", "ensure_test_network",
        "_build_base_image", "_initialize_runner_user", "_require_runner_base_receipt",
        "_require_imported_runner_smoke",
    ):
        monkeypatch.setattr(orchestrator, name, lambda *args, **kwargs: None)

    def create(*args, **kwargs):
        if args[:3] == ("vm", "create", target):
            if timed_out:
                result = subprocess.CompletedProcess(args, None, stdout="primary create stdout", stderr="primary create stderr")
                raise orchestrator._MvmCommandTimeout(args, kwargs["timeout"], result, ["cleanup uncertainty"])
            raise RuntimeError("preexisting runner create refused")
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", create)
    with pytest.raises(subprocess.TimeoutExpired if timed_out else RuntimeError) as captured:
        orchestrator.run_prepare(binding=binding)
    message = str(captured.value)
    if timed_out:
        assert "primary create stdout" in message and "primary create stderr" in message
        assert "cleanup uncertainty" in message
    else:
        assert message == "preexisting runner create refused"
    assert destroyed == ([] if tier == 1 else ["prep-t1"])


@pytest.mark.parametrize(
    "output,returncode",
    [
        ("Downloading image", 0),
        (
            "Using local mirror for download\nMirror checksum mismatch, falling back to HTTP download",
            0,
        ),
        ("Using local mirror for download\nCopied to asset mirror", 0),
        ("Using local mirror for download", 23),
    ],
)
def test_prepare_mirror_failure_is_fatal_and_cleans_owned_runners(
    orchestrator,
    monkeypatch,
    output,
    returncode,
):
    destroyed = []
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0")
    monkeypatch.setattr(orchestrator, "_unique_name", lambda prefix: prefix)
    monkeypatch.setattr(orchestrator, "destroy_vm", destroyed.append)
    for name in (
        "ensure_shared_volume",
        "ensure_test_network",
        "_build_base_image",
        "_initialize_runner_user",
    ):
        monkeypatch.setattr(orchestrator, name, lambda *args, **kwargs: None)

    def fake_mvm(*args, **kwargs):
        is_pull = args[0] == "exec" and "image pull alpine:3.23" in args[-1]
        return subprocess.CompletedProcess(
            args,
            returncode if is_pull else 0,
            stdout=output if is_pull else "",
            stderr="",
        )

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    with pytest.raises(RuntimeError, match="asset pull"):
        orchestrator.run_prepare()
    assert destroyed == ["prep-t1", "prep-t2"]


@pytest.mark.parametrize("phase", ["lookup", "rebuild"])
def test_imported_image_never_looks_up_or_removes_stock_key(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    phase,
):
    binding, info, _path = imported_runner_kernel
    calls = []

    def fake_mvm(*args, **kwargs):
        calls.append((args, kwargs))
        if args[:2] == ("kernel", "inspect"):
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps(info))
        if args[:2] == ("image", "ls"):
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps({
                "schema_version": 1, "status": "success", "error": None,
                "data": [
                    {"id": "a" * 64, "type": orchestrator.BASE_IMAGE_NAME, "version": "0.3.0", "is_present": True},
                    {"id": "b" * 64, "type": f"mvm-test-runner-kernel-{binding.id}", "version": "0.3.0", "is_present": True},
                ],
            }), stderr="")
        if args[:2] == ("vm", "create"):
            raise RuntimeError("create refused")
        return subprocess.CompletedProcess(args, 0, stdout="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    if phase == "lookup":
        assert orchestrator._build_base_image("0.3.0", binding=binding) == "0.3.0"
    else:
        with pytest.raises(RuntimeError, match="create refused"):
            orchestrator._build_base_image("0.3.0", rebuild=True, binding=binding)
    images = [(args, kwargs) for args, kwargs in calls if args[0] == "image"]
    assert images
    assert images[0][0] == ("image", "ls", "--json")
    if phase != "lookup":
        assert images[-1][0] == ("image", "rm", "b" * 64, "--force")
        assert images[-1][1]["check"] is True
    else:
        assert len(images) == 1
    assert not any(args[:2] == ("vm", "rm") for args, _ in calls)


@pytest.mark.parametrize("tier", [1, 2])
@pytest.mark.parametrize(
    "fault,cleanup_fails,error",
    [
        ("receipt", False, "receipt does not match"),
        ("build-id", False, "live GNU build ID does not match"),
        ("nftables", False, "nftables_available: true"),
        ("nftables", True, "nftables_available: true"),
    ],
    ids=["receipt", "build-id", "nftables", "readiness-and-cleanup"],
)
def test_imported_domain_binding_failure_keeps_owned_cleanup_and_stale_import_refusal(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    tier,
    fault,
    cleanup_fails,
    error,
):
    binding, info, path = imported_runner_kernel
    calls = []
    live_vm_ids = {"unrelated-vm"}
    receipt = orchestrator._runner_base_receipt("0.3.0", binding)
    if fault == "receipt":
        receipt["kernel_sha256"] = "b" * 64
    monkeypatch.setattr(orchestrator, "_unique_name", lambda prefix: "owned-runner")

    def fake_mvm(*args, **kwargs):
        calls.append(args)
        stdout = ""
        if args[:2] == ("kernel", "inspect"):
            stdout = json.dumps(info)
        elif args[:2] == ("vm", "create"):
            live_vm_ids.add(args[2])
        elif args[:2] == ("vm", "rm"):
            if cleanup_fails:
                raise RuntimeError("owned destruction refused")
            live_vm_ids.remove(args[2])
        elif args[:2] == ("vm", "inspect"):
            stdout = json.dumps({"assets": {"kernel": {"id": binding.id}}})
        elif args[0] == "exec":
            if args[-1].startswith("head -c"):
                stdout = json.dumps(receipt)
            elif args[-1].startswith("python3 -c"):
                build_id = "b" * 40 if fault == "build-id" else binding.build_id
                stdout = f"Linux 7.0.11 GNU build ID {build_id}\n"
            elif args[-1] == "/usr/local/bin/mvm host status --json":
                stdout = json.dumps({"resources": {"nftables_available": False}})
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    run = getattr(orchestrator, f"run_tier{tier}_domain")
    result = run("cli", ["tests/system/cli/test_cli.py"], "0.3.0", binding=binding)
    assert result["passed"] is False
    assert error in result["output"]
    assert [args for args in calls if args[:2] == ("vm", "rm")] == [("vm", "rm", "owned-runner", "--force")]
    assert live_vm_ids == ({"unrelated-vm", "owned-runner"} if cleanup_fails else {"unrelated-vm"})
    assert not any(args[0] == "exec" and "python3 -m pytest" in args[-1] for args in calls)
    assert not any(args[0] == "exec" and "kernel pull" in args[-1] for args in calls)
    if cleanup_fails:
        assert "runner VM destruction failed for owned-runner: owned destruction refused" in result["output"]
    calls.clear()
    path.unlink()
    result = run("cli", ["tests/system/cli/test_cli.py"], "0.3.0", binding=binding)
    assert result["passed"] is False
    assert calls == [("kernel", "inspect", binding.id, "--json")]
    assert live_vm_ids == ({"unrelated-vm", "owned-runner"} if cleanup_fails else {"unrelated-vm"})


@pytest.mark.parametrize("tier", [1, 2])
def test_main_prepare_and_worker_handoff_keep_one_imported_binding(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    tier,
):
    binding, _info, _path = imported_runner_kernel
    candidate = Path(orchestrator.MVM_CANDIDATE_BINARY)
    candidate.chmod(0o755)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", candidate)
    monkeypatch.setattr(
        orchestrator.shutil, "which", lambda binary: "/usr/local/bin/mvm"
    )
    monkeypatch.setattr(
        orchestrator, "_resolve_imported_runner_kernel_binding", lambda: binding
    )
    monkeypatch.setattr(
        orchestrator, "_require_distinct_candidate_controller", lambda: None
    )
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0")
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        ["run-system-tests.py", "--prepare", "--tier", str(tier)],
    )
    for name in ("ensure_shared_volume", "ensure_test_network"):
        monkeypatch.setattr(orchestrator, name, lambda *args, **kwargs: None)
    seen = []
    monkeypatch.setattr(
        orchestrator, "run_prepare", lambda **kwargs: seen.append(kwargs["binding"])
    )
    real_run_domains = orchestrator.run_domains

    def worker(domain, files, version, push, *, binding):
        seen.append(binding)
        return {"domain": domain, "tier": tier, "passed": True, "output": ""}

    def one_domain(domains, selected_tier, runner, workers, version, push, *, binding):
        return real_run_domains(
            {"focused": ["test.py"]},
            selected_tier,
            worker,
            1,
            version,
            push=push,
            binding=binding,
        )

    monkeypatch.setattr(orchestrator, "run_domains", one_domain)
    orchestrator.main()
    assert len(seen) == 2
    assert all(value is binding for value in seen)


@pytest.mark.parametrize(
    "bad_kernel,bad_nftables", [(True, False), (False, False), (False, 1)]
)
def test_imported_smoke_requires_exact_kernel_and_true_nftables(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    bad_kernel,
    bad_nftables,
):
    binding, _info, _path = imported_runner_kernel
    calls = []

    def fake_mvm(*args, **kwargs):
        calls.append(args)
        if args[:2] == ("vm", "inspect"):
            data = {
                "assets": {"kernel": {"id": "b" * 64 if bad_kernel else binding.id}}
            }
        elif args[-1] == "/usr/local/bin/mvm host status --json":
            data = {"resources": {"nftables_available": bad_nftables}}
        else:
            return subprocess.CompletedProcess(
                args, 0, stdout=f"Linux 7.0.11 GNU build ID {binding.build_id}\n"
            )
        return subprocess.CompletedProcess(args, 0, stdout=json.dumps(data))

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    with pytest.raises(RuntimeError, match="exact imported kernel|nftables_available"):
        orchestrator._require_imported_runner_smoke("runner", binding)
    if bad_kernel:
        assert len(calls) == 1


@pytest.mark.parametrize("live_build_id", [_RUNNER_ELF_BUILD_ID.hex(), "b" * 40])
def test_imported_smoke_requires_live_build_id_to_match_validated_elf(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    live_build_id,
):
    binding, _info, _path = imported_runner_kernel
    calls = []

    def fake_mvm(*args, **kwargs):
        calls.append(args)
        if args[:2] == ("vm", "inspect"):
            stdout = json.dumps({"assets": {"kernel": {"id": binding.id}}})
        elif args[-1] == "/usr/local/bin/mvm host status --json":
            stdout = json.dumps({"resources": {"nftables_available": True}})
        else:
            stdout = f"Linux 7.0.11 GNU build ID {live_build_id}\n"
        return subprocess.CompletedProcess(args, 0, stdout=stdout)

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    if live_build_id == binding.build_id:
        orchestrator._require_imported_runner_smoke("runner", binding)
        assert len(calls) == 3
    else:
        with pytest.raises(RuntimeError, match="live GNU build ID does not match"):
            orchestrator._require_imported_runner_smoke("runner", binding)
        assert len(calls) == 2


@pytest.mark.parametrize(
    "payload",
    [
        b"not an ELF",
        _minimal_runner_elf(b""),
        _minimal_runner_elf(struct.pack("<III", 4, 20, 3) + b"GNU\x00"),
        _minimal_runner_elf(struct.pack("<III", 4, 0, 3) + b"GNU\x00"),
        _minimal_runner_elf(
            (struct.pack("<III", 4, 20, 3) + b"GNU\x00" + _RUNNER_ELF_BUILD_ID) * 2
        ),
    ],
    ids=[
        "invalid-elf",
        "missing-build-id",
        "truncated-note",
        "empty-build-id",
        "duplicate-build-id",
    ],
)
def test_imported_runner_refuses_malformed_or_missing_elf_build_id_before_effects(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
    payload,
):
    _binding, info, path = imported_runner_kernel
    digest = hashlib.sha256(payload).hexdigest()
    kernel_id = hashlib.sha256(f"{digest}:7.0.11:x86_64".encode()).hexdigest()
    new_path = path.parent / kernel_id
    path.rename(new_path)
    new_path.write_bytes(payload)
    info["kernel"]["id"] = kernel_id
    info["storage"]["path"] = str(new_path)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_ID", kernel_id)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_SHA256", digest)
    calls = []

    def fake_mvm(*args, **kwargs):
        calls.append(args)
        return subprocess.CompletedProcess(args, 0, stdout=json.dumps(info))

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    with pytest.raises(RuntimeError, match="imported runner ELF"):
        orchestrator._resolve_imported_runner_kernel_binding()
    assert calls == [("kernel", "inspect", kernel_id, "--json")]


def test_imported_elf_identity_refuses_change_between_hash_and_note_read(
    orchestrator,
    monkeypatch,
    imported_runner_kernel,
):
    binding, _info, path = imported_runner_kernel
    read_build_id = orchestrator._imported_runner_elf_build_id

    def change_after_hash(fd, size):
        build_id = read_build_id(fd, size)
        path.write_bytes(_minimal_runner_elf() + b"changed")
        return build_id

    monkeypatch.setattr(
        orchestrator, "_imported_runner_elf_build_id", change_after_hash
    )
    with pytest.raises(RuntimeError, match="changed while reading identity"):
        orchestrator._imported_runner_elf_identity(path, binding.sha256)


def _shared_volume_inspection(
    path: str, *, protected: bool = False
) -> dict[str, object]:
    volume_id = "a" * 64
    return {
        "schema_version": 1,
        "status": "success",
        "data": {
            "volume": {
                "id": volume_id,
                "name": "asset-mirror",
                "path": path,
                "format": "raw",
                "is_read_only": True,
            },
            "disk_info": {
                "volume_id": volume_id,
                "logical_bytes": 4096,
                "allocated_bytes": 4096,
                "protected": protected,
            },
        },
        "error": None,
    }


@pytest.mark.parametrize(
    "legacy", [False, True], ids=["schema-1-id-path", "legacy-name-path"]
)
def test_shared_volume_creation_seeds_only_the_inspected_source(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    legacy: bool,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / f"{'asset-mirror' if legacy else 'a' * 64}.raw"
    backing.parent.mkdir(parents=True)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    (mirror / "asset").write_bytes(b"mirrored asset")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    response = _shared_volume_inspection(str(backing))
    if legacy:
        response = response["data"]
        response["disk_info"] = {"format": "raw"}
    calls: list[tuple[str, ...]] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        if args[:2] == ("volume", "create"):
            backing.write_bytes(b"\0" * 4096)
        elif args[:2] != ("volume", "inspect"):
            raise AssertionError("successful source seeding must not remove a volume")
        return subprocess.CompletedProcess(
            args, 0, stdout=json.dumps(response), stderr=""
        )

    def seed_pinned_source(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        (fd,) = kwargs["pass_fds"]
        assert command == ["mkfs.ext4", "-F", "-d", str(mirror), f"/proc/self/fd/{fd}"]
        assert os.fstat(fd).st_ino == backing.stat().st_ino
        os.write(fd, b"seeded")
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(orchestrator.subprocess, "run", seed_pinned_source)

    orchestrator._create_and_seed_volume()

    assert [call[:2] for call in calls] == [("volume", "create"), ("volume", "inspect")]
    assert backing.read_bytes().startswith(b"seeded")


@pytest.mark.parametrize(
    "rebuild, removal_refused",
    [(False, False), (True, False), (True, True)],
    ids=["reuse", "typed-rebuild", "refused-rebuild"],
)
def test_shared_volume_protected_backing_delegates_explicit_rebuild_to_controller(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    rebuild: bool,
    removal_refused: bool,
) -> None:
    cache = tmp_path / "cache"
    source = cache / "volumes" / f"{'a' * 64}.raw"
    source.parent.mkdir(parents=True)
    source.write_bytes(b"caller must not mutate retained staging")
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    response = _shared_volume_inspection("", protected=True)
    calls: list[tuple[str, ...]] = []
    events: list[str] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        events.append(args[1])
        if args[:2] == ("volume", "rm"):
            assert args == ("volume", "rm", "a" * 64, "--force")
            assert kwargs == {"check": True, "timeout": 30}
            if removal_refused:
                raise RuntimeError("root refused referenced or changed-source volume")
        else:
            assert args[:2] == ("volume", "inspect")
        return subprocess.CompletedProcess(args, 0, stdout=json.dumps(response))

    def no_source_access(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("protected backing must not access old caller staging")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(orchestrator.subprocess, "run", no_source_access)
    monkeypatch.setattr(
        orchestrator, "_validated_volume_backing_path", no_source_access
    )
    monkeypatch.setattr(
        orchestrator,
        "_require_shared_volume_path_absent_before_create",
        no_source_access,
    )
    monkeypatch.setattr(
        orchestrator, "_create_and_seed_volume", lambda: events.append("create")
    )
    if removal_refused:
        with pytest.raises(RuntimeError, match="root refused"):
            orchestrator.ensure_shared_volume(rebuild=True)
    else:
        orchestrator.ensure_shared_volume(rebuild=rebuild)
    assert len(calls) == (2 if rebuild else 1)
    assert events == (
        ["inspect", "rm"]
        if removal_refused
        else ["inspect", "rm", "create"]
        if rebuild
        else ["inspect"]
    )
    assert source.read_bytes() == b"caller must not mutate retained staging"


@pytest.mark.parametrize(
    "defect",
    [
        "different-name",
        "invalid-id",
        "mismatched-id",
        "wrong-source-id",
        "missing-authority",
        "invalid-protected",
        "visible-protected-path",
        "bare-authority",
    ],
)
def test_shared_volume_refuses_invalid_identity_before_any_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    defect: str,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / f"{'a' * 64}.raw"
    backing.parent.mkdir(parents=True)
    backing.write_bytes(b"source must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    response = _shared_volume_inspection(str(backing))
    info = response["data"]
    if defect == "different-name":
        info["volume"]["name"] = "unrelated"
    elif defect == "invalid-id":
        info["volume"]["id"] = "../unrelated"
    elif defect == "mismatched-id":
        info["disk_info"]["volume_id"] = "b" * 64
    elif defect == "wrong-source-id":
        info["volume"]["path"] = str(backing.with_name(f"{'b' * 64}.raw"))
    elif defect == "missing-authority":
        del info["disk_info"]
    elif defect == "invalid-protected":
        info["disk_info"]["protected"] = "true"
    elif defect == "visible-protected-path":
        info["disk_info"]["protected"] = True
    elif defect == "bare-authority":
        response = info
    calls: list[tuple[str, ...]] = []

    def inspect_only(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        assert args[:2] == ("volume", "inspect"), (
            "invalid identity must not create or remove"
        )
        return subprocess.CompletedProcess(args, 0, stdout=json.dumps(response))

    monkeypatch.setattr(orchestrator, "mvm", inspect_only)
    monkeypatch.setattr(
        orchestrator.subprocess,
        "run",
        lambda *_args, **_kwargs: pytest.fail(
            "invalid identity must not seed a source"
        ),
    )
    with pytest.raises(orchestrator._UnsafeVolumeBackingPath):
        orchestrator.ensure_shared_volume()
    assert len(calls) == 1
    assert backing.read_bytes() == b"source must survive"


@pytest.mark.parametrize(
    "malformed", [False, True], ids=["already-protected", "malformed-response"]
)
def test_created_shared_volume_with_uncertain_source_is_not_seeded_or_removed(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    malformed: bool,
) -> None:
    cache = tmp_path / "cache"
    source = cache / "volumes" / f"{'a' * 64}.raw"
    source.parent.mkdir(parents=True)
    source.write_bytes(b"source must survive")
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    stdout = (
        "not-json"
        if malformed
        else json.dumps(_shared_volume_inspection("", protected=True))
    )
    calls: list[tuple[str, ...]] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        assert args[:2] in [("volume", "create"), ("volume", "inspect")]
        return subprocess.CompletedProcess(args, 0, stdout=stdout)

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(
        orchestrator.subprocess,
        "run",
        lambda *_args, **_kwargs: pytest.fail("uncertain source must not be seeded"),
    )
    with pytest.raises(orchestrator._UnsafeVolumeBackingPath):
        orchestrator._create_and_seed_volume()
    assert [call[:2] for call in calls] == [("volume", "create"), ("volume", "inspect")]
    assert source.read_bytes() == b"source must survive"


@pytest.mark.parametrize("backing_kind", ["missing", "directory", "symlink"])
def test_ensure_shared_volume_recreates_only_after_stale_backing_is_absent(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    backing_kind: str,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    if backing_kind == "directory":
        backing.mkdir()
    elif backing_kind == "symlink":
        target = tmp_path / "target.raw"
        target.write_bytes(b"volume")
        backing.symlink_to(target)
    leftover = backing.lstat() if backing_kind != "missing" else None

    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_shared_volume",
        lambda: (True, str(backing), None, False),
    )
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    seeded: list[bool] = []
    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(
        orchestrator, "_create_and_seed_volume", lambda: seeded.append(True)
    )

    if backing_kind == "missing":
        orchestrator.ensure_shared_volume()
        assert seeded == [True]
    else:
        with pytest.raises(RuntimeError, match="still exists before volume create"):
            orchestrator.ensure_shared_volume()
        assert seeded == []
        preserved = backing.lstat()
        assert (preserved.st_dev, preserved.st_ino, preserved.st_mode) == (
            leftover.st_dev,
            leftover.st_ino,
            leftover.st_mode,
        )
        if backing_kind == "symlink":
            assert backing.readlink() == target
            assert target.read_bytes() == b"volume"

    assert calls == [
        (
            ("volume", "rm", orchestrator.SHARED_VOLUME_NAME, "--force"),
            {"timeout": 30},
        )
    ]


def test_ensure_shared_volume_rejects_poisoned_arbitrary_path_without_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    (cache / "volumes").mkdir(parents=True)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    unrelated = tmp_path / "unrelated.data"
    unrelated.write_bytes(b"must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_shared_volume",
        lambda: (True, str(unrelated), None, False),
    )

    def unexpected_mvm(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("poisoned records must not trigger volume mutation")

    monkeypatch.setattr(orchestrator, "mvm", unexpected_mvm)

    with pytest.raises(RuntimeError, match="unexpected shared volume backing path"):
        orchestrator.ensure_shared_volume()

    assert unrelated.read_bytes() == b"must survive"


def test_populate_volume_image_uses_unprivileged_mkfs_directory_copy(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    (mirror / "manifest.json").write_text("{}", encoding="utf-8")
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    backing.write_bytes(b"\0" * 4096)
    owner_before = (backing.stat().st_uid, backing.stat().st_gid)
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    orchestrator._populate_volume_image(backing, mirror)

    assert len(calls) == 1
    command, kwargs = calls[0]
    assert command[:4] == ["mkfs.ext4", "-F", "-d", str(mirror)]
    assert command[4].startswith("/proc/self/fd/")
    assert kwargs["capture_output"] is True
    assert kwargs["text"] is True
    assert kwargs["timeout"] == 900
    assert kwargs["pass_fds"] == (int(command[4].rsplit("/", 1)[-1]),)
    assert (backing.stat().st_uid, backing.stat().st_gid) == owner_before


def test_populate_volume_image_rejects_symlink_backing_before_mkfs(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    target = tmp_path / "target.raw"
    target.write_bytes(b"volume")
    backing.symlink_to(target)
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("mkfs must not run for a symlink backing path")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="symlink"):
        orchestrator._populate_volume_image(backing, mirror)


def test_populate_volume_image_rejects_symlinked_cache_ancestor_before_mkfs(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    actual_cache = tmp_path / "actual-cache"
    actual_backing = actual_cache / "volumes" / "asset-mirror.raw"
    actual_backing.parent.mkdir(parents=True)
    actual_backing.write_bytes(b"volume")
    configured_cache = tmp_path / "configured-cache"
    configured_cache.symlink_to(actual_cache, target_is_directory=True)
    recorded_backing = configured_cache / "volumes" / "asset-mirror.raw"
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(configured_cache))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("mkfs must not traverse a symlinked cache ancestor")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="safely open shared volume directory"):
        orchestrator._populate_volume_image(recorded_backing, mirror)

    assert actual_backing.read_bytes() == b"volume"


def test_create_volume_removes_record_when_created_backing_is_missing(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    cache = tmp_path / "cache"
    missing_backing = cache / "volumes" / "asset-mirror.raw"
    missing_backing.parent.mkdir(parents=True)
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_shared_volume",
        lambda: (True, str(missing_backing), None, False),
    )
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match="backing file is missing"):
        orchestrator._create_and_seed_volume()

    assert calls[0][0][:2] == ("volume", "create")
    assert calls[-1] == (
        ("volume", "rm", orchestrator.SHARED_VOLUME_NAME, "--force"),
        {"check": False, "timeout": 30},
    )


@pytest.mark.parametrize("leftover_kind", ["dangling_symlink", "directory", "file"])
def test_stale_record_cleanup_must_remove_backing_path_before_volume_create(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    leftover_kind: str,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    if leftover_kind == "dangling_symlink":
        backing.symlink_to(tmp_path / "missing-target.raw")
    elif leftover_kind == "directory":
        backing.mkdir()
    else:
        backing.write_bytes(b"volume")
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_shared_volume",
        lambda: (True, str(backing), None, False),
    )
    calls: list[tuple[str, ...]] = []

    def stale_remove(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        if args[:2] == ("volume", "create"):
            raise AssertionError("volume create must not follow stale cleanup")
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", stale_remove)

    with pytest.raises(RuntimeError, match="still exists before volume create"):
        orchestrator.ensure_shared_volume(rebuild=True)

    assert calls == [
        ("volume", "rm", orchestrator.SHARED_VOLUME_NAME, "--force")
    ]
    assert backing.lstat()


def test_created_volume_with_unsafe_record_path_skips_automatic_cleanup(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    (cache / "volumes").mkdir(parents=True)
    unrelated = tmp_path / "unrelated.data"
    unrelated.write_bytes(b"must survive")
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_shared_volume",
        lambda: (True, str(unrelated), None, False),
    )
    calls: list[tuple[str, ...]] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match="unexpected shared volume backing path"):
        orchestrator._create_and_seed_volume()

    assert len(calls) == 1
    assert calls[0][:2] == ("volume", "create")
    assert unrelated.read_bytes() == b"must survive"


def test_populate_volume_image_rejects_arbitrary_regular_file(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    (cache / "volumes").mkdir(parents=True)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    unrelated = tmp_path / "unrelated.data"
    unrelated.write_bytes(b"must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("mkfs must not run for an arbitrary path")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="unexpected shared volume backing path"):
        orchestrator._populate_volume_image(unrelated, mirror)

    assert unrelated.read_bytes() == b"must survive"


def test_populate_volume_image_detects_path_replacement_and_pins_original_inode(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    backing.write_bytes(b"volume")
    original_inode = backing.stat().st_ino
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    victim = tmp_path / "victim.data"
    victim.write_bytes(b"must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    command_seen: list[str] = []

    def replace_path_during_mkfs(
        command: list[str], **_kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        command_seen.extend(command)
        backing.unlink()
        backing.symlink_to(victim)
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(
        orchestrator.subprocess, "run", replace_path_during_mkfs
    )

    with pytest.raises(RuntimeError, match="replaced during population"):
        orchestrator._populate_volume_image(backing, mirror)

    pinned_fd = int(command_seen[-1].rsplit("/", 1)[-1])
    assert command_seen[-1] == f"/proc/self/fd/{pinned_fd}"
    assert original_inode != victim.stat().st_ino
    assert victim.read_bytes() == b"must survive"


@pytest.mark.skipif(
    shutil.which("mkfs.ext4") is None or shutil.which("debugfs") is None,
    reason="e2fsprogs is required for the direct ext4 population check",
)
def test_populate_volume_image_directly_seeds_a_real_ext4_image(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    with backing.open("wb") as volume_file:
        volume_file.truncate(32 * 1024 * 1024)
    owner_before = (backing.stat().st_uid, backing.stat().st_gid)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    (mirror / "manifest.json").write_text('{"seeded": true}\n', encoding="utf-8")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    orchestrator._populate_volume_image(backing, mirror)

    inspected = subprocess.run(
        ["debugfs", "-R", "cat /manifest.json", str(backing)],
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert inspected.returncode == 0, inspected.stderr
    assert inspected.stdout == '{"seeded": true}\n'
    assert (backing.stat().st_uid, backing.stat().st_gid) == owner_before


def test_candidate_version_probe_is_exact_and_isolated_from_host_state(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    candidate = tmp_path / "candidate-mvm"
    candidate.write_bytes(b"candidate")
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    monkeypatch.setattr(orchestrator, "MVM_BINARY", "/host/controller-mvm")
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        return subprocess.CompletedProcess(
            command, 0, stdout="mvm 0.3.0-rc.1\n", stderr=""
        )

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)

    version = orchestrator._get_mvm_version()

    assert version == "0.3.0-rc.1"
    assert len(calls) == 1
    command, kwargs = calls[0]
    assert command == [str(candidate), "--version"]
    assert kwargs["capture_output"] is True
    assert kwargs["text"] is True
    assert kwargs["timeout"] == 10
    env = kwargs["env"]
    assert isinstance(env, dict)
    for variable in ("MVM_CACHE_DIR", "MVM_CONFIG_DIR", "MVM_TEMP_DIR"):
        assert env[variable] != os.environ.get(variable)
        assert "mvm-candidate-version-" in env[variable]


def test_rebuild_outputs_only_to_the_configured_candidate(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    candidate = tmp_path / "dist" / "mvm"
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    calls: list[list[str]] = []

    def fake_run(command: list[str], **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(command)
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)
    monkeypatch.setattr(
        orchestrator, "_get_mvm_version", lambda: "0.3.0-rc.1"
    )

    orchestrator._build_mvm_binary("0.3.0-rc.1")

    assert len(calls) == 1
    assert calls[0][-4:-2] == ["--version", "0.3.0-rc.1"]
    assert calls[0][-2:] == ["--output", str(candidate)]
    assert str(controller) not in calls[0]


def test_rebuild_rejects_candidate_with_wrong_reported_version(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    candidate = tmp_path / "dist" / "mvm"
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    monkeypatch.setattr(
        orchestrator.subprocess,
        "run",
        lambda command, **_kwargs: subprocess.CompletedProcess(
            command, 0, stdout="", stderr=""
        ),
    )
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.2.6")

    with pytest.raises(RuntimeError, match="requested 0.3.0, reported 0.2.6"):
        orchestrator._build_mvm_binary("0.3.0")


def test_explicit_candidate_version_does_not_consult_git(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("an explicit candidate version must not consult git")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    assert orchestrator._resolve_candidate_build_version("v0.3.0-rc.2") == (
        "0.3.0-rc.2"
    )


def test_candidate_version_uses_one_exact_release_tag(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        stdout = "v0.3.0-rc.3\n" if command[1] == "tag" else ""
        return subprocess.CompletedProcess(
            command,
            0,
            stdout=stdout,
            stderr="",
        )

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)

    assert orchestrator._resolve_candidate_build_version(None) == "0.3.0-rc.3"
    assert len(calls) == 2
    assert calls[0][0] == [
        "git",
        "tag",
        "--points-at",
        "HEAD",
        "--list",
        "v[0-9]*",
    ]
    assert calls[1][0] == [
        "git",
        "status",
        "--porcelain=v1",
        "--untracked-files=all",
    ]
    for _command, kwargs in calls:
        assert kwargs["cwd"] == str(orchestrator._REPO_ROOT)
        assert kwargs["capture_output"] is True
        assert kwargs["text"] is True
        assert kwargs["timeout"] == 10


@pytest.mark.parametrize("tag_output", ["", "v0.2.0\nv0.3.0\n", "release-0.3.0\n"])
def test_candidate_version_rejects_untagged_or_ambiguous_checkout(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tag_output: str,
) -> None:
    monkeypatch.setattr(
        orchestrator.subprocess,
        "run",
        lambda command, **_kwargs: subprocess.CompletedProcess(
            command, 0, stdout=tag_output, stderr=""
        ),
    )

    with pytest.raises(RuntimeError, match="--candidate-version"):
        orchestrator._resolve_candidate_build_version(None)


def test_candidate_version_rejects_dirty_exact_release_tag(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def fake_run(
        command: list[str], **_kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        stdout = "v0.3.0\n" if command[1] == "tag" else " M internal/file.go\n"
        return subprocess.CompletedProcess(command, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)

    with pytest.raises(RuntimeError, match="--candidate-version"):
        orchestrator._resolve_candidate_build_version(None)


def test_candidate_version_flag_is_typed(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    parsed = parser.parse_args(
        ["--rebuild", "--candidate-version", "v0.3.0-rc.4"]
    )
    assert parsed.candidate_version == "0.3.0-rc.4"

    with pytest.raises(SystemExit):
        parser.parse_args(["--rebuild", "--candidate-version", "not-a-version"])


@pytest.mark.parametrize(
    "version",
    [
        "01.2.3",
        "1.02.3",
        "1.2.03",
        "1.2.3-01",
        "1.2.3-",
        "1.2.3-rc..1",
        "1.2.3-rc.",
        "1.2.3+",
        "1.2.3+meta..build",
        "1.2.3+meta.",
    ],
)
def test_candidate_version_parser_rejects_non_semver_identifiers(
    orchestrator: ModuleType,
    version: str,
) -> None:
    with pytest.raises(argparse.ArgumentTypeError):
        orchestrator._parse_candidate_version(version)


def test_candidate_version_flag_requires_rebuild(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        ["run-system-tests.py", "--candidate-version", "0.3.0"],
    )

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 2


def test_rebuild_rejects_candidate_controller_alias_before_build(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(controller))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("build must not start when candidate aliases controller")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="outer controller"):
        orchestrator._build_mvm_binary("0.3.0")


def test_rebuild_rejects_candidate_symlink_before_build(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    target = tmp_path / "candidate-target"
    target.write_bytes(b"candidate")
    configured = tmp_path / "candidate-mvm"
    configured.symlink_to(target)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", configured)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(target))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("build must not follow a candidate symlink")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="symlink"):
        orchestrator._build_mvm_binary("0.3.0")


def test_rebuild_rejects_candidate_controller_hardlink_before_build(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    candidate = tmp_path / "candidate-mvm"
    os.link(controller, candidate)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", candidate)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("build must not replace a controller inode alias")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="outer controller"):
        orchestrator._build_mvm_binary("0.3.0")
