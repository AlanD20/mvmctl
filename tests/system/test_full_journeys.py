"""End-to-end journey system tests."""

from __future__ import annotations

import json
import uuid
from concurrent.futures import ThreadPoolExecutor
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet, wait_for_ssh

pytestmark = [pytest.mark.system, pytest.mark.requires_kvm, pytest.mark.slow]


class TestQuickStartJourney:
    """Test the quick start workflow from README."""

    def test_journey_create_and_ssh(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        """Full journey: create VM with SSH key and SSH into it."""
        # Create a throwaway SSH key
        key_name = f"sys-journey-key-{uuid.uuid4().hex[:6]}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )

        # Create VM with SSH key injected
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--ssh-key",
            key_name,
        )
        assert result.returncode == 0

        try:
            # Wait for SSH
            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"
        finally:
            # Guaranteed cleanup — VM first, key second
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


@pytest.mark.requires_network
class TestNetworkVMJourney:
    """Test network + VM workflow."""

    def test_journey_network_then_vm(
        self, mvm_binary, unique_network_name, unique_vm_name
    ):
        """Create network, then create VM on that network."""
        subnet = _unique_subnet(unique_network_name)
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            subnet,
            "--non-interactive",
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
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_journey_network_vm_with_explicit_ip(
        self, mvm_binary, unique_network_name, unique_vm_name
    ):
        """Create network, then create VM on that network with explicit IP."""
        subnet = _unique_subnet(unique_network_name)
        ip = subnet.replace(".0/24", ".50")

        _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )

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
                "--ip",
                ip,
            )
            assert result.returncode == 0

            # Verify IP assignment
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found"
            assert vm["ipv4"] == ip, f"Expected {ip}, got {vm['ipv4']}"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                unique_network_name,
                check=False,
            )


