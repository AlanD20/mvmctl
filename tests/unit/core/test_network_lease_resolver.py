"""Tests for core/network/_lease_resolver.py — NetworkLeaseResolver."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.core.network._lease_resolver import NetworkLeaseResolver
from mvmctl.models import NetworkLeaseItem


class TestNetworkLeaseResolver:
    """Tests for NetworkLeaseResolver — lease resolution and enrichment.

    NetworkLeaseResolver wraps a ``LeaseRepository`` and optionally applies
    ``RelationEnricher`` when an ``include`` list is provided.  The resolver
    exposes four query methods, each of which we verify delegates to the
    repo and (where applicable) applies enrichment.
    """

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    @staticmethod
    def _lease(
        ipv4: str = "10.0.0.2",
        network_id: str = "net-abc",
        vm_id: str | None = None,
    ) -> NetworkLeaseItem:
        return NetworkLeaseItem(
            network_id=network_id,
            ipv4=ipv4,
            leased_at="2026-01-01T12:00:00",
            vm_id=vm_id,
        )

    # ------------------------------------------------------------------
    # _enrich — uncovered line 29 (plain return when no include)
    # ------------------------------------------------------------------

    def test_enrich_no_include_returns_raw(self) -> None:
        """When ``include`` is None, ``_enrich`` returns leases unchanged."""
        repo = MagicMock()
        resolver = NetworkLeaseResolver(repo=repo, include=None)
        leases = [self._lease()]
        result = resolver._enrich(leases)
        assert result is leases  # same list reference — no copy

    def test_enrich_empty_leases_returns_raw(self) -> None:
        """When leases list is empty, ``_enrich`` returns it unchanged."""
        repo = MagicMock()
        resolver = NetworkLeaseResolver(repo=repo, include=["vm"])
        result = resolver._enrich([])
        assert result == []

    # ------------------------------------------------------------------
    # list_by_network_id — uncovered entirely (lines 32-35)
    # ------------------------------------------------------------------

    def test_list_by_network_id(self) -> None:
        """Delegates to ``repo.list_all(network_id)``."""
        repo = MagicMock()
        expected = [self._lease(ipv4="10.0.0.2")]
        repo.list_all.return_value = expected

        resolver = NetworkLeaseResolver(repo=repo)
        result = resolver.list_by_network_id("net-abc")

        repo.list_all.assert_called_once_with("net-abc")
        assert result == expected

    # ------------------------------------------------------------------
    # list_by_network_id_batch — uncovered line 46 (lease grouping)
    # ------------------------------------------------------------------

    def test_list_by_network_id_batch_groups_correctly(self) -> None:
        """Leases are grouped by ``network_id`` in the result dict.

        Leases whose ``network_id`` is *not* in the requested batch are
        silently dropped.
        """
        repo = MagicMock()
        repo.list_all_batch.return_value = [
            self._lease(ipv4="10.0.0.2", network_id="net-a"),
            self._lease(ipv4="10.0.0.3", network_id="net-a"),
            self._lease(ipv4="10.0.0.4", network_id="net-b"),
            self._lease(ipv4="10.0.0.5", network_id="net-c"),  # not requested
        ]

        resolver = NetworkLeaseResolver(repo=repo)
        result = resolver.list_by_network_id_batch(["net-a", "net-b"])

        assert list(result.keys()) == ["net-a", "net-b"]
        assert len(result["net-a"]) == 2
        assert len(result["net-b"]) == 1
        assert result["net-a"][0].ipv4 == "10.0.0.2"
        assert result["net-a"][1].ipv4 == "10.0.0.3"
        assert result["net-b"][0].ipv4 == "10.0.0.4"

    def test_list_by_network_id_batch_empty_result(self) -> None:
        """When no leases match, each network ID gets an empty list."""
        repo = MagicMock()
        repo.list_all_batch.return_value = []

        resolver = NetworkLeaseResolver(repo=repo)
        result = resolver.list_by_network_id_batch(["net-a", "net-b"])

        assert list(result.keys()) == ["net-a", "net-b"]
        assert result["net-a"] == []
        assert result["net-b"] == []

    # ------------------------------------------------------------------
    # get — uncovered lines 52-55
    # ------------------------------------------------------------------

    def test_get_returns_lease_when_found(self) -> None:
        """get() returns the enriched lease when the repo finds it."""
        repo = MagicMock()
        lease = self._lease(ipv4="10.0.0.2", network_id="net-a")
        repo.get.return_value = lease

        resolver = NetworkLeaseResolver(repo=repo)
        result = resolver.get("net-a", "10.0.0.2")

        repo.get.assert_called_once_with("net-a", "10.0.0.2")
        assert result is not None
        assert result.ipv4 == "10.0.0.2"
        assert result.network_id == "net-a"

    def test_get_returns_none_when_not_found(self) -> None:
        """get() returns None when the repo returns None."""
        repo = MagicMock()
        repo.get.return_value = None

        resolver = NetworkLeaseResolver(repo=repo)
        result = resolver.get("net-a", "10.0.0.99")

        repo.get.assert_called_once_with("net-a", "10.0.0.99")
        assert result is None

    # ------------------------------------------------------------------
    # list_by_vm — uncovered lines 59-60
    # ------------------------------------------------------------------

    def test_list_by_vm(self) -> None:
        """Delegates to ``repo.list_by_vm(network_id, vm_id)``."""
        repo = MagicMock()
        expected = [
            self._lease(ipv4="10.0.0.2", network_id="net-a", vm_id="vm-1"),
        ]
        repo.list_by_vm.return_value = expected

        resolver = NetworkLeaseResolver(repo=repo)
        result = resolver.list_by_vm("net-a", "vm-1")

        repo.list_by_vm.assert_called_once_with("net-a", "vm-1")
        assert result == expected
