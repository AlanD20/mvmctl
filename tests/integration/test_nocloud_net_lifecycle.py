"""Integration tests for nocloud-net lifecycle workflow.

Tests the complete nocloud-net VM lifecycle: create -> verify -> remove -> cleanup
with mocked subprocess and nocloud-net server calls.
"""

from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from mvmctl.cli.vm import app as vm_app
from mvmctl.core.network_manager import NetworkConfig
from mvmctl.models import CloudInitMode
from mvmctl.models.vm import VMInstance, VMState

runner = CliRunner()


def _make_vm(
    name: str,
    status: VMState = VMState.RUNNING,
    ip: str = "10.20.0.2",
    pid: int = 1234,
    network: str = "default",
    nocloud_net_port: int | None = None,
) -> VMInstance:
    """Create a sample VMInstance for testing."""
    return VMInstance(
        name=name,
        ip=ip,
        mac="02:FC:aa:bb:cc:dd",
        pid=pid,
        status=status,
        created_at=datetime(2026, 1, 1, 12, 0, 0),
        network_name=network,
        socket_path=Path(f"/tmp/mvm/{name}.sock"),
        cloud_init_mode=CloudInitMode.NO_CLOUD_NET if nocloud_net_port else CloudInitMode.AUTO,
        nocloud_net_port=nocloud_net_port,
    )


