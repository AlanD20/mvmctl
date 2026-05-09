"""Business outcome system tests — verify real-world VM behavior.

These tests validate that core VM features work correctly inside the guest:
volume attachment, user-data execution, DNS resolution, and volume
persistence across stop/start cycles.

All tests are black-box CLI invocations only — no mvmctl.* imports.
"""

from __future__ import annotations

import uuid
from pathlib import Path

import pytest

from tests.system.conftest import _run_mvm, wait_for_ssh

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_outcome,
]

# Timeout for SSH to become available after VM creation
_SSH_WAIT_TIMEOUT = 120
# Timeout for SSH after VM stop/start (VM reboots from scratch)
_REBOOT_SSH_WAIT_TIMEOUT = 180


class TestVolumeInGuest:
    """Volume behavior inside guest VMs."""

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_volume_device_visible_in_guest(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        timing_targets: dict[str, float],
    ) -> None:
        """Verify an attached volume appears as a block device inside the guest.

        Creates an SSH key, a 1G volume, and a VM with that volume attached.
        SSHs into the VM and checks that the additional block device (vdb)
        is visible via lsblk.
        """
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:6]}"
        vol_name = f"sys-outcome-vol-{uuid.uuid4().hex[:6]}"
        vm_name = unique_vm_name

        try:
            # Create SSH key
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )

            # Create volume
            _run_mvm(mvm_binary, "volume", "create", vol_name, "1G")

            # Create VM with volume attached
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
                "--volume",
                vol_name,
            )

            # Wait for SSH
            ssh_timeout = max(
                timing_targets.get("alpine-3.21", 15), _SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            # Check block devices
            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "-c",
                "lsblk -o NAME",
            )
            assert "vdb" in result.stdout, (
                f"Volume not visible as block device. lsblk output:\n{result.stdout}"
            )

        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_volume_mountable_in_guest(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        timing_targets: dict[str, float],
    ) -> None:
        """Verify an attached volume can be formatted and mounted inside the guest.

        Creates a key, volume, and VM. SSHs in and:
        1. Formats /dev/vdb as ext4
        2. Mounts it to /mnt/test
        3. Creates a file on the mounted volume
        """
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:6]}"
        vol_name = f"sys-outcome-vol-{uuid.uuid4().hex[:6]}"
        vm_name = unique_vm_name

        try:
            # Create SSH key
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )

            # Create volume
            _run_mvm(mvm_binary, "volume", "create", vol_name, "1G")

            # Create VM with volume attached
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
                "--volume",
                vol_name,
            )

            # Wait for SSH
            ssh_timeout = max(
                timing_targets.get("alpine-3.21", 15), _SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            # Format as ext4
            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "-c",
                "mkfs.ext4 /dev/vdb",
                check=False,
                timeout=60,
            )
            assert result.returncode == 0, (
                f"mkfs.ext4 failed: {result.stdout}\n{result.stderr}"
            )

            # Mount and create a file
            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "-c",
                "mkdir -p /mnt/test && mount /dev/vdb /mnt/test && touch /mnt/test/hello.txt",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"Mount or file creation failed: {result.stdout}\n{result.stderr}"
            )

            # Verify the file exists
            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "-c",
                "test -f /mnt/test/hello.txt && echo 'EXISTS'",
            )
            assert "EXISTS" in result.stdout, (
                f"hello.txt not found on mounted volume:\n{result.stdout}"
            )

        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestUserData:
    """Cloud-init user-data execution inside VMs."""

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_user_data_script_executes(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        timing_targets: dict[str, float],
        tmp_path: Path,
    ) -> None:
        """Verify cloud-init user-data runs inside the VM.

        Creates a user-data script that creates a sentinel file, attaches
        it via --user-data and --cloud-init-mode inject, boots the VM,
        and checks for the sentinel file via SSH.
        """
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:6]}"
        vm_name = unique_vm_name

        # Create a user-data script that writes a sentinel file
        user_data_path = tmp_path / "user-data"
        user_data_path.write_text("#!/bin/sh\ntouch /tmp/user-data-sentinel\n")
        user_data_path.chmod(0o755)

        try:
            # Create SSH key
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )

            # Create VM with user-data (use inject mode so it actually gets processed)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
                "--user-data",
                str(user_data_path),
                "--cloud-init-mode",
                "inject",
            )

            # Wait for SSH (allow extra time for cloud-init to run)
            ssh_timeout = max(
                timing_targets.get("alpine-3.21", 15), _SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            # Check for the sentinel file created by user-data
            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "-c",
                "test -f /tmp/user-data-sentinel && echo 'EXISTS'",
                check=False,
            )
            assert result.returncode == 0, (
                f"user-data sentinel file not found: {result.stdout}\n{result.stderr}"
            )
            assert "EXISTS" in result.stdout, (
                f"Expected 'EXISTS' in output, got: {result.stdout}"
            )

        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestDNSResolutionInsideVM:
    """DNS resolution inside guest VMs."""

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_dns_resolution_inside_vm(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        timing_targets: dict[str, float],
    ) -> None:
        """Verify DNS resolution works inside the VM.

        Creates a VM, SSHs in, and resolves google.com via getent.
        """
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:6]}"
        vm_name = unique_vm_name

        try:
            # Create SSH key
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )

            # Create VM
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
            )

            # Wait for SSH
            ssh_timeout = max(
                timing_targets.get("alpine-3.21", 15), _SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            # Resolve google.com
            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "-c",
                "getent hosts google.com",
                check=False,
                timeout=30,
            )
            assert result.returncode == 0, (
                f"DNS resolution failed: {result.stdout}\n{result.stderr}"
            )
            assert "google.com" in result.stdout, (
                f"Expected google.com in output, got: {result.stdout}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestVMStopStartWithVolumes:
    """Volume persistence across VM stop/start cycles."""

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_survives_stop_start_with_volumes(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        timing_targets: dict[str, float],
    ) -> None:
        """Verify volumes persist across VM stop/start cycles.

        Creates a VM with a volume, waits for SSH, verifies the block
        device is visible, stops the VM, starts it again, waits for
        SSH, and verifies the volume is still visible.
        """
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:6]}"
        vol_name = f"sys-outcome-vol-{uuid.uuid4().hex[:6]}"
        vm_name = unique_vm_name

        try:
            # Create SSH key
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )

            # Create volume
            _run_mvm(mvm_binary, "volume", "create", vol_name, "1G")

            # Create VM with volume attached
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
                "--volume",
                vol_name,
            )

            # Phase 1: Initial boot — verify volume is visible
            ssh_timeout = max(
                timing_targets.get("alpine-3.21", 15), _SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "-c",
                "lsblk -o NAME",
            )
            assert "vdb" in result.stdout, (
                f"Volume not visible at first boot. lsblk:\n{result.stdout}"
            )

            # Phase 2: Stop the VM
            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            # Phase 3: Start the VM
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            # Phase 4: Wait for SSH again (VM boots fresh)
            ssh_after_start = wait_for_ssh(
                mvm_binary, vm_name, "root", _REBOOT_SSH_WAIT_TIMEOUT
            )
            assert ssh_after_start, (
                f"SSH not available after stop/start "
                f"within {_REBOOT_SSH_WAIT_TIMEOUT}s"
            )

            # Phase 5: Verify volume is still visible
            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                "root",
                "-c",
                "lsblk -o NAME",
            )
            assert "vdb" in result.stdout, (
                f"Volume not visible after stop/start. lsblk:\n{result.stdout}"
            )

        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
