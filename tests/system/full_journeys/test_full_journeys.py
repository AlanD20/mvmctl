"""Bounded installed-CLI journeys for the v0.3.0 resource and policy contracts."""

from __future__ import annotations

import os
import shlex
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest

from tests.system.conftest import _json_success_data, _run_mvm, _unique_subnet

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_full_journeys,
    pytest.mark.needs_kvm,
    pytest.mark.slow,
    pytest.mark.tier2,
    pytest.mark.timeout(600),
]


def _name(role: str) -> str:
    return f"journey-{role}-{uuid.uuid4().hex[:8]}"


def _data(runner_vm: str, *args: str, kind: type = dict, timeout: int = 60):
    return _json_success_data(
        _run_mvm(runner_vm, *args, "--json", timeout=timeout).stdout, kind
    )


def _listed(runner_vm: str, domain: str) -> list[dict]:
    return _data(runner_vm, domain, "ls", kind=list)


def _require_journey_assets(runner_vm: str) -> None:
    assert os.geteuid() != 0, "journeys must exercise the installed CLI as a non-root operator"
    mirror = os.environ.get("MVM_ASSET_MIRROR", "")
    assert mirror and Path(mirror).is_dir(), "the preseeded runner mirror is required"
    images = [
        item for item in _listed(runner_vm, "image")
        if item["type"] == "alpine" and item["version"] == "3.23" and item["is_present"]
    ]
    assert len(images) == 1, "preseed alpine:3.23; this journey never pulls assets"
    image = _data(runner_vm, "image", "inspect", images[0]["id"])
    minimum = image["requirements"]["minimum_rootfs_size_mib"]
    assert 0 < minimum <= 512, f"Alpine minimum {minimum} MiB exceeds the bounded 512 MiB disk"
    assert any(item["is_present"] and item["is_default"] for item in _listed(runner_vm, "kernel"))
    binaries = _listed(runner_vm, "bin")
    for kind in ("firecracker", "jailer"):
        assert any(
            item["type"] == kind and item["version"] == "1.16.2"
            and item["is_default"] and item["is_present"]
            for item in binaries
        ), f"preseed the default {kind} 1.16.2"
    host = _data(runner_vm, "host", "status")
    assert host["resources"]["nftables_available"] is True


def _remove_owned(runner_vm: str, domain: str, resource: dict) -> None:
    """Admit only the exact name/ID created or attempted by this journey."""
    matches = [item for item in _listed(runner_vm, domain) if item["name"] == resource["name"]]
    assert len(matches) <= 1, matches
    if matches:
        item = matches[0]
        if resource.get("id"):
            assert item["id"] == resource["id"], (resource, item)
        flags = ["--force"] if domain in ("vm", "volume") else []
        _run_mvm(runner_vm, domain, "rm", item["id"], *flags, timeout=120 if domain == "vm" else 60)
    assert all(
        item["name"] != resource["name"] and item["id"] != resource.get("id")
        for item in _listed(runner_vm, domain)
    ), resource


def _remove_traffic_grant(runner_vm: str, grant: dict) -> None:
    policies = _data(runner_vm, "policy", "traffic", "ls")["vm_to_vm"]
    matches = [
        item for item in policies
        if item["source_vm_id"] == grant["source_vm_id"]
        and item["destination_vm_id"] == grant["destination_vm_id"]
        and item["protocol"] == "tcp"
        and item["destination_port_start"] == item["destination_port_end"] == 18080
    ]
    assert len(matches) <= 1, matches
    for item in matches:
        if grant.get("id"):
            assert item["id"] == grant["id"], (grant, item)
        _run_mvm(runner_vm, "policy", "traffic", "rm", item["id"], "--from-vm", "--force")
    assert all(
        item["source_vm_id"] != grant["source_vm_id"]
        or item["destination_vm_id"] != grant["destination_vm_id"]
        or item["protocol"] != "tcp"
        or item["destination_port_start"] != 18080
        for item in _data(runner_vm, "policy", "traffic", "ls")["vm_to_vm"]
    )


