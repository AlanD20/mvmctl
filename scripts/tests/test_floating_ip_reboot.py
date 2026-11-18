"""Reboot receipt and exact owned-policy preservation failures; no runtime effects."""

import copy
import json

import pytest

from scripts.tests.test_run_system_tests import orchestrator  # noqa: F401
from tests.system.host.floating_ip_reboot import validate_pre, without_owned_policy, without_owned_policies
from tests.system.host.test_host_reboot import _preserve_persistent_inventory


@pytest.fixture
def receipt():
    boot = "11111111-1111-4111-8111-111111111111"
    network, resource, owner, source = "a" * 64, "b" * 32, "c" * 32, "d" * 32
    def peer(vm_id):
        return dict(binding=dict(id=vm_id, network_id=network), pid=12, process_start_time=34, boot_id=boot)
    grants = [dict(id=key * 32, method=method, vm_id=owner, boot_id=boot, network_id=network,
                   resource_id=resource, state="active", launch_generation=2)
              for key, method in (("e", "inspect"), ("f", "associate"), ("0", "release"))]
    persistent = dict(id="4" * 32, network_id=network, name="operator", ipv4="10.91.0.101", generation=1)
    operator = dict(
        resource=persistent,
        associated=dict(id=persistent["id"], network_id=network, name="operator", ipv4=persistent["ipv4"],
                        generation=2, retired=False, phase="owned", owner_vm_id=owner, owner_origin="operator",
                        lifetime="persistent", owner_boot_id=boot, owner_launch_generation=2,
                        authority_state="active", lease_remaining_ms=0, alias_synchronization="synchronized"),
        policy=dict(id="8" * 64, source_vm_id=source, destination_floating_ip_id=persistent["id"], protocol="tcp",
                    destination_port_start=8080, destination_port_end=8080), nonce="5" * 32)
    return dict(schema_version=2, boot_id=boot, network=dict(id=network), operator=operator,
                source=peer(source), owner=peer(owner),
                resource=dict(id=resource, network_id=network, name="service", ipv4="10.91.0.100"), grants=grants,
                policy=dict(id="9" * 64, source_vm_id=source, destination_floating_ip_id=resource, protocol="tcp",
                            destination_port_start=8080, destination_port_end=8080),
                alias=dict(label="eth0:f123456", snapshot=dict(interface="eth0")),
                associated=dict(resource_id=resource, ownership="self", lease_remaining_ms=300000),
                nonce="1" * 32, marker="2" * 32, suffix="3" * 12)


def test_exact_pre_receipt_is_accepted(receipt):
    assert validate_pre(receipt, receipt["boot_id"]) is receipt


@pytest.mark.parametrize("fault", ["schema", "unknown", "host_boot", "resource_network", "same_vm", "stopped_process",
                                  "grant_vm", "grant_resource", "grant_revoked", "duplicate_grant", "policy_source",
                                  "policy_resource", "policy_port", "alias", "no_claim"])
def test_pre_receipt_refuses_rebound_or_incomplete_proof(receipt, fault):
    if fault == "schema":receipt["schema_version"] = 1
    elif fault == "unknown":receipt["worker_pid"] = 100
    elif fault == "host_boot":receipt["boot_id"] = "22222222-2222-4222-8222-222222222222"
    elif fault == "resource_network":receipt["resource"]["network_id"] = "1" * 64
    elif fault == "same_vm":receipt["source"] = copy.deepcopy(receipt["owner"])
    elif fault == "stopped_process":receipt["owner"]["pid"] = 0
    elif fault == "grant_vm":receipt["grants"][0]["vm_id"] = "1" * 32
    elif fault == "grant_resource":receipt["grants"][0]["resource_id"] = "1" * 32
    elif fault == "grant_revoked":receipt["grants"][0]["state"] = "revoked"
    elif fault == "duplicate_grant":receipt["grants"][1]["id"] = receipt["grants"][0]["id"]
    elif fault == "policy_source":receipt["policy"]["source_vm_id"] = receipt["owner"]["binding"]["id"]
    elif fault == "policy_resource":receipt["policy"]["destination_floating_ip_id"] = "1" * 32
    elif fault == "policy_port":receipt["policy"]["destination_port_end"] = 8081
    elif fault == "alias":receipt["alias"]["label"] = "eth1:foreign"
    else:receipt["associated"]["ownership"] = "unassociated"
    with pytest.raises(AssertionError):validate_pre(receipt, "11111111-1111-4111-8111-111111111111")


