"""Finite lease and incarnation boundaries through the installed operator CLI.

Whole-root-worker failure, snapshots, environment and host reboot have dedicated
follow-up selectors; these nonfault journeys do not qualify those boundaries.
"""

import os
import time

import pytest

from tests.system.conftest import _run_mvm
from tests.system.network.floating_ip_helpers import floating_journey  # noqa: F401
from tests.system.network.test_policies import _assert_tcp_peer, _tcp_data

pytestmark = [
    pytest.mark.system, pytest.mark.domain_network, pytest.mark.requires_kvm,
    pytest.mark.requires_network, pytest.mark.slow, pytest.mark.tier2,
]


def _operator():
    assert os.getuid() > 0 and os.geteuid() == os.getuid(), (
        "run the installed CLI as the enrolled non-root qualification operator"
    )


def _wait_past(deadline):
    while time.monotonic() <= deadline:
        time.sleep(min(0.25, max(0.01, deadline - time.monotonic())))


def _traffic_case(journey):
    network = journey.network("lease")
    source = journey.vm("source", network)
    owner = journey.vm("owner", network)
    resource = journey.resource(network, "service")
    log = journey.listener(owner)
    journey.policy(source, resource, "vm")
    return source, owner, resource, log


@pytest.mark.timeout(720, method="signal")
def test_renewal_survives_original_expiry_then_fences_stale_alias(floating_journey):
    _operator()
    journey = floating_journey
    source, owner, resource, log = _traffic_case(journey)
    for method in ("inspect", "associate", "renew", "release"):
        journey.grant(owner, resource, method, lease_seconds=24)
    initial = journey.control(owner, resource)
    associated = journey.control(owner, resource, "associate", initial["generation"])
    associated_returned = time.monotonic()
    assert associated["ownership"] == "self" and 0 < associated["lease_remaining_ms"] <= 24_000
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
    _wait_past(associated_returned + 10)
    renew_started = time.monotonic()
    renewed = journey.control(owner, resource, "renew", associated["generation"])
    renew_returned = time.monotonic()
    assert renewed["generation"] == associated["generation"] and renewed["ownership"] == "self"
    assert 0 < renewed["lease_remaining_ms"] <= 24_000
    stale = journey.stale_alias(owner, resource)

    # Host association happened before its command returned. Return+24 is thus
    # later than EVERY possible original expiry, irrespective of response latency.
    latest_original_expiry = associated_returned + 24
    _wait_past(latest_original_expiry + 0.25)
    assert time.monotonic() > latest_original_expiry
    # The complete ACK must still fit inside a conservative renewed lower bound.
    earliest_renewed_expiry = renew_started + renewed["lease_remaining_ms"] / 1000
    assert time.monotonic() + 4 < earliest_renewed_expiry, "renewal observation left no bounded probe window"
    journey.stale_alias(owner, resource, previous=stale)
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
    assert time.monotonic() < earliest_renewed_expiry

    _wait_past(renew_returned + 24 + 0.25)
    journey.stale_alias(owner, resource, previous=stale)
    journey.flow(source, owner, log, resource["ipv4"], allowed=False)
    journey.stale_alias(owner, resource, previous=stale)
    observed = journey.control(owner, resource)
    assert observed["ownership"] == "unassociated" and observed["lease_remaining_ms"] == 0
    journey.stale_alias(owner, resource, previous=stale)


@pytest.mark.timeout(720, method="signal")
def test_stop_start_retires_old_grants_and_requires_current_incarnation(floating_journey):
    _operator()
    journey = floating_journey
    network = journey.network("restart")
    owner = journey.vm("owner", network)
    resource = journey.resource(network, "service")
    grants = [journey.grant(owner, resource, method) for method in ("inspect", "associate", "release")]
    initial = journey.control(owner, resource)
    associated = journey.control(owner, resource, "associate", initial["generation"])
    assert associated["ownership"] == "self"
    journey.alias(owner, resource, present=True)
    _run_mvm(journey.runner, "vm", "stop", owner["binding"]["id"], "--force", timeout=120)
    assert _tcp_data(journey.runner, "vm", "inspect", owner["binding"]["id"])["vm"]["status"] == "stopped"
    for grant in grants:
        retired = _tcp_data(journey.runner, "floating-ip", "delegation", "inspect", grant["id"],
                            "--network", network["id"])
        assert retired["state"] == "revoked" and retired["revision"] > grant["revision"], retired
    _run_mvm(journey.runner, "vm", "start", owner["binding"]["id"], timeout=120)
    restarted = journey.refresh_after_start(owner)
    journey.alias(restarted, resource, present=False)
    journey.control(restarted, resource, failure="unavailable")
    # A new inspect grant starts an authenticated current session, but cannot
    # resurrect the old associate permission from the previous launch.
    current = journey.grant(restarted, resource, "inspect")
    assert current["id"] not in {grant["id"] for grant in grants}
    assert current["launch_generation"] > grants[0]["launch_generation"]
    assert current["boot_id"] == grants[0]["boot_id"], "runner itself must not reboot"
    observed = journey.control(restarted, resource)
    assert observed["ownership"] == "unassociated"
    journey.control(restarted, resource, "associate", observed["generation"], failure="denied")
    journey.grant(restarted, resource, "associate")
    active = journey.control(restarted, resource, "associate", observed["generation"])
    assert active["ownership"] == "self" and active["generation"] > associated["generation"]
    journey.alias(restarted, resource, present=True)


@pytest.mark.timeout(720, method="signal")
def test_pause_expiry_does_not_resume_or_renew_implicitly(floating_journey):
    _operator()
    journey = floating_journey
    source, owner, resource, log = _traffic_case(journey)
    for method in ("inspect", "associate", "release"):
        journey.grant(owner, resource, method, lease_seconds=12)
    initial = journey.control(owner, resource)
    associated = journey.control(owner, resource, "associate", initial["generation"])
    associated_returned = time.monotonic()
    assert associated["ownership"] == "self"
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
    _run_mvm(journey.runner, "vm", "pause", owner["binding"]["id"])
    try:
        # These read operations must never unpause the VM, renew ownership, or
        # manufacture a new launch to make the guest available.
        _tcp_data(journey.runner, "floating-ip", "inspect", resource["id"], "--network", resource["network_id"])
        _tcp_data(journey.runner, "floating-ip", "delegation", "ls", resource["id"],
                  "--network", resource["network_id"], kind=list)
        _wait_past(associated_returned + 12 + 0.25)
        matches = [item for item in _tcp_data(journey.runner, "vm", "ls", kind=list)
                   if item["id"] == owner["binding"]["id"]]
        assert len(matches) == 1, matches
        paused = matches[0]
        assert paused["status"] == "paused"
        assert (paused["pid"], paused["process_start_time"]) == (owner["pid"], owner["process_start_time"])
    finally:
        _run_mvm(journey.runner, "vm", "resume", owner["binding"]["id"])
    _assert_tcp_peer(journey.runner, owner)
    observed = journey.control(owner, resource)
    assert observed["ownership"] == "unassociated" and observed["lease_remaining_ms"] == 0
    journey.flow(source, owner, log, resource["ipv4"], allowed=False)
    # Same-incarnation grants survive pause. The coordinator requires an expired
    # current owner to release before a new associate; resume cannot do either.
    released = journey.control(owner, resource, "release", observed["generation"])
    assert released["ownership"] == "unassociated"
    active = journey.control(owner, resource, "associate", released["generation"])
    assert active["ownership"] == "self"
    journey.alias(owner, resource, present=True)
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
