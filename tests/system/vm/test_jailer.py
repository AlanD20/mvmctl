"""Canonical Firecracker Jailer launcher system tests — Tier 2 runner VM."""

from __future__ import annotations

import errno
import hashlib
import json
import os
import platform
import re
import select
import shlex
import socket
import subprocess
import tarfile
import time
import uuid
from contextvars import ContextVar
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Generator

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _parse_vm_list,
    _run_mvm as _installed_mvm,
    _unique_subnet,
)
from tests.system.vm.test_vm_lifecycle import _kill_exact_test_firecracker
from tests.system.bin.test_bin import _file_identity, _require_bin_root_projection

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
    pytest.mark.tier2,
    pytest.mark.needs_kvm,
    pytest.mark.slow,
]


@dataclass(frozen=True)
class _JailerReceipt:
    domain: str
    identifier: str
    name: str
    identity: tuple[tuple[str, Any], ...]


@dataclass(frozen=True)
class _JailerSnapshotIntent:
    identifier: str
    name: str
    source_id: str
    path: str


class _JailerResources:
    """Own only admitted test creations; names never authorize removal."""

    def __init__(self, runner_vm: str) -> None:
        self.runner_vm = runner_vm
        self.baseline = {domain: self.rows(domain) for domain in ("vm", "network", "volume", "snapshot")}
        self.attempts: dict[tuple[str, str], str | None] = {}
        self.pending: set[tuple[str, str]] = set()
        self.receipts: dict[tuple[str, str], _JailerReceipt] = {}
        self.snapshot_intents: dict[str, _JailerSnapshotIntent] = {}
        self.binaries = self.rows("bin")
        self.kernels = self.rows("kernel")
        self.images = self.rows("image")
        self.settings = _json_success_data(
            _installed_mvm(runner_vm, "config", "get", "settings", "--json").stdout,
            dict,
        )
        guestfs = _json_success_data(
            _installed_mvm(runner_vm, "config", "get", "settings", "guestfs_enabled", "--json").stdout,
            bool,
        )
        assert guestfs is False, "preseed the runner's LoopMount setting"
        assert any(row["is_default"] and row["is_present"] for row in self.kernels), "preseed the default kernel"
        assert any(row["type"] == "alpine" and row["version"] == "3.23" and row["is_present"] for row in self.images), (
            "preseed alpine:3.23"
        )
        defaults = {row["type"]: row for row in self.binaries if row["is_default"]}
        assert set(defaults) == {"firecracker", "jailer"}
        assert all(row["version"] == "1.17.0" and row["is_present"] for row in defaults.values())

    def rows(self, domain: str) -> list[dict[str, Any]]:
        return _json_success_data(_installed_mvm(self.runner_vm, domain, "ls", "--json").stdout, list)

    def attempt(self, domain: str, name: str, source_id: str | None = None) -> None:
        assert all(row["name"] != name for row in self.baseline[domain]), f"pre-existing {domain}: {name}"
        assert all(row["name"] != name for row in self.rows(domain)), f"occupied {domain}: {name}"
        self.attempts[domain, name] = source_id
        self.pending.add((domain, name))

    def retain(self, domain: str, row: dict[str, Any]) -> None:
        identifier, name = row["id"], row["name"]
        assert re.fullmatch(r"[0-9a-f]{32}" if domain == "vm" else r"[0-9a-f]{64}", identifier), row
        assert all(item["id"] != identifier for item in self.baseline[domain]), row
        if domain == "volume" and (domain, name) not in self.attempts:
            assert any(
                receipt.domain == "vm" and receipt.identifier == row.get("vm_id") for receipt in self.receipts.values()
            ), "unowned restored volume"
        else:
            assert (domain, name) in self.attempts, (domain, row)
        fields = {
            "vm": (
                "name",
                "kernel_id",
                "image_id",
                "network_id",
                "binary_id",
                "jailer_binary_id",
            ),
            "network": ("name", "subnet"),
            "volume": ("name", "path", "format"),
            "snapshot": (
                "name",
                "source_vm_id",
                "memory_file",
                "state_file",
                "rootfs_file",
            ),
        }[domain]
        identity = tuple((field, row[field]) for field in fields)
        if domain == "snapshot":
            assert row["source_vm_id"] == self.attempts[domain, name]
            intent = self.snapshot_intents.get(identifier)
            assert intent is not None and (intent.name, intent.source_id) == (
                name,
                row["source_vm_id"],
            )
            assert str(Path(row["memory_file"]).parent) == intent.path
        receipt = _JailerReceipt(domain, identifier, name, identity)
        previous = self.receipts.get((domain, identifier))
        assert (
            previous is not None
            or (domain, name) in self.pending
            or (domain == "volume" and (domain, name) not in self.attempts)
        ), "refuse a replacement outside the creation attempt"
        assert previous is None or previous == receipt, "creation identity changed"
        self.receipts[domain, identifier] = receipt
        self.pending.discard((domain, name))

    def reconcile(self) -> None:
        """Retain partial projections before assertions and before removing any VM."""
        for domain in ("vm", "network", "snapshot"):
            for row in self.rows(domain):
                if (domain, row["name"]) in self.attempts:
                    self.retain(domain, row)
        for row in self.rows("volume"):
            if ("volume", row["name"]) in self.attempts or any(
                receipt.domain == "vm" and receipt.identifier == row.get("vm_id") for receipt in self.receipts.values()
            ):
                self.retain("volume", row)

    def checked_id(self, domain: str, identifier: str) -> str:
        rows = self.rows(domain)
        selected = [row for row in rows if row["id"] == identifier or row["name"] == identifier]
        assert len(selected) == 1, (domain, identifier, selected)
        row = selected[0]
        receipt = self.receipts.get((domain, row["id"]))
        assert receipt is not None, f"refuse removal without {domain} creation receipt"
        assert row["name"] == receipt.name
        assert tuple((field, row[field]) for field, _ in receipt.identity) == receipt.identity
        return receipt.identifier

    def retain_snapshot_intent(self, stderr: str, name: str, source_id: str, directory: str) -> None:
        identifiers = [
            line.removeprefix("Snapshot request ID: ")
            for line in stderr.splitlines()
            if line.startswith("Snapshot request ID: ")
        ]
        assert len(identifiers) <= 1, stderr
        for identifier in identifiers:
            assert re.fullmatch(r"[0-9a-f]{64}", identifier), stderr
            assert all(row["id"] != identifier for row in self.baseline["snapshot"])
            intent = _JailerSnapshotIntent(identifier, name, source_id, str(Path(directory) / identifier))
            previous = self.snapshot_intents.get(identifier)
            assert previous is None or previous == intent
            self.snapshot_intents[identifier] = intent

    def path_present(self, path: str) -> bool:
        result = _guest_run(self.runner_vm, f"sudo -n test -e {shlex.quote(path)}", check=False)
        assert result.returncode in (0, 1) and not result.stderr, (
            result.stdout,
            result.stderr,
        )
        return result.returncode == 0

    def cleanup(self, *, check_assets: bool = True) -> None:
        errors: list[str] = []

        def checked(label: str, operation: Any) -> None:
            try:
                operation()
            except Exception as error:
                errors.append(f"{label}: {error}")

        checked("retain partial creations", self.reconcile)
        for domain in ("vm", "volume", "snapshot", "network"):
            for receipt in tuple(self.receipts.values()):
                if receipt.domain != domain:
                    continue

                def remove(receipt: _JailerReceipt = receipt) -> None:
                    rows = self.rows(receipt.domain)
                    assert all(
                        row["name"] != receipt.name
                        or row["id"] == receipt.identifier
                        or (receipt.domain, row["id"]) in self.receipts
                        for row in rows
                    )
                    if any(row["id"] == receipt.identifier for row in rows):
                        exact = self.checked_id(receipt.domain, receipt.identifier)
                        result = _installed_mvm(
                            self.runner_vm,
                            receipt.domain,
                            "rm",
                            exact,
                            "--force",
                            check=False,
                            timeout=120,
                        )
                        assert result.returncode == 0, (result.stdout, result.stderr)
                    assert all(row["id"] != receipt.identifier for row in self.rows(receipt.domain))
                    if receipt.domain == "vm":
                        assert not self.path_present(_jail_root(receipt.identifier))
                    elif receipt.domain == "volume":
                        assert not self.path_present(dict(receipt.identity)["path"])
                    elif receipt.domain == "snapshot":
                        assert not self.path_present(str(Path(dict(receipt.identity)["memory_file"]).parent))

                checked(f"remove {domain} {receipt.identifier}", remove)
            if domain == "snapshot":
                for intent in self.snapshot_intents.values():

                    def remove_intent(intent: _JailerSnapshotIntent = intent) -> None:
                        if self.path_present(intent.path):
                            result = _installed_mvm(
                                self.runner_vm,
                                "snapshot",
                                "rm",
                                intent.identifier,
                                "--force",
                                check=False,
                            )
                            assert result.returncode == 0, (
                                result.stdout,
                                result.stderr,
                            )
                        assert not self.path_present(intent.path)

                    checked(
                        f"remove partial snapshot request {intent.identifier}",
                        remove_intent,
                    )
            checked(
                f"restore {domain} inventory",
                lambda domain=domain: self.require_baseline(domain),
            )
        if check_assets:
            for domain, original in (
                ("bin", self.binaries),
                ("kernel", self.kernels),
                ("image", self.images),
            ):
                checked(
                    f"preserve {domain} assets/defaults",
                    lambda domain=domain, original=original: self.require_assets(domain, original),
                )
        checked("preserve settings", lambda: self.require_settings())
        assert not errors, "Jailer teardown failures:\n" + "\n".join(errors)

    def require_baseline(self, domain: str) -> None:
        fields = {
            "vm": ("id", "name", "status", "pid", "process_start_time", "network_id", "ipv4", "mac"),
            "network": ("id", "name", "subnet", "is_default", "is_present"),
            "volume": ("id", "name", "path", "format", "status", "vm_id", "size_bytes", "is_shareable", "is_read_only"),
            "snapshot": ("id", "name", "source_vm_id", "memory_file", "state_file", "rootfs_file"),
        }[domain]
        current = {tuple(row.get(field) for field in fields) for row in self.rows(domain)}
        original = {tuple(row.get(field) for field in fields) for row in self.baseline[domain]}
        assert current == original, (domain, current, original)

    def require_assets(self, domain: str, original: list[dict[str, Any]]) -> None:
        def identity(rows: list[dict[str, Any]]) -> set[tuple[Any, ...]]:
            return {
                (
                    row["id"],
                    row.get("path"),
                    row.get("is_present"),
                    row.get("is_default"),
                )
                for row in rows
            }

        assert identity(self.rows(domain)) == identity(original), domain

    def require_settings(self) -> None:
        current = _json_success_data(
            _installed_mvm(self.runner_vm, "config", "get", "settings", "--json").stdout,
            dict,
        )
        assert current == self.settings


_resources: ContextVar[_JailerResources] = ContextVar("jailer_resources")


