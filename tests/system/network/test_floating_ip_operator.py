"""Persistent operator ownership, finite guest authority and lifecycle fencing."""

import json
import shlex

import pytest

from tests.system.conftest import _run_mvm
from tests.system.network.floating_ip_helpers import floating_journey  # noqa: F401
from tests.system.network.test_floating_ip_lease_lifecycle import _operator
from tests.system.network.test_policies import _start_tcp_listener, _tcp_data

pytestmark = [
    pytest.mark.system, pytest.mark.domain_network, pytest.mark.requires_kvm,
    pytest.mark.requires_network, pytest.mark.slow, pytest.mark.tier2,
]


def _host(journey, resource, method="inspect", vm=None, generation=None):
    args = ["floating-ip", "--network", resource["network_id"], method, resource["id"]]
    if vm is not None:
        args.append(vm["binding"]["id"])
    if generation is not None:
        args += ["--generation", str(generation)]
    return _tcp_data(journey.runner, *args)


def _persistent(item, resource, vm, *, synchronized=False):
    for key in ("id", "network_id", "name", "ipv4"):
        assert item[key] == resource[key], item
    assert item["generation"] > resource["generation"] and item["retired"] is False, item
    assert item["phase"] == "owned" and item["owner_vm_id"] == vm["binding"]["id"], item
    assert item["owner_origin"] == "operator" and item["lifetime"] == "persistent", item
    assert item["authority_state"] == "active" and item["lease_remaining_ms"] == 0, item
    assert item["owner_boot_id"] and item["owner_launch_generation"] > 0, item
    if synchronized:
        assert item["alias_synchronization"] == "synchronized", item


def _ownerless(item, resource, *, inspection=False):
    for key in ("id", "network_id", "name", "ipv4"):
        assert item[key] == resource[key], item
    # Release acknowledges completed fencing; unowned inspection conservatively
    # returns the root record without claiming a fresh kernel observation.
    assert item["phase"] == "unassociated", item
    assert item["authority_state"] == ("unobserved" if inspection else "absent"), item
    assert item["owner_vm_id"] == item["owner_origin"] == item["lifetime"] == item["owner_boot_id"] == "", item
    assert item["owner_launch_generation"] == item["lease_remaining_ms"] == 0, item
    assert item["retired"] is False, item


def _grants(journey, resource):
    return _tcp_data(journey.runner, "floating-ip", "delegation", "ls", resource["id"],
                     "--network", resource["network_id"], kind=list)


def _case(journey):
    _operator()
    network = journey.network("operator")
    source = journey.vm("source", network)
    owner = journey.vm("owner", network)
    resource = journey.resource(network, "service")
    log = journey.listener(owner)
    return network, source, owner, resource, log


def _absent_nonce(journey, vm, log, nonce):
    result = _run_mvm(
        journey.runner, "exec", vm["binding"]["id"], "--user", "root", "--",
        shlex.join(["python3", "-c", "import pathlib,sys; assert sys.argv[2].encode() not in "
                    "pathlib.Path(sys.argv[1]).read_bytes().splitlines()", log + "/8080.received", nonce]),
    )
    assert result.returncode == 0


@pytest.mark.timeout(1200, method="signal")
def test_host_ownership_is_usable_at_return_and_policy_follows_transfer(floating_journey):
    journey = floating_journey
    network, source, owner, resource, log = _case(journey)
    replacement = journey.vm("replacement", network)
    replacement_log = journey.listener(replacement)
    assert _grants(journey, resource) == []
    active = _host(journey, resource, "associate", owner, resource["generation"])
    _persistent(active, resource, owner, synchronized=True)
    journey.alias(owner, resource, present=True)
    assert _grants(journey, resource) == []
    journey.flow(source, owner, log, resource["ipv4"], allowed=False)
    other = journey.resource(network, "other", offset=101)
    _persistent(_host(journey, other, "associate", owner), other, owner, synchronized=True)
    policy = journey.policy(source, resource, "vm")
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
    for address, port in ((owner["binding"]["ipv4"], 8080), (other["ipv4"], 8080),
                          (resource["ipv4"], 8081)):
        journey.flow(source, owner, log, address, port, allowed=False)
    _persistent(_host(journey, resource), resource, owner)

    refused = _run_mvm(journey.runner, "floating-ip", "transfer", resource["id"],
                       replacement["binding"]["id"], "--network", network["id"],
                       "--generation", str(resource["generation"]), "--json", check=False)
    assert refused.returncode != 0
    error = json.loads(refused.stdout)["error"]
    assert error["code"] == "validation.failed" and error["class"] == 2, error
    assert _host(journey, resource)["generation"] == active["generation"]
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)

    stale = journey.stale_alias(owner, resource)
    moved = _host(journey, resource, "transfer", replacement, active["generation"])
    _persistent(moved, resource, replacement, synchronized=True)
    assert moved["generation"] > active["generation"]
    journey.stale_alias(owner, resource, previous=stale)
    journey.alias(replacement, resource, present=True)
    nonce = journey.flow(source, replacement, replacement_log, resource["ipv4"], allowed=True)
    _absent_nonce(journey, owner, log, nonce)
    journey.stale_alias(owner, resource, previous=stale)
    journey.assert_policy(policy, journey.policy_intents[-1])
    journey.flow(source, replacement, replacement_log, replacement["binding"]["ipv4"], allowed=False)
    assert _grants(journey, resource) == []

    released = _host(journey, resource, "release", generation=moved["generation"])
    _ownerless(released, resource)
    assert released["alias_synchronization"] == "synchronized"
    assert released["generation"] > moved["generation"]
    journey.alias(replacement, resource, present=False)
    journey.flow(source, replacement, replacement_log, resource["ipv4"], allowed=False)
    # Old unmanaged alias remains present, so denial also proves root fencing.
    journey.stale_alias(owner, resource, previous=stale)
    journey.flow(source, owner, log, resource["ipv4"], allowed=False)
    journey.stale_alias(owner, resource, previous=stale)
    journey.remove_stale_alias(owner, resource, stale)
    _ownerless(_host(journey, resource), resource, inspection=True)
    journey.assert_policy(policy, journey.policy_intents[-1])


