"""System tests for ``mvm exec`` — execute commands inside VMs via vsock agent.

Architecture
============
Tests use a module-scoped, networkless VM created with the ``--user runner``
flag. The ``exec_vm`` fixture verifies the vsock agent is reachable before any
test runs and checks teardown afterward.

Edge cases covered per file
---------------------------
Happy path: basic command execution, interactive shell, --port, --user.
Invalid args: nonexistent VM and invalid vsock port.
JSON output: exec does not have --json; output is raw command output.
Confirmation prompts: N/A for exec.
Non-existent resources: nonexistent VM → error.
Duplicates: N/A for exec.
"""

from __future__ import annotations

import subprocess
import time
import uuid
from typing import Any, Generator

import pytest

from tests.system.conftest import (
    SYSTEM_MVM_BINARY,
    _guest_run,
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
    ensure_vm_deps,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_vm,
]


# ============================================================================
# Helpers
# ============================================================================


def _wait_for_exec(
    runner_vm: str, vm_name: str, timeout: float = 10.0
) -> bool:
    """Poll ``mvm exec`` until the vsock agent responds or *timeout* expires."""
    start = time.monotonic()
    while time.monotonic() - start < timeout:
        result = _run_mvm(
            runner_vm,
            "exec",
            vm_name,
            "--timeout",
            "10",
            "--",
            "exit",
            check=False,
        )
        if result.returncode == 0:
            return True
        time.sleep(0.5)
    return False


def _exec_cmd(
    runner_vm: str,
    vm_name: str,
    command: str,
    *,
    user: str = "root",
    port: int | None = None,
    timeout_s: int = 10,
    check: bool = True,
) -> subprocess.CompletedProcess[str]:
    """Run *command* inside *vm_name* via ``mvm exec``.

    Returns a ``subprocess.CompletedProcess`` with ``stdout`` and ``stderr``.
    """
    args: list[str] = ["exec", vm_name]
    if user:
        args.extend(["--user", user])
    if port is not None:
        args.extend(["--port", str(port)])
    if timeout_s:
        args.extend(["--timeout", str(timeout_s)])
    args.extend(["--", command])
    return _run_mvm(runner_vm, *args, check=check, timeout=timeout_s + 5)


# ============================================================================
# Fixtures
# ============================================================================


