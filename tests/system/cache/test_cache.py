"""Cache management system tests.

Migrated from tests/e2e/cache/test_cache.py with destructive tests
merged from tests/e2e/zzz_destructive/test_zzz_destructive.py.

Violations fixed (cache source):
  - Import from tests.system.conftest instead of tests.e2e.conftest
  - sqlite3.connect(Path.home() / ...) on host → JSON verification via _run_mvm inside VM
  - subprocess.run(["ip", "link", ...]) on host → _run_mvm(runner_vm, "sh", "-c", "ip link show ...")
  - subprocess.run directly with stdin pipe → _run_mvm with timeout handling
  - os.path.exists on host → _run_mvm(runner_vm, "sh", "-c", "test -f ... && echo exists")
  - _restore_service_binaries() removed (operated on host filesystem/DB)
  - pytest.skip() removed → precondition assertions or graceful handling
  - Path import removed (host filesystem operations eliminated)

Violations fixed (zzz_destructive source):
  - pytest.skip() on cache clean failure → assertion with pytest.fail semantic
  - Import from tests.system.conftest instead of tests.e2e.conftest
  - Moved into TestZzzDestructive class with @pytest.mark.destructive
  - No separate tests/system/zzz_destructive/ directory
"""

from __future__ import annotations

import errno
import hashlib
import json
import os
import select
import shlex
import shutil
import signal
import stat
import subprocess
import time
import uuid
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
)

pytestmark = [pytest.mark.system, pytest.mark.domain_cache]


def _cache_inventory(runner_vm):
    """Read logical user state without opening SQLite or changing defaults."""
    inventory = {
        domain: _json_success_data(
            _run_mvm(runner_vm, domain, "ls", "--json").stdout, list
        )
        for domain in ("vm", "network", "image", "kernel", "bin", "key", "volume")
    }
    inventory["config"] = _json_success_data(
        _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
    )
    return inventory


class _SelectedCacheCase:
    """One user cache; only exact attempted key/network names may be removed."""

    def __init__(self, runner_vm, cache_dir, config_dir):
        self.runner_vm = runner_vm
        self.cache_dir = cache_dir
        self.config_dir = config_dir
        self.attempted = {"key": set(), "network": set()}
        self.identities = {}
        self.initialized = False

    def initialize(self):
        self.initialized = True
        result = _run_mvm(self.runner_vm, "cache", "init", "--json")
        data = _json_success_data(result.stdout, dict)
        assert data["cache_dir"] == str(self.cache_dir)
        expected = {
            str(self.cache_dir / name)
            for name in ("vms", "images", "kernels", "bin", "logs")
        }
        expected.add(str(self.config_dir / "keys"))
        assert set(data["directories"]) == expected
        for directory in expected:
            assert stat.S_ISDIR(Path(directory).lstat().st_mode), directory
        database = self.cache_dir / "mvmdb.db"
        assert stat.S_ISREG(database.lstat().st_mode)
        assert database.stat().st_size > 0
        # Misc previews must have a real, case-owned warm file to preserve.
        ready = self.cache_dir / "tmp" / "ready"
        ready.mkdir(parents=True, exist_ok=True)
        marker = ready / "preview-sentinel"
        if not marker.is_file():
            marker.write_bytes(b"cache-preview-must-preserve-this-file\n")
        return data

    def prepare(self):
        self.initialize()
        name = f"cache-key-{uuid.uuid4().hex[:10]}"
        self.attempted["key"].add(name)
        data = _json_success_data(
            _run_mvm(
                self.runner_vm, "key", "create", name, "--algorithm", "ed25519",
                "--out", str(self.cache_dir / "keys"), "--json",
            ).stdout,
            dict,
        )
        self.identities[("key", name)] = data["id"]
        assert data["is_default"] is False
        assert any(row["id"] == data["id"] for row in _cache_inventory(self.runner_vm)["key"])
        for key_path in (data["public_key_path"], data["private_key_path"]):
            path = Path(key_path)
            assert path.is_relative_to(self.cache_dir)
            assert stat.S_ISREG(path.lstat().st_mode)

    def create_network(self):
        name = f"cache-net-{uuid.uuid4().hex[:10]}"
        self.attempted["network"].add(name)
        data = _json_success_data(
            _run_mvm(
                self.runner_vm, "network", "create", name,
                "--subnet", _unique_subnet(name), "--no-nat", "--json",
            ).stdout,
            dict,
        )
        self.identities[("network", name)] = data["id"]
        assert not data["is_default"]
        return data

    def snapshot(self):
        inventory = _cache_inventory(self.runner_vm)
        files = {}
        for root in (self.cache_dir, self.config_dir):
            for path in sorted(root.rglob("*")):
                # SQLite journal bytes are not a stable public read contract.
                # Compare its logical rows above and DB identity separately.
                if path.name.startswith("mvmdb.db"):
                    continue
                info = path.lstat()
                assert not stat.S_ISLNK(info.st_mode), path
                digest = hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else None
                files[str(path)] = (info.st_dev, info.st_ino, info.st_mode, digest)
        db_info = (self.cache_dir / "mvmdb.db").lstat()
        return inventory, files, (db_info.st_dev, db_info.st_ino, db_info.st_mode)

    def cleanup(self):
        failures = []
        if not self.initialized:
            return failures
        for domain in ("network", "key"):
            for name in self.attempted[domain]:
                try:
                    inventory = _json_success_data(
                        _run_mvm(self.runner_vm, domain, "ls", "--json").stdout, list
                    )
                    rows = [row for row in inventory if row["name"] == name]
                    assert len(rows) <= 1, rows
                    if rows:
                        row = rows[0]
                        remembered = self.identities.get((domain, name))
                        assert remembered is None or remembered == row["id"], rows
                        flags = ("--force",) if domain == "network" else ()
                        _run_mvm(self.runner_vm, domain, "rm", row["id"], *flags, "--json")
                        if domain == "network":
                            bridge = shlex.quote(row["bridge"])
                            absent = _guest_run(self.runner_vm, f"ip link show dev {bridge}", check=False)
                            assert absent.returncode != 0, absent.stdout
                        else:
                            for key_path in (row["public_key_path"], row["private_key_path"]):
                                path = Path(key_path)
                                assert path.is_relative_to(self.cache_dir)
                                with pytest.raises(FileNotFoundError):
                                    path.lstat()
                    if domain == "key":
                        # A failed creation must not leave an unpublished keypair.
                        # Preserve unexpected remnants and fail rather than erase evidence.
                        for suffix in ("", ".pub"):
                            with pytest.raises(FileNotFoundError):
                                (self.cache_dir / "keys" / f"{name}{suffix}").lstat()
                except (Exception, pytest.fail.Exception) as error:
                    failures.append(f"{domain} cleanup {name}: {error!r}")
            try:
                remaining = _json_success_data(
                    _run_mvm(self.runner_vm, domain, "ls", "--json").stdout, list
                )
                assert not remaining, remaining
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"{domain} absence check: {error!r}")
        return failures


@pytest.fixture
def selected_cache_case(runner_vm, tmp_path, monkeypatch):
    """Select private paths for the bounded preview and validation cases only."""
    baseline = _cache_inventory(runner_vm)
    cache_dir = (tmp_path / "cache").resolve()
    config_dir = (tmp_path / "config").resolve()
    cache_dir.mkdir(mode=0o700)
    config_dir.mkdir(mode=0o700)
    failures = []
    with monkeypatch.context() as selection:
        selection.setenv("MVM_CACHE_DIR", str(cache_dir))
        selection.setenv("MVM_CONFIG_DIR", str(config_dir))
        selection.setenv("MVM_TEMP_DIR", str(cache_dir / "tmp"))
        selection.delenv("MVM_WARM_POOL", raising=False)
        case = _SelectedCacheCase(runner_vm, cache_dir, config_dir)
        try:
            yield case
        finally:
            try:
                failures.extend(case.cleanup())
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"selected cache cleanup: {error!r}")
    # The caller environment has been restored. Each baseline check is independent
    # so a network failure cannot prevent the key or configuration observations.
    for domain, expected in baseline.items():
        try:
            current = _json_success_data(
                _run_mvm(runner_vm, domain, "ls", "--json").stdout,
                dict if domain == "config" else list,
            )
            assert current == expected, (expected, current)
        except Exception as error:
            failures.append(f"runner main {domain} baseline: {error!r}")
    assert not failures, "Cache case teardown failed:\n" + "\n".join(failures)


