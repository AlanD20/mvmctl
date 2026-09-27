"""Tier-2 traffic-policy enforcement through the installed CLI."""

from __future__ import annotations

import uuid

import pytest

from tests.system.conftest import (
    _json_success_data,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_network,
    pytest.mark.requires_kvm,
    pytest.mark.requires_network,
    pytest.mark.slow,
    pytest.mark.tier2,
]


def _guest_probe(runner_vm: str, source: str, destination: str, port: int) -> bool:
    result = _run_mvm(
        runner_vm,
        "exec",
        source,
        "--user",
        "root",
        "--",
        f"nc -z -w 2 {destination} {port}",
        check=False,
        timeout=15,
    )
    return result.returncode == 0


def test_cross_network_traffic_policy_exact_allow_and_revoke(runner_vm: str) -> None:
    """A network-source grant opens one destination TCP port, then closes it."""
    suffix = uuid.uuid4().hex[:7]
    source_net, destination_net = f"pol-src-{suffix}", f"pol-dst-{suffix}"
    source_vm, destination_vm = f"pol-src-vm-{suffix}", f"pol-dst-vm-{suffix}"
    created_networks: list[str] = []
    created_vms: list[str] = []
    policy_id: str | None = None

    try:
        ensure_vm_deps(runner_vm)
        for name in (source_net, destination_net):
            _run_mvm(
                runner_vm, "network", "create", name, "--subnet", _unique_subnet(name)
            )
            created_networks.append(name)
        for vm_name, net_name in (
            (source_vm, source_net),
            (destination_vm, destination_net),
        ):
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--cloud-init-mode",
                "inject",
                "--no-pci",
                "--disk-size",
                "512M",
                "--vcpu",
                "1",
                "--mem",
                "256M",
                timeout=180,
            )
            created_vms.append(vm_name)

        destination = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", destination_vm, "--json").stdout, dict
        )
        destination_ip = destination["networking"]["ipv4"]
        assert destination_ip
        _run_mvm(
            runner_vm,
            "exec",
            destination_vm,
            "--user",
            "root",
            "--",
            "sh -c 'nohup nc -lk -p 8080 -e /bin/cat </dev/null >/tmp/policy-8080.log 2>&1 & "
            "nohup nc -lk -p 8081 -e /bin/cat </dev/null >/tmp/policy-8081.log 2>&1 &'",
            timeout=15,
        )
        assert _guest_probe(runner_vm, destination_vm, "127.0.0.1", 8080)
        assert _guest_probe(runner_vm, destination_vm, "127.0.0.1", 8081)
        assert _guest_probe(runner_vm, destination_vm, destination_ip, 8080), (
            "destination listener is not reachable on its managed IPv4:8080"
        )
        assert _guest_probe(runner_vm, destination_vm, destination_ip, 8081), (
            "destination listener is not reachable on its managed IPv4:8081"
        )
        assert not _guest_probe(runner_vm, source_vm, destination_ip, 8080)

        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "policy",
                "traffic",
                "create",
                "--from-network",
                source_net,
                "--to-vm",
                destination_vm,
                "--protocol",
                "tcp",
                "--port",
                "8080",
                "--json",
            ).stdout,
            dict,
        )
        policy_id = created["id"]
        listed = _json_success_data(
            _run_mvm(runner_vm, "policy", "traffic", "ls", "--json").stdout, dict
        )
        assert isinstance(listed["vm_to_vm"], list)
        assert any(
            item["id"] == policy_id
            and item["source_network_name"] == source_net
            and item["destination_vm_name"] == destination_vm
            and item["protocol"] == "tcp"
            and item["destination_port_start"] == 8080
            and item["destination_port_end"] == 8080
            for item in listed["network_to_vm"]
        )
        assert _guest_probe(runner_vm, source_vm, destination_ip, 8080)
        assert not _guest_probe(runner_vm, source_vm, destination_ip, 8081)

        _run_mvm(
            runner_vm, "policy", "traffic", "rm", policy_id, "--from-network", "--force"
        )
        policy_id = None
        listed = _json_success_data(
            _run_mvm(runner_vm, "policy", "traffic", "ls", "--json").stdout, dict
        )
        assert not any(
            item["source_network_name"] == source_net
            and item["destination_vm_name"] == destination_vm
            for item in listed["network_to_vm"]
        )
        assert not _guest_probe(runner_vm, source_vm, destination_ip, 8080)
    finally:
        if policy_id is not None:
            _run_mvm(
                runner_vm,
                "policy",
                "traffic",
                "rm",
                policy_id,
                "--from-network",
                "--force",
            )
        for name in reversed(created_vms):
            _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
        for name in reversed(created_networks):
            _run_mvm(runner_vm, "network", "rm", name, "--force", timeout=60)
