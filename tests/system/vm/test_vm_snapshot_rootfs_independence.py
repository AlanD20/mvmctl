"""Verify snapshot restore gives the restored guest an independent rootfs.

Launch and restore use protected backing disks, separate from caller-owned
staging paths reported by VM inspection. These tests read and write synced
guest markers through mvm exec to verify independence from the source VM
and other restored VMs. Positive cases use networkless snapshots so multiple
restores do not clone a managed IP/MAC endpoint. A managed companion capture
checks captured-network replacement and source-held endpoint refusals.
"""

from __future__ import annotations

import concurrent.futures
import json
import shlex
import uuid

import pytest

from tests.system.conftest import (
    _json_success_data,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
)

MARKER_PATH = "/root/.snapshot-marker"


def _read_rootfs_marker(runner_vm: str, vm_name: str) -> str:
    """Read the marker from the running guest's active rootfs over vsock."""
    result = _run_mvm(
        runner_vm, "exec", vm_name, "--timeout", "60", "--", "cat", MARKER_PATH,
    )
    return result.stdout.strip()


def _write_rootfs_marker(runner_vm: str, vm_name: str, content: str) -> None:
    """Write and sync a guest marker to verify other VMs are unaffected."""
    _run_mvm(
        runner_vm, "exec", vm_name, "--timeout", "60", "--",
        f"printf '%s' {shlex.quote(content)} > {MARKER_PATH} && sync",
    )


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
    snaps = _json_success_data(ls_result.stdout, list)
    src_snaps = [s for s in snaps if s.get("source_vm_name") == src_vm]
    assert src_snaps, f"no snapshots found for {src_vm}"
    return src_snaps[-1]["id"]


