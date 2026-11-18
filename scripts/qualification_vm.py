"""Explicit retained mvmctl-030-test execution, using only the existing controller transport."""

from __future__ import annotations

import base64
import gzip
import hashlib
import ipaddress
import json
from pathlib import Path
import shlex
import subprocess
import uuid

from system_test_evidence import (
    MatrixEvidence, MAX_OUTPUT_BYTES, NODE_REPORT_ENV, _encode, _read, _write,
    require_node_report, selected_node_remainder, source_identity, verify_collection_task, verify_matrix_task,
)
from system_test_outcomes import PYTEST_OUTCOME_REPORT_ENV, require_complete_pytest_outcomes

_ALLOWED = {"floating_ip", "env", "floating_ip_worker_fault"}  # env's registered journeys support this actual nesting level.
_NAMESPACE_KEYS = {"schema_version", "runner", "runner_id", "operator", "paths"}


def validate_args(parser, args):
    if not args.qualification_vm:
        if args.qualification_namespace:
            parser.error("--qualification-namespace requires --qualification-vm")
        return
    if not args.qualification_namespace or args.qualification_vm != "mvmctl-030-test":
        parser.error("qualification mode requires the exact named QA VM and explicit namespace")
    if any((args.all, args.tier, args.host_direct, args.prepare, args.image, args.volume, args.rebuild,
            args.push, args.skip_volume_check, args.release_qualification, args.candidate_version,
            args.host_reboot_phase, args.host_reboot_task, args.host_reboot_namespace, args.host_reboot_envelope,
            args.official_kernel_carry, args.rc030_qa_source_carry)):
        parser.error("qualification mode forbids provisioning, local-host execution, reboot and historical carry")
    if args.collect_release_nodes:
        if args.domains or args.record_evidence or args.collection_task or args.selected_node_file or args.completed_matrix_task:
            parser.error("qualification collection is standalone")
    elif not args.domains or not args.record_evidence or set(args.domains) - _ALLOWED:
        parser.error("qualification execution requires recorded supported floating_ip/env domains; reboot and incompatible tiers unavailable")
    if bool(args.collection_task) != bool(args.selected_node_file) or args.completed_matrix_task and not args.collection_task:
        parser.error("qualification remainder requires collection task and exact selected-node file")


def read_namespace(o, path):
    data = o._host_reboot_json(o._host_reboot_read(path))
    if set(data) != _NAMESPACE_KEYS | {"expected_vm"}:
        raise RuntimeError("qualification namespace has unknown or missing fields")
    o._host_reboot_require_namespace({key: data[key] for key in _NAMESPACE_KEYS})
    expected = data["expected_vm"]
    if not isinstance(expected, dict) or set(expected) != {"created_at", "boot_id", "ipv4", "ssh_host_key"} or not isinstance(expected["created_at"], str) or not expected["created_at"]:
        raise RuntimeError("qualification expected VM identity is incomplete")
    if str(uuid.UUID(expected["boot_id"])) != expected["boot_id"] or str(ipaddress.IPv4Address(expected["ipv4"])) != expected["ipv4"]:
        raise RuntimeError("qualification boot/IP pin is not canonical")
    key = expected["ssh_host_key"].split()
    if len(key) != 2 or key[0] != "ssh-ed25519" or " ".join(key) != expected["ssh_host_key"]:
        raise RuntimeError("qualification requires one exact public SSH host-key pin")
    decoded = base64.b64decode(key[1], validate=True)
    if len(decoded) != 51 or decoded[:19] != b"\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20":
        raise RuntimeError("qualification SSH host key is invalid")
    return data


def _outer(o, namespace):
    raw = o.mvm("vm", "inspect", namespace["runner_id"], "--json", timeout=15)
    info = o._runner_binding_json(raw.stdout)
    row = info["vm"]
    if (row["id"] != namespace["runner_id"] or row["name"] != namespace["runner"] or row["status"] != "running"
            or row["created_at"] != namespace["expected_vm"]["created_at"]
            or info["networking"]["ipv4"] != namespace["expected_vm"]["ipv4"] or row["pid"] <= 0):
        raise RuntimeError("qualification VM identity differs from explicit target")
    pid = row["pid"]
    before = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
    if len(before) < 20 or before[0] in ("Z", "X") or int(before[19]) <= 0:
        raise RuntimeError("qualification VMM process is not live")
    if Path(f"/proc/{pid}/cgroup").read_text() != f"0::/mvmctl/{row['id']}\n":
        raise RuntimeError("qualification VMM cgroup differs")
    if Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()[19] != before[19]:
        raise RuntimeError("qualification VMM process changed")
    return dict(id=row["id"], name=row["name"], created_at=row["created_at"], pid=pid,
                start_ticks=int(before[19]), ipv4=info["networking"]["ipv4"])


