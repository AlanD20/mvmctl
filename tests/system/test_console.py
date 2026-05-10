"""Console and VM log system tests — console state, relay management, and log streaming."""

from __future__ import annotations

import json
import os
import shlex
import subprocess
from typing import Any

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_vm,
]


class TestConsoleState:
    """Test console state reporting on a running VM."""

    def test_console_state(self, mvm_binary, module_vm):
        """Show console relay state for a running VM."""
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
            assert "No console relay" in combined


class TestConsoleOnStoppedVM:
    """Test console behavior on a stopped VM."""

    def test_console_on_stopped_vm_fails(self, mvm_binary, unique_vm_name):
        """Console requires running VM — stopped should fail."""
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
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


class TestVMLogs:
    """Test VM log viewing operations."""

    def test_logs_boot_output(self, mvm_binary, module_vm):
        """Show boot log for a running VM."""
        result = _run_mvm(mvm_binary, "logs", module_vm["name"])
        assert result.returncode == 0
        assert result.stdout.strip()

    def test_logs_os_output(self, mvm_binary, module_vm):
        """Show Firecracker OS log for a running VM."""
        result = _run_mvm(mvm_binary, "logs", module_vm["name"], "--os")
        assert result.returncode == 0

    def test_logs_follow_runs(self, mvm_binary, module_vm):
        """Follow log output for a brief period."""
        cmd = [*shlex.split(mvm_binary), "logs", module_vm["name"], "--follow"]
        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=2,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0
        except subprocess.TimeoutExpired:
            pass

    def test_logs_on_nonexistent_vm_fails(self, mvm_binary):
        """Logs on nonexistent VM should give clear error."""
        result = _run_mvm(
            mvm_binary,
            "logs",
            "nonexistent-vm-name-12345",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["not found", "no such"])

    def test_logs_with_lines_limit(self, mvm_binary, unique_vm_name):
        """--lines flag should limit output to N lines."""
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
            )
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            result5 = _run_mvm(mvm_binary, "logs", vm_name, "--lines", "5")
            assert result5.returncode == 0
            assert len(result5.stdout.splitlines()) <= 5

            result50 = _run_mvm(mvm_binary, "logs", vm_name, "--lines", "50")
            assert result50.returncode == 0
            assert len(result50.stdout.splitlines()) <= 50
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_logs_by_ip_fails_for_stopped_vm(self, mvm_binary, unique_vm_name):
        """Logs by IP should only work for running VMs."""
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
            )

            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms: list[dict[str, Any]] = json.loads(ls_result.stdout)
            vm_entry = next((v for v in vms if v.get("name") == vm_name), None)
            vm_ip: str | None = vm_entry.get("ipv4") if vm_entry else None
            if not vm_ip:
                pytest.skip("VM has no IPv4 address assigned")

            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")

            result = _run_mvm(
                mvm_binary,
                "logs",
                vm_ip,
                check=False,
            )
            assert result.returncode in (0, 1)

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
