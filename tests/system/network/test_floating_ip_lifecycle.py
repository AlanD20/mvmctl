"""Nested-VM primary allocation must respect root-owned floating reservations."""

from __future__ import annotations

import pytest

from tests.system.conftest import _run_mvm
from tests.system.network.test_floating_ip import _address, _create, _data, _items

pytestmark = [pytest.mark.system, pytest.mark.requires_network, pytest.mark.requires_kvm,
              pytest.mark.domain_network, pytest.mark.tier2]


def _vm(runner, name, network, address=None):
    args = ["vm", "create", name, "--network", network["name"], "--image", "alpine:3.23",
            "--mem", "256M", "--vcpu", "1", "-s", "1G", "--cloud-init-mode", "off"]
    if address is not None:
        args.extend(["--ip", address])
    _run_mvm(runner, *args, timeout=120)
    values = _data(runner, "vm", "ls", kind=list)
    return next(item for item in values if item["name"] == name)


def test_vm_automatic_primary_skips_floating_ip(runner_vm, floating_network, floating_test_vm):
    net = floating_network
    floating = _create(runner_vm, net, "api", _address(net, 2))
    vm = _vm(runner_vm, floating_test_vm, net)
    assert vm["ipv4"] == _address(net, 3)
    assert vm["status"] == "running"
    assert _items(runner_vm, net) == [floating]


def test_floating_allocation_refuses_stopped_primary(runner_vm, floating_network, floating_test_vm):
    net = floating_network
    _vm(runner_vm, floating_test_vm, net, _address(net, 10))
    _run_mvm(runner_vm, "vm", "stop", floating_test_vm, timeout=60)
    stopped = next(item for item in _data(runner_vm, "vm", "ls", kind=list) if item["name"] == floating_test_vm)
    assert stopped["status"] == "stopped"
    assert stopped["ipv4"] == _address(net, 10)
    refused = _run_mvm(runner_vm, "network", "floating-ip", "create", "conflict", "--network", net["name"],
                       "--ip", _address(net, 10), "--json", check=False)
    assert refused.returncode != 0
    assert _items(runner_vm, net) == []
    available = _create(runner_vm, net, "available", _address(net, 11))
    assert _items(runner_vm, net) == [available]