def _exec(o, namespace, command, *, timeout=60, check=True):
    user, paths = namespace["operator"], namespace["paths"]
    environment = ["env", "HOME=" + user["home"], "USER=" + user["user"], "LOGNAME=" + user["user"],
                   "MVM_CACHE_DIR=" + paths["cache"], "MVM_CONFIG_DIR=" + paths["config"], "MVM_ASSET_MIRROR=" + paths["mirror"],
                   "PYTHONDONTWRITEBYTECODE=1", "sh", "-c", command]
    # The retained controller may enforce this guest-command budget itself.
    # Leave bounded transport time beyond it rather than racing the same deadline.
    return o.mvm("exec", namespace["runner_id"], "--user", user["user"], "--timeout", str(timeout), "--",
                 shlex.join(environment), check=check, timeout=timeout + 30)


def observe(o, namespace):
    # The isolated interpreter ignores caller Python configuration. No provisioning or downloads.
    script = r'''
import hashlib,json,os,pathlib,pwd,stat,subprocess,socket
expected = json.loads(EXPECTED)
user,paths = expected['operator'],expected['paths']
account = pwd.getpwuid(os.getuid())
assert os.getuid()==os.geteuid()==user['uid']>0 and os.getgid()==os.getegid()==user['gid']>0
assert pwd.getpwnam(user['user'])==account
assert dict(user=account.pw_name,uid=account.pw_uid,gid=account.pw_gid,home=account.pw_dir)==user
assert {key:os.environ[key] for key in ('HOME','USER','LOGNAME')}==dict(HOME=user['home'],USER=user['user'],LOGNAME=user['user'])
identities={}
for key,value in dict(home=user['home'],task_parent=user['home']+'/.cache',**paths).items():
 path=pathlib.Path(value);assert path.is_absolute() and path.resolve(strict=True)==path
 fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW|os.O_CLOEXEC)
 try:
  row=os.fstat(fd); assert (row.st_uid,row.st_gid)==(user['uid'],user['gid']) and not row.st_mode&0o022
  identity=lambda r:[r.st_dev,r.st_ino,r.st_uid,r.st_gid,stat.S_IMODE(r.st_mode)]
  assert identity(os.fstat(fd))==identity(row)==identity(path.lstat());identities[key]=identity(row)
 finally:os.close(fd)
path=pathlib.Path('/usr/local/bin/mvm');assert path.resolve(strict=True)==path
for parent in path.parents:
 info=parent.lstat();assert stat.S_ISDIR(info.st_mode) and info.st_uid==0 and not info.st_mode&0o022
row=path.stat();assert stat.S_ISREG(row.st_mode) and (row.st_uid,row.st_gid,stat.S_IMODE(row.st_mode),row.st_nlink)==(0,0,0o755,1)
identity=lambda r:[r.st_dev,r.st_ino,r.st_uid,r.st_gid,stat.S_IMODE(r.st_mode),r.st_nlink,r.st_size,r.st_mtime_ns,r.st_ctime_ns]
digest=hashlib.sha256(path.read_bytes()).hexdigest();assert identity(path.stat())==identity(row)
def cli(*args):
 return subprocess.check_output(['/usr/local/bin/mvm',*args],text=True,timeout=30)
def data(*args):
 envelope=json.loads(cli(*args,'--json'));assert envelope['schema_version']==1 and envelope['status']=='success' and envelope['error'] is None
 return envelope['data']
inventory={domain:data(domain,'ls') for domain in ('vm','network','volume','snapshot','image','kernel','bin','key')}
inventory['config']=data('config','ls');inventory['traffic']=data('policy','traffic','ls');inventory['exec']=data('policy','exec','ls')
assert inventory['vm']==[], 'qualification namespace must be quiescent'
assert all(any(row['type']==kind and row['version']=='1.17.0' and row['is_default'] and row['is_present'] for row in inventory['bin']) for kind in ('firecracker','jailer'))
assert any(row['type']=='alpine' and row['version']=='3.23' and row['is_present'] for row in inventory['image'])
assert any(row['is_default'] and row['is_present'] for row in inventory['kernel'])
status=data('host','status');assert status['user_in_group'] is True and status['state']['initialized'] is True
assert data('host','info')['dependencies']['nftables_available'] is True
hostkey=' '.join(pathlib.Path('/etc/ssh/ssh_host_ed25519_key.pub').read_text().split()[:2])
print(json.dumps(dict(hostname=socket.gethostname(),boot_id=pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
 namespace_identity=identities,host_key=hostkey,inventory=inventory,
 controller=dict(path=str(path),command=[str(path)],sha256=digest,identity=identity(row)),version=cli('--version').strip()),sort_keys=True))
'''.replace("EXPECTED", repr(json.dumps(namespace)))
    result = _exec(o, namespace, shlex.join(["/usr/bin/python3", "-I", "-S", "-B", "-c", script]))
    observed = o._host_reboot_json(result.stdout.encode())
    if (set(observed) != {"hostname", "boot_id", "namespace_identity", "host_key", "inventory", "controller", "version"}
            or observed["hostname"] != namespace["runner"] or observed["boot_id"] != namespace["expected_vm"]["boot_id"]
            or observed["host_key"] != namespace["expected_vm"]["ssh_host_key"]):
        raise RuntimeError("qualification guest boot, hostname or SSH pin changed")
    identities = observed["namespace_identity"]
    if (not isinstance(identities, dict) or set(identities) != {"home", "task_parent", "cache", "config", "mirror"}
            or any(not isinstance(value, list) or len(value) != 5 or any(type(n) is not int for n in value)
                   or value[2:4] != [namespace["operator"]["uid"], namespace["operator"]["gid"]]
                   or value[4] & 0o022 for value in identities.values())
            or set(observed["controller"]) != {"path", "command", "sha256", "identity"}
            or observed["controller"]["path"] != "/usr/local/bin/mvm"
            or observed["controller"]["command"] != ["/usr/local/bin/mvm"]
            or not isinstance(observed["controller"]["identity"], list)
            or len(observed["controller"]["identity"]) != 9
            or observed["controller"]["identity"][2:6] != [0, 0, 0o755, 1]):
        raise RuntimeError("qualification operator namespace or installed executable is unsafe")
    return dict(outer=_outer(o, namespace), guest=observed, outer_controller=o._host_reboot_controller())


