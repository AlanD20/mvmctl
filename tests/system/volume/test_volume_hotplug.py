"""Volume hotplug/hot-unplug system tests — attach/detach to/from a running VM.

All tests in this file require a real running VM because they exercise
the PCI hotplug notification path inside the guest OS. A stopped VM cannot
process ACPI PCI hotplug events (Firecracker issues an ACPI notification
that the guest kernel must handle). Volume-only tests are covered in
test_volume.py.

Migrated from tests/e2e/volume/test_volume_hotplug.py.

VIOLATIONS REMOVED:
  - _require_firecracker_hotplug used pytest.skip() → replaced with pytest.fail()
  - All commands run through _run_mvm / _guest_run (no subprocess on host)
  - No pytest.skip() calls anywhere
  - Import from tests.system.conftest instead of tests.e2e.conftest
"""

from __future__ import annotations

import json
import subprocess
import time
import uuid

import pytest

from tests.system.conftest import (
    _ensure_image,
    _run_mvm,
    _unique_subnet,
    cleanup_vm_resources,
    create_vm_core,
    ensure_vm_deps,
    wait_for_ssh,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_volume,
]


# ============================================================================
# Helper functions
# ============================================================================


def _wait_for_vdb(runner_vm: str, vm_name: str, timeout: float = 10.0) -> bool:
    """Poll guest /proc/partitions via vsock exec until vdb appears or *timeout* expires.

    Uses ``mvm vm exec`` (vsock agent) instead of ``mvm ssh`` because the
    Alpine cloud image's SSHD configuration rejects publickey auth in nested
    virtualization (PAM + StrictModes). The vsock agent is always available.
    Uses ``cat /proc/partitions`` because Alpine lacks ``lsblk``.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = _run_mvm(
            runner_vm, "vm", "exec", vm_name, "--user", "root", "--timeout",
            "10", "--", "cat /proc/partitions",
            check=False, timeout=15,
        )
        if result.returncode == 0 and "vdb" in result.stdout:
            return True
        time.sleep(0.5)
    return False


def _wait_for_no_vdb(
    runner_vm: str, vm_name: str, timeout: float = 10.0
) -> bool:
    """Poll guest /proc/partitions via vsock exec until vdb disappears or *timeout* expires."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = _run_mvm(
            runner_vm, "vm", "exec", vm_name, "--user", "root", "--timeout",
            "10", "--", "cat /proc/partitions",
            check=False, timeout=15,
        )
        if result.returncode == 0:
            vdb_found = "vdb" in result.stdout
            if not vdb_found:
                return True
        time.sleep(0.5)
    return False


def _require_firecracker_hotplug(runner_vm: str) -> None:
    """Fail the test if the default Firecracker binary doesn't support hotplug.

    Hotplug requires Firecracker v1.5+ (version gate in the API layer).
    This helper is called at the start of every hotplug test to ensure
    the environment is capable.
    """
    result = _run_mvm(
        runner_vm, "bin", "ls", "--json", timeout=30
    )
    if result.returncode != 0:
        pytest.fail("Cannot check Firecracker version (bin ls failed)")

    bins = json.loads(result.stdout)
    fc_default = next(
        (b for b in bins
         if b.get("type") == "firecracker" and b.get("is_default")),
        None,
    )
    if fc_default is None:
        pytest.fail("No default Firecracker binary set")
    version = fc_default.get("version", "")
    clean = version.lstrip("v").split("-")[0].split(".")[:2]
    try:
        major, minor = int(clean[0]), int(clean[1])
    except (ValueError, IndexError):
        pytest.fail(f"Firecracker version '''{version}''' is not parseable")
    if major < 1 or (major == 1 and minor < 5):
        pytest.fail(
            f"Hotplug tests require Firecracker v1.5+ "
            f"(current: v{version}). "
            f"Install a newer binary with: mvm bin pull firecracker <version>"
        )


def _count_virtio_block_devices(runner_vm: str, vm_name: str) -> int:
    result = _run_mvm(
        runner_vm, "vm", "exec", vm_name, "--",
        "sh -c \"ls /sys/block | grep '^vd[b-z]' | wc -l\"",
        check=False, timeout=10,
    )
    if result.returncode != 0:
        return 0
    try:
        return int(result.stdout.strip())
    except ValueError:
        return 0


