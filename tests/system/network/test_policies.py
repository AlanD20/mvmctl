"""Tier-2 traffic-policy enforcement through the installed CLI."""

from __future__ import annotations

import json
import re
import shlex
import time
import uuid
from typing import Any

import pytest

from tests.system.conftest import (
    _json_success_data,
    _run_mvm,
    _unique_subnet,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_network,
    pytest.mark.requires_kvm,
    pytest.mark.requires_network,
    pytest.mark.slow,
    pytest.mark.tier2,
]


def _tcp_data(runner_vm: str, *args: str, kind: type = dict) -> Any:
    return _json_success_data(_run_mvm(runner_vm, *args, "--json").stdout, kind)


def _tcp_baseline(runner_vm: str) -> dict[str, Any]:
    """Capture complete public inventories, defaults and configuration."""
    before = {
        domain: sorted(
            _tcp_data(runner_vm, domain, "ls", kind=list),
            key=lambda item: item["id"],
        )
        for domain in (
            "vm",
            "network",
            "volume",
            "snapshot",
            "image",
            "kernel",
            "bin",
            "key",
        )
    }
    before["config"] = _tcp_data(runner_vm, "config", "ls")
    before["traffic"] = _tcp_data(runner_vm, "policy", "traffic", "ls")
    before["exec"] = sorted(
        _tcp_data(runner_vm, "policy", "exec", "ls", kind=list),
        key=lambda item: item["id"],
    )
    return before


def _require_tcp_preseed(before: dict[str, Any]) -> None:
    """Missing runner assets fail before effects; do not pull or change defaults."""
    assert all(
        any(
            item["type"] == binary_type
            and item["version"] == "1.17.0"
            and item["is_default"]
            and item["is_present"]
            for item in before["bin"]
        )
        for binary_type in ("firecracker", "jailer")
    ), "preseed the mirrored default Firecracker/Jailer 1.17.0 pair"
    assert any(
        item["is_default"] and item["is_present"] for item in before["kernel"]
    ), "preseed a default kernel"
    assert any(
        item["type"] == "alpine" and item["version"] == "3.23" and item["is_present"]
        for item in before["image"]
    ), "preseed mirrored Alpine 3.23"


def _tcp_network_binding(item: dict[str, Any]) -> dict[str, Any]:
    assert re.fullmatch(r"[0-9a-f]{64}", item["id"]), item
    assert item["name"] and item["created_at"], item
    return {
        key: item[key]
        for key in (
            "id",
            "name",
            "created_at",
            "subnet",
            "ipv4_gateway",
            "bridge",
            "nat_enabled",
        )
    }


def _tcp_vm_binding(item: dict[str, Any]) -> dict[str, Any]:
    assert re.fullmatch(r"[0-9a-f]{32}", item["id"]), item
    assert item["name"] and item["created_at"], item
    assert re.fullmatch(r"[0-9a-f]{64}", item["network_id"]), item
    return {
        key: item[key]
        for key in ("id", "name", "created_at", "network_id", "ipv4", "mac")
    }


def _create_tcp_network(
    runner_vm: str, name: str, attempts: dict[str, dict[str, Any] | None]
) -> dict[str, Any]:
    assert not any(
        item["name"] == name
        for item in _tcp_data(runner_vm, "network", "ls", kind=list)
    ), name
    attempts[name] = None  # A failed create may already have published a resource.
    created = _tcp_data(
        runner_vm,
        "network",
        "create",
        name,
        "--subnet",
        _unique_subnet(name),
        "--no-nat",
    )
    receipt = _tcp_network_binding(created)
    attempts[name] = receipt
    assert receipt["name"] == name and receipt["subnet"] == _unique_subnet(name), (
        created
    )
    assert receipt["nat_enabled"] is False, created
    assert receipt["bridge"], created
    return receipt