def require_same(before, after):
    if before != after:
        raise RuntimeError("qualification VM/operator/candidate/inventory identity changed")


def _command(o, namespace, script, *args, timeout=60, check=True):
    return _exec(o, namespace, shlex.join(["/usr/bin/python3", "-I", "-S", "-B", "-c", script, *args]), timeout=timeout, check=check)


def stage(o, namespace, source):
    guest_task = namespace["operator"]["home"] + "/.cache/mvmctl-qualification-" + uuid.uuid4().hex
    _command(o, namespace, "import pathlib,sys; p=pathlib.Path(sys.argv[1]);p.mkdir(mode=0o700);(p/'source').mkdir(mode=0o700)", guest_task)
    for relative, digest in source["payload"].items():
        raw = (o._REPO_ROOT / relative).read_bytes()
        if hashlib.sha256(raw).hexdigest() != digest:
            raise RuntimeError("qualification source changed before staging")
        encoded = base64.b64encode(gzip.compress(raw, mtime=0)).decode()
        script = ("import base64,gzip,hashlib,pathlib,sys;raw=gzip.decompress(base64.b64decode(sys.argv[1]));"
                  "assert hashlib.sha256(raw).hexdigest()==sys.argv[2];p=pathlib.Path(sys.argv[3]);"
                  "p.parent.mkdir(parents=True,exist_ok=True);p.open('xb').write(raw)")
        _command(o, namespace, script, encoded, digest, guest_task + "/source/" + relative)
    return guest_task


