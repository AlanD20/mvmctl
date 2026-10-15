"""Installed-candidate rootfs persistence through a full VM snapshot."""

from __future__ import annotations

import json
import os
import re
import subprocess
import uuid
from pathlib import Path
from typing import Generator

import pytest

from tests.system.conftest import (
    _json_success_data,
    _run_mvm,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
    pytest.mark.tier2,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
]


def _persistence_inventory(runner_vm: str, domain: str) -> list:
    rows = _json_success_data(_run_mvm(runner_vm, domain, "ls", "--json").stdout, list)
    # Kernel listing can refresh this observational timestamp.
    if domain == "kernel":
        rows = [
            {key: value for key, value in row.items() if key != "updated_at"}
            for row in rows
        ]
    return sorted(rows, key=lambda row: json.dumps(row, sort_keys=True))


def _require_persistence_assets(runner_vm: str) -> None:
    """Admit the preseeded runner without pulling assets or changing defaults."""
    assert os.geteuid() != 0, "the installed public CLI must run as the runner operator"
    mirror = os.environ.get("MVM_ASSET_MIRROR")
    assert mirror and Path(mirror).expanduser().is_dir(), (
        "preseeded read-only mirror required"
    )
    images = [
        row
        for row in _persistence_inventory(runner_vm, "image")
        if (row["type"], row["version"]) == ("alpine", "3.23") and row["is_present"]
    ]
    assert len(images) == 1 and 0 < images[0]["minimum_rootfs_size_mib"] <= 512, images
    kernels = [
        row for row in _persistence_inventory(runner_vm, "kernel") if row["is_default"]
    ]
    assert len(kernels) == 1 and kernels[0]["is_present"], kernels
    binaries = _persistence_inventory(runner_vm, "bin")
    for kind in ("firecracker", "jailer"):
        selected = [
            row for row in binaries if row["type"] == kind and row["is_default"]
        ]
        assert (
            len(selected) == 1
            and selected[0]["is_present"]
            and selected[0]["version"] == "1.17.0"
        ), selected
    settings = _json_success_data(
        _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
    )
    guestfs = settings["settings"]["guestfs_enabled"]
    assert (
        guestfs["override"] if guestfs["override"] is not None else guestfs["default"]
    ) is False


