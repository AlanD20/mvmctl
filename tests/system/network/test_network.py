"""Installed-candidate network CRUD, default selection, sync and placement.

Safe cases own function-scoped resources and compare caller inventory, settings,
policies and kernel configuration after checked typed cleanup. The final
missing-bridge administrator fault remains a separately authorized dependency.
All commands execute inside the named disposable runner VM.
"""

from __future__ import annotations

import hashlib
import ipaddress
import json
import os
import re
import shlex
import stat
import time
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _run_mvm,
    _unique_subnet,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_network,
    pytest.mark.slow,
    pytest.mark.domain_network,
]


def _data(runner_vm: str, *args: str, kind: type = dict) -> Any:
    return _json_success_data(_run_mvm(runner_vm, *args, "--json").stdout, kind)


def _networks(runner_vm: str) -> list[dict[str, Any]]:
    return _data(runner_vm, "network", "ls", kind=list)


def _network_info(runner_vm: str, selector: str) -> dict[str, Any]:
    return _data(runner_vm, "network", "inspect", selector)["network"]


def _bridge_name(runner_vm: str, name: str) -> str:
    bridge = _network_info(runner_vm, name)["bridge"]
    assert isinstance(bridge, str) and bridge
    return bridge


def _assert_bridge_exists(runner_vm: str, bridge: str) -> None:
    result = _guest_run(
        runner_vm, f"ip link show {shlex.quote(bridge)}", check=False, timeout=10
    )
    assert result.returncode == 0, result.stderr


def _links(runner_vm: str) -> list[dict[str, Any]]:
    return json.loads(_guest_run(runner_vm, "ip -j -d link show").stdout)


def _namespace(runner_vm: str, path: str = "/proc/self/ns/net") -> tuple[int, int]:
    prefix = "" if path == "/proc/self/ns/net" else "sudo -n "
    result = _guest_run(runner_vm, f"{prefix}stat -Lc '%d %i' {shlex.quote(path)}")
    values = result.stdout.split()
    assert len(values) == 2, result.stdout
    return int(values[0]), int(values[1])


def _inet_rules(runner_vm: str, chain: str) -> list[str]:
    result = _guest_run(
        runner_vm, f"sudo -n nft list chain inet mvmctl_inet {shlex.quote(chain)}"
    )
    return [
        line.strip()
        for line in result.stdout.splitlines()
        if line.strip()
        and not line.strip().startswith(("table ", "chain ", "type ", "#"))
        and line.strip() not in ("{", "}")
    ]


def _assert_firewall_rules_contain_bridge(runner_vm: str, bridge: str) -> None:
    rules = _inet_rules(runner_vm, "forward")
    quoted = json.dumps(bridge)
    assert any(quoted in rule and "accept" in rule for rule in rules), rules
    assert any(quoted in rule and "drop" in rule for rule in rules), rules


def _assert_masquerade_for_subnet(runner_vm: str, bridge: str, subnet: str) -> None:
    rules = _inet_rules(runner_vm, "postrouting")
    assert any(
        json.dumps(bridge) in rule and subnet in rule and "masquerade" in rule
        for rule in rules
    ), rules


def _stable_kernel(value: Any) -> Any:
    """Exclude elapsed bridge timers, nft handles and packet counters only."""
    if isinstance(value, dict):
        return {
            key: _stable_kernel(item)
            for key, item in value.items()
            if key
            not in {
                "forward_delay_timer",
                "gc_timer",
                "hello_timer",
                "tcn_timer",
                "topology_change_timer",
                "handle",
                "packets",
                "bytes",
                "metainfo",
            }
        }
    if isinstance(value, list):
        return [_stable_kernel(item) for item in value]
    return value


def _inventory(runner_vm: str) -> dict[str, Any]:
    inventory: dict[str, Any] = {}
    for domain in (
        "network",
        "vm",
        "image",
        "kernel",
        "bin",
        "volume",
        "key",
        "snapshot",
    ):
        items = _data(runner_vm, domain, "ls", kind=list)
        # Default selection and sync update this display timestamp legitimately.
        if domain == "network":
            items = [
                {key: value for key, value in item.items() if key != "updated_at"}
                for item in items
            ]
        inventory[domain] = sorted(items, key=lambda item: item["id"])
    inventory["config"] = _data(runner_vm, "config", "ls")
    inventory["traffic"] = _data(runner_vm, "policy", "traffic", "ls")
    inventory["exec"] = _data(runner_vm, "policy", "exec", "ls", kind=list)
    inventory["namespace"] = _namespace(runner_vm)
    inventory["links"] = _stable_kernel(_links(runner_vm))
    addresses = json.loads(_guest_run(runner_vm, "ip -j -4 address show").stdout)
    inventory["addresses"] = addresses
    inventory["routes"] = json.loads(
        _guest_run(runner_vm, "ip -j -4 route show table all").stdout
    )
    rules = json.loads(_guest_run(runner_vm, "sudo -n nft -j list ruleset").stdout)
    inventory["firewall"] = [
        _stable_kernel(item) for item in rules["nftables"] if "metainfo" not in item
    ]
    mounts = _guest_run(
        runner_vm,
        "findmnt --json --types nsfs --output TARGET,SOURCE,FSTYPE,OPTIONS",
        check=False,
    )
    assert mounts.returncode in (0, 1), mounts.stderr
    assert mounts.returncode == 0 or not mounts.stdout.strip(), mounts.stdout
    inventory["namespace_mounts"] = (
        json.loads(mounts.stdout) if mounts.stdout.strip() else {}
    )
    return inventory