class _GuestfsPreviewSentinel:
    """Pin one owned appliance directory outside the selected user cache."""

    def __init__(self):
        self.uid = os.getuid()
        self.parent_name = f".guestfs-{self.uid}"
        self.directory_name = f"appliance.d-{uuid.uuid4().hex}"
        self.payload = b"aggregate-cache-preview-must-preserve-global-guestfs-state\n"
        self.fds = {}
        self.identities = {}
        self.created = set()

    @staticmethod
    def _identity(info):
        return info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode

    def _binding(self, name, parent_fd, identity, *, directory):
        info = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
        assert self._identity(info) == identity, (name, identity, info)
        assert info.st_uid == self.uid, (name, info.st_uid)
        assert stat.S_IMODE(info.st_mode) == (0o700 if directory else 0o600), name
        assert (stat.S_ISDIR if directory else stat.S_ISREG)(info.st_mode), name
        if not directory:
            assert info.st_nlink == 1, name
        return info

    def seed(self):
        flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
        self.fds["base"] = os.open("/var/tmp", flags)
        base = os.fstat(self.fds["base"])
        assert stat.S_ISDIR(base.st_mode) and base.st_uid == 0
        assert base.st_mode & stat.S_ISVTX
        self.identities["base"] = self._identity(base)
        try:
            os.mkdir(self.parent_name, mode=0o700, dir_fd=self.fds["base"])
            self.created.add("parent")
        except FileExistsError:
            pass
        self.identities["parent"] = self._identity(
            os.stat(self.parent_name, dir_fd=self.fds["base"], follow_symlinks=False)
        )
        self._binding(self.parent_name, self.fds["base"], self.identities["parent"], directory=True)
        self.fds["parent"] = os.open(self.parent_name, flags, dir_fd=self.fds["base"])
        assert self._identity(os.fstat(self.fds["parent"])) == self.identities["parent"]
        os.mkdir(self.directory_name, mode=0o700, dir_fd=self.fds["parent"])
        self.created.add("directory")
        self.identities["directory"] = self._identity(
            os.stat(self.directory_name, dir_fd=self.fds["parent"], follow_symlinks=False)
        )
        self._binding(
            self.directory_name, self.fds["parent"], self.identities["directory"], directory=True
        )
        self.fds["directory"] = os.open(self.directory_name, flags, dir_fd=self.fds["parent"])
        assert self._identity(os.fstat(self.fds["directory"])) == self.identities["directory"]
        self.fds["marker"] = os.open(
            "marker", os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC,
            0o600, dir_fd=self.fds["directory"],
        )
        self.created.add("marker")
        self.identities["marker"] = self._identity(os.fstat(self.fds["marker"]))
        self._binding("marker", self.fds["directory"], self.identities["marker"], directory=False)
        offset = 0
        while offset < len(self.payload):
            written = os.write(self.fds["marker"], self.payload[offset:])
            assert written > 0
            offset += written
        os.fsync(self.fds["marker"])
        self.snapshot()

    def snapshot(self):
        assert self._identity(os.fstat(self.fds["base"])) == self.identities["base"]
        assert self._identity(os.lstat("/var/tmp")) == self.identities["base"]
        parent = self._binding(
            self.parent_name, self.fds["base"], self.identities["parent"], directory=True
        )
        directory = self._binding(
            self.directory_name, self.fds["parent"], self.identities["directory"], directory=True
        )
        marker = self._binding(
            "marker", self.fds["directory"], self.identities["marker"], directory=False
        )
        for kind in ("parent", "directory", "marker"):
            assert self._identity(os.fstat(self.fds[kind])) == self.identities[kind]
        content = os.pread(self.fds["marker"], len(self.payload) + 1, 0)
        assert marker.st_size == len(self.payload)
        assert content == self.payload
        return self._identity(parent), self._identity(directory), self._identity(marker), content

    def cleanup(self):
        failures = []
        try:
            if "base" in self.identities:
                assert self._identity(os.fstat(self.fds["base"])) == self.identities["base"]
                assert self._identity(os.lstat("/var/tmp")) == self.identities["base"]
            if "parent" in self.identities:
                self._binding(self.parent_name, self.fds["base"], self.identities["parent"], directory=True)
                if "parent" in self.fds:
                    assert self._identity(os.fstat(self.fds["parent"])) == self.identities["parent"]
            if "directory" in self.created:
                assert "directory" in self.identities, "Unpinned sentinel directory preserved"
                try:
                    self._binding(
                        self.directory_name, self.fds["parent"], self.identities["directory"], directory=True
                    )
                except FileNotFoundError:
                    pass  # The regression already reports an illicit dry-run deletion.
                else:
                    if "directory" in self.fds:
                        assert self._identity(os.fstat(self.fds["directory"])) == self.identities["directory"]
                    if "marker" in self.created:
                        try:
                            self._binding(
                                "marker", self.fds["directory"], self.identities["marker"], directory=False
                            )
                        except FileNotFoundError:
                            pass
                        else:
                            assert self._identity(os.fstat(self.fds["marker"])) == self.identities["marker"]
                            os.unlink("marker", dir_fd=self.fds["directory"])
                            with pytest.raises(FileNotFoundError):
                                os.stat("marker", dir_fd=self.fds["directory"], follow_symlinks=False)
                    # rmdir is deliberately non-recursive and refuses unexpected contents.
                    self._binding(
                        self.directory_name, self.fds["parent"], self.identities["directory"], directory=True
                    )
                    os.rmdir(self.directory_name, dir_fd=self.fds["parent"])
                    with pytest.raises(FileNotFoundError):
                        os.stat(self.directory_name, dir_fd=self.fds["parent"], follow_symlinks=False)
            if "parent" in self.created:
                assert "parent" in self.identities, "Unpinned guestfs parent preserved"
                self._binding(self.parent_name, self.fds["base"], self.identities["parent"], directory=True)
                try:
                    os.rmdir(self.parent_name, dir_fd=self.fds["base"])
                except OSError as error:
                    if error.errno != errno.ENOTEMPTY:
                        raise
                else:
                    with pytest.raises(FileNotFoundError):
                        os.stat(self.parent_name, dir_fd=self.fds["base"], follow_symlinks=False)
        except (Exception, pytest.fail.Exception) as error:
            failures.append(f"guestfs sentinel cleanup: {error!r}")
        finally:
            for kind in ("marker", "directory", "parent", "base"):
                if kind in self.fds:
                    try:
                        os.close(self.fds[kind])
                    except OSError as error:
                        failures.append(f"guestfs sentinel close {kind}: {error!r}")
        assert not failures, "\n".join(failures)


@pytest.fixture
def guestfs_preview_sentinel(selected_cache_case):
    """Seed before preview; preserve existing parent state through exact cleanup."""
    sentinel = _GuestfsPreviewSentinel()
    try:
        sentinel.seed()
        yield sentinel
    finally:
        sentinel.cleanup()


