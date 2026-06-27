"""System tests for ``mvm exec`` — execute commands inside VMs via vsock agent.

Architecture
============
Tests use a module-scoped VM (``module_vm``) created with ubuntu:24.04 and
the ``--user runner`` flag. The ``exec_vm`` fixture verifies the vsock agent
is reachable before any test runs.

Edge cases covered per file
---------------------------
Happy path: basic command execution, interactive shell, --port, --timeout, --user.
Invalid args: nonexistent VM, empty command (valid — starts interactive shell).
JSON output: exec does not have --json; output is raw command output.
Confirmation prompts: N/A for exec.
Non-existent resources: nonexistent VM → error.
Duplicates: N/A for exec.
"""

from __future__ import annotations

import subprocess
import time
import uuid

import pytest

from tests.system.conftest import _guest_run, _run_mvm

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
def exec_vm(runner_vm, module_vm) -> dict:
    """Module-scoped VM with vsock exec verified available.

    Wraps ``module_vm`` and polls until ``mvm exec {vm} -- exit`` succeeds.
    Tests in this file can assume the vsock agent is reachable.
    """
    vm_name = module_vm["name"]
    if not _wait_for_exec(runner_vm, vm_name):
        pytest.fail(
            f"mvm exec is not available for VM '{vm_name}' within timeout"
        )
    return module_vm


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
        interactive vsock session that reads commands from stdin.  We pipe
        a command via ``printf`` to exercise this code path.

        Tier: L2 (requires running VM with vsock agent).
        """
        vm_name = exec_vm["name"]
        result = _guest_run(
            runner_vm,
            f"printf 'echo hello\\n' | mvm exec {vm_name}",
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
# TestExecFlags — --port, --timeout, --user
# ========================================================================


class TestExecFlags:
    """Exec flag handling: --port, --timeout, --user."""

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

    def test_exec_with_timeout(self, runner_vm, exec_vm):
        """Execute a command with ``--timeout`` set explicitly.

        ``--timeout`` controls how long the vsock client waits for the
        agent's initial response.  A reasonable positive value (10 s)
        should work identically to the default for a healthy VM.

        Tier: L1 (timeout is a client-side parameter, but a running VM
        is needed to confirm the flag is accepted without error).
        """
        vm_name = exec_vm["name"]
        result = _exec_cmd(
            runner_vm, vm_name, "echo hello", timeout_s=10,
        )
        assert result.returncode == 0, (
            f"exec --timeout 30 failed: rc={result.returncode} "
            f"stderr={result.stderr!r}"
        )
        assert "hello" in result.stdout, (
            f"Expected 'hello' in stdout, got: {result.stdout!r}"
        )

    def test_exec_with_user(self, runner_vm, exec_vm):
        """Execute a command as a non-root user via ``--user``.

        The ``module_vm`` was created with ``--user runner``, so a
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
        """``mvm exec <vm> --port -1 -- echo hello`` must fail."""
        vm_name = exec_vm["name"]
        result = _run_mvm(
            runner_vm,
            "exec",
            vm_name,
            "--port",
            "-1",
            "--",
            "echo hello",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected exec with invalid port to fail, "
            f"but got rc=0: stdout={result.stdout!r}"
        )

    def test_exec_empty_command(self, runner_vm, exec_vm):
        """``mvm exec <vm>`` without ``--`` starts interactive shell.

        With no command and no ``--`` separator, exec enters interactive
        mode.  We send an empty input (just newline) and verify the
        session starts (non-zero exit indicates failure).

        Tier: L2 (interactive session requires a running VM).
        """
        vm_name = exec_vm["name"]
        # Use _guest_run with a pipe: send a newline to the interactive
        # session, which should cause the remote shell to produce a
        # prompt or at least not crash.
        result = _guest_run(
            runner_vm,
            f"printf '\\n' | mvm exec {vm_name}",
            check=False,
        )
        # Interactive mode with just a newline should succeed (non-error).
        # Some output is expected (the remote shell's prompt or empty echo).
        assert result.returncode == 0, (
            f"Interactive exec with empty input failed: "
            f"rc={result.returncode} stdout={result.stdout!r} "
            f"stderr={result.stderr!r}"
        )
