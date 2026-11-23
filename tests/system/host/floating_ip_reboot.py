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
           "policy", "alias", "associated", "nonce", "marker", "suffix", "operator"}


def validate_pre(receipt, boot):
    assert set(receipt) == _FIELDS and type(receipt["schema_version"]) is int and receipt["schema_version"] == 2
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
    operator = receipt["operator"]
    assert set(operator) == {"resource", "associated", "policy", "nonce"}
    persistent = operator["resource"]
    assert re.fullmatch(r"[0-9a-f]{32}", persistent["id"]) and persistent["network_id"] == network["id"]
    assert persistent["id"] != resource["id"] and persistent["ipv4"] != resource["ipv4"]
    _assert_persistent(operator["associated"], persistent, receipt["owner"], boot)
    policy = operator["policy"]
    assert re.fullmatch(r"[0-9a-f]{64}", policy["id"]) and policy["id"] != receipt["policy"]["id"]
    assert policy["source_vm_id"] == receipt["source"]["binding"]["id"]
    assert policy["destination_floating_ip_id"] == persistent["id"] and policy["protocol"] == "tcp"
    assert policy["destination_port_start"] == policy["destination_port_end"] == 8080
    assert re.fullmatch(r"[0-9a-f]{32}", operator["nonce"]) and operator["nonce"] != receipt["nonce"]
    return receipt


def _host(journey, resource, method="inspect", vm=None, generation=None):
    args = ["network", "floating-ip", "--network", resource["network_id"], method, resource["id"]]
    if vm is not None:
        args.append(vm["binding"]["id"])
    if generation is not None:
        args += ["--generation", str(generation)]
    return _tcp_data(journey.runner, *args)


def _assert_persistent(item, resource, owner, boot):
    for key in ("id", "network_id", "ipv4", "name"):
        assert item[key] == resource[key], item
    assert item["generation"] > resource["generation"] and item["retired"] is False, item
    assert item["phase"] == "owned" and item["owner_vm_id"] == owner["binding"]["id"], item
    assert item["owner_origin"] == "operator" and item["lifetime"] == "persistent", item
    assert item["owner_boot_id"] == boot and item["owner_launch_generation"] > 0, item
    assert item["authority_state"] == "active" and item["lease_remaining_ms"] == 0, item
    assert item["alias_synchronization"] == "synchronized", item


def _assert_ownerless(item, resource, *, inspection):
    for key in ("id", "network_id", "ipv4", "name"):
        assert item[key] == resource[key], item
    assert item["phase"] == "unassociated", item
    assert item["authority_state"] == ("unobserved" if inspection else "absent"), item
    assert item["owner_vm_id"] == item["owner_origin"] == item["lifetime"] == item["owner_boot_id"] == "", item
    assert item["owner_launch_generation"] == item["lease_remaining_ms"] == 0 and item["retired"] is False, item


def without_owned_policy(traffic, policy, *, allow_missing=False):
    """Exclude exactly the retained policy receipt, never a family or matching tuple."""
    result = copy.deepcopy(traffic)
    rows = result["vm_to_floating_ip"]
    matches = [row for row in rows if row["id"] == policy["id"]]
    assert matches == [policy] or allow_missing and matches == [], "owned reboot policy changed or duplicated"
    result["vm_to_floating_ip"] = [row for row in rows if row["id"] != policy["id"]]
    return result


def without_owned_policies(traffic, receipt, *, allow_missing=False):
    """Exclude exactly both PRE receipts while preserving every raw unrelated row."""
    policies = (receipt["policy"], receipt["operator"]["policy"])
    assert policies[0]["id"] != policies[1]["id"], "owned reboot policy identities overlap"
    result = traffic
    for policy in policies:
        result = without_owned_policy(result, policy, allow_missing=allow_missing)
    return result


