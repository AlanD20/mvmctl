"""Version-1 floating-IP workflow ownership through the installed candidate."""

from __future__ import annotations

import json
import uuid

import pytest

from tests.system.conftest import _json_success_data, _run_mvm, _unique_subnet
from tests.system.exec.test_vm_to_vm_exec import _require_source_kernel

pytestmark = [pytest.mark.system, pytest.mark.domain_env, pytest.mark.tier3]


def _data(runner, *args, kind=dict):
    return _json_success_data(_run_mvm(runner, *args, "--json").stdout, kind)


@pytest.fixture
def floating_env_specs(tmp_path, runner_vm):
    """Destroy only this test's workflows, adopting workflows before their owner."""
    paths = []

    def write(name, spec):
        path = str(tmp_path / name)
        (tmp_path / name).write_text(json.dumps(spec))
        paths.append(path)
        return path

    yield write
    errors = []
    for path in reversed(paths):
        try:
            if path in _run_mvm(runner_vm, "env", "ls").stdout:
                _run_mvm(runner_vm, "env", "destroy", path, timeout=180)
        except Exception as error:
            errors.append(f"{path}: {error}")
    assert not errors, "\n".join(errors)


def _reservation_spec():
    suffix = uuid.uuid4().hex[:10]
    name = f"env-fip-{suffix}"
    return {
        "version": "1",
        "network": {"cluster": {"name": name, "subnet": _unique_subnet(name), "nat_enabled": False}},
        "floating_ip": {"api": {"name": "api", "network": "@network:cluster",
                                "depends_on": ["@network:cluster"]}},
    }, name, suffix


def _lifecycle_spec(runner):
    spec, network_name, suffix = _reservation_spec()
    vm_name = f"env-fip-controller-{suffix}"
    spec["vm"] = {"controller": {
        "name": vm_name, "image": "alpine:3.23", "kernel": _require_source_kernel(runner),
        "network": "@network:cluster", "user": "runner", "vcpu": 1, "mem": "256M",
        "cloud_init_mode": "off", "skip_deblob": True, "depends_on": ["@floating_ip:api"],
    }}
    spec["floating_ip_grant"] = {"associate": {
        "network": "@network:cluster", "resource": "@floating_ip:api", "vm": "@vm:controller",
        "method": "associate", "lease": "10s", "depends_on": ["@vm:controller", "@floating_ip:api"],
    }}
    spec["traffic_policy"] = {}
    for source in ("vm", "network"):
        spec["traffic_policy"][source] = {
            f"source_{source}": "@vm:controller" if source == "vm" else "@network:cluster",
            "destination_floating_ip": "@floating_ip:api", "destination_network": "@network:cluster",
            "protocol": "tcp", "port": 18080,
            "depends_on": ["@floating_ip:api", "@vm:controller"],
        }
    return spec, network_name, vm_name


def _snapshot(runner, network_name, vm_name):
    network = next(n for n in _data(runner, "network", "ls", kind=list) if n["name"] == network_name)
    vm = next(v for v in _data(runner, "vm", "ls", kind=list) if v["name"] == vm_name)
    reservation = _data(runner, "floating-ip", "inspect", "api", "--network", network["id"])
    grants = _data(runner, "floating-ip", "delegation", "ls", reservation["id"],
                   "--network", network["id"], kind=list)
    assert len(grants) == 1, grants
    grant = grants[0]
    assert (grant["network_id"], grant["resource_id"], grant["vm_id"], grant["method"], grant["state"]) == (
        network["id"], reservation["id"], vm["id"], "associate", "active"), grant
    policies = _data(runner, "policy", "traffic", "ls")
    selected = {}
    for source in ("vm", "network"):
        matches = [p for p in policies[f"{source}_to_floating_ip"]
                   if p["destination_floating_ip_id"] == reservation["id"]]
        assert len(matches) == 1, matches
        policy = dict(matches[0])
        # Ensure refreshes projection updated_at; every other receipt field stays strict.
        policy.pop("updated_at")
        assert (policy[f"source_{source}_id"], policy["destination_network_id"], policy["protocol"],
                policy["destination_port_start"], policy["destination_port_end"]) == (
                    vm["id"] if source == "vm" else network["id"], network["id"], "tcp", 18080, 18080), policy
        selected[source] = policy
    return {"network": network["id"], "vm": vm["id"], "reservation": reservation,
            "grant": grant, "policies": selected}


