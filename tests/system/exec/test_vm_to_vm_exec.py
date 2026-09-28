"""Directional VM-to-VM exec through the installed candidate and guest agents."""

from __future__ import annotations

import json
import os
import select
import time
import uuid
from contextlib import contextmanager
from pathlib import Path

import pytest

from tests.system.conftest import (
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
    pytest.mark.requires_kvm,
    pytest.mark.requires_network,
    pytest.mark.slow,
    pytest.mark.tier2,
]


def _guest_exec(runner_vm: str, vm_name: str, *command: str, check: bool = True):
    return _run_mvm(
        runner_vm,
        "exec",
        vm_name,
        "--user",
        "runner",
        "--",
        *command,
        check=check,
    )


def _remote_exec(runner_vm: str, source: str, destination: str, *command: str):
    return _run_mvm(
        runner_vm,
        "exec",
        source,
        "--user",
        "runner",
        "--allow-vm-exec",
        "--",
        "/usr/bin/mvm-agent",
        "remote",
        "--user",
        "runner",
        destination,
        "--",
        *command,
        check=False,
    )


def _wait_for_runner(runner_vm: str, vm_name: str) -> None:
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        result = _guest_exec(runner_vm, vm_name, "id", "-un", check=False)
        if result.returncode == 0 and result.stdout.strip() == "runner":
            return
        time.sleep(0.5)
    pytest.fail(f"runner account or vsock agent not ready in {vm_name}")


def _sentinel_absent(runner_vm: str, vm_name: str, path: str) -> None:
    result = _guest_exec(runner_vm, vm_name, "test", "!", "-e", path)
    assert result.returncode == 0, f"unexpected destination side effect: {path}"


def _exec_policies(runner_vm: str) -> list[dict]:
    return _json_success_data(
        _run_mvm(runner_vm, "policy", "exec", "ls", "--json").stdout, list
    )


def _cleanup_pair(
    runner_vm: str, source: str, destination: str, networks: list[str]
) -> None:
    """Remove only this case's resources and prove each is absent afterward."""
    errors: list[str] = []
    try:
        policies = [
            item
            for item in _exec_policies(runner_vm)
            if item["source_vm_name"] == source
            and item["destination_vm_name"] == destination
            and item["target_user"] == "runner"
        ]
        for item in policies:
            _run_mvm(runner_vm, "policy", "exec", "rm", item["id"], "--force")
        if any(
            item["id"] in {policy["id"] for policy in policies}
            for item in _exec_policies(runner_vm)
        ):
            errors.append("execution policy remains")
    except Exception as exc:
        errors.append(f"execution policy cleanup: {exc}")

    for vm_name in (destination, source):
        try:
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if any(item["name"] == vm_name for item in listed):
                _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", timeout=120)
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if any(item["name"] == vm_name for item in listed):
                errors.append(f"VM remains: {vm_name}")
        except Exception as exc:
            errors.append(f"VM cleanup {vm_name}: {exc}")

    for name in reversed(networks):
        try:
            listed = _json_success_data(
                _run_mvm(runner_vm, "network", "ls", "--json").stdout, list
            )
            if any(item["name"] == name for item in listed):
                _run_mvm(runner_vm, "network", "rm", name, timeout=60)
            listed = _json_success_data(
                _run_mvm(runner_vm, "network", "ls", "--json").stdout, list
            )
            if any(item["name"] == name for item in listed):
                errors.append(f"network remains: {name}")
        except Exception as exc:
            errors.append(f"network cleanup {name}: {exc}")

    assert not errors, "; ".join(errors)