def _create_tcp_vm(
    runner_vm: str,
    name: str,
    network_id: str,
    attempts: dict[str, tuple[str, dict[str, Any] | None]],
) -> dict[str, Any]:
    assert not any(
        item["name"] == name for item in _tcp_data(runner_vm, "vm", "ls", kind=list)
    ), name
    attempts[name] = (network_id, None)
    created = _json_success_data(
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            name,
            "--image",
            "alpine:3.23",
            "--network",
            network_id,
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
            timeout=180,
        ).stdout,
        list,
    )
    assert len(created) == 1, created
    receipt = _tcp_vm_binding(created[0])
    attempts[name] = (network_id, receipt)
    assert receipt["name"] == name and receipt["network_id"] == network_id, created
    current = _tcp_data(runner_vm, "vm", "inspect", receipt["id"])
    assert current["vm"]["id"] == receipt["id"], current
    assert current["vm"]["created_at"] == receipt["created_at"], current
    assert current["networking"]["network"]["id"] == network_id, current
    assert current["networking"]["ipv4"] == receipt["ipv4"] and receipt["ipv4"], current
    assert current["networking"]["mac"] == receipt["mac"], current
    matches = [
        item
        for item in _tcp_data(runner_vm, "vm", "ls", kind=list)
        if item["id"] == receipt["id"] or item["name"] == name
    ]
    assert len(matches) == 1 and _tcp_vm_binding(matches[0]) == receipt, matches
    live = matches[0]
    assert live["status"] == "running" and live["pid"] > 0, live
    assert (
        isinstance(live["process_start_time"], int) and live["process_start_time"] > 0
    ), live
    # A running Firecracker process can precede guest-agent startup.
    boot_id = _run_mvm(
        runner_vm,
        "exec",
        receipt["id"],
        "--user",
        "root",
        "--timeout",
        "30",
        "--",
        "cat /proc/sys/kernel/random/boot_id",
        timeout=15,
    ).stdout.strip()
    assert re.fullmatch(r"[0-9a-f-]{36}", boot_id), boot_id
    return {
        "binding": receipt,
        "pid": live["pid"],
        "process_start_time": live["process_start_time"],
        "boot_id": boot_id,
    }


def _assert_tcp_peer(runner_vm: str, peer: dict[str, Any]) -> None:
    receipt = peer["binding"]
    matches = [
        item
        for item in _tcp_data(runner_vm, "vm", "ls", kind=list)
        if item["id"] == receipt["id"] or item["name"] == receipt["name"]
    ]
    assert len(matches) == 1 and _tcp_vm_binding(matches[0]) == receipt, matches
    assert matches[0]["status"] == "running", matches
    assert matches[0]["pid"] == peer["pid"], matches
    assert matches[0]["process_start_time"] == peer["process_start_time"], matches


_TCP_RESPONDER = """
import pathlib, selectors, socket, sys, time
directory = pathlib.Path(sys.argv[1])
selector = selectors.DefaultSelector()
for port in map(int, sys.argv[2:]):
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(('0.0.0.0', port))
    sock.listen(8)
    selector.register(sock, selectors.EVENT_READ, port)
    (directory / f'{port}.received').touch()
(directory / 'ready').touch()
deadline = time.monotonic() + 600
try:
    while time.monotonic() < deadline:
        for key, _ in selector.select(timeout=1):
            connection, _ = key.fileobj.accept()
            with connection:
                connection.settimeout(2)
                payload = b''
                while not payload.endswith(b'\\n'):
                    chunk = connection.recv(64)
                    assert chunk and len(payload) + len(chunk) <= 64, repr(payload)
                    payload += chunk
                with (directory / f'{key.data}.received').open('ab') as log:
                    log.write(payload)
                connection.sendall(b'ack:' + payload)
finally:
    for key in list(selector.get_map().values()):
        key.fileobj.close()
    selector.close()
"""