@dataclass(frozen=True)
class NetworkReceipt:
    id: str
    name: str
    created_at: str
    subnet: str
    gateway: str
    nat_enabled: bool
    bridge: str
    ifindex: int
    alias: str
    namespace: tuple[int, int]


def _network_receipt(runner_vm: str, item: dict[str, Any]) -> NetworkReceipt:
    assert re.fullmatch(r"[0-9a-f]{64}", item["id"]), item
    assert item["name"] and item["created_at"] and item["bridge"], item
    assert item["is_present"] and item["bridge_active"], item
    assert isinstance(item["nat_enabled"], bool), item
    matches = [link for link in _links(runner_vm) if link["ifname"] == item["bridge"]]
    assert len(matches) == 1, matches
    link = matches[0]
    assert link["linkinfo"]["info_kind"] == "bridge", link
    assert link["ifindex"] > 0 and link.get("ifalias"), link
    assert "UP" in link["flags"], link
    return NetworkReceipt(
        item["id"],
        item["name"],
        item["created_at"],
        item["subnet"],
        item["ipv4_gateway"],
        item["nat_enabled"],
        item["bridge"],
        link["ifindex"],
        link["ifalias"],
        _namespace(runner_vm),
    )


def _assert_network_receipt(runner_vm: str, receipt: NetworkReceipt) -> None:
    matches = [
        item
        for item in _networks(runner_vm)
        if item["id"] == receipt.id or item["name"] == receipt.name
    ]
    assert len(matches) == 1, f"refusing ambiguous network cleanup: {matches}"
    assert _network_receipt(runner_vm, matches[0]) == receipt, matches
    assert _network_info(runner_vm, receipt.id)["created_at"] == receipt.created_at


def _assert_network_absent(runner_vm: str, receipt: NetworkReceipt) -> None:
    assert not any(
        item["id"] == receipt.id or item["name"] == receipt.name
        for item in _networks(runner_vm)
    )
    assert not any(
        link["ifname"] == receipt.bridge or link["ifindex"] == receipt.ifindex
        for link in _links(runner_vm)
    )
    # An exact removal must retire both the bridge and its firewall references.
    rules = _guest_run(runner_vm, "sudo -n nft -j list ruleset").stdout
    assert receipt.bridge not in rules, rules


@dataclass(frozen=True)
class VMCreateReceipt:
    id: str
    name: str
    created_at: str
    network_id: str


@dataclass(frozen=True)
class VMReceipt:
    id: str
    name: str
    created_at: str
    network_id: str
    ipv4: str
    mac: str
    namespace: tuple[int, int]


