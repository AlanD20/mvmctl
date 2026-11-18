"""Full-memory restore keeps guest state but inherits no floating-IP authority."""

import os

import pytest

from tests.system.conftest import _json_success_data, _run_mvm
from tests.system.network.floating_ip_helpers import FloatingIPJourney
from tests.system.network.test_policies import _assert_tcp_peer, _tcp_data, _tcp_vm_binding

pytestmark = [
    pytest.mark.system, pytest.mark.domain_network, pytest.mark.requires_kvm,
    pytest.mark.requires_network, pytest.mark.slow, pytest.mark.tier2,
]


@pytest.fixture
def snapshot_journey(runner_vm):
    journey = FloatingIPJourney(runner_vm)
    capture = {"name": f"fip-capture-{journey.suffix}", "id": None, "restored": None, "source_id": None}
    try:
        yield journey, capture
    finally:
        errors = []
        # Release the exact restored reference before removing the memory capture.
        try:
            if capture["restored"] is not None:
                restored = capture["restored"]
                matches = [vm for vm in _tcp_data(runner_vm, "vm", "ls", kind=list)
                           if vm["name"] == restored["name"]]
                if matches:
                    assert len(matches) == 1 and _tcp_vm_binding(matches[0]) == restored, matches
                    _run_mvm(runner_vm, "vm", "rm", restored["id"], "--force", timeout=120)
                assert all(vm["id"] != restored["id"] for vm in
                           _tcp_data(runner_vm, "vm", "ls", kind=list))
        except Exception as exc:
            errors.append(f"restored VM cleanup: {exc}")
        try:
            if capture["id"] is None and capture["source_id"] is not None:
                matches = [item for item in _tcp_data(runner_vm, "snapshot", "ls", kind=list)
                           if item["name"] == capture["name"]]
                assert len(matches) <= 1, matches
                if matches:
                    assert matches[0]["source_vm_id"] == capture["source_id"], matches
                    assert matches[0]["id"] not in {item["id"] for item in journey.before["snapshot"]}
                    capture["id"] = matches[0]["id"]
            if capture["id"] is not None:
                _run_mvm(runner_vm, "snapshot", "rm", capture["id"], "--force", timeout=120)
            assert all(item["name"] != capture["name"] for item in
                       _tcp_data(runner_vm, "snapshot", "ls", kind=list))
        except Exception as exc:
            errors.append(f"snapshot cleanup: {exc}")
        try:
            journey.cleanup()
        except Exception as exc:
            errors.append(str(exc))
        assert not errors, "\n".join(errors)


