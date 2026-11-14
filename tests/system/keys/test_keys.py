"""VM-free key CRUD and export system tests with checked resource/default ownership."""

from __future__ import annotations

import json
import shlex
import stat
import tempfile
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import _guest_run, _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_keys]


def _success_data(result, expected_type=list):
    """Read the installed CLI's versioned success response for key commands."""
    assert result.returncode == 0, result.stderr or result.stdout
    envelope = json.loads(result.stdout)
    assert envelope["schema_version"] == 1
    assert envelope["status"] == "success"
    assert envelope["error"] is None
    data = envelope["data"]
    assert isinstance(data, expected_type), envelope
    return data


def _key_inventory(runner_vm):
    items = _success_data(_run_mvm(runner_vm, "key", "ls", "--json"))
    assert len({item["id"] for item in items}) == len(items), items
    return {item["id"]: item for item in items}


def _default_ids(items):
    assert all(type(item["is_default"]) is bool for item in items.values()), items
    return {key_id for key_id, item in items.items() if item["is_default"]}


def _inventory_state(items):
    # Setting a prior default through the public CLI advances its update timestamp.
    return {key_id: {key: value for key, value in item.items() if key != "updated_at"}
            for key_id, item in items.items()}


def _file_identity(path):
    metadata = path.lstat()
    assert stat.S_ISREG(metadata.st_mode), path
    return metadata.st_dev, metadata.st_ino


