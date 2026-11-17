"""Real installed-CLI floating-IP reservations, refusal paths and cleanup ordering."""

from __future__ import annotations

import ipaddress
import json
import re
import uuid

import pytest

from tests.system.conftest import _json_success_data, _run_mvm, _unique_subnet

pytestmark = [pytest.mark.system, pytest.mark.requires_network, pytest.mark.domain_network]


def _data(runner, *args, kind=dict):
    return _json_success_data(_run_mvm(runner, *args, "--json").stdout, kind)


def _address(network, offset):
    return str(ipaddress.ip_network(network["subnet"]).network_address + offset)


def _create(runner, network, name, address=None):
    args = ["floating-ip", "create", name, "--network", network["name"]]
    if address is not None:
        args.extend(["--ip", address])
    return _data(runner, *args)


def _items(runner, network):
    return _data(runner, "floating-ip", "ls", "--network", network["name"], kind=list)


def test_floating_ip_create_inspect_and_explicit_removal(runner_vm, floating_network):
    net = floating_network
    item = _create(runner_vm, net, "api", _address(net, 100))
    assert re.fullmatch(r"[0-9a-f]{32}", item["id"])
    assert item == {
        "id": item["id"], "network_id": net["id"], "name": "api",
        "ipv4": _address(net, 100), "generation": 1, "retired": False,
    }
    assert _items(runner_vm, net) == [item]
    for selector in ("api", item["id"][:8], item["ipv4"]):
        assert _data(runner_vm, "floating-ip", "inspect", selector, "--network", net["name"]) == item

    unconfirmed = _run_mvm(runner_vm, "floating-ip", "rm", item["id"], "--network", net["name"], "--json", check=False)
    assert unconfirmed.returncode != 0
    error = json.loads(unconfirmed.stdout)["error"]
    assert error["code"] == "cli.interaction.required"
    assert error["class"] == 5  # Public errs.ClassNeedsInteraction integer enum.
    assert _items(runner_vm, net) == [item]
    removed = _data(runner_vm, "floating-ip", "rm", item["id"], "--network", net["name"], "--force")
    assert removed == {**item, "retired": True, "generation": 2}
    assert _items(runner_vm, net) == []


def test_floating_ip_automatic_allocation_and_reuse(runner_vm, floating_network):
    net = floating_network
    first = _create(runner_vm, net, "reserved", _address(net, 2))
    second = _create(runner_vm, net, "automatic")
    assert second["ipv4"] == _address(net, 3)
    _run_mvm(runner_vm, "floating-ip", "rm", first["id"], "--network", net["name"], "--force")
    replacement = _create(runner_vm, net, "reserved")
    assert replacement["ipv4"] == _address(net, 2)
    assert replacement["id"] != first["id"]
    assert replacement["generation"] == 1


@pytest.mark.parametrize("offset", [0, 1, 255, 256])
def test_floating_ip_rejects_unusable_address_without_creating_resource(runner_vm, floating_network, offset):
    net = floating_network
    result = _run_mvm(runner_vm, "floating-ip", "create", "invalid", "--network", net["name"],
                      "--ip", _address(net, offset), "--json", check=False)
    assert result.returncode != 0
    assert json.loads(result.stdout)["status"] == "error"
    assert _items(runner_vm, net) == []


def test_floating_ip_duplicate_name_or_address_preserves_existing_resource(runner_vm, floating_network):
    net = floating_network
    item = _create(runner_vm, net, "api", _address(net, 100))
    for name, address in (("api", _address(net, 101)), ("other", _address(net, 100))):
        result = _run_mvm(runner_vm, "floating-ip", "create", name, "--network", net["name"],
                          "--ip", address, "--json", check=False)
        assert result.returncode != 0
        assert _items(runner_vm, net) == [item]


def test_floating_ip_name_is_scoped_to_network(runner_vm, floating_network):
    first_net = floating_network
    first = _create(runner_vm, first_net, "api")
    name = f"sys-fip-other-{uuid.uuid4().hex[:10]}"
    other = _data(runner_vm, "network", "create", name, "--subnet", _unique_subnet(name), "--no-nat")
    try:
        second = _create(runner_vm, other, "api")
        assert second["id"] != first["id"]
        assert _items(runner_vm, first_net) == [first]
        assert _items(runner_vm, other) == [second]
    finally:
        _run_mvm(runner_vm, "network", "rm", name, "--force")


def test_network_removal_refuses_reservation_and_force_retires_it(runner_vm, floating_network):
    net = floating_network
    item = _create(runner_vm, net, "api", _address(net, 100))
    refused = _run_mvm(runner_vm, "network", "rm", net["name"], check=False)
    assert refused.returncode != 0
    assert _items(runner_vm, net) == [item]
    _run_mvm(runner_vm, "network", "rm", net["name"], "--force")
    assert all(value["id"] != net["id"] for value in _data(runner_vm, "network", "ls", kind=list))
