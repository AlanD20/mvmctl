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
        # The VM was created with --no-console (see _create_minimal_vm_core),
        # so the console relay is stopped. "stopped" is valid state info.
        assert (
            "stopped" in result.stdout.lower()
            or "not running" in (result.stdout + result.stderr).lower()
        ), (
            f"Expected console state output ('stopped' or 'not running'), "
            f"got: {result.stdout}"
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
        assert (
            "stopped" in result.stdout.lower()
            or "not running" in (result.stdout + result.stderr).lower()
        ), (
            f"Expected console state output ('stopped' or 'not running'), "
            f"got: {result.stdout}"
        )

    def test_console_state_by_ip(self, mvm_binary, module_vm):
        """Show console relay state using IP as positional arg."""
        # Rationale: Needs a running VM with an IP (module_vm) to test
        # console state resolution by IP address.
        ip = module_vm.get("ipv4")
        if not ip:
            # Skip-reason: VM was created without DHCP lease or network.
            # Console state by IP requires a known IPv4 address.
            pytest.skip("VM has no IPv4 address assigned")
        result = _run_mvm(
            mvm_binary,
            "console",
            ip,
            "--state",
        )
        assert result.returncode == 0
        assert (
            "stopped" in result.stdout.lower()
            or "not running" in (result.stdout + result.stderr).lower()
        ), (
            f"Expected console state output ('stopped' or 'not running'), "
            f"got: {result.stdout}"
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
            assert "stopped" in result.stdout, (
                f"Expected 'stopped' in console kill output, got: {result.stdout}"
            )
        else:
            combined = result.stdout + result.stderr
            assert "not running" in combined

    def test_console_kill_check_state_then_kill(self, mvm_binary, module_vm):
        """Check console state, then kill the relay, then verify it's no longer running.

        Rationale: Verifies the full console lifecycle: check state → kill →
        verify stopped. A regression where --state reports running but --kill
        silently fails would leave orphan relay processes.
        """
        # Step 1: Check console state before killing (result may be unused)
        _run_mvm(
            mvm_binary,
            "console",
            module_vm["name"],
            "--state",
            check=False,
        )
        # The relay may or may not be running — accept either

        # Step 2: Kill the console relay
        kill_result = _run_mvm(
            mvm_binary,
            "console",
            module_vm["name"],
            "--kill",
            check=False,
        )

        # Step 3: Check console state after killing
        state_after = _run_mvm(
            mvm_binary,
            "console",
            module_vm["name"],
            "--state",
            check=False,
        )

        # After kill, state should indicate stopped or not running
        if kill_result.returncode == 0:
            assert "stopped" in kill_result.stdout, (
                f"Expected 'stopped' in kill output, got: {kill_result.stdout}"
            )
            # After successful kill, state should show stopped
            if state_after.returncode == 0:
                assert (
                    "stopped" in state_after.stdout.lower()
                    or "not running"
                    in (state_after.stdout + state_after.stderr).lower()
                ), (
                    f"Expected console to be stopped after kill, "
                    f"got: {state_after.stdout}"
                )
        else:
            combined = kill_result.stdout + kill_result.stderr
            assert "not running" in combined, (
                f"Expected 'not running' if kill fails, got: {combined}"
            )


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