def _runtime_source(o, namespace, guest_task, source):
    expected = {path: digest for path, digest in source["payload"].items() if path.startswith("tests/system/") or path in
                ("scripts/system_test_evidence.py", "scripts/system_test_outcomes.py")}
    script = "import hashlib,json,pathlib,sys; root=pathlib.Path(sys.argv[1]);print(json.dumps({p:hashlib.sha256((root/p).read_bytes()).hexdigest() for p in json.loads(sys.argv[2])},sort_keys=True))"
    raw = _command(o, namespace, script, guest_task + "/source", json.dumps(expected)).stdout.encode()
    if json.loads(raw) != expected:
        raise RuntimeError("qualification staged source changed")
    return raw, expected


def _fetch(o, namespace, path):
    result = _command(o, namespace, "import pathlib,sys;raw=pathlib.Path(sys.argv[1]).read_bytes();assert len(raw)<=int(sys.argv[2]);sys.stdout.buffer.write(raw)", path, str(MAX_OUTPUT_BYTES))
    return result.stdout.encode()


def _pytest(o, namespace, guest_task, source, files, label, *, collect=False, capture=None):
    report = guest_task + "/" + label
    _command(o, namespace, "import pathlib,sys;p=pathlib.Path(sys.argv[1]);p.mkdir(mode=0o700);(p/'tmp').mkdir(mode=0o700)", report)
    raw_source, expected = _runtime_source(o, namespace, guest_task, source)
    if capture is not None and not collect:
        capture.record_runtime_source(raw_source, expected)
    root = guest_task + "/source"
    cmd = ["/usr/bin/python3", "-B", "-m", "pytest", "-c", "tests/system/pytest.ini", "--rootdir=.", "-p", "system_test_evidence",
           "-p", "no:cacheprovider", "-p", "pytest_timeout", "--basetemp=" + report + "/tmp", "--junitxml=" + report + "/pytest.xml", "-x", "-q",
           *(["--collect-only"] if collect else []), *files]
    env = ["env", "PYTHONPATH=" + root + "/scripts", "PYTHONDONTWRITEBYTECODE=1", "PYTEST_DISABLE_PLUGIN_AUTOLOAD=1", "TMPDIR=" + report + "/tmp",
           "MVM_TEST_VM=" + namespace["runner"], "MVM_TEST_QUALIFICATION_HOST=" + namespace["runner"],
           NODE_REPORT_ENV + "=" + report + "/nodes.json", PYTEST_OUTCOME_REPORT_ENV + "=" + ("" if collect else report + "/outcomes.json")]
    result = _exec(o, namespace, "cd " + shlex.quote(root) + " && " + shlex.join([*env, *cmd]), timeout=1800, check=False)
    if collect:
        for name, raw in (("collection-native.json", _encode(dict(returncode=result.returncode))),
                          ("collection-stdout", result.stdout.encode()), ("collection-stderr", result.stderr.encode())):
            _write(capture.task, name, raw, MAX_OUTPUT_BYTES)
    else:
        capture.record_native(result)
    raw_nodes = _fetch(o, namespace, report + "/nodes.json")
    if collect:
        _write(capture.task, "collection-nodes.json", raw_nodes, MAX_OUTPUT_BYTES)
        require_node_report(raw_nodes, native=result.returncode, collect_only=True)
        _runtime_source(o, namespace, guest_task, source)
        return result, raw_nodes
    raw_outcomes = _fetch(o, namespace, report + "/outcomes.json")
    capture.record_outcomes(raw_outcomes); capture.record_nodes(raw_nodes)
    capture.record_junit(_fetch(o, namespace, report + "/pytest.xml"))
    outcome = require_complete_pytest_outcomes(raw_outcomes, process_returncode=result.returncode)
    capture.require_nodes(raw_nodes, result.returncode, outcome)
    _runtime_source(o, namespace, guest_task, source)
    # Only these literal report receivers belong to this invocation; staged source/temp diagnostics remain.
    _command(o, namespace, "import pathlib,sys;p=pathlib.Path(sys.argv[1]);[(p/name).unlink() for name in ('outcomes.json','nodes.json','pytest.xml')]", report)
    capture.report_cleanup = True
    return result, None