@pytest.mark.parametrize("fault", ["unknown", "same_resource", "same_address", "network", "owner", "boot",
                                  "launch", "origin", "finite", "remaining_lease", "inactive", "pending_alias",
                                  "unclaimed", "generation", "policy_source", "policy_resource", "policy_port",
                                  "policy_overlap", "nonce_reused"])
def test_pre_receipt_requires_independent_persistent_operator_proof(receipt, fault):
    operator = receipt["operator"]
    associated = operator["associated"]
    if fault == "unknown":operator["worker_pid"] = 10
    elif fault == "same_resource":operator["resource"]["id"] = receipt["resource"]["id"]
    elif fault == "same_address":operator["resource"]["ipv4"] = receipt["resource"]["ipv4"]
    elif fault == "network":operator["resource"]["network_id"] = "1" * 64
    elif fault == "owner":associated["owner_vm_id"] = receipt["source"]["binding"]["id"]
    elif fault == "boot":associated["owner_boot_id"] = "22222222-2222-4222-8222-222222222222"
    elif fault == "launch":associated["owner_launch_generation"] = 0
    elif fault == "origin":associated["owner_origin"] = "delegated"
    elif fault == "finite":associated["lifetime"] = "finite"
    elif fault == "remaining_lease":associated["lease_remaining_ms"] = 1
    elif fault == "inactive":associated["authority_state"] = "absent"
    elif fault == "pending_alias":associated["alias_synchronization"] = "pending"
    elif fault == "unclaimed":associated["phase"] = "unassociated"
    elif fault == "generation":associated["generation"] = operator["resource"]["generation"]
    elif fault == "policy_source":operator["policy"]["source_vm_id"] = receipt["owner"]["binding"]["id"]
    elif fault == "policy_resource":operator["policy"]["destination_floating_ip_id"] = receipt["resource"]["id"]
    elif fault == "policy_port":operator["policy"]["destination_port_start"] = 8079
    elif fault == "policy_overlap":operator["policy"]["id"] = receipt["policy"]["id"]
    else:operator["nonce"] = receipt["nonce"]
    with pytest.raises(AssertionError):validate_pre(receipt, receipt["boot_id"])


@pytest.mark.parametrize("fault", [None, "owned_rebound", "operator_rebound", "duplicate", "operator_missing",
                                  "finite_missing", "unrelated_changed", "unrelated_floating_changed",
                                  "owned_not_cleaned", "operator_not_cleaned", "overlapping_ids"])