def _journey(runner, receipt):
    """Reopen only exact PRE-owned identities; current unrelated inventory stays protected."""
    journey = FloatingIPJourney(runner)
    journey.suffix = receipt["suffix"]
    network, resource = receipt["network"], receipt["resource"]
    owned = [receipt[name]["binding"] for name in ("source", "owner")]
    journey.before["vm"] = [row for row in journey.before["vm"] if row["id"] not in {vm["id"] for vm in owned}]
    journey.before["network"] = [row for row in journey.before["network"] if row["id"] != network["id"]]
    journey.before["traffic"] = without_owned_policies(journey.before["traffic"], receipt, allow_missing=True)
    journey.networks = {network["name"]: network}
    journey.vms = {vm["name"]: (network["id"], vm) for vm in owned}
    resources = (resource, receipt["operator"]["resource"])
    journey.resources = [{"network": network, "name": item["name"], "ipv4": item["ipv4"], "item": item}
                         for item in resources]
    journey.policy_intents = [("--from-vm", receipt["source"]["binding"]["id"], item["id"])
                              for item in resources]
    return journey


def cleanup(runner, receipt):
    _journey(runner, receipt).cleanup()


def prepare(runner, boot):
    journey = FloatingIPJourney(runner)
    try:
        network = journey.network("host-reboot")
        source, owner = journey.vm("source", network), journey.vm("owner", network)
        resource = journey.resource(network, "service")
        persistent = journey.resource(network, "operator", offset=101)
        log = journey.listener(owner)
        operator_policy = journey.policy(source, persistent, "vm")
        journey.flow(source, owner, log, persistent["ipv4"], allowed=False)
        operator_claim = _host(journey, persistent, "associate", owner, persistent["generation"])
        _assert_persistent(operator_claim, persistent, owner, boot)
        journey.alias(owner, persistent, present=True)
        assert _tcp_data(runner, "network", "floating-ip", "delegation", "ls", persistent["id"],
                         "--network", network["id"], kind=list) == []
        operator_nonce = journey.flow(source, owner, log, persistent["ipv4"], allowed=True)
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
        return validate_pre(dict(schema_version=2, boot_id=boot, network=network, source=source, owner=owner,
                                 resource=resource, grants=grants, policy=policy, alias=alias, associated=associated,
                                 nonce=nonce, marker=marker, suffix=journey.suffix,
                                 operator=dict(resource=persistent, associated=operator_claim,
                                               policy=operator_policy, nonce=operator_nonce)), boot)
    except BaseException:
        journey.cleanup()
        raise