@pytest.fixture(scope="module")
def exec_vm(runner_vm: str) -> Generator[dict[str, Any], None, None]:
    """A networkless guest with a reachable agent and checked removal."""
    ensure_vm_deps(runner_vm)
    vm_name = f"sys-exec-{uuid.uuid4().hex[:8]}"
    created_by_fixture = False
    try:
        creation = _run_mvm(
            runner_vm, "vm", "create", vm_name, "--no-network", "--cloud-init-mode", "off",
            "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
            "--user", "runner", "--json", timeout=180,
        )
        created_by_fixture = True
        created = _json_success_data(
            creation.stdout,
            list,
        )
        assert len(created) == 1 and created[0]["name"] == vm_name
        assert _wait_for_exec(runner_vm, vm_name), (
            f"mvm exec is not available for VM '{vm_name}' within timeout"
        )
        yield {"name": vm_name}
    finally:
        if created_by_fixture:
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if any(vm["name"] == vm_name for vm in listed):
                removed = _run_mvm(
                    runner_vm, "vm", "rm", vm_name, "--force", check=False, timeout=120,
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert all(vm["name"] != vm_name for vm in listed)


# ========================================================================
# TestExecBasic — simple command execution
# ========================================================================


class TestExecBasic:
    """Basic execution: run a command, run an interactive shell command."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_exec_basic_command(self, runner_vm, exec_vm):
        """Run a simple command via ``mvm exec <id> -- <cmd>``.

        Tier: L2 (requires running VM with vsock agent).
        """
        vm_name = exec_vm["name"]
        result = _exec_cmd(runner_vm, vm_name, "echo hello")
        assert result.returncode == 0, (
            f"exec failed: rc={result.returncode} "
            f"stderr={result.stderr!r}"
        )
        assert "hello" in result.stdout, (
            f"Expected 'hello' in stdout, got: {result.stdout!r}"
        )

    def test_exec_interactive_shell(self, runner_vm, exec_vm):
        """Run a command via interactive shell (no ``--`` separator).

        ``mvm exec <id>`` (without ``--`` or a trailing command) starts an
        interactive vsock session. The CLI needs a terminal for raw mode, so
        ``script`` supplies a PTY while stdin feeds a command and exit.

        Tier: L2 (requires running VM with vsock agent).
        """
        vm_name = exec_vm["name"]
        result = _guest_run(
            runner_vm,
            "printf 'echo hello\\nexit\\n' | "
            f"script -q -e -c '{SYSTEM_MVM_BINARY} exec {vm_name}' /dev/null",
            check=False,
        )
        assert result.returncode == 0, (
            f"interactive exec failed: rc={result.returncode} "
            f"stdout={result.stdout!r} stderr={result.stderr!r}"
        )
        assert "hello" in result.stdout, (
            f"Expected 'hello' in interactive exec stdout, "
            f"got: {result.stdout!r}"
        )


# ========================================================================
# TestExecFlags — --port, --user
# ========================================================================


class TestExecFlags:
    """Exec flag handling: --port and --user."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_exec_with_port(self, runner_vm, exec_vm):
        """Execute a command with explicit ``--port``.

        The default vsock agent port is 1024.  We set ``--port 1024``
        explicitly to verify the flag is properly wired and forwarded
        to the vsock client.

        Tier: L2 (requires running VM with vsock agent).
        """
        vm_name = exec_vm["name"]
        result = _exec_cmd(
            runner_vm, vm_name, "echo hello", port=1024,
        )
        assert result.returncode == 0, (
            f"exec --port 1024 failed: rc={result.returncode} "
            f"stderr={result.stderr!r}"
        )
        assert "hello" in result.stdout, (
            f"Expected 'hello' in stdout, got: {result.stdout!r}"
        )

    def test_exec_with_user(self, runner_vm, exec_vm):
        """Execute a command as a non-root user via ``--user``.

        The ``exec_vm`` was created with ``--user runner``, so a
        ``runner`` user should exist inside the guest.  Running
        ``whoami`` via ``--user runner`` should return ``runner``.

        Tier: L2 (requires the guest to have a non-root user configured).
        """
        vm_name = exec_vm["name"]
        result = _exec_cmd(
            runner_vm, vm_name, "whoami", user="runner",
        )
        assert result.returncode == 0, (
            f"exec --user runner failed: rc={result.returncode} "
            f"stderr={result.stderr!r}"
        )
        stdout = result.stdout.strip()
        assert stdout == "runner", (
            f"Expected 'runner' from whoami, got: {stdout!r}"
        )


# ========================================================================
# TestExecErrorCases — invalid inputs and resource states
# ========================================================================


class TestExecErrorCases:
    """Error handling for missing resources and invalid flags."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_exec_nonexistent_vm(self, runner_vm):
        """``mvm exec <nonexistent> -- echo hello`` must fail."""
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
        result = _run_mvm(
            runner_vm,
            "exec",
            nonexistent,
            "--",
            "echo hello",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected exec on nonexistent VM to fail, "
            f"but got rc=0: stdout={result.stdout!r}"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined or "error" in combined, (
            f"Expected error about VM not found, "
            f"got: stderr={result.stderr!r} stdout={result.stdout!r}"
        )

    def test_exec_invalid_port(self, runner_vm, exec_vm):
        """Invalid 32-bit vsock ports must reject before guest effects."""
        vm_name = exec_vm["name"]
        for port in ("-1", str(2**32)):
            sentinel = f"/tmp/mvm-invalid-port-{uuid.uuid4().hex[:8]}"
            result = _run_mvm(
                runner_vm,
                "exec",
                vm_name,
                "--port",
                port,
                "--",
                "touch",
                sentinel,
                check=False,
            )
            assert result.returncode != 0, (
                f"Expected exec with invalid port {port} to fail, "
                f"but got rc=0: stdout={result.stdout!r}"
            )
            assert "port" in (result.stdout + result.stderr).lower()
            absent = _exec_cmd(runner_vm, vm_name, f"test ! -e {sentinel}")
            assert absent.returncode == 0, f"invalid port {port} reached the guest"