class _UnrelatedGuestfishProcess:
    """A creation-owned sleep process whose identity is held by a pidfd."""

    def __init__(self):
        self.pidfd = None
        self.admitted = False
        self.pid = None
        self.start = None
        self.argv = f"guestfish-cache-sentinel-{uuid.uuid4().hex}".encode()
        self.uid = os.getuid()
        self.executable = os.stat("/bin/sleep")

    @staticmethod
    def _start(pid):
        # Field 22 follows a parenthesized comm, which may itself contain spaces.
        raw = Path(f"/proc/{pid}/stat").read_text()
        return int(raw[raw.rindex(")") + 2:].split()[19])

    def seed(self):
        assert self.uid != 0, "Run the cache sentinel as the ordinary runner user"
        receipt_read, receipt_write = os.pipe2(os.O_CLOEXEC)
        gate_read, gate_write = os.pipe2(os.O_CLOEXEC)
        intermediary = None
        try:
            intermediary = os.fork()
            if intermediary == 0:
                try:
                    os.close(receipt_read)
                    os.close(gate_write)
                    child = os.fork()
                    if child != 0:
                        os._exit(0)
                    # Only this directly created child can write the private
                    # receipt. Without the parent's ack it exits within 5s.
                    receipt = f"{os.getpid()} {self._start(os.getpid())}\n".encode()
                    assert os.write(receipt_write, receipt) == len(receipt)
                    os.close(receipt_write)
                    ready, _, _ = select.select([gate_read], [], [], 5)
                    if not ready or os.read(gate_read, 1) != b"x":
                        os._exit(1)
                    os.close(gate_read)
                    os.execv("/bin/sleep", [os.fsdecode(self.argv), "180"])
                except BaseException:
                    os._exit(1)
            os.close(receipt_write)
            receipt_write = None
            os.close(gate_read)
            gate_read = None
            ready, _, _ = select.select([receipt_read], [], [], 5)
            assert ready, "Owned sleep child did not publish its creation receipt"
            receipt = os.read(receipt_read, 128)
            assert receipt.endswith(b"\n") and len(receipt) < 128, receipt
            self.pid, self.start = map(int, receipt.split())
            assert self.pid > 1 and self.start > 0
            self.pidfd = os.pidfd_open(self.pid)
            assert not select.select([self.pidfd], [], [], 0)[0]
            assert self._start(self.pid) == self.start
            status = Path(f"/proc/{self.pid}/status").read_text()
            fields = dict(line.split(":", 1) for line in status.splitlines() if ":" in line)
            assert tuple(map(int, fields["Uid"].split())) == (self.uid,) * 4
            assert int(fields["PPid"]) in (intermediary, 1)
            assert self._start(self.pid) == self.start
            assert not select.select([self.pidfd], [], [], 0)[0]
            self.admitted = True
            assert os.write(gate_write, b"x") == 1
            os.close(gate_write)
            gate_write = None
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                assert not select.select([self.pidfd], [], [], 0)[0], "Owned sleep exited at startup"
                cmdline = Path(f"/proc/{self.pid}/cmdline").read_bytes()
                status = Path(f"/proc/{self.pid}/status").read_text()
                fields = dict(line.split(":", 1) for line in status.splitlines() if ":" in line)
                if cmdline == self.argv + b"\x00180\x00" and int(fields["PPid"]) == 1:
                    break
                time.sleep(0.01)
            self.snapshot()
        finally:
            for fd in (receipt_read, receipt_write, gate_read, gate_write):
                if fd is not None:
                    os.close(fd)
            if intermediary not in (None, 0):
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline:
                    waited, status = os.waitpid(intermediary, os.WNOHANG)
                    if waited:
                        assert os.waitstatus_to_exitcode(status) == 0
                        break
                    time.sleep(0.01)
                else:
                    pytest.fail("Owned intermediary did not exit within 5s")

    def snapshot(self):
        assert self.pidfd is not None
        assert not select.select([self.pidfd], [], [], 0)[0], "Unrelated owned sleep was terminated"
        assert self._start(self.pid) == self.start
        status = Path(f"/proc/{self.pid}/status").read_text()
        fields = dict(line.split(":", 1) for line in status.splitlines() if ":" in line)
        uids = tuple(map(int, fields["Uid"].split()))
        assert uids == (self.uid,) * 4
        assert int(fields["PPid"]) == 1, "A test ancestor would make the old cleaner miss this process"
        cmdline = Path(f"/proc/{self.pid}/cmdline").read_bytes()
        assert cmdline == self.argv + b"\x00180\x00"
        # The old predicate examines the process itself, then stops before PID 1:
        # same UID + guestfish in argv + no mvm ancestor is an admitted target.
        assert b"guestfish" in cmdline.lower() and b"mvm" not in cmdline.lower()
        executable = os.stat(f"/proc/{self.pid}/exe")
        identity = (executable.st_dev, executable.st_ino)
        assert identity == (self.executable.st_dev, self.executable.st_ino)
        assert self._start(self.pid) == self.start
        assert not select.select([self.pidfd], [], [], 0)[0]
        return self.pid, self.start, uids, identity, cmdline

    def cleanup(self):
        if self.pidfd is None:
            return
        try:
            if not self.admitted:
                return  # Preserve any process whose creation identity was not proved.
            if not select.select([self.pidfd], [], [], 0)[0]:
                # A pidfd can signal only the directly created incarnation,
                # including on startup failure; a recycled raw PID is never used.
                try:
                    signal.pidfd_send_signal(self.pidfd, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                if not select.select([self.pidfd], [], [], 2)[0]:
                    try:
                        signal.pidfd_send_signal(self.pidfd, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
            assert select.select([self.pidfd], [], [], 2)[0], "Owned sleep cleanup did not finish"
        finally:
            os.close(self.pidfd)
            self.pidfd = None


@pytest.fixture
def unrelated_guestfish_process(selected_cache_case, record_property):
    """No GuestFS installation is needed to exercise the old process selector."""
    sentinel = _UnrelatedGuestfishProcess()
    try:
        sentinel.seed()
        record_property("misc_process_creation", json.dumps(sentinel.snapshot(), default=bytes.hex))
        yield sentinel
    finally:
        sentinel.cleanup()
        record_property(
            "misc_process_cleanup",
            "admitted pidfd exited and closed" if sentinel.admitted else "startup gate closed; no admitted pidfd",
        )


def _prune_metadata(info):
    return (
        info.st_dev,
        info.st_ino,
        info.st_uid,
        info.st_gid,
        info.st_mode,
        info.st_nlink,
    )


def _prune_backing(path, *, digest=True):
    """Read an exact regular-file binding without following its final symlink."""
    assert path.is_absolute() and path.resolve() == path, path
    info = path.lstat()
    assert stat.S_ISREG(info.st_mode) and info.st_nlink == 1, path
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(fd, "rb") as source:
        assert _prune_metadata(os.fstat(source.fileno())) == _prune_metadata(info), path
        content = hashlib.file_digest(source, "sha256").hexdigest() if digest else None
        assert os.fstat(source.fileno()).st_size == info.st_size, path
    return _prune_metadata(info), info.st_size, content


def _prune_row_identity(domain, row):
    fields = {
        "image": (
            "id",
            "name",
            "type",
            "version",
            "arch",
            "path",
            "created_at",
            "is_imported",
        ),
        "kernel": (
            "id",
            "name",
            "base_name",
            "type",
            "version",
            "arch",
            "path",
            "created_at",
        ),
        "network": (
            "id",
            "name",
            "subnet",
            "bridge",
            "ipv4_gateway",
            "nat_enabled",
            "created_at",
        ),
        "vm": (
            "id",
            "name",
            "created_at",
            "image_id",
            "kernel_id",
            "binary_id",
            "jailer_binary_id",
            "rootfs_path",
            "config_path",
            "network_placement",
            "network_id",
            "tap_device",
            "vcpu_count",
            "mem_size_mib",
            "disk_size_mib",
        ),
    }[domain]
    return {field: row[field] for field in fields}


class _OwnedCachePrune:
    """Class-specific pruning in one private cache; root releases are borrowed."""

    def __init__(self, runner_vm, cache_dir, config_dir, baseline, sources):
        self.runner_vm = runner_vm
        self.cache_dir = cache_dir
        self.config_dir = config_dir
        self.baseline = baseline
        self.sources = sources
        self.kernel_namespace = uuid.uuid4().hex
        self.roots = {
            path: _prune_metadata(path.lstat())[:5] for path in (cache_dir, config_dir)
        }
        self.attempted = {domain: {} for domain in ("vm", "network", "kernel", "image")}
        self.receipts = {domain: {} for domain in self.attempted}
        self.staging = {}
        self.release = None
        self.firewall_baseline = None
        self.initialized = False

    def inventory(self, domain):
        rows = _json_success_data(
            _run_mvm(self.runner_vm, domain, "ls", "--json").stdout, list
        )
        assert len({row["id"] for row in rows}) == len(rows), rows
        return {row["id"]: row for row in rows}

    def initialize(self):
        self.initialized = True
        data = _json_success_data(
            _run_mvm(self.runner_vm, "cache", "init", "--json").stdout, dict
        )
        assert data["cache_dir"] == str(self.cache_dir), data
        assert all(not self.inventory(domain) for domain in self.attempted)

    def source(self, domain):
        assert domain in self.sources, (
            f"preseeded runner {domain} is required; no pull fallback"
        )
        row, path, receipt = self.sources[domain]
        assert _prune_backing(path) == receipt, path
        return row, path

    def remember(self, domain, name):
        rows = [
            row
            for row in self.inventory(domain).values()
            if row["base_name" if domain == "kernel" else "name"] == name
        ]
        assert len(rows) <= 1, rows
        if not rows:
            return
        row = rows[0]
        item_id = row["id"]
        assert len(item_id) == (32 if domain == "vm" else 64)
        assert all(char in "0123456789abcdef" for char in item_id), row
        assert item_id not in {item["id"] for item in self.baseline[domain]}, row
        expected_id = self.attempted[domain][name]
        assert expected_id is None or item_id == expected_id, row
        identity = _prune_row_identity(domain, row)
        if item_id in self.receipts[domain]:
            assert identity == self.receipts[domain][item_id]["row"], row
            return
        receipt = {"row": identity, "files": {}}
        self.receipts[domain][item_id] = receipt
        if domain in ("image", "kernel"):
            data = _json_success_data(
                _run_mvm(self.runner_vm, domain, "inspect", item_id, "--json").stdout,
                dict,
            )
            assert data[domain]["id"] == item_id, data
            path = Path(data["storage"]["path"])
            assert path.is_absolute() and path.is_relative_to(
                self.cache_dir / f"{domain}s"
            ), path
            paths = [path]
            if domain == "image":
                paths = [
                    entry
                    for entry in path.parent.iterdir()
                    if entry.name.startswith(item_id)
                ]
                assert path in paths, (path, paths)
            receipt["files"] = {path: _prune_backing(path) for path in paths}
        elif domain == "vm":
            rootfs = Path(row["rootfs_path"])
            assert rootfs.is_relative_to(self.cache_dir / "vms"), rootfs
            receipt["files"][rootfs] = _prune_backing(rootfs, digest=False)
            vm_dir = rootfs.parent
            receipt["directory"] = (vm_dir, _prune_metadata(vm_dir.lstat()))
        else:
            bridge = json.loads(
                _guest_run(
                    self.runner_vm,
                    f"ip -d -j link show dev {shlex.quote(row['bridge'])}",
                ).stdout
            )
            assert len(bridge) == 1 and bridge[0]["ifindex"] > 0, bridge
            assert bridge[0]["linkinfo"]["info_kind"] == "bridge", bridge
            receipt["bridge"] = bridge
            receipt["namespace"] = _prune_metadata(Path("/proc/self/ns/net").stat())
            authority = (
                Path("/var/lib/mvmctl/networks") / str(os.getuid()) / f"{item_id}.json"
            )
            receipt["authority"] = authority, self.root_file_receipt(authority)
            recorded = json.loads(
                _guest_run(
                    self.runner_vm,
                    f"sudo -n cat -- {shlex.quote(str(authority))}",
                ).stdout
            )
            assert (
                recorded["owner_uid"] == os.getuid()
                and recorded["network_id"] == item_id
            ), recorded
            assert recorded["managed_bridge"]["name"] == row["bridge"], recorded
            assert recorded["managed_bridge"]["ifindex"] == bridge[0]["ifindex"], (
                recorded
            )
            assert (
                tuple(
                    recorded["managed_bridge"]["namespace"][key]
                    for key in ("device", "inode")
                )
                == (receipt["namespace"][:2])
            ), recorded
        receipt["complete"] = True

    @staticmethod
    def bridge_comparison(links):
        assert isinstance(links, list) and len(links) == 1, links
        projected = json.loads(json.dumps(links))
        assert projected[0]["linkinfo"]["info_kind"] == "bridge", links
        # Runner captures proved only this GC countdown changes with elapsed
        # time; every other identity, configuration and policy field is retained.
        projected[0]["linkinfo"]["info_data"].pop("gc_timer", None)
        return projected

    def root_file_receipt(self, path):
        quoted = shlex.quote(str(path))
        metadata = _guest_run(
            self.runner_vm,
            f"sudo -n stat -c '%d:%i:%u:%g:%f:%h:%s' -- {quoted}",
        ).stdout.strip()
        parts = metadata.split(":")
        assert len(parts) == 7 and parts[2:4] == ["0", "0"], metadata
        assert stat.S_ISREG(int(parts[4], 16)) and parts[5] == "1", metadata
        digest = _guest_run(
            self.runner_vm, f"sudo -n sha256sum -- {quoted}"
        ).stdout.split()[0]
        assert len(digest) == 64, digest
        return metadata, digest

    def import_kernel(self, role, *, default=False):
        source_row, path = self.source("kernel")
        version = f"{source_row['version']}-qa-{self.kernel_namespace}-role-{role}"
        name = f"cache-kernel-{uuid.uuid4().hex}"
        digest = self.sources["kernel"][2][2]
        item_id = hashlib.sha256(
            f"{digest}:{version}:{source_row['arch']}".encode()
        ).hexdigest()
        assert item_id not in self.inventory("kernel"), item_id
        self.attempted["kernel"][name] = item_id
        flags = ("--default",) if default else ()
        try:
            imported = _json_success_data(
                _run_mvm(
                    self.runner_vm,
                    "kernel",
                    "import",
                    name,
                    str(path),
                    "--version",
                    version,
                    *flags,
                    "--json",
                ).stdout,
                dict,
            )
            assert imported["id"] == item_id, imported
        finally:
            self.remember("kernel", name)
        assert self.inventory("kernel")[item_id]["is_default"] is default
        assert (
            next(iter(self.receipts["kernel"][item_id]["files"].values()))[2] == digest
        )
        return self.inventory("kernel")[item_id]

    def import_image(self, *, default=False):
        _, source = self.source("image")
        name = f"cache-image-{uuid.uuid4().hex}"
        raw = self.cache_dir / f"{name}.raw"
        with raw.open("xb") as target:
            self.staging[raw] = (_prune_metadata(os.fstat(target.fileno())), None)
            if source.suffix == ".zst":
                result = subprocess.run(
                    ["/usr/bin/zstd", "--decompress", "--stdout", "--", str(source)],
                    stdout=target,
                    stderr=subprocess.PIPE,
                    timeout=120,
                    check=False,
                )
                assert result.returncode == 0, result.stderr
            else:
                with source.open("rb") as origin:
                    shutil.copyfileobj(origin, target)
        self.staging[raw] = (_prune_metadata(raw.lstat()), _prune_backing(raw))
        self.source("image")
        self.attempted["image"][name] = None
        flags = ("--default",) if default else ()
        try:
            imported = _json_success_data(
                _run_mvm(
                    self.runner_vm,
                    "image",
                    "import",
                    name,
                    str(raw),
                    "--format",
                    "raw",
                    "--skip-optimization",
                    *flags,
                    "--json",
                    timeout=120,
                ).stdout,
                dict,
            )
        finally:
            self.remember("image", name)
        assert imported["id"] in self.receipts["image"], imported
        row = self.inventory("image")[imported["id"]]
        assert (
            row["is_default"] is default and row["is_imported"] and row["is_present"]
        ), row
        return row

    def create_network(self):
        assert self.firewall_baseline is None, "one owned network per cache case"
        self.firewall_baseline = self.firewall_snapshot()
        foreign = [
            key
            for key in self.firewall_baseline[1]
            if key not in (("bridge", "mvmctl_bridge"), ("inet", "mvmctl_inet"))
        ]
        if not foreign:
            print(
                "Firewall baseline has no foreign table; existing policy preservation is checked, "
                "but foreign-table loss is not exercised."
            )
        name = f"cache-net-{uuid.uuid4().hex[:16]}"
        self.attempted["network"][name] = None
        try:
            created = _json_success_data(
                _run_mvm(
                    self.runner_vm,
                    "network",
                    "create",
                    name,
                    "--subnet",
                    _unique_subnet(name),
                    "--no-nat",
                    "--json",
                ).stdout,
                dict,
            )
        finally:
            self.remember("network", name)
        assert (
            created["id"] in self.receipts["network"] and not created["is_default"]
        ), created
        return created

    def firewall_snapshot(self):
        """Preserve all policy expressions and rule order within every table."""
        data = json.loads(
            _guest_run(self.runner_vm, "sudo -n nft -j list ruleset").stdout
        )
        assert set(data) == {"nftables"} and isinstance(data["nftables"], list), data

        def counters(value):
            if isinstance(value, list):
                return [counters(item) for item in value]
            if not isinstance(value, dict):
                return value
            result = {}
            for key, item in value.items():
                if key == "counter" and isinstance(item, dict):
                    # Keep the counter expression, name and configuration; only
                    # packets/bytes are live observations, not policy inputs.
                    item = {
                        field: observed
                        for field, observed in item.items()
                        if field not in ("packets", "bytes")
                    }
                result[key] = counters(item)
            return result

        metadata = []
        tables = {}
        for entry in data["nftables"]:
            assert isinstance(entry, dict) and len(entry) == 1, entry
            kind, value = next(iter(entry.items()))
            if kind == "metainfo":
                metadata.append(entry)
                continue
            assert isinstance(value, dict) and "family" in value, entry
            table = value["name"] if kind == "table" else value["table"]
            # generation application deletes/recreates the managed tables
            # (kernel_application.go), allocating fresh object handles. Table
            # listing order is not packet evaluation order; retain each table's
            # complete ordered contents and canonicalize only table grouping.
            value = counters(
                {kind: {key: item for key, item in value.items() if key != "handle"}}
            )
            tables.setdefault((entry[kind]["family"], table), []).append(value)
        return metadata, dict(sorted(tables.items()))

    def assert_firewall_baseline(self):
        assert self.firewall_baseline is not None
        assert self.firewall_snapshot() == self.firewall_baseline, (
            "unrelated firewall policy changed"
        )

    def release_identity(self, paths):
        quoted = " ".join(shlex.quote(str(path)) for path in paths)
        metadata = _guest_run(
            self.runner_vm,
            f"sudo -n stat -c '%d:%i:%u:%g:%f:%h:%s' -- {quoted}",
        ).stdout.splitlines()
        assert len(metadata) == len(paths), metadata
        for index, line in enumerate(metadata):
            parts = line.split(":")
            assert len(parts) == 7 and parts[2:4] == ["0", "0"], line
            mode = int(parts[4], 16)
            assert (stat.S_ISDIR if index == 0 else stat.S_ISREG)(mode), line
            assert not mode & 0o022, line
            if index:
                assert parts[5] == "1" and int(parts[6]) > 0, line
        digests = _guest_run(
            self.runner_vm,
            "sudo -n sha256sum -- "
            + " ".join(shlex.quote(str(path)) for path in paths[1:]),
        ).stdout.splitlines()
        assert len(digests) == 2, digests
        return metadata, digests

    def reuse_release(self):
        pairs = [item for item in self.baseline["bin"] if item["version"] == "1.16.2"]
        assert len(pairs) == 2 and {item["type"] for item in pairs} == {
            "firecracker",
            "jailer",
        }, pairs
        assert all(item["is_present"] and item["is_default"] for item in pairs), pairs
        architecture = pairs[0]["architecture"]
        assert all(item["architecture"] == architecture for item in pairs), pairs
        slot = Path("/var/lib/mvmctl/binaries") / architecture / "1.16.2"
        paths = [slot, slot / "firecracker", slot / "jailer"]
        assert {item["path"] for item in pairs} == {str(path) for path in paths[1:]}, (
            pairs
        )
        mirror = os.environ.get("MVM_ASSET_MIRROR", "")
        assert mirror, "the preseeded local asset mirror is required"
        archive = Path(mirror) / f"firecracker-v1.16.2-{architecture}.tgz"
        archive_receipt = _prune_backing(archive)
        assert archive_receipt[1] > 0, archive
        # This root read is a prerequisite; never install an absent release slot.
        self.release = (paths, self.release_identity(paths))
        registered = _json_success_data(
            _run_mvm(
                self.runner_vm,
                "bin",
                "pull",
                "1.16.2",
                "--json",
                timeout=300,
            ).stdout,
            list,
        )
        assert {item["id"] for item in registered} == {item["id"] for item in pairs}, (
            registered
        )
        assert self.release_identity(paths) == self.release[1]
        assert _prune_backing(archive) == archive_receipt, archive
        return next(item for item in registered if item["type"] == "firecracker")

    def create_vm(self):
        binary = self.reuse_release()
        kernel = self.import_kernel("6.1", default=True)
        image = self.import_image(default=True)
        name = f"cache-vm-{uuid.uuid4().hex}"
        self.attempted["vm"][name] = None
        try:
            created = _json_success_data(
                _run_mvm(
                    self.runner_vm,
                    "vm",
                    "create",
                    name,
                    "--image",
                    image["id"],
                    "--kernel",
                    kernel["id"],
                    "--no-network",
                    "--cloud-init-mode",
                    "off",
                    "--skip-deblob",
                    "--no-pci",
                    "--disk-size",
                    "512M",
                    "--mem",
                    "256M",
                    "--vcpu",
                    "1",
                    "--json",
                    timeout=180,
                ).stdout,
                list,
            )
            assert len(created) == 1 and created[0]["name"] == name, created
        finally:
            self.remember("vm", name)
        row = self.inventory("vm")[created[0]["id"]]
        assert (
            row["status"] == "running" and row["network_placement"] == "networkless"
        ), row
        assert row["binary_id"] == binary["id"], row
        self.receipts["vm"][row["id"]]["runtime"] = self.vm_runtime(row)
        self.receipts["vm"][row["id"]]["netns"] = self.vm_namespace(row)
        return row

    def vm_namespace(self, row):
        # deriveNamespaceHandle in network/identifiers.go binds this exact
        # root-owned networkless namespace handle to the owner UID and full VM ID.
        path = Path("/run/mvmctl/authority") / str(os.getuid()) / "netns" / row["id"]
        metadata = _guest_run(
            self.runner_vm,
            f"sudo -n stat -c '%d:%i:%u:%g:%f' -- {shlex.quote(str(path))}",
        ).stdout.strip()
        fields = metadata.split(":")
        assert len(fields) == 5 and fields[2:4] == ["0", "0"], metadata
        assert stat.S_ISREG(int(fields[4], 16)), metadata
        process_identity = _guest_run(
            self.runner_vm,
            f"sudo -n stat -Lc '%d:%i' -- /proc/{row['pid']}/ns/net",
        ).stdout.strip()
        assert process_identity == ":".join(fields[:2]), (metadata, process_identity)
        return path, metadata

    def guest_boot_id(self, row):
        result = _run_mvm(
            self.runner_vm,
            "exec",
            row["id"],
            "--",
            "cat",
            "/proc/sys/kernel/random/boot_id",
        )
        boot_id = result.stdout.strip()
        assert str(uuid.UUID(boot_id)) == boot_id, result.stdout
        return boot_id

    def preview_marker(self):
        ready = self.cache_dir / "tmp" / "ready"
        ready.mkdir(parents=True, exist_ok=True)
        path = ready / f"preview-{uuid.uuid4().hex}"
        with path.open("xb") as target:
            self.staging[path] = (_prune_metadata(os.fstat(target.fileno())), None)
            target.write(b"owned-warm-preview-must-survive\n")
        self.staging[path] = (_prune_metadata(path.lstat()), _prune_backing(path))

    def vm_runtime(self, row):
        inspected = _json_success_data(
            _run_mvm(
                self.runner_vm,
                "vm",
                "inspect",
                row["id"],
                "--json",
            ).stdout,
            dict,
        )
        assert (
            inspected["vm"]["id"] == row["id"]
            and inspected["vm"]["status"] == "running"
        ), inspected
        pid = inspected["vm"]["pid"]
        assert pid > 0, inspected
        process = _guest_run(self.runner_vm, f"sudo -n cat /proc/{pid}/stat").stdout
        start_time = int(process.rsplit(")", 1)[1].split()[19])
        cgroup = inspected["resources"]["cgroup"]
        assert cgroup["status"] == "enforced" and cgroup["path"].startswith(
            "/sys/fs/cgroup/"
        ), cgroup
        return pid, start_time, cgroup["path"], cgroup["requested"], cgroup["actual"]

    def check_bindings(self, domain, item_id):
        receipt = self.receipts[domain][item_id]
        assert receipt.get("complete"), (
            "incomplete ownership receipt preserved",
            domain,
            item_id,
        )
        row = self.inventory(domain)[item_id]
        assert _prune_row_identity(domain, row) == receipt["row"], row
        for path, expected in receipt["files"].items():
            assert _prune_backing(path, digest=domain != "vm") == expected, path
        if domain == "vm":
            path, expected = receipt["directory"]
            assert _prune_metadata(path.lstat()) == expected, path
            if row["status"] == "running" and "netns" in receipt:
                assert self.vm_namespace(row) == receipt["netns"]
        if domain == "network":
            assert (
                _prune_metadata(Path("/proc/self/ns/net").stat())
                == receipt["namespace"]
            )
            links = json.loads(
                _guest_run(
                    self.runner_vm,
                    f"ip -d -j link show dev {shlex.quote(row['bridge'])}",
                ).stdout
            )
            assert self.bridge_comparison(links) == self.bridge_comparison(
                receipt["bridge"]
            ), links
            path, expected = receipt["authority"]
            assert self.root_file_receipt(path) == expected, path
        return row

    def assert_absent(self, domain, item_id):
        receipt = self.receipts[domain][item_id]
        assert item_id not in self.inventory(domain), item_id
        for path in receipt["files"]:
            with pytest.raises(FileNotFoundError):
                path.lstat()
        if domain == "network":
            result = _guest_run(
                self.runner_vm,
                f"ip -j link show dev {shlex.quote(receipt['row']['bridge'])}",
                check=False,
            )
            assert result.returncode != 0 and "does not exist" in result.stderr, result
            path = receipt["authority"][0]
            absent = _guest_run(
                self.runner_vm, f"sudo -n stat -- {shlex.quote(str(path))}", check=False
            )
            assert (
                absent.returncode != 0 and "No such file or directory" in absent.stderr
            ), absent
            rules = _guest_run(self.runner_vm, "sudo -n nft -j list ruleset").stdout
            json.loads(rules)
            assert receipt["row"]["bridge"] not in rules, rules
            self.assert_firewall_baseline()
        if domain == "vm":
            with pytest.raises(FileNotFoundError):
                receipt["directory"][0].lstat()
            if "netns" in receipt:
                path = receipt["netns"][0]
                absent = _guest_run(
                    self.runner_vm,
                    f"sudo -n stat -- {shlex.quote(str(path))}",
                    check=False,
                )
                assert (
                    absent.returncode != 0
                    and "No such file or directory" in absent.stderr
                ), absent
            if "runtime" in receipt:
                pid, started, cgroup, _, _ = receipt["runtime"]
                observed = _guest_run(
                    self.runner_vm, f"sudo -n cat /proc/{pid}/stat", check=False
                )
                if observed.returncode == 0:
                    assert (
                        int(observed.stdout.rsplit(")", 1)[1].split()[19]) != started
                    ), observed.stdout
                else:
                    assert "No such file or directory" in observed.stderr, (
                        observed.stderr
                    )
                for path in (
                    cgroup,
                    f"/var/lib/mvmctl/jailer/firecracker/{item_id}/root",
                ):
                    absent = _guest_run(
                        self.runner_vm,
                        f"sudo -n stat -- {shlex.quote(path)}",
                        check=False,
                    )
                    assert (
                        absent.returncode != 0
                        and "No such file or directory" in absent.stderr
                    ), absent

    def snapshot(self):
        rows = _cache_inventory(self.runner_vm)
        rows["snapshot"] = list(self.inventory("snapshot").values())
        mutable = set()
        for domain, receipts in self.receipts.items():
            for item_id, receipt in receipts.items():
                self.check_bindings(domain, item_id)
                if domain == "vm":
                    mutable.update(receipt["files"])
        bindings = {}
        for root in (self.cache_dir, self.config_dir):
            for path in root.rglob("*"):
                if path.name.startswith("mvmdb.db"):
                    continue
                info = path.lstat()
                assert not stat.S_ISLNK(info.st_mode), path
                bindings[path] = (
                    _prune_backing(path, digest=path not in mutable)
                    if stat.S_ISREG(info.st_mode)
                    else _prune_metadata(info)
                )
        database = self.cache_dir / "mvmdb.db"
        return rows, bindings, _prune_metadata(database.lstat())

    def cleanup(self):
        failures = []
        # Remove exact staging bindings first: image removal also clears the
        # selected warm pool, so the preview marker must be checked before it.
        for path, (identity, content) in self.staging.items():
            try:
                assert _prune_metadata(path.lstat()) == identity, path
                if content is not None:
                    assert _prune_backing(path) == content, path
                path.unlink()
                with pytest.raises(FileNotFoundError):
                    path.lstat()
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"staging cleanup {path}: {error!r}")
        if self.initialized:
            for domain in ("vm", "network", "kernel", "image"):
                for name in self.attempted[domain]:
                    try:
                        self.remember(domain, name)
                    except (Exception, pytest.fail.Exception) as error:
                        failures.append(f"recover {domain} receipt {name}: {error!r}")
                for item_id in self.receipts[domain]:
                    try:
                        if item_id in self.inventory(domain):
                            self.check_bindings(domain, item_id)
                            _run_mvm(
                                self.runner_vm,
                                domain,
                                "rm",
                                item_id,
                                "--force",
                                "--json",
                                timeout=120,
                            )
                        self.assert_absent(domain, item_id)
                    except (Exception, pytest.fail.Exception) as error:
                        failures.append(f"remove {domain} {item_id}: {error!r}")
                try:
                    assert not self.inventory(domain), domain
                except (Exception, pytest.fail.Exception) as error:
                    failures.append(f"selected {domain} absence: {error!r}")
        for domain, (_, path, expected) in self.sources.items():
            try:
                assert _prune_backing(path) == expected, path
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"preseeded {domain} preservation: {error!r}")
        if self.release is not None:
            try:
                assert self.release_identity(self.release[0]) == self.release[1]
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"borrowed root release preservation: {error!r}")
        if self.firewall_baseline is not None:
            try:
                self.assert_firewall_baseline()
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"complete firewall baseline preservation: {error!r}")
        for path, expected in self.roots.items():
            try:
                assert _prune_metadata(path.lstat())[:5] == expected, path
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"private root binding {path}: {error!r}")
        return failures


