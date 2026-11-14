"""Preseeded kernel import, byte identity, and bounded guest lifecycle journeys."""

from __future__ import annotations

import hashlib
import os
import shutil
import stat
import tempfile
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import _json_success_data, _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_kernel]


def _inventory(runner_vm, domain):
    items = _json_success_data(_run_mvm(runner_vm, domain, "ls", "--json").stdout, list)
    assert len({item["id"] for item in items}) == len(items), (domain, items)
    return {item["id"]: item for item in items}


def _kernel_state(items):
    # Public kernel listing reconciles missing recorded files and advances updated_at.
    return {item_id: {key: value for key, value in item.items() if key != "updated_at"}
            for item_id, item in items.items()}


def _kernel_identity(item):
    return {key: value for key, value in item.items() if key not in ("updated_at", "is_default")}


def _vm_identity(item):
    return {key: item[key] for key in (
        "id", "name", "created_at", "kernel_id", "image_id", "disk_size_mib", "mem_size_mib", "vcpu_count",
    )}


def _file_identity(path):
    metadata = path.lstat()
    assert stat.S_ISREG(metadata.st_mode), path
    return metadata.st_dev, metadata.st_ino, metadata.st_size


def _digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


class _KernelImportOwner:
    """Own newly admitted kernel receipts and at most one small networkless VM."""

    def __init__(self, runner_vm):
        self.runner_vm = runner_vm
        self.baseline = {domain: _inventory(runner_vm, domain) for domain in (
            "vm", "network", "volume", "snapshot", "key", "kernel", "image", "bin",
        )}
        self.config = _json_success_data(_run_mvm(runner_vm, "config", "ls", "--json").stdout, dict)
        self.kernels = {}
        self.vm_name = None
        self.vm_record = None
        mirror = os.environ.get("MVM_ASSET_MIRROR", "")
        assert mirror and Path(mirror).is_dir(), "a preseeded local asset mirror is required"
        defaults = [item for item in self.baseline["kernel"].values() if item["is_default"]]
        assert len(defaults) == 1, defaults
        self.default_id = defaults[0]["id"]
        assert defaults[0]["is_present"], defaults
        sources = [item for item in self.baseline["kernel"].values()
                   if item["type"] == "firecracker"
                   and item["version"].removeprefix("v") == "6.1.155"
                   and item["is_present"]]
        assert len(sources) == 1, sources
        source = sources[0]
        for kind in ("firecracker", "jailer"):
            selected = [item for item in self.baseline["bin"].values()
                        if item["type"] == kind and item["is_default"]]
            assert len(selected) == 1, selected
            assert selected[0]["version"] == "1.17.0" and selected[0]["is_present"], selected
        self.source_path = self.kernel_path(source["id"])
        self.source_identity = _file_identity(self.source_path)
        self.source_digest = _digest(self.source_path)
        self.source_arch = source["arch"]
        assert self.source_arch, source
        # KernelID excludes type/name: even an absent record of another type can collide.
        self.import_id = hashlib.sha256(
            f"{self.source_digest}:6.1:{self.source_arch}".encode(),
        ).hexdigest()
        assert self.import_id not in self.baseline["kernel"], ("baseline content ID collision", self.import_id)

    def kernel_path(self, kernel_id):
        inspected = _json_success_data(
            _run_mvm(self.runner_vm, "kernel", "inspect", kernel_id, "--json").stdout, dict,
        )
        assert inspected["kernel"]["id"] == kernel_id, inspected
        path = Path(inspected["storage"]["path"])
        assert path.is_absolute(), path
        _file_identity(path)
        return path

    def remember_kernel(self, name, import_attempt=False):
        matching = [item for item in _inventory(self.runner_vm, "kernel").values()
                    if item["base_name"] == name]
        assert len(matching) <= 1, matching
        if matching:
            item = matching[0]
            assert item["id"] not in self.baseline["kernel"], ("baseline kernel is never owned", item)
            assert item["id"] == self.import_id, item
            path = self.kernel_path(item["id"])
            receipt = (_kernel_identity(item), path, _file_identity(path), _digest(path))
            assert receipt[3] == self.source_digest, receipt
            if item["id"] in self.kernels:
                prior = self.kernels[item["id"]]
                assert (prior[0], prior[1], prior[3]) == (receipt[0], receipt[1], receipt[3]), (prior, receipt)
                # An explicit repeat import atomically replaces the managed backing inode.
                # Admit that publication only here; ordinary cleanup still requires its exact inode.
                if not import_attempt:
                    assert prior[2] == receipt[2], (prior, receipt)
            self.kernels[item["id"]] = receipt

    def import_kernel(self, name, source=None, *, default=False, auto_version=False):
        assert name not in {item["base_name"] for item in self.baseline["kernel"].values()}, name
        assert _file_identity(self.source_path) == self.source_identity
        assert _digest(self.source_path) == self.source_digest
        source = Path(source or self.source_path)
        assert _digest(source) == self.source_digest, source
        prospective_id = hashlib.sha256(f"{_digest(source)}:6.1:{self.source_arch}".encode()).hexdigest()
        assert prospective_id == self.import_id and prospective_id not in self.baseline["kernel"], prospective_id
        # Refuse an unrelated publication or inode replacement before authorizing another import.
        self.remember_kernel(name)
        args = ["kernel", "import", name, str(source)]
        if not auto_version:
            args.extend(["--version", "6.1"])
        if default:
            args.append("--default")
        try:
            imported = _json_success_data(_run_mvm(self.runner_vm, *args, "--json").stdout, dict)
            assert imported["id"] not in self.baseline["kernel"], ("dedup returned a baseline ID", imported)
            assert imported["id"] == prospective_id, imported
            assert len(imported["id"]) == 64 and all(char in "0123456789abcdef" for char in imported["id"]), imported
        finally:
            # Recover only the reserved name's new public receipt if command parsing fails.
            self.remember_kernel(name, import_attempt=True)
        assert imported["id"] in self.kernels, imported
        return imported

    def assert_import(self, imported, name):
        item = _inventory(self.runner_vm, "kernel")[imported["id"]]
        assert (item["base_name"], item["type"], item["version"], item["is_present"]) == (
            name, "custom", "6.1", True,
        ), item
        assert item["arch"], item
        inspected = _json_success_data(
            _run_mvm(self.runner_vm, "kernel", "inspect", imported["id"], "--json").stdout, dict,
        )
        kernel = inspected["kernel"]
        assert kernel["id"] == imported["id"]
        assert (kernel["base_name"], kernel["type"], kernel["version"], kernel["is_present"]) == (
            name, "custom", "6.1", True,
        ), kernel
        assert kernel["arch"] and kernel["name"] == f"{name} 6.1", kernel
        path = self.kernel_path(imported["id"])
        assert path.stat().st_size == self.source_path.stat().st_size
        assert _digest(path) == self.source_digest

    def remember_vm(self):
        matching = [item for item in _inventory(self.runner_vm, "vm").values() if item["name"] == self.vm_name]
        assert len(matching) <= 1, matching
        if matching:
            item = matching[0]
            assert item["id"] not in self.baseline["vm"], item
            record = _vm_identity(item)
            if self.vm_record is not None:
                assert record == self.vm_record, (record, self.vm_record)
            else:
                self.vm_record = record

    def create_vm(self, kernel_id):
        images = [item for item in self.baseline["image"].values()
                  if item["type"] == "alpine" and item["version"] == "3.23" and item["is_present"]]
        assert len(images) == 1, images
        assert self.vm_name is None, "maximum one owned VM"
        assert not any(item["status"] not in ("stopped", "failed")
                       for item in self.baseline["vm"].values()), self.baseline["vm"]
        self.vm_name = f"sys-import-vm-{uuid.uuid4().hex[:12]}"
        assert self.vm_name not in {item["name"] for item in self.baseline["vm"].values()}
        try:
            created = _json_success_data(_run_mvm(
                self.runner_vm, "vm", "create", self.vm_name, "--image", "alpine:3.23",
                "--kernel", kernel_id, "--no-network", "--cloud-init-mode", "off", "--skip-deblob",
                "--no-pci", "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                "--json", timeout=180,
            ).stdout, list)
            assert len(created) == 1 and created[0]["name"] == self.vm_name, created
        finally:
            self.remember_vm()
        assert self.vm_record == _vm_identity(created[0])
        return self.vm_state(kernel_id, "running")

    def vm_state(self, kernel_id, status):
        self.remember_vm()
        assert self.vm_record is not None, self.vm_name
        vm = _inventory(self.runner_vm, "vm")[self.vm_record["id"]]
        assert _vm_identity(vm) == self.vm_record
        assert vm["kernel_id"] == kernel_id and vm["status"] == status, vm
        assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (1, 256, 512), vm
        inspected = _json_success_data(_run_mvm(
            self.runner_vm, "vm", "inspect", vm["id"], "--json",
        ).stdout, dict)
        assert (inspected["vm"]["id"], inspected["vm"]["name"], inspected["vm"]["status"]) == (
            vm["id"], self.vm_name, status,
        ), inspected
        return vm

    def cleanup(self):
        failures = []
        if self.vm_name is not None:
            try:
                self.remember_vm()
                if self.vm_record is not None:
                    vm_id = self.vm_record["id"]
                    current = _inventory(self.runner_vm, "vm")
                    if vm_id in current:
                        assert _vm_identity(current[vm_id]) == self.vm_record
                        _run_mvm(self.runner_vm, "vm", "rm", vm_id, "--force", timeout=120)
                    after = _inventory(self.runner_vm, "vm")
                    assert vm_id not in after and all(item["name"] != self.vm_name for item in after.values())
            except Exception as error:
                failures.append(("owned VM removal", repr(error)))
        # Default restoration must still run when any resource removal fails.
        try:
            current = _inventory(self.runner_vm, "kernel")
            assert _kernel_identity(current[self.default_id]) == _kernel_identity(self.baseline["kernel"][self.default_id])
            if {item["id"] for item in current.values() if item["is_default"]} != {self.default_id}:
                _run_mvm(self.runner_vm, "kernel", "default", self.default_id)
            assert {item["id"] for item in _inventory(self.runner_vm, "kernel").values()
                    if item["is_default"]} == {self.default_id}
        except Exception as error:
            failures.append(("prior kernel default restoration", repr(error)))
        for kernel_id, (identity, path, backing, digest) in self.kernels.items():
            try:
                assert kernel_id not in self.baseline["kernel"], kernel_id
                current = _inventory(self.runner_vm, "kernel")
                if kernel_id in current:
                    assert _kernel_identity(current[kernel_id]) == identity
                    assert self.kernel_path(kernel_id) == path and _file_identity(path) == backing
                    assert _digest(path) == digest
                    assert _file_identity(self.source_path) == self.source_identity
                    assert _digest(self.source_path) == self.source_digest
                    assert path != self.source_path and backing[:2] != self.source_identity[:2], path
                    _run_mvm(self.runner_vm, "kernel", "rm", kernel_id, "--force")
                assert kernel_id not in _inventory(self.runner_vm, "kernel")
                assert not path.exists() and not path.is_symlink(), path
            except Exception as error:
                failures.append(("owned kernel removal", kernel_id, repr(error)))
        for domain, baseline in self.baseline.items():
            try:
                current = _inventory(self.runner_vm, domain)
                if domain == "kernel":
                    assert _kernel_state(current) == _kernel_state(baseline), (baseline, current)
                else:
                    assert current == baseline, (domain, baseline, current)
            except Exception as error:
                failures.append((f"{domain} baseline/default readback", repr(error)))
        try:
            assert _json_success_data(_run_mvm(self.runner_vm, "config", "ls", "--json").stdout, dict) == self.config
            assert _file_identity(self.source_path) == self.source_identity
            assert _digest(self.source_path) == self.source_digest
        except Exception as error:
            failures.append(("config/source kernel preservation", repr(error)))
        assert not failures, failures