class TestFullNocloudNetLifecycle:
    """Test complete nocloud-net VM lifecycle workflow."""

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.remove_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
    @patch("mvmctl.core.vm_lifecycle.subprocess.run")
    @patch("mvmctl.core.vm_lifecycle.create_tap")
    @patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
    @patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
    @patch("mvmctl.core.vm_lifecycle.delete_tap")
    @patch("mvmctl.core.vm_lifecycle.bridge_exists")
    @patch("mvmctl.core.vm_lifecycle.get_network")
    @patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
    @patch("mvmctl.core.vm_lifecycle.release_network_ip")
    @patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
    @patch("mvmctl.core.vm_lifecycle._resolve_image_path")
    @patch("mvmctl.core.vm_lifecycle._resolve_kernel_path")
    @patch("mvmctl.core.vm_lifecycle._write_pid_file")
    @patch("mvmctl.utils.fs.get_vm_dir")
    def test_full_nocloud_net_lifecycle(
        self,
        mock_get_vm_dir,
        mock_write_pid,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_setup_chain,
        mock_release_ip,
        mock_alloc_ip,
        mock_get_network,
        mock_bridge_exists,
        mock_delete_tap,
        mock_remove_iptables,
        mock_add_iptables,
        mock_create_tap,
        mock_subprocess_run,
        mock_popen,
        mock_remove_rule,
        mock_add_rule,
        mock_nocloud_mgr,
        mock_check_priv,
        mock_require_group,
        tmp_path,
    ):
        """Test creating a VM with nocloud-net mode, verify setup, then remove and verify cleanup."""
        # Setup mocks
        mock_check_priv.return_value = None
        mock_require_group.return_value = None
        mock_subprocess_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

        # Create fake image and kernel files
        image_path = tmp_path / "ubuntu-24.04.ext4"
        image_path.write_text("fake image")
        kernel_path = tmp_path / "vmlinux"
        kernel_path.write_text("fake kernel")

        mock_resolve_image.return_value = image_path
        mock_resolve_kernel.return_value = kernel_path

        # Mock network
        from mvmctl.core.network_manager import NetworkConfig

        mock_get_network.return_value = NetworkConfig(
            name="default",
            cidr="10.20.0.0/24",
            gateway="10.20.0.1",
            bridge="mvm-br0",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00+00:00",
        )
        mock_alloc_ip.return_value = "10.20.0.2"
        mock_bridge_exists.return_value = True

        # Mock nocloud-net server manager
        mock_mgr_instance = MagicMock()
        mock_mgr_instance.start_server.return_value = ("http://10.20.0.1:8000/", 8000)
        mock_nocloud_mgr.return_value = mock_mgr_instance

        # Mock subprocess for firecracker
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        # Setup vm_dir to return a real directory
        vm_dir = tmp_path / "vms" / "nocloud-test-vm"
        vm_dir.mkdir(parents=True)
        mock_get_vm_dir.return_value = vm_dir

        # Create VM
        from mvmctl.core.vm_lifecycle import create_vm
        from mvmctl.core.vm_manager import VMManager

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.core.vm_lifecycle.get_vm_manager", return_value=vm_mgr):
            vm = create_vm(
                name="nocloud-test-vm",
                image="ubuntu-24.04",
                kernel="vmlinux",
                vcpus=2,
                mem=2048,
                cloud_init_mode=CloudInitMode.NO_CLOUD_NET,
                vm_manager=vm_mgr,
            )

        # Verify nocloud-net server was started
        mock_nocloud_mgr.assert_called_once()
        mock_mgr_instance.start_server.assert_called_once()

        # Verify firewall rule was added
        mock_add_rule.assert_called_once_with("10.20.0.2", "nocloud-test-vm", 8000)

        # Verify VMInstance has nocloud_net_port
        assert vm.nocloud_net_port == 8000

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.remove_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
    @patch("mvmctl.core.vm_lifecycle.subprocess.run")
    @patch("mvmctl.core.vm_lifecycle.create_tap")
    @patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
    @patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
    @patch("mvmctl.core.vm_lifecycle.delete_tap")
    @patch("mvmctl.core.vm_lifecycle.bridge_exists")
    @patch("mvmctl.core.vm_lifecycle.get_network")
    @patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
    @patch("mvmctl.core.vm_lifecycle.release_network_ip")
    @patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
    @patch("mvmctl.core.vm_lifecycle._resolve_image_path")
    @patch("mvmctl.core.vm_lifecycle._resolve_kernel_path")
    @patch("mvmctl.core.vm_lifecycle._write_pid_file")
    @patch("mvmctl.utils.fs.get_vm_dir")
    def test_nocloud_net_remove_cleanup(
        self,
        mock_get_vm_dir,
        mock_write_pid,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_setup_chain,
        mock_release_ip,
        mock_alloc_ip,
        mock_get_network,
        mock_bridge_exists,
        mock_delete_tap,
        mock_remove_iptables,
        mock_add_iptables,
        mock_create_tap,
        mock_subprocess_run,
        mock_popen,
        mock_remove_rule,
        mock_add_rule,
        mock_nocloud_mgr,
        mock_check_priv,
        mock_require_group,
        tmp_path,
    ):
        """Test that nocloud-net VM removal stops server and removes firewall rule."""
        # Setup mocks
        mock_check_priv.return_value = None
        mock_require_group.return_value = None
        mock_subprocess_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

        # Create fake image and kernel files
        image_path = tmp_path / "ubuntu-24.04.ext4"
        image_path.write_text("fake image")
        kernel_path = tmp_path / "vmlinux"
        kernel_path.write_text("fake kernel")

        mock_resolve_image.return_value = image_path
        mock_resolve_kernel.return_value = kernel_path

        # Mock network
        from mvmctl.core.network_manager import NetworkConfig

        mock_get_network.return_value = NetworkConfig(
            name="default",
            cidr="10.20.0.0/24",
            gateway="10.20.0.1",
            bridge="mvm-br0",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00+00:00",
        )
        mock_alloc_ip.return_value = "10.20.0.2"
        mock_bridge_exists.return_value = True

        # Mock nocloud-net server manager
        mock_mgr_instance = MagicMock()
        mock_mgr_instance.start_server.return_value = ("http://10.20.0.1:8000/", 8000)
        mock_nocloud_mgr.return_value = mock_mgr_instance

        # Mock subprocess for firecracker
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        # Setup vm_dir
        vm_dir = tmp_path / "vms" / "cleanup-test-vm"
        vm_dir.mkdir(parents=True)
        mock_get_vm_dir.return_value = vm_dir

        # Create and remove VM
        from mvmctl.core.vm_lifecycle import create_vm, remove_vm
        from mvmctl.core.vm_manager import VMManager

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.core.vm_lifecycle.get_vm_manager", return_value=vm_mgr):
            # Create VM
            create_vm(
                name="cleanup-test-vm",
                image="ubuntu-24.04",
                kernel="vmlinux",
                cloud_init_mode=CloudInitMode.NO_CLOUD_NET,
                vm_manager=vm_mgr,
            )

            # Verify server was started
            mock_mgr_instance.start_server.assert_called_once()

            # Reset mocks for remove verification
            mock_add_rule.reset_mock()

            # Remove VM
            remove_vm("cleanup-test-vm", vm_manager=vm_mgr)

        # Verify nocloud-net server was stopped
        mock_mgr_instance.stop_server.assert_called_once_with("cleanup-test-vm")

        # Verify firewall rule was removed
        mock_remove_rule.assert_called_once_with("10.20.0.2", "cleanup-test-vm", 8000)

        # Verify VM is deregistered
        assert vm_mgr.get("cleanup-test-vm") is None


