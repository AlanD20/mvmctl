"""Tests for MVMDatabase VM state, network, and network lease operations.

Comprehensive test suite covering:
- VM state CRUD operations and targeted updates
- Network CRUD operations and default management
- Network lease operations (acquire, release, VM lease cleanup)
- Foreign key constraint validation
- Unique constraint validation for leases
"""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Network, VMState


def make_vm(name: str = "testvm", status: str = "STOPPED") -> VMState:
    """Create a minimal VMState for testing."""
    return VMState(id="a" * 64, name=name, status=status)


def make_network(name: str = "default") -> Network:
    """Create a minimal Network for testing."""
    return Network(
        id="b" * 64,
        name=name,
        subnet="172.35.0.0/24",
        bridge="mvm-default",
        ipv4_gateway="172.35.0.1",
    )


@pytest.fixture
def db(tmp_path: Path) -> MVMDatabase:
    """Create a database with migrations applied.

    Args:
        tmp_path: Pytest temporary directory fixture.

    Returns:
        MVMDatabase instance with schema initialized.
    """
    db_instance = MVMDatabase(db_path=tmp_path / "test.db")
    db_instance.migrate()
    return db_instance


class TestVMStateOperations:
    """Tests for VM state CRUD operations."""

    def test_get_vm_found(self, db: MVMDatabase) -> None:
        """Test retrieving an existing VM by full ID."""
        vm = make_vm(name="myvm", status="RUNNING")
        db.upsert_vm(vm)

        retrieved = db.get_vm("a" * 64)
        assert retrieved is not None
        assert retrieved.id == "a" * 64
        assert retrieved.name == "myvm"
        assert retrieved.status == "RUNNING"

    def test_get_vm_not_found(self, db: MVMDatabase) -> None:
        """Test that get_vm returns None for non-existent ID."""
        result = db.get_vm("nonexistent" + "a" * 53)
        assert result is None

    def test_get_vm_by_name_found(self, db: MVMDatabase) -> None:
        """Test retrieving a VM by name."""
        vm = make_vm(name="myvm", status="STOPPED")
        db.upsert_vm(vm)

        retrieved = db.get_vm_by_name("myvm")
        assert retrieved is not None
        assert retrieved.id == "a" * 64
        assert retrieved.name == "myvm"

    def test_get_vm_by_name_not_found(self, db: MVMDatabase) -> None:
        """Test that get_vm_by_name returns None for non-existent name."""
        result = db.get_vm_by_name("nonexistent")
        assert result is None

    def test_upsert_vm_insert(self, db: MVMDatabase) -> None:
        """Test inserting a new VM record."""
        vm = make_vm(name="newvm", status="CREATING")
        db.upsert_vm(vm)

        retrieved = db.get_vm("a" * 64)
        assert retrieved is not None
        assert retrieved.name == "newvm"
        assert retrieved.status == "CREATING"

    def test_upsert_vm_update(self, db: MVMDatabase) -> None:
        """Test updating an existing VM (ON CONFLICT)."""
        vm1 = make_vm(name="myvm", status="STOPPED")
        db.upsert_vm(vm1)

        vm2 = VMState(
            id="a" * 64,
            name="myvm",
            status="RUNNING",
            pid=1234,
            ipv4="172.35.0.10",
        )
        db.upsert_vm(vm2)

        retrieved = db.get_vm("a" * 64)
        assert retrieved is not None
        assert retrieved.status == "RUNNING"
        assert retrieved.pid == 1234
        assert retrieved.ipv4 == "172.35.0.10"

    def test_delete_vm_found(self, db: MVMDatabase) -> None:
        """Test deleting an existing VM."""
        vm = make_vm(name="myvm")
        db.upsert_vm(vm)
        assert db.get_vm("a" * 64) is not None

        db.delete_vm("a" * 64)
        assert db.get_vm("a" * 64) is None

    def test_delete_vm_not_found(self, db: MVMDatabase) -> None:
        """Test that deleting non-existent VM is a no-op."""
        db.delete_vm("nonexistent" + "a" * 53)
        assert db.get_vm("nonexistent" + "a" * 53) is None

    def test_list_vms_empty(self, db: MVMDatabase) -> None:
        """Test listing VMs when none exist."""
        vms = db.list_vms()
        assert vms == []

    def test_list_vms_multiple(self, db: MVMDatabase) -> None:
        """Test listing multiple VMs ordered by created_at."""
        vm1 = VMState(
            id="a" * 64,
            name="vm1",
            status="STOPPED",
            created_at="2026-04-01T10:00:00Z",
        )
        vm2 = VMState(
            id="b" * 64,
            name="vm2",
            status="RUNNING",
            created_at="2026-04-02T10:00:00Z",
        )
        db.upsert_vm(vm1)
        db.upsert_vm(vm2)

        vms = db.list_vms()
        assert len(vms) == 2
        assert vms[0].id == "a" * 64
        assert vms[1].id == "b" * 64

    def test_find_vms_by_prefix_exact(self, db: MVMDatabase) -> None:
        """Test finding VM by exact prefix match."""
        vm = VMState(
            id="abc123" + "d" * 58,
            name="myvm",
            status="STOPPED",
        )
        db.upsert_vm(vm)

        results = db.find_vms_by_prefix("abc123")
        assert len(results) == 1
        assert results[0].id == "abc123" + "d" * 58

    def test_find_vms_by_prefix_multiple(self, db: MVMDatabase) -> None:
        """Test finding multiple VMs with same prefix."""
        vm1 = VMState(
            id="abc000" + "d" * 58,
            name="vm1",
            status="STOPPED",
        )
        vm2 = VMState(
            id="abc111" + "d" * 58,
            name="vm2",
            status="RUNNING",
        )
        db.upsert_vm(vm1)
        db.upsert_vm(vm2)

        results = db.find_vms_by_prefix("abc")
        assert len(results) == 2

    def test_find_vms_by_prefix_no_match(self, db: MVMDatabase) -> None:
        """Test finding VMs with non-matching prefix."""
        vm = VMState(
            id="xyz" + "a" * 61,
            name="myvm",
            status="STOPPED",
        )
        db.upsert_vm(vm)

        results = db.find_vms_by_prefix("abc")
        assert results == []

    def test_update_vm_status(self, db: MVMDatabase) -> None:
        """Test targeted update of VM status field."""
        vm = make_vm(name="myvm", status="STOPPED")
        db.upsert_vm(vm)

        db.update_vm_status("a" * 64, "RUNNING")

        retrieved = db.get_vm("a" * 64)
        assert retrieved is not None
        assert retrieved.status == "RUNNING"
        # Other fields unchanged
        assert retrieved.name == "myvm"

    def test_update_vm_pid(self, db: MVMDatabase) -> None:
        """Test targeted update of VM PID field."""
        vm = make_vm(name="myvm", status="RUNNING")
        vm.pid = 1000
        db.upsert_vm(vm)

        db.update_vm_pid("a" * 64, 5678)

        retrieved = db.get_vm("a" * 64)
        assert retrieved is not None
        assert retrieved.pid == 5678

    def test_update_vm_pid_to_none(self, db: MVMDatabase) -> None:
        """Test setting VM PID to None."""
        vm = make_vm(name="myvm", status="RUNNING")
        vm.pid = 1000
        db.upsert_vm(vm)

        db.update_vm_pid("a" * 64, None)

        retrieved = db.get_vm("a" * 64)
        assert retrieved is not None
        assert retrieved.pid is None

    def test_vm_with_all_optional_fields(self, db: MVMDatabase) -> None:
        """Test that upsert/get preserves all optional VM fields."""
        # Create referenced records first (FK constraints)
        network = Network(
            id="d" * 64,
            name="testnet",
            subnet="10.0.0.0/24",
            bridge="mvm-test",
            ipv4_gateway="10.0.0.1",
        )
        db.upsert_network(network)

        vm = VMState(
            id="c" * 64,
            name="fullvm",
            status="RUNNING",
            pid=1234,
            ipv4="172.35.0.10",
            mac="aa:bb:cc:dd:ee:ff",
            network_id="d" * 64,
            tap_device="mvm-def-ful-123",
            api_socket_path="/tmp/vm.sock",
            console_socket_path="/tmp/console.sock",
            config_path="/tmp/config.json",
            cloud_init_mode="nocloud-net",
            nocloud_net_port=8080,
            nocloud_server_pid=5678,
            console_relay_pid=5679,
            exit_code=0,
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            rootfs_path="/cache/vms/fullvm/rootfs.ext4",
            rootfs_suffix="ext4",
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        db.upsert_vm(vm)

        retrieved = db.get_vm("c" * 64)
        assert retrieved is not None
        assert retrieved.pid == 1234
        assert retrieved.ipv4 == "172.35.0.10"
        assert retrieved.mac == "aa:bb:cc:dd:ee:ff"
        assert retrieved.network_id == "d" * 64
        assert retrieved.tap_device == "mvm-def-ful-123"
        assert retrieved.api_socket_path == "/tmp/vm.sock"
        assert retrieved.console_socket_path == "/tmp/console.sock"
        assert retrieved.config_path == "/tmp/config.json"
        assert retrieved.cloud_init_mode == "nocloud-net"
        assert retrieved.nocloud_net_port == 8080
        assert retrieved.nocloud_server_pid == 5678
        assert retrieved.console_relay_pid == 5679
        assert retrieved.exit_code == 0
        assert retrieved.vcpu_count == 2
        assert retrieved.mem_size_mib == 512
        assert retrieved.disk_size_mib == 1024
        assert retrieved.rootfs_path == "/cache/vms/fullvm/rootfs.ext4"
        assert retrieved.rootfs_suffix == "ext4"


class TestNetworkOperations:
    """Tests for network CRUD operations."""

    def test_get_network_found(self, db: MVMDatabase) -> None:
        """Test retrieving an existing network by full ID."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        retrieved = db.get_network("b" * 64)
        assert retrieved is not None
        assert retrieved.id == "b" * 64
        assert retrieved.name == "mynet"
        assert retrieved.subnet == "172.35.0.0/24"

    def test_get_network_not_found(self, db: MVMDatabase) -> None:
        """Test that get_network returns None for non-existent ID."""
        result = db.get_network("nonexistent" + "b" * 53)
        assert result is None

    def test_get_network_by_name_found(self, db: MVMDatabase) -> None:
        """Test retrieving a network by name."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        retrieved = db.get_network_by_name("mynet")
        assert retrieved is not None
        assert retrieved.id == "b" * 64
        assert retrieved.name == "mynet"

    def test_get_network_by_name_not_found(self, db: MVMDatabase) -> None:
        """Test that get_network_by_name returns None for non-existent name."""
        result = db.get_network_by_name("nonexistent")
        assert result is None

    def test_upsert_network_insert(self, db: MVMDatabase) -> None:
        """Test inserting a new network record."""
        network = make_network(name="newnet")
        db.upsert_network(network)

        retrieved = db.get_network("b" * 64)
        assert retrieved is not None
        assert retrieved.name == "newnet"

    def test_upsert_network_update(self, db: MVMDatabase) -> None:
        """Test updating an existing network (ON CONFLICT)."""
        network1 = make_network(name="mynet")
        db.upsert_network(network1)

        network2 = Network(
            id="b" * 64,
            name="mynet",
            subnet="10.0.0.0/16",
            bridge="mvm-custom",
            ipv4_gateway="10.0.0.1",
            bridge_active=True,
        )
        db.upsert_network(network2)

        retrieved = db.get_network("b" * 64)
        assert retrieved is not None
        assert retrieved.subnet == "10.0.0.0/16"
        assert retrieved.bridge == "mvm-custom"
        assert retrieved.ipv4_gateway == "10.0.0.1"
        assert bool(retrieved.bridge_active) is True

    def test_delete_network_found(self, db: MVMDatabase) -> None:
        """Test deleting an existing network."""
        network = make_network(name="mynet")
        db.upsert_network(network)
        assert db.get_network("b" * 64) is not None

        db.delete_network("b" * 64)
        assert db.get_network("b" * 64) is None

    def test_delete_network_not_found(self, db: MVMDatabase) -> None:
        """Test that deleting non-existent network is a no-op."""
        db.delete_network("nonexistent" + "b" * 53)
        assert db.get_network("nonexistent" + "b" * 53) is None

    def test_list_networks_empty(self, db: MVMDatabase) -> None:
        """Test listing networks when none exist."""
        networks = db.list_networks()
        assert networks == []

    def test_list_networks_multiple(self, db: MVMDatabase) -> None:
        """Test listing multiple networks ordered by created_at."""
        network1 = Network(
            id="b" * 64,
            name="net1",
            subnet="172.35.0.0/24",
            bridge="mvm-net1",
            ipv4_gateway="172.35.0.1",
            created_at="2026-04-01T10:00:00Z",
        )
        network2 = Network(
            id="c" * 64,
            name="net2",
            subnet="10.0.0.0/16",
            bridge="mvm-net2",
            ipv4_gateway="10.0.0.1",
            created_at="2026-04-02T10:00:00Z",
        )
        db.upsert_network(network1)
        db.upsert_network(network2)

        networks = db.list_networks()
        assert len(networks) == 2
        assert networks[0].id == "b" * 64
        assert networks[1].id == "c" * 64

    def test_find_networks_by_prefix_exact(self, db: MVMDatabase) -> None:
        """Test finding network by exact prefix match."""
        network = Network(
            id="abc123" + "d" * 58,
            name="mynet",
            subnet="172.35.0.0/24",
            bridge="mvm-net",
            ipv4_gateway="172.35.0.1",
        )
        db.upsert_network(network)

        results = db.find_networks_by_prefix("abc123")
        assert len(results) == 1
        assert results[0].id == "abc123" + "d" * 58

    def test_find_networks_by_prefix_multiple(self, db: MVMDatabase) -> None:
        """Test finding multiple networks with same prefix."""
        network1 = Network(
            id="abc000" + "d" * 58,
            name="net1",
            subnet="172.35.0.0/24",
            bridge="mvm-net1",
            ipv4_gateway="172.35.0.1",
        )
        network2 = Network(
            id="abc111" + "d" * 58,
            name="net2",
            subnet="10.0.0.0/16",
            bridge="mvm-net2",
            ipv4_gateway="10.0.0.1",
        )
        db.upsert_network(network1)
        db.upsert_network(network2)

        results = db.find_networks_by_prefix("abc")
        assert len(results) == 2

    def test_find_networks_by_prefix_no_match(self, db: MVMDatabase) -> None:
        """Test finding networks with non-matching prefix."""
        network = Network(
            id="xyz" + "a" * 61,
            name="mynet",
            subnet="172.35.0.0/24",
            bridge="mvm-net",
            ipv4_gateway="172.35.0.1",
        )
        db.upsert_network(network)

        results = db.find_networks_by_prefix("abc")
        assert results == []

    def test_update_network_bridge_active(self, db: MVMDatabase) -> None:
        """Test targeted update of bridge_active field."""
        network = make_network(name="mynet")
        network.bridge_active = False
        db.upsert_network(network)

        db.update_network_bridge_active("b" * 64, True)

        retrieved = db.get_network("b" * 64)
        assert retrieved is not None
        assert bool(retrieved.bridge_active) is True

    def test_set_default_network_single(self, db: MVMDatabase) -> None:
        """Test setting one network as default."""
        network = make_network(name="mynet")
        network.is_default = False
        db.upsert_network(network)

        db.set_default_network("b" * 64)

        retrieved = db.get_network("b" * 64)
        assert retrieved is not None
        assert bool(retrieved.is_default) is True

    def test_set_default_network_clears_others(self, db: MVMDatabase) -> None:
        """Test that set_default_network ensures only one is_default=True."""
        network1 = Network(
            id="b" * 64,
            name="net1",
            subnet="172.35.0.0/24",
            bridge="mvm-net1",
            ipv4_gateway="172.35.0.1",
            is_default=True,
        )
        network2 = Network(
            id="c" * 64,
            name="net2",
            subnet="10.0.0.0/16",
            bridge="mvm-net2",
            ipv4_gateway="10.0.0.1",
            is_default=False,
        )
        db.upsert_network(network1)
        db.upsert_network(network2)

        db.set_default_network("c" * 64)

        net1 = db.get_network("b" * 64)
        net2 = db.get_network("c" * 64)
        assert net1 is not None
        assert net2 is not None
        assert bool(net1.is_default) is False
        assert bool(net2.is_default) is True

    def test_set_default_network_idempotent(self, db: MVMDatabase) -> None:
        """Test that calling set_default_network twice is safe."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        db.set_default_network("b" * 64)
        db.set_default_network("b" * 64)

        retrieved = db.get_network("b" * 64)
        assert retrieved is not None
        assert bool(retrieved.is_default) is True

    def test_network_with_all_optional_fields(self, db: MVMDatabase) -> None:
        """Test that upsert/get preserves all optional network fields."""
        network = Network(
            id="d" * 64,
            name="fullnet",
            subnet="192.168.0.0/24",
            bridge="mvm-fullnet",
            ipv4_gateway="192.168.0.1",
            bridge_active=True,
            nat_gateways="eth0,eth1",
            nat_enabled=True,
            is_default=True,
            created_at="2026-04-02T10:00:00Z",
            updated_at="2026-04-02T10:00:00Z",
        )
        db.upsert_network(network)

        retrieved = db.get_network("d" * 64)
        assert retrieved is not None
        assert bool(retrieved.bridge_active) is True
        assert retrieved.nat_gateways == "eth0,eth1"
        assert bool(retrieved.nat_enabled) is True
        assert bool(retrieved.is_default) is True


class TestNetworkLeaseOperations:
    """Tests for network lease operations."""

    def test_acquire_lease_success(self, db: MVMDatabase) -> None:
        """Test successfully acquiring an IP lease."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        # Create a VM first (FK constraint on vm_id)
        vm = make_vm(name="testvm")
        vm.id = "a" * 64
        db.upsert_vm(vm)

        lease = db.acquire_lease("b" * 64, "172.35.0.10", vm_id="a" * 64)

        assert lease is not None
        assert lease.network_id == "b" * 64
        assert lease.ipv4 == "172.35.0.10"
        assert lease.vm_id == "a" * 64

    def test_acquire_lease_duplicate_raises_integrity_error(self, db: MVMDatabase) -> None:
        """Test that acquiring duplicate lease raises IntegrityError."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        # Create a VM first (FK constraint on vm_id)
        vm = make_vm(name="testvm")
        vm.id = "a" * 64
        db.upsert_vm(vm)

        db.acquire_lease("b" * 64, "172.35.0.10", vm_id="a" * 64)

        with pytest.raises(sqlite3.IntegrityError):
            db.acquire_lease("b" * 64, "172.35.0.10", vm_id="b" * 64)

    def test_get_lease_found(self, db: MVMDatabase) -> None:
        """Test retrieving a lease by network_id + ipv4."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        # Create a VM first (FK constraint on vm_id)
        vm = make_vm(name="testvm")
        vm.id = "a" * 64
        db.upsert_vm(vm)

        db.acquire_lease("b" * 64, "172.35.0.10", vm_id="a" * 64)

        retrieved = db.get_lease("b" * 64, "172.35.0.10")

        assert retrieved is not None
        assert retrieved.network_id == "b" * 64
        assert retrieved.ipv4 == "172.35.0.10"
        assert retrieved.vm_id == "a" * 64

    def test_get_lease_not_found(self, db: MVMDatabase) -> None:
        """Test that get_lease returns None for non-existent lease."""
        result = db.get_lease("b" * 64, "172.35.0.99")
        assert result is None

    def test_list_leases_empty(self, db: MVMDatabase) -> None:
        """Test listing leases when none exist for a network."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        leases = db.list_leases("b" * 64)
        assert leases == []

    def test_list_leases_multiple(self, db: MVMDatabase) -> None:
        """Test listing multiple leases for a network."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        # Create VMs first (FK constraint on vm_id)
        vm1 = make_vm(name="vm1")
        vm1.id = "a" * 64
        vm2 = make_vm(name="vm2")
        vm2.id = "c" * 64
        db.upsert_vm(vm1)
        db.upsert_vm(vm2)

        db.acquire_lease("b" * 64, "172.35.0.10", vm_id="a" * 64)
        db.acquire_lease("b" * 64, "172.35.0.11", vm_id="c" * 64)

        leases = db.list_leases("b" * 64)
        assert len(leases) == 2
        ips = {lease.ipv4 for lease in leases}
        assert ips == {"172.35.0.10", "172.35.0.11"}

    def test_release_lease_found(self, db: MVMDatabase) -> None:
        """Test releasing an existing lease."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        # Create a VM first (FK constraint on vm_id)
        vm = make_vm(name="testvm")
        vm.id = "a" * 64
        db.upsert_vm(vm)

        db.acquire_lease("b" * 64, "172.35.0.10", vm_id="a" * 64)
        assert db.get_lease("b" * 64, "172.35.0.10") is not None

        db.release_lease("b" * 64, "172.35.0.10")

        assert db.get_lease("b" * 64, "172.35.0.10") is None

    def test_release_lease_not_found(self, db: MVMDatabase) -> None:
        """Test that releasing non-existent lease is a no-op."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        db.release_lease("b" * 64, "172.35.0.99")
        # Should not raise

    def test_release_vm_leases(self, db: MVMDatabase) -> None:
        """Test releasing all leases held by a VM."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        # Create VMs first (FK constraint on vm_id)
        vm_a = make_vm(name="vma")
        vm_a.id = "a" * 64
        vm_c = make_vm(name="vmc")
        vm_c.id = "c" * 64
        db.upsert_vm(vm_a)
        db.upsert_vm(vm_c)

        db.acquire_lease("b" * 64, "172.35.0.10", vm_id="a" * 64)
        db.acquire_lease("b" * 64, "172.35.0.11", vm_id="a" * 64)
        db.acquire_lease("b" * 64, "172.35.0.12", vm_id="c" * 64)

        db.release_vm_leases("a" * 64)

        assert db.get_lease("b" * 64, "172.35.0.10") is None
        assert db.get_lease("b" * 64, "172.35.0.11") is None
        assert db.get_lease("b" * 64, "172.35.0.12") is not None


class TestForeignKeyConstraints:
    """Tests for foreign key constraint enforcement."""

    def test_upsert_vm_with_nonexistent_network_id_raises_integrity_error(
        self, db: MVMDatabase
    ) -> None:
        """Test that upsert_vm with non-existent network_id raises IntegrityError."""
        vm = VMState(
            id="a" * 64,
            name="myvm",
            status="RUNNING",
            network_id="nonexistent" + "b" * 53,
        )

        with pytest.raises(sqlite3.IntegrityError):
            db.upsert_vm(vm)

    def test_upsert_vm_with_valid_network_id_succeeds(self, db: MVMDatabase) -> None:
        """Test that upsert_vm with valid network_id succeeds."""
        network = make_network(name="mynet")
        db.upsert_network(network)

        vm = VMState(
            id="a" * 64,
            name="myvm",
            status="RUNNING",
            network_id="b" * 64,
        )
        db.upsert_vm(vm)

        retrieved = db.get_vm("a" * 64)
        assert retrieved is not None
        assert retrieved.network_id == "b" * 64

    def test_acquire_lease_with_nonexistent_network_raises_integrity_error(
        self, db: MVMDatabase
    ) -> None:
        """Test that acquire_lease with non-existent network_id raises IntegrityError."""
        with pytest.raises(sqlite3.IntegrityError):
            db.acquire_lease("nonexistent" + "b" * 53, "172.35.0.10")


class TestEdgeCases:
    """Tests for edge cases and constraints."""

    def test_set_default_network_nonexistent(self, db: MVMDatabase) -> None:
        """Test setting non-existent network as default (no error)."""
        # Should not raise an exception
        db.set_default_network("nonexistent" + "b" * 53)

    def test_multiple_network_defaults_cleared_atomically(self, db: MVMDatabase) -> None:
        """Test that set_default_network operations are atomic."""
        network1 = Network(
            id="b" * 64,
            name="net1",
            subnet="172.35.0.0/24",
            bridge="mvm-net1",
            ipv4_gateway="172.35.0.1",
            is_default=True,
        )
        network2 = Network(
            id="c" * 64,
            name="net2",
            subnet="10.0.0.0/16",
            bridge="mvm-net2",
            ipv4_gateway="10.0.0.1",
            is_default=False,
        )
        network3 = Network(
            id="d" * 64,
            name="net3",
            subnet="192.168.0.0/24",
            bridge="mvm-net3",
            ipv4_gateway="192.168.0.1",
            is_default=False,
        )
        db.upsert_network(network1)
        db.upsert_network(network2)
        db.upsert_network(network3)

        db.set_default_network("d" * 64)

        net1 = db.get_network("b" * 64)
        net2 = db.get_network("c" * 64)
        net3 = db.get_network("d" * 64)
        assert net1 is not None
        assert net2 is not None
        assert net3 is not None
        assert bool(net1.is_default) is False
        assert bool(net2.is_default) is False
        assert bool(net3.is_default) is True

    def test_find_by_prefix_case_insensitive(self, db: MVMDatabase) -> None:
        """Test that prefix search is case-insensitive (SQLite LIKE default)."""
        vm = VMState(
            id="ABC123" + "d" * 58,
            name="myvm",
            status="STOPPED",
        )
        db.upsert_vm(vm)

        # SQLite LIKE is case-insensitive by default
        results = db.find_vms_by_prefix("abc123")
        assert len(results) == 1
        assert results[0].id == "ABC123" + "d" * 58

        # Uppercase should also match
        results = db.find_vms_by_prefix("ABC123")
        assert len(results) == 1


class TestFindVmByName:
    def test_returns_vm_when_found(self, db: MVMDatabase) -> None:
        db.upsert_vm(make_vm(name="myvm"))
        result = db.find_vm_by_name("myvm")
        assert result is not None
        assert result.name == "myvm"

    def test_returns_none_when_not_found(self, db: MVMDatabase) -> None:
        assert db.find_vm_by_name("ghost") is None


class TestFindVmByIp:
    def test_returns_vm_when_ip_matches(self, db: MVMDatabase) -> None:
        vm = VMState(id="c" * 64, name="ipvm", status="RUNNING", ipv4="10.0.0.5")
        db.upsert_vm(vm)
        result = db.find_vm_by_ip("10.0.0.5")
        assert result is not None
        assert result.name == "ipvm"

    def test_returns_none_when_ip_not_found(self, db: MVMDatabase) -> None:
        assert db.find_vm_by_ip("1.2.3.4") is None


class TestSetDefaultBinary:
    def test_inserts_new_default(self, db: MVMDatabase) -> None:
        db.set_default_binary("firecracker", "1.15.0", "/cache/bin/firecracker")
        result = db.get_binary_default("firecracker")
        assert result is not None
        assert result.version == "1.15.0"
        assert result.path == "/cache/bin/firecracker"

    def test_upserts_existing_default(self, db: MVMDatabase) -> None:
        db.set_default_binary("firecracker", "1.15.0", "/cache/bin/fc-1.15")
        db.set_default_binary("firecracker", "1.16.0", "/cache/bin/fc-1.16")
        result = db.get_binary_default("firecracker")
        assert result is not None
        assert result.version == "1.16.0"

    def test_different_names_are_independent(self, db: MVMDatabase) -> None:
        db.set_default_binary("firecracker", "1.15.0", "/bin/fc")
        db.set_default_binary("jailer", "1.15.0", "/bin/jailer")
        assert db.get_binary_default("firecracker") is not None
        assert db.get_binary_default("jailer") is not None
