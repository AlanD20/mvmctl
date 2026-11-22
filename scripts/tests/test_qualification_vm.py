"""Explicit retained-target admission and effect-boundary behavior, without real VMs."""

import base64
import copy
import hashlib
import json
from pathlib import Path
import shlex
import subprocess
from types import SimpleNamespace

import pytest

from test_run_system_tests import orchestrator, _complete_pytest_outcome_report, _minimal_runner_elf  # noqa: F401
from test_system_test_evidence import _junit, _node_report


@pytest.mark.parametrize("missing", [None, "group_exists", "user_in_group", "sudoers_exists"])
def test_probe_requires_live_authority_without_setup_history(orchestrator, missing):
    probe = {}
    exec(orchestrator.qualification_vm._HOST_AUTHORITY_PROBE, probe)
    status = dict(group_exists=True, user_in_group=True, sudoers_exists=True, state=None)
    if missing is None:
        assert probe["require_host_authority"](status) is None
    else:
        status[missing] = False
        with pytest.raises(RuntimeError, match="administrator admission is missing"):
            probe["require_host_authority"](status)


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
    original_admit = q._admit_source_kernel
    monkeypatch.setattr(q, "_admit_source_kernel", lambda *a: orchestrator.ImportedRunnerKernelBinding("d"*64, "e"*64))
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
    def pytest_run(o, ns, guest_task, actual_source, files, label, *, collect=False, capture=None, kernel_binding=None, admin_stall=None):
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
    return SimpleNamespace(q=q, o=orchestrator, args=args, task=task, calls=calls, source=source, registry=registry,
                           before=observed, original_admit=original_admit)


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


@pytest.mark.parametrize("fault", ["missing", "id_only", "sha_only", "malformed", "mismatch"])
def test_env_source_kernel_pair_refuses_before_staging(execution, monkeypatch, fault):
    e = execution
    monkeypatch.setattr(e.q, "_admit_source_kernel", e.original_admit)
    monkeypatch.setattr(e.q, "stage", lambda *a: pytest.fail("invalid kernel pair must not stage or test"))
    monkeypatch.delenv("MVM_RUNNER_KERNEL_ID", raising=False)
    monkeypatch.delenv("MVM_RUNNER_KERNEL_SHA256", raising=False)
    if fault in ("id_only", "malformed", "mismatch"):
        monkeypatch.setenv("MVM_RUNNER_KERNEL_ID", "bad" if fault == "malformed" else "d"*64)
    if fault in ("sha_only", "malformed", "mismatch"):
        monkeypatch.setenv("MVM_RUNNER_KERNEL_SHA256", "e"*64)
    with pytest.raises(RuntimeError, match="explicit MVM_RUNNER_KERNEL|binding is invalid"):
        e.q.run(e.o, e.args)
    assert list(e.task.iterdir()) == []