class _KeyOwner:
    """Own exact key records and a flat directory of admitted export/source files."""

    def __init__(self, runner_vm):
        self.runner_vm = runner_vm
        self.baseline = _key_inventory(runner_vm)
        self.defaults = _default_ids(self.baseline)
        self.names = {}
        self.backing = {}
        self.directory = Path(tempfile.mkdtemp(prefix="mvm-keys-", dir=Path.home() / ".cache"))
        metadata = self.directory.lstat()
        self.directory_identity = metadata.st_dev, metadata.st_ino
        self.artifacts = {}

    def reserve(self, name):
        assert name not in {item["name"] for item in self.baseline.values()}, name
        assert "/" not in name and name not in ("", ".", ".."), name
        self.names.setdefault(name, None)

    def entry(self, name):
        items = [item for item in _key_inventory(self.runner_vm).values() if item["name"] == name]
        assert len(items) == 1, items
        return items[0]

    def remember_key(self, name):
        items = [item for item in _key_inventory(self.runner_vm).values() if item["name"] == name]
        assert len(items) <= 1, items
        if items:
            item = items[0]
            self.names[name] = item["id"]
            for raw in (item["public_key_path"], item["private_key_path"]):
                if raw is not None:
                    path = Path(raw)
                    self.backing[path] = _file_identity(path)

    def admit_pair(self, name):
        for filename in (name, f"{name}.pub"):
            self.artifacts.setdefault(self.directory / filename, None)

    def remember_artifacts(self):
        for path in self.artifacts:
            if path.exists() or path.is_symlink():
                self.artifacts[path] = _file_identity(path)

    def create(self, name, *options):
        self.reserve(name)
        if "--out" in options:
            assert options[options.index("--out") + 1] == str(self.directory), options
            self.admit_pair(name)
        try:
            return _success_data(_run_mvm(
                self.runner_vm, "key", "create", name, *options, "--json",
            ), dict)
        finally:
            self.remember_key(name)
            self.remember_artifacts()

    def source_pair(self, name="source"):
        self.admit_pair(name)
        path = self.directory / name
        try:
            _guest_run(self.runner_vm, f"ssh-keygen -t ed25519 -f {shlex.quote(str(path))} -N '' -q")
        finally:
            self.remember_artifacts()
        return path

    def import_key(self, name, source, *options):
        self.reserve(name)
        try:
            return _success_data(_run_mvm(
                self.runner_vm, "key", "import", name, str(source), *options, "--json",
            ), dict)
        finally:
            self.remember_key(name)
            self.remember_artifacts()

    def export(self, name, *options):
        self.admit_pair(name)
        try:
            paths = _success_data(_run_mvm(
                self.runner_vm, "key", "export", name, str(self.directory), *options, "--json",
            ))
        finally:
            self.remember_artifacts()
        assert set(paths) == {str(self.directory / name), str(self.directory / f"{name}.pub")}, paths
        for raw in paths:
            assert _file_identity(Path(raw)) == self.artifacts[Path(raw)]
        return self.directory / name

    def remove(self, name):
        items = [item for item in _key_inventory(self.runner_vm).values() if item["name"] == name]
        assert len(items) <= 1, items
        if items:
            item = items[0]
            assert item["id"] == self.names[name], (item, self.names)
            for raw in (item["public_key_path"], item["private_key_path"]):
                if raw is not None:
                    path = Path(raw)
                    assert _file_identity(path) == self.backing[path], path
            _run_mvm(self.runner_vm, "key", "rm", item["id"])
        assert all(item["name"] != name for item in _key_inventory(self.runner_vm).values())

    def cleanup(self):
        failures = []
        for name in self.names:
            try:
                self.remove(name)
            except Exception as error:
                failures.append(("owned key removal", name, repr(error)))
        remaining = None
        try:
            remaining = _key_inventory(self.runner_vm)
            assert set(remaining) == set(self.baseline), (self.baseline, remaining)
            def identity_fields(items):
                return {key_id: {key: value for key, value in item.items()
                                 if key not in ("is_default", "updated_at")}
                        for key_id, item in items.items()}
            assert identity_fields(remaining) == identity_fields(self.baseline), (self.baseline, remaining)
            defaults_changed = _default_ids(remaining) != self.defaults
        except Exception as error:
            failures.append(("baseline membership proof", repr(error)))
        else:
            if defaults_changed:
                try:
                    _run_mvm(self.runner_vm, "key", "default", "--clear")
                except Exception as error:
                    failures.append(("clear changed defaults", repr(error)))
                if self.defaults:
                    try:
                        _run_mvm(self.runner_vm, "key", "default", *sorted(self.defaults))
                    except Exception as error:
                        failures.append(("restore prior defaults", repr(error)))
            try:
                restored = _key_inventory(self.runner_vm)
                assert _default_ids(restored) == self.defaults, restored
                assert _inventory_state(restored) == _inventory_state(self.baseline), (self.baseline, restored)
            except Exception as error:
                failures.append(("baseline/default readback", repr(error)))
        for path in self.backing:
            if path not in self.artifacts:
                try:
                    assert not path.exists() and not path.is_symlink(), path
                except Exception as error:
                    failures.append(("cache backing absence", str(path), repr(error)))
        try:
            metadata = self.directory.lstat()
            assert stat.S_ISDIR(metadata.st_mode), self.directory
            assert (metadata.st_dev, metadata.st_ino) == self.directory_identity, self.directory
        except Exception as error:
            failures.append(("scratch directory identity", repr(error)))
        else:
            try:
                unexpected = set(self.directory.iterdir()) - set(self.artifacts)
                assert not unexpected, unexpected
            except Exception as error:
                failures.append(("unexpected scratch artifacts", repr(error)))
            # Do not bypass failed key removal by deleting backing still referenced by a key.
            try:
                assert remaining is not None, "Key inventory unavailable for backing-reference proof"
                retained = {Path(raw) for item in remaining.values()
                            for raw in (item["public_key_path"], item["private_key_path"]) if raw is not None}
            except Exception as error:
                failures.append(("scratch backing-reference proof", repr(error)))
                retained = set(self.backing)
            for path, identity in self.artifacts.items():
                try:
                    if path.exists() or path.is_symlink():
                        metadata = self.directory.lstat()
                        assert (metadata.st_dev, metadata.st_ino) == self.directory_identity, self.directory
                        assert path not in retained, f"Still referenced by a retained key: {path}"
                        assert identity is not None and _file_identity(path) == identity, path
                        path.unlink()
                except Exception as error:
                    failures.append(("owned scratch artifact", str(path), repr(error)))
            try:
                metadata = self.directory.lstat()
                assert (metadata.st_dev, metadata.st_ino) == self.directory_identity, self.directory
                self.directory.rmdir()
                assert not self.directory.exists(), self.directory
            except Exception as error:
                failures.append(("scratch directory removal", repr(error)))
        assert not failures, failures


