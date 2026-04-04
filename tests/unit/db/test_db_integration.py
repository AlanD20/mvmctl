"""Database integration tests for cross-table FK constraints and workflows.

Tests verify:
1. FK Constraint Tests (RESTRICT) — 5 tests
   - Delete image/kernel/binary/network referenced by vm_state → IntegrityError
   - Delete network with active leases → IntegrityError

2. FK Constraint Tests (CASCADE) — 2 tests
   - Delete network → leases auto-deleted
   - Delete vm → leases auto-deleted

3. Multi-Table Workflow Tests — 5+ tests
   - Full VM lifecycle workflow
   - Network lease workflow
   - Default tracking for images/kernels/networks

4. Schema Integrity Tests — 10+ tests
   - All 10 tables exist
   - PRAGMA user_version = 1
   - All UNIQUE constraints
   - All CHECK constraints (IPv4, MAC validation)
"""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Binary, Image, Kernel, Network, VMInstance

# Full 64-char hash IDs for testing
IMAGE_ID = "i" * 64
KERNEL_ID = "k" * 64
BINARY_ID = "b" * 64
NETWORK_ID = "n" * 64
VM_ID = "v" * 64


def make_image(image_id: str = IMAGE_ID) -> Image:
    """Create a test Image."""
    return Image(
        id=image_id,
        os_slug="test-image",
        path="/cache/images/test.img",
    )


def make_kernel(kernel_id: str = KERNEL_ID) -> Kernel:
    """Create a test Kernel."""
    return Kernel(
        id=kernel_id,
        name="vmlinux-test",
        version="6.1.0",
        arch="x86_64",
        path="/cache/kernels/vmlinux",
    )


def make_binary(binary_id: str = BINARY_ID) -> Binary:
    """Create a test Binary."""
    return Binary(
        id=binary_id,
        name="firecracker",
        version="1.15.0",
        path="/cache/bin/fc",
    )


def make_network(network_id: str = NETWORK_ID) -> Network:
    """Create a test Network."""
    return Network(
        id=network_id,
        name="default",
        subnet="172.35.0.0/24",
        bridge="mvm-default",
        ipv4_gateway="172.35.0.1",
    )


def make_vm(
    vm_id: str = VM_ID,
    image_id: str = IMAGE_ID,
    kernel_id: str = KERNEL_ID,
    binary_id: str = BINARY_ID,
    network_id: str = NETWORK_ID,
    name: str = "testvm",
) -> VMInstance:
    """Create a test VMState."""
    return VMInstance(
        id=vm_id,
        name=name,
        status="STOPPED",
        image_id=image_id,
        kernel_id=kernel_id,
        binary_id=binary_id,
        network_id=network_id,
    )


@pytest.fixture
def db(tmp_path: Path) -> MVMDatabase:
    """Create and migrate a test database."""
    d = MVMDatabase(db_path=tmp_path / "test.db")
    d.migrate()
    return d


