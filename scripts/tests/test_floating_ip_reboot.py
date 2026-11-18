"""Reboot receipt and exact owned-policy preservation failures; no runtime effects."""

import copy
import json

import pytest

from scripts.tests.test_run_system_tests import orchestrator  # noqa: F401
from tests.system.host.floating_ip_reboot import validate_pre, without_owned_policy
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
    return dict(schema_version=1, boot_id=boot, network=dict(id=network),
                source=peer(source), owner=peer(owner), resource=dict(id=resource, network_id=network), grants=grants,
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
    if fault == "schema":receipt["schema_version"] = 2
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


@pytest.mark.parametrize("fault", [None, "owned_rebound", "duplicate", "unrelated_changed", "owned_not_cleaned"])
def test_raw_preservation_excludes_only_exact_owned_policy(orchestrator, receipt, tmp_path, fault):
    baseline = {domain: [] for domain in ("image", "kernel", "bin", "volume", "key", "snapshot", "config", "exec")}
    baseline["traffic"] = {"vm_to_floating_ip": [], "network_to_vm": [{"id": "unrelated", "port": 443}]}
    after = copy.deepcopy(baseline)
    after["traffic"]["vm_to_floating_ip"] = [copy.deepcopy(receipt["policy"])]
    final = copy.deepcopy(baseline)
    if fault == "owned_rebound":after["traffic"]["vm_to_floating_ip"][0]["destination_port_end"] = 8081
    elif fault == "duplicate":after["traffic"]["vm_to_floating_ip"] *= 2
    elif fault == "unrelated_changed":after["traffic"]["network_to_vm"][0]["port"] = 444
    elif fault == "owned_not_cleaned":final = copy.deepcopy(after)
    preserved = dict(before=baseline, after_boot=after, final=final)
    raw = json.dumps(preserved).encode()
    pre = dict(physical=baseline, floating_ip=receipt)
    post = dict(physical_before=baseline, physical_after_boot=after, physical_after=final)
    if fault is None:
        orchestrator._host_reboot_require_preservation(pre, post, raw)
        _preserve_persistent_inventory(tmp_path, baseline, after, final, receipt)
        assert json.loads((tmp_path / "preservation.json").read_bytes()) == preserved
        assert after["traffic"]["vm_to_floating_ip"] == [receipt["policy"]], "raw observation must remain literal"
    else:
        with pytest.raises(RuntimeError):orchestrator._host_reboot_require_preservation(pre, post, raw)
        with pytest.raises(AssertionError):_preserve_persistent_inventory(tmp_path, baseline, after, final, receipt)


def test_absent_owned_policy_is_not_adopted_for_preservation(receipt):
    traffic = {"vm_to_floating_ip": [], "network_to_vm": []}
    with pytest.raises(AssertionError):without_owned_policy(traffic, receipt["policy"])
    assert without_owned_policy(traffic, receipt["policy"], allow_missing=True) == traffic
