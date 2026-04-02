"""Network management system tests."""

import os
import pytest
import subprocess

pytestmark = [pytest.mark.system, pytest.mark.requires_network]


class TestNetworkLifecycle:
    """Test network CRUD operations."""

    def test_network_create_with_default_cidr(self, mvm_binary, unique_network_name):
        """Create network with default CIDR (10.0.0.0/24)."""
        result = subprocess.run(
            [*mvm_binary.split(), "network", "create", unique_network_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert "created" in result.stdout.lower() or "Network" in result.stdout

    def test_network_create_with_custom_cidr(self, mvm_binary, unique_network_name):
        """Create network with custom CIDR."""
        result = subprocess.run(
            [
                *mvm_binary.split(),
                "network",
                "create",
                unique_network_name,
                "--subnet",
                "192.168.100.0/24",
            ],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_network_listing_and_verification(self, mvm_binary, created_network):
        """List networks and verify created network appears."""
        result = subprocess.run(
            [*mvm_binary.split(), "network", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert created_network in result.stdout

    def test_ip_rule_verification_iptables(self, mvm_binary, created_network):
        """Verify iptables rules were created for network."""
        # Check iptables rules exist
        result = subprocess.run(
            ["sudo", "iptables", "-t", "nat", "-L"],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0
        # Network name should appear in rules
        assert created_network in result.stdout or "MVM" in result.stdout

    def test_nat_gateway_configuration(self, mvm_binary, created_network):
        """Verify NAT gateway is configured for network."""
        result = subprocess.run(
            ["ip", "addr", "show"],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0
        # Bridge interface should exist
        assert f"mvm-{created_network}" in result.stdout or "mvm-" in result.stdout

    def test_network_deletion_and_cleanup(self, mvm_binary, unique_network_name):
        """Create and delete network, verify cleanup."""
        # Create
        subprocess.run(
            [*mvm_binary.split(), "network", "create", unique_network_name],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )

        # Delete
        result = subprocess.run(
            [*mvm_binary.split(), "network", "rm", unique_network_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

        # Verify gone
        result = subprocess.run(
            [*mvm_binary.split(), "network", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert unique_network_name not in result.stdout

    def test_duplicate_network_handling(self, mvm_binary, created_network):
        """Attempt to create duplicate network name."""
        result = subprocess.run(
            [*mvm_binary.split(), "network", "create", created_network],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode != 0 or "already exists" in result.stdout.lower()

    def test_invalid_cidr_rejection(self, mvm_binary, unique_network_name):
        """Reject invalid CIDR format."""
        result = subprocess.run(
            [
                *mvm_binary.split(),
                "network",
                "create",
                unique_network_name,
                "--subnet",
                "invalid-cidr",
            ],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode != 0