class TestFKConstraintsRestrict:
    """Test RESTRICT foreign key constraints (5 tests)."""

    def test_delete_image_referenced_by_vm_raises_integrity_error(self, db: MVMDatabase) -> None:
        """Verify deleting an image referenced by a VM raises IntegrityError."""
        # Setup: create image, kernel, binary, network, and VM
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        network = make_network()
        vm = make_vm()

        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_network(network)
        db.upsert_vm(vm)

        # Attempt to delete the image — should fail due to FK constraint
        with pytest.raises(sqlite3.IntegrityError):
            db.delete_image(image.id)

    def test_delete_kernel_referenced_by_vm_raises_integrity_error(self, db: MVMDatabase) -> None:
        """Verify deleting a kernel referenced by a VM raises IntegrityError."""
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        network = make_network()
        vm = make_vm()

        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_network(network)
        db.upsert_vm(vm)

        # Attempt to delete the kernel — should fail due to FK constraint
        with pytest.raises(sqlite3.IntegrityError):
            db.delete_kernel(kernel.id)

    def test_delete_binary_referenced_by_vm_raises_integrity_error(self, db: MVMDatabase) -> None:
        """Verify deleting a binary referenced by a VM raises IntegrityError."""
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        network = make_network()
        vm = make_vm()

        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_network(network)
        db.upsert_vm(vm)

        # Attempt to delete the binary — should fail due to FK constraint
        with pytest.raises(sqlite3.IntegrityError):
            db.delete_binary(binary.id)

    def test_delete_network_referenced_by_vm_raises_integrity_error(self, db: MVMDatabase) -> None:
        """Verify deleting a network referenced by a VM raises IntegrityError."""
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        network = make_network()
        vm = make_vm()

        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_network(network)
        db.upsert_vm(vm)

        # Attempt to delete the network — should fail due to FK constraint
        with pytest.raises(sqlite3.IntegrityError):
            db.delete_network(network.id)

    def test_delete_network_with_active_leases_raises_integrity_error(
        self, db: MVMDatabase
    ) -> None:
        """Verify deleting a network with active leases raises IntegrityError.

        Note: Leases have CASCADE delete to network, but VM has RESTRICT.
        This test verifies the RESTRICT constraint on vm_states.network_id.
        """
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        network = make_network()
        vm = make_vm()

        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_network(network)
        db.upsert_vm(vm)

        # Create a lease for the VM on the network
        db.acquire_lease(network.id, "172.35.0.10", vm.id)

        # Attempt to delete the network — should fail due to VM FK constraint
        with pytest.raises(sqlite3.IntegrityError):
            db.delete_network(network.id)


class TestFKConstraintsCascade:
    """Test CASCADE foreign key constraints (2 tests)."""

    def test_delete_network_cascades_to_leases(self, db: MVMDatabase) -> None:
        """Verify deleting a network auto-deletes its leases."""
        network = make_network()
        db.upsert_network(network)

        # Create leases without VM references (no RESTRICT constraint)
        db.acquire_lease(network.id, "172.35.0.10")
        db.acquire_lease(network.id, "172.35.0.11")

        # Verify leases exist
        assert db.get_lease(network.id, "172.35.0.10") is not None
        assert db.get_lease(network.id, "172.35.0.11") is not None

        # Delete the network
        db.delete_network(network.id)

        # Verify leases are auto-deleted
        assert db.get_lease(network.id, "172.35.0.10") is None
        assert db.get_lease(network.id, "172.35.0.11") is None

    def test_delete_vm_cascades_to_leases(self, db: MVMDatabase) -> None:
        """Verify deleting a VM auto-deletes its leases."""
        network = make_network()
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        vm = make_vm()

        db.upsert_network(network)
        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_vm(vm)

        # Create leases for the VM
        db.acquire_lease(network.id, "172.35.0.10", vm.id)
        db.acquire_lease(network.id, "172.35.0.11", vm.id)

        # Verify leases exist
        assert db.get_lease(network.id, "172.35.0.10") is not None
        assert db.get_lease(network.id, "172.35.0.11") is not None

        # Delete the VM
        db.delete_vm(vm.id)

        # Verify leases are auto-deleted
        assert db.get_lease(network.id, "172.35.0.10") is None
        assert db.get_lease(network.id, "172.35.0.11") is None


