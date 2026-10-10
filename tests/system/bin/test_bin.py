"""Firecracker binary management system tests."""

from __future__ import annotations

import hashlib
import json
import os
import platform
import re
import select
import stat
import subprocess
import tarfile
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import (
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
)

pytestmark = [pytest.mark.system, pytest.mark.domain_bin]


def _installed_pair(binaries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Require the preinstalled launch pair without repairing runner defaults."""
    pair = [item for item in binaries if item["is_default"]]
    assert len(pair) == 2, pair
    assert {item["type"] for item in pair} == {"firecracker", "jailer"}
    for item in pair:
        assert item["version"] == "1.17.0", item
        assert item["architecture"] == platform.machine(), item
        assert item["is_present"] is True, item
        assert re.fullmatch(r"[0-9a-f]{64}", item["id"]), item
        assert item["path"] == (
            f"/var/lib/mvmctl/binaries/{item['architecture']}/1.17.0/{item['type']}"
        ), item
    return pair


def _binary_identity(binaries: list[dict[str, Any]]) -> set[tuple]:
    """Compare release identities and defaults while allowing projection timestamps to refresh."""
    return {
        (item["id"], item["type"], item["architecture"], item["version"],
         item["path"], item["is_default"], item["is_present"])
        for item in binaries
    }


def _file_identity(path: Path) -> tuple:
    """Record exact regular files rather than following a substituted fixture leaf."""
    info = path.lstat()
    assert stat.S_ISREG(info.st_mode) and info.st_nlink == 1, path
    return (info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode,
            info.st_mtime_ns, info.st_ctime_ns, info.st_size)


# ============================================================================
# Read-only listing tests (no state modification)
# ============================================================================


class TestBinLifecycle:
    """Test Firecracker binary management operations."""

    def test_bin_list_cached(self, runner_vm):
        """List cached firecracker versions."""
        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        data: list[dict[str, Any]] = _json_success_data(result.stdout, list)
        for entry in data:
            assert isinstance(entry, dict), entry
            assert isinstance(entry.get("version"), str) and entry["version"], entry
            assert isinstance(entry.get("id"), str) and entry["id"], entry
            assert isinstance(entry.get("is_present"), bool), entry
        _installed_pair(data)


# ============================================================================
# Edge cases (error paths and mirrored pull; no present-release removal)
# ============================================================================


class TestBinaryEdges:
    """Binary management edge case tests.

    The exact absent-slot removal is a typed no-op; pull cases use mirrored
    assets. Present-release removal is exercised separately below.
    """

    def test_bin_ls_remote_limit_applies_to_json(self, runner_vm: str) -> None:
        """Remote release metadata is structured, and --limit bounds the JSON data."""
        default = _json_success_data(
            _run_mvm(runner_vm, "bin", "ls", "--remote", "--json", timeout=60).stdout,
            list,
        )
        assert len(default) > 3, "remote source must expose enough releases to prove --limit"
        limited = _json_success_data(
            _run_mvm(
                runner_vm, "bin", "ls", "--remote", "--limit", "3", "--json", timeout=60,
            ).stdout,
            list,
        )
        assert len(limited) == 3
        assert [item["version"] for item in limited] == [item["version"] for item in default[:3]]
        for item in limited:
            assert re.fullmatch(r"\d+\.\d+\.\d+", item["version"])
            assert item["type"] == "bin"
            assert item["name"] == "firecracker"

    def test_bin_pull_invalid_version_rejected_before_effects(self, runner_vm: str) -> None:
        """Malformed exact selectors fail locally without contacting the asset source."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        rejected = _run_mvm(runner_vm, "bin", "pull", "not-a-version", "--json", check=False)
        assert rejected.returncode != 0
        envelope = json.loads(rejected.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["error"]["code"] == "binary.pull_failed"
        assert "invalid version spec" in envelope["error"]["message"].lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

    def test_bin_default_missing_id_rejected_before_effects(self, runner_vm: str) -> None:
        """A missing binary ID leaves both default projections unchanged."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        rejected = _run_mvm(
            runner_vm, "bin", "default", "totally-nonexistent-binary", "--json", check=False,
        )
        assert rejected.returncode != 0
        envelope = json.loads(rejected.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["error"]["code"] == "binary.default_set_failed"
        assert "could be resolved" in envelope["error"]["message"].lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

    def test_bin_rm_removed_version_flag_rejected_before_effects(self, runner_vm: str) -> None:
        """A removed selector cannot alter any trusted release."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        rejected = _run_mvm(
            runner_vm, "bin", "rm", "--version", "999.999.999", check=False,
        )
        assert rejected.returncode != 0
        assert "unknown flag: --version" in (rejected.stdout + rejected.stderr).lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

    def test_bin_pull_removed_git_ref_rejected_before_effects(self, runner_vm: str) -> None:
        """The removed source-build selector must not change trusted-release state."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        rejected = _run_mvm(
            runner_vm, "bin", "pull", "1.16.0", "--git-ref", "main", check=False,
        )
        assert rejected.returncode != 0
        assert "unknown flag: --git-ref" in (rejected.stdout + rejected.stderr).lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

    def test_bin_rm_exact_absent_version_is_skipped(self, runner_vm: str) -> None:
        """Root-authoritative removal reports a typed no-op for an absent exact slot."""
        version = "999.999.999"
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert all(item["version"] != version for item in before)
        data = _json_success_data(
            _run_mvm(runner_vm, "bin", "rm", version, "--json").stdout,
            dict,
        )
        assert len(data["items"]) == 1
        item = data["items"][0]
        assert item["error"] is None
        assert item["result"]["status"] == "skipped"
        assert item["result"]["code"] == "binary.remove_absent"
        assert item["result"]["item"] == version
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before

    # ---- Destructive pull tests (still no rm) ----

    def test_bin_pull_present_exact_version_from_mirror(self, runner_vm: str) -> None:
        """An exact mirrored release can be re-admitted without changing its pair or default."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        prior_pair = _installed_pair(before)
        version = "1.17.0"
        prior_ids = {item["type"]: item["id"] for item in prior_pair}
        mirror = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser()
        archive = mirror / f"firecracker-v{version}-{platform.machine()}.tgz"
        archive_identity = _file_identity(archive)
        assert archive_identity[-1] > 0, archive

        pulled = _json_success_data(
            _run_mvm(runner_vm, "bin", "pull", version, "--json", timeout=300).stdout,
            list,
        )
        assert len(pulled) == 2
        assert {item["type"] for item in pulled} == {"firecracker", "jailer"}
        assert {item["version"] for item in pulled} == {version}
        assert all(item["is_present"] for item in pulled)
        assert {item["type"]: item["id"] for item in pulled} == prior_ids
        assert _binary_identity(pulled) == _binary_identity(prior_pair)
        assert _file_identity(archive) == archive_identity
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert _binary_identity(after) == _binary_identity(before)

# ============================================================================
# Pull, set-default, and remove (includes rm — last destructive section)
# ============================================================================


_BIN_RELEASE_RECEIVER = r'''
import hashlib, json, os, re, stat, sys

fds, parents = [], []
journal_baseline = None
directory_flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
file_flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC

def identity(info, directory=False):
    basic = [info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode]
    return basic if directory else basic + [info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns]

def unique_object(pairs):
    result = {}
    for key, value in pairs:
        assert key not in result, "duplicate root receipt field"
        result[key] = value
    return result

def open_directory(parent, name, managed=True):
    fd = os.open(name, directory_flags, dir_fd=parent)
    fds.append(fd)
    info = os.fstat(fd)
    assert info.st_uid == info.st_gid == 0 and stat.S_ISDIR(info.st_mode), name
    assert stat.S_IMODE(info.st_mode) == 0o700 if managed else not info.st_mode & 0o022, name
    parents.append((parent, name, fd, identity(info, True)))
    return fd

def verify_parents():
    for parent, name, fd, expected in parents:
        assert identity(os.fstat(fd), True) == expected, name
        assert identity(os.stat(name, dir_fd=parent, follow_symlinks=False), True) == expected, name

def read_file(fd, maximum):
    before = identity(os.fstat(fd))
    assert before[6] <= maximum
    os.lseek(fd, 0, os.SEEK_SET)
    data = bytearray()
    while len(data) <= maximum:
        part = os.read(fd, min(65536, maximum + 1 - len(data)))
        if not part:
            break
        data.extend(part)
    assert len(data) <= maximum and identity(os.fstat(fd)) == before
    return bytes(data)

def scan_instances():
    verify_parents()
    try:
        root = os.open("instances", directory_flags, dir_fd=state)
    except FileNotFoundError:
        try:
            os.stat("instances", dir_fd=state, follow_symlinks=False)
        except FileNotFoundError:
            verify_parents()
            return {"directory": None, "owners": {}}
        raise AssertionError("instance directory appeared during absence observation")

    def directory_receipt(fd):
        info = os.fstat(fd)
        assert info.st_uid == info.st_gid == 0 and stat.S_ISDIR(info.st_mode)
        assert stat.S_IMODE(info.st_mode) == 0o700
        return identity(info)

    def verify_directory(parent, name, fd, expected):
        assert identity(os.fstat(fd)) == expected, name
        assert identity(os.stat(name, dir_fd=parent, follow_symlinks=False)) == expected, name

    try:
        root_identity = directory_receipt(root)
        owners = sorted(os.listdir(root))
        assert len(owners) <= 64
        snapshot = {"directory": root_identity, "owners": {}}
        for owner in owners:
            assert re.fullmatch(r"[1-9][0-9]*", owner), owner
            uid = os.open(owner, directory_flags, dir_fd=root)
            try:
                uid_identity = directory_receipt(uid)
                names = sorted(os.listdir(uid))
                assert len(names) <= 1000
                records = {}
                for name in names:
                    assert re.fullmatch(r"[0-9a-f]{32}\.json", name), name
                    fd = os.open(name, file_flags, dir_fd=uid)
                    try:
                        info = os.fstat(fd)
                        assert info.st_uid == info.st_gid == 0 and stat.S_ISREG(info.st_mode)
                        assert info.st_nlink == 1 and stat.S_IMODE(info.st_mode) == 0o600
                        raw = read_file(fd, 65536)
                        record = json.loads(raw, object_pairs_hook=unique_object)
                        assert type(record["schema_version"]) is int and record["schema_version"] == 6
                        assert type(record["owner_uid"]) is int and record["owner_uid"] == int(owner)
                        assert record["vm_id"] + ".json" == name and record["lifecycle"] == "cleaned", name
                        expected = identity(info)
                        assert identity(os.fstat(fd)) == expected, name
                        assert identity(os.stat(name, dir_fd=uid, follow_symlinks=False)) == expected, name
                        records[name] = {"identity": expected, "sha256": hashlib.sha256(raw).hexdigest()}
                    finally:
                        os.close(fd)
                assert sorted(os.listdir(uid)) == names, owner
                for name, receipt in records.items():
                    assert identity(os.stat(name, dir_fd=uid, follow_symlinks=False)) == receipt["identity"], name
                verify_directory(root, owner, uid, uid_identity)
                snapshot["owners"][owner] = {"directory": uid_identity, "records": records}
            finally:
                os.close(uid)
        assert sorted(os.listdir(root)) == owners
        verify_directory(state, "instances", root, root_identity)
        verify_parents()
        return snapshot
    finally:
        os.close(root)

def no_live_instances():
    global journal_baseline
    snapshot = scan_instances()
    if journal_baseline is None:
        journal_baseline = snapshot
    else:
        assert snapshot == journal_baseline, "instance journals changed; preserve release state"

def absent_target():
    verify_parents()
    try:
        os.stat("1.16.1", dir_fd=architecture, follow_symlinks=False)
    except FileNotFoundError:
        return
    raise AssertionError("1.16.1 root slot exists; preserve it")

def open_pair(version):
    directory = open_directory(architecture, version)
    assert set(os.listdir(directory)) == {"manifest.json", "firecracker", "jailer"}
    leaves = {}
    for name in ("manifest.json", "firecracker", "jailer"):
        fd = os.open(name, file_flags, dir_fd=directory)
        fds.append(fd)
        info = os.fstat(fd)
        assert info.st_uid == info.st_gid == 0 and stat.S_ISREG(info.st_mode) and info.st_nlink == 1
        assert stat.S_IMODE(info.st_mode) == (0o600 if name == "manifest.json" else 0o755)
        leaves[name] = (fd, identity(info))
    return directory, leaves

def pair_receipt(pair, version):
    directory, leaves = pair
    verify_parents()
    assert set(os.listdir(directory)) == set(leaves)
    content, identities = {}, {}
    for name, (fd, expected) in leaves.items():
        assert identity(os.fstat(fd)) == expected, name
        assert identity(os.stat(name, dir_fd=directory, follow_symlinks=False)) == expected, name
        content[name] = read_file(fd, 4096 if name == "manifest.json" else 64 * 1024 * 1024)
        identities[name] = expected
    manifest = json.loads(content["manifest.json"], object_pairs_hook=unique_object)
    assert set(manifest) == {"schema_version", "release", "archive_sha256", "firecracker", "jailer"}
    assert type(manifest["schema_version"]) is int and manifest["schema_version"] == 1
    assert manifest["release"] == {"version": version, "architecture": "x86_64"}
    assert re.fullmatch(r"[0-9a-f]{64}", manifest["archive_sha256"])
    for name in ("firecracker", "jailer"):
        item = manifest[name]
        assert set(item) == {"sha256", "size_bytes"} and type(item["size_bytes"]) is int
        assert 120 <= item["size_bytes"] <= 64 * 1024 * 1024
        assert item["size_bytes"] == len(content[name])
        assert item["sha256"] == hashlib.sha256(content[name]).hexdigest()
    for name, (fd, expected) in leaves.items():
        assert identity(os.fstat(fd)) == expected, name
        assert identity(os.stat(name, dir_fd=directory, follow_symlinks=False)) == expected, name
    verify_parents()
    return {"manifest": manifest, "directory": identity(os.fstat(directory), True), "files": identities}

try:
    root = open_directory(None, "/", False)
    var = open_directory(root, "var", False)
    lib = open_directory(var, "lib", False)
    state = open_directory(lib, "mvmctl")
    binaries = open_directory(state, "binaries")
    architecture = open_directory(binaries, "x86_64")
    original = open_pair("1.17.0")
    original_receipt = pair_receipt(original, "1.17.0")
    no_live_instances()
    absent_target()
    print(json.dumps({"original": original_receipt, "target_absent": True}), flush=True)
    owned = None
    for line in sys.stdin:
        command = line.strip()
        if command == "close":
            break
        if command == "removed":
            assert owned is not None
            # Root removal intentionally unlinks this pinned version directory.
            parents[:] = [entry for entry in parents if entry[2] != owned[0]]
        assert pair_receipt(original, "1.17.0") == original_receipt
        no_live_instances()
        if command == "absent":
            assert owned is None
            absent_target()
            result = {"target_absent": True}
        elif command == "admit":
            assert owned is None
            owned = open_pair("1.16.1")
            result = pair_receipt(owned, "1.16.1")
        elif command == "verify":
            assert owned is not None
            result = pair_receipt(owned, "1.16.1")
        elif command == "removed":
            absent_target()
            result = {"target_absent": True, "original": original_receipt}
        else:
            raise AssertionError("unknown bin ownership phase")
        print(json.dumps(result), flush=True)
except BaseException as error:
    print(json.dumps({"error": str(error)}), flush=True)
    raise
finally:
    for fd in reversed(fds):
        os.close(fd)
'''


class _BinReleaseOwnership:
    """Keep fixed root receipts pinned; this oracle performs no root mutation."""

    def __init__(self):
        self.process = subprocess.Popen(
            ["sudo", "-n", "/usr/bin/python3", "-I", "-u", "-c", _BIN_RELEASE_RECEIVER],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )

    def read(self) -> dict:
        assert select.select([self.process.stdout], [], [], 30)[0], "root receipt oracle timed out"
        line = self.process.stdout.readline(16385)
        assert line and len(line) <= 16384, "missing or oversized root receipt"
        receipt = json.loads(line)
        assert "error" not in receipt, receipt
        return receipt

    def require(self, phase: str) -> dict:
        self.process.stdin.write(phase + "\n")
        self.process.stdin.flush()
        return self.read()

    def close(self) -> None:
        if self.process.poll() is None:
            try:
                self.process.stdin.write("close\n")
                self.process.stdin.flush()
            except BrokenPipeError:
                # A rejected receipt may already have closed the root reader.
                pass
        try:
            self.process.stdin.close()
        except BrokenPipeError:
            pass  # Its nonzero exit and stderr below retain the receiver failure.
        try:
            self.process.wait(timeout=30)
            error = self.process.stderr.read(16385)
        finally:
            self.process.stdout.close()
            self.process.stderr.close()
        assert self.process.returncode == 0, error


def _mirrored_bin_1161() -> tuple[Path, tuple, dict]:
    """Hash only the audited mirrored archive and its exact executable members."""
    assert platform.machine() == "x86_64", "this audited release fixture is x86_64 only"
    archive = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser() / "firecracker-v1.16.1-x86_64.tgz"
    identity = _file_identity(archive)
    assert 0 < identity[-1] <= 128 * 1024 * 1024, archive
    fd = os.open(archive, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(fd, "rb") as source:
        assert _file_identity(archive) == identity
        info = os.fstat(source.fileno())
        assert (info.st_dev, info.st_ino) == identity[:2]
        digest = hashlib.file_digest(source, "sha256").hexdigest()
        source.seek(0)
        manifest = {"schema_version": 1, "release": {"version": "1.16.1", "architecture": "x86_64"},
                    "archive_sha256": digest}
        names = {f"release-v1.16.1-x86_64/{kind}-v1.16.1-x86_64": kind
                 for kind in ("firecracker", "jailer")}
        with tarfile.open(fileobj=source, mode="r|gz") as bundle:
            for index, member in enumerate(bundle):
                assert index < 128, "unexpected mirrored archive member count"
                if member.name in names:
                    kind = names[member.name]
                    assert kind not in manifest and member.isfile()
                    assert 120 <= member.size <= 64 * 1024 * 1024
                    with bundle.extractfile(member) as executable:
                        manifest[kind] = {"sha256": hashlib.file_digest(executable, "sha256").hexdigest(),
                                          "size_bytes": member.size}
        assert set(manifest) == {"schema_version", "release", "archive_sha256", "firecracker", "jailer"}
        assert _file_identity(archive) == identity
        assert os.fstat(source.fileno()).st_ctime_ns == identity[-2]
    return archive, identity, manifest


def _require_bin_root_projection(rows: list[dict], receipt: dict) -> list[dict]:
    """Bind complete display IDs to the retained root executable digests."""
    manifest = receipt["manifest"]
    version = manifest["release"]["version"]
    pair = [row for row in rows if row["version"] == version]
    assert len(pair) == 2 and {row["type"] for row in pair} == {"firecracker", "jailer"}, pair
    for row in pair:
        kind = row["type"]
        digest = manifest[kind]["sha256"]
        assert row["id"] == hashlib.sha256(
            f"mvmctl:binary-release:v1:{kind}:x86_64:{version}:{digest}".encode(),
        ).hexdigest(), row
        assert row["architecture"] == "x86_64" and row["is_present"] is True, row
        assert row["path"] == f"/var/lib/mvmctl/binaries/x86_64/{version}/{kind}", row
    return pair


class TestBinaryPullAndLifecycle:
    """Test Firecracker binary pull, set-default, and remove operations.

    All tests are serial and destructive. Read-only tests appear in
    TestBinLifecycle and TestBinaryEdges above.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_bin,
    ]

    @pytest.mark.slow
    @pytest.mark.destructive
    def test_bin_pull_and_set_default(self, runner_vm: str, record_property) -> None:
        """Install an absent mirrored release, transfer defaults, then remove the test-owned slot."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        defaults = _installed_pair(before)
        target = "1.16.1"
        assert all(item["version"] != target for item in before), before
        assert not _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        archive, archive_identity, expected_manifest = _mirrored_bin_1161()
        authority = _BinReleaseOwnership()
        attempted = False
        owned = None
        failures: list[BaseException] = []

        def checked(label: str, operation) -> bool:
            try:
                operation()
            except BaseException as error:
                error.add_note(label)
                failures.append(error)
                return False
            return True

        try:
            original = authority.read()
            record_property("bin_original_root_receipt", json.dumps(original))
            _require_bin_root_projection(defaults, original["original"])
            assert original["target_absent"] is True
            assert authority.require("absent")["target_absent"] is True
            attempted = True
            pulled = _json_success_data(
                _run_mvm(runner_vm, "bin", "pull", target, "--json", timeout=300).stdout,
                list,
            )
            admitted = authority.require("admit")
            assert admitted["manifest"] == expected_manifest, admitted
            assert _file_identity(archive) == archive_identity
            _require_bin_root_projection(pulled, admitted)
            owned = admitted
            record_property("bin_owned_root_receipt", json.dumps(owned))
            assert all(item["is_default"] is False for item in pulled), pulled
            assert authority.require("verify") == owned
            pulled = _json_success_data(
                _run_mvm(runner_vm, "bin", "pull", target, "--default", "--json", timeout=300).stdout,
                list,
            )
            _require_bin_root_projection(pulled, owned)
            listing = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
            assert {
                (item["type"], item["version"], item["id"])
                for item in listing
                if item["is_default"]
            } == {(item["type"], target, item["id"]) for item in pulled}
        except BaseException as error:
            error.add_note("binary lifecycle assertion or operation failed")
            failures.append(error)
        finally:
            # Default selection only changes the caller projection. Restore both
            # captured IDs even when the root observer cannot prove target ownership.
            restored = True
            for item in defaults:
                succeeded = checked(
                    f"restore original {item['type']} default {item['id']}",
                    lambda item=item: _run_mvm(runner_vm, "bin", "default", item["id"]),
                )
                restored = succeeded and restored

            def verify_defaults() -> None:
                listing = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
                assert {item["id"] for item in listing if item["is_default"]} == {
                    item["id"] for item in defaults
                }, listing
                assert _binary_identity([item for item in listing if item["version"] != target]) == (
                    _binary_identity(before)
                )

            defaults_verified = checked("verify both original defaults and unaffected projections", verify_defaults)
            restored = defaults_verified and restored
            if owned is not None:
                def verify_owned() -> None:
                    assert authority.require("verify") == owned
                    listing = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
                    _require_bin_root_projection(listing, original["original"])
                    _require_bin_root_projection(listing, owned)

                ownership_verified = checked("verify unchanged owned and original root releases", verify_owned)
                if restored and ownership_verified:
                    def remove_owned() -> None:
                        verify_defaults()
                        assert authority.require("verify") == owned
                        removed = _json_success_data(
                            _run_mvm(runner_vm, "bin", "rm", target, "--force", "--json").stdout,
                            dict,
                        )
                        assert len(removed["items"]) == 1
                        result = removed["items"][0]
                        assert result["error"] is None and result["result"]["item"] == target
                        assert result["result"]["status"] == "success"
                        assert result["result"]["code"] == "binary.removed"
                        absent = authority.require("removed")
                        assert absent == {"target_absent": True, "original": original["original"]}
                        record_property("bin_removed_root_receipt", json.dumps(absent))
                        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
                        assert _binary_identity(after) == _binary_identity(before)
                        assert {item["id"]: item["created_at"] for item in after} == {
                            item["id"]: item["created_at"] for item in before
                        }

                    checked("remove only the verified owned release and verify exact restoration", remove_owned)
                else:
                    failures.append(AssertionError(
                        "uncertain 1.16.1 root/projection state preserved; explicit recovery required"
                    ))
            elif attempted:
                failures.append(AssertionError(
                    "unadmitted or uncertain 1.16.1 root/projection state preserved; explicit recovery required"
                ))
            checked("close retained root release observations", authority.close)

        if len(failures) == 1:
            raise failures[0]
        if failures:
            raise BaseExceptionGroup("binary lifecycle and cleanup failures", failures)

    def test_bin_rm_rejects_binary_id_without_effects(self, runner_vm: str) -> None:
        """The exact-version removal API must not interpret a display ID as a release slot."""
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        selected = next(
            item for item in before
            if item["type"] == "firecracker" and item["is_default"]
        )
        rejected = _run_mvm(runner_vm, "bin", "rm", selected["id"], check=False)
        assert rejected.returncode != 0
        assert "requires exact versions" in (rejected.stdout + rejected.stderr).lower()
        after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        assert after == before


# ============================================================================
# VM-integrated binary deletion
# ============================================================================


class TestBinaryLiveVMReleaseLease:
    """A running VM's trusted release cannot be removed."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_bin,
        pytest.mark.tier2,
        pytest.mark.needs_kvm,
    ]

    def test_force_remove_denied_and_identical_pull_keeps_live_release(
        self, runner_vm: str, unique_vm_name: str
    ) -> None:
        """Removal is denied; identical force-pull leaves the live pair usable."""
        vm_name = unique_vm_name
        before = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
        pair = _installed_pair(before)
        version = "1.17.0"
        mirror = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser()
        archive = mirror / f"firecracker-v{version}-{platform.machine()}.tgz"
        archive_identity = _file_identity(archive)
        assert archive_identity[-1] > 0, archive
        guestfs = _json_success_data(
            _run_mvm(runner_vm, "config", "get", "settings", "guestfs_enabled", "--json").stdout,
            bool,
        )
        assert guestfs is False, "live release fixture requires the prepared LoopMount runner"
        kernels = _json_success_data(_run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list)
        kernel = [item for item in kernels if item["is_default"] and item["is_present"]]
        assert len(kernel) == 1, kernels
        images = _json_success_data(_run_mvm(runner_vm, "image", "ls", "--json").stdout, list)
        image = [item for item in images if item["type"] == "alpine"
                 and item["version"] == "3.23" and item["is_present"]]
        assert len(image) == 1, images
        initial_vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        assert all(item["name"] != vm_name for item in initial_vms), initial_vms
        created = None
        files: dict[Path, tuple] = {}
        directory_identity = None

        try:
            receipt = _json_success_data(_run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                image[0]["id"],
                "--kernel",
                kernel[0]["id"],
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
                timeout=180,
            ).stdout, list)
            assert len(receipt) == 1, receipt
            created = receipt[0]
            assert created["name"] == vm_name, created
            assert re.fullmatch(r"[0-9a-f]{32}", created["id"]), created
            assert all(item["id"] != created["id"] for item in initial_vms), created
            assert created["created_at"], created
            cache = Path(os.environ.get("MVM_CACHE_DIR", "~/.cache/mvmctl")).expanduser()
            vm_dir = cache / "vms" / created["id"]
            directory = vm_dir.lstat()
            assert stat.S_ISDIR(directory.st_mode) and directory.st_uid == os.getuid(), vm_dir
            directory_identity = (directory.st_dev, directory.st_ino, directory.st_uid, directory.st_mode)
            for field, leaf in (("config_path", "firecracker.json"), ("rootfs_path", "rootfs.img")):
                path = Path(created[field])
                assert path == vm_dir / leaf, created
                identity = _file_identity(path)
                assert identity[2] == os.getuid(), path
                files[path] = identity
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(item for item in vms if item["id"] == created["id"])
            assert vm["status"] == "running"
            assert {vm["binary_id"], vm["jailer_binary_id"]} == {item["id"] for item in pair}
            process_identity = (vm["pid"], vm["process_start_time"])
            assert process_identity[0] > 0 and process_identity[1] > 0, vm
            assert _run_mvm(
                runner_vm, "exec", created["id"], "--", "printf bin-live-pair-ok",
            ).stdout == "bin-live-pair-ok"

            denied = _run_mvm(
                runner_vm, "bin", "rm", version, "--force", "--json", check=False,
            )
            assert denied.returncode != 0
            envelope = json.loads(denied.stdout)
            assert envelope["schema_version"] == 1
            assert envelope["status"] == "error"
            assert len(envelope["data"]["items"]) == 1
            rejection = envelope["data"]["items"][0]
            assert rejection["result"]["item"] == version
            assert rejection["error"]["code"] == "vm.state.invalid"
            assert rejection["error"]["class"] == 2  # errs.ClassConflict
            assert "referenced by live privileged VM authority" in rejection["error"]["message"]

            pulled = _json_success_data(
                _run_mvm(
                    runner_vm, "bin", "pull", version, "--force", "--json", timeout=300,
                ).stdout,
                list,
            )
            assert {item["id"] for item in pulled} == {item["id"] for item in pair}
            assert {item["type"] for item in pulled} == {"firecracker", "jailer"}
            assert _binary_identity(pulled) == _binary_identity(pair)
            assert _file_identity(archive) == archive_identity

            after = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
            assert _binary_identity(after) == _binary_identity(before)
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(item for item in vms if item["id"] == created["id"])
            assert vm["status"] == "running"
            assert (vm["pid"], vm["process_start_time"]) == process_identity
            assert _run_mvm(
                runner_vm, "exec", created["id"], "--", "printf bin-live-pair-ok",
            ).stdout == "bin-live-pair-ok"
        finally:
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if created is not None:
                matches = [item for item in vms if item["id"] == created["id"]]
                assert len(matches) == 1, (created, matches)
                current = matches[0]
                for field in ("name", "created_at", "config_path", "rootfs_path", "binary_id", "jailer_binary_id"):
                    assert current[field] == created[field], (field, created, current)
                directory = Path(current["config_path"]).parent.lstat()
                assert (directory.st_dev, directory.st_ino, directory.st_uid,
                        directory.st_mode) == directory_identity
                assert len(files) == 2, "incomplete file receipt; preserve fixture for explicit recovery"
                for path, identity in files.items():
                    assert _file_identity(path) == identity, path
                _run_mvm(runner_vm, "vm", "rm", created["id"], "--force", timeout=120)
                for path in (*files, Path(current["config_path"]).parent):
                    with pytest.raises(FileNotFoundError):
                        path.lstat()
            vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert all(item["name"] != vm_name for item in vms)
            if created is not None:
                assert all(item["id"] != created["id"] for item in vms)
