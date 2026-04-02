"""VM lifecycle system tests — state operations with both approaches."""

import os
import pytest
import time
import subprocess
import json

pytestmark = [pytest.mark.system, pytest.mark.requires_kvm, pytest.mark.slow]


class TestVMCreatePerImage:
    """Test VM creation with each supported image."""

    @pytest.mark.parametrize(
        "image_id",
        [
            "alpine-3.21",
            "ubuntu-24.04-minimal",
            "ubuntu-24.04",
            "archlinux",
            "debian-bookworm",
        ],
    )
    def test_vm_create(self, mvm_binary, unique_vm_name, image_id, timing_targets):
        """Create VM with specific image."""
        start = time.monotonic()
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "create", "--name", unique_vm_name, "--image", image_id],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        duration = time.monotonic() - start

        assert result.returncode == 0
        # Command should return quickly (<1s target)
        assert duration < 1.0, f"Create command took {duration:.2f}s, expected <1s"

        # Cleanup
        subprocess.run(
            [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name],
            check=False,
            env={**os.environ, "NO_COLOR": "1"},
        )


class TestVMStateOperationsShared:
    """Test state operations on shared VM (approach 1)."""

    pytestmark = pytest.mark.shared_vm

    def test_vm_pause_resume_chain(self, mvm_binary, lifecycle_vm):
        """Pause then resume VM."""
        vm_name = lifecycle_vm["name"]

        # Pause
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "pause", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

        # Verify paused
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm["status"] == "PAUSED"

        # Resume
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "resume", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_vm_stop_start_chain(self, mvm_binary, lifecycle_vm):
        """Stop then restart VM."""
        vm_name = lifecycle_vm["name"]

        # Stop
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "stop", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

        # Start
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "start", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_vm_reboot_graceful(self, mvm_binary, lifecycle_vm):
        """Reboot VM (stop + start)."""
        vm_name = lifecycle_vm["name"]

        result = subprocess.run(
            [*mvm_binary.split(), "vm", "reboot", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0


class TestVMStateOperationsIndependent:
    """Test state operations with independent VMs (approach 2)."""

    pytestmark = pytest.mark.independent_vm

    def test_vm_pause_independent(self, mvm_binary, created_vm):
        """Pause independently created VM."""
        vm_name = created_vm["name"]

        result = subprocess.run(
            [*mvm_binary.split(), "vm", "pause", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_vm_resume_independent(self, mvm_binary, created_vm):
        """Resume independently created VM."""
        vm_name = created_vm["name"]

        result = subprocess.run(
            [*mvm_binary.split(), "vm", "resume", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_vm_stop_independent(self, mvm_binary, created_vm):
        """Stop independently created VM."""
        vm_name = created_vm["name"]

        result = subprocess.run(
            [*mvm_binary.split(), "vm", "stop", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_vm_start_independent(self, mvm_binary, created_vm):
        """Start independently created VM."""
        vm_name = created_vm["name"]

        result = subprocess.run(
            [*mvm_binary.split(), "vm", "start", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0


class TestVMSSH:
    """Test VM SSH operations."""

    def test_vm_ssh_available(self, mvm_binary, created_vm, timing_targets):
        """SSH is available after VM boots."""
        vm_name = created_vm["name"]
        vm_ip = created_vm.get("ipv4", "")

        if not vm_ip:
            pytest.skip("VM has no IP address")

        # Wait for SSH with timeout
        from conftest import wait_for_ssh

        available = wait_for_ssh(vm_ip, "root", timing_targets["alpine-3.21"])
        assert available, f"SSH not available after {timing_targets['alpine-3.21']}s"


class TestVMList:
    """Test VM listing operations."""

    def test_vm_list_json(self, mvm_binary, created_vm):
        """List VMs in JSON format."""
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert any(v["name"] == created_vm["name"] for v in vms)

    def test_vm_list_table(self, mvm_binary, created_vm):
        """List VMs in table format."""
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert created_vm["name"] in result.stdout


class TestVMRemove:
    """Test VM removal operations."""

    def test_vm_remove(self, mvm_binary, unique_vm_name):
        """Create and remove VM."""
        # Create
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

        # Remove
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

        # Verify gone
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        vms = json.loads(result.stdout)
        assert not any(v["name"] == unique_vm_name for v in vms)

    def test_vm_remove_force(self, mvm_binary, unique_vm_name):
        """Force remove running VM."""
        # Create
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

        # Force remove
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
