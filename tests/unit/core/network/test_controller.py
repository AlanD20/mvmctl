"""Tests for NetworkController — entity lifecycle management."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.network._controller import NetworkController
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.exceptions import NetworkError, NetworkNotFoundError
from mvmctl.models import NetworkItem


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied."""
    database = Database()
    database.migrate()
    return database


@pytest.fixture
def repo(db: Database) -> NetworkRepository:
    """Create a NetworkRepository."""
    return NetworkRepository(db)


@pytest.fixture
def seed_network(repo: NetworkRepository) -> NetworkItem:
    """Seed and return a network for testing."""
    network = NetworkItem(
        id="ctrl-net-001",
        name="ctrl-net",
        subnet="10.0.0.0/24",
        bridge="mvm-ctrl-net",
        ipv4_gateway="10.0.0.1",
        bridge_active=True,
        nat_enabled=True,
        is_default=False,
        is_present=True,
        created_at="2026-01-01T00:00:00Z",
        updated_at="2026-01-01T00:00:00Z",
        nat_gateways="eth0",
    )
    repo.upsert(network)
    return network


@pytest.fixture
def seed_second_network(repo: NetworkRepository) -> NetworkItem:
    """Seed a second network for multi-network tests."""
    network = NetworkItem(
        id="ctrl-net-002",
        name="second-net",
        subnet="10.1.0.0/24",
        bridge="mvm-second-net",
        ipv4_gateway="10.1.0.1",
        bridge_active=False,
        nat_enabled=False,
        is_default=False,
        is_present=True,
        created_at="2026-01-01T00:00:00Z",
        updated_at="2026-01-01T00:00:00Z",
    )
    repo.upsert(network)
    return network


class TestNetworkControllerInit:
    """Tests for NetworkController initialization."""

    def test_init_with_network_item(
        self, seed_network: NetworkItem, repo: NetworkRepository
    ) -> None:
        """NetworkController accepts a NetworkItem directly."""
        controller = NetworkController(seed_network, repo)
        assert controller.get().name == "ctrl-net"
        assert controller.get().id == "ctrl-net-001"

    def test_init_with_name_string(
        self, seed_network: NetworkItem, repo: NetworkRepository
    ) -> None:
        """NetworkController resolves network by name string."""
        controller = NetworkController("ctrl-net", repo)
        assert controller.get().name == "ctrl-net"
        assert controller.get().id == "ctrl-net-001"

    def test_init_with_nonexistent_name(self, repo: NetworkRepository) -> None:
        """NetworkController raises NetworkNotFoundError for unknown name."""
        with pytest.raises(NetworkNotFoundError, match="not found"):
            NetworkController("nonexistent", repo)


class TestNetworkControllerGet:
    """Tests for the get() method."""

    def test_get_returns_network_item(
        self, seed_network: NetworkItem, repo: NetworkRepository
    ) -> None:
        """get() returns the resolved NetworkItem."""
        controller = NetworkController(seed_network, repo)
        network = controller.get()
        assert isinstance(network, NetworkItem)
        assert network.name == "ctrl-net"
        assert network.subnet == "10.0.0.0/24"
        assert network.bridge == "mvm-ctrl-net"
        assert network.ipv4_gateway == "10.0.0.1"
        assert network.bridge_active is True
        assert network.nat_enabled is True


class TestNetworkControllerSetDefault:
    """Tests for set_default() and get_default()."""

    def test_set_default(
        self, seed_network: NetworkItem, repo: NetworkRepository
    ) -> None:
        """set_default() marks the network as default."""
        controller = NetworkController(seed_network, repo)
        controller.set_default()
        default = repo.get_default()
        assert default is not None
        assert default.id == "ctrl-net-001"
        assert bool(default.is_default)

    def test_set_default_clears_previous(
        self,
        seed_network: NetworkItem,
        seed_second_network: NetworkItem,
        repo: NetworkRepository,
    ) -> None:
        """set_default() clears default from previously default network."""
        # First set first network as default
        controller1 = NetworkController(seed_network, repo)
        controller1.set_default()

        # Then set second network as default
        controller2 = NetworkController(seed_second_network, repo)
        controller2.set_default()

        # First network should no longer be default
        net1 = repo.get("ctrl-net-001")
        assert net1 is not None
        assert not net1.is_default

        # Second network should be default
        net2 = repo.get("ctrl-net-002")
        assert net2 is not None
        assert bool(net2.is_default)

    def test_get_default_initially_none(self, repo: NetworkRepository) -> None:
        """get_default() returns None when no default is set."""
        default = repo.get_default()
        assert default is None


class TestNetworkControllerGetLeases:
    """Tests for get_leases()."""

    def test_get_leases_empty(
        self, seed_network: NetworkItem, repo: NetworkRepository
    ) -> None:
        """get_leases() returns empty list when no leases exist."""
        controller = NetworkController(seed_network, repo)
        leases = controller.get_leases()
        assert leases == []

    def test_get_leases_with_allocations(
        self, seed_network: NetworkItem, repo: NetworkRepository
    ) -> None:
        """get_leases() returns all leases after allocation."""
        from mvmctl.core.network._lease_service import LeaseService
        from mvmctl.core.network._repository import LeaseRepository

        # Allocate IPs via LeaseService
        lease_repo = LeaseRepository(repo.db)
        lease_service = LeaseService(seed_network, lease_repo)
        lease_service.lease("vm-1")
        lease_service.lease("vm-2")

        # Then get leases via controller
        controller = NetworkController(seed_network, repo)
        leases = controller.get_leases()
        assert len(leases) == 2
        ips = {lease.ipv4 for lease in leases}
        assert ips == {"10.0.0.2", "10.0.0.3"}

    def test_get_leases_with_vm_ids(
        self, seed_network: NetworkItem, repo: NetworkRepository
    ) -> None:
        """get_leases() returns leases with correct VM IDs."""
        from mvmctl.core.network._lease_service import LeaseService
        from mvmctl.core.network._repository import LeaseRepository

        lease_repo = LeaseRepository(repo.db)
        lease_service = LeaseService(seed_network, lease_repo)
        lease_service.lease("vm-test-1")
        lease_service.lease("vm-test-2")

        controller = NetworkController(seed_network, repo)
        leases = controller.get_leases()
        vm_ids = {lease.vm_id for lease in leases}
        assert vm_ids == {"vm-test-1", "vm-test-2"}


