"""Owned floating-IP evidence inside the existing two singleton reboot phases."""

import copy
import json
import re
import shlex
import uuid

from tests.system.conftest import _run_mvm
from tests.system.network.floating_ip_helpers import FloatingIPJourney
from tests.system.network.test_policies import _tcp_data

_FIELDS = {"schema_version", "boot_id", "network", "source", "owner", "resource", "grants",
           "policy", "alias", "associated", "nonce", "marker", "suffix"}


def validate_pre(receipt, boot):
    assert set(receipt) == _FIELDS and type(receipt["schema_version"]) is int and receipt["schema_version"] == 1
    assert receipt["boot_id"] == boot and str(uuid.UUID(boot)) == boot
    network, resource = receipt["network"], receipt["resource"]
    assert re.fullmatch(r"[0-9a-f]{64}", network["id"])
    assert re.fullmatch(r"[0-9a-f]{32}", resource["id"]) and resource["network_id"] == network["id"]
    for name in ("source", "owner"):
        vm = receipt[name]
        assert re.fullmatch(r"[0-9a-f]{32}", vm["binding"]["id"])
        assert vm["binding"]["network_id"] == network["id"] and vm["pid"] > 0 and vm["process_start_time"] > 0
        assert str(uuid.UUID(vm["boot_id"])) == vm["boot_id"]
    assert receipt["source"]["binding"]["id"] != receipt["owner"]["binding"]["id"]
    grants = receipt["grants"]
    assert len(grants) == len({item["id"] for item in grants}) == 3
    assert {item["method"] for item in grants} == {"inspect", "associate", "release"}
    for grant in grants:
        assert re.fullmatch(r"[0-9a-f]{32}", grant["id"])
        assert grant["vm_id"] == receipt["owner"]["binding"]["id"] and grant["boot_id"] == boot
        assert grant["network_id"] == network["id"] and grant["resource_id"] == resource["id"]
        assert grant["state"] == "active" and grant["launch_generation"] > 0
    policy = receipt["policy"]
    assert re.fullmatch(r"[0-9a-f]{64}", policy["id"])
    assert policy["source_vm_id"] == receipt["source"]["binding"]["id"]
    assert policy["destination_floating_ip_id"] == resource["id"] and policy["protocol"] == "tcp"
    assert policy["destination_port_start"] == policy["destination_port_end"] == 8080
    assert receipt["associated"]["resource_id"] == resource["id"] and receipt["associated"]["ownership"] == "self"
    assert receipt["associated"]["lease_remaining_ms"] > 0
    assert re.fullmatch(r"[0-9a-f]{32}", receipt["nonce"]) and re.fullmatch(r"[0-9a-f]{32}", receipt["marker"])
    assert re.fullmatch(r"[0-9a-f]{12}", receipt["suffix"])
    assert receipt["alias"]["label"].startswith(receipt["alias"]["snapshot"]["interface"] + ":f")
    return receipt


def without_owned_policy(traffic, policy, *, allow_missing=False):
    """Exclude exactly the retained policy receipt, never a family or matching tuple."""
    result = copy.deepcopy(traffic)
    rows = result["vm_to_floating_ip"]
    matches = [row for row in rows if row["id"] == policy["id"]]
    assert matches == [policy] or allow_missing and matches == [], "owned reboot policy changed or duplicated"
    result["vm_to_floating_ip"] = [row for row in rows if row["id"] != policy["id"]]
    return result


def _journey(runner, receipt):
    """Reopen only exact PRE-owned identities; current unrelated inventory stays protected."""
    journey = FloatingIPJourney(runner)
    journey.suffix = receipt["suffix"]
    network, resource = receipt["network"], receipt["resource"]
    owned = [receipt[name]["binding"] for name in ("source", "owner")]
    journey.before["vm"] = [row for row in journey.before["vm"] if row["id"] not in {vm["id"] for vm in owned}]
    journey.before["network"] = [row for row in journey.before["network"] if row["id"] != network["id"]]
    journey.before["traffic"] = without_owned_policy(journey.before["traffic"], receipt["policy"], allow_missing=True)
    journey.networks = {network["name"]: network}
    journey.vms = {vm["name"]: (network["id"], vm) for vm in owned}
    journey.resources = [{"network": network, "name": resource["name"], "ipv4": resource["ipv4"], "item": resource}]
    journey.policy_intents = [("--from-vm", receipt["source"]["binding"]["id"], resource["id"])]
    return journey


