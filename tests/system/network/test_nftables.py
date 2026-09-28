"""System coverage for the fixed, root-managed nftables generation."""

from __future__ import annotations

import ipaddress
import json
import os
import subprocess
import time
import uuid
from pathlib import Path
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


def _reuse_vm(runner_vm: str, name: str) -> dict:
    vms = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
    matches = [vm for vm in vms if vm["name"] == name]
    assert len(matches) == 1 and matches[0]["status"] == "running", matches
    return matches[0]


def _reuse_journal(runner_vm: str, vm_id: str) -> dict:
    path = f"/var/lib/mvmctl/networks/{os.getuid()}/topologies/{vm_id}.json"
    metadata = _guest_run(runner_vm, f"sudo -n stat -c '%u:%g:%a:%h' -- {quote(path)}")
    assert metadata.stdout.strip() == "0:0:600:1", metadata.stdout
    return json.loads(_guest_run(runner_vm, f"sudo -n cat -- {quote(path)}").stdout)


def _reuse_namespace(runner_vm: str, vm: dict, network: dict) -> dict:
    """Tie independent read-only kernel observations to the root-owned endpoint."""
    record = _reuse_journal(runner_vm, vm["id"])
    assert record["lifecycle"] == "active" and not record["pending"], record
    assert record["owner_uid"] == os.getuid() and record["vm_id"] == vm["id"], record
    assert record["network_id"] == network["id"], record
    assert record["guest_ipv4"] == vm["ipv4"] and record["guest_mac"] == vm["mac"], record
    topology = record["topology"]
    namespace = topology["Namespace"]
    path = f"/run/mvmctl/authority/{os.getuid()}/netns/{vm['id']}"
    assert namespace["Path"] == path, namespace
    expected = f"{namespace['Identity']['Device']}:{namespace['Identity']['Inode']}"
    observed = _guest_run(
        runner_vm,
        f"sudo -n stat -Lc '%d:%i' -- {quote(path)} /proc/{int(vm['pid'])}/ns/net",
    ).stdout.splitlines()
    assert observed == [expected, expected], (expected, observed)
    runner_namespace = _guest_run(runner_vm, "stat -Lc '%d:%i' /proc/self/ns/net").stdout.strip()
    assert expected != runner_namespace, (expected, runner_namespace)
    host = topology["VethPair"]["Host"]
    bridge = topology["ManagedBridge"]
    assert bridge["Name"] == network["bridge"], topology
    assert f"{bridge['Namespace']['Device']}:{bridge['Namespace']['Inode']}" == runner_namespace, bridge
    host_links = json.loads(_guest_run(runner_vm, "ip -j link show").stdout)
    host_link = next(link for link in host_links if link["ifname"] == host["Name"])
    assert host_link["ifindex"] == host["IfIndex"], host_link
    assert host_link["master"] == bridge["Name"] and "UP" in host_link["flags"], host_link
    links = json.loads(_guest_run(
        runner_vm, f"sudo -n nsenter --net={quote(path)} -- ip -j link show"
    ).stdout)
    expected_links = (topology["LocalBridge"], topology["TAP"], topology["VethPair"]["Peer"])
    assert {link["ifname"] for link in links} == {"lo", *(link["Name"] for link in expected_links)}, links
    for identity in expected_links:
        link = next(link for link in links if link["ifname"] == identity["Name"])
        assert link["ifindex"] == identity["IfIndex"] and "UP" in link["flags"], link
    for identity in (topology["TAP"], topology["VethPair"]["Peer"]):
        link = next(link for link in links if link["ifname"] == identity["Name"])
        assert link["master"] == topology["LocalBridge"]["Name"], link
    record["observed_namespace"] = expected
    return record


def _reuse_neighbor(runner_vm: str, bridge: str, address: str) -> list[dict]:
    result = _guest_run(
        runner_vm, f"ip -j -4 neigh show to {quote(address)}"
    )
    # A dev-filtered ip listing omits the device; retain its observed kernel name.
    return [entry for entry in json.loads(result.stdout) if entry.get("dev") == bridge]


def _assert_reuse_neighbor(runner_vm: str, bridge: str, address: str, mac: str) -> None:
    neighbors = _reuse_neighbor(runner_vm, bridge, address)
    assert len(neighbors) == 1, f"Missing exact {bridge}/{address} neighbor: {neighbors}"
    neighbor = neighbors[0]
    assert neighbor["dst"] == address and neighbor["dev"] == bridge, neighbor
    assert neighbor.get("lladdr") == mac, neighbor
    assert set(neighbor["state"]) <= {"REACHABLE", "STALE", "DELAY", "PROBE"}, neighbor
    assert neighbor["state"] and not neighbor.get("flags"), neighbor