def _worker_fixture(o, namespace, guest_task):
    script = ("import json,runpy,sys;module=runpy.run_path(sys.argv[1]);"
              "check=module.get('validate_worker_fixture');assert check is not None,'fixed worker fixture unavailable';"
              "print(json.dumps(check(),sort_keys=True))")
    result = _command(o, namespace, script, guest_task + "/source/tests/system/network/floating_ip_worker_fault.py")
    return json.loads(result.stdout)


def run(o, args):
    namespace = read_namespace(o, args.qualification_namespace)
    source = source_identity(o._REPO_ROOT)
    artifact_sha256 = o._sha256_runner_input(Path(o.MVM_CANDIDATE_BINARY))
    o._require_distinct_candidate_controller()
    before = observe(o, namespace)
    # Only bytes are read from the workstation artifact. Its public version is
    # queried inside the admitted VM after installed bytes match that artifact.
    version = before["guest"]["version"]
    if (before["guest"]["controller"]["sha256"] != artifact_sha256 or not version.startswith("mvm ")
            or o._CANDIDATE_VERSION_PATTERN.fullmatch(version[4:]) is None):
        raise RuntimeError("qualification installed candidate differs from official paired artifact")
    candidate = dict(sha256=artifact_sha256, version=version)
    registry = o._evidence_registry()
    selection = registry if args.collect_release_nodes else {domain: registry[domain] for domain in args.domains}
    if args.selected_node_file:
        collection = verify_collection_task(args.collection_task, source, candidate, registry)
        for task in (args.collection_task, *args.completed_matrix_task):
            previous = json.loads(_read(task / "qualification-vm.json", MAX_OUTPUT_BYTES))
            if previous["namespace"] != namespace or previous["before"] != before:
                raise RuntimeError("qualification previous receipt names a different target/boot/namespace")
        reports = [verify_matrix_task(path, source, candidate, registry) for path in args.completed_matrix_task]
        remainder = selected_node_remainder(args.selected_node_file, collection, reports, [], set(selection))
        selection = {domain: {**registry[domain], "nodes": nodes} for domain, nodes in remainder.items()}
    capture = MatrixEvidence(args.collect_release_nodes or args.record_evidence,
        dict(schema_version=1, host=namespace["runner"], boot_id=before["guest"]["boot_id"], source=source,
             candidate=candidate, controller=before["guest"]["controller"], registry=registry, selection=selection))
    guest_task = stage(o, namespace, source)
    require_same(before, observe(o, namespace))
    fixture = _worker_fixture(o, namespace, guest_task) if "floating_ip_worker_fault" in selection and not args.collect_release_nodes else None
    observations = dict(namespace=namespace, before=before, after=None, guest_task=guest_task,
                        fixture_before=fixture, fixture_after=None)
    results = []
    try:
        if args.collect_release_nodes:
            _pytest(o, namespace, guest_task, source, [path for row in registry.values() for path in row["files"]],
                    "collection", collect=True, capture=capture)
            results = [dict(domain="full_collection", passed=True)]
        else:
            for domain, selected in selection.items():
                item = dict(domain=domain, tier=selected["tier"], passed=False, output="")
                domain_capture = capture.domain(domain, selected["tier"], selected.get("nodes", selected["files"]))
                try:
                    require_same(before, observe(o, namespace))
                    _pytest(o, namespace, guest_task, source, domain_capture.files, domain, capture=domain_capture)
                    require_same(before, observe(o, namespace))
                    item["passed"] = True
                except Exception as error:
                    item["output"] = str(error)
                finally:
                    domain_capture.close(item)
                results.append(item)
                if not item["passed"]:break
    except Exception as error:
        results.append(dict(domain="full_collection", passed=False, output=str(error)))
    finally:
        observations["after"] = observe(o, namespace)
        require_same(before, observations["after"])
        observations["fixture_after"] = _worker_fixture(o, namespace, guest_task) if fixture is not None else None
        if observations["fixture_after"] != fixture:
            raise RuntimeError("qualification worker fixture changed")
        if source_identity(o._REPO_ROOT) != source:
            raise RuntimeError("qualification source changed during execution")
        _write(capture.task, "qualification-vm.json", _encode(observations))
        capture.close(results, source)
    passed = bool(results) and len(results) == (1 if args.collect_release_nodes else len(selection)) and all(item["passed"] for item in results)
    print(json.dumps(dict(qualification_vm=namespace["runner_id"], passed=passed, results=results), sort_keys=True), flush=True)
    return passed
