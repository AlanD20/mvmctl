"""Root-observed console journey with receipted, checked VM/network cleanup.

Requires a disposable Tier 2 runner with fresh root authority state, present
nondefault Alpine 3.23, a present default kernel and the trusted 1.16.2 pair.
No asset pulls, default changes, or old-journal adoption occur here.
"""

from __future__ import annotations

import json
import os
import select
import stat
import time
import uuid
from pathlib import Path
from typing import Any, Generator

import pytest

from tests.system.conftest import _json_success_data, _run_mvm, _unique_subnet

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_console,
]

_INVENTORIES = {
    "image": ("image", "ls"),
    "kernel": ("kernel", "ls"),
    "bin": ("bin", "ls"),
    "vm": ("vm", "ls"),
    "network": ("network", "ls"),
    "volume": ("volume", "ls"),
    "key": ("key", "ls"),
    "snapshot": ("snapshot", "ls"),
    "env": ("env", "ls"),
    "exec_policy": ("policy", "exec", "ls"),
    "traffic_policy": ("policy", "traffic", "ls"),
    "config": ("config", "ls"),
}
_VM_IDENTITY = ("id", "name", "created_at")
_NETWORK_IDENTITY = (
    "id",
    "name",
    "created_at",
    "subnet",
    "bridge",
    "ipv4_gateway",
    "nat_enabled",
)


def _inventory(runner_vm: str, domain: str) -> Any:
    expected = dict if domain in ("traffic_policy", "config") else list
    data = _json_success_data(
        _run_mvm(runner_vm, *_INVENTORIES[domain], "--json").stdout, expected
    )
    return (
        sorted(data, key=lambda item: json.dumps(item, sort_keys=True))
        if isinstance(data, list)
        else data
    )


def _assets(baseline: dict[str, Any]) -> dict[str, str]:
    images = [
        item
        for item in baseline["image"]
        if item["is_present"]
        and not item["is_default"]
        and (item["type"], item["version"]) == ("alpine", "3.23")
    ]
    assert len(images) == 1, images
    kernels = [item for item in baseline["kernel"] if item["is_default"]]
    assert len(kernels) == 1 and kernels[0]["is_present"], kernels
    selected = {"image_id": images[0]["id"], "kernel_id": kernels[0]["id"]}
    for kind in ("firecracker", "jailer"):
        binaries = [
            item
            for item in baseline["bin"]
            if item["type"] == kind and item["is_default"]
        ]
        assert (
            len(binaries) == 1
            and binaries[0]["is_present"]
            and binaries[0]["version"] == "1.16.2"
        ), binaries
        selected["binary_id" if kind == "firecracker" else "jailer_binary_id"] = (
            binaries[0]["id"]
        )
    return selected


def _same_identity(
    item: dict[str, Any], receipt: dict[str, Any], fields: tuple[str, ...]
) -> None:
    assert {key: item[key] for key in fields} == {
        key: receipt[key] for key in fields
    }, (item, receipt)


def _owned_vm(runner_vm: str, receipt: dict[str, Any]) -> dict[str, Any]:
    matches = [
        item
        for item in _inventory(runner_vm, "vm")
        if item["id"] == receipt["id"] or item["name"] == receipt["name"]
    ]
    assert len(matches) == 1, matches
    _same_identity(matches[0], receipt, _VM_IDENTITY)
    return matches[0]


def _assert_live(task: tuple[int, int, int]) -> None:
    poller = select.poll()
    poller.register(task[0], select.POLLIN)
    assert poller.poll(0) == [], task


def _pin_task(vm: dict[str, Any]) -> tuple[int, int, int]:
    pid, ticks = int(vm["pid"]), int(vm["process_start_time"])
    assert pid > 0 and ticks > 0, vm
    fd = os.pidfd_open(pid)
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
        assert (
            len(fields) >= 20
            and int(fields[19]) == ticks
            and fields[0] not in ("Z", "X")
        ), (vm, fields)
        task = (fd, pid, ticks)
        _assert_live(task)
        return task
    except BaseException:
        os.close(fd)
        raise


def _assert_exited(task: tuple[int, int, int]) -> None:
    poller = select.poll()
    poller.register(task[0], select.POLLIN)
    events = poller.poll(10_000)
    assert len(events) == 1 and events[0][0] == task[0], (task, events)
    assert events[0][1] & (select.POLLIN | select.POLLHUP), events
    assert not events[0][1] & (select.POLLERR | select.POLLNVAL), events