@pytest.mark.parametrize("fault", [None, "metadata", "hash", "symlink"])
def test_source_kernel_admission_checks_target_metadata_and_actual_elf(orchestrator, namespace, monkeypatch, tmp_path, fault):
    payload = _minimal_runner_elf()
    digest = hashlib.sha256(payload).hexdigest()
    kernel_id = hashlib.sha256(f"{digest}:7.0.11:x86_64".encode()).hexdigest()
    monkeypatch.setenv("MVM_RUNNER_KERNEL_ID", kernel_id)
    monkeypatch.setenv("MVM_RUNNER_KERNEL_SHA256", digest)
    namespace["paths"]["cache"] = str(tmp_path / "cache")
    path = Path(namespace["paths"]["cache"]) / "kernels" / kernel_id
    path.parent.mkdir(parents=True)
    path.write_bytes(payload if fault != "hash" else b"wrong ELF bytes")
    if fault == "symlink":
        target = path.with_name("other");path.rename(target);path.symlink_to(target)
    info = dict(kernel=dict(id=kernel_id, type="custom", version="7.0.11", arch="x86_64", is_present=True),
                storage=dict(path=str(path)))
    if fault == "metadata":info["kernel"]["version"] = "7.0.10"
    calls = []
    def mvm(*args, **kwargs):
        # Keep the real _exec/_command and run their actual isolated ELF reader.
        command = shlex.split(shlex.split(args[-1])[-1]);calls.append(command)
        if command[0] == "/usr/local/bin/mvm":
            assert command == ["/usr/local/bin/mvm", "kernel", "inspect", kernel_id, "--json"]
            return subprocess.CompletedProcess([], 0, json.dumps(info), "")
        assert command[:5] == ["/usr/bin/python3", "-I", "-S", "-B", "-c"]
        result = subprocess.run(command, capture_output=True, text=True, timeout=10)
        result.check_returncode()
        return result
    monkeypatch.setattr(orchestrator, "mvm", mvm)
    if fault is None:
        admitted = orchestrator.qualification_vm._admit_source_kernel(orchestrator, namespace)
        assert (admitted.id, admitted.sha256) == (kernel_id, digest)
        assert admitted.build_id and len(calls) == 2
    else:
        with pytest.raises((RuntimeError, subprocess.CalledProcessError)):
            orchestrator.qualification_vm._admit_source_kernel(orchestrator, namespace)
        assert len(calls) == (1 if fault == "metadata" else 2)


@pytest.mark.parametrize("when", ["before", "after"])
def test_source_kernel_drift_prevents_env_pass(execution, monkeypatch, when):
    e = execution
    calls = []
    def admit(*args):
        calls.append(None)
        changed = len(calls) >= (2 if when == "before" else 3)
        return e.o.ImportedRunnerKernelBinding("d"*64, "e"*64, build_id="bb" if changed else "aa")
    monkeypatch.setattr(e.q, "_admit_source_kernel", admit)
    assert e.q.run(e.o, e.args) is False
    result = json.loads((e.task / "env.result.json").read_text())
    assert result["passed"] is False and f"changed {when} env execution" in result["output"]
    assert [label for label, files in e.calls] == (["floating_ip"] if when == "before" else ["floating_ip", "env"])


@pytest.mark.parametrize("label", ["floating_ip", "env", "floating_ip_worker_fault"])
def test_remote_command_budget_reaches_actual_long_pytest_mvm_argv(orchestrator, namespace, monkeypatch, tmp_path, label):
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
    binding = orchestrator.ImportedRunnerKernelBinding("d"*64, "e"*64) if label == "env" else None
    q._pytest(orchestrator, namespace, "/home/runner/.cache/mvmctl-qualification-" + "a" * 32,
              dict(payload=expected), files, label, capture=capture, kernel_binding=binding,
              admin_stall={} if label == "floating_ip_worker_fault" else None)
    pytest_calls = [(args, kwargs) for args, kwargs in calls if " -m pytest " in args[-1]]
    assert len(pytest_calls) == 1
    argv, options = pytest_calls[0]
    assert argv[:7] == ("exec", namespace["runner_id"], "--user", "runner", "--timeout", "1800", "--")
    assert options == {"check": False, "timeout": 1830}
    assert ("MVM_RUNNER_KERNEL_ID=" + "d"*64 in argv[-1]) is (label == "env")
    assert ("MVM_RUNNER_KERNEL_SHA256=" + "e"*64 in argv[-1]) is (label == "env")
    assert ("MVM_WORKER_STALL_ADMIN_TICKET=" in argv[-1]) is (label == "floating_ip_worker_fault")
    assert capture.report_cleanup is True
    short_calls = [(args, kwargs) for args, kwargs in calls if " -m pytest " not in args[-1]]
    assert short_calls and all(args[5] == "60" and kwargs["timeout"] == 90 for args, kwargs in short_calls)