class TestKeyVMJourney:
    """Test key + VM workflow."""

    def test_journey_key_then_vm(
        self, mvm_binary, unique_key_name, unique_vm_name
    ):
        """Create key, then create VM with that key."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
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
                "--ssh-key",
                unique_key_name,
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
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
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "paused", (
                f"Expected paused, got {vm['status']}"
            )

            # Resume (paused → running)
            result = _run_mvm(mvm_binary, "vm", "resume", unique_vm_name)
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )

            # Stop (running → stopped)
            result = _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "stopped", (
                f"Expected stopped, got {vm['status']}"
            )

            # Start (stopped → running)
            result = _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestIPJourney:
    """Test VM IP assignment journeys."""

    def test_journey_vm_with_explicit_ip(
        self, mvm_binary, unique_vm_name, unique_network_name, timing_targets
    ):
        """Create VM with explicit IP on a dedicated network and verify assignment."""
        subnet = _unique_subnet(unique_network_name)
        ip = subnet.replace(".0/24", ".100")

        _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )

        key_name = f"sys-journey-key-{uuid.uuid4().hex[:6]}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                unique_network_name,
                "--ip",
                ip,
                "--ssh-key",
                key_name,
            )

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found in listing"
            assert vm["ipv4"] == ip, f"Expected {ip}, got {vm['ipv4']}"

            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                unique_network_name,
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_journey_multiple_vms_same_network(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        """Create two VMs on same default network and verify both are reachable."""
        name_a = f"{unique_vm_name}-a"
        name_b = f"{unique_vm_name}-b"

        key_a_name = f"sys-multi-key-a-{uuid.uuid4().hex[:6]}"
        key_b_name = f"sys-multi-key-b-{uuid.uuid4().hex[:6]}"
        _run_mvm(
            mvm_binary, "key", "create", key_a_name, "--algorithm", "ed25519"
        )
        _run_mvm(
            mvm_binary, "key", "create", key_b_name, "--algorithm", "ed25519"
        )

        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            name_a,
            "--image",
            "alpine-3.21",
            "--ssh-key",
            key_a_name,
        )
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            name_b,
            "--image",
            "alpine-3.21",
            "--ssh-key",
            key_b_name,
        )

        try:
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_a = next((v for v in vms if v["name"] == name_a), None)
            vm_b = next((v for v in vms if v["name"] == name_b), None)
            assert vm_a is not None, f"VM '{name_a}' not found in listing"
            assert vm_b is not None, f"VM '{name_b}' not found in listing"

            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_a = wait_for_ssh(mvm_binary, name_a, "root", ssh_timeout)
            assert ssh_a, (
                f"SSH not available for '{name_a}' within {ssh_timeout}s"
            )
            ssh_b = wait_for_ssh(mvm_binary, name_b, "root", ssh_timeout)
            assert ssh_b, (
                f"SSH not available for '{name_b}' within {ssh_timeout}s"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", name_a, "--force", check=False)
            _run_mvm(mvm_binary, "vm", "rm", name_b, "--force", check=False)
            _run_mvm(mvm_binary, "key", "rm", key_a_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_b_name, check=False)


class TestSSHJourney:
    """Test SSH-related VM journeys."""

    def test_journey_ssh_cli_command(
        self, mvm_binary, created_vm, timing_targets
    ):
        """Create VM and verify SSH CLI command execution."""
        vm_info = created_vm
        ssh_timeout = timing_targets["alpine-3.21"]
        ssh_available = wait_for_ssh(
            mvm_binary, vm_info["name"], "root", ssh_timeout
        )
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
        assert result.returncode == 0
        assert "Linux" in result.stdout, (
            f"Expected 'Linux' in SSH output, got: {result.stdout}"
        )

    def test_journey_reboot_chain(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        """Create VM, reboot, and verify SSH availability after reboot."""
        key_name = f"sys-reboot-key-{uuid.uuid4().hex[:6]}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--ssh-key",
            key_name,
        )

        try:
            # Get VM info
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found in listing"

            # Wait for SSH
            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            # Reboot VM
            result = _run_mvm(mvm_binary, "vm", "reboot", unique_vm_name)
            assert result.returncode == 0

            # Re-query VM info (IP may have changed)
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, (
                f"VM '{unique_vm_name}' not found after reboot"
            )

            # Wait for SSH again after reboot
            ssh_after_reboot = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_after_reboot, (
                f"SSH not available after reboot within {ssh_timeout}s"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestMultiKeyJourney:
    """Test VM creation with multiple SSH keys."""

    def test_journey_multiple_ssh_keys(
        self, mvm_binary, unique_key_name, unique_vm_name
    ):
        """Create two keys, then create VM with both keys."""
        key_a = f"{unique_key_name}-a"
        key_b = f"{unique_key_name}-b"

        # Create two keys
        _run_mvm(mvm_binary, "key", "create", key_a, "--algorithm", "ed25519")
        _run_mvm(mvm_binary, "key", "create", key_b, "--algorithm", "ed25519")

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
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_a, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_b, check=False)


class TestInterVMCommunication:
    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
    ]

    def test_journey_ping_between_vms(
        self, mvm_binary, unique_network_name, unique_vm_name, timing_targets
    ):
        subnet = _unique_subnet(unique_network_name)
        name_a = f"{unique_vm_name}-a"
        name_b = f"{unique_vm_name}-b"

        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        assert result.returncode == 0

        key_a_name = f"sys-ping-key-a-{uuid.uuid4().hex[:6]}"
        key_b_name = f"sys-ping-key-b-{uuid.uuid4().hex[:6]}"
        _run_mvm(
            mvm_binary, "key", "create", key_a_name, "--algorithm", "ed25519"
        )
        _run_mvm(
            mvm_binary, "key", "create", key_b_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                name_a,
                "--image",
                "alpine-3.21",
                "--network",
                unique_network_name,
                "--ssh-key",
                key_a_name,
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                name_b,
                "--image",
                "alpine-3.21",
                "--network",
                unique_network_name,
                "--ssh-key",
                key_b_name,
            )

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_a = next((v for v in vms if v["name"] == name_a), None)
            vm_b = next((v for v in vms if v["name"] == name_b), None)
            assert vm_a is not None, f"VM '{name_a}' not found in listing"
            assert vm_b is not None, f"VM '{name_b}' not found in listing"

            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_available = wait_for_ssh(
                mvm_binary, name_a, "root", ssh_timeout
            )
            assert ssh_available, (
                f"SSH not available for '{name_a}' within {ssh_timeout}s"
            )

            ping_result = _run_mvm(
                mvm_binary,
                "ssh",
                name_a,
                "-u",
                "root",
                "-c",
                f"ping -c 3 {vm_b['ipv4']}",
                check=False,
                timeout=30,
            )
            assert ping_result.returncode == 0, (
                f"Ping failed: {ping_result.stdout}\n{ping_result.stderr}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", name_a, "--force", check=False)
            _run_mvm(mvm_binary, "vm", "rm", name_b, "--force", check=False)
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_a_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_b_name, check=False)


class TestVMExportImportJourney:
    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_journey_export_then_import(
        self, mvm_binary, unique_vm_name, tmp_path
    ):
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

        new_name = f"{unique_vm_name}-imported"
        try:
            result = _run_mvm(mvm_binary, "vm", "export", unique_vm_name)
            assert result.returncode == 0
            export_data = json.loads(result.stdout)

            # Remove original VM to release IP lease before import
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name)

            export_path = tmp_path / "vm_export.json"
            export_path.write_text(json.dumps(export_data))

            result = _run_mvm(
                mvm_binary,
                "vm",
                "import",
                str(export_path),
                "--name",
                new_name,
            )
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            imported_vm = next((v for v in vms if v["name"] == new_name), None)
            assert imported_vm is not None, (
                f"Imported VM '{new_name}' not found in listing"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                new_name,
                "--force",
                check=False,
            )


class TestConcurrentVMCreation:
    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_concurrent_vm_creation_and_ssh(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        vm_count = 10
        vm_names = [f"{unique_vm_name}-{i}" for i in range(vm_count)]
        key_names = [f"{unique_vm_name}-key-{i}" for i in range(vm_count)]

        for key_name in key_names:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )

        def create_vm(name: str, key_name: str) -> Any:
            return _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
            )

        try:
            with ThreadPoolExecutor(max_workers=vm_count) as executor:
                futures = [
                    executor.submit(create_vm, n, k)
                    for n, k in zip(vm_names, key_names)
                ]
                results = [f.result() for f in futures]

            assert all(r.returncode == 0 for r in results), (
                f"One or more VM creations failed: "
                f"{[r.stderr for r in results if r.returncode != 0]}"
            )

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_name_set = set(vm_names)
            created_vms = [v for v in vms if v["name"] in vm_name_set]
            assert len(created_vms) == vm_count, (
                f"Expected {vm_count} VMs, found {len(created_vms)}"
            )
            for vm in created_vms:
                assert vm["status"] == "running", (
                    f"VM '{vm['name']}' not RUNNING: {vm['status']}"
                )

            ssh_timeout = timing_targets["alpine-3.21"]
            for vm in created_vms:
                ssh_available = wait_for_ssh(
                    mvm_binary, vm["name"], "root", ssh_timeout
                )
                assert ssh_available, (
                    f"SSH not available for '{vm['name']}' "
                    f"within {ssh_timeout}s"
                )
        finally:
            for name in vm_names:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "rm",
                    name,
                    "--force",
                    check=False,
                )
            for key_name in key_names:
                _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