@pytest.fixture
def journey_resources(runner_vm: str):
    """Function-owned resources; no stale-prefix cleanup or asset/config mutation."""
    _require_journey_assets(runner_vm)
    owned: dict[str, list[dict]] = {domain: [] for domain in ("vm", "volume", "network", "key", "traffic")}
    baseline = {
        domain: {item["id"] for item in _listed(runner_vm, domain)}
        for domain in ("vm", "volume", "network", "key")
    }
    defaults = {
        domain: sorted(item["id"] for item in _listed(runner_vm, domain) if item["is_default"])
        for domain in ("network", "key")
    }
    config = _data(runner_vm, "config", "ls")
    try:
        yield owned
    finally:
        errors = []
        for grant in reversed(owned["traffic"]):
            try:
                _remove_traffic_grant(runner_vm, grant)
            except Exception as exc:
                errors.append(f"traffic cleanup: {exc}")
        for domain in ("vm", "volume", "network", "key"):
            for resource in reversed(owned[domain]):
                try:
                    _remove_owned(runner_vm, domain, resource)
                except Exception as exc:
                    errors.append(f"{domain} cleanup {resource['name']}: {exc}")
        for domain in ("network", "key"):
            try:
                current = sorted(item["id"] for item in _listed(runner_vm, domain) if item["is_default"])
                if current != defaults[domain] and defaults[domain]:
                    _run_mvm(runner_vm, domain, "default", *defaults[domain])
                elif current != defaults[domain] and domain == "key":
                    _run_mvm(runner_vm, "key", "default", "--clear")
                assert sorted(
                    item["id"] for item in _listed(runner_vm, domain) if item["is_default"]
                ) == defaults[domain]
            except Exception as exc:
                errors.append(f"{domain} default restoration: {exc}")
        for domain, ids in baseline.items():
            try:
                assert {item["id"] for item in _listed(runner_vm, domain)} == ids
            except Exception as exc:
                errors.append(f"{domain} inventory restoration: {exc}")
        try:
            assert _data(runner_vm, "config", "ls") == config, "journey changed runner configuration"
        except Exception as exc:
            errors.append(f"config preservation: {exc}")
        assert not errors, "; ".join(errors)


def _journey_key(runner_vm: str, owned: dict) -> dict:
    resource = {"name": _name("key")}
    owned["key"].append(resource)
    _run_mvm(runner_vm, "key", "create", resource["name"], "--algorithm", "ed25519")
    matches = [item for item in _listed(runner_vm, "key") if item["name"] == resource["name"]]
    assert len(matches) == 1 and matches[0]["is_present"], matches
    resource["id"] = matches[0]["id"]
    return resource


def _journey_network(runner_vm: str, owned: dict) -> dict:
    resource = {"name": _name("net")}
    owned["network"].append(resource)
    resource["subnet"] = _unique_subnet(resource["name"])
    _run_mvm(
        runner_vm, "network", "create", resource["name"],
        "--subnet", resource["subnet"], "--no-nat",
    )
    inspected = _data(runner_vm, "network", "inspect", resource["name"])["network"]
    assert inspected["name"] == resource["name"]
    resource["id"] = inspected["id"]
    return resource


def _journey_vm(
    runner_vm: str, owned: dict, network: dict, *, keys: tuple[dict, ...] = (),
    extra: tuple[str, ...] = (), pci: bool = False,
) -> dict:
    resource = {"name": _name("vm")}
    owned["vm"].append(resource)
    args = [
        "vm", "create", resource["name"], "--image", "alpine:3.23",
        "--network", network["id"], "--cloud-init-mode", "inject", "--skip-deblob",
        "--vcpu", "1", "--mem", "256M", "--disk-size", "512M", "--user", "root",
    ]
    if not pci:
        args.append("--no-pci")
    if keys:
        args.extend(["--ssh-key", ",".join(key["id"] for key in keys)])
    args.extend(extra)
    created = _data(runner_vm, *args, kind=list, timeout=180)
    assert len(created) == 1 and created[0]["name"] == resource["name"], created
    resource["id"] = created[0]["id"]
    assert created[0]["status"] == "running", created
    assert (created[0]["vcpu_count"], created[0]["mem_size_mib"], created[0]["disk_size_mib"]) == (1, 256, 512)
    resource["ipv4"] = created[0]["ipv4"]
    _guest_ready(runner_vm, resource)
    return resource