_TCP_REQUEST = """
import json, pathlib, socket, sys
destination, port, nonce = sys.argv[1:]
boot_id = pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
print('TCP_PROBE_STARTED ' + nonce, flush=True)
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.settimeout(2)
    try:
        sock.connect((destination, int(port)))
    except socket.timeout:
        outcome = 'connect_timeout'
    else:
        payload = nonce.encode('ascii') + b'\\n'
        sock.sendall(payload)
        reply = b''
        while not reply.endswith(b'\\n'):
            chunk = sock.recv(64)
            assert chunk and len(reply) + len(chunk) <= 64, repr(reply)
            reply += chunk
        assert reply == b'ack:' + payload, repr(reply)
        outcome = 'ack'
print(json.dumps({
    'nonce': nonce, 'boot_id': boot_id, 'destination': destination,
    'port': int(port), 'outcome': outcome,
}, sort_keys=True))
"""


def _start_tcp_listener(
    runner_vm: str, destination: dict[str, Any], directory: str
) -> None:
    _assert_tcp_peer(runner_vm, destination)
    command = shlex.join(
        ["python3", "-u", "-c", _TCP_RESPONDER, directory, "8080", "8081"]
    )
    _run_mvm(
        runner_vm,
        "exec",
        destination["binding"]["id"],
        "--user",
        "root",
        "--",
        f"mkdir {shlex.quote(directory)} && "
        f"nohup {command} </dev/null >{shlex.quote(directory + '/server.log')} 2>&1 &",
        timeout=15,
    )
    ready = """
import pathlib, sys, time
path = pathlib.Path(sys.argv[1])
deadline = time.monotonic() + 10
while not path.is_file() and time.monotonic() < deadline:
    time.sleep(0.1)
assert path.is_file(), 'TCP responder failed to bind; inspect server.log'
"""
    _run_mvm(
        runner_vm,
        "exec",
        destination["binding"]["id"],
        "--user",
        "root",
        "--",
        shlex.join(["python3", "-c", ready, directory + "/ready"]),
        timeout=15,
    )


def _guest_probe(
    runner_vm: str, source: dict[str, Any], destination: str, port: int, nonce: str
) -> bool:
    """Only a completed guest connect timeout is denial; exec failures raise."""
    _assert_tcp_peer(runner_vm, source)
    result = _run_mvm(
        runner_vm,
        "exec",
        source["binding"]["id"],
        "--user",
        "root",
        "--",
        shlex.join(["python3", "-c", _TCP_REQUEST, destination, str(port), nonce]),
        timeout=15,
    )
    lines = result.stdout.splitlines()
    assert len(lines) == 2 and lines[0] == f"TCP_PROBE_STARTED {nonce}", result.stdout
    observed = json.loads(lines[1])
    outcome = observed["outcome"]
    assert outcome in ("ack", "connect_timeout"), observed
    assert observed == {
        "nonce": nonce,
        "boot_id": source["boot_id"],
        "destination": destination,
        "port": port,
        "outcome": outcome,
    }, observed
    return outcome == "ack"


def _assert_tcp_flow(
    runner_vm: str,
    source: dict[str, Any],
    destination: dict[str, Any],
    directory: str,
    port: int,
    *,
    allowed: bool,
) -> None:
    ip = destination["binding"]["ipv4"]
    assert _guest_probe(runner_vm, destination, ip, port, uuid.uuid4().hex), (
        f"destination listener unavailable on {ip}:{port}"
    )
    nonce = uuid.uuid4().hex
    assert _guest_probe(runner_vm, source, ip, port, nonce) is allowed
    # A positive ACK proves replies; the independent log proves actual delivery.
    received = """
import pathlib, sys
path, nonce = sys.argv[1:]
payloads = pathlib.Path(path).read_bytes().splitlines()
print('DELIVERED' if nonce.encode('ascii') in payloads else 'ABSENT')
"""
    _assert_tcp_peer(runner_vm, destination)
    observed = _run_mvm(
        runner_vm,
        "exec",
        destination["binding"]["id"],
        "--user",
        "root",
        "--",
        shlex.join(["python3", "-c", received, f"{directory}/{port}.received", nonce]),
        timeout=15,
    ).stdout.strip()
    assert observed == ("DELIVERED" if allowed else "ABSENT"), observed
    assert _guest_probe(runner_vm, destination, ip, port, uuid.uuid4().hex), (
        f"destination listener disappeared on {ip}:{port}"
    )


