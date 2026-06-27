"""Verify snapshot restore gives the restored VM an independent rootfs.

The vmstate file hardcodes the phantom rootfs path (captured at snapshot
create via PATCH). On restore, the phantom symlink is updated to point to
the new VM's rootfs copy. LoadSnapshot follows the symlink.

These tests verify the restored VM's rootfs is independent of both the
source VM and other restored VMs by reading the marker file directly
from the ext4 disk image via debugfs (vsock doesn't work reliably on
restored VMs; SSH can't reach the guest IP on a different network).
"""

from __future__ import annotations

import concurrent.futures
import json
import os
import subprocess
import tempfile
import uuid

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet, ensure_vm_deps, wait_for_ssh

MARKER_PATH = "/root/.snapshot-marker"


def _read_rootfs_marker(runner_vm: str, vm_name: str) -> str | None:
    """Read the marker file from a VM's rootfs ext4 image using debugfs.

    _run_mvm runs mvm commands directly on the test host (vm_name is
    ignored), so the rootfs path is on the local filesystem and
    debugfs can access it directly.
    """
    result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
    info = json.loads(result.stdout)
    fs_info = info.get("filesystem", {})
    rootfs_path = fs_info.get("rootfs_path", "")
    if not rootfs_path:
        return None
    result = subprocess.run(
        ["debugfs", "-R", f"cat {MARKER_PATH}", rootfs_path],
        capture_output=True, text=True, check=False,
    )
    if result.returncode != 0:
        # debugfs may fail if the rootfs is not an ext4 image or not mounted
        return None
    return result.stdout.strip()


def _write_rootfs_marker(runner_vm: str, vm_name: str, content: str) -> None:
    """Write content to the marker file in a VM's rootfs ext4 image via debugfs.
    Used to verify other VMs are unaffected by the write.
    """
    result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
    info = json.loads(result.stdout)
    fs_info = info.get("filesystem", {})
    rootfs_path = fs_info.get("rootfs_path", "")
    assert rootfs_path, f"no rootfs_path for {vm_name}"
    with tempfile.NamedTemporaryFile(mode="w", delete=False) as f:
        f.write(content)
        tmp_path = f.name
    try:
        subprocess.run(
            ["debugfs", "-w", "-R", f"rm {MARKER_PATH}", rootfs_path],
            capture_output=True, text=True, check=False,
        )
        subprocess.run(
            ["debugfs", "-w", "-R", f"write {tmp_path} {MARKER_PATH}", rootfs_path],
            capture_output=True, text=True, check=True,
        )
    finally:
        os.unlink(tmp_path)


pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_vm,
    pytest.mark.tier3,
]


def _get_snap_id(runner_vm: str, src_vm: str) -> str:
    """Return the most recent snapshot ID for the given source VM."""
    ls_result = _run_mvm(runner_vm, "snapshot", "ls", "--json")
    snaps = json.loads(ls_result.stdout)
    src_snaps = [s for s in snaps if s.get("source_vm_name") == src_vm]
    assert src_snaps, f"no snapshots found for {src_vm}"
    return src_snaps[-1]["id"]