class NetworkCase:
    """Own only successful creates with complete identity receipts."""

    def __init__(self, runner_vm: str):
        self.runner_vm = runner_vm
        self.before = _inventory(runner_vm)
        self.networks: list[NetworkReceipt] = []
        self.vm_creates: list[VMCreateReceipt] = []
        self.vms: list[VMReceipt] = []
        self.attempts: list[tuple[str, str]] = []
        defaults = [item for item in self.before["network"] if item["is_default"]]
        assert len(defaults) <= 1, defaults
        self.original_default = defaults[0] if defaults else None
        self.original_default_active: NetworkReceipt | None = None
        if self.original_default is not None:
            original = self.original_default
            assert re.fullmatch(r"[0-9a-f]{64}", original["id"]), original
            assert original["name"] and original["created_at"], original
            assert isinstance(original["is_present"], bool), original
            assert isinstance(original["bridge_active"], bool), original
            if original["is_present"] and original["bridge_active"]:
                self.original_default_active = _network_receipt(runner_vm, original)
            else:
                assert not original["is_present"] and not original["bridge_active"], original
        self.default_changed = False

    def create_network(
        self,
        *,
        name: str | None = None,
        subnet: str | None = None,
        nat: bool = True,
        gateway: str | None = None,
        default: bool = False,
    ) -> NetworkReceipt:
        name = name or f"sys-network-{uuid.uuid4().hex[:19]}"
        subnet = subnet or _unique_subnet(name)
        before = _networks(self.runner_vm)
        assert not any(item["name"] == name or item["id"] == name for item in before), (
            before
        )
        prefix = ipaddress.ip_network(subnet)
        assert not any(
            prefix.overlaps(ipaddress.ip_network(item["subnet"])) for item in before
        ), before
        args = ["network", "create", name, "--subnet", subnet]
        if not nat:
            args.append("--no-nat")
        if gateway:
            args.extend(["--ipv4-gateway", gateway])
        if default:
            self.default_changed = True
            args.append("--default")
        self.attempts.append(("network", name))
        created = _data(self.runner_vm, *args)
        assert created["name"] == name and created["subnet"] == subnet, created
        assert created["nat_enabled"] is nat, created
        assert created["ipv4_gateway"] == (
            gateway or str(prefix.network_address + 1)
        ), created
        receipt = _network_receipt(self.runner_vm, created)
        assert receipt.id not in {item["id"] for item in before}, created
        self.networks.append(receipt)
        _assert_network_receipt(self.runner_vm, receipt)
        return receipt

    def remove_network(self, receipt: NetworkReceipt, *, force: bool = False) -> None:
        assert receipt in self.networks, "no owned network receipt"
        _assert_network_receipt(self.runner_vm, receipt)
        args = ["network", "rm", receipt.id]
        if force:
            args.append("--force")
        _run_mvm(self.runner_vm, *args)
        _assert_network_absent(self.runner_vm, receipt)
        self.networks.remove(receipt)

    def set_default(self, receipt: NetworkReceipt) -> None:
        assert receipt in self.networks, "default target must be test-owned"
        _assert_network_receipt(self.runner_vm, receipt)
        self.default_changed = True
        _run_mvm(self.runner_vm, "network", "default", receipt.id)
        defaults = [item for item in _networks(self.runner_vm) if item["is_default"]]
        assert len(defaults) == 1 and defaults[0]["id"] == receipt.id, defaults

    def _vm_kernel_id(self) -> str:
        kernel_id = os.environ.get("MVM_RUNNER_KERNEL_ID")
        kernel_sha256 = os.environ.get("MVM_RUNNER_KERNEL_SHA256")
        if kernel_id is None and kernel_sha256 is None:
            kernels = [
                item for item in self.before["kernel"]
                if item["is_default"] and item["is_present"]
            ]
            assert len(kernels) == 1, "preseed one default kernel"
            return kernels[0]["id"]
        assert kernel_id and kernel_sha256, (
            "imported kernel ID and SHA256 must be supplied together"
        )
        assert re.fullmatch(r"[0-9a-f]{64}", kernel_id), (
            "imported kernel requires a full canonical ID"
        )
        assert re.fullmatch(r"[0-9a-f]{64}", kernel_sha256), (
            "imported kernel requires a canonical SHA256"
        )
        kernels = [item for item in self.before["kernel"] if item["id"] == kernel_id]
        assert len(kernels) == 1 and kernels[0]["is_present"], kernels
        inspected = _data(self.runner_vm, "kernel", "inspect", kernel_id)
        kernel, storage = inspected["kernel"], inspected["storage"]
        assert (
            kernel["id"] == kernel_id
            and kernel["type"] == "custom"
            and kernel["is_present"] is True
        ), inspected
        cache = Path(os.path.abspath(
            Path(os.environ.get("MVM_CACHE_DIR", "~/.cache/mvmctl")).expanduser()
        ))
        path = cache / "kernels" / kernel_id
        assert storage["path"] == str(path), inspected
        flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
        parent = os.open("/", flags)
        try:
            for component in path.parts[1:-1]:
                child = os.open(component, flags, dir_fd=parent)
                os.close(parent)
                parent = child
            descriptor = os.open(
                path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, dir_fd=parent,
            )
        finally:
            os.close(parent)
        try:
            before = os.fstat(descriptor)
            assert stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_size > 0, path
            digest = hashlib.sha256()
            read_bytes = 0
            deadline = time.monotonic() + 10
            while chunk := os.read(descriptor, 1024 * 1024):
                read_bytes += len(chunk)
                assert read_bytes <= before.st_size and time.monotonic() <= deadline, path
                if read_bytes == len(chunk):
                    assert chunk.startswith(b"\x7fELF"), "imported kernel is not an ELF"
                digest.update(chunk)
            after = os.fstat(descriptor)
            current = path.lstat()
            for field in ("st_dev", "st_ino", "st_size", "st_mtime_ns", "st_ctime_ns"):
                assert getattr(before, field) == getattr(after, field) == getattr(current, field), path
            assert read_bytes == before.st_size and digest.hexdigest() == kernel_sha256, path
        finally:
            os.close(descriptor)
        return kernel_id

    def create_vm(
        self, network: NetworkReceipt, *, use_default: bool = False
    ) -> VMReceipt:
        # Read-only admission: no helper may pull assets or alter defaults/settings.
        assert (
            _data(
                self.runner_vm,
                "config",
                "get",
                "settings",
                "guestfs_enabled",
                kind=bool,
            )
            is False
        ), "preconfigure LoopMount; network tests never change backend settings"
        for binary_type in ("firecracker", "jailer"):
            matches = [
                item
                for item in self.before["bin"]
                if item["type"] == binary_type and item["is_default"]
            ]
            assert (
                len(matches) == 1
                and matches[0]["version"] == "1.17.0"
                and matches[0]["is_present"]
            ), matches
        kernel_id = self._vm_kernel_id()
        images = [
            item
            for item in self.before["image"]
            if item["type"] == "alpine"
            and item["version"] == "3.23"
            and item["is_present"]
        ]
        assert len(images) == 1 and not images[0]["is_default"], (
            "preseed nondefault Alpine 3.23"
        )
        _assert_network_receipt(self.runner_vm, network)
        name = f"sys-network-vm-{uuid.uuid4().hex}"
        vms_before = _data(self.runner_vm, "vm", "ls", kind=list)
        assert not any(item["name"] == name for item in vms_before)
        if use_default:
            self.set_default(network)
        args = [
            "vm",
            "create",
            name,
            "--image",
            images[0]["id"],
            "--kernel",
            kernel_id,
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
        ]
        if not use_default:
            args.extend(["--network", network.id])
        self.attempts.append(("vm", name))
        created = _json_success_data(
            _run_mvm(self.runner_vm, *args, timeout=180).stdout, list
        )
        assert len(created) == 1, created
        item = created[0]
        assert item["name"] == name and re.fullmatch(r"[0-9a-f]{32}", item["id"]), item
        assert not any(previous["id"] == item["id"] for previous in vms_before), item
        assert isinstance(item["created_at"], str) and item["created_at"], item
        assert not any(receipt.id == item["id"] for receipt in self.vm_creates), item
        self.vm_creates.append(
            VMCreateReceipt(item["id"], name, item["created_at"], network.id)
        )
        assert item["status"] == "running", item
        assert (
            item["network_placement"] == "managed" and item["network_id"] == network.id
        ), item
        assert ipaddress.ip_address(item["ipv4"]) in ipaddress.ip_network(
            network.subnet
        ), item
        assert re.fullmatch(r"02:fc(?::[0-9a-f]{2}){4}", item["mac"]), item
        path = f"/run/mvmctl/authority/{os.getuid()}/netns/{item['id']}"
        namespace = _namespace(self.runner_vm, path)
        assert namespace != network.namespace
        receipt = VMReceipt(
            item["id"],
            name,
            item["created_at"],
            network.id,
            item["ipv4"],
            item["mac"],
            namespace,
        )
        self.vms.append(receipt)
        self.assert_vm(receipt)
        return receipt

    def assert_vm(self, receipt: VMReceipt) -> dict[str, Any]:
        matches = [
            item
            for item in _data(self.runner_vm, "vm", "ls", kind=list)
            if item["id"] == receipt.id or item["name"] == receipt.name
        ]
        assert len(matches) == 1, f"refusing ambiguous VM cleanup: {matches}"
        item = matches[0]
        assert (
            item["id"],
            item["name"],
            item["created_at"],
            item["network_id"],
            item["ipv4"],
            item["mac"],
        ) == (
            receipt.id,
            receipt.name,
            receipt.created_at,
            receipt.network_id,
            receipt.ipv4,
            receipt.mac,
        ), item
        assert item["status"] == "running", item
        path = f"/run/mvmctl/authority/{os.getuid()}/netns/{receipt.id}"
        assert _namespace(self.runner_vm, path) == receipt.namespace
        assert item["pid"] > 0, item
        assert (
            _namespace(self.runner_vm, f"/proc/{item['pid']}/ns/net")
            == receipt.namespace
        )
        inspected = _data(self.runner_vm, "vm", "inspect", receipt.id)
        assert inspected["vm"]["created_at"] == receipt.created_at
        assert inspected["networking"]["network"]["id"] == receipt.network_id
        assert inspected["networking"]["mac"] == receipt.mac
        return inspected

    def remove_vm(self, receipt: VMReceipt | VMCreateReceipt) -> None:
        owned = [item for item in self.vm_creates if item.id == receipt.id]
        assert len(owned) == 1, "no unique owned VM create receipt"
        created = owned[0]
        assert receipt == created or receipt in self.vms, "no owned VM receipt"
        matches = [
            item for item in _data(self.runner_vm, "vm", "ls", kind=list)
            if item["id"] == created.id or item["name"] == created.name
        ]
        assert len(matches) == 1, f"refusing ambiguous VM cleanup: {matches}"
        item = matches[0]
        assert (item["id"], item["name"], item["created_at"], item["network_id"]) == (
            created.id, created.name, created.created_at, created.network_id,
        ), item
        assert item["network_placement"] == "managed", item
        live = [item for item in self.vms if item.id == created.id]
        assert len(live) <= 1, live
        if live:
            assert (item["ipv4"], item["mac"]) == (live[0].ipv4, live[0].mac), item
        if item["status"] == "running":
            if live:
                self.assert_vm(live[0])
            else:
                # No complete live receipt was established; freshly bind the owned process.
                assert item["pid"] > 0, item
                path = f"/run/mvmctl/authority/{os.getuid()}/netns/{created.id}"
                namespace = _namespace(self.runner_vm, path)
                assert namespace != _namespace(self.runner_vm)
                assert (
                    _namespace(self.runner_vm, f"/proc/{item['pid']}/ns/net")
                    == namespace
                )
        else:
            assert item["status"] == "stopped", (
                f"refusing unknown VM cleanup state: {item}"
            )
            # Root observes absence; an inaccessible path is not proof of absence.
            script = """
import os, pathlib, sys
owner, vm_id, pid, ticks = sys.argv[1:]
assert os.geteuid() == 0 and int(pid) >= 0
for path in (f'/run/mvmctl/authority/{owner}/netns/{vm_id}',
             f'/sys/fs/cgroup/mvmctl/{vm_id}'):
    try:
        os.lstat(path)
    except FileNotFoundError:
        pass
    else:
        raise AssertionError(('stopped VM retains active namespace/cgroup', path))
if int(pid) > 0:
    assert ticks != 'None' and int(ticks) > 0
    try:
        fields = pathlib.Path(f'/proc/{pid}/stat').read_text().rpartition(') ')[2].split()
    except FileNotFoundError:
        pass
    else:
        assert int(fields[19]) != int(ticks), 'stopped VM process is still alive'
"""
            _guest_run(
                self.runner_vm,
                shlex.join([
                    "sudo", "-n", "python3", "-I", "-c", script,
                    str(os.getuid()), created.id, str(item["pid"]),
                    str(item.get("process_start_time")),
                ]),
                timeout=10,
            )
        # The typed public transaction gracefully stops and removes this exact VM.
        _run_mvm(self.runner_vm, "vm", "rm", receipt.id, timeout=120)
        assert not any(
            item["id"] == receipt.id or item["name"] == receipt.name
            for item in _data(self.runner_vm, "vm", "ls", kind=list)
        )
        mounts = _guest_run(
            self.runner_vm, "findmnt -rn --types nsfs --output TARGET", check=False
        )
        assert mounts.returncode in (0, 1), mounts.stderr
        assert mounts.returncode == 0 or not mounts.stdout.strip(), mounts.stdout
        assert f"/netns/{receipt.id}" not in mounts.stdout, mounts.stdout
        path = f"/run/mvmctl/authority/{os.getuid()}/netns/{receipt.id}"
        leaf = _guest_run(
            self.runner_vm, f"sudo -n stat -c '%d %i' {shlex.quote(path)}", check=False
        )
        assert (
            leaf.returncode != 0 and "no such file or directory" in leaf.stderr.lower()
        ), leaf
        if live:
            self.vms.remove(live[0])
        self.vm_creates.remove(created)

    def finish(self) -> None:
        errors = []
        for receipt in reversed(self.vm_creates[:]):
            try:
                self.remove_vm(receipt)
            except Exception as exc:
                errors.append(f"VM {receipt.id}: {exc}")
        if self.default_changed and self.original_default:
            try:
                original = self.original_default
                matches = [
                    item
                    for item in _networks(self.runner_vm)
                    if item["id"] == original["id"] or item["name"] == original["name"]
                ]
                assert len(matches) == 1, f"refusing ambiguous default restoration: {matches}"
                assert {
                    key: value for key, value in matches[0].items()
                    if key not in {"is_default", "updated_at"}
                } == {
                    key: value for key, value in original.items()
                    if key not in {"is_default", "updated_at"}
                }, matches
                if self.original_default_active is not None:
                    _assert_network_receipt(self.runner_vm, self.original_default_active)
                _run_mvm(self.runner_vm, "network", "default", original["id"])
            except Exception as exc:
                errors.append(
                    f"restore exact default {self.original_default['id']}: {exc}"
                )
        for receipt in reversed(self.networks[:]):
            try:
                self.remove_network(receipt)
            except Exception as exc:
                errors.append(f"network {receipt.id}: {exc}")
        try:
            for domain, name in self.attempts:
                matches = [
                    item
                    for item in _data(self.runner_vm, domain, "ls", kind=list)
                    if item["name"] == name
                ]
                assert not matches, (
                    f"unresolved create {domain}/{name}; no complete cleanup receipt: {matches}"
                )
            after = _inventory(self.runner_vm)
            for field, expected in self.before.items():
                assert after[field] == expected, (
                    f"baseline changed: {field}: {expected!r} -> {after[field]!r}"
                )
        except Exception as exc:
            errors.append(f"baseline/partial-create verification: {exc}")
        assert not errors, "\n".join(errors)


@pytest.fixture
def network_case(runner_vm):
    case = NetworkCase(runner_vm)
    try:
        yield case
    finally:
        case.finish()


def _assert_rejected(
    case: NetworkCase, *args: str, message: str, code: str | None = None
) -> dict[str, Any]:
    before = _inventory(case.runner_vm)
    result = _run_mvm(case.runner_vm, *args, "--json", check=False)
    assert result.returncode != 0, result.stdout
    envelope = json.loads(result.stdout)
    assert envelope["schema_version"] == 1 and envelope["status"] == "error", envelope
    assert message.lower() in envelope["error"]["message"].lower(), envelope
    if code:
        assert envelope["error"]["code"] == code, envelope
    assert _inventory(case.runner_vm) == before, "rejected command changed state"
    return envelope["error"]


class TestNetworkLifecycle:
    def test_network_listing_and_verification(self, network_case):
        """List returns the exact newly owned bridge and resolved intent."""
        receipt = network_case.create_network()
        items = [
            item
            for item in _networks(network_case.runner_vm)
            if item["id"] == receipt.id
        ]
        assert len(items) == 1, items
        item = items[0]
        assert item["name"] == receipt.name and item["subnet"] == receipt.subnet
        assert (
            item["bridge"] == receipt.bridge and item["ipv4_gateway"] == receipt.gateway
        )
        assert item["nat_enabled"] is True and item["is_default"] is False

    def test_managed_nftables_rules_reference_bridge(self, network_case):
        receipt = network_case.create_network()
        _assert_firewall_rules_contain_bridge(network_case.runner_vm, receipt.bridge)
        _assert_masquerade_for_subnet(
            network_case.runner_vm, receipt.bridge, receipt.subnet
        )

    def test_network_inspect(self, network_case):
        receipt = network_case.create_network()
        inspected = _data(network_case.runner_vm, "network", "inspect", receipt.id)
        assert inspected["network"] == _network_info(
            network_case.runner_vm, receipt.name
        )
        assert inspected["status"] == {
            "bridge_active": True,
            "is_present": True,
            "is_default": False,
        }
        assert inspected["nat"] == {"nat_enabled": True}
        assert inspected["leases"] == []
        _assert_network_receipt(network_case.runner_vm, receipt)

    def test_network_default_without_name(self, network_case):
        _assert_rejected(
            network_case, "network", "default", message="missing required argument"
        )

    def test_network_remove_nonexistent(self, network_case):
        _assert_rejected(
            network_case,
            "network",
            "rm",
            f"missing-{uuid.uuid4().hex[:23]}",
            message="no networks found matching identifiers",
        )

    def test_set_default_nonexistent_network_fails(self, network_case):
        _assert_rejected(
            network_case,
            "network",
            "default",
            f"missing-{uuid.uuid4().hex[:23]}",
            message="no networks found matching identifiers",
        )

    def test_network_create_with_invalid_cidr_format(self, network_case):
        _assert_rejected(
            network_case,
            "network",
            "create",
            f"sys-invalid-{uuid.uuid4().hex[:19]}",
            "--subnet",
            "not-a-cidr",
            message="canonical masked IPv4 prefix",
            code="validation.failed",
        )

    def test_network_create_with_custom_cidr(self, network_case):
        receipt = network_case.create_network()
        addresses = json.loads(
            _guest_run(
                network_case.runner_vm, f"ip -j -4 address show dev {receipt.bridge}"
            ).stdout
        )
        prefix = ipaddress.ip_network(receipt.subnet)
        assert [
            (item["local"], item["prefixlen"])
            for link in addresses
            for item in link["addr_info"]
        ] == [(receipt.gateway, prefix.prefixlen)]
        routes = json.loads(
            _guest_run(network_case.runner_vm, "ip -j -4 route show table main").stdout
        )
        connected = [route for route in routes if route.get("dev") == receipt.bridge]
        assert len(connected) == 1 and connected[0]["dst"] == receipt.subnet, connected
        assert "gateway" not in connected[0] and connected[0].get("metric", 0) == 0, (
            connected
        )

    def test_network_create_no_nat(self, network_case):
        receipt = network_case.create_network(nat=False)
        inspected = _data(network_case.runner_vm, "network", "inspect", receipt.id)
        assert inspected["nat"] == {"nat_enabled": False}
        assert not any(
            json.dumps(receipt.bridge) in rule
            for rule in _inet_rules(network_case.runner_vm, "postrouting")
        )
        rules = _inet_rules(network_case.runner_vm, "forward")
        assert any(
            json.dumps(receipt.bridge) in rule and "drop" in rule for rule in rules
        ), rules
        assert not any(
            f"iifname {json.dumps(receipt.bridge)}" in rule
            and "oifname" in rule
            and "accept" in rule
            for rule in rules
        ), rules

    def test_network_set_default(self, network_case):
        first = network_case.create_network()
        second = network_case.create_network()
        network_case.set_default(first)
        network_case.set_default(second)
        assert _network_info(network_case.runner_vm, first.id)["is_default"] is False
        assert _network_info(network_case.runner_vm, second.id)["is_default"] is True

    def test_network_create_with_default_flag(self, network_case):
        receipt = network_case.create_network(default=True)
        defaults = [
            item for item in _networks(network_case.runner_vm) if item["is_default"]
        ]
        assert len(defaults) == 1 and defaults[0]["id"] == receipt.id, defaults

    def test_overlapping_subnet_across_networks_rejected(self, network_case):
        first = network_case.create_network()
        name = f"sys-overlap-{uuid.uuid4().hex[:19]}"
        _assert_rejected(
            network_case,
            "network",
            "create",
            name,
            "--subnet",
            first.subnet,
            message="overlaps",
            code="network.subnet.overlap",
        )
        _assert_network_receipt(network_case.runner_vm, first)

    @pytest.mark.parametrize("subnet", ["10.252.0.0/31", "10.252.0.0/32"])
    def test_network_create_with_slash_32_subnet(self, network_case, subnet):
        _assert_rejected(
            network_case,
            "network",
            "create",
            f"sys-small-{uuid.uuid4().hex[:21]}",
            "--subnet",
            subnet,
            message="no longer than /30",
            code="validation.failed",
        )

    def test_network_create_with_same_name(self, network_case):
        first = network_case.create_network()
        error = _assert_rejected(
            network_case,
            "network",
            "create",
            first.name,
            "--subnet",
            first.subnet,
            message=first.name,
            code="network.already_exists",
        )
        assert error["message"] == first.name and error["class"] == 2, error
        _assert_network_receipt(network_case.runner_vm, first)

    def test_recreate_after_remove_gets_new_root_identity(self, network_case):
        """Same-second recreation must not reuse a retired root identity."""
        name = f"sys-recreate-{uuid.uuid4().hex[:18]}"
        subnet = _unique_subnet(name)
        for _ in range(4):
            first = network_case.create_network(name=name, subnet=subnet)
            network_case.remove_network(first)
            second = network_case.create_network(name=name, subnet=subnet)
            assert second.id != first.id and second.bridge != first.bridge
            _assert_network_receipt(network_case.runner_vm, second)
            if second.created_at == first.created_at:
                return
            network_case.remove_network(second)
        pytest.fail("four create/remove pairs did not share a timestamp second")

    @pytest.mark.destructive
    def test_network_deletion_and_cleanup(self, network_case):
        receipt = network_case.create_network()
        network_case.remove_network(receipt)
        _assert_network_absent(network_case.runner_vm, receipt)

    @pytest.mark.destructive
    def test_network_remove_multiple(self, network_case):
        first = network_case.create_network()
        second = network_case.create_network()
        for receipt in (first, second):
            _assert_network_receipt(network_case.runner_vm, receipt)
        _run_mvm(network_case.runner_vm, "network", "rm", first.id, second.id)
        for receipt in (first, second):
            _assert_network_absent(network_case.runner_vm, receipt)
            network_case.networks.remove(receipt)


class TestNetworkInspectTree:
    def test_network_inspect_tree(self, network_case):
        receipt = network_case.create_network()
        result = _run_mvm(network_case.runner_vm, "network", "inspect", receipt.id)
        assert receipt.name in result.stdout and receipt.subnet in result.stdout
        assert receipt.bridge in result.stdout
        assert "├─ " in result.stdout or "└─ " in result.stdout


class TestNetworkAdvancedCreate:
    def test_network_create_with_ipv4_gateway(self, network_case):
        name = f"sys-gateway-{uuid.uuid4().hex[:19]}"
        subnet = _unique_subnet(name)
        gateway = str(ipaddress.ip_network(subnet).network_address + 100)
        receipt = network_case.create_network(name=name, subnet=subnet, gateway=gateway)
        addresses = json.loads(
            _guest_run(
                network_case.runner_vm, f"ip -j -4 address show dev {receipt.bridge}"
            ).stdout
        )
        assert [
            (address["local"], address["prefixlen"])
            for link in addresses
            for address in link["addr_info"]
        ] == [(gateway, 24)]
        assert (
            _network_info(network_case.runner_vm, receipt.id)["ipv4_gateway"] == gateway
        )

    def test_network_create_with_nat_gateways_rejected(self, network_case):
        name = f"sys-uplink-{uuid.uuid4().hex[:20]}"
        _assert_rejected(
            network_case,
            "network",
            "create",
            name,
            "--subnet",
            _unique_subnet(name),
            "--nat-gateways",
            "eth0",
            message="unknown flag: --nat-gateways",
        )

    def test_network_create_invalid_gateway_fails(self, network_case):
        name = f"sys-gateway-{uuid.uuid4().hex[:19]}"
        _assert_rejected(
            network_case,
            "network",
            "create",
            name,
            "--subnet",
            _unique_subnet(name),
            "--ipv4-gateway",
            "not-an-ip",
            message="canonical usable address",
            code="validation.failed",
        )


class TestNetworkSync:
    def test_network_sync_preserves_all_networks(self, network_case):
        first = network_case.create_network()
        second = network_case.create_network()
        before = _inventory(network_case.runner_vm)
        for selectors in ((), (first.id,), (first.name,)):
            result = _data(network_case.runner_vm, "network", "sync", *selectors)
            expected = (
                {item["id"] for item in _networks(network_case.runner_vm)}
                if not selectors
                else {first.id}
            )
            assert set(result) == expected, result
            assert all(
                stats == {"verified": 1, "added": 0, "orphaned": 0}
                for stats in result.values()
            ), result
            for receipt in (first, second):
                _assert_network_receipt(network_case.runner_vm, receipt)
                _assert_firewall_rules_contain_bridge(
                    network_case.runner_vm, receipt.bridge
                )
                _assert_masquerade_for_subnet(
                    network_case.runner_vm, receipt.bridge, receipt.subnet
                )
            assert _inventory(network_case.runner_vm) == before, (
                "sync changed network identity, configuration or rules"
            )

    def test_network_sync_conntrack_rule(self, network_case):
        receipt = network_case.create_network()
        _run_mvm(network_case.runner_vm, "network", "sync", receipt.id)
        rules = _inet_rules(network_case.runner_vm, "forward")
        assert any(
            json.dumps(receipt.bridge) in rule
            and "ct state established,related" in rule
            and "accept" in rule
            for rule in rules
        ), rules

    def test_network_sync_on_nonexistent_network(self, network_case):
        _assert_rejected(
            network_case,
            "network",
            "sync",
            f"missing-{uuid.uuid4().hex[:23]}",
            message="no networks found matching identifiers",
        )


