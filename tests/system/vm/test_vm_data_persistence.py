"""Installed-candidate rootfs persistence through a full VM snapshot."""

from __future__ import annotations

import uuid
from pathlib import Path

import pytest

from tests.system.conftest import (
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
    ensure_vm_deps,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
    pytest.mark.tier2,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
]


def test_copied_rootfs_data_survives_independent_snapshot_restore(
    runner_vm: str, tmp_path: Path,
) -> None:
    """A copied file survives capture; later source writes do not change the restored rootfs."""
    ensure_vm_deps(runner_vm)
    suffix = uuid.uuid4().hex[:8]
    source_vm = f"sys-rootfs-src-{suffix}"
    restored_vm = f"sys-rootfs-restored-{suffix}"
    snapshot_name = f"sys-rootfs-snapshot-{suffix}"
    guest_path = f"/root/.mvm-rootfs-persistence-{suffix}"
    original = f"before-snapshot-{uuid.uuid4().hex}\n"
    modified = f"after-snapshot-{uuid.uuid4().hex}"
    payload = tmp_path / "payload.txt"
    payload.write_text(original)
    snapshot_id: str | None = None

    try:
        _run_mvm(
            runner_vm, "vm", "create", source_vm, "--image", "alpine:3.23",
            "--no-network", "--cloud-init-mode", "off", "--skip-deblob",
            "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
            timeout=180,
        )
        _run_mvm(runner_vm, "cp", str(payload), f"{source_vm}:{guest_path}")
        assert _run_mvm(runner_vm, "exec", source_vm, "--", "cat", guest_path).stdout == original
        _run_mvm(runner_vm, "exec", source_vm, "--", "sync")

        captured = _json_success_data(
            _run_mvm(
                runner_vm, "snapshot", "create", source_vm,
                "--name", snapshot_name, "--json", timeout=180,
            ).stdout,
            dict,
        )
        snapshot_id = captured["id"]
        assert captured["memory_file"] and captured["state_file"]

        payload.write_text(modified + "\n")
        _run_mvm(runner_vm, "cp", str(payload), f"{source_vm}:{guest_path}", "--force")
        _run_mvm(runner_vm, "exec", source_vm, "--", "sync")
        assert _run_mvm(runner_vm, "exec", source_vm, "--", "cat", guest_path).stdout.strip() == modified
        _run_mvm(runner_vm, "vm", "rm", source_vm, "--force", timeout=120)

        restored = _json_success_data(
            _run_mvm(
                runner_vm, "snapshot", "restore", snapshot_id, restored_vm,
                "--resume", "--json", timeout=180,
            ).stdout,
            list,
        )
        assert len(restored) == 1 and restored[0]["name"] == restored_vm
        assert _run_mvm(runner_vm, "exec", restored_vm, "--", "cat", guest_path).stdout == original
    finally:
        for vm_name in (restored_vm, source_vm):
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if any(vm["name"] == vm_name for vm in listed):
                removed = _run_mvm(
                    runner_vm, "vm", "rm", vm_name, "--force", check=False, timeout=120,
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
        snapshots = _json_success_data(
            _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
        )
        for snapshot in snapshots:
            if snapshot["name"] == snapshot_name:
                removed = _run_mvm(
                    runner_vm, "snapshot", "rm", snapshot["id"], "--force", check=False,
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
        assert all(vm["name"] not in (source_vm, restored_vm) for vm in _parse_vm_list(
            _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
        ))
        assert all(snapshot["name"] != snapshot_name for snapshot in _json_success_data(
            _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
        ))
