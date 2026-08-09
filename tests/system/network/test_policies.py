"""Tier-2 routed service-access policy specification."""

from __future__ import annotations

import json
import uuid

import pytest

from tests.system.conftest import _guest_run, _run_mvm, _unique_subnet, ensure_vm_deps

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_network,
    pytest.mark.requires_kvm,
    pytest.mark.requires_network,
    pytest.mark.slow,
    pytest.mark.tier2,
]


def _vm_ip(runner_vm: str, name: str) -> str:
    data = json.loads(_run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout)
    item = data.get("vm", data)
    ip = item.get("ipv4")
    assert isinstance(ip, str) and ip
    return ip


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


def _chain(runner_vm: str, backend: str, chain: str) -> str:
    if backend == "nftables":
        command = f"sudo -n nft list chain ip filter {chain}"
    else:
        command = f"sudo -n iptables -t filter -S {chain}"
    result = _guest_run(runner_vm, command, check=False, timeout=15)
    assert result.returncode == 0, result.stderr
    return result.stdout


@pytest.mark.parametrize("backend", ["nftables", "iptables"])
def test_routed_service_access_policy_specification(
    runner_vm: str, backend: str
) -> None:
    """Default-deny, exact allow, sync recovery, and invalidation for each backend."""
    suffix = uuid.uuid4().hex[:7]
    source_net = f"pol-src-{suffix}"
    destination_net = f"pol-dst-{suffix}"
    other_net = f"pol-other-{suffix}"
    source_vm = f"pol-src-vm-{suffix}"
    destination_vm = f"pol-dst-vm-{suffix}"
    other_vm = f"pol-other-vm-{suffix}"
    policy_id = ""

    original = _run_mvm(
        runner_vm,
        "config",
        "get",
        "settings",
        "firewall_backend",
        check=False,
    ).stdout
    original_backend = "nftables"
    if "=" in original:
        configured = original.rsplit("=", 1)[-1].strip()
        if configured in {"nftables", "iptables"}:
            original_backend = configured

    try:
        _run_mvm(
            runner_vm,
            "config",
            "set",
            "settings",
            "firewall_backend",
            backend,
        )
        ensure_vm_deps(runner_vm)
        for name in (source_net, destination_net, other_net):
            _run_mvm(
                runner_vm,
                "network",
                "create",
                name,
                "--subnet",
                _unique_subnet(name),
                "--non-interactive",
            )
        for vm_name, net_name in ((destination_vm, destination_net), (other_vm, other_net)):
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
                timeout=180,
            )
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            source_vm,
            "--image",
            "alpine:3.23",
            "--network",
            source_net,
            "--cloud-init-mode",
            "net",
            "--nocloud-net-port",
            "9999",
            timeout=180,
        )

        destination_ip = _vm_ip(runner_vm, destination_vm)
        other_ip = _vm_ip(runner_vm, other_vm)
        source_inspect = json.loads(
            _run_mvm(runner_vm, "vm", "inspect", source_vm, "--json").stdout
        ).get("vm", {})
        source_network = json.loads(
            _run_mvm(runner_vm, "network", "inspect", source_net, "--json").stdout
        ).get("network", {})
        source_gateway = source_network["ipv4_gateway"]
        for vm_name in (destination_vm, other_vm):
            _run_mvm(
                runner_vm,
                "exec",
                vm_name,
                "--user",
                "root",
                "--",
                "sh -c 'nohup nc -lk -p 8080 >/tmp/nc.log 2>&1 &'",
                timeout=15,
            )

        assert not _guest_probe(runner_vm, source_vm, destination_ip, 8080)
        assert not _guest_probe(runner_vm, source_vm, other_ip, 8080)

        created = _run_mvm(
            runner_vm,
            "policy",
            "create",
            source_net,
            destination_vm,
            "tcp",
            "8080-8081",
        )
        listed = json.loads(_run_mvm(runner_vm, "policy", "ls", "--json").stdout)
        matching = [
            item
            for item in listed
            if item["source_network_name"] == source_net
            and item["destination_vm_name"] == destination_vm
        ]
        assert len(matching) == 1, created.stdout
        policy_id = matching[0]["id"]
        assert matching[0]["protocol"] == "tcp"
        assert matching[0]["destination_port_start"] == 8080
        assert matching[0]["destination_port_end"] == 8081

        routed = _chain(runner_vm, backend, "MVM-ROUTED-POLICY")
        assert destination_ip in routed
        assert "8080" in routed and "8081" in routed
        assert "DROP" in routed.upper()
        host_input = _chain(runner_vm, backend, "MVM-HOST-INPUT")
        assert "DROP" in host_input.upper()
        nocloud_input = _chain(runner_vm, backend, "MVM-NOCLOUDNET-INPUT")
        assert "9999" in nocloud_input
        if backend == "nftables":
            parent = _guest_run(
                runner_vm,
                "sudo -n nft list chain ip filter INPUT",
                timeout=15,
            ).stdout
        else:
            parent = _guest_run(
                runner_vm,
                "sudo -n iptables -t filter -S INPUT",
                timeout=15,
            ).stdout
        assert parent.index("MVM-NOCLOUDNET-INPUT") < parent.index("MVM-HOST-INPUT")

        host_denied = _guest_probe(runner_vm, source_vm, source_gateway, 22)
        assert not host_denied
        nocloud = _run_mvm(
            runner_vm,
            "exec",
            source_vm,
            "--user",
            "root",
            "--",
            (
                f"wget -qO- http://{source_gateway}:9999/meta-data || "
                f"wget -qO- http://{source_gateway}:9999/{source_inspect['id']}/meta-data"
            ),
            check=False,
            timeout=15,
        )
        assert nocloud.returncode == 0, nocloud.stderr

        assert _guest_probe(runner_vm, source_vm, destination_ip, 8080)
        assert not _guest_probe(runner_vm, source_vm, destination_ip, 8082)
        assert not _guest_probe(runner_vm, source_vm, other_ip, 8080)
        internet = _run_mvm(
            runner_vm,
            "exec",
            source_vm,
            "--user",
            "root",
            "--",
            "ping -c 1 -W 3 1.1.1.1",
            check=False,
            timeout=15,
        )
        assert internet.returncode == 0, internet.stderr

        if backend == "nftables":
            flush = "sudo -n nft flush chain ip filter MVM-ROUTED-POLICY"
        else:
            flush = "sudo -n iptables -t filter -F MVM-ROUTED-POLICY"
        _guest_run(runner_vm, flush, timeout=15)
        assert destination_ip not in _chain(
            runner_vm, backend, "MVM-ROUTED-POLICY"
        )
        sync = json.loads(_run_mvm(runner_vm, "policy", "sync", "--json").stdout)
        assert sync["policies"] == 1
        assert destination_ip in _chain(runner_vm, backend, "MVM-ROUTED-POLICY")
        _guest_run(runner_vm, flush, timeout=15)
        _run_mvm(runner_vm, "network", "sync", "--json")
        assert destination_ip in _chain(runner_vm, backend, "MVM-ROUTED-POLICY")
        assert _guest_probe(runner_vm, source_vm, destination_ip, 8080)

        duplicate = _run_mvm(
            runner_vm,
            "policy",
            "create",
            source_net,
            destination_vm,
            "tcp",
            "8080-8081",
            check=False,
        )
        assert duplicate.returncode != 0
        same_network = _run_mvm(
            runner_vm,
            "policy",
            "create",
            destination_net,
            destination_vm,
            "tcp",
            "80",
            check=False,
        )
        assert same_network.returncode != 0
        missing = _run_mvm(
            runner_vm, "policy", "inspect", "does-not-exist", "--json", check=False
        )
        assert missing.returncode != 0

        _run_mvm(runner_vm, "policy", "rm", policy_id[:12], "--force")
        assert not any(
            item["id"] == policy_id
            for item in json.loads(
                _run_mvm(runner_vm, "policy", "ls", "--json").stdout
            )
        )
        assert not _guest_probe(runner_vm, source_vm, destination_ip, 8080)

        recreated = _run_mvm(
            runner_vm,
            "policy",
            "create",
            source_net,
            destination_vm,
            "udp",
            "5353",
        )
        assert recreated.returncode == 0
        _run_mvm(runner_vm, "vm", "rm", destination_vm, "--force", timeout=120)
        remaining = json.loads(_run_mvm(runner_vm, "policy", "ls", "--json").stdout)
        assert not any(item["destination_vm_name"] == destination_vm for item in remaining)

        _run_mvm(
            runner_vm,
            "policy",
            "create",
            source_net,
            other_vm,
            "tcp",
            "8080",
        )
        _run_mvm(runner_vm, "vm", "rm", source_vm, "--force", timeout=120)
        _run_mvm(runner_vm, "network", "rm", source_net, "--force")
        remaining = json.loads(_run_mvm(runner_vm, "policy", "ls", "--json").stdout)
        assert not any(item["source_network_name"] == source_net for item in remaining)
    finally:
        for vm_name in (source_vm, destination_vm, other_vm):
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False, timeout=120)
        for net_name in (source_net, destination_net, other_net):
            _run_mvm(runner_vm, "network", "rm", net_name, "--force", check=False)
        _run_mvm(
            runner_vm,
            "config",
            "set",
            "settings",
            "firewall_backend",
            original_backend,
            check=False,
        )