@pytest.mark.destructive
class TestNetworkRemoveForce:
    def test_network_rm_with_force(self, network_case):
        receipt = network_case.create_network()
        network_case.remove_network(receipt, force=True)
        _assert_network_absent(network_case.runner_vm, receipt)


@pytest.mark.tier2
@pytest.mark.requires_kvm
class TestNetworkVMDependency:
    def test_network_inspect_after_all_vms_removed(self, network_case):
        network = network_case.create_network(nat=False)
        vm = network_case.create_vm(network)
        inspected = _data(network_case.runner_vm, "network", "inspect", network.id)
        assert len(inspected["leases"]) == 1, inspected
        assert (
            inspected["leases"][0]["vm_id"] == vm.id
            and inspected["leases"][0]["ipv4"] == vm.ipv4
        )
        network_case.remove_vm(vm)
        inspected = _data(network_case.runner_vm, "network", "inspect", network.id)
        assert inspected["leases"] == []
        _assert_network_receipt(network_case.runner_vm, network)

    @pytest.mark.parametrize(
        "use_default", [False, True], ids=["explicit-network", "default-network"]
    )
    def test_network_rm_with_active_vm_fails_without_force(
        self, network_case, use_default
    ):
        """Explicit and omitted placement retain the exact network and lease."""
        network = network_case.create_network(nat=False)
        vm = network_case.create_vm(network, use_default=use_default)
        _assert_rejected(
            network_case,
            "network",
            "rm",
            network.id,
            message="referenced by",
            code="network.remove_failed",
        )
        inspected = network_case.assert_vm(vm)
        assert inspected["vm"]["status"] == "running"
        leases = _data(network_case.runner_vm, "network", "inspect", network.id)[
            "leases"
        ]
        assert (
            len(leases) == 1
            and leases[0]["vm_id"] == vm.id
            and leases[0]["ipv4"] == vm.ipv4
        ), leases
        _assert_network_receipt(network_case.runner_vm, network)


class TestNetworkEdgeCases:
    def test_network_create_without_subnet(self, network_case):
        _assert_rejected(
            network_case,
            "network",
            "create",
            f"sys-missing-{uuid.uuid4().hex[:19]}",
            message="Missing required option '--subnet'",
        )


class TestNetworkSyncMissingBridge:
    """Same-boot bridge disappearance must fail closed, not be re-created.

    This class directly deletes a managed bridge and is placed last in the file.
    A real prior-boot recovery scenario needs a runner reboot, not link deletion.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.domain_network,
    ]

    @pytest.mark.destructive
    def test_sync_refuses_missing_managed_bridge(self, network_case) -> None:
        """An exact administrator fault is rejected and removed through typed authority."""
        runner_vm = network_case.runner_vm
        qualification_host = os.environ.get("MVM_TEST_QUALIFICATION_HOST")
        assert (
            qualification_host
            and qualification_host == os.environ.get("MVM_TEST_VM") == runner_vm
        ), "administrator fault requires explicit exact qualification-host admission"
        receipt = network_case.create_network()
        boot_id = _guest_run(
            runner_vm, "cat /proc/sys/kernel/random/boot_id"
        ).stdout.strip()
        # The root fault pins its inherited namespace, freshly admits the receipt
        # and childless bridge, then deletes by that observed ifindex, not a name.
        fault = """