@pytest.fixture
def owned_cache_prune(runner_vm, tmp_path, monkeypatch):
    """Capture runner defaults before selecting any private database or paths."""
    baseline = _cache_inventory(runner_vm)
    baseline["snapshot"] = _json_success_data(
        _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout,
        list,
    )
    sources = {}
    for domain in ("image", "kernel"):
        candidates = [
            row
            for row in baseline[domain]
            if row["is_present"]
            and (
                row["type"] == "alpine" and row["version"] == "3.23"
                if domain == "image"
                else row["is_default"]
            )
        ]
        if len(candidates) == 1:
            row = candidates[0]
            inspected = _json_success_data(
                _run_mvm(
                    runner_vm,
                    domain,
                    "inspect",
                    row["id"],
                    "--json",
                ).stdout,
                dict,
            )
            assert inspected[domain]["id"] == row["id"]
            path = Path(inspected["storage"]["path"])
            sources[domain] = row, path, _prune_backing(path)
    cache_dir = (tmp_path / "prune-cache").resolve()
    config_dir = (tmp_path / "prune-config").resolve()
    cache_dir.mkdir(mode=0o700)
    config_dir.mkdir(mode=0o700)
    case = _OwnedCachePrune(runner_vm, cache_dir, config_dir, baseline, sources)
    failures = []
    with monkeypatch.context() as selection:
        selection.setenv("MVM_CACHE_DIR", str(cache_dir))
        selection.setenv("MVM_CONFIG_DIR", str(config_dir))
        selection.setenv("MVM_TEMP_DIR", str(cache_dir / "tmp"))
        selection.delenv("MVM_WARM_POOL", raising=False)
        try:
            case.initialize()
            yield case
        finally:
            try:
                failures.extend(case.cleanup())
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"owned cache cleanup: {error!r}")
    for domain, expected in baseline.items():
        try:
            current = _json_success_data(
                _run_mvm(runner_vm, domain, "ls", "--json").stdout,
                dict if domain == "config" else list,
            )
            assert current == expected, (expected, current)
        except (Exception, pytest.fail.Exception) as error:
            failures.append(f"runner main {domain} baseline/defaults: {error!r}")
    assert not failures, "Owned prune teardown failed:\n" + "\n".join(failures)


