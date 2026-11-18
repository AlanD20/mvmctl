"""Real guest-to-host typed operations; stored permissions are not readiness."""

import pytest

from tests.system.network.floating_ip_helpers import floating_journey  # noqa: F401

pytestmark = [
    pytest.mark.system, pytest.mark.domain_network, pytest.mark.requires_kvm,
    pytest.mark.requires_network, pytest.mark.slow, pytest.mark.tier2,
]


@pytest.mark.timeout(720, method="signal")
def test_guest_floating_ip_operations_and_authenticated_denials(floating_journey):
    journey = floating_journey
    network = journey.network("control")
    vm = journey.vm("control", network)
    resource = journey.resource(network, "service")
    unrelated = journey.resource(network, "unrelated", offset=101)
    journey.grant(vm, resource, "inspect")
    # This successful exchange proves the session; a missing gateway is a failure.
    initial = journey.control(vm, resource)
    assert initial["ownership"] == "unassociated" and initial["lease_remaining_ms"] == 0
    journey.control(vm, resource, "associate", initial["generation"], failure="denied")
    journey.control(vm, unrelated, failure="denied")
    assert journey.control(vm, resource) == initial

    journey.assert_nonroot_socket_denied(vm, resource)

    journey.grant(vm, resource, "associate")
    associated = journey.control(vm, resource, "associate", initial["generation"])
    assert associated["ownership"] == "self" and associated["generation"] > initial["generation"]
    journey.alias(vm, resource, present=True)
    journey.control(vm, resource, "renew", associated["generation"], failure="denied")
    assert journey.control(vm, resource)["generation"] == associated["generation"]
    journey.grant(vm, resource, "renew")
    journey.control(vm, resource, "renew", initial["generation"], failure="conflict")
    assert journey.control(vm, resource)["generation"] == associated["generation"]
    renewed = journey.control(vm, resource, "renew", associated["generation"])
    assert renewed["ownership"] == "self" and renewed["generation"] == associated["generation"]
    assert 0 < renewed["lease_remaining_ms"] <= 300_000
    journey.grant(vm, resource, "release")
    released = journey.control(vm, resource, "release", renewed["generation"])
    assert released["ownership"] == "unassociated" and released["lease_remaining_ms"] == 0
    assert released["generation"] > renewed["generation"]
    journey.alias(vm, resource, present=False)
    assert journey.control(vm, resource) == released