def _vm_state(runner_vm: str, vm: dict, status: str = "running") -> dict:
    matches = [item for item in _listed(runner_vm, "vm") if item["id"] == vm["id"]]
    assert len(matches) == 1 and matches[0]["name"] == vm["name"], matches
    inspected = _data(runner_vm, "vm", "inspect", vm["id"])
    assert inspected["vm"]["id"] == vm["id"]
    assert matches[0]["status"] == inspected["vm"]["status"] == status
    return matches[0]


def _guest(runner_vm: str, vm: dict, command: str, *, check: bool = True):
    return _run_mvm(
        runner_vm, "exec", vm["id"], "--user", "root", "--timeout", "2",
        "--", command, check=check, timeout=10,
    )


def _guest_ready(runner_vm: str, vm: dict) -> None:
    deadline = time.monotonic() + 30
    last = None
    while time.monotonic() < deadline:
        last = _guest(runner_vm, vm, "id -un", check=False)
        if last.returncode == 0 and last.stdout.strip() == "root":
            return
        time.sleep(0.5)
    pytest.fail(f"guest agent not ready for {vm['id']}: {last}")


def _ssh(runner_vm: str, vm: dict, key: dict, command: str = "uname -a") -> str:
    deadline = time.monotonic() + 30
    last = None
    while time.monotonic() < deadline:
        last = _run_mvm(
            runner_vm, "ssh", vm["id"], "--key", key["id"], "--user", "root",
            "--timeout", "2", "--cmd", command, check=False, timeout=10,
        )
        if last.returncode == 0:
            return last.stdout
        time.sleep(0.5)
    pytest.fail(f"SSH with exact key {key['id']} failed for {vm['id']}: {last}")


def _boot_id(runner_vm: str, vm: dict) -> str:
    _guest_ready(runner_vm, vm)
    value = _guest(runner_vm, vm, "cat /proc/sys/kernel/random/boot_id").stdout.strip()
    assert str(uuid.UUID(value)) == value
    return value


class TestQuickStartJourney:
    pytestmark = [pytest.mark.domain_vm]

    def test_journey_create_and_ssh(self, runner_vm, journey_resources):
        key = _journey_key(runner_vm, journey_resources)
        network = _journey_network(runner_vm, journey_resources)
        vm = _journey_vm(runner_vm, journey_resources, network, keys=(key,))
        assert "Linux" in _ssh(runner_vm, vm, key)


@pytest.mark.needs_network
class TestNetworkVMJourney:
    pytestmark = [pytest.mark.domain_vm]

    def test_journey_network_then_vm(self, runner_vm, journey_resources):
        network = _journey_network(runner_vm, journey_resources)
        vm = _journey_vm(runner_vm, journey_resources, network)
        _vm_state(runner_vm, vm)
        inspected = _data(runner_vm, "vm", "inspect", vm["id"])
        assert inspected["networking"]["network"]["id"] == network["id"]
        assert inspected["networking"]["network"]["name"] == network["name"]
        assert inspected["networking"]["ipv4"] == vm["ipv4"]
        assert vm["ipv4"] in _guest(runner_vm, vm, "ip -4 addr show dev eth0").stdout


class TestKeyVMJourney:
    pytestmark = [pytest.mark.domain_vm]

    def test_journey_key_then_vm(self, runner_vm, journey_resources):
        key = _journey_key(runner_vm, journey_resources)
        network = _journey_network(runner_vm, journey_resources)
        vm = _journey_vm(runner_vm, journey_resources, network, keys=(key,))
        assert key["name"] in _data(runner_vm, "vm", "inspect", vm["id"])["vm"]["ssh_keys"]
        assert "Linux" in _ssh(runner_vm, vm, key)


