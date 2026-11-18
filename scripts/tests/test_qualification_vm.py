"""Explicit retained-target admission and effect-boundary behavior, without real VMs."""

import base64
import copy
import json
from pathlib import Path
import subprocess
from types import SimpleNamespace

import pytest

from test_run_system_tests import orchestrator, _complete_pytest_outcome_report  # noqa: F401
from test_system_test_evidence import _junit, _node_report


@pytest.fixture
def namespace():
    return dict(schema_version=1, runner="mvmctl-030-test", runner_id="1" * 32,
                operator=dict(user="runner", uid=1000, gid=1000, home="/home/runner"),
                paths=dict(cache="/home/runner/cache", config="/home/runner/config", mirror="/home/runner/mirror"),
                expected_vm=dict(created_at="2026-11-18T00:00:00Z", boot_id="11111111-1111-4111-8111-111111111111",
                                 ipv4="10.25.0.3", ssh_host_key="ssh-ed25519 " + base64.b64encode(b"\0\0\0\x0bssh-ed25519\0\0\0\x20" + b"a" * 32).decode()))


@pytest.fixture
def observed(namespace):
    controller = dict(path="/usr/local/bin/mvm", command=["/usr/local/bin/mvm"], sha256="a" * 64,
                      identity=[1, 2, 0, 0, 0o755, 1, 100, 3, 4])
    return dict(outer=dict(id=namespace["runner_id"], name=namespace["runner"], created_at=namespace["expected_vm"]["created_at"],
                           pid=123, start_ticks=100, ipv4="10.25.0.3"),
                guest=dict(hostname=namespace["runner"], boot_id=namespace["expected_vm"]["boot_id"],
                           namespace_identity={key: [1, index, 1000, 1000, 0o700] for index, key in
                                               enumerate(("home", "task_parent", "cache", "config", "mirror"), 1)},
                           host_key=namespace["expected_vm"]["ssh_host_key"], controller=controller,
                           version="mvm 0.3.0-rc.fixture", inventory={"vm": []}),
                outer_controller=dict(path="/usr/local/bin/mvm", command=["/usr/local/bin/mvm"], identity=[1,3,0,0,0o755,1,20,1,1], sha256="b"*64))


@pytest.mark.parametrize("fault", [None, "unknown", "boot", "ipv4", "key", "root_operator"])
def test_private_namespace_requires_exact_pins(orchestrator, namespace, tmp_path, fault):
    if fault == "unknown":namespace["auto_install"] = True
    elif fault == "boot":namespace["expected_vm"]["boot_id"] = namespace["expected_vm"]["boot_id"].upper()
    elif fault == "ipv4":namespace["expected_vm"]["ipv4"] = "010.25.0.3"
    elif fault == "key":namespace["expected_vm"]["ssh_host_key"] += " comment"
    elif fault == "root_operator":namespace["operator"]["uid"] = 0
    path = tmp_path / "namespace.json";path.write_text(json.dumps(namespace));path.chmod(0o600)
    # Use an actually noncanonical UUID for the boot mutation (digits alone have no case).
    if fault == "boot":
        namespace["expected_vm"]["boot_id"] = "NOT-A-UUID";path.write_text(json.dumps(namespace))
    if fault is None:
        assert orchestrator.qualification_vm.read_namespace(orchestrator, path) == namespace
    else:
        with pytest.raises((RuntimeError, ValueError)):
            orchestrator.qualification_vm.read_namespace(orchestrator, path)


@pytest.mark.parametrize("fault", ["boot", "hostname", "host_key", "namespace", "candidate_mode"])
def test_wire_observation_refuses_identity_drift_before_staging(orchestrator, namespace, observed, monkeypatch, fault):
    guest = copy.deepcopy(observed["guest"])
    if fault == "boot":guest["boot_id"] = "22222222-2222-4222-8222-222222222222"
    elif fault == "hostname":guest["hostname"] = "other"
    elif fault == "host_key":guest["host_key"] = "other-key"
    elif fault == "namespace":guest["namespace_identity"]["cache"][2] = 0
    else:guest["controller"]["identity"][4] = 0o777
    monkeypatch.setattr(orchestrator.qualification_vm, "_exec", lambda *a, **k: subprocess.CompletedProcess([],0,json.dumps(guest),""))
    monkeypatch.setattr(orchestrator.qualification_vm, "_outer", lambda *a: pytest.fail("changed wire proof must fail before outer reread"))
    with pytest.raises(RuntimeError):orchestrator.qualification_vm.observe(orchestrator, namespace)