# ============================================================================
# TestVolumeHotplug — hotplug a volume to a running VM
# ============================================================================


@pytest.fixture
def hotplug_vm(runner_vm) -> str:
    """Function-scoped VM (alpine, SSH) — one fresh VM per hotplug test.

    Previously module-scoped for speed, but PCI hotplug state leaks
    across tests (stale drive references / guest PCI slot reuse) causing
    later tests to fail waiting for /dev/vdb. Fresh VMs per test avoid
    this entirely.
    """
    import uuid
    vm_name = f"sys-hpmod-{uuid.uuid4().hex[:8]}"
    key_name = f"sys-hpmod-key-{uuid.uuid4().hex[:6]}"
    net_name = f"sys-hpmod-net-{uuid.uuid4().hex[:6]}"

    _run_mvm(runner_vm, "key", "create", key_name, "--algorithm", "ed25519", timeout=30)
    _run_mvm(runner_vm, "key", "default", key_name, check=False, timeout=10)
    _ensure_image(runner_vm, "alpine:3.23")
    create_vm_core(vm_name, net_name, ssh_key_name=key_name, image="alpine:3.23")
    wait_for_ssh(runner_vm, vm_name, "root", timeout=120)
    try:
        yield vm_name
    finally:
        cleanup_vm_resources(vm_name, net_name, key_name)


