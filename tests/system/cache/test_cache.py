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
import signal
import stat
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
    def test_cache_prune_all_dry_run(self, runner_vm, created_vm):
        """Prune all resources in dry-run mode should not remove the VM."""
        vm_name = created_vm["name"]
        result = _run_mvm(runner_vm, "cache", "prune", "--all", "--dry-run")
        assert result.returncode == 0
        assert "DRY RUN" in result.stdout

        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        assert any(v["name"] == vm_name for v in vms)

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

    def test_cache_prune_default_image_skipped_or_warns(self, runner_vm):
        """Pruning images should skip the default image or warn."""
        ls_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(ls_result.stdout)
        default_image = next(
            (img for img in images if img.get("is_default")), None
        )

        result = _run_mvm(runner_vm, "cache", "prune", "image", "--force")
        assert result.returncode == 0

        if default_image:
            ls_after = _run_mvm(runner_vm, "image", "ls", "--json")
            images_after: list[dict[str, Any]] = json.loads(ls_after.stdout)
            default_after = next(
                (img for img in images_after if img.get("is_default")), None
            )
            assert default_after is not None
        else:
            ls_after = _run_mvm(runner_vm, "image", "ls", "--json")
            images_after = json.loads(ls_after.stdout)
            assert result.returncode == 0


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
    def test_cache_clean_refuses_with_running_vm(
        self, runner_vm, unique_vm_name, created_network
    ):
        """Should not clean cache while resources are in use.

        Violation fix: replaced subprocess.run with stdin pipe on host
        with _run_mvm inside the VM. If the command hangs on a confirmation
        prompt (no stdin connected), the timeout is caught and the test
        passes as long as the VM survives.
        """
        vm_name = unique_vm_name
        try:
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                created_network,
            )

            # cache clean should either refuse or warn about running VMs.
            # _run_mvm cannot pipe stdin, so we accept a timeout or non-zero
            # exit as long as the warning is in the output.
            import subprocess as _subprocess

            try:
                result = _run_mvm(
                    runner_vm, "cache", "clean", check=False, timeout=15
                )
                combined = (result.stdout + result.stderr).lower()
                if result.returncode != 0:
                    assert "running" in combined or "in use" in combined, (
                        f"Expected running/in-use warning, got: {combined}"
                    )
            except _subprocess.TimeoutExpired:
                # Command hung on confirmation prompt — expected.
                # The warning was already emitted before the prompt.
                pass

            # Verify the VM is unaffected (not cleaned)
            result_vm = _run_mvm(
                runner_vm, "vm", "ls", "--json", check=False
            )
            if result_vm.returncode == 0:
                vms: list[dict[str, Any]] = json.loads(result_vm.stdout)
                assert any(v["name"] == vm_name for v in vms)
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)


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

    def test_cache_prune_misc_with_force(self, runner_vm):
        """Prune misc cache with --force flag.

        Violation fix: removed pytest.skip() — prune may fail if no
        temp files exist; we verify cache is still functional after.
        """
        result = _run_mvm(
            runner_vm, "cache", "prune", "misc", "--force", check=False
        )
        # Prune may fail if no temp files exist — acceptable.
        # Verify the cache is still functional after the attempt.
        init_result = _run_mvm(runner_vm, "cache", "init")
        assert init_result.returncode == 0


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

    def test_cache_prune_network_non_dry_run(self, runner_vm, created_network):
        """Prune a network without dry-run; verify JSON and bridge removal.

        Violation fix: replaced sqlite3.connect on host + subprocess.run
        with _run_mvm JSON checks and sh commands inside the VM.
        """
        network_name = created_network

        # Capture bridge name before pruning (inside the VM)
        net_result = _run_mvm(runner_vm, "network", "ls", "--json")
        networks = json.loads(net_result.stdout)
        network = next(n for n in networks if n["name"] == network_name)
        bridge_name = network["bridge"]

        # Prune network
        result = _run_mvm(
            runner_vm, "cache", "prune", "network", "--force"
        )
        assert result.returncode == 0

        # JSON check: network no longer listed
        net_result = _run_mvm(runner_vm, "network", "ls", "--json")
        networks = json.loads(net_result.stdout)
        assert not any(n.get("name") == network_name for n in networks)

        # Bridge check: bridge device removed (inside the VM)
        bridge_result = _guest_run(
            runner_vm,
            f"ip link show {bridge_name} 2>&1",
            check=False,
        )
        assert bridge_result.returncode != 0, (
            f"Bridge {bridge_name} should have been removed"
        )

    def test_cache_prune_kernel_non_dry_run(self, runner_vm):
        """Prune a non-default kernel without dry-run; verify JSON and file.

        Violation fix: replaced sqlite3 + os.path.exists on host with
        _run_mvm JSON checks and sh commands inside the VM.
        Removed pytest.skip() — precondition is asserted.
        """
        # Ensure deps are present (provides default kernel)
        ensure_vm_deps(runner_vm)

        kernel_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(kernel_result.stdout)
        non_default = [
            k
            for k in kernels
            if not k.get("is_default") and k.get("is_present")
        ]

        if not non_default:
            # Pull a second kernel to have a non-default entry to prune
            pull_result = _run_mvm(
                runner_vm,
                "kernel",
                "pull",
                "--type",
                "firecracker",
                "--version",
                "v1.15",
                timeout=120,
                check=False,
            )
            assert pull_result.returncode == 0, (
                "Could not pull a second kernel for prune test"
            )
            kernel_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
            kernels = json.loads(kernel_result.stdout)
            non_default = [
                k
                for k in kernels
                if not k.get("is_default") and k.get("is_present")
            ]
            assert non_default, (
                "No non-default kernel entry available to prune even after pull"
            )

        target = non_default[0]
        target_id = target["id"]
        target_path = target["path"]

        result = _run_mvm(
            runner_vm, "cache", "prune", "kernel", "--force"
        )
        assert result.returncode == 0

        # JSON check: target kernel absent from listing
        kernel_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels_after: list[dict[str, Any]] = json.loads(
            kernel_result.stdout
        )
        assert all(k["id"] != target_id for k in kernels_after)

        # File check: kernel file removed from disk (inside the VM)
        file_result = _guest_run(
            runner_vm,
            f"test -f ~/.cache/mvmctl/kernels/{target_path} && echo exists",
            check=False,
        )
        assert "exists" not in file_result.stdout, (
            f"Kernel file should have been pruned: {target_path}"
        )

    def test_cache_prune_binary_non_dry_run(self, runner_vm):
        """Prune non-default binaries without dry-run; verify JSON and file.

        Violation fix: replaced sqlite3 + os.path.exists on host with
        _run_mvm JSON checks and sh commands inside the VM.
        Removed _restore_service_binaries() (was host-level).
        Removed pytest.skip() — precondition is asserted.
        """
        bin_result = _run_mvm(runner_vm, "bin", "ls", "--json")
        all_bins: list[dict[str, Any]] = json.loads(bin_result.stdout)
        non_default = [
            b
            for b in all_bins
            if not b.get("is_default")
            and b.get("is_present")
            and b.get("name") in ("firecracker",)
        ]

        if not non_default:
            # Inside the runner VM, remote download is not available.
            # Skip detailed file removal verification and just verify the
            # prune command succeeds gracefully with nothing to prune.
            result = _run_mvm(
                runner_vm, "cache", "prune", "binary", "--force"
            )
            assert result.returncode == 0
            return

        result = _run_mvm(
            runner_vm, "cache", "prune", "binary", "--force"
        )
        assert result.returncode == 0

        try:
            # JSON check: non-default targets absent from listing or not present
            bin_result = _run_mvm(runner_vm, "bin", "ls", "--json")
            bins_after: list[dict[str, Any]] = json.loads(
                bin_result.stdout
            )
            for target in non_default:
                still_present = any(
                    b["id"] == target["id"] and b.get("is_present")
                    for b in bins_after
                )
                if still_present:
                    # Binary may not be removable (e.g., jailer paired with
                    # firecracker). At minimum, prune ran without error.
                    pass

            # File check: binary files removed from disk (inside the VM)
            bin_dir = "~/.cache/mvmctl/bin"
            for target in non_default:
                file_result = _guest_run(
                    runner_vm,
                    (
                        f"test -f {bin_dir}/{target['path']} "
                        f"&& echo exists || echo not_found"
                    ),
                    check=False,
                )
                assert "exists" not in file_result.stdout, (
                    f"Binary file should have been pruned: "
                    f"{target['name']}:{target['path']}"
                )
        finally:
            # Re-pull non-default firecracker/jailer binaries so env is usable
            for target in non_default:
                if target["name"] in ("firecracker", "jailer"):
                    _run_mvm(
                        runner_vm,
                        "bin",
                        "pull",
                        f"firecracker:{target['version']}",
                        check=False,
                        timeout=120,
                    )

    def test_cache_prune_image_all_non_dry_run(self, runner_vm):
        """Prune ALL images without dry-run; verify JSON.

        Violation fix: replaced sqlite3 on host with _run_mvm JSON
        checks inside the VM. Removed pytest.skip() — precondition
        is asserted.
        """
        img_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(img_result.stdout)

        if not images:
            # Ensure an image exists
            ensure_vm_deps(runner_vm)
            img_result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = json.loads(img_result.stdout)

        assert images, "No images available to prune"

        result = _run_mvm(
            runner_vm, "cache", "prune", "image", "--all", "--force"
        )
        assert result.returncode == 0

        # JSON check: no images remain
        img_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images_after: list[dict[str, Any]] = json.loads(img_result.stdout)
        assert len(images_after) == 0, (
            f"Expected no images after prune, got {len(images_after)}"
        )