@pytest.fixture
def admin_stall(orchestrator, namespace, observed):
    import os
    helper = orchestrator.qualification_vm._admin_module(orchestrator)
    raw = (orchestrator._REPO_ROOT / orchestrator.qualification_vm._ADMIN_HELPER).read_bytes()
    ticket = dict(schema_version=1,nonce='a'*32,hostname='mvmctl-030-test',
        boot_id=Path('/proc/sys/kernel/random/boot_id').read_text().strip(),operator=namespace['operator'],
        source_sha256=hashlib.sha256(raw).hexdigest(),candidate_sha256='b'*64)
    descriptor = dict(namespace=namespace,source=dict(payload={orchestrator.qualification_vm._ADMIN_HELPER:ticket['source_sha256']}),
        guest_task='/home/runner/.cache/mvmctl-qualification-'+'a'*32,ticket=ticket,before=observed,candidate=dict(sha256='b'*64))
    request = dict(ticket,vm_id='c'*32)
    admission = dict(vm_id=request['vm_id'],owner_uid=1000,boot_id=ticket['boot_id'],launch_generation=2,
        pid=123,start_time_ticks=456,all_threads_stopped=True)
    return helper,descriptor,request,admission


@pytest.mark.parametrize('fault',[None,'argv','pid','path','signal','nonce','vm','principal','source'])
def test_admin_worker_stall_request_is_one_closed_operation(admin_stall,fault):
    helper,descriptor,request,admission=admin_stall
    if fault in ('argv','pid','path','signal'):request[fault]='unapproved'
    elif fault=='nonce':request['nonce']='bad'
    elif fault=='vm':request['vm_id']='1'
    elif fault=='principal':request['operator']['uid']=0
    elif fault=='source':request['source_sha256']='wrong'
    if fault is None:assert helper['_admin_validate'](request,request=True) is request
    else:
        with pytest.raises((AssertionError,TypeError)):helper['_admin_validate'](request,request=True)


@pytest.mark.parametrize('fault',['nonce','source','boot','operator'])
def test_admin_worker_request_cannot_rebind_ticket(orchestrator,admin_stall,fault):
    _,descriptor,request,_=admin_stall
    request=copy.deepcopy(request)
    if fault=='nonce':request['nonce']='d'*32
    elif fault=='source':request['source_sha256']='e'*64
    elif fault=='boot':request['boot_id']='11111111-1111-4111-8111-111111111111'
    else:request['operator']['gid']=0
    with pytest.raises((RuntimeError,AssertionError)):
        orchestrator.qualification_vm._require_admin_request(orchestrator,descriptor,request)


@pytest.mark.parametrize('failure',[None,'admission','test','ack'])
def test_operator_admin_stall_always_requests_exact_cont(orchestrator,admin_stall,monkeypatch,tmp_path,failure):
    import os
    helper,descriptor,request,admission=admin_stall
    # Execute the real context manager with only its transport wait injected.
    namespace=helper['_administrator_stall'].__wrapped__.__globals__
    monkeypatch.setattr(Path,'home',classmethod(lambda cls:tmp_path))
    task=tmp_path/'.cache'/('mvmctl-qualification-'+'a'*32);task.mkdir(parents=True,mode=0o700)
    ticket=descriptor['ticket'];helper['_admin_write'](task/'worker-stall-admin.json',ticket)
    monkeypatch.setenv('MVM_WORKER_STALL_ADMIN_TICKET',str(task/'worker-stall-admin.json'))
    def wait(path,owner,deadline):
        actual=helper['_admin_read'](task/'worker-stall-request.json',os.getuid())
        if path.name=='worker-stall-admitted.json':
            if failure=='admission':raise AssertionError('admission unavailable')
            return dict(request=actual,admission=admission)
        return dict(request=actual,returncode=1 if failure=='ack' else 0,output='CONTINUED_EXACT_ROOT_WORKER')
    monkeypatch.setitem(namespace,'_admin_wait',wait)
    def run():
        with helper['_administrator_stall'](request['vm_id']) as received:
            assert received==admission
            if failure=='test':raise AssertionError('test failure')
    if failure is None:run()
    else:
        with pytest.raises(AssertionError):run()
    assert helper['_admin_read'](task/'worker-stall-continue.json',os.getuid())==request