class _RootfsPersistenceOwner:
    """Own only the two VM attempts and single capture in this journey."""

    def __init__(self, runner_vm: str):
        self.runner_vm = runner_vm
        self.baseline = {
            domain: _persistence_inventory(runner_vm, domain)
            for domain in (
                "vm",
                "snapshot",
                "network",
                "volume",
                "key",
                "image",
                "kernel",
                "bin",
            )
        }
        self.settings = _json_success_data(
            _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
        )
        self.vm_attempts: dict[str, tuple | None] = {}
        self.snapshot_name: str | None = None
        self.snapshot_source: str | None = None
        self.snapshot_request: str | None = None
        self.snapshot_receipt: tuple | None = None

    @staticmethod
    def _identity(domain: str, row: dict) -> tuple:
        length = 32 if domain == "vm" else 64
        assert isinstance(row["id"], str) and re.fullmatch(
            rf"[0-9a-f]{{{length}}}", row["id"]
        ), row
        fields = ("id", "name", "created_at")
        if domain == "snapshot":
            fields += ("source_vm_id", "source_vm_name")
            assert re.fullmatch(r"[0-9a-f]{32}", row["source_vm_id"]), row
        assert all(isinstance(row[key], str) and row[key] for key in fields), row
        if domain == "snapshot":
            fields += (
                "snapshot_dir",
                "memory_file",
                "state_file",
                "rootfs_file",
            )
        assert all(isinstance(row[key], str) for key in fields), row
        return tuple(row[key] for key in fields)

    def before_vm_create(self, name: str) -> None:
        assert name not in self.vm_attempts, (name, "repeated attempt")
        rows = self.baseline["vm"] + _persistence_inventory(self.runner_vm, "vm")
        assert all(row["name"] != name for row in rows), (name, "collision")
        self.vm_attempts[name] = None

    def vm_created(self, name: str, published: list) -> str:
        assert name in self.vm_attempts, (name, "unattempted publication")
        assert len(published) == 1 and published[0]["name"] == name, published
        receipt = self._identity("vm", published[0])
        assert all(row["id"] != receipt[0] for row in self.baseline["vm"]), receipt
        retained = self.vm_attempts[name]
        assert retained is None or retained == receipt, (name, "receipt changed")
        self.vm_attempts[name] = receipt
        return receipt[0]

    def before_snapshot_create(self, name: str, source_id: str) -> None:
        assert self.snapshot_name is None, "only one capture is owned"
        assert any(
            receipt and receipt[0] == source_id for receipt in self.vm_attempts.values()
        ), source_id
        rows = self.baseline["snapshot"] + _persistence_inventory(
            self.runner_vm, "snapshot"
        )
        assert all(row["name"] != name for row in rows), (name, "collision")
        self.snapshot_name, self.snapshot_source = name, source_id

    def snapshot_requested(self, stderr: str | bytes | None) -> None:
        assert self.snapshot_name is not None, "unattempted capture"
        if isinstance(stderr, bytes):
            stderr = stderr.decode()
        ids = re.findall(
            r"^Snapshot request ID: ([0-9a-f]{64})$", stderr or "", re.MULTILINE
        )
        assert len(ids) <= 1, stderr
        if ids:
            assert all(row["id"] != ids[0] for row in self.baseline["snapshot"]), ids
            assert self.snapshot_request is None or self.snapshot_request == ids[0], ids
            self.snapshot_request = ids[0]

    def snapshot_created(self, published: dict) -> str:
        receipt = self._identity("snapshot", published)
        assert (receipt[1], receipt[3]) == (self.snapshot_name, self.snapshot_source), (
            receipt
        )
        assert all(row["id"] != receipt[0] for row in self.baseline["snapshot"]), (
            receipt
        )
        assert self.snapshot_request is None or self.snapshot_request == receipt[0], (
            receipt
        )
        assert self.snapshot_receipt is None or self.snapshot_receipt == receipt, (
            receipt
        )
        self.snapshot_receipt = receipt
        self.snapshot_request = receipt[0]
        return receipt[0]

    def remove_vm(self, name: str) -> None:
        retained = self.vm_attempts[name]
        first = _persistence_inventory(self.runner_vm, "vm")
        second = _persistence_inventory(self.runner_vm, "vm")
        matches = [
            row
            for row in first
            if row["name"] == name or (retained and row["id"] == retained[0])
        ]
        current = [
            row
            for row in second
            if row["name"] == name or (retained and row["id"] == retained[0])
        ]
        if matches or current:
            # A partial publication is admitted only after this case's create attempt.
            identifier = self.vm_created(name, matches)
            assert (
                len(current) == 1
                and self._identity("vm", current[0]) == self.vm_attempts[name]
            ), current
            _run_mvm(self.runner_vm, "vm", "rm", identifier, "--force", timeout=120)
        receipt = self.vm_attempts[name]
        assert all(
            row["name"] != name and (receipt is None or row["id"] != receipt[0])
            for row in _persistence_inventory(self.runner_vm, "vm")
        ), name

    def remove_snapshot(self) -> None:
        if self.snapshot_name is None:
            return
        first = _persistence_inventory(self.runner_vm, "snapshot")
        second = _persistence_inventory(self.runner_vm, "snapshot")
        matches = [
            row
            for row in first
            if row["name"] == self.snapshot_name or row["id"] == self.snapshot_request
        ]
        current = [
            row
            for row in second
            if row["name"] == self.snapshot_name or row["id"] == self.snapshot_request
        ]
        if matches or current:
            assert len(matches) == len(current) == 1, (matches, current)
            self.snapshot_created(matches[0])
            assert self._identity("snapshot", current[0]) == self.snapshot_receipt, (
                current
            )
        if self.snapshot_request is not None:
            # The pre-effect full ID also removes a root capture lacking a local projection.
            _run_mvm(self.runner_vm, "snapshot", "rm", self.snapshot_request, "--force")
        assert all(
            row["name"] != self.snapshot_name and row["id"] != self.snapshot_request
            for row in _persistence_inventory(self.runner_vm, "snapshot")
        ), self.snapshot_name

    def cleanup(self) -> None:
        failures = []
        for name in reversed(list(self.vm_attempts)):
            try:
                self.remove_vm(name)
            except Exception as error:
                failures.append(f"vm/{name}: {error}")
        try:
            self.remove_snapshot()
        except Exception as error:
            failures.append(f"snapshot/{self.snapshot_name}: {error}")
        for domain, baseline in self.baseline.items():
            try:
                assert _persistence_inventory(self.runner_vm, domain) == baseline, (
                    domain
                )
            except Exception as error:
                failures.append(f"{domain} baseline: {error}")
        try:
            assert (
                _json_success_data(
                    _run_mvm(self.runner_vm, "config", "ls", "--json").stdout,
                    dict,
                )
                == self.settings
            )
        except Exception as error:
            failures.append(f"config baseline: {error}")
        assert not failures, "\n".join(failures)


