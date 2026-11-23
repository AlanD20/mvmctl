"""Durable, ordinary-user execution of the two fixed host-reboot pytest phases."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys

LIMIT = 128 * 1024


class _PublicationPending(Exception):
    """The exclusive hardlink handoff has not reached its single-link state."""


def _read(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        row = os.fstat(stream.fileno())
        if row.st_nlink == 2:
            raise _PublicationPending
        if (not stat.S_ISREG(row.st_mode) or row.st_uid != os.getuid() or row.st_nlink != 1
                or stat.S_IMODE(row.st_mode) != 0o600 or row.st_size > LIMIT):
            raise RuntimeError('invalid host-reboot phase receipt')
        raw = stream.read(LIMIT + 1)
    return json.loads(raw)


def _write(path, value):
    raw = (json.dumps(value, sort_keys=True) + '\n').encode()
    if len(raw) > LIMIT:
        raise RuntimeError('host-reboot phase receipt exceeds bound')
    pending = path.with_name(path.name + '.writing')
    fd = os.open(pending, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())
    os.link(pending, path)
    pending.unlink()


def _identity(pid):
    fields = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
    return dict(pid=pid, start_ticks=int(fields[19]))


def _context(spec):
    if Path('/proc/sys/kernel/random/boot_id').read_text().strip() != spec['boot_id']:
        raise RuntimeError('host-reboot phase boot changed')
    if os.getuid() != spec['uid'] or os.geteuid() != spec['uid'] or os.getgid() != spec['gid']:
        raise RuntimeError('host-reboot phase operator changed')
    for path, digest in spec['source'].items():
        if hashlib.sha256(Path(path).read_bytes()).hexdigest() != digest:
            raise RuntimeError('host-reboot phase source changed')
    for path, expected in spec['namespace'].items():
        row = Path(path).lstat()
        actual = [row.st_dev, row.st_ino, row.st_uid, row.st_gid, stat.S_IMODE(row.st_mode)]
        if not stat.S_ISDIR(row.st_mode) or Path(path).resolve() != Path(path) or actual != expected:
            raise RuntimeError('host-reboot phase namespace changed')


def _paths(task, phase):
    if phase not in ('pre', 'post'):
        raise RuntimeError('invalid host-reboot phase')
    row = task.lstat()
    if task.resolve() != task or not stat.S_ISDIR(row.st_mode) or row.st_uid != os.getuid() or stat.S_IMODE(row.st_mode) != 0o700:
        raise RuntimeError('invalid host-reboot task')
    return {key: task / f'{phase}.{key}' for key in ('input.json', 'start.json', 'complete.json', 'stdout', 'stderr')}


def launch(task, phase, spec):
    """Launch once; an existing input is an error, never permission to adopt/restart."""
    paths = _paths(task, phase)
    _context(spec)
    _write(paths['input.json'], spec)
    descriptors = []
    try:
        for key in ('stdout', 'stderr'):
            descriptors.append(os.open(paths[key], os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600))
        child = subprocess.Popen(
            [sys.executable, '-I', '-S', '-B', str(Path(__file__).resolve()), 'run', str(task), phase],
            stdin=subprocess.DEVNULL, stdout=descriptors[0], stderr=descriptors[1], start_new_session=True,
        )
        return _identity(child.pid)
    finally:
        for fd in descriptors:
            os.close(fd)


def supervise(task, phase):
    paths = _paths(task, phase)
    spec = _read(paths['input.json'])
    _context(spec)
    supervisor = _identity(os.getpid())
    child = subprocess.Popen(spec['argv'], cwd=spec['cwd'], env=spec['env'], stdin=subprocess.DEVNULL)
    start = dict(schema_version=1, phase=phase, nonce=spec['nonce'], supervisor=supervisor,
                 pytest=_identity(child.pid), boot_id=spec['boot_id'])
    _write(paths['start.json'], start)
    code = child.wait()
    # Publish atomically only after the actual child was reaped and output fsynced.
    for stream in (sys.stdout, sys.stderr):
        stream.flush()
        os.fsync(stream.fileno())
    temporary = task / f'{phase}.complete.pending.json'
    _write(temporary, dict(start=start, returncode=code))
    os.link(temporary, paths['complete.json'])
    temporary.unlink()
    directory = os.open(task, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def observe(task, phase, nonce):
    try:
        return _observe_ready(task, phase, nonce)
    except _PublicationPending:
        # Never accept a partially published receipt; retry within the caller's budget.
        return dict(state='publishing')


def _observe_ready(task, phase, nonce):
    paths = _paths(task, phase)
    spec = _read(paths['input.json'])
    if spec['nonce'] != nonce:
        raise RuntimeError('host-reboot phase launch identity changed')
    _context(spec)
    if not paths['start.json'].exists():
        return dict(state='starting')
    start = _read(paths['start.json'])
    if (set(start) != {'schema_version', 'phase', 'nonce', 'supervisor', 'pytest', 'boot_id'}
            or start['schema_version'] != 1 or start['phase'] != phase or start['nonce'] != nonce
            or start['boot_id'] != spec['boot_id']):
        raise RuntimeError('host-reboot phase process receipt changed')
    for key in ('supervisor', 'pytest'):
        identity = start[key]
        if set(identity) != {'pid', 'start_ticks'} or any(type(n) is not int or n <= 0 for n in identity.values()):
            raise RuntimeError('invalid host-reboot phase process identity')
    # Completion survives normal process exit/PID reuse: validate it before /proc.
    if paths['complete.json'].exists():
        complete = _read(paths['complete.json'])
        if set(complete) != {'start', 'returncode'} or complete['start'] != start or type(complete['returncode']) is not int:
            raise RuntimeError('invalid host-reboot phase completion')
        return dict(state='complete', **complete)
    for key in ('supervisor', 'pytest'):
        try:
            current = _identity(start[key]['pid'])
        except FileNotFoundError:
            # Recheck atomic completion across the exit/observation race.
            if paths['complete.json'].exists():
                return observe(task, phase, nonce)
            if key == 'pytest':
                return dict(state='finishing', start=start)
            raise RuntimeError('host-reboot phase process disappeared without completion')
        if current != start[key]:
            raise RuntimeError('host-reboot phase process identity changed')
    return dict(state='running', start=start)


def main():
    mode, task, phase = sys.argv[1:4]
    task = Path(task)
    if mode == 'launch':
        spec = json.loads(sys.argv[4])
        # The command interface only launches the fixed phase test; never arbitrary commands.
        expected = ['python3', '-B', '-m', 'pytest']
        if spec['argv'][:4] != expected or spec['argv'][-1] != 'tests/system/host/test_host_reboot.py::test_host_reboot_' + phase:
            raise RuntimeError('not the fixed host-reboot pytest command')
        print(json.dumps(launch(task, phase, spec)), flush=True)
    elif mode == 'run':
        supervise(task, phase)
    elif mode == 'observe':
        try:
            result = observe(task, phase, sys.argv[4])
        except (OSError, RuntimeError, ValueError, KeyError, TypeError) as exc:
            result = dict(state='error', error=str(exc))
        print(json.dumps(result), flush=True)
    else:
        raise RuntimeError('invalid host-reboot phase operation')


if __name__ == '__main__':
    main()