def _assert_reuse_removed(runner_vm: str, record: dict) -> None:
    """A cleaned journal is a receipt; its live namespace, links, and jail must be absent."""
    cleaned = _reuse_journal(runner_vm, record["vm_id"])
    assert cleaned["lifecycle"] == "cleaned" and not cleaned["pending"], cleaned
    assert not any(cleaned["progress"].values()), cleaned
    topology = record["topology"]
    paths = (
        topology["Namespace"]["Path"],
        f"/var/lib/mvmctl/jailer/firecracker/{record['vm_id']}",
    )
    for path in paths:
        _guest_run(runner_vm, f"sudo -n test ! -e {quote(path)} && sudo -n test ! -L {quote(path)}")
    host_names = {link["ifname"] for link in json.loads(_guest_run(runner_vm, "ip -j link show").stdout)}
    removed_names = {
        topology["VethPair"]["Host"]["Name"], topology["VethPair"]["Peer"]["Name"],
        topology["LocalBridge"]["Name"], topology["TAP"]["Name"],
    }
    assert host_names.isdisjoint(removed_names), (removed_names, host_names)
    for chain in ("prerouting", "forward"):
        output = _guest_run(
            runner_vm, f"sudo -n nft list chain bridge mvmctl_bridge {quote(chain)}"
        ).stdout
        assert topology["VethPair"]["Host"]["Name"] not in output, output


