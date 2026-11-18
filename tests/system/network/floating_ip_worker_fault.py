"""Qualification-only exact root-worker stall; operator CLI remains unprivileged.

Source lock order: Jailer instance_authority.go lockOwnedInstanceMode takes the
index before VM. Worker request/monitor/reconnect/retirement take that VM before
network/firewall/worker locks. Stop every thread while BOTH locks are held, then
release them before ordinary CLI probes. VM-only stopping can strand the index.
"""

from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import pwd
import socket
import select
import signal
import stat
import subprocess
import sys
import time


def _fingerprint(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def _unique(pairs):
    item = {}
    for key, value in pairs:
        assert key not in item, "duplicate root authority field"
        item[key] = value
    return item


def _bind_records(worker, instance, owner, group, vm_id, boot):
    assert set(worker) == {"schema", "resource_owner_uid", "resource_owner_gid", "vm_id", "boot_id",
                           "launch_generation", "execution_uid", "generation", "revision", "phase", "process"}
    assert type(worker["schema"]) is int and worker["schema"] == 1
    for key in ("resource_owner_uid", "resource_owner_gid", "launch_generation", "execution_uid", "generation", "revision"):
        assert type(worker[key]) is int and 0 < worker[key] < 2**64
    assert worker["resource_owner_uid"] == owner > 0 and worker["resource_owner_gid"] == group > 0
    assert re.fullmatch(r"[0-9a-f]{32}", vm_id) and worker["vm_id"] == vm_id
    assert worker["phase"] == "ready" and worker["boot_id"] == boot
    assert type(worker["generation"]) is int and worker["revision"] >= worker["generation"] > 0
    assert set(instance) == {"schema_version", "owner_uid", "vm_id", "network_placement", "lifecycle",
                             "launch_generation", "cleanup_generation", "launch", "active"}
    for key in ("schema_version", "owner_uid", "launch_generation", "cleanup_generation"):
        assert type(instance[key]) is int and 0 <= instance[key] < 2**64
    assert instance["schema_version"] == 6 and instance["owner_uid"] == owner
    assert instance["vm_id"] == vm_id and instance["lifecycle"] == "active"
    assert instance["network_placement"] == "managed" and instance["active"] is not None
    assert instance["launch_generation"] == worker["launch_generation"] > instance["cleanup_generation"] >= 0
    assert instance["launch"]["host_boot_id"] == boot
    assert instance["launch"]["execution_identity"]["uid"] == worker["execution_uid"] not in (0, owner)
    process = worker["process"]
    assert set(process) == {"pid", "start_time_ticks", "expected_uid", "expected_gid", "executable",
                            "mount_device", "mount_inode", "network_device", "network_inode", "root_device", "root_inode"}
    for key in ("pid", "start_time_ticks", "expected_uid", "expected_gid"):
        assert type(process[key]) is int and 0 <= process[key] < 2**64
    assert process["pid"] > 0 and process["start_time_ticks"] > 0
    assert process["expected_uid"] == process["expected_gid"] == 0
    expected = process["executable"]
    assert set(expected) == {"Device", "Inode", "SizeBytes", "Mode", "UID", "GID", "Links", "SHA256"}
    for key in ("Device", "Inode", "SizeBytes", "Mode", "UID", "GID", "Links"):
        assert type(expected[key]) is int and 0 <= expected[key] < 2**64
    assert expected["Mode"] == stat.S_IFREG | 0o755 and expected["UID"] == expected["GID"] == 0
    assert expected["Links"] == 1 and 0 < expected["SizeBytes"] <= 64 * 1024 * 1024
    assert expected["Device"] > 0 and expected["Inode"] > 0
    assert re.fullmatch(r"[0-9a-f]{64}", expected["SHA256"])
    for key in ("mount_device", "mount_inode", "network_device", "network_inode", "root_device", "root_inode"):
        assert type(process[key]) is int and process[key] > 0
    return process


def _wait_lock(fd, deadline):
    while True:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return
        except BlockingIOError:
            assert time.monotonic() < deadline, "qualification lock acquisition timed out"
            time.sleep(0.025)


def _all_stopped(states):
    return bool(states) and all(state in ("T", "t") for state in states)


def _observe_process(process, pidfd):
    poller = select.poll(); poller.register(pidfd, select.POLLIN)
    assert poller.poll(0) == [], "root worker exited"
    descriptors = []
    def keep(fd):
        descriptors.append(fd); return fd
    def read_at(parent, name, maximum):
        fd = keep(os.open(name, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=parent))
        raw = os.read(fd, maximum + 1)
        assert len(raw) <= maximum
        return raw.decode('ascii')
    try:
        proc = keep(os.open(f"/proc/{process['pid']}", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW))
        fields = read_at(proc, 'stat', 16384).rpartition(") ")[2].split()
        assert int(fields[19]) == process["start_time_ticks"] and fields[0] not in ("Z", "X")
        lines = read_at(proc, 'status', 65536).splitlines()
        for prefix in ("Uid:", "Gid:"):
            assert [line.split()[1:] for line in lines if line.startswith(prefix)] == [["0"] * 4]
        # procfs root/exe/ns links are kernel observations; caller paths are never followed.
        for name, stem in (("ns/mnt", "mount"), ("ns/net", "network"), ("root", "root")):
            info = os.stat(name, dir_fd=proc)
            assert (info.st_dev, info.st_ino) == (process[stem + "_device"], process[stem + "_inode"])
        fd = keep(os.open('exe', os.O_RDONLY | os.O_CLOEXEC, dir_fd=proc))
        info = os.fstat(fd)
        actual = dict(Device=info.st_dev, Inode=info.st_ino, SizeBytes=info.st_size,
                      Mode=info.st_mode, UID=info.st_uid, GID=info.st_gid, Links=info.st_nlink)
        expected = process["executable"]
        assert all(expected[key] == value for key, value in actual.items())
        digest = hashlib.sha256(); count = 0
        while True:
            chunk = os.read(fd, 65536)
            if not chunk:break
            count += len(chunk); assert count <= 64 * 1024 * 1024
            digest.update(chunk)
        assert count == info.st_size and digest.hexdigest() == expected["SHA256"]
        assert _fingerprint(os.fstat(fd)) == _fingerprint(info), "worker executable changed during observation"
        tasks = keep(os.open('task', os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=proc))
        tids = sorted(os.listdir(tasks)); assert tids
        states = []
        for tid in tids:
            assert tid.isdecimal()
            thread = keep(os.open(tid, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=tasks))
            states.append(read_at(thread, 'stat', 16384).rpartition(") ")[2].split()[0])
        if tids != sorted(os.listdir(tasks)):
            states.append("R")  # A changing task set cannot prove whole-process stop.
        assert poller.poll(0) == []
        return states
    finally:
        for fd in reversed(descriptors):os.close(fd)


def _continue_admitted_worker(process, pidfd, *, observe=_observe_process, send=signal.pidfd_send_signal):
    """Always thaw the retained exact process; report all observation/signal failures afterward."""
    failures = []
    try:
        observe(process, pidfd)
    except BaseException as error:
        failures.append(error)
    # Initial admission already pinned this process. A fallible final observation
    # must never prevent its continuation or cause a numeric-PID fallback.
    try:
        send(pidfd, signal.SIGCONT)
    except BaseException as error:
        failures.append(error)
    else:
        try:
            deadline = time.monotonic() + 5
            while any(state in ('T', 't') for state in observe(process, pidfd)):
                assert time.monotonic() < deadline, 'root worker did not continue'
                time.sleep(0.025)
        except BaseException as error:
            failures.append(error)
    if failures:
        raise BaseExceptionGroup('root worker continuation proof or signal failed', failures)


def _root_observer(vm_id, owner, group):
    assert os.getuid() == os.geteuid() == 0
    assert int(os.environ["SUDO_UID"]) == owner > 0 and int(os.environ["SUDO_GID"]) == group > 0
    assert re.fullmatch(r"[0-9a-f]{32}", vm_id)
    descriptors, locks, lock_bindings = [], [], []
    pidfd = None; process = None; stopped = False
    def keep(fd):
        descriptors.append(fd); return fd
    def directory(parent, name, private=False):
        fd = keep(os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=parent))
        info = os.fstat(fd)
        assert info.st_uid == info.st_gid == 0 and not info.st_mode & 0o022
        if private:assert stat.S_IMODE(info.st_mode) == 0o700
        return fd
    def fixed_file(parent, name, maximum):
        fd = keep(os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, dir_fd=parent))
        info = os.fstat(fd)
        assert stat.S_ISREG(info.st_mode) and info.st_uid == info.st_gid == 0
        assert stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1 and info.st_size <= maximum
        raw = os.read(fd, maximum + 1); assert len(raw) == info.st_size <= maximum
        assert _fingerprint(os.fstat(fd)) == _fingerprint(info)
        return (info.st_dev, info.st_ino, raw), json.loads(raw, object_pairs_hook=_unique) if raw else None
    def boot():return Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    try:
        root = directory(None, '/')
        run = directory(root, 'run'); runtime = directory(run, 'mvmctl'); authority = directory(runtime, 'authority', True)
        operator = directory(authority, str(owner), True)
        deadline = time.monotonic() + 10
        for parent, name in ((authority, 'index.lock'), (operator, vm_id + '.lock')):
            fd = keep(os.open(name, os.O_RDWR | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent))
            info = os.fstat(fd)
            assert stat.S_ISREG(info.st_mode) and info.st_uid == info.st_gid == 0
            assert stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1 and info.st_size == 0
            _wait_lock(fd, deadline); locks.append(fd)
            lock_bindings.append((parent, name, fd, _fingerprint(info)))
        var = directory(root, 'var'); lib = directory(var, 'lib'); state = directory(lib, 'mvmctl')
        instances = directory(state, 'instances', True); instance_owner = directory(instances, str(owner), True)
        workers = directory(state, 'guest-control', True); worker_owner = directory(workers, str(owner), True)
        name = vm_id + '.json'; initial_boot = boot()
        instance_identity, instance = fixed_file(instance_owner, name, 16384)
        worker_identity, worker = fixed_file(worker_owner, name, 16384)
        process = _bind_records(worker, instance, owner, group, vm_id, initial_boot)
        pidfd = keep(os.pidfd_open(process['pid']))
        _observe_process(process, pidfd)
        assert fixed_file(instance_owner, name, 16384)[0] == instance_identity
        assert fixed_file(worker_owner, name, 16384)[0] == worker_identity and boot() == initial_boot
        for parent, leaf, fd, expected in lock_bindings:
            assert _fingerprint(os.fstat(fd)) == expected
            assert _fingerprint(os.stat(leaf, dir_fd=parent, follow_symlinks=False)) == expected
        signal.pidfd_send_signal(pidfd, signal.SIGSTOP); stopped = True
        deadline = time.monotonic() + 5
        while not _all_stopped(_observe_process(process, pidfd)):
            assert time.monotonic() < deadline, 'whole worker did not stop'
            time.sleep(0.025)
        # Both authority locks prevent every worker thread retaining downstream
        # locks before group-stop completes. Release them before any exec probe.
        for fd in reversed(locks):fcntl.flock(fd, fcntl.LOCK_UN)
        locks.clear()
        print(json.dumps({'vm_id': vm_id, 'owner_uid': owner, 'boot_id': initial_boot,
                          'launch_generation': worker['launch_generation'], 'pid': process['pid'],
                          'start_time_ticks': process['start_time_ticks'], 'all_threads_stopped': True}), flush=True)
        assert select.select([sys.stdin], [], [], 90)[0], 'root observer CONT deadline expired'
        assert sys.stdin.buffer.readline(16) == b'CONT\n', 'invalid fixed observer completion'
        assert boot() == initial_boot and fixed_file(worker_owner, name, 16384)[0] == worker_identity
        assert _all_stopped(_observe_process(process, pidfd)), "whole worker did not remain stalled"
    finally:
        try:
            if stopped:
                # The retained pidfd already binds the admitted process. Always
                # attempt continuation before reporting final proof failures.
                _continue_admitted_worker(process, pidfd)
                stopped = False
                print('CONTINUED_EXACT_ROOT_WORKER', flush=True)
        finally:
            for fd in reversed(locks):fcntl.flock(fd, fcntl.LOCK_UN)
            for fd in reversed(descriptors):os.close(fd)