@pytest.fixture
def key_owner(runner_vm):
    owner = _KeyOwner(runner_vm)
    try:
        yield owner
    finally:
        owner.cleanup()


@pytest.fixture
def created_key(key_owner, unique_key_name):
    """Replace the inherited fixture with a function-owned, checked key record."""
    key_owner.create(unique_key_name, "--algorithm", "ed25519")
    return unique_key_name


@pytest.fixture
def fresh_key_cache(runner_vm, monkeypatch):
    baseline = _key_inventory(runner_vm)
    with tempfile.TemporaryDirectory(prefix="mvm-keys-empty-", dir=Path.home() / ".cache") as cache_dir:
        with monkeypatch.context() as environment:
            environment.setenv("MVM_CACHE_DIR", cache_dir)
            _run_mvm(runner_vm, "cache", "init")
            try:
                yield
            finally:
                assert _key_inventory(runner_vm) == {}
    assert _key_inventory(runner_vm) == baseline


class TestEmptyKeyListing:
    def test_key_list_json_uses_array_in_fresh_cache(self, runner_vm, fresh_key_cache):
        """A fresh key cache exposes an empty list, not JSON null."""
        assert _key_inventory(runner_vm) == {}


class TestKeyInspectTree:
    def test_key_inspect_tree(self, runner_vm, created_key):
        output = _run_mvm(runner_vm, "key", "inspect", created_key).stdout
        assert f"Key: {created_key}" in output, output
        assert "├─ " in output or "└─ " in output, output


class TestKeyExportForce:
    def test_key_export_force(self, runner_vm, created_key, key_owner):
        """Export real bytes, reject overwrite, and replace admitted files with --force."""
        private = key_owner.export(created_key)
        public = private.with_suffix(".pub")
        original = public.read_bytes()
        source = Path(key_owner.entry(created_key)["private_key_path"])
        assert private.read_bytes() == source.read_bytes()
        rejected = _run_mvm(
            runner_vm, "key", "export", created_key, str(key_owner.directory), check=False,
        )
        assert rejected.returncode != 0, (rejected.stdout, rejected.stderr)
        assert public.read_bytes() == original
        public.write_text("replace-this-owned-export\n")
        key_owner.export(created_key, "--force")
        assert public.read_bytes() == original