class TestVMStateJourney:
    pytestmark = [pytest.mark.domain_vm]

    def test_journey_pause_resume_stop_start(self, runner_vm, journey_resources):
        network = _journey_network(runner_vm, journey_resources)
        vm = _journey_vm(runner_vm, journey_resources, network)
        before = _vm_state(runner_vm, vm)
        boot = _boot_id(runner_vm, vm)
        _run_mvm(runner_vm, "vm", "pause", vm["id"])
        _vm_state(runner_vm, vm, "paused")
        _run_mvm(runner_vm, "vm", "resume", vm["id"])
        resumed = _vm_state(runner_vm, vm)
        assert (resumed["pid"], resumed["process_start_time"]) == (before["pid"], before["process_start_time"])
        assert _boot_id(runner_vm, vm) == boot
        _run_mvm(runner_vm, "vm", "stop", vm["id"])
        _vm_state(runner_vm, vm, "stopped")
        _run_mvm(runner_vm, "vm", "start", vm["id"], timeout=120)
        _guest_ready(runner_vm, vm)
        restarted = _vm_state(runner_vm, vm)
        assert (restarted["pid"], restarted["process_start_time"]) != (before["pid"], before["process_start_time"])
        assert _boot_id(runner_vm, vm) != boot
        assert restarted["ipv4"] == vm["ipv4"]
        assert _guest(runner_vm, vm, "printf restarted").stdout == "restarted"


class TestIPJourney:
    pytestmark = [pytest.mark.domain_vm]

    def test_journey_vm_with_explicit_ip(self, runner_vm, journey_resources):
        network = _journey_network(runner_vm, journey_resources)
        key = _journey_key(runner_vm, journey_resources)
        ip = network["subnet"].replace(".0/24", ".100")
        vm = _journey_vm(runner_vm, journey_resources, network, keys=(key,), extra=("--ip", ip))
        assert _vm_state(runner_vm, vm)["ipv4"] == ip
        assert ip in _guest(runner_vm, vm, "ip -4 addr show dev eth0").stdout
        assert "Linux" in _ssh(runner_vm, vm, key)

    def test_journey_multiple_vms_same_network(self, runner_vm, journey_resources):
        network = _journey_network(runner_vm, journey_resources)
        peers = []
        for _ in range(2):
            key = _journey_key(runner_vm, journey_resources)
            vm = _journey_vm(runner_vm, journey_resources, network, keys=(key,))
            peers.append((key, vm))
        addresses = set()
        for key, vm in peers:
            addresses.add(_vm_state(runner_vm, vm)["ipv4"])
            assert _data(runner_vm, "vm", "inspect", vm["id"])["networking"]["network"]["id"] == network["id"]
            assert "Linux" in _ssh(runner_vm, vm, key)
        assert len(addresses) == 2


class TestSSHJourney:
    pytestmark = [pytest.mark.domain_vm]

    def test_journey_ssh_cli_command(self, runner_vm, journey_resources):
        network = _journey_network(runner_vm, journey_resources)
        key = _journey_key(runner_vm, journey_resources)
        vm = _journey_vm(runner_vm, journey_resources, network, keys=(key,))
        assert "Linux" in _ssh(runner_vm, vm, key, "uname -a")

    def test_journey_reboot_chain(self, runner_vm, journey_resources):
        network = _journey_network(runner_vm, journey_resources)
        key = _journey_key(runner_vm, journey_resources)
        vm = _journey_vm(runner_vm, journey_resources, network, keys=(key,))
        assert "Linux" in _ssh(runner_vm, vm, key)
        before = _vm_state(runner_vm, vm)
        boot = _boot_id(runner_vm, vm)
        _run_mvm(runner_vm, "vm", "reboot", vm["id"], timeout=120)
        assert "Linux" in _ssh(runner_vm, vm, key)
        after = _vm_state(runner_vm, vm)
        assert (after["pid"], after["process_start_time"]) != (before["pid"], before["process_start_time"])
        assert _boot_id(runner_vm, vm) != boot
        assert after["ipv4"] == before["ipv4"]


class TestMultiKeyJourney:
    pytestmark = [pytest.mark.domain_vm]

    def test_journey_multiple_ssh_keys(self, runner_vm, journey_resources):
        keys = tuple(_journey_key(runner_vm, journey_resources) for _ in range(2))
        network = _journey_network(runner_vm, journey_resources)
        vm = _journey_vm(runner_vm, journey_resources, network, keys=keys)
        assert set(_data(runner_vm, "vm", "inspect", vm["id"])["vm"]["ssh_keys"]) == {key["name"] for key in keys}
        for key in keys:
            assert "Linux" in _ssh(runner_vm, vm, key)


