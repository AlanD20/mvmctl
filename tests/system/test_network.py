"""Network management system tests."""

from __future__ import annotations

import json
import subprocess

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_network,
    pytest.mark.slow,
    pytest.mark.serial,
    pytest.mark.domain_network,
]


def _compute_bridge_name(name: str) -> str:
    """Replicate NetworkUtils.compute_bridge_name for test assertions.

    Bridge names are limited to 15 chars (IFNAMSIZ). If the full
    mvm-{name} exceeds 15, the name portion is truncated and a hash
    suffix is appended to preserve uniqueness.
    """
    import hashlib

    raw = f"mvm-{name}"
    if len(raw) <= 15:
        return raw
    hash_len = 8
    prefix = "mvm-"
    max_name = 15 - len(prefix) - hash_len - 1  # -1 for '-' separator
    name_truncated = name[:max_name]
    short_hash = hashlib.sha256(name.encode()).hexdigest()[:hash_len]
    return f"{prefix}{name_truncated}-{short_hash}"


class TestNetworkLifecycle:
    """Test network CRUD operations."""

    def test_network_create_with_generated_subnet(
        self, mvm_binary, unique_network_name
    ):
        """Create network with a dynamically generated unique subnet."""
        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            assert result.returncode == 0
            assert unique_network_name in result.stdout
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_with_custom_cidr(
        self, mvm_binary, unique_network_name
    ):
        """Create network with custom CIDR."""
        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_listing_and_verification(self, mvm_binary, module_network):
        """List networks and verify created network appears."""
        result = _run_mvm(mvm_binary, "network", "ls")
        assert result.returncode == 0
        assert module_network in result.stdout

    def test_ip_rule_verification_iptables(self, module_network):
        """Verify iptables rules were created for network."""
        bridge = _compute_bridge_name(module_network)
        result = subprocess.run(
            ["sudo", "iptables", "-t", "nat", "-L"],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0
        # Bridge name for this network should appear in iptables rules
        assert bridge in result.stdout

    def test_nat_gateway_configuration(self, module_network):
        """Verify bridge interface exists for created network."""
        bridge = _compute_bridge_name(module_network)
        result = subprocess.run(
            ["ip", "addr", "show"],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0
        assert bridge in result.stdout

    def test_network_deletion_and_cleanup(
        self, mvm_binary, unique_network_name
    ):
        """Create and delete network, verify cleanup."""
        # Create — use module_network fixture pattern but manual for delete test
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            _unique_subnet(unique_network_name),
            "--non-interactive",
        )

        try:
            # Verify it appears
            result = _run_mvm(mvm_binary, "network", "ls")
            assert unique_network_name in result.stdout

            # Delete
            result = _run_mvm(mvm_binary, "network", "rm", unique_network_name)
            assert result.returncode == 0
        finally:
            # Cleanup: ensure network is removed even if assertions fail
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

        # Verify gone
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks = json.loads(result.stdout)
        assert not any(n.get("name") == unique_network_name for n in networks)

    def test_duplicate_network_handling(self, mvm_binary, module_network):
        """Attempt to create duplicate network name is rejected."""
        subnet = _unique_subnet(module_network)
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            module_network,
            "--subnet",
            subnet,
            "--non-interactive",
            check=False,
        )
        assert result.returncode != 0
        assert (
            "already exists" in result.stdout.lower()
            or "already exists" in result.stderr.lower()
        )

    def test_invalid_cidr_rejection(self, mvm_binary, unique_network_name):
        """Reject invalid CIDR format."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            "invalid-cidr",
            "--non-interactive",
            check=False,
        )
        assert result.returncode != 0
        assert (
            "invalid" in result.stdout.lower()
            or "invalid" in result.stderr.lower()
        )

    def test_network_inspect(self, mvm_binary, module_network):
        """Inspect a network and verify name appears in output."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "inspect",
            module_network,
        )
        assert result.returncode == 0
        assert module_network in result.stdout

    def test_network_inspect_json(self, mvm_binary, module_network):
        """Inspect a network with --json and verify parsed fields."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "inspect",
            module_network,
            "--json",
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert "name" in data
        assert "subnet" in data
        assert "bridge" in data

    def test_network_remove_nonexistent(self, mvm_binary):
        """Removing a non-existent network returns error."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "rm",
            "nonexistent-network-name-xyz",
            check=False,
        )
        assert result.returncode != 0
        assert (
            "not found" in result.stdout.lower()
            or "not found" in result.stderr.lower()
        )

    def test_network_create_no_nat(self, mvm_binary, unique_network_name):
        """Create a network with --no-nat flag."""
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                _unique_subnet(unique_network_name),
                "--no-nat",
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_set_default(self, mvm_binary, module_network):
        """Set a network as the default."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "default",
            module_network,
        )
        assert result.returncode == 0
        assert "default" in result.stdout.lower()

    def test_network_list_json(self, mvm_binary, module_network):
        """List networks in JSON format."""
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, list)
        assert any(n["name"] == module_network for n in data)

    def test_network_remove_multiple(self, mvm_binary, unique_network_name):
        """Remove multiple networks at once."""
        name_a = f"{unique_network_name}-a"
        name_b = f"{unique_network_name}-b"

        _run_mvm(
            mvm_binary,
            "network",
            "create",
            name_a,
            "--subnet",
            _unique_subnet(name_a),
            "--non-interactive",
        )
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            name_b,
            "--subnet",
            _unique_subnet(name_b),
            "--non-interactive",
        )

        try:
            result = _run_mvm(mvm_binary, "network", "rm", name_a, name_b)
            assert result.returncode == 0
        finally:
            _run_mvm(mvm_binary, "network", "rm", name_a, check=False)
            _run_mvm(mvm_binary, "network", "rm", name_b, check=False)


class TestNetworkSync:
    """Test mvm network sync command."""

    def test_network_sync_all(self, mvm_binary, module_network):
        """Sync all networks."""
        result = _run_mvm(mvm_binary, "network", "sync", check=False)
        assert result.returncode == 0

    def test_network_sync_specific(self, mvm_binary, module_network):
        """Sync a specific network by name."""
        result = _run_mvm(
            mvm_binary, "network", "sync", module_network, check=False
        )
        assert result.returncode == 0

    def test_network_sync_json(self, mvm_binary, module_network):
        """Sync with JSON output."""
        result = _run_mvm(mvm_binary, "network", "sync", "--json", check=False)
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, dict)


class TestNetworkAdvancedCreate:
    """Test advanced network creation options."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_network_create_with_ipv4_gateway(
        self, mvm_binary, unique_network_name
    ):
        """Create a network with explicit --ipv4-gateway."""
        subnet = _unique_subnet(unique_network_name)
        custom_gateway = subnet.replace(".0/24", ".100")
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--ipv4-gateway",
                custom_gateway,
                "--non-interactive",
            )
            assert result.returncode == 0

            # Inspect and verify gateway
            inspect = _run_mvm(
                mvm_binary,
                "network",
                "inspect",
                unique_network_name,
                "--json",
            )
            data = json.loads(inspect.stdout)
            assert data.get("ipv4_gateway") == custom_gateway, (
                f"Expected gateway {custom_gateway}, got {data.get('ipv4_gateway')}"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_with_nat_gateways(
        self, mvm_binary, unique_network_name
    ):
        """Create a network with explicit --nat-gateways."""
        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--nat-gateways",
                "wlo1",
                "--non-interactive",
            )
            assert result.returncode == 0

            # Verify NAT gateways in inspect output
            inspect = _run_mvm(
                mvm_binary,
                "network",
                "inspect",
                unique_network_name,
                "--json",
            )
            data = json.loads(inspect.stdout)
            gateways = data.get("nat_gateways", [])
            assert "wlo1" in gateways, (
                f"Expected nat_gateways to contain 'wlo1', got {gateways}"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_invalid_gateway_fails(
        self, mvm_binary, unique_network_name
    ):
        """Creating a network with an invalid gateway should fail."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            "10.99.99.0/24",
            "--ipv4-gateway",
            "not-an-ip",
            "--non-interactive",
            check=False,
        )
        assert result.returncode != 0
        # Cleanup if somehow created
        _run_mvm(mvm_binary, "network", "rm", unique_network_name, check=False)


class TestNetworkInspectTree:
    """Test network inspect with --tree flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_network_inspect_tree(self, mvm_binary, module_network):
        """Inspect a network with --tree and verify tree characters in output."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "inspect",
            module_network,
            "--tree",
        )
        assert result.returncode == 0
        assert "├──" in result.stdout or "└──" in result.stdout


class TestNetworkRemoveForce:
    """Test network removal with --force flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_network_rm_with_force(self, mvm_binary, unique_network_name):
        """Create a network and remove it with --force, verify cleanup."""
        subnet = _unique_subnet(unique_network_name)
        try:
            # Create
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            # Remove with --force
            result = _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, "--force"
            )
            assert result.returncode == 0
        finally:
            # Cleanup in case force removal failed
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

        # Verify network is gone via JSON listing
        ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks = json.loads(ls_result.stdout)
        assert not any(n.get("name") == unique_network_name for n in networks)
