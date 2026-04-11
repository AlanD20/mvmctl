"""Unit tests for api/vm/_creation.py — VMCreationContext, GuestfsProvisioner."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.api.vm._creation import (
    CloudInitProvisionResult,
    CloudInitProvisioner,
    GuestfsProvisioner,
    VMCreationContext,
)
from mvmctl.api.vm._registry import (
    _perform_creation_cleanup,
    _persist_failed_vm,
)
from mvmctl.exceptions import VMCreateError
from mvmctl.models.cloud_init import CloudInitMode
from mvmctl.models.network import NetworkConfig


# =============================================================================
# VMCreationContext Tests
# =============================================================================


class TestVMCreationContext:
    """Tests for VMCreationContext state tracking and cleanup."""

    @pytest.fixture
    def mock_resolved(self):
        """Create a mock ResolvedVMInputs object."""
        mock = MagicMock()
        mock.name = "test-vm"
        mock.vm_id = "abc123def456"
        mock.network_name = "default"
        return mock

    @pytest.fixture
    def creation_context(self, mock_resolved, tmp_path):
        """Create a VMCreationContext with mocked resolved inputs."""
        return VMCreationContext(resolved=mock_resolved)

    def test_init(self, mock_resolved):
        """Test VMCreationContext initialization."""
        ctx = VMCreationContext(resolved=mock_resolved)

        assert ctx.resolved == mock_resolved
        assert ctx.vm_dir is None
        assert ctx.tap_name == ""
        assert ctx.guest_ip == ""
        assert ctx.net_manager is None
        assert ctx.relay_mgr is None
        assert ctx.pty_master_fd is None
        assert ctx.pty_slave_fd is None
        assert ctx.nocloud_net_port == 0
        assert ctx.log_fp is None
        assert ctx.console_fp is None
        assert ctx.cloud_init_result is None
        assert ctx.resources_created == {}

    def test_mark_created(self, creation_context):
        """Test mark_created tracks resources."""
        creation_context.mark_created("vm_dir")
        creation_context.mark_created("tap")
        creation_context.mark_created("network_ip")

        assert creation_context.resources_created == {
            "vm_dir": True,
            "tap": True,
            "network_ip": True,
        }

    def test_was_created_true(self, creation_context):
        """Test was_created returns True for created resources."""
        creation_context.mark_created("vm_dir")
        creation_context.mark_created("tap")

        assert creation_context.was_created("vm_dir") is True
        assert creation_context.was_created("tap") is True

    def test_was_created_false(self, creation_context):
        """Test was_created returns False for non-created resources."""
        creation_context.mark_created("vm_dir")

        assert creation_context.was_created("tap") is False
        assert creation_context.was_created("network_ip") is False

    def test_was_created_empty(self, creation_context):
        """Test was_created returns False for empty resources."""
        assert creation_context.was_created("vm_dir") is False
        assert creation_context.was_created("anything") is False

    @patch("mvmctl.api.vm._registry.logger")
    def test_cleanup_closes_log_fp(self, mock_logger, creation_context, tmp_path):
        """Test cleanup closes log file pointer."""
        mock_fp = MagicMock()
        creation_context.log_fp = mock_fp
        creation_context.mark_created("vm_dir")
        creation_context.vm_dir = tmp_path / "vm"
        creation_context.vm_dir.mkdir()

        with patch("shutil.rmtree"):
            _perform_creation_cleanup(creation_context)

        mock_fp.close.assert_called_once()

    @patch("mvmctl.api.vm._registry.logger")
    def test_cleanup_closes_console_fp(self, mock_logger, creation_context, tmp_path):
        """Test cleanup closes console file pointer."""
        mock_fp = MagicMock()
        creation_context.console_fp = mock_fp
        creation_context.mark_created("vm_dir")
        creation_context.vm_dir = tmp_path / "vm"
        creation_context.vm_dir.mkdir()

        with patch("shutil.rmtree"):
            _perform_creation_cleanup(creation_context)

        mock_fp.close.assert_called_once()

    @patch("mvmctl.api.vm._registry.logger")
    def test_cleanup_handles_close_errors(self, mock_logger, creation_context, tmp_path):
        """Test cleanup handles file close errors gracefully."""
        mock_fp = MagicMock()
        mock_fp.close.side_effect = OSError("Close failed")
        creation_context.log_fp = mock_fp
        creation_context.mark_created("vm_dir")
        creation_context.vm_dir = tmp_path / "vm"
        creation_context.vm_dir.mkdir()

        with patch("shutil.rmtree"):
            _perform_creation_cleanup(creation_context)

        mock_logger.warning.assert_called()

    def test_cleanup_stops_nocloud_server(self, creation_context, mock_resolved):
        """Test cleanup stops nocloud server if created."""
        mock_net_manager = MagicMock()
        creation_context.net_manager = mock_net_manager
        creation_context.mark_created("nocloud_server")
        mock_resolved.name = "test-vm"
        mock_resolved.vm_id = "abc123"

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        mock_net_manager.stop_server.assert_called_once_with("test-vm", "abc123")

    @patch("mvmctl.api.vm._registry.logger")
    def test_cleanup_handles_nocloud_stop_error(self, mock_logger, creation_context, mock_resolved):
        """Test cleanup handles nocloud stop errors gracefully."""
        mock_net_manager = MagicMock()
        mock_net_manager.stop_server.side_effect = Exception("Stop failed")
        creation_context.net_manager = mock_net_manager
        creation_context.mark_created("nocloud_server")
        mock_resolved.name = "test-vm"
        mock_resolved.vm_id = "abc123"

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        mock_logger.warning.assert_called()

    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    def test_cleanup_removes_firewall_rule(self, mock_remove_rule, creation_context, mock_resolved):
        """Test cleanup removes firewall rule if created."""
        creation_context.guest_ip = "10.20.0.5"
        creation_context.nocloud_net_port = 8080
        creation_context.mark_created("firewall_rule")
        mock_resolved.name = "test-vm"

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        mock_remove_rule.assert_called_once_with("10.20.0.5", "test-vm", 8080)

    @patch("mvmctl.api.vm._registry.logger")
    @patch("mvmctl.core.firewall.remove_nocloud_input_rule")
    def test_cleanup_handles_firewall_error(
        self, mock_remove_rule, mock_logger, creation_context, mock_resolved
    ):
        """Test cleanup handles firewall removal errors gracefully."""
        from mvmctl.exceptions import NetworkError

        mock_remove_rule.side_effect = NetworkError("Remove failed")
        creation_context.guest_ip = "10.20.0.5"
        creation_context.nocloud_net_port = 8080
        creation_context.mark_created("firewall_rule")
        mock_resolved.name = "test-vm"

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        mock_logger.warning.assert_called()

    @patch("mvmctl.core.vm_process.cleanup_tap")
    @patch("mvmctl.api.network.get_network")
    def test_cleanup_cleans_tap(
        self, mock_get_network, mock_cleanup_tap, creation_context, mock_resolved
    ):
        """Test cleanup cleans TAP device if created."""
        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-br0"
        mock_get_network.return_value = mock_net_config
        creation_context.tap_name = "mvm-tap0"
        creation_context.mark_created("tap")
        mock_resolved.network_name = "default"

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        mock_cleanup_tap.assert_called_once_with("mvm-tap0", bridge="mvm-br0")

    @patch("mvmctl.api.network.release_network_ip")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    def test_cleanup_releases_network_ip(
        self, mock_db_class, mock_release_ip, creation_context, mock_resolved
    ):
        """Test cleanup releases network IP if created."""
        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db_net = MagicMock()
        mock_db_net.id = "net-123"
        mock_db.get_network_by_name.return_value = mock_db_net
        creation_context.mark_created("network_ip")
        mock_resolved.network_name = "default"
        mock_resolved.vm_id = "vm-123"

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        mock_release_ip.assert_called_once_with("net-123", "vm-123")

    @patch("mvmctl.api.vm._registry.logger")
    @patch("mvmctl.core.mvm_db.MVMDatabase")
    def test_cleanup_handles_ip_release_error(
        self, mock_db_class, mock_logger, creation_context, mock_resolved
    ):
        """Test cleanup handles IP release errors gracefully."""
        from mvmctl.exceptions import NetworkError

        mock_db = MagicMock()
        mock_db_class.return_value = mock_db
        mock_db.get_network_by_name.side_effect = NetworkError("Release failed")
        creation_context.mark_created("network_ip")
        mock_resolved.network_name = "default"

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        mock_logger.warning.assert_called()

    def test_cleanup_stops_console_relay(self, creation_context, mock_resolved):
        """Test cleanup stops console relay if created."""
        mock_relay_mgr = MagicMock()
        creation_context.relay_mgr = mock_relay_mgr
        creation_context.mark_created("console_relay")
        mock_resolved.name = "test-vm"
        mock_resolved.vm_id = "abc123"

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        mock_relay_mgr.stop_relay.assert_called_once_with("test-vm", "abc123")

    @patch("mvmctl.api.vm._registry.logger")
    def test_cleanup_handles_console_relay_error(
        self, mock_logger, creation_context, mock_resolved
    ):
        """Test cleanup handles console relay stop errors gracefully."""
        mock_relay_mgr = MagicMock()
        mock_relay_mgr.stop_relay.side_effect = Exception("Stop failed")
        creation_context.relay_mgr = mock_relay_mgr
        creation_context.mark_created("console_relay")
        mock_resolved.name = "test-vm"
        mock_resolved.vm_id = "abc123"

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        mock_logger.warning.assert_called()

    @patch("mvmctl.api.vm._registry.os.close")
    def test_cleanup_closes_pty_fds(self, mock_close, creation_context):
        """Test cleanup closes PTY file descriptors."""
        creation_context.pty_slave_fd = 5
        creation_context.pty_master_fd = 6
        creation_context.mark_created("vm_dir")

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        assert mock_close.call_count == 2

    @patch("mvmctl.api.vm._registry.os.close")
    def test_cleanup_handles_pty_close_errors(self, mock_close, creation_context):
        """Test cleanup handles PTY close errors gracefully."""
        mock_close.side_effect = OSError("Close failed")
        creation_context.pty_slave_fd = 5
        creation_context.pty_master_fd = 6
        creation_context.mark_created("vm_dir")

        with patch("shutil.rmtree"):
            creation_context.cleanup()

        # Should not raise exception

    def test_cleanup_removes_vm_dir(self, creation_context, tmp_path):
        """Test cleanup removes VM directory if created."""
        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()
        (vm_dir / "test-file").write_text("test")
        creation_context.vm_dir = vm_dir
        creation_context.mark_created("vm_dir")

        creation_context.cleanup()

        assert not vm_dir.exists()

    @patch("mvmctl.api.vm._registry.logger")
    def test_cleanup_handles_vm_dir_removal_error(self, mock_logger, creation_context, tmp_path):
        """Test cleanup handles VM directory removal errors gracefully."""
        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()
        creation_context.vm_dir = vm_dir
        creation_context.mark_created("vm_dir")

        with patch("shutil.rmtree", side_effect=OSError("Remove failed")):
            creation_context.cleanup()

        mock_logger.warning.assert_called()

    def test_persist_failed_vm(self, creation_context, mock_resolved):
        """Test persist_failed_vm stores failed VM in DB."""
        mock_manager = MagicMock()
        mock_instance = MagicMock()

        creation_context.persist_failed_vm(mock_instance, mock_manager)

        assert mock_instance.status.value == "error"
        mock_manager.register.assert_called_once_with(mock_instance)

    @patch("mvmctl.api.vm._registry.logger")
    def test_persist_failed_vm_handles_none_manager(self, mock_logger, creation_context):
        """Test persist_failed_vm handles None manager gracefully."""
        mock_instance = MagicMock()

        creation_context.persist_failed_vm(mock_instance, None)

        mock_logger.warning.assert_called_once()
        mock_logger.warning.assert_called_with("Failed to persist failed VM: manager is None")

    @patch("mvmctl.api.vm._registry.logger")
    def test_persist_failed_vm_handles_register_error(self, mock_logger, creation_context):
        """Test persist_failed_vm handles register errors gracefully."""
        mock_manager = MagicMock()
        mock_manager.register.side_effect = Exception("Register failed")
        mock_instance = MagicMock()

        creation_context.persist_failed_vm(mock_instance, mock_manager)

        mock_logger.warning.assert_called()


# =============================================================================
# GuestfsProvisioner Tests
# =============================================================================


class TestGuestfsProvisioner:
    """Tests for GuestfsProvisioner SSH/guestfs setup."""

    @pytest.fixture
    def rootfs_path(self, tmp_path):
        """Create a temporary rootfs path."""
        return tmp_path / "rootfs.ext4"

    @pytest.fixture
    def provisioner(self, rootfs_path):
        """Create a GuestfsProvisioner instance."""
        return GuestfsProvisioner(
            rootfs_path=rootfs_path,
            hostname="test-vm",
            user="testuser",
            ssh_pub_key=[
                "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDIhz2GK/XCUj4i6Q5yQJNL1MXMY0RxzPV2QrBqfHrD+ test@example.com"
            ],
        )

    def test_init(self, rootfs_path):
        """Test GuestfsProvisioner initialization."""
        prov = GuestfsProvisioner(
            rootfs_path=rootfs_path,
            hostname="my-vm",
            user="myuser",
            ssh_pub_key="single-key",
        )

        assert prov._rootfs_path == rootfs_path
        assert prov._hostname == "my-vm"
        assert prov._user == "myuser"
        assert prov._ssh_pub_key == "single-key"
        assert prov._guestfs_handle is None

    def test_init_with_list_keys(self, rootfs_path):
        """Test GuestfsProvisioner initialization with list of keys."""
        keys = ["key1", "key2"]
        prov = GuestfsProvisioner(
            rootfs_path=rootfs_path,
            hostname="my-vm",
            user="myuser",
            ssh_pub_key=keys,
        )

        assert prov._ssh_pub_key == keys

    def test_init_with_none_keys(self, rootfs_path):
        """Test GuestfsProvisioner initialization with None keys."""
        prov = GuestfsProvisioner(
            rootfs_path=rootfs_path,
            hostname="my-vm",
            user="myuser",
            ssh_pub_key=None,
        )

        assert prov._ssh_pub_key is None

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    def test_provision_raises_when_libguestfs_missing(self, mock_check, provisioner):
        """Test provision raises when libguestfs is not available."""
        mock_check.return_value = False

        with pytest.raises(VMCreateError, match="libguestfs required"):
            provisioner.provision()

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_with_resize(self, mock_guestfs_ctx, mock_check, provisioner, rootfs_path):
        """Test provision resizes rootfs when target_size_bytes provided."""
        mock_check.return_value = True

        # Create a small file to simulate rootfs
        rootfs_path.write_bytes(b"\x00" * 1024)

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.return_value = False
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision(target_size_bytes=2048)

        # File should be truncated to target size
        assert rootfs_path.stat().st_size == 2048

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_finds_root_device(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision finds root device from filesystems."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/sda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.return_value = False
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        mock_guestfs._g.mount.assert_called_with("/dev/sda1", "/")

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_raises_when_no_filesystem(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision raises when no filesystem found."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {}
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        with pytest.raises(VMCreateError, match="No filesystem found"):
            provisioner.provision()

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_sets_up_ssh_keys(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision sets up SSH authorized_keys."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.side_effect = lambda path: path == "/etc/ssh/sshd_config"
        mock_guestfs._g.read_file.return_value = ""
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        # Should create .ssh directory
        mock_guestfs._g.mkdir_p.assert_any_call("/home/testuser/.ssh")

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_sets_hostname(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision sets hostname in /etc/hostname."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.return_value = False
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        mock_guestfs._g.write.assert_any_call("/etc/hostname", "test-vm")

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_sets_up_dns(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision sets up DNS in /etc/resolv.conf."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.return_value = False
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        # Should write resolv.conf
        mock_guestfs._g.write.assert_any_call("/etc/resolv.conf", "nameserver 1.1.1.1\n")

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_skips_dns_if_exists(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision skips DNS setup if resolv.conf already has nameserver."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.side_effect = lambda path: path == "/etc/resolv.conf"
        mock_guestfs._g.read_file.return_value = "nameserver 8.8.8.8\n"
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        # Should not write resolv.conf
        for call in mock_guestfs._g.write.call_args_list:
            assert call[0][0] != "/etc/resolv.conf"

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_sets_up_hosts_file(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision sets up /etc/hosts with hostname."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.return_value = False
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        # Should write hosts file with 127.0.1.1 entry
        hosts_write_calls = [
            call for call in mock_guestfs._g.write.call_args_list if call[0][0] == "/etc/hosts"
        ]
        assert len(hosts_write_calls) == 1
        assert "127.0.1.1\ttest-vm" in hosts_write_calls[0][0][1]

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_appends_to_existing_hosts(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision appends hostname to existing hosts file."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.side_effect = lambda path: path == "/etc/hosts"
        mock_guestfs._g.read_file.return_value = "127.0.0.1\tlocalhost\n"
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        # Should write hosts file preserving existing entries
        hosts_write_calls = [
            call for call in mock_guestfs._g.write.call_args_list if call[0][0] == "/etc/hosts"
        ]
        assert len(hosts_write_calls) == 1
        content = hosts_write_calls[0][0][1]
        assert "127.0.0.1\tlocalhost" in content
        assert "127.0.1.1\ttest-vm" in content

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_updates_existing_127_0_1_1(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision updates existing 127.0.1.1 entry in hosts file."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.side_effect = lambda path: path == "/etc/hosts"
        mock_guestfs._g.read_file.return_value = "127.0.1.1\told-hostname\n"
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        # Should update 127.0.1.1 entry
        hosts_write_calls = [
            call for call in mock_guestfs._g.write.call_args_list if call[0][0] == "/etc/hosts"
        ]
        assert len(hosts_write_calls) == 1
        content = hosts_write_calls[0][0][1]
        assert "127.0.1.1\ttest-vm" in content
        assert "old-hostname" not in content

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_disables_cloud_init(self, mock_guestfs_ctx, mock_check, provisioner):
        """Test provision disables cloud-init services."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.return_value = False
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        # Should create cloud-init disabled marker
        mock_guestfs._g.write.assert_any_call(
            "/etc/cloud/cloud-init.disabled", "disabled by mvmctl\n"
        )

    @patch("mvmctl.utils.guestfs.check_libguestfs")
    @patch("mvmctl.utils.guestfs.optimized_guestfs")
    def test_provision_creates_ssh_installer_service(
        self, mock_guestfs_ctx, mock_check, provisioner
    ):
        """Test provision creates first-boot SSH installer service."""
        mock_check.return_value = True

        mock_guestfs = MagicMock()
        mock_guestfs._g.list_filesystems.return_value = {"/dev/vda1": "ext4"}
        mock_guestfs._g.vfs_type.return_value = "ext4"
        mock_guestfs._g.exists.return_value = False
        mock_guestfs_ctx.return_value.__enter__ = MagicMock(return_value=mock_guestfs)
        mock_guestfs_ctx.return_value.__exit__ = MagicMock(return_value=False)

        provisioner.provision()

        # Should create first-boot SSH installer service
        service_writes = [
            call
            for call in mock_guestfs._g.write.call_args_list
            if call[0][0] == "/etc/systemd/system/first-boot-ssh-installer.service"
        ]
        assert len(service_writes) == 1
        content = service_writes[0][0][1]
        assert "First-boot SSH installer" in content


