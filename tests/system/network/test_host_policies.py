"""Installed-candidate gateway grants; real sockets and independently observed delivery."""

from __future__ import annotations

import json
import os
import selectors
import shlex
import socket
import subprocess
import threading
import time
import uuid
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Iterator

import pytest

from tests.system.conftest import _run_mvm
from tests.system.network.test_policies import (
    _assert_tcp_peer,
    _cleanup_tcp_resources,
    _create_tcp_network,
    _create_tcp_vm,
    _require_tcp_preseed,
    _start_tcp_listener,
    _tcp_baseline,
    _tcp_data,
    _tcp_vm_binding,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_network,
    pytest.mark.requires_kvm,
    pytest.mark.requires_network,
    pytest.mark.slow,
    pytest.mark.tier2,
]


_HOST_REQUEST = """
import json, pathlib, socket, sys
address, port, protocol, source_port, nonce = sys.argv[1:]
payload = nonce.encode('ascii') + b'\\n'
kind = socket.SOCK_STREAM if protocol == 'tcp' else socket.SOCK_DGRAM
with socket.socket(socket.AF_INET, kind) as connection:
    connection.settimeout(2)
    connection.bind(('0.0.0.0', int(source_port)))
    try:
        connection.connect((address, int(port)))
        connection.sendall(payload)
        reply = connection.recv(128)
        if protocol == 'tcp':
            while not reply.endswith(b'\\n'):
                chunk = connection.recv(128)
                assert chunk and len(reply) + len(chunk) <= 128, repr(reply)
                reply += chunk
        assert reply == b'ack:' + payload, repr(reply)
        outcome = 'ack'
    except socket.timeout:
        outcome = 'timeout'
    except ConnectionRefusedError:
        outcome = 'refused'
print(json.dumps({'nonce': nonce, 'address': address, 'port': int(port),
                  'protocol': protocol, 'outcome': outcome,
                  'boot_id': pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()}))
"""


class _HostResponder:
    """Own the exact listener sockets, worker and received payloads for one test."""

    def __init__(self) -> None:
        self.selector = selectors.DefaultSelector()
        self.received: set[bytes] = set()
        self.lock = threading.Lock()
        self.stop = threading.Event()
        self.errors: list[Exception] = []
        self.sockets: list[socket.socket] = []
        self.thread: threading.Thread | None = None
        self.port = 0
        self.loopback_port = 0

    def bind(self) -> None:
        # Reserve both protocols and the adjacent port, rather than assuming a guessed port is free.
        for _ in range(16):
            opened: list[socket.socket] = []
            try:
                first = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
                opened.append(first)
                first.bind(("0.0.0.0", 0))
                port = first.getsockname()[1]
                if port == 65535:
                    first.close()
                    continue
                first.listen(8)
                for kind, selected in (
                    (socket.SOCK_STREAM, port + 1),
                    (socket.SOCK_DGRAM, port),
                    (socket.SOCK_DGRAM, port + 1),
                ):
                    listener = socket.socket(socket.AF_INET, kind)
                    opened.append(listener)
                    listener.bind(("0.0.0.0", selected))
                    if kind == socket.SOCK_STREAM:
                        listener.listen(8)
                self.port = port
                self.sockets = opened
                break
            except OSError:
                for listener in opened:
                    listener.close()
        assert self.sockets, "could not reserve a pair of TCP/UDP ports"
        loopback = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sockets.append(loopback)
        loopback.bind(("127.0.0.1", 0))
        loopback.listen(8)
        self.loopback_port = loopback.getsockname()[1]
        for listener in self.sockets:
            self.selector.register(listener, selectors.EVENT_READ)
        self.thread = threading.Thread(target=self.serve, daemon=True)
        self.thread.start()

    def serve(self) -> None:
        try:
            while not self.stop.is_set():
                for key, _ in self.selector.select(timeout=0.1):
                    listener = key.fileobj
                    if listener.type == socket.SOCK_DGRAM:
                        payload, peer = listener.recvfrom(128)
                        with self.lock:
                            self.received.add(payload)
                        listener.sendto(b"ack:" + payload, peer)
                    else:
                        connection, _ = listener.accept()
                        with connection:
                            connection.settimeout(2)
                            payload = b""
                            while not payload.endswith(b"\n"):
                                chunk = connection.recv(128)
                                assert chunk and len(payload) + len(chunk) <= 128
                                payload += chunk
                            with self.lock:
                                self.received.add(payload)
                            connection.sendall(b"ack:" + payload)
        except Exception as exc:
            self.errors.append(exc)

    def control(self, address: str, port: int, protocol: str) -> None:
        """A timeout is evidence only while the actual destination listener is healthy."""
        kind = socket.SOCK_STREAM if protocol == "tcp" else socket.SOCK_DGRAM
        payload = uuid.uuid4().hex.encode("ascii") + b"\n"
        with socket.socket(socket.AF_INET, kind) as connection:
            connection.settimeout(2)
            connection.connect((address, port))
            connection.sendall(payload)
            reply = connection.recv(128)
            if protocol == "tcp":
                while not reply.endswith(b"\n"):
                    chunk = connection.recv(128)
                    assert chunk and len(reply) + len(chunk) <= 128, repr(reply)
                    reply += chunk
            assert reply == b"ack:" + payload, reply
        assert not self.errors, self.errors

    def close(self) -> None:
        self.stop.set()
        try:
            if self.thread is not None:
                self.thread.join(timeout=4)
                assert not self.thread.is_alive(), "owned responder did not stop"
        finally:
            for listener in self.sockets:
                listener.close()
            self.selector.close()
        assert not self.errors, self.errors