def test_floating_ip_owned_apply_reapply_destroy(runner_vm, floating_env_specs):
    """Retry retains reservation, grant lifetime and both policy IDs; destroy revokes all."""
    spec, network_name, vm_name = _lifecycle_spec(runner_vm)
    # Keep the network inspectable until exact reservation/grant cleanup is proved.
    network_path = floating_env_specs("network.yaml", {"version": "1", "network": spec["network"]})
    _run_mvm(runner_vm, "env", "apply", network_path)
    network = next(n for n in _data(runner_vm, "network", "ls", kind=list) if n["name"] == network_name)
    spec["network"] = {"cluster": {"existing": network["id"]}}
    path = floating_env_specs("owned.yaml", spec)
    _run_mvm(runner_vm, "env", "apply", path, timeout=240)
    original = _snapshot(runner_vm, network_name, vm_name)
    _run_mvm(runner_vm, "env", "apply", path, timeout=180)
    assert _snapshot(runner_vm, network_name, vm_name) == original
    _run_mvm(runner_vm, "env", "destroy", path, timeout=180)
    assert all(v["id"] != original["vm"] for v in _data(runner_vm, "vm", "ls", kind=list))
    policies = _data(runner_vm, "policy", "traffic", "ls")
    for source, policy in original["policies"].items():
        assert all(p["id"] != policy["id"] for p in policies[f"{source}_to_floating_ip"])
    assert _data(runner_vm, "floating-ip", "ls", "--network", original["network"], kind=list) == []
    revoked = _data(runner_vm, "floating-ip", "delegation", "inspect", original["grant"]["id"],
                    "--network", original["network"])
    assert revoked["id"] == original["grant"]["id"] and revoked["state"] == "revoked"
    assert path not in _run_mvm(runner_vm, "env", "ls").stdout
    _run_mvm(runner_vm, "env", "destroy", network_path)
    assert all(n["id"] != original["network"] for n in _data(runner_vm, "network", "ls", kind=list))


def test_floating_ip_adopted_resources_survive_destroy(runner_vm, floating_env_specs):
    """An adopting workflow does not acquire the owning workflow's cleanup authority."""
    spec, network_name, vm_name = _lifecycle_spec(runner_vm)
    owner = floating_env_specs("owner.yaml", spec)
    _run_mvm(runner_vm, "env", "apply", owner, timeout=240)
    original = _snapshot(runner_vm, network_name, vm_name)
    adopted = {"version": "1", "network": {"cluster": {"existing": original["network"]}},
               "vm": {"controller": {"existing": original["vm"]}},
               "floating_ip": {"api": {"network": "@network:cluster", "existing": original["reservation"]["id"],
                                       "depends_on": ["@network:cluster"]}},
               "traffic_policy": {}}
    for source in ("vm", "network"):
        adopted["traffic_policy"][source] = {**spec["traffic_policy"][source],
                                            "existing": original["policies"][source]["id"]}
    path = floating_env_specs("adopted.yaml", adopted)
    _run_mvm(runner_vm, "env", "apply", path, timeout=120)
    _run_mvm(runner_vm, "env", "apply", path, timeout=120)
    _run_mvm(runner_vm, "env", "destroy", path, timeout=120)
    assert _snapshot(runner_vm, network_name, vm_name) == original
    assert path not in _run_mvm(runner_vm, "env", "ls").stdout
    assert owner in _run_mvm(runner_vm, "env", "ls").stdout


def test_floating_ip_saved_id_refuses_same_name_replacement(runner_vm, floating_env_specs):
    """Public removal/recreation cannot transfer the original workflow's identity."""
    spec, network_name, _ = _reservation_spec()
    network_path = floating_env_specs("network.yaml", {"version": "1", "network": spec["network"]})
    _run_mvm(runner_vm, "env", "apply", network_path)
    network = next(n for n in _data(runner_vm, "network", "ls", kind=list) if n["name"] == network_name)
    reservation_spec = {"version": "1", "floating_ip": {"api": {"network": network["id"], "name": "api"}}}
    path = floating_env_specs("reservation.yaml", reservation_spec)
    replacement = None
    try:
        _run_mvm(runner_vm, "env", "apply", path)
        first = _data(runner_vm, "floating-ip", "inspect", "api", "--network", network["id"])
        _run_mvm(runner_vm, "floating-ip", "rm", first["id"], "--network", network["id"], "--force")
        replacement = _data(runner_vm, "floating-ip", "create", "api", "--network", network["id"],
                            "--ip", first["ipv4"])
        assert replacement["id"] != first["id"]
        refused = _run_mvm(runner_vm, "env", "apply", path, check=False)
        assert refused.returncode != 0, refused.stdout
        assert _data(runner_vm, "floating-ip", "inspect", replacement["id"], "--network", network["id"]) == replacement
        _run_mvm(runner_vm, "env", "destroy", path)
        assert _data(runner_vm, "floating-ip", "ls", "--network", network["id"], kind=list) == [replacement]
    finally:
        if replacement is not None:
            _run_mvm(runner_vm, "floating-ip", "rm", replacement["id"], "--network", network["id"], "--force")