class TestRootfsIndependence:
    """Snapshot restore must not share the source VM's modified rootfs."""

    def test_rootfs_independent_while_source_runs(
        self,
        runner_vm: str,
        unique_vm_name: str,
    ) -> None:
        """Source VM modified after snapshot; restored VM must NOT see changes."""
        src_vm = unique_vm_name
        restored_vm = f"rst-{src_vm}"
        snap_id: str | None = None

        try:
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--no-network", "--cloud-init-mode", "off", "--no-pci",
                     "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                     "--writeback", timeout=180)

            # Write marker BEFORE snapshot
            _write_rootfs_marker(runner_vm, src_vm, "ORIGINAL")
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
            _write_rootfs_marker(runner_vm, src_vm, "MODIFIED-AFTER-SNAPSHOT")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "MODIFIED-AFTER-SNAPSHOT", \
                "source VM should see modified content"

            # Restore beside the running networkless source
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, restored_vm, timeout=180)
            _run_mvm(runner_vm, "vm", "resume", restored_vm)

            # Verify restored guest has ORIGINAL (NOT modified)
            marker = _read_rootfs_marker(runner_vm, restored_vm)
            assert marker == "ORIGINAL", \
                f"restored VM rootfs should have ORIGINAL, got: {marker}"

        finally:
            for name in (restored_vm, src_vm):
                listed = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
                if any(vm["name"] == name for vm in listed):
                    _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
                assert all(vm["name"] != name for vm in _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
                ))
            for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ):
                if snapshot["source_vm_name"] == src_vm:
                    _run_mvm(runner_vm, "snapshot", "rm", snapshot["id"], "--force", timeout=120)
            assert all(snapshot["source_vm_name"] != src_vm for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ))

    def test_rootfs_independent_after_source_deleted(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """Source VM deleted after snapshot; restored VM must still get correct rootfs."""
        src_vm = unique_vm_name
        endpoint_vm = f"endpoint-{src_vm}"
        refused_vm = f"denied-{src_vm}"
        src_net = unique_network_name
        other_net = f"other-{uuid.uuid4().hex[:8]}"
        restored_vm = f"rst-{src_vm}"
        snap_id: str | None = None

        try:
            ensure_vm_deps(runner_vm)

            # Managed captures cannot replace or duplicate their captured endpoint.
            _run_mvm(runner_vm, "network", "create", src_net,
                     "--subnet", _unique_subnet(src_net), "--no-nat")
            _run_mvm(runner_vm, "network", "create", other_net,
                     "--subnet", _unique_subnet(other_net), "--no-nat")
            managed = _json_success_data(_run_mvm(
                runner_vm, "vm", "create", endpoint_vm, "--image", "alpine:3.23",
                "--network", src_net, "--cloud-init-mode", "off", "--no-pci",
                "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                "--writeback", "--json", timeout=180,
            ).stdout, list)[0]
            _write_rootfs_marker(runner_vm, endpoint_vm, "MANAGED-SOURCE")
            _run_mvm(runner_vm, "snapshot", "create", endpoint_vm)
            endpoint_snap = _get_snap_id(runner_vm, endpoint_vm)
            for network, message in (
                (other_net, "snapshot restore cannot replace the captured network, IP, or MAC"),
                (src_net, f"captured IP {managed['ipv4']} is already leased on network {managed['network_id']}"),
            ):
                before = sorted((vm["id"], vm["name"], vm["status"]) for vm in _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
                ))
                denied = _run_mvm(
                    runner_vm, "snapshot", "restore", endpoint_snap, refused_vm,
                    "--network", network, "--json", check=False,
                )
                assert denied.returncode != 0, (denied.stdout, denied.stderr)
                error = json.loads(denied.stdout)
                assert error["schema_version"] == 1 and error["status"] == "error", error
                assert error["data"] is None and error["error"]["code"] == "snapshot.restore_failed", error
                assert message in error["error"]["message"], error
                assert sorted((vm["id"], vm["name"], vm["status"]) for vm in _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
                )) == before
                assert _read_rootfs_marker(runner_vm, endpoint_vm) == "MANAGED-SOURCE"
            _run_mvm(runner_vm, "vm", "rm", endpoint_vm, "--force", timeout=120)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--no-network", "--cloud-init-mode", "off", "--no-pci",
                     "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                     "--writeback", timeout=180)

            # Write marker
            _write_rootfs_marker(runner_vm, src_vm, "PERSISTENT-MARKER")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "PERSISTENT-MARKER"

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)

            # Delete source VM (leaves snapshot untouched)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force", timeout=120)
            assert all(vm["name"] != src_vm for vm in _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
            ))

            # Restore to new VM
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, restored_vm, timeout=180)
            _run_mvm(runner_vm, "vm", "resume", restored_vm)

            # Verify marker survived in the restored VM's rootfs
            marker = _read_rootfs_marker(runner_vm, restored_vm)
            assert marker == "PERSISTENT-MARKER", \
                f"restored VM rootfs should have PERSISTENT-MARKER, got: {marker}"

        finally:
            for name in (refused_vm, restored_vm, src_vm, endpoint_vm):
                listed = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
                if any(vm["name"] == name for vm in listed):
                    _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
                assert all(vm["name"] != name for vm in _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
                ))
            for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ):
                if snapshot["source_vm_name"] in (src_vm, endpoint_vm):
                    _run_mvm(runner_vm, "snapshot", "rm", snapshot["id"], "--force", timeout=120)
            assert all(snapshot["source_vm_name"] not in (src_vm, endpoint_vm) for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ))
            for network in (other_net, src_net):
                listed = _json_success_data(_run_mvm(runner_vm, "network", "ls", "--json").stdout, list)
                if any(item["name"] == network for item in listed):
                    _run_mvm(runner_vm, "network", "rm", network, "--force", timeout=120)
                assert all(item["name"] != network for item in _json_success_data(
                    _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
                ))

    def test_source_stopped_not_deleted(
        self,
        runner_vm: str,
        unique_vm_name: str,
    ) -> None:
        """Source VM stopped but retained; restored VM must be independent."""
        src_vm = unique_vm_name
        restored_vm = f"rst-{src_vm}"
        snap_id: str | None = None

        try:
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--no-network", "--cloud-init-mode", "off", "--no-pci",
                     "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                     "--writeback", timeout=180)

            # Write marker
            _write_rootfs_marker(runner_vm, src_vm, "STOPPED-NOT-DELETED")

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)

            # Stop source VM while retaining its resources
            _run_mvm(runner_vm, "vm", "stop", src_vm, "--force")
            source = [vm for vm in _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
            ) if vm["name"] == src_vm]
            assert len(source) == 1 and source[0]["status"] == "stopped", source

            # Restore to new VM
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, restored_vm, timeout=180)
            _run_mvm(runner_vm, "vm", "resume", restored_vm)

            # Verify marker in restored VM's rootfs
            marker = _read_rootfs_marker(runner_vm, restored_vm)
            assert marker == "STOPPED-NOT-DELETED", \
                f"restored VM rootfs should have STOPPED-NOT-DELETED, got: {marker}"

            _write_rootfs_marker(runner_vm, restored_vm, "MODIFIED-IN-RESTORED")
            assert _read_rootfs_marker(runner_vm, restored_vm) == "MODIFIED-IN-RESTORED"
            _run_mvm(runner_vm, "vm", "start", src_vm, timeout=180)
            assert _read_rootfs_marker(runner_vm, src_vm) == "STOPPED-NOT-DELETED"

        finally:
            for name in (restored_vm, src_vm):
                listed = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
                if any(vm["name"] == name for vm in listed):
                    _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
                assert all(vm["name"] != name for vm in _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
                ))
            for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ):
                if snapshot["source_vm_name"] == src_vm:
                    _run_mvm(runner_vm, "snapshot", "rm", snapshot["id"], "--force", timeout=120)
            assert all(snapshot["source_vm_name"] != src_vm for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ))

    def test_sequential_restores_independent(
        self,
        runner_vm: str,
        unique_vm_name: str,
    ) -> None:
        """Two sequential restores from the same snapshot must be independent.

        Verifies: source-VM changes don't leak into restored VMs,
        and modifications to one restored VM's rootfs (via mvm exec)
        don't affect the other restored VM or the source VM.
        """
        src_vm = unique_vm_name
        vm_b = f"seq-b-{uuid.uuid4().hex[:6]}"
        vm_c = f"seq-c-{uuid.uuid4().hex[:6]}"
        snap_id: str | None = None

        try:
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--no-network", "--cloud-init-mode", "off", "--no-pci",
                     "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                     "--writeback", timeout=180)

            # Write marker
            _write_rootfs_marker(runner_vm, src_vm, "ORIGINAL")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "ORIGINAL"

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)
            _run_mvm(runner_vm, "vm", "resume", src_vm)

            # --- First restore: VM B ---
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, vm_b, timeout=180)
            _run_mvm(runner_vm, "vm", "resume", vm_b)

            marker_b = _read_rootfs_marker(runner_vm, vm_b)
            assert marker_b == "ORIGINAL", \
                f"VM B should have ORIGINAL, got: {marker_b}"

            # --- Second restore: VM C ---
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, vm_c, timeout=180)
            _run_mvm(runner_vm, "vm", "resume", vm_c)

            marker_c = _read_rootfs_marker(runner_vm, vm_c)
            assert marker_c == "ORIGINAL", \
                f"VM C should have ORIGINAL, got: {marker_c}"

            # --- Three-way independence check ---
            # Modify VM B's active guest rootfs
            _write_rootfs_marker(runner_vm, vm_b, "MODIFIED-IN-B")
            assert _read_rootfs_marker(runner_vm, vm_b) == "MODIFIED-IN-B"

            # VM C must still have ORIGINAL
            marker_c2 = _read_rootfs_marker(runner_vm, vm_c)
            assert marker_c2 == "ORIGINAL", \
                f"VM C should STILL have ORIGINAL despite VM B modification, " \
                f"got: {marker_c2}"

            # Source VM must still have ORIGINAL (no leak from VM B's write)
            source_marker = _run_mvm(runner_vm, "exec", src_vm, "--",
                                     f"cat {MARKER_PATH}")
            assert source_marker.stdout.strip() == "ORIGINAL", \
                f"Source VM should STILL have ORIGINAL despite VM B modification, " \
                f"got: {source_marker.stdout.strip()}"

        finally:
            for name in (vm_c, vm_b, src_vm):
                listed = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
                if any(vm["name"] == name for vm in listed):
                    _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
                assert all(vm["name"] != name for vm in _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
                ))
            for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ):
                if snapshot["source_vm_name"] == src_vm:
                    _run_mvm(runner_vm, "snapshot", "rm", snapshot["id"], "--force", timeout=120)
            assert all(snapshot["source_vm_name"] != src_vm for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ))

    def test_multi_vm_restore(
        self,
        runner_vm: str,
        unique_vm_name: str,
    ) -> None:
        """Count-two refuses; two single-target restores prove disk independence.

        Verifies modifying one restored VM's rootfs (via mvm exec) does NOT
        affect the other restored VM from the same snapshot.
        """
        src_vm = unique_vm_name
        base_name = f"multi-{src_vm}"
        vm0 = base_name
        vm1 = f"{base_name}-2"
        snap_id: str | None = None

        try:
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--no-network", "--cloud-init-mode", "off", "--no-pci",
                     "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                     "--writeback", timeout=180)

            # Write marker
            _write_rootfs_marker(runner_vm, src_vm, "SHARED-MARKER")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "SHARED-MARKER"

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)
            _run_mvm(runner_vm, "vm", "resume", src_vm)

            # Count-two is unsupported and must not publish either target.
            before = sorted((vm["id"], vm["name"], vm["status"]) for vm in _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
            ))
            denied = _run_mvm(
                runner_vm, "snapshot", "restore", snap_id, base_name,
                "--count", "2", "--json", check=False,
            )
            assert denied.returncode != 0, (denied.stdout, denied.stderr)
            error = json.loads(denied.stdout)
            assert error["schema_version"] == 1 and error["status"] == "error", error
            assert error["data"] is None and error["error"]["code"] == "snapshot.restore_failed", error
            assert "exactly one target VM per invocation" in error["error"]["message"], error
            assert sorted((vm["id"], vm["name"], vm["status"]) for vm in _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
            )) == before

            # Two supported single-target restores exercise independent disks.
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, vm0, timeout=180)
            _run_mvm(runner_vm, "snapshot", "restore", snap_id, vm1, timeout=180)
            _run_mvm(runner_vm, "vm", "resume", vm0)
            _run_mvm(runner_vm, "vm", "resume", vm1)

            # Both must have the marker initially
            for vm in (vm0, vm1):
                marker = _read_rootfs_marker(runner_vm, vm)
                assert marker == "SHARED-MARKER", \
                    f"{vm} should have SHARED-MARKER, got: {marker}"

            # Modify VM0's active guest rootfs
            _write_rootfs_marker(runner_vm, vm0, "MODIFIED-IN-VM0")
            assert _read_rootfs_marker(runner_vm, vm0) == "MODIFIED-IN-VM0"

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

            _write_rootfs_marker(runner_vm, vm1, "MODIFIED-IN-VM1")
            assert _read_rootfs_marker(runner_vm, vm1) == "MODIFIED-IN-VM1"
            assert _read_rootfs_marker(runner_vm, vm0) == "MODIFIED-IN-VM0"
            assert _read_rootfs_marker(runner_vm, src_vm) == "SHARED-MARKER"

        finally:
            for name in (vm1, vm0, src_vm):
                listed = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
                if any(vm["name"] == name for vm in listed):
                    _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
                assert all(vm["name"] != name for vm in _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
                ))
            for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ):
                if snapshot["source_vm_name"] == src_vm:
                    _run_mvm(runner_vm, "snapshot", "rm", snapshot["id"], "--force", timeout=120)
            assert all(snapshot["source_vm_name"] != src_vm for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ))

    def test_concurrent_restores(
        self,
        runner_vm: str,
        unique_vm_name: str,
    ) -> None:
        """Concurrent restores from the same snapshot must be independent.

        Verifies concurrent restores produce independent rootfs images:
        modifying one does NOT affect the other or the source VM.
        """
        src_vm = unique_vm_name
        vm_b = f"cur-b-{uuid.uuid4().hex[:6]}"
        vm_c = f"cur-c-{uuid.uuid4().hex[:6]}"
        snap_id: str | None = None

        try:
            ensure_vm_deps(runner_vm)

            _run_mvm(runner_vm, "vm", "create", src_vm,
                     "--image", "alpine:3.23",
                     "--no-network", "--cloud-init-mode", "off", "--no-pci",
                     "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                     "--writeback", timeout=180)

            # Write marker
            _write_rootfs_marker(runner_vm, src_vm, "CONCURRENT-MARKER")
            result = _run_mvm(runner_vm, "exec", src_vm, "--", f"cat {MARKER_PATH}")
            assert result.stdout.strip() == "CONCURRENT-MARKER"

            # Snapshot
            _run_mvm(runner_vm, "vm", "pause", src_vm)
            _run_mvm(runner_vm, "snapshot", "create", src_vm)

            snap_id = _get_snap_id(runner_vm, src_vm)
            _run_mvm(runner_vm, "vm", "resume", src_vm)

            # Launch two restore commands concurrently.
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
                fut_b = executor.submit(
                    _run_mvm, runner_vm, "snapshot", "restore", snap_id, vm_b,
                    timeout=180,
                )
                fut_c = executor.submit(
                    _run_mvm, runner_vm, "snapshot", "restore", snap_id, vm_c,
                    timeout=180,
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
            # Modify VM B's active guest rootfs
            _write_rootfs_marker(runner_vm, vm_b, "MODIFIED-IN-B")
            assert _read_rootfs_marker(runner_vm, vm_b) == "MODIFIED-IN-B"

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
            for name in (vm_c, vm_b, src_vm):
                listed = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
                if any(vm["name"] == name for vm in listed):
                    _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
                assert all(vm["name"] != name for vm in _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
                ))
            for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ):
                if snapshot["source_vm_name"] == src_vm:
                    _run_mvm(runner_vm, "snapshot", "rm", snapshot["id"], "--force", timeout=120)
            assert all(snapshot["source_vm_name"] != src_vm for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ))
