"""Explicit retained mvmctl-030-test execution, using only the existing controller transport."""

from __future__ import annotations

import argparse
import base64
import gzip
import hashlib
import ipaddress
import inspect
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import select
import sys
import threading
import time
import runpy
import importlib.util
import uuid

from system_test_evidence import (
    MatrixEvidence, MAX_OUTPUT_BYTES, NODE_REPORT_ENV, _encode, _read, _write,
    require_node_report, selected_node_remainder, source_identity, verify_collection_task, verify_matrix_task,
)
from system_test_outcomes import PYTEST_OUTCOME_REPORT_ENV, require_complete_pytest_outcomes

_ALLOWED = {"floating_ip", "env", "floating_ip_worker_fault"}  # env's registered journeys support this actual nesting level.
_NAMESPACE_KEYS = {"schema_version", "runner", "runner_id", "operator", "paths"}

# Shared verbatim with the isolated probe so tests exercise its authority check.
# Host status deliberately leaves user-owned setup history (`state`) unset.
_HOST_AUTHORITY_PROBE = '''
def require_host_authority(status):
 if not all(status.get(field) is True for field in ('group_exists','user_in_group','sudoers_exists')):
  raise RuntimeError('qualification host administrator admission is missing')
'''


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
    script = _HOST_AUTHORITY_PROBE + r'''
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
require_host_authority(data('host','status'))
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


def _admit_source_kernel(o, namespace):
    kernel_id, digest = (os.environ.get(key) for key in ("MVM_RUNNER_KERNEL_ID", "MVM_RUNNER_KERNEL_SHA256"))
    if not kernel_id or not digest:
        raise RuntimeError("qualification env requires explicit MVM_RUNNER_KERNEL_ID and MVM_RUNNER_KERNEL_SHA256 together")
    binding = o.ImportedRunnerKernelBinding(kernel_id, digest)
    if (re.fullmatch(r"[0-9a-f]{64}", binding.id) is None
            or re.fullmatch(r"[0-9a-f]{64}", binding.sha256) is None
            or hashlib.sha256(f"{binding.sha256}:{binding.version}:{binding.arch}".encode()).hexdigest() != binding.id):
        raise RuntimeError("qualification source kernel ID/SHA256 binding is invalid")
    result = _exec(o, namespace, shlex.join(["/usr/local/bin/mvm", "kernel", "inspect", binding.id, "--json"]))
    info = o._runner_binding_json(result.stdout)
    kernel, storage = info.get("kernel", {}), info.get("storage", {})
    path = namespace["paths"]["cache"] + "/kernels/" + binding.id
    if (kernel.get("id"), kernel.get("type"), kernel.get("version"), kernel.get("arch"),
            storage.get("path")) != (binding.id, "custom", binding.version, binding.arch, path) or kernel.get("is_present") is not True:
        raise RuntimeError("qualification source kernel metadata/path differs from binding")
    # Reuse the existing stable no-follow ELF/hash/GNU-note proof, on the QA filesystem.
    program = "import os,stat,struct,hashlib; from pathlib import Path\n"
    program += "\n".join(inspect.getsource(function) for function in (
        o._open_directory_without_symlinks, o._imported_runner_elf_build_id, o._imported_runner_elf_identity,
    ))
    program += f"\nprint(_imported_runner_elf_identity(Path({path!r}), {binding.sha256!r}))\n"
    build_id = _command(o, namespace, program).stdout.strip()
    if re.fullmatch(r"[0-9a-f]+", build_id) is None:
        raise RuntimeError("qualification source kernel GNU build ID is invalid")
    return o.ImportedRunnerKernelBinding(binding.id, binding.sha256, build_id=build_id)


def _pytest(o, namespace, guest_task, source, files, label, *, collect=False, capture=None, kernel_binding=None, admin_stall=None):
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
    if admin_stall is not None:
        if collect or label != "floating_ip_worker_fault":
            raise RuntimeError("administrator stall ticket is only for the whole worker-fault domain")
        env += ["MVM_WORKER_STALL_ADMIN_TICKET=" + guest_task + "/worker-stall-admin.json"]
    if kernel_binding is not None:
        if collect or label != "env":
            raise RuntimeError("source kernel binding is only for qualification env execution")
        env += ["MVM_RUNNER_KERNEL_ID=" + kernel_binding.id, "MVM_RUNNER_KERNEL_SHA256=" + kernel_binding.sha256]
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
    admin_stall = os.environ.get("MVM_QUALIFICATION_ADMIN_STALL")
    if admin_stall is not None and (admin_stall != "1" or args.collect_release_nodes
            or args.domains != ["floating_ip_worker_fault"] or args.selected_node_file):
        raise RuntimeError("administrator stall mode requires only the whole worker-fault domain")
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
    kernel_binding = _admit_source_kernel(o, namespace) if "env" in selection and not args.collect_release_nodes else None
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
    admin_task = Path(str(capture.task) + ".worker-stall-admin") if admin_stall else None
    if admin_task is not None:admin_task.mkdir(mode=0o700)
    guest_task = stage(o, namespace, source)
    require_same(before, observe(o, namespace))
    fixture = (_prepare_admin_stall(o, namespace, guest_task, source, candidate, before, admin_task)
               if admin_stall else _worker_fixture(o, namespace, guest_task)
               if "floating_ip_worker_fault" in selection and not args.collect_release_nodes else None)
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
                    if domain == "env" and _admit_source_kernel(o, namespace) != kernel_binding:
                        raise RuntimeError("qualification source kernel changed before env execution")
                    kwargs = {"kernel_binding": kernel_binding} if domain == "env" else {}
                    if admin_stall:kwargs["admin_stall"] = fixture
                    try:
                        if admin_stall:
                            proof = _run_admin_stall_case(o, fixture, admin_task, lambda: _pytest(
                                o, namespace, guest_task, source, domain_capture.files, domain, capture=domain_capture, **kwargs))
                            item["output"] = json.dumps(proof,sort_keys=True)
                        else:
                            _pytest(o, namespace, guest_task, source, domain_capture.files, domain, capture=domain_capture, **kwargs)
                    finally:
                        if domain == "env" and _admit_source_kernel(o, namespace) != kernel_binding:
                            raise RuntimeError("qualification source kernel changed after env execution")
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
        observations["fixture_after"] = (_read_admin_ticket(o, fixture) if admin_stall
                                          else _worker_fixture(o, namespace, guest_task) if fixture is not None else None)
        if admin_stall:
            try:
                proof = _join_admin_stall(o,fixture,admin_task)
                if not results or json.loads(results[-1]["output"]) != proof:
                    raise RuntimeError("administrator observer proof changed after domain validation")
            except Exception as error:
                _write(admin_task,"final-join-error.json",_encode(dict(error=str(error))))
                for item in results:
                    item["passed"] = False
                    item["output"] = "final administrator observer proof failed: " + str(error)
        if observations["fixture_after"] != fixture:
            raise RuntimeError("qualification worker fixture changed")
        if source_identity(o._REPO_ROOT) != source:
            raise RuntimeError("qualification source changed during execution")
        _write(capture.task, "qualification-vm.json", _encode(observations))
        capture.close(results, source)
    passed = bool(results) and len(results) == (1 if args.collect_release_nodes else len(selection)) and all(item["passed"] for item in results)
    print(json.dumps(dict(qualification_vm=namespace["runner_id"], passed=passed, results=results), sort_keys=True), flush=True)
    return passed


# One explicit qualification operation: no command, path, PID or signal is accepted
# from the guest request. The operator process never opens an administrator channel.
_ADMIN_HELPER = "tests/system/network/floating_ip_worker_fault.py"


def _admin_module(o):
    return runpy.run_path(str(o._REPO_ROOT / _ADMIN_HELPER))


def _prepare_admin_stall(o, namespace, guest_task, source, candidate, before, evidence):
    ticket = dict(schema_version=1, nonce=uuid.uuid4().hex, hostname=namespace["runner"],
                  boot_id=namespace["expected_vm"]["boot_id"], operator=namespace["operator"],
                  source_sha256=source["payload"][_ADMIN_HELPER], candidate_sha256=candidate["sha256"])
    _admin_module(o)["_admin_validate"](ticket)
    script = "import os,sys;fd=os.open(sys.argv[1],os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600);os.write(fd,sys.argv[2].encode());os.fsync(fd);os.close(fd)"
    _command(o, namespace, script, guest_task + "/worker-stall-admin.json", json.dumps(ticket,sort_keys=True))
    descriptor = dict(namespace=namespace, guest_task=guest_task, source=source, candidate=candidate,
                      ticket=ticket, before=before)
    _write(evidence, "admin-worker-stall.json", _encode(descriptor))
    return descriptor


def _admin_guest_read(o, descriptor, name):
    assert name in {"worker-stall-request.json", "worker-stall-continue.json", "worker-stall-admin.json"}
    script = r"""
import json,os,stat,sys
try:fd=os.open(sys.argv[1],os.O_RDONLY|os.O_NOFOLLOW)
except FileNotFoundError:
 print('ADMIN_STALL_PENDING');raise SystemExit(0)
with os.fdopen(fd,'rb') as source:
 row=os.fstat(source.fileno())
 if row.st_nlink==2:
  print('ADMIN_STALL_PENDING');raise SystemExit(0)
 assert stat.S_ISREG(row.st_mode) and row.st_uid==os.getuid() and row.st_nlink==1
 assert stat.S_IMODE(row.st_mode)==0o600 and row.st_size<=16384
 raw=source.read(16385);assert len(raw)==row.st_size
 print(raw.decode(),end='')
"""
    command=shlex.join(["/usr/bin/python3","-I","-S","-B","-c",script,descriptor["guest_task"]+"/"+name])
    result=_exec(o,descriptor["namespace"],command,timeout=10,check=False)
    if result.returncode==0 and result.stdout.strip()=="ADMIN_STALL_PENDING":return None
    if result.returncode!=0:raise RuntimeError("administrator stall mailbox observation unavailable: "+result.stderr)
    raw=result.stdout.encode()
    if len(raw)>16384:raise RuntimeError("administrator stall receipt exceeds bound")
    return o._host_reboot_json(raw)


def _read_admin_ticket(o, descriptor):
    if _admin_guest_read(o, descriptor, "worker-stall-admin.json") != descriptor["ticket"]:
        raise RuntimeError("administrator stall ticket changed")
    return descriptor


def _require_admin_request(o, descriptor, request):
    _admin_module(o)["_admin_validate"](request, request=True)
    if {key:value for key,value in request.items() if key != "vm_id"} != descriptor["ticket"]:
        raise RuntimeError("administrator stall request rebound")
    return request


def _admin_guest_write(o, descriptor, name, value):
    assert name in {"worker-stall-admitted.json", "worker-stall-continued.json"}
    # Load the pinned helper only as the ordinary operator, never as root.
    script = "import runpy,sys,json;from pathlib import Path;m=runpy.run_path(sys.argv[1]);m['_admin_write'](Path(sys.argv[2]),json.loads(sys.argv[3]))"
    _command(o, descriptor["namespace"], script, descriptor["guest_task"] + "/source/" + _ADMIN_HELPER,
             descriptor["guest_task"] + "/" + name, json.dumps(value,sort_keys=True))


def _run_admin_stall_case(o, descriptor, evidence, execute):
    stop = threading.Event(); errors = []
    def announce():
        while not stop.is_set():
            try:
                request = _admin_guest_read(o, descriptor, "worker-stall-request.json")
                if request is not None:
                    _require_admin_request(o, descriptor, request)
                    _write(evidence, "admin-worker-request.json", _encode(request))
                    print("ADMIN WORKER STALL REQUEST " + json.dumps(request,sort_keys=True),flush=True)
                    return
            except Exception as error:
                errors.append(error);return
            stop.wait(0.5)
    observer = threading.Thread(target=announce, name="worker-stall-request", daemon=True)
    observer.start()
    try:
        result = execute()
    finally:
        stop.set();observer.join(timeout=45)
    if observer.is_alive() or errors:
        raise RuntimeError("administrator stall request observation unavailable")
    return _join_admin_stall(o, descriptor, evidence)


def _root_stall_payload(raw, request):
    digest = hashlib.sha256(raw).hexdigest()
    return ("import base64,hashlib,json;raw=base64.b64decode(" + repr(base64.b64encode(raw).decode()) + ");"
            + "assert hashlib.sha256(raw).hexdigest()==" + repr(digest) + ";"
            + "module={'__name__':'qualification_admin_observer'};exec(compile(raw,'<pinned-qa-worker-observer>','exec'),module);"
            + "module['_administrator_root_observer'](json.loads(" + repr(json.dumps(request,sort_keys=True)) + ")," + repr(digest) + ","
            + repr(request['candidate_sha256']) + ")")


def _join_admin_stall(o, descriptor, evidence):
    request = _require_admin_request(o, descriptor, json.loads(_read(evidence / "admin-worker-request.json",16384)))
    proof = json.loads(_read(evidence / "admin-worker-observer.json",MAX_OUTPUT_BYTES))
    expected = {"request", "admission", "returncode", "output", "stderr", "payload_sha256", "error"}
    raw = (o._REPO_ROOT / _ADMIN_HELPER).read_bytes()
    if (set(proof) != expected or proof["request"] != request or type(proof["returncode"]) is not int
            or proof["returncode"] != 0 or proof["output"].strip() != "CONTINUED_EXACT_ROOT_WORKER"
            or proof["error"] is not None
            or proof["payload_sha256"] != hashlib.sha256(_root_stall_payload(raw,request).encode()).hexdigest()):
        raise RuntimeError("administrator stall lacks independent exact root admission/thaw proof")
    _admin_module(o)["_admin_admission"](request, proof["admission"])
    return proof


def _drive_admin_observer(o, descriptor, request, command, evidence, *, popen=subprocess.Popen):
    """Only the separately invoked administrator entry calls this function."""
    if _admin_guest_read(o,descriptor,"worker-stall-continue.json") is not None:
        raise RuntimeError("administrator stall request already cancelled; no root observer launched")
    child = popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    admission = None; output = errors = ""; failure = None
    try:
        if not select.select([child.stdout],[],[],40)[0]:raise RuntimeError("root observer admission unavailable")
        line = child.stdout.readline(16385)
        admission = json.loads(line)
        _admin_module(o)["_admin_admission"](request,admission)
        _admin_guest_write(o,descriptor,"worker-stall-admitted.json",dict(request=request,admission=admission))
        deadline = time.monotonic()+90
        while time.monotonic()<deadline:
            continuation = _admin_guest_read(o,descriptor,"worker-stall-continue.json")
            if continuation is not None:
                if continuation != request:raise RuntimeError("administrator CONT request rebound")
                break
            time.sleep(.1)
        else:raise RuntimeError("administrator CONT request unavailable")
    except BaseException as error:
        failure = str(error)
    finally:
        # Always attempt CONT through this exact retained SSH stdin. Never signal
        # a guessed PID, kill the observer, or use a different root transport.
        try:
            output,errors = child.communicate('CONT\n',timeout=110)
        except BaseException as error:
            failure = failure or str(error)
        raw = (o._REPO_ROOT / _ADMIN_HELPER).read_bytes()
        proof = dict(request=request,admission=admission,returncode=child.returncode,output=output,stderr=errors,
                     payload_sha256=hashlib.sha256(_root_stall_payload(raw,request).encode()).hexdigest(),error=failure)
        _write(evidence,"admin-worker-observer.json",_encode(proof),MAX_OUTPUT_BYTES)
        if child.returncode is not None:
            _admin_guest_write(o,descriptor,"worker-stall-continued.json",dict(request=request,
                               returncode=child.returncode,output=output))
    if failure or child.returncode != 0:raise RuntimeError("administrator worker observer failed: " + str(failure or errors))
    _join_admin_stall(o,descriptor,evidence)
    return proof



def _observe_admin_stall_target(o, descriptor, request):
    namespace = descriptor["namespace"]
    if _outer(o,namespace) != descriptor["before"]["outer"]:
        raise RuntimeError("administrator outer VM process changed")
    if o._host_reboot_controller() != descriptor["before"]["outer_controller"]:
        raise RuntimeError("administrator outer controller changed")
    script = r"""
import hashlib,json,os,pathlib,stat,subprocess,sys,socket
expected=json.loads(sys.argv[1]);request=json.loads(sys.argv[2]);user=expected['namespace']['operator']
assert os.getuid()==os.geteuid()==user['uid'] and os.getgid()==user['gid']
assert socket.gethostname()==request['hostname']
assert pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()==request['boot_id']
assert hashlib.sha256(pathlib.Path('/usr/local/bin/mvm').read_bytes()).hexdigest()==request['candidate_sha256']
paths=dict(home=user['home'],task_parent=user['home']+'/.cache',**expected['namespace']['paths'])
for key,name in paths.items():
 p=pathlib.Path(name);row=p.lstat();assert p.resolve()==p and stat.S_ISDIR(row.st_mode)
 assert [row.st_dev,row.st_ino,row.st_uid,row.st_gid,stat.S_IMODE(row.st_mode)]==expected['namespace_identity'][key]
raw=subprocess.check_output(['/usr/local/bin/mvm','vm','inspect',request['vm_id'],'--json'],text=True,timeout=30)
item=json.loads(raw);assert item['status']=='success' and item['error'] is None
vm=item['data']['vm'];assert vm['id']==request['vm_id'] and vm['status']=='running' and vm['pid']>0
"""
    probe = dict(namespace=namespace,namespace_identity=descriptor["before"]["guest"]["namespace_identity"])
    _command(o,namespace,script,json.dumps(probe),json.dumps(request))

def administrator_main(argv=None):
    parser = argparse.ArgumentParser(description="Explicit one-shot QA root-worker STOP/CONT administrator")
    parser.add_argument("operation",choices=["admin-worker-stall"])
    parser.add_argument("--evidence",type=Path,required=True)
    parser.add_argument("--vm",required=True)
    parser.add_argument("--key",type=Path,required=True)
    parser.add_argument("--known-hosts",type=Path,required=True)
    args = parser.parse_args(argv)
    # Load the unchanged orchestrator interfaces without executing its CLI.
    path = Path(__file__).with_name("run-system-tests.py")
    spec = importlib.util.spec_from_file_location("worker_stall_admin_orchestrator",path)
    o = importlib.util.module_from_spec(spec);sys.modules[spec.name]=o;spec.loader.exec_module(o)
    evidence = Path(str(args.evidence) + ".worker-stall-admin")
    descriptor = json.loads(_read(evidence / "admin-worker-stall.json",MAX_OUTPUT_BYTES))
    request = _require_admin_request(o,descriptor,json.loads(_read(evidence / "admin-worker-request.json",16384)))
    if request["vm_id"] != args.vm:raise RuntimeError("explicit administrator target differs from observed request")
    if source_identity(o._REPO_ROOT) != descriptor["source"]:raise RuntimeError("administrator source changed")
    _observe_admin_stall_target(o,descriptor,request)
    raw = (o._REPO_ROOT / _ADMIN_HELPER).read_bytes()
    if hashlib.sha256(raw).hexdigest() != request["source_sha256"]:raise RuntimeError("administrator observer source changed")
    namespace = descriptor["namespace"]
    expected = namespace["expected_vm"]["ssh_host_key"].split()
    entries = [line.split() for line in args.known_hosts.read_text().splitlines() if line.strip() and not line.startswith('#')]
    if not any(len(row)==3 and namespace["expected_vm"]["ipv4"] in row[0].split(',') and row[1:]==expected for row in entries):
        raise RuntimeError("administrator SSH host key differs from retained exact target")
    command = ["ssh","-F","/dev/null","-i",str(args.key),"-o","BatchMode=yes","-o","StrictHostKeyChecking=yes",
               "-o","UserKnownHostsFile="+str(args.known_hosts),"-o","ConnectTimeout=8",
               "root@"+namespace["expected_vm"]["ipv4"],
               shlex.join(["/usr/bin/python3","-I","-S","-B","-c",_root_stall_payload(raw,request)])]
    _write(evidence,"admin-worker-command.json",_encode(command),MAX_OUTPUT_BYTES)
    _drive_admin_observer(o,descriptor,request,command,evidence)
    return 0


if __name__ == "__main__":
    raise SystemExit(administrator_main())