@contextmanager
def _host_responder() -> Iterator[_HostResponder]:
    server = _HostResponder()
    try:
        server.bind()
        yield server
    finally:
        server.close()


def _host_grant(
    runner_vm: str, source_kind: str, source: str, protocol: str, port: str
) -> dict[str, Any]:
    item = _tcp_data(
        runner_vm,
        "policy",
        "traffic",
        "create",
        f"--from-{source_kind}",
        source,
        "--to-host",
        "--protocol",
        protocol,
        "--port",
        port,
    )
    first, _, last = port.partition("-")
    assert item[f"source_{source_kind}_id"] == source, item
    assert item["protocol"] == protocol, item
    assert item["destination_port_start"] == int(first), item
    assert item["destination_port_end"] == int(last or first), item
    inspected = _tcp_data(
        runner_vm,
        "policy",
        "traffic",
        "inspect",
        item["id"],
        f"--from-{source_kind}",
        "--to-host",
    )
    assert inspected == item, (item, inspected)
    return item


def _assert_nocloud_slot(
    runner_vm: str,
    network: dict[str, Any],
    peers: tuple[dict[str, Any], ...],
    directory: Path,
) -> None:
    """Exercise the existing fixed service while an independent user host grant remains installed."""
    directory.mkdir()
    marker = uuid.uuid4().hex
    (directory / "probe").write_text(marker, encoding="ascii")
    log = directory / "service.log"
    service = subprocess.Popen(
        [
            "/usr/local/bin/mvm",
            "run",
            "nocloudnet",
            "serve",
            "--network-id",
            network["id"],
            "--cloud-init-dir",
            str(directory),
            "--log-file",
            str(log),
            "--kill-after",
            "1m",
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env={**os.environ, "NO_COLOR": "1"},
    )
    try:
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            assert service.poll() is None, service.communicate()
            if log.is_file() and "NoCloud server ready" in log.read_text(
                encoding="utf-8"
            ):
                break
            time.sleep(0.1)
        else:
            pytest.fail("fixed NoCloud service failed to become ready")
        for peer in peers:
            result = _run_mvm(
                runner_vm,
                "exec",
                peer["binding"]["id"],
                "--user",
                "root",
                "--timeout",
                "30",
                "--",
                "printf 'GET /probe HTTP/1.0\\r\\nHost: gateway\\r\\n\\r\\n' | "
                f"nc -w 3 {shlex.quote(network['ipv4_gateway'])} 8000",
            )
            response = result.stdout.replace("\r\n", "\n")
            assert response.startswith(("HTTP/1.0 200 ", "HTTP/1.1 200 ")), response
            assert response.split("\n\n", 1)[-1].strip() == marker, response
    finally:
        if service.poll() is None:
            service.terminate()
        try:
            stdout, stderr = service.communicate(timeout=40)
        except subprocess.TimeoutExpired:
            service.kill()
            stdout, stderr = service.communicate(timeout=10)
            pytest.fail(f"owned NoCloud service did not stop: {stdout!r}, {stderr!r}")
        assert service.returncode == 0, (stdout, stderr)


@pytest.mark.timeout(900, method="signal")
def test_gateway_grants_restrict_source_destination_protocol_and_survive_restart(
    runner_vm: str,
    tmp_path: Path,
) -> None:
    before = _tcp_baseline(runner_vm)
    _require_tcp_preseed(before)
    networks: dict[str, dict[str, Any] | None] = {}
    vms: dict[str, tuple[str, dict[str, Any] | None]] = {}
    policies: list[tuple[str, dict[str, Any]]] = []
    workflows_before = _tcp_data(runner_vm, "env", "ls", kind=list)
    workflow_path = tmp_path / "host-policy.yaml"
    workflow_attempted = False
    suffix = uuid.uuid4().hex[:12]
    source_port = 40000
    try:
        source_network = _create_tcp_network(runner_vm, f"host-src-{suffix}", networks)
        other_network = _create_tcp_network(runner_vm, f"host-other-{suffix}", networks)
        source = _create_tcp_vm(
            runner_vm, f"host-vm-{suffix}", source_network["id"], vms
        )
        sibling = _create_tcp_vm(
            runner_vm, f"host-sibling-{suffix}", source_network["id"], vms
        )
        foreign = _create_tcp_vm(
            runner_vm, f"host-foreign-{suffix}", other_network["id"], vms
        )
        gateway = source_network["ipv4_gateway"]
        other_gateway = other_network["ipv4_gateway"]
        with _host_responder() as server:

            def probe(
                peer: dict[str, Any],
                address: str,
                port: int,
                protocol: str,
                expected: str,
                *,
                control_address: str | None = None,
            ) -> None:
                nonlocal source_port
                _assert_tcp_peer(runner_vm, peer)
                server.control(control_address or address, port, protocol)
                source_port += 1
                nonce = uuid.uuid4().hex
                result = _run_mvm(
                    runner_vm,
                    "exec",
                    peer["binding"]["id"],
                    "--user",
                    "root",
                    "--timeout",
                    "30",
                    "--",
                    shlex.join(
                        [
                            "python3",
                            "-c",
                            _HOST_REQUEST,
                            address,
                            str(port),
                            protocol,
                            str(source_port),
                            nonce,
                        ]
                    ),
                )
                observed = json.loads(result.stdout)
                assert observed == {
                    "nonce": nonce,
                    "address": address,
                    "port": port,
                    "protocol": protocol,
                    "outcome": expected,
                    "boot_id": peer["boot_id"],
                }, observed
                with server.lock:
                    assert ((nonce.encode("ascii") + b"\n") in server.received) == (
                        expected == "ack"
                    )
                server.control(control_address or address, port, protocol)

            probe(source, gateway, server.port, "tcp", "timeout")
            vm_grant = _host_grant(
                runner_vm, "vm", source["binding"]["id"], "tcp", str(server.port)
            )
            policies.append(("vm", vm_grant))
            assert vm_grant["gateway"] == gateway, vm_grant
            probe(source, gateway, server.port, "tcp", "ack")
            _assert_nocloud_slot(
                runner_vm, source_network, (source, sibling), tmp_path / "nocloud"
            )
            probe(source, gateway, server.port, "tcp", "ack")
            probe(sibling, gateway, server.port, "tcp", "timeout")
            probe(source, gateway, server.port + 1, "tcp", "timeout")
            probe(source, gateway, server.port, "udp", "timeout")
            probe(source, other_gateway, server.port, "tcp", "timeout")
            duplicate = _host_grant(
                runner_vm, "vm", source["binding"]["id"], "tcp", str(server.port)
            )
            assert (
                duplicate["id"] == vm_grant["id"]
                and duplicate["incarnation"] == vm_grant["incarnation"]
            )

            _run_mvm(runner_vm, "vm", "stop", source["binding"]["id"])
            _run_mvm(runner_vm, "vm", "start", source["binding"]["id"], timeout=180)
            live = next(
                item
                for item in _tcp_data(runner_vm, "vm", "ls", kind=list)
                if item["id"] == source["binding"]["id"]
            )
            assert _tcp_vm_binding(live) == source["binding"], live
            new_boot = _run_mvm(
                runner_vm,
                "exec",
                live["id"],
                "--user",
                "root",
                "--timeout",
                "30",
                "--",
                "cat /proc/sys/kernel/random/boot_id",
            ).stdout.strip()
            assert new_boot != source["boot_id"], new_boot
            source.update(
                pid=live["pid"],
                process_start_time=live["process_start_time"],
                boot_id=new_boot,
            )
            probe(source, gateway, server.port, "tcp", "ack")
            _run_mvm(
                runner_vm,
                "policy",
                "traffic",
                "rm",
                vm_grant["id"],
                "--from-vm",
                "--to-host",
                "--force",
            )
            probe(source, gateway, server.port, "tcp", "timeout")

            network_grant = _host_grant(
                runner_vm,
                "network",
                source_network["id"],
                "udp",
                f"{server.port}-{server.port + 1}",
            )
            policies.append(("network", network_grant))
            for peer in (source, sibling):
                for port in (server.port, server.port + 1):
                    probe(peer, gateway, port, "udp", "ack")
            probe(foreign, other_gateway, server.port, "udp", "timeout")
            probe(source, other_gateway, server.port, "udp", "timeout")
            probe(source, gateway, server.port, "tcp", "timeout")
            _run_mvm(
                runner_vm,
                "policy",
                "traffic",
                "rm",
                network_grant["id"],
                "--from-network",
                "--to-host",
                "--force",
            )
            probe(sibling, gateway, server.port, "udp", "timeout")

            localhost_grant = _host_grant(
                runner_vm,
                "vm",
                source["binding"]["id"],
                "tcp",
                str(server.loopback_port),
            )
            policies.append(("vm", localhost_grant))
            probe(
                source,
                gateway,
                server.loopback_port,
                "tcp",
                "refused",
                control_address="127.0.0.1",
            )
            _start_tcp_listener(runner_vm, source, f"/root/host-policy-{suffix}")
            # Host-to-VM remains reachable without a VM-destination traffic grant.
            server.control(source["binding"]["ipv4"], 8080, "tcp")

            workflow_path.write_text(
                f'''version: "1"
traffic_policy:
  owned-vm-host:
    source_vm: "{source["binding"]["id"]}"
    destination_host: true
    protocol: tcp
    port: {server.port + 1}
  owned-network-host:
    source_network: "{source_network["id"]}"
    destination_host: true
    protocol: udp
    port: {server.port}
  selected-host:
    source_vm: "{source["binding"]["id"]}"
    destination_host: true
    protocol: tcp
    port: {server.loopback_port}
    existing: "{localhost_grant["id"]}"
''',
                encoding="utf-8",
            )
            workflow_attempted = True
            _run_mvm(runner_vm, "env", "apply", str(workflow_path))
            inventory = _tcp_data(runner_vm, "policy", "traffic", "ls")
            owned_vm = next(
                item
                for item in inventory["vm_to_host"]
                if item["source_vm_id"] == source["binding"]["id"]
                and item["destination_port_start"] == server.port + 1
            )
            owned_network = next(
                item
                for item in inventory["network_to_host"]
                if item["source_network_id"] == source_network["id"]
                and item["destination_port_start"] == server.port
            )
            policies.extend((("vm", owned_vm), ("network", owned_network)))
            assert owned_vm["claim_id"] and owned_vm["incarnation"], owned_vm
            assert owned_network["claim_id"] and owned_network["incarnation"], (
                owned_network
            )
            probe(source, gateway, server.port + 1, "tcp", "ack")
            probe(sibling, gateway, server.port, "udp", "ack")
            _run_mvm(runner_vm, "env", "apply", str(workflow_path))
            reapplied = _tcp_data(runner_vm, "policy", "traffic", "ls")
            for family, original in (
                ("vm_to_host", owned_vm),
                ("network_to_host", owned_network),
            ):
                observed = next(
                    item for item in reapplied[family] if item["id"] == original["id"]
                )
                assert (observed["claim_id"], observed["incarnation"]) == (
                    original["claim_id"],
                    original["incarnation"],
                ), observed

            _run_mvm(
                runner_vm,
                "policy",
                "traffic",
                "rm",
                owned_vm["id"],
                "--from-vm",
                "--to-host",
                "--force",
            )
            recreated = _host_grant(
                runner_vm, "vm", source["binding"]["id"], "tcp", str(server.port + 1)
            )
            policies.append(("vm", recreated))
            assert (
                recreated["id"] == owned_vm["id"]
                and recreated["incarnation"] != owned_vm["incarnation"]
            )
            assert not recreated.get("claim_id"), recreated
            refused = _run_mvm(
                runner_vm, "env", "destroy", str(workflow_path), check=False
            )
            assert refused.returncode != 0, refused.stdout
            current = _tcp_data(
                runner_vm,
                "policy",
                "traffic",
                "inspect",
                recreated["id"],
                "--from-vm",
                "--to-host",
            )
            assert current["incarnation"] == recreated["incarnation"], current
            probe(source, gateway, server.port + 1, "tcp", "ack")
            _run_mvm(
                runner_vm,
                "policy",
                "traffic",
                "rm",
                recreated["id"],
                "--from-vm",
                "--to-host",
                "--force",
            )
            _run_mvm(runner_vm, "env", "destroy", str(workflow_path))
            selected = _tcp_data(
                runner_vm,
                "policy",
                "traffic",
                "inspect",
                localhost_grant["id"],
                "--from-vm",
                "--to-host",
            )
            assert selected["incarnation"] == localhost_grant["incarnation"], selected
            assert _tcp_data(runner_vm, "env", "ls", kind=list) == workflows_before
    finally:
        cleanup_errors = []
        for kind, expected in reversed(policies):
            try:
                family = "vm_to_host" if kind == "vm" else "network_to_host"
                matches = [
                    item
                    for item in _tcp_data(runner_vm, "policy", "traffic", "ls")[family]
                    if item["id"] == expected["id"]
                ]
                if matches:
                    assert (
                        len(matches) == 1
                        and matches[0]["incarnation"] == expected["incarnation"]
                    ), matches
                    _run_mvm(
                        runner_vm,
                        "policy",
                        "traffic",
                        "rm",
                        expected["id"],
                        f"--from-{kind}",
                        "--to-host",
                        "--force",
                    )
            except Exception as exc:
                cleanup_errors.append(str(exc))
        if workflow_attempted:
            try:
                matching = [
                    item
                    for item in _tcp_data(runner_vm, "env", "ls", kind=list)
                    if item["spec_path"] == str(workflow_path)
                ]
                assert len(matching) <= 1, matching
                if matching:
                    _run_mvm(runner_vm, "env", "destroy", str(workflow_path))
                assert _tcp_data(runner_vm, "env", "ls", kind=list) == workflows_before
            except Exception as exc:
                cleanup_errors.append(str(exc))
        try:
            _cleanup_tcp_resources(
                runner_vm,
                before,
                networks,
                vms,
                policy_attempted=False,
                source_network=None,
                destination=None,
                policy=None,
            )
        except Exception as exc:
            cleanup_errors.append(str(exc))
        assert not cleanup_errors, "\n".join(cleanup_errors)
