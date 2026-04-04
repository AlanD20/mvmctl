"""End-to-end journey system tests."""

import os
import subprocess
import time

import pytest

pytestmark = [pytest.mark.system, pytest.mark.requires_kvm, pytest.mark.slow]


class TestQuickStartJourney:
    """Test the quick start workflow from README."""

    def test_journey_create_and_ssh(self, mvm_binary, unique_vm_name, timing_targets):
        """Full journey: create VM and SSH into it."""
        # Create VM
        start = time.monotonic()
        result = subprocess.run(
            [
                *mvm_binary.split(),
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
            ],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        time.monotonic() - start

        # Get VM info
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        import json

        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == unique_vm_name), None)
        assert vm is not None

        # Wait for SSH
        from conftest import wait_for_ssh

        ssh_available = wait_for_ssh(vm["ipv4"], "root", timing_targets["alpine-3.21"])
        assert ssh_available, "SSH not available within timeout"

        # Cleanup
        subprocess.run(
            [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
            check=False,
            env={**os.environ, "NO_COLOR": "1"},
        )

        # Timing assertion
        total_time = time.monotonic() - start
        assert total_time < timing_targets["alpine-3.21"], f"Journey took {total_time:.2f}s"


class TestNetworkVMJourney:
    """Test network + VM workflow."""

    def test_journey_network_then_vm(self, mvm_binary, unique_network_name, unique_vm_name):
        """Create network, then create VM on that network."""
        # Create network
        result = subprocess.run(
            [
                *mvm_binary.split(),
                "network",
                "create",
                unique_network_name,
                "--subnet",
                "10.99.0.0/24",
            ],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

        try:
            # Create VM on network
            result = subprocess.run(
                [
                    *mvm_binary.split(),
                    "vm",
                    "create",
                    "--name",
                    unique_vm_name,
                    "--image",
                    "alpine-3.21",
                    "--network",
                    unique_network_name,
                ],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0

            # Verify VM is on correct network
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "ls", "--json"],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            import json

            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["network"] == unique_network_name or "10.99.0" in vm.get("ipv4", "")

        finally:
            # Cleanup
            subprocess.run(
                [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )
            subprocess.run(
                [*mvm_binary.split(), "network", "rm", unique_network_name],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )


class TestKeyVMJourney:
    """Test key + VM workflow."""

    def test_journey_key_then_vm(self, mvm_binary, unique_key_name, unique_vm_name):
        """Create key, then create VM with that key."""
        # Create key
        result = subprocess.run(
            [*mvm_binary.split(), "key", "create", unique_key_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

        try:
            # Create VM with key
            result = subprocess.run(
                [
                    *mvm_binary.split(),
                    "vm",
                    "create",
                    "--name",
                    unique_vm_name,
                    "--image",
                    "alpine-3.21",
                    "--key",
                    unique_key_name,
                ],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0

        finally:
            # Cleanup
            subprocess.run(
                [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )
            subprocess.run(
                [*mvm_binary.split(), "key", "rm", unique_key_name],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )


class TestVMStateJourney:
    """Test VM state transition journey."""

    def test_journey_pause_resume_stop_start(self, mvm_binary, unique_vm_name):
        """Full state transition journey."""
        # Create VM
        subprocess.run(
            [
                *mvm_binary.split(),
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
            ],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )

        try:
            # Pause
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "pause", unique_vm_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0

            # Resume
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "resume", unique_vm_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0

            # Stop
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "stop", unique_vm_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0

            # Start
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "start", unique_vm_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0

        finally:
            # Cleanup
            subprocess.run(
                [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )
