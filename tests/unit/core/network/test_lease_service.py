"""Tests for LeaseService — IP allocation, release, availability checks."""

from __future__ import annotations

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.network._lease_service import LeaseService
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.exceptions import NetworkError
from mvmctl.models import NetworkItem


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied."""
    database = Database()
    database.migrate()
    return database


@pytest.fixture
def network_repo(db: Database) -> NetworkRepository:
    """Create a NetworkRepository."""
    return NetworkRepository(db)


@pytest.fixture
def lease_repo(db: Database) -> LeaseRepository:
    """Create a LeaseRepository."""
    return LeaseRepository(db)


@pytest.fixture
def sample_network(network_repo: NetworkRepository) -> NetworkItem:
    """Seed and return a sample network for testing."""
    network = NetworkItem(
        id="lease-net-001",
        name="lease-net",
        subnet="10.0.0.0/24",
        bridge="mvm-lease-net",
        ipv4_gateway="10.0.0.1",
        bridge_active=True,
        nat_enabled=False,
        is_default=False,
        is_present=True,
        created_at="2026-01-01T00:00:00Z",
        updated_at="2026-01-01T00:00:00Z",
    )
    network_repo.upsert(network)
    return network


@pytest.fixture
def lease_service(
    sample_network: NetworkItem, lease_repo: LeaseRepository
) -> LeaseService:
    """Create a LeaseService for the sample network."""
    return LeaseService(sample_network, lease_repo)


class TestLeaseServiceAllocateIP:
    """Tests for IP allocation."""

    def test_lease_allocates_first_available(
        self, lease_service: LeaseService
    ) -> None:
        """lease() returns the first available IP in the subnet."""
        ip = lease_service.lease("vm-1")
        assert ip == "10.0.0.2"

    def test_lease_skips_gateway(self, lease_service: LeaseService) -> None:
        """lease() never allocates the gateway IP."""
        ip = lease_service.lease("vm-gw-test")
        assert ip != "10.0.0.1"

    def test_lease_skips_allocated_ips(
        self, lease_service: LeaseService
    ) -> None:
        """lease() skips already allocated IPs."""
        ip1 = lease_service.lease("vm-a")
        ip2 = lease_service.lease("vm-b")
        ip3 = lease_service.lease("vm-c")
        assert ip1 == "10.0.0.2"
        assert ip2 == "10.0.0.3"
        assert ip3 == "10.0.0.4"

    def test_lease_raises_when_exhausted(
        self, lease_service: LeaseService
    ) -> None:
        """lease() raises NetworkError when no IPs available."""
        # Allocate all 253 usable IPs in /24 (10.0.0.2 - 10.0.0.254)
        for i in range(253):
            lease_service.lease(f"vm-{i}")
        # Next allocation should fail
        with pytest.raises(NetworkError, match="No available IP"):
            lease_service.lease("vm-exhausted")

    def test_lease_specific_allocates(
        self, lease_service: LeaseService
    ) -> None:
        """lease_specific() allocates a specific IP."""
        ip = lease_service.lease_specific("10.0.0.42", "vm-specific")
        assert ip == "10.0.0.42"

    def test_lease_specific_raises_when_taken(
        self, lease_service: LeaseService
    ) -> None:
        """lease_specific() raises when IP is already leased."""
        lease_service.lease_specific("10.0.0.50", "vm-first")
        with pytest.raises(NetworkError, match="already leased"):
            lease_service.lease_specific("10.0.0.50", "vm-second")


class TestLeaseServiceRelease:
    """Tests for IP release."""

    def test_release_frees_ip(self, lease_service: LeaseService) -> None:
        """release() frees an IP for reuse."""
        ip = lease_service.lease("vm-to-free")
        assert ip == "10.0.0.2"
        lease_service.release("vm-to-free")
        # Same VM can get a new IP
        new_ip = lease_service.lease("vm-to-free")
        assert new_ip == "10.0.0.2"

    def test_release_multiple_vms(self, lease_service: LeaseService) -> None:
        """release() only frees the specified VM's leases."""
        ip1 = lease_service.lease("vm-a")
        ip2 = lease_service.lease("vm-b")
        lease_service.release("vm-a")
        # vm-a can now get a new IP (reuses the freed one)
        new_ip = lease_service.lease("vm-a")
        assert new_ip == ip1
        # vm-b should still have its IP
        assert not lease_service.is_available(ip2)

    def test_release_noop_for_unknown_vm(
        self, lease_service: LeaseService
    ) -> None:
        """release() is a no-op for a VM with no leases."""
        lease_service.release("never-existed")  # Should not raise


class TestLeaseServiceAvailability:
    """Tests for IP availability checks."""

    def test_is_available_returns_true_for_free(
        self, lease_service: LeaseService
    ) -> None:
        """is_available() returns True for an unleased IP."""
        assert lease_service.is_available("10.0.0.2") is True

    def test_is_available_returns_false_for_leased(
        self, lease_service: LeaseService
    ) -> None:
        """is_available() returns False for a leased IP."""
        lease_service.lease("vm-occupy")
        assert lease_service.is_available("10.0.0.2") is False