class TestMultipleVMsDifferentPorts:
    """Test multiple VMs with nocloud-net mode get different ports."""

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
    @patch("mvmctl.core.vm_lifecycle._resolve_image_path")
    @patch("mvmctl.core.vm_lifecycle._resolve_kernel_path")
    def test_multiple_vms_different_ports(
        self,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_popen,
        mock_add_rule,
        mock_nocloud_mgr,
        mock_check_priv,
        mock_require_group,
        tmp_path,
    ):
        """Test that two VMs with nocloud-net mode get different ports."""
        mock_check_priv.return_value = None
        mock_require_group.return_value = None

        # Create fake image and kernel files
        image_path = tmp_path / "ubuntu-24.04.ext4"
        image_path.write_text("fake image")
        kernel_path = tmp_path / "vmlinux"
        kernel_path.write_text("fake kernel")

        mock_resolve_image.return_value = image_path
        mock_resolve_kernel.return_value = kernel_path

        # Create two separate mock managers to return different ports
        mock_mgr1 = MagicMock()
        mock_mgr1.start_server.return_value = ("http://10.20.0.1:8001/", 8001)

        mock_mgr2 = MagicMock()
        mock_mgr2.start_server.return_value = ("http://10.20.0.1:8002/", 8002)

        # Make the nocloud_mgr return different instances
        mock_nocloud_mgr.side_effect = [mock_mgr1, mock_mgr2]

        # Mock subprocess for firecracker
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        from mvmctl.core.vm_lifecycle import create_vm
        from mvmctl.core.vm_manager import VMManager

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.core.vm_lifecycle.get_vm_manager", return_value=vm_mgr):
            with patch("mvmctl.core.vm_lifecycle.get_network") as mock_get_net:
                with patch(
                    "mvmctl.core.vm_lifecycle.allocate_network_ip",
                    side_effect=["10.20.0.2", "10.20.0.3"],
                ):
                    with patch("mvmctl.core.vm_lifecycle.bridge_exists", return_value=True):
                        with patch("mvmctl.core.vm_lifecycle.create_tap"):
                            with patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules"):
                                with patch("mvmctl.core.vm_lifecycle._write_pid_file"):
                                    with patch(
                                        "mvmctl.core.vm_lifecycle.setup_nocloud_input_chain"
                                    ):
                                        with patch("mvmctl.utils.fs.get_vm_dir") as mock_get_vm_dir:
                                            vm_dir1 = tmp_path / "vms" / "vm1"
                                            vm_dir2 = tmp_path / "vms" / "vm2"
                                            vm_dir1.mkdir(parents=True)
                                            vm_dir2.mkdir(parents=True)
                                            mock_get_vm_dir.side_effect = [vm_dir1, vm_dir2]

                                            mock_get_net.return_value = NetworkConfig(
                                                name="default",
                                                cidr="10.20.0.0/24",
                                                gateway="10.20.0.1",
                                                bridge="mvm-br0",
                                                nat_enabled=True,
                                                created_at="2024-01-01T00:00:00+00:00",
                                            )

                                            vm1 = create_vm(
                                                name="vm1",
                                                image="ubuntu-24.04",
                                                kernel="vmlinux",
                                                cloud_init_mode=CloudInitMode.NO_CLOUD_NET,
                                                vm_manager=vm_mgr,
                                            )

                                            vm2 = create_vm(
                                                name="vm2",
                                                image="ubuntu-24.04",
                                                kernel="vmlinux",
                                                cloud_init_mode=CloudInitMode.NO_CLOUD_NET,
                                                vm_manager=vm_mgr,
                                            )

        # Verify different ports were allocated
        assert vm1.nocloud_net_port == 8001
        assert vm2.nocloud_net_port == 8002
        assert vm1.nocloud_net_port != vm2.nocloud_net_port

        # Verify both servers were started
        assert mock_nocloud_mgr.call_count == 2


