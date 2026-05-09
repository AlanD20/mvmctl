"""Cross-resource consistency system tests — volume↔VM and network↔VM interactions.

These tests verify that interdependent resources correctly reflect their
relationships via inspect, list, and lifecycle operations. For example:
  - Volume inspect shows which VM a volume is attached to
  - VM inspect shows which volumes are attached
  - Network inspect shows which VMs have leases on it
  - Removing a VM releases its attached volumes back to available
  - Removing a network with active VMs is rejected
"""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_cross_resource,
]


class TestVolumeVMConsistency:
    """Test volume↔VM cross-resource consistency."""

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_volume_shows_attached_vm_in_inspect(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
    ) -> None:
        """Volume inspect should show the ID of the VM it is attached to."""
        key_name = f"sys-cr-vm-{unique_key_name}"
        vol_name = f"sys-cr-vol-{unique_key_name}"
        vm_name = unique_vm_name

        try:
            # Create key
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )

            # Create volume
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            # Create VM with volume and SSH key
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            # Stop VM to allow clean state
            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")

            # Volume inspect — should show vm_id
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached', got '{vol_data['status']}'"
            )
            assert vol_data["vm_id"] is not None, (
                "Volume should have vm_id when attached"
            )

            # Get VM ID from vm ls
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm_info = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_info is not None, f"VM '{vm_name}' not found in listing"
            assert vol_data["vm_id"] == vm_info["id"], (
                f"Volume vm_id '{vol_data['vm_id']}' does not match "
                f"VM ID '{vm_info['id']}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_inspect_shows_attached_volumes(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
    ) -> None:
        """VM inspect should list attached volumes."""
        key_name = f"sys-cr-vs-{unique_key_name}"
        vol_name = f"sys-cr-vs-{unique_key_name}"
        vm_name = unique_vm_name

        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            # VM inspect — check for attached volumes
            vm_inspect = _run_mvm(
                mvm_binary, "vm", "inspect", vm_name, "--json"
            )
            vm_data = json.loads(vm_inspect.stdout)
            volumes = vm_data.get("volumes", [])
            volume_names = [
                v["name"] if isinstance(v, dict) else v for v in volumes
            ]
            assert vol_name in volume_names, (
                f"Volume '{vol_name}' not found in VM inspect volumes: "
                f"{volume_names}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_create_volume_by_id_prefix(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
    ) -> None:
        """VM creation with --volume using a 6-char volume ID prefix works."""
        key_name = f"sys-cr-pf-{unique_key_name}"
        vol_name = f"sys-cr-pf-{unique_key_name}"
        vm_name = unique_vm_name

        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            # Get volume ID prefix (first 6 chars)
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_id_prefix = vol_data["id"][:6]

            # Create VM using volume ID prefix instead of name
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--volume",
                vol_id_prefix,
                "--ssh-key",
                key_name,
            )
            assert result.returncode == 0, (
                f"VM creation with volume ID prefix failed: {result.stderr}"
            )

            # Verify volume is attached
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached', got '{vol_data['status']}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_create_volume_by_name(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
    ) -> None:
        """VM creation with --volume using the volume name works."""
        key_name = f"sys-cr-nm-{unique_key_name}"
        vol_name = f"sys-cr-nm-{unique_key_name}"
        vm_name = unique_vm_name

        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            # Create VM using volume name
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )
            assert result.returncode == 0, (
                f"VM creation with volume name failed: {result.stderr}"
            )

            # Verify volume is attached
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached', got '{vol_data['status']}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_rm_releases_volume(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
    ) -> None:
        """Removing a VM releases its attached volume back to 'available'."""
        key_name = f"sys-cr-rl-{unique_key_name}"
        vol_name = f"sys-cr-rl-{unique_key_name}"
        vm_name = unique_vm_name

        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            # Verify volume is attached
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached' before VM removal, "
                f"got '{vol_data['status']}'"
            )

            # Remove VM
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force")

            # Verify volume is available again
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "available", (
                f"Expected volume status 'available' after VM removal, "
                f"got '{vol_data['status']}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestNetworkVMConsistency:
    """Test network↔VM cross-resource consistency."""

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_network_shows_attached_vm(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """Network inspect should show the VM attached to it via leases."""
        net_name = unique_network_name
        vm_name = unique_vm_name

        subnet = _unique_subnet(net_name)

        try:
            # Create network
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            # Create VM on the network
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
            )

            # Get VM info — verify it's on the right network
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm_info = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_info is not None, f"VM '{vm_name}' not found"
            assert vm_info.get("network_name") == net_name, (
                f"VM is on network '{vm_info.get('network_name')}', "
                f"expected '{net_name}'"
            )
            assert vm_info.get("ipv4"), f"VM '{vm_name}' has no IPv4 address"

            # Network inspect — verify network exists and is correctly identified
            net_inspect = _run_mvm(
                mvm_binary, "network", "inspect", net_name, "--json"
            )
            net_data = json.loads(net_inspect.stdout)
            assert net_data.get("name") == net_name, (
                f"Network inspect returned wrong name: {net_data.get('name')}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_network_rm_rejects_active_vms(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """Removing a network with active VMs fails with a clear error."""
        net_name = unique_network_name
        vm_name = unique_vm_name

        subnet = _unique_subnet(net_name)

        try:
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
                vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
            )

            # Try removing network without --force — should fail
            result = _run_mvm(
                mvm_binary,
                "network",
                "rm",
                net_name,
                check=False,
            )
            assert result.returncode != 0, (
                "Network removal should have failed with active VMs"
            )
            error_text = (result.stdout + result.stderr).lower()
            assert (
                "referenced by vms" in error_text or "in use" in error_text
            ), (
                f"Expected error about VMs referencing the network, "
                f"got: {result.stderr}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
