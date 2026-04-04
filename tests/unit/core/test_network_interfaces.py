"""Unit tests for list_network_interfaces function."""

from pathlib import Path
from unittest.mock import MagicMock

import pytest
from pytest_mock import MockerFixture

from mvmctl.utils.network import list_network_interfaces
from mvmctl.exceptions import NetworkError


class TestListNetworkInterfaces:
    """Tests for list_network_interfaces function."""

    def test_list_network_interfaces_success(self, mocker: MockerFixture, tmp_path: Path):
        """Mock /sys/class/net with valid interfaces."""
        # Create a mock /sys/class/net directory structure
        mock_net_path = mocker.patch("mvmctl.utils.network.Path")
        mock_net_path.return_value.exists.return_value = True

        # Create mock interface entries
        mock_entries = []
        for name in ["eth0", "enp0s1", "wlan0"]:
            mock_entry = MagicMock()
            mock_entry.name = name
            mock_entries.append(mock_entry)

        mock_iterdir = mock_net_path.return_value.iterdir
        mock_iterdir.return_value = iter(mock_entries)

        result = list_network_interfaces()

        assert sorted(result) == ["enp0s1", "eth0", "wlan0"]

    def test_list_network_interfaces_filters_virtual(self, mocker: MockerFixture, tmp_path: Path):
        """Ensure virtual interfaces are filtered."""
        mock_net_path = mocker.patch("mvmctl.utils.network.Path")
        mock_net_path.return_value.exists.return_value = True

        # Create mock interface entries including virtual ones
        mock_entries = []
        for name in ["eth0", "mvm-test", "tap0", "br-mvm", "lo", "virbr0", "docker0", "veth123"]:
            mock_entry = MagicMock()
            mock_entry.name = name
            mock_entries.append(mock_entry)

        mock_iterdir = mock_net_path.return_value.iterdir
        mock_iterdir.return_value = iter(mock_entries)

        result = list_network_interfaces()

        # Should only contain eth0 (lo is excluded, mvm-*/tap/br-/virbr/docker/veth* are filtered)
        assert result == ["eth0"]

    def test_list_network_interfaces_empty(self, mocker: MockerFixture, tmp_path: Path):
        """Handle case with no interfaces."""
        mock_net_path = mocker.patch("mvmctl.utils.network.Path")
        mock_net_path.return_value.exists.return_value = True
        mock_net_path.return_value.iterdir.return_value = iter([])

        result = list_network_interfaces()

        assert result == []

    def test_list_network_interfaces_error(self, mocker: MockerFixture, tmp_path: Path):
        """Handle read errors gracefully."""
        mock_net_path = mocker.patch("mvmctl.utils.network.Path")
        mock_net_path.return_value.exists.return_value = True
        mock_net_path.return_value.iterdir.side_effect = OSError("Permission denied")

        with pytest.raises(NetworkError, match="Failed to list network interfaces"):
            list_network_interfaces()