def _assert_tcp_policy(
    runner_vm: str,
    policy: dict[str, Any],
    source_network: dict[str, Any],
    destination: dict[str, Any],
    *,
    same_network: bool,
) -> None:
    assert re.fullmatch(r"[0-9a-f]{64}", policy["id"]), policy
    assert policy["created_at"], policy
    expected = {
        "id": policy["id"],
        "created_at": policy["created_at"],
        "source_network_id": source_network["id"],
        "source_network_name": source_network["name"],
        "destination_vm_id": destination["binding"]["id"],
        "destination_vm_name": destination["binding"]["name"],
        "destination_network_id": destination["binding"]["network_id"],
        "network_relation": "same-network" if same_network else "cross-network",
        "protocol": "tcp",
        "destination_port_start": 8080,
        "destination_port_end": 8080,
    }
    inspected = _tcp_data(
        runner_vm, "policy", "traffic", "inspect", policy["id"], "--from-network"
    )
    assert all(inspected[key] == value for key, value in expected.items()), inspected
    listed = _tcp_data(runner_vm, "policy", "traffic", "ls")
    assert isinstance(listed["vm_to_vm"], list), listed
    matches = [item for item in listed["network_to_vm"] if item["id"] == policy["id"]]
    assert len(matches) == 1, matches
    assert all(matches[0][key] == value for key, value in expected.items()), matches


def _cleanup_tcp_resources(
    runner_vm: str,
    before: dict[str, Any],
    networks: dict[str, dict[str, Any] | None],
    vms: dict[str, tuple[str, dict[str, Any] | None]],
    *,
    policy_attempted: bool,
    source_network: dict[str, Any] | None,
    destination: dict[str, Any] | None,
    policy: dict[str, Any] | None,
) -> None:
    errors = []
    if policy_attempted:
        try:
            assert source_network is not None and destination is not None
            matches = [
                item
                for item in _tcp_data(runner_vm, "policy", "traffic", "ls")[
                    "network_to_vm"
                ]
                if item["source_network_id"] == source_network["id"]
                and item["destination_vm_id"] == destination["binding"]["id"]
                and item["protocol"] == "tcp"
                and item["destination_port_start"] == 8080
                and item["destination_port_end"] == 8080
            ]
            assert len(matches) <= 1, matches
            for item in matches:
                assert re.fullmatch(r"[0-9a-f]{64}", item["id"]), item
                assert policy is None or (
                    item["id"] == policy["id"]
                    and item["created_at"] == policy["created_at"]
                ), item
                _run_mvm(
                    runner_vm,
                    "policy",
                    "traffic",
                    "rm",
                    item["id"],
                    "--from-network",
                    "--force",
                )
                assert not any(
                    remaining["id"] == item["id"]
                    for remaining in _tcp_data(runner_vm, "policy", "traffic", "ls")[
                        "network_to_vm"
                    ]
                ), f"owned TCP policy remains: {item['id']}"
        except Exception as exc:
            errors.append(f"TCP policy cleanup: {exc}")
    for name, (network_id, expected) in reversed(list(vms.items())):
        try:
            matches = [
                item
                for item in _tcp_data(runner_vm, "vm", "ls", kind=list)
                if item["name"] == name
                or (expected is not None and item["id"] == expected["id"])
            ]
            assert len(matches) <= 1, matches
            if matches:
                receipt = _tcp_vm_binding(matches[0])
                assert (
                    receipt["name"] == name and receipt["network_id"] == network_id
                ), receipt
                assert expected is None or receipt == expected, receipt
                assert receipt["id"] not in {item["id"] for item in before["vm"]}, (
                    receipt
                )
                expected = receipt  # Pin a partial create before exact-ID removal.
                _run_mvm(runner_vm, "vm", "rm", receipt["id"], "--force", timeout=120)
            assert not any(
                item["name"] == name
                or (expected is not None and item["id"] == expected["id"])
                for item in _tcp_data(runner_vm, "vm", "ls", kind=list)
            ), f"owned VM remains: {name}"
        except Exception as exc:
            errors.append(f"VM cleanup {name}: {exc}")
    for name, expected in reversed(list(networks.items())):
        try:
            matches = [
                item
                for item in _tcp_data(runner_vm, "network", "ls", kind=list)
                if item["name"] == name
                or (expected is not None and item["id"] == expected["id"])
            ]
            assert len(matches) <= 1, matches
            if matches:
                receipt = _tcp_network_binding(matches[0])
                assert receipt["name"] == name and receipt["subnet"] == _unique_subnet(
                    name
                ), receipt
                assert receipt["nat_enabled"] is False, receipt
                assert expected is None or receipt == expected, receipt
                assert receipt["id"] not in {
                    item["id"] for item in before["network"]
                }, receipt
                expected = receipt
                _run_mvm(
                    runner_vm, "network", "rm", receipt["id"], "--force", timeout=60
                )
            assert not any(
                item["name"] == name
                or (expected is not None and item["id"] == expected["id"])
                for item in _tcp_data(runner_vm, "network", "ls", kind=list)
            ), f"owned network remains: {name}"
        except Exception as exc:
            errors.append(f"network cleanup {name}: {exc}")
    try:
        after = _tcp_baseline(runner_vm)
        assert after == before, {
            key: {"before": before[key], "after": after[key]}
            for key in before
            if after[key] != before[key]
        }
    except Exception as exc:
        errors.append(f"TCP journey baseline restoration: {exc}")
    assert not errors, "\n".join(errors)