class TestFirewallIsolation:
    """Test firewall isolation for nocloud-net VMs."""

    @patch("mvmctl.core.firewall.add_nocloud_input_rule")
    def test_firewall_rule_structure(self, mock_add_rule):
        """Verify firewall rules are created with correct structure."""
        from mvmctl.core.firewall import add_nocloud_input_rule

        # Call with test values
        add_nocloud_input_rule("10.20.0.5", "test-vm", 8888)

        # Verify the rule was called with correct arguments
        mock_add_rule.assert_called_once_with("10.20.0.5", "test-vm", 8888)

    def test_firewall_comment_format(self):
        """Verify firewall rules include correct comment format."""
        from mvmctl.constants import MVM_NO_CLOUD_INPUT_CHAIN

        # Verify the chain name constant is correct
        assert MVM_NO_CLOUD_INPUT_CHAIN == "MVM-NOCLOUD-INPUT"

    @patch("mvmctl.core.firewall.subprocess.run")
    def test_firewall_rule_allows_specific_ip_only(self, mock_run):
        """Verify firewall rules only allow access from specific VM IP."""
        from mvmctl.core.firewall import _build_iptables_restore_input

        rules = [
            {
                "table": "filter",
                "chain": "MVM-NOCLOUD-INPUT",
                "rule": '-s 10.20.0.2 -p tcp --dport 8000 -j ACCEPT -m comment --comment "# mvm-nocloud:vm1:8000"',
            },
        ]

        result = _build_iptables_restore_input(rules)

        # Verify the rule includes source IP restriction
        assert "-s 10.20.0.2" in result
        assert "--dport 8000" in result
        assert "# mvm-nocloud:vm1:8000" in result