def _wait_for_exec(runner_vm: str, vm_id: str) -> None:
    deadline = time.monotonic() + 15
    while True:
        result = _run_mvm(
            runner_vm,
            "exec",
            vm_id,
            "--timeout",
            "10",
            "--",
            "echo console-alive",
            check=False,
        )
        if result.returncode == 0:
            assert result.stdout.strip() == "console-alive", result.stdout
            return
        assert time.monotonic() < deadline, (result.stdout, result.stderr)
        time.sleep(0.5)


def _console_state(
    runner_vm: str, receipt: dict[str, Any], running: bool, selector: str | None = None
) -> dict[str, Any]:
    state = _json_success_data(
        _run_mvm(
            runner_vm, "console", selector or receipt["id"], "--state", "--json"
        ).stdout,
        dict,
    )
    inspected = _json_success_data(
        _run_mvm(runner_vm, "vm", "inspect", receipt["id"], "--json").stdout, dict
    )
    _same_identity(inspected["vm"], receipt, _VM_IDENTITY)
    assert state["running"] is running, state
    socket = (
        f"/run/mvmctl/runtime/{os.getuid()}/{receipt['id']}/console.sock"
        if running
        else ""
    )
    assert state["socket_path"] == socket, state
    assert inspected["console"] == {
        "relay_running": running,
        "relay_pid": state["pid"],
        "relay_socket_path": socket if running else None,
    }, (state, inspected["console"])
    if running:
        assert isinstance(state["pid"], int) and state["pid"] > 0, state
        assert stat.S_ISSOCK(Path(socket).lstat().st_mode), state
    else:
        assert state["pid"] is None, state
    return state