def _exercise_placement(runner_vm: str, placement: str) -> None:
    suffix = uuid.uuid4().hex[:8]
    source, destination = f"exec-src-{suffix}", f"exec-dst-{suffix}"
    networks: list[str] = []

    try:
        if placement != "networkless":
            count = 1 if placement == "same" else 2
            for index in range(count):
                name = f"exec-net-{index}-{suffix}"
                networks.append(name)
                _run_mvm(
                    runner_vm,
                    "network",
                    "create",
                    name,
                    "--subnet",
                    _unique_subnet(name),
                )

        for vm_name, network in (
            (source, networks[0] if networks else None),
            (destination, networks[-1] if networks else None),
        ):
            placement_args = ["--network", network] if network else ["--no-network"]
            created = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "vm",
                    "create",
                    vm_name,
                    "--image",
                    "alpine:3.23",
                    *placement_args,
                    "--cloud-init-mode",
                    "off" if network is None else "inject",
                    "--skip-deblob",
                    "--no-pci",
                    "--disk-size",
                    "512M",
                    "--mem",
                    "256M",
                    "--vcpu",
                    "1",
                    "--user",
                    "runner",
                    "--json",
                    timeout=180,
                ).stdout,
                list,
            )
            assert len(created) == 1 and created[0]["name"] == vm_name
            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            assert inspected["vm"]["pci_enabled"] is False
            assert inspected["resources"]["vcpu"] == 1
            assert inspected["resources"]["mem"] == 256
            assert inspected["resources"]["disk"] == 512
            if network is None:
                assert inspected["networking"].get("network") is None
                assert inspected["networking"]["ipv4"] == ""
                assert inspected["networking"]["mac"] == ""
                assert inspected["networking"]["tap_device"] == ""
            else:
                assert inspected["networking"]["network"]["name"] == network
            _wait_for_runner(runner_vm, vm_name)

        before = f"/tmp/exec-denied-{suffix}"
        _sentinel_absent(runner_vm, destination, before)
        denied = _remote_exec(runner_vm, source, destination, "touch", before)
        assert denied.returncode != 0, (denied.stdout, denied.stderr)
        _sentinel_absent(runner_vm, destination, before)

        policy = _json_success_data(
            _run_mvm(
                runner_vm,
                "policy",
                "exec",
                "create",
                source,
                destination,
                "runner",
                "--json",
            ).stdout,
            dict,
        )
        assert policy["source_vm_name"] == source
        assert policy["destination_vm_name"] == destination
        assert policy["target_user"] == "runner"
        assert any(item["id"] == policy["id"] for item in _exec_policies(runner_vm))

        identity = _remote_exec(runner_vm, source, destination, "id", "-un")
        assert identity.returncode == 0, (identity.stdout, identity.stderr)
        assert identity.stdout.strip() == "runner"
        allowed_path = f"/tmp/exec-allowed-{suffix}"
        allowed = _remote_exec(runner_vm, source, destination, "touch", allowed_path)
        assert allowed.returncode == 0, (allowed.stdout, allowed.stderr)
        assert (
            _guest_exec(runner_vm, destination, "test", "-e", allowed_path).returncode
            == 0
        )

        reverse_path = f"/tmp/exec-reverse-{suffix}"
        _sentinel_absent(runner_vm, source, reverse_path)
        reverse = _remote_exec(runner_vm, destination, source, "touch", reverse_path)
        assert reverse.returncode != 0, (reverse.stdout, reverse.stderr)
        _sentinel_absent(runner_vm, source, reverse_path)

        _run_mvm(runner_vm, "policy", "exec", "rm", policy["id"], "--force")
        assert all(item["id"] != policy["id"] for item in _exec_policies(runner_vm))
        revoked_path = f"/tmp/exec-revoked-{suffix}"
        _sentinel_absent(runner_vm, destination, revoked_path)
        revoked = _remote_exec(runner_vm, source, destination, "touch", revoked_path)
        assert revoked.returncode != 0, (revoked.stdout, revoked.stderr)
        _sentinel_absent(runner_vm, destination, revoked_path)
    finally:
        _cleanup_pair(runner_vm, source, destination, networks)


def test_directional_vm_to_vm_exec_policy(runner_vm: str) -> None:
    """Same-network, cross-network, and networkless grants stay directional."""
    ensure_vm_deps(runner_vm)
    for placement in ("same", "cross", "networkless"):
        _exercise_placement(runner_vm, placement)


def _require_exec_lifecycle_assets(runner_vm: str) -> None:
    """Fail before creation when the runner needs an asset download."""
    mirror = os.environ.get("MVM_ASSET_MIRROR", "")
    assert mirror and Path(mirror).is_dir(), "a preseeded asset mirror is required"
    binaries = _json_success_data(
        _run_mvm(runner_vm, "bin", "ls", "--json").stdout, list
    )
    for kind in ("firecracker", "jailer"):
        assert any(
            item["type"] == kind
            and item["version"] == "1.16.2"
            and item["is_present"]
            and item["is_default"]
            for item in binaries
        ), f"preseeded default {kind} 1.16.2 required"
    kernels = _json_success_data(
        _run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list
    )
    assert any(item["is_present"] and item["is_default"] for item in kernels)
    images = _json_success_data(
        _run_mvm(runner_vm, "image", "ls", "--json").stdout, list
    )
    assert any(
        item["type"] == "alpine"
        and item["version"] == "3.23"
        and item["is_present"]
        for item in images
    ), "preseeded alpine:3.23 required"