@pytest.mark.timeout(900, method="signal")
def test_memory_restore_requires_fresh_current_incarnation_grants(snapshot_journey):
    assert os.getuid() > 0 and os.geteuid() == os.getuid(), (
        "run as the enrolled non-root qualification operator"
    )
    journey, capture = snapshot_journey
    network = journey.network("snapshot")
    probe = journey.vm("probe", network)
    source = journey.vm("captured", network)
    resource = journey.resource(network, "service")
    log = journey.listener(source)
    journey.policy(probe, resource, "vm")
    grants = [journey.grant(source, resource, method)
              for method in ("inspect", "associate", "renew", "release")]
    initial = journey.control(source, resource)
    associated = journey.control(source, resource, "associate", initial["generation"])
    assert associated["ownership"] == "self" and associated["lease_remaining_ms"] > 0
    journey.flow(probe, source, log, resource["ipv4"], allowed=True)
    # Preserve a real address across memory restore. Guest hygiene must not be
    # sufficient to explain the denied packet: this changed label is unmanaged.
    stale = journey.stale_alias(source, resource)
    journey.flow(probe, source, log, resource["ipv4"], allowed=True)
    marker = f"snapshot-memory-{journey.suffix}"
    _run_mvm(journey.runner, "exec", source["binding"]["id"], "--user", "root", "--",
             f"printf '%s' '{marker}' > /root/fip-snapshot-marker && sync")
    capture["source_id"] = source["binding"]["id"]
    result = _run_mvm(journey.runner, "snapshot", "create", source["binding"]["id"],
                      "--name", capture["name"], "--json", check=False, timeout=240)
    intents = [line.removeprefix("Snapshot request ID: ") for line in result.stderr.splitlines()
               if line.startswith("Snapshot request ID: ")]
    assert len(intents) <= 1, result.stderr
    if intents:
        capture["id"] = intents[0]
    assert result.returncode == 0, (result.stdout, result.stderr)
    captured = _json_success_data(result.stdout, dict)
    assert captured["name"] == capture["name"]
    assert captured["source_vm_id"] == source["binding"]["id"]
    assert captured["id"] not in {item["id"] for item in journey.before["snapshot"]}
    if capture["id"] is not None:
        assert captured["id"] == capture["id"]
    capture["id"] = captured["id"]
    assert captured["memory_file"] and captured["state_file"] and captured["rootfs_file"]
    _assert_tcp_peer(journey.runner, source)
    journey.stale_alias(source, resource, previous=stale)
    journey.flow(probe, source, log, resource["ipv4"], allowed=True)

    # Managed full restore reuses its captured endpoint; simultaneous cloning is
    # unsupported. Retire the exact source, then admit an independently named VM.
    _run_mvm(journey.runner, "vm", "rm", source["binding"]["id"], "--force", timeout=120)
    assert all(vm["id"] != source["binding"]["id"] for vm in
               _tcp_data(journey.runner, "vm", "ls", kind=list))
    del journey.vms[source["binding"]["name"]]
    for grant in grants:
        retired = _tcp_data(journey.runner, "floating-ip", "delegation", "inspect", grant["id"],
                            "--network", network["id"])
        assert retired["state"] == "revoked" and retired["revision"] > grant["revision"], retired
    name = f"fip-restored-{journey.suffix}"
    journey.vms[name] = (network["id"], None)  # Retain intent before restore effects.
    restored_items = _json_success_data(_run_mvm(
        journey.runner, "snapshot", "restore", capture["id"], name, "--resume", "--json", timeout=180,
    ).stdout, list)
    assert len(restored_items) == 1, restored_items
    binding = _tcp_vm_binding(restored_items[0])
    capture["restored"] = binding
    journey.vms[name] = (network["id"], binding)
    assert binding["name"] == name and binding["id"] != source["binding"]["id"]
    for key in ("network_id", "ipv4", "mac"):
        assert binding[key] == source["binding"][key], (binding, source)
    matches = [item for item in _tcp_data(journey.runner, "vm", "ls", kind=list)
               if item["id"] == binding["id"] or item["name"] == name]
    assert len(matches) == 1 and _tcp_vm_binding(matches[0]) == binding, matches
    current = matches[0]
    restored = {"binding": binding, "pid": current["pid"],
                "process_start_time": current["process_start_time"], "boot_id": source["boot_id"]}
    assert (restored["pid"], restored["process_start_time"]) != (source["pid"], source["process_start_time"])
    _assert_tcp_peer(journey.runner, restored)
    assert _run_mvm(journey.runner, "exec", binding["id"], "--user", "root", "--timeout", "30", "--",
                    "cat /proc/sys/kernel/random/boot_id").stdout.strip() == source["boot_id"]
    assert _run_mvm(journey.runner, "exec", binding["id"], "--user", "root", "--",
                    "cat /root/fip-snapshot-marker").stdout == marker
    journey.stale_alias(restored, resource, previous=stale)
    journey.control(restored, resource, "renew", associated["generation"], failure="unavailable")
    journey.flow(probe, restored, log, resource["ipv4"], allowed=False)
    journey.stale_alias(restored, resource, previous=stale)

    # New session setup first recovers guest-local receipts. Remove the exact
    # deliberately changed alias after the denied packet proof.
    journey.remove_stale_alias(restored, resource, stale)
    current_inspect = journey.grant(restored, resource, "inspect")
    assert current_inspect["id"] not in {grant["id"] for grant in grants}
    assert current_inspect["vm_id"] == binding["id"]
    assert current_inspect["boot_id"] == grants[0]["boot_id"], "runner boot must stay unchanged"
    observed = journey.control(restored, resource)
    assert observed["ownership"] == "unassociated" and observed["lease_remaining_ms"] == 0
    assert observed["generation"] > associated["generation"]
    journey.control(restored, resource, "associate", observed["generation"], failure="denied")
    journey.control(restored, resource, "renew", associated["generation"], failure="denied")
    journey.flow(probe, restored, log, resource["ipv4"], allowed=False)
    journey.alias(restored, resource, present=False)
    current_associate = journey.grant(restored, resource, "associate")
    assert current_associate["vm_id"] == current_inspect["vm_id"]
    assert current_associate["launch_generation"] == current_inspect["launch_generation"]
    assert current_associate["id"] not in {grant["id"] for grant in grants}
    active = journey.control(restored, resource, "associate", observed["generation"])
    assert active["ownership"] == "self" and active["generation"] > observed["generation"]
    assert active["lease_remaining_ms"] > 0
    journey.alias(restored, resource, present=True)
    journey.flow(probe, restored, log, resource["ipv4"], allowed=True)
