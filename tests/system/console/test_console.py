"""Console system tests — console state and relay management."""

from __future__ import annotations

import json
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_console,
]


class TestConsoleState:
    """Test console state reporting on a running VM."""

    def test_console_state(self, mvm_binary, module_vm):
        """Show console relay state for a running VM."""
        # Rationale: Needs a running VM (module_vm) because console relay
        # state is only meaningful when a VM process is active.
        result = _run_mvm(
            mvm_binary,
            "console",
            module_vm["name"],
            "--state",
        )
        assert result.returncode == 0
        combined = result.stdout + result.stderr
        assert (
            "Console" in combined
            or "running" in combined
            or "stopped" in combined
        )

    def test_console_state_by_name_flag(self, mvm_binary, module_vm):
        """Show console relay state using VM name as positional arg."""
        # Rationale: Needs a running VM (module_vm) to validate console
        # state by name resolution.
        result = _run_mvm(
            mvm_binary,
            "console",
            module_vm["name"],
            "--state",
        )
        assert result.returncode == 0
        combined = result.stdout + result.stderr
        assert (
            "Console" in combined
            or "running" in combined
            or "stopped" in combined
        )

    def test_console_state_by_ip(self, mvm_binary, module_vm):
        """Show console relay state using IP as positional arg."""
        # Rationale: Needs a running VM with an IP (module_vm) to test
        # console state resolution by IP address.
        ip = module_vm.get("ipv4")
        if not ip:
            pytest.skip("VM has no IPv4 address assigned")
        result = _run_mvm(
            mvm_binary,
            "console",
            ip,
            "--state",
        )
        assert result.returncode == 0
        combined = result.stdout + result.stderr
        assert (
            "Console" in combined
            or "running" in combined
            or "stopped" in combined
        )


class TestConsoleKill:
    """Test console relay kill operation."""

    def test_console_kill(self, mvm_binary, module_vm):
        """Kill the console relay for a VM.

        The relay may not be running if no one has attached yet,
        so we accept either success or the expected error message.
        """
        # Rationale: Needs a running VM (module_vm) because console relay
        # kill only applies to an active VM process.
        result = _run_mvm(
            mvm_binary,
            "console",
            module_vm["name"],
            "--kill",
            check=False,
        )

        if result.returncode == 0:
            assert (
                "stopped" in result.stdout
                or "killed" in (result.stdout + result.stderr).lower()
            )
        else:
            combined = result.stdout + result.stderr
            assert "not running" in combined


class TestConsoleOnStoppedVM:
    """Test console behavior on a stopped VM."""

    def test_console_on_stopped_vm_fails(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Console requires running VM — stopped should fail."""
        # Rationale: Needs a VM (even stopped) to verify the console
        # command correctly rejects non-running VMs. VM and network are
        # created and cleaned up within this test.
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            # Create a network for the VM
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
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
            )

            result = _run_mvm(
                mvm_binary,
                "console",
                vm_name,
                check=False,
            )
            assert result.returncode != 0

            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                vms: list[dict[str, Any]] = json.loads(ls_result.stdout)
                vm_entry = next(
                    (v for v in vms if v.get("name") == vm_name), None
                )
                if vm_entry:
                    assert vm_entry.get("state") != "Running"
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