@pytest.mark.timeout(720, method="signal")
def test_cross_network_traffic_policy_exact_allow_and_revoke(runner_vm: str) -> None:
    """A network-source grant opens one destination TCP port, then closes it."""
    suffix = uuid.uuid4().hex[:12]
    source_net, destination_net = f"pol-src-{suffix}", f"pol-dst-{suffix}"
    source_vm, destination_vm = f"pol-src-vm-{suffix}", f"pol-dst-vm-{suffix}"
    directory = f"/tmp/policy-tcp-{suffix}"
    before = _tcp_baseline(runner_vm)
    _require_tcp_preseed(before)
    assert not any(
        item["name"] in (source_net, destination_net) for item in before["network"]
    )
    assert not any(item["name"] in (source_vm, destination_vm) for item in before["vm"])
    networks: dict[str, dict[str, Any] | None] = {}
    vms: dict[str, tuple[str, dict[str, Any] | None]] = {}
    source_network = destination = policy = None
    policy_attempted = False
    try:
        source_network = _create_tcp_network(runner_vm, source_net, networks)
        destination_network = _create_tcp_network(runner_vm, destination_net, networks)
        source = _create_tcp_vm(runner_vm, source_vm, source_network["id"], vms)
        destination = _create_tcp_vm(
            runner_vm, destination_vm, destination_network["id"], vms
        )
        _start_tcp_listener(runner_vm, destination, directory)
        for port in (8080, 8081):
            assert _guest_probe(
                runner_vm, destination, "127.0.0.1", port, uuid.uuid4().hex
            )
        _assert_tcp_flow(runner_vm, source, destination, directory, 8080, allowed=False)
        policy_attempted = True
        policy = _tcp_data(
            runner_vm,
            "policy",
            "traffic",
            "create",
            "--from-network",
            source_network["id"],
            "--to-vm",
            destination["binding"]["id"],
            "--protocol",
            "tcp",
            "--port",
            "8080",
        )
        _assert_tcp_policy(
            runner_vm, policy, source_network, destination, same_network=False
        )
        _assert_tcp_flow(runner_vm, source, destination, directory, 8080, allowed=True)
        _assert_tcp_flow(runner_vm, source, destination, directory, 8081, allowed=False)
        _run_mvm(
            runner_vm,
            "policy",
            "traffic",
            "rm",
            policy["id"],
            "--from-network",
            "--force",
        )
        assert not any(
            item["source_network_id"] == source_network["id"]
            and item["destination_vm_id"] == destination["binding"]["id"]
            for item in _tcp_data(runner_vm, "policy", "traffic", "ls")["network_to_vm"]
        )
        _assert_tcp_flow(runner_vm, source, destination, directory, 8080, allowed=False)
    finally:
        _cleanup_tcp_resources(
            runner_vm,
            before,
            networks,
            vms,
            policy_attempted=policy_attempted,
            source_network=source_network,
            destination=destination,
            policy=policy,
        )


