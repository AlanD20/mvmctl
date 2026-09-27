"""System coverage for the fixed, root-managed nftables generation."""

from __future__ import annotations

import uuid
from shlex import quote

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
    wait_for_ssh,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_network,
]


def _chain_output(runner_vm: str, family: str, chain: str) -> str:
    """Read the installed generation; an absent table/chain has no rules."""
    table = {"bridge": "mvmctl_bridge", "inet": "mvmctl_inet"}[family]
    command = "sudo -n nft list chain " + " ".join(
        quote(part) for part in (family, table, chain)
    )
    result = _guest_run(runner_vm, command, check=False, timeout=15)
    return result.stdout if result.returncode == 0 else ""


def _rules(output: str) -> list[str]:
    """Extract rule lines from nft's chain listing, excluding its header."""
    return [
        line.strip()
        for line in output.splitlines()
        if line.strip()
        and not line.strip().startswith(("table ", "chain ", "type ", "#"))
        and line.strip() not in ("{", "}")
    ]


def _has_rule(output: str, *parts: str) -> bool:
    return any(all(part in rule for part in parts) for rule in _rules(output))


def _network(runner_vm: str, name: str) -> dict:
    inspected = _run_mvm(runner_vm, "network", "inspect", name, "--json")
    return _json_success_data(inspected.stdout, dict)["network"]


class TestNFTablesFirewallBackend:
    def test_nftables_end_to_end(
        # Rationale: Real network, nested VM, SSH, and kernel rule lifecycle.
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
        timing_targets: dict[str, float],
    ) -> None:
        net_name = unique_network_name
        vm_name = unique_vm_name
        key_name = f"sysk-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(runner_vm, "key", "create", key_name, "--algorithm", "ed25519")
            _run_mvm(runner_vm, "network", "create", net_name, "--subnet", subnet)
            bridge = _network(runner_vm, net_name)["bridge"]
            assert bridge, f"Network {net_name} has no managed bridge"

            inet_forward = _chain_output(runner_vm, "inet", "forward")
            inet_input = _chain_output(runner_vm, "inet", "input")
            postrouting = _chain_output(runner_vm, "inet", "postrouting")
            assert _has_rule(inet_forward, bridge, "accept"), inet_forward
            assert _has_rule(inet_forward, bridge, "drop"), inet_forward
            assert _has_rule(inet_input, bridge, "drop"), inet_input
            assert _has_rule(postrouting, bridge, subnet, "masquerade"), postrouting

            prerouting_before = _rules(_chain_output(runner_vm, "bridge", "prerouting"))
            bridge_forward_before = _rules(_chain_output(runner_vm, "bridge", "forward"))
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm", "create", vm_name,
                "--image", "alpine:3.23", "--network", net_name,
                "--ssh-key", key_name, "--cloud-init-mode", "inject",
                timeout=180,
            )
            vms = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
            vm_info = next((vm for vm in vms if vm["name"] == vm_name), None)
            assert vm_info is not None and vm_info["status"] == "running", vm_info

            prerouting_with_vm = _rules(_chain_output(runner_vm, "bridge", "prerouting"))
            bridge_forward_with_vm = _rules(_chain_output(runner_vm, "bridge", "forward"))
            assert len(prerouting_with_vm) > len(prerouting_before), prerouting_with_vm
            assert len(bridge_forward_with_vm) > len(bridge_forward_before), bridge_forward_with_vm
            assert _has_rule("\n".join(prerouting_with_vm), "drop"), prerouting_with_vm
            assert _has_rule("\n".join(bridge_forward_with_vm), "drop"), bridge_forward_with_vm

            ssh_timeout = max(timing_targets.get("alpine:3.23", 15.0), 30.0)
            assert wait_for_ssh(runner_vm, vm_name, "root", ssh_timeout)
            echoed = _run_mvm(runner_vm, "ssh", vm_name, "--cmd", "echo OK")
            assert "OK" in echoed.stdout, echoed.stdout

            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", timeout=120)
            vms = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
            assert not any(vm["name"] == vm_name for vm in vms)
            assert _rules(_chain_output(runner_vm, "bridge", "prerouting")) == prerouting_before
            assert _rules(_chain_output(runner_vm, "bridge", "forward")) == bridge_forward_before
            postrouting = _chain_output(runner_vm, "inet", "postrouting")
            assert _has_rule(postrouting, bridge, subnet, "masquerade"), postrouting

            _run_mvm(runner_vm, "network", "rm", net_name)
            for chain in ("forward", "input", "postrouting"):
                output = _chain_output(runner_vm, "inet", chain)
                assert bridge not in output, output
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)


class TestAtomicRuleSync:
    """Reconciliation preserves the complete managed nftables generation."""

    pytestmark = [pytest.mark.requires_network]

    def test_conntrack_rule_present_after_sync(
        # Rationale: Read the real inet chains after reconciling a managed network.
        self,
        runner_vm: str,
        module_network: str,
    ) -> None:
        bridge = _network(runner_vm, module_network)["bridge"]
        _run_mvm(runner_vm, "network", "sync", module_network)
        for chain in ("forward", "input"):
            output = _chain_output(runner_vm, "inet", chain)
            assert _has_rule(output, bridge, "ct state established,related", "accept"), output
            assert _has_rule(output, bridge, "drop"), output

    def test_sync_idempotent_no_rule_duplication(
        # Rationale: Repeated reconciliation must leave each managed chain unchanged.
        self,
        runner_vm: str,
        module_network: str,
    ) -> None:
        chains = (
            ("bridge", "prerouting"),
            ("bridge", "forward"),
            ("inet", "forward"),
            ("inet", "input"),
            ("inet", "postrouting"),
        )
        _run_mvm(runner_vm, "network", "sync", module_network)
        first = {chain: _rules(_chain_output(runner_vm, *chain)) for chain in chains}
        assert first[("inet", "forward")], first

        _run_mvm(runner_vm, "network", "sync", module_network)
        second = {chain: _rules(_chain_output(runner_vm, *chain)) for chain in chains}
        assert second == first, f"Managed nftables rules changed on repeat sync: {first} -> {second}"

    def test_sync_preserves_masquerade_rule(
        # Rationale: Reconciliation retains NAT and the network's forward policy.
        self,
        runner_vm: str,
        unique_network_name: str,
    ) -> None:
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        try:
            _run_mvm(runner_vm, "network", "create", net_name, "--subnet", subnet)
            bridge = _network(runner_vm, net_name)["bridge"]
            assert bridge, f"Network {net_name} has no managed bridge"
            _run_mvm(runner_vm, "network", "sync", net_name)

            postrouting = _chain_output(runner_vm, "inet", "postrouting")
            inet_forward = _chain_output(runner_vm, "inet", "forward")
            assert _has_rule(postrouting, bridge, subnet, "masquerade"), postrouting
            assert _has_rule(inet_forward, bridge, "accept"), inet_forward
            assert _has_rule(inet_forward, bridge, "drop"), inet_forward
        finally:
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)
