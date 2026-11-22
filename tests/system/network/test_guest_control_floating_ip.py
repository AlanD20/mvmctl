"""Real guest-to-host typed operations; stored permissions are not readiness."""

import json
import pathlib
import shlex
import time

import pytest

from tests.system.conftest import _run_mvm
from tests.system.network.floating_ip_helpers import floating_journey  # noqa: F401
from tests.system.network.test_floating_ip_lease_lifecycle import _operator
from tests.system.network.test_floating_ip_operator import _grants, _host, _persistent
from tests.system.network.test_policies import _assert_tcp_peer, _tcp_data

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


@pytest.mark.timeout(720, method="signal")
def test_agent_restart_reconnects_same_incarnation_without_regrant(floating_journey):
    """Restart the guest daemon, leaving the VMM and retained root worker alone."""
    _operator()
    journey = floating_journey
    network = journey.network("reconnect")
    source = journey.vm("source", network)
    owner = journey.vm("owner", network)
    resource = journey.resource(network, "service")
    log = journey.listener(owner)
    policy = journey.policy(source, resource, "vm")
    active = _host(journey, resource, "associate", owner, resource["generation"])
    _persistent(active, resource, owner, synchronized=True)
    # Inspect has no lease policy; only association/renewal grants need one.
    grant = journey.grant(owner, resource, "inspect")
    grants = _grants(journey, resource)
    assert grants == [grant], grants
    assert grant["boot_id"] == active["owner_boot_id"]
    assert grant["launch_generation"] == active["owner_launch_generation"]
    initial = journey.control(owner, resource)
    assert initial["ownership"] == "self" and initial["lifetime"] == "persistent", initial
    assert initial["lease_remaining_ms"] == 0, initial
    journey.alias(owner, resource, present=True)
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
    before = _host(journey, resource)
    host_boot = pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    assert host_boot == grant["boot_id"], grant

    # listener() has already proved guest python3 by running its TCP responder.
    # Match the provisioned daemon, not a transient `mvm-agent control` client.
    snapshot = r"""
import json, pathlib
daemons = []
for entry in pathlib.Path('/proc').iterdir():
    if not entry.name.isdecimal():
        continue
    try:
        executable = (entry / 'exe').readlink()
        args = (entry / 'cmdline').read_bytes().split(b'\0')
        stat = (entry / 'stat').read_text().rsplit(')', 1)[1].split()
    except (FileNotFoundError, ProcessLookupError):
        continue
    if str(executable) == '/usr/bin/mvm-agent' and b'-port' in args:
        daemons.append([int(entry.name), int(stat[19])])
assert len(daemons) == 1, daemons
print(json.dumps({'daemon': daemons[0],
                  'boot_id': pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()}))
"""
    snapshot_command = shlex.join(["python3", "-c", snapshot])
    old = json.loads(_run_mvm(
        journey.runner, "exec", owner["binding"]["id"], "--user", "root", "--", snapshot_command,
    ).stdout)
    assert old["boot_id"] == owner["boot_id"], old
    # The production agent upgrade uses the same detached delay so its exec
    # response can close before the service manager terminates the old daemon.
    restart = (
        "nohup sh -c 'sleep 2; "
        "if command -v systemctl >/dev/null 2>&1; then systemctl restart mvm-agent; "
        "else rc-service mvm-agent restart; fi' >/dev/null 2>&1 </dev/null &"
    )
    _run_mvm(journey.runner, "exec", owner["binding"]["id"], "--user", "root", "--", restart)
    deadline = time.monotonic() + 45
    last = None
    while time.monotonic() < deadline:
        result = _run_mvm(
            journey.runner, "exec", owner["binding"]["id"], "--user", "root", "--timeout", "2",
            "--", snapshot_command, check=False,
        )
        last = (result.returncode, result.stdout, result.stderr)
        if result.returncode == 0:
            current = json.loads(result.stdout)
            assert current["boot_id"] == old["boot_id"], current
            if current["daemon"] != old["daemon"]:
                break
        else:
            # A transport outage is expected while the service changes. Guest
            # command failures and authentication refusals must fail immediately.
            assert (
                "agent did not become reachable within 2s" in result.stderr
                or "failed to read exec response" in result.stderr
                or "failed to send exec request" in result.stderr
            ), last
        time.sleep(0.25)
    else:
        pytest.fail(f"guest mvm-agent daemon did not restart: {last}")

    # Exec readiness precedes the retained worker's authenticated reconnect.
    control = shlex.join(["/usr/bin/mvm-agent", "control", "floating-ip", "inspect", resource["id"]])
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        result = _run_mvm(
            journey.runner, "exec", owner["binding"]["id"], "--user", "root", "--timeout", "2",
            "--", control, check=False,
        )
        if result.returncode == 0:
            assert json.loads(result.stdout) == initial, result.stdout
            break
        assert result.stderr.strip() in (
            "error: guest control is unavailable",
            "error: guest control request failed: unavailable",
        ), (result.stdout, result.stderr)
        time.sleep(0.25)
    else:
        pytest.fail(f"same-incarnation typed control did not recover: {result.stdout} {result.stderr}")

    assert journey.control(owner, resource) == initial
    _assert_tcp_peer(journey.runner, owner)
    assert pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip() == host_boot
    assert _grants(journey, resource) == grants
    assert _tcp_data(journey.runner, "floating-ip", "delegation", "inspect", grant["id"],
                     "--network", network["id"]) == grant
    assert _host(journey, resource) == before
    journey.alias(owner, resource, present=True)
    journey.assert_policy(policy, journey.policy_intents[0])
    journey.flow(source, owner, log, resource["ipv4"], allowed=True)