class TestNetworkServiceRemove:
    """Tests for NetworkService.remove() — infrastructure + DB removal."""

    def _make_service(self, repo: NetworkRepository) -> NetworkService:
        """Create NetworkService and mock subprocess calls."""
        service = NetworkService(repo)
        return service

    def test_remove_raises_when_vms_reference(
        self, seed_network: NetworkItem, repo: NetworkRepository, mocker
    ) -> None:
        """remove() raises NetworkError when VMs reference the network and force=False."""
        # Seed a VM that references this network
        from mvmctl.models import VMInstanceItem

        now = "2026-01-01T00:00:00Z"
        vm = VMInstanceItem(
            id="vm-on-net-001",
            name="vm-on-net",
            status="stopped",
            pid=0,
            ipv4="10.0.0.2",
            mac="02:FC:00:00:00:01",
            network_id="ctrl-net-001",
            tap_device="tap-vm1",
            image_id="img-001",
            kernel_id="krn-001",
            binary_id="bin-001",
            api_socket_path="/tmp/vm.sock",
            config_path="/tmp/vm.json",
            cloud_init_mode="disabled",
            vcpu_count=1,
            mem_size_mib=512,
            disk_size_mib=1000,
            rootfs_path="/tmp/rootfs.ext4",
            rootfs_suffix="ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=True,
            created_at=now,
            updated_at=now,
        )
        # Seed required FK records (image, kernel, binary)
        now = "2026-01-01T00:00:00Z"
        with repo.db.connect() as conn:
            conn.execute(
                "INSERT INTO images (id, type, name, arch, path, fs_type, "
                "original_size, minimum_rootfs_size_mib, pulled_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    "img-001",
                    "test",
                    "test-os",
                    "x86_64",
                    "/tmp/img",
                    "ext4",
                    1000,
                    500,
                    now,
                ),
            )
            conn.execute(
                "INSERT INTO kernels (id, name, base_name, version, arch, type, path) "
                "VALUES (?, ?, ?, ?, ?, ?, ?)",
                (
                    "krn-001",
                    "test",
                    "test",
                    "1.0",
                    "x86_64",
                    "vmlinux",
                    "/tmp/krn",
                ),
            )
            conn.execute(
                "INSERT INTO binaries (id, name, version, full_version, path) "
                "VALUES (?, ?, ?, ?, ?)",
                ("bin-001", "firecracker", "1.0", "1.0.0", "/tmp/bin"),
            )
            conn.execute(
                """
                INSERT INTO vm_instances (
                    id, name, status, pid, ipv4, mac, network_id, tap_device,
                    image_id, kernel_id, binary_id, api_socket_path, config_path,
                    cloud_init_mode, vcpu_count, mem_size_mib, disk_size_mib,
                    rootfs_path, rootfs_suffix, enable_pci,
                    enable_logging, enable_metrics, enable_console,
                    created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    vm.id,
                    vm.name,
                    vm.status,
                    vm.pid,
                    vm.ipv4,
                    vm.mac,
                    vm.network_id,
                    vm.tap_device,
                    vm.image_id,
                    vm.kernel_id,
                    vm.binary_id,
                    vm.api_socket_path,
                    vm.config_path,
                    vm.cloud_init_mode,
                    vm.vcpu_count,
                    vm.mem_size_mib,
                    vm.disk_size_mib,
                    vm.rootfs_path,
                    vm.rootfs_suffix,
                    int(vm.enable_pci),
                    int(vm.enable_logging),
                    int(vm.enable_metrics),
                    int(vm.enable_console),
                    vm.created_at,
                    vm.updated_at,
                ),
            )

        # Mock subprocess calls for NAT and bridge teardown
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch(
            "mvmctl.utils.network.NetworkUtils.get_bridge_taps", return_value=[]
        )
        mocker.patch.object(NetworkService, "remove_nat", autospec=True)
        mocker.patch.object(NetworkService, "remove_bridge", autospec=True)
        # The VM reference check comes from VMRepository.find_by_network_id,
        # which queries the database directly (no mock needed since we seeded the VM)

        # Populate seed_network.vms so the enrichment-like check in
        # NetworkService.remove() sees the referencing VMs.
        seed_network.vms = [vm]

        service = self._make_service(repo)
        with pytest.raises(NetworkError, match="referenced by VMs"):
            service.remove(seed_network, force=False)

    def test_remove_hard_deletes_when_no_vms(
        self, seed_network: NetworkItem, repo: NetworkRepository, mocker
    ) -> None:
        """remove() hard-deletes the network when no VMs reference it."""
        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch(
            "mvmctl.utils.network.NetworkUtils.get_bridge_taps", return_value=[]
        )
        mocker.patch.object(NetworkService, "remove_nat", autospec=True)
        mocker.patch.object(NetworkService, "remove_bridge", autospec=True)

        service = self._make_service(repo)
        service.remove(seed_network, force=False)
        assert repo.get("ctrl-net-001") is None

    def test_remove_force_with_vms(
        self, seed_network: NetworkItem, repo: NetworkRepository, mocker
    ) -> None:
        """remove(force=True) soft-deletes even when VMs reference it."""
        from mvmctl.models import VMInstanceItem

        now = "2026-01-01T00:00:00Z"
        vm = VMInstanceItem(
            id="vm-force-001",
            name="vm-force",
            status="stopped",
            pid=0,
            ipv4="10.0.0.2",
            mac="02:FC:00:00:00:01",
            network_id="ctrl-net-001",
            tap_device="tap-vm1",
            image_id="img-001",
            kernel_id="krn-001",
            binary_id="bin-001",
            api_socket_path="/tmp/vm.sock",
            config_path="/tmp/vm.json",
            cloud_init_mode="disabled",
            vcpu_count=1,
            mem_size_mib=512,
            disk_size_mib=1000,
            rootfs_path="/tmp/rootfs.ext4",
            rootfs_suffix="ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=True,
            created_at=now,
            updated_at=now,
        )
        # Seed required FK records (image, kernel, binary)
        now = "2026-01-01T00:00:00Z"
        with repo.db.connect() as conn:
            conn.execute(
                "INSERT INTO images (id, type, name, arch, path, fs_type, "
                "original_size, minimum_rootfs_size_mib, pulled_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    "img-001",
                    "test",
                    "test-os",
                    "x86_64",
                    "/tmp/img",
                    "ext4",
                    1000,
                    500,
                    now,
                ),
            )
            conn.execute(
                "INSERT INTO kernels (id, name, base_name, version, arch, type, path) "
                "VALUES (?, ?, ?, ?, ?, ?, ?)",
                (
                    "krn-001",
                    "test",
                    "test",
                    "1.0",
                    "x86_64",
                    "vmlinux",
                    "/tmp/krn",
                ),
            )
            conn.execute(
                "INSERT INTO binaries (id, name, version, full_version, path) "
                "VALUES (?, ?, ?, ?, ?)",
                ("bin-001", "firecracker", "1.0", "1.0.0", "/tmp/bin"),
            )
            conn.execute(
                """
                INSERT INTO vm_instances (
                    id, name, status, pid, ipv4, mac, network_id, tap_device,
                    image_id, kernel_id, binary_id, api_socket_path, config_path,
                    cloud_init_mode, vcpu_count, mem_size_mib, disk_size_mib,
                    rootfs_path, rootfs_suffix, enable_pci,
                    enable_logging, enable_metrics, enable_console,
                    created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    vm.id,
                    vm.name,
                    vm.status,
                    vm.pid,
                    vm.ipv4,
                    vm.mac,
                    vm.network_id,
                    vm.tap_device,
                    vm.image_id,
                    vm.kernel_id,
                    vm.binary_id,
                    vm.api_socket_path,
                    vm.config_path,
                    vm.cloud_init_mode,
                    vm.vcpu_count,
                    vm.mem_size_mib,
                    vm.disk_size_mib,
                    vm.rootfs_path,
                    vm.rootfs_suffix,
                    int(vm.enable_pci),
                    int(vm.enable_logging),
                    int(vm.enable_metrics),
                    int(vm.enable_console),
                    vm.created_at,
                    vm.updated_at,
                ),
            )

        # Populate seed_network.vms so the enrichment-like check in
        # NetworkService.remove() sees the referencing VMs.
        seed_network.vms = [vm]

        mocker.patch("subprocess.run", return_value=MagicMock(returncode=0))
        mocker.patch(
            "mvmctl.utils.network.NetworkUtils.get_bridge_taps", return_value=[]
        )
        mocker.patch.object(NetworkService, "remove_nat", autospec=True)
        mocker.patch.object(NetworkService, "remove_bridge", autospec=True)

        service = self._make_service(repo)
        service.remove(seed_network, force=True)
        # Network should be soft-deleted (still exists in DB with deleted_at)
        fetched = repo.get("ctrl-net-001")
        assert fetched is None  # get() filters out deleted

        # But it should still be in the table
        with repo.db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM networks WHERE id = ?", ("ctrl-net-001",)
            ).fetchone()
        assert row is not None
        assert row["deleted_at"] is not None