def _exec_lifecycle_state(runner_vm: str, name: str, vm_id: str, status: str) -> dict:
    matches = [
        item for item in _parse_vm_list(
            _run_mvm(runner_vm, "vm", "ls", "--json").stdout
        ) if item["name"] == name
    ]
    assert len(matches) == 1 and matches[0]["id"] == vm_id, matches
    vm = matches[0]
    inspected = _json_success_data(
        _run_mvm(runner_vm, "vm", "inspect", vm_id, "--json").stdout, dict
    )["vm"]
    assert vm["status"] == inspected["status"] == status, (vm, inspected)
    assert inspected["id"] == vm_id
    if status == "running":
        assert inspected["pid"] == vm["pid"], (vm, inspected)
    return vm


def _exec_lifecycle_identity(runner_vm: str, name: str, vm_id: str) -> dict:
    _wait_for_runner(runner_vm, name)
    vm = _exec_lifecycle_state(runner_vm, name, vm_id, "running")
    pid, ticks = int(vm["pid"]), int(vm["process_start_time"])
    assert pid > 0 and ticks > 0, vm
    fields = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
    assert len(fields) >= 20 and int(fields[19]) == ticks, (pid, ticks, fields)
    assert fields[0] not in ("Z", "X"), fields
    boot_id = _guest_exec(
        runner_vm, name, "cat", "/proc/sys/kernel/random/boot_id"
    ).stdout.strip()
    assert str(uuid.UUID(boot_id)) == boot_id, boot_id
    return {"vm_id": vm_id, "pid": pid, "start_ticks": ticks, "boot_id": boot_id}


@contextmanager
def _exec_lifecycle_task(identity: dict):
    """Retain the original process across a typed lifecycle operation."""
    pidfd = os.pidfd_open(identity["pid"])
    try:
        fields = Path(f"/proc/{identity['pid']}/stat").read_text().rpartition(") ")[2].split()
        assert len(fields) >= 20 and int(fields[19]) == identity["start_ticks"]
        poller = select.poll()
        poller.register(pidfd, select.POLLIN)
        assert poller.poll(0) == [], identity
        yield poller
        events = poller.poll(10_000)
        assert len(events) == 1 and events[0][0] == pidfd, (identity, events)
        assert events[0][1] & (select.POLLIN | select.POLLHUP), events
        assert not events[0][1] & (select.POLLERR | select.POLLNVAL), events
    finally:
        os.close(pidfd)


def _exec_lifecycle_grant(runner_vm: str, policy: dict, stage: str) -> None:
    current = [item for item in _exec_policies(runner_vm) if item["id"] == policy["id"]]
    assert current == [policy], (stage, policy, current)
    print(f"exec lifecycle {stage} policy={json.dumps(current, sort_keys=True)}", flush=True)


def _exec_lifecycle_remote_effect(
    runner_vm: str, source: str, destination: str, path: str, marker: str
) -> None:
    from shlex import join, quote

    destination_command = join([
        "sh", "-c",
        'test "$(id -un)" = runner && printf %s "$2" > "$1" && cat "$1"',
        "sh", path, marker,
    ])
    # The source shell must pass one complete command to remote's second argv join.
    result = _remote_exec(
        runner_vm, source, destination, quote(destination_command),
    )
    assert result.returncode == 0, (result.stdout, result.stderr)
    assert result.stdout == marker, (result.stdout, result.stderr)
    assert _guest_exec(runner_vm, destination, "cat", path).stdout == marker