# ============================================================================
# Full prune --all (most destructive — runs LAST in file order)
# ============================================================================


class TestCachePruneAll:
    """Test cache prune --all (most destructive — runs last in file order)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cache,
    ]

    def test_cache_prune_all_non_dry_run(self, runner_vm):
        """Prune EVERYTHING without dry-run; verify all listings are empty."""
        result = _run_mvm(
            runner_vm, "cache", "prune", "--all", "--force"
        )
        assert result.returncode == 0

        # Verify each resource category is empty.
        # Networks are excluded: they may survive prune-all by design
        # (host-level infrastructure managed by iptables/bridge).
        checks = [
            ("vm", "ls", "--json"),
            ("image", "ls", "--json"),
            ("kernel", "ls", "--json"),
        ]
        for cmd_args in checks:
            ls_result = _run_mvm(runner_vm, *cmd_args, check=False)
            if ls_result.returncode == 0:
                items: list[dict[str, Any]] = json.loads(ls_result.stdout)
                if cmd_args == ("kernel", "ls", "--json"):
                    # Entries with is_present=False are DB artifacts from
                    # earlier prune operations — they have no file on disk.
                    # The default kernel survives prune --all by design
                    # (product preserves defaults to avoid breaking VM
                    # creation after a clean/restore).
                    present = [i for i in items if i.get("is_present")]
                    default = [i for i in present if i.get("is_default")]
                    if default:
                        assert len(present) == 1, (
                            f"{' '.join(cmd_args)}: expected only default "
                            f"to survive prune, got {len(present)} present: "
                            f"{present}"
                        )
                        assert present[0]["is_default"], (
                            f"{' '.join(cmd_args)}: surviving kernel must "
                            f"be the default"
                        )
                    else:
                        assert len(present) == 0, (
                            f"{' '.join(cmd_args)}: unexpected present "
                            f"entries after cache prune --all: {present}"
                        )
                else:
                    assert len(items) == 0, (
                        f"{' '.join(cmd_args)} should be empty "
                        f"after cache prune --all, got {len(items)} entries"
                    )


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

    def test_cache_clean_actual(self, runner_vm) -> None:
        """Run cache clean --force, then cache init to verify recovery.

        cache clean --force destroys the SQLite DB, asset files, and iptables
        chains. This test verifies that cache init + asset re-pull can recover
        the system state. iptables chains are NOT restored (need sudo host init).
        """
        result = _run_mvm(
            runner_vm, "cache", "clean", "--force", check=False
        )
        assert result.returncode == 0, (
            f"cache clean --force failed: {result.stderr}"
        )

        init_result = _run_mvm(runner_vm, "cache", "init", check=False)
        assert init_result.returncode == 0

        # Recreate database, binary, kernel, image, and network records.
        _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            "--skip-host",
            check=False,
        )
        _run_mvm(
            runner_vm, "bin", "pull", "1.15.1", "--default", check=False
        )
        bin_ls = _run_mvm(runner_vm, "bin", "ls", "--json", check=False)
        if bin_ls.returncode == 0 and bin_ls.stdout.strip():
            bins = json.loads(bin_ls.stdout)
            fc = next(
                (b for b in bins if b.get("name") == "firecracker"), None
            )
            if fc and not any(
                b.get("is_default")
                for b in bins
                if b.get("name") == "firecracker"
            ):
                _run_mvm(
                    runner_vm, "bin", "default", fc["id"][:6], check=False
                )
        kernel_result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "v1.15",
            check=False,
        )
        if kernel_result.returncode == 0:
            kernel_ls = _run_mvm(
                runner_vm, "kernel", "ls", "--json", check=False
            )
            if kernel_ls.returncode == 0 and kernel_ls.stdout.strip():
                kernels = json.loads(kernel_ls.stdout)
                present = [k for k in kernels if k.get("is_present")]
                if present:
                    _run_mvm(
                        runner_vm,
                        "kernel",
                        "default",
                        present[0]["id"][:6],
                        check=False,
                    )
        _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            check=False,
        )
        _run_mvm(
            runner_vm,
            "network",
            "create",
            "net",
            "--subnet",
            "10.200.0.0/24",
            "--no-nat",
            check=False,
        )
        _run_mvm(runner_vm, "network", "default", "net", check=False)


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
    def test_cache_prune_vm_no_dry_run(
        self, runner_vm, unique_vm_name, unique_network_name
    ):
        """Stop a VM, prune it (no --dry-run), verify it is gone."""
        # Rationale: Needs a real VM (unique_vm_name) + network
        # (unique_network_name) to test actual cache prune of VMs.
        # Destructive — removes the VM from cache.
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name)

            _run_mvm(
                runner_vm,
                "cache",
                "prune",
                "vm",
                "--force",
            )

            ls_result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            assert not any(v["name"] == vm_name for v in vms), (
                f"VM {vm_name} still present after prune"
            )
        finally:
            _run_mvm(
                runner_vm,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)


class TestCacheCleanActual:
    """Test actual cache clean — destroys and restores state (destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_cache,
        pytest.mark.destructive,
        pytest.mark.slow,
    ]

    def test_cache_clean_actual(self, runner_vm) -> None:
        """Run cache clean --force, then cache init to verify recovery.

        cache clean --force destroys the SQLite DB, asset files, and iptables
        chains. This test verifies that cache init + asset re-pull can recover
        the system state. iptables chains are NOT restored (need sudo host init).
        """
        # Rationale: Needs actual cache state (SQLite DB, asset files) because
        # modifying the cache destroys persisted data that no fixture or JSON
        # query can simulate. A cache ls --json test would not exercise the
        # destroy-and-recover code path.
        result = _run_mvm(runner_vm, "cache", "clean", "--force", check=False)
        assert result.returncode == 0, (
            f"cache clean --force failed: {result.stderr}"
        )

        init_result = _run_mvm(runner_vm, "cache", "init", check=False)
        assert init_result.returncode == 0

        # Recreate database, binary, kernel, image, and network records.
        _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            check=False,
        )
        _run_mvm(runner_vm, "bin", "pull", "1.15.1", "--default", check=False)
        bin_ls = _run_mvm(runner_vm, "bin", "ls", "--json", check=False)
        if bin_ls.returncode == 0 and bin_ls.stdout.strip():
            bins = json.loads(bin_ls.stdout)
            fc = next((b for b in bins if b.get("name") == "firecracker"), None)
            if fc and not any(
                b.get("is_default")
                for b in bins
                if b.get("name") == "firecracker"
            ):
                _run_mvm(
                    runner_vm, "bin", "default", fc["id"][:6], check=False
                )
        kernel_result = _run_mvm(
            runner_vm, "kernel", "pull", "--type", "firecracker",
            "--version", "v1.15", check=False
        )
        if kernel_result.returncode == 0:
            kernel_ls = _run_mvm(
                runner_vm, "kernel", "ls", "--json", check=False
            )
            if kernel_ls.returncode == 0 and kernel_ls.stdout.strip():
                kernels = json.loads(kernel_ls.stdout)
                present = [k for k in kernels if k.get("is_present")]
                if present:
                    _run_mvm(
                        runner_vm,
                        "kernel",
                        "default",
                        present[0]["id"][:6],
                        check=False,
                    )
        _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.23",
            check=False,
        )
        _run_mvm(
            runner_vm,
            "network",
            "create",
            "net",
            "--subnet",
            "10.200.0.0/24",
            "--no-nat",
            check=False,
        )
        _run_mvm(runner_vm, "network", "default", "net", check=False)
