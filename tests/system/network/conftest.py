"""Function-owned network resources for floating-IP system tests."""

from __future__ import annotations

import uuid

import pytest

from tests.system.conftest import _json_success_data, _run_mvm, _unique_subnet


@pytest.fixture
def floating_network(runner_vm):
    name = f"sys-fip-{uuid.uuid4().hex[:10]}"
    subnet = _unique_subnet(name)
    created = _json_success_data(
        _run_mvm(runner_vm, "network", "create", name, "--subnet", subnet, "--no-nat", "--json").stdout,
        dict,
    )
    try:
        yield created
    finally:
        networks = _json_success_data(_run_mvm(runner_vm, "network", "ls", "--json").stdout, list)
        if any(item["id"] == created["id"] for item in networks):
            items = _json_success_data(
                _run_mvm(runner_vm, "network", "floating-ip", "ls", "--network", name, "--json").stdout, list
            )
            for item in items:
                _run_mvm(runner_vm, "network", "floating-ip", "rm", item["id"], "--network", name, "--force")
            _run_mvm(runner_vm, "network", "rm", name, "--force")
        remaining = _json_success_data(_run_mvm(runner_vm, "network", "ls", "--json").stdout, list)
        assert all(item["id"] != created["id"] for item in remaining), "owned test network survived cleanup"


@pytest.fixture
def floating_test_vm(runner_vm, floating_network):
    """Use cached assets and one small nested VM; always remove it before the parent network."""
    name = f"sys-fip-vm-{uuid.uuid4().hex[:10]}"
    try:
        yield name
    finally:
        items = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
        if any(item["name"] == name for item in items):
            _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
        remaining = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
        assert all(item["name"] != name for item in remaining), "owned test VM survived cleanup"
