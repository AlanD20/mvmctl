"""Unit tests for IP lease checking functions in network module."""

import pytest
from pytest_mock import MockerFixture

from mvmctl.api.network import (
    check_ip_available,
    is_ip_available,
)
from mvmctl.core.network_manager import NetworkLease
from mvmctl.api.inputs import NetworkConfig
from mvmctl.exceptions import NetworkError


class TestIsIpAvailable:
    """Tests for is_ip_available function."""

    def test_is_ip_available_true(self, mocker: MockerFixture):
        """IP not in leases returns True."""
        mocker.patch(
            "mvmctl.api.network.get_network",
            return_value=NetworkConfig(
                name="default",
                subnet="10.0.0.0/24",
                ipv4_gateway="10.0.0.1",
                bridge="mvm-default",
            ),
        )
        mocker.patch(
            "mvmctl.api.network.get_network_leases",
            return_value=[
                NetworkLease(vm_id="vm1", ipv4="10.0.0.2"),
                NetworkLease(vm_id="vm2", ipv4="10.0.0.3"),
            ],
        )

        result = is_ip_available("default", "10.0.0.5")

        assert result is True

    def test_is_ip_available_false(self, mocker: MockerFixture):
        """IP in leases returns False."""
        mocker.patch(
            "mvmctl.api.network.get_network",
            return_value=NetworkConfig(
                name="default",
                subnet="10.0.0.0/24",
                ipv4_gateway="10.0.0.1",
                bridge="mvm-default",
            ),
        )
        mocker.patch(
            "mvmctl.api.network.get_network_leases",
            return_value=[
                NetworkLease(vm_id="vm1", ipv4="10.0.0.2"),
                NetworkLease(vm_id="vm2", ipv4="10.0.0.5"),
            ],
        )

        result = is_ip_available("default", "10.0.0.5")

        assert result is False


class TestCheckIpAvailable:
    """Tests for check_ip_available function."""

    def test_check_ip_available_raises(self, mocker: MockerFixture):
        """Raises NetworkError when IP is taken."""
        mocker.patch(
            "mvmctl.api.network.get_network",
            return_value=NetworkConfig(
                name="default",
                subnet="10.0.0.0/24",
                ipv4_gateway="10.0.0.1",
                bridge="mvm-default",
            ),
        )
        mocker.patch(
            "mvmctl.api.network.get_network_leases",
            return_value=[
                NetworkLease(vm_id="vm1", ipv4="10.0.0.5"),
            ],
        )

        with pytest.raises(NetworkError, match="10.0.0.5 is already in use"):
            check_ip_available("default", "10.0.0.5")

    def test_check_ip_available_passes(self, mocker: MockerFixture):
        """No error when IP is available."""
        mocker.patch(
            "mvmctl.api.network.get_network",
            return_value=NetworkConfig(
                name="default",
                subnet="10.0.0.0/24",
                ipv4_gateway="10.0.0.1",
                bridge="mvm-default",
            ),
        )
        mocker.patch(
            "mvmctl.api.network.get_network_leases",
            return_value=[],
        )

        check_ip_available("default", "10.0.0.5")