@pytest.mark.timeout(720, method="signal")
def test_same_network_traffic_policy_exact_allow_and_revoke(runner_vm: str) -> None:
    """A network-source grant opens only its port between peers on one bridge."""
    suffix = uuid.uuid4().hex[:12]
    network = f"pol-same-{suffix}"
    source_vm, destination_vm = f"pol-same-src-{suffix}", f"pol-same-dst-{suffix}"
    directory = f"/tmp/policy-tcp-{suffix}"
    before = _tcp_baseline(runner_vm)
    _require_tcp_preseed(before)
    assert not any(item["name"] == network for item in before["network"])
    assert not any(item["name"] in (source_vm, destination_vm) for item in before["vm"])
    networks: dict[str, dict[str, Any] | None] = {}
    vms: dict[str, tuple[str, dict[str, Any] | None]] = {}
    source_network = destination = policy = None
    policy_attempted = False
    try:
        source_network = _create_tcp_network(runner_vm, network, networks)
        source = _create_tcp_vm(runner_vm, source_vm, source_network["id"], vms)
        destination = _create_tcp_vm(
            runner_vm, destination_vm, source_network["id"], vms
        )
        _start_tcp_listener(runner_vm, destination, directory)
        _assert_tcp_flow(runner_vm, source, destination, directory, 8080, allowed=False)
        policy_attempted = True
        policy = _tcp_data(
            runner_vm,
            "policy",
            "traffic",
            "create",
            "--from-network",
            source_network["id"],
            "--to-vm",
            destination["binding"]["id"],
            "--protocol",
            "tcp",
            "--port",
            "8080",
        )
        _assert_tcp_policy(
            runner_vm, policy, source_network, destination, same_network=True
        )
        _assert_tcp_flow(runner_vm, source, destination, directory, 8080, allowed=True)
        _assert_tcp_flow(runner_vm, source, destination, directory, 8081, allowed=False)
        _run_mvm(
            runner_vm,
            "policy",
            "traffic",
            "rm",
            policy["id"],
            "--from-network",
            "--force",
        )
        assert not any(
            item["id"] == policy["id"]
            for item in _tcp_data(runner_vm, "policy", "traffic", "ls")["network_to_vm"]
        )
        _assert_tcp_flow(runner_vm, source, destination, directory, 8080, allowed=False)
    finally:
        _cleanup_tcp_resources(
            runner_vm,
            before,
            networks,
            vms,
            policy_attempted=policy_attempted,
            source_network=source_network,
            destination=destination,
            policy=policy,
        )


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
                and item["version"] == "1.17.0"
                and item["is_default"]
                and item["is_present"]
                for item in binaries
            )
            for binary_type in ("firecracker", "jailer")
        ), "preseed the mirrored default Firecracker/Jailer 1.17.0 pair"
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