def _cache_root_directory_receipt(runner_vm, path):
    metadata = _guest_run(
        runner_vm, f"sudo -n stat -c '%d:%i:%u:%g:%f' -- {shlex.quote(str(path))}"
    ).stdout.strip()
    fields = metadata.split(":")
    assert len(fields) == 5 and fields[2:4] == ["0", "0"], metadata
    mode = int(fields[4], 16)
    assert stat.S_ISDIR(mode) and not mode & 0o022, metadata
    return metadata


def _cache_release_receipt(case, version):
    """Read the complete fixed root slot, including its authority manifest."""
    slot = Path("/var/lib/mvmctl/binaries") / os.uname().machine / version
    directory = _cache_root_directory_receipt(case.runner_vm, slot)
    files = {
        name: _OwnedCachePrune.root_file_receipt(case, slot / name)
        for name in ("manifest.json", "firecracker", "jailer")
    }
    return directory, files


class _CacheExternalPreservation:
    """Creation-owned GuestFS sentinels and read-only runner authority receipts."""

    def __init__(self, case):
        self.case = case
        self.runner_vm = case.runner_vm
        self.appliance = _GuestfsPreviewSentinel()
        self.process = _UnrelatedGuestfishProcess()
        self.before = None

    def seed(self):
        self.appliance.seed()
        self.process.seed()
        self.before = (
            self.appliance.snapshot(), self.process.snapshot(),
            _cache_release_receipt(self.case, "1.16.2"),
            _OwnedCachePrune.firewall_snapshot(self),
        )

    def assert_preserved(self):
        assert self.before is not None
        failures = []
        observations = (
            ("global appliance", self.appliance.snapshot, self.before[0]),
            ("unrelated process", self.process.snapshot, self.before[1]),
            ("default root release", lambda: _cache_release_receipt(self.case, "1.16.2"), self.before[2]),
            ("complete firewall", lambda: _OwnedCachePrune.firewall_snapshot(self), self.before[3]),
        )
        for name, observe, expected in observations:
            try:
                assert observe() == expected, name
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"{name}: {error!r}")
        assert not failures, "Cache crossed its ownership boundary:\n" + "\n".join(failures)

    def cleanup(self):
        failures = []
        for name, sentinel in (("process", self.process), ("appliance", self.appliance)):
            try:
                sentinel.cleanup()
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"{name} cleanup: {error!r}")
        assert not failures, "Cache sentinel teardown failed:\n" + "\n".join(failures)


@pytest.fixture
def selected_cache_preservation(selected_cache_case):
    preservation = _CacheExternalPreservation(selected_cache_case)
    try:
        preservation.seed()
        yield preservation
    finally:
        preservation.cleanup()


@pytest.fixture
def owned_cache_preservation(owned_cache_prune):
    preservation = _CacheExternalPreservation(owned_cache_prune)
    try:
        preservation.seed()
        yield preservation
    finally:
        preservation.cleanup()


class _CacheMiscArtifacts:
    """Exact unprivileged artifacts created only in the selected private cache."""

    def __init__(self, case):
        cache = case.cache_dir
        appliance = cache / "appliance"
        appliance.mkdir(mode=0o700)
        ready = cache / "tmp" / "ready"
        ready.mkdir(parents=True, exist_ok=True)
        artifacts = [appliance / name for name in ("kernel", "initrd", "root")]
        artifacts.extend((cache / "bin" / "owned-force-artifact", ready / "owned-force-warm"))
        for path in artifacts:
            with path.open("xb") as target:
                target.write(b"exact-selected-cache-misc-artifact\n")
        # _SelectedCacheCase.initialize also creates this case-owned marker.
        marker = ready / "preview-sentinel"
        if marker.is_file():
            assert marker.read_bytes() == b"cache-preview-must-preserve-this-file\n"
            artifacts.append(marker)
        self.files = {path: _prune_backing(path) for path in artifacts}
        self.directories = {
            path: _prune_metadata(path.lstat()) for path in (appliance, cache / "bin")
        }

    def assert_bound(self):
        for path, receipt in self.files.items():
            assert _prune_backing(path) == receipt, path
        for path, receipt in self.directories.items():
            assert _prune_metadata(path.lstat()) == receipt, path

    def assert_absent(self):
        for path in (*self.files, *self.directories):
            with pytest.raises(FileNotFoundError):
                path.lstat()