@pytest.mark.requires_network
def test_private_namespaces_reused_ip_has_fresh_gateway_neighbor(
    runner_vm: str,
    unique_vm_name: str,
    unique_network_name: str,
    tmp_path: Path,
) -> None:
    """Exercise natural stale-IP reuse, without manufacturing neighbor or firewall state.

    A known old binding before B launch plus B's first gateway response proves reuse
    recovery. Guest boot ARP may also refresh it: pre-exposure invalidation ordering
    remains an independent source/Go gate, not a claim of this journey.
    """
    net_name = unique_network_name
    vm_a, vm_b, control = unique_vm_name, f"{unique_vm_name}-b", f"{unique_vm_name}-ctl"
    subnet = _unique_subnet(net_name)
    prefix = ipaddress.IPv4Network(subnet)
    reused_ip, control_ip = str(prefix.network_address + 50), str(prefix.network_address + 51)
    assert os.getuid() != 0, "Run the installed CLI journey as the enrolled non-root runner operator"
    mac_seed = uuid.uuid4().hex
    mac_prefix = "02:" + ":".join(mac_seed[index:index + 2] for index in range(0, 8, 2))
    mac_a, mac_b, mac_control = f"{mac_prefix}:0a", f"{mac_prefix}:0b", f"{mac_prefix}:0c"
    marker = f"gateway-{uuid.uuid4().hex}"
    (tmp_path / "probe").write_text(marker, encoding="utf-8")
    log = tmp_path / "gateway.log"
    service: subprocess.Popen[str] | None = None
    attempted_vms: list[str] = []
    records: dict[str, dict] = {}
    network_attempted = False
    network: dict = {}

    def create(name: str, address: str, mac: str) -> dict:
        attempted_vms.append(name)
        _run_mvm(
            runner_vm, "vm", "create", name,
            "--image", "alpine:3.23", "--network", net_name,
            "--ip", address, "--mac", mac, "--cloud-init-mode", "inject",
            "--no-pci", "--vcpu", "1", "--mem", "256M", "--disk-size", "512M",
            timeout=180,
        )
        vm = _reuse_vm(runner_vm, name)
        assert vm["ipv4"] == address and vm["mac"] == mac, vm
        records[name] = _reuse_journal(runner_vm, vm["id"])
        records[name] = _reuse_namespace(runner_vm, vm, network)
        return vm

    def gateway_request(name: str) -> None:
        # vsock transports only the command; the HTTP exchange uses guest eth0.
        request = "printf 'GET /probe HTTP/1.0\\r\\nHost: gateway\\r\\n\\r\\n' "
        result = _run_mvm(
            runner_vm, "exec", name, "--user", "root", "--timeout", "10", "--",
            request + f"| nc -w 3 {quote(network['ipv4_gateway'])} 8000",
        )
        response = result.stdout.replace("\r\n", "\n")
        assert response.startswith(("HTTP/1.0 200 ", "HTTP/1.1 200 ")), response
        assert response.split("\n\n", 1)[-1].strip() == marker, response

    try:
        # Preparation is the runner owner's job; this journey never pulls assets.
        for domain in ("kernel", "image", "bin"):
            assets = _json_success_data(_run_mvm(runner_vm, domain, "ls", "--json").stdout, list)
            if domain == "image":
                assert any(asset["type"] == "alpine" and asset["version"] == "3.23"
                           and asset["is_present"] for asset in assets), assets
            elif domain == "bin":
                for kind in ("firecracker", "jailer"):
                    assert any(asset["type"] == kind and asset["version"] == "1.16.2"
                               and asset["is_default"] and asset["is_present"] for asset in assets), assets
            else:
                assert any(asset["is_default"] and asset["is_present"] for asset in assets), assets
        network_attempted = True
        _run_mvm(runner_vm, "network", "create", net_name, "--subnet", subnet, "--no-nat")
        network = _network(runner_vm, net_name)
        service = subprocess.Popen(
            ["/usr/local/bin/mvm", "run", "nocloudnet", "serve",
             "--cloud-init-dir", str(tmp_path), "--network-id", network["id"], "--log-file", str(log)],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            assert service.poll() is None, service.communicate()
            if log.is_file() and "NoCloud server ready" in log.read_text(encoding="utf-8"):
                break
            time.sleep(0.1)
        else:
            pytest.fail("The typed NoCloud gateway service did not become ready within 10s")

        create(control, control_ip, mac_control)
        gateway_request(control)
        _assert_reuse_neighbor(runner_vm, network["bridge"], control_ip, mac_control)
        a = create(vm_a, reused_ip, mac_a)
        assert records[vm_a]["observed_namespace"] != records[control]["observed_namespace"]
        gateway_request(vm_a)
        _assert_reuse_neighbor(runner_vm, network["bridge"], reused_ip, mac_a)
        _run_mvm(runner_vm, "vm", "rm", vm_a, "--force", timeout=120)
        attempted_vms.remove(vm_a)
        _assert_reuse_removed(runner_vm, records[vm_a])
        # If natural teardown/GC discarded A's binding, this is missing oracle
        # evidence, not a passing IP-reuse test. Never seed an artificial entry.
        try:
            _assert_reuse_neighbor(runner_vm, network["bridge"], reused_ip, mac_a)
        except AssertionError as error:
            pytest.fail(f"Cannot qualify stale-IP reuse: A's old dynamic binding was not retained: {error}")
        b = create(vm_b, reused_ip, mac_b)
        assert b["id"] != a["id"]
        assert records[vm_b]["endpoint_id"] != records[vm_a]["endpoint_id"]
        topology_a, topology_b = records[vm_a]["topology"], records[vm_b]["topology"]
        assert topology_b["Namespace"]["Path"] != topology_a["Namespace"]["Path"]
        assert topology_b["VethPair"]["Host"]["Name"] != topology_a["VethPair"]["Host"]["Name"]
        assert records[vm_b]["observed_namespace"] != records[control]["observed_namespace"]
        before_request = _reuse_neighbor(runner_vm, network["bridge"], reused_ip)
        assert all(entry.get("lladdr") != mac_a for entry in before_request), before_request
        gateway_request(vm_b)
        _assert_reuse_neighbor(runner_vm, network["bridge"], reused_ip, mac_b)
        _assert_reuse_neighbor(runner_vm, network["bridge"], control_ip, mac_control)
        _assert_reuse_removed(runner_vm, records[vm_a])
    finally:
        failures: list[str] = []
        if service is not None:
            try:
                if service.poll() is None:
                    service.terminate()
                stdout, stderr = service.communicate(timeout=40)
                if service.returncode != 0:
                    failures.append(f"NoCloud shutdown: rc={service.returncode}, {stdout!r}, {stderr!r}")
            except subprocess.TimeoutExpired:
                service.kill()
                stdout, stderr = service.communicate(timeout=10)
                failures.append(f"NoCloud shutdown timed out: {stdout!r}, {stderr!r}")
            except Exception as error:
                failures.append(f"NoCloud shutdown failed: {error}")
        for name in reversed(attempted_vms):
            try:
                result = _run_mvm(runner_vm, "vm", "rm", name, "--force", check=False, timeout=120)
                if result.returncode != 0:
                    failures.append(f"VM {name} removal: {result.stdout!r}, {result.stderr!r}")
            except Exception as error:
                failures.append(f"VM {name} removal failed: {error}")
        for name, record in records.items():
            try:
                _assert_reuse_removed(runner_vm, record)
            except Exception as error:
                failures.append(f"VM {name} cleanup observation: {error}")
        try:
            remaining = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
            if any(vm["name"] in (vm_a, vm_b, control) for vm in remaining):
                failures.append(f"Test VMs remain registered: {remaining}")
        except Exception as error:
            failures.append(f"VM absence observation failed: {error}")
        if network_attempted:
            try:
                result = _run_mvm(runner_vm, "network", "rm", net_name, check=False)
                if result.returncode != 0:
                    failures.append(f"Network removal: {result.stdout!r}, {result.stderr!r}")
            except Exception as error:
                failures.append(f"Network removal failed: {error}")
            try:
                networks = _json_success_data(_run_mvm(runner_vm, "network", "ls", "--json").stdout, list)
                if any(item["name"] == net_name for item in networks):
                    failures.append(f"Network {net_name} remains registered")
                links = json.loads(_guest_run(runner_vm, "ip -j link show").stdout)
                if network and any(link["ifname"] == network["bridge"] for link in links):
                    failures.append(f"Managed bridge {network['bridge']} remains")
            except Exception as error:
                failures.append(f"Network absence observation failed: {error}")
        assert not failures, "IP-reuse cleanup failed:\n" + "\n".join(failures)
