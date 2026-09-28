"""System coverage for the fixed, root-managed nftables generation."""

from __future__ import annotations

import ipaddress
import json
import os
import re
import stat
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
    if result.returncode == 0:
        return result.stdout
    # ENOENT is the only accepted failure, independently confirmed below. Sudo,
    # netlink, tool and timeout failures must never become an empty-rule oracle.
    errors = [line for line in result.stderr.splitlines() if line.startswith("Error:")]
    assert result.returncode == 1 and errors == ["Error: No such file or directory"], (
        command,
        result.returncode,
        result.stdout,
        result.stderr,
    )
    tables = json.loads(_guest_run(runner_vm, "sudo -n nft -j list tables").stdout)[
        "nftables"
    ]
    if any(
        item.get("table", {}).get("family") == family and item["table"]["name"] == table
        for item in tables
    ):
        listing = json.loads(
            _guest_run(
                runner_vm, f"sudo -n nft -j list table {quote(family)} {quote(table)}"
            ).stdout
        )["nftables"]
        assert not any(
            item.get("chain", {}).get("name") == chain for item in listing
        ), ("chain exists despite failed read", result.stderr, listing)
    return ""


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
    data = _json_success_data(inspected.stdout, dict)
    return {**data["network"], "nat_enabled": data["nat"]["nat_enabled"]}


_NFT_IDENTITIES = {
    "network": (
        "id",
        "name",
        "created_at",
        "subnet",
        "bridge",
        "ipv4_gateway",
        "nat_enabled",
    ),
    "vm": (
        "id",
        "name",
        "created_at",
        "network_id",
        "network_placement",
        "ipv4",
        "mac",
    ),
    "key": (
        "id",
        "name",
        "created_at",
        "fingerprint",
        "algorithm",
        "public_key_path",
        "private_key_path",
    ),
}


def _nft_items(runner_vm: str, domain: str) -> list[dict]:
    return _json_success_data(_run_mvm(runner_vm, domain, "ls", "--json").stdout, list)


def _nft_inventory(runner_vm: str) -> dict:
    """Retain resource membership, defaults and configuration, including unrelated assets."""
    baseline = {}
    for domain in (
        "network",
        "vm",
        "key",
        "volume",
        "snapshot",
        "image",
        "kernel",
        "bin",
        "env",
    ):
        items = _nft_items(runner_vm, domain)
        # Sync/default selection legitimately advances only this display field.
        if domain in ("network", "key"):
            items = [
                {key: value for key, value in item.items() if key != "updated_at"}
                for item in items
            ]
        identity_key = "workflow_id" if domain == "env" else "id"
        baseline[domain] = sorted(items, key=lambda item: item[identity_key])
    for label, args, kind in (
        ("config", ("config", "ls"), dict),
        ("traffic", ("policy", "traffic", "ls"), dict),
        ("exec", ("policy", "exec", "ls"), list),
    ):
        baseline[label] = _json_success_data(
            _run_mvm(runner_vm, *args, "--json").stdout, kind
        )
    return baseline


def _nft_preseed(runner_vm: str) -> tuple[str, str]:
    """Admit preseeded assets without pulls, default changes or backend changes."""
    for kind in ("firecracker", "jailer"):
        matches = [
            item
            for item in _nft_items(runner_vm, "bin")
            if item["type"] == kind and item["is_default"]
        ]
        assert (
            len(matches) == 1
            and matches[0]["version"] == "1.16.2"
            and matches[0]["is_present"]
        ), matches
    kernels = [
        item
        for item in _nft_items(runner_vm, "kernel")
        if item["is_default"] and item["is_present"]
    ]
    images = [
        item
        for item in _nft_items(runner_vm, "image")
        if item["type"] == "alpine" and item["version"] == "3.23" and item["is_present"]
    ]
    assert len(kernels) == len(images) == 1, (kernels, images)
    backend = _json_success_data(
        _run_mvm(
            runner_vm, "config", "get", "settings", "guestfs_enabled", "--json"
        ).stdout,
        bool,
    )
    assert backend is False, (
        "preconfigure LoopMount; nftables tests never change backend settings"
    )
    return images[0]["id"], kernels[0]["id"]