def _stale_alias(runner, owner, resource, label, *, retained=None, remove=False, replace=False):
    """Add/check/remove only the test-labeled VIP in the current guest incarnation."""
    script = r"""
import json, subprocess, sys
vip, primary, label, raw, remove, replace = sys.argv[1:]
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
    if replace == 'yes':
        assert len(aliases) == 1 and aliases[0]['prefixlen'] == 32 and aliases[0].get('label') != label
        run('addr', 'del', vip + '/32', 'dev', before['interface'])
    else:
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
                                  json.dumps(retained), "yes" if remove else "no", "yes" if replace else "no"]))
    return json.loads(result.stdout)


def _release_stale_claim(journey, source, owner, resource, log, generation, label, stale):
    # Host release fences before agent-local cleanup. A changed label makes that
    # cleanup refuse and the agent returns internal; this is not a host-state proof.
    journey.control(owner, resource, "release", generation, failure="internal")
    denied = journey.flow(source, owner, log, resource["ipv4"], allowed=False)
    _stale_alias(journey.runner, owner, resource, label, retained=stale)
    _stale_alias(journey.runner, owner, resource, label, retained=stale, remove=True)
    observed = journey.control(owner, resource)
    assert observed["ownership"] == "unassociated" and observed["lease_remaining_ms"] == 0
    return denied


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
            current = _tcp_data(runner, "network", "floating-ip", "delegation", "inspect", grant["id"], "--network", resource["network_id"])
            assert current["state"] == "revoked" and current["revision"] > grant["revision"]
            assert {key: value for key, value in current.items() if key not in ("state", "revision")} == {
                key: value for key, value in grant.items() if key not in ("state", "revision")}
        persistent = receipt["operator"]["resource"]
        fenced = _host(journey, persistent)
        _assert_ownerless(fenced, persistent, inspection=True)
        assert fenced["generation"] > receipt["operator"]["associated"]["generation"]
        assert _tcp_data(runner, "network", "floating-ip", "delegation", "ls", persistent["id"],
                         "--network", persistent["network_id"], kind=list) == []
        journey.alias(owner, persistent, present=False)
        journey.alias(owner, resource, present=False)
        for policy, item in ((receipt["policy"], resource), (receipt["operator"]["policy"], persistent)):
            journey.assert_policy(policy, ("--from-vm", source["binding"]["id"], item["id"]))
        journey.suffix = uuid.uuid4().hex[:12]  # PRE listener directory survives on the guest disk.
        log = journey.listener(owner)
        stale = _stale_alias(runner, owner, resource, receipt["alias"]["label"])
        denied = journey.flow(source, owner, log, resource["ipv4"], allowed=False)
        _stale_alias(runner, owner, resource, receipt["alias"]["label"], retained=stale, remove=True)
        # Re-add a test-owned persistent VIP before the probe. A removed guest
        # alias cannot satisfy this post-boot kernel-fencing assertion.
        operator_stale = _stale_alias(runner, owner, persistent, receipt["alias"]["label"])
        operator_denied = journey.flow(source, owner, log, persistent["ipv4"], allowed=False)
        _stale_alias(runner, owner, persistent, receipt["alias"]["label"], retained=operator_stale)
        _stale_alias(runner, owner, persistent, receipt["alias"]["label"], retained=operator_stale, remove=True)
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
        label = receipt["alias"]["label"]
        stale = _stale_alias(runner, owner, resource, label, replace=True)
        released_denied = _release_stale_claim(journey, source, owner, resource, log, active["generation"], label, stale)
        operator_inspect = journey.grant(owner, persistent, "inspect")
        assert operator_inspect["boot_id"] == boot
        operator_observed = journey.control(owner, persistent)
        assert operator_observed["ownership"] == "unassociated" and operator_observed["lease_remaining_ms"] == 0
        journey.control(owner, persistent, "associate", operator_observed["generation"], failure="denied")
        claimed = _host(journey, persistent, "associate", owner, operator_observed["generation"])
        _assert_persistent(claimed, persistent, owner, boot)
        assert claimed["owner_boot_id"] != receipt["operator"]["associated"]["owner_boot_id"]
        journey.alias(owner, persistent, present=True)
        operator_allowed = journey.flow(source, owner, log, persistent["ipv4"], allowed=True)
        journey.assert_policy(receipt["operator"]["policy"],
                              ("--from-vm", source["binding"]["id"], persistent["id"]))
        operator_released = _host(journey, persistent, "release", generation=claimed["generation"])
        _assert_ownerless(operator_released, persistent, inspection=False)
        assert operator_released["alias_synchronization"] == "synchronized"
        journey.alias(owner, persistent, present=False)
        operator_stale = _stale_alias(runner, owner, persistent, receipt["alias"]["label"])
        operator_released_denied = journey.flow(source, owner, log, persistent["ipv4"], allowed=False)
        _stale_alias(runner, owner, persistent, receipt["alias"]["label"], retained=operator_stale, remove=True)
        return dict(schema_version=2, boot_id=boot, pre_boot_id=receipt["boot_id"], resource_id=resource["id"],
                    old_grant_ids=[item["id"] for item in receipt["grants"]],
                    current_grant_ids=[item["id"] for item in (inspect, associate, release)],
                    old_owner=receipt["owner"], owner=owner, source=source,
                    initial_denied_nonce=denied, allowed_nonce=allowed, released_denied_nonce=released_denied,
                    operator=dict(resource_id=persistent["id"], initial_denied_nonce=operator_denied,
                                  associated=claimed, current_inspect_grant_id=operator_inspect["id"],
                                  allowed_nonce=operator_allowed, released=operator_released,
                                  released_denied_nonce=operator_released_denied), owned_cleanup=True)
    finally:
        journey.cleanup()
