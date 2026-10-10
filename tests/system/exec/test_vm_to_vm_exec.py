"""Directional VM-to-VM exec through the installed candidate and guest agents."""

from __future__ import annotations

import json
import os
import select
import signal
import stat
import subprocess
import time
import uuid
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from shlex import join, quote

import pytest

from tests.system.conftest import (
    SYSTEM_MVM_BINARY,
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
    _unique_subnet,
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
    result = None
    while time.monotonic() < deadline:
        result = _guest_exec(runner_vm, vm_name, "id", "-un", check=False)
        if result.returncode == 0 and result.stdout.strip() == "runner":
            return
        time.sleep(0.5)
    last_attempt = None if result is None else {
        "returncode": result.returncode,
        "stdout": result.stdout,
        "stderr": result.stderr,
    }
    print(f"last runner readiness exec in {vm_name}: {last_attempt!r}", flush=True)
    pytest.fail(f"runner account or vsock agent not ready in {vm_name}; last exec: {last_attempt!r}")


def _sentinel_absent(runner_vm: str, vm_name: str, path: str) -> None:
    result = _guest_exec(runner_vm, vm_name, "test", "!", "-e", path)
    assert result.returncode == 0, f"unexpected destination side effect: {path}"


def _exec_policies(runner_vm: str) -> list[dict]:
    return _json_success_data(
        _run_mvm(runner_vm, "policy", "exec", "ls", "--json").stdout, list
    )


def _pair_inventory(runner_vm: str, domain: str):
    args = {"exec_policy": ("policy", "exec"), "traffic_policy": ("policy", "traffic")}.get(domain, (domain,))
    expected = dict if domain in ("config", "traffic_policy") else list
    data = _json_success_data(_run_mvm(runner_vm, *args, "ls", "--json").stdout, expected)
    return sorted(data, key=lambda item: json.dumps(item, sort_keys=True)) if isinstance(data, list) else data


def _pair_owner(runner_vm: str) -> dict:
    """Admit mirrored assets and retain unrelated records before either old journey."""
    _require_exec_lifecycle_assets(runner_vm)
    baseline = {domain: _pair_inventory(runner_vm, domain) for domain in (
        "image", "kernel", "bin", "vm", "network", "key", "volume", "snapshot", "env", "exec_policy", "traffic_policy", "config",
    )}
    images = [item for item in baseline["image"] if item["is_present"]
              and (item["type"], item["version"]) == ("alpine", "3.23")]
    assert len(images) == 1, images
    kernels = [item for item in baseline["kernel"] if item["is_present"] and item["is_default"]]
    assert len(kernels) == 1, kernels
    assets = {"image_id": images[0]["id"], "kernel_id": kernels[0]["id"]}
    for kind in ("firecracker", "jailer"):
        binaries = [item for item in baseline["bin"] if item["type"] == kind and item["is_present"]
                    and item["is_default"] and item["version"] == "1.17.0"]
        assert len(binaries) == 1, binaries
        assets["binary_id" if kind == "firecracker" else "jailer_binary_id"] = binaries[0]["id"]
    return {"baseline": baseline, "image_id": images[0]["id"], "assets": assets, "vms": {}, "vm_intents": [],
            "vm_files": {}, "runtime": {}, "networks": {}, "network_intents": {},
            "policy_ids": set(), "policies": {}}


def _pair_create_vm(runner_vm: str, owned: dict, name: str, args: list[str]) -> list[dict]:
    owned["vms"][name] = {}
    before_ids = _extra_vm_creation_intent(runner_vm, owned, name, args)
    result = _run_mvm(runner_vm, *args, check=False, timeout=180)
    _extra_retain_vm_receipts(result, owned, name, before_ids)
    for vm in json.loads(result.stdout).get("data") or []:
        assert vm["id"] in owned["vms"][name], vm
        owned["vm_files"][vm["id"]] = {key: vm[key] for key in ("config_path", "rootfs_path")}
        owned["runtime"][name] = {"vm_id": vm["id"], "pid": vm["pid"], "start_ticks": vm["process_start_time"]}
        assert {key: vm[key] for key in owned["assets"]} == owned["assets"], (vm, owned["assets"])
    assert result.returncode == 0, (result.stdout, result.stderr)
    return _json_success_data(result.stdout, list)


def _pair_identity(runner_vm: str, owned: dict, name: str, vm_id: str) -> dict:
    identity = _exec_lifecycle_identity(runner_vm, name, vm_id)
    owned["runtime"][name] = identity
    return identity


def _pair_create_network(runner_vm: str, owned: dict, name: str) -> dict:
    before = _pair_inventory(runner_vm, "network")
    subnet = _unique_subnet(name)
    assert all(item["name"] != name and item["subnet"] != subnet for item in before), (name, subnet, before)
    args = ["network", "create", name, "--subnet", subnet, "--json"]
    owned["networks"][name] = {}
    owned["network_intents"][name] = {"arguments": args[:], "before_ids": {item["id"] for item in before}}
    result = _run_mvm(runner_vm, *args, check=False)
    envelope = json.loads(result.stdout)
    assert envelope["schema_version"] == 1 and envelope["status"] in ("success", "partial", "error"), envelope
    if envelope["data"] is not None:
        item = envelope["data"]
        assert isinstance(item, dict) and item["name"] == name and item["subnet"] == subnet, item
        assert item["id"] not in owned["network_intents"][name]["before_ids"] and item["created_at"], item
        owned["networks"][name][item["id"]] = {key: item[key] for key in (
            "id", "name", "created_at", "bridge", "subnet", "ipv4_gateway", "nat_enabled",
        )}
    assert result.returncode == 0, (result.stdout, result.stderr)
    return _json_success_data(result.stdout, dict)


def _pair_create_policy(runner_vm: str, owned: dict) -> dict:
    policy = _extra_exec_grant(runner_vm, owned)
    owned["policies"][policy["id"]] = policy
    return policy


def _cleanup_pair(runner_vm: str, owned: dict) -> None:
    """Reject replacements; remove only returned exact identities and current admitted tasks."""
    errors: list[str] = []
    try:
        for policy in _exec_policies(runner_vm):
            if policy["id"] in owned["policy_ids"]:
                assert policy == owned["policies"].get(policy["id"]), (policy, owned["policies"])
                _run_mvm(runner_vm, "policy", "exec", "rm", policy["id"], "--force")
        assert all(item["id"] not in owned["policy_ids"] for item in _exec_policies(runner_vm))
    except Exception as exc:
        errors.append(f"execution policy cleanup: {exc}")
    for name, receipts in reversed(list(owned["vms"].items())):
        try:
            listed = _pair_inventory(runner_vm, "vm")
            matches = [item for item in listed if item["name"] == name or item["id"] in receipts]
            assert len(matches) <= 1 and all(item["id"] in receipts for item in matches), matches
            for vm in matches:
                assert {key: vm[key] for key in receipts[vm["id"]]} == receipts[vm["id"]], vm
                assert {key: vm[key] for key in owned["vm_files"][vm["id"]]} == owned["vm_files"][vm["id"]], vm
                if vm["status"] in ("running", "paused"):
                    identity = owned["runtime"][name]
                    assert (vm["pid"], vm["process_start_time"]) == (identity["pid"], identity["start_ticks"]), (vm, identity)
                    with _exec_lifecycle_task(identity):
                        _run_mvm(runner_vm, "vm", "rm", vm["id"], "--force", timeout=120)
                else:
                    _run_mvm(runner_vm, "vm", "rm", vm["id"], "--force", timeout=120)
            assert all(item["name"] != name and item["id"] not in receipts for item in _pair_inventory(runner_vm, "vm"))
        except Exception as exc:
            errors.append(f"VM cleanup {name}: {exc}")
    for name, receipts in reversed(list(owned["networks"].items())):
        try:
            matches = [item for item in _pair_inventory(runner_vm, "network")
                       if item["name"] == name or item["id"] in receipts]
            assert len(matches) <= 1 and all(item["id"] in receipts for item in matches), matches
            for item in matches:
                assert {key: item[key] for key in receipts[item["id"]]} == receipts[item["id"]], item
                _run_mvm(runner_vm, "network", "rm", item["id"], timeout=60)
            assert all(item["name"] != name and item["id"] not in receipts for item in _pair_inventory(runner_vm, "network"))
        except Exception as exc:
            errors.append(f"network cleanup {name}: {exc}")
    for domain, baseline in owned["baseline"].items():
        try:
            assert _pair_inventory(runner_vm, domain) == baseline, f"{domain} baseline changed"
        except Exception as exc:
            errors.append(f"{domain} baseline: {exc}")
    assert not errors, "; ".join(errors)


def _exercise_placement(runner_vm: str, placement: str) -> None:
    suffix = uuid.uuid4().hex[:8]
    source, destination = f"exec-src-{suffix}", f"exec-dst-{suffix}"
    networks: list[str] = []
    owned = _pair_owner(runner_vm)

    try:
        if placement != "networkless":
            count = 1 if placement == "same" else 2
            for index in range(count):
                name = f"exec-net-{index}-{suffix}"
                networks.append(name)
                _pair_create_network(runner_vm, owned, name)

        for vm_name, network in (
            (source, networks[0] if networks else None),
            (destination, networks[-1] if networks else None),
        ):
            placement_args = ["--network", next(iter(owned["networks"][network]))] if network else ["--no-network"]
            created = _pair_create_vm(
                runner_vm, owned, vm_name, [
                    "vm",
                    "create",
                    vm_name,
                    "--image",
                    owned["image_id"],
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
                ],
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
            _pair_identity(runner_vm, owned, vm_name, created[0]["id"])

        owned["source_id"] = next(iter(owned["vms"][source]))
        owned["destination_id"] = next(iter(owned["vms"][destination]))

        before = f"/tmp/exec-denied-{suffix}"
        _sentinel_absent(runner_vm, destination, before)
        denied = _remote_exec(runner_vm, source, destination, "touch", before)
        assert denied.returncode != 0, (denied.stdout, denied.stderr)
        _sentinel_absent(runner_vm, destination, before)

        policy = _pair_create_policy(runner_vm, owned)
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
        _cleanup_pair(runner_vm, owned)


def test_directional_vm_to_vm_exec_policy(runner_vm: str) -> None:
    """Same-network, cross-network, and networkless grants stay directional."""
    _require_exec_lifecycle_assets(runner_vm)
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
            and item["version"] == "1.17.0"
            and item["is_present"]
            and item["is_default"]
            for item in binaries
        ), f"preseeded default {kind} 1.17.0 required"
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
    owned = _pair_owner(runner_vm)
    try:
        for name in (source, destination):
            created = _pair_create_vm(
                runner_vm, owned, name, ["vm", "create", name, "--image", owned["image_id"],
                    "--no-network", "--cloud-init-mode", "off", "--skip-deblob",
                    "--no-pci", "--disk-size", "512M", "--mem", "256M",
                    "--vcpu", "1", "--user", "runner", "--json"],
            )
            assert len(created) == 1 and created[0]["name"] == name, created
            vm_ids[name] = created[0]["id"]
            assert (created[0]["vcpu_count"], created[0]["mem_size_mib"],
                    created[0]["disk_size_mib"]) == (1, 256, 512)
            assert created[0]["network_placement"] == "networkless", created
            assert created[0]["ipv4"] == "" and created[0]["mac"] == "", created
            identities[name] = _pair_identity(runner_vm, owned, name, vm_ids[name])

        owned["source_id"], owned["destination_id"] = vm_ids[source], vm_ids[destination]
        policy = _pair_create_policy(runner_vm, owned)
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
            current = _pair_identity(runner_vm, owned, name, vm_ids[name])
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
        current = _pair_identity(runner_vm, owned, destination, vm_ids[destination])
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
        _cleanup_pair(runner_vm, owned)


def _exec_case_baseline(runner_vm: str) -> dict:
    """Observe operator defaults without running the dependency-mutating helper."""
    baseline = {}
    for domain in ("image", "kernel", "bin", "network"):
        items = _json_success_data(
            _run_mvm(runner_vm, domain, "ls", "--json").stdout, list,
        )
        baseline[domain] = sorted(item["id"] for item in items if item["is_default"])
    for category in ("settings", "defaults.vm"):
        baseline[category] = _json_success_data(
            _run_mvm(runner_vm, "config", "get", category, "--json").stdout, dict,
        )
    return baseline


def _extra_vm_creation_intent(runner_vm: str, owned: dict, name: str, args: list[str]) -> set[str]:
    before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
    assert all(item["name"] != name for item in before), f"creation name already exists: {name}"
    before_ids = {item["id"] for item in before}
    owned["vm_intents"].append({"name": name, "arguments": args[:], "before_ids": before_ids})
    return before_ids


def _extra_retain_vm_receipts(result, owned: dict, name: str, before_ids: set[str]) -> None:
    """Retain only creation-returned identities, including a partial JSON result."""
    envelope = json.loads(result.stdout)
    assert envelope["schema_version"] == 1 and envelope["status"] in ("success", "partial", "error"), envelope
    if envelope["data"] is None:
        return
    assert isinstance(envelope["data"], list) and len(envelope["data"]) <= 1, envelope
    for vm in envelope["data"]:
        assert vm["name"] == name and vm["id"] not in before_ids and vm["created_at"], vm
        receipt = {key: vm[key] for key in ("id", "name", "created_at")}
        previous = owned["vms"][name].get(vm["id"])
        assert previous is None or previous == receipt, (previous, receipt)
        owned["vms"][name][vm["id"]] = receipt


@pytest.fixture
def owned_exec_pair(runner_vm: str):
    """Own at most two tiny guests, exact policy IDs, and optional capture IDs."""
    _require_exec_lifecycle_assets(runner_vm)
    baseline = _exec_case_baseline(runner_vm)
    suffix = uuid.uuid4().hex[:8]
    owned = {
        "suffix": suffix, "source": f"exec-extra-src-{suffix}",
        "destination": f"exec-extra-dst-{suffix}", "vms": {},
        "policy_ids": set(), "snapshot_ids": set(), "snapshot_names": set(),
        "vm_intents": [], "snapshot_intents": {}, "snapshot_receipts": {},
    }
    try:
        for name in (owned["source"], owned["destination"]):
            owned["vms"][name] = {}
            args = ["vm", "create", name, "--image", "alpine:3.23",
                "--no-network", "--cloud-init-mode", "off", "--skip-deblob",
                "--no-pci", "--disk-size", "512M", "--mem", "256M",
                "--vcpu", "1", "--user", "runner", "--json"]
            before_ids = _extra_vm_creation_intent(runner_vm, owned, name, args)
            result = _run_mvm(runner_vm, *args, check=False, timeout=180)
            _extra_retain_vm_receipts(result, owned, name, before_ids)
            assert result.returncode == 0, (result.stdout, result.stderr)
            created = _json_success_data(result.stdout, list)
            assert len(created) == 1 and created[0]["name"] == name, created
            vm = created[0]
            assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (1, 256, 512)
            assert vm["network_placement"] == "networkless" and vm["network_id"] == "", vm
            assert vm["ipv4"] == vm["mac"] == vm["tap_device"] == "", vm
            assert vm["pci_enabled"] is False, vm
            for domain, field in (("image", "image_id"), ("kernel", "kernel_id"),
                                  ("bin", "binary_id"), ("bin", "jailer_binary_id")):
                assets = _json_success_data(
                    _run_mvm(runner_vm, domain, "ls", "--json").stdout, list,
                )
                selected = [item for item in assets if item["id"] == vm[field]]
                assert len(selected) == 1 and selected[0]["is_present"], (field, selected)
                if domain == "image":
                    assert (selected[0]["type"], selected[0]["version"]) == ("alpine", "3.23")
                if domain == "bin":
                    assert selected[0]["version"] == "1.17.0", selected
                print(f"exec extra asset vm={vm['id']} field={field} "
                      f"id={selected[0]['id']} version={selected[0]['version']}", flush=True)
            _exec_lifecycle_identity(runner_vm, name, vm["id"])
        owned["source_id"] = next(iter(owned["vms"][owned["source"]]))
        owned["destination_id"] = next(iter(owned["vms"][owned["destination"]]))
        print(f"exec extra resources source={owned['source_id']} "
              f"destination={owned['destination_id']} defaults={json.dumps(baseline, sort_keys=True)}",
              flush=True)
        yield owned
    finally:
        errors = []
        try:
            # A partial policy publication is still owned by these exact endpoints.
            ids = {vm_id for values in owned["vms"].values() for vm_id in values}
            for policy in _exec_policies(runner_vm):
                if policy["source_vm_id"] in ids and policy["destination_vm_id"] in ids:
                    owned["policy_ids"].add(policy["id"])
            for policy_id in sorted(owned["policy_ids"]):
                if any(item["id"] == policy_id for item in _exec_policies(runner_vm)):
                    _run_mvm(runner_vm, "policy", "exec", "rm", policy_id, "--force")
            assert all(item["id"] not in owned["policy_ids"] for item in _exec_policies(runner_vm))
        except Exception as exc:
            errors.append(f"policy cleanup: {exc}")
        for name, receipts in reversed(list(owned["vms"].items())):
            try:
                listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
                matches = [item for item in listed if item["name"] == name or item["id"] in receipts]
                assert all(item["id"] in receipts for item in matches), f"unreceipted VM at owned name: {matches}"
                for vm in matches:
                    assert {key: vm[key] for key in ("id", "name", "created_at")} == receipts[vm["id"]], vm
                    if vm["status"] == "running":
                        identity = {"vm_id": vm["id"], "pid": int(vm["pid"]),
                                    "start_ticks": int(vm["process_start_time"])}
                        with _exec_lifecycle_task(identity):
                            _run_mvm(runner_vm, "vm", "rm", vm["id"], "--force", timeout=120)
                    else:
                        _run_mvm(runner_vm, "vm", "rm", vm["id"], "--force", timeout=120)
                listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
                assert all(item["id"] not in receipts and item["name"] != name for item in listed)
            except Exception as exc:
                errors.append(f"VM cleanup {name}: {exc}")
        try:
            listed = _json_success_data(_run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list)
            assert all(item["id"] in owned["snapshot_ids"] for item in listed
                       if item["name"] in owned["snapshot_names"]), "unreceipted snapshot at owned name"
            for snapshot_id in sorted(owned["snapshot_ids"]):
                matches = [item for item in listed if item["id"] == snapshot_id]
                assert len(matches) <= 1, matches
                if matches:
                    receipt = owned["snapshot_receipts"].get(snapshot_id)
                    assert receipt is not None, f"published snapshot lacks creation metadata: {matches}"
                    assert {key: matches[0][key] for key in receipt} == receipt, (matches, receipt)
                else:
                    assert any(intent.get("request_id") == snapshot_id
                               for intent in owned["snapshot_intents"].values()), "no capture request receipt"
                _run_mvm(runner_vm, "snapshot", "rm", snapshot_id, "--force", timeout=120)
            listed = _json_success_data(_run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list)
            assert all(item["id"] not in owned["snapshot_ids"]
                       and item["name"] not in owned["snapshot_names"] for item in listed)
        except Exception as exc:
            errors.append(f"snapshot cleanup: {exc}")
        try:
            assert _exec_case_baseline(runner_vm) == baseline, "operator defaults changed"
        except Exception as exc:
            errors.append(f"defaults baseline: {exc}")
        assert not errors, "; ".join(errors)


def _extra_exec_grant(runner_vm: str, owned: dict, user: str = "runner") -> dict:
    policy = _json_success_data(_run_mvm(
        runner_vm, "policy", "exec", "create", owned["source_id"], owned["destination_id"],
        user, "--json",
    ).stdout, dict)
    owned["policy_ids"].add(policy["id"])
    assert (policy["source_vm_id"], policy["destination_vm_id"], policy["target_user"]) == (
        owned["source_id"], owned["destination_id"], user,
    ), policy
    return policy


def _extra_remote_args(owned: dict, command: str, *, user: str = "runner", opt_in: bool = True):
    return ["exec", owned["source_id"], "--user", "runner",
            *(["--allow-vm-exec"] if opt_in else []), "--", "/usr/bin/mvm-agent", "remote",
            "--user", user, owned["destination_id"], "--", quote(command)]


def test_vm_to_vm_exec_requires_opt_in_and_exact_nonroot_user(runner_vm: str, owned_exec_pair: dict):
    """An existing runner grant does not admit ordinary exec or another account."""
    owned = owned_exec_pair
    policy = _extra_exec_grant(runner_vm, owned)
    for user, opt_in in (("runner", False), ("root", True), ("nobody", True)):
        path = f"/tmp/exec-extra-denied-{owned['suffix']}-{user}-{int(opt_in)}"
        _sentinel_absent(runner_vm, owned["destination_id"], path)
        denied = _run_mvm(runner_vm, *_extra_remote_args(
            owned, join(["touch", path]), user=user, opt_in=opt_in,
        ), check=False)
        assert denied.returncode != 0, (user, opt_in, denied.stdout, denied.stderr)
        _sentinel_absent(runner_vm, owned["destination_id"], path)
    assert [item for item in _exec_policies(runner_vm) if item["id"] == policy["id"]] == [policy]
    allowed = _run_mvm(runner_vm, *_extra_remote_args(owned, "id -un"))
    assert allowed.stdout.strip() == "runner", (allowed.stdout, allowed.stderr)


@contextmanager
def _extra_audit_reader(runner_vm: str, owned: dict):
    """Retain the documented user-owned audit output, without changing old bytes."""
    # An unclaimed opt-in creates the sink without inventing an audit request.
    _run_mvm(runner_vm, "exec", owned["source_id"], "--user", "runner", "--allow-vm-exec", "--", "exit 0")
    path = Path(os.environ.get("MVM_CACHE_DIR", str(Path.home() / ".cache" / "mvmctl"))) / "vm-exec-audit.jsonl"
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        identity = os.fstat(fd)
        assert stat.S_ISREG(identity.st_mode) and identity.st_uid == os.geteuid(), identity
        os.lseek(fd, 0, os.SEEK_END)

        def records():
            current = os.stat(path, follow_symlinks=False)
            assert (current.st_dev, current.st_ino, current.st_uid) == (
                identity.st_dev, identity.st_ino, identity.st_uid,
            ), "audit output identity changed"
            data = bytearray()
            while chunk := os.read(fd, 65536):
                data.extend(chunk)
            for current in (os.fstat(fd), os.stat(path, follow_symlinks=False)):
                assert (current.st_dev, current.st_ino, current.st_uid) == (
                    identity.st_dev, identity.st_ino, identity.st_uid,
                ), "audit output identity changed during read"
            assert not data or data.endswith(b"\n"), "incomplete audit output"
            rows = [json.loads(line) for line in data.splitlines()]
            return [row for row in rows if row.get("source_vm_id") == owned["source_id"]]

        yield records
    finally:
        os.close(fd)


def _extra_audit_assert(row: dict, owned: dict, result: str, exit_code: int | None):
    assert set(row) == {"schema_version", "operation", "request_id", "source_vm_id", "destination_vm_id",
                        "target_user", "started_at", "duration_ms", "result", "exit_code"}, row
    assert row["schema_version"] == 1 and row["operation"] == "vm_exec", row
    assert row["source_vm_id"] == owned["source_id"] and row["destination_vm_id"] == owned["destination_id"], row
    assert row["target_user"] == "runner", row
    assert len(row["request_id"]) == 32 and all(char in "0123456789abcdef" for char in row["request_id"]), row
    started = datetime.fromisoformat(row["started_at"].replace("Z", "+00:00"))
    assert started.utcoffset() == timezone.utc.utcoffset(started), row
    assert 0 <= row["duration_ms"] <= 300_000, row
    assert (row["result"], row["exit_code"]) == (result, exit_code), row


def test_vm_to_vm_exec_stream_result_and_redacted_audit(runner_vm: str, owned_exec_pair: dict):
    """Denied, successful, and nonzero commands have exact redacted public records."""
    owned = owned_exec_pair
    secret = f"command-must-not-be-audited-{uuid.uuid4().hex}"
    with _extra_audit_reader(runner_vm, owned) as records:
        denied_path = f"/tmp/exec-extra-audit-{owned['suffix']}"
        denied = _run_mvm(runner_vm, *_extra_remote_args(owned, join(["touch", denied_path])), check=False)
        assert denied.returncode != 0, (denied.stdout, denied.stderr)
        _sentinel_absent(runner_vm, owned["destination_id"], denied_path)
        rows = records()
        assert len(rows) == 1, rows
        _extra_audit_assert(rows[0], owned, "denied", None)
        request_ids = {rows[0]["request_id"]}
        _extra_exec_grant(runner_vm, owned)
        for exit_code in (0, 23):
            stdout, stderr = f"out-{secret}", f"err-{secret}"
            script = f"printf %s {quote(stdout)}; printf %s {quote(stderr)} >&2; exit {exit_code}"
            result = _run_mvm(runner_vm, *_extra_remote_args(owned, script), check=False)
            assert result.returncode == exit_code, (result.stdout, result.stderr)
            assert result.stdout == stdout and result.stderr == stderr, (result.stdout, result.stderr)
            rows = records()
            assert len(rows) == 1 and rows[0]["request_id"] not in request_ids, rows
            request_ids.add(rows[0]["request_id"])
            _extra_audit_assert(rows[0], owned, "succeeded" if exit_code == 0 else "command_failed", exit_code)
            assert secret not in json.dumps(rows), rows


def _extra_stat_identity(line: str) -> tuple[int, int, str, int]:
    pid = int(line.split(" ", 1)[0])
    fields = line.rpartition(") ")[2].split()
    assert pid > 0 and len(fields) >= 20 and int(fields[19]) > 0, line
    return pid, int(fields[19]), fields[0], int(fields[2])


@pytest.mark.timeout(240)
def test_vm_to_vm_exec_interrupt_reaps_destination_group(runner_vm: str, owned_exec_pair: dict):
    """Interrupt one retained CLI task; exact destination tasks exit and a new relay works."""
    owned = owned_exec_pair
    _extra_exec_grant(runner_vm, owned)
    script = (
        "sleep 120 & child=$!; "
        "set -- $(cat /proc/$$/stat); group=$5; "
        "printf 'leader '; cat /proc/$group/stat; "
        "printf 'parent '; cat /proc/$$/stat; "
        "printf 'child '; cat /proc/$child/stat; wait $child"
    )
    process = subprocess.Popen(
        [SYSTEM_MVM_BINARY, *_extra_remote_args(owned, join(["sh", "-c", script]))],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        env={**os.environ, "NO_COLOR": "1"},
    )
    pidfd = None
    identities = []
    try:
        pidfd = os.pidfd_open(process.pid)
        assert process.stdout is not None
        poller = select.poll()
        poller.register(process.stdout.fileno(), select.POLLIN | select.POLLHUP)
        data = bytearray()
        deadline = time.monotonic() + 30
        while data.count(b"\n") < 3 and time.monotonic() < deadline:
            assert process.poll() is None, "relay ended before exposing destination identities"
            if poller.poll(500):
                chunk = os.read(process.stdout.fileno(), 4096)
                assert chunk, "relay output closed before exposing destination identities"
                data.extend(chunk)
        lines = data.decode().splitlines()
        assert len(lines) == 3, lines
        for label, line in zip(("leader", "parent", "child"), lines, strict=True):
            assert line.startswith(label + " "), lines
            identities.append(_extra_stat_identity(line.removeprefix(label + " ")))
        assert all(item[2] not in ("Z", "X") for item in identities), identities
        assert len({item[3] for item in identities}) == 1, identities
        assert identities[0][0] == identities[0][3] and identities[1][0] != identities[2][0], identities
        print(f"exec cancellation destination identities={identities}", flush=True)
        signal.pidfd_send_signal(pidfd, signal.SIGINT)
        stdout, stderr = process.communicate(timeout=15)
        assert process.returncode != 0, (stdout, stderr)
        deadline = time.monotonic() + 10
        live = identities
        while live and time.monotonic() < deadline:
            live = []
            for pid, ticks, _, group in identities:
                observed = _guest_exec(runner_vm, owned["destination_id"], "cat", f"/proc/{pid}/stat", check=False)
                if observed.returncode == 0:
                    current = _extra_stat_identity(observed.stdout.strip())
                    if current[1] == ticks and current[2] not in ("Z", "X"):
                        live.append((pid, ticks, current[2], group))
                else:
                    assert "No such file or directory" in observed.stderr, (observed.stdout, observed.stderr)
            if live:
                time.sleep(0.2)
        assert not live, f"cancelled destination tasks survived: {live}"
        allowed = _run_mvm(runner_vm, *_extra_remote_args(owned, "id -un"))
        assert allowed.stdout.strip() == "runner", (allowed.stdout, allowed.stderr)
    finally:
        # The retained pidfd is the only signal target; fixture VM removal owns guest cleanup on failure.
        if process.poll() is None:
            if pidfd is None:
                pidfd = os.pidfd_open(process.pid)
            signal.pidfd_send_signal(pidfd, signal.SIGKILL)
            process.communicate(timeout=10)
        if pidfd is not None:
            os.close(pidfd)
        if process.stdout is not None:
            process.stdout.close()
        if process.stderr is not None:
            process.stderr.close()


@pytest.mark.timeout(600)
def test_vm_to_vm_exec_restore_requires_new_endpoint_grant(runner_vm: str, owned_exec_pair: dict):
    """A full restore preserves guest state but receives a new VM/grant identity."""
    owned = owned_exec_pair
    old_id = owned["destination_id"]
    old_identity = _exec_lifecycle_identity(runner_vm, owned["destination"], old_id)
    policy = _extra_exec_grant(runner_vm, owned)
    marker_path = f"/home/runner/exec-extra-snapshot-{owned['suffix']}"
    _exec_lifecycle_remote_effect(runner_vm, owned["source_id"], old_id, marker_path, owned["suffix"])
    _guest_exec(runner_vm, old_id, "sync")
    name = f"exec-extra-capture-{owned['suffix']}"
    before_snapshots = _json_success_data(_run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list)
    assert all(item["name"] != name for item in before_snapshots), f"capture name already exists: {name}"
    owned["snapshot_intents"][name] = {"name": name, "source_vm_id": old_id,
                                       "before_ids": {item["id"] for item in before_snapshots}}
    owned["snapshot_names"].add(name)
    result = _run_mvm(runner_vm, "snapshot", "create", old_id, "--name", name,
                      "--json", check=False, timeout=180)
    intents = [line.removeprefix("Snapshot request ID: ") for line in result.stderr.splitlines()
               if line.startswith("Snapshot request ID: ")]
    assert len(intents) <= 1, result.stderr
    if intents:
        snapshot_id = intents[0]
        assert len(snapshot_id) == 64 and all(char in "0123456789abcdef" for char in snapshot_id), snapshot_id
        assert snapshot_id not in owned["snapshot_intents"][name]["before_ids"], snapshot_id
        owned["snapshot_intents"][name]["request_id"] = snapshot_id
        owned["snapshot_ids"].add(snapshot_id)
    envelope = json.loads(result.stdout)
    if isinstance(envelope.get("data"), dict):
        item = envelope["data"]
        assert item["id"] == owned["snapshot_intents"][name].get("request_id"), item
        assert item["name"] == name and item["source_vm_id"] == old_id and item["created_at"], item
        owned["snapshot_receipts"][item["id"]] = {
            key: item[key] for key in ("id", "name", "source_vm_id", "created_at")
        }
    assert result.returncode == 0, (result.stdout, result.stderr)
    captured = _json_success_data(result.stdout, dict)
    assert captured["id"] == owned["snapshot_intents"][name].get("request_id"), captured
    assert captured["name"] == name and captured["source_vm_id"] == old_id, captured
    assert captured["memory_file"] and captured["state_file"] and captured["rootfs_file"], captured
    assert (captured["vcpu_count"], captured["mem_size_mib"], captured["disk_size_mib"]) == (1, 256, 512)
    inspected = _json_success_data(
        _run_mvm(runner_vm, "snapshot", "inspect", captured["id"], "--json").stdout, dict,
    )
    assert inspected["snapshot"]["id"] == captured["id"] and inspected["snapshot"]["source_vm_id"] == old_id, inspected
    # Release the old destination before restoring: never more than two guests.
    with _exec_lifecycle_task(old_identity):
        _run_mvm(runner_vm, "vm", "rm", old_id, "--force", timeout=120)
    assert all(item["id"] != old_id for item in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout))
    assert all(item["id"] != policy["id"] for item in _exec_policies(runner_vm))
    args = ["snapshot", "restore", captured["id"], owned["destination"], "--resume", "--json"]
    before_ids = _extra_vm_creation_intent(runner_vm, owned, owned["destination"], args)
    result = _run_mvm(runner_vm, *args, check=False, timeout=180)
    _extra_retain_vm_receipts(result, owned, owned["destination"], before_ids)
    assert result.returncode == 0, (result.stdout, result.stderr)
    restored = _json_success_data(result.stdout, list)
    assert len(restored) == 1 and restored[0]["name"] == owned["destination"], restored
    owned["destination_id"] = restored[0]["id"]
    assert (restored[0]["vcpu_count"], restored[0]["mem_size_mib"], restored[0]["disk_size_mib"]) == (1, 256, 512)
    assert restored[0]["network_placement"] == "networkless" and restored[0]["network_id"] == "", restored
    current = _exec_lifecycle_identity(runner_vm, owned["destination"], owned["destination_id"])
    assert current["vm_id"] != old_id and current["boot_id"] == old_identity["boot_id"], (old_identity, current)
    assert (current["pid"], current["start_ticks"]) != (old_identity["pid"], old_identity["start_ticks"])
    assert _guest_exec(runner_vm, owned["destination_id"], "cat", marker_path).stdout == owned["suffix"]
    denied_path = f"/tmp/exec-extra-restored-denied-{owned['suffix']}"
    denied = _run_mvm(runner_vm, *_extra_remote_args(owned, join(["touch", denied_path])), check=False)
    assert denied.returncode != 0, (denied.stdout, denied.stderr)
    _sentinel_absent(runner_vm, owned["destination_id"], denied_path)
    fresh = _extra_exec_grant(runner_vm, owned)
    assert fresh["id"] != policy["id"], (policy, fresh)
    allowed = _run_mvm(runner_vm, *_extra_remote_args(owned, "id -un"))
    assert allowed.stdout.strip() == "runner", (allowed.stdout, allowed.stderr)
    print(f"exec restore capture={captured['id']} old={old_id} new={owned['destination_id']} "
          f"old_policy={policy['id']} new_policy={fresh['id']}", flush=True)
