"""Tier-2 traffic-policy enforcement through the installed CLI."""

from __future__ import annotations

import shlex
import time
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


def test_same_network_traffic_policy_exact_allow_and_revoke(runner_vm: str) -> None:
    """A network-source grant opens only its port between peers on one bridge."""
    suffix = uuid.uuid4().hex[:7]
    network = f"pol-same-{suffix}"
    source_vm, destination_vm = f"pol-same-src-{suffix}", f"pol-same-dst-{suffix}"
    created_networks: list[str] = []
    created_vms: list[str] = []
    policy_id: str | None = None

    try:
        ensure_vm_deps(runner_vm)
        _run_mvm(
            runner_vm, "network", "create", network, "--subnet", _unique_subnet(network)
        )
        created_networks.append(network)
        for vm_name in (source_vm, destination_vm):
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network,
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
                network,
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
        assert _guest_probe(runner_vm, source_vm, destination_ip, 8080)
        assert not _guest_probe(runner_vm, source_vm, destination_ip, 8081)

        _run_mvm(
            runner_vm, "policy", "traffic", "rm", policy_id, "--from-network", "--force"
        )
        policy_id = None
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


_UDP_RESPONDER = """
import pathlib, selectors, socket, sys, time
directory = pathlib.Path(sys.argv[1])
selector = selectors.DefaultSelector()
for port in map(int, sys.argv[2:]):
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(('0.0.0.0', port))
    selector.register(sock, selectors.EVENT_READ, port)
(directory / 'ready').touch()
deadline = time.monotonic() + 600
try:
    while time.monotonic() < deadline:
        for key, _ in selector.select(timeout=1):
            payload, peer = key.fileobj.recvfrom(512)
            with (directory / f'{key.data}.received').open('ab') as log:
                log.write(payload + b'\\n')
            key.fileobj.sendto(b'ack:' + payload, peer)
finally:
    for key in list(selector.get_map().values()):
        key.fileobj.close()
    selector.close()
"""

_UDP_REQUEST = """
import socket, sys
destination, port, local_port, nonce = sys.argv[1:]
with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
    sock.bind(('0.0.0.0', int(local_port)))
    sock.settimeout(2)
    sock.connect((destination, int(port)))
    sock.send(nonce.encode('ascii'))
    try:
        reply = sock.recv(512)
    except socket.timeout:
        print('TIMEOUT')
    else:
        assert reply == b'ack:' + nonce.encode('ascii'), repr(reply)
        print('ACK ' + nonce)
"""