class TestVolumeHotplug:
    """Tests for hotplugging a volume to a running VM.

    All tests rely on the hotplug_vm fixture (module-scoped, SSH-based)
    which provides a single running VM shared across all tests in this class.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_volume,
        pytest.mark.slow,
        pytest.mark.requires_firecracker_116,
    ]

    @pytest.fixture(autouse=True)
    def _ensure_firecracker_ge_1_16(self, runner_vm: str) -> None:
        """Fail all hotplug tests if Firecracker is too old."""
        _require_firecracker_hotplug(runner_vm)

    def test_hotplug_volume_appears_in_guest(
        self,
        runner_vm: str,
        hotplug_vm: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because we are testing
        # PCI hotplug notification. The hotplug_vm fixture provides the VM
        # with SSH access. A volume (1-3s) is the cheapest resource that can
        # be hotplugged and observed inside the guest.
        """Hotplug a volume and verify the block device appears inside the guest."""
        vm_name: str = hotplug_vm
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        # VM is running — vsock agent is available immediately
        wait_for_ssh(runner_vm, vm_name, "root", 30.0)

        try:
            # -- Phase 1: Create and hotplug the volume --------------------
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )

            # Wait for Firecracker ACPI hotplug notification + guest processing
            time.sleep(3.0)
            assert _wait_for_vdb(runner_vm, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name} "
                f"after hotplug"
            )

            # -- Phase 2: Verify device in guest ---------------------------
            # Verify virtio driver is attached via sysfs
            driver_result = _run_mvm(
                runner_vm, "vm", "exec", vm_name, "--",
                "readlink /sys/block/vdb/device/driver",
                check=False, timeout=10,
            )
            assert driver_result.returncode == 0, (
                f"Could not read vdb driver symlink: "
                f"stdout={driver_result.stdout!r} stderr={driver_result.stderr!r}"
            )
            assert "virtio" in driver_result.stdout.lower(), (
                f"Expected virtio driver for vdb, got: {driver_result.stdout!r}"
            )

            # -- Phase 3: Verify CLI state ---------------------------------
            vols = json.loads(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} not found in listing"
            )
            assert vol_entry.get('status') == "attached", (
                f"Expected status '''attached''', got '''{vol_entry.get('status')}'''"
            )
        finally:
            _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )

    def test_hotplug_volume_format_and_mount(
        self,
        runner_vm: str,
        hotplug_vm: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because we need the
        # hotplugged block device to appear in the guest for filesystem
        # operations. A stopped VM has no kernel processing hotplug events.
        """Hotplug a volume, format it, mount it, write+read, then unmount."""
        vm_name: str = hotplug_vm
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        wait_for_ssh(runner_vm, vm_name, "root", 30.0)

        try:
            # -- Phase 1: Create and hotplug -------------------------------
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(runner_vm, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name}"
            )

            # -- Phase 2: Format the block device --------------------------
            fmt_result = _run_mvm(
                runner_vm,
            "vm",
            "exec",
            vm_name,
            "--",
                "mkfs.ext4 -F /dev/vdb",
                check=False,
                timeout=30,
            )
            assert fmt_result.returncode == 0, (
                f"mkfs.ext4 failed: {fmt_result.stderr}"
            )

            # -- Phase 3: Mount and write a file ---------------------------
            mount_result = _run_mvm(
                runner_vm,
            "vm",
            "exec",
            vm_name,
            "--",
                "mount /dev/vdb /mnt",
                check=False,
                timeout=10,
            )
            assert mount_result.returncode == 0, (
                f"mount failed: {mount_result.stderr}"
            )

            write_result = _run_mvm(
                runner_vm,
            "vm",
            "exec",
            vm_name,
            "--",
                "echo '''hotplug test data''' > /mnt/test-hotplug.txt",
                check=False,
                timeout=10,
            )
            assert write_result.returncode == 0, (
                f"write to volume failed: {write_result.stderr}"
            )

            # -- Phase 4: Read back and verify content ---------------------
            read_result = _run_mvm(
                runner_vm,
            "vm",
            "exec",
            vm_name,
            "--",
                "cat /mnt/test-hotplug.txt",
                check=False,
                timeout=10,
            )
            assert read_result.returncode == 0, (
                f"read from volume failed: {read_result.stderr}"
            )
            assert "hotplug test data" in read_result.stdout, (
                f"Expected '''hotplug test data''' in output, "
                f"got: {read_result.stdout!r}"
            )

            # -- Phase 5: Unmount ------------------------------------------
            umount_result = _run_mvm(
                runner_vm,
            "vm",
            "exec",
            vm_name,
            "--",
                "umount /mnt",
                check=False,
                timeout=10,
            )
            assert umount_result.returncode == 0, (
                f"umount failed: {umount_result.stderr}"
            )
        finally:
            _run_mvm(
                runner_vm,
            "vm",
            "exec",
            vm_name,
            "--",
                "umount /mnt",
                check=False,
                timeout=10,
            )
            _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )

    def test_hotplug_volume_hot_unplug(
        self,
        runner_vm: str,
        hotplug_vm: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because hot-unplug
        # (ACPI PCI detach notification) requires a running guest kernel.
        # A stopped VM cannot process or reflect hot-unplug events.
        """Hotplug a volume, then hot-unplug it and verify the device disappears."""
        vm_name: str = hotplug_vm
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        wait_for_ssh(runner_vm, vm_name, "root", 30.0)

        try:
            # -- Phase 1: Create, hotplug, verify presence -----------------
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(runner_vm, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name}"
            )

            # Count Virtio block devices before detach
            block_count_before = _count_virtio_block_devices(runner_vm, vm_name)
            assert block_count_before > 0, (
                "Expected at least one Virtio block device before detach"
            )

            # -- Phase 2: Hot-unplug ---------------------------------------
            _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
            )

            # Wait for hot-unplug to propagate
            assert _wait_for_no_vdb(runner_vm, vm_name, timeout=10.0), (
                f"Block device vdb did not disappear after hot-unplug "
                f"on VM {vm_name}"
            )

            # Verify fewer Virtio block devices after detach
            block_count_after = _count_virtio_block_devices(runner_vm, vm_name)
            assert block_count_after < block_count_before, (
                f"Block device count did not decrease after hot-unplug: "
                f"before={block_count_before}, after={block_count_after}"
            )

            # -- Phase 3: Verify CLI state ---------------------------------
            vols = json.loads(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist as '''available'''"
            )
            assert vol_entry.get('status') == "available", (
                f"Expected status '''available''' after detach, "
                f"got '''{vol_entry.get('status')}'''"
            )
        finally:
            _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )

    def test_hotplug_no_pci_guest_does_not_see_device(
        self,
        runner_vm: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because we need to
        # verify that the guest kernel does NOT see the hotplugged block
        # device when PCI is disabled.  A stopped VM cannot demonstrate the
        # absence of hotplug events.  The VM is created inline with --no-pci
        # since the hotplug_vm fixture always enables PCI.
        """Attach a volume to a VM created with --no-pci — device should
        NOT appear inside the guest because there are no ACPI PCI hotplug
        notifications.
        """
        vm_name: str = f"sys-hp-nopci-{uuid.uuid4().hex[:8]}"
        key_name: str = f"sys-hp-key-{uuid.uuid4().hex[:6]}"
        net_name: str = f"sys-hp-net-{uuid.uuid4().hex[:6]}"
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        ensure_vm_deps(runner_vm)
        subnet = _unique_subnet(net_name)

        try:
            # -- Phase 1: Create key, network, and VM with --no-pci --------
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                "--no-pci",
            )

            # -- Phase 2: Create and attach volume -------------------------
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            result = _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode != 0, (
                f"Expected attach-volume to fail on no-PCI VM, "
                f"but got returncode {result.returncode}"
            )
            combined: str = (result.stdout + result.stderr).lower()
            assert "pci is not enabled" in combined, (
                f"Expected 'pci is not enabled' in output, "
                f"got: stdout={result.stdout!r} stderr={result.stderr!r}"
            )
        finally:
            _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    @pytest.mark.xfail(
        reason="Firecracker v1.16 dev-preview hot-unplug does not fully reset PCI BARs; "
               "re-attaching the same volume to the same running VM fails virtio-blk probe."
    )
    def test_hotplug_re_attach_after_hot_unplug(
        self,
        runner_vm: str,
        hotplug_vm: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because both hotplug
        # and hot-unplug require a running guest kernel to process ACPI PCI
        # events.  The hotplug_vm fixture provides the VM with SSH key
        # already injected.  Re-attaching the *same* volume (rather than a
        # new one) validates that the Firecracker API can recycle the same
        # drive after detach without requiring a new backing file.
        """Hotplug a volume, hot-unplug it, then re-attach the **same**
        volume and verify the device reappears in the guest.
        """
        vm_name: str = hotplug_vm
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        # VM is running — vsock agent is available immediately
        wait_for_ssh(runner_vm, vm_name, "root", 30.0)

        try:
            # -- Phase 1: Create, hotplug, verify presence -----------------
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(runner_vm, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name} "
                f"after initial hotplug"
            )

            # -- Phase 2: Hot-unplug ---------------------------------------
            _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
            )
            assert _wait_for_no_vdb(runner_vm, vm_name, timeout=10.0), (
                f"Block device vdb did not disappear after hot-unplug "
                f"on VM {vm_name}"
            )

            # Verify CLI state after detach
            vols = json.loads(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist after detach"
            )
            assert vol_entry.get('status') == "available", (
                f"Expected status '''available''' after detach, "
                f"got '''{vol_entry.get('status')}'''"
            )

            # -- Phase 3: Re-attach the same volume -----------------------
            _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(runner_vm, vm_name, timeout=10.0), (
                f"Block device vdb did not re-appear after re-attach "
                f"on VM {vm_name}"
            )

            # Verify CLI state after re-attach
            vols = json.loads(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist after re-attach"
            )
            assert vol_entry.get('status') == "attached", (
                f"Expected status '''attached''' after re-attach, "
                f"got '''{vol_entry.get('status')}'''"
            )
        finally:
            _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )


# ============================================================================
# TestVolumeHotplugVersionGate — VersionGate tests for old Firecracker
# ============================================================================


class TestVolumeHotplugVersionGate:
    """VersionGate tests — hotplug blocked on Firecracker < 1.16."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_volume,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_attach_volume_to_stopped_vm_succeeds(
        self,
        runner_vm: str,
    ) -> None:
        """Cold-attach a volume to a stopped VM — must succeed."""
        vm_name: str = f"sys-vg-{uuid.uuid4().hex[:8]}"
        key_name: str = f"sys-vg-key-{uuid.uuid4().hex[:6]}"
        net_name: str = f"sys-vg-net-{uuid.uuid4().hex[:6]}"
        vol_name: str = f"sys-vg-vol-{uuid.uuid4().hex[:8]}"

        ensure_vm_deps(runner_vm)
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            # Cold-attach to stopped VM must succeed
            result = _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode == 0, (
                f"Cold attach-volume to stopped VM should succeed, "
                f"got rc={result.returncode}: {result.stderr}"
            )

            # Volume must be "attached" in listing
            vols = json.loads(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None
            assert vol_entry.get('status') == "attached"
        finally:
            _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, "--force", check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_detach_volume_from_stopped_vm_succeeds(
        self,
        runner_vm: str,
    ) -> None:
        """Cold-attach a volume, then cold-detach from a stopped VM — must succeed."""
        vm_name: str = f"sys-vg-{uuid.uuid4().hex[:8]}"
        key_name: str = f"sys-vg-key-{uuid.uuid4().hex[:6]}"
        net_name: str = f"sys-vg-net-{uuid.uuid4().hex[:6]}"
        vol_name: str = f"sys-vg-vol-{uuid.uuid4().hex[:8]}"

        ensure_vm_deps(runner_vm)
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            # Cold-attach
            _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )

            # Cold-detach
            result = _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode == 0, (
                f"Cold detach-volume from stopped VM should succeed, "
                f"got rc={result.returncode}: {result.stderr}"
            )

            # Volume must be "available" in listing
            vols = json.loads(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None
            assert vol_entry.get('status') == "available"
        finally:
            _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, "--force", check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)


