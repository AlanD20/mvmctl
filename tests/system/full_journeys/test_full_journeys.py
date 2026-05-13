"""End-to-end journey system tests.

Merged from: test_full_journeys.py (existing), test_integration_workflows.py (coverage)
"""

from __future__ import annotations

import json
import uuid
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet, wait_for_ssh

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
]


@pytest.fixture(scope="module", autouse=True)
def _cleanup_stale_networks(mvm_binary):
    """Remove stale sys-* networks from prior runs to avoid subnet collisions."""
    result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
    if result.returncode != 0:
        return
    try:
        nets = json.loads(result.stdout)
    except json.JSONDecodeError:
        return
    for net in nets:
        name = net.get("name", "")
        if name.startswith("sys-"):
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                name,
                "--non-interactive",
                check=False,
            )


class TestQuickStartJourney:
    """Test the quick start workflow from README."""

    pytestmark = [pytest.mark.domain_vm]

    def test_journey_create_and_ssh(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        """Full journey: create VM with SSH key and SSH into it."""
        key_name = f"sys-journey-key-{uuid.uuid4().hex[:6]}"
        net_name = f"sys-journey-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
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

        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--network",
            net_name,
            "--ssh-key",
            key_name,
        )
        assert result.returncode == 0

        try:
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
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


@pytest.mark.requires_network
class TestNetworkVMJourney:
    """Test network + VM workflow."""

    pytestmark = [pytest.mark.domain_vm]

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


class TestKeyVMJourney:
    """Test key + VM workflow."""

    pytestmark = [pytest.mark.domain_vm]

    def test_journey_key_then_vm(
        self, mvm_binary, unique_key_name, unique_vm_name
    ):
        """Create key, then create VM with that key."""
        net_name = f"sys-journey-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)

        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
        )
        assert result.returncode == 0

        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
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
                net_name,
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
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)


class TestVMStateJourney:
    """Test VM state transition journey."""

    pytestmark = [pytest.mark.domain_vm]

    def test_journey_pause_resume_stop_start(self, mvm_binary, unique_vm_name):
        """Full state transition journey: create → pause → resume → stop → start."""
        net_name = f"sys-journey-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--network",
            net_name,
        )

        try:
            result = _run_mvm(mvm_binary, "vm", "pause", unique_vm_name)
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "paused", (
                f"Expected paused, got {vm['status']}"
            )

            result = _run_mvm(mvm_binary, "vm", "resume", unique_vm_name)
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )

            result = _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "stopped", (
                f"Expected stopped, got {vm['status']}"
            )

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
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestIPJourney:
    """Test VM IP assignment journeys."""

    pytestmark = [pytest.mark.domain_vm]

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
        """Create two VMs on same network and verify both are reachable."""
        name_a = f"{unique_vm_name}-a"
        name_b = f"{unique_vm_name}-b"
        net_name = f"sys-multi-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)

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
            "--name",
            name_a,
            "--image",
            "alpine-3.21",
            "--network",
            net_name,
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
            net_name,
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
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_a_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_b_name, check=False)


class TestSSHJourney:
    """Test SSH-related VM journeys."""

    pytestmark = [pytest.mark.domain_vm]

    def test_journey_ssh_cli_command(
        self, mvm_binary, created_vm, timing_targets
    ):
        """Create VM and verify SSH CLI command execution."""
        vm_info = created_vm
        ssh_timeout = max(timing_targets.get("alpine-3.21", 15), 30)
        ssh_available = wait_for_ssh(
            mvm_binary, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            mvm_binary,
            "ssh",
            vm_info["name"],
            "--cmd",
            "uname -a",
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
        net_name = f"sys-reboot-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--network",
            net_name,
            "--ssh-key",
            key_name,
        )

        try:
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found in listing"

            ssh_timeout = timing_targets["alpine-3.21"]
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            result = _run_mvm(mvm_binary, "vm", "reboot", unique_vm_name)
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, (
                f"VM '{unique_vm_name}' not found after reboot"
            )

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
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestMultiKeyJourney:
    """Test VM creation with multiple SSH keys."""

    pytestmark = [pytest.mark.domain_vm]

    def test_journey_multiple_ssh_keys(
        self, mvm_binary, unique_key_name, unique_vm_name
    ):
        """Create two keys, then create VM with both keys."""
        key_a = f"{unique_key_name}-a"
        key_b = f"{unique_key_name}-b"
        net_name = f"sys-multikey-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)

        _run_mvm(mvm_binary, "key", "create", key_a, "--algorithm", "ed25519")
        _run_mvm(mvm_binary, "key", "create", key_b, "--algorithm", "ed25519")
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
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
                net_name,
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
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_a, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_b, check=False)


class TestInterVMCommunication:
    """Test inter-VM communication."""

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
                "--cmd",
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