@pytest.fixture
def persistence_owner(runner_vm: str) -> Generator[_RootfsPersistenceOwner, None, None]:
    _require_persistence_assets(runner_vm)
    owner = _RootfsPersistenceOwner(runner_vm)
    try:
        yield owner
    finally:
        owner.cleanup()


def test_copied_rootfs_data_survives_independent_snapshot_restore(
    runner_vm: str,
    tmp_path: Path,
    persistence_owner: _RootfsPersistenceOwner,
) -> None:
    """A copied file survives capture; later source writes do not change the restored rootfs."""
    suffix = uuid.uuid4().hex[:8]
    source_vm = f"sys-rootfs-src-{suffix}"
    restored_vm = f"sys-rootfs-restored-{suffix}"
    snapshot_name = f"sys-rootfs-snapshot-{suffix}"
    guest_path = f"/root/.mvm-rootfs-persistence-{suffix}"
    original = f"before-snapshot-{uuid.uuid4().hex}\n"
    modified = f"after-snapshot-{uuid.uuid4().hex}"
    payload = tmp_path / "payload.txt"
    payload.write_text(original)
    owner = persistence_owner
    owner.before_vm_create(source_vm)
    source_id = owner.vm_created(
        source_vm,
        _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                source_vm,
                "--image",
                "alpine:3.23",
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
            ).stdout,
            list,
        ),
    )
    _run_mvm(runner_vm, "cp", str(payload), f"{source_id}:{guest_path}")
    assert (
        _run_mvm(runner_vm, "exec", source_id, "--", "cat", guest_path).stdout
        == original
    )
    _run_mvm(runner_vm, "exec", source_id, "--", "sync")

    owner.before_snapshot_create(snapshot_name, source_id)
    try:
        capture_result = _run_mvm(
            runner_vm,
            "snapshot",
            "create",
            source_id,
            "--name",
            snapshot_name,
            "--json",
            check=False,
            timeout=180,
        )
    except subprocess.TimeoutExpired as error:
        owner.snapshot_requested(error.stderr)
        raise
    owner.snapshot_requested(capture_result.stderr)
    captured = _json_success_data(capture_result.stdout, dict)
    snapshot_id = owner.snapshot_created(captured)
    assert all(
        captured[key]
        for key in ("snapshot_dir", "memory_file", "state_file", "rootfs_file")
    ), captured
    assert capture_result.returncode == 0, (
        capture_result.stdout,
        capture_result.stderr,
    )

    payload.write_text(modified + "\n")
    _run_mvm(runner_vm, "cp", str(payload), f"{source_id}:{guest_path}", "--force")
    _run_mvm(runner_vm, "exec", source_id, "--", "sync")
    assert (
        _run_mvm(runner_vm, "exec", source_id, "--", "cat", guest_path).stdout.strip()
        == modified
    )
    owner.remove_vm(source_vm)

    owner.before_vm_create(restored_vm)
    restored_id = owner.vm_created(
        restored_vm,
        _json_success_data(
            _run_mvm(
                runner_vm,
                "snapshot",
                "restore",
                snapshot_id,
                restored_vm,
                "--resume",
                "--json",
                timeout=180,
            ).stdout,
            list,
        ),
    )
    assert restored_id != source_id, (restored_id, source_id)
    assert (
        _run_mvm(runner_vm, "exec", restored_id, "--timeout", "30", "--", "cat", guest_path).stdout
        == original
    )