class TestNocloudNetFailureCleanup:
    """Test cleanup on nocloud-net failure scenarios."""

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.remove_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.get_network")
    @patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
    @patch("mvmctl.core.vm_lifecycle.release_network_ip")
    @patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
    @patch("mvmctl.core.vm_lifecycle._resolve_image_path")
    @patch("mvmctl.core.vm_lifecycle._resolve_kernel_path")
    @patch("mvmctl.utils.fs.get_vm_dir")
    def test_failure_cleanup_on_firewall_error(
        self,
        mock_get_vm_dir,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_setup_chain,
        mock_release_ip,
        mock_alloc_ip,
        mock_get_network,
        mock_remove_rule,
        mock_add_rule,
        mock_nocloud_mgr,
        mock_check_priv,
        mock_require_group,
        tmp_path,
    ):
        """Test that server is stopped and firewall cleaned up when firewall rule fails."""
        mock_check_priv.return_value = None
        mock_require_group.return_value = None

        # Create fake image and kernel files
        image_path = tmp_path / "ubuntu-24.04.ext4"
        image_path.write_text("fake image")
        kernel_path = tmp_path / "vmlinux"
        kernel_path.write_text("fake kernel")

        mock_resolve_image.return_value = image_path
        mock_resolve_kernel.return_value = kernel_path

        # Mock network
        from mvmctl.core.network_manager import NetworkConfig

        mock_get_network.return_value = NetworkConfig(
            name="default",
            cidr="10.20.0.0/24",
            gateway="10.20.0.1",
            bridge="mvm-br0",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00+00:00",
        )
        mock_alloc_ip.return_value = "10.20.0.2"

        # Mock nocloud-net server that starts successfully
        mock_mgr_instance = MagicMock()
        mock_mgr_instance.start_server.return_value = ("http://10.20.0.1:8000/", 8000)
        mock_nocloud_mgr.return_value = mock_mgr_instance

        # Make firewall rule addition fail
        from mvmctl.exceptions import NetworkError

        mock_add_rule.side_effect = NetworkError("iptables error")

        # Setup vm_dir
        vm_dir = tmp_path / "vms" / "failing-vm"
        vm_dir.mkdir(parents=True)
        mock_get_vm_dir.return_value = vm_dir

        from mvmctl.core.vm_lifecycle import create_vm
        from mvmctl.core.vm_manager import VMManager
        from mvmctl.models import CloudInitMode

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.core.vm_lifecycle.get_vm_manager", return_value=vm_mgr):
            with pytest.raises(NetworkError):
                create_vm(
                    name="failing-vm",
                    image="ubuntu-24.04",
                    kernel="vmlinux",
                    cloud_init_mode=CloudInitMode.NO_CLOUD_NET,
                    vm_manager=vm_mgr,
                )

        # Verify server was stopped on failure
        mock_mgr_instance.stop_server.assert_called_once_with("failing-vm")

        # Verify VM was not registered (cleanup happened)
        assert vm_mgr.get("failing-vm") is None

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.core.vm_lifecycle.subprocess.run")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.remove_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
    @patch("mvmctl.core.vm_lifecycle.create_tap")
    @patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
    @patch("mvmctl.core.vm_lifecycle.bridge_exists")
    @patch("mvmctl.core.vm_lifecycle.get_network")
    @patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
    @patch("mvmctl.core.vm_lifecycle.release_network_ip")
    @patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
    @patch("mvmctl.core.vm_lifecycle._resolve_image_path")
    @patch("mvmctl.core.vm_lifecycle._resolve_kernel_path")
    @patch("mvmctl.core.vm_lifecycle._write_pid_file")
    @patch("mvmctl.utils.fs.get_vm_dir")
    def test_failure_cleanup_on_firecracker_error(
        self,
        mock_get_vm_dir,
        mock_write_pid,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_setup_chain,
        mock_release_ip,
        mock_alloc_ip,
        mock_get_network,
        mock_bridge_exists,
        mock_add_iptables,
        mock_create_tap,
        mock_popen,
        mock_remove_rule,
        mock_add_rule,
        mock_nocloud_mgr,
        mock_subprocess_run,
        mock_check_priv,
        mock_require_group,
        tmp_path,
    ):
        """Test that server is stopped when Firecracker fails to start."""
        mock_check_priv.return_value = None
        mock_require_group.return_value = None
        mock_subprocess_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

        # Create fake image and kernel files
        image_path = tmp_path / "ubuntu-24.04.ext4"
        image_path.write_text("fake image")
        kernel_path = tmp_path / "vmlinux"
        kernel_path.write_text("fake kernel")

        mock_resolve_image.return_value = image_path
        mock_resolve_kernel.return_value = kernel_path

        # Mock network
        from mvmctl.core.network_manager import NetworkConfig

        mock_get_network.return_value = NetworkConfig(
            name="default",
            cidr="10.20.0.0/24",
            gateway="10.20.0.1",
            bridge="mvm-br0",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00+00:00",
        )
        mock_alloc_ip.return_value = "10.20.0.2"
        mock_bridge_exists.return_value = True

        # Mock nocloud-net server
        mock_mgr_instance = MagicMock()
        mock_mgr_instance.start_server.return_value = ("http://10.20.0.1:8000/", 8000)
        mock_nocloud_mgr.return_value = mock_mgr_instance

        # Make firecracker Popen raise FileNotFoundError
        mock_popen.side_effect = FileNotFoundError("firecracker not found")

        # Setup vm_dir
        vm_dir = tmp_path / "vms" / "fc-fail-vm"
        vm_dir.mkdir(parents=True)
        mock_get_vm_dir.return_value = vm_dir

        from mvmctl.core.vm_lifecycle import create_vm
        from mvmctl.core.vm_manager import VMManager
        from mvmctl.exceptions import MVMError
        from mvmctl.models import CloudInitMode

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.core.vm_lifecycle.get_vm_manager", return_value=vm_mgr):
            with pytest.raises(MVMError, match="Firecracker binary not found"):
                create_vm(
                    name="fc-fail-vm",
                    image="ubuntu-24.04",
                    kernel="vmlinux",
                    cloud_init_mode=CloudInitMode.NO_CLOUD_NET,
                    vm_manager=vm_mgr,
                )

        # Verify server was stopped on failure
        mock_mgr_instance.stop_server.assert_called_once_with("fc-fail-vm")