# ============================================================================
# TestVolumeHotplugDestructive — destructive tests at the END of the file
# ============================================================================


class TestVolumeHotplugDestructive:
    """Destructive hotplug tests — remove volumes while attached, double attach.

    These tests modify global state (force-removing attached volumes, testing
    duplicate attach rejection) and must run serially.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_volume,
        pytest.mark.slow,
        pytest.mark.requires_firecracker_116,
    ]

    @pytest.fixture(autouse=True)
    def _ensure_firecracker_ge_1_16(self, runner_vm: str) -> None:
        """Fail all hotplug destructive tests if Firecracker is too old."""
        _require_firecracker_hotplug(runner_vm)

    def test_hotplug_force_remove_attached_volume(
        self,
        runner_vm: str,
        hotplug_vm: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) and an attached volume
        # (1-3s) to test the force-remove path. The volume must be attached
        # to a running VM so we can verify the guest still sees the device
        # after CLI-level removal (force-remove does NOT hot-unplug).
        """Force-remove a volume that is hotplugged to a running VM."""
        vm_name: str = hotplug_vm
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        wait_for_ssh(runner_vm, vm_name, "root", 30.0)

        try:
            # -- Phase 1: Create, hotplug, verify presence -----------------
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(runner_vm, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name}"
            )

            # -- Phase 2: Force-remove while attached ----------------------
            _run_mvm(runner_vm, "volume", "rm", vol_name, "--force")

            # Volume must be gone from the CLI listing
            vols = json.loads(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout
            )
            assert not any(v["name"] == vol_name for v in vols), (
                f"Volume {vol_name} should be removed from CLI listing "
                f"after force-remove"
            )

            # -- Phase 3: Guest should still see the device ----------------
            device_still_present = _wait_for_vdb(
                runner_vm, vm_name, timeout=10.0
            )
            assert device_still_present, (
                "Device vdb should still be visible in the guest "
                "after CLI-level force-remove (force-remove does not "
                "hot-unplug)"
            )
        finally:
            _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )

    def test_hotplug_double_attach_fails(
        self,
        runner_vm: str,
        hotplug_vm: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) and a single volume
        # (1-3s). The minimum viable cost is one volume and one VM — testing
        # duplicate attach requires only one VM since the constraint is
        # volume-level exclusivity (one volume cannot be attached twice
        # to the same VM either).
        """Attaching the same volume twice to a running VM must fail."""
        vm_name: str = hotplug_vm
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        try:
            # -- Phase 1: Create and attach once ---------------------------
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )

            time.sleep(1.0)

            # -- Phase 2: Second attach must fail --------------------------
            result = _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode != 0, (
                "Expected attaching the same volume twice to fail, "
                f"but got returncode {result.returncode}"
            )
            combined: str = (result.stdout + result.stderr).lower()
            assert "already attached" in combined, (
                f"Expected error message containing '''already attached''', "
                f"got: {combined}"
            )

            # -- Phase 3: Volume must still be in "attached" state ---------
            vols = json.loads(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist"
            )
            assert vol_entry.get('status') == "attached", (
                f"Expected status '''attached''' after failed double attach, "
                f"got '''{vol_entry.get('status')}'''"
            )
        finally:
            _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
