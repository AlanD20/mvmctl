"""Volume hotplug/hot-unplug system tests — attach/detach to/from a running VM.

All tests in this file require a real running VM because they exercise
the PCI hotplug notification path inside the guest OS. A stopped VM cannot
process ACPI PCI hotplug events (Firecracker issues an ACPI notification
that the guest kernel must handle). Volume-only tests are covered in
``test_volume.py``.
"""

from __future__ import annotations

import json
import time
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import (
    _run_mvm,
    _unique_subnet,
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


def _wait_for_vdb(mvm_binary: str, vm_name: str, timeout: float = 10.0) -> bool:
    """Poll guest ``lsblk -J`` until ``vdb`` appears or *timeout* expires.

    Returns ``True`` if ``vdb`` is detected before the deadline.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = _run_mvm(
            mvm_binary,
            "ssh",
            vm_name,
            "-u",
            "root",
            "--cmd",
            "lsblk -J",
            check=False,
            timeout=10,
        )
        if result.returncode == 0:
            try:
                data = json.loads(result.stdout)
                for dev in data.get("blockdevices", []):
                    if dev.get("name") == "vdb":
                        return True
            except (json.JSONDecodeError, AttributeError):
                pass
        time.sleep(0.5)
    return False


def _wait_for_no_vdb(
    mvm_binary: str, vm_name: str, timeout: float = 10.0
) -> bool:
    """Poll guest ``lsblk -J`` until ``vdb`` disappears or *timeout* expires.

    Returns ``True`` if ``vdb`` is confirmed gone before the deadline.

    **Important:** Only returns ``True`` when SSH succeeds (returncode == 0)
    AND ``vdb`` is confirmed absent from the parsed output.  When SSH fails
    (e.g. transient connectivity issue) the function keeps polling so it
    does not incorrectly claim the device is gone.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = _run_mvm(
            mvm_binary,
            "ssh",
            vm_name,
            "-u",
            "root",
            "--cmd",
            "lsblk -J",
            check=False,
            timeout=10,
        )
        if result.returncode == 0:
            # SSH succeeded — we can trust the output
            try:
                data = json.loads(result.stdout)
                vdb_found = any(
                    dev.get("name") == "vdb"
                    for dev in data.get("blockdevices", [])
                )
                if not vdb_found:
                    return True  # Confirmed gone
            except (json.JSONDecodeError, AttributeError):
                # Malformed output — keep polling
                pass
        # SSH failed (transient issue) or output unparseable — keep polling
        time.sleep(0.5)
    return False


def _require_firecracker_ge_1_16(mvm_binary: str) -> None:
    """Skip test if the default Firecracker binary is older than v1.16.

    Hotplug requires Firecracker v1.16+ (version gate in the API layer).
    This helper is called at the start of every hotplug test to ensure
    the environment is capable.
    """
    result = _run_mvm(
        mvm_binary, "bin", "ls", "--json", timeout=30
    )
    if result.returncode != 0:
        pytest.skip("Cannot check Firecracker version (bin ls failed)")

    bins = json.loads(result.stdout)
    fc_default = next(
        (b for b in bins
         if b.get("name") == "firecracker" and b.get("is_default")),
        None,
    )
    if fc_default is None:
        pytest.skip("No default Firecracker binary set")
    version = fc_default.get("version", "")
    # Strip leading 'v' and non-numeric suffixes, then parse.
    clean = version.lstrip("v").split("-")[0].split(".")[:2]
    try:
        major, minor = int(clean[0]), int(clean[1])
    except (ValueError, IndexError):
        pytest.skip(f"Firecracker version '{version}' is not parseable")
    if major < 1 or (major == 1 and minor < 16):
        pytest.skip(
            f"Hotplug tests require Firecracker v1.16+ "
            f"(current: v{version}). "
            f"Install a newer binary with: mvm bin pull firecracker <version>"
        )


# ============================================================================
# TestVolumeHotplug — hotplug a volume to a running VM
# ============================================================================


class TestVolumeHotplug:
    """Tests for hotplugging a volume to a running VM.

    All tests rely on the ``created_vm`` fixture which provides a running VM
    with SSH key injected.  The fixture is function-scoped so each test gets
    an isolated VM (expensive, ~60-120s, but required for hotplug tests).
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_volume,
        pytest.mark.serial,
        pytest.mark.slow,
        pytest.mark.requires_firecracker_116,
    ]

    @pytest.fixture(autouse=True)
    def _ensure_firecracker_ge_1_16(self, mvm_binary: str) -> None:
        """Skip all hotplug tests if Firecracker is too old."""
        _require_firecracker_ge_1_16(mvm_binary)

    def test_hotplug_volume_appears_in_guest(
        self,
        mvm_binary: str,
        created_vm: dict,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because we are testing
        # PCI hotplug notification. The ``created_vm`` fixture provides the VM
        # with SSH access. A volume (1-3s) is the cheapest resource that can
        # be hotplugged and observed inside the guest.
        """Hotplug a volume and verify the block device appears inside the guest."""
        vm_name: str = created_vm["name"]
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        assert wait_for_ssh(mvm_binary, vm_name, "root", 30.0), (
            f"SSH not available for VM {vm_name} — cannot run hotplug test"
        )

        try:
            # -- Phase 1: Create and hotplug the volume --------------------
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )

            # Wait for Firecracker ACPI hotplug notification + guest processing
            time.sleep(3.0)
            assert _wait_for_vdb(mvm_binary, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name} "
                f"after hotplug"
            )

            # -- Phase 2: Verify device in guest ---------------------------
            # lspci should show a Virtio block device for the hotplugged volume
            lspci_result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "--cmd",
                "lspci -D | grep 'Virtio.*block'",
                check=False,
                timeout=10,
            )
            assert lspci_result.returncode == 0, (
                f"No Virtio block device found in lspci: "
                f"stdout={lspci_result.stdout!r} stderr={lspci_result.stderr!r}"
            )
            assert "Virtio" in lspci_result.stdout, (
                "lspci output should contain 'Virtio'"
            )

            # -- Phase 3: Verify CLI state ---------------------------------
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} not found in listing"
            )
            assert vol_entry.get("status") == "attached", (
                f"Expected status 'attached', got '{vol_entry.get('status')}'"
            )
        finally:
            # Detach first (best-effort), then force-remove the volume
            _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )

    def test_hotplug_volume_format_and_mount(
        self,
        mvm_binary: str,
        created_vm: dict,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because we need the
        # hotplugged block device to appear in the guest for filesystem
        # operations. A stopped VM has no kernel processing hotplug events.
        """Hotplug a volume, format it, mount it, write+read, then unmount."""
        vm_name: str = created_vm["name"]
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        assert wait_for_ssh(mvm_binary, vm_name, "root", 30.0), (
            f"SSH not available for VM {vm_name}"
        )

        try:
            # -- Phase 1: Create and hotplug -------------------------------
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(mvm_binary, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name}"
            )

            # -- Phase 2: Format the block device --------------------------
            fmt_result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "--cmd",
                "mkfs.ext4 -F /dev/vdb",
                check=False,
                timeout=30,
            )
            assert fmt_result.returncode == 0, (
                f"mkfs.ext4 failed: {fmt_result.stderr}"
            )

            # -- Phase 3: Mount and write a file ---------------------------
            mount_result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "--cmd",
                "mount /dev/vdb /mnt",
                check=False,
                timeout=10,
            )
            assert mount_result.returncode == 0, (
                f"mount failed: {mount_result.stderr}"
            )

            write_result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "--cmd",
                "echo 'hotplug test data' > /mnt/test-hotplug.txt",
                check=False,
                timeout=10,
            )
            assert write_result.returncode == 0, (
                f"write to volume failed: {write_result.stderr}"
            )

            # -- Phase 4: Read back and verify content ---------------------
            read_result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "--cmd",
                "cat /mnt/test-hotplug.txt",
                check=False,
                timeout=10,
            )
            assert read_result.returncode == 0, (
                f"read from volume failed: {read_result.stderr}"
            )
            assert "hotplug test data" in read_result.stdout, (
                f"Expected 'hotplug test data' in output, "
                f"got: {read_result.stdout!r}"
            )

            # -- Phase 5: Unmount ------------------------------------------
            umount_result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "--cmd",
                "umount /mnt",
                check=False,
                timeout=10,
            )
            assert umount_result.returncode == 0, (
                f"umount failed: {umount_result.stderr}"
            )
        finally:
            # Ensure unmounted, then detach and remove volume
            _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "--cmd",
                "umount /mnt",
                check=False,
                timeout=10,
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )

    def test_hotplug_volume_hot_unplug(
        self,
        mvm_binary: str,
        created_vm: dict,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because hot-unplug
        # (ACPI PCI detach notification) requires a running guest kernel.
        # A stopped VM cannot process or reflect hot-unplug events.
        """Hotplug a volume, then hot-unplug it and verify the device disappears."""
        vm_name: str = created_vm["name"]
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        assert wait_for_ssh(mvm_binary, vm_name, "root", 30.0), (
            f"SSH not available for VM {vm_name}"
        )

        try:
            # -- Phase 1: Create, hotplug, verify presence -----------------
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(mvm_binary, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name}"
            )

            # Count Virtio block devices before detach
            lspci_before = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "--cmd",
                "lspci -D | grep 'Virtio.*block'",
                check=False,
                timeout=10,
            )
            block_count_before: int = (
                len(lspci_before.stdout.strip().splitlines())
                if lspci_before.stdout.strip()
                else 0
            )
            assert block_count_before > 0, (
                "Expected at least one Virtio block device before detach"
            )

            # -- Phase 2: Hot-unplug ---------------------------------------
            _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
            )

            # Wait for hot-unplug to propagate
            assert _wait_for_no_vdb(mvm_binary, vm_name, timeout=10.0), (
                f"Block device vdb did not disappear after hot-unplug "
                f"on VM {vm_name}"
            )

            # Verify fewer Virtio block devices after detach
            lspci_after = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "--cmd",
                "lspci -D | grep 'Virtio.*block'",
                check=False,
                timeout=10,
            )
            block_count_after: int = (
                len(lspci_after.stdout.strip().splitlines())
                if lspci_after.stdout.strip()
                else 0
            )
            assert block_count_after < block_count_before, (
                f"Block device count did not decrease after hot-unplug: "
                f"before={block_count_before}, after={block_count_after}"
            )

            # -- Phase 3: Verify CLI state ---------------------------------
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist as 'available'"
            )
            assert vol_entry.get("status") == "available", (
                f"Expected status 'available' after detach, "
                f"got '{vol_entry.get('status')}'"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )

    def test_hotplug_no_pci_guest_does_not_see_device(
        self,
        mvm_binary: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because we need to
        # verify that the guest kernel does NOT see the hotplugged block
        # device when PCI is disabled.  A stopped VM cannot demonstrate the
        # absence of hotplug events.  The VM is created inline with ``--no-pci``
        # since the ``created_vm`` fixture always enables PCI.
        """Attach a volume to a VM created with ``--no-pci`` — device should
        NOT appear inside the guest because there are no ACPI PCI hotplug
        notifications.
        """
        vm_name: str = f"sys-hp-nopci-{uuid.uuid4().hex[:8]}"
        key_name: str = f"sys-hp-key-{uuid.uuid4().hex[:6]}"
        net_name: str = f"sys-hp-net-{uuid.uuid4().hex[:6]}"
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        ensure_vm_deps(mvm_binary)
        subnet = _unique_subnet(net_name)

        try:
            # -- Phase 1: Create key, network, and VM with --no-pci --------
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--no-console",
                "--ssh-key",
                key_name,
                "--no-pci",
            )

            # Wait for SSH
            assert wait_for_ssh(mvm_binary, vm_name, "root", 30.0), (
                f"SSH not available for no-PCI VM {vm_name}"
            )

            # -- Phase 2: Create and attach volume -------------------------
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)

            # -- Phase 3: Verify device does NOT appear in guest -----------
            # Without PCI, the ACPI hotplug notification is never sent so
            # the guest kernel never probes the new Virtio block device.
            # A short timeout (5s) is sufficient — if it were going to
            # appear it would do so within 1-2s of the attach.
            device_appeared: bool = _wait_for_vdb(
                mvm_binary, vm_name, timeout=5.0
            )
            assert not device_appeared, (
                f"Block device vdb appeared in no-PCI guest {vm_name} "
                f"— expected it to remain absent"
            )

            # -- Phase 4: Verify CLI state (drive IS attached at Firecracker
            # level, just not visible in the guest) ------------------------
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} not found in listing"
            )
            assert vol_entry.get("status") == "attached", (
                f"Expected status 'attached', got '{vol_entry.get('status')}'"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_hotplug_re_attach_after_hot_unplug(
        self,
        mvm_binary: str,
        created_vm: dict,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because both hotplug
        # and hot-unplug require a running guest kernel to process ACPI PCI
        # events.  The ``created_vm`` fixture provides the VM with SSH key
        # already injected.  Re-attaching the *same* volume (rather than a
        # new one) validates that the Firecracker API can recycle the same
        # drive after detach without requiring a new backing file.
        """Hotplug a volume, hot-unplug it, then re-attach the **same**
        volume and verify the device reappears in the guest.
        """
        vm_name: str = created_vm["name"]
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        assert wait_for_ssh(mvm_binary, vm_name, "root", 30.0), (
            f"SSH not available for VM {vm_name} — cannot run hotplug test"
        )

        try:
            # -- Phase 1: Create, hotplug, verify presence -----------------
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(mvm_binary, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name} "
                f"after initial hotplug"
            )

            # -- Phase 2: Hot-unplug ---------------------------------------
            _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
            )
            assert _wait_for_no_vdb(mvm_binary, vm_name, timeout=10.0), (
                f"Block device vdb did not disappear after hot-unplug "
                f"on VM {vm_name}"
            )

            # Verify CLI state after detach
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist after detach"
            )
            assert vol_entry.get("status") == "available", (
                f"Expected status 'available' after detach, "
                f"got '{vol_entry.get('status')}'"
            )

            # -- Phase 3: Re-attach the same volume -----------------------
            _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(mvm_binary, vm_name, timeout=10.0), (
                f"Block device vdb did not re-appear after re-attach "
                f"on VM {vm_name}"
            )

            # Verify CLI state after re-attach
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist after re-attach"
            )
            assert vol_entry.get("status") == "attached", (
                f"Expected status 'attached' after re-attach, "
                f"got '{vol_entry.get('status')}'"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                mvm_binary,
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
        pytest.mark.serial,
        pytest.mark.slow,
    ]

    def test_attach_volume_fails_with_old_firecracker(
        self,
        mvm_binary: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because the VersionGate
        # in attach_volume only triggers on the hotplug path (running VM).
        # The VM is created inline with ``--firecracker-bin`` pointing at the v1.15.1
        # binary so the binary_id resolves to a version that fails the gate.
        """Attach a volume to a running VM using Firecracker v1.15.1 — must fail."""
        firecracker_bin: str = (
            f"{Path.home()}/.cache/mvmctl/bin/firecracker-v1.15.1"
        )
        vm_name: str = f"sys-vg-{uuid.uuid4().hex[:8]}"
        key_name: str = f"sys-vg-key-{uuid.uuid4().hex[:6]}"
        net_name: str = f"sys-vg-net-{uuid.uuid4().hex[:6]}"
        vol_name: str = f"sys-vg-vol-{uuid.uuid4().hex[:8]}"

        ensure_vm_deps(mvm_binary)
        subnet = _unique_subnet(net_name)

        try:
            # -- Phase 1: Create key, network, volume, and VM with v1.15.1 ---
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                "--no-console",
                "--firecracker-bin",
                firecracker_bin,
            )

            # Wait for SSH so the VM is fully running
            assert wait_for_ssh(mvm_binary, vm_name, "root", 30.0), (
                f"SSH not available for VM {vm_name}"
            )

            # -- Phase 2: Attach volume while running must fail ---------------
            result = _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode != 0, (
                f"Expected attach-volume to fail with old Firecracker, "
                f"but got returncode {result.returncode}"
            )
            combined: str = (result.stdout + result.stderr).lower()
            assert "firecracker v1.16" in combined, (
                f"Expected 'firecracker v1.16' in error message, "
                f"got: {combined}"
            )

            # -- Phase 3: Option C — volume status is still "available" -------
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist after failed attach"
            )
            assert vol_entry.get("status") == "available", (
                f"Expected status 'available' after failed attach, "
                f"got '{vol_entry.get('status')}'"
            )
        finally:
            # Detach (best-effort), force-remove volume, force-remove VM,
            # then network and key
            _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_detach_volume_fails_with_old_firecracker(
        self,
        mvm_binary: str,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) because the VersionGate
        # in detach_volume only triggers on the hot-unplug path (running VM).
        # The volume is attached while stopped (cold attach, bypasses gate),
        # then the VM is started to exercise the detach gate. The VM uses
        # ``--firecracker-bin`` pointing at the v1.15.1 binary.
        """Cold-attach a volume, start VM, then hot-unplug — must fail."""
        firecracker_bin: str = (
            f"{Path.home()}/.cache/mvmctl/bin/firecracker-v1.15.1"
        )
        vm_name: str = f"sys-vg-{uuid.uuid4().hex[:8]}"
        key_name: str = f"sys-vg-key-{uuid.uuid4().hex[:6]}"
        net_name: str = f"sys-vg-net-{uuid.uuid4().hex[:6]}"
        vol_name: str = f"sys-vg-vol-{uuid.uuid4().hex[:8]}"

        ensure_vm_deps(mvm_binary)
        subnet = _unique_subnet(net_name)

        try:
            # -- Phase 1: Create key, network, VM with v1.15.1, and volume ----
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                "--no-console",
                "--firecracker-bin",
                firecracker_bin,
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            # -- Phase 2: Stop VM and cold-attach the volume ------------------
            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")
            _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )

            # Option C: Verify volume status is "attached"
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should exist after cold attach"
            )
            assert vol_entry.get("status") == "attached", (
                f"Expected status 'attached' after cold attach, "
                f"got '{vol_entry.get('status')}'"
            )

            # -- Phase 3: Start VM and wait for SSH ---------------------------
            _run_mvm(mvm_binary, "vm", "start", vm_name)
            assert wait_for_ssh(mvm_binary, vm_name, "root", 30.0), (
                f"SSH not available for VM {vm_name} after start"
            )

            # -- Phase 4: Detach while running must fail ----------------------
            result = _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode != 0, (
                f"Expected detach-volume to fail with old Firecracker, "
                f"but got returncode {result.returncode}"
            )
            combined: str = (result.stdout + result.stderr).lower()
            assert "firecracker v1.16" in combined, (
                f"Expected 'firecracker v1.16' in error message, "
                f"got: {combined}"
            )

            # -- Phase 5: Option C — volume status is still "attached" --------
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist after failed detach"
            )
            assert vol_entry.get("status") == "attached", (
                f"Expected status 'attached' after failed detach, "
                f"got '{vol_entry.get('status')}'"
            )
        finally:
            # Detach (best-effort), force-remove volume, force-remove VM,
            # then network and key
            _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


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
        pytest.mark.serial,
        pytest.mark.slow,
        pytest.mark.requires_firecracker_116,
    ]

    @pytest.fixture(autouse=True)
    def _ensure_firecracker_ge_1_16(self, mvm_binary: str) -> None:
        """Skip all hotplug destructive tests if Firecracker is too old."""
        _require_firecracker_ge_1_16(mvm_binary)

    def test_hotplug_force_remove_attached_volume(
        self,
        mvm_binary: str,
        created_vm: dict,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) and an attached volume
        # (1-3s) to test the force-remove path. The volume must be attached
        # to a running VM so we can verify the guest still sees the device
        # after CLI-level removal (force-remove does NOT hot-unplug).
        """Force-remove a volume that is hotplugged to a running VM."""
        vm_name: str = created_vm["name"]
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        assert wait_for_ssh(mvm_binary, vm_name, "root", 30.0), (
            f"SSH not available for VM {vm_name}"
        )

        try:
            # -- Phase 1: Create, hotplug, verify presence -----------------
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            time.sleep(3.0)
            assert _wait_for_vdb(mvm_binary, vm_name, timeout=10.0), (
                f"Block device vdb did not appear in guest {vm_name}"
            )

            # -- Phase 2: Force-remove while attached ----------------------
            _run_mvm(mvm_binary, "volume", "rm", vol_name, "--force")

            # Volume must be gone from the CLI listing
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            assert not any(v["name"] == vol_name for v in vols), (
                f"Volume {vol_name} should be removed from CLI listing "
                f"after force-remove"
            )

            # -- Phase 3: Guest should still see the device ----------------
            # Force-remove only removes the volume from the database and
            # filesystem; it does NOT issue a hot-unplug ACPI event, so
            # the guest should still see vdb.
            device_still_present = _wait_for_vdb(
                mvm_binary, vm_name, timeout=10.0
            )
            assert device_still_present, (
                "Device vdb should still be visible in the guest "
                "after CLI-level force-remove (force-remove does not "
                "hot-unplug)"
            )
        finally:
            # Attempt to clean up the volume (may not exist after --force)
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )

    def test_hotplug_double_attach_fails(
        self,
        mvm_binary: str,
        created_vm: dict,
    ) -> None:
        # Rationale: Needs a real running VM (30-120s) and a single volume
        # (1-3s). The minimum viable cost is one volume and one VM — testing
        # duplicate attach requires only one VM since the constraint is
        # volume-level exclusivity (one volume cannot be attached twice
        # to the same VM either).
        """Attaching the same volume twice to a running VM must fail."""
        vm_name: str = created_vm["name"]
        vol_name: str = f"sys-hp-{uuid.uuid4().hex[:8]}"

        try:
            # -- Phase 1: Create and attach once ---------------------------
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )

            # Brief wait for the first attach to settle
            time.sleep(1.0)

            # -- Phase 2: Second attach must fail --------------------------
            result = _run_mvm(
                mvm_binary,
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
                f"Expected error message containing 'already attached', "
                f"got: {combined}"
            )

            # -- Phase 3: Volume must still be in "attached" state ---------
            vols = json.loads(
                _run_mvm(mvm_binary, "volume", "ls", "--json").stdout
            )
            vol_entry = next((v for v in vols if v["name"] == vol_name), None)
            assert vol_entry is not None, (
                f"Volume {vol_name} should still exist"
            )
            assert vol_entry.get("status") == "attached", (
                f"Expected status 'attached' after failed double attach, "
                f"got '{vol_entry.get('status')}'"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