_JAILER_RELEASE_RECEIVER = r"""
import hashlib, json, os, re, stat, sys

fds, parents = [], []
directory_flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
file_flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC

def identity(info, directory=False):
    value = [info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode]
    return value if directory else value + [info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns]

def unique_object(pairs):
    value = {}
    for key, item in pairs:
        assert key not in value, "duplicate trusted release field"
        value[key] = item
    return value

def directory(parent, name, managed=True, mode=0o700):
    fd = os.open(name, directory_flags, dir_fd=parent)
    fds.append(fd)
    info = os.fstat(fd)
    assert info.st_uid == info.st_gid == 0 and stat.S_ISDIR(info.st_mode), name
    assert stat.S_IMODE(info.st_mode) == mode if managed else not info.st_mode & 0o022, name
    parents.append((parent, name, fd, identity(info, True)))
    return fd

def verify_parents():
    for parent, name, fd, expected in parents:
        assert identity(os.fstat(fd), True) == expected, name
        assert identity(os.stat(name, dir_fd=parent, follow_symlinks=False), True) == expected, name

def read_file(fd, maximum):
    expected = identity(os.fstat(fd))
    assert expected[6] <= maximum
    os.lseek(fd, 0, os.SEEK_SET)
    data = bytearray()
    while len(data) <= maximum:
        part = os.read(fd, min(65536, maximum + 1 - len(data)))
        if not part:
            break
        data.extend(part)
    assert len(data) <= maximum and identity(os.fstat(fd)) == expected
    return bytes(data)

def absent(version):
    verify_parents()
    try:
        os.stat(version, dir_fd=architecture, follow_symlinks=False)
    except FileNotFoundError:
        return
    raise AssertionError("trusted release slot exists: " + version)

def pair(version):
    slot = directory(architecture, version)
    assert set(os.listdir(slot)) == {"manifest.json", "firecracker", "jailer"}
    leaves = {}
    for name in ("manifest.json", "firecracker", "jailer"):
        fd = os.open(name, file_flags, dir_fd=slot)
        fds.append(fd)
        info = os.fstat(fd)
        assert info.st_uid == info.st_gid == 0 and stat.S_ISREG(info.st_mode) and info.st_nlink == 1
        assert stat.S_IMODE(info.st_mode) == (0o600 if name == "manifest.json" else 0o755)
        leaves[name] = (fd, identity(info))
    return slot, leaves

def receipt(retained, version):
    slot, leaves = retained
    verify_parents()
    assert set(os.listdir(slot)) == set(leaves)
    contents, identities = {}, {}
    for name, (fd, expected) in leaves.items():
        assert identity(os.fstat(fd)) == expected
        assert identity(os.stat(name, dir_fd=slot, follow_symlinks=False)) == expected
        contents[name] = read_file(fd, 4096 if name == "manifest.json" else 64 * 1024 * 1024)
        identities[name] = expected
    manifest = json.loads(contents["manifest.json"], object_pairs_hook=unique_object)
    assert set(manifest) == {"schema_version", "release", "archive_sha256", "firecracker", "jailer"}
    assert type(manifest["schema_version"]) is int and manifest["schema_version"] == 1
    assert manifest["release"] == {"version": version, "architecture": "x86_64"}
    assert re.fullmatch(r"[0-9a-f]{64}", manifest["archive_sha256"])
    for name in ("firecracker", "jailer"):
        item = manifest[name]
        assert set(item) == {"sha256", "size_bytes"} and type(item["size_bytes"]) is int
        assert 120 <= item["size_bytes"] <= 64 * 1024 * 1024
        assert item["size_bytes"] == len(contents[name])
        assert re.fullmatch(r"[0-9a-f]{64}", item["sha256"])
        assert item["sha256"] == hashlib.sha256(contents[name]).hexdigest()
    return {"manifest": manifest, "directory": identity(os.fstat(slot), True), "files": identities}

def forget(retained):
    parents[:] = [entry for entry in parents if entry[2] != retained[0]]

try:
    root = directory(None, "/", False)
    var = directory(root, "var", False)
    lib = directory(var, "lib", False)
    state = directory(lib, "mvmctl", mode=0o711)
    binaries = directory(state, "binaries")
    architecture = directory(binaries, "x86_64")
    source = pair("1.17.0")
    source_baseline = receipt(source, "1.17.0")
    try:
        os.stat("1.16.0", dir_fd=architecture, follow_symlinks=False)
    except FileNotFoundError:
        older = None
        absent("1.16.0")
    else:
        older = pair("1.16.0")
    older_baseline = receipt(older, "1.16.0") if older else None
    print(json.dumps({"source": source_baseline, "older": older_baseline}), flush=True)
    for line in sys.stdin:
        command = line.strip()
        if command == "close":
            break
        if command == "older-admit":
            assert older is None
            older = pair("1.16.0")
            value = receipt(older, "1.16.0")
        elif command == "older-observe":
            if older is None:
                try:
                    os.stat("1.16.0", dir_fd=architecture, follow_symlinks=False)
                except FileNotFoundError:
                    absent("1.16.0")
                    value = {"absent": True}
                else:
                    older = pair("1.16.0")
                    value = receipt(older, "1.16.0")
            else:
                value = receipt(older, "1.16.0")
        elif command == "older-verify":
            assert older is not None
            value = receipt(older, "1.16.0")
        elif command == "older-removed":
            assert older_baseline is None and older is not None
            forget(older)
            older = None
            absent("1.16.0")
            value = {"absent": True}
        elif command == "older-absent":
            assert older is None
            absent("1.16.0")
            value = {"absent": True}
        elif command == "source-verify":
            assert source is not None
            value = receipt(source, "1.17.0")
        elif command == "source-removed":
            assert source is not None
            forget(source)
            source = None
            absent("1.17.0")
            value = {"absent": True}
        elif command == "source-observe":
            try:
                os.stat("1.17.0", dir_fd=architecture, follow_symlinks=False)
            except FileNotFoundError:
                if source is not None:
                    forget(source)
                    source = None
                absent("1.17.0")
                value = {"absent": True}
            else:
                if source is None:
                    source = pair("1.17.0")
                value = receipt(source, "1.17.0")
                assert value["manifest"] == source_baseline["manifest"]
        elif command == "source-readmit":
            assert source is None
            source = pair("1.17.0")
            value = receipt(source, "1.17.0")
            assert value["manifest"] == source_baseline["manifest"]
        else:
            raise AssertionError("unknown Jailer release observation")
        if source is not None:
            assert receipt(source, "1.17.0")["manifest"] == source_baseline["manifest"]
        if older_baseline is not None:
            assert receipt(older, "1.16.0") == older_baseline
        print(json.dumps(value), flush=True)
except BaseException as error:
    print(json.dumps({"error": str(error)}), flush=True)
    raise
finally:
    for fd in reversed(fds):
        os.close(fd)
"""


class _JailerReleaseOwnership:
    """Pin the two fixed release slots through the deliberate release-failure tests."""

    def __init__(self) -> None:
        assert platform.machine() == "x86_64"
        self.process = subprocess.Popen(
            [
                "sudo",
                "-n",
                "/usr/bin/python3",
                "-I",
                "-u",
                "-c",
                _JAILER_RELEASE_RECEIVER,
            ],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )

    def read(self) -> dict[str, Any]:
        assert select.select([self.process.stdout], [], [], 30)[0], "release ownership observation timed out"
        line = self.process.stdout.readline(16385)
        assert line and len(line) <= 16384, "missing or oversized release receipt"
        receipt = json.loads(line)
        assert "error" not in receipt, receipt
        return receipt

    def require(self, phase: str) -> dict[str, Any]:
        self.process.stdin.write(phase + "\n")
        self.process.stdin.flush()
        return self.read()

    def close(self) -> None:
        if self.process.poll() is None:
            try:
                self.process.stdin.write("close\n")
                self.process.stdin.flush()
            except BrokenPipeError:
                pass
        try:
            self.process.stdin.close()
        except BrokenPipeError:
            pass
        self.process.wait(timeout=30)
        error = self.process.stderr.read(16385)
        self.process.stdout.close()
        self.process.stderr.close()
        assert self.process.returncode == 0, error