class TestRootfsIndependence:
    """Snapshot restore must not share the source VM's modified rootfs."""

    def test_rootfs_independent_while_source_runs(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Source VM modified after snapshot; restored VM must NOT see changes."""
        src_vm = unique_vm_name
        key_name = unique_key_name
        src_net = unique_network_name
        src_subnet = _unique_subnet(src_net)
        restored_vm = f"rst-{src_vm}"
        restored_net = f"net-{uuid.uuid4().hex[:6]}"
        restored_subnet = _unique_subnet(restored_net)
        snap_id: str | None = None

        try:
            _run_mvm(runner_vm, "network", "create", src_net,
                     "--subnet", src_subnet, "--non-interactive")
            _run_mvm(runner_vm, "network", "create", restored_net,
                     "--subnet", restored_subnet, "--non-interactive")
            _run_mvm(runner_vm, "key", "create", key_name,
                     "--algorithm", "ed25519")
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--network", src_net,
                     "--ssh-key", key_name,
                     "--writeback")

            assert wait_for_ssh(runner_vm, src_vm), f"SSH timeout on source VM {src_vm}"

            # Write marker BEFORE snapshot
            _run_mvm(runner_vm, "ssh", src_vm, "-u", "root",
                     "--cmd", f"echo 'ORIGINAL' > {MARKER_PATH} && sync")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "ORIGINAL"

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)
            _run_mvm(runner_vm, "vm", "resume", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)

            # Verify source VM survived snapshot create
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "ORIGINAL", \
                "source VM should still have ORIGINAL after snapshot resume"

            # Modify marker AFTER snapshot in source VM
            _run_mvm(runner_vm, "ssh", src_vm, "-u", "root",
                     "--cmd", f"echo 'MODIFIED-AFTER-SNAPSHOT' > {MARKER_PATH} && sync")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "MODIFIED-AFTER-SNAPSHOT", \
                "source VM should see modified content"

            # Restore to new VM on different network
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, restored_vm,
                     "--network", restored_net)
            _run_mvm(runner_vm, "vm", "resume", restored_vm)

            # Verify restored VM's rootfs on disk has ORIGINAL (NOT modified)
            marker = _read_rootfs_marker(runner_vm, restored_vm)
            assert marker == "ORIGINAL", \
                f"restored VM rootfs should have ORIGINAL, got: {marker}"

        finally:
            _run_mvm(runner_vm, "vm", "rm", restored_vm, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force", check=False)
            if snap_id:
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", restored_net, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "network", "rm", src_net, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_rootfs_independent_after_source_deleted(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Source VM deleted after snapshot; restored VM must still get correct rootfs."""
        src_vm = unique_vm_name
        key_name = unique_key_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        restored_vm = f"rst-{src_vm}"
        snap_id: str | None = None

        try:
            _run_mvm(runner_vm, "network", "create", net_name,
                     "--subnet", subnet, "--non-interactive")
            _run_mvm(runner_vm, "key", "create", key_name,
                     "--algorithm", "ed25519")
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--network", net_name,
                     "--ssh-key", key_name,
                     "--writeback")

            assert wait_for_ssh(runner_vm, src_vm), f"SSH timeout on source VM {src_vm}"

            # Write marker
            _run_mvm(runner_vm, "ssh", src_vm, "-u", "root",
                     "--cmd", f"echo 'PERSISTENT-MARKER' > {MARKER_PATH} && sync")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "PERSISTENT-MARKER"

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)

            # Delete source VM (leaves snapshot untouched)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force")

            # Restore to new VM
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, restored_vm,
                     "--network", net_name)
            _run_mvm(runner_vm, "vm", "resume", restored_vm)

            # Verify marker survived in the restored VM's rootfs
            marker = _read_rootfs_marker(runner_vm, restored_vm)
            assert marker == "PERSISTENT-MARKER", \
                f"restored VM rootfs should have PERSISTENT-MARKER, got: {marker}"

        finally:
            _run_mvm(runner_vm, "vm", "rm", restored_vm, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force", check=False)
            if snap_id:
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_source_stopped_not_deleted(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Source VM stopped (dir+rootfs still on disk); restored VM must be independent."""
        src_vm = unique_vm_name
        key_name = unique_key_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        restored_vm = f"rst-{src_vm}"
        snap_id: str | None = None

        try:
            _run_mvm(runner_vm, "network", "create", net_name,
                     "--subnet", subnet, "--non-interactive")
            _run_mvm(runner_vm, "key", "create", key_name,
                     "--algorithm", "ed25519")
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--network", net_name,
                     "--ssh-key", key_name,
                     "--writeback")

            assert wait_for_ssh(runner_vm, src_vm), f"SSH timeout on source VM {src_vm}"

            # Write marker
            _run_mvm(runner_vm, "ssh", src_vm, "-u", "root",
                     "--cmd", f"echo 'STOPPED-NOT-DELETED' > {MARKER_PATH} && sync")

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)

            # Stop source VM — directory and rootfs file stay on disk
            _run_mvm(runner_vm, "vm", "stop", src_vm, "--force")

            # Restore to new VM
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, restored_vm,
                     "--network", net_name)
            _run_mvm(runner_vm, "vm", "resume", restored_vm)

            # Verify marker in restored VM's rootfs
            marker = _read_rootfs_marker(runner_vm, restored_vm)
            assert marker == "STOPPED-NOT-DELETED", \
                f"restored VM rootfs should have STOPPED-NOT-DELETED, got: {marker}"

        finally:
            _run_mvm(runner_vm, "vm", "rm", restored_vm, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force", check=False)
            if snap_id:
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_sequential_restores_independent(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Two sequential restores from the same snapshot must be independent.

        Verifies: source-VM changes don't leak into restored VMs,
        and modifications to one restored VM's rootfs (via debugfs)
        don't affect the other restored VM or the source VM.
        """
        src_vm = unique_vm_name
        key_name = unique_key_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        vm_b = f"seq-b-{uuid.uuid4().hex[:6]}"
        vm_c = f"seq-c-{uuid.uuid4().hex[:6]}"
        net_b = f"net-b-{uuid.uuid4().hex[:6]}"
        net_c = f"net-c-{uuid.uuid4().hex[:6]}"
        sub_b = _unique_subnet(net_b)
        sub_c = _unique_subnet(net_c)
        snap_id: str | None = None

        try:
            _run_mvm(runner_vm, "network", "create", net_name,
                     "--subnet", subnet, "--non-interactive")
            _run_mvm(runner_vm, "network", "create", net_b,
                     "--subnet", sub_b, "--non-interactive")
            _run_mvm(runner_vm, "network", "create", net_c,
                     "--subnet", sub_c, "--non-interactive")
            _run_mvm(runner_vm, "key", "create", key_name,
                     "--algorithm", "ed25519")
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--network", net_name,
                     "--ssh-key", key_name,
                     "--writeback")

            assert wait_for_ssh(runner_vm, src_vm), f"SSH timeout on source VM {src_vm}"

            # Write marker
            _run_mvm(runner_vm, "ssh", src_vm, "-u", "root",
                     "--cmd", f"echo 'ORIGINAL' > {MARKER_PATH} && sync")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "ORIGINAL"

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)
            _run_mvm(runner_vm, "vm", "resume", src_vm)

            # --- First restore: VM B ---
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, vm_b,
                     "--network", net_b)
            _run_mvm(runner_vm, "vm", "resume", vm_b)

            marker_b = _read_rootfs_marker(runner_vm, vm_b)
            assert marker_b == "ORIGINAL", \
                f"VM B should have ORIGINAL, got: {marker_b}"

            # --- Second restore: VM C ---
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, vm_c,
                     "--network", net_c)
            _run_mvm(runner_vm, "vm", "resume", vm_c)

            marker_c = _read_rootfs_marker(runner_vm, vm_c)
            assert marker_c == "ORIGINAL", \
                f"VM C should have ORIGINAL, got: {marker_c}"

            # --- Three-way independence check ---
            # Modify VM B's rootfs via debugfs on disk
            _write_rootfs_marker(runner_vm, vm_b, "MODIFIED-IN-B")

            # VM C must still have ORIGINAL
            marker_c2 = _read_rootfs_marker(runner_vm, vm_c)
            assert marker_c2 == "ORIGINAL", \
                f"VM C should STILL have ORIGINAL despite VM B modification, " \
                f"got: {marker_c2}"

            # Source VM must still have ORIGINAL (no leak from debugfs write)
            source_marker = _run_mvm(runner_vm, "exec", src_vm, "--",
                                     f"cat {MARKER_PATH}")
            assert source_marker.stdout.strip() == "ORIGINAL", \
                f"Source VM should STILL have ORIGINAL despite VM B modification, " \
                f"got: {source_marker.stdout.strip()}"

        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_c, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", vm_b, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force", check=False)
            if snap_id:
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_c, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "network", "rm", net_b, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "network", "rm", net_name, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_multi_vm_restore(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Restore multiple VMs from one snapshot via --count 2; verify independence.

        Verifies modifying one restored VM's rootfs (via debugfs) does NOT
        affect the other restored VM from the same --count restore.
        """
        src_vm = unique_vm_name
        key_name = unique_key_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        base_name = f"multi-{src_vm}"
        vm0 = base_name        # first VM keeps base name
        vm1 = f"{base_name}-2"  # second VM gets -2 suffix
        snap_id: str | None = None

        try:
            _run_mvm(runner_vm, "network", "create", net_name,
                     "--subnet", subnet, "--non-interactive")
            _run_mvm(runner_vm, "key", "create", key_name,
                     "--algorithm", "ed25519")
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--network", net_name,
                     "--ssh-key", key_name,
                     "--writeback")

            assert wait_for_ssh(runner_vm, src_vm), f"SSH timeout on source VM {src_vm}"

            # Write marker
            _run_mvm(runner_vm, "ssh", src_vm, "-u", "root",
                     "--cmd", f"echo 'SHARED-MARKER' > {MARKER_PATH} && sync")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "SHARED-MARKER"

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)
            _run_mvm(runner_vm, "vm", "resume", src_vm)

            # Restore 2 VMs from the snapshot
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, base_name,
                     "--network", net_name, "--count", "2")
            _run_mvm(runner_vm, "vm", "resume", vm0)
            _run_mvm(runner_vm, "vm", "resume", vm1)

            # Both must have the marker initially
            for vm in (vm0, vm1):
                marker = _read_rootfs_marker(runner_vm, vm)
                assert marker == "SHARED-MARKER", \
                    f"{vm} should have SHARED-MARKER, got: {marker}"

            # Modify VM0's rootfs via debugfs
            _write_rootfs_marker(runner_vm, vm0, "MODIFIED-IN-VM0")

            # VM1 must still have SHARED-MARKER (three-way independence)
            marker_vm1 = _read_rootfs_marker(runner_vm, vm1)
            assert marker_vm1 == "SHARED-MARKER", \
                f"VM1 should STILL have SHARED-MARKER despite VM0 modification, " \
                f"got: {marker_vm1}"

            # Source VM must still have SHARED-MARKER
            source_marker = _run_mvm(runner_vm, "exec", src_vm, "--",
                                     f"cat {MARKER_PATH}")
            assert source_marker.stdout.strip() == "SHARED-MARKER", \
                f"Source VM should STILL have SHARED-MARKER despite VM0 modification, " \
                f"got: {source_marker.stdout.strip()}"

        finally:
            _run_mvm(runner_vm, "vm", "rm", vm1, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", vm0, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force", check=False)
            if snap_id:
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_concurrent_restores(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Concurrent restores from same snapshot; .restore.lock must serialize.

        Verifies concurrent restores produce independent rootfs images:
        modifying one does NOT affect the other or the source VM.
        """
        src_vm = unique_vm_name
        key_name = unique_key_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        vm_b = f"cur-b-{uuid.uuid4().hex[:6]}"
        vm_c = f"cur-c-{uuid.uuid4().hex[:6]}"
        net_b = f"net-b-{uuid.uuid4().hex[:6]}"
        net_c = f"net-c-{uuid.uuid4().hex[:6]}"
        sub_b = _unique_subnet(net_b)
        sub_c = _unique_subnet(net_c)
        snap_id: str | None = None

        try:
            _run_mvm(runner_vm, "network", "create", net_name,
                     "--subnet", subnet, "--non-interactive")
            _run_mvm(runner_vm, "network", "create", net_b,
                     "--subnet", sub_b, "--non-interactive")
            _run_mvm(runner_vm, "network", "create", net_c,
                     "--subnet", sub_c, "--non-interactive")
            _run_mvm(runner_vm, "key", "create", key_name,
                     "--algorithm", "ed25519")
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--network", net_name,
                     "--ssh-key", key_name,
                     "--writeback")

            assert wait_for_ssh(runner_vm, src_vm), f"SSH timeout on source VM {src_vm}"

            # Write marker
            _run_mvm(runner_vm, "ssh", src_vm, "-u", "root",
                     "--cmd", f"echo 'CONCURRENT-MARKER' > {MARKER_PATH} && sync")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "CONCURRENT-MARKER"

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)
            _run_mvm(runner_vm, "vm", "resume", src_vm)

            # Launch two restore commands concurrently.
            # The .restore.lock serializes phantom symlink updates.
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
                fut_b = executor.submit(
                    _run_mvm, runner_vm, "snapshot", "restore", snap_id, vm_b,
                    "--network", net_b,
                )
                fut_c = executor.submit(
                    _run_mvm, runner_vm, "snapshot", "restore", snap_id, vm_c,
                    "--network", net_c,
                )

                done, not_done = concurrent.futures.wait(
                    [fut_b, fut_c], timeout=180, return_when=concurrent.futures.ALL_COMPLETED,
                )
                assert len(not_done) == 0, \
                    f"concurrent restore(s) timed out: {[f for f in not_done]}"

                for f, name in [(fut_b, vm_b), (fut_c, vm_c)]:
                    exc = f.exception()
                    assert exc is None, f"restore to {name} failed: {exc}"

            # Resume each VM individually
            for vm in (vm_b, vm_c):
                _run_mvm(runner_vm, "vm", "resume", vm)

            # Both VMs must have the marker from the snapshot
            for vm in (vm_b, vm_c):
                marker = _read_rootfs_marker(runner_vm, vm)
                assert marker == "CONCURRENT-MARKER", \
                    f"{vm} should have CONCURRENT-MARKER, got: {marker}"

            # --- Three-way independence check ---
            # Modify VM B's rootfs via debugfs
            _write_rootfs_marker(runner_vm, vm_b, "MODIFIED-IN-B")

            # VM C must still have CONCURRENT-MARKER
            marker_c2 = _read_rootfs_marker(runner_vm, vm_c)
            assert marker_c2 == "CONCURRENT-MARKER", \
                f"VM C should STILL have CONCURRENT-MARKER despite VM B modification, " \
                f"got: {marker_c2}"

            # Source VM must still have CONCURRENT-MARKER
            source_marker = _run_mvm(runner_vm, "exec", src_vm, "--",
                                     f"cat {MARKER_PATH}")
            assert source_marker.stdout.strip() == "CONCURRENT-MARKER", \
                f"Source VM should STILL have CONCURRENT-MARKER despite VM B modification, " \
                f"got: {source_marker.stdout.strip()}"

        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_c, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", vm_b, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force", check=False)
            if snap_id:
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_c, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "network", "rm", net_b, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "network", "rm", net_name, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
