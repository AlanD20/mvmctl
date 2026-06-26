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

    def test_console_state(self, runner_vm, module_vm):
        """Show console relay state for a running VM."""
        result = _run_mvm(
            runner_vm,
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

    def test_console_state_by_name_flag(self, runner_vm, module_vm):
        """Show console relay state using VM name as positional arg."""
        result = _run_mvm(
            runner_vm,
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

    def test_console_state_by_ip(self, runner_vm, module_vm):
        """Show console relay state using IP as positional arg."""
        ip = module_vm.get("ipv4")
        assert ip, f"VM has no IPv4 address assigned: {module_vm}"
        result = _run_mvm(
            runner_vm,
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

    def test_console_kill(self, runner_vm, module_vm):
        """Kill the console relay for a VM.

        The relay may not be running if no one has attached yet,
        so we accept either success or the expected error message.
        """
        result = _run_mvm(
            runner_vm,
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
            assert "not running" in combined or "No console relay" in combined

    def test_console_kill_check_state_then_kill(self, runner_vm, module_vm):
        """Check console state, then kill the relay, then verify it's no longer running."""
        # Step 1: Check console state before killing
        _run_mvm(
            runner_vm,
            "console",
            module_vm["name"],
            "--state",
            check=False,
        )

        # Step 2: Kill the console relay
        kill_result = _run_mvm(
            runner_vm,
            "console",
            module_vm["name"],
            "--kill",
            check=False,
        )

        # Step 3: Check console state after killing
        state_after = _run_mvm(
            runner_vm,
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
            assert "not running" in combined or "No console relay" in combined, (
                f"Expected 'not running' or 'No console relay' if kill fails, got: {combined}"
            )


class TestConsoleOnStoppedVM:
    """Test console behavior on a stopped VM."""

    def test_console_on_stopped_vm_fails(
        self, runner_vm, unique_vm_name, unique_network_name
    ):
        """Console requires running VM — stopped should fail."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )

            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name)

            result = _run_mvm(
                runner_vm,
                "console",
                vm_name,
                check=False,
            )
            assert result.returncode != 0

            ls_result = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                vms: list[dict[str, Any]] = json.loads(ls_result.stdout)
                vm_entry = next(
                    (v for v in vms if v.get("name") == vm_name), None
                )
                if vm_entry:
                    assert vm_entry.get("state") != "Running"
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)
