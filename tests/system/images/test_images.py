"""Image CLI contracts against the installed candidate inside a disposable runner."""

from __future__ import annotations

import hashlib
import json
import os
import re
import select
import shlex
import shutil
import signal
import subprocess
import uuid
from contextlib import contextmanager
from pathlib import Path

import pytest

from tests.system.conftest import _guest_run, _json_success_data, _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_image]


_VM_IMPORT_DEATH_OBSERVER = r"""
import hashlib
import json
import os
import re
import select
import signal
import stat
import sys
import time

vm_id, owner, source_pid, source_ticks, cache, import_name, fault_mode = sys.argv[1:]
owner, source_pid, source_ticks = int(owner), int(source_pid), int(source_ticks)
assert fault_mode in ('death', 'sigint-survivor')
assert os.geteuid() == 0 and int(os.environ['SUDO_UID']) == owner > 0
assert re.fullmatch(r'[0-9a-f]{32}', vm_id)
assert re.fullmatch(r'sys-image-[0-9a-f]{32}', import_name)
fds, receiver, receiver_pidfd, image_id, exports, copies = [], None, None, None, None, None
exports_parent = None
receiver_owned = False

def keep(fd):
    fds.append(fd)
    return fd

def forget(fd):
    os.close(fd)
    fds.remove(fd)

def identity(info):
    return info.st_dev, info.st_ino

def stable(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)

def unique(pairs):
    value = {}
    for key, item in pairs:
        assert key not in value, 'duplicate authority field'
        value[key] = item
    return value

def read(parent, name, maximum):
    fd = os.open(name, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
    try:
        before = os.fstat(fd)
        value = bytearray()
        while len(value) <= maximum:
            part = os.read(fd, min(65536, maximum + 1 - len(value)))
            if not part:
                break
            value.extend(part)
        after = os.fstat(fd)
        assert len(value) <= maximum and stable(before) == stable(after), 'unstable or oversized observation'
        return before, bytes(value)
    finally:
        os.close(fd)

def directory(parent, name):
    fd = keep(os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=parent))
    info = os.fstat(fd)
    assert info.st_uid == info.st_gid == 0 and not info.st_mode & 0o022, (name, info)
    return fd

def authority(parent, name, maximum=16384, mode=0o600):
    info, raw = read(parent, name, maximum)
    assert info.st_mode == stat.S_IFREG | mode and info.st_uid == info.st_gid == 0
    assert info.st_nlink == 1 and 0 < info.st_size <= maximum
    value = json.loads(raw, object_pairs_hook=unique)
    assert json.dumps(value, separators=(',', ':'), ensure_ascii=False).encode() == raw
    return info, raw, value

def proc(pid):
    return os.open('/proc/' + str(pid), os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW)

def process(fd):
    _, raw = read(fd, 'stat', 16384)
    fields = raw.decode().rpartition(') ')[2].split()
    return {'state': fields[0], 'parent': int(fields[1]), 'group': int(fields[2]), 'ticks': int(fields[19])}

def credentials(fd):
    _, raw = read(fd, 'status', 65536)
    values = dict(line.split(':', 1) for line in raw.decode().splitlines() if ':' in line)
    return [int(x) for x in values['Uid'].split()], [int(x) for x in values['Gid'].split()]

def exited(pidfd, timeout=0):
    poller = select.poll()
    poller.register(pidfd, select.POLLIN)
    events = poller.poll(timeout)
    assert not events or not events[0][1] & (select.POLLERR | select.POLLNVAL), events
    return bool(events)

def digest_executable(parent):
    # /proc/<admitted-pid>/exe is an intentional process-identity read, not a selector.
    fd = os.open('exe', os.O_RDONLY | os.O_CLOEXEC, dir_fd=parent)
    try:
        info = os.fstat(fd)
        assert stat.S_ISREG(info.st_mode) and info.st_uid == info.st_gid == 0
        assert stat.S_IMODE(info.st_mode) == 0o755 and info.st_nlink == 1
        assert 0 < info.st_size <= 64 * 1024 * 1024
        with os.fdopen(os.dup(fd), 'rb') as value:
            return identity(info), hashlib.file_digest(value, 'sha256').hexdigest()
    finally:
        os.close(fd)

def source_unchanged():
    assert not exited(source_pidfd)
    current = process(source_proc)
    assert current['ticks'] == source_ticks and current['state'] not in ('Z', 'X')
    assert credentials(source_proc) == ([execution_uid] * 4, [execution_gid] * 4)
    info, raw, _ = authority(instances, vm_id + '.json')
    assert identity(info) == identity(source_record_info) and raw == source_record_raw
    assert identity(os.stat('root', dir_fd=source_proc)) == jail_identity
    assert identity(os.stat('ns/mnt', dir_fd=source_proc)) == mount_identity
    _, cgroup = read(source_proc, 'cgroup', 16384)
    assert cgroup.decode() == '0::/mvmctl/' + vm_id + '\n'
    _, boot = read(None, '/proc/sys/kernel/random/boot_id', 128)
    assert boot.decode().strip() == boot_id
    _, _, current_device = authority(jail, '.mvm-device-authority.json', 262144, 0o400)
    assert current_device['rootfs'] == rootfs and current_device['launch_generation'] == generation
    executable = os.open('exe', os.O_RDONLY | os.O_CLOEXEC, dir_fd=source_proc)
    try:
        info = os.fstat(executable)
        observed = {'device': info.st_dev, 'inode': info.st_ino, 'size_bytes': info.st_size,
                    'mode': info.st_mode, 'uid': info.st_uid, 'gid': info.st_gid, 'links': info.st_nlink}
        assert {key: active['executable'][key] for key in observed} == observed
        assert 0 < info.st_size <= 64 * 1024 * 1024
        with os.fdopen(os.dup(executable), 'rb') as value:
            assert hashlib.file_digest(value, 'sha256').hexdigest() == launch['release']['firecracker_sha256']
    finally:
        os.close(executable)

def receiver_unchanged():
    assert receiver_pidfd is not None and not exited(receiver_pidfd)
    current = process(receiver)
    assert current['ticks'] == receiver_identity['ticks'] and current['state'] not in ('Z', 'X')
    assert current['group'] == receiver_identity['group']
    assert credentials(receiver) == ([0] * 4, [0] * 4)
    assert digest_executable(receiver) == candidate
    assert read(receiver, 'cmdline', 4096)[1].split(b'\0')[:-1] == receiver_argv
    assert read(receiver, 'cgroup', 16384)[1] == cli_cgroup
    assert identity(os.stat('ns/mnt', dir_fd=receiver)) == cli_mount

def children(pid):
    # Go may fork from any thread: enumerate only this admitted ancestry subtree.
    task = os.open('/proc/' + str(pid) + '/task', os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW)
    try:
        names = os.listdir(task)
        assert len(names) <= 256
        values = set()
        for name in names:
            try:
                _, raw = read(task, name + '/children', 4096)
                values.update(int(x) for x in raw.split())
            except FileNotFoundError:
                pass
        return values
    finally:
        os.close(task)

def find_receiver(cli_pid):
    pending, seen, found = [(cli_pid, cli_ticks)], set(), []
    while pending:
        pid, ticks = pending.pop()
        assert pid not in seen and len(seen) < 32, 'ambiguous or oversized CLI ancestry'
        seen.add(pid)
        try:
            parent = proc(pid)
        except FileNotFoundError:
            continue
        try:
            try:
                assert process(parent)['ticks'] == ticks
            except (FileNotFoundError, ProcessLookupError):
                continue
            for child_pid in children(pid):
                try:
                    child = proc(child_pid)
                except FileNotFoundError:
                    continue
                try:
                    try:
                        observed = process(child)
                        argv = read(child, 'cmdline', 4096)[1].split(b'\0')[:-1]
                    except (FileNotFoundError, ProcessLookupError):
                        continue
                    assert observed['parent'] == pid
                    if argv == receiver_argv:
                        assert credentials(child) == ([0] * 4, [0] * 4)
                        assert digest_executable(child) == candidate
                        found.append((child_pid, observed))
                    pending.append((child_pid, observed['ticks']))
                finally:
                    os.close(child)
        finally:
            os.close(parent)
    assert len(found) <= 1, 'multiple export receivers'
    return found

def copy_phase():
    _, _, progress = authority(exports, vm_id + '.json')
    assert progress['owner_uid'] == owner and progress['vm_id'] == vm_id
    assert progress['generation'] == generation and progress['boot_id'] == boot_id
    assert progress['rootfs'] == rootfs and progress['cache_root'] == cache
    assert progress['original_state'] == 'Running'
    identifier = progress['image_id']
    assert re.fullmatch(r'[0-9a-f]{64}', identifier)
    entry = directory(copies, identifier)
    try:
        _, _, record = authority(entry, 'copy.json')
    finally:
        forget(entry)
    bound = record['source']
    for key in ('owner_uid', 'vm_id', 'generation', 'boot_id', 'rootfs', 'cache_root', 'image_id'):
        assert bound[key] == progress[key]
    assert record['file'] == {'device': progress['output_device'], 'inode': progress['output_inode']}
    assert record['directory'] == {'device': progress['parent_device'], 'inode': progress['parent_inode']}
    return progress, record

def freeze_copy():
    global image_id, data_uid, copy_record, progress_before, partial_size
    progress, record = copy_phase()
    if progress['stage'] != 'copying' or record['phase'] != 'allocated':
        assert progress['stage'] in ('prepared', 'pause_inflight', 'paused'), 'missed mid-copy fault window'
        return False
    source_unchanged()
    receiver_unchanged()
    signal.pidfd_send_signal(receiver_pidfd, signal.SIGSTOP)
    deadline = time.monotonic() + 5
    while process(receiver)['state'] != 'T' and time.monotonic() < deadline:
        assert not exited(receiver_pidfd)
        time.sleep(0.001)
    assert process(receiver)['state'] == 'T', 'receiver did not stop'
    receiver_unchanged()
    progress, record = copy_phase()
    # Retain admitted request ownership even when the fault phase raced past us.
    image_id = progress['image_id']
    assert progress['stage'] == 'copying' and record['phase'] == 'allocated', 'missed mid-copy fault window'
    data_root = directory(state, 'vm-rootfs-export-data')
    assert identity(os.fstat(data_root)) == (record['data_root']['device'], record['data_root']['inode'])
    data_uid = directory(data_root, str(owner))
    assert identity(os.fstat(data_uid)) == (record['uid_directory']['device'], record['uid_directory']['inode'])
    data = directory(data_uid, image_id)
    assert identity(os.fstat(data)) == (progress['parent_device'], progress['parent_inode'])
    assert os.listdir(data) == [], 'partial output must remain anonymous'
    marker = os.stat(image_id + '.copy', dir_fd=data_uid, follow_symlinks=False)
    assert identity(marker) == (record['marker']['device'], record['marker']['inode'])
    assert stat.S_ISREG(marker.st_mode) and marker.st_uid == marker.st_gid == 0 and marker.st_nlink == 1
    descriptor_dir = os.open('fd', os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=receiver)
    try:
        names = os.listdir(descriptor_dir)
        assert len(names) <= 256
        matches = [os.stat(name, dir_fd=descriptor_dir) for name in names]
        matches = [info for info in matches if identity(info) == (progress['output_device'], progress['output_inode'])]
        assert len(matches) == 1
        info = matches[0]
        assert info.st_mode == stat.S_IFREG | 0o600 and info.st_uid == info.st_gid == 0 and info.st_nlink == 0
        assert 0 < info.st_size < rootfs['bytes'], 'copy was not interrupted mid-write'
    finally:
        os.close(descriptor_dir)
    copy_record, progress_before, partial_size = record, progress, info.st_size
    print(json.dumps({'phase': 'copying', 'image_id': image_id, 'receiver_pid': receiver_pid,
                      'receiver_start_ticks': receiver_identity['ticks'], 'partial_bytes': info.st_size,
                      'source_generation': generation, 'source_rootfs': rootfs}), flush=True)
    return True

def suspended_copy_unchanged():
    source_unchanged()
    receiver_unchanged()
    assert process(receiver)['state'] == 'T', 'SIGINT receiver did not survive suspended'
    progress, record = copy_phase()
    assert progress == progress_before and record == copy_record
    data = directory(data_uid, image_id)
    try:
        assert identity(os.fstat(data)) == (progress['parent_device'], progress['parent_inode'])
        assert os.listdir(data) == [], 'suspended copy became public'
    finally:
        forget(data)
    descriptor_dir = os.open('fd', os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=receiver)
    try:
        names = os.listdir(descriptor_dir)
        assert len(names) <= 256
        matches = [os.stat(name, dir_fd=descriptor_dir) for name in names]
        matches = [info for info in matches if identity(info) == (progress['output_device'], progress['output_inode'])]
        assert len(matches) == 1
        info = matches[0]
        assert info.st_mode == stat.S_IFREG | 0o600 and info.st_uid == info.st_gid == 0
        assert info.st_nlink == 0 and info.st_size == partial_size
    finally:
        os.close(descriptor_dir)

def observe_sigint_survivor():
    assert exited(cli_pidfd, 5000), 'SIGINT caller did not exit'
    suspended_copy_unchanged()
    print(json.dumps({'phase': 'interrupted', 'image_id': image_id,
                      'receiver_exited': False, 'receiver_stopped': True}), flush=True)
    while select.select([sys.stdin], [], [], 45)[0]:
        command = sys.stdin.readline(256)
        assert command in ('busy\n', 'continue\n'), 'missing survivor command'
        suspended_copy_unchanged()
        if command == 'busy\n':
            print(json.dumps({'phase': 'busy', 'image_id': image_id,
                              'copy_authority_unchanged': True}), flush=True)
            continue
        signal.pidfd_send_signal(receiver_pidfd, signal.SIGCONT)
        assert exited(receiver_pidfd, 90000), 'surviving receiver did not settle'
        source_unchanged()
        progress, record = copy_phase()
        assert progress['stage'] == 'complete' and progress['original_state'] == 'Running'
        for key in ('owner_uid', 'vm_id', 'generation', 'boot_id', 'rootfs', 'cache_root',
                    'image_id', 'output_device', 'output_inode', 'parent_device', 'parent_inode'):
            assert progress[key] == progress_before[key]
        assert record['phase'] == 'released' and record['file'] == copy_record['file']
        assert image_id not in os.listdir(data_uid), 'failed-response copy was not discarded'
        print(json.dumps({'phase': 'settled', 'image_id': image_id, 'receiver_exited': True,
                          'source_generation': generation, 'source_recovery_pending': False}), flush=True)
        return
    raise AssertionError('SIGINT survivor observation timeout')

try:
    root = directory(None, '/')
    var = directory(root, 'var')
    lib = directory(var, 'lib')
    state = directory(lib, 'mvmctl')
    instances = directory(directory(state, 'instances'), str(owner))
    source_record_info, source_record_raw, source_record = authority(instances, vm_id + '.json')
    assert source_record['owner_uid'] == owner and source_record['vm_id'] == vm_id
    assert source_record['lifecycle'] == 'active' and source_record['network_placement'] == 'networkless'
    launch, active = source_record['launch'], source_record['active']
    generation = source_record['launch_generation']
    assert launch['pid'] == source_pid and launch['start_time_ticks'] == source_ticks
    execution_uid, execution_gid = launch['execution_identity']['uid'], launch['execution_identity']['gid']
    assert execution_uid not in (0, owner) and generation > 0
    source_pidfd = keep(os.pidfd_open(source_pid))
    source_proc = keep(proc(source_pid))
    jail = keep(os.open('root', os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC, dir_fd=source_proc))
    jail_identity = active['jail_root']['device'], active['jail_root']['inode']
    mount_identity = active['mount_namespace']['device'], active['mount_namespace']['inode']
    _, _, device = authority(jail, '.mvm-device-authority.json', 262144, 0o400)
    rootfs, boot_id = device['rootfs'], device['host_boot_id']
    assert device['vm_id'] == vm_id and device['owner_uid'] == owner and device['launch_generation'] == generation
    assert rootfs['kind'] == 'rootfs' and rootfs['logical_id'] == vm_id and rootfs['owner_uid'] == owner
    assert rootfs['bytes'] == 512 * 1024 * 1024 and rootfs['identity']['size'] == rootfs['bytes']
    source_unchanged()
    print(json.dumps({'phase': 'ready', 'source_generation': generation}), flush=True)
    assert select.select([sys.stdin], [], [], 30)[0], 'CLI arm timeout'
    command = sys.stdin.readline(256).split()
    assert len(command) == 3 and command[0] == 'arm'
    cli_pid, cli_ticks = int(command[1]), int(command[2])
    cli_pidfd, cli_proc = keep(os.pidfd_open(cli_pid)), keep(proc(cli_pid))
    assert process(cli_proc)['ticks'] == cli_ticks and not exited(cli_pidfd)
    assert credentials(cli_proc)[0] == [owner] * 4
    candidate = digest_executable(cli_proc)
    assert candidate[0] == identity(os.stat('/usr/local/bin/mvm', follow_symlinks=False))
    assert read(cli_proc, 'cmdline', 4096)[1].split(b'\0')[:-1] == [
        b'/usr/local/bin/mvm', b'image', b'import', import_name.encode(), vm_id.encode(),
        b'--skip-optimization', b'--json',
    ]
    cli_cgroup = read(cli_proc, 'cgroup', 16384)[1]
    cli_mount = identity(os.stat('ns/mnt', dir_fd=cli_proc))
    receiver_argv = [b'/usr/local/bin/mvm', b'__mvm_privileged_v1', b'jailer-vm-rootfs-export']
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        assert not exited(cli_pidfd), 'CLI exited before the copy fault'
        if receiver is None:
            found = find_receiver(cli_pid)
            if found:
                receiver_pid, receiver_identity = found[0]
                receiver_pidfd, receiver = keep(os.pidfd_open(receiver_pid)), keep(proc(receiver_pid))
                receiver_unchanged()
                receiver_owned = True
        if receiver is not None:
            try:
                if exports is None:
                    if exports_parent is None:
                        exports_parent = directory(state, 'vm-rootfs-exports')
                    exports = directory(exports_parent, str(owner))
                if copies is None:
                    copies = directory(exports, 'copies')
                if freeze_copy():
                    break
            except FileNotFoundError:
                pass
        time.sleep(0.002)
    else:
        raise AssertionError('no authentic partial-copy fault phase')
    caller_timeout = 60 if fault_mode == 'sigint-survivor' else 30
    assert select.select([sys.stdin], [], [], caller_timeout)[0], 'receiver fault command timeout'
    command = sys.stdin.readline(256)
    if fault_mode == 'sigint-survivor':
        assert command == 'interrupted\n'
        observe_sigint_survivor()
    else:
        assert command == 'kill\n'
        assert exited(cli_pidfd, 5000), 'client must be dead before killing the receiver'
        receiver_unchanged()
        assert process(receiver)['state'] == 'T'
        signal.pidfd_send_signal(receiver_pidfd, signal.SIGKILL)
        assert exited(receiver_pidfd, 10000), 'retained receiver did not exit'
        source_unchanged()
        progress, record = copy_phase()
        assert progress == progress_before and record == copy_record
        assert os.listdir(directory(data_uid, image_id)) == []
        print(json.dumps({'phase': 'dead', 'image_id': image_id, 'receiver_exited': True,
                          'source_recovery_pending': True}), flush=True)
        while select.select([sys.stdin], [], [], 120)[0]:
            command = sys.stdin.readline(256)
            if not command:
                break
            parts = command.split()
            assert parts == ['released'] or (
                len(parts) == 2 and parts[0] == 'recovered' and re.fullmatch(r'[0-9a-f]{64}', parts[1])
            )
            source_unchanged()
            entry = directory(copies, image_id)
            try:
                _, _, record = authority(entry, 'copy.json')
            finally:
                forget(entry)
            assert record['phase'] == 'released' and record['file'] == copy_record['file']
            assert image_id not in os.listdir(data_uid)
            if command == 'released\n':
                assert copy_phase()[0] == progress_before, 'copy release falsely cleared pending source'
            else:
                _, _, recovered = authority(exports, vm_id + '.json')
                assert recovered['image_id'] == parts[1] != image_id and recovered['stage'] == 'complete'
                for key in ('owner_uid', 'vm_id', 'generation', 'boot_id', 'rootfs', 'cache_root', 'original_state'):
                    assert recovered[key] == progress_before[key]
            print(json.dumps({'phase': parts[0], 'image_id': image_id}), flush=True)
        else:
            raise AssertionError('orphan recovery observation timeout')
except BaseException as error:
    print(json.dumps({'error': str(error), 'image_id': image_id}), flush=True)
    # Let the unprivileged owner kill its CLI first, preventing an API cleanup race.
    if select.select([sys.stdin], [], [], 30)[0]:
        sys.stdin.readline(256)
    raise
finally:
    if receiver_owned and not exited(receiver_pidfd):
        signal.pidfd_send_signal(receiver_pidfd, signal.SIGKILL)
        assert exited(receiver_pidfd, 10000), 'owned receiver cleanup did not finish'
    for fd in reversed(fds):
        os.close(fd)
"""