_SNAPSHOT_CAPTURE_FAULT_OBSERVER = r"""
import grp
import hashlib
import json
import os
import pwd
import re
import resource
import select
import signal
import stat
import sys

pid, ticks, vm_id, owner = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3], int(sys.argv[4])
cache_path = sys.argv[5]
assert os.geteuid() == 0 and int(os.environ["SUDO_UID"]) == owner and owner > 0
assert re.fullmatch(r"[0-9a-f]{32}", vm_id) and pid > 0 and ticks > 0
fds, pidfd, worker, mutation_possible = [], -1, None, False

def keep(fd):
    fds.append(fd)
    return fd

def unique_object(pairs):
    result = {}
    for key, value in pairs:
        assert key not in result, "duplicate authority field"
        result[key] = value
    return result

def bounded(fd, maximum):
    data = b""
    while len(data) <= maximum:
        part = os.read(fd, min(65536, maximum + 1 - len(data)))
        if not part:
            return data
        data += part
    raise AssertionError("oversized observation")

def read_at(parent, name, maximum):
    fd = os.open(name, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
    try:
        return os.fstat(fd), bounded(fd, maximum)
    finally:
        os.close(fd)

def directory(parent, name, uid=0, mode=None):
    fd = keep(os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=parent))
    info = os.fstat(fd)
    assert info.st_uid == uid and not info.st_mode & 0o022, (name, info)
    if mode is not None:
        assert stat.S_IMODE(info.st_mode) == mode
    return fd

def identity(info):
    return info.st_dev, info.st_ino

def exited():
    poller = select.poll()
    poller.register(pidfd, select.POLLIN)
    events = poller.poll(0)
    assert not events or not events[0][1] & (select.POLLERR | select.POLLNVAL)
    return bool(events)

def require_exit():
    poller = select.poll()
    poller.register(pidfd, select.POLLIN)
    events = poller.poll(10_000)
    assert len(events) == 1 and events[0][0] == pidfd, "original Firecracker did not exit"
    assert not events[0][1] & (select.POLLERR | select.POLLNVAL)
    assert events[0][1] & (select.POLLIN | select.POLLHUP)

def status(parent):
    _, raw = read_at(parent, "status", 65536)
    return dict(line.split(":", 1) for line in raw.decode("ascii").splitlines() if ":" in line)

def require_process():
    assert not exited(), "admitted Firecracker has exited"
    _, raw_stat = read_at(proc_fd, "stat", 16384)
    fields = raw_stat.decode("ascii").rpartition(") ")[2].split()
    assert int(fields[19]) == ticks and fields[0] not in ("Z", "X")
    values = status(proc_fd)
    assert [int(value) for value in values["Uid"].split()] == [uid] * 4
    assert [int(value) for value in values["Gid"].split()] == [gid] * 4
    assert values["Groups"].split() == []
    assert active["user_ids"] == dict.fromkeys(("real", "effective", "saved", "filesystem"), uid)
    assert active["group_ids"] == dict.fromkeys(("real", "effective", "saved", "filesystem"), gid)
    assert active["supplementary_groups"] == []
    for name, section in (("root", "jail_root"), ("ns/mnt", "mount_namespace")):
        info = os.stat(name, dir_fd=proc_fd)
        assert identity(info) == (active[section]["device"], active[section]["inode"])
    _, cgroup = read_at(proc_fd, "cgroup", 16384)
    assert cgroup.decode("ascii") == f"0::/mvmctl/{vm_id}\n"
    exe_fd = os.open("exe", os.O_RDONLY | os.O_CLOEXEC, dir_fd=proc_fd)
    try:
        info = os.fstat(exe_fd)
        observed = {"device": info.st_dev, "inode": info.st_ino, "size_bytes": info.st_size,
                    "mode": info.st_mode, "uid": info.st_uid, "gid": info.st_gid, "links": info.st_nlink}
        assert {key: active["executable"][key] for key in observed} == observed
        assert active["executable"]["manifest_sha256"] == digest
        assert hashlib.sha256(bounded(exe_fd, 64 * 1024 * 1024)).hexdigest() == digest
    finally:
        os.close(exe_fd)
    current_info, current_raw = read_at(owner_dir, vm_id + ".json", 16384)
    assert identity(current_info) == identity(record_info) and current_raw == raw_record
    with open("/proc/sys/kernel/random/boot_id", encoding="ascii") as boot:
        assert boot.read().strip() == launch["host_boot_id"]
    assert not exited()

def require_account_processes(expected):
    users = [user for user in pwd.getpwall() if user.pw_uid == uid]
    groups = [group for group in grp.getgrall() if group.gr_gid == gid]
    assert len(users) == len(groups) == 1
    assert users[0].pw_gid == gid and users[0].pw_name == groups[0].gr_name
    assert users[0].pw_shell in ("/usr/sbin/nologin", "/sbin/nologin")
    observed = set()
    for name in os.listdir("/proc"):
        if not name.isdecimal():
            continue
        try:
            process = os.open("/proc/" + name, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW)
        except FileNotFoundError:
            continue
        try:
            try:
                values = status(process)
            except (FileNotFoundError, ProcessLookupError):
                continue
            if uid in map(int, values["Uid"].split()) or gid in map(int, values["Gid"].split()):
                observed.add(int(name))
        finally:
            os.close(process)
    assert observed == expected, ("isolated identity has another process", observed, expected)

def pipe_receipt(fd):
    assert select.select([fd], [], [], 10)[0], "file-limit worker timed out"
    raw = os.read(fd, 4097)
    assert raw and len(raw) <= 4096
    value = json.loads(raw, object_pairs_hook=unique_object)
    assert "error" not in value, value
    return value

def limit_worker(ready_fd, proceed_fd):
    try:
        # Keep only anonymous communication pipes. No authority directory, pidfd,
        # process-root descriptor or root observer stdio reaches the isolated worker.
        for name in os.listdir("/proc/self/fd"):
            fd = int(name)
            if fd not in (ready_fd, proceed_fd):
                try:
                    os.close(fd)
                except OSError as error:
                    assert error.errno == 9
        os.setgroups([])
        os.setresgid(gid, gid, gid)
        os.setresuid(uid, uid, uid)
        assert os.getresuid() == (uid, uid, uid) and os.getresgid() == (gid, gid, gid)
        assert os.getgroups() == []
        with open("/proc/self/status", encoding="ascii") as source:
            values = dict(line.split(":", 1) for line in source if ":" in line)
        assert [int(value) for value in values["Uid"].split()] == [uid] * 4
        assert [int(value) for value in values["Gid"].split()] == [gid] * 4
        assert all(int(values[name], 16) == 0 for name in ("CapEff", "CapPrm", "CapInh", "CapAmb"))
        os.write(ready_fd, json.dumps({"phase": "unprivileged", "pid": os.getpid()}).encode())
        assert select.select([proceed_fd], [], [], 10)[0] and os.read(proceed_fd, 1) == b"1"
        # prlimit is numeric-PID based. Matching isolated credentials, no caps,
        # and exclusive lifecycle ownership exclude effects on a recycled foreign PID.
        original = resource.prlimit(pid, resource.RLIMIT_FSIZE)
        assert all(value == resource.RLIM_INFINITY or value >= 256 * 1024 * 1024 for value in original)
        assert resource.prlimit(pid, resource.RLIMIT_FSIZE, (16 * 1024 * 1024, original[1])) == original
        applied = resource.prlimit(pid, resource.RLIMIT_FSIZE)
        assert applied == (16 * 1024 * 1024, original[1])
        os.write(ready_fd, json.dumps({"phase": "limited", "original": original, "applied": applied}).encode())
    except BaseException as error:
        os.write(ready_fd, json.dumps({"error": str(error)}).encode())
        os._exit(1)
    os._exit(0)

def observe_capture(snapshot_id):
    assert re.fullmatch(r"[0-9a-f]{64}", snapshot_id)
    require_exit()  # A live EFBIG outcome must not pass as the proposed producer crash.
    directory_fd = root
    assert cache_path.startswith("/") and ".." not in cache_path.split("/")
    for component in cache_path.split("/")[1:]:
        assert component
        directory_fd = keep(os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC,
                                    dir_fd=directory_fd))
        info = os.fstat(directory_fd)
        assert info.st_uid in (0, owner) and not info.st_mode & 0o022
    protected = directory(directory_fd, ".mvm-protected")
    snapshots = directory(protected, "snapshots", mode=0o700)
    entry = directory(snapshots, snapshot_id, mode=0o700)
    info, raw = read_at(entry, "capture-progress.json", 4096)
    assert info.st_uid == info.st_gid == 0 and info.st_mode == stat.S_IFREG | 0o600 and info.st_nlink == 1
    progress = json.loads(raw, object_pairs_hook=unique_object)
    assert progress == {"snapshot_id": snapshot_id, "source_vm_id": vm_id, "owner_uid": owner,
                        "launch_generation": generation, "stage": "capture_inflight",
                        "vm_state": "paused", "published": False}, progress
    assert "capture.json" not in os.listdir(entry)
    producer = directory(jail_root, "capture-" + snapshot_id, uid=uid, mode=0o700)
    assert os.fstat(producer).st_gid == gid
    assert set(os.listdir(producer)) == {"memory", "vmstate"}
    lengths = {}
    for name in ("memory", "vmstate"):
        leaf = os.open(name, os.O_PATH | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=producer)
        try:
            info = os.fstat(leaf)
            assert info.st_mode == stat.S_IFREG | 0o600 and info.st_nlink == 1
            assert (info.st_uid, info.st_gid) == (uid, gid)
            lengths[name] = info.st_size
        finally:
            os.close(leaf)
    assert 0 < lengths["vmstate"] <= 10_000_000 and 0 <= lengths["memory"] < 256 * 1024 * 1024
    return {"phase": "interrupted", "progress": progress, "lengths": lengths, "exited": True}

try:
    pidfd = keep(os.pidfd_open(pid))
    assert not exited()
    root = directory(None, "/")
    var = directory(root, "var")
    lib = directory(var, "lib")
    state = directory(lib, "mvmctl")
    instances = directory(state, "instances", mode=0o700)
    owner_dir = directory(instances, str(owner), mode=0o700)
    record_info, raw_record = read_at(owner_dir, vm_id + ".json", 16384)
    assert record_info.st_mode == stat.S_IFREG | 0o600 and record_info.st_uid == record_info.st_gid == 0
    assert record_info.st_nlink == 1
    record = json.loads(raw_record, object_pairs_hook=unique_object)
    assert set(record) == {"schema_version", "owner_uid", "vm_id", "network_placement", "lifecycle",
                           "launch_generation", "cleanup_generation", "launch", "active"}
    assert record["schema_version"] == 6 and record["owner_uid"] == owner and record["vm_id"] == vm_id
    assert record["network_placement"] == "networkless" and record["lifecycle"] == "active"
    generation = record["launch_generation"]
    assert type(generation) is int and generation > record["cleanup_generation"] >= 0
    launch, active = record["launch"], record["active"]
    assert launch["pid"] == pid and launch["start_time_ticks"] == ticks
    assert launch["network_placement"] == "networkless"
    uid, gid = launch["execution_identity"]["uid"], launch["execution_identity"]["gid"]
    assert type(uid) is int and uid not in (0, owner)
    assert type(gid) is int and gid not in (0, int(os.environ["SUDO_GID"]))
    assert launch["release"]["version"] == "1.17.0" and launch["release"]["architecture"] == "x86_64"
    digest = launch["release"]["firecracker_sha256"]
    assert re.fullmatch(r"[0-9a-f]{64}", digest)
    proc_fd = keep(os.open(f"/proc/{pid}", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW))
    require_process()
    jail_root = keep(os.open("root", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC, dir_fd=proc_fd))
    assert identity(os.fstat(jail_root)) == (active["jail_root"]["device"], active["jail_root"]["inode"])
    require_account_processes({pid})
    ready_read, ready_write = os.pipe2(os.O_CLOEXEC)
    proceed_read, proceed_write = os.pipe2(os.O_CLOEXEC)
    fds.extend((ready_read, ready_write, proceed_read, proceed_write))
    worker = os.fork()
    if worker == 0:
        limit_worker(ready_write, proceed_read)
    ready = pipe_receipt(ready_read)
    assert ready == {"phase": "unprivileged", "pid": worker}
    require_account_processes({pid, worker})
    require_process()
    mutation_possible = True
    os.write(proceed_write, b"1")
    receipt = pipe_receipt(ready_read)
    assert receipt["phase"] == "limited"
    waited, worker_status = os.waitpid(worker, 0)
    assert waited == worker and os.waitstatus_to_exitcode(worker_status) == 0
    worker = None
    require_account_processes({pid})
    require_process()
    print(json.dumps({**receipt, "phase": "armed", "vm_id": vm_id, "launch_generation": generation,
                      "pid": pid, "start_ticks": ticks}), flush=True)
    assert select.select([sys.stdin], [], [], 240)[0], "capture observer expired"
    command = sys.stdin.readline(128).strip()
    if command:
        assert command.startswith("observe ")
        print(json.dumps(observe_capture(command.removeprefix("observe "))), flush=True)
        assert select.select([sys.stdin], [], [], 30)[0], "capture observer release expired"
        assert sys.stdin.readline(128) == ""
except BaseException as error:
    print(json.dumps({"error": str(error)}), flush=True)
    raise
finally:
    # Never restore through a numeric PID. Only the retained original pidfd may
    # be killed; public CLI cleanup reconciles its exact VM after confirmed exit.
    if mutation_possible:
        if not exited():
            signal.pidfd_send_signal(pidfd, signal.SIGKILL)
        require_exit()
    if worker is not None:
        os.close(proceed_write)
        fds.remove(proceed_write)
        os.waitpid(worker, 0)
    for fd in reversed(fds):
        os.close(fd)
"""