class TestInterVMCommunication:
    pytestmark = [pytest.mark.needs_kvm, pytest.mark.needs_network]

    def test_journey_ping_between_vms(self, runner_vm, journey_resources):
        """The historical inter-VM journey now proves exact TCP authority, not implicit ICMP access."""
        network = _journey_network(runner_vm, journey_resources)
        source = _journey_vm(runner_vm, journey_resources, network)
        destination = _journey_vm(runner_vm, journey_resources, network)
        _vm_state(runner_vm, source)
        _vm_state(runner_vm, destination)
        _guest(
            runner_vm, destination,
            "nohup nc -lk -p 18080 -e /bin/cat </dev/null >/tmp/journey-tcp.log 2>&1 &",
        )
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if _guest(runner_vm, destination, "nc -z -w 1 127.0.0.1 18080", check=False).returncode == 0:
                break
            time.sleep(0.2)
        else:
            pytest.fail("destination TCP listener did not become ready")
        assert _guest(
            runner_vm, destination, f"nc -z -w 1 {destination['ipv4']} 18080"
        ).returncode == 0

        marker = _name("delivery")
        command = f"printf %s {shlex.quote(marker)} | nc -w 1 {destination['ipv4']} 18080"
        denied = _guest(runner_vm, source, command, check=False)
        assert denied.returncode != 0 and marker not in denied.stdout, (denied.stdout, denied.stderr)
        grant = {"source_vm_id": source["id"], "destination_vm_id": destination["id"]}
        journey_resources["traffic"].append(grant)
        created = _data(
            runner_vm, "policy", "traffic", "create", "--from-vm", source["id"],
            "--to-vm", destination["id"], "--protocol", "tcp", "--port", "18080",
        )
        grant["id"] = created["id"]
        inspected = _data(runner_vm, "policy", "traffic", "inspect", grant["id"], "--from-vm")
        assert inspected["source_vm_id"] == source["id"]
        assert inspected["destination_vm_id"] == destination["id"]
        assert inspected["protocol"] == "tcp"
        assert inspected["destination_port_start"] == inspected["destination_port_end"] == 18080
        allowed = _guest(runner_vm, source, command)
        assert allowed.stdout == marker, (allowed.stdout, allowed.stderr)
        _remove_traffic_grant(runner_vm, grant)
        denied = _guest(runner_vm, source, command, check=False)
        assert denied.returncode != 0 and marker not in denied.stdout, (denied.stdout, denied.stderr)
        assert _guest(
            runner_vm, destination, f"nc -z -w 1 {destination['ipv4']} 18080"
        ).returncode == 0


class TestCreateWithAllFlags:
    def test_create_with_all_flags(self, runner_vm, journey_resources):
        network = _journey_network(runner_vm, journey_resources)
        vm = _journey_vm(
            runner_vm, journey_resources, network, pci=True,
            extra=("--enable-logging", "--enable-metrics"),
        )
        data = _data(runner_vm, "vm", "inspect", vm["id"])
        assert (data["resources"]["vcpu"], data["resources"]["mem"], data["resources"]["disk"]) == (1, 256, 512)
        assert data["vm"]["enable_logging"] is True
        assert data["vm"]["enable_metrics"] is True
        assert data["vm"]["pci_enabled"] is True
        assert _guest(runner_vm, vm, "test -d /sys/bus/pci").returncode == 0


class TestMultipleVolumes:
    @pytest.mark.needs_network
    def test_multiple_volumes_on_one_vm(self, runner_vm, journey_resources):
        key = _journey_key(runner_vm, journey_resources)
        network = _journey_network(runner_vm, journey_resources)
        volumes = []
        for _ in range(3):
            volume = {"name": _name("volume")}
            journey_resources["volume"].append(volume)
            _run_mvm(runner_vm, "volume", "create", volume["name"], "16M")
            matches = [item for item in _listed(runner_vm, "volume") if item["name"] == volume["name"]]
            assert len(matches) == 1
            volume["id"] = matches[0]["id"]
            volumes.append(volume)
        args = tuple(arg for volume in volumes for arg in ("--volume", volume["id"]))
        vm = _journey_vm(runner_vm, journey_resources, network, keys=(key,), extra=args)
        _vm_state(runner_vm, vm)
        inspected = _data(runner_vm, "vm", "inspect", vm["id"])
        assert {item["id"] for item in inspected["volumes"]} == {volume["id"] for volume in volumes}
        assert all(item["size"] == 16 * 1024 * 1024 and item["status"] == "attached" for item in inspected["volumes"])
        assert "Linux" in _ssh(runner_vm, vm, key)
        sizes = _guest(
            runner_vm, vm, "cat /sys/class/block/vdb/size /sys/class/block/vdc/size /sys/class/block/vdd/size"
        ).stdout.splitlines()
        assert sizes == ["32768"] * 3, sizes


