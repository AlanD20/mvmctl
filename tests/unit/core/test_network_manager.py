"""Unit tests for IP lease checking functions in network_manager module."""

from unittest.mock import MagicMock, patch

import pytest
from pytest_mock import MockerFixture

from mvmctl.core.network_manager import (
    NetworkLease,
    check_ip_available,
    is_ip_available,
)
from mvmctl.exceptions import NetworkError


class TestIsIpAvailable:
    """Tests for is_ip_available function."""

    def test_is_ip_available_true(self, mocker: MockerFixture):
        """IP not in leases returns True."""
        # Mock get_network_leases to return some leases, but not the IP we're checking
        mocker.patch(
            "mvmctl.core.network_manager.get_network_leases",
            return_value=[
                NetworkLease(vm_name="vm1", ip="10.0.0.2"),
                NetworkLease(vm_name="vm2", ip="10.0.0.3"),
            ],
        )
        
        result = is_ip_available("default", "10.0.0.5")
        
        assert result is True

    def test_is_ip_available_false(self, mocker: MockerFixture):
        """IP in leases returns False."""
        # Mock get_network_leases to return leases that include the IP we're checking
        mocker.patch(
            "mvmctl.core.network_manager.get_network_leases",
            return_value=[
                NetworkLease(vm_name="vm1", ip="10.0.0.2"),
                NetworkLease(vm_name="vm2", ip="10.0.0.5"),  # This is the IP we're checking
            ],
        )
        
        result = is_ip_available("default", "10.0.0.5")
        
        assert result is False


class TestCheckIpAvailable:
    """Tests for check_ip_available function."""

    def test_check_ip_available_raises(self, mocker: MockerFixture):
        """Raises NetworkError when IP is taken."""
        # Mock get_network_leases to return leases that include the IP
        mocker.patch(
            "mvmctl.core.network_manager.get_network_leases",
            return_value=[
                NetworkLease(vm_name="vm1", ip="10.0.0.5"),
            ],
        )
        
        with pytest.raises(NetworkError, match="10.0.0.5 is already in use"):
            check_ip_available("default", "10.0.0.5")

    def test_check_ip_available_passes(self, mocker: MockerFixture):
        """No error when IP is available."""
        # Mock get_network_leases to return empty leases
        mocker.patch(
            "mvmctl.core.network_manager.get_network_leases",
            return_value=[],
        )
        
        # Should not raise any exception
        check_ip_available("default", "10.0.0.5")