class TestLeaseServiceGetLeases:
    """Tests for get_leases and related methods."""

    def test_get_leases_empty(self, lease_service: LeaseService) -> None:
        """get_leases() returns empty list when no leases exist."""
        leases = lease_service.get_leases()
        assert leases == []

    def test_get_leases_returns_all(self, lease_service: LeaseService) -> None:
        """get_leases() returns all leases for the network."""
        lease_service.lease("vm-get-1")
        lease_service.lease("vm-get-2")
        leases = lease_service.get_leases()
        assert len(leases) == 2

    def test_get_returns_lease_for_ip(
        self, lease_service: LeaseService
    ) -> None:
        """get() returns the lease for a specific IP."""
        lease_service.lease_specific("10.0.0.10", "vm-get-ip")
        lease = lease_service.get("10.0.0.10")
        assert lease is not None
        assert lease.ipv4 == "10.0.0.10"
        assert lease.vm_id == "vm-get-ip"

    def test_get_returns_none_for_unleased(
        self, lease_service: LeaseService
    ) -> None:
        """get() returns None for an unleased IP."""
        lease = lease_service.get("10.0.0.99")
        assert lease is None

    def test_get_by_vm_id(self, lease_service: LeaseService) -> None:
        """get_by_vm_id() returns leases for a specific VM."""
        lease_service.lease("vm-a")
        lease_service.lease("vm-b")
        leases_a = lease_service.get_by_vm_id("vm-a")
        assert len(leases_a) == 1
        assert leases_a[0].vm_id == "vm-a"

    def test_get_by_vm_id_returns_empty_for_unknown(
        self, lease_service: LeaseService
    ) -> None:
        """get_by_vm_id() returns empty list for unknown VM."""
        assert lease_service.get_by_vm_id("unknown") == []


class TestLeaseServiceNetworkResolution:
    """Tests for LeaseService with string-based network resolution."""

    def test_init_with_name(
        self,
        db: Database,
        network_repo: NetworkRepository,
        lease_repo: LeaseRepository,
    ) -> None:
        """LeaseService resolves network by name string."""
        # Seed a network
        network = NetworkItem(
            id="resolve-name-001",
            name="resolve-me",
            subnet="10.10.0.0/24",
            bridge="mvm-resolve-me",
            ipv4_gateway="10.10.0.1",
            bridge_active=True,
            nat_enabled=False,
            is_default=False,
            is_present=True,
            created_at="2026-01-01T00:00:00Z",
            updated_at="2026-01-01T00:00:00Z",
        )
        network_repo.upsert(network)

        service = LeaseService("resolve-me", lease_repo)
        assert service.network_name == "resolve-me"
        assert service.network_id == "resolve-name-001"

    def test_get_leases_after_init_with_name(
        self,
        db: Database,
        network_repo: NetworkRepository,
        lease_repo: LeaseRepository,
    ) -> None:
        """LeaseService can allocate IPs after init with name string."""
        network = NetworkItem(
            id="resolve-alloc-001",
            name="alloc-me",
            subnet="10.20.0.0/24",
            bridge="mvm-alloc-me",
            ipv4_gateway="10.20.0.1",
            bridge_active=True,
            nat_enabled=False,
            is_default=False,
            is_present=True,
            created_at="2026-01-01T00:00:00Z",
            updated_at="2026-01-01T00:00:00Z",
        )
        network_repo.upsert(network)

        service = LeaseService("alloc-me", lease_repo)
        ip = service.lease("vm-test")
        assert ip == "10.20.0.2"


class TestLeaseServiceEdgeCases:
    """Tests for edge cases."""

    def test_subnet_with_31_prefix(
        self, network_repo: NetworkRepository, lease_repo: LeaseRepository
    ) -> None:
        """lease() works with /31 subnets (RFC 3021)."""
        network = NetworkItem(
            id="rfc3021-001",
            name="rfc3021-net",
            subnet="10.99.0.0/31",
            bridge="mvm-rfc3021",
            ipv4_gateway="10.99.0.1",
            bridge_active=True,
            nat_enabled=False,
            is_default=False,
            is_present=True,
            created_at="2026-01-01T00:00:00Z",
            updated_at="2026-01-01T00:00:00Z",
        )
        network_repo.upsert(network)

        service = LeaseService(network, lease_repo)
        ip = service.lease("vm-rfc3021")
        # /31 has only 2 usable addresses: 10.99.0.1 (gateway) and 10.99.0.2
        # So the first allocation should be 10.99.0.2 if gateway is 10.99.0.1
        assert ip is not None
        assert ip != network.ipv4_gateway

    def test_lease_after_release_reuses_ip(
        self, lease_service: LeaseService
    ) -> None:
        """Released IPs should be reused by subsequent allocations."""
        ip1 = lease_service.lease("vm-reuse-1")
        lease_service.release("vm-reuse-1")
        ip2 = lease_service.lease("vm-reuse-2")
        assert ip2 == ip1