class _SnapshotCaptureFault:
    """Own one file-limit fault and its original VMM pidfd until producer exit."""

    def __init__(self, source: dict[str, Any], cache: Path) -> None:
        qualification_host = os.environ.get("MVM_TEST_QUALIFICATION_HOST")
        assert (
            qualification_host and qualification_host == os.environ.get("MVM_TEST_VM")
        ), "administrator fault requires explicit exact qualification-host admission"
        assert "PYTEST_XDIST_WORKER" not in os.environ, "capture fault requires exclusive lifecycle ownership"
        self.process = subprocess.Popen(
            [
                "/usr/bin/sudo",
                "-n",
                "/usr/bin/python3",
                "-I",
                "-u",
                "-c",
                _SNAPSHOT_CAPTURE_FAULT_OBSERVER,
                str(source["pid"]),
                str(source["process_start_time"]),
                source["id"],
                str(os.getuid()),
                str(cache),
            ],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )

    def read(self) -> dict[str, Any]:
        assert select.select([self.process.stdout], [], [], 30)[0], "capture fault observer timed out"
        line = self.process.stdout.readline(4097)
        assert line and len(line) <= 4096, "missing or oversized capture fault receipt"
        receipt = json.loads(line)
        assert "error" not in receipt, receipt
        return receipt

    def observe(self, snapshot_id: str) -> dict[str, Any]:
        assert re.fullmatch(r"[0-9a-f]{64}", snapshot_id)
        self.process.stdin.write("observe " + snapshot_id + "\n")
        self.process.stdin.flush()
        return self.read()

    def close(self) -> None:
        if self.process.stdin.closed:
            return
        self.process.stdin.close()
        self.process.wait(timeout=30)
        diagnostic = self.process.stderr.read(16385)
        self.process.stdout.close()
        self.process.stderr.close()
        assert self.process.returncode == 0, diagnostic


def _mirrored_jailer_release(version: str) -> tuple[Path, tuple, dict[str, Any]]:
    assert version in ("1.16.0", "1.17.0") and platform.machine() == "x86_64"
    archive = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser() / f"firecracker-v{version}-x86_64.tgz"
    identity = _file_identity(archive)
    assert 0 < identity[-1] <= 128 * 1024 * 1024, archive
    fd = os.open(archive, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(fd, "rb") as source:
        assert _file_identity(archive) == identity
        info = os.fstat(source.fileno())
        assert (info.st_dev, info.st_ino) == identity[:2]
        manifest = {
            "schema_version": 1,
            "release": {"version": version, "architecture": "x86_64"},
            "archive_sha256": hashlib.file_digest(source, "sha256").hexdigest(),
        }
        source.seek(0)
        names = {f"release-v{version}-x86_64/{kind}-v{version}-x86_64": kind for kind in ("firecracker", "jailer")}
        with tarfile.open(fileobj=source, mode="r|gz") as bundle:
            for index, member in enumerate(bundle):
                assert index < 128
                if member.name in names:
                    kind = names[member.name]
                    assert kind not in manifest and member.isfile()
                    assert 120 <= member.size <= 64 * 1024 * 1024
                    with bundle.extractfile(member) as executable:
                        manifest[kind] = {
                            "sha256": hashlib.file_digest(executable, "sha256").hexdigest(),
                            "size_bytes": member.size,
                        }
        assert set(manifest) == {
            "schema_version",
            "release",
            "archive_sha256",
            "firecracker",
            "jailer",
        }
        assert _file_identity(archive) == identity
        assert os.fstat(source.fileno()).st_ctime_ns == identity[-2]
    return archive, identity, manifest


class _JailerReleases:
    """Restore only this journey's observed default and fixed-release mutations."""

    def __init__(self, runner_vm: str, *, remove_source: bool = False) -> None:
        self.runner_vm = runner_vm
        self.resources = _resources.get()
        self.defaults = {row["type"]: row["id"] for row in self.resources.binaries if row["is_default"]}
        self.older_archive = _mirrored_jailer_release("1.16.0")
        self.source_archive = _mirrored_jailer_release("1.17.0") if remove_source else None
        self.older_pull_attempted = False
        self.source_remove_attempted = False
        self.older_receipt: dict[str, Any] | None = None
        self.ownership = _JailerReleaseOwnership()
        try:
            self.baseline = self.ownership.read()
            self.source_pair = _require_bin_root_projection(self.resources.binaries, self.baseline["source"])
            self.older_owned = self.baseline["older"] is None
            older_rows = [row for row in self.resources.binaries if row["version"] == "1.16.0"]
            if self.older_owned:
                assert not older_rows, "preserve an existing unadmitted 1.16.0 projection"
            else:
                _require_bin_root_projection(self.resources.binaries, self.baseline["older"])
            if self.source_archive is not None:
                assert self.source_archive[2] == self.baseline["source"]["manifest"]
        except Exception:
            self.ownership.close()
            raise

    def pull_older(self) -> dict[str, Any]:
        if self.older_owned:
            self.ownership.require("older-absent")
            assert _file_identity(self.older_archive[0]) == self.older_archive[1]
            self.older_pull_attempted = True
            result = _installed_mvm(
                self.runner_vm,
                "bin",
                "pull",
                "1.16.0",
                "--json",
                check=False,
                timeout=300,
            )
            self.older_receipt = self.ownership.require("older-observe")
            assert "manifest" in self.older_receipt, (result.stdout, result.stderr)
            assert self.older_receipt["manifest"] == self.older_archive[2]
            assert _file_identity(self.older_archive[0]) == self.older_archive[1]
            assert result.returncode == 0, (result.stdout, result.stderr)
        else:
            self.older_receipt = self.ownership.require("older-verify")
        pair = _require_bin_root_projection(self.resources.rows("bin"), self.older_receipt)
        return next(row for row in pair if row["type"] == "firecracker")

    def remove_source(self) -> None:
        assert self.source_archive is not None
        assert self.ownership.require("source-verify") == self.baseline["source"]
        _require_bin_root_projection(self.resources.rows("bin"), self.baseline["source"])
        self.source_remove_attempted = True
        result = _installed_mvm(self.runner_vm, "bin", "rm", "1.17.0", "--force", check=False)
        observed = self.ownership.require("source-observe")
        assert result.returncode == 0 and observed == {"absent": True}, (
            result.stdout,
            result.stderr,
            observed,
        )

    def cleanup(self) -> None:
        errors: list[str] = []

        def checked(label: str, operation: Any) -> None:
            try:
                operation()
            except Exception as error:
                errors.append(f"{label}: {error}")

        checked(
            "remove owned VM/storage resources",
            lambda: self.resources.cleanup(check_assets=False),
        )

        def restore_source() -> None:
            if self.source_remove_attempted:
                observed = self.ownership.require("source-observe")
                if observed == {"absent": True}:
                    assert self.source_archive is not None
                    assert _file_identity(self.source_archive[0]) == self.source_archive[1]
                    result = _installed_mvm(
                        self.runner_vm,
                        "bin",
                        "pull",
                        "1.17.0",
                        "--json",
                        check=False,
                        timeout=300,
                    )
                    admitted = self.ownership.require("source-readmit")
                    assert admitted["manifest"] == self.baseline["source"]["manifest"]
                    assert _file_identity(self.source_archive[0]) == self.source_archive[1]
                    _require_bin_root_projection(self.resources.rows("bin"), admitted)
                    assert result.returncode == 0, (result.stdout, result.stderr)
            else:
                assert self.ownership.require("source-verify") == self.baseline["source"]

        checked("restore exact captured source release", restore_source)
        for kind, identifier in self.defaults.items():
            checked(
                f"restore {kind} default {identifier}",
                lambda identifier=identifier: _installed_mvm(self.runner_vm, "bin", "default", identifier),
            )

        def remove_older() -> None:
            assert self.ownership.require("source-verify")["manifest"] == self.baseline["source"]["manifest"]
            rows = self.resources.rows("bin")
            assert {row["type"]: row["id"] for row in rows if row["is_default"]} == self.defaults
            if self.older_owned and self.older_pull_attempted:
                observed = self.ownership.require("older-observe")
                if observed != {"absent": True}:
                    assert observed["manifest"] == self.older_archive[2]
                    if self.older_receipt is not None and "manifest" in self.older_receipt:
                        assert observed == self.older_receipt
                    self.older_receipt = observed
                    _require_bin_root_projection(rows, observed)
                    assert self.ownership.require("older-verify") == observed
                    result = _installed_mvm(self.runner_vm, "bin", "rm", "1.16.0", "--force", check=False)
                    assert result.returncode == 0, (result.stdout, result.stderr)
                    assert self.ownership.require("older-removed") == {"absent": True}
                else:
                    assert self.ownership.require("older-absent") == {"absent": True}
            elif not self.older_owned:
                assert self.ownership.require("older-verify") == self.baseline["older"]

        checked("remove only the owned older release", remove_older)
        checked("restore all inventories/assets/defaults", self.resources.cleanup)
        checked("close retained root release observations", self.ownership.close)
        assert not errors, "Jailer release rollback failures:\n" + "\n".join(errors)


@pytest.fixture(autouse=True)
def jailer_resources(runner_vm: str) -> Generator[_JailerResources, None, None]:
    resources = _JailerResources(runner_vm)
    token = _resources.set(resources)
    try:
        yield resources
    finally:
        try:
            resources.cleanup()
        finally:
            _resources.reset(token)


def _run_mvm(runner_vm: str, *args: str, check: bool = True, timeout: int = 60) -> subprocess.CompletedProcess[str]:
    """Retain this module's create/restore receipts before returning to its test body."""
    resources = _resources.get()
    command = list(args)
    domain, action = command[:2]
    created_domain: str | None = None
    snapshot_intent: tuple[str, str, str] | None = None
    if action == "create" and domain in ("vm", "network", "volume", "snapshot"):
        created_domain = domain
        if domain == "snapshot":
            source = _vm_entry(runner_vm, command[2])
            source_id = resources.checked_id("vm", source["id"])
            if "--name" not in command:
                command.extend(["--name", f"sys-jailer-snapshot-{uuid.uuid4().hex[:8]}"])
            resources.attempt(domain, command[command.index("--name") + 1], source_id)
            inspected = _json_success_data(
                _installed_mvm(runner_vm, "vm", "inspect", source_id, "--json").stdout,
                dict,
            )
            assert inspected["vm"]["id"] == source_id
            cache = Path(inspected["filesystem"]["vm_dir"]).parent.parent
            snapshot_intent = (
                command[command.index("--name") + 1],
                source_id,
                str(cache / ".mvm-protected" / "snapshots"),
            )
        else:
            resources.attempt(domain, command[2])
    elif domain == "snapshot" and action == "restore":
        created_domain = "vm"
        resources.attempt("vm", command[3])
    elif action == "rm" and domain in ("vm", "network", "volume", "snapshot"):
        resources.reconcile()
        command[2] = resources.checked_id(domain, command[2])
    if created_domain and "--json" not in command:
        command.append("--json")
    try:
        result = _installed_mvm(runner_vm, *command, check=False, timeout=timeout)
    except subprocess.TimeoutExpired as error:
        if snapshot_intent is not None and error.stderr:
            stderr = error.stderr.decode() if isinstance(error.stderr, bytes) else error.stderr
            resources.retain_snapshot_intent(stderr, *snapshot_intent)
        raise
    if snapshot_intent is not None:
        resources.retain_snapshot_intent(result.stderr, *snapshot_intent)
    if created_domain:
        if result.returncode == 0:
            rows = _json_success_data(result.stdout, list if created_domain == "vm" else dict)
            for row in rows if isinstance(rows, list) else [rows]:
                resources.retain(created_domain, row)
        elif result.stdout.strip():
            response = json.loads(result.stdout)
            assert response["schema_version"] == 1 and response["status"] == "error", response
            partial = response["data"]
            if partial is not None:
                assert isinstance(partial, list if created_domain == "vm" else dict), response
                for row in partial if isinstance(partial, list) else [partial]:
                    resources.retain(created_domain, row)
        resources.reconcile()
    if check:
        assert result.returncode == 0, (command, result.stdout, result.stderr)
    return result


def _vm_entry(runner_vm: str, vm_name: str) -> dict[str, Any]:
    result = _run_mvm(runner_vm, "vm", "ls", "--json")
    vms = _parse_vm_list(result.stdout)
    entry = next((vm for vm in vms if vm.get("name") == vm_name), None)
    assert entry is not None, f"VM {vm_name!r} is absent from vm ls --json"
    _resources.get().checked_id("vm", entry["id"])
    return entry


def _jail_root(vm_id: str) -> str:
    return f"/var/lib/mvmctl/jailer/firecracker/{vm_id}/root"


@pytest.fixture
def jailed_vm(runner_vm: str) -> Generator[dict[str, Any], None, None]:
    vm_name = f"sys-jailer-{uuid.uuid4().hex[:8]}"
    net_name = f"sys-jailer-net-{uuid.uuid4().hex[:6]}"
    try:
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
        )
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--console",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1
        vm = created[0]
        vm["network_name"] = net_name
        yield vm
    finally:
        _resources.get().cleanup()


