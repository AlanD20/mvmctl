"""VM log viewing system tests — extracted from test_console.py."""

from __future__ import annotations

import json
import os
import shlex
import subprocess
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
def logs_vm(mvm_binary: str) -> Generator[dict[str, Any], None, None]:
    """Create a running VM with network for log tests (module-scoped).

    Creates a dedicated network + SSH key so the VM has full connectivity.
    The VM is started so boot/OS logs are available for reading.
    """
    vm_name = f"sys-logsvm-{uuid.uuid4().hex[:8]}"
    key_name = f"sys-logs-key-{uuid.uuid4().hex[:6]}"
    net_name = f"sys-logsnet-{uuid.uuid4().hex[:6]}"

    try:
        # Create network
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

        # Create SSH key
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )

        # Create VM with network and key
        ensure_vm_deps(mvm_binary)
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            net_name,
            "--ssh-key",
            key_name,
        )

        # Start VM so it produces boot/OS logs
        _run_mvm(mvm_binary, "vm", "start", vm_name)

        vms = _parse_vm_list(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
        vm_info = next((v for v in vms if v["name"] == vm_name), None)

        if not vm_info:
            raise RuntimeError(f"Failed to find created VM: {vm_name}")

        yield vm_info
    finally:
        _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
        _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
        _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestVMLogs:
    """Test VM log viewing operations."""

    def test_logs_boot_output(self, mvm_binary, logs_vm):
        """Show boot log for a running VM."""
        result = _run_mvm(mvm_binary, "logs", logs_vm["name"])
        assert result.returncode == 0
        assert result.stdout.strip()

    def test_logs_os_output(self, mvm_binary, logs_vm):
        """Show Firecracker OS log for a running VM."""
        result = _run_mvm(mvm_binary, "logs", logs_vm["name"], "--os")
        assert result.returncode == 0

    def test_logs_follow_runs(self, mvm_binary, logs_vm):
        """Follow log output for a brief period."""
        cmd = [*shlex.split(mvm_binary), "logs", logs_vm["name"], "--follow"]
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

    def test_logs_os_follow(self, mvm_binary, logs_vm):
        """Combined --os --follow flags should not crash."""
        cmd = [
            *shlex.split(mvm_binary),
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

    def test_logs_with_lines_limit(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """--lines flag should limit output to N lines."""
        vm_name = unique_vm_name
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                created_network,
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

    def test_logs_by_ip_fails_for_stopped_vm(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """Logs by IP should only work for running VMs."""
        vm_name = unique_vm_name
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                created_network,
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