def cleanup(runner, receipt):
    _journey(runner, receipt).cleanup()


def prepare(runner, boot):
    journey = FloatingIPJourney(runner)
    try:
        network = journey.network("host-reboot")
        source, owner = journey.vm("source", network), journey.vm("owner", network)
        resource = journey.resource(network, "service")
        log = journey.listener(owner)
        policy = journey.policy(source, resource, "vm")
        journey.flow(source, owner, log, resource["ipv4"], allowed=False)
        grants = [journey.grant(owner, resource, method) for method in ("inspect", "associate", "release")]
        initial = journey.control(owner, resource)
        associated = journey.control(owner, resource, "associate", initial["generation"])
        assert associated["ownership"] == "self"
        journey.flow(source, owner, log, resource["ipv4"], allowed=True)
        alias = journey.stale_alias(owner, resource)
        nonce = journey.flow(source, owner, log, resource["ipv4"], allowed=True)
        marker = uuid.uuid4().hex
        _run_mvm(runner, "exec", owner["binding"]["id"], "--user", "root", "--",
                 shlex.join(["python3", "-c", "import pathlib,sys; pathlib.Path('/root/floating-reboot-marker').write_text(sys.argv[1])", marker]))
        _run_mvm(runner, "exec", owner["binding"]["id"], "--user", "root", "--", "sync")
        return validate_pre(dict(schema_version=1, boot_id=boot, network=network, source=source, owner=owner,
                                 resource=resource, grants=grants, policy=policy, alias=alias, associated=associated,
                                 nonce=nonce, marker=marker, suffix=journey.suffix), boot)
    except BaseException:
        journey.cleanup()
        raise


def _stale_alias(runner, owner, resource, label, *, retained=None, remove=False):
    """Add/check/remove only the test-labeled VIP in the current guest incarnation."""
    script = r"""
import json, subprocess, sys
vip, primary, label, raw, remove = sys.argv[1:]
retained = json.loads(raw)
def run(*args):return subprocess.check_output(['ip', *args], text=True)
def snapshot():
    owners = [item for item in json.loads(run('-j', '-4', 'addr', 'show'))
              if any(address['local'] == primary for address in item.get('addr_info', []))]
    assert len(owners) == 1
    interface = owners[0]
    aliases = [address for address in interface['addr_info'] if address['local'] == vip]
    routes = json.loads(run('-j', '-4', 'route', 'show', 'table', 'all'))
    routes = [route for route in routes if not (route.get('type') == 'local' and route.get('dst') in (vip, vip + '/32')
                                               and route.get('dev') == interface['ifname'] and route.get('prefsrc') == vip)]
    return {'interface': interface['ifname'], 'ifindex': interface['ifindex'],
            'primary_addresses': [address for address in interface['addr_info'] if address['local'] != vip],
            'routes': sorted(routes, key=lambda item: json.dumps(item, sort_keys=True))}, aliases
before, aliases = snapshot()
assert label.startswith(before['interface'] + ':f') and len(label) <= 15
if retained is None:
    assert aliases == [], 'refusing existing guest alias'
    run('addr', 'add', vip + '/32', 'dev', before['interface'], 'label', label, 'noprefixroute')
    retained = before
else:
    assert before == retained
current, aliases = snapshot()
assert current == retained and len(aliases) == 1
assert aliases[0]['prefixlen'] == 32 and aliases[0].get('label') == label
if remove == 'yes':
    run('addr', 'del', vip + '/32', 'dev', retained['interface'])
    after, aliases = snapshot()
    assert after == retained and aliases == []
print(json.dumps(retained, sort_keys=True))
"""
    result = _run_mvm(runner, "exec", owner["binding"]["id"], "--user", "root", "--",
                      shlex.join(["python3", "-c", script, resource["ipv4"], owner["binding"]["ipv4"], label,
                                  json.dumps(retained), "yes" if remove else "no"]))
    return json.loads(result.stdout)


