"""Directional VM-to-VM exec through the installed candidate and guest agents."""

from __future__ import annotations

import time
import uuid

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