def test_raw_preservation_excludes_only_exact_owned_policy(orchestrator, receipt, tmp_path, fault):
    baseline = {domain: [] for domain in ("image", "kernel", "bin", "volume", "key", "snapshot", "config", "exec")}
    baseline["traffic"] = {"vm_to_floating_ip": [{"id": "7" * 64, "port": 443}],
                           "network_to_vm": [{"id": "unrelated", "port": 443}]}
    after = copy.deepcopy(baseline)
    after["traffic"]["vm_to_floating_ip"] += [copy.deepcopy(receipt["policy"]),
                                              copy.deepcopy(receipt["operator"]["policy"])]
    final = copy.deepcopy(baseline)
    rows = after["traffic"]["vm_to_floating_ip"]
    if fault == "owned_rebound":rows[1]["destination_port_end"] = 8081
    elif fault == "operator_rebound":rows[2]["destination_port_end"] = 8081
    elif fault == "duplicate":rows.append(copy.deepcopy(rows[2]))
    elif fault == "operator_missing":rows.pop(2)
    elif fault == "finite_missing":rows.pop(1)
    elif fault == "unrelated_changed":after["traffic"]["network_to_vm"][0]["port"] = 444
    elif fault == "unrelated_floating_changed":rows[0]["port"] = 444
    elif fault == "owned_not_cleaned":final = copy.deepcopy(after)
    elif fault == "operator_not_cleaned":final["traffic"]["vm_to_floating_ip"].append(copy.deepcopy(rows[2]))
    elif fault == "overlapping_ids":receipt["operator"]["policy"]["id"] = receipt["policy"]["id"]
    preserved = dict(before=baseline, after_boot=after, final=final)
    raw = json.dumps(preserved).encode()
    pre = dict(physical=baseline, floating_ip=receipt)
    post = dict(physical_before=baseline, physical_after_boot=after, physical_after=final)
    if fault is None:
        orchestrator._host_reboot_require_preservation(pre, post, raw)
        _preserve_persistent_inventory(tmp_path, baseline, after, final, receipt)
        assert json.loads((tmp_path / "preservation.json").read_bytes()) == preserved
        assert after["traffic"]["vm_to_floating_ip"] == [baseline["traffic"]["vm_to_floating_ip"][0],
                                                         receipt["policy"], receipt["operator"]["policy"]], (
            "raw observation must remain literal, including unrelated floating-IP policies"
        )
    else:
        with pytest.raises(RuntimeError):orchestrator._host_reboot_require_preservation(pre, post, raw)
        with pytest.raises(AssertionError):_preserve_persistent_inventory(tmp_path, baseline, after, final, receipt)


def test_absent_owned_policy_is_not_adopted_for_preservation(receipt):
    traffic = {"vm_to_floating_ip": [], "network_to_vm": []}
    with pytest.raises(AssertionError):without_owned_policy(traffic, receipt["policy"])
    assert without_owned_policy(traffic, receipt["policy"], allow_missing=True) == traffic


def test_two_policy_exclusion_is_copying_exact_receipts_only(receipt):
    unrelated = {"id": "7" * 64, "port": 443}
    traffic = {"vm_to_floating_ip": [unrelated, receipt["operator"]["policy"], receipt["policy"]],
               "network_to_vm": [{"id": "untouched"}]}
    before = copy.deepcopy(traffic)
    assert without_owned_policies(traffic, receipt) == {
        "vm_to_floating_ip": [unrelated], "network_to_vm": [{"id": "untouched"}]}
    assert traffic == before


@pytest.mark.parametrize("fault", [None, "still_owned", "remaining_lease", "packet_not_fenced"])
def test_local_release_error_requires_packet_fencing_then_fresh_host_inspection(monkeypatch, fault):
    from tests.system.host import floating_ip_reboot as helper
    alias_actions = []
    def alias(*args, **kwargs):
        alias_actions.append(kwargs.get("remove", False))
    monkeypatch.setattr(helper, "_stale_alias", alias)
    class Journey:
        runner = "qualification-runner"
        def control(self, owner, resource, method="inspect", generation=None, *, failure=None):
            if method == "release":
                assert failure == "internal" and generation == 2
                return None
            return {"ownership": "self" if fault == "still_owned" else "unassociated",
                    "lease_remaining_ms": 1 if fault == "remaining_lease" else 0}
        def flow(self, source, owner, log, address, *, allowed):
            assert allowed is False
            assert fault != "packet_not_fenced", "packet was delivered after release"
            return "packet-denied-nonce"
    args = (Journey(), {}, {}, {"ipv4": "10.91.0.100"}, "/owned/log", 2, "eth0:f123456", {})
    if fault is None:
        assert helper._release_stale_claim(*args) == "packet-denied-nonce"
    else:
        with pytest.raises(AssertionError):helper._release_stale_claim(*args)
    if fault == "packet_not_fenced":
        assert alias_actions == [], "do not remove stale-alias evidence before denied packet proof"
    else:
        assert alias_actions == [False, True]