@pytest.mark.timeout(900, method="signal")
def test_finite_guest_grants_cannot_convert_operator_ownership_but_can_release(floating_journey):
    journey = floating_journey
    _, source, owner, resource, log = _case(journey)
    active = _host(journey, resource, "associate", owner)
    _persistent(active, resource, owner, synchronized=True)
    journey.policy(source, resource, "vm")
    grants = {method: journey.grant(owner, resource, method, lease_seconds=12)
              for method in ("inspect", "associate", "renew", "release")}
    observed = journey.control(owner, resource)
    assert observed["ownership"] == "self" and observed["lifetime"] == "persistent"
    assert observed["lease_remaining_ms"] == 0
    for method in ("associate", "renew"):
        journey.control(owner, resource, method, active["generation"], failure="denied")
        current = _host(journey, resource)
        _persistent(current, resource, owner)
        assert current["generation"] == active["generation"]
        journey.flow(source, owner, log, resource["ipv4"], allowed=True)
    revoked = _tcp_data(journey.runner, "floating-ip", "delegation", "revoke", grants["associate"]["id"],
                        "--network", resource["network_id"])
    assert revoked["id"] == grants["associate"]["id"] and revoked["state"] == "revoked"
    assert revoked["revision"] > grants["associate"]["revision"]
    current = _host(journey, resource)
    _persistent(current, resource, owner)
    assert current["generation"] == active["generation"]
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
    released = journey.control(owner, resource, "release", active["generation"])
    assert released["ownership"] == "unassociated" and released["lease_remaining_ms"] == 0
    assert released["generation"] > active["generation"]
    journey.alias(owner, resource, present=False)
    journey.flow(source, owner, log, resource["ipv4"], allowed=False)
    _ownerless(_host(journey, resource), resource, inspection=True)


@pytest.mark.timeout(900, method="signal")
def test_vm_stop_fences_persistent_owner_and_restart_requires_new_host_claim(floating_journey):
    journey = floating_journey
    _, source, owner, resource, log = _case(journey)
    active = _host(journey, resource, "associate", owner)
    _persistent(active, resource, owner, synchronized=True)
    policy = journey.policy(source, resource, "vm")
    grants = [journey.grant(owner, resource, method) for method in ("inspect", "associate", "release")]
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
    _run_mvm(journey.runner, "vm", "stop", owner["binding"]["id"], "--force", timeout=120)
    assert _tcp_data(journey.runner, "vm", "inspect", owner["binding"]["id"])["vm"]["status"] == "stopped"
    fenced = _host(journey, resource)
    _ownerless(fenced, resource, inspection=True)
    assert fenced["generation"] > active["generation"]
    for grant in grants:
        retired = _tcp_data(journey.runner, "floating-ip", "delegation", "inspect", grant["id"],
                            "--network", resource["network_id"])
        assert retired["state"] == "revoked" and retired["revision"] > grant["revision"]
    _run_mvm(journey.runner, "vm", "start", owner["binding"]["id"], timeout=120)
    restarted = journey.refresh_after_start(owner)
    journey.alias(restarted, resource, present=False)
    _ownerless(_host(journey, resource), resource, inspection=True)
    journey.control(restarted, resource, failure="unavailable")
    inspect = journey.grant(restarted, resource, "inspect")
    assert inspect["launch_generation"] > grants[0]["launch_generation"]
    assert inspect["id"] not in {grant["id"] for grant in grants}
    observed = journey.control(restarted, resource)
    assert observed["ownership"] == "unassociated" and observed["lease_remaining_ms"] == 0
    journey.control(restarted, resource, "associate", observed["generation"], failure="denied")
    restarted_log = f"/tmp/floating-restarted-{journey.suffix}"
    _start_tcp_listener(journey.runner, restarted, restarted_log)
    journey.flow(source, restarted, restarted_log, resource["ipv4"], allowed=False)
    claimed = _host(journey, resource, "associate", restarted, fenced["generation"])
    _persistent(claimed, resource, restarted, synchronized=True)
    assert claimed["owner_launch_generation"] > active["owner_launch_generation"]
    journey.alias(restarted, resource, present=True)
    journey.flow(source, restarted, restarted_log, resource["ipv4"], allowed=True)
    journey.assert_policy(policy, journey.policy_intents[-1])