class TestStressCreateDestroy:
    def test_stress_create_destroy_sequential(self, runner_vm, journey_resources):
        """All five bounded create/destroy cycles must converge with no owned VM left."""
        network = _journey_network(runner_vm, journey_resources)
        ids = set()
        for _ in range(5):
            vm = _journey_vm(runner_vm, journey_resources, network)
            ids.add(_vm_state(runner_vm, vm)["id"])
            assert _guest(runner_vm, vm, "printf cycle-ready").stdout == "cycle-ready"
            _remove_owned(runner_vm, "vm", vm)
        assert len(ids) == 5
        assert not ids.intersection(item["id"] for item in _listed(runner_vm, "vm"))


class TestExportImport:
    @pytest.mark.needs_network
    def test_inspect_provides_full_config(self, runner_vm, journey_resources):
        network = _journey_network(runner_vm, journey_resources)
        vm = _journey_vm(runner_vm, journey_resources, network)
        data = _data(runner_vm, "vm", "inspect", vm["id"])
        for section in ("vm", "resources", "networking", "assets", "filesystem", "console"):
            assert isinstance(data[section], dict), (section, data)
        for key in ("name", "status", "ssh_user", "cloud_init_mode"):
            assert key in data["vm"]
        assert data["vm"]["name"] == vm["name"] and data["vm"]["id"] == vm["id"]
        assert data["assets"]["image"]["type"] == "alpine"
        assert data["assets"]["image"]["version"] == "3.23"
        assert (data["resources"]["vcpu"], data["resources"]["mem"], data["resources"]["disk"]) == (1, 256, 512)
        assert data["networking"]["ipv4"] == vm["ipv4"]
        assert data["networking"]["mac"]
        assert data["networking"]["network"]["id"] == network["id"]
        assert data["filesystem"]["rootfs_path"]
        assert _guest(runner_vm, vm, "uname -s").stdout.strip() == "Linux"


class TestConcurrentVMCreation:
    pytestmark = [pytest.mark.needs_kvm]

    def test_concurrent_vm_creation_and_ssh(self, runner_vm, journey_resources):
        """Ten creations converge in concurrent cohorts of at most three live guests."""
        network = _journey_network(runner_vm, journey_resources)
        keys = [_journey_key(runner_vm, journey_resources) for _ in range(10)]
        completed = set()
        # Retain the ten-VM workload; verify each cohort together before removal
        # frees the 2 GiB runner's memory/disk for the next concurrent cohort.
        with ThreadPoolExecutor(max_workers=3) as executor:
            for offset in range(0, 10, 3):
                cohort_keys = keys[offset:offset + 3]
                futures = [
                    executor.submit(_journey_vm, runner_vm, journey_resources, network, keys=(key,))
                    for key in cohort_keys
                ]
                cohort = [future.result() for future in futures]
                cohort_ids = {vm["id"] for vm in cohort}
                assert len(cohort_ids) == len(cohort_keys)
                current = [
                    item for item in _listed(runner_vm, "vm")
                    if item["id"] in cohort_ids
                ]
                assert len(current) == len(cohort)
                assert all(item["status"] == "running" for item in current)
                ssh = [
                    executor.submit(_ssh, runner_vm, vm, key)
                    for vm, key in zip(cohort, cohort_keys)
                ]
                assert all("Linux" in future.result() for future in ssh)
                completed.update(cohort_ids)
                for vm in cohort:
                    _remove_owned(runner_vm, "vm", vm)
                assert not cohort_ids.intersection(item["id"] for item in _listed(runner_vm, "vm"))
        assert len(completed) == 10
        assert not completed.intersection(item["id"] for item in _listed(runner_vm, "vm"))