@pytest.fixture
def execution(orchestrator, namespace, observed, monkeypatch, tmp_path):
    q = orchestrator.qualification_vm
    registry = {"floating_ip": dict(tier=2, files=["tests/system/network/test_fixture.py"]),
                "env": dict(tier=3, files=["tests/system/env/test_fixture.py"])}
    source = dict(revision="f"*40, tree="e"*40, payload={path: "d"*64 for row in registry.values() for path in row["files"]})
    monkeypatch.setattr(q, "read_namespace", lambda *a: namespace)
    monkeypatch.setattr(q, "source_identity", lambda *a: copy.deepcopy(source))
    monkeypatch.setattr(q, "observe", lambda *a: copy.deepcopy(observed))
    monkeypatch.setattr(orchestrator, "_sha256_runner_input", lambda *a: "a"*64)
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.3.0-rc.fixture")
    monkeypatch.setattr(orchestrator, "_require_distinct_candidate_controller", lambda: None)
    monkeypatch.setattr(orchestrator, "_evidence_registry", lambda: registry)
    calls = []
    def forbidden(*a, **k):pytest.fail("retained mode crossed provisioning/local execution boundary")
    for name in ("mvm", "provision_t1", "provision_t2", "destroy_vm", "ensure_shared_volume", "ensure_test_network",
                 "_install_system_binary_in_runner", "_initialize_system_binary_in_runner", "_run_local_pytest", "_get_mvm_version"):
        monkeypatch.setattr(orchestrator, name, forbidden)
    monkeypatch.setattr(q, "stage", lambda *a: "/home/runner/.cache/mvmctl-qualification-" + "a"*32)
    def pytest_run(o, ns, guest_task, actual_source, files, label, *, collect=False, capture=None):
        calls.append((label, files))
        nodes = [path + "::test_fixture" for path in files]
        result = subprocess.CompletedProcess([], 0, "literal output", "")
        if collect:
            for name,raw in (("collection-native.json",b'{"returncode":0}'),("collection-stdout",b'literal output'),
                             ("collection-stderr",b''),("collection-nodes.json",_node_report(nodes,collect_only=True))):
                q._write(capture.task,name,raw)
            return result, _node_report(nodes, collect_only=True)
        capture.record_runtime_source(json.dumps(source["payload"]).encode(), source["payload"])
        capture.record_pytest(result, _complete_pytest_outcome_report(collected=len(nodes)))
        capture.record_nodes(_node_report(nodes));capture.record_junit(_junit(__import__('system_test_evidence'), nodes))
        capture.report_cleanup = True
        return result, None
    monkeypatch.setattr(q, "_pytest", pytest_run)
    task = tmp_path / "task";task.mkdir(mode=0o700)
    args = orchestrator._build_parser().parse_args(["floating_ip","env","--qualification-vm",namespace["runner"],
        "--qualification-namespace",str(tmp_path/"namespace.json"),"--record-evidence",str(task)])
    return SimpleNamespace(q=q, o=orchestrator, args=args, task=task, calls=calls, source=source, registry=registry, before=observed)


def test_retained_execution_preserves_tiers_and_never_provisions_or_runs_local(execution):
    e=execution
    assert e.q.run(e.o,e.args) is True
    binding=json.loads((e.task/'binding.json').read_text())
    assert binding['selection']['floating_ip']['tier']==2 and binding['selection']['env']['tier']==3
    for name in ('floating_ip','env'):
        assert json.loads((e.task/(name+'.result.json')).read_text())['runner_cleanup'] is None
    evidence=__import__('system_test_evidence')
    assert evidence.verify_matrix_task(e.task,e.source,binding['candidate'],e.registry)['domains']=={'floating_ip':1,'env':1}
    assert [name for name,_ in e.calls]==['floating_ip','env']


