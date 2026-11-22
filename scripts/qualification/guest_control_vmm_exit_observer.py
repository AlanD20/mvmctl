"""Read-only named-QA observation of an admitted VMM and its retained worker.

Executed over the administrator's existing SSH channel, never installed or added
to sudoers. No signals, authority locks, lifecycle commands or record writes.
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import select
import stat
import sys
import time


def _regular(path, *, owner, mode, limit=16384, retain_identity=False):
    """Read one stable regular file through canonical, no-follow directories."""
    path = Path(path)
    assert path.is_absolute() and path.resolve(strict=True) == path
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        for name in path.parts[1:-1]:
            child = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            os.close(fd)
            fd = child
            row = os.fstat(fd)
            assert not row.st_mode & 0o022, "writable observation ancestor"
        leaf = os.open(path.name, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
        try:
            row = os.fstat(leaf)
            assert stat.S_ISREG(row.st_mode) and row.st_uid == owner
            assert owner != 0 or row.st_gid == 0
            assert stat.S_IMODE(row.st_mode) == mode and row.st_nlink == 1 and row.st_size <= limit
            raw = os.read(leaf, limit + 1)
            identity = lambda r: (r.st_dev, r.st_ino, r.st_uid, r.st_gid, r.st_mode, r.st_nlink,
                                  r.st_size, r.st_mtime_ns, r.st_ctime_ns)
            assert len(raw) == row.st_size and identity(os.fstat(leaf)) == identity(row)
            assert identity(os.stat(path.name, dir_fd=fd, follow_symlinks=False)) == identity(row)
            return (raw, identity(row)) if retain_identity else raw
        finally:
            os.close(leaf)
    finally:
        os.close(fd)


def _bind_vmm(instance, vm, boot):
    launch = instance["launch"]
    assert launch["host_boot_id"] == boot
    assert (launch["pid"], launch["start_time_ticks"]) == (vm["pid"], vm["process_start_time"])
    assert type(launch["pid"]) is int and launch["pid"] > 0
    assert type(launch["start_time_ticks"]) is int and launch["start_time_ticks"] > 0
    return launch["pid"], launch["start_time_ticks"]


def _observe_vmm(instance, pidfd, *, proc_root=Path("/proc"), poll_factory=select.poll):
    launch, active = instance["launch"], instance["active"]
    poller = poll_factory()
    poller.register(pidfd, select.POLLIN)
    assert not poller.poll(0), "VMM already exited"
    proc = proc_root / str(launch["pid"])
    raw = (proc / "stat").read_text().rpartition(") ")[2].split()
    assert int(raw[19]) == launch["start_time_ticks"] and raw[0] not in ("Z", "X")
    status = (proc / "status").read_text().splitlines()
    for prefix, key in (("Uid:", "uid"), ("Gid:", "gid")):
        expected = launch["execution_identity"][key]
        assert expected > 0
        assert [line.split()[1:] for line in status if line.startswith(prefix)] == [[str(expected)] * 4]
    for link, key in (("ns/mnt", "mount_namespace"), ("root", "jail_root")):
        row = (proc / link).stat()
        assert (row.st_dev, row.st_ino) == (active[key]["device"], active[key]["inode"])
    row = (proc / "exe").stat()
    expected = active["executable"]
    for key, value in dict(device=row.st_dev, inode=row.st_ino, size_bytes=row.st_size, mode=row.st_mode,
                           uid=row.st_uid, gid=row.st_gid, links=row.st_nlink).items():
        assert expected[key] == value, "VMM executable differs"
    assert int((proc / "stat").read_text().rpartition(") ")[2].split()[19]) == launch["start_time_ticks"]
    assert not poller.poll(0), "VMM exited during admission"


def _wait_exits(handles, timeout, *, poll_factory=select.poll, clock=time.monotonic):
    poller = poll_factory()
    for fd in handles.values():
        poller.register(fd, select.POLLIN)
    started = clock()
    events = {}
    while len(events) != len(handles):
        remaining = timeout - (clock() - started)
        assert remaining > 0, "exact retained process exit deadline expired"
        for fd, flags in poller.poll(min(250, max(1, int(remaining * 1000)))):
            assert flags & select.POLLIN and not flags & (select.POLLERR | select.POLLNVAL), "invalid pidfd event"
            name = next(key for key, value in handles.items() if value == fd)
            assert name not in events, "duplicate exit event"
            events[name] = clock() - started
            poller.unregister(fd)
    return events


def observe(expected):
    assert os.getuid() == os.geteuid() == 0
    namespace, vm = expected["namespace"], expected["vm"]
    boot = lambda: Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    assert os.uname().nodename == namespace["runner"] == "mvmctl-030-test"
    assert boot() == namespace["expected_vm"]["boot_id"]
    # Execute only already reviewed helper bytes, verified before compilation.
    raw = _regular(expected["helper_path"], owner=namespace["operator"]["uid"], mode=0o644, limit=65536)
    assert hashlib.sha256(raw).hexdigest() == expected["helper_sha256"]
    helper = {"__name__": "qualification_readonly_helpers"}
    exec(compile(raw, expected["helper_path"], "exec"), helper)
    uid, gid = namespace["operator"]["uid"], namespace["operator"]["gid"]
    vm_id = vm["binding"]["id"]
    assert len(vm_id) == 32 and all(c in "0123456789abcdef" for c in vm_id)
    worker_path = f"/var/lib/mvmctl/guest-control/{uid}/{vm_id}.json"
    instance_path = f"/var/lib/mvmctl/instances/{uid}/{vm_id}.json"
    worker_raw, worker_identity = _regular(worker_path, owner=0, mode=0o600, retain_identity=True)
    instance_raw, instance_identity = _regular(instance_path, owner=0, mode=0o600, retain_identity=True)
    worker = json.loads(worker_raw, object_pairs_hook=helper["_unique"])
    instance = json.loads(instance_raw, object_pairs_hook=helper["_unique"])
    process = helper["_bind_records"](worker, instance, uid, gid, vm_id, boot())
    assert process["executable"]["SHA256"] == expected["candidate_sha256"]
    vmm_pid, start = _bind_vmm(instance, vm, boot())
    handles = {}
    try:
        handles["worker"] = os.pidfd_open(process["pid"])
        handles["vmm"] = os.pidfd_open(vmm_pid)
        helper["_observe_process"](process, handles["worker"])
        _observe_vmm(instance, handles["vmm"])
        assert _regular(worker_path, owner=0, mode=0o600, retain_identity=True) == (worker_raw, worker_identity)
        assert _regular(instance_path, owner=0, mode=0o600, retain_identity=True) == (instance_raw, instance_identity)
        assert boot() == namespace["expected_vm"]["boot_id"]
        ready = dict(event="READY", vm_id=vm_id, boot_id=boot(), launch_generation=worker["launch_generation"],
                     worker_generation=worker["generation"], worker_pid=process["pid"],
                     worker_start=process["start_time_ticks"], vmm_pid=vmm_pid, vmm_start=start,
                     worker_record_sha256=hashlib.sha256(worker_raw).hexdigest(),
                     instance_record_sha256=hashlib.sha256(instance_raw).hexdigest())
        print(json.dumps(ready, sort_keys=True), flush=True)
        # The controller confirms READY before it schedules guest shutdown.
        assert select.select([sys.stdin], [], [], 30)[0], "observer ARM deadline expired"
        assert sys.stdin.readline(16) == "ARM\n", "observer requires exact ARM"
        for fd in handles.values():
            poller = select.poll(); poller.register(fd, select.POLLIN)
            assert not poller.poll(0), "process exited before ARM"
        print(json.dumps(dict(event="ARMED", vm_id=vm_id)), flush=True)
        exits = _wait_exits(handles, 60)
        assert boot() == ready["boot_id"]
        print(json.dumps(dict(event="EXITED", admission=ready, exit_elapsed_seconds=exits), sort_keys=True), flush=True)
    finally:
        for fd in handles.values():
            os.close(fd)


if __name__ == "__main__":
    observe(json.loads(sys.argv[1]))


def test_vmm_binding_refuses_reused_pid():
    import pytest
    instance = dict(launch=dict(pid=12, start_time_ticks=34, host_boot_id="boot"))
    vm = dict(pid=12, process_start_time=35)
    with pytest.raises(AssertionError):
        _bind_vmm(instance, vm, "boot")
    vm["process_start_time"] = 34
    assert _bind_vmm(instance, vm, "boot") == (12, 34)
    with pytest.raises(AssertionError):
        _bind_vmm(instance, vm, "another-boot")


def test_exit_wait_requires_both_handles_without_ordering():
    class Poll:
        def __init__(self): self.pending = []
        def register(self, fd, flags): self.pending.insert(0, fd)
        def unregister(self, fd): self.pending.remove(fd)
        def poll(self, timeout): return [(self.pending[0], select.POLLIN)]
    assert set(_wait_exits(dict(vmm=1, worker=2), 1, poll_factory=Poll)) == {"vmm", "worker"}


def test_exit_wait_rejects_timeout_and_invalid_descriptor():
    import pytest
    class Poll:
        def register(self, fd, flags): pass
        def poll(self, timeout): return [(1, select.POLLNVAL)]
    with pytest.raises(AssertionError, match="invalid pidfd"):
        _wait_exits(dict(vmm=1), 1, poll_factory=Poll)
    with pytest.raises(AssertionError, match="deadline"):
        _wait_exits(dict(vmm=1), 0, poll_factory=Poll)


def test_vmm_observation_checks_live_start_credentials_and_executable(tmp_path):
    import pytest
    proc = tmp_path / "42"
    proc.mkdir(); (proc / "ns").mkdir()
    (proc / "ns/mnt").symlink_to(tmp_path, target_is_directory=True)
    (proc / "root").symlink_to(tmp_path, target_is_directory=True)
    (proc / "exe").write_bytes(b"fixture executable")
    fields = ["S"] + ["0"] * 18 + ["100"]
    (proc / "stat").write_text("42 (vmm fixture) " + " ".join(fields))
    (proc / "status").write_text("Uid:\t65000\t65000\t65000\t65000\nGid:\t65001\t65001\t65001\t65001\n")
    row = (proc / "exe").stat(); directory = tmp_path.stat()
    executable = dict(device=row.st_dev, inode=row.st_ino, size_bytes=row.st_size, mode=row.st_mode,
                      uid=row.st_uid, gid=row.st_gid, links=row.st_nlink)
    instance = dict(launch=dict(pid=42, start_time_ticks=100, execution_identity=dict(uid=65000, gid=65001)),
                    active=dict(executable=executable, mount_namespace=dict(device=directory.st_dev, inode=directory.st_ino),
                                jail_root=dict(device=directory.st_dev, inode=directory.st_ino)))
    class Poll:
        def register(self, fd, flags): assert fd == 91
        def poll(self, timeout): return []
    _observe_vmm(instance, 91, proc_root=tmp_path, poll_factory=Poll)
    instance["launch"]["start_time_ticks"] = 101
    with pytest.raises(AssertionError):
        _observe_vmm(instance, 91, proc_root=tmp_path, poll_factory=Poll)
    instance["launch"]["start_time_ticks"] = 100
    instance["active"]["executable"]["inode"] += 1
    with pytest.raises(AssertionError):
        _observe_vmm(instance, 91, proc_root=tmp_path, poll_factory=Poll)
    instance["active"]["executable"]["inode"] -= 1
    instance["launch"]["execution_identity"]["gid"] = 65000
    with pytest.raises(AssertionError):
        _observe_vmm(instance, 91, proc_root=tmp_path, poll_factory=Poll)