# =============================================================================
# CloudInitProvisioner Tests
# =============================================================================


class TestCloudInitProvisioner:
    """Tests for CloudInitProvisioner cloud-init mode handling."""

    @pytest.fixture
    def provisioner(self):
        """Create a CloudInitProvisioner instance."""
        return CloudInitProvisioner()

    @pytest.fixture
    def net_config(self):
        """Create a NetworkConfig for tests."""
        return NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
        )

    def test_provision_off_mode(self, provisioner):
        """Test provision returns empty result for OFF mode."""
        result = provisioner.provision(
            mode=CloudInitMode.OFF,
            vm_dir=Path("/tmp/vm"),
            guest_ip="10.20.0.5",
            user="testuser",
            ssh_pub_key=None,
            user_data=None,
            net_config=MagicMock(),
            vm_id="abc123",
            nocloud_net_port=None,
            cloud_init_iso_path=None,
            keep_cloud_init_iso=False,
        )

        assert isinstance(result, CloudInitProvisionResult)
        assert result.iso_path is None
        assert result.nocloud_url is None
        assert result.nocloud_port == 0
        assert result.nocloud_pid is None

    @patch("mvmctl.core.cloud_init.write_cloud_init")
    @patch("mvmctl.services.nocloud_server.manager.NoCloudNetServerManager")
    def test_provision_net_mode(
        self, mock_manager_class, mock_write, provisioner, net_config, tmp_path
    ):
        """Test provision starts nocloud server for NET mode."""
        mock_manager = MagicMock()
        mock_manager.start_server.return_value = ("http://10.20.0.1:8080", 8080)
        mock_manager.get_server_pid.return_value = 1234
        mock_manager_class.return_value = mock_manager

        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()

        result = provisioner.provision(
            mode=CloudInitMode.NET,
            vm_dir=vm_dir,
            guest_ip="10.20.0.5",
            user="testuser",
            ssh_pub_key=["ssh-key"],
            user_data=None,
            net_config=net_config,
            vm_id="abc123",
            nocloud_net_port=8080,
            cloud_init_iso_path=None,
            keep_cloud_init_iso=False,
        )

        assert result.nocloud_url == "http://10.20.0.1:8080"
        assert result.nocloud_port == 8080
        assert result.nocloud_pid == 1234
        mock_manager.start_server.assert_called_once()

    @patch("mvmctl.core.cloud_init.write_cloud_init")
    @patch("mvmctl.core.cloud_init.create_cloud_init_iso")
    def test_provision_iso_mode(
        self, mock_create_iso, mock_write, provisioner, net_config, tmp_path
    ):
        """Test provision creates ISO for ISO mode."""
        mock_create_iso.return_value = None

        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()

        result = provisioner.provision(
            mode=CloudInitMode.ISO,
            vm_dir=vm_dir,
            guest_ip="10.20.0.5",
            user="testuser",
            ssh_pub_key=["ssh-key"],
            user_data=None,
            net_config=net_config,
            vm_id="abc123",
            nocloud_net_port=None,
            cloud_init_iso_path=None,
            keep_cloud_init_iso=False,
        )

        assert result.iso_path is not None
        assert result.iso_path.name == "cloud-init.iso"
        mock_create_iso.assert_called_once()

    def test_provision_iso_mode_with_custom_path(self, provisioner, net_config, tmp_path):
        """Test provision uses custom ISO path when provided."""
        custom_iso = tmp_path / "custom.iso"
        custom_iso.write_text("fake iso")

        result = provisioner.provision(
            mode=CloudInitMode.ISO,
            vm_dir=tmp_path / "vm",
            guest_ip="10.20.0.5",
            user="testuser",
            ssh_pub_key=None,
            user_data=None,
            net_config=net_config,
            vm_id="abc123",
            nocloud_net_port=None,
            cloud_init_iso_path=custom_iso,
            keep_cloud_init_iso=False,
        )

        assert result.iso_path == custom_iso

    def test_provision_iso_mode_missing_custom_path(self, provisioner, net_config, tmp_path):
        """Test provision raises when custom ISO path doesn't exist."""
        custom_iso = tmp_path / "nonexistent.iso"

        with pytest.raises(Exception, match="Custom cloud-init ISO not found"):
            provisioner.provision(
                mode=CloudInitMode.ISO,
                vm_dir=tmp_path / "vm",
                guest_ip="10.20.0.5",
                user="testuser",
                ssh_pub_key=None,
                user_data=None,
                net_config=net_config,
                vm_id="abc123",
                nocloud_net_port=None,
                cloud_init_iso_path=custom_iso,
                keep_cloud_init_iso=False,
            )

    @patch("mvmctl.core.cloud_init.write_cloud_init")
    @patch("mvmctl.core.rootfs_injector.inject_cloud_init")
    def test_provision_inject_mode(
        self, mock_inject, mock_write, provisioner, net_config, tmp_path
    ):
        """Test provision injects cloud-init for INJECT mode."""
        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()
        rootfs = vm_dir / "rootfs.ext4"
        rootfs.write_text("fake rootfs")

        result = provisioner.provision(
            mode=CloudInitMode.INJECT,
            vm_dir=vm_dir,
            guest_ip="10.20.0.5",
            user="testuser",
            ssh_pub_key=["ssh-key"],
            user_data=None,
            net_config=net_config,
            vm_id="abc123",
            nocloud_net_port=None,
            cloud_init_iso_path=None,
            keep_cloud_init_iso=False,
        )

        assert isinstance(result, CloudInitProvisionResult)
        mock_inject.assert_called_once()

    @patch("mvmctl.core.cloud_init.write_cloud_init")
    @patch("mvmctl.core.rootfs_injector.inject_cloud_init")
    def test_provision_inject_mode_raises_on_error(
        self, mock_inject, mock_write, provisioner, net_config, tmp_path
    ):
        """Test provision raises CloudInitError on injection failure."""
        from mvmctl.exceptions import CloudInitError

        mock_inject.side_effect = Exception("Injection failed")

        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()
        rootfs = vm_dir / "rootfs.ext4"
        rootfs.write_text("fake rootfs")

        with pytest.raises(CloudInitError, match="Direct injection failed"):
            provisioner.provision(
                mode=CloudInitMode.INJECT,
                vm_dir=vm_dir,
                guest_ip="10.20.0.5",
                user="testuser",
                ssh_pub_key=None,
                user_data=None,
                net_config=net_config,
                vm_id="abc123",
                nocloud_net_port=None,
                cloud_init_iso_path=None,
                keep_cloud_init_iso=False,
            )


# =============================================================================
# CloudInitProvisionResult Tests
# =============================================================================


class TestCloudInitProvisionResult:
    """Tests for CloudInitProvisionResult dataclass."""

    def test_default_values(self):
        """Test default values for CloudInitProvisionResult."""
        result = CloudInitProvisionResult()

        assert result.iso_path is None
        assert result.nocloud_url is None
        assert result.nocloud_port == 0
        assert result.nocloud_pid is None

    def test_custom_values(self):
        """Test custom values for CloudInitProvisionResult."""
        result = CloudInitProvisionResult(
            iso_path=Path("/tmp/iso"),
            nocloud_url="http://10.0.0.1:8080",
            nocloud_port=8080,
            nocloud_pid=1234,
        )

        assert result.iso_path == Path("/tmp/iso")
        assert result.nocloud_url == "http://10.0.0.1:8080"
        assert result.nocloud_port == 8080
        assert result.nocloud_pid == 1234