def test_first_failed_domain_leaves_remaining_selection_unproved(execution, monkeypatch):
    e=execution
    def failed(*a,**k):raise RuntimeError('packet did not fence')
    monkeypatch.setattr(e.q,'_pytest',failed)
    assert e.q.run(e.o,e.args) is False
    terminal=json.loads((e.task/'terminal.json').read_text())
    assert terminal['passed'] is False and terminal['domains']==['floating_ip']
    assert not (e.task/'env.result.json').exists()
    with pytest.raises(RuntimeError,match='incomplete or failed'):
        __import__('system_test_evidence').verify_matrix_task(e.task,e.source,json.loads((e.task/'binding.json').read_text())['candidate'],e.registry)


def test_candidate_mismatch_refuses_before_source_staging(execution, monkeypatch):
    e=execution
    e.before['guest']['controller']['sha256']='c'*64
    monkeypatch.setattr(e.q,'stage',lambda *a:pytest.fail('candidate mismatch must not stage'))
    with pytest.raises(RuntimeError,match='candidate differs'):e.q.run(e.o,e.args)
    assert list(e.task.iterdir())==[]


def test_remote_source_drift_prevents_a_runtime_receipt(orchestrator, namespace, monkeypatch):
    q=orchestrator.qualification_vm
    monkeypatch.setattr(q,'_command',lambda *a,**k:subprocess.CompletedProcess([],0,'{}',''))
    with pytest.raises(RuntimeError,match='staged source changed'):
        q._runtime_source(orchestrator,namespace,'/owned/task',dict(payload={'tests/system/test_fixture.py':'a'*64}))


@pytest.mark.parametrize('fault',[None,'missing','malformed','deselected','wrong_nodes'])
def test_remote_pytest_needs_strict_literal_reports(orchestrator,namespace,monkeypatch,tmp_path,fault):
    q=orchestrator.qualification_vm
    evidence=__import__('system_test_evidence')
    files=['tests/system/network/test_fixture.py'];nodes=[files[0]+'::test_fixture']
    expected={files[0]:'d'*64}
    task=tmp_path/'capture';task.mkdir(mode=0o700)
    capture=evidence.DomainEvidence(task,'floating_ip',2,files,expected)
    calls=[]
    def command(*args,**kwargs):
        calls.append(args[2]);return subprocess.CompletedProcess([],0,'','')
    monkeypatch.setattr(q,'_command',command)
    monkeypatch.setattr(q,'_runtime_source',lambda *a:(json.dumps(expected).encode(),expected))
    def execute(o,ns,cmd,**kwargs):
        assert '--rootdir=.' in cmd and '/mnt' not in cmd and ' -x ' in cmd
        return subprocess.CompletedProcess([],0,'literal pytest','')
    monkeypatch.setattr(q,'_exec',execute)
    def fetch(o,ns,path):
        if path.endswith('nodes.json'):
            return _node_report(['tests/system/network/test_other.py::other'] if fault=='wrong_nodes' else nodes)
        if path.endswith('pytest.xml'):return _junit(evidence,nodes)
        if fault=='missing':raise RuntimeError('report missing')
        if fault=='malformed':return b'{}'
        raw=json.loads(_complete_pytest_outcome_report())
        if fault=='deselected':raw['deselected']=1
        return json.dumps(raw).encode()
    monkeypatch.setattr(q,'_fetch',fetch)
    args=(orchestrator,namespace,'/home/runner/.cache/mvmctl-qualification-'+'a'*32,dict(payload=expected),files,'floating_ip')
    if fault is None:
        q._pytest(*args,capture=capture)
        assert capture.report_cleanup is True
        assert any('unlink' in script for script in calls)
    else:
        with pytest.raises(RuntimeError):q._pytest(*args,capture=capture)
        assert capture.report_cleanup is None and not any('unlink' in script for script in calls)