class _OwnedCacheRelease:
    """An absent audited slot becomes owned only after a complete CLI install."""

    def __init__(self, case):
        self.case = case
        self.version = None
        self.receipt = None
        self.attempted = False
        self.archive = None
        self.archive_receipt = None

    def slot_absent(self, version):
        slot = Path("/var/lib/mvmctl/binaries") / os.uname().machine / version
        result = _guest_run(
            self.case.runner_vm, f"sudo -n stat -- {shlex.quote(str(slot))}", check=False
        )
        if result.returncode == 0:
            return False
        assert "No such file or directory" in result.stderr, result
        return True

    def install(self):
        self.case.reuse_release()
        mirror = Path(os.environ["MVM_ASSET_MIRROR"])
        for version in ("1.16.0", "1.16.1"):
            if any(row["version"] == version for row in self.case.baseline["bin"]):
                continue
            if not self.slot_absent(version):
                continue
            archive = mirror / f"firecracker-v{version}-{os.uname().machine}.tgz"
            try:
                receipt = _prune_backing(archive)
            except FileNotFoundError:
                continue
            assert receipt[1] > 0, archive
            self.version, self.archive, self.archive_receipt = version, archive, receipt
            break
        assert self.version is not None, "No absent audited mirrored release slot; preserve runner releases"
        assert self.slot_absent(self.version), "Slot appeared before install; no removal authority"
        self.attempted = True
        pair = _json_success_data(
            _run_mvm(self.case.runner_vm, "bin", "pull", self.version, "--json", timeout=300).stdout,
            list,
        )
        assert len(pair) == 2 and {row["type"] for row in pair} == {"firecracker", "jailer"}, pair
        assert all(
            row["version"] == self.version and row["architecture"] == os.uname().machine
            and row["is_present"] and not row["is_default"]
            and row["path"] == str(
                Path("/var/lib/mvmctl/binaries") / row["architecture"] / self.version / row["type"]
            ) for row in pair
        ), pair
        self.receipt = _cache_release_receipt(self.case, self.version)
        assert _prune_backing(self.archive) == self.archive_receipt
        return pair

    def assert_absent(self):
        assert self.receipt is not None, "Incomplete release receipt; preserve any root remnant"
        assert self.slot_absent(self.version), self.version
        assert all(row["version"] != self.version for row in self.case.inventory("bin").values())
        assert _prune_backing(self.archive) == self.archive_receipt

    def assert_bound(self):
        assert self.receipt is not None, "Destructive prune requires the complete owned release receipt"
        assert _cache_release_receipt(self.case, self.version) == self.receipt
        assert _prune_backing(self.archive) == self.archive_receipt

    def cleanup(self):
        if not self.attempted:
            return
        if self.receipt is None:
            assert self.slot_absent(self.version), "Uncertain install retained; no admitted removal receipt"
            return
        if not self.slot_absent(self.version):
            assert _cache_release_receipt(self.case, self.version) == self.receipt
            data = _json_success_data(
                _run_mvm(
                    self.case.runner_vm, "bin", "rm", self.version, "--force", "--json", timeout=120
                ).stdout,
                dict,
            )
            assert len(data["items"]) == 1 and data["items"][0]["error"] is None, data
            result = data["items"][0]["result"]
            assert result["code"] == "binary.removed" and result["item"] == self.version, result
        self.assert_absent()


@pytest.fixture
def owned_cache_release(owned_cache_prune):
    release = _OwnedCacheRelease(owned_cache_prune)
    try:
        yield release
    finally:
        release.cleanup()


def _cache_config_receipt(case):
    return {
        path: _prune_backing(path) if path.is_file() else _prune_metadata(path.lstat())
        for path in case.config_dir.rglob("*")
    }


def _require_unprotected_clean_case(case):
    """Success cases never ask an ordinary user to erase root-controlled history."""
    assert not case.receipts["vm"] and not case.inventory("vm")
    assert not case.inventory("volume") and not case.inventory("snapshot")
    with pytest.raises(FileNotFoundError):
        (case.cache_dir / ".mvm-protected").lstat()


def _reinitialize_cleaned_cache(case, data, expected_ids, config_before):
    """Acknowledge checked removal before admitting a fresh cache incarnation."""
    assert data["cache_dir"] == str(case.cache_dir) and data["cache_dir_removed"] is True, data
    prune = data["prune_result"]
    assert set(prune["pruned_ids"]) == expected_ids and len(prune["pruned_ids"]) == len(expected_ids), prune
    assert not prune["failed_ids"] and prune["had_running_vms"] is False, prune
    with pytest.raises(FileNotFoundError):
        case.cache_dir.lstat()
    assert _cache_config_receipt(case) == config_before
    for path in case.staging:
        with pytest.raises(FileNotFoundError):
            path.lstat()
    case.initialize()
    assert not case.inventory("bin")
    for domain, receipts in case.receipts.items():
        for item_id in receipts:
            case.assert_absent(domain, item_id)
        # Only already-proved absent resources leave the teardown ledger.
        receipts.clear()
        case.attempted[domain].clear()
    case.staging.clear()
    case.roots[case.cache_dir] = _prune_metadata(case.cache_dir.lstat())[:5]
    if case.firewall_baseline is not None:
        case.assert_firewall_baseline()
        case.firewall_baseline = None
    assert _cache_config_receipt(case) == config_before


class TestCacheInit:
    """Test cache initialization operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cache,
    ]

    def test_cache_init(self, runner_vm, selected_cache_case):
        """Initialize cache resources."""
        assert list(selected_cache_case.cache_dir.iterdir()) == []
        selected_cache_case.initialize()
        assert _cache_inventory(runner_vm)["bin"] == []

    def test_cache_init_idempotent(self, runner_vm, selected_cache_case):
        """Running cache init multiple times should be safe (idempotent)."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        selected_cache_case.initialize()
        assert selected_cache_case.snapshot() == before


class TestCachePruneDryRun:
    """Test cache prune dry-run operations."""

    @pytest.mark.needs_kvm
    @pytest.mark.slow
    @pytest.mark.tier2
    def test_cache_prune_all_dry_run(self, runner_vm, owned_cache_prune):
        """Aggregate preview retains one running owned VM and its borrowed pair."""
        case = owned_cache_prune
        vm = case.create_vm()
        boot_id = case.guest_boot_id(vm)
        case.preview_marker()
        before = case.snapshot()
        runtime = case.vm_runtime(vm)
        data = _json_success_data(
            _run_mvm(
                runner_vm,
                "cache",
                "prune",
                "--all",
                "--dry-run",
                "--json",
            ).stdout,
            dict,
        )
        assert data["had_running_vms"] is True and not data["failed_ids"], data
        assert set(data["pruned_ids"]) == {
            vm["name"],
            vm["image_id"],
            vm["kernel_id"],
            "warm_images",
        }, data
        assert case.snapshot() == before
        assert case.guest_boot_id(case.inventory("vm")[vm["id"]]) == boot_id
        assert case.vm_runtime(case.inventory("vm")[vm["id"]]) == runtime
        assert case.release_identity(case.release[0]) == case.release[1]

    def test_cache_prune_dry_run_shows_what_would_be_removed(
        self, runner_vm, selected_cache_case, guestfs_preview_sentinel
    ):
        """Aggregate preview preserves both selected cache and global guestfs state."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        sentinel_before = guestfs_preview_sentinel.snapshot()
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "--dry-run", "--all", "--json").stdout,
            dict,
        )
        assert guestfs_preview_sentinel.snapshot() == sentinel_before
        assert selected_cache_case.snapshot() == before
        assert "warm_images" in data["pruned_ids"]
        assert "guestfs_state" not in data["pruned_ids"]
        assert not data["failed_ids"]
        assert data["had_running_vms"] is False

    def test_cache_prune_vm_dry_run(self, runner_vm, selected_cache_case):
        """cache prune vm --dry-run should succeed and not remove VMs."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        assert before[0]["vm"] == []
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "vm", "--dry-run", "--json").stdout,
            dict,
        )
        assert len(data["items"]) == 1
        assert data["items"][0]["result"]["status"] == "success"
        assert not data["items"][0]["result"].get("item")
        assert selected_cache_case.snapshot() == before

    def test_cache_prune_network_dry_run(self, runner_vm, selected_cache_case):
        """cache prune network --dry-run should succeed and not remove networks."""
        selected_cache_case.prepare()
        network = selected_cache_case.create_network()
        command = f"ip -j link show dev {shlex.quote(network['bridge'])}"
        bridge_before = json.loads(_guest_run(runner_vm, command).stdout)
        assert len(bridge_before) == 1
        before = selected_cache_case.snapshot()
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "network", "--dry-run", "--json").stdout,
            list,
        )
        assert data == [network["name"]]
        assert selected_cache_case.snapshot() == before
        assert json.loads(_guest_run(runner_vm, command).stdout) == bridge_before