@pytest.fixture
def created_network(runner_vm: str) -> Generator[str, None, None]:
    """Own the mismatch-oracle network without changing a shared default."""
    name = f"sys-jailer-other-net-{uuid.uuid4().hex[:8]}"
    try:
        _run_mvm(runner_vm, "network", "create", name, "--subnet", _unique_subnet(name))
        yield name
    finally:
        _resources.get().cleanup()


class TestCanonicalJailerLaunch:
    def test_process_is_non_root_inside_expected_jail(
        self, runner_vm: str, jailed_vm: dict[str, Any]
    ) -> None:
        """Prove the VM process is chrooted under an isolated non-root identity."""
        vm = _vm_entry(runner_vm, jailed_vm["name"])
        pid = int(vm["pid"])
        invoking_uid = int(_guest_run(runner_vm, "id -u").stdout.strip())

        status = _guest_run(runner_vm, f"sudo -n cat /proc/{pid}/status").stdout
        uid_line = next(line for line in status.splitlines() if line.startswith("Uid:"))
        process_uid = int(uid_line.split()[1])
        assert process_uid != invoking_uid
        assert process_uid != 0

        expected_root = _jail_root(vm["id"])
        root_identities = _guest_run(
            runner_vm,
            f"sudo -n stat -Lc '%d:%i' -- {shlex.quote(expected_root)} "
            f"/proc/{pid}/root",
        ).stdout.splitlines()
        assert len(root_identities) == 2
        assert root_identities[0] == root_identities[1]
        cmdline = _guest_run(
            runner_vm, f"sudo -n cat /proc/{pid}/cmdline"
        ).stdout.replace("\0", " ")
        assert "firecracker" in cmdline

        api_socket = f"/proc/{pid}/root/vmm-runtime/firecracker.api.socket"
        socket_state = _guest_run(
            runner_vm, f"sudo -n test -S {shlex.quote(api_socket)} && echo socket"
        )
        assert socket_state.stdout.strip() == "socket"

    def test_exec_cp_console_and_lifecycle_parity(
        self, runner_vm: str, jailed_vm: dict[str, Any]
    ) -> None:
        """Exercise API socket, vsock exec/cp, console, stop/start, and reboot."""
        vm_name = jailed_vm["name"]
        marker = f"jailer-{uuid.uuid4().hex}"
        exec_result = _run_mvm(
            runner_vm, "exec", vm_name, "--", "printf", marker
        )
        assert exec_result.stdout == marker

        source = f"/tmp/{marker}.txt"
        _guest_run(runner_vm, f"printf %s {shlex.quote(marker)} > {shlex.quote(source)}")
        _run_mvm(runner_vm, "cp", source, f"{vm_name}:/tmp/jailer-marker.txt")
        copied = _run_mvm(
            runner_vm, "exec", vm_name, "--", "cat", "/tmp/jailer-marker.txt"
        )
        assert copied.stdout.strip() == marker

        console = _run_mvm(runner_vm, "console", vm_name, "--state")
        console_state = (console.stdout + console.stderr).lower()
        assert "running" in console_state or "stopped" in console_state

        before = _vm_entry(runner_vm, vm_name)
        jail = _jail_root(before["id"])
        _run_mvm(runner_vm, "vm", "stop", vm_name)
        stopped = _vm_entry(runner_vm, vm_name)
        assert stopped["status"] == "stopped"
        assert _guest_run(
            runner_vm, f"sudo -n test ! -e {shlex.quote(jail)}", check=False
        ).returncode == 0

        _run_mvm(runner_vm, "vm", "start", vm_name)
        started = _vm_entry(runner_vm, vm_name)
        assert started["status"] == "running"
        assert int(started["pid"]) != int(before["pid"])
        assert _guest_run(
            runner_vm, f"sudo -n test -d {shlex.quote(jail)}", check=False
        ).returncode == 0

        _run_mvm(runner_vm, "vm", "reboot", vm_name)
        rebooted = _vm_entry(runner_vm, vm_name)
        assert rebooted["status"] == "running"
        assert int(rebooted["pid"]) != int(started["pid"])

    def test_volume_and_snapshot_parity(
        self, runner_vm: str, jailed_vm: dict[str, Any], created_network: str
    ) -> None:
        """Selected volume and snapshot resources remain usable without collection mounts."""
        vm_name = jailed_vm["name"]
        volume_name = f"sys-jailer-vol-{uuid.uuid4().hex[:6]}"
        snapshot_id: str | None = None
        try:
            source_inspect = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            source_cgroup = source_inspect["resources"]["cgroup"]
            assert source_cgroup["status"] == "enforced"
            source_limits = source_cgroup["requested"]

            _run_mvm(runner_vm, "volume", "create", volume_name, "64M")
            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "volume", "attach", vm_name, volume_name)
            attached = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", volume_name, "--json").stdout,
                dict,
            )
            assert attached["volume"]["status"] == "attached"
            _run_mvm(runner_vm, "vm", "start", vm_name)
            partitions = _run_mvm(
                runner_vm, "exec", vm_name, "--", "cat", "/proc/partitions"
            )
            assert "vdb" in partitions.stdout

            _run_mvm(runner_vm, "snapshot", "create", vm_name)
            snapshots: list[dict[str, Any]] = _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout,
                list,
            )
            selected = [s for s in snapshots if s.get("source_vm_name") == vm_name]
            assert selected
            snapshot_id = selected[-1]["id"]

            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "volume", "detach", vm_name, volume_name)
            detached = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", volume_name, "--json").stdout,
                dict,
            )
            assert detached["volume"]["status"] == "available"

            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")
            denied_result = _run_mvm(
                runner_vm, "snapshot", "restore", snapshot_id, vm_name,
                "--network", created_network, "--json", check=False,
            )
            assert denied_result.returncode != 0
            denied = json.loads(denied_result.stdout)
            assert denied["status"] == "error"
            assert denied["error"]["code"] == "snapshot.restore_failed"
            assert "cannot replace the captured network" in denied["error"]["message"]
            assert all(vm["name"] != vm_name for vm in _parse_vm_list(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
            ))
            restored = _run_mvm(
                runner_vm, "snapshot", "restore", snapshot_id, vm_name,
                "--network", jailed_vm["network_name"], "--resume",
                check=False, timeout=180,
            )
            assert restored.returncode == 0, restored.stderr
            restored_vm = _vm_entry(runner_vm, vm_name)
            assert restored_vm["status"] == "running"
            restored_volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
            )
            attached_copies = [
                volume for volume in restored_volumes
                if volume.get("vm_id") == restored_vm["id"]
            ]
            assert len(attached_copies) == 1, attached_copies
            assert ("volume", attached_copies[0]["id"]) in _resources.get().receipts
            assert attached_copies[0]["id"] != attached["volume"]["id"]
            restored_inspect = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            restored_cgroup = restored_inspect["resources"]["cgroup"]
            assert restored_cgroup["status"] == "enforced"
            assert restored_cgroup["requested"] == source_limits
            assert restored_cgroup["actual"] == source_limits
            restored_jail = _jail_root(restored_vm["id"])
            assert _guest_run(
                runner_vm, f"sudo -n test -d {shlex.quote(restored_jail)}",
                check=False,
            ).returncode == 0
        finally:
            _resources.get().cleanup()


@pytest.fixture
def networkless_vm_name(runner_vm: str) -> Generator[str, None, None]:
    """Give each mutating networkless journey its own VM and checked teardown."""
    name = f"sys-networkless-{uuid.uuid4().hex[:8]}"
    try:
        yield name
    finally:
        _resources.get().cleanup()


def _networkless_guest_interfaces(runner_vm: str, name: str) -> set[str]:
    """Wait for the guest agent and read the guest's kernel interface list."""
    deadline = time.monotonic() + 20
    last_error = ""
    while time.monotonic() < deadline:
        result = _run_mvm(
            runner_vm,
            "exec",
            name,
            "--",
            "cat",
            "/proc/net/dev",
            check=False,
            timeout=15,
        )
        if result.returncode == 0:
            return {line.split(":", 1)[0].strip() for line in result.stdout.splitlines() if ":" in line}
        last_error = result.stderr
        time.sleep(0.5)
    raise AssertionError(f"vsock agent did not return guest interfaces: {last_error}")


