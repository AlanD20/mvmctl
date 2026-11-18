"""Owned Tier-2 floating-IP journeys through the installed public CLI."""

from __future__ import annotations

import ipaddress
import json
import re
import shlex
import uuid

import pytest

from tests.system.conftest import _run_mvm
from tests.system.network.test_policies import (
    _assert_tcp_peer,
    _cleanup_tcp_resources,
    _create_tcp_network,
    _create_tcp_vm,
    _guest_probe,
    _require_tcp_preseed,
    _start_tcp_listener,
    _tcp_baseline,
    _tcp_data,
    _tcp_vm_binding,
)


class FloatingIPJourney:
    """Retain exact receipts; never adopt pre-existing resources during cleanup."""

    def __init__(self, runner):
        self.runner = runner
        self.suffix = uuid.uuid4().hex[:12]
        self.before = _tcp_baseline(runner)
        _require_tcp_preseed(self.before)
        self.networks = {}
        self.vms = {}
        self.resources = []
        self.policy_intents = []

    def network(self, label):
        return _create_tcp_network(
            self.runner, f"fip-{label}-{self.suffix}", self.networks
        )

    def vm(self, label, network):
        return _create_tcp_vm(
            self.runner, f"fip-{label}-{self.suffix}", network["id"], self.vms
        )

    def resource(self, network, label, offset=100):
        address = str(ipaddress.ip_network(network["subnet"]).network_address + offset)
        # Persist intent before the call so a committed-but-lost response is recoverable.
        intent = {"network": network, "name": label, "ipv4": address, "item": None}
        self.resources.append(intent)
        item = _tcp_data(
            self.runner, "floating-ip", "create", label, "--network", network["id"],
            "--ip", address,
        )
        assert re.fullmatch(r"[0-9a-f]{32}", item["id"]), item
        assert item == {
            "id": item["id"], "network_id": network["id"], "name": label,
            "ipv4": address, "generation": 1, "retired": False,
        }, item
        intent["item"] = item
        return item

    def grant(self, vm, resource, method, *, lease_seconds=300):
        args = [
            "floating-ip", "--network", resource["network_id"], "delegation", "grant",
            resource["id"], "--vm", vm["binding"]["id"], "--method", method,
        ]
        # This is an explicit test policy, not an operator default.
        if method in ("associate", "renew"):
            args += ["--lease", f"{lease_seconds}s"]
        item = _tcp_data(self.runner, *args)
        assert re.fullmatch(r"[0-9a-f]{32}", item["id"]), item
        assert item["vm_id"] == vm["binding"]["id"], item
        assert item["network_id"] == resource["network_id"], item
        assert item["resource_id"] == resource["id"] and item["method"] == method, item
        assert item["state"] == "active" and item["launch_generation"] > 0, item
        assert item["lease_duration_ns"] == (
            lease_seconds * 1_000_000_000 if method in ("associate", "renew") else 0
        ), item
        return item

    def control(self, vm, resource, method="inspect", generation=None, *, failure=None):
        _assert_tcp_peer(self.runner, vm)
        command = ["/usr/bin/mvm-agent", "control", "floating-ip", method]
        if generation is not None:
            command += ["--generation", str(generation)]
        command.append(resource["id"])
        result = _run_mvm(
            self.runner, "exec", vm["binding"]["id"], "--user", "root", "--",
            shlex.join(command), check=False,
        )
        _assert_tcp_peer(self.runner, vm)
        if failure is not None:
            assert result.returncode != 0, result.stdout
            assert result.stderr.strip() == f"error: guest control request failed: {failure}", (
                result.stdout, result.stderr
            )
            return None
        assert result.returncode == 0, (result.stdout, result.stderr)
        item = json.loads(result.stdout)
        assert item["resource_id"] == resource["id"] and item["ipv4"] == resource["ipv4"], item
        assert item["generation"] >= resource["generation"], item
        assert item["ownership"] in ("self", "other", "unassociated"), item
        return item

    def assert_nonroot_socket_denied(self, vm, resource):
        # Exercise the private local transport as an actual unprivileged guest UID.
        # Root success on both sides prevents a dead gateway from satisfying denial.
        self.control(vm, resource)
        script = """
import os, socket
assert os.getuid() != 0 and os.geteuid() != 0
with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
    try:
        client.connect('/run/mvm-agent/control.sock')
    except PermissionError:
        print('NONROOT_CONTROL_SOCKET_DENIED')
    else:
        raise AssertionError('non-root process connected to the private control socket')
"""
        result = _run_mvm(
            self.runner, "exec", vm["binding"]["id"], "--user", "nobody", "--",
            shlex.join(["python3", "-c", script]),
        )
        assert result.stdout.strip() == "NONROOT_CONTROL_SOCKET_DENIED", result.stdout
        self.control(vm, resource)

    def associate(self, vm, resource):
        for method in ("inspect", "associate", "release"):
            self.grant(vm, resource, method)
        observed = self.control(vm, resource)
        assert observed["ownership"] == "unassociated", observed
        item = self.control(vm, resource, "associate", observed["generation"])
        assert item["ownership"] == "self" and item["lease_remaining_ms"] > 0, item
        self.alias(vm, resource, present=True)
        return item

    def alias(self, vm, resource, *, present):
        _assert_tcp_peer(self.runner, vm)
        result = _run_mvm(
            self.runner, "exec", vm["binding"]["id"], "--user", "root", "--",
            "ip -j -4 addr show",
        )
        addresses = [
            entry["local"] for interface in json.loads(result.stdout)
            for entry in interface.get("addr_info", [])
        ]
        assert vm["binding"]["ipv4"] in addresses, addresses
        assert (resource["ipv4"] in addresses) is present, addresses

    def refresh_after_start(self, vm):
        """Keep entity binding, but explicitly observe the new process and guest boot."""
        matches = [item for item in _tcp_data(self.runner, "vm", "ls", kind=list)
                   if item["id"] == vm["binding"]["id"]]
        assert len(matches) == 1, matches
        current = matches[0]
        assert _tcp_vm_binding(current) == vm["binding"], current
        assert current["status"] == "running" and current["pid"] > 0, current
        boot = _run_mvm(
            self.runner, "exec", vm["binding"]["id"], "--user", "root", "--timeout", "30", "--",
            "cat /proc/sys/kernel/random/boot_id",
        ).stdout.strip()
        assert re.fullmatch(r"[0-9a-f-]{36}", boot), boot
        assert (current["pid"], current["process_start_time"]) != (vm["pid"], vm["process_start_time"])
        assert boot != vm["boot_id"], boot
        return {"binding": vm["binding"], "pid": current["pid"],
                "process_start_time": current["process_start_time"], "boot_id": boot}

    def stale_alias(self, vm, resource, *, previous=None):
        """Replace only this VIP with a test-owned label, or verify the retained replacement.

        Guest alias cleanup must preserve the changed unmanaged address. Keeping it
        present across denied TCP probes prevents guest cleanup from proving fencing.
        """
        _assert_tcp_peer(self.runner, vm)
        script = r"""
import json, subprocess, sys
vip, primary, label = sys.argv[1:4]
previous = json.loads(sys.argv[4])
def run(*args):
    return subprocess.check_output(['ip', *args], text=True)
def snapshot():
    interfaces = json.loads(run('-j', '-4', 'addr', 'show'))
    routes = json.loads(run('-j', '-4', 'route', 'show', 'table', 'all'))
    owners = [interface for interface in interfaces
              if any(address['local'] == primary for address in interface.get('addr_info', []))]
    assert len(owners) == 1
    interface = owners[0]
    addresses = interface.get('addr_info', [])
    aliases = [address for address in addresses if address['local'] == vip]
    assert len(aliases) == 1 and aliases[0]['prefixlen'] == 32
    return {'interface': interface['ifname'], 'ifindex': interface['ifindex'],
            'primary_addresses': [address for address in addresses if address['local'] != vip],
            'routes': sorted(routes, key=lambda route: json.dumps(route, sort_keys=True))}, aliases[0]
before, address = snapshot()
if previous is None:
    name = before['interface']
    label = name + ':' + label
    assert len(label) <= 15 and address.get('label') != label
    run('addr', 'del', vip + '/32', 'dev', name)
    run('addr', 'add', vip + '/32', 'dev', name, 'label', label, 'noprefixroute')
    after, address = snapshot()
    assert after == before, (before, after)
    previous = {'snapshot': after, 'label': label}
else:
    assert before == previous['snapshot'], (before, previous)
assert address['label'] == previous['label'], address
print(json.dumps(previous, sort_keys=True))
"""
        result = _run_mvm(
            self.runner, "exec", vm["binding"]["id"], "--user", "root", "--",
            shlex.join(["python3", "-c", script, resource["ipv4"], vm["binding"]["ipv4"],
                        "f" + self.suffix[:6], json.dumps(previous)]),
        )
        return json.loads(result.stdout)

    def listener(self, vm):
        directory = f"/tmp/floating-{self.suffix}"
        _start_tcp_listener(self.runner, vm, directory)
        return directory

    def flow(self, source, receiver, directory, address, port=8080, *, allowed):
        # Loopback health cannot be supplied by the route or alias under test.
        assert _guest_probe(self.runner, receiver, "127.0.0.1", port, uuid.uuid4().hex)
        nonce = uuid.uuid4().hex
        assert _guest_probe(self.runner, source, address, port, nonce) is allowed
        script = (
            "import pathlib,sys; "
            "print('DELIVERED' if sys.argv[2].encode() in "
            "pathlib.Path(sys.argv[1]).read_bytes().splitlines() else 'ABSENT')"
        )
        result = _run_mvm(
            self.runner, "exec", receiver["binding"]["id"], "--user", "root", "--",
            shlex.join(["python3", "-c", script, f"{directory}/{port}.received", nonce]),
        )
        assert result.stdout.strip() == ("DELIVERED" if allowed else "ABSENT"), result.stdout
        assert _guest_probe(self.runner, receiver, "127.0.0.1", port, uuid.uuid4().hex)
        return nonce

    def policy(self, source, resource, source_kind):
        flag = "--from-vm" if source_kind == "vm" else "--from-network"
        source_id = source["binding"]["id"] if source_kind == "vm" else source["id"]
        intent = (flag, source_id, resource["id"])
        self.policy_intents.append(intent)
        item = _tcp_data(
            self.runner, "policy", "traffic", "create", flag, source_id,
            "--to-floating-ip", resource["id"], "--to-network", resource["network_id"],
            "--protocol", "tcp", "--port", "8080",
        )
        assert re.fullmatch(r"[0-9a-f]{64}", item["id"]), item
        self.assert_policy(item, intent)
        return item

    def assert_policy(self, item, intent):
        flag, source_id, resource_id = intent
        inspected = _tcp_data(
            self.runner, "policy", "traffic", "inspect", item["id"], flag,
            "--to-floating-ip",
        )
        for key in ("id", "created_at"):
            assert inspected[key] == item[key], inspected
        assert inspected["source_vm_id" if flag == "--from-vm" else "source_network_id"] == source_id
        assert inspected["destination_floating_ip_id"] == resource_id, inspected
        assert inspected["protocol"] == "tcp", inspected
        assert inspected["destination_port_start"] == inspected["destination_port_end"] == 8080

    def cleanup(self):
        errors = []
        # Remove exact intent matches even if create committed without returning a receipt.
        for flag, source_id, resource_id in reversed(self.policy_intents):
            try:
                family = "vm_to_floating_ip" if flag == "--from-vm" else "network_to_floating_ip"
                source_key = "source_vm_id" if flag == "--from-vm" else "source_network_id"
                for item in _tcp_data(self.runner, "policy", "traffic", "ls")[family]:
                    if item[source_key] == source_id and item["destination_floating_ip_id"] == resource_id:
                        assert item["protocol"] == "tcp"
                        assert item["destination_port_start"] == item["destination_port_end"] == 8080
                        _run_mvm(self.runner, "policy", "traffic", "rm", item["id"], flag,
                                 "--to-floating-ip", "--force")
            except Exception as exc:
                errors.append(f"policy cleanup: {exc}")
        # VM lifecycle cleanup fences ownership and grants before retiring reservations.
        for name, (network_id, expected) in reversed(list(self.vms.items())):
            try:
                for item in _tcp_data(self.runner, "vm", "ls", kind=list):
                    if item["name"] == name:
                        receipt = _tcp_vm_binding(item)
                        assert receipt["network_id"] == network_id
                        assert expected is None or receipt == expected, receipt
                        assert item["id"] not in {vm["id"] for vm in self.before["vm"]}
                        _run_mvm(self.runner, "vm", "rm", item["id"], "--force", timeout=120)
            except Exception as exc:
                errors.append(f"VM cleanup {name}: {exc}")
        for intent in reversed(self.resources):
            try:
                network = intent["network"]
                for item in _tcp_data(self.runner, "floating-ip", "ls", "--network", network["id"], kind=list):
                    if item["name"] == intent["name"]:
                        assert item["network_id"] == network["id"] and item["ipv4"] == intent["ipv4"]
                        assert intent["item"] is None or item["id"] == intent["item"]["id"]
                        _run_mvm(self.runner, "floating-ip", "rm", item["id"], "--network",
                                 network["id"], "--force")
            except Exception as exc:
                errors.append(f"resource cleanup: {exc}")
        try:
            _cleanup_tcp_resources(
                self.runner, self.before, self.networks, self.vms, policy_attempted=False,
                source_network=None, destination=None, policy=None,
            )
        except Exception as exc:
            errors.append(str(exc))
        assert not errors, "\n".join(errors)


@pytest.fixture
def floating_journey(runner_vm):
    journey = FloatingIPJourney(runner_vm)
    try:
        yield journey
    finally:
        journey.cleanup()