_FIXTURE_EXECUTABLE = Path('/usr/local/libexec/mvmctl-qa-worker-stall')
_FIXTURE_MANIFEST = Path('/var/lib/mvmctl-qa-worker-observer/manifest.json')
_FIXTURE_PREFIX = b'#!/usr/bin/python3 -I\n'


def _fixture_read(path, mode):
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW
    parent = os.open('/', flags)
    try:
        for name in path.parts[1:-1]:
            child = os.open(name, flags, dir_fd=parent)
            os.close(parent)
            parent = child
            row = os.fstat(parent)
            assert row.st_uid == row.st_gid == 0 and not row.st_mode & 0o022
        fd = os.open(path.name, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        try:
            row = os.fstat(fd)
            assert stat.S_ISREG(row.st_mode) and row.st_uid == row.st_gid == 0 and row.st_nlink == 1
            assert stat.S_IMODE(row.st_mode) == mode and 0 < row.st_size <= 128 * 1024
            raw = os.read(fd, 128 * 1024 + 1)
            assert len(raw) == row.st_size and _fingerprint(os.fstat(fd)) == _fingerprint(row)
            assert _fingerprint(os.stat(path.name, dir_fd=parent, follow_symlinks=False)) == _fingerprint(row)
            return raw
        finally:
            os.close(fd)
    finally:
        os.close(parent)


def _validate_fixture_values(manifest, payload, source, owner, group, hostname, boot):
    assert set(manifest) == {'schema_version', 'hostname', 'boot_id', 'operator', 'source_sha256',
                             'payload_sha256', 'tool_sha256', 'enrollment_sha256', 'policy_sha256'}
    assert type(manifest['schema_version']) is int and manifest['schema_version'] == 1
    assert hostname == manifest['hostname'] == 'mvmctl-030-test' and manifest['boot_id'] == boot
    assert set(manifest['operator']) == {'user', 'uid', 'gid', 'home'}
    assert type(owner) is int and type(group) is int and owner > 0 and group > 0
    assert type(manifest['operator']['uid']) is int and type(manifest['operator']['gid']) is int
    assert (manifest['operator']['uid'], manifest['operator']['gid']) == (owner, group)
    for key in ('source_sha256', 'payload_sha256', 'tool_sha256', 'enrollment_sha256', 'policy_sha256'):
        assert re.fullmatch(r'[0-9a-f]{64}', manifest[key])
    assert payload == _FIXTURE_PREFIX + source
    assert hashlib.sha256(payload).hexdigest() == manifest['payload_sha256']
    assert hashlib.sha256(source).hexdigest() == manifest['source_sha256']
    return manifest


def validate_worker_fixture(owner=None, group=None):
    """Read-only ordinary-mode admission; no environment-selected executable path."""
    owner = os.getuid() if owner is None else owner
    group = os.getgid() if group is None else group
    raw = _fixture_read(_FIXTURE_MANIFEST, 0o644)
    manifest = json.loads(raw, object_pairs_hook=_unique)
    payload = _fixture_read(_FIXTURE_EXECUTABLE, 0o555)
    installed = Path(__file__).absolute() == _FIXTURE_EXECUTABLE
    source = payload[len(_FIXTURE_PREFIX):] if installed else Path(__file__).read_bytes()
    _validate_fixture_values(manifest, payload, source, owner, group, socket.gethostname(),
                             Path('/proc/sys/kernel/random/boot_id').read_text().strip())
    account = pwd.getpwuid(owner)
    assert (account.pw_name, account.pw_gid, account.pw_dir) == (
        manifest['operator']['user'], group, manifest['operator']['home'])
    return manifest


@contextmanager
def stalled_root_worker(runner, vm):
    assert os.getuid() > 0 and os.geteuid() == os.getuid()
    assert os.environ.get('MVM_TEST_QUALIFICATION_HOST') == os.environ.get('MVM_TEST_VM') == runner
    assert runner
    vm_id = vm['binding']['id']
    validate_worker_fixture()
    command = ['/usr/bin/sudo', '-n', str(_FIXTURE_EXECUTABLE), vm_id, str(os.getuid()), str(os.getgid())]
    child = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        assert select.select([child.stdout], [], [], 40)[0], 'root stall observer failed to admit'
        line = child.stdout.readline(16385)
        receipt = json.loads(line)
        assert receipt['vm_id'] == vm_id and receipt['owner_uid'] == os.getuid()
        assert receipt['all_threads_stopped'] is True
        yield receipt
    finally:
        output, errors = child.communicate('CONT\n', timeout=20)
        assert child.returncode == 0 and output.strip() == 'CONTINUED_EXACT_ROOT_WORKER', (output, errors)


if __name__ == '__main__':
    assert len(sys.argv) == 4
    assert Path(__file__).absolute() == _FIXTURE_EXECUTABLE
    assert re.fullmatch(r'[0-9a-f]{32}', sys.argv[1])
    owner, group = int(sys.argv[2]), int(sys.argv[3])
    assert sys.argv[2:] == [str(owner), str(group)], 'canonical numeric principal arguments required'
    validate_worker_fixture(owner, group)
    _root_observer(sys.argv[1], owner, group)
