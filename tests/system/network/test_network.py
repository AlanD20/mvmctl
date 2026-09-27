"""Network management system tests.

Covers: create, ls, inspect, rm, default, sync, --no-nat, --ipv4-gateway,
rejected --nat-gateways, error paths, destructive removal, VM dependency, and
same-boot bridge disappearance rejection.

Structure (top-to-bottom):
  - Helper functions
  - Non-destructive / read-only classes
  - State-modifying but self-cleaning classes
  - Destructive classes (force-rm, VM-dependent operations, bridge deletion)

All commands execute inside the named disposable runner VM.
"""

from __future__ import annotations

import json
import uuid
from typing import Any

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_network,
    pytest.mark.slow,
    pytest.mark.domain_network,
]


# ============================================================================
# Helpers — commands run inside the test VM via _guest_run
# ============================================================================


def _network_info(runner_vm: str, name: str) -> dict[str, Any]:
    """Read the bridge identity published by the installed CLI."""
    data = _json_success_data(
        _run_mvm(runner_vm, "network", "inspect", name, "--json").stdout,
        dict,
    )
    network = data["network"]
    assert network["name"] == name
    return network


def _bridge_name(runner_vm: str, name: str) -> str:
    bridge = _network_info(runner_vm, name)["bridge"]
    assert isinstance(bridge, str) and bridge
    return bridge


def _assert_bridge_exists(runner_vm: str, bridge: str) -> None:
    """Assert a bridge interface exists via ``ip link show`` inside the test VM."""
    result = _guest_run(
        runner_vm, f"ip link show {bridge} 2>&1", check=False, timeout=10
    )
    assert result.returncode == 0, (
        f"Bridge '{bridge}' should exist:\n{result.stderr}"
    )


def _inet_rules(runner_vm: str, chain: str) -> list[str]:
    """Read actual rules from the fixed managed inet generation."""
    result = _guest_run(
        runner_vm,
        f"sudo -n nft list chain inet mvmctl_inet {chain}",
        check=False,
        timeout=10,
    )
    assert result.returncode == 0, (
        f"managed inet {chain} chain is unavailable: {result.stderr}"
    )
    return [
        line.strip()
        for line in result.stdout.splitlines()
        if line.strip()
        and not line.strip().startswith(("table ", "chain ", "type ", "#"))
        and line.strip() not in ("{", "}")
    ]


def _assert_firewall_rules_contain_bridge(runner_vm: str, bridge: str) -> None:
    rules = _inet_rules(runner_vm, "forward")
    assert any(bridge in rule and "accept" in rule for rule in rules), rules
    assert any(bridge in rule and "drop" in rule for rule in rules), rules


def _assert_masquerade_for_subnet(runner_vm: str, bridge: str, subnet: str) -> None:
    rules = _inet_rules(runner_vm, "postrouting")
    assert any(
        bridge in rule and subnet in rule and "masquerade" in rule
        for rule in rules
    ), rules


def _assert_network_firewall_rules(runner_vm: str, name: str, subnet: str) -> None:
    bridge = _bridge_name(runner_vm, name)
    _assert_bridge_exists(runner_vm, bridge)
    _assert_firewall_rules_contain_bridge(runner_vm, bridge)
    _assert_masquerade_for_subnet(runner_vm, bridge, subnet)


# ============================================================================
# Tests
# ============================================================================