class NftablesCase:
    """Function-local ownership for the four network/rule lifecycle cases."""

    def __init__(self, runner_vm: str):
        self.runner_vm = runner_vm
        self.before = _nft_inventory(runner_vm)
        self.attempts: list[tuple[str, str]] = []
        self.receipts: dict[tuple[str, str], dict] = {}
        self.live: list[tuple[str, str]] = []

    def admit(self, domain: str, name: str) -> None:
        items = _nft_items(self.runner_vm, domain)
        assert not any(item["name"] == name or item["id"] == name for item in items), (
            items
        )
        self.attempts.append((domain, name))

    def capture(self, domain: str, name: str, item: dict) -> dict:
        assert item["name"] == name and item["created_at"], item
        if domain == "key":
            assert item["id"] == item["fingerprint"] and re.fullmatch(
                r"SHA256:[A-Za-z0-9+/]{43}", item["id"]
            ), item
        else:
            width = 32 if domain == "vm" else 64
            assert re.fullmatch(rf"[0-9a-f]{{{width}}}", item["id"]), item
        assert item["id"] not in {entry["id"] for entry in self.before[domain]}, item
        receipt = {key: item[key] for key in _NFT_IDENTITIES[domain]}
        # Retain the returned immutable identity before any independent oracle can fail.
        self.receipts[(domain, name)] = receipt
        self.live.append((domain, name))
        if domain == "network":
            links = json.loads(_guest_run(self.runner_vm, "ip -j -d link show").stdout)
            matches = [link for link in links if link["ifname"] == item["bridge"]]
            assert (
                len(matches) == 1 and matches[0]["linkinfo"]["info_kind"] == "bridge"
            ), matches
            link = matches[0]
            assert (
                link["ifindex"] > 0 and link.get("ifalias") and "UP" in link["flags"]
            ), link
            receipt["link"] = (link["ifindex"], link["ifalias"])
            receipt["namespace"] = _guest_run(
                self.runner_vm, "stat -Lc '%d:%i' /proc/self/ns/net"
            ).stdout.strip()
        elif domain == "vm":
            assert (
                item["status"] == "running" and item["network_placement"] == "managed"
            ), item
            receipt["journal"] = _reuse_journal(self.runner_vm, item["id"])
            journal = receipt["journal"]
            assert (
                journal["owner_uid"] == os.getuid() and journal["vm_id"] == item["id"]
            ), journal
            assert (
                journal["network_id"] == item["network_id"]
                and journal["lifecycle"] == "active"
            ), journal
            assert not journal["pending"], journal
        else:
            receipt["files"] = {}
            for raw in (item["public_key_path"], item["private_key_path"]):
                metadata = Path(raw).lstat()
                assert (
                    stat.S_ISREG(metadata.st_mode) and metadata.st_uid == os.getuid()
                ), raw
                receipt["files"][raw] = (metadata.st_dev, metadata.st_ino)
            observed = _guest_run(
                self.runner_vm, f"ssh-keygen -lf {quote(item['public_key_path'])}"
            ).stdout.split()[1]
            assert observed == item["id"], (observed, item)
        return receipt

    def assert_receipt(self, domain: str, receipt: dict) -> None:
        items = _nft_items(self.runner_vm, domain)
        matches = [
            item
            for item in items
            if item["id"] == receipt["id"] or item["name"] == receipt["name"]
        ]
        assert len(matches) == 1, ("ambiguous cleanup identity", receipt, matches)
        assert {key: matches[0][key] for key in _NFT_IDENTITIES[domain]} == {
            key: receipt[key] for key in _NFT_IDENTITIES[domain]
        }, (receipt, matches)
        if domain == "network":
            links = json.loads(_guest_run(self.runner_vm, "ip -j -d link show").stdout)
            link = next(item for item in links if item["ifname"] == receipt["bridge"])
            assert (
                link["linkinfo"]["info_kind"] == "bridge"
                and (link["ifindex"], link.get("ifalias")) == receipt["link"]
            ), link
            assert (
                _guest_run(
                    self.runner_vm, "stat -Lc '%d:%i' /proc/self/ns/net"
                ).stdout.strip()
                == receipt["namespace"]
            )
        elif domain == "vm":
            journal = _reuse_journal(self.runner_vm, receipt["id"])
            for field in (
                "owner_uid",
                "vm_id",
                "network_id",
                "endpoint_id",
                "guest_ipv4",
                "guest_mac",
                "topology",
            ):
                assert journal[field] == receipt["journal"][field], (
                    field,
                    receipt,
                    journal,
                )
        else:
            for raw, identity in receipt["files"].items():
                metadata = Path(raw).lstat()
                assert (
                    stat.S_ISREG(metadata.st_mode)
                    and (metadata.st_dev, metadata.st_ino) == identity
                ), raw

    def create_network(self, name: str) -> dict:
        self.admit("network", name)
        subnet = _unique_subnet(name)
        assert not any(
            ipaddress.ip_network(subnet).overlaps(ipaddress.ip_network(item["subnet"]))
            for item in _nft_items(self.runner_vm, "network")
        ), subnet
        item = _json_success_data(
            _run_mvm(
                self.runner_vm, "network", "create", name, "--subnet", subnet, "--json"
            ).stdout,
            dict,
        )
        receipt = self.capture("network", name, item)
        assert (
            item["subnet"] == subnet and item["nat_enabled"] and not item["is_default"]
        ), item
        return receipt

    def create_key(self) -> dict:
        name = f"sysk-{uuid.uuid4().hex}"
        self.admit("key", name)
        item = _json_success_data(
            _run_mvm(
                self.runner_vm,
                "key",
                "create",
                name,
                "--algorithm",
                "ed25519",
                "--json",
            ).stdout,
            dict,
        )
        receipt = self.capture("key", name, item)
        assert not item["is_default"], item
        return receipt

    def create_vm(self, name: str, network: dict, key: dict) -> dict:
        image, kernel = _nft_preseed(self.runner_vm)
        self.assert_receipt("network", network)
        self.assert_receipt("key", key)
        self.admit("vm", name)
        created = _json_success_data(
            _run_mvm(
                self.runner_vm,
                "vm",
                "create",
                name,
                "--image",
                image,
                "--kernel",
                kernel,
                "--network",
                network["id"],
                "--ssh-key",
                key["id"],
                "--cloud-init-mode",
                "inject",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1, created
        receipt = self.capture("vm", name, created[0])
        assert receipt["network_id"] == network["id"], receipt
        return receipt

    def remove(self, domain: str, receipt: dict) -> None:
        assert (domain, receipt["name"]) in self.live, "missing live owned receipt"
        self.assert_receipt(domain, receipt)
        args = [domain, "rm", receipt["id"]]
        if domain == "vm":
            args.append("--force")
        _run_mvm(self.runner_vm, *args, timeout=120 if domain == "vm" else 60)
        assert not any(
            item["id"] == receipt["id"] or item["name"] == receipt["name"]
            for item in _nft_items(self.runner_vm, domain)
        ), receipt
        if domain == "network":
            links = json.loads(_guest_run(self.runner_vm, "ip -j link show").stdout)
            assert not any(
                link["ifname"] == receipt["bridge"]
                or link["ifindex"] == receipt["link"][0]
                for link in links
            ), links
            assert (
                receipt["bridge"]
                not in _guest_run(self.runner_vm, "sudo -n nft -j list ruleset").stdout
            )
        elif domain == "vm":
            _assert_reuse_removed(self.runner_vm, receipt["journal"])
        else:
            for raw in receipt["files"]:
                assert not Path(raw).exists() and not Path(raw).is_symlink(), raw
        self.live.remove((domain, receipt["name"]))

    def finish(self) -> None:
        failures = []
        for domain in ("vm", "network", "key"):
            for kind, name in reversed(self.live[:]):
                if kind != domain:
                    continue
                try:
                    self.remove(domain, self.receipts[(domain, name)])
                except Exception as error:
                    failures.append(f"{domain}/{name} cleanup: {error}")
        for domain, name in self.attempts:
            try:
                assert not any(
                    item["name"] == name for item in _nft_items(self.runner_vm, domain)
                ), f"unresolved attempted create: {domain}/{name}"
            except Exception as error:
                failures.append(f"attempted-create absence: {error}")
        try:
            after = _nft_inventory(self.runner_vm)
            for label, expected in self.before.items():
                assert after[label] == expected, (
                    "baseline/default/config changed",
                    label,
                    expected,
                    after[label],
                )
        except Exception as error:
            failures.append(f"inventory restoration: {error}")
        assert not failures, "nftables cleanup failed:\n" + "\n".join(failures)


@pytest.fixture
def nftables_case(runner_vm: str):
    case = NftablesCase(runner_vm)
    try:
        yield case
    finally:
        case.finish()


class TestNFTablesFirewallBackend:
    def test_nftables_end_to_end(
        # Rationale: Real network, nested VM, SSH, and kernel rule lifecycle.
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
        timing_targets: dict[str, float],
        nftables_case: NftablesCase,
    ) -> None:
        _nft_preseed(runner_vm)
        key = nftables_case.create_key()
        network = nftables_case.create_network(unique_network_name)
        bridge, subnet = network["bridge"], network["subnet"]
        assert bridge, f"Network {unique_network_name} has no managed bridge"

        inet_forward = _chain_output(runner_vm, "inet", "forward")
        inet_input = _chain_output(runner_vm, "inet", "input")
        postrouting = _chain_output(runner_vm, "inet", "postrouting")
        assert _has_rule(inet_forward, bridge, "accept"), inet_forward
        assert _has_rule(inet_forward, bridge, "drop"), inet_forward
        assert _has_rule(inet_input, bridge, "drop"), inet_input
        assert _has_rule(postrouting, bridge, subnet, "masquerade"), postrouting

        prerouting_before = _rules(_chain_output(runner_vm, "bridge", "prerouting"))
        bridge_forward_before = _rules(_chain_output(runner_vm, "bridge", "forward"))
        vm_info = nftables_case.create_vm(unique_vm_name, network, key)
        vm_id = vm_info["id"]

        prerouting_with_vm = _rules(_chain_output(runner_vm, "bridge", "prerouting"))
        bridge_forward_with_vm = _rules(_chain_output(runner_vm, "bridge", "forward"))
        assert len(prerouting_with_vm) > len(prerouting_before), prerouting_with_vm
        assert len(bridge_forward_with_vm) > len(bridge_forward_before), (
            bridge_forward_with_vm
        )
        assert _has_rule("\n".join(prerouting_with_vm), "drop"), prerouting_with_vm
        assert _has_rule("\n".join(bridge_forward_with_vm), "drop"), (
            bridge_forward_with_vm
        )

        ssh_timeout = max(timing_targets.get("alpine:3.23", 15.0), 30.0)
        assert wait_for_ssh(runner_vm, vm_id, "root", ssh_timeout)
        echoed = _run_mvm(runner_vm, "ssh", vm_id, "--cmd", "echo OK")
        assert "OK" in echoed.stdout, echoed.stdout

        nftables_case.remove("vm", vm_info)
        vms = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
        assert not any(vm["name"] == unique_vm_name for vm in vms)
        assert (
            _rules(_chain_output(runner_vm, "bridge", "prerouting"))
            == prerouting_before
        )
        assert (
            _rules(_chain_output(runner_vm, "bridge", "forward"))
            == bridge_forward_before
        )
        postrouting = _chain_output(runner_vm, "inet", "postrouting")
        assert _has_rule(postrouting, bridge, subnet, "masquerade"), postrouting

        nftables_case.remove("network", network)
        for chain in ("forward", "input", "postrouting"):
            output = _chain_output(runner_vm, "inet", chain)
            assert bridge not in output, output


class TestAtomicRuleSync:
    """Reconciliation preserves the complete managed nftables generation."""

    pytestmark = [pytest.mark.requires_network]

    def test_conntrack_rule_present_after_sync(
        # Rationale: Read the real inet chains after reconciling a managed network.
        self,
        runner_vm: str,
        unique_network_name: str,
        nftables_case: NftablesCase,
    ) -> None:
        network = nftables_case.create_network(unique_network_name)
        bridge = network["bridge"]
        _run_mvm(runner_vm, "network", "sync", network["id"])
        for chain in ("forward", "input"):
            output = _chain_output(runner_vm, "inet", chain)
            assert _has_rule(
                output, bridge, "ct state established,related", "accept"
            ), output
            assert _has_rule(output, bridge, "drop"), output

    def test_sync_idempotent_no_rule_duplication(
        # Rationale: Repeated reconciliation must leave each managed chain unchanged.
        self,
        runner_vm: str,
        unique_network_name: str,
        nftables_case: NftablesCase,
    ) -> None:
        network = nftables_case.create_network(unique_network_name)
        chains = (
            ("bridge", "prerouting"),
            ("bridge", "forward"),
            ("inet", "forward"),
            ("inet", "input"),
            ("inet", "postrouting"),
        )
        _run_mvm(runner_vm, "network", "sync", network["id"])
        first = {chain: _rules(_chain_output(runner_vm, *chain)) for chain in chains}
        assert first[("inet", "forward")], first

        _run_mvm(runner_vm, "network", "sync", network["id"])
        second = {chain: _rules(_chain_output(runner_vm, *chain)) for chain in chains}
        assert second == first, (
            f"Managed nftables rules changed on repeat sync: {first} -> {second}"
        )

    def test_sync_preserves_masquerade_rule(
        # Rationale: Reconciliation retains NAT and the network's forward policy.
        self,
        runner_vm: str,
        unique_network_name: str,
        nftables_case: NftablesCase,
    ) -> None:
        network = nftables_case.create_network(unique_network_name)
        bridge, subnet = network["bridge"], network["subnet"]
        assert bridge, f"Network {unique_network_name} has no managed bridge"
        _run_mvm(runner_vm, "network", "sync", network["id"])

        postrouting = _chain_output(runner_vm, "inet", "postrouting")
        inet_forward = _chain_output(runner_vm, "inet", "forward")
        assert _has_rule(postrouting, bridge, subnet, "masquerade"), postrouting
        assert _has_rule(inet_forward, bridge, "accept"), inet_forward
        assert _has_rule(inet_forward, bridge, "drop"), inet_forward


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
    assert record["guest_ipv4"] == vm["ipv4"] and record["guest_mac"] == vm["mac"], (
        record
    )
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
    runner_namespace = _guest_run(
        runner_vm, "stat -Lc '%d:%i' /proc/self/ns/net"
    ).stdout.strip()
    assert expected != runner_namespace, (expected, runner_namespace)
    host = topology["VethPair"]["Host"]
    bridge = topology["ManagedBridge"]
    assert bridge["Name"] == network["bridge"], topology
    assert (
        f"{bridge['Namespace']['Device']}:{bridge['Namespace']['Inode']}"
        == runner_namespace
    ), bridge
    host_links = json.loads(_guest_run(runner_vm, "ip -j link show").stdout)
    host_link = next(link for link in host_links if link["ifname"] == host["Name"])
    assert host_link["ifindex"] == host["IfIndex"], host_link
    assert host_link["master"] == bridge["Name"] and "UP" in host_link["flags"], (
        host_link
    )
    links = json.loads(
        _guest_run(
            runner_vm, f"sudo -n nsenter --net={quote(path)} -- ip -j link show"
        ).stdout
    )
    expected_links = (
        topology["LocalBridge"],
        topology["TAP"],
        topology["VethPair"]["Peer"],
    )
    assert {link["ifname"] for link in links} == {
        "lo",
        *(link["Name"] for link in expected_links),
    }, links
    for identity in expected_links:
        link = next(link for link in links if link["ifname"] == identity["Name"])
        assert link["ifindex"] == identity["IfIndex"] and "UP" in link["flags"], link
    for identity in (topology["TAP"], topology["VethPair"]["Peer"]):
        link = next(link for link in links if link["ifname"] == identity["Name"])
        assert link["master"] == topology["LocalBridge"]["Name"], link
    record["observed_namespace"] = expected
    return record


def _reuse_neighbor(runner_vm: str, bridge: str, address: str) -> list[dict]:
    result = _guest_run(runner_vm, f"ip -j -4 neigh show to {quote(address)}")
    # A dev-filtered ip listing omits the device; retain its observed kernel name.
    return [entry for entry in json.loads(result.stdout) if entry.get("dev") == bridge]


def _assert_reuse_neighbor(runner_vm: str, bridge: str, address: str, mac: str) -> None:
    neighbors = _reuse_neighbor(runner_vm, bridge, address)
    assert len(neighbors) == 1, (
        f"Missing exact {bridge}/{address} neighbor: {neighbors}"
    )
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
        _guest_run(
            runner_vm,
            f"sudo -n test ! -e {quote(path)} && sudo -n test ! -L {quote(path)}",
        )
    host_names = {
        link["ifname"]
        for link in json.loads(_guest_run(runner_vm, "ip -j link show").stdout)
    }
    removed_names = {
        topology["VethPair"]["Host"]["Name"],
        topology["VethPair"]["Peer"]["Name"],
        topology["LocalBridge"]["Name"],
        topology["TAP"]["Name"],
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
    reused_ip, control_ip = (
        str(prefix.network_address + 50),
        str(prefix.network_address + 51),
    )
    assert os.getuid() != 0, (
        "Run the installed CLI journey as the enrolled non-root runner operator"
    )
    mac_seed = uuid.uuid4().hex
    mac_prefix = "02:" + ":".join(
        mac_seed[index : index + 2] for index in range(0, 8, 2)
    )
    mac_a, mac_b, mac_control = (
        f"{mac_prefix}:0a",
        f"{mac_prefix}:0b",
        f"{mac_prefix}:0c",
    )
    marker = f"gateway-{uuid.uuid4().hex}"
    (tmp_path / "probe").write_text(marker, encoding="utf-8")
    log = tmp_path / "gateway.log"
    service: subprocess.Popen[str] | None = None
    attempted_vms: list[str] = []
    vm_identities: dict[str, dict] = {}
    records: dict[str, dict] = {}
    network_attempted = False
    network: dict = {}

    def create(name: str, address: str, mac: str) -> dict:
        assert not any(
            item["name"] == name or item["id"] == name
            for item in _nft_items(runner_vm, "vm")
        ), name
        attempted_vms.append(name)
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--ip",
                address,
                "--mac",
                mac,
                "--cloud-init-mode",
                "inject",
                "--no-pci",
                "--vcpu",
                "1",
                "--mem",
                "256M",
                "--disk-size",
                "512M",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1 and created[0]["name"] == name, created
        assert re.fullmatch(r"[0-9a-f]{32}", created[0]["id"]), created
        vm_identities[name] = {
            field: created[0][field] for field in _NFT_IDENTITIES["vm"]
        }
        vm = _reuse_vm(runner_vm, name)
        assert {field: vm[field] for field in _NFT_IDENTITIES["vm"]} == vm_identities[
            name
        ], vm
        assert vm["ipv4"] == address and vm["mac"] == mac, vm
        records[name] = _reuse_journal(runner_vm, vm["id"])
        records[name] = _reuse_namespace(runner_vm, vm, network)
        return vm

    def gateway_request(name: str) -> None:
        # vsock transports only the command; the HTTP exchange uses guest eth0.
        request = "printf 'GET /probe HTTP/1.0\\r\\nHost: gateway\\r\\n\\r\\n' "
        result = _run_mvm(
            runner_vm,
            "exec",
            name,
            "--user",
            "root",
            "--timeout",
            "10",
            "--",
            request + f"| nc -w 3 {quote(network['ipv4_gateway'])} 8000",
        )
        response = result.stdout.replace("\r\n", "\n")
        assert response.startswith(("HTTP/1.0 200 ", "HTTP/1.1 200 ")), response
        assert response.split("\n\n", 1)[-1].strip() == marker, response

    try:
        # Preparation is the runner owner's job; this journey never pulls assets.
        for domain in ("kernel", "image", "bin"):
            assets = _json_success_data(
                _run_mvm(runner_vm, domain, "ls", "--json").stdout, list
            )
            if domain == "image":
                assert any(
                    asset["type"] == "alpine"
                    and asset["version"] == "3.23"
                    and asset["is_present"]
                    for asset in assets
                ), assets
            elif domain == "bin":
                for kind in ("firecracker", "jailer"):
                    assert any(
                        asset["type"] == kind
                        and asset["version"] == "1.16.2"
                        and asset["is_default"]
                        and asset["is_present"]
                        for asset in assets
                    ), assets
            else:
                assert any(
                    asset["is_default"] and asset["is_present"] for asset in assets
                ), assets
        networks_before = _nft_items(runner_vm, "network")
        assert not any(
            item["name"] == net_name or item["id"] == net_name
            for item in networks_before
        ), networks_before
        assert not any(
            prefix.overlaps(ipaddress.ip_network(item["subnet"]))
            for item in networks_before
        ), networks_before
        vms_before = _nft_items(runner_vm, "vm")
        assert not any(item["name"] in (vm_a, vm_b, control) for item in vms_before), (
            vms_before
        )
        network_attempted = True
        network = _json_success_data(
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--no-nat",
                "--json",
            ).stdout,
            dict,
        )
        assert network["name"] == net_name and re.fullmatch(
            r"[0-9a-f]{64}", network["id"]
        ), network
        assert network["id"] not in {item["id"] for item in networks_before}, network
        inspected_network = _network(runner_vm, network["id"])
        assert {
            field: inspected_network[field] for field in _NFT_IDENTITIES["network"]
        } == {field: network[field] for field in _NFT_IDENTITIES["network"]}, network
        service = subprocess.Popen(
            [
                "/usr/local/bin/mvm",
                "run",
                "nocloudnet",
                "serve",
                "--cloud-init-dir",
                str(tmp_path),
                "--network-id",
                network["id"],
                "--log-file",
                str(log),
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            assert service.poll() is None, service.communicate()
            if log.is_file() and "NoCloud server ready" in log.read_text(
                encoding="utf-8"
            ):
                break
            time.sleep(0.1)
        else:
            pytest.fail(
                "The typed NoCloud gateway service did not become ready within 10s"
            )

        create(control, control_ip, mac_control)
        gateway_request(control)
        _assert_reuse_neighbor(runner_vm, network["bridge"], control_ip, mac_control)
        a = create(vm_a, reused_ip, mac_a)
        assert (
            records[vm_a]["observed_namespace"]
            != records[control]["observed_namespace"]
        )
        gateway_request(vm_a)
        _assert_reuse_neighbor(runner_vm, network["bridge"], reused_ip, mac_a)
        removal_vm = _reuse_vm(runner_vm, vm_a)
        assert {
            field: removal_vm[field] for field in _NFT_IDENTITIES["vm"]
        } == vm_identities[vm_a]
        _run_mvm(runner_vm, "vm", "rm", a["id"], "--force", timeout=120)
        attempted_vms.remove(vm_a)
        _assert_reuse_removed(runner_vm, records[vm_a])
        # If natural teardown/GC discarded A's binding, this is missing oracle
        # evidence, not a passing IP-reuse test. Never seed an artificial entry.
        try:
            _assert_reuse_neighbor(runner_vm, network["bridge"], reused_ip, mac_a)
        except AssertionError as error:
            pytest.fail(
                f"Cannot qualify stale-IP reuse: A's old dynamic binding was not retained: {error}"
            )
        b = create(vm_b, reused_ip, mac_b)
        assert b["id"] != a["id"]
        assert records[vm_b]["endpoint_id"] != records[vm_a]["endpoint_id"]
        topology_a, topology_b = records[vm_a]["topology"], records[vm_b]["topology"]
        assert topology_b["Namespace"]["Path"] != topology_a["Namespace"]["Path"]
        assert (
            topology_b["VethPair"]["Host"]["Name"]
            != topology_a["VethPair"]["Host"]["Name"]
        )
        assert (
            records[vm_b]["observed_namespace"]
            != records[control]["observed_namespace"]
        )
        before_request = _reuse_neighbor(runner_vm, network["bridge"], reused_ip)
        assert all(entry.get("lladdr") != mac_a for entry in before_request), (
            before_request
        )
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
                    failures.append(
                        f"NoCloud shutdown: rc={service.returncode}, {stdout!r}, {stderr!r}"
                    )
            except subprocess.TimeoutExpired:
                service.kill()
                stdout, stderr = service.communicate(timeout=10)
                failures.append(f"NoCloud shutdown timed out: {stdout!r}, {stderr!r}")
            except Exception as error:
                failures.append(f"NoCloud shutdown failed: {error}")
        for name in reversed(attempted_vms):
            try:
                matches = [
                    item
                    for item in _nft_items(runner_vm, "vm")
                    if item["name"] == name
                    or item["id"] == vm_identities.get(name, {}).get("id")
                ]
                assert len(matches) <= 1, matches
                if matches:
                    assert name in vm_identities, (
                        "unresolved partial VM; no immutable receipt",
                        name,
                        matches,
                    )
                    assert {
                        field: matches[0][field] for field in _NFT_IDENTITIES["vm"]
                    } == vm_identities[name], matches
                    _run_mvm(
                        runner_vm,
                        "vm",
                        "rm",
                        vm_identities[name]["id"],
                        "--force",
                        timeout=120,
                    )
            except Exception as error:
                failures.append(f"VM {name} removal failed: {error}")
        for name, record in records.items():
            try:
                _assert_reuse_removed(runner_vm, record)
            except Exception as error:
                failures.append(f"VM {name} cleanup observation: {error}")
        try:
            remaining = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            if any(vm["name"] in (vm_a, vm_b, control) for vm in remaining):
                failures.append(f"Test VMs remain registered: {remaining}")
        except Exception as error:
            failures.append(f"VM absence observation failed: {error}")
        if network_attempted:
            try:
                matches = [
                    item
                    for item in _nft_items(runner_vm, "network")
                    if item["name"] == net_name or item["id"] == network.get("id")
                ]
                assert len(matches) <= 1, matches
                if matches:
                    assert network, (
                        "unresolved partial network; no immutable receipt",
                        matches,
                    )
                    assert {
                        field: matches[0][field] for field in _NFT_IDENTITIES["network"]
                    } == {
                        field: network[field] for field in _NFT_IDENTITIES["network"]
                    }, matches
                    _run_mvm(runner_vm, "network", "rm", network["id"])
            except Exception as error:
                failures.append(f"Network removal failed: {error}")
            try:
                networks = _json_success_data(
                    _run_mvm(runner_vm, "network", "ls", "--json").stdout, list
                )
                if any(item["name"] == net_name for item in networks):
                    failures.append(f"Network {net_name} remains registered")
                links = json.loads(_guest_run(runner_vm, "ip -j link show").stdout)
                if network and any(
                    link["ifname"] == network["bridge"] for link in links
                ):
                    failures.append(f"Managed bridge {network['bridge']} remains")
            except Exception as error:
                failures.append(f"Network absence observation failed: {error}")
        assert not failures, "IP-reuse cleanup failed:\n" + "\n".join(failures)