@pytest.mark.parametrize('fault',[None,'admission','publish','continue','native','output','cancelled'])
def test_outside_admin_observer_finally_cont_and_independent_closure(
    orchestrator,admin_stall,monkeypatch,tmp_path,fault,
):
    import io
    q=orchestrator.qualification_vm;_,descriptor,request,admission=admin_stall
    evidence=tmp_path/'evidence';evidence.mkdir(mode=0o700)
    q._write(evidence,'admin-worker-request.json',q._encode(request))
    sent=[];published=[]
    class Channel:
        returncode=None
        stdout=io.StringIO('not json\n' if fault=='admission' else json.dumps(admission)+'\n')
        def communicate(self,value,timeout):
            sent.append(value);self.returncode=1 if fault=='native' else 0
            return ('wrong' if fault=='output' else 'CONTINUED_EXACT_ROOT_WORKER\n','')
    child=Channel()
    monkeypatch.setattr(q.select,'select',lambda readers,*a:(readers,[],[]))
    def publish(o,d,name,value):
        published.append((name,value))
        if fault=='publish' and name=='worker-stall-admitted.json':raise RuntimeError('publication unavailable')
    monkeypatch.setattr(q,'_admin_guest_write',publish)
    reads=[]
    def read(*args):
        reads.append(args[-1])
        if len(reads)==1:return request if fault=='cancelled' else None
        return dict(request,nonce='e'*32) if fault=='continue' else request
    monkeypatch.setattr(q,'_admin_guest_read',read)
    def run():return q._drive_admin_observer(orchestrator,descriptor,request,['explicit-root-observer'],evidence,popen=lambda *a,**k:child)
    if fault is None:
        proof=run();assert proof['returncode']==0 and proof['admission']==admission
        assert q._join_admin_stall(orchestrator,descriptor,evidence)==proof
    else:
        with pytest.raises((RuntimeError,AssertionError)):run()
    if fault=='cancelled':
        assert sent==[] and not (evidence/'admin-worker-observer.json').exists()
        return
    assert sent==['CONT\n']
    assert (evidence/'admin-worker-observer.json').exists()
    assert published[-1][0]=='worker-stall-continued.json'


@pytest.mark.parametrize('fault',['missing','wrong-request','nonzero','wrong-thaw','wrong-source'])
def test_admin_stall_requires_independent_root_native_proof(orchestrator,admin_stall,tmp_path,fault):
    q=orchestrator.qualification_vm;_,descriptor,request,admission=admin_stall
    task=tmp_path/'proof';task.mkdir(mode=0o700)
    q._write(task,'admin-worker-request.json',q._encode(request))
    raw=(orchestrator._REPO_ROOT/q._ADMIN_HELPER).read_bytes()
    proof=dict(request=copy.deepcopy(request),admission=admission,returncode=0,output='CONTINUED_EXACT_ROOT_WORKER',stderr='',
        payload_sha256=hashlib.sha256(q._root_stall_payload(raw,request).encode()).hexdigest(),error=None)
    if fault=='wrong-request':proof['request']['vm_id']='d'*32
    elif fault=='nonzero':proof['returncode']=1
    elif fault=='wrong-thaw':proof['output']='wrong'
    elif fault=='wrong-source':proof['payload_sha256']='0'*64
    if fault!='missing':q._write(task,'admin-worker-observer.json',q._encode(proof))
    with pytest.raises((RuntimeError,OSError)):q._join_admin_stall(orchestrator,descriptor,task)


@pytest.mark.parametrize('value,domains',[('true',['floating_ip_worker_fault']),('1',['floating_ip']),('1',['floating_ip_worker_fault','env'])])
def test_admin_stall_mode_never_enables_other_domains(execution,orchestrator,monkeypatch,value,domains):
    args=execution.args;args.domains=domains
    monkeypatch.setenv('MVM_QUALIFICATION_ADMIN_STALL',value)
    with pytest.raises(RuntimeError,match='whole worker-fault domain'):orchestrator.qualification_vm.run(orchestrator,args)


