"""End-to-end journey system tests."""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm, wait_for_ssh

pytestmark = [pytest.mark.system, pytest.mark.requires_kvm, pytest.mark.slow]


class TestQuickStartJourney:
    """Test the quick start workflow from README."""

    def test_journey_create_and_ssh(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        """Full journey: create VM and SSH into it."""
        # Create VM
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
        )
        assert result.returncode == 0

        try:
            # Get VM info
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found in listing"

            # Wait for SSH
            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_available = wait_for_ssh(vm["ipv4"], "root", ssh_timeout)
            assert ssh_available, f"SSH not available within {ssh_timeout}s"
        finally:
            # Guaranteed cleanup
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestNetworkVMJourney:
    """Test network + VM workflow."""

    def test_journey_network_then_vm(
        self, mvm_binary, unique_network_name, unique_vm_name
    ):
        """Create network, then create VM on that network."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            "10.99.0.0/24",
        )
        assert result.returncode == 0

        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                unique_network_name,
            )
            assert result.returncode == 0

            # Verify VM is on correct network
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found"
            assert unique_network_name in str(vm.get("network_name", "")), (
                f"VM not on network '{unique_network_name}'"
            )

        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )


class TestKeyVMJourney:
    """Test key + VM workflow."""

    def test_journey_key_then_vm(
        self, mvm_binary, unique_key_name, unique_vm_name
    ):
        """Create key, then create VM with that key."""
        result = _run_mvm(mvm_binary, "key", "create", unique_key_name)
        assert result.returncode == 0

        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--key",
                unique_key_name,
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)


class TestVMStateJourney:
    """Test VM state transition journey."""

    def test_journey_pause_resume_stop_start(self, mvm_binary, unique_vm_name):
        """Full state transition journey: create → pause → resume → stop → start."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
        )

        try:
            # Pause (running → paused)
            result = _run_mvm(mvm_binary, "vm", "pause", unique_vm_name)
            assert result.returncode == 0

            # Resume (paused → running)
            result = _run_mvm(mvm_binary, "vm", "resume", unique_vm_name)
            assert result.returncode == 0

            # Stop (running → stopped)
            result = _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            assert result.returncode == 0

            # Start (stopped → running)
            result = _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestIPJourney:
    """Test VM IP assignment journeys."""

    def test_journey_vm_with_explicit_ip(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        """Create VM with explicit IP and verify assignment."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--ip",
            "172.27.0.100",
        )

        try:
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found in listing"
            assert vm["ipv4"] == "172.27.0.100", (
                f"Expected ipv4 172.27.0.100, got {vm['ipv4']}"
            )

            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_available = wait_for_ssh(vm["ipv4"], "root", ssh_timeout)
            assert ssh_available, f"SSH not available within {ssh_timeout}s"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
                unique_vm_name,
                "--force",
                check=False,
            )

    def test_journey_multiple_vms_same_network(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        """Create two VMs on same default network and verify both are reachable."""
        name_a = f"{unique_vm_name}-a"
        name_b = f"{unique_vm_name}-b"

        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            name_a,
            "--image",
            "alpine-3.21",
        )
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            name_b,
            "--image",
            "alpine-3.21",
        )

        try:
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_a = next((v for v in vms if v["name"] == name_a), None)
            vm_b = next((v for v in vms if v["name"] == name_b), None)
            assert vm_a is not None, f"VM '{name_a}' not found in listing"
            assert vm_b is not None, f"VM '{name_b}' not found in listing"

            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_a = wait_for_ssh(vm_a["ipv4"], "root", ssh_timeout)
            assert ssh_a, (
                f"SSH not available for '{name_a}' within {ssh_timeout}s"
            )
            ssh_b = wait_for_ssh(vm_b["ipv4"], "root", ssh_timeout)
            assert ssh_b, (
                f"SSH not available for '{name_b}' within {ssh_timeout}s"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestSSHJourney:
    """Test SSH-related VM journeys."""

    def test_journey_ssh_cli_command(
        self, mvm_binary, created_vm, timing_targets
    ):
        """Create VM and verify SSH CLI command execution."""
        vm_info = created_vm
        ssh_timeout = timing_targets["alpine-3.21"]
        ssh_available = wait_for_ssh(vm_info["ipv4"], "root", ssh_timeout)
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            mvm_binary,
            "ssh",
            "--name",
            vm_info["name"],
            "-c",
            "uname -r",
            check=False,
        )
        assert "Linux" in result.stdout, (
            f"Expected 'Linux' in SSH output, got: {result.stdout}"
        )

    def test_journey_reboot_chain(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        """Create VM, reboot, and verify SSH availability after reboot."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
        )

        try:
            # Get VM info
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found in listing"

            # Wait for SSH
            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_available = wait_for_ssh(vm["ipv4"], "root", ssh_timeout)
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            # Reboot VM
            result = _run_mvm(mvm_binary, "vm", "reboot", unique_vm_name)
            assert result.returncode == 0

            # Wait for SSH again after reboot
            ssh_after_reboot = wait_for_ssh(vm["ipv4"], "root", ssh_timeout)
            assert ssh_after_reboot, (
                f"SSH not available after reboot within {ssh_timeout}s"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestMultiKeyJourney:
    """Test VM creation with multiple SSH keys."""

    def test_journey_multiple_ssh_keys(
        self, mvm_binary, unique_key_name, unique_vm_name
    ):
        """Create two keys, then create VM with both keys."""
        key_a = f"{unique_key_name}-a"
        key_b = f"{unique_key_name}-b"

        # Create two keys
        _run_mvm(mvm_binary, "key", "create", key_a)
        _run_mvm(mvm_binary, "key", "create", key_b)

        try:
            # Create VM with both keys
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                f"{key_a},{key_b}",
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_a, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_b, check=False)