@pytest.fixture
def kernel_import_owner(runner_vm):
    """Capture complete caller state before each independent case can publish resources."""
    owner = _KernelImportOwner(runner_vm)
    try:
        yield owner
    finally:
        owner.cleanup()


class TestKernelImportLifecycle:
    """Independent import, exact full-ID selection, and stop/start cases."""

    pytestmark = [pytest.mark.requires_kvm]

    def test_import_present_kernel(self, kernel_import_owner):
        owner = kernel_import_owner
        name = f"sys-test-import-{uuid.uuid4().hex[:12]}"
        imported = owner.import_kernel(name)
        owner.assert_import(imported, name)

    def test_create_vm_with_imported_kernel(self, kernel_import_owner):
        owner = kernel_import_owner
        name = f"sys-test-create-{uuid.uuid4().hex[:12]}"
        imported = owner.import_kernel(name)
        owner.assert_import(imported, name)
        owner.create_vm(imported["id"])
        assert _run_mvm(owner.runner_vm, "exec", owner.vm_record["id"], "--", "uname", "-r").stdout.strip()

    def test_imported_kernel_stop_start(self, kernel_import_owner):
        owner = kernel_import_owner
        name = f"sys-test-restart-{uuid.uuid4().hex[:12]}"
        imported = owner.import_kernel(name)
        owner.assert_import(imported, name)
        owner.create_vm(imported["id"])
        vm_id = owner.vm_record["id"]
        before = _run_mvm(owner.runner_vm, "exec", vm_id, "--", "cat", "/proc/sys/kernel/random/boot_id").stdout.strip()
        assert before, "initial guest boot identity is required"
        _run_mvm(owner.runner_vm, "vm", "stop", vm_id)
        owner.vm_state(imported["id"], "stopped")
        assert vm_id not in {item["id"] for item in _json_success_data(
            _run_mvm(owner.runner_vm, "vm", "ps", "--json").stdout, list,
        )}
        _run_mvm(owner.runner_vm, "vm", "start", vm_id, timeout=180)
        owner.vm_state(imported["id"], "running")
        after = _run_mvm(owner.runner_vm, "exec", vm_id, "--", "cat", "/proc/sys/kernel/random/boot_id").stdout.strip()
        assert after and after != before, (before, after)