@pytest.mark.timeout(720, method="signal")
def test_source_vm_udp_policy_exact_allow_direction_and_revoke(runner_vm: str) -> None:
    """An exact VM grant delivers acknowledged UDP only to its destination port."""
    suffix = uuid.uuid4().hex[:12]
    network = f"pol-udp-{suffix}"
    source, destination, sibling = (
        f"pol-udp-{role}-{suffix}" for role in ("src", "dst", "other")
    )
    directory = f"/tmp/policy-udp-{suffix}"
    vm_ids: dict[str, str | None] = {}
    network_id: str | None = None
    network_attempted = False
    policy_attempted = False
    policy_id: str | None = None
    deadline = time.monotonic() + 420
    local_port = 30000

    def run(*args: str, seconds: int = 60):
        # _run_mvm adds 30 seconds; account for that within the absolute budget.
        remaining = min(seconds, int(deadline - time.monotonic()))
        assert remaining > 30, f"UDP journey deadline reached before {args!r}"
        return _run_mvm(runner_vm, *args, timeout=remaining - 30)

    def data(*args: str, kind: type = dict):
        return _json_success_data(run(*args, "--json").stdout, kind)

    def guest(vm: str, command: str):
        return run("exec", vm, "--user", "root", "--", command)

    def request(vm: str, ip: str, port: int, nonce: str) -> str:
        nonlocal local_port
        # Every request has a fresh tuple, including reverse and post-revoke flows.
        local_port += 1
        command = shlex.join(
            ["python3", "-c", _UDP_REQUEST, ip, str(port), str(local_port), nonce]
        )
        return guest(vm, command).stdout.strip()

    def probe(vm: str, target: str, ip: str, port: int, *, allowed: bool) -> None:
        nonce = uuid.uuid4().hex
        assert request(target, ip, port, uuid.uuid4().hex).startswith("ACK "), (
            f"UDP listener unavailable on {target}:{port}"
        )
        result = request(vm, ip, port, nonce)
        expected = f"ACK {nonce}" if allowed else "TIMEOUT"
        assert result == expected, f"{vm} -> {target}:{port}: {result!r}"
        # A timeout alone could hide a delivered packet whose reply was blocked.
        received = """
import pathlib, sys
path, nonce = sys.argv[1:]
payloads = pathlib.Path(path).read_bytes().splitlines()
print('DELIVERED' if nonce.encode('ascii') in payloads else 'ABSENT')
"""
        observed = guest(
            target,
            shlex.join(
                ["python3", "-c", received, f"{directory}/{port}.received", nonce]
            ),
        ).stdout.strip()
        assert observed == ("DELIVERED" if allowed else "ABSENT"), observed
        assert request(target, ip, port, uuid.uuid4().hex).startswith("ACK "), (
            f"UDP listener disappeared on {target}:{port}"
        )

    try:
        # These reads fail closed rather than invoking helpers that pull assets.
        binaries = data("bin", "ls", kind=list)
        assert all(
            any(
                item["type"] == binary_type
                and item["version"] == "1.16.2"
                and item["is_default"]
                and item["is_present"]
                for item in binaries
            )
            for binary_type in ("firecracker", "jailer")
        ), "preseed the mirrored default Firecracker/Jailer 1.16.2 pair"
        assert any(
            item["is_default"] and item["is_present"]
            for item in data("kernel", "ls", kind=list)
        ), "preseed a default kernel"
        assert any(
            item["type"] == "alpine"
            and item["version"] == "3.23"
            and item["is_present"]
            for item in data("image", "ls", kind=list)
        ), "preseed mirrored Alpine 3.23"
        assert not any(
            item["name"] == network for item in data("network", "ls", kind=list)
        )
        assert not any(
            item["name"] in (source, destination, sibling)
            for item in data("vm", "ls", kind=list)
        )

        network_attempted = True
        created_network = data(
            "network",
            "create",
            network,
            "--subnet",
            _unique_subnet(network),
            "--no-nat",
        )
        assert created_network["name"] == network
        network_id = created_network["id"]
        for name in (source, destination, sibling):
            vm_ids[name] = None  # Include a partially completed create in teardown.
            created = _json_success_data(
                run(
                    "vm",
                    "create",
                    name,
                    "--image",
                    "alpine:3.23",
                    "--network",
                    network,
                    "--cloud-init-mode",
                    "inject",
                    "--no-pci",
                    "--disk-size",
                    "512M",
                    "--vcpu",
                    "1",
                    "--mem",
                    "256M",
                    "--json",
                    seconds=180,
                ).stdout,
                list,
            )
            assert len(created) == 1 and created[0]["name"] == name
            vm_ids[name] = created[0]["id"]

        addresses = {}
        for name in (source, destination):
            inspected = data("vm", "inspect", vm_ids[name])
            assert inspected["vm"]["id"] == vm_ids[name]
            addresses[name] = inspected["networking"]["ipv4"]
            assert addresses[name]
            ports = ["18080", "18081"] if name == destination else ["18080"]
            command = shlex.join(
                ["python3", "-u", "-c", _UDP_RESPONDER, directory, *ports]
            )
            guest(
                name,
                f"mkdir {shlex.quote(directory)} && "
                f"nohup {command} </dev/null >{shlex.quote(directory + '/server.log')} 2>&1 &",
            )
            ready = """
import pathlib, sys, time
path = pathlib.Path(sys.argv[1])
deadline = time.monotonic() + 10
while not path.is_file() and time.monotonic() < deadline:
    time.sleep(0.1)
assert path.is_file(), 'UDP responder failed to bind; inspect server.log'
"""
            guest(name, shlex.join(["python3", "-c", ready, directory + "/ready"]))

        probe(source, destination, addresses[destination], 18080, allowed=False)
        policy_attempted = True
        created_policy = data(
            "policy",
            "traffic",
            "create",
            "--from-vm",
            vm_ids[source],
            "--to-vm",
            vm_ids[destination],
            "--protocol",
            "udp",
            "--port",
            "18080",
        )
        policy_id = created_policy["id"]
        inspected_policy = data("policy", "traffic", "inspect", policy_id, "--from-vm")
        assert inspected_policy["source_vm_id"] == vm_ids[source]
        assert inspected_policy["destination_vm_id"] == vm_ids[destination]
        assert inspected_policy["protocol"] == "udp"
        assert inspected_policy["destination_port_start"] == 18080
        assert inspected_policy["destination_port_end"] == 18080
        listed = data("policy", "traffic", "ls")
        assert any(item["id"] == policy_id for item in listed["vm_to_vm"])

        probe(source, destination, addresses[destination], 18080, allowed=True)
        probe(sibling, destination, addresses[destination], 18080, allowed=False)
        probe(source, destination, addresses[destination], 18081, allowed=False)
        probe(destination, source, addresses[source], 18080, allowed=False)
        run("policy", "traffic", "rm", policy_id, "--from-vm", "--force")
        assert not any(
            item["id"] == policy_id
            for item in data("policy", "traffic", "ls")["vm_to_vm"]
        )
        policy_id = None
        probe(source, destination, addresses[destination], 18080, allowed=False)
    finally:
        deadline = time.monotonic() + 240
        errors = []
        if policy_attempted:
            try:
                for item in data("policy", "traffic", "ls")["vm_to_vm"]:
                    if (
                        item["source_vm_id"] == vm_ids[source]
                        and item["destination_vm_id"] == vm_ids[destination]
                        and item["protocol"] == "udp"
                        and item["destination_port_start"] == 18080
                        and item["destination_port_end"] == 18080
                    ):
                        assert policy_id is None or item["id"] == policy_id
                        run(
                            "policy",
                            "traffic",
                            "rm",
                            item["id"],
                            "--from-vm",
                            "--force",
                        )
                        assert not any(
                            remaining["id"] == item["id"]
                            for remaining in data("policy", "traffic", "ls")["vm_to_vm"]
                        ), f"owned UDP policy remains: {item['id']}"
            except Exception as exc:
                errors.append(f"UDP policy cleanup: {exc}")
        # Full immutable IDs pin removals; UUID names identify this test's partial creates.
        # VM removal also owns teardown of guest responders and their unique files.
        for name, expected_id in reversed(list(vm_ids.items())):
            try:
                matches = [
                    item for item in data("vm", "ls", kind=list) if item["name"] == name
                ]
                assert len(matches) <= 1, matches
                if matches:
                    actual_id = matches[0]["id"]
                    assert expected_id is None or actual_id == expected_id, matches
                    run("vm", "rm", actual_id, "--force", seconds=90)
                assert not any(
                    item["name"] == name or item["id"] == expected_id
                    for item in data("vm", "ls", kind=list)
                ), f"owned VM remains: {name}"
            except Exception as exc:
                errors.append(f"VM cleanup {name}: {exc}")
        if network_attempted:
            try:
                matches = [
                    item
                    for item in data("network", "ls", kind=list)
                    if item["name"] == network
                ]
                assert len(matches) <= 1, matches
                if matches:
                    actual_id = matches[0]["id"]
                    assert network_id is None or actual_id == network_id, matches
                    run("network", "rm", actual_id, "--force")
                assert not any(
                    item["name"] == network or item["id"] == network_id
                    for item in data("network", "ls", kind=list)
                ), f"owned network remains: {network}"
            except Exception as exc:
                errors.append(f"network cleanup {network}: {exc}")
        assert not errors, "\n".join(errors)
