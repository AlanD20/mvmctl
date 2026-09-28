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

import json
import os
import select
import subprocess
import time
import uuid
from pathlib import Path
from typing import Any, Generator

import pytest

from tests.system.conftest import (
    SYSTEM_MVM_BINARY,
    _guest_run,
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
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


_EXEC_INVENTORIES = {
    "image": ("image", "ls"), "kernel": ("kernel", "ls"), "bin": ("bin", "ls"),
    "vm": ("vm", "ls"), "network": ("network", "ls"), "volume": ("volume", "ls"),
    "key": ("key", "ls"), "snapshot": ("snapshot", "ls"), "env": ("env", "ls"),
    "exec_policy": ("policy", "exec", "ls"), "traffic_policy": ("policy", "traffic", "ls"),
    "config": ("config", "ls"),
}


def _exec_inventory(runner_vm: str, domain: str):
    expected = dict if domain in ("traffic_policy", "config") else list
    data = _json_success_data(_run_mvm(runner_vm, *_EXEC_INVENTORIES[domain], "--json").stdout, expected)
    return sorted(data, key=lambda item: json.dumps(item, sort_keys=True)) if isinstance(data, list) else data


def _require_exec_assets(baseline: dict) -> dict[str, str]:
    """Require the preseeded launch selection without pulling or changing defaults."""
    images = [item for item in baseline["image"] if item["is_present"]
              and (item["type"], item["version"]) == ("alpine", "3.23")]
    assert len(images) == 1, images
    kernels = [item for item in baseline["kernel"] if item["is_default"]]
    assert len(kernels) == 1 and kernels[0]["is_present"], kernels
    selected = {"image_id": images[0]["id"], "kernel_id": kernels[0]["id"]}
    for kind in ("firecracker", "jailer"):
        binaries = [item for item in baseline["bin"] if item["type"] == kind and item["is_default"]]
        assert len(binaries) == 1 and binaries[0]["is_present"] and binaries[0]["version"] == "1.16.2", binaries
        selected["binary_id" if kind == "firecracker" else "jailer_binary_id"] = binaries[0]["id"]
    return selected


def _exec_pin_task(vm: dict) -> tuple[int, int, int]:
    """Pin the CLI-observed task and prove its exact start identity and liveness."""
    pid, ticks = int(vm["pid"]), int(vm["process_start_time"])
    assert pid > 0 and ticks > 0, vm
    fd = os.pidfd_open(pid)
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
        assert len(fields) >= 20 and int(fields[19]) == ticks and fields[0] not in ("Z", "X"), (vm, fields)
        poller = select.poll()
        poller.register(fd, select.POLLIN)
        assert poller.poll(0) == [], vm
        return fd, pid, ticks
    except BaseException:
        os.close(fd)
        raise


@pytest.fixture(scope="module")
def exec_vm(runner_vm: str) -> Generator[dict[str, Any], None, None]:
    """One preseeded networkless guest, retained creation identity, and checked cleanup."""
    baseline = {domain: _exec_inventory(runner_vm, domain) for domain in _EXEC_INVENTORIES}
    selected_assets = _require_exec_assets(baseline)
    vm_name = f"sys-exec-{uuid.uuid4().hex[:8]}"
    current_vms = _exec_inventory(runner_vm, "vm")
    assert current_vms == baseline["vm"], "VM inventory changed before fixture creation"
    before_ids = {item["id"] for item in baseline["vm"]}
    assert all(item["name"] != vm_name for item in baseline["vm"]), vm_name
    args = ["vm", "create", vm_name, "--image", selected_assets["image_id"], "--no-network", "--cloud-init-mode", "off",
            "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
            "--user", "runner", "--json"]
    intent = {"name": vm_name, "arguments": args[:], "before_ids": before_ids}
    receipt = None
    task = None
    try:
        creation = _run_mvm(runner_vm, *args, check=False, timeout=180)
        envelope = json.loads(creation.stdout)
        assert envelope["schema_version"] == 1 and envelope["status"] in ("success", "partial", "error"), envelope
        if envelope["data"] is not None:
            assert isinstance(envelope["data"], list) and len(envelope["data"]) <= 1, envelope
            for vm in envelope["data"]:
                assert vm["name"] == intent["name"] and vm["id"] not in intent["before_ids"] and vm["created_at"], vm
                receipt = {key: vm[key] for key in ("id", "name", "created_at", "pid", "process_start_time")}
        assert creation.returncode == 0, (creation.stdout, creation.stderr)
        created = _json_success_data(
            creation.stdout,
            list,
        )
        assert len(created) == 1 and created[0]["name"] == vm_name
        vm = created[0]
        assert receipt is not None and vm["id"] == receipt["id"], (vm, receipt)
        assert {key: vm[key] for key in selected_assets} == selected_assets, (vm, selected_assets)
        assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (1, 256, 512), vm
        assert vm["network_placement"] == "networkless" and vm["network_id"] == "", vm
        assert vm["ipv4"] == vm["mac"] == vm["tap_device"] == "" and vm["pci_enabled"] is False, vm
        task = _exec_pin_task(vm)
        assert _wait_for_exec(runner_vm, vm_name), (
            f"mvm exec is not available for VM '{vm_name}' within timeout"
        )
        print(f"ordinary exec intent={intent['arguments']} receipt={json.dumps(receipt, sort_keys=True)} "
              f"assets={json.dumps(selected_assets, sort_keys=True)}", flush=True)
        yield {"name": vm_name}
    finally:
        errors = []
        try:
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            matches = [item for item in listed if item["name"] == vm_name
                       or (receipt is not None and item["id"] == receipt["id"])]
            assert len(matches) <= 1, matches
            for vm in matches:
                assert receipt is not None, f"unreceipted VM at creation name: {matches}"
                assert {key: vm[key] for key in ("id", "name", "created_at")} == {
                    key: receipt[key] for key in ("id", "name", "created_at")
                }, (vm, receipt)
                if vm["status"] == "running":
                    assert (vm["pid"], vm["process_start_time"]) == (receipt["pid"], receipt["process_start_time"]), (vm, receipt)
                    if task is None:
                        task = _exec_pin_task(vm)
                    poller = select.poll()
                    poller.register(task[0], select.POLLIN)
                    assert poller.poll(0) == [], (vm, task)
                removed = _run_mvm(
                    runner_vm, "vm", "rm", receipt["id"], "--force", check=False, timeout=120,
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
                if task is not None:
                    poller = select.poll()
                    poller.register(task[0], select.POLLIN)
                    events = poller.poll(10_000)
                    assert len(events) == 1 and events[0][0] == task[0], (receipt, events)
                    assert events[0][1] & (select.POLLIN | select.POLLHUP), events
                    assert not events[0][1] & (select.POLLERR | select.POLLNVAL), events
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert all(item["name"] != vm_name and (receipt is None or item["id"] != receipt["id"])
                       for item in listed), listed
        except Exception as exc:
            errors.append(f"owned VM cleanup: {exc}")
        finally:
            if task is not None:
                os.close(task[0])
        for domain, original in baseline.items():
            try:
                assert _exec_inventory(runner_vm, domain) == original, f"{domain} inventory/config changed"
            except Exception as exc:
                errors.append(f"{domain} baseline: {exc}")
        assert not errors, "; ".join(errors)


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