class TestKeyLifecycle:
    def test_key_listing(self, runner_vm, created_key, key_owner):
        assert key_owner.entry(created_key)["is_present"] is True

    def test_duplicate_key_rejection(self, runner_vm, created_key, key_owner):
        before = _key_inventory(runner_vm)
        result = _run_mvm(
            runner_vm, "key", "create", created_key, "--algorithm", "ed25519", check=False,
        )
        assert result.returncode != 0, (result.stdout, result.stderr)
        assert "already exists" in (result.stdout + result.stderr).lower()
        assert _key_inventory(runner_vm) == before

    def test_key_show(self, runner_vm, created_key, key_owner):
        data = _success_data(_run_mvm(runner_vm, "key", "inspect", created_key, "--json"), dict)
        item = key_owner.entry(created_key)
        assert data["key"]["name"] == created_key
        assert data["key"]["id"] == item["id"]
        assert data["key"]["fingerprint"] == item["fingerprint"]

    def test_key_remove_nonexistent(self, runner_vm):
        before = _key_inventory(runner_vm)
        name = f"missing-key-{uuid.uuid4().hex}"
        result = _run_mvm(runner_vm, "key", "rm", name, check=False)
        assert result.returncode != 0, (result.stdout, result.stderr)
        assert "not found" in (result.stdout + result.stderr).lower()
        assert _key_inventory(runner_vm) == before

    def test_key_inspect_json(self, runner_vm, created_key, key_owner):
        data = _success_data(_run_mvm(runner_vm, "key", "inspect", created_key, "--json"), dict)
        item = key_owner.entry(created_key)
        assert set(data) == {"key", "files", "timestamps"}, data
        assert data["key"] == {key: item[key] for key in (
            "id", "name", "fingerprint", "algorithm", "comment", "is_default", "is_present",
        )}
        assert data["files"] == {key: item[key] for key in ("private_key_path", "public_key_path")}

    def test_key_export(self, runner_vm, created_key, key_owner):
        private = key_owner.export(created_key)
        item = key_owner.entry(created_key)
        assert private.read_bytes() == Path(item["private_key_path"]).read_bytes()
        assert private.with_suffix(".pub").read_bytes() == Path(item["public_key_path"]).read_bytes()

    def test_key_list_json(self, runner_vm, created_key, key_owner):
        listed = _success_data(_run_mvm(runner_vm, "key", "ls", "--json"))
        assert [item for item in listed if item["name"] == created_key] == [key_owner.entry(created_key)]

    def test_key_set_default(self, runner_vm, created_key, key_owner):
        _run_mvm(runner_vm, "key", "default", created_key)
        assert _default_ids(_key_inventory(runner_vm)) == key_owner.defaults | {key_owner.names[created_key]}

    def test_key_set_default_clear(self, runner_vm, created_key, key_owner):
        _run_mvm(runner_vm, "key", "default", created_key)
        assert key_owner.entry(created_key)["is_default"] is True
        _run_mvm(runner_vm, "key", "default", "--clear")
        assert _default_ids(_key_inventory(runner_vm)) == set()

    def test_key_create_ed25519(self, runner_vm, unique_key_name, key_owner):
        key_owner.create(unique_key_name, "--algorithm", "ed25519")
        assert key_owner.entry(unique_key_name)["algorithm"] == "ssh-ed25519"

    def test_key_create_rsa(self, runner_vm, unique_key_name, key_owner):
        key_owner.create(unique_key_name, "--algorithm", "rsa")
        assert key_owner.entry(unique_key_name)["algorithm"] == "ssh-rsa"

    def test_key_create_ecdsa(self, runner_vm, unique_key_name, key_owner):
        key_owner.create(unique_key_name, "--algorithm", "ecdsa")
        assert key_owner.entry(unique_key_name)["algorithm"].startswith("ecdsa-sha2-")

    def test_key_add_existing(self, runner_vm, unique_key_name, key_owner):
        source = key_owner.source_pair()
        imported = key_owner.import_key(unique_key_name, source.with_suffix(".pub"))
        assert key_owner.entry(unique_key_name)["id"] == imported["id"]
        assert Path(imported["public_key_path"]).read_bytes() == source.with_suffix(".pub").read_bytes()

    def test_key_import_shows_fingerprint(self, runner_vm, unique_key_name, key_owner):
        source = key_owner.source_pair()
        key_owner.import_key(unique_key_name, source.with_suffix(".pub"))
        item = key_owner.entry(unique_key_name)
        fingerprint = _guest_run(runner_vm, f"ssh-keygen -lf {shlex.quote(str(source))}").stdout.split()[1]
        assert item["fingerprint"] == fingerprint
        assert item["id"] == fingerprint

    def test_key_delete(self, runner_vm, unique_key_name, key_owner):
        key_owner.create(unique_key_name, "--algorithm", "ed25519")
        _run_mvm(runner_vm, "key", "rm", unique_key_name)
        assert all(item["name"] != unique_key_name for item in _key_inventory(runner_vm).values())

    def test_remove_default_key_when_only_key(self, runner_vm, fresh_key_cache):
        owner = _KeyOwner(runner_vm)
        name = f"sys-only-key-{uuid.uuid4().hex[:8]}"
        try:
            assert owner.baseline == {}
            owner.create(name, "--algorithm", "ed25519", "--default")
            assert set(_key_inventory(runner_vm)) == {owner.names[name]}
            assert _default_ids(_key_inventory(runner_vm)) == {owner.names[name]}
            _run_mvm(runner_vm, "key", "rm", name)
            assert _key_inventory(runner_vm) == {}
        finally:
            owner.cleanup()

    def test_key_rm_multiple(self, runner_vm, unique_key_name, key_owner):
        names = [f"{unique_key_name}-1", f"{unique_key_name}-2"]
        for name in names:
            key_owner.create(name, "--algorithm", "ed25519")
        _run_mvm(runner_vm, "key", "rm", *names)
        assert all(item["name"] not in names for item in _key_inventory(runner_vm).values())


