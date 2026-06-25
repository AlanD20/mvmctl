"""VM log viewing system tests — extracted from test_console.py."""

from __future__ import annotations

import json
import os
import shlex
import subprocess
import time
import uuid
from typing import Any, Generator

import pytest

from tests.system.conftest import (
    _parse_vm_list,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_logs,
]


@pytest.fixture(scope="module")
def logs_vm(runner_vm: str) -> Generator[dict[str, Any], None, None]:
    """Create a running VM with network for log tests (module-scoped)."""
    vm_name = f"sys-logsvm-{uuid.uuid4().hex[:8]}"
    key_name = f"sys-logs-key-{uuid.uuid4().hex[:6]}"
    net_name = f"sys-logsnet-{uuid.uuid4().hex[:6]}"

    try:
        subnet = _unique_subnet(net_name)
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )

        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )

        ensure_vm_deps(runner_vm)
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            vm_name,
            "--image",
            "alpine:3.23",
            "--network",
            net_name,
            "--ssh-key",
            key_name,
        )

        time.sleep(2)

        vms = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        vm_info = next((v for v in vms if v["name"] == vm_name), None)

        if not vm_info:
            raise RuntimeError(f"Failed to find created VM: {vm_name}")

        yield vm_info
    finally:
        _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
        _run_mvm(runner_vm, "key", "rm", key_name, check=False)
        _run_mvm(runner_vm, "network", "rm", net_name, check=False)


class TestLogsBasic:
    """Basic log output tests using module_vm fixture (read-only, L1)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_logs,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def _start_if_needed(self, runner_vm: str, vm_name: str) -> None:
        """Start VM if not already running, then wait briefly for logs."""
        vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        entry = next((v for v in vms if v["name"] == vm_name), None)
        if entry and entry.get("state") != "Running":
            _run_mvm(runner_vm, "vm", "start", vm_name)
            time.sleep(2)

    def test_logs_basic_output(
        self, runner_vm: str, module_vm: dict[str, Any]
    ) -> None:
        """Read boot log from a running VM via ``mvm logs <vm>``."""
        self._start_if_needed(runner_vm, module_vm["name"])
        result = _run_mvm(runner_vm, "logs", module_vm["name"])
        assert result.returncode == 0
        assert result.stdout.strip(), (
            "Expected non-empty boot log from running VM"
        )

    def test_logs_os_flag(
        self, runner_vm: str, module_vm: dict[str, Any]
    ) -> None:
        """Read Firecracker OS log via ``mvm logs <vm> --os``."""
        self._start_if_needed(runner_vm, module_vm["name"])
        result = _run_mvm(runner_vm, "logs", module_vm["name"], "--os")
        assert result.returncode == 0
        assert result.stdout.strip(), (
            "Expected non-empty OS log from running VM"
        )

    def test_logs_lines_flag(
        self, runner_vm: str, module_vm: dict[str, Any]
    ) -> None:
        """Verify ``--lines N`` flag limits output to at most N lines."""
        self._start_if_needed(runner_vm, module_vm["name"])
        result = _run_mvm(runner_vm, "logs", module_vm["name"], "--lines", "5")
        assert result.returncode == 0
        assert result.stdout.strip(), (
            "Expected non-empty log output with --lines 5"
        )
        lines = result.stdout.splitlines()
        non_empty = [ln for ln in lines if ln.strip()]
        assert len(non_empty) <= 5, (
            f"Expected at most 5 non-empty lines with --lines 5, got {len(non_empty)}"
        )


class TestVMLogs:
    """Test VM log viewing operations."""

    def test_logs_boot_output(self, runner_vm, logs_vm):
        """Show boot log for a running VM."""
        result = _run_mvm(runner_vm, "logs", logs_vm["name"])
        assert result.returncode == 0
        assert result.stdout.strip()

    def test_logs_os_output(self, runner_vm, logs_vm):
        """Show Firecracker OS log for a running VM."""
        result = _run_mvm(runner_vm, "logs", logs_vm["name"], "--os")
        assert result.returncode == 0, f"logs --os failed: {result.stderr}"
        assert result.stdout.strip(), (
            "Expected OS log output from running VM, got empty stdout"
        )

    def test_logs_follow_runs(self, runner_vm, logs_vm):
        """Follow log output for a brief period."""
        cmd = ["mvm", "logs", logs_vm["name"], "--follow"]
        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=2,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0, (
                f"logs --follow completed with error: {result.stderr}"
            )
            assert result.stdout.strip(), "Expected log output from --follow"
        except subprocess.TimeoutExpired:
            # Timeout expected — --follow is an infinite streaming command.
            pass

    def test_logs_os_follow(self, runner_vm, logs_vm):
        """Combined --os --follow flags should not crash."""
        cmd = [
            "mvm",
            "logs",
            logs_vm["name"],
            "--os",
            "--follow",
        ]
        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=2,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0, (
                f"logs --os --follow completed with error: {result.stderr}"
            )
            assert result.stdout.strip(), (
                "Expected log output from --os --follow"
            )
        except subprocess.TimeoutExpired:
            pass

    def test_logs_on_nonexistent_vm_fails(self, runner_vm):
        """Logs on nonexistent VM should give clear error."""
        result = _run_mvm(
            runner_vm,
            "logs",
            "nonexistent-vm-name-12345",
            check=False,
        )
        assert result.returncode != 0, (
            f"Expected non-zero for nonexistent VM, "
            f"got rc={result.returncode}: {result.stderr}"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined, (
            f"Expected 'not found' error for nonexistent VM, "
            f"got stdout: {result.stdout} stderr: {result.stderr}"
        )

    def test_logs_with_lines_limit(
        self, runner_vm, unique_vm_name, created_network
    ):
        """--lines flag should limit output to N lines."""
        vm_name = unique_vm_name
        try:
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                created_network,
            )
            _run_mvm(runner_vm, "vm", "start", vm_name)

            result5 = _run_mvm(runner_vm, "logs", vm_name, "--lines", "5")
            assert result5.returncode == 0
            lines5 = result5.stdout.splitlines()
            non_empty5 = [ln for ln in lines5 if ln.strip()]
            assert len(non_empty5) <= 5

            result50 = _run_mvm(runner_vm, "logs", vm_name, "--lines", "50")
            assert result50.returncode == 0
            lines50 = result50.stdout.splitlines()
            non_empty50 = [ln for ln in lines50 if ln.strip()]
            assert len(non_empty50) <= 50
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)

    def test_logs_by_ip_fails_for_stopped_vm(
        self, runner_vm, unique_vm_name, created_network
    ):
        """Logs by IP should only work for running VMs."""
        vm_name = unique_vm_name
        try:
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                created_network,
            )

            ls_result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms: list[dict[str, Any]] = json.loads(ls_result.stdout)
            vm_entry = next((v for v in vms if v.get("name") == vm_name), None)
            vm_ip: str | None = vm_entry.get("ipv4") if vm_entry else None
            assert vm_ip, "VM has no IPv4 address assigned — cannot test logs by IP"

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            result = _run_mvm(
                runner_vm,
                "logs",
                vm_ip,
                check=False,
            )
            assert result.returncode in (0, 1)

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


class TestVMLogsByIdentifier:
    """Test ``mvm logs`` using different identifier types."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_logs,
    ]

    def test_logs_by_ip(self, runner_vm, created_vm):
        """Show boot log using IP as identifier instead of name."""
        ip = created_vm.get("ipv4", "")
        assert ip, "VM has no IP address — cannot test IP-based log lookup"
        result = _run_mvm(runner_vm, "logs", ip, check=False)
        assert result.returncode == 0
        assert result.stdout.strip()