class TestNetworkLifecycle:
    """Test network CRUD operations.

    Order within class:
      1. Read-only / error-path tests (no resource creation)
      2. State-modifying but self-cleaning tests (create + cleanup)
      3. Destructive tests (rm, delete)
    """

    # ── Read-only / error-path tests (no resources needed) ──────────────
    def test_network_list_empty(self, runner_vm):
        # Rationale: Only needs JSON parsing (free). No resources needed --
        # verifies empty list is valid.
        """network ls --json returns valid empty list when no networks exist."""
        result = _run_mvm(runner_vm, "network", "ls", "--json")
        assert result.returncode == 0, (
            f"network ls --json failed: {result.stderr}"
        )
        networks = _json_success_data(result.stdout, list)
        assert isinstance(networks, list), (
            f"Expected list, got {type(networks).__name__}: {networks}"
        )
        if len(networks) == 0:
            return  # Empty state — ideal
        # If networks exist, validate entry structure
        for net in networks:
            assert isinstance(net.get("name"), str) and net["name"], (
                f"Expected non-empty name: {net}"
            )
            assert isinstance(net.get("id"), str) and net["id"], (
                f"Expected non-empty id: {net}"
            )

    def test_network_listing_and_verification(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture (shared, 5-10s). Verifies
        # network appears in JSON listing.
        """List networks and verify created network appears."""
        result = _run_mvm(runner_vm, "network", "ls", "--json")
        networks: list[dict[str, Any]] = _json_success_data(result.stdout, list)
        matched = [n for n in networks if n.get("name") == module_network]
        assert len(matched) == 1, (
            f"Network '{module_network}' not found in JSON listing"
        )
        network = matched[0]
        assert isinstance(network.get("id"), str) and network["id"], (
            f"Expected non-empty id: {network}"
        )
        assert isinstance(network.get("subnet"), str) and network["subnet"], (
            f"Expected non-empty subnet: {network}"
        )

    def test_managed_nftables_rules_reference_bridge(self, runner_vm, module_network):
        """The installed forward generation contains this network's bridge."""
        bridge = _bridge_name(runner_vm, module_network)
        _assert_firewall_rules_contain_bridge(runner_vm, bridge)

    def test_nat_gateway_configuration(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Verifies bridge interface
        # exists via ip addr inside test VM.
        """Verify bridge interface exists for created network."""
        bridge = _bridge_name(runner_vm, module_network)
        _assert_bridge_exists(runner_vm, bridge)

    def test_network_inspect(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Verifies JSON fields (name,
        # subnet, bridge) — was originally checking table output, now uses --json.
        """Inspect a network and verify JSON fields."""
        result = _run_mvm(
            runner_vm, "network", "inspect", module_network, "--json"
        )
        data: dict[str, Any] = _json_success_data(result.stdout, dict)
        network_info = data.get("network", {})
        assert network_info.get("name") == module_network, (
            f"Expected name '{module_network}', got '{network_info.get('name')}'"
        )
        assert isinstance(
            network_info.get("subnet"), str
        ) and "/" in network_info.get("subnet", ""), (
            f"Expected CIDR format subnet, got: {network_info.get('subnet')}"
        )
        assert (
            isinstance(network_info.get("bridge"), str)
            and network_info["bridge"]
        ), f"Expected non-empty bridge name, got: {network_info.get('bridge')}"

    def test_network_inspect_json(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Verifies --json output
        # has expected fields.
        """Inspect a network with --json and verify parsed fields."""
        result = _run_mvm(
            runner_vm, "network", "inspect", module_network, "--json"
        )
        assert result.returncode == 0
        data: dict[str, Any] = _json_success_data(result.stdout, dict)
        assert "network" in data
        network_info = data["network"]
        assert "name" in network_info
        assert "subnet" in network_info
        assert "bridge" in network_info

    def test_network_ls_structure(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Verifies JSON field
        # structure in listing.
        """Verify network ls --json returns a list with well-formed entries.

        Every entry must have non-empty name, id, and subnet fields.
        """
        result = _run_mvm(runner_vm, "network", "ls", "--json")
        data: list[dict[str, Any]] = _json_success_data(result.stdout, list)
        assert isinstance(data, list)
        if data:
            for entry in data:
                assert isinstance(entry.get("name"), str) and entry["name"], (
                    f"Expected non-empty name: {entry}"
                )
                assert isinstance(entry.get("id"), str) and entry["id"], (
                    f"Expected non-empty id: {entry}"
                )
                assert (
                    isinstance(entry.get("subnet"), str) and entry["subnet"]
                ), f"Expected non-empty subnet: {entry}"

    def test_network_list_json(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Verifies network appears
        # in JSON listing.
        """List networks in JSON format."""
        result = _run_mvm(runner_vm, "network", "ls", "--json")
        assert result.returncode == 0
        data: list[dict[str, Any]] = _json_success_data(result.stdout, list)
        assert isinstance(data, list)
        assert any(n["name"] == module_network for n in data)

    def test_network_default_without_name(self, runner_vm):
        # Rationale: No resources needed -- error path for missing name
        # argument.
        """Calling network default without a name should show guidance."""
        result = _run_mvm(runner_vm, "network", "default", check=False)
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "missing required argument" in combined, (
            f"Expected 'missing required argument', got: "
            f"{result.stdout} / {result.stderr}"
        )

    def test_network_remove_nonexistent(self, runner_vm):
        # Rationale: No resources needed -- error path for nonexistent network.
        """Removing a non-existent network returns error."""
        result = _run_mvm(
            runner_vm,
            "network",
            "rm",
            "nonexistent-network-name-xyz",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "no networks found matching identifiers" in combined, (
            f"Expected 'No networks found matching identifiers' in output, "
            f"got stdout={result.stdout!r} stderr={result.stderr!r}"
        )

    def test_set_default_nonexistent_network_fails(self, runner_vm):
        # Rationale: No resources needed -- error path for nonexistent network.
        """Setting a nonexistent network as default should be rejected."""
        result = _run_mvm(
            runner_vm,
            "network",
            "default",
            "totally-nonexistent-network",
            check=False,
        )
        assert result.returncode != 0
        assert (
            "no networks found matching identifiers" in result.stderr.lower()
        ), (
            f"Expected 'No networks found matching identifiers' in stderr, "
            f"got stdout={result.stdout!r} stderr={result.stderr!r}"
        )

        ls_result = _run_mvm(runner_vm, "network", "ls", "--json", check=False)
        if ls_result.returncode == 0 and ls_result.stdout.strip():
            networks = _json_success_data(ls_result.stdout, list)
            assert not any(
                n.get("name") == "totally-nonexistent-network" for n in networks
            )

    def test_network_create_with_invalid_cidr_format(self, runner_vm):
        # Rationale: No resources needed -- error path for invalid CIDR.
        """Create network with an invalid CIDR string should be rejected."""
        net_name = f"sys-edge-{uuid.uuid4().hex[:6]}"
        result = _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            "not-a-cidr",
            check=False,
        )
        assert result.returncode != 0
        assert "network subnet must be a canonical masked IPv4 prefix" in result.stderr

        ls_result = _run_mvm(runner_vm, "network", "ls", "--json", check=False)
        if ls_result.returncode == 0 and ls_result.stdout.strip():
            networks = _json_success_data(ls_result.stdout, list)
            assert not any(n.get("name") == net_name for n in networks)

    # ── State-modifying tests (create + cleanup) ───────────────────────

    def test_network_create_with_generated_subnet(
        # Rationale: Needs a real network (5-10s). Tests basic creation with
        # generated subnet.
        self,
        runner_vm,
        unique_network_name,
    ):
        """Create network with a dynamically generated unique subnet (verify via --json)."""
        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
            )
            assert result.returncode == 0
            # Verify via JSON listing
            ls_result = _run_mvm(runner_vm, "network", "ls", "--json")
            networks = _json_success_data(ls_result.stdout, list)
            assert any(n["name"] == unique_network_name for n in networks)
        finally:
            _run_mvm(
                runner_vm, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_with_custom_cidr(
        # Rationale: Needs a real network (5-10s). Tests custom CIDR creation.
        self,
        runner_vm,
        unique_network_name,
    ):
        """Create network with custom CIDR (verify via --json)."""
        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
            )
            assert result.returncode == 0
            # Verify via JSON listing
            ls_result = _run_mvm(runner_vm, "network", "ls", "--json")
            networks = _json_success_data(ls_result.stdout, list)
            assert any(n["name"] == unique_network_name for n in networks)
        finally:
            _run_mvm(
                runner_vm, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_no_nat(self, runner_vm, unique_network_name):
        # Rationale: Needs a real network (5-10s). Tests --no-nat flag.
        """Create a network with --no-nat flag.

        Checks the JSON projection, real bridge, and absence of its NAT rule.
        """
        try:
            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                _unique_subnet(unique_network_name),
                "--no-nat",
            )
            assert result.returncode == 0

            # Verify network appears in JSON listing.
            ls_result = _run_mvm(runner_vm, "network", "ls", "--json")
            networks = _json_success_data(ls_result.stdout, list)
            assert any(n.get("name") == unique_network_name for n in networks)

            # Verify the bridge exists and --no-nat was honored.
            bridge = _bridge_name(runner_vm, unique_network_name)
            _assert_bridge_exists(runner_vm, bridge)
            inspect_result = _run_mvm(
                runner_vm, "network", "inspect", unique_network_name, "--json"
            )
            inspect_data = _json_success_data(inspect_result.stdout, dict)
            assert inspect_data.get("nat", {}).get("nat_enabled") is False, (
                f"Expected nat_enabled=False for --no-nat network, "
                f"got: {inspect_data.get('nat', {}).get('nat_enabled')}"
            )
            subnet = _unique_subnet(unique_network_name)
            assert not any(
                bridge in rule and subnet in rule and "masquerade" in rule
                for rule in _inet_rules(runner_vm, "postrouting")
            )
        finally:
            _run_mvm(
                runner_vm, "network", "rm", unique_network_name, check=False
            )

    def test_network_set_default(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Tests setting/restoring
        # default network.
        """Set a network as the default."""
        # Save original default network before changing
        ls_before = _run_mvm(runner_vm, "network", "ls", "--json", check=False)
        original_default = None
        if ls_before.returncode == 0 and ls_before.stdout.strip():
            nets_before = _json_success_data(ls_before.stdout, list)
            orig = next((n for n in nets_before if n.get("is_default")), None)
            if orig:
                original_default = orig["name"]

        try:
            result = _run_mvm(runner_vm, "network", "default", module_network)
            assert result.returncode == 0
            # Verify via JSON listing that the network is now default
            ls_result = _run_mvm(runner_vm, "network", "ls", "--json")
            networks = _json_success_data(ls_result.stdout, list)
            default = next(
                (n for n in networks if n.get("name") == module_network),
                None,
            )
            assert default is not None
            assert default.get("is_default", False)
        finally:
            # Restore original default so subsequent tests aren't broken
            if original_default:
                _run_mvm(
                    runner_vm,
                    "network",
                    "default",
                    original_default,
                    check=False,
                )

    def test_network_create_with_default_flag(
        self,
        runner_vm,
        unique_network_name,
    ):
        """Create a network with --default flag and verify is_default=true in ls JSON.

        Rationale: The --default flag on network create sets the new network as
        the default for VM creation. A regression where --default is silently
        ignored would leave the previous default (or no default) in place.
        """
        subnet = _unique_subnet(unique_network_name)
        # Save original default to restore later
        ls_before = _run_mvm(runner_vm, "network", "ls", "--json", check=False)
        original_default = None
        if ls_before.returncode == 0 and ls_before.stdout.strip():
            nets_before = _json_success_data(ls_before.stdout, list)
            orig = next((n for n in nets_before if n.get("is_default")), None)
            if orig:
                original_default = orig["name"]

        try:
            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--default",
            )
            assert result.returncode == 0

            # L2: Verify is_default=true in ls JSON
            ls_result = _run_mvm(runner_vm, "network", "ls", "--json")
            networks = _json_success_data(ls_result.stdout, list)
            new_net = next(
                (n for n in networks if n.get("name") == unique_network_name),
                None,
            )
            assert new_net is not None, (
                f"Network '{unique_network_name}' not found in listing"
            )
            assert new_net.get("is_default") is True, (
                f"Network '{unique_network_name}' should have is_default=True "
                f"after create --default"
            )
        finally:
            _run_mvm(
                runner_vm, "network", "rm", unique_network_name, check=False
            )
            # Restore original default
            if original_default:
                _run_mvm(
                    runner_vm,
                    "network",
                    "default",
                    original_default,
                    check=False,
                )

    # ── Error path create + cleanup ────────────────────────────────────

    def test_overlapping_subnet_across_networks_rejected(self, runner_vm):
        # Rationale: Needs a real network. Tests duplicate subnet rejection.
        """Two networks with the same subnet should be rejected at create time."""
        net_a = f"sys-edge-{uuid.uuid4().hex[:6]}"
        net_b = f"sys-edge-{uuid.uuid4().hex[:6]}"
        shared_subnet = "10.251.0.0/24"

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_a,
                "--subnet",
                shared_subnet,
            )

            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                net_b,
                "--subnet",
                shared_subnet,
                check=False,
            )
            assert result.returncode != 0
            assert "overlap" in result.stderr.lower()

            ls_result = _run_mvm(
                runner_vm, "network", "ls", "--json", check=False
            )
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                networks = _json_success_data(ls_result.stdout, list)
                same_subnet = [
                    n for n in networks if n.get("subnet") == shared_subnet
                ]
                assert len(same_subnet) == 1
        finally:
            _run_mvm(runner_vm, "network", "rm", net_a, check=False)
            _run_mvm(runner_vm, "network", "rm", net_b, check=False)

    def test_network_create_with_slash_32_subnet(self, runner_vm):
        # Rationale: Needs one network attempt. Tests /32 rejection.
        """Create network with /32 subnet should be rejected (too small)."""
        net_name = f"sys-edge-{uuid.uuid4().hex[:6]}"

        try:
            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                "10.252.0.0/32",
                check=False,
            )
            assert result.returncode != 0
            assert "no longer than /30" in result.stderr

            ls_result = _run_mvm(
                runner_vm, "network", "ls", "--json", check=False
            )
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                networks = _json_success_data(ls_result.stdout, list)
                assert not any(n.get("name") == net_name for n in networks)
        finally:
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    def test_network_create_with_same_name(self, runner_vm):
        # Rationale: Needs a real network. Tests duplicate name rejection.
        """Two networks with the same name should be rejected."""
        net_name = f"sys-edge-{uuid.uuid4().hex[:6]}"

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )

            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(f"{net_name}-other"),
                "--json",
                check=False,
            )
            assert result.returncode != 0
            response = json.loads(result.stdout)
            assert response["schema_version"] == 1
            assert response["status"] == "error"
            assert response["error"]["code"] == "network.already_exists"

            ls_result = _run_mvm(
                runner_vm, "network", "ls", "--json", check=False
            )
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                networks = _json_success_data(ls_result.stdout, list)
                same_name = [n for n in networks if n.get("name") == net_name]
                assert len(same_name) == 1
        finally:
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    @pytest.mark.requires_network
    def test_overlapping_subnet_rejected(self, runner_vm: str) -> None:
        # Rationale: Needs real networks. Tests overlapping subnet rejection.
        """Creating a second network with the same subnet CIDR should fail."""
        net_a = f"sys-ovlap-a-{uuid.uuid4().hex[:6]}"
        net_b = f"sys-ovlap-b-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_a)
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_a,
                "--subnet",
                subnet,
            )
            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                net_b,
                "--subnet",
                subnet,
                check=False,
            )
            assert result.returncode != 0
            assert "overlap" in result.stderr.lower()
        finally:
            _run_mvm(runner_vm, "network", "rm", net_a, check=False)

    # ── Destructive tests (remove / delete) ────────────────────────────

    def test_network_deletion_and_cleanup(
        # Rationale: Needs a real network. Tests create, verify, delete,
        # verify gone.
        self,
        runner_vm,
        unique_network_name,
    ):
        """Create and delete network, verify cleanup."""
        _run_mvm(
            runner_vm,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            _unique_subnet(unique_network_name),
        )

        try:
            result = _run_mvm(runner_vm, "network", "ls", "--json")
            networks_before = _json_success_data(result.stdout, list)
            assert any(
                n.get("name") == unique_network_name for n in networks_before
            )

            result = _run_mvm(runner_vm, "network", "rm", unique_network_name)
            assert result.returncode == 0
        finally:
            _run_mvm(
                runner_vm, "network", "rm", unique_network_name, check=False
            )

        result = _run_mvm(runner_vm, "network", "ls", "--json")
        networks: list[dict[str, Any]] = _json_success_data(result.stdout, list)
        assert not any(n.get("name") == unique_network_name for n in networks)

    def test_network_remove_multiple(self, runner_vm, unique_network_name):
        # Rationale: Needs two real networks. Tests multi-rm.
        # Cleanup in finally.
        """Remove multiple networks at once."""
        name_a = f"{unique_network_name}-a"
        name_b = f"{unique_network_name}-b"

        _run_mvm(
            runner_vm,
            "network",
            "create",
            name_a,
            "--subnet",
            _unique_subnet(name_a),
        )
        _run_mvm(
            runner_vm,
            "network",
            "create",
            name_b,
            "--subnet",
            _unique_subnet(name_b),
        )

        try:
            result = _run_mvm(runner_vm, "network", "rm", name_a, name_b)
            assert result.returncode == 0
            ls_result = _run_mvm(runner_vm, "network", "ls", "--json")
            networks = _json_success_data(ls_result.stdout, list)
            assert not any(n.get("name") in (name_a, name_b) for n in networks)
        finally:
            _run_mvm(runner_vm, "network", "rm", name_a, check=False)
            _run_mvm(runner_vm, "network", "rm", name_b, check=False)