class TestCachePruneEdgeCases:
    """Test edge cases for cache prune command."""

    def test_cache_prune_with_nonexistent_category(self, runner_vm, selected_cache_case):
        """Pruning a nonexistent category should fail."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        result = _run_mvm(
            runner_vm,
            "cache",
            "prune",
            "nonexistent-category",
            "--dry-run",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert "nonexistent-category" in envelope["error"]["message"]
        assert selected_cache_case.snapshot() == before

    def test_cache_prune_nonexistent_category_flag(self, runner_vm, selected_cache_case):
        """cache prune with an unknown flag should fail."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        result = _run_mvm(
            runner_vm,
            "cache",
            "prune",
            "--nonexistent-category",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert "nonexistent-category" in envelope["error"]["message"]
        assert selected_cache_case.snapshot() == before

    def test_cache_prune_without_category_or_all_fails(self, runner_vm, selected_cache_case):
        """cache prune without resource and --all should fail with guidance."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        guidance = _run_mvm(runner_vm, "cache", "prune", check=False)
        assert guidance.returncode != 0
        assert "--all" in guidance.stdout + guidance.stderr
        result = _run_mvm(runner_vm, "cache", "prune", "--json", check=False)
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert envelope["error"]["message"] == "no resource specified"
        assert selected_cache_case.snapshot() == before

    @pytest.mark.destructive
    def test_cache_prune_default_image_skipped_or_warns(
        self, runner_vm, owned_cache_prune
    ):
        """Class-specific prune removes the eligible image and preserves exact default bytes."""
        case = owned_cache_prune
        protected = case.import_image(default=True)
        target = case.import_image()
        before = case.check_bindings("image", protected["id"])
        removed = _json_success_data(
            _run_mvm(
                runner_vm,
                "cache",
                "prune",
                "image",
                "--force",
                "--json",
            ).stdout,
            list,
        )
        assert removed == [target["id"]], removed
        case.assert_absent("image", target["id"])
        assert case.check_bindings("image", protected["id"]) == before
        assert set(case.inventory("image")) == {protected["id"]}
        assert case.inventory("image")[protected["id"]]["is_default"] is True


class TestCacheClean:
    """Test cache clean command."""

    def test_cache_clean_dry_run(self, runner_vm, selected_cache_case, guestfs_preview_sentinel):
        """Clean preview preserves selected cache and the global appliance sentinel."""
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        sentinel_before = guestfs_preview_sentinel.snapshot()
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "clean", "--dry-run", "--force", "--json").stdout,
            dict,
        )
        assert guestfs_preview_sentinel.snapshot() == sentinel_before
        assert selected_cache_case.snapshot() == before
        assert data["cache_dir"] == str(selected_cache_case.cache_dir)
        assert data["cache_dir_removed"] is True  # Planned removal, not an actual deletion.
        assert "warm_images" in data["prune_result"]["pruned_ids"]
        assert "guestfs_state" not in data["prune_result"]["pruned_ids"]
        assert not data["prune_result"]["failed_ids"]
        assert data["prune_result"]["had_running_vms"] is False

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    @pytest.mark.tier2
    @pytest.mark.destructive
    def test_cache_clean_refuses_with_running_vm(
        self, runner_vm, owned_cache_prune, owned_cache_preservation, tmp_path,
        monkeypatch, record_property,
    ):
        """Root host cleanup and cache's process scan independently refuse the live VM."""
        case = owned_cache_prune
        vm = case.create_vm()
        assert case.check_bindings("vm", vm["id"])["status"] == "running"
        boot_before = case.guest_boot_id(vm)
        runtime_before = case.vm_runtime(vm)
        namespace_before = case.vm_namespace(vm)
        live_pid = runtime_before[0]
        live_comm = _guest_run(runner_vm, f"sudo -n cat /proc/{live_pid}/comm").stdout.strip()
        assert live_comm == "firecracker", live_comm
        cache_b = (tmp_path / "unregistered-clean-cache").resolve()
        config_b = (tmp_path / "unregistered-clean-config").resolve()
        cache_b.mkdir(mode=0o700)
        config_b.mkdir(mode=0o700)
        other = _SelectedCacheCase(runner_vm, cache_b, config_b)
        with monkeypatch.context() as selection:
            selection.setenv("MVM_CACHE_DIR", str(cache_b))
            selection.setenv("MVM_CONFIG_DIR", str(config_b))
            selection.setenv("MVM_TEMP_DIR", str(cache_b / "tmp"))
            selection.delenv("MVM_WARM_POOL", raising=False)
            try:
                other.prepare()
                before = other.snapshot()
                assert not before[0]["vm"], "The live VM must be absent from B's SQLite projection"
                host_result = _run_mvm(runner_vm, "host", "clean", "--json", check=False)
                assert host_result.returncode != 0, host_result.stdout
                host_envelope = json.loads(host_result.stdout)
                assert host_envelope["schema_version"] == 1 and host_envelope["status"] == "error", host_envelope
                assert host_envelope["error"]["code"] == "cli.failed", host_envelope
                assert host_envelope["error"]["message"] == f"VMs still running: {vm['id']}", host_envelope
                assert other.snapshot() == before
                # Cache clean's earlier legacy comm scanner is a separate refusal,
                # not the root-owner inventory contract exercised above.
                result = _run_mvm(
                    runner_vm, "cache", "clean", "--force", "--json", check=False, timeout=180
                )
                assert result.returncode != 0, result.stdout
                envelope = json.loads(result.stdout)
                assert envelope["schema_version"] == 1 and envelope["status"] == "partial", envelope
                assert envelope["error"]["code"] == "cache.clean_failed", envelope
                assert envelope["error"]["message"] == (
                    f"Orphan process(es) still running (PID(s) {live_pid}: {live_comm}). "
                    "Kill them manually and re-run ``mvm clean``."
                ), envelope
                data = envelope["data"]
                assert data["cache_dir"] == str(cache_b) and data["cache_dir_removed"] is False, data
                assert data["prune_result"]["pruned_ids"] == ["warm_images"], data
                assert not data["prune_result"]["failed_ids"]
                assert data["prune_result"]["had_running_vms"] is False
                after = other.snapshot()
                assert after[0] == before[0] and after[2] == before[2]
                consumed = {str(cache_b / "bin"), str(cache_b / "tmp" / "ready" / "preview-sentinel")}
                assert after[1] == {
                    path: receipt for path, receipt in before[1].items() if path not in consumed
                }
                record_property("cache_process_scan_refusal", json.dumps(envelope, sort_keys=True))
                record_property("root_owner_host_clean_refusal", json.dumps(host_envelope, sort_keys=True))
            finally:
                failures = other.cleanup()
                assert not failures, "Refused cache B teardown failed:\n" + "\n".join(failures)
        assert case.check_bindings("vm", vm["id"])["status"] == "running"
        assert case.guest_boot_id(vm) == boot_before
        assert case.vm_runtime(vm) == runtime_before
        assert case.vm_namespace(vm) == namespace_before
        owned_cache_preservation.assert_preserved()
        record_property("refusal_live_vm", json.dumps({
            "vm_id": vm["id"], "boot_id": boot_before, "runtime": runtime_before,
            "namespace": [str(namespace_before[0]), namespace_before[1]],
        }, sort_keys=True))


class TestCachePruneActual:
    """Test actual (non-dry-run) cache prune operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_cache,
    ]

    def test_cache_prune_misc_actual(
        self, runner_vm, selected_cache_case, guestfs_preview_sentinel,
        unrelated_guestfish_process, record_property,
    ):
        """Misc prune removes selected artifacts and preserves unrelated GuestFS state."""
        selected_cache_case.prepare()
        cache = selected_cache_case.cache_dir
        appliance = cache / "appliance"
        appliance.mkdir(mode=0o700)
        artifacts = [appliance / name for name in ("kernel", "initrd", "root")]
        artifacts.append(cache / "bin" / "owned-service-artifact")
        for path in artifacts:
            with path.open("xb") as output:
                output.write(b"selected-cache-owned-misc-artifact\n")
        warm = cache / "tmp" / "ready" / "preview-sentinel"
        assert warm.read_bytes() == b"cache-preview-must-preserve-this-file\n"
        before = selected_cache_case.snapshot()
        global_before = guestfs_preview_sentinel.snapshot()
        process_before = unrelated_guestfish_process.snapshot()
        record_property("misc_global_appliance_before", json.dumps(global_before, default=bytes.hex))
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "misc", "--force", "--json").stdout,
            dict,
        )
        record_property("misc_prune_result", json.dumps(data, sort_keys=True))
        # Observe both independently so one illicit global mutation cannot hide
        # the other regression in the old binary's failure output.
        failures = []
        for kind, sentinel, expected in (
            ("global appliance", guestfs_preview_sentinel, global_before),
            ("unrelated process", unrelated_guestfish_process, process_before),
        ):
            try:
                assert sentinel.snapshot() == expected
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"{kind} preservation: {error!r}")
        assert not failures, "Misc prune crossed the selected cache boundary:\n" + "\n".join(failures)
        assert data["service_binaries"] is True
        assert data["appliance"] is True
        assert data["warm_images"] is True
        assert data.get("guestfs_state", False) is False
        assert data["stale_provision_mounts"] is False
        removed_paths = (*artifacts, appliance, cache / "bin", warm)
        for path in removed_paths:
            with pytest.raises(FileNotFoundError):
                path.lstat()
        after = selected_cache_case.snapshot()
        assert after[0] == before[0]
        assert after[2] == before[2]
        removed = {str(path) for path in removed_paths}
        expected_files = {path: value for path, value in before[1].items() if path not in removed}
        assert after[1] == expected_files
        record_property("misc_selected_artifacts_removed", json.dumps(sorted(removed)))
        record_property(
            "misc_global_appliance_after", json.dumps(guestfs_preview_sentinel.snapshot(), default=bytes.hex)
        )
        record_property("misc_process_after", json.dumps(unrelated_guestfish_process.snapshot(), default=bytes.hex))

    @pytest.mark.destructive
    def test_cache_prune_misc_with_force(
        self, runner_vm, selected_cache_case, selected_cache_preservation, record_property
    ):
        """Forced misc prune consumes exact artifacts and an empty retry succeeds."""
        case = selected_cache_case
        case.prepare()
        artifacts = _CacheMiscArtifacts(case)
        artifacts.assert_bound()
        before = case.snapshot()
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "misc", "--force", "--json").stdout,
            dict,
        )
        assert data == {
            "service_binaries": True, "appliance": True, "warm_images": True,
            "guestfs_state": False, "stale_provision_mounts": False,
        }, data
        artifacts.assert_absent()
        after = case.snapshot()
        assert after[0] == before[0] and after[2] == before[2]
        removed = {str(path) for path in (*artifacts.files, *artifacts.directories)}
        assert after[1] == {path: receipt for path, receipt in before[1].items() if path not in removed}
        selected_cache_preservation.assert_preserved()
        retry = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "misc", "--force", "--json").stdout,
            dict,
        )
        # GetBinDir creates its fixed directory even when no service file remains.
        assert retry == {
            "service_binaries": True, "appliance": False, "warm_images": False,
            "guestfs_state": False, "stale_provision_mounts": False,
        }, retry
        artifacts.assert_absent()
        assert case.snapshot() == after
        selected_cache_preservation.assert_preserved()
        record_property("misc_force_results", json.dumps([data, retry], sort_keys=True))


# ============================================================================
# Non-dry-run prune tests (serial / destructive state changes)
# ============================================================================


class TestCachePruneNonDryRun:
    """Test actual (non-dry-run) cache prune operations for specific resources.

    These tests modify shared cache state and MUST run serially.
    They are placed at the end of the file to avoid interfering with
    non-destructive cache tests.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cache,
    ]

    @pytest.mark.destructive
    def test_cache_prune_network_non_dry_run(self, runner_vm, owned_cache_prune):
        """Prune only the privately registered network and its exact bridge."""
        case = owned_cache_prune
        network = case.create_network()
        case.check_bindings("network", network["id"])
        removed = _json_success_data(
            _run_mvm(
                runner_vm,
                "cache",
                "prune",
                "network",
                "--force",
                "--json",
            ).stdout,
            list,
        )
        assert removed == [network["name"]], removed
        case.assert_absent("network", network["id"])

    @pytest.mark.destructive
    def test_cache_prune_kernel_non_dry_run(self, runner_vm, owned_cache_prune):
        """Prune an imported kernel while retaining the exact owned default."""
        case = owned_cache_prune
        protected = case.import_kernel("6.1", default=True)
        target = case.import_kernel("6.2")
        before = case.check_bindings("kernel", protected["id"])
        case.check_bindings("kernel", target["id"])
        removed = _json_success_data(
            _run_mvm(
                runner_vm,
                "cache",
                "prune",
                "kernel",
                "--force",
                "--json",
            ).stdout,
            list,
        )
        assert removed == [target["id"]], removed
        case.assert_absent("kernel", target["id"])
        assert _prune_row_identity(
            "kernel", case.check_bindings("kernel", protected["id"])
        ) == (_prune_row_identity("kernel", before))
        assert set(case.inventory("kernel")) == {protected["id"]}
        assert case.inventory("kernel")[protected["id"]]["is_default"] is True

    @pytest.mark.destructive
    def test_cache_prune_binary_non_dry_run(
        self, runner_vm, owned_cache_prune, owned_cache_release, owned_cache_preservation,
        record_property,
    ):
        """Prune one newly installed non-default root release, retaining the borrowed pair."""
        case = owned_cache_prune
        pair = owned_cache_release.install()
        before = case.inventory("bin")
        assert {row["id"] for row in pair}.issubset(before)
        owned_cache_release.assert_bound()
        removed = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "binary", "--force", "--json").stdout,
            list,
        )
        assert removed == [owned_cache_release.version], removed
        owned_cache_release.assert_absent()
        after = case.inventory("bin")
        assert after == {item_id: row for item_id, row in before.items() if row["version"] == "1.16.2"}
        assert len(after) == 2 and all(row["is_default"] for row in after.values()), after
        owned_cache_preservation.assert_preserved()
        record_property("owned_release_pruned", json.dumps({
            "version": owned_cache_release.version,
            "pair_ids": sorted(row["id"] for row in pair),
            "root_receipt": owned_cache_release.receipt,
        }, sort_keys=True))

    @pytest.mark.destructive
    def test_cache_prune_image_all_non_dry_run(self, runner_vm, owned_cache_prune):
        """Image-only --all removes both owned backing files, including the default."""
        case = owned_cache_prune
        protected = case.import_image(default=True)
        target = case.import_image()
        for item in (protected, target):
            case.check_bindings("image", item["id"])
        removed = _json_success_data(
            _run_mvm(
                runner_vm,
                "cache",
                "prune",
                "image",
                "--all",
                "--force",
                "--json",
            ).stdout,
            list,
        )
        assert set(removed) == {protected["id"], target["id"]} and len(removed) == 2, (
            removed
        )
        for item in (protected, target):
            case.assert_absent("image", item["id"])
        assert not case.inventory("image")