class TestKernelImportAutoVersion:
    def test_import_auto_detected_version(self, kernel_import_owner):
        owner = kernel_import_owner
        name = f"sys-test-auto-{uuid.uuid4().hex[:12]}"
        with tempfile.TemporaryDirectory(prefix="sys-test-kernel-") as directory:
            source = Path(directory) / "vmlinux-6.1-x86_64"
            shutil.copyfile(owner.source_path, source)
            assert _digest(source) == owner.source_digest
            imported = owner.import_kernel(name, source, auto_version=True)
            owner.assert_import(imported, name)


class TestKernelImportDefault:
    def test_import_kernel_with_default(self, kernel_import_owner):
        owner = kernel_import_owner
        name = f"sys-test-default-{uuid.uuid4().hex[:12]}"
        imported = owner.import_kernel(name, default=True)
        owner.assert_import(imported, name)
        assert {item["id"] for item in _inventory(owner.runner_vm, "kernel").values()
                if item["is_default"]} == {imported["id"]}


class TestKernelImportError:
    def test_import_nonexistent_path_fails(self, kernel_import_owner):
        owner = kernel_import_owner
        with tempfile.TemporaryDirectory(prefix="sys-test-missing-kernel-") as directory:
            missing = Path(directory) / "absent.vmlinux"
            assert not missing.exists() and not missing.is_symlink()
            result = _run_mvm(owner.runner_vm, "kernel", "import", f"sys-test-missing-{uuid.uuid4().hex[:12]}",
                              str(missing), "--version", "6.1", check=False)
            assert result.returncode != 0, (result.stdout, result.stderr)
            combined = (result.stdout + result.stderr).lower()
            assert "invalid value for 'path'" in combined or "source file not found" in combined, combined

    def test_import_empty_name_fails(self, kernel_import_owner):
        owner = kernel_import_owner
        result = _run_mvm(owner.runner_vm, "kernel", "import", "", str(owner.source_path),
                          "--version", "6.1", check=False)
        assert result.returncode != 0, (result.stdout, result.stderr)

    def test_import_same_bytes_reuses_content_id(self, kernel_import_owner):
        owner = kernel_import_owner
        name = f"sys-test-dup-{uuid.uuid4().hex[:12]}"
        first = owner.import_kernel(name)
        second = owner.import_kernel(name)
        assert second["id"] == first["id"], (first, second)
        owner.assert_import(second, name)
        assert len([item for item in _inventory(owner.runner_vm, "kernel").values()
                    if item["id"] == first["id"]]) == 1