class TestCreateWithAllFlags:
    """Creating VM with every flag simultaneously should work."""

    pytestmark = [pytest.mark.domain_workflow]

    def test_create_with_all_flags(
        self, mvm_binary: str, unique_vm_name: str
    ) -> None:
        vm_name = unique_vm_name
        net_name = f"sys-allflags-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
                "--vcpus",
                "2",
                "--mem",
                "1024",
                "--disk-size",
                "2G",
                "--enable-logging",
                "--enable-metrics",
                "--enable-pci",
                "--no-console",
            )

            result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            data: dict[str, Any] = json.loads(result.stdout)

            assert data.get("vcpus") == 2, (
                f"Expected vcpus=2, got {data.get('vcpus')}"
            )
            assert data.get("mem_mib") == 1024, (
                f"Expected mem_mib=1024, got {data.get('mem_mib')}"
            )
            assert data.get("disk_mib") == 2048, (
                f"Expected disk_mib=2048, got {data.get('disk_mib')}"
            )
            assert data.get("enable_logging") is True, (
                f"Expected enable_logging=True, got {data.get('enable_logging')}"
            )
            assert data.get("enable_metrics") is True, (
                f"Expected enable_metrics=True, got {data.get('enable_metrics')}"
            )
            assert data.get("enable_pci") is True, (
                f"Expected enable_pci=True, got {data.get('enable_pci')}"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestMultipleVolumes:
    """VM with 3 volumes should start and run."""

    pytestmark = [pytest.mark.domain_workflow]

    @pytest.mark.requires_network
    def test_multiple_volumes_on_one_vm(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
    ) -> None:
        vm_name = unique_vm_name
        key_name = unique_key_name
        net_name = f"sys-multivol-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
        vol_a = f"sys-vol-a-{uuid.uuid4().hex[:6]}"
        vol_b = f"sys-vol-b-{uuid.uuid4().hex[:6]}"
        vol_c = f"sys-vol-c-{uuid.uuid4().hex[:6]}"

        try:
            _run_mvm(
                mvm_binary,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
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

            _run_mvm(mvm_binary, "volume", "create", vol_a, "512M")
            _run_mvm(mvm_binary, "volume", "create", vol_b, "512M")
            _run_mvm(mvm_binary, "volume", "create", vol_c, "512M")

            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
                "--volume",
                vol_a,
                "--volume",
                vol_b,
                "--volume",
                vol_c,
                "--ssh-key",
                key_name,
            )

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms: list[dict[str, Any]] = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None, f"VM '{vm_name}' not found"
            assert vm_entry.get("status") == "running", (
                f"Expected 'running', got '{vm_entry.get('status')}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_a,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_b,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_c,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestStressCreateDestroy:
    """Create and destroy 5 VMs sequentially to detect resource leak accumulation."""

    pytestmark = [pytest.mark.domain_workflow]

    def test_stress_create_destroy_sequential(self, mvm_binary: str) -> None:
        vm_names = [f"sys-stress-{uuid.uuid4().hex[:8]}" for _ in range(5)]
        net_name = f"sys-stress-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
        success_count = 0

        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )

        try:
            for vm_name in vm_names:
                result = _run_mvm(
                    mvm_binary,
                    "vm",
                    "create",
                    "--name",
                    vm_name,
                    "--image",
                    "alpine-3.21",
                    "--network",
                    net_name,
                    check=False,
                )
                if result.returncode != 0:
                    continue

                ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
                vms: list[dict[str, Any]] = json.loads(ls_result.stdout)
                vm_entry = next((v for v in vms if v["name"] == vm_name), None)
                if vm_entry is None:
                    continue

                rm_result = _run_mvm(
                    mvm_binary,
                    "vm",
                    "rm",
                    vm_name,
                    "--force",
                    check=False,
                )
                if rm_result.returncode == 0:
                    success_count += 1

            assert success_count == 5, (
                f"Expected 5 successful create/destroy cycles, "
                f"got {success_count}"
            )
        finally:
            for vm_name in vm_names:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "rm",
                    vm_name,
                    "--force",
                    check=False,
                )
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestExportImport:
    """Export a VM to file, then import it back."""

    pytestmark = [pytest.mark.domain_workflow]

    @pytest.mark.requires_network
    def test_export_then_import_vm(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        tmp_path: Path,
    ) -> None:
        vm_name = unique_vm_name
        new_name = f"{vm_name}-imported"
        network_name = f"{vm_name}-net"
        subnet = _unique_subnet(network_name)

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                network_name,
            )

            export_path = tmp_path / "vm_export.json"
            _run_mvm(
                mvm_binary,
                "vm",
                "export",
                vm_name,
                str(export_path),
            )

            export_data = json.loads(export_path.read_text())
            assert isinstance(export_data, dict), (
                "Exported config must be a dict"
            )

            _run_mvm(mvm_binary, "vm", "rm", vm_name)

            _run_mvm(
                mvm_binary,
                "vm",
                "import",
                str(export_path),
                "--name",
                new_name,
            )

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms: list[dict[str, Any]] = json.loads(result.stdout)
            imported_vm = next((v for v in vms if v["name"] == new_name), None)
            assert imported_vm is not None, (
                f"Imported VM '{new_name}' not found"
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
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )


class TestConcurrentVMCreation:
    """Create 10 VMs concurrently via ThreadPoolExecutor."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
    ]

    def test_concurrent_vm_creation_and_ssh(
        self, mvm_binary, unique_vm_name, timing_targets
    ):
        vm_count = 10
        vm_names = [f"{unique_vm_name}-{i}" for i in range(vm_count)]
        key_names = [f"{unique_vm_name}-key-{i}" for i in range(vm_count)]
        net_name = f"sys-concurrent-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)

        for key_name in key_names:
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

        def create_vm(name: str, key_name: str) -> Any:
            return _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
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
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            for key_name in key_names:
                _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