# ============================================================================
# Full prune --all (most destructive — runs LAST in file order)
# ============================================================================


class TestCachePruneAll:
    """Test cache prune --all (most destructive — runs last in file order)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cache,
    ]

    @pytest.mark.tier2
    @pytest.mark.requires_kvm
    @pytest.mark.destructive
    def test_cache_prune_all_non_dry_run(
        self, runner_vm, owned_cache_prune, owned_cache_release, owned_cache_preservation,
        record_property,
    ):
        """Aggregate prune removes only owned resources and retains default/root history."""
        case = owned_cache_prune
        vm = case.create_vm()
        _run_mvm(runner_vm, "vm", "stop", vm["id"], "--json", timeout=120)
        assert case.check_bindings("vm", vm["id"])["status"] == "stopped"
        network = case.create_network()
        owned_cache_release.install()
        artifacts = _CacheMiscArtifacts(case)
        artifacts.assert_bound()
        protected = case.cache_dir / ".mvm-protected"
        history_before = _cache_root_directory_receipt(runner_vm, protected)
        for domain, item_id in (
            ("vm", vm["id"]), ("network", network["id"]),
            ("image", vm["image_id"]), ("kernel", vm["kernel_id"]),
        ):
            case.check_bindings(domain, item_id)
        expected = {
            vm["name"], network["name"], vm["image_id"], vm["kernel_id"],
            owned_cache_release.version, "appliance",
        }
        owned_cache_release.assert_bound()
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "prune", "--all", "--force", "--json", timeout=180).stdout,
            dict,
        )
        assert set(data["pruned_ids"]) == expected and len(data["pruned_ids"]) == len(expected), data
        assert not data["failed_ids"] and data["had_running_vms"] is False, data
        for domain, item_id in (
            ("vm", vm["id"]), ("network", network["id"]),
            ("image", vm["image_id"]), ("kernel", vm["kernel_id"]),
        ):
            case.assert_absent(domain, item_id)
            assert not case.inventory(domain), domain
        owned_cache_release.assert_absent()
        artifacts.assert_absent()
        assert _cache_root_directory_receipt(runner_vm, protected) == history_before
        binaries = case.inventory("bin")
        assert len(binaries) == 2 and all(
            row["version"] == "1.16.2" and row["is_default"] and row["is_present"]
            for row in binaries.values()
        ), binaries
        owned_cache_preservation.assert_preserved()
        record_property("aggregate_owned_prune", json.dumps(data, sort_keys=True))
        record_property("retained_protected_store", history_before)


# ============================================================================
# Destructive tests — must run last, run serially
# ============================================================================


@pytest.mark.destructive
class TestZzzDestructive:
    """Destructive tests — clean cache state; run last and serially.

    Migrated from tests/e2e/zzz_destructive/test_zzz_destructive.py.

    Violations fixed:
      - pytest.skip() removed → hard assertion failure
      - import from tests.system.conftest
      - commands through _run_mvm inside VM (were already correct)
    """

    pytestmark = [
        pytest.mark.destructive,
        pytest.mark.system,
        pytest.mark.domain_cache,
        pytest.mark.slow,
    ]

    def test_cache_clean_actual(
        self, runner_vm, owned_cache_prune, owned_cache_preservation, record_property
    ) -> None:
        """Clean an unprotected key/misc cache, then recreate usable local state."""
        case = owned_cache_prune
        keys = _SelectedCacheCase(runner_vm, case.cache_dir, case.config_dir)
        try:
            keys.prepare()
            case.reuse_release()
            artifacts = _CacheMiscArtifacts(case)
            artifacts.assert_bound()
            _require_unprotected_clean_case(case)
            key_before = keys.snapshot()[0]["key"]
            config_before = _cache_config_receipt(case)
            data = _json_success_data(
                _run_mvm(runner_vm, "cache", "clean", "--force", "--json", timeout=180).stdout,
                dict,
            )
            artifacts.assert_absent()
            owned_cache_preservation.assert_preserved()
            _reinitialize_cleaned_cache(case, data, {"appliance", "warm_images"}, config_before)
            assert not case.inventory("key")
            for row in key_before:
                for field in ("public_key_path", "private_key_path"):
                    with pytest.raises(FileNotFoundError):
                        Path(row[field]).lstat()
            keys.prepare()
            case.reuse_release()
            recovered = keys.snapshot()[0]["key"]
            assert len(recovered) == 1 and recovered[0]["id"] not in {row["id"] for row in key_before}
            owned_cache_preservation.assert_preserved()
            record_property("clean_key_recovery", json.dumps({
                "clean": data, "removed_keys": [row["id"] for row in key_before],
                "recovered_key": recovered[0]["id"],
            }, sort_keys=True))
        finally:
            failures = keys.cleanup()
            assert not failures, "Clean key teardown failed:\n" + "\n".join(failures)


class TestCacheEdgeCases:
    """Tests for cache command edge cases (destructive — must be last)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_cache,
        pytest.mark.destructive,
    ]

    def test_cache_prune_no_args(self, runner_vm, selected_cache_case):
        """``cache prune`` without resource and without --all should fail."""
        # Rationale: No resources needed — testing CLI validation for
        # missing arguments. Non-destructive.
        selected_cache_case.prepare()
        before = selected_cache_case.snapshot()
        guidance = _run_mvm(runner_vm, "cache", "prune", check=False)
        assert guidance.returncode != 0
        assert "No resource specified" in guidance.stdout + guidance.stderr
        result = _run_mvm(runner_vm, "cache", "prune", "--json", check=False)
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert envelope["error"]["message"] == "no resource specified"
        assert selected_cache_case.snapshot() == before

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cache_prune_vm_no_dry_run(self, runner_vm, owned_cache_prune):
        """VM-only prune removes a stopped owned VM, preserving its imported assets."""
        case = owned_cache_prune
        vm = case.create_vm()
        _run_mvm(runner_vm, "vm", "stop", vm["id"], "--json", timeout=120)
        stopped = case.check_bindings("vm", vm["id"])
        assert stopped["status"] == "stopped", stopped
        data = _json_success_data(
            _run_mvm(
                runner_vm,
                "cache",
                "prune",
                "vm",
                "--force",
                "--json",
            ).stdout,
            dict,
        )
        assert len(data["items"]) == 1, data
        result = data["items"][0]["result"]
        assert result["status"] == "success" and result["code"] == "cache.pruned", (
            result
        )
        assert result["item"] == [vm["name"]], result
        case.assert_absent("vm", vm["id"])
        for domain in ("kernel", "image"):
            row = case.check_bindings(domain, vm[f"{domain}_id"])
            assert row["is_default"] is True and row["is_present"] is True, row
        assert case.release_identity(case.release[0]) == case.release[1]


class TestCacheCleanActual:
    """Test actual cache clean — destroys and restores state (destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_cache,
        pytest.mark.destructive,
        pytest.mark.slow,
    ]

    def test_cache_clean_actual(
        self, runner_vm, owned_cache_prune, owned_cache_preservation, record_property
    ) -> None:
        """Clean owned unprotected assets/network and recover from exact local sources."""
        case = owned_cache_prune
        case.reuse_release()
        kernel = case.import_kernel("before-clean", default=True)
        image = case.import_image(default=True)
        network = case.create_network()
        artifacts = _CacheMiscArtifacts(case)
        artifacts.assert_bound()
        _require_unprotected_clean_case(case)
        config_before = _cache_config_receipt(case)
        expected = {kernel["id"], image["id"], network["name"], "appliance"}
        data = _json_success_data(
            _run_mvm(runner_vm, "cache", "clean", "--force", "--json", timeout=180).stdout,
            dict,
        )
        artifacts.assert_absent()
        owned_cache_preservation.assert_preserved()
        _reinitialize_cleaned_cache(case, data, expected, config_before)
        case.reuse_release()
        recovered_kernel = case.import_kernel("after-clean", default=True)
        recovered_image = case.import_image(default=True)
        recovered_network = case.create_network()
        assert recovered_kernel["id"] != kernel["id"]
        assert recovered_image["id"] != image["id"]
        assert recovered_network["id"] != network["id"], "Removal tombstones must not be resurrected"
        for domain, row in (
            ("kernel", recovered_kernel), ("image", recovered_image), ("network", recovered_network),
        ):
            case.check_bindings(domain, row["id"])
        # The recovered owned network intentionally changes only managed firewall state.
        assert _cache_release_receipt(case, "1.16.2") == owned_cache_preservation.before[2]
        assert owned_cache_preservation.appliance.snapshot() == owned_cache_preservation.before[0]
        assert owned_cache_preservation.process.snapshot() == owned_cache_preservation.before[1]
        record_property("clean_asset_recovery", json.dumps({
            "clean": data, "recovered_kernel": recovered_kernel["id"],
            "recovered_image": recovered_image["id"], "recovered_network": recovered_network["id"],
        }, sort_keys=True))