class _VMImportReceiverDeath:
    """Own only one ordinary importing CLI and its authenticated export receiver."""

    def __init__(self, runner_vm, source, *, mode="death"):
        assert os.environ.get("MVM_TEST_VM") == "mvmctl-030-test"
        assert "PYTEST_XDIST_WORKER" not in os.environ
        assert mode in ("death", "sigint-survivor")
        self.mode = mode
        self.runner_vm = runner_vm
        self.name = f"sys-image-{uuid.uuid4().hex}"
        self.image_id = None
        self.cli = None
        self.cli_pidfd = None
        self.baseline_ids = {image["id"] for image in _images(runner_vm)}
        cache = Path(source["rootfs_path"]).parents[2]
        assert cache.is_absolute() and cache.resolve() == cache
        self.observer = subprocess.Popen(
            [
                "/usr/bin/sudo",
                "-n",
                "/usr/bin/python3",
                "-I",
                "-u",
                "-c",
                _VM_IMPORT_DEATH_OBSERVER,
                source["id"],
                str(os.getuid()),
                str(source["pid"]),
                str(source["process_start_time"]),
                str(cache),
                self.name,
                mode,
            ],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )

    def read(self):
        timeout = 95 if self.mode == "sigint-survivor" else 45
        assert select.select([self.observer.stdout], [], [], timeout)[0], (
            "import observer timed out"
        )
        line = self.observer.stdout.readline(16385)
        assert line and len(line) <= 16384, "missing or oversized import fault receipt"
        receipt = json.loads(line)
        if receipt.get("image_id") is not None:
            identifier = receipt["image_id"]
            assert re.fullmatch(r"[0-9a-f]{64}", identifier)
            assert identifier not in self.baseline_ids
            assert self.image_id is None or self.image_id == identifier
            self.image_id = identifier
        assert "error" not in receipt, receipt
        print(f"VM import fault receipt={receipt}", flush=True)
        return receipt

    def require(self, command):
        self.observer.stdin.write(command + "\n")
        self.observer.stdin.flush()
        return self.read()

    def arm(self, source):
        ready = self.read()
        assert ready["phase"] == "ready"
        self.source_generation = ready["source_generation"]
        assert all(image["type"] != self.name for image in _images(self.runner_vm))
        self.cli = subprocess.Popen(
            [
                "/usr/local/bin/mvm",
                "image",
                "import",
                self.name,
                source["id"],
                "--skip-optimization",
                "--json",
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env={**os.environ, "NO_COLOR": "1"},
            text=True,
        )
        self.cli_pidfd = os.pidfd_open(self.cli.pid)
        fields = (
            Path(f"/proc/{self.cli.pid}/stat").read_text().rpartition(") ")[2].split()
        )
        assert fields[0] not in ("Z", "X") and self.cli.poll() is None
        receipt = self.require(f"arm {self.cli.pid} {fields[19]}")
        assert (
            receipt["phase"] == "copying"
            and 0 < receipt["partial_bytes"] < 512 * 1024 * 1024
        )

    def kill(self):
        assert self.cli.poll() is None and self.image_id is not None
        signal.pidfd_send_signal(self.cli_pidfd, signal.SIGKILL)
        stdout, stderr = self.cli.communicate(timeout=10)
        assert self.cli.returncode == -signal.SIGKILL, (stdout, stderr)
        assert not stdout, "interrupted import published a success response"
        print(f"interrupted import stderr={stderr}", flush=True)
        receipt = self.require("kill")
        assert receipt == {
            "phase": "dead",
            "image_id": self.image_id,
            "receiver_exited": True,
            "source_recovery_pending": True,
        }

    def interrupt(self):
        assert self.mode == "sigint-survivor"
        assert self.cli.poll() is None and self.image_id is not None
        # Signal only the admitted caller, never its group or privileged receiver.
        signal.pidfd_send_signal(self.cli_pidfd, signal.SIGINT)
        stdout, stderr = self.cli.communicate(timeout=45)
        assert self.cli.returncode > 0, (self.cli.returncode, stdout, stderr)
        response = json.loads(stdout)
        assert response["status"] == "error" and response["data"] is None
        assert "context canceled" in response["error"]["message"]
        print(f"SIGINT caller stdout={stdout} stderr={stderr}", flush=True)
        receipt = self.require("interrupted")
        assert receipt == {
            "phase": "interrupted",
            "image_id": self.image_id,
            "receiver_exited": False,
            "receiver_stopped": True,
        }

    def finish_survivor(self):
        assert self.mode == "sigint-survivor" and self.cli.returncode > 0
        receipt = self.require("continue")
        assert receipt["phase"] == "settled" and receipt["image_id"] == self.image_id
        assert receipt["receiver_exited"] is True
        assert receipt["source_recovery_pending"] is False
        assert receipt["source_generation"] == self.source_generation
        self.observer.wait(timeout=10)
        assert self.observer.returncode == 0, self.observer.stderr.read(16385)
        return receipt

    def close(self):
        errors = []
        try:
            if self.cli is not None:
                if self.cli.poll() is None:
                    assert self.cli_pidfd is not None
                    signal.pidfd_send_signal(self.cli_pidfd, signal.SIGKILL)
                stdout, stderr = self.cli.communicate(timeout=10)
                if self.cli.returncode == 0:
                    created = _json_success_data(stdout, dict)
                    assert (
                        created["type"] == self.name
                        and created["id"] not in self.baseline_ids
                    )
                    assert self.image_id is None or self.image_id == created["id"]
                    self.image_id = created["id"]
                print(
                    f"import cleanup exit={self.cli.returncode} stdout={stdout} stderr={stderr}",
                    flush=True,
                )
        except Exception as error:
            errors.append(f"CLI cleanup: {error}")
        finally:
            if self.cli_pidfd is not None:
                os.close(self.cli_pidfd)
            if self.cli is not None:
                self.cli.stdout.close()
                self.cli.stderr.close()
        try:
            self.observer.stdin.close()
            self.observer.wait(timeout=35)
            diagnostic = self.observer.stderr.read(16385)
            assert self.observer.returncode == 0, diagnostic
        except Exception as error:
            errors.append(f"receiver observer cleanup: {error}")
        finally:
            self.observer.stdout.close()
            self.observer.stderr.close()
        if self.image_id is not None:
            try:
                rows = [
                    image
                    for image in _images(self.runner_vm)
                    if image["id"] == self.image_id
                ]
                if rows:
                    assert len(rows) == 1 and rows[0]["type"] == self.name
                    rows[0]["_backing_identity"] = _backing_identity(rows[0]["path"])
                    _remove_image(self.runner_vm, rows[0])
                else:
                    _run_mvm(
                        self.runner_vm,
                        "image",
                        "rm",
                        self.image_id,
                        "--force",
                        "--json",
                    )
                assert all(
                    image["id"] != self.image_id for image in _images(self.runner_vm)
                )
            except Exception as error:
                errors.append(f"owned import cleanup: {error}")
        assert not errors, errors


def _images(runner_vm):
    return _json_success_data(_run_mvm(runner_vm, "image", "ls", "--json").stdout, list)


def _alpine(runner_vm):
    matches = [
        i
        for i in _images(runner_vm)
        if i["type"] == "alpine" and i["version"] == "3.23" and i["is_present"]
    ]
    assert len(matches) == 1, (
        "runner prerequisite: exactly one present mirrored Alpine 3.23 image"
    )
    return matches[0]


def _backing_identity(path):
    info = Path(path).lstat()
    assert Path(path).is_file() and not Path(path).is_symlink(), path
    return info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns


def _defaults(runner_vm):
    return [i["id"] for i in _images(runner_vm) if i["is_default"]]


def _image_state(items):
    # SetDefault records the selection time, including when restoring the prior default.
    return [
        {key: value for key, value in item.items() if key != "updated_at"}
        for item in items
    ]


def _restore_default(runner_vm, original):
    # The CLI has no clear-default operation; require a prepared default before changing it.
    assert len(original) == 1, "runner prerequisite: one original image default"
    _run_mvm(runner_vm, "image", "default", original[0])
    assert _defaults(runner_vm) == original


@pytest.fixture(autouse=True)
def image_workspace(runner_vm, tmp_path, monkeypatch):
    """Keep warm files private and prove that prepared image records/backings survive each case."""
    before = _images(runner_vm)
    backing = {i["id"]: _backing_identity(i["path"]) for i in before if i["is_present"]}
    monkeypatch.setenv("MVM_TEMP_DIR", str(tmp_path / "work"))
    monkeypatch.delenv("MVM_WARM_POOL", raising=False)
    yield tmp_path
    assert _image_state(_images(runner_vm)) == _image_state(before), (
        "test changed pre-existing image records or leaked an image"
    )
    for image in before:
        if image["id"] in backing:
            assert _backing_identity(image["path"]) == backing[image["id"]]


@pytest.fixture
def isolated_pull_cache(runner_vm, image_workspace, monkeypatch):
    """Force pulls may replace only records created inside this selected cache."""
    with monkeypatch.context() as selection:
        cache = image_workspace / "pull-cache"
        selection.setenv("MVM_CACHE_DIR", str(cache))
        selection.setenv("MVM_CONFIG_DIR", str(image_workspace / "pull-config"))
        selection.setenv("MVM_TEMP_DIR", str(cache / "tmp"))
        _run_mvm(runner_vm, "cache", "init", "--json")
        # Project the already installed trusted release into the private database.
        _run_mvm(runner_vm, "bin", "pull", "1.16.2", "--default", "--json", timeout=300)
        yield cache
        for item in _images(runner_vm):
            _remove_image(runner_vm, item)
        assert _images(runner_vm) == []


def _extract_alpine(runner_vm, directory):
    image = _alpine(runner_vm)
    raw = directory / f"alpine-{uuid.uuid4().hex}.raw"
    _guest_run(
        runner_vm, f"zstd -d {shlex.quote(image['path'])} -o {shlex.quote(str(raw))}"
    )
    assert raw.is_file() and raw.stat().st_size > 0
    # A unique filesystem UUID also prevents content-derived collisions if IDs change later.
    _guest_run(runner_vm, f"tune2fs -U {uuid.uuid4()} {shlex.quote(str(raw))}")
    return raw


def _ext4(runner_vm, directory, size="64M"):
    raw = directory / f"ext4-{uuid.uuid4().hex}.raw"
    seed = directory / f"ext4-seed-{uuid.uuid4().hex}"
    seed.mkdir(mode=0o700)
    (seed / "etc").mkdir()
    (seed / "etc" / "os-release").write_text("ID=linux\n", encoding="utf-8")
    _guest_run(runner_vm, f"truncate -s {shlex.quote(size)} {shlex.quote(str(raw))}")
    _guest_run(
        runner_vm, f"mkfs.ext4 -F -d {shlex.quote(str(seed))} {shlex.quote(str(raw))}"
    )
    return raw


def _partition_disk(runner_vm, directory):
    """Type favors partition 1; disabling type favors the larger partition 2."""
    sources = [_ext4(runner_vm, directory), _ext4(runner_vm, directory, "128M")]
    uuids = [
        _guest_run(
            runner_vm, f"blkid -p -s UUID -o value {shlex.quote(str(source))}"
        ).stdout.strip()
        for source in sources
    ]
    assert uuids[0] and uuids[1] and uuids[0] != uuids[1]
    disk = directory / "partitioned.raw"
    _guest_run(runner_vm, f"truncate -s 194M {shlex.quote(str(disk))}")
    _guest_run(
        runner_vm,
        f"printf 'label: dos\nstart=2048,size=131072,type=83\nstart=133120,size=262144,type=7\n' | sfdisk {shlex.quote(str(disk))}",
    )
    for source, offset in zip(sources, (1, 65), strict=True):
        _guest_run(
            runner_vm,
            f"dd if={shlex.quote(str(source))} of={shlex.quote(str(disk))} bs=1M seek={offset} conv=notrunc status=none",
        )
    return disk, uuids


def _assert_partition_uuid(runner_vm, image, expected, directory):
    """Read the actual imported compressed backing, independently of its UUID metadata."""
    raw = directory / f"readback-{image['id']}.raw"
    _guest_run(
        runner_vm, f"zstd -d {shlex.quote(image['path'])} -o {shlex.quote(str(raw))}"
    )
    actual = _guest_run(
        runner_vm, f"blkid -p -s UUID -o value {shlex.quote(str(raw))}"
    ).stdout.strip()
    assert actual == expected and image["fs_uuid"] == expected


def _qcow2(runner_vm, raw):
    output = raw.with_suffix(".qcow2")
    _guest_run(
        runner_vm,
        f"qemu-img convert -f raw -O qcow2 {shlex.quote(str(raw))} {shlex.quote(str(output))}",
    )
    info = json.loads(
        _guest_run(
            runner_vm, f"qemu-img info --output=json {shlex.quote(str(output))}"
        ).stdout
    )
    assert info["format"] == "qcow2"
    return output


def _remove_image(runner_vm, receipt, *flags):
    """Remove only the exact full-ID record and backing admitted by a creation receipt."""
    current = next(i for i in _images(runner_vm) if i["id"] == receipt["id"])
    assert current["path"] == receipt["path"] and current["type"] == receipt["type"]
    if "_backing_identity" in receipt:
        assert _backing_identity(receipt["path"]) == receipt["_backing_identity"]
    result = _json_success_data(
        _run_mvm(runner_vm, "image", "rm", receipt["id"], *flags, "--json").stdout, dict
    )
    assert len(result["items"]) == 1
    assert result["items"][0]["result"]["code"] == "image.removed"
    assert result["items"][0]["error"] is None
    assert all(i["id"] != receipt["id"] for i in _images(runner_vm))
    assert not Path(receipt["path"]).exists(), receipt


@contextmanager
def _imported(runner_vm, source, *flags):
    """A unique import identity must be absent before effects; successful JSON grants cleanup ownership."""
    name = f"sys-image-{uuid.uuid4().hex}"
    before = _images(runner_vm)
    assert all(i["type"] != name and i["name"] != name for i in before)
    item = None
    identity = None
    try:
        result = _run_mvm(
            runner_vm,
            "image",
            "import",
            name,
            str(source),
            *flags,
            "--json",
            timeout=300,
        )
        receipt = _json_success_data(result.stdout, dict)
        assert re.fullmatch(r"[0-9a-f]{64}", receipt["id"]), receipt
        assert receipt["id"] not in {i["id"] for i in before}, (
            "creation receipt collided with a baseline ID"
        )
        assert receipt["path"] not in {i["path"] for i in before}, (
            "creation receipt reused a baseline backing"
        )
        item = receipt
        identity = _backing_identity(item["path"])
        item["_backing_identity"] = identity
        assert item["type"] == name and item["is_imported"] and item["is_present"], item
        listed = next(i for i in _images(runner_vm) if i["id"] == item["id"])
        assert all(
            listed[key] == item[key]
            for key in (
                "id",
                "type",
                "version",
                "path",
                "fs_type",
                "is_imported",
                "is_present",
            )
        )
        yield item
    finally:
        if item is not None:
            current = next(
                (i for i in _images(runner_vm) if i["id"] == item["id"]), None
            )
            if current is not None:
                assert _backing_identity(item["path"]) == identity, (
                    "owned image backing changed before cleanup"
                )
                _remove_image(runner_vm, item)


@contextmanager
def _booted(runner_vm, image, *, disk_size_mib=None):
    if disk_size_mib is None:
        disk_size_mib = max(512, image["minimum_rootfs_size_mib"] + 64)
    name = f"sys-image-vm-{uuid.uuid4().hex[:8]}"
    before = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
    assert all(vm["name"] != name for vm in before)
    receipt = None
    try:
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                name,
                "--image",
                image["id"],
                "--no-network",
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--no-pci",
                "-s",
                f"{disk_size_mib}M",
                "--mem",
                "256M",
                "--vcpu",
                "1",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1
        candidate = created[0]
        assert candidate["id"] not in {vm["id"] for vm in before}
        assert candidate["name"] == name
        receipt = candidate
        assert receipt["status"] == "running" and receipt["image_id"] == image["id"]
        yield receipt
    finally:
        if receipt is not None:
            _run_mvm(
                runner_vm, "vm", "rm", receipt["id"], "--force", "--json", timeout=120
            )
            after = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            assert after == before, "VM teardown changed unrelated VMs or leaked its VM"


class TestImagePull:
    @pytest.mark.parametrize(
        "image_args",
        [
            ["alpine:3.23"],
            ["alpine", "--version", "3.23"],
            ["unused-selector", "--type", "alpine", "--version", "3.23"],
        ],
        ids=["slug", "version", "type-override"],
    )
    def test_image_pull(self, runner_vm, image_args):
        """Cached pull flags select the exact prepared image without replacing it."""
        expected = _alpine(runner_vm)
        item = _json_success_data(
            _run_mvm(
                runner_vm, "image", "pull", *image_args, "--json", timeout=300
            ).stdout,
            dict,
        )
        assert item == expected


class TestImageList:
    def test_image_list_json(self, runner_vm):
        image = _alpine(runner_vm)
        assert re.fullmatch(r"[0-9a-f]{64}", image["id"])
        assert image["fs_type"] == "ext4" and image["distro"] == "alpine"

    def test_image_list_table(self, runner_vm):
        image = _alpine(runner_vm)
        output = _run_mvm(runner_vm, "image", "ls").stdout
        assert all(
            field in output.splitlines()[0]
            for field in ("ID", "NAME", "VERSION", "TYPE")
        )
        assert image["id"][:6] in output and image["name"] in output

    def test_image_inspect_human(self, runner_vm):
        image = _alpine(runner_vm)
        output = _run_mvm(runner_vm, "image", "inspect", image["id"]).stdout
        assert f"Image: {image['name']}" in output
        assert f"ID: {image['id']}" in output and f"Path: {image['path']}" in output

    def test_image_list_remote(self, runner_vm):
        """Bound registry metadata to Alpine; no dynamically selected artifact downloads."""
        data = _json_success_data(
            _run_mvm(
                runner_vm, "image", "ls", "--remote", "--type", "alpine", "--json"
            ).stdout,
            list,
        )
        assert data and all(i["type"] == "alpine" for i in data)
        assert any(i["version"] == "3.23" for i in data)

    def test_image_inspect_json(self, runner_vm):
        image = _alpine(runner_vm)
        data = _json_success_data(
            _run_mvm(runner_vm, "image", "inspect", image["id"][:6], "--json").stdout,
            dict,
        )
        assert (
            data["image"]["id"] == image["id"]
            and data["image"]["name"] == image["name"]
        )
        assert (
            data["storage"]["path"] == image["path"]
            and data["storage"]["fs_type"] == "ext4"
        )


class TestImageDefaults:
    @pytest.mark.destructive
    def test_image_set_default(self, runner_vm):
        original = _defaults(runner_vm)
        assert len(original) == 1
        try:
            target = _alpine(runner_vm)["id"]
            _run_mvm(runner_vm, "image", "default", target)
            assert _defaults(runner_vm) == [target]
        finally:
            _restore_default(runner_vm, original)

    def test_set_default_nonexistent_image_fails(self, runner_vm):
        before = _images(runner_vm)
        result = _run_mvm(
            runner_vm,
            "image",
            "default",
            f"missing-{uuid.uuid4().hex}",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        response = json.loads(result.stdout)
        assert (
            response["schema_version"] == 1
            and response["status"] == "error"
            and response["data"] is None
        )
        assert response["error"]["code"] == "image.not_found"
        assert _images(runner_vm) == before


class TestImageWarm:
    @pytest.mark.parametrize("selector", ["slug", "prefix", "all"])
    def test_image_warm(self, runner_vm, image_workspace, selector):
        """Warm output contains complete bytes from each selected compressed backing."""
        image = _alpine(runner_vm)
        selected = _images(runner_vm) if selector == "all" else [image]
        args = (
            ["--all"]
            if selector == "all"
            else ["alpine:3.23" if selector == "slug" else image["id"][:6]]
        )
        paths = _json_success_data(
            _run_mvm(runner_vm, "image", "warm", *args, "--json").stdout, list
        )
        assert len(paths) == len(selected)
        for item, path in zip(selected, paths, strict=True):
            warmed = Path(path)
            assert warmed.parent == image_workspace / "work" / "ready"
            assert warmed.name == f"{item['id']}.{item['fs_type']}"
            expected = image_workspace / f"expected-{item['id']}.raw"
            _guest_run(
                runner_vm,
                f"zstd -d {shlex.quote(item['path'])} -o {shlex.quote(str(expected))}",
            )
            assert warmed.stat().st_size == expected.stat().st_size
            with warmed.open("rb") as actual, expected.open("rb") as source:
                assert (
                    hashlib.file_digest(actual, "sha256").digest()
                    == hashlib.file_digest(source, "sha256").digest()
                )
        identities = [_backing_identity(path) for path in paths]
        again = _json_success_data(
            _run_mvm(runner_vm, "image", "warm", *args, "--json").stdout, list
        )
        assert (
            again == paths and [_backing_identity(path) for path in again] == identities
        )

    def test_warm_nonexistent_image_fails(self, runner_vm):
        result = _run_mvm(
            runner_vm,
            "image",
            "warm",
            f"missing-{uuid.uuid4().hex}",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        response = json.loads(result.stdout)
        assert (
            response["status"] == "error"
            and response["error"]["code"] == "image.not_found"
        )


class TestImageImport:
    def test_image_import_local_file(self, runner_vm, image_workspace):
        source = _extract_alpine(runner_vm, image_workspace)
        with _imported(runner_vm, source, "--format", "raw") as image:
            assert image["fs_type"] == "ext4" and image["distro"] == "alpine"

    def test_import_from_nonexistent_path_fails(self, runner_vm, image_workspace):
        name = f"missing-{uuid.uuid4().hex}"
        before = _images(runner_vm)
        result = _run_mvm(
            runner_vm,
            "image",
            "import",
            name,
            str(image_workspace / "missing.qcow2"),
            "--json",
            check=False,
        )
        assert result.returncode != 0
        response = json.loads(result.stdout)
        assert (
            response["schema_version"] == 1
            and response["status"] == "error"
            and response["data"] is None
        )
        assert response["error"]["code"] == "image.import.failed"
        assert "source not found" in response["error"]["message"]
        assert _images(runner_vm) == before


class TestImagePullAdvanced:
    @pytest.mark.destructive
    def test_pull_cached_image_with_default_sets_default(self, runner_vm):
        original = _defaults(runner_vm)
        assert len(original) == 1
        try:
            image = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "image",
                    "pull",
                    "alpine:3.23",
                    "--default",
                    "--json",
                    timeout=300,
                ).stdout,
                dict,
            )
            assert image["id"] == _alpine(runner_vm)["id"]
            assert _defaults(runner_vm) == [image["id"]]
        finally:
            _restore_default(runner_vm, original)

    @pytest.mark.parametrize(
        "flags",
        [[], ["--no-cache"], ["--skip-optimization"], ["--disable-detector", "all"]],
        ids=["force", "no-cache", "skip-optimization", "disable-detector"],
    )
    def test_pull_cached_image_with_force_redownloads(
        self, runner_vm, isolated_pull_cache, flags
    ):
        """Mirror-backed replacement preserves the selected default and retires its old backing."""
        first = _json_success_data(
            _run_mvm(
                runner_vm,
                "image",
                "pull",
                "alpine:3.23",
                "--default",
                "--json",
                timeout=300,
            ).stdout,
            dict,
        )
        assert first["is_default"] is True
        # IDs include second-resolution creation time: wait for a distinct creation interval.
        _guest_run(runner_vm, "sleep 1")
        second = _json_success_data(
            _run_mvm(
                runner_vm,
                "image",
                "pull",
                "alpine:3.23",
                "--force",
                *flags,
                "--json",
                timeout=300,
            ).stdout,
            dict,
        )
        assert second["is_default"] is True
        assert second["id"] != first["id"]
        assert (
            second["type"] == "alpine"
            and second["version"] == "3.23"
            and second["is_present"]
        )
        assert _defaults(runner_vm) == [second["id"]]
        assert Path(second["path"]).is_file() and not Path(first["path"]).exists()
        assert all(
            i["id"] == second["id"] or not i["is_present"] for i in _images(runner_vm)
        )

    def test_pull_nonexistent_image_fails_gracefully(self, runner_vm):
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            f"missing-{uuid.uuid4().hex}",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        response = json.loads(result.stdout)
        assert (
            response["status"] == "error"
            and response["error"]["code"] == "image.pull.failed"
        )


class TestImageImportAdvanced:
    pytestmark = [pytest.mark.tier2]

    @pytest.mark.requires_kvm
    def test_image_import_with_format_qcow2(self, runner_vm, image_workspace):
        source = _qcow2(runner_vm, _extract_alpine(runner_vm, image_workspace))
        with _imported(
            runner_vm, source, "--format", "qcow2", "--skip-optimization"
        ) as image:
            assert image["fs_type"] == "ext4" and image["distro"] == "alpine"
            with _booted(runner_vm, image) as vm:
                guest = _run_mvm(
                    runner_vm, "exec", vm["id"], "--", "cat", "/etc/os-release"
                )
                assert "ID=alpine" in guest.stdout.splitlines()

    def test_image_import_force_overwrite(self, runner_vm, image_workspace):
        source = _ext4(runner_vm, image_workspace)
        with _imported(
            runner_vm, source, "--format", "raw", "--skip-optimization"
        ) as image:
            again = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "image",
                    "import",
                    image["type"],
                    str(source),
                    "--format",
                    "raw",
                    "--json",
                ).stdout,
                dict,
            )
            assert again["id"] == image["id"]
            _guest_run(runner_vm, "sleep 1")
            before = _images(runner_vm)
            new = None
            try:
                candidate = _json_success_data(
                    _run_mvm(
                        runner_vm,
                        "image",
                        "import",
                        image["type"],
                        str(source),
                        "--format",
                        "raw",
                        "--skip-optimization",
                        "--force",
                        "--json",
                        timeout=300,
                    ).stdout,
                    dict,
                )
                assert re.fullmatch(r"[0-9a-f]{64}", candidate["id"])
                assert candidate["id"] not in {item["id"] for item in before}
                assert candidate["path"] not in {item["path"] for item in before}
                assert candidate["type"] == image["type"]
                new = candidate
                new["_backing_identity"] = _backing_identity(new["path"])
                assert new["is_imported"] and new["is_present"]
                assert not Path(image["path"]).exists()
            finally:
                if new is not None:
                    _remove_image(runner_vm, new)

    def test_image_import_with_root_partition(self, runner_vm, image_workspace):
        """Explicit partition 2 must differ from the automatic detector's partition 1."""
        disk, uuids = _partition_disk(runner_vm, image_workspace)
        qcow2 = _qcow2(runner_vm, disk)
        with _imported(
            runner_vm, qcow2, "--format", "qcow2", "--skip-optimization"
        ) as automatic:
            _assert_partition_uuid(runner_vm, automatic, uuids[0], image_workspace)
        with _imported(
            runner_vm,
            qcow2,
            "--format",
            "qcow2",
            "--root-partition",
            "2",
            "--skip-optimization",
        ) as image:
            _assert_partition_uuid(runner_vm, image, uuids[1], image_workspace)

    @pytest.mark.parametrize(
        "disabled", ["type", "all"], ids=["disable-type", "disable-all"]
    )
    def test_image_import_with_disable_detector(
        self, runner_vm, image_workspace, disabled
    ):
        """The sfdisk path has no label/filesystem scores; type and size decide this layout."""
        disk, uuids = _partition_disk(runner_vm, image_workspace)
        qcow2 = _qcow2(runner_vm, disk)
        # With type enabled: 64 MiB Linux scores .5-.25; 128 MiB neutral scores zero.
        # Disabling type selects partition 2; disabling all ties both at zero.
        with _imported(
            runner_vm, qcow2, "--format", "qcow2", "--skip-optimization"
        ) as automatic:
            _assert_partition_uuid(runner_vm, automatic, uuids[0], image_workspace)
        if disabled == "type":
            with _imported(
                runner_vm,
                qcow2,
                "--format",
                "qcow2",
                "--skip-optimization",
                "--disable-detector",
                disabled,
            ) as image:
                _assert_partition_uuid(runner_vm, image, uuids[1], image_workspace)
        else:
            before = _images(runner_vm)
            name = f"sys-image-{uuid.uuid4().hex}"
            assert all(item["type"] != name for item in before)
            unexpected = None
            try:
                result = _run_mvm(
                    runner_vm,
                    "image",
                    "import",
                    name,
                    str(qcow2),
                    "--format",
                    "qcow2",
                    "--skip-optimization",
                    "--disable-detector",
                    disabled,
                    "--json",
                    check=False,
                    timeout=300,
                )
                if result.returncode == 0:
                    candidate = _json_success_data(result.stdout, dict)
                    assert re.fullmatch(r"[0-9a-f]{64}", candidate["id"])
                    assert candidate["id"] not in {item["id"] for item in before}
                    assert candidate["path"] not in {item["path"] for item in before}
                    assert candidate["type"] == name
                    unexpected = candidate
                    unexpected["_backing_identity"] = _backing_identity(
                        unexpected["path"]
                    )
                assert result.returncode != 0
                response = json.loads(result.stdout)
                assert (
                    response["schema_version"] == 1
                    and response["status"] == "error"
                    and response["data"] is None
                )
                assert response["error"]["code"] == "image.import.failed"
                assert "tie score 0.000000" in response["error"]["message"]
                assert _images(runner_vm) == before
            finally:
                if unexpected is not None:
                    _remove_image(runner_vm, unexpected)

    @pytest.mark.parametrize(
        "flags,version",
        [
            ([], ""),
            (["--version", "1.0"], "1.0"),
            (["--skip-optimization"], ""),
        ],
        ids=[
            "auto-format",
            "version",
            "skip-optimization",
        ],
    )
    def test_image_import_without_format_auto_detect(
        self, runner_vm, image_workspace, flags, version
    ):
        source = _ext4(runner_vm, image_workspace)
        with _imported(runner_vm, source, *flags) as image:
            assert image["version"] == version and image["fs_type"] == "ext4"
            assert image["arch"] == _alpine(runner_vm)["arch"]