def test_absent_worker_fixture_cannot_start_fault_pytest(execution,monkeypatch):
    e=execution
    path='tests/system/network/test_floating_ip_worker_fault.py'
    e.registry['floating_ip_worker_fault']=dict(tier=2,files=[path]);e.source['payload'][path]='d'*64
    e.args.domains=['floating_ip_worker_fault']
    monkeypatch.setattr(e.q,'_worker_fixture',lambda *a:(_ for _ in ()).throw(RuntimeError('fixed worker fixture unavailable')))
    monkeypatch.setattr(e.q,'_pytest',lambda *a,**k:pytest.fail('fault tests must not run without fixed fixture'))
    with pytest.raises(RuntimeError,match='fixture unavailable'):e.q.run(e.o,e.args)
    assert not (e.task/'floating_ip_worker_fault.native.json').exists()



def test_full_collection_is_remote_and_uses_existing_collection_receipts(execution):
    e=execution
    e.args.domains=[];e.args.collect_release_nodes=e.task;e.args.record_evidence=None
    assert e.q.run(e.o,e.args) is True
    binding=json.loads((e.task/'binding.json').read_text())
    checked=__import__('system_test_evidence').verify_collection_task(e.task,e.source,binding['candidate'],e.registry)
    assert set(checked['nodes'])=={'floating_ip','env'}
    assert [name for name,_ in e.calls]==['collection']



def test_remote_command_budget_keeps_default_probe_bounded(orchestrator, namespace, monkeypatch):
    calls = []
    def mvm(*args, **kwargs):
        calls.append((args, kwargs))
        return subprocess.CompletedProcess([], 0, "probe-ok", "")
    monkeypatch.setattr(orchestrator, "mvm", mvm)
    result = orchestrator.qualification_vm._command(orchestrator, namespace, "print('probe-ok')")
    assert result.stdout == "probe-ok" and len(calls) == 1
    argv, options = calls[0]
    assert argv[:7] == ("exec", namespace["runner_id"], "--user", "runner", "--timeout", "60", "--")
    assert options == {"check": True, "timeout": 90}
    assert "/usr/bin/python3" in argv[-1] and "print" in argv[-1]


def test_remote_command_budget_reaches_actual_long_pytest_mvm_argv(orchestrator, namespace, monkeypatch, tmp_path):
    q = orchestrator.qualification_vm
    evidence = __import__("system_test_evidence")
    files = ["tests/system/network/test_fixture.py"]
    nodes = [files[0] + "::test_fixture"]
    expected = {files[0]: "d" * 64}
    capture_task = tmp_path / "capture"; capture_task.mkdir(mode=0o700)
    capture = evidence.DomainEvidence(capture_task, "floating_ip", 2, files, expected)
    calls = []
    def mvm(*args, **kwargs):
        calls.append((args, kwargs))
        return subprocess.CompletedProcess([], 0, "literal pytest output", "")
    monkeypatch.setattr(orchestrator, "mvm", mvm)
    # Keep _exec and _command real: only source/report observations are injected.
    monkeypatch.setattr(q, "_runtime_source", lambda *a: (json.dumps(expected).encode(), expected))
    def fetch(o, ns, path):
        if path.endswith("nodes.json"):return _node_report(nodes)
        if path.endswith("pytest.xml"):return _junit(evidence, nodes)
        return _complete_pytest_outcome_report()
    monkeypatch.setattr(q, "_fetch", fetch)
    q._pytest(orchestrator, namespace, "/home/runner/.cache/mvmctl-qualification-" + "a" * 32,
              dict(payload=expected), files, "floating_ip", capture=capture)
    pytest_calls = [(args, kwargs) for args, kwargs in calls if " -m pytest " in args[-1]]
    assert len(pytest_calls) == 1
    argv, options = pytest_calls[0]
    assert argv[:7] == ("exec", namespace["runner_id"], "--user", "runner", "--timeout", "1800", "--")
    assert options == {"check": False, "timeout": 1830}
    assert capture.report_cleanup is True
    short_calls = [(args, kwargs) for args, kwargs in calls if " -m pytest " not in args[-1]]
    assert short_calls and all(args[5] == "60" and kwargs["timeout"] == 90 for args, kwargs in short_calls)