@pytest.mark.timeout(900)
def test_vm_to_vm_exec_current_peers_after_restart_and_reboot(runner_vm: str) -> None:
    """One durable grant admits fresh source/destination tasks and guest boots."""
    _require_exec_lifecycle_assets(runner_vm)
    suffix = uuid.uuid4().hex[:8]
    source, destination = f"exec-life-src-{suffix}", f"exec-life-dst-{suffix}"
    identities: dict[str, dict] = {}
    vm_ids: dict[str, str] = {}
    effect_path = f"/home/runner/exec-life-effect-{suffix}"
    stopped_path = f"/home/runner/exec-life-stopped-{suffix}"
    revoked_path = f"/home/runner/exec-life-revoked-{suffix}"
    try:
        for name in (source, destination):
            created = _json_success_data(
                _run_mvm(
                    runner_vm, "vm", "create", name, "--image", "alpine:3.23",
                    "--no-network", "--cloud-init-mode", "off", "--skip-deblob",
                    "--no-pci", "--disk-size", "512M", "--mem", "256M",
                    "--vcpu", "1", "--user", "runner", "--json", timeout=180,
                ).stdout, list,
            )
            assert len(created) == 1 and created[0]["name"] == name, created
            vm_ids[name] = created[0]["id"]
            assert (created[0]["vcpu_count"], created[0]["mem_size_mib"],
                    created[0]["disk_size_mib"]) == (1, 256, 512)
            assert created[0]["network_placement"] == "networkless", created
            assert created[0]["ipv4"] == "" and created[0]["mac"] == "", created
            identities[name] = _exec_lifecycle_identity(runner_vm, name, vm_ids[name])

        policy = _json_success_data(
            _run_mvm(
                runner_vm, "policy", "exec", "create", source, destination,
                "runner", "--json",
            ).stdout, dict,
        )
        assert policy["source_vm_id"] == vm_ids[source]
        assert policy["destination_vm_id"] == vm_ids[destination]
        assert policy["target_user"] == "runner"
        _exec_lifecycle_grant(runner_vm, policy, "initial")
        print(f"exec lifecycle initial identities={json.dumps(identities, sort_keys=True)}", flush=True)
        _exec_lifecycle_remote_effect(runner_vm, source, destination, effect_path, f"initial-{suffix}")
        _sentinel_absent(runner_vm, destination, stopped_path)

        # Restart each exact endpoint once. No VM or grant is recreated here.
        for name in (destination, source):
            previous = identities[name]
            with _exec_lifecycle_task(previous):
                _run_mvm(runner_vm, "vm", "stop", vm_ids[name], timeout=60)
                _exec_lifecycle_state(runner_vm, name, vm_ids[name], "stopped")
            _exec_lifecycle_grant(runner_vm, policy, f"{name}-stopped")
            denied = _remote_exec(runner_vm, source, destination, "touch", stopped_path)
            assert denied.returncode != 0, (denied.stdout, denied.stderr)
            _run_mvm(runner_vm, "vm", "start", vm_ids[name], timeout=60)
            current = _exec_lifecycle_identity(runner_vm, name, vm_ids[name])
            print(f"exec lifecycle restart old={json.dumps(previous, sort_keys=True)} "
                  f"new={json.dumps(current, sort_keys=True)}", flush=True)
            assert current["vm_id"] == previous["vm_id"]
            assert (current["pid"], current["start_ticks"]) != (previous["pid"], previous["start_ticks"])
            assert current["boot_id"] != previous["boot_id"]
            identities[name] = current
            _exec_lifecycle_grant(runner_vm, policy, f"{name}-started")
            _sentinel_absent(runner_vm, destination, stopped_path)
            _exec_lifecycle_remote_effect(runner_vm, source, destination, effect_path, f"{name}-started")

        previous = identities[destination]
        with _exec_lifecycle_task(previous):
            _run_mvm(runner_vm, "vm", "reboot", vm_ids[destination], timeout=60)
        current = _exec_lifecycle_identity(runner_vm, destination, vm_ids[destination])
        print(f"exec lifecycle reboot old={json.dumps(previous, sort_keys=True)} "
              f"new={json.dumps(current, sort_keys=True)}", flush=True)
        assert current["vm_id"] == previous["vm_id"]
        assert (current["pid"], current["start_ticks"]) != (previous["pid"], previous["start_ticks"])
        assert current["boot_id"] != previous["boot_id"]
        _exec_lifecycle_grant(runner_vm, policy, "destination-rebooted")
        _exec_lifecycle_remote_effect(runner_vm, source, destination, effect_path, f"rebooted-{suffix}")

        _sentinel_absent(runner_vm, destination, revoked_path)
        _run_mvm(runner_vm, "policy", "exec", "rm", policy["id"], "--force")
        assert all(item["id"] != policy["id"] for item in _exec_policies(runner_vm))
        denied = _remote_exec(runner_vm, source, destination, "touch", revoked_path)
        assert denied.returncode != 0, (denied.stdout, denied.stderr)
        _sentinel_absent(runner_vm, destination, revoked_path)
    finally:
        _cleanup_pair(runner_vm, source, destination, [])