def test_explicit_admin_mode_only_runs_whole_fault_case_without_sudo_fixture(execution,admin_stall,monkeypatch):
    e=execution;q=e.q;_,descriptor,request,_=admin_stall
    path='tests/system/network/test_floating_ip_worker_fault.py'
    e.registry['floating_ip_worker_fault']=dict(tier=2,files=[path]);e.source['payload'][path]='d'*64
    e.args.domains=['floating_ip_worker_fault']
    monkeypatch.setenv('MVM_QUALIFICATION_ADMIN_STALL','1')
    monkeypatch.setattr(q,'_prepare_admin_stall',lambda *a:descriptor)
    monkeypatch.setattr(q,'_read_admin_ticket',lambda *a:descriptor)
    monkeypatch.setattr(q,'_worker_fixture',lambda *a:pytest.fail('explicit administrator mode must not adopt sudo installation'))
    proof=dict(request=request,returncode=0)
    def operation(o,d,t,execute):execute();return proof
    monkeypatch.setattr(q,'_run_admin_stall_case',operation)
    monkeypatch.setattr(q,'_join_admin_stall',lambda *a:proof)
    assert q.run(e.o,e.args) is True
    assert [name for name,_ in e.calls]==['floating_ip_worker_fault']
    assert set(json.loads((e.task/'qualification-vm.json').read_text()))=={'namespace','before','after','guest_task','fixture_before','fixture_after'}
    assert json.loads(json.loads((e.task/'floating_ip_worker_fault.result.json').read_text())['output'])==proof
    binding=json.loads((e.task/'binding.json').read_text())
    verified=__import__('system_test_evidence').verify_matrix_task(e.task,e.source,binding['candidate'],e.registry)
    assert verified['domains']=={'floating_ip_worker_fault':1}


def test_admin_target_probe_uses_bounded_arguments_not_full_source_manifest(orchestrator,admin_stall,monkeypatch):
    q=orchestrator.qualification_vm;_,descriptor,request,_=admin_stall
    descriptor['source']['payload']={'tests/system/full_source_'+str(index)+'.py':'d'*64 for index in range(2212)}
    assert len(json.dumps(descriptor))>128*1024
    calls=[]
    monkeypatch.setattr(q,'_outer',lambda *a:descriptor['before']['outer'])
    monkeypatch.setattr(orchestrator,'_host_reboot_controller',lambda:descriptor['before']['outer_controller'])
    def mvm(*args,**kwargs):
        calls.append(args);return subprocess.CompletedProcess(args,0,'','')
    monkeypatch.setattr(orchestrator,'mvm',mvm)
    q._observe_admin_stall_target(orchestrator,descriptor,request)
    assert len(calls)==1
    environment=shlex.split(calls[0][-1]);guest=shlex.split(environment[-1])
    probe=json.loads(guest[-2]);target=json.loads(guest[-1])
    assert set(probe)=={'namespace','namespace_identity'}
    assert probe['namespace']==descriptor['namespace']
    assert probe['namespace_identity']==descriptor['before']['guest']['namespace_identity']
    assert target==request
    assert max(len(argument.encode()) for argument in calls[0])<16384


@pytest.mark.parametrize('binding',['outer','controller'])
def test_admin_target_changed_outer_binding_refuses_before_remote_probe(orchestrator,admin_stall,monkeypatch,binding):
    q=orchestrator.qualification_vm;_,descriptor,request,_=admin_stall
    monkeypatch.setattr(q,'_outer',lambda *a: {} if binding=='outer' else descriptor['before']['outer'])
    monkeypatch.setattr(orchestrator,'_host_reboot_controller',lambda: {} if binding=='controller' else descriptor['before']['outer_controller'])
    monkeypatch.setattr(q,'_command',lambda *a:pytest.fail('changed admission must refuse before remote probe'))
    with pytest.raises(RuntimeError,match='changed'):q._observe_admin_stall_target(orchestrator,descriptor,request)


