"""Exact resource-destination policy follows ownership without widening delivery."""

import shlex

from tests.system.conftest import _run_mvm

import pytest

from tests.system.network.floating_ip_helpers import floating_journey  # noqa: F401

pytestmark = [
    pytest.mark.system, pytest.mark.domain_network, pytest.mark.requires_kvm,
    pytest.mark.requires_network, pytest.mark.slow, pytest.mark.tier2,
]


@pytest.mark.parametrize("placement", ["same", "cross"])
@pytest.mark.parametrize("source_kind", ["vm", "network"])
@pytest.mark.timeout(1200, method="signal")
def test_floating_ip_policy_exact_destination_and_movement(floating_journey, placement, source_kind):
    journey = floating_journey
    destination_net = journey.network("destination")
    source_net = destination_net if placement == "same" else journey.network("source")
    outsider_net = journey.network("outsider")
    source = journey.vm("source", source_net)
    source_peer = journey.vm("source-peer", source_net)
    owner = journey.vm("owner", destination_net)
    replacement = journey.vm("replacement", destination_net)
    outsider = journey.vm("outsider", outsider_net)
    service = journey.resource(destination_net, "service")
    alias = journey.resource(destination_net, "other", offset=101)
    owned = journey.associate(owner, service)
    journey.associate(owner, alias)
    owner_log = journey.listener(owner)
    replacement_log = journey.listener(replacement)
    source_log = journey.listener(source)
    journey.flow(source, owner, owner_log, service["ipv4"], allowed=False)
    policy = journey.policy(source if source_kind == "vm" else source_net, service, source_kind)
    journey.flow(source, owner, owner_log, service["ipv4"], allowed=True)
    # A VM grant must not accidentally become a source-network grant.
    journey.flow(source_peer, owner, owner_log, service["ipv4"],
                 allowed=source_kind == "network")
    for address, port in ((owner["binding"]["ipv4"], 8080), (alias["ipv4"], 8080),
                          (service["ipv4"], 8081)):
        journey.flow(source, owner, owner_log, address, port, allowed=False)
    journey.flow(outsider, owner, owner_log, service["ipv4"], allowed=False)
    journey.flow(owner, source, source_log, source["binding"]["ipv4"], allowed=False)

    released = journey.control(owner, service, "release", owned["generation"])
    assert released["ownership"] == "unassociated"
    journey.alias(owner, service, present=False)
    journey.flow(source, owner, owner_log, service["ipv4"], allowed=False)
    moved = journey.associate(replacement, service)
    assert moved["generation"] > released["generation"]
    journey.alias(owner, service, present=False)
    nonce = journey.flow(source, replacement, replacement_log, service["ipv4"], allowed=True)
    # The new receiver ACK and its log must not mask delivery to the old receiver.
    result = _run_mvm(
        journey.runner, "exec", owner["binding"]["id"], "--user", "root", "--",
        shlex.join(["python3", "-c",
                    "import pathlib,sys; assert sys.argv[2].encode() not in "
                    "pathlib.Path(sys.argv[1]).read_bytes().splitlines()",
                    owner_log + "/8080.received", nonce]),
    )
    assert result.returncode == 0
    journey.assert_policy(policy, journey.policy_intents[-1])
    journey.flow(source, replacement, replacement_log, replacement["binding"]["ipv4"], allowed=False)
