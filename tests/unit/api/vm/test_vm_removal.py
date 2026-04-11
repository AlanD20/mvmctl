"""Unit tests for api/vm/_removal.py — VMRemovalContext, VMBulkCleanupContext."""

from __future__ import annotations

import signal
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.api.vm._removal import VMBulkCleanupContext, VMRemovalContext
from mvmctl.models.network import NetworkConfig
from mvmctl.models.vm import VMInstance, VMStatus


# =============================================================================
# VMRemovalContext Tests
# =============================================================================


class TestVMRemovalContext:
    """Tests for VMRemovalContext removal state management."""

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
        assert ctx._net_config == net_config
        assert ctx._bridge == "mvm-br0"
        assert ctx._manager == mock_manager
        assert ctx.pid is None

    def test_vm_property(self, removal_context, sample_vm):
        """Test vm property returns the VM instance."""
        assert removal_context.vm == sample_vm

    def test_vm_dir_property(self, removal_context, tmp_path):
        """Test vm_dir property returns the VM directory."""
        assert removal_context.vm_dir == tmp_path / "test-vm"

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

    @patch("mvmctl.api.vm._removal.os.kill")
    def test_shutdown_force(self, mock_kill, removal_context):
        """Test shutdown with force=True sends SIGKILL."""
        removal_context.pid = 1234

        removal_context.shutdown(force=True)

        mock_kill.assert_called_once_with(1234, signal.SIGKILL)

    @patch("mvmctl.api.vm._removal.os.kill")
    def test_shutdown_force_handles_errors(self, mock_kill, removal_context):
        """Test shutdown with force=True handles errors gracefully."""
        mock_kill.side_effect = ProcessLookupError()
        removal_context.pid = 1234

        removal_context.shutdown(force=True)

        # Should not raise exception

    @patch("mvmctl.core.vm_process.graceful_shutdown")
    def test_shutdown_graceful(self, mock_graceful, removal_context):
        """Test shutdown with force=False uses graceful_shutdown."""
        removal_context.pid = 1234
        removal_context.vm.api_socket_path = Path("/tmp/test.sock")

        removal_context.shutdown(force=False)

        mock_graceful.assert_called_once_with(1234, Path("/tmp/test.sock"))

    @patch("mvmctl.api.vm._removal.os.waitpid")
    def test_wait_and_record_exit_normal(self, mock_waitpid, removal_context, tmp_path):
        """Test wait_and_record_exit records normal exit code."""
        removal_context.pid = 1234
        mock_waitpid.return_value = (1234, 0x0000)  # Normal exit, status 0

        removal_context.wait_and_record_exit()

        exit_code_file = removal_context.vm_dir / "firecracker.exitcode"
        assert exit_code_file.exists()
        assert exit_code_file.read_text() == "0"

    @patch("mvmctl.api.vm._removal.os.waitpid")
    def test_wait_and_record_exit_signal(self, mock_waitpid, removal_context, tmp_path):
        """Test wait_and_record_exit records signal exit code."""
        removal_context.pid = 1234
        mock_waitpid.return_value = (1234, 0x0009)  # Killed by SIGKILL (signal 9)

        removal_context.wait_and_record_exit()

        exit_code_file = removal_context.vm_dir / "firecracker.exitcode"
        assert exit_code_file.exists()
        # Signal exit code is 128 + signal number
        assert exit_code_file.read_text() == "137"  # 128 + 9

    @patch("mvmctl.api.vm._removal.os.waitpid")
    def test_wait_and_record_exit_handles_errors(self, mock_waitpid, removal_context):
        """Test wait_and_record_exit handles errors gracefully."""
        removal_context.pid = 1234
        mock_waitpid.side_effect = ChildProcessError()

        removal_context.wait_and_record_exit()

        # Should not raise exception

    def test_wait_and_record_exit_no_pid(self, removal_context):
        """Test wait_and_record_exit does nothing when pid is None."""
        removal_context.pid = None

        removal_context.wait_and_record_exit()

        # Should not raise exception or write file

    @patch("mvmctl.services.console_relay.ConsoleRelayManager")
    def test_cleanup_console(self, mock_manager_class, removal_context, sample_vm):
        """Test _cleanup_console stops console relay."""
        mock_manager = MagicMock()
        mock_manager_class.return_value = mock_manager
        sample_vm.console_relay_pid = 5678

        removal_context._cleanup_console()

        mock_manager.stop_relay.assert_called_once_with("test-vm", "abc123def4567890")

    @patch("mvmctl.services.console_relay.ConsoleRelayManager")
    @patch("mvmctl.api.vm._removal.logger")
    def test_cleanup_console_handles_errors(self, mock_logger, mock_manager_class, removal_context, sample_vm):
        """Test _cleanup_console handles errors gracefully."""
        mock_manager = MagicMock()
        mock_manager.stop_relay.side_effect = RuntimeError("Stop failed")
        mock_manager_class.return_value = mock_manager
        sample_vm.console_relay_pid = 5678

        removal_context._cleanup_console()

        mock_logger.warning.assert_called()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    def test_cleanup_nocloud(self, mock_remove_rule, mock_manager_class, removal_context, sample_vm):
        """Test _cleanup_nocloud stops nocloud server and removes firewall rule."""
        mock_manager = MagicMock()
        mock_manager_class.return_value = mock_manager
        sample_vm.nocloud_net_port = 8080
        sample_vm.ipv4 = "10.20.0.5"

        removal_context._cleanup_nocloud()

        mock_manager.stop_server.assert_called_once_with("test-vm", "abc123def4567890")
        mock_remove_rule.assert_called_once_with("10.20.0.5", "test-vm", 8080)

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.logger")
    def test_cleanup_nocloud_handles_errors(self, mock_logger, mock_remove_rule, mock_manager_class, removal_context, sample_vm):
        """Test _cleanup_nocloud handles errors gracefully."""
        mock_manager = MagicMock()
        mock_manager.stop_server.side_effect = RuntimeError("Stop failed")
        mock_manager_class.return_value = mock_manager
        sample_vm.nocloud_net_port = 8080
        sample_vm.ipv4 = "10.20.0.5"

        removal_context._cleanup_nocloud()

        mock_logger.warning.assert_called()

    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    def test_cleanup_network(self, mock_teardown_nat, mock_remove_rules, mock_delete_tap, removal_context, sample_vm):
        """Test _cleanup_network removes iptables rules, NAT, and TAP."""
        sample_vm.tap_device = "mvm-tap0"

        removal_context._cleanup_network()

        mock_remove_rules.assert_called_once_with("mvm-tap0", bridge="mvm-br0")
        mock_teardown_nat.assert_called_once_with("mvm-br0", force=False, subnet="10.20.0.0/24")
        mock_delete_tap.assert_called_once_with("mvm-tap0")

    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.api.vm._removal.logger")
    def test_cleanup_network_handles_nat_error(self, mock_logger, mock_teardown_nat, mock_remove_rules, mock_delete_tap, removal_context, sample_vm):
        """Test _cleanup_network handles NAT teardown errors gracefully."""
        from mvmctl.exceptions import NetworkError

        mock_teardown_nat.side_effect = NetworkError("NAT teardown failed")
        sample_vm.tap_device = "mvm-tap0"

        removal_context._cleanup_network()

        mock_logger.debug.assert_called()

    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    def test_cleanup_network_handles_tap_error(self, mock_teardown_nat, mock_remove_rules, mock_delete_tap, removal_context, sample_vm):
        """Test _cleanup_network handles TAP deletion errors gracefully."""
        from mvmctl.exceptions import NetworkError

        mock_delete_tap.side_effect = NetworkError("TAP delete failed")
        sample_vm.tap_device = "mvm-tap0"

        removal_context._cleanup_network()

        # Should not raise exception

    @patch("mvmctl.api.network.release_network_ip")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    def test_cleanup_ip(self, mock_db_class, mock_release_ip, removal_context, sample_vm, net_config):
        """Test _cleanup_ip releases network IP."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.id = "net-123"
        mock_db.get_network_by_name.return_value = mock_db_net

        removal_context._cleanup_ip()

        mock_release_ip.assert_called_once_with("net-123", "abc123def4567890")

    @patch("mvmctl.api.network.release_network_ip")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.vm._removal.logger")
    def test_cleanup_ip_handles_errors(self, mock_logger, mock_db_class, mock_release_ip, removal_context):
        """Test _cleanup_ip handles errors gracefully."""
        from mvmctl.exceptions import NetworkError

        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db.get_network_by_name.side_effect = NetworkError("DB error")

        removal_context._cleanup_ip()

        mock_logger.warning.assert_called()

    def test_cleanup_ssh_known_hosts(self, removal_context, sample_vm):
        """Test _cleanup_ssh_known_hosts removes VM from known_hosts."""
        sample_vm.ipv4 = "10.20.0.5"

        with patch("subprocess.run") as mock_run:
            removal_context._cleanup_ssh_known_hosts()
            mock_run.assert_called_once_with(["ssh-keygen", "-R", "10.20.0.5"], capture_output=True, check=False)

    def test_cleanup_ssh_known_hosts_no_ip(self, removal_context, sample_vm):
        """Test _cleanup_ssh_known_hosts does nothing when no IP."""
        sample_vm.ipv4 = None

        with patch("subprocess.run") as mock_run:
            removal_context._cleanup_ssh_known_hosts()
            mock_run.assert_not_called()

    def test_cleanup_ssh_known_hosts_handles_missing_ssh_keygen(self, removal_context, sample_vm):
        """Test _cleanup_ssh_known_hosts handles missing ssh-keygen gracefully."""
        sample_vm.ipv4 = "10.20.0.5"

        with patch("subprocess.run", side_effect=FileNotFoundError()) as mock_run:
            removal_context._cleanup_ssh_known_hosts()
            # Should not raise exception

    @patch.object(VMRemovalContext, "_cleanup_console")
    @patch.object(VMRemovalContext, "_cleanup_nocloud")
    @patch.object(VMRemovalContext, "_cleanup_network")
    @patch.object(VMRemovalContext, "_cleanup_ip")
    @patch.object(VMRemovalContext, "_cleanup_ssh_known_hosts")
    def test_cleanup_all(self, mock_ssh, mock_ip, mock_network, mock_nocloud, mock_console, removal_context):
        """Test cleanup_all runs all cleanup tasks."""
        removal_context.cleanup_all(fast=False)

        mock_console.assert_called_once()
        mock_nocloud.assert_called_once()
        mock_network.assert_called_once()
        mock_ip.assert_called_once()
        mock_ssh.assert_called_once()

    @patch.object(VMRemovalContext, "_cleanup_console")
    @patch.object(VMRemovalContext, "_cleanup_nocloud")
    @patch.object(VMRemovalContext, "_cleanup_network")
    @patch.object(VMRemovalContext, "_cleanup_ip")
    @patch.object(VMRemovalContext, "_cleanup_ssh_known_hosts")
    def test_cleanup_all_fast_mode(self, mock_ssh, mock_ip, mock_network, mock_nocloud, mock_console, removal_context):
        """Test cleanup_all skips SSH cleanup in fast mode."""
        removal_context.cleanup_all(fast=True)

        mock_console.assert_called_once()
        mock_nocloud.assert_called_once()
        mock_network.assert_called_once()
        mock_ip.assert_called_once()
        mock_ssh.assert_not_called()

    @patch.object(VMRemovalContext, "_cleanup_console")
    @patch.object(VMRemovalContext, "_cleanup_nocloud")
    @patch.object(VMRemovalContext, "_cleanup_network")
    @patch.object(VMRemovalContext, "_cleanup_ip")
    @patch("mvmctl.api.vm._removal.logger")
    def test_cleanup_all_handles_task_errors(self, mock_logger, mock_ip, mock_network, mock_nocloud, mock_console, removal_context):
        """Test cleanup_all handles individual task errors gracefully."""
        mock_console.side_effect = Exception("Console cleanup failed")

        removal_context.cleanup_all(fast=False)

        # Other tasks should still run
        mock_nocloud.assert_called_once()
        mock_network.assert_called_once()
        mock_ip.assert_called_once()

    def test_deregister(self, removal_context, tmp_path):
        """Test deregister removes VM from DB and deletes directory."""
        removal_context.deregister(fast=False)

        removal_context._manager.deregister.assert_called_once_with("abc123def4567890")
        assert not removal_context.vm_dir.exists()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    def test_deregister_fast_mode(self, mock_manager_class, removal_context):
        """Test deregister skips orphan cleanup in fast mode."""
        mock_manager = MagicMock()
        mock_manager_class.return_value = mock_manager

        removal_context.deregister(fast=True)

        mock_manager.cleanup_orphans.assert_not_called()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    def test_deregister_normal_mode(self, mock_manager_class, removal_context):
        """Test deregister runs orphan cleanup in normal mode."""
        mock_manager = MagicMock()
        mock_manager_class.return_value = mock_manager

        removal_context.deregister(fast=False)

        mock_manager.cleanup_orphans.assert_called_once()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.api.vm._removal.logger")
    def test_deregister_handles_orphan_cleanup_error(self, mock_logger, mock_manager_class, removal_context):
        """Test deregister handles orphan cleanup errors gracefully."""
        mock_manager = MagicMock()
        mock_manager.cleanup_orphans.side_effect = Exception("Cleanup failed")
        mock_manager_class.return_value = mock_manager

        removal_context.deregister(fast=False)

        # Should not raise exception


# =============================================================================
# VMBulkCleanupContext Tests
# =============================================================================


class TestVMBulkCleanupContext:
    """Tests for VMBulkCleanupContext bulk cleanup management."""

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

        assert ctx._manager == mock_manager
        assert ctx._cache_dir == tmp_path
        assert ctx._targets == []

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

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.os.kill")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_cleanup_all(
        self,
        mock_get_vm_dir,
        mock_teardown_nat,
        mock_remove_rules,
        mock_delete_tap,
        mock_get_network,
        mock_db_class,
        mock_kill,
        mock_remove_rule,
        mock_manager_class,
        bulk_context,
        sample_vms,
        tmp_path,
    ):
        """Test cleanup_all cleans up all target VMs."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.name = "default"
        mock_db.get_network.return_value = mock_db_net

        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config

        vm_dir = tmp_path / "vm-1"
        vm_dir.mkdir()
        mock_get_vm_dir.return_value = vm_dir

        bulk_context.set_targets(sample_vms[:1])  # Just first VM
        bulk_context.cleanup_all()

        # Should stop nocloud server
        mock_manager_class.return_value.stop_server.assert_called()
        # Should remove firewall rule
        mock_remove_rule.assert_called()
        # Should kill VM process
        mock_kill.assert_called_with(1001, signal.SIGKILL)
        # Should deregister VM
        bulk_context._manager.deregister.assert_called()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.os.kill")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_cleanup_all_multiple_vms(
        self,
        mock_get_vm_dir,
        mock_teardown_nat,
        mock_remove_rules,
        mock_delete_tap,
        mock_get_network,
        mock_db_class,
        mock_kill,
        mock_remove_rule,
        mock_manager_class,
        bulk_context,
        sample_vms,
        tmp_path,
    ):
        """Test cleanup_all cleans up multiple VMs."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.name = "default"
        mock_db.get_network.return_value = mock_db_net

        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config

        def get_vm_dir(vm_id):
            vm_dir = tmp_path / vm_id[:8]
            vm_dir.mkdir(exist_ok=True)
            return vm_dir

        mock_get_vm_dir.side_effect = get_vm_dir

        bulk_context.set_targets(sample_vms)
        bulk_context.cleanup_all()

        # Should kill both VM processes
        assert mock_kill.call_count == 2
        # Should deregister both VMs
        assert bulk_context._manager.deregister.call_count == 2

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.os.kill")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_cleanup_all_handles_missing_vm_dir(
        self,
        mock_get_vm_dir,
        mock_teardown_nat,
        mock_remove_rules,
        mock_delete_tap,
        mock_get_network,
        mock_db_class,
        mock_kill,
        mock_remove_rule,
        mock_manager_class,
        bulk_context,
        sample_vms,
    ):
        """Test cleanup_all handles missing VM directories gracefully."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.name = "default"
        mock_db.get_network.return_value = mock_db_net

        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config

        mock_get_vm_dir.return_value = None  # No VM directory

        bulk_context.set_targets(sample_vms[:1])
        bulk_context.cleanup_all()

        # Should not raise exception
        bulk_context._manager.deregister.assert_called_once()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.os.kill")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_cleanup_all_handles_process_kill_errors(
        self,
        mock_get_vm_dir,
        mock_teardown_nat,
        mock_remove_rules,
        mock_delete_tap,
        mock_get_network,
        mock_db_class,
        mock_kill,
        mock_remove_rule,
        mock_manager_class,
        bulk_context,
        sample_vms,
    ):
        """Test cleanup_all handles process kill errors gracefully."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.name = "default"
        mock_db.get_network.return_value = mock_db_net

        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config

        mock_get_vm_dir.return_value = None
        mock_kill.side_effect = ProcessLookupError()

        bulk_context.set_targets(sample_vms[:1])
        bulk_context.cleanup_all()

        # Should continue with cleanup despite kill error
        bulk_context._manager.deregister.assert_called_once()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.os.kill")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_cleanup_all_handles_network_errors(
        self,
        mock_get_vm_dir,
        mock_teardown_nat,
        mock_remove_rules,
        mock_delete_tap,
        mock_get_network,
        mock_db_class,
        mock_kill,
        mock_remove_rule,
        mock_manager_class,
        bulk_context,
        sample_vms,
    ):
        """Test cleanup_all handles network cleanup errors gracefully."""
        from mvmctl.exceptions import NetworkError

        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.name = "default"
        mock_db.get_network.return_value = mock_db_net

        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config

        mock_get_vm_dir.return_value = None
        mock_delete_tap.side_effect = NetworkError("TAP delete failed")
        mock_teardown_nat.side_effect = NetworkError("NAT teardown failed")

        bulk_context.set_targets(sample_vms[:1])
        bulk_context.cleanup_all()

        # Should continue with cleanup despite network errors
        bulk_context._manager.deregister.assert_called_once()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.os.kill")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_cleanup_all_cleans_orphans(
        self,
        mock_get_vm_dir,
        mock_teardown_nat,
        mock_remove_rules,
        mock_delete_tap,
        mock_get_network,
        mock_db_class,
        mock_kill,
        mock_remove_rule,
        mock_manager_class,
        bulk_context,
        sample_vms,
    ):
        """Test cleanup_all cleans up orphaned nocloud servers."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.name = "default"
        mock_db.get_network.return_value = mock_db_net

        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config

        mock_get_vm_dir.return_value = None

        mock_nocloud_manager = MagicMock()
        mock_manager_class.return_value = mock_nocloud_manager

        bulk_context.set_targets([])  # Empty targets
        bulk_context.cleanup_all()

        mock_nocloud_manager.cleanup_orphans.assert_called_once()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.os.kill")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_cleanup_all_handles_orphan_cleanup_error(
        self,
        mock_get_vm_dir,
        mock_teardown_nat,
        mock_remove_rules,
        mock_delete_tap,
        mock_get_network,
        mock_db_class,
        mock_kill,
        mock_remove_rule,
        mock_manager_class,
        bulk_context,
        sample_vms,
    ):
        """Test cleanup_all handles orphan cleanup errors gracefully."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.name = "default"
        mock_db.get_network.return_value = mock_db_net

        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config

        mock_get_vm_dir.return_value = None

        mock_nocloud_manager = MagicMock()
        mock_nocloud_manager.cleanup_orphans.side_effect = Exception("Cleanup failed")
        mock_manager_class.return_value = mock_nocloud_manager

        bulk_context.set_targets([])
        bulk_context.cleanup_all()

        # Should not raise exception

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.os.kill")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_cleanup_all_cleans_nocloud_cache(
        self,
        mock_get_vm_dir,
        mock_teardown_nat,
        mock_remove_rules,
        mock_delete_tap,
        mock_get_network,
        mock_db_class,
        mock_kill,
        mock_remove_rule,
        mock_manager_class,
        bulk_context,
        sample_vms,
        tmp_path,
    ):
        """Test cleanup_all cleans up nocloud cache directories."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.name = "default"
        mock_db.get_network.return_value = mock_db_net

        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config

        mock_get_vm_dir.return_value = None

        # Create nocloud cache directory
        nocloud_cache = tmp_path / f"nocloud-{sample_vms[0].id}"
        nocloud_cache.mkdir()
        (nocloud_cache / "test-file").write_text("test")

        bulk_context.set_targets(sample_vms[:1])
        bulk_context.cleanup_all()

        assert not nocloud_cache.exists()

    @patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm._removal.os.kill")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.core.network.delete_tap")
    @patch("mvmctl.core.network.remove_iptables_forward_rules")
    @patch("mvmctl.core.network.teardown_nat")
    @patch("mvmctl.utils.fs.get_vm_dir_by_hash")
    def test_cleanup_all_handles_vm_without_id(
        self,
        mock_get_vm_dir,
        mock_teardown_nat,
        mock_remove_rules,
        mock_delete_tap,
        mock_get_network,
        mock_db_class,
        mock_kill,
        mock_remove_rule,
        mock_manager_class,
        bulk_context,
        tmp_path,
    ):
        """Test cleanup_all handles VMs without ID gracefully."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.name = "default"
        mock_db.get_network.return_value = mock_db_net

        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config

        mock_get_vm_dir.return_value = None

        vm_without_id = VMInstance(
            name="vm-no-id",
            id="",
            pid=1003,
            ipv4="10.20.0.7",
            mac="02:FC:00:11:22:55",
            network_id="net-123",
            tap_device="mvm-tap2",
            created_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
            updated_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
            status=VMStatus.RUNNING,
            rootfs_suffix=".ext4",
            kernel_id="kern-123",
            image_id="img-123",
            binary_id="bin-123",
            disk_size_mib=1024,
            nocloud_net_port=8082,
        )

        bulk_context.set_targets([vm_without_id])
        bulk_context.cleanup_all()

        # Should deregister by name when ID is empty
        bulk_context._manager.deregister.assert_called_once_with("vm-no-id")
