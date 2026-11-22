#!/usr/bin/env python3
"""One-VM Guest Control VMM-exit qualification, separate from ordinary46 proof.

Run from a clean pinned source with the unchanged outer MVM_BINARY. The existing
qualification namespace admits the enrolled operator; administrative SSH is used
only for the read-only pidfd observer. This never installs a binary or helper.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import select
import shlex
import stat
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
SELF = "scripts/qualification/guest_control_vmm_exit.py"
OBSERVER = "scripts/qualification/guest_control_vmm_exit_observer.py"
HELPER = "tests/system/network/floating_ip_worker_fault.py"
PAYLOAD = (SELF, OBSERVER, HELPER, "tests/__init__.py", "tests/system/__init__.py",
           "tests/system/conftest.py", "tests/system/network/__init__.py",
           "tests/system/network/floating_ip_helpers.py", "tests/system/network/test_policies.py")


def _orchestrator():
    sys.path.insert(0, str(ROOT / "scripts"))
    spec = importlib.util.spec_from_file_location("vmm_exit_orchestrator", ROOT / "scripts/run-system-tests.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def _write(path, item):
    raw = (json.dumps(item, sort_keys=True) + "\n").encode()
    assert len(raw) <= 128 * 1024
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(raw); stream.flush(); os.fsync(stream.fileno())


def _read(path):
    # Same private regular-file contract as the qualification receipts. This
    # small reader also runs inside the staged subset without the orchestrator.
    path = Path(path)
    assert path.is_absolute() and path.resolve(strict=True) == path
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        row = os.fstat(stream.fileno())
        assert stat.S_ISREG(row.st_mode) and row.st_nlink == 1 and row.st_uid == os.getuid()
        assert stat.S_IMODE(row.st_mode) == 0o600 and row.st_size <= 128 * 1024
        raw = stream.read(128 * 1024 + 1)
        identity = lambda r: (r.st_dev, r.st_ino, r.st_mode, r.st_nlink, r.st_size, r.st_mtime_ns, r.st_ctime_ns)
        assert len(raw) == row.st_size and identity(os.fstat(stream.fileno())) == identity(row)
        assert identity(path.lstat()) == identity(row)
    def unique(pairs):
        item = {}
        for key, value in pairs:
            assert key not in item, "duplicate receipt field"
            item[key] = value
        return item
    item = json.loads(raw, object_pairs_hook=unique)
    assert isinstance(item, dict)
    return item


def _line(stream, timeout):
    assert select.select([stream], [], [], timeout)[0], "observer response deadline expired"
    raw = stream.readline(16385)
    assert raw.endswith(b"\n") and len(raw) <= 16384, "observer response missing or oversized"
    def unique(pairs):
        item = {}
        for key, value in pairs:
            assert key not in item, "duplicate observer field"
            item[key] = value
        return item
    return json.loads(raw, object_pairs_hook=unique)


def _ready(item, vm, namespace):
    assert set(item) == {"event", "vm_id", "boot_id", "launch_generation", "worker_generation", "worker_pid",
                         "worker_start", "vmm_pid", "vmm_start", "worker_record_sha256", "instance_record_sha256"}
    assert item["event"] == "READY" and item["vm_id"] == vm["binding"]["id"]
    assert item["boot_id"] == namespace["expected_vm"]["boot_id"]
    assert (item["vmm_pid"], item["vmm_start"]) == (vm["pid"], vm["process_start_time"])
    for key in ("launch_generation", "worker_generation", "worker_pid", "worker_start", "vmm_pid", "vmm_start"):
        assert type(item[key]) is int and item[key] > 0
    assert item["worker_pid"] != item["vmm_pid"]
    for key in ("worker_record_sha256", "instance_record_sha256"):
        assert re.fullmatch(r"[0-9a-f]{64}", item[key])


def _exited(item, ready):
    assert set(item) == {"event", "admission", "exit_elapsed_seconds"}
    assert item["event"] == "EXITED" and item["admission"] == ready
    events = item["exit_elapsed_seconds"]
    assert set(events) == {"vmm", "worker"}
    assert all(type(value) in (int, float) and 0 <= value <= 61 for value in events.values())


def _guest(phase, task):
    """Private enrolled-operator phases; the outer controller owns their order."""
    sys.path.insert(0, str(ROOT))
    from tests.system.conftest import _run_mvm
    from tests.system.network.floating_ip_helpers import FloatingIPJourney
    task = Path(task)
    assert os.getuid() == os.geteuid() > 0
    state_path = task / "prepared.json"
    if phase == "prepare":
        journey = FloatingIPJourney("mvmctl-030-test")
        state = {}
        try:
            network = journey.network("vmm-exit")
            vm = journey.vm("owner", network)
            resource = journey.resource(network, "service")
            grants = [journey.grant(vm, resource, method) for method in ("inspect", "associate")]
            initial = journey.control(vm, resource)
            associated = journey.control(vm, resource, "associate", initial["generation"])
            assert associated["ownership"] == "self"
            journey.alias(vm, resource, present=True)
            state.update(vm=vm, resource=resource, grants=grants, associated=associated)
        finally:
            state["journey"] = journey.__dict__
            _write(state_path, state)
        print("PREPARED", flush=True)
    elif phase == "poweroff":
        state = _read(state_path)
        # The only effect between observer admission and exit proof. Delaying
        # shutdown leaves time for the typed exec response to close normally.
        result = _run_mvm("mvmctl-030-test", "exec", state["vm"]["binding"]["id"], "--user", "root", "--",
                          "command -v poweroff >/dev/null || exit 1; nohup sh -c 'sleep 3; poweroff' "
                          "</dev/null >/dev/null 2>&1 &", timeout=30)
        assert result.returncode == 0
        print("POWEROFF_SCHEDULED", flush=True)
    elif phase == "cleanup":
        state = _read(state_path)
        journey = FloatingIPJourney.__new__(FloatingIPJourney)
        journey.__dict__.update(state["journey"])
        journey.cleanup()
        print("CLEANED", flush=True)
    else:
        raise RuntimeError("unknown private qualification phase")


def _remote_source(o, namespace, task, payload):
    script = ("import hashlib,json,pathlib,sys;root=pathlib.Path(sys.argv[1]);expected=json.loads(sys.argv[2]);"
              "assert all((root/p).resolve(strict=True)==root/p for p in expected);"
              "assert {p:hashlib.sha256((root/p).read_bytes()).hexdigest() for p in expected}==expected")
    o.qualification_vm._command(o, namespace, script, task + "/source", json.dumps(payload))


def _phase(o, namespace, task, phase, payload):
    _remote_source(o, namespace, task, payload)
    command = shlex.join(["/usr/bin/python3", "-I", "-B", task + "/source/" + SELF, "--guest", phase, task])
    result = o.qualification_vm._exec(o, namespace, command, timeout=300, check=False)
    assert result.returncode == 0, (phase, result.stdout, result.stderr)
    assert result.stdout.strip() == {"prepare": "PREPARED", "poweroff": "POWEROFF_SCHEDULED", "cleanup": "CLEANED"}[phase]
    return dict(returncode=result.returncode, stdout=result.stdout, stderr=result.stderr)


def run(args):
    o = _orchestrator()
    q = o.qualification_vm
    namespace = q.read_namespace(o, Path(args.namespace))
    source = o.source_identity(ROOT)
    assert source["revision"] == args.source_revision, "source revision differs"
    payload = {path: source["payload"][path] for path in PAYLOAD}
    task = Path(args.task)
    assert task.is_absolute() and task.resolve(strict=True) == task
    row = task.lstat()
    assert stat.S_ISDIR(row.st_mode) and row.st_uid == os.getuid() and stat.S_IMODE(row.st_mode) == 0o700
    assert not list(task.iterdir()), "use a fresh empty private evidence task"
    before = q.observe(o, namespace)
    assert before["guest"]["controller"]["sha256"] == args.candidate_sha256
    assert before["guest"]["version"] == args.candidate_version
    transport = dict(ssh_key=args.ssh_key, known_hosts=args.known_hosts,
                     known_hosts_sha256=o._sha256_runner_input(Path(args.known_hosts)))
    _write(task / "binding.json", dict(schema_version=1, source=source, staged_payload=payload, namespace=namespace,
                                       before=before, candidate_sha256=args.candidate_sha256,
                                       candidate_version=args.candidate_version, administrative_transport=transport))
    guest_task = q.stage(o, namespace, dict(payload=payload))
    q._command(o, namespace, "import pathlib,sys;root=pathlib.Path(sys.argv[1]);"
               "[(root/p).chmod(0o644) for p in __import__('json').loads(sys.argv[2])]",
               guest_task + "/source", json.dumps(list(payload)))
    _write(task / "guest-task.json", dict(path=guest_task))
    # Root sees only reviewed source bytes sent through its existing SSH channel.
    # Its helper read is checked against this same source binding before execution.
    observer_raw = (ROOT / OBSERVER).read_bytes()
    assert hashlib.sha256(observer_raw).hexdigest() == payload[OBSERVER]
    observer = None
    prepared = False
    proof = None
    failure = None
    cleanup = None
    try:
        phase = _phase(o, namespace, guest_task, "prepare", payload)
        prepared = True
        _write(task / "prepare-native.json", phase)
        state_script = "import pathlib,sys;sys.path.insert(0,sys.argv[1]);from qualification.guest_control_vmm_exit import _read;import json;print(json.dumps(_read(sys.argv[2])))"
        raw = q._command(o, namespace, state_script, guest_task + "/source/scripts", guest_task + "/prepared.json").stdout
        state = o._host_reboot_json(raw.encode())
        _write(task / "prepared.json", state)
        expected = dict(namespace=namespace, vm=state["vm"], candidate_sha256=args.candidate_sha256,
                        helper_path=guest_task + "/source/" + HELPER, helper_sha256=payload[HELPER])
        code = "import base64;exec(compile(base64.b64decode(" + repr(base64.b64encode(observer_raw).decode()) + "),'<pinned-vmm-exit-observer>','exec'))"
        remote = shlex.join(["/usr/bin/python3", "-I", "-S", "-B", "-c", code, json.dumps(expected)])
        command = ["ssh", "-F", "/dev/null", "-i", args.ssh_key, "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
                   "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8", "-o", "UserKnownHostsFile=" + args.known_hosts,
                   "root@" + namespace["expected_vm"]["ipv4"], remote]
        observer = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        ready = _line(observer.stdout, 20)
        _ready(ready, state["vm"], namespace)
        _write(task / "observer-ready.json", ready)
        observer.stdin.write(b"ARM\n"); observer.stdin.flush()
        armed = _line(observer.stdout, 5)
        assert armed == dict(event="ARMED", vm_id=ready["vm_id"])
        _write(task / "observer-armed.json", armed)
        _write(task / "poweroff-native.json", _phase(o, namespace, guest_task, "poweroff", payload))
        exited = _line(observer.stdout, 65)
        _exited(exited, ready)
        output, errors = observer.communicate(timeout=10)
        _write(task / "observer-native.json", dict(returncode=observer.returncode,
                                                  stdout=output.decode(errors="replace"), stderr=errors.decode(errors="replace")))
        assert observer.returncode == 0 and output == b"" and errors == b"", (observer.returncode, output, errors)
        proof = exited
        _write(task / "observer-exited.json", proof)
    except Exception as error:
        failure = str(error)
    finally:
        # Finish/close the read-only observer BEFORE any lifecycle cleanup. On a
        # failed admission/deadline it contributes no positive exit proof.
        if observer is not None and not (task / "observer-native.json").exists():
            try:
                observer.stdin.close()
                observer.stdin = None
                output, errors = observer.communicate(timeout=70)
                _write(task / "observer-native.json", dict(returncode=observer.returncode,
                                                          stdout=output.decode(errors="replace"), stderr=errors.decode(errors="replace")))
            except Exception as error:
                # No signaling, including local SSH termination. Refuse cleanup
                # while an observer could still be proving the unassisted exit.
                failure = (failure or "") + "; observer close failed: " + str(error)
        if observer is None or observer.poll() is not None:
            try:
                # prepare persists partial owned intents even if it fails.
                _phase(o, namespace, guest_task, "cleanup", payload)
                cleanup = q.observe(o, namespace)
                q.require_same(before, cleanup)
            except Exception as error:
                failure = (failure or "") + "; cleanup failed: " + str(error)
    passed = prepared and proof is not None and cleanup == before and failure is None
    if o.source_identity(ROOT) != source:
        passed = False
        failure = (failure or "") + "; qualification source changed"
    terminal = dict(schema_version=1, verdict="PASS" if passed else "FAIL", requirement="worker-exits-on-vmm-exit",
                    ordinary46_reexecuted=False, guest_task=guest_task, proof=proof, cleanup=cleanup,
                    failure=failure, source_revision=source["revision"], candidate_sha256=args.candidate_sha256)
    _write(task / "terminal.json", terminal)
    print(json.dumps(terminal, sort_keys=True))
    return 0 if passed else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--task", required=True)
    parser.add_argument("--source-revision", required=True)
    parser.add_argument("--candidate-sha256", required=True)
    parser.add_argument("--candidate-version", required=True)
    parser.add_argument("--ssh-key", required=True)
    parser.add_argument("--known-hosts", required=True)
    args = parser.parse_args()
    assert re.fullmatch(r"[0-9a-f]{40}", args.source_revision)
    assert re.fullmatch(r"[0-9a-f]{64}", args.candidate_sha256)
    for path in (args.ssh_key, args.known_hosts):
        assert Path(path).is_absolute() and Path(path).resolve(strict=True) == Path(path)
    return run(args)


if __name__ == "__main__":
    if len(sys.argv) == 4 and sys.argv[1] == "--guest":
        _guest(sys.argv[2], sys.argv[3])
    else:
        raise SystemExit(main())


def test_handshake_requires_exact_admission_and_two_exits():
    import pytest
    vm = dict(binding=dict(id="a" * 32), pid=12, process_start_time=34)
    namespace = dict(expected_vm=dict(boot_id="boot"))
    ready = dict(event="READY", vm_id="a" * 32, boot_id="boot", launch_generation=1, worker_generation=2,
                 worker_pid=56, worker_start=78, vmm_pid=12, vmm_start=34,
                 worker_record_sha256="b" * 64, instance_record_sha256="c" * 64)
    _ready(ready, vm, namespace)
    proof = dict(event="EXITED", admission=ready, exit_elapsed_seconds=dict(worker=1.2, vmm=2.3))
    _exited(proof, ready)
    proof["exit_elapsed_seconds"].pop("worker")
    with pytest.raises(AssertionError):
        _exited(proof, ready)
    ready["vmm_start"] += 1
    with pytest.raises(AssertionError):
        _ready(ready, vm, namespace)


def test_receipt_refuses_symlink_and_overwrite(tmp_path):
    import pytest
    path = tmp_path / "receipt.json"
    _write(path, dict(event="READY"))
    assert _read(path) == dict(event="READY")
    with pytest.raises(FileExistsError):
        _write(path, {})
    link = tmp_path / "link.json"
    link.symlink_to(path)
    with pytest.raises((AssertionError, OSError)):
        _read(link)


def test_observer_channel_rejects_duplicate_or_incomplete_message():
    import pytest
    for raw in (b'{"event":"READY"}\n', b'{"event":"READY","event":"EXITED"}\n', b'{}'):
        read_fd, write_fd = os.pipe()
        os.write(write_fd, raw); os.close(write_fd)
        with os.fdopen(read_fd, "rb") as stream:
            if raw == b'{"event":"READY"}\n':
                assert _line(stream, 1) == dict(event="READY")
            else:
                with pytest.raises(AssertionError):
                    _line(stream, 1)