class TestNetworklessJailerLifecycle:
    def test_unqualified_release_is_rejected_before_plain_launch(
        self,
        runner_vm: str,
        networkless_vm_name: str,
    ) -> None:
        """An installed 1.16.0 pair cannot launch even a networkless, no-PCI VM."""
        releases = _JailerReleases(runner_vm)
        try:
            older = releases.pull_older()
            _run_mvm(runner_vm, "bin", "default", older["id"])
            denied = _run_mvm(
                runner_vm,
                "vm",
                "create",
                networkless_vm_name,
                "--no-network",
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--no-pci",
                "-s",
                "512M",
                "--mem",
                "256M",
                "--vcpu",
                "1",
                "--json",
                check=False,
                timeout=180,
            )
            assert denied.returncode != 0
            response = json.loads(denied.stdout)
            assert response["schema_version"] == 1
            assert response["status"] == "error"
            assert response["error"]["code"] == "binary.version.gate"
            assert "Firecracker/Jailer 1.17.0" in response["error"]["message"]
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert all(vm["name"] != networkless_vm_name for vm in listed)
        finally:
            releases.cleanup()

    def test_default_deblob_provisions_bootable_guest(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """The root-brokered default provisioning path edits the guest and still boots it."""
        name = networkless_vm_name
        _run_mvm(
            runner_vm, "vm", "create", name, "--image", "alpine:3.23",
            "--no-network", "--cloud-init-mode", "off", "--no-pci",
            "-s", "512M", "--mem", "256M", "--vcpu", "1", timeout=180,
        )
        assert _vm_entry(runner_vm, name)["status"] == "running"
        marker = _run_mvm(
            runner_vm, "exec", name, "--", "cat", "/etc/cloud/cloud-init.disabled",
            timeout=30,
        )
        assert marker.stdout.strip() == "disabled by mvmctl"
        alpine_rc = _run_mvm(
            runner_vm, "exec", name, "--", "cat", "/etc/rc.conf", timeout=30,
        )
        assert 'rc_parallel="YES"' in alpine_rc.stdout.splitlines()

    def test_operator_cannot_traverse_trusted_release_pair(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """A launched exact pair is usable by the broker but not directly readable by the operator."""
        name = networkless_vm_name
        _run_mvm(
            runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
            "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
            timeout=180,
        )
        vm = _vm_entry(runner_vm, name)
        assert vm["status"] == "running"
        selected_ids = {vm["binary_id"], vm["jailer_binary_id"]}
        binaries = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        pair = [item for item in binaries if item["id"] in selected_ids]
        assert len(pair) == 2
        assert {item["type"] for item in pair} == {"firecracker", "jailer"}
        assert len({item["version"] for item in pair}) == 1
        assert all(item["is_present"] for item in pair)
        for item in pair:
            denied = _guest_run(runner_vm, f"stat -L {shlex.quote(item['path'])}", check=False)
            assert denied.returncode != 0
            assert "permission denied" in denied.stderr.lower()

    def test_ps_and_inspect_follow_stop_and_start(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """The public process list and inspection follow the VM lifecycle."""
        name = networkless_vm_name
        _run_mvm(
            runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
            "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
            timeout=180,
        )
        running = _parse_vm_list(_run_mvm(runner_vm, "vm", "ps", "--json").stdout)
        assert any(vm["name"] == name for vm in running)
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout, dict,
        )
        assert inspected["vm"]["status"] == "running"

        _run_mvm(runner_vm, "vm", "stop", name, timeout=120)
        running = _parse_vm_list(_run_mvm(runner_vm, "vm", "ps", "--json").stdout)
        assert all(vm["name"] != name for vm in running)
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout, dict,
        )
        assert inspected["vm"]["status"] == "stopped"

        _run_mvm(runner_vm, "vm", "start", name, timeout=120)
        running = _parse_vm_list(_run_mvm(runner_vm, "vm", "ps", "--json").stdout)
        assert any(vm["name"] == name for vm in running)
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout, dict,
        )
        assert inspected["vm"]["status"] == "running"

    def test_operator_cannot_connect_to_firecracker_api(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """The public VM lifecycle works without exposing direct Firecracker control."""
        name = networkless_vm_name
        created = _json_success_data(_run_mvm(
            runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
            "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
            "--json", timeout=180,
        ).stdout, list)
        assert len(created) == 1
        assert created[0]["name"] == name
        assert "api_socket_path" not in created[0]
        vm = _vm_entry(runner_vm, name)
        assert vm["status"] == "running"
        assert "api_socket_path" not in vm
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout, dict,
        )
        assert "api_socket_path" not in inspected["vm"]

        operator_uid = os.getuid()
        assert operator_uid != 0
        api_socket = (
            f"/run/mvmctl/vmm-runtime/{operator_uid}/{vm['id']}/firecracker.api.socket"
        )
        socket_type = _guest_run(
            runner_vm, f"sudo -n stat -Lc '%F' -- {shlex.quote(api_socket)}"
        )
        assert socket_type.stdout.strip() == "socket"
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
            assert client.connect_ex(api_socket) in (errno.EACCES, errno.EPERM)
        _run_mvm(runner_vm, "vm", "stop", name, timeout=120)
        assert _vm_entry(runner_vm, name)["status"] == "stopped"

    def test_firecracker_uses_isolated_execution_uid(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """The guest VMM must not inherit either root or the operator's UID."""
        name = networkless_vm_name
        created = _json_success_data(
            _run_mvm(
                runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
                "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
                "--json", timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1
        vm = _vm_entry(runner_vm, name)
        assert vm["status"] == "running"
        status = _guest_run(runner_vm, f"cat /proc/{int(vm['pid'])}/status").stdout
        process_uid = int(next(line for line in status.splitlines() if line.startswith("Uid:")).split()[1])
        operator_uid = int(_guest_run(runner_vm, "id -u").stdout.strip())
        assert operator_uid != 0
        assert process_uid not in (0, operator_uid)

    def test_raw_volume_is_attached_at_launch_and_after_restart(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """A raw data volume reaches the guest through the protected Jailer launch."""
        name = networkless_vm_name
        volume_name = f"sys-networkless-vol-{uuid.uuid4().hex[:8]}"
        try:
            created_volume = _json_success_data(
                _run_mvm(
                    runner_vm, "volume", "create", volume_name, "16M", "--json",
                ).stdout,
                dict,
            )
            assert created_volume["format"] == "raw"

            created_vm = _json_success_data(
                _run_mvm(
                    runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
                    "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M",
                    "--vcpu", "1", "--volume", volume_name, "--json", timeout=180,
                ).stdout,
                list,
            )
            assert len(created_vm) == 1
            assert created_vm[0]["status"] == "running"

            attached = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", volume_name, "--json").stdout,
                dict,
            )
            assert attached["volume"]["status"] == "attached"
            assert attached["attachment"]["vm_id"] == created_vm[0]["id"]

            for attempt in range(2):
                partitions = _run_mvm(
                    runner_vm, "exec", name, "--", "cat", "/proc/partitions", timeout=30,
                )
                assert "vdb" in partitions.stdout
                if attempt == 0:
                    _run_mvm(runner_vm, "vm", "stop", name, timeout=120)
                    _run_mvm(runner_vm, "vm", "start", name, timeout=120)
        finally:
            _resources.get().cleanup()


    @pytest.mark.destructive
    @pytest.mark.parametrize("source_crash", [False, True], ids=["normal-removal", "source-crash"])
    def test_networkless_snapshot_restores_independent_data_volume(
        self, runner_vm: str, networkless_vm_name: str, source_crash: bool,
    ) -> None:
        """Restore one captured epoch across guest RAM, rootfs, and an independent volume."""
        source_name = networkless_vm_name
        restored_name = f"{source_name}-restore"
        volume_name = f"sys-snapshot-vol-{uuid.uuid4().hex[:8]}"
        snapshot_name = f"sys-snapshot-{uuid.uuid4().hex[:8]}"
        epoch = uuid.uuid4().hex
        changed_epoch = uuid.uuid4().hex
        memory_mount = f"/mnt/mvm-snapshot-{epoch}"
        memory_file = f"{memory_mount}/epoch"
        root_file = f"/root/mvm-snapshot-{epoch}"
        try:
            _run_mvm(runner_vm, "volume", "create", volume_name, "16M", "--writeback", "--json")
            _run_mvm(
                runner_vm, "vm", "create", source_name, "--no-network", "--cloud-init-mode", "off",
                "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
                "--volume", volume_name, "--writeback", "--json", timeout=180,
            )
            # A fresh tmpfs mount has no cold-boot recreation path. Its contents and
            # the captured kernel boot ID distinguish restored RAM from disk boot.
            mount_check = (
                f"awk '$2 == \"{memory_mount}\" && $3 == \"tmpfs\" {{found=1}} "
                "END {exit !found}' /proc/mounts"
            )
            _run_mvm(
                runner_vm, "exec", source_name, "--",
                f"set -e; mkdir {shlex.quote(memory_mount)}; "
                f"mount -t tmpfs -o size=1M mvm-snapshot {shlex.quote(memory_mount)}; "
                f"{mount_check}; printf %s {epoch} > {shlex.quote(memory_file)}; "
                f"printf %s {epoch} > {shlex.quote(root_file)}; sync",
            )
            _run_mvm(
                runner_vm, "exec", source_name, "--", "dd", "if=/dev/urandom", "of=/dev/vdb",
                "bs=4096", "count=1", "conv=fsync", timeout=30,
            )
            _run_mvm(
                runner_vm, "exec", source_name, "--",
                f"printf %s {epoch} | dd of=/dev/vdb bs=1 count={len(epoch)} conv=fsync,notrunc",
            )
            captured_state = _run_mvm(
                runner_vm, "exec", source_name, "--",
                f"set -e; {mount_check}; cat {shlex.quote(memory_file)}; echo; "
                f"cat {shlex.quote(root_file)}; echo; cat /proc/sys/kernel/random/boot_id",
            ).stdout.splitlines()
            assert len(captured_state) == 3
            assert captured_state[:2] == [epoch, epoch]
            captured_boot_id = str(uuid.UUID(captured_state[2]))
            captured_hash = _run_mvm(
                runner_vm, "exec", source_name, "--", "sha256sum", "/dev/vdb", timeout=30,
            ).stdout.split()[0]

            captured = _json_success_data(
                _run_mvm(
                    runner_vm, "snapshot", "create", source_name, "--name", snapshot_name,
                    "--json", timeout=180,
                ).stdout,
                dict,
            )
            assert captured["memory_file"] and captured["state_file"]

            _run_mvm(
                runner_vm, "exec", source_name, "--",
                f"set -e; {mount_check}; printf %s {changed_epoch} > {shlex.quote(memory_file)}; "
                f"printf %s {changed_epoch} > {shlex.quote(root_file)}; sync",
            )
            _run_mvm(
                runner_vm, "exec", source_name, "--", "dd", "if=/dev/zero", "of=/dev/vdb",
                "bs=4096", "count=1", "conv=fsync", timeout=30,
            )
            _run_mvm(
                runner_vm, "exec", source_name, "--",
                f"printf %s {changed_epoch} | dd of=/dev/vdb bs=1 "
                f"count={len(changed_epoch)} conv=fsync,notrunc",
            )
            changed_state = _run_mvm(
                runner_vm, "exec", source_name, "--",
                f"set -e; {mount_check}; cat {shlex.quote(memory_file)}; echo; "
                f"cat {shlex.quote(root_file)}; echo; "
                f"dd if=/dev/vdb bs=1 count={len(changed_epoch)}; echo",
            ).stdout.splitlines()
            assert changed_state == [changed_epoch, changed_epoch, changed_epoch]
            changed_hash = _run_mvm(
                runner_vm, "exec", source_name, "--", "sha256sum", "/dev/vdb", timeout=30,
            ).stdout.split()[0]
            assert changed_hash != captured_hash

            if source_crash:
                source = _vm_entry(runner_vm, source_name)
                inspected = _json_success_data(
                    _run_mvm(runner_vm, "vm", "inspect", source_name, "--json").stdout,
                    dict,
                )
                assert source["status"] == inspected["vm"]["status"] == "running"
                assert source["id"] == inspected["vm"]["id"]
                assert source["pid"] == inspected["vm"]["pid"]
                assert int(source["pid"]) > 0
                assert int(source["process_start_time"]) > 0
                _kill_exact_test_firecracker(
                    int(source["pid"]), int(source["process_start_time"]), source["id"],
                    expected_placement="networkless",
                )
                crashed = _vm_entry(runner_vm, source_name)
                assert crashed["id"] == source["id"]
                assert crashed["status"] == "crashed"
                inspected = _json_success_data(
                    _run_mvm(runner_vm, "vm", "inspect", source_name, "--json").stdout,
                    dict,
                )
                assert inspected["vm"]["id"] == source["id"]
                assert inspected["vm"]["status"] == "crashed"
                assert all(vm["id"] != source["id"] for vm in _parse_vm_list(
                    _run_mvm(runner_vm, "vm", "ps", "--json").stdout,
                ))

            _run_mvm(runner_vm, "vm", "rm", source_name, "--force", timeout=120)
            _run_mvm(runner_vm, "volume", "rm", volume_name, "--force")
            denied = _run_mvm(
                runner_vm, "snapshot", "restore", captured["id"], restored_name,
                "--network", "not-a-network", "--json", check=False,
            )
            assert denied.returncode != 0
            assert json.loads(denied.stdout)["status"] == "error"
            assert all(vm["name"] != restored_name for vm in _parse_vm_list(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
            ))
            restored = _json_success_data(
                _run_mvm(
                    runner_vm, "snapshot", "restore", captured["id"], restored_name,
                    "--resume", "--json", timeout=180,
                ).stdout,
                list,
            )
            # Register restored disks before assertions or guest readback can fail;
            # VM removal in finally clears the attachment used to identify them.
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
            )
            restored_vm_ids = {vm["id"] for vm in restored}
            restored_volumes = [volume for volume in volumes if volume.get("vm_id") in restored_vm_ids]
            assert len(restored) == 1
            assert restored[0]["network_placement"] == "networkless"
            assert not restored[0]["network_id"]
            assert not restored[0]["ipv4"]
            assert not restored[0]["mac"]
            restored_state = _run_mvm(
                runner_vm, "exec", restored_name, "--",
                f"set -e; {mount_check}; cat {shlex.quote(memory_file)}; echo; "
                f"cat {shlex.quote(root_file)}; echo; cat /proc/sys/kernel/random/boot_id; "
                f"dd if=/dev/vdb bs=1 count={len(epoch)}; echo",
            ).stdout.splitlines()
            assert restored_state == [epoch, epoch, captured_boot_id, epoch]
            restored_hash = _run_mvm(
                runner_vm, "exec", restored_name, "--", "sha256sum", "/dev/vdb", timeout=30,
            ).stdout.split()[0]
            assert restored_hash == captured_hash
            assert len(restored_volumes) == 1
        finally:
            _resources.get().cleanup()

    @pytest.mark.destructive
    def test_snapshot_capture_file_limit_failure_recovers(
        self,
        runner_vm: str,
        networkless_vm_name: str,
    ) -> None:
        """Recover an unpublished producer failure, then restore one complete captured epoch.

        This is filesystem producer interruption, not power loss or root-receiver death.
        The named runner and this VM's lifecycle are exclusively assigned to this case.
        """
        qualification_host = os.environ.get("MVM_TEST_QUALIFICATION_HOST")
        assert (
            qualification_host
            and qualification_host == os.environ.get("MVM_TEST_VM") == runner_vm
        ), "administrator fault requires explicit exact qualification-host admission"
        assert "PYTEST_XDIST_WORKER" not in os.environ
        resources = _resources.get()
        source_name = networkless_vm_name
        restored_name = f"{source_name}-restore"
        volume_name = f"sys-capture-fault-vol-{uuid.uuid4().hex[:8]}"
        fault_name = f"sys-capture-fault-{uuid.uuid4().hex[:8]}"
        control_name = f"sys-capture-control-{uuid.uuid4().hex[:8]}"
        initial_epoch, captured_epoch, changed_epoch = (uuid.uuid4().hex for _ in range(3))
        root_file = f"/root/mvm-capture-{initial_epoch}"
        memory_mount = f"/mnt/mvm-capture-{initial_epoch}"
        memory_file = memory_mount + "/epoch"
        mount_check = f'awk \'$2 == "{memory_mount}" && $3 == "tmpfs" {{found=1}} END {{exit !found}}\' /proc/mounts'
        fault: _SnapshotCaptureFault | None = None
        try:
            original_volume = _json_success_data(
                _run_mvm(runner_vm, "volume", "create", volume_name, "16M", "--writeback", "--json").stdout,
                dict,
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                source_name,
                "--no-network",
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--no-pci",
                "-s",
                "512M",
                "--mem",
                "256M",
                "--vcpu",
                "1",
                "--volume",
                volume_name,
                "--writeback",
                "--json",
                timeout=180,
            )
            # Firecracker API readiness does not mean the guest agent has finished booting.
            _run_mvm(
                runner_vm,
                "exec",
                source_name,
                "--timeout",
                "30",
                "--",
                f"set -e; printf %s {initial_epoch} > {root_file}; "
                f"printf %s {initial_epoch} | dd of=/dev/vdb bs=1 count=32 conv=fsync,notrunc; sync",
            )
            old_boot = _run_mvm(
                runner_vm,
                "exec",
                source_name,
                "--",
                "cat",
                "/proc/sys/kernel/random/boot_id",
            ).stdout.strip()
            uuid.UUID(old_boot)
            initial_hash = _run_mvm(
                runner_vm,
                "exec",
                source_name,
                "--",
                "sha256sum",
                "/dev/vdb",
            ).stdout.split()[0]
            _run_mvm(runner_vm, "vm", "pause", source_name)
            source = _vm_entry(runner_vm, source_name)
            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", source_name, "--json").stdout,
                dict,
            )
            assert source["status"] == inspected["vm"]["status"] == "paused"
            assert source["id"] == inspected["vm"]["id"] and source["pid"] == inspected["vm"]["pid"]
            cache = Path(inspected["filesystem"]["vm_dir"]).parent.parent
            fault = _SnapshotCaptureFault(source, cache)
            armed = fault.read()
            assert armed["phase"] == "armed" and armed["vm_id"] == source["id"]
            assert armed["pid"] == source["pid"] and armed["start_ticks"] == source["process_start_time"]
            assert armed["applied"] == [16 * 1024 * 1024, armed["original"][1]]

            denied = _run_mvm(
                runner_vm,
                "snapshot",
                "create",
                source_name,
                "--name",
                fault_name,
                "--json",
                check=False,
                timeout=180,
            )
            # The wrapper registers the full intent even when no local snapshot row exists.
            intents = [intent for intent in resources.snapshot_intents.values() if intent.name == fault_name]
            assert len(intents) == 1
            intent = intents[0]
            assert intent.source_id == source["id"]
            assert denied.returncode != 0, (denied.stdout, denied.stderr)
            response = json.loads(denied.stdout)
            assert response["schema_version"] == 1 and response["status"] == "error"
            assert response["data"] is None and response["error"]["code"] == "snapshot.create_failed"
            observation = fault.observe(intent.identifier)
            assert observation["phase"] == "interrupted" and observation["exited"] is True
            assert observation["progress"]["launch_generation"] == armed["launch_generation"]
            fault.close()
            fault = None

            assert all(
                row["id"] != intent.identifier and row["name"] != fault_name for row in resources.rows("snapshot")
            )
            unpublished = _run_mvm(
                runner_vm,
                "snapshot",
                "inspect",
                intent.identifier,
                "--json",
                check=False,
            )
            assert unpublished.returncode != 0
            response = json.loads(unpublished.stdout)
            assert response["schema_version"] == 1 and response["status"] == "error" and response["data"] is None
            assert response["error"]["code"] == "snapshot.create_failed"
            details = response["error"]["details"]
            assert details["snapshot_id"] == intent.identifier and details["source_vm_id"] == source["id"]
            assert details["snapshot_stage"] == "capture_inflight" and details["publication_state"] == "unpublished"
            assert details["snapshot_published"] is False and details["vm_state"] == "unknown"
            assert _vm_entry(runner_vm, source_name)["status"] == "crashed"
            crashed = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", source_name, "--json").stdout,
                dict,
            )
            assert crashed["vm"]["id"] == source["id"] and crashed["vm"]["status"] == "crashed"
            assert all(
                row["id"] != source["id"]
                for row in _parse_vm_list(
                    _run_mvm(runner_vm, "vm", "ps", "--json").stdout,
                )
            )

            _run_mvm(runner_vm, "vm", "stop", source["id"], "--force", timeout=120)
            assert _vm_entry(runner_vm, source_name)["status"] == "stopped"
            assert not resources.path_present(_jail_root(source["id"]))
            # The normal rm wrapper needs a DB receipt. This exact-ID intent is
            # instead admitted above and uses the installed public recovery route.
            for _ in range(2):
                removed = _installed_mvm(
                    runner_vm,
                    "snapshot",
                    "rm",
                    intent.identifier,
                    "--force",
                    check=False,
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
                assert not resources.path_present(intent.path)
            assert all(row["id"] != intent.identifier for row in resources.rows("snapshot"))

            _run_mvm(runner_vm, "vm", "start", source["id"], timeout=120)
            restarted = _vm_entry(runner_vm, source_name)
            assert restarted["id"] == source["id"] and restarted["status"] == "running"
            assert (restarted["pid"], restarted["process_start_time"]) != (source["pid"], source["process_start_time"])
            recovered = _run_mvm(
                runner_vm,
                "exec",
                source_name,
                "--timeout",
                "30",
                "--",
                f"set -e; cat {root_file}; echo; dd if=/dev/vdb bs=1 count=32; echo; "
                "cat /proc/sys/kernel/random/boot_id",
            ).stdout.splitlines()
            assert recovered[:2] == [initial_epoch, initial_epoch] and len(recovered) == 3
            captured_boot = str(uuid.UUID(recovered[2]))
            assert captured_boot != old_boot
            assert (
                _run_mvm(
                    runner_vm,
                    "exec",
                    source_name,
                    "--",
                    "sha256sum",
                    "/dev/vdb",
                ).stdout.split()[0]
                == initial_hash
            )

            _run_mvm(
                runner_vm,
                "exec",
                source_name,
                "--",
                f"set -e; mkdir {memory_mount}; mount -t tmpfs -o size=1M mvm-capture {memory_mount}; "
                f"{mount_check}; printf %s {captured_epoch} > {memory_file}; "
                f"printf %s {captured_epoch} > {root_file}; "
                f"printf %s {captured_epoch} | dd of=/dev/vdb bs=1 count=32 conv=fsync,notrunc; sync",
            )
            captured_hash = _run_mvm(
                runner_vm,
                "exec",
                source_name,
                "--",
                "sha256sum",
                "/dev/vdb",
            ).stdout.split()[0]
            captured = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "snapshot",
                    "create",
                    source_name,
                    "--name",
                    control_name,
                    "--json",
                    timeout=180,
                ).stdout,
                dict,
            )
            assert captured["source_vm_id"] == source["id"]
            assert captured["memory_file"] and captured["state_file"] and captured["rootfs_file"]
            # A full successful capture also proves the replacement process no longer has the injected cap.
            changed_state = _run_mvm(
                runner_vm,
                "exec",
                source_name,
                "--",
                f"set -e; {mount_check}; printf %s {changed_epoch} > {memory_file}; "
                f"printf %s {changed_epoch} > {root_file}; "
                f"printf %s {changed_epoch} | dd of=/dev/vdb bs=1 count=32 conv=fsync,notrunc; sync; "
                f"cat {memory_file}; echo; cat {root_file}; echo; dd if=/dev/vdb bs=1 count=32; echo",
            ).stdout.splitlines()
            assert changed_state == [changed_epoch, changed_epoch, changed_epoch]
            assert (
                _run_mvm(
                    runner_vm,
                    "exec",
                    source_name,
                    "--",
                    "sha256sum",
                    "/dev/vdb",
                ).stdout.split()[0]
                != captured_hash
            )
            _run_mvm(runner_vm, "vm", "rm", source_name, "--force", timeout=120)
            _run_mvm(runner_vm, "volume", "rm", volume_name, "--force")
            restored = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "snapshot",
                    "restore",
                    captured["id"],
                    restored_name,
                    "--resume",
                    "--json",
                    timeout=180,
                ).stdout,
                list,
            )
            assert len(restored) == 1 and restored[0]["network_placement"] == "networkless"
            restored_volumes = [row for row in resources.rows("volume") if row.get("vm_id") == restored[0]["id"]]
            assert len(restored_volumes) == 1 and restored_volumes[0]["id"] != original_volume["id"]
            restored_state = _run_mvm(
                runner_vm,
                "exec",
                restored_name,
                "--",
                f"set -e; {mount_check}; cat {memory_file}; echo; cat {root_file}; echo; "
                "cat /proc/sys/kernel/random/boot_id; dd if=/dev/vdb bs=1 count=32; echo",
            ).stdout.splitlines()
            assert restored_state == [captured_epoch, captured_epoch, captured_boot, captured_epoch]
            assert (
                _run_mvm(
                    runner_vm,
                    "exec",
                    restored_name,
                    "--",
                    "sha256sum",
                    "/dev/vdb",
                ).stdout.split()[0]
                == captured_hash
            )
        finally:
            try:
                if fault is not None:
                    fault.close()
            finally:
                resources.cleanup()

    @pytest.mark.destructive
    def test_snapshot_refuses_competing_writable_volume_capture(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """Public VM creation shares writable bytes; capture refuses the competing writer."""
        source_name = networkless_vm_name
        peer_name = f"{source_name}-peer"
        volume_name = f"sys-snapshot-shared-{uuid.uuid4().hex[:8]}"
        snapshot_name = f"sys-snapshot-denied-{uuid.uuid4().hex[:8]}"
        snapshot_id: str | None = None
        snapshot_path: str | None = None
        try:
            volume = _json_success_data(
                _run_mvm(
                    runner_vm, "volume", "create", volume_name, "16M", "--shareable",
                    "--writeback", "--json",
                ).stdout, dict,
            )
            assert volume["is_shareable"] is True
            assert volume["is_read_only"] is False
            assert volume["format"] == "raw"
            boot_ids: dict[str, str] = {}
            for vm_name in (source_name, peer_name):
                # VM creation admits the existing raw volume. The separate
                # volume-attach command rejects a second writable attachment.
                created = _json_success_data(
                    _run_mvm(
                        runner_vm, "vm", "create", vm_name, "--no-network", "--cloud-init-mode", "off",
                        "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "224M", "--vcpu", "1",
                        "--volume", volume_name, "--writeback", "--json", timeout=180,
                    ).stdout, list,
                )
                assert len(created) == 1
                assert created[0]["status"] == "running"
                boot_ids[vm_name] = _run_mvm(
                    runner_vm, "exec", vm_name, "--", "cat", "/proc/sys/kernel/random/boot_id",
                ).stdout.strip()
            assert boot_ids[source_name] != boot_ids[peer_name]

            # Invalidate guest block caches before every read so both directions
            # establish shared backing rather than a guest's cached prior write.
            for writer, reader in ((source_name, peer_name), (peer_name, source_name)):
                marker = uuid.uuid4().hex
                _run_mvm(
                    runner_vm, "exec", writer, "--",
                    f"printf %s {marker} | dd of=/dev/vdb bs=1 count={len(marker)} conv=fsync,notrunc",
                )
                observed = _run_mvm(
                    runner_vm, "exec", reader, "--",
                    f"set -e; sync; echo 3 > /proc/sys/vm/drop_caches; "
                    f"dd if=/dev/vdb bs=1 count={len(marker)}",
                ).stdout
                assert observed == marker

            denied = _run_mvm(
                runner_vm, "snapshot", "create", source_name, "--name", snapshot_name,
                "--json", check=False, timeout=180,
            )
            intents = [line.removeprefix("Snapshot request ID: ") for line in denied.stderr.splitlines()
                       if line.startswith("Snapshot request ID: ")]
            assert len(intents) <= 1, denied.stderr
            if intents:
                request_id = intents[0]
                assert len(request_id) == 64 and all(c in "0123456789abcdef" for c in request_id)
                snapshot_id = request_id
                snapshot_path = str(Path(volume["path"]).parent.parent / ".mvm-protected" / "snapshots" / snapshot_id)
            assert denied.returncode != 0, (denied.stdout, denied.stderr)
            response = json.loads(denied.stdout)
            assert response["schema_version"] == 1
            assert response["status"] == "error"
            assert response["data"] is None
            assert response["error"]["code"] == "snapshot.create_failed"
            assert response["error"]["class"] == 1  # errs.ClassValidation
            assert response["error"]["message"] == "trusted snapshot capture failed"
            assert snapshot_id is not None
            snapshots = _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            )
            assert all(snapshot["id"] != snapshot_id and snapshot["name"] != snapshot_name
                       for snapshot in snapshots)
            assert _guest_run(runner_vm, f"sudo -n test ! -e {shlex.quote(snapshot_path)}").returncode == 0
            for vm_name in (source_name, peer_name):
                assert _vm_entry(runner_vm, vm_name)["status"] == "running"
                responsive = _run_mvm(
                    runner_vm, "exec", vm_name, "--",
                    f"set -e; cat /proc/sys/kernel/random/boot_id; "
                    f"sync; echo 3 > /proc/sys/vm/drop_caches; dd if=/dev/vdb bs=1 count={len(marker)}",
                ).stdout.splitlines()
                assert responsive == [boot_ids[vm_name], marker]

            # Removing only the competing VM must make the same source capture
            # succeed; a generally broken capture path cannot satisfy this case.
            source_id = _vm_entry(runner_vm, source_name)["id"]
            _run_mvm(runner_vm, "vm", "rm", peer_name, "--force", timeout=120)
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert all(vm["name"] != peer_name for vm in listed)
            accepted = _run_mvm(
                runner_vm, "snapshot", "create", source_name, "--name", snapshot_name,
                "--json", check=False, timeout=180,
            )
            intents = [line.removeprefix("Snapshot request ID: ") for line in accepted.stderr.splitlines()
                       if line.startswith("Snapshot request ID: ")]
            assert len(intents) <= 1, accepted.stderr
            if intents:
                request_id = intents[0]
                assert len(request_id) == 64 and all(c in "0123456789abcdef" for c in request_id)
                snapshot_id = request_id
                snapshot_path = str(Path(volume["path"]).parent.parent / ".mvm-protected" / "snapshots" / snapshot_id)
            assert accepted.returncode == 0, (accepted.stdout, accepted.stderr)
            assert len(intents) == 1, accepted.stderr
            captured = _json_success_data(accepted.stdout, dict)
            assert captured["id"] == snapshot_id
            assert captured["name"] == snapshot_name
            assert captured["source_vm_id"] == source_id
            for field, basename in (("memory_file", "memory"), ("state_file", "vmstate"),
                                    ("rootfs_file", "rootfs.img")):
                assert captured[field] == str(Path(snapshot_path) / basename)
                assert _guest_run(
                    runner_vm, f"sudo -n test -f {shlex.quote(captured[field])}",
                ).returncode == 0
        finally:
            _resources.get().cleanup()


    def test_restore_missing_release_leaves_no_vm_projection(
        self,
        runner_vm: str,
        networkless_vm_name: str,
    ) -> None:
        """A root rejection before launch must not strand a user-side VM record."""
        releases = _JailerReleases(runner_vm, remove_source=True)
        source_name = networkless_vm_name
        target_name = f"{source_name}-failed-restore"
        snapshot_name = f"sys-restore-failure-{uuid.uuid4().hex[:8]}"
        snapshot_id: str | None = None
        try:
            alternate = releases.pull_older()
            source = next(row for row in releases.source_pair if row["type"] == "firecracker")
            _run_mvm(runner_vm, "bin", "default", source["id"])
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                source_name,
                "--no-network",
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--no-pci",
                "-s",
                "512M",
                "--mem",
                "256M",
                "--vcpu",
                "1",
                timeout=180,
            )
            captured = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "snapshot",
                    "create",
                    source_name,
                    "--name",
                    snapshot_name,
                    "--json",
                    timeout=180,
                ).stdout,
                dict,
            )
            snapshot_id = captured["id"]
            _run_mvm(runner_vm, "vm", "rm", source_name, "--force", timeout=120)
            _run_mvm(runner_vm, "bin", "default", alternate["id"])
            releases.remove_source()

            denied = _run_mvm(
                runner_vm,
                "snapshot",
                "restore",
                snapshot_id,
                target_name,
                "--json",
                check=False,
            )
            assert denied.returncode != 0
            response = json.loads(denied.stdout)
            assert response["status"] == "error"
            assert response["data"] is None
            assert "did not launch VM" in response["error"]["message"]
            assert all(
                vm["name"] != target_name
                for vm in _parse_vm_list(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
                )
            )
            # Snapshot removal reaches the root-owned reference check; a leaked
            # restore reference would reject this operation.
            _run_mvm(runner_vm, "snapshot", "rm", snapshot_id, "--force")
            snapshot_id = None
        finally:
            releases.cleanup()

    def test_create_restart_reboot_and_vsock_exec_without_guest_nic(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """The installed candidate can restart a loopback-only VM without a network record."""
        name = networkless_vm_name
        created = _json_success_data(
            _run_mvm(
                runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
                "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
                "--image", "alpine:3.23",
                "--json", timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1, created
        original = created[0]
        assert original["name"] == name
        assert original["status"] == "running"
        assert original["network_placement"] == "networkless"
        assert (original["network_id"], original["ipv4"], original["mac"], original["tap_device"]) == (
            "", "", "", "",
        )
        first_boot_id = _run_mvm(
            runner_vm, "exec", original["id"], "--timeout", "5", "--",
            "cat", "/proc/sys/kernel/random/boot_id",
        ).stdout.strip()
        assert str(uuid.UUID(first_boot_id)) == first_boot_id, first_boot_id

        interfaces = _networkless_guest_interfaces(runner_vm, name)
        assert "lo" in interfaces
        assert "eth0" not in interfaces

        _run_mvm(runner_vm, "vm", "stop", name)
        stopped = next(vm for vm in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
                       if vm["name"] == name)
        assert stopped["status"] == "stopped"

        _run_mvm(runner_vm, "vm", "start", name)
        started = next(vm for vm in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
                       if vm["name"] == name)
        assert started["status"] == "running"
        assert started["process_start_time"] != original["process_start_time"]
        interfaces = _networkless_guest_interfaces(runner_vm, name)
        assert "lo" in interfaces
        assert "eth0" not in interfaces

        _run_mvm(runner_vm, "vm", "reboot", name, timeout=120)
        rebooted = next(vm for vm in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
                        if vm["name"] == name)
        assert rebooted["status"] == "running"
        assert rebooted["process_start_time"] != started["process_start_time"]
        interfaces = _networkless_guest_interfaces(runner_vm, name)
        assert "lo" in interfaces
        assert "eth0" not in interfaces