class TestNetworkInspectTree:
    """Test network inspect with default format output."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.domain_network,
    ]

    def test_network_inspect_tree(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Verifies default inspect
        # output format.
        """Inspect a network and verify tree characters in default output."""
        result = _run_mvm(runner_vm, "network", "inspect", module_network)
        assert result.returncode == 0
        assert "├─ " in result.stdout or "└─ " in result.stdout


class TestNetworkAdvancedCreate:
    """Test advanced network creation options."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.domain_network,
    ]

    def test_network_create_with_ipv4_gateway(
        # Rationale: Needs a real network (5-10s). Tests --ipv4-gateway flag.
        self,
        runner_vm,
        unique_network_name,
    ):
        """Create a network with explicit --ipv4-gateway."""
        subnet = _unique_subnet(unique_network_name)
        custom_gateway = subnet.replace(".0/24", ".100")
        try:
            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--ipv4-gateway",
                custom_gateway,
            )
            assert result.returncode == 0

            inspect = _run_mvm(
                runner_vm,
                "network",
                "inspect",
                unique_network_name,
                "--json",
            )
            data: dict[str, Any] = _json_success_data(inspect.stdout, dict)
            network_info = data.get("network", {})
            assert network_info.get("ipv4_gateway") == custom_gateway
        finally:
            _run_mvm(
                runner_vm, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_with_nat_gateways_rejected(
        # Rationale: Removed --nat-gateways flag must fail before network creation.
        self,
        runner_vm,
        unique_network_name,
    ):
        """Removed --nat-gateways is rejected without creating a network or bridge."""
        subnet = _unique_subnet(unique_network_name)
        bridges_before = _guest_run(runner_vm, "ip -o link show type bridge").stdout
        result = _run_mvm(
            runner_vm,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            subnet,
            "--nat-gateways",
            "eth0",
            check=False,
        )
        assert result.returncode != 0
        assert "unknown flag: --nat-gateways" in result.stderr.lower()

        ls_result = _run_mvm(runner_vm, "network", "ls", "--json")
        networks = _json_success_data(ls_result.stdout, list)
        assert not any(
            net.get("name") == unique_network_name for net in networks
        )

        bridges_after = _guest_run(runner_vm, "ip -o link show type bridge").stdout
        assert bridges_after == bridges_before, (
            f"Rejected network create changed bridge inventory: {bridges_before!r} -> {bridges_after!r}"
        )

    def test_network_create_invalid_gateway_fails(
        # Rationale: Needs one network attempt. Tests invalid gateway rejection.
        self,
        runner_vm,
        unique_network_name,
    ):
        """Creating a network with an invalid gateway should fail."""
        result = _run_mvm(
            runner_vm,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            "10.99.99.0/24",
            "--ipv4-gateway",
            "not-an-ip",
            check=False,
        )
        assert result.returncode != 0
        _run_mvm(runner_vm, "network", "rm", unique_network_name, check=False)


class TestNetworkSync:
    """Test network sync against the fixed managed nftables generation.

    Non-destructive: all tests either create+cleanup their own networks or
    use the module_network fixture. This class is placed BEFORE destructive
    classes so idempotent tests run if file execution is interrupted.
    """

    def test_network_sync_all(self, runner_vm):
        # Rationale: Creates own network and checks bridge/rules after global sync.
        """Sync all networks — verifies bridge and firewall rules persist."""
        net_name = f"sys-sync-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
            )

            # Read bridge name from inspect output (authoritative source)
            bridge = _bridge_name(runner_vm, net_name)

            result = _run_mvm(runner_vm, "network", "sync", check=False)
            assert result.returncode == 0, f"sync failed: {result.stderr}"

            # The exact bridge remains present.
            _assert_bridge_exists(runner_vm, bridge)

            # The bridge retains its IP address.
            ip_result = _guest_run(
                runner_vm, f"ip addr show {bridge}", check=False, timeout=10
            )
            assert ip_result.returncode == 0
            assert "inet " in ip_result.stdout, (
                f"Bridge {bridge} has no IP after sync:\n{ip_result.stdout}"
            )

            # The managed firewall rules still reference the bridge.
            _assert_firewall_rules_contain_bridge(runner_vm, bridge)
        finally:
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                net_name,
                "--force",
                check=False,
            )

    def test_network_sync_specific(self, runner_vm):
        # Rationale: Creates own network inline. Verifies sync by name.
        # Self-contained to avoid depending on earlier network test state.
        """Sync a specific network by name."""
        net_name = f"sys-sync-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
            )

            # Read bridge name from inspect output
            bridge = _bridge_name(runner_vm, net_name)

            result = _run_mvm(
                runner_vm, "network", "sync", net_name, check=False
            )
            assert result.returncode == 0, (
                f"sync {net_name} failed: {result.stderr}"
            )

            # The exact bridge and rules remain present.
            _assert_bridge_exists(runner_vm, bridge)
            _assert_firewall_rules_contain_bridge(runner_vm, bridge)
        finally:
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                net_name,
                "--force",
                check=False,
            )

    def test_network_sync_preserves_all_networks(self, runner_vm):
        # Rationale: Regression for cross-network wipe bug. Syncing all
        # networks (or a specific one) must preserve the union of all managed
        # nftables rules, not only those for the last network.
        """Sync global and per-network must preserve every network's rules.

        Regression guard: global or specific sync must not remove another
        managed network's nftables rules.
        """
        net_a = f"sys-sync-a-{uuid.uuid4().hex[:6]}"
        net_b = f"sys-sync-b-{uuid.uuid4().hex[:6]}"
        subnet_a = _unique_subnet(net_a)
        subnet_b = _unique_subnet(net_b)
        def _assert_both_networks() -> None:
            _assert_network_firewall_rules(runner_vm, net_a, subnet_a)
            _assert_network_firewall_rules(runner_vm, net_b, subnet_b)

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_a,
                "--subnet",
                subnet_a,
            )
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_b,
                "--subnet",
                subnet_b,
            )

            # Baseline: both networks have active firewall rules
            _assert_both_networks()

            # Global sync must preserve rules for ALL networks
            result = _run_mvm(runner_vm, "network", "sync", check=False)
            assert result.returncode == 0, (
                f"global network sync failed: {result.stderr}"
            )
            _assert_both_networks()

            # Specific sync must preserve rules for OTHER networks too
            result = _run_mvm(
                runner_vm, "network", "sync", net_a, check=False
            )
            assert result.returncode == 0, (
                f"specific network sync {net_a} failed: {result.stderr}"
            )
            _assert_both_networks()
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_a, "--force", check=False
            )
            _run_mvm(
                runner_vm, "network", "rm", net_b, "--force", check=False
            )

    def test_network_sync_json(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Verifies JSON result
        # structure from sync.
        """Sync with JSON output — verify result structure has per-network stats."""
        result = _run_mvm(runner_vm, "network", "sync", "--json", check=False)
        assert result.returncode == 0
        data = _json_success_data(result.stdout, dict)
        assert isinstance(data, dict), f"Expected dict, got {type(data)}"
        assert len(data) > 0, "Expected at least one network in sync results"
        for net_id, stats in data.items():
            assert isinstance(stats, dict), (
                f"Expected dict for network {net_id}"
            )
            assert "added" in stats, f"Missing 'added' in {net_id}"
            assert "verified" in stats, f"Missing 'verified' in {net_id}"
            assert "orphaned" in stats, f"Missing 'orphaned' in {net_id}"

    def test_network_sync_idempotent(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Verifies no rule
        # duplication on second sync.
        """Sync twice without changing the actual managed forward rules."""

        # First sync — establish baseline
        _run_mvm(runner_vm, "network", "sync")
        rules_after_first = _inet_rules(runner_vm, "forward")
        assert rules_after_first

        # Second sync — should not add duplicates
        _run_mvm(runner_vm, "network", "sync")
        rules_after_second = _inet_rules(runner_vm, "forward")

        assert rules_after_second == rules_after_first, (
            f"Managed forward rules changed after second sync: "
            f"{rules_after_first} -> {rules_after_second}"
        )

    def test_network_sync_conntrack_rule(self, runner_vm, module_network):
        # Rationale: Uses module_network fixture. Verifies conntrack
        # established/related accept rule.
        """Sync ensures conntrack established/related accept rule exists."""
        _run_mvm(runner_vm, "network", "sync")
        bridge = _bridge_name(runner_vm, module_network)
        rules = _inet_rules(runner_vm, "forward")
        assert any(
            bridge in rule and "ct state established,related" in rule and "accept" in rule
            for rule in rules
        ), rules

    def test_network_sync_on_nonexistent_network(self, runner_vm):
        # Rationale: No resources needed -- error path for nonexistent
        # network sync.
        """Syncing a network that doesn't exist should fail gracefully."""
        result = _run_mvm(
            runner_vm,
            "network",
            "sync",
            "totally-nonexistent-network",
            check=False,
        )
        assert result.returncode != 0
        assert (
            "no networks found matching identifiers" in result.stderr.lower()
        ), (
            f"Expected 'No networks found matching identifiers' in stderr, "
            f"got stdout={result.stdout!r} stderr={result.stderr!r}"
        )

        ls_result = _run_mvm(runner_vm, "network", "ls", "--json", check=False)
        if ls_result.returncode == 0 and ls_result.stdout.strip():
            networks = _json_success_data(ls_result.stdout, list)
            assert not any(
                n.get("name") == "totally-nonexistent-network" for n in networks
            )


class TestNetworkRemoveForce:
    """Test network removal with --force flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.domain_network,
    ]

    def test_network_rm_with_force(self, runner_vm, unique_network_name):
        # Rationale: Needs a real network. Tests --force removal.
        # Cleanup in finally; verifies the bridge is actually gone.
        """Create a network and remove it with --force, verify cleanup."""
        subnet = _unique_subnet(unique_network_name)
        bridge: str | None = None
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
            )
            bridge = _bridge_name(runner_vm, unique_network_name)

            result = _run_mvm(
                runner_vm, "network", "rm", unique_network_name, "--force"
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                runner_vm, "network", "rm", unique_network_name, check=False
            )

        ls_result = _run_mvm(runner_vm, "network", "ls", "--json")
        networks = _json_success_data(ls_result.stdout, list)
        assert not any(n.get("name") == unique_network_name for n in networks)

        # Verify bridge interface is actually gone from the system.
        # Rationale: A deletion that removes the DB entry but fails to
        # delete the bridge would leave a stale interface consuming
        # system resources. The `ip link show` check proves the bridge
        # was actually torn down by network rm --force.
        result = _guest_run(
            runner_vm, f"ip link show {bridge} 2>&1", check=False, timeout=10
        )
        assert result.returncode != 0, (
            f"Bridge '{bridge}' should no longer exist after "
            f"network rm --force:\n{result.stdout}"
        )


class TestNetworkVMDependency:
    """Test network operations that depend on VM lifecycle."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.domain_network,
    ]

    def test_network_inspect_after_all_vms_removed(
        # Rationale: Needs a real VM (30-120s). Verifies network vm_count=0
        # after VM removal.
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """After VM removal, network inspect should show zero VM count."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
            )

            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")

            result = _run_mvm(
                runner_vm, "network", "inspect", net_name, "--json"
            )
            data: dict[str, Any] = _json_success_data(result.stdout, dict)
            assert len(data.get("leases", [])) == 0
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    def test_network_rm_with_active_vm_fails_without_force(
        # Rationale: Needs a real VM (30-120s). Tests network rm rejection
        # with active VMs.
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Network with active VM must not be deletable."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
            )

            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            result = _run_mvm(
                runner_vm, "network", "rm", net_name, check=False
            )
            assert result.returncode != 0
            assert "referenced by" in result.stderr.lower()

            result_net = _run_mvm(
                runner_vm, "network", "ls", "--json", check=False
            )
            if result_net.returncode == 0:
                nets: list[dict[str, Any]] = _json_success_data(result_net.stdout, list)
                assert any(n["name"] == net_name for n in nets)

            result_vm = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if result_vm.returncode == 0:
                vms: list[dict[str, Any]] = _json_success_data(result_vm.stdout, list)
                assert any(v["name"] == vm_name for v in vms)
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)


class TestNetworkSyncMissingBridge:
    """Same-boot bridge disappearance must fail closed, not be re-created.

    This class directly deletes a managed bridge and is placed last in the file.
    A real prior-boot recovery scenario needs a runner reboot, not link deletion.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.domain_network,
    ]

    def test_sync_refuses_missing_managed_bridge(
        # Rationale: Real bridge deletion proves same-boot tamper refusal.
        self,
        runner_vm,
        unique_network_name,
    ) -> None:
        """Sync rejects the missing recorded bridge without adopting a replacement."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        bridge: str | None = None

        # 1. Create the network
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
            )
            bridge = _bridge_name(runner_vm, net_name)

            # Verify bridge exists before removal
            _assert_bridge_exists(runner_vm, bridge)

            # 2. Delete the bridge without changing the host boot identity.
            result = _guest_run(
                runner_vm,
                f"sudo ip link delete {bridge}",
                check=False,
                timeout=10,
            )
            assert result.returncode == 0, (
                f"Failed to delete bridge {bridge}: {result.stderr}"
            )

            # Verify bridge is really gone
            result = _guest_run(
                runner_vm,
                f"ip link show {bridge} 2>&1",
                check=False,
                timeout=10,
            )
            assert result.returncode != 0, (
                f"Bridge {bridge} should no longer exist"
            )

            # 3. The same-boot authority record must reject silent re-creation.
            sync_result = _run_mvm(
                runner_vm, "network", "sync", check=False
            )
            assert sync_result.returncode != 0, (
                "sync unexpectedly accepted a missing managed bridge"
            )
            assert "managed bridge disappeared" in sync_result.stderr
            result = _guest_run(
                runner_vm,
                f"ip link show {bridge} 2>&1",
                check=False,
                timeout=10,
            )
            assert result.returncode != 0, "sync recreated the missing bridge"

        finally:
            # Explicit exact-network removal retires the stale firewall intent.
            _run_mvm(runner_vm, "network", "rm", net_name)


class TestNetworkEdgeCases:
    """Tests for network command edge cases."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_network,
    ]

    def test_network_create_without_subnet(self, runner_vm):
        """``network create`` without --subnet should fail with clear error."""
        # Rationale: No resources needed — testing CLI validation for
        # missing --subnet flag.
        result = _run_mvm(
            runner_vm,
            "network",
            "create",
            "test-network",
            check=False,
        )
        assert result.returncode != 0
        assert "Missing required option '--subnet'" in (
            result.stdout + result.stderr
        )

    def test_network_set_default_nonexistent(self, runner_vm):
        """``network set-default`` with nonexistent name should fail."""
        # Rationale: No resources needed — testing CLI error for
        # nonexistent network name.
        result = _run_mvm(
            runner_vm,
            "network",
            "default",
            "nonexistent-network-name-xyz",
            check=False,
        )
        assert result.returncode != 0

    def test_network_create_without_prompt_flag(
        self, runner_vm, unique_network_name
    ):
        """Network creation needs no prompt-suppression flag."""
        net_name = unique_network_name
        subnet = f"10.{abs(hash(net_name)) % 254 + 1}.0.0/24"
        try:
            result = _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                check=False,
            )
            assert result.returncode == 0, (
                f"network create failed: {result.stderr}"
            )

            ls_result = _run_mvm(runner_vm, "network", "ls", "--json")
            networks = _json_success_data(ls_result.stdout, list)
            assert any(n.get("name") == net_name for n in networks), (
                f"Network '{net_name}' not found in ls --json after create"
            )
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