class TestKeyCreateAdvanced:
    def test_key_create_with_bits(self, runner_vm, unique_key_name, key_owner):
        key_owner.create(unique_key_name, "--algorithm", "rsa", "--bits", "2048")
        public = key_owner.entry(unique_key_name)["public_key_path"]
        assert _guest_run(runner_vm, f"ssh-keygen -lf {shlex.quote(public)}").stdout.split()[0] == "2048"

    def test_key_create_with_comment(self, runner_vm, unique_key_name, key_owner):
        comment = f"test-comment-{uuid.uuid4().hex}"
        key_owner.create(unique_key_name, "--algorithm", "ed25519", "--comment", comment)
        inspected = _success_data(_run_mvm(runner_vm, "key", "inspect", unique_key_name, "--json"), dict)
        assert inspected["key"]["comment"] == comment
        assert Path(key_owner.entry(unique_key_name)["public_key_path"]).read_text().split(maxsplit=2)[2].strip() == comment

    def test_key_create_with_out(self, runner_vm, unique_key_name, key_owner):
        created = key_owner.create(
            unique_key_name, "--algorithm", "ed25519", "--out", str(key_owner.directory),
        )
        assert created["private_key_path"] == str(key_owner.directory / unique_key_name)
        assert created["public_key_path"] == str(key_owner.directory / f"{unique_key_name}.pub")
        assert _file_identity(Path(created["private_key_path"])) == key_owner.artifacts[Path(created["private_key_path"])]
        assert _file_identity(Path(created["public_key_path"])) == key_owner.artifacts[Path(created["public_key_path"])]

    def test_key_create_with_set_default(self, runner_vm, unique_key_name, key_owner):
        key_owner.create(unique_key_name, "--algorithm", "ed25519", "--default")
        assert _default_ids(_key_inventory(runner_vm)) == key_owner.defaults | {key_owner.names[unique_key_name]}

    def test_key_create_force_overwrite(self, runner_vm, unique_key_name, key_owner):
        original = key_owner.create(unique_key_name, "--algorithm", "ed25519")
        replacement = key_owner.create(unique_key_name, "--algorithm", "ed25519", "--force")
        assert replacement["id"] != original["id"]
        assert key_owner.entry(unique_key_name)["id"] == replacement["id"]
        assert original["id"] not in _key_inventory(runner_vm)


class TestKeyImportOverwrite:
    def test_key_import_overwrite(self, runner_vm, unique_key_name, key_owner):
        source = key_owner.source_pair()
        replacement_source = key_owner.source_pair("replacement")
        original = key_owner.import_key(unique_key_name, source.with_suffix(".pub"))
        result = _run_mvm(
            runner_vm, "key", "import", unique_key_name, str(replacement_source.with_suffix(".pub")),
            check=False,
        )
        assert result.returncode != 0, (result.stdout, result.stderr)
        assert "already exists" in (result.stdout + result.stderr).lower()
        assert key_owner.entry(unique_key_name)["id"] == original["id"]
        replacement = key_owner.import_key(unique_key_name, replacement_source.with_suffix(".pub"), "--force")
        assert replacement["id"] != original["id"]
        assert key_owner.entry(unique_key_name)["id"] == replacement["id"]