class TestMultiTableWorkflows:
    """Test complete workflows involving multiple tables (5+ tests)."""

    def test_full_vm_lifecycle_workflow(self, db: MVMDatabase) -> None:
        """Test complete VM lifecycle: create network → image → kernel → binary → VM."""
        # Create all dependencies
        network = make_network()
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        vm = make_vm()

        # Insert in dependency order
        db.upsert_network(network)
        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_vm(vm)

        # Verify all records exist
        assert db.get_network(network.id) is not None
        assert db.get_image(image.id) is not None
        assert db.get_kernel(kernel.id) is not None
        assert db.get_binary(binary.id) is not None
        assert db.get_vm(vm.id) is not None

        # Verify VM references are correct
        retrieved_vm = db.get_vm(vm.id)
        assert retrieved_vm is not None
        assert retrieved_vm.image_id == image.id
        assert retrieved_vm.kernel_id == kernel.id
        assert retrieved_vm.binary_id == binary.id
        assert retrieved_vm.network_id == network.id

        # Delete VM (should succeed, cascading to leases)
        db.delete_vm(vm.id)
        assert db.get_vm(vm.id) is None

        # Verify other records still exist
        assert db.get_network(network.id) is not None
        assert db.get_image(image.id) is not None
        assert db.get_kernel(kernel.id) is not None
        assert db.get_binary(binary.id) is not None

    def test_network_lease_workflow(self, db: MVMDatabase) -> None:
        """Test network lease workflow: create network → acquire lease with VM."""
        network = make_network()
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        vm = make_vm()

        db.upsert_network(network)
        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_vm(vm)

        # Acquire lease for VM
        lease = db.acquire_lease(network.id, "172.35.0.10", vm.id)
        assert lease.network_id == network.id
        assert lease.ipv4 == "172.35.0.10"
        assert lease.vm_id == vm.id

        # Verify lease can be retrieved
        retrieved_lease = db.get_lease(network.id, "172.35.0.10")
        assert retrieved_lease is not None
        assert retrieved_lease.vm_id == vm.id

        # List leases for network
        leases = db.list_leases(network.id)
        assert len(leases) == 1
        assert leases[0].ipv4 == "172.35.0.10"

        # Delete VM — lease should cascade delete
        db.delete_vm(vm.id)
        assert db.get_lease(network.id, "172.35.0.10") is None

    def test_image_default_tracking(self, db: MVMDatabase) -> None:
        """Test that only one image can be marked as default."""
        image1 = make_image(image_id="i" * 64)
        image2 = make_image(image_id="j" * 64)
        image2.os_slug = "test-image-2"

        db.upsert_image(image1)
        db.upsert_image(image2)

        # Set image1 as default
        db.set_default_image(image1.id)
        retrieved_image1 = db.get_image(image1.id)
        retrieved_image2 = db.get_image(image2.id)
        assert retrieved_image1 is not None
        assert retrieved_image2 is not None
        assert retrieved_image1.is_default == 1
        assert retrieved_image2.is_default == 0

        # Set image2 as default — image1 should be cleared
        db.set_default_image(image2.id)
        retrieved_image1 = db.get_image(image1.id)
        retrieved_image2 = db.get_image(image2.id)
        assert retrieved_image1 is not None
        assert retrieved_image2 is not None
        assert retrieved_image1.is_default == 0
        assert retrieved_image2.is_default == 1

    def test_kernel_default_tracking(self, db: MVMDatabase) -> None:
        """Test that only one kernel can be marked as default."""
        kernel1 = make_kernel(kernel_id="k" * 64)
        kernel2 = make_kernel(kernel_id="l" * 64)
        kernel2.name = "vmlinux-test-2"

        db.upsert_kernel(kernel1)
        db.upsert_kernel(kernel2)

        # Set kernel1 as default
        db.set_default_kernel(kernel1.id)
        retrieved_kernel1 = db.get_kernel(kernel1.id)
        retrieved_kernel2 = db.get_kernel(kernel2.id)
        assert retrieved_kernel1 is not None
        assert retrieved_kernel2 is not None
        assert retrieved_kernel1.is_default == 1
        assert retrieved_kernel2.is_default == 0

        # Set kernel2 as default — kernel1 should be cleared
        db.set_default_kernel(kernel2.id)
        retrieved_kernel1 = db.get_kernel(kernel1.id)
        retrieved_kernel2 = db.get_kernel(kernel2.id)
        assert retrieved_kernel1 is not None
        assert retrieved_kernel2 is not None
        assert retrieved_kernel1.is_default == 0
        assert retrieved_kernel2.is_default == 1

    def test_network_default_tracking(self, db: MVMDatabase) -> None:
        """Test that only one network can be marked as default."""
        network1 = make_network(network_id="n" * 64)
        network2 = make_network(network_id="o" * 64)
        network2.name = "secondary"

        db.upsert_network(network1)
        db.upsert_network(network2)

        # Set network1 as default
        db.set_default_network(network1.id)
        retrieved_network1 = db.get_network(network1.id)
        retrieved_network2 = db.get_network(network2.id)
        assert retrieved_network1 is not None
        assert retrieved_network2 is not None
        assert retrieved_network1.is_default == 1
        assert retrieved_network2.is_default == 0

        # Set network2 as default — network1 should be cleared
        db.set_default_network(network2.id)
        retrieved_network1 = db.get_network(network1.id)
        retrieved_network2 = db.get_network(network2.id)
        assert retrieved_network1 is not None
        assert retrieved_network2 is not None
        assert retrieved_network1.is_default == 0
        assert retrieved_network2.is_default == 1

    def test_multiple_vms_on_same_network(self, db: MVMDatabase) -> None:
        """Test multiple VMs can share the same network with different leases."""
        network = make_network()
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        vm1 = make_vm(vm_id="v" * 63 + "1")
        vm2 = make_vm(vm_id="v" * 63 + "2", name="testvm2")

        db.upsert_network(network)
        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_vm(vm1)
        db.upsert_vm(vm2)

        # Acquire leases for both VMs
        db.acquire_lease(network.id, "172.35.0.10", vm1.id)
        db.acquire_lease(network.id, "172.35.0.11", vm2.id)

        # Verify both leases exist
        retrieved_lease1 = db.get_lease(network.id, "172.35.0.10")
        retrieved_lease2 = db.get_lease(network.id, "172.35.0.11")
        assert retrieved_lease1 is not None
        assert retrieved_lease2 is not None
        assert retrieved_lease1.vm_id == vm1.id
        assert retrieved_lease2.vm_id == vm2.id

        # List leases for network
        leases = db.list_leases(network.id)
        assert len(leases) == 2

        # Delete vm1 — only its lease should be deleted
        db.delete_vm(vm1.id)
        assert db.get_lease(network.id, "172.35.0.10") is None
        assert db.get_lease(network.id, "172.35.0.11") is not None


