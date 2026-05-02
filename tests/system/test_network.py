"""Network management system tests."""

from __future__ import annotations

import json
import subprocess

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet

pytestmark = [pytest.mark.system, pytest.mark.requires_network]


class TestNetworkLifecycle:
    """Test network CRUD operations."""

    def test_network_create_with_generated_subnet(
        self, mvm_binary, unique_network_name
    ):
        """Create network with a dynamically generated unique subnet."""
        from tests.system.conftest import _unique_subnet

        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
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
        from tests.system.conftest import _unique_subnet

        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_listing_and_verification(
        self, mvm_binary, created_network
    ):
        """List networks and verify created network appears."""
        result = _run_mvm(mvm_binary, "network", "ls")
        assert result.returncode == 0
        assert created_network in result.stdout

    def test_ip_rule_verification_iptables(self, created_network):
        """Verify iptables rules were created for network."""
        result = subprocess.run(
            ["sudo", "iptables", "-t", "nat", "-L"],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0
        # Bridge name for this network should appear in iptables rules
        assert f"mvm-{created_network}" in result.stdout

    def test_nat_gateway_configuration(self, created_network):
        """Verify bridge interface exists for created network."""
        result = subprocess.run(
            ["ip", "addr", "show"],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0
        assert f"mvm-{created_network}" in result.stdout

    def test_network_deletion_and_cleanup(
        self, mvm_binary, unique_network_name
    ):
        """Create and delete network, verify cleanup."""
        # Create — use created_network fixture pattern but manual for delete test
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            _unique_subnet(unique_network_name),
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

    def test_duplicate_network_handling(self, mvm_binary, created_network):
        """Attempt to create duplicate network name is rejected."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            created_network,
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
            check=False,
        )
        assert result.returncode != 0
        assert (
            "invalid" in result.stdout.lower()
            or "invalid" in result.stderr.lower()
        )

    def test_network_inspect(self, mvm_binary, created_network):
        """Inspect a network and verify name appears in output."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "inspect",
            created_network,
        )
        assert result.returncode == 0
        assert created_network in result.stdout

    def test_network_inspect_json(self, mvm_binary, created_network):
        """Inspect a network with --json and verify parsed fields."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "inspect",
            created_network,
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

    def test_network_set_default(self, mvm_binary, created_network):
        """Set a network as the default."""
        from tests.system.conftest import _skip_if_parallel

        _skip_if_parallel()
        result = _run_mvm(
            mvm_binary,
            "network",
            "set-default",
            created_network,
        )
        assert result.returncode == 0
        assert "default" in result.stdout.lower()

    def test_network_list_json(self, mvm_binary, created_network):
        """List networks in JSON format."""
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, list)
        assert any(n["name"] == created_network for n in data)

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
        )
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            name_b,
            "--subnet",
            _unique_subnet(name_b),
        )

        try:
            result = _run_mvm(mvm_binary, "network", "rm", name_a, name_b)
            assert result.returncode == 0
        finally:
            _run_mvm(mvm_binary, "network", "rm", name_a, check=False)
            _run_mvm(mvm_binary, "network", "rm", name_b, check=False)


class TestNetworkSync:
    """Test mvm network sync command."""

    def test_network_sync_all(self, mvm_binary, created_network):
        """Sync all networks."""
        result = _run_mvm(mvm_binary, "network", "sync", check=False)
        assert result.returncode == 0

    def test_network_sync_specific(self, mvm_binary, created_network):
        """Sync a specific network by name."""
        result = _run_mvm(
            mvm_binary, "network", "sync", created_network, check=False
        )
        assert result.returncode == 0

    def test_network_sync_json(self, mvm_binary, created_network):
        """Sync with JSON output."""
        result = _run_mvm(mvm_binary, "network", "sync", "--json", check=False)
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, dict)
