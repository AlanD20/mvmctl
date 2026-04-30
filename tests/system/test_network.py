"""Network management system tests."""

from __future__ import annotations

import json
import subprocess

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.requires_network]


class TestNetworkLifecycle:
    """Test network CRUD operations."""

    def test_network_create_with_default_cidr(
        self, mvm_binary, unique_network_name
    ):
        """Create network with default CIDR (10.0.0.0/24)."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
        )
        assert result.returncode == 0
        assert unique_network_name in result.stdout

    def test_network_create_with_custom_cidr(
        self, mvm_binary, unique_network_name
    ):
        """Create network with custom CIDR."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            "192.168.100.0/24",
        )
        assert result.returncode == 0

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
        assert created_network in result.stdout

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
        _run_mvm(mvm_binary, "network", "create", unique_network_name)

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
        result = _run_mvm(mvm_binary, "network", "ls")
        assert unique_network_name not in result.stdout

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
                "--no-nat",
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_set_default(self, mvm_binary, created_network):
        """Set a network as the default."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "set-default",
            created_network,
        )
        assert result.returncode == 0
        assert "default" in result.stdout.lower()