@pytest.mark.parametrize('reason',['missing proof','changed proof'])
def test_final_admin_proof_failure_cannot_emit_a_passing_matrix(execution,admin_stall,monkeypatch,reason):
    e=execution;q=e.q;_,descriptor,request,_=admin_stall
    path='tests/system/network/test_floating_ip_worker_fault.py'
    e.registry['floating_ip_worker_fault']=dict(tier=2,files=[path]);e.source['payload'][path]='d'*64
    e.args.domains=['floating_ip_worker_fault'];monkeypatch.setenv('MVM_QUALIFICATION_ADMIN_STALL','1')
    monkeypatch.setattr(q,'_prepare_admin_stall',lambda *a:descriptor)
    monkeypatch.setattr(q,'_read_admin_ticket',lambda *a:descriptor)
    joins=[]
    def join(*args):
        joins.append(args)
        if len(joins)>1:raise RuntimeError(reason)
        return dict(request=request,returncode=0)
    monkeypatch.setattr(q,'_join_admin_stall',join)
    def execute(o,d,t,operation):
        operation();return q._join_admin_stall(o,d,t)
    monkeypatch.setattr(q,'_run_admin_stall_case',execute)
    assert q.run(e.o,e.args) is False
    assert len(joins)==2
    terminal=json.loads((e.task/'terminal.json').read_text())
    assert terminal['passed'] is False
    error=json.loads((Path(str(e.task)+'.worker-stall-admin')/'final-join-error.json').read_text())
    assert error['error']==reason
    candidate=json.loads((e.task/'binding.json').read_text())['candidate']
    with pytest.raises(RuntimeError,match='incomplete or failed'):
        __import__('system_test_evidence').verify_matrix_task(e.task,e.source,candidate,e.registry)


@pytest.mark.parametrize('comment',['',' root@mvmctl-030-test',' operator key comment with spaces'])
def test_admin_ssh_command_accepts_matching_key_with_optional_comment(orchestrator,namespace,tmp_path,comment):
    known=tmp_path/'known-hosts';key=tmp_path/'outside-private-key'
    known.write_text(namespace['expected_vm']['ipv4']+' '+namespace['expected_vm']['ssh_host_key']+comment+'\n')
    original=known.read_bytes()
    payload='pinned observer payload'
    command=orchestrator.qualification_vm._admin_ssh_command(namespace,key,known,payload)
    assert command[:5]==['ssh','-F','/dev/null','-i',str(key)]
    assert 'StrictHostKeyChecking=yes' in command and 'UserKnownHostsFile='+str(known) in command
    assert command[-2]=='root@'+namespace['expected_vm']['ipv4']
    assert shlex.split(command[-1])==['/usr/bin/python3','-I','-S','-B','-c',payload]
    assert known.read_bytes()==original and not key.exists()


@pytest.mark.parametrize('fault',['host','type','key'])
def test_admin_ssh_command_comment_does_not_weaken_exact_pin(orchestrator,namespace,tmp_path,fault):
    host=namespace['expected_vm']['ipv4'];kind,value=namespace['expected_vm']['ssh_host_key'].split()
    if fault=='host':host='10.25.0.99'
    elif fault=='type':kind='ssh-rsa'
    else:value=value[:-1]+('A' if value[-1]!='A' else 'B')
    known=tmp_path/'known-hosts';known.write_text(host+' '+kind+' '+value+' root@mvmctl-030-test\n')
    original=known.read_bytes()
    with pytest.raises(RuntimeError,match='differs from retained exact target'):
        orchestrator.qualification_vm._admin_ssh_command(namespace,tmp_path/'outside-private-key',known,'pinned observer')
    assert known.read_bytes()==original