@pytest.fixture
def console_vm(runner_vm: str) -> Generator[dict[str, Any], None, None]:
    """Admit returned identities; remove only those exact VM and network IDs."""
    baseline = {domain: _inventory(runner_vm, domain) for domain in _INVENTORIES}
    selected = _assets(baseline)
    suffix = uuid.uuid4().hex[:8]
    vm_name, network_name = f"sys-console-{suffix}", f"sys-console-net-{suffix}"
    subnet = _unique_subnet(network_name)
    network_receipt = None
    receipt = None
    tasks: list[tuple[int, int, int]] = []
    files: dict[str, tuple[int, int]] = {}
    owned: dict[str, Any] = {"tasks": tasks}
    try:
        for domain, name in (("vm", vm_name), ("network", network_name)):
            assert _inventory(runner_vm, domain) == baseline[domain], domain
            assert all(item["name"] != name for item in baseline[domain]), name
        creation = _run_mvm(
            runner_vm,
            "network",
            "create",
            network_name,
            "--subnet",
            subnet,
            "--json",
            check=False,
        )
        envelope = json.loads(creation.stdout)
        assert envelope["schema_version"] == 1, envelope
        if envelope["data"] is not None:
            network = envelope["data"]
            assert (
                isinstance(network, dict)
                and network["name"] == network_name
                and network["created_at"]
            ), envelope
            assert network["id"] not in {item["id"] for item in baseline["network"]}, (
                network
            )
            network_receipt = {key: network[key] for key in _NETWORK_IDENTITY}
        assert creation.returncode == 0, (creation.stdout, creation.stderr)
        network = _json_success_data(creation.stdout, dict)
        assert network_receipt is not None and network["subnet"] == subnet, network
        args = [
            "vm",
            "create",
            vm_name,
            "--image",
            selected["image_id"],
            "--network",
            network_receipt["id"],
            "--cloud-init-mode",
            "off",
            "--skip-deblob",
            "--no-pci",
            "--console",
            "-s",
            "512M",
            "--mem",
            "256M",
            "--vcpu",
            "1",
            "--json",
        ]
        creation = _run_mvm(runner_vm, *args, check=False, timeout=180)
        envelope = json.loads(creation.stdout)
        assert envelope["schema_version"] == 1, envelope
        if envelope["data"] is not None:
            assert isinstance(envelope["data"], list) and len(envelope["data"]) <= 1, (
                envelope
            )
            for vm in envelope["data"]:
                assert vm["name"] == vm_name and vm["created_at"], vm
                assert vm["id"] not in {item["id"] for item in baseline["vm"]}, vm
                fields = (
                    *_VM_IDENTITY,
                    "pid",
                    "process_start_time",
                    "rootfs_path",
                    "config_path",
                    "cgroup_limits",
                    "network_id",
                    "ipv4",
                    "mac",
                    "tap_device",
                )
                receipt = {key: vm[key] for key in fields}
        assert creation.returncode == 0, (creation.stdout, creation.stderr)
        created = _json_success_data(creation.stdout, list)
        assert len(created) == 1 and receipt is not None, created
        vm = created[0]
        _same_identity(vm, receipt, _VM_IDENTITY)
        assert {key: vm[key] for key in selected} == selected, (vm, selected)
        assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (
            1,
            256,
            512,
        ), vm
        assert (
            vm["network_placement"] == "managed"
            and vm["network_id"] == network_receipt["id"]
        ), vm
        assert vm["ipv4"] and vm["mac"] and vm["tap_device"], vm
        assert (
            vm["pci_enabled"] is False
            and vm["enable_console"] is True
            and vm["status"] == "running"
        ), vm
        owned["current_task_locator"] = (vm["pid"], vm["process_start_time"])
        tasks.append(_pin_task(vm))
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", receipt["id"], "--json").stdout, dict
        )
        for key in ("rootfs_path", "config_path"):
            path = receipt[key]
            assert path and inspected["filesystem"][key] == path, (receipt, inspected)
            identity = Path(path).lstat()
            assert stat.S_ISREG(identity.st_mode), path
            files[path] = (identity.st_dev, identity.st_ino)
        vm_dir = inspected["filesystem"]["vm_dir"]
        directory = Path(vm_dir).lstat()
        assert stat.S_ISDIR(directory.st_mode), vm_dir
        owned.update(
            {
                "receipt": receipt,
                "network": network_receipt,
                "files": files,
                "assets": selected,
                "vm_dir": vm_dir,
                "vm_dir_identity": (directory.st_dev, directory.st_ino),
                "baseline": baseline,
                "arguments": args,
            }
        )
        _wait_for_exec(runner_vm, receipt["id"])
        print(
            f"console intent={args} receipt={json.dumps(receipt, sort_keys=True)} "
            f"network={json.dumps(network_receipt, sort_keys=True)} files={files}",
            flush=True,
        )
        yield owned
    finally:
        errors = []
        try:
            matches = [
                item
                for item in _inventory(runner_vm, "vm")
                if item["name"] == vm_name
                or (receipt is not None and item["id"] == receipt["id"])
            ]
            assert len(matches) <= 1, matches
            for vm in matches:
                assert receipt is not None, f"unreceipted VM: {matches}"
                _same_identity(vm, receipt, _VM_IDENTITY)
                assert vm["cgroup_limits"] == receipt["cgroup_limits"], (vm, receipt)
                if vm["status"] == "running":
                    expected_task = owned.get(
                        "current_task_locator",
                        (receipt["pid"], receipt["process_start_time"]),
                    )
                    assert (vm["pid"], vm["process_start_time"]) == expected_task, (
                        vm,
                        expected_task,
                    )
                    matching_tasks = [
                        task
                        for task in tasks
                        if task[1:] == (vm["pid"], vm["process_start_time"])
                    ]
                    if not matching_tasks:
                        tasks.append(_pin_task(vm))
                        matching_tasks = [tasks[-1]]
                    assert len(matching_tasks) == 1, (vm, tasks)
                    _assert_live(matching_tasks[0])
                for path, identity in files.items():
                    current = Path(path).lstat()
                    assert (current.st_dev, current.st_ino) == identity, path
                if "vm_dir" in owned:
                    directory = Path(owned["vm_dir"]).lstat()
                    assert (directory.st_dev, directory.st_ino) == owned[
                        "vm_dir_identity"
                    ], owned
                removed = _run_mvm(
                    runner_vm,
                    "vm",
                    "rm",
                    receipt["id"],
                    "--force",
                    check=False,
                    timeout=120,
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
            for task in tasks:
                _assert_exited(task)
            for path in files:
                assert not Path(path).exists(), path
            if "vm_dir" in owned:
                assert not Path(owned["vm_dir"]).exists(), owned["vm_dir"]
            if receipt is not None:
                assert not Path(
                    f"/run/mvmctl/runtime/{os.getuid()}/{receipt['id']}"
                ).exists(), receipt
            assert all(
                item["name"] != vm_name
                and (receipt is None or item["id"] != receipt["id"])
                for item in _inventory(runner_vm, "vm")
            ), receipt
        except Exception as exc:
            errors.append(f"owned VM cleanup: {exc}")
        finally:
            for task in tasks:
                os.close(task[0])
        try:
            matches = [
                item
                for item in _inventory(runner_vm, "network")
                if item["name"] == network_name
                or (network_receipt is not None and item["id"] == network_receipt["id"])
            ]
            assert len(matches) <= 1, matches
            for network in matches:
                assert network_receipt is not None, f"unreceipted network: {matches}"
                _same_identity(network, network_receipt, _NETWORK_IDENTITY)
                removed = _run_mvm(
                    runner_vm, "network", "rm", network_receipt["id"], check=False
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
            assert all(
                item["name"] != network_name
                and (network_receipt is None or item["id"] != network_receipt["id"])
                for item in _inventory(runner_vm, "network")
            ), network_receipt
        except Exception as exc:
            errors.append(f"owned network cleanup: {exc}")
        for domain, original in baseline.items():
            try:
                assert _inventory(runner_vm, domain) == original, (
                    f"{domain} inventory/config changed"
                )
            except Exception as exc:
                errors.append(f"{domain} baseline: {exc}")
        assert not errors, "; ".join(errors)


def test_console_active_relay_journey(
    runner_vm: str, console_vm: dict[str, Any]
) -> None:
    """Kill only the relay, reject inactive attach, then renew it on VM restart."""
    receipt = console_vm["receipt"]
    original_task = console_vm["tasks"][0]
    initial = _console_state(runner_vm, receipt, True)
    # Preserve the real IPv4 selector case against the same root-observed relay.
    assert _console_state(runner_vm, receipt, True, receipt["ipv4"]) == initial
    unsupported = _run_mvm(runner_vm, "console", receipt["id"], "--json", check=False)
    assert unsupported.returncode != 0, (unsupported.stdout, unsupported.stderr)
    assert (
        "structured output is not supported for mvm console"
        in unsupported.stdout + unsupported.stderr
    )
    assert _console_state(runner_vm, receipt, True) == initial

    killed = _run_mvm(
        runner_vm, "console", receipt["id"], "--kill", "--json", check=False
    )
    assert killed.returncode == 0, (killed.stdout, killed.stderr)
    envelope = json.loads(killed.stdout)
    assert (
        envelope["schema_version"] == 1
        and envelope["status"] == "success"
        and envelope["error"] is None
    ), envelope
    _console_state(runner_vm, receipt, False)
    still_running = _owned_vm(runner_vm, receipt)
    assert still_running["status"] == "running", still_running
    assert (still_running["pid"], still_running["process_start_time"]) == original_task[
        1:
    ], still_running
    _assert_live(original_task)
    _wait_for_exec(runner_vm, receipt["id"])

    repeated = _run_mvm(
        runner_vm, "console", receipt["id"], "--kill", "--json", check=False
    )
    assert repeated.returncode != 0, (repeated.stdout, repeated.stderr)
    error = json.loads(repeated.stdout)
    assert error["schema_version"] == 1 and error["status"] == "error", error
    assert (
        error["error"]["code"] == "console.not_running" and error["error"]["class"] == 1
    ), error
    _console_state(runner_vm, receipt, False)
    attached = _run_mvm(runner_vm, "console", receipt["id"], check=False)
    assert attached.returncode != 0, (attached.stdout, attached.stderr)
    assert (
        f"No console relay running for VM '{receipt['id']}'"
        in attached.stdout + attached.stderr
    )
    _console_state(runner_vm, receipt, False)

    _run_mvm(runner_vm, "vm", "stop", receipt["id"])
    _assert_exited(original_task)
    assert _owned_vm(runner_vm, receipt)["status"] == "stopped"
    _console_state(runner_vm, receipt, False)
    attached = _run_mvm(runner_vm, "console", receipt["id"], check=False)
    assert attached.returncode != 0, (attached.stdout, attached.stderr)
    assert (
        f"No console relay running for VM '{receipt['id']}'"
        in attached.stdout + attached.stderr
    )
    _console_state(runner_vm, receipt, False)

    _run_mvm(runner_vm, "vm", "start", receipt["id"])
    restarted = _owned_vm(runner_vm, receipt)
    assert restarted["status"] == "running", restarted
    console_vm["current_task_locator"] = (
        restarted["pid"],
        restarted["process_start_time"],
    )
    renewed_task = _pin_task(restarted)
    console_vm["tasks"].append(renewed_task)
    assert renewed_task[1:] != original_task[1:], (original_task, renewed_task)
    renewed = _console_state(runner_vm, receipt, True)
    assert renewed["socket_path"] == initial["socket_path"], (initial, renewed)
    assert _console_state(runner_vm, receipt, True, receipt["ipv4"]) == renewed
    # Restart may rewrite its generated config; retain the new exact file identity.
    assert (
        restarted["rootfs_path"] == receipt["rootfs_path"]
        and restarted["config_path"] == receipt["config_path"]
    ), restarted
    rootfs = Path(receipt["rootfs_path"]).lstat()
    assert (rootfs.st_dev, rootfs.st_ino) == console_vm["files"][
        receipt["rootfs_path"]
    ], restarted
    config = Path(receipt["config_path"]).lstat()
    assert stat.S_ISREG(config.st_mode), restarted
    console_vm["files"][receipt["config_path"]] = (config.st_dev, config.st_ino)
    _wait_for_exec(runner_vm, receipt["id"])
    _assert_live(renewed_task)