class TestSchemaIntegrity:
    """Test schema-level constraints (10+ tests)."""

    def test_all_nine_tables_exist_after_migration(self, db: MVMDatabase) -> None:
        """Verify all 9 tables exist after migration."""
        with db._connect() as conn:
            cursor = conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
            )
            tables = {row[0] for row in cursor.fetchall()}

        expected_tables = {
            "images",
            "kernels",
            "binaries",
            "networks",
            "network_leases",
            "vm_instances",
            "host_state",
            "host_state_changes",
            "db_migrations",
        }
        assert tables == expected_tables

    def test_pragma_user_version_is_one_after_migration(self, db: MVMDatabase) -> None:
        assert db.get_current_version() == 1

    def test_images_os_slug_unique_constraint(self, db: MVMDatabase) -> None:
        """Verify images.os_slug UNIQUE constraint."""
        image1 = make_image(image_id="i" * 64)
        image2 = make_image(image_id="j" * 64)
        # Both have same os_slug

        db.upsert_image(image1)

        # Attempt to insert second image with same os_slug — should fail
        with pytest.raises(sqlite3.IntegrityError):
            db.upsert_image(image2)

    def test_vm_states_name_unique_constraint(self, db: MVMDatabase) -> None:
        """Verify vm_states.name UNIQUE constraint."""
        network = make_network()
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()

        db.upsert_network(network)
        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)

        vm1 = make_vm(vm_id="v" * 63 + "1")
        vm2 = make_vm(vm_id="v" * 63 + "2")
        # Both have name="testvm"

        db.upsert_vm(vm1)

        # Attempt to insert second VM with same name — should fail
        with pytest.raises(sqlite3.IntegrityError):
            db.upsert_vm(vm2)

    def test_networks_name_unique_constraint(self, db: MVMDatabase) -> None:
        network1 = make_network(network_id="n" * 64)
        network2 = make_network(network_id="o" * 64)
        # Both have name="default"

        db.upsert_network(network1)
        db.upsert_network(network2)

        networks = db.list_networks()
        assert len(networks) == 1
        assert networks[0].id == network1.id
        assert networks[0].name == network2.name
        assert networks[0].subnet == network2.subnet
        assert networks[0].bridge == network2.bridge

    def test_network_leases_network_ipv4_unique_constraint(self, db: MVMDatabase) -> None:
        """Verify network_leases.(network_id, ipv4) UNIQUE constraint."""
        network = make_network()
        db.upsert_network(network)

        # Acquire first lease
        db.acquire_lease(network.id, "172.35.0.10")

        # Attempt to acquire same IP on same network — should fail
        with pytest.raises(sqlite3.IntegrityError):
            db.acquire_lease(network.id, "172.35.0.10")

    def test_network_leases_ipv4_check_constraint_invalid_ip_fails(self, db: MVMDatabase) -> None:
        """Verify network_leases.ipv4 CHECK constraint rejects invalid IPs."""
        network = make_network()
        db.upsert_network(network)

        # Attempt to insert invalid IP — should fail CHECK constraint
        with pytest.raises(sqlite3.IntegrityError):
            db.acquire_lease(network.id, "invalid-ip")

    def test_vm_states_ipv4_check_constraint_invalid_ip_fails(self, db: MVMDatabase) -> None:
        """Verify vm_states.ipv4 CHECK constraint rejects invalid IPs."""
        network = make_network()
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()

        db.upsert_network(network)
        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)

        # Create VM with invalid IP
        vm = make_vm()
        vm.ipv4 = "invalid-ip"

        # Attempt to insert — should fail CHECK constraint
        with pytest.raises(sqlite3.IntegrityError):
            db.upsert_vm(vm)

    def test_vm_states_ipv4_check_constraint_allows_null(self, db: MVMDatabase) -> None:
        """Verify vm_states.ipv4 CHECK constraint allows NULL."""
        network = make_network()
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        vm = make_vm()
        vm.ipv4 = None  # NULL is allowed

        db.upsert_network(network)
        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_vm(vm)

        # Verify VM was inserted
        assert db.get_vm(vm.id) is not None

    def test_vm_states_mac_check_constraint_invalid_mac_fails(self, db: MVMDatabase) -> None:
        """Verify vm_states.mac CHECK constraint rejects invalid MACs."""
        network = make_network()
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()

        db.upsert_network(network)
        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)

        # Create VM with invalid MAC
        vm = make_vm()
        vm.mac = "invalid-mac"

        # Attempt to insert — should fail CHECK constraint
        with pytest.raises(sqlite3.IntegrityError):
            db.upsert_vm(vm)

    def test_vm_states_mac_check_constraint_allows_null(self, db: MVMDatabase) -> None:
        """Verify vm_states.mac CHECK constraint allows NULL."""
        network = make_network()
        image = make_image()
        kernel = make_kernel()
        binary = make_binary()
        vm = make_vm()
        vm.mac = None  # NULL is allowed

        db.upsert_network(network)
        db.upsert_image(image)
        db.upsert_kernel(kernel)
        db.upsert_binary(binary)
        db.upsert_vm(vm)

        # Verify VM was inserted
        assert db.get_vm(vm.id) is not None

    def test_host_state_changes_session_order_unique_constraint(self, db: MVMDatabase) -> None:
        """Verify host_state_changes.(session_id, change_order) UNIQUE constraint."""
        from mvmctl.db.models import HostStateChange

        change1 = HostStateChange(
            session_id="session-1",
            init_timestamp="2024-01-01T00:00:00Z",
            setting="test_setting",
            mechanism="test_mechanism",
            applied_value="value1",
            change_order=1,
        )
        change2 = HostStateChange(
            session_id="session-1",
            init_timestamp="2024-01-01T00:00:00Z",
            setting="test_setting_2",
            mechanism="test_mechanism_2",
            applied_value="value2",
            change_order=1,  # Same change_order as change1
        )

        db.add_host_change(change1)

        # Attempt to add second change with same session_id and change_order — should fail
        with pytest.raises(sqlite3.IntegrityError):
            db.add_host_change(change2)
