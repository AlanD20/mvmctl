"""Dedicated genuine whole-root-worker stall; requires root qualification observer."""

import time

import pytest

from tests.system.network.floating_ip_helpers import floating_journey  # noqa: F401
from tests.system.network.floating_ip_worker_fault import stalled_root_worker
from tests.system.network.test_floating_ip_lease_lifecycle import _operator, _traffic_case, _wait_past

pytestmark = [
    pytest.mark.system, pytest.mark.domain_network, pytest.mark.requires_kvm,
    pytest.mark.requires_network, pytest.mark.slow, pytest.mark.tier2,
]


@pytest.mark.destructive
@pytest.mark.timeout(720, method="signal")
def test_renewed_expiry_fences_stale_alias_with_whole_root_worker_stalled(floating_journey):
    _operator()
    journey = floating_journey
    source, owner, resource, log = _traffic_case(journey)
    for method in ("inspect", "associate", "renew", "release"):
        journey.grant(owner, resource, method, lease_seconds=24)
    initial = journey.control(owner, resource)
    associated = journey.control(owner, resource, "associate", initial["generation"])
    associated_returned = time.monotonic()
    assert associated["ownership"] == "self"
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
    _wait_past(associated_returned + 10)
    renew_started = time.monotonic()
    renewed = journey.control(owner, resource, "renew", associated["generation"])
    renew_returned = time.monotonic()
    assert renewed["ownership"] == "self" and renewed["generation"] == associated["generation"]
    assert 0 < renewed["lease_remaining_ms"] <= 24_000
    stale = journey.stale_alias(owner, resource)
    with stalled_root_worker(journey.runner, owner) as stopped:
        assert stopped["all_threads_stopped"] is True
        # No control request or lifecycle mutation may wait on the stalled worker.
        # Its Jailer locks were released after verified whole-process group-stop.
        latest_original_expiry = associated_returned + 24
        _wait_past(latest_original_expiry + 0.25)
        earliest_renewed_expiry = renew_started + renewed["lease_remaining_ms"] / 1000
        assert time.monotonic() > latest_original_expiry
        assert time.monotonic() + 4 < earliest_renewed_expiry
        journey.stale_alias(owner, resource, previous=stale)
        journey.flow(source, owner, log, resource["ipv4"], allowed=True)
        assert time.monotonic() < earliest_renewed_expiry
        _wait_past(renew_returned + 24 + 0.25)
        journey.stale_alias(owner, resource, previous=stale)
        journey.flow(source, owner, log, resource["ipv4"], allowed=False)
        journey.stale_alias(owner, resource, previous=stale)
    # Context exit proves the same root process remained stopped, then resumes it
    # through the retained pidfd even if a packet or stale-alias assertion failed.