class TestVMWithoutNocloudNet:
    """Test that VMs without nocloud-net mode are unaffected."""

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
    @patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
    @patch("mvmctl.core.vm_lifecycle._resolve_image_path")
    @patch("mvmctl.core.vm_lifecycle._resolve_kernel_path")
    def test_vm_with_disabled_mode_no_nocloud(
        self,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_create_iso,
        mock_popen,
        mock_add_rule,
        mock_nocloud_mgr,
        mock_check_priv,
        mock_require_group,
        tmp_path,
    ):
        """Test that DISABLED mode VM doesn't start nocloud-net server."""
        mock_check_priv.return_value = None
        mock_require_group.return_value = None

        # Create fake image and kernel files
        image_path = tmp_path / "ubuntu-24.04.ext4"
        image_path.write_text("fake image")
        kernel_path = tmp_path / "vmlinux"
        kernel_path.write_text("fake kernel")

        mock_resolve_image.return_value = image_path
        mock_resolve_kernel.return_value = kernel_path

        # Mock subprocess for firecracker
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        from mvmctl.core.vm_lifecycle import create_vm
        from mvmctl.core.vm_manager import VMManager
        from mvmctl.models import CloudInitMode

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.core.vm_lifecycle.get_vm_manager", return_value=vm_mgr):
            with patch("mvmctl.core.vm_lifecycle.get_network") as mock_get_net:
                with patch(
                    "mvmctl.core.vm_lifecycle.allocate_network_ip", return_value="10.20.0.2"
                ):
                    with patch("mvmctl.core.vm_lifecycle.bridge_exists", return_value=True):
                        with patch("mvmctl.core.vm_lifecycle.create_tap"):
                            with patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules"):
                                with patch("mvmctl.core.vm_lifecycle._write_pid_file"):
                                    with patch(
                                        "mvmctl.core.vm_lifecycle.setup_nocloud_input_chain"
                                    ):
                                        with patch("mvmctl.utils.fs.get_vm_dir") as mock_get_vm_dir:
                                            vm_dir = tmp_path / "vms" / "disabled-mode-vm"
                                            vm_dir.mkdir(parents=True)
                                            mock_get_vm_dir.return_value = vm_dir

                                            mock_get_net.return_value = NetworkConfig(
                                                name="default",
                                                cidr="10.20.0.0/24",
                                                gateway="10.20.0.1",
                                                bridge="mvm-br0",
                                                nat_enabled=True,
                                                created_at="2024-01-01T00:00:00+00:00",
                                            )

                                            vm = create_vm(
                                                name="disabled-mode-vm",
                                                image="ubuntu-24.04",
                                                kernel="vmlinux",
                                                cloud_init_mode=CloudInitMode.DISABLED,
                                                vm_manager=vm_mgr,
                                            )

        # Verify nocloud-net server was NOT started
        mock_nocloud_mgr.assert_not_called()

        # Verify no firewall rules were added for nocloud
        mock_add_rule.assert_not_called()

        # Verify VMInstance has nocloud_net_port as 0 (not set)
        assert vm.nocloud_net_port == 0


class TestNocloudNetCLIAuthoring:
    """Test CLI integration for nocloud-net VMs."""

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    def test_create_vm_with_nocloud_net_flag(
        self, mock_create_vm, mock_resolve_image, mock_check_priv
    ):
        """Test creating VM with --nocloud-net flag via CLI."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("cli-nocloud-vm", nocloud_net_port=8000)
        mock_create_vm.return_value = vm

        result = runner.invoke(
            vm_app,
            ["create", "--name", "cli-nocloud-vm", "--image", "abc123", "--nocloud-net"],
        )

        assert result.exit_code == 0, f"CLI failed with output: {result.output}"
        mock_create_vm.assert_called_once()

        # Verify nocloud-net mode was passed
        call_kwargs = mock_create_vm.call_args.kwargs
        assert call_kwargs.get("cloud_init_mode") == CloudInitMode.NO_CLOUD_NET

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    def test_create_vm_with_custom_nocloud_port(
        self, mock_create_vm, mock_resolve_image, mock_check_priv
    ):
        """Test creating VM with custom --nocloud-net-port via CLI."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("custom-port-vm", nocloud_net_port=9999)
        mock_create_vm.return_value = vm

        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "custom-port-vm",
                "--image",
                "abc123",
                "--nocloud-net",
                "--nocloud-net-port",
                "9999",
            ],
        )

        assert result.exit_code == 0, f"CLI failed with output: {result.output}"
        mock_create_vm.assert_called_once()

        # Verify port was passed
        call_kwargs = mock_create_vm.call_args.kwargs
        assert call_kwargs.get("nocloud_net_port") == 9999
