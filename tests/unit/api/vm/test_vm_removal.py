"""Unit tests for api/vm/_removal.py — VMRemovalContext, VMBulkCleanupContext.

These tests verify that the context classes are pure state trackers.
All orchestration logic has been moved to _registry.py.
"""

from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.api.vm._removal import VMBulkCleanupContext, VMRemovalContext
from mvmctl.api.inputs import NetworkConfig
from mvmctl.models.vm import VMInstance, VMStatus


# =============================================================================
# VMRemovalContext Tests
# =============================================================================


class TestVMRemovalContext:
    """Tests for VMRemovalContext as a pure state tracker."""

    @pytest.fixture
    def sample_vm(self):
        """Create a sample VMInstance for tests."""
        return VMInstance(
            name="test-vm",
            id="abc123def4567890",
            pid=1234,
            ipv4="10.20.0.5",
            mac="02:FC:00:11:22:33",
            network_id="net-123",
            tap_device="mvm-tap0",
            created_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
            updated_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
            status=VMStatus.RUNNING,
            rootfs_suffix=".ext4",
            kernel_id="kern-123",
            image_id="img-123",
            binary_id="bin-123",
            disk_size_mib=1024,
            api_socket_path=Path("/tmp/test-vm.sock"),
            console_relay_pid=5678,
            nocloud_net_port=8080,
            nocloud_server_pid=9999,
        )

    @pytest.fixture
    def net_config(self):
        """Create a NetworkConfig for tests."""
        return NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
        )

    @pytest.fixture
    def removal_context(self, sample_vm, net_config, tmp_path):
        """Create a VMRemovalContext instance."""
        mock_manager = MagicMock()
        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()

        return VMRemovalContext(
            vm=sample_vm,
            vm_dir=vm_dir,
            net_config=net_config,
            bridge="mvm-br0",
            manager=mock_manager,
        )

    def test_init(self, sample_vm, net_config, tmp_path):
        """Test VMRemovalContext initialization."""
        mock_manager = MagicMock()
        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()

        ctx = VMRemovalContext(
            vm=sample_vm,
            vm_dir=vm_dir,
            net_config=net_config,
            bridge="mvm-br0",
            manager=mock_manager,
        )

        assert ctx.vm == sample_vm
        assert ctx.vm_dir == vm_dir
        assert ctx.net_config == net_config
        assert ctx.bridge == "mvm-br0"
        assert ctx.manager == mock_manager
        assert ctx.pid is None

    def test_vm_property(self, removal_context, sample_vm):
        """Test vm property returns the VM instance."""
        assert removal_context.vm == sample_vm

    def test_vm_dir_property(self, removal_context, tmp_path):
        """Test vm_dir property returns the VM directory."""
        assert removal_context.vm_dir == tmp_path / "test-vm"

    def test_net_config_property(self, removal_context, net_config):
        """Test net_config property returns the network config."""
        assert removal_context.net_config == net_config

    def test_bridge_property(self, removal_context):
        """Test bridge property returns the bridge name."""
        assert removal_context.bridge == "mvm-br0"

    def test_manager_property(self, removal_context):
        """Test manager property returns the VM manager."""
        assert removal_context.manager is not None

    def test_pid_property_get(self, removal_context):
        """Test pid property getter."""
        assert removal_context.pid is None

        removal_context._pid = 1234
        assert removal_context.pid == 1234

    def test_pid_property_set(self, removal_context):
        """Test pid property setter."""
        removal_context.pid = 5678
        assert removal_context._pid == 5678
        assert removal_context.pid == 5678

    def test_is_pure_state_tracker(self, removal_context):
        """Test that VMRemovalContext has no orchestration methods."""
        # These methods were removed - orchestration is now in _registry.py
        assert not hasattr(removal_context, "shutdown")
        assert not hasattr(removal_context, "wait_and_record_exit")
        assert not hasattr(removal_context, "cleanup_all")
        assert not hasattr(removal_context, "deregister")
        assert not hasattr(removal_context, "_cleanup_console")
        assert not hasattr(removal_context, "_cleanup_nocloud")
        assert not hasattr(removal_context, "_cleanup_network")
        assert not hasattr(removal_context, "_cleanup_ip")
        assert not hasattr(removal_context, "_cleanup_ssh_known_hosts")


# =============================================================================
# VMBulkCleanupContext Tests
# =============================================================================


class TestVMBulkCleanupContext:
    """Tests for VMBulkCleanupContext as a pure state tracker."""

    @pytest.fixture
    def sample_vms(self):
        """Create sample VMInstances for tests."""
        return [
            VMInstance(
                name="vm-1",
                id="abc123def4567890",
                pid=1001,
                ipv4="10.20.0.5",
                mac="02:FC:00:11:22:33",
                network_id="net-123",
                tap_device="mvm-tap0",
                created_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
                updated_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
                status=VMStatus.RUNNING,
                rootfs_suffix=".ext4",
                kernel_id="kern-123",
                image_id="img-123",
                binary_id="bin-123",
                disk_size_mib=1024,
                nocloud_net_port=8080,
            ),
            VMInstance(
                name="vm-2",
                id="def456abc1237890",
                pid=1002,
                ipv4="10.20.0.6",
                mac="02:FC:00:11:22:44",
                network_id="net-123",
                tap_device="mvm-tap1",
                created_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
                updated_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
                status=VMStatus.RUNNING,
                rootfs_suffix=".ext4",
                kernel_id="kern-123",
                image_id="img-123",
                binary_id="bin-123",
                disk_size_mib=1024,
                nocloud_net_port=8081,
            ),
        ]

    @pytest.fixture
    def bulk_context(self, tmp_path):
        """Create a VMBulkCleanupContext instance."""
        mock_manager = MagicMock()
        return VMBulkCleanupContext(
            manager=mock_manager,
            cache_dir=tmp_path,
        )

    def test_init(self, tmp_path):
        """Test VMBulkCleanupContext initialization."""
        mock_manager = MagicMock()
        ctx = VMBulkCleanupContext(
            manager=mock_manager,
            cache_dir=tmp_path,
        )

        assert ctx.manager == mock_manager
        assert ctx.cache_dir == tmp_path
        assert ctx.targets == []

    def test_targets_property(self, bulk_context):
        """Test targets property returns the list of VMs."""
        assert bulk_context.targets == []

        mock_vm = MagicMock()
        bulk_context._targets = [mock_vm]
        assert bulk_context.targets == [mock_vm]

    def test_set_targets(self, bulk_context, sample_vms):
        """Test set_targets sets the list of VMs to clean up."""
        bulk_context.set_targets(sample_vms)

        assert bulk_context._targets == sample_vms
        assert bulk_context.targets == sample_vms

    def test_is_pure_state_tracker(self, bulk_context):
        """Test that VMBulkCleanupContext has no orchestration methods."""
        # These methods were removed - orchestration is now in _registry.py
        assert not hasattr(bulk_context, "cleanup_all")