class TestImageImportSetDefault:
    @pytest.mark.destructive
    def test_image_import_with_set_default(self, runner_vm, image_workspace):
        original = _defaults(runner_vm)
        assert len(original) == 1
        source = _ext4(runner_vm, image_workspace)
        try:
            with _imported(
                runner_vm, source, "--format", "raw", "--default", "--skip-optimization"
            ) as image:
                assert _defaults(runner_vm) == [image["id"]]
        finally:
            _restore_default(runner_vm, original)


class TestImageImportCreateVM:
    pytestmark = [pytest.mark.tier2, pytest.mark.requires_kvm, pytest.mark.slow]

    def test_vm_import_sigint_survivor_refuses_release_then_restores_and_discards(
        self, runner_vm, image_workspace
    ):
        """Caller SIGINT leaves a surviving locked receiver to settle, not root cancellation."""
        marker = f"/qa-image-import-{uuid.uuid4().hex}"
        contents = f"sigint-survivor-{uuid.uuid4().hex}"

        def running_source(source, *, pin_process):
            listed = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            rows = [item for item in listed if item["id"] == source["id"]]
            assert len(rows) == 1 and rows[0]["status"] == "running"
            observed = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", source["id"], "--json").stdout,
                dict,
            )["vm"]
            assert observed["status"] == "running"
            for key in ("id", "rootfs_path"):
                assert observed[key] == rows[0][key] == source[key]
            for key in ("pid", "process_start_time"):
                assert observed[key] == rows[0][key] and observed[key] > 0
                if pin_process:
                    assert observed[key] == source[key]
            return rows[0]

        with _booted(runner_vm, _alpine(runner_vm), disk_size_mib=512) as source:
            written = _run_mvm(
                runner_vm,
                "exec",
                source["id"],
                "--user",
                "root",
                "--timeout",
                "30",
                "--",
                "sh",
                "-c",
                f"printf '%s' {shlex.quote(contents)} > {shlex.quote(marker)} "
                f"&& sync && cat {shlex.quote(marker)}",
            )
            assert written.stdout == contents
            source = running_source(source, pin_process=False)
            fault = _VMImportReceiverDeath(runner_vm, source, mode="sigint-survivor")
            try:
                fault.arm(source)
                fault.interrupt()
                assert all(
                    image["id"] != fault.image_id and image["type"] != fault.name
                    for image in _images(runner_vm)
                )
                # No public VM observation while the suspended receiver owns the VM lock.
                refused = _run_mvm(
                    runner_vm,
                    "image",
                    "rm",
                    fault.image_id,
                    "--force",
                    "--json",
                    check=False,
                )
                assert refused.returncode != 0
                response = json.loads(refused.stdout)
                assert response["status"] == "error"
                assert response["error"]["code"] == "cli.batch.failed"
                assert len(response["data"]["items"]) == 1
                item = response["data"]["items"][0]
                assert item["result"]["status"] == "error"
                assert item["result"]["item"] == fault.image_id
                assert item["error"]["code"] == "process.error"
                assert item["error"]["class"] == "retryable"
                assert (
                    "wait for protected backing object lock" in item["error"]["message"]
                )
                assert fault.require("busy") == {
                    "phase": "busy",
                    "image_id": fault.image_id,
                    "copy_authority_unchanged": True,
                }
                assert all(
                    image["id"] != fault.image_id and image["type"] != fault.name
                    for image in _images(runner_vm)
                )
                fault.finish_survivor()
                running_source(source, pin_process=True)
                readback = _run_mvm(
                    runner_vm,
                    "exec",
                    source["id"],
                    "--user",
                    "root",
                    "--timeout",
                    "30",
                    "--",
                    "cat",
                    marker,
                )
                assert readback.stdout == contents
                for _ in range(2):
                    removed = _json_success_data(
                        _run_mvm(
                            runner_vm,
                            "image",
                            "rm",
                            fault.image_id,
                            "--force",
                            "--json",
                        ).stdout,
                        dict,
                    )
                    assert len(removed["items"]) == 1
                    item = removed["items"][0]
                    assert item["error"] is None
                    result = item["result"]
                    assert result["status"] == "success"
                    assert result["code"] == "image.export_removed"
                    assert result["item"] == fault.image_id and result["warnings"] == []
                    assert result["metadata"] == {
                        "export_only": True,
                        "source_vm_id": source["id"],
                    }
                assert all(
                    image["id"] != fault.image_id and image["type"] != fault.name
                    for image in _images(runner_vm)
                )
                running_source(source, pin_process=True)
            finally:
                fault.close()

    def test_vm_import_receiver_death_retires_orphan_and_recovers_source(
        self, runner_vm, image_workspace
    ):
        """Actual client/receiver death leaves public exact-ID recovery, not a partial image."""
        marker = f"/qa-image-import-{uuid.uuid4().hex}"
        contents = f"receiver-death-{uuid.uuid4().hex}"

        def observed_source(source, status, *, pin_process=True):
            rows = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            matches = [vm for vm in rows if vm["id"] == source["id"]]
            assert len(matches) == 1 and matches[0]["status"] == status
            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", source["id"], "--json").stdout,
                dict,
            )["vm"]
            for key in ("id", "rootfs_path"):
                assert inspected[key] == matches[0][key] == source[key]
            for key in ("pid", "process_start_time"):
                assert inspected[key] == matches[0][key] and matches[0][key] > 0
                if pin_process:
                    assert matches[0][key] == source[key]
            assert inspected["status"] == status
            return matches[0]

        with _booted(runner_vm, _alpine(runner_vm), disk_size_mib=512) as source:
            written = _run_mvm(
                runner_vm,
                "exec",
                source["id"],
                "--user",
                "root",
                "--timeout",
                "30",
                "--",
                "sh",
                "-c",
                f"printf '%s' {shlex.quote(contents)} > {shlex.quote(marker)} "
                f"&& sync && cat {shlex.quote(marker)}",
            )
            assert written.stdout == contents
            source = observed_source(source, "running", pin_process=False)
            fault = _VMImportReceiverDeath(runner_vm, source)
            try:
                fault.arm(source)
                fault.kill()
                assert all(
                    image["id"] != fault.image_id for image in _images(runner_vm)
                )
                observed_source(source, "paused")
                for _ in range(2):
                    removed = _json_success_data(
                        _run_mvm(
                            runner_vm,
                            "image",
                            "rm",
                            fault.image_id,
                            "--force",
                            "--json",
                        ).stdout,
                        dict,
                    )
                    assert len(removed["items"]) == 1
                    item = removed["items"][0]
                    assert item["error"] is None
                    result = item["result"]
                    assert (
                        result["status"] == "success"
                        and result["code"] == "image.export_removed"
                    )
                    assert result["item"] == fault.image_id
                    assert result["metadata"] == {
                        "export_only": True,
                        "source_vm_id": source["id"],
                    }
                    assert result["warnings"] == [
                        f"Source VM {source['id']} still requires state recovery or normal termination; "
                        "only the independent copy was removed."
                    ]
                    assert fault.require("released")["phase"] == "released"
                refused = _run_mvm(
                    runner_vm, "vm", "resume", source["id"], "--json", check=False
                )
                assert refused.returncode != 0
                response = json.loads(refused.stdout)
                assert response["status"] == "error" and response["data"] is None
                assert (
                    "VM rootfs export requires recovery; repeat image import or stop the VM"
                    in response["error"]["message"]
                )
                observed_source(source, "paused")
                assert fault.require("released")["phase"] == "released"
                with _imported(
                    runner_vm, source["id"], "--skip-optimization"
                ) as recovered:
                    assert recovered["id"] != fault.image_id
                    observed_source(source, "running")
                    assert (
                        fault.require(f"recovered {recovered['id']}")["phase"]
                        == "recovered"
                    )
                    source_readback = _run_mvm(
                        runner_vm,
                        "exec",
                        source["id"],
                        "--user",
                        "root",
                        "--timeout",
                        "30",
                        "--",
                        "cat",
                        marker,
                    )
                    assert source_readback.stdout == contents
                    with _booted(runner_vm, recovered, disk_size_mib=512) as clone:
                        readback = _run_mvm(
                            runner_vm,
                            "exec",
                            clone["id"],
                            "--user",
                            "root",
                            "--timeout",
                            "30",
                            "--",
                            "cat",
                            marker,
                        )
                        assert readback.stdout == contents
                    observed_source(source, "running")
            finally:
                fault.close()

    @pytest.mark.parametrize("original_status", ["running", "paused", "stopped"])
    def test_vm_import_preserves_guest_contents_and_source_state(
        self, runner_vm, image_workspace, original_status
    ):
        """VM selectors capture guest-written bytes and restore the source's observed state."""
        marker = f"/qa-image-import-{uuid.uuid4().hex}"
        guest_contents = f"guest-{uuid.uuid4().hex}"

        def source_state(source):
            listed = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            matches = [item for item in listed if item["id"] == source["id"]]
            assert len(matches) == 1 and matches[0]["status"] == original_status
            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", source["id"], "--json").stdout,
                dict,
            )
            assert inspected["vm"]["id"] == source["id"]
            assert inspected["vm"]["status"] == original_status
            return inspected

        with _booted(runner_vm, _alpine(runner_vm), disk_size_mib=512) as source:
            assert re.fullmatch(r"[0-9a-f]{32}", source["id"])
            written = _run_mvm(
                runner_vm,
                "exec",
                source["id"],
                "--user",
                "root",
                "--timeout",
                "30",
                "--",
                "sh",
                "-c",
                f"printf '%s' {shlex.quote(guest_contents)} > {shlex.quote(marker)} "
                f"&& sync && cat {shlex.quote(marker)}",
            )
            assert written.stdout == guest_contents
            if original_status != "running":
                _run_mvm(
                    runner_vm,
                    "vm",
                    "pause" if original_status == "paused" else "stop",
                    source["id"],
                    "--json",
                )
            inspected = source_state(source)
            if original_status == "stopped":
                # The caller's staging file is not the disk receiving guest writes.
                # Put contradictory bytes there, without opening protected root storage.
                cache = Path(
                    os.environ.get("MVM_CACHE_DIR", str(Path.home() / ".cache/mvmctl"))
                )
                staging = cache / "vms" / source["id"] / "rootfs.img"
                assert staging.is_absolute() and staging.resolve() == staging
                assert staging == Path(source["rootfs_path"])
                assert staging == Path(inspected["filesystem"]["rootfs_path"])
                info = staging.lstat()
                assert (
                    staging.is_file()
                    and info.st_uid == os.getuid()
                    and info.st_nlink == 1
                )
                assert info.st_size == 512 * 1024 * 1024
                identity = (info.st_dev, info.st_ino, info.st_size)
                stale_contents = f"caller-staging-{uuid.uuid4().hex}"
                stale_marker = image_workspace / "staging-marker"
                stale_marker.write_text(stale_contents, encoding="utf-8")
                _guest_run(
                    runner_vm,
                    f"debugfs -w -R {shlex.quote(f'write {stale_marker} {marker}')} "
                    f"{shlex.quote(str(staging))}",
                )
                stale_readback = _guest_run(
                    runner_vm,
                    f"debugfs -R {shlex.quote(f'cat {marker}')} "
                    f"{shlex.quote(str(staging))}",
                )
                assert stale_readback.stdout == stale_contents
                after = staging.lstat()
                assert (after.st_dev, after.st_ino, after.st_size) == identity

            with _imported(runner_vm, source["id"], "--skip-optimization") as image:
                source_state(source)
                with _booted(runner_vm, image, disk_size_mib=512) as clone:
                    readback = _run_mvm(
                        runner_vm,
                        "exec",
                        clone["id"],
                        "--user",
                        "root",
                        "--timeout",
                        "30",
                        "--",
                        "cat",
                        marker,
                    )
                    assert readback.stdout == guest_contents
                source_state(source)

    def test_imported_image_vm_creation(self, runner_vm, image_workspace):
        source = _extract_alpine(runner_vm, image_workspace)
        with _imported(
            runner_vm, source, "--format", "raw", "--skip-optimization"
        ) as image:
            with _booted(runner_vm, image) as vm:
                assert (
                    "ID=alpine"
                    in _run_mvm(
                        runner_vm, "exec", vm["id"], "--", "cat", "/etc/os-release"
                    ).stdout.splitlines()
                )

    def test_import_ubuntu_tar_rootfs(self, runner_vm, image_workspace):
        """Retain tar-rootfs import/boot coverage using the prepared mirror, never curl."""
        mirror = os.environ.get("MVM_ASSET_MIRROR")
        assert mirror, (
            "runner prerequisite: MVM_ASSET_MIRROR points to the prepared read-only mirror"
        )
        source = Path(mirror) / "noble-server-cloudimg-amd64-root.tar.xz"
        assert source.is_file(), (
            f"runner prerequisite: mirrored Ubuntu tar-rootfs {source}"
        )
        mirror_identity = _backing_identity(source)
        with source.open("rb") as original:
            mirror_digest = hashlib.file_digest(original, "sha256").digest()
        try:
            # Only the private copy is an import source; the mirror remains outside cleanup ownership.
            local = image_workspace / source.name
            assert not local.exists()
            shutil.copyfile(source, local)
            assert _backing_identity(source) == mirror_identity
            assert local.stat().st_size == mirror_identity[2]
            with local.open("rb") as copied:
                assert hashlib.file_digest(copied, "sha256").digest() == mirror_digest
            with _imported(
                runner_vm, local, "--format", "tar-rootfs", "--skip-optimization"
            ) as image:
                assert image["distro"] == "ubuntu" and image["fs_type"] == "ext4"
                warmed = _json_success_data(
                    _run_mvm(runner_vm, "image", "warm", image["id"], "--json").stdout,
                    list,
                )
                assert len(warmed) == 1
                backing = Path(warmed[0])
                assert backing.parent == image_workspace / "work" / "ready"
                assert backing.name == f"{image['id']}.ext4"
                backing_bytes = _backing_identity(backing)[2]
                minimum_mib = image["minimum_rootfs_size_mib"]
                assert (
                    backing_bytes > 0
                    and isinstance(minimum_mib, int)
                    and minimum_mib > 0
                )
                disk_mib = max(minimum_mib, (backing_bytes + 1048575) // 1048576)
                with _booted(runner_vm, image, disk_size_mib=disk_mib) as vm:
                    release = _run_mvm(
                        runner_vm, "exec", vm["id"], "--", "cat", "/etc/os-release"
                    ).stdout.splitlines()
                    assert "ID=ubuntu" in release and 'VERSION_ID="24.04"' in release
        finally:
            assert _backing_identity(source) == mirror_identity
            with source.open("rb") as original:
                assert hashlib.file_digest(original, "sha256").digest() == mirror_digest


class TestImageRemove:
    @pytest.mark.destructive
    @pytest.mark.parametrize("flags", [[], ["--force"]], ids=["normal", "force"])
    def test_image_remove_with_fixture(self, runner_vm, image_workspace, flags):
        source = _ext4(runner_vm, image_workspace)
        with _imported(
            runner_vm, source, "--format", "raw", "--skip-optimization"
        ) as image:
            _remove_image(runner_vm, image, *flags)


class TestImageDependencyDeletion:
    pytestmark = [pytest.mark.tier2, pytest.mark.requires_kvm]

    @pytest.mark.parametrize("stopped", [False, True], ids=["running", "stopped"])
    def test_delete_image_used_by_stopped_vm_fails(
        self, runner_vm, image_workspace, stopped
    ):
        source = _extract_alpine(runner_vm, image_workspace)
        with _imported(
            runner_vm, source, "--format", "raw", "--skip-optimization"
        ) as image:
            with _booted(runner_vm, image) as vm:
                if stopped:
                    _run_mvm(
                        runner_vm,
                        "vm",
                        "stop",
                        vm["id"],
                        "--force",
                        "--json",
                        timeout=120,
                    )
                before = _images(runner_vm)
                identity = _backing_identity(image["path"])
                result = _run_mvm(
                    runner_vm, "image", "rm", image["id"], "--json", check=False
                )
                assert result.returncode != 0
                response = json.loads(result.stdout)
                assert response["status"] == "error"
                assert response["data"]["items"][0]["result"]["code"] == "image.in_use"
                assert (
                    _images(runner_vm) == before
                    and _backing_identity(image["path"]) == identity
                )
                vms = _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
                )
                assert next(i for i in vms if i["id"] == vm["id"])["status"] == (
                    "stopped" if stopped else "running"
                )

    @pytest.mark.destructive
    def test_delete_default_image_promotes_other_or_clears(
        self, runner_vm, image_workspace
    ):
        """Remove a formerly-default owned image after explicitly selecting its replacement."""
        original = _defaults(runner_vm)
        assert len(original) == 1
        with _imported(
            runner_vm,
            _ext4(runner_vm, image_workspace),
            "--format",
            "raw",
            "--skip-optimization",
        ) as image:
            try:
                _run_mvm(runner_vm, "image", "default", image["id"])
                assert _defaults(runner_vm) == [image["id"]]
                _restore_default(runner_vm, original)
                _remove_image(runner_vm, image)
                assert _defaults(runner_vm) == original
            finally:
                _restore_default(runner_vm, original)
