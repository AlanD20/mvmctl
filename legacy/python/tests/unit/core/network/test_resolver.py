"""Tests for NetworkResolver — network entity resolution."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.core.network._resolver import NetworkResolver
from mvmctl.exceptions import NetworkNotFoundError
from mvmctl.models import NetworkItem


def _make_net(name="testnet", net_id="net-001", **kwargs) -> NetworkItem:
    defaults = dict(
        id=net_id,
        name=name,
        subnet="10.0.0.0/24",
        bridge="mvm-br0",
        ipv4_gateway="10.0.0.1",
        bridge_active=True,
        nat_enabled=True,
        is_default=False,
        is_present=True,
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
    )
    defaults.update(kwargs)
    return NetworkItem(**defaults)


class TestNetworkResolverById:
    def test_finds_by_exact_id(self, mocker):
        net = _make_net(net_id="net-001")
        mock_repo = MagicMock()
        mock_repo.find_by_prefix.return_value = [net]
        resolver = NetworkResolver(mock_repo)
        result = resolver.by_id("net-001")
        assert result.id == "net-001"

    def test_raises_on_not_found(self, mocker):
        mock_repo = MagicMock()
        mock_repo.find_by_prefix.return_value = []
        resolver = NetworkResolver(mock_repo)
        with pytest.raises(NetworkNotFoundError, match="not found"):
            resolver.by_id("nonexistent")

    def test_raises_on_ambiguous(self, mocker):
        mock_repo = MagicMock()
        mock_repo.find_by_prefix.return_value = [
            _make_net(net_id="abc111"),
            _make_net(net_id="abc222"),
        ]
        resolver = NetworkResolver(mock_repo)
        with pytest.raises(NetworkNotFoundError, match="ambiguous"):
            resolver.by_id("abc")


class TestNetworkResolverByName:
    def test_finds_by_name(self, mocker):
        net = _make_net(name="testnet")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = net
        resolver = NetworkResolver(mock_repo)
        result = resolver.by_name("testnet")
        assert result.name == "testnet"

    def test_raises_on_not_found(self, mocker):
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        resolver = NetworkResolver(mock_repo)
        with pytest.raises(NetworkNotFoundError, match="not found"):
            resolver.by_name("nonexistent")


class TestNetworkResolverGetDefault:
    def test_returns_default(self, mocker):
        net = _make_net(name="default", is_default=True)
        mock_repo = MagicMock()
        mock_repo.get_default.return_value = net
        resolver = NetworkResolver(mock_repo)
        result = resolver.get_default()
        assert result.name == "default"
        assert result.is_default is True

    def test_returns_none(self, mocker):
        mock_repo = MagicMock()
        mock_repo.get_default.return_value = None
        resolver = NetworkResolver(mock_repo)
        assert resolver.get_default() is None


class TestNetworkResolverResolve:
    def test_resolve_by_name(self, mocker):
        net = _make_net(name="mynet")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = net
        resolver = NetworkResolver(mock_repo)
        result = resolver.resolve("mynet")
        assert result.name == "mynet"

    def test_resolve_fallback_to_id(self, mocker):
        net = _make_net(net_id="id-001")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mock_repo.find_by_prefix.return_value = [net]
        resolver = NetworkResolver(mock_repo)
        result = resolver.resolve("id-001")
        assert result.id == "id-001"
        mock_repo.get_by_name.assert_called_once_with("id-001")
        mock_repo.find_by_prefix.assert_called_once_with("id-001")

    def test_resolve_raises_on_no_match(self, mocker):
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mock_repo.find_by_prefix.return_value = []
        resolver = NetworkResolver(mock_repo)
        with pytest.raises(NetworkNotFoundError):
            resolver.resolve("unknown")


class TestNetworkResolverResolveMany:
    def test_resolves_multiple(self, mocker):
        net1 = _make_net(name="net1", net_id="id1")
        net2 = _make_net(name="net2", net_id="id2")
        mock_repo = MagicMock()
        mock_repo.get_by_name.side_effect = [net1, net2]
        resolver = NetworkResolver(mock_repo)
        result = resolver.resolve_many(["net1", "net2"])
        assert len(result.items) == 2
        assert result.exit_code == 0

    def test_deduplicates(self, mocker):
        net = _make_net(name="net1", net_id="id1")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = net
        resolver = NetworkResolver(mock_repo)
        result = resolver.resolve_many(["net1", "net1"])
        assert len(result.items) == 1

    def test_partial_errors(self, mocker):
        net1 = _make_net(name="net1", net_id="id1")
        mock_repo = MagicMock()
        mock_repo.get_by_name.side_effect = [
            net1,
            NetworkNotFoundError("not found"),
        ]
        resolver = NetworkResolver(mock_repo)
        result = resolver.resolve_many(["net1", "bad"])
        assert len(result.items) == 1
        assert len(result.errors) == 1