def resume(runner, receipt, boot):
    validate_pre(receipt, receipt["boot_id"])
    assert boot != receipt["boot_id"], "floating-IP proof requires an actual changed host boot"
    journey = _journey(runner, receipt)
    try:
        for name in ("source", "owner"):
            vm_id = receipt[name]["binding"]["id"]
            current = _tcp_data(runner, "vm", "inspect", vm_id)["vm"]
            assert current["status"] == "stopped" and current["created_at"] == receipt[name]["binding"]["created_at"]
            assert _run_mvm(runner, "exec", vm_id, "--", "true", check=False).returncode != 0
        _run_mvm(runner, "network", "sync", receipt["network"]["id"])
        peers = {}
        for name in ("owner", "source"):
            _run_mvm(runner, "vm", "start", receipt[name]["binding"]["id"], timeout=120)
            matches = [item for item in _tcp_data(runner, "vm", "ls", kind=list)
                       if item["id"] == receipt[name]["binding"]["id"]]
            assert len(matches) == 1 and matches[0]["status"] == "running"
            current = matches[0]
            from tests.system.network.test_policies import _tcp_vm_binding
            assert _tcp_vm_binding(current) == receipt[name]["binding"]
            guest_boot = _run_mvm(runner, "exec", current["id"], "--user", "root", "--timeout", "30", "--",
                                  "cat /proc/sys/kernel/random/boot_id").stdout.strip()
            assert str(uuid.UUID(guest_boot)) == guest_boot != receipt[name]["boot_id"]
            assert current["pid"] > 0 and current["process_start_time"] > 0
            # Numeric PID/start pairs may repeat across host boots. The changed
            # host boot and fresh guest boot provide the incarnation boundary.
            peers[name] = dict(binding=receipt[name]["binding"], pid=current["pid"],
                               process_start_time=current["process_start_time"], boot_id=guest_boot)
        source, owner, resource = peers["source"], peers["owner"], receipt["resource"]
        assert _run_mvm(runner, "exec", owner["binding"]["id"], "--user", "root", "--",
                        "cat /root/floating-reboot-marker").stdout == receipt["marker"]
        for grant in receipt["grants"]:
            current = _tcp_data(runner, "floating-ip", "delegation", "inspect", grant["id"], "--network", resource["network_id"])
            assert current["state"] == "revoked" and current["revision"] > grant["revision"]
            assert {key: value for key, value in current.items() if key not in ("state", "revision")} == {
                key: value for key, value in grant.items() if key not in ("state", "revision")}
        journey.alias(owner, resource, present=False)
        journey.suffix = uuid.uuid4().hex[:12]  # PRE listener directory survives on the guest disk.
        log = journey.listener(owner)
        stale = _stale_alias(runner, owner, resource, receipt["alias"]["label"])
        denied = journey.flow(source, owner, log, resource["ipv4"], allowed=False)
        _stale_alias(runner, owner, resource, receipt["alias"]["label"], retained=stale, remove=True)
        journey.control(owner, resource, failure="unavailable")
        inspect = journey.grant(owner, resource, "inspect")
        assert inspect["boot_id"] == boot and inspect["id"] not in {item["id"] for item in receipt["grants"]}
        observed = journey.control(owner, resource)
        assert observed["ownership"] == "unassociated" and observed["lease_remaining_ms"] == 0
        journey.control(owner, resource, "associate", observed["generation"], failure="denied")
        associate = journey.grant(owner, resource, "associate")
        release = journey.grant(owner, resource, "release")
        active = journey.control(owner, resource, "associate", observed["generation"])
        assert active["ownership"] == "self"
        allowed = journey.flow(source, owner, log, resource["ipv4"], allowed=True)
        stale = journey.stale_alias(owner, resource)
        released = journey.control(owner, resource, "release", active["generation"])
        assert released["ownership"] == "unassociated"
        released_denied = journey.flow(source, owner, log, resource["ipv4"], allowed=False)
        journey.stale_alias(owner, resource, previous=stale)
        return dict(schema_version=1, boot_id=boot, pre_boot_id=receipt["boot_id"], resource_id=resource["id"],
                    old_grant_ids=[item["id"] for item in receipt["grants"]],
                    current_grant_ids=[item["id"] for item in (inspect, associate, release)],
                    old_owner=receipt["owner"], owner=owner, source=source,
                    initial_denied_nonce=denied, allowed_nonce=allowed, released_denied_nonce=released_denied,
                    owned_cleanup=True)
    finally:
        journey.cleanup()