import json, os, socket, struct, subprocess, sys
bridge, index, alias, device, inode, mac, boot_id = sys.argv[1:]
index, device, inode = int(index), int(device), int(inode)
assert os.geteuid() == 0
namespace_fd = os.open('/proc/self/ns/net', os.O_RDONLY | os.O_CLOEXEC)
channel = None
def links():
    return json.loads(subprocess.check_output(['ip', '-j', '-d', 'link', 'show'], text=True))
def namespace():
    current = os.stat('/proc/self/ns/net')
    pinned = os.fstat(namespace_fd)
    assert (current.st_dev, current.st_ino) == (pinned.st_dev, pinned.st_ino) == (device, inode)
def same_boot():
    with open('/proc/sys/kernel/random/boot_id') as source:
        assert source.read().strip() == boot_id
try:
    namespace()
    same_boot()
    observed = links()
    matches = [link for link in observed if link['ifname'] == bridge or link['ifindex'] == index]
    assert len(matches) == 1
    link = matches[0]
    assert link['ifname'] == bridge and link['ifindex'] == index
    assert link['linkinfo']['info_kind'] == 'bridge' and link.get('ifalias') == alias
    assert link['address'] == mac and 'UP' in link['flags']
    assert not any(link.get('master') in (bridge, index) for link in observed)
    channel = socket.socket(socket.AF_NETLINK, socket.SOCK_RAW | socket.SOCK_CLOEXEC, socket.NETLINK_ROUTE)
    channel.settimeout(5)
    channel.bind((0, 0))
    namespace()
    same_boot()
    # RTM_DELLINK, NLM_F_REQUEST | NLM_F_ACK, exact admitted ifindex.
    body = struct.pack('=BBHiII', socket.AF_UNSPEC, 0, 0, index, 0, 0)
    channel.sendto(struct.pack('=IHHII', 16 + len(body), 17, 5, 1, 0) + body, (0, 0))
    reply, peer = channel.recvfrom(65536)
    assert peer[0] == 0 and len(reply) >= 20
    length, kind, flags, sequence, pid = struct.unpack_from('=IHHII', reply)
    assert 20 <= length <= len(reply) and kind == 2 and sequence == 1
    assert struct.unpack_from('=i', reply, 16)[0] == 0
    namespace()
    same_boot()
    assert not any(link['ifname'] == bridge or link['ifindex'] == index for link in links())
    print('deleted exact bridge')
finally:
    if channel is not None:
        channel.close()
    os.close(namespace_fd)
"""
        try:
            _assert_network_receipt(runner_vm, receipt)
            inspected = _data(runner_vm, "network", "inspect", receipt.id)
            assert inspected["leases"] == []
            link = next(
                link for link in _links(runner_vm) if link["ifname"] == receipt.bridge
            )
            result = _guest_run(
                runner_vm,
                shlex.join(
                    [
                        "sudo",
                        "-n",
                        "python3",
                        "-I",
                        "-c",
                        fault,
                        receipt.bridge,
                        str(receipt.ifindex),
                        receipt.alias,
                        str(receipt.namespace[0]),
                        str(receipt.namespace[1]),
                        link["address"],
                        boot_id,
                    ]
                ),
                timeout=10,
            )
            assert result.stdout.strip() == "deleted exact bridge", result
            assert _namespace(runner_vm) == receipt.namespace
            assert not any(
                link["ifname"] == receipt.bridge or link["ifindex"] == receipt.ifindex
                for link in _links(runner_vm)
            )
            error = _assert_rejected(
                network_case,
                "network",
                "sync",
                receipt.id,
                message="managed bridge disappeared",
                code="network.error",
            )
            assert error["class"] == 4, error
            assert (
                _guest_run(
                    runner_vm, "cat /proc/sys/kernel/random/boot_id"
                ).stdout.strip()
                == boot_id
            )
        finally:
            # Presence admits ordinary cleanup. Absence admits only exact logical
            # retirement through root; a replacement is never faulted or repaired.
            assert _namespace(runner_vm) == receipt.namespace
            links = _links(runner_vm)
            if any(
                link["ifname"] == receipt.bridge or link["ifindex"] == receipt.ifindex
                for link in links
            ):
                network_case.remove_network(receipt)
            else:
                matches = [
                    item
                    for item in _networks(runner_vm)
                    if item["id"] == receipt.id or item["name"] == receipt.name
                ]
                assert len(matches) == 1, "refusing ambiguous missing-bridge cleanup"
                item = matches[0]
                assert (
                    item["id"],
                    item["name"],
                    item["created_at"],
                    item["subnet"],
                    item["ipv4_gateway"],
                    item["nat_enabled"],
                    item["bridge"],
                ) == (
                    receipt.id,
                    receipt.name,
                    receipt.created_at,
                    receipt.subnet,
                    receipt.gateway,
                    receipt.nat_enabled,
                    receipt.bridge,
                ), item
                assert item["is_present"] is False and item["bridge_active"] is False, (
                    item
                )
                _run_mvm(runner_vm, "network", "rm", receipt.id)
                _assert_network_absent(runner_vm, receipt)
                network_case.networks.remove(receipt)
