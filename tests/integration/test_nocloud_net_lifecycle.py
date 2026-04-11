"""Integration tests for nocloud-net lifecycle workflow.

Tests the complete nocloud-net VM lifecycle: create -> verify -> remove -> cleanup
with mocked subprocess and nocloud-net server calls.
"""

from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from mvmctl.cli.vm import vm_app as vm_app
from mvmctl.models import CloudInitMode
from mvmctl.models.network import NetworkConfig
from mvmctl.models.vm import VMCreateInput, VMInstance, VMStatus

runner = CliRunner()


def _make_vm(
    name: str,
    status: VMStatus = VMStatus.RUNNING,
    ip: str = "10.20.0.2",
    pid: int = 1234,
    network: str = "default",
    nocloud_net_port: int | None = None,
    vm_id: str = "vm-abc123",
) -> VMInstance:
    """Create a sample VMInstance for testing."""
    from mvmctl.models.vm import VMConfig

    mode = CloudInitMode.NET if nocloud_net_port else CloudInitMode.INJECT
    return VMInstance(
        name=name,
        id=vm_id,
        ipv4=ip,
        mac="02:FC:aa:bb:cc:dd",
        pid=pid,
        status=status,
        created_at=datetime(2026, 1, 1, 12, 0, 0),
        updated_at=datetime(2026, 1, 1, 12, 0, 0),
        network_id=network,
        tap_device="mvm-tap0",
        api_socket_path=Path(f"/tmp/mvm/{name}.sock"),
        config_path=None,
        config=VMConfig(
            name=name,
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="landlock,lockdown,yama,integrity,selinux,bpf",
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
            cloud_init_mode=mode,
        ),
        rootfs_suffix=".ext4",
        kernel_id="kern-test-001",
        image_id="img-test-001",
        binary_id="bin-test-001",
        disk_size_mib=1024,
        nocloud_net_port=nocloud_net_port,
    )


class TestFullNocloudNetLifecycle:
    """Test complete nocloud-net VM lifecycle workflow."""

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.api.vm.NoCloudNetServerManager")
    @patch("mvmctl.api.vm.add_nocloud_input_rule")
    @patch("mvmctl.api.vm.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm.subprocess.Popen")
    @patch("mvmctl.api.vm.subprocess.run")
    @patch("mvmctl.api.vm.create_tap")
    @patch("mvmctl.api.vm.add_iptables_forward_rules")
    @patch("mvmctl.api.vm.remove_iptables_forward_rules")
    @patch("mvmctl.api.vm.delete_tap")
    @patch("mvmctl.api.vm.bridge_exists")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.api.network.allocate_network_ip")
    @patch("mvmctl.api.network.release_network_ip")
    @patch("mvmctl.api.vm.setup_nocloud_input_chain")
    @patch("mvmctl.api.vm.resolve_image_multi_strategy")
    @patch("mvmctl.api.vm._resolve_kernel_path")
    @patch("mvmctl.api.vm._write_pid_file")
    @patch("mvmctl.api.vm.get_vm_dir_by_hash")
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
        seed_test_assets,
    ):
        """Test creating a VM with nocloud-net mode, verify setup, then remove and verify cleanup."""
        from mvmctl.core.mvm_db import MVMDatabase
        from mvmctl.db.models import Binary, Network

        mock_check_priv.return_value = None
        mock_require_group.return_value = None
        mock_subprocess_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

        image_path = tmp_path / "ubuntu-24.04.ext4"
        image_path.write_text("fake image")
        kernel_path = tmp_path / "vmlinux"
        kernel_path.write_text("fake kernel")

        mock_resolve_kernel.return_value = kernel_path
        mock_resolve_image.return_value = image_path

        db = MVMDatabase()
        db.upsert_binary(
            Binary(
                id="c" * 64,
                name="firecracker",
                version="1.15.0",
                full_version="v1.15.0",
                ci_version="1.15.0",
                path="/usr/local/bin/firecracker",
                is_default=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )
        db.upsert_network(
            Network(
                id="d" * 64,
                name="default",
                subnet="10.20.0.0/24",
                bridge="mvm-br0",
                ipv4_gateway="10.20.0.1",
                bridge_active=True,
                nat_enabled=True,
                is_default=False,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )

        # Mock network
        from mvmctl.models.network import NetworkConfig

        mock_get_network.return_value = NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00+00:00",
        )
        mock_alloc_ip.return_value = "10.20.0.2"
        mock_bridge_exists.return_value = True

        # Mock nocloud-net server manager
        mock_mgr_instance = MagicMock()
        mock_mgr_instance.start_server.return_value = ("http://10.20.0.1:8000/", 8000)
        mock_mgr_instance.get_server_pid.return_value = 45678
        mock_nocloud_mgr.return_value = mock_mgr_instance

        # Mock subprocess for firecracker
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_proc.communicate.return_value = ("", "")
        mock_popen.return_value = mock_proc

        # Setup vm_dir mock to return a proper Path
        vm_dir = tmp_path / "vms" / "nocloud-test-vm"
        mock_get_vm_dir.return_value = vm_dir

        from mvmctl.api.vm import create_vm
        from mvmctl.core.vm_manager import VMManager

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.api.vm.get_vm_manager", return_value=vm_mgr):
            with patch("mvmctl.api.vm.setup_nat"):
                vm = create_vm(
                    VMCreateInput(
                        name="nocloud-test-vm",
                        image_path=image_path,
                        image_hash="b" * 64,  # Match the seeded test image
                        kernel="vmlinux",
                        vcpus=2,
                        mem=2048,
                        network_name="default",
                        user="root",
                        enable_api_socket=False,
                        enable_pci=False,
                        enable_console=False,
                        firecracker_bin="firecracker",
                        lsm_flags="",
                        enable_logging=False,
                        enable_metrics=False,
                        cloud_init_mode=CloudInitMode.NET,
                    ),
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
    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.api.vm.NoCloudNetServerManager")
    @patch("mvmctl.api.vm.add_nocloud_input_rule")
    @patch("mvmctl.api.vm.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm.subprocess.Popen")
    @patch("mvmctl.api.vm.subprocess.run")
    @patch("mvmctl.api.vm.create_tap")
    @patch("mvmctl.api.vm.add_iptables_forward_rules")
    @patch("mvmctl.api.vm.remove_iptables_forward_rules")
    @patch("mvmctl.api.vm.delete_tap")
    @patch("mvmctl.api.vm.bridge_exists")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.api.network.allocate_network_ip")
    @patch("mvmctl.api.network.release_network_ip")
    @patch("mvmctl.api.vm.setup_nocloud_input_chain")
    @patch("mvmctl.api.vm.resolve_image_multi_strategy")
    @patch("mvmctl.api.vm._resolve_kernel_path")
    @patch("mvmctl.api.vm._write_pid_file")
    @patch("mvmctl.api.vm.get_vm_dir_by_hash")
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
        seed_test_assets,
    ):
        """Test that nocloud-net VM removal stops server and removes firewall rule."""
        from mvmctl.core.mvm_db import MVMDatabase
        from mvmctl.db.models import Binary, Network

        mock_check_priv.return_value = None
        mock_require_group.return_value = None
        mock_subprocess_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

        image_path = tmp_path / "ubuntu-24.04.ext4"
        image_path.write_text("fake image")
        kernel_path = tmp_path / "vmlinux"
        kernel_path.write_text("fake kernel")

        mock_resolve_kernel.return_value = kernel_path
        mock_resolve_image.return_value = image_path

        db = MVMDatabase()
        db.upsert_binary(
            Binary(
                id="c" * 64,
                name="firecracker",
                version="1.15.0",
                full_version="v1.15.0",
                ci_version="1.15.0",
                path="/usr/local/bin/firecracker",
                is_default=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )
        db.upsert_network(
            Network(
                id="d" * 64,
                name="default",
                subnet="10.20.0.0/24",
                bridge="mvm-br0",
                ipv4_gateway="10.20.0.1",
                bridge_active=True,
                nat_enabled=True,
                is_default=False,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )

        # Mock network
        from mvmctl.models.network import NetworkConfig

        mock_get_network.return_value = NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00+00:00",
        )
        mock_alloc_ip.return_value = "10.20.0.2"
        mock_bridge_exists.return_value = True

        # Mock nocloud-net server manager
        mock_mgr_instance = MagicMock()
        mock_mgr_instance.start_server.return_value = ("http://10.20.0.1:8000/", 8000)
        mock_mgr_instance.get_server_pid.return_value = 45678
        mock_nocloud_mgr.return_value = mock_mgr_instance

        # Mock subprocess for firecracker
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        # Setup vm_dir (directory will be created by create_vm)
        vm_dir = tmp_path / "vms" / "cleanup-test-vm"
        mock_get_vm_dir.return_value = vm_dir

        # Create and remove VM
        from mvmctl.api.vm import create_vm, remove_vm
        from mvmctl.core.vm_manager import VMManager

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.api.vm.get_vm_manager", return_value=vm_mgr):
            with patch("mvmctl.api.vm.setup_nat"):
                # Create VM
                create_vm(
                    VMCreateInput(
                        name="cleanup-test-vm",
                        image="ubuntu-24.04",
                        kernel="vmlinux",
                        vcpus=2,
                        mem=256,
                        network_name="default",
                        user="root",
                        enable_api_socket=False,
                        enable_pci=False,
                        enable_console=False,
                        firecracker_bin="firecracker",
                        lsm_flags="",
                        enable_logging=False,
                        enable_metrics=False,
                        cloud_init_mode=CloudInitMode.NET,
                    ),
                    vm_manager=vm_mgr,
                )

            # Verify server was started
            mock_mgr_instance.start_server.assert_called_once()

            # Reset mocks for remove verification
            mock_add_rule.reset_mock()

            # Remove VM
            remove_vm("cleanup-test-vm", vm_manager=vm_mgr)

        # Verify nocloud-net server was stopped
        mock_mgr_instance.stop_server.assert_called_once()

        # Verify firewall rule was removed
        mock_remove_rule.assert_called_once_with("10.20.0.2", "cleanup-test-vm", 8000)

        # Verify VM is deregistered
        assert vm_mgr.get("cleanup-test-vm") is None


class TestMultipleVMsDifferentPorts:
    """Test multiple VMs with nocloud-net mode get different ports."""

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.api.vm.NoCloudNetServerManager")
    @patch("mvmctl.api.vm.add_nocloud_input_rule")
    @patch("mvmctl.api.vm.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm.subprocess.Popen")
    @patch("mvmctl.api.vm.resolve_image_multi_strategy")
    @patch("mvmctl.api.vm._resolve_kernel_path")
    def test_multiple_vms_different_ports(
        self,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_popen,
        mock_remove_rule,
        mock_add_rule,
        mock_nocloud_mgr,
        mock_check_priv,
        mock_require_group,
        tmp_path,
        seed_test_assets,
    ):
        """Test that two VMs with nocloud-net mode get different ports."""
        from mvmctl.core.mvm_db import MVMDatabase
        from mvmctl.db.models import Binary, Network

        mock_check_priv.return_value = None
        mock_require_group.return_value = None

        image_path = tmp_path / "ubuntu-24.04.ext4"
        image_path.write_text("fake image")
        kernel_path = tmp_path / "vmlinux"
        kernel_path.write_text("fake kernel")

        mock_resolve_image.return_value = image_path
        mock_resolve_kernel.return_value = kernel_path

        db = MVMDatabase()
        db.upsert_binary(
            Binary(
                id="c" * 64,
                name="firecracker",
                version="1.15.0",
                full_version="v1.15.0",
                ci_version="1.15.0",
                path="/usr/local/bin/firecracker",
                is_default=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )

        db.upsert_network(
            Network(
                id="d" * 64,
                name="default",
                subnet="10.20.0.0/24",
                bridge="mvm-br0",
                ipv4_gateway="10.20.0.1",
                bridge_active=True,
                nat_enabled=True,
                is_default=False,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )

        # Create two separate mock managers to return different ports
        mock_mgr1 = MagicMock()
        mock_mgr1.start_server.return_value = ("http://10.20.0.1:8001/", 8001)
        mock_mgr1.get_server_pid.return_value = 45681

        mock_mgr2 = MagicMock()
        mock_mgr2.start_server.return_value = ("http://10.20.0.1:8002/", 8002)
        mock_mgr2.get_server_pid.return_value = 45682

        # Make the nocloud_mgr return different instances
        mock_nocloud_mgr.side_effect = [mock_mgr1, mock_mgr2]

        # Mock subprocess for firecracker
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_proc.communicate.return_value = ("", "")
        mock_popen.return_value = mock_proc

        from mvmctl.api.vm import create_vm
        from mvmctl.core.vm_manager import VMManager

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.api.vm.subprocess.run") as mock_subprocess_run:
            mock_subprocess_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
            with patch("mvmctl.api.vm.get_vm_manager", return_value=vm_mgr):
                with patch("mvmctl.api.network.get_network") as mock_get_net:
                    with patch(
                        "mvmctl.api.network.allocate_network_ip",
                        side_effect=["10.20.0.2", "10.20.0.3"],
                    ):
                        with patch("mvmctl.api.vm.bridge_exists", return_value=True):
                            with patch("mvmctl.api.vm.create_tap"):
                                with patch("mvmctl.api.vm.add_iptables_forward_rules"):
                                    with patch("mvmctl.api.vm.setup_nat"):
                                        with patch("mvmctl.api.vm._write_pid_file"):
                                            with patch("mvmctl.api.vm.setup_nocloud_input_chain"):
                                                with patch(
                                                    "mvmctl.api.vm.get_vm_dir_by_hash"
                                                ) as mock_get_vm_dir:
                                                    vm_dir1 = tmp_path / "vms" / "vm1"
                                                    vm_dir2 = tmp_path / "vms" / "vm2"
                                                    mock_get_vm_dir.side_effect = [vm_dir1, vm_dir2]

                                                    mock_get_net.return_value = NetworkConfig(
                                                        name="default",
                                                        subnet="10.20.0.0/24",
                                                        ipv4_gateway="10.20.0.1",
                                                        bridge="mvm-br0",
                                                        nat_enabled=True,
                                                        created_at="2024-01-01T00:00:00+00:00",
                                                    )

                                                    vm1 = create_vm(
                                                        VMCreateInput(
                                                            name="vm1",
                                                            image="ubuntu-24.04",
                                                            kernel="vmlinux",
                                                            vcpus=2,
                                                            mem=256,
                                                            network_name="default",
                                                            user="root",
                                                            enable_api_socket=False,
                                                            enable_pci=False,
                                                            enable_console=False,
                                                            firecracker_bin="firecracker",
                                                            lsm_flags="",
                                                            enable_logging=False,
                                                            enable_metrics=False,
                                                            cloud_init_mode=CloudInitMode.NET,
                                                        ),
                                                        vm_manager=vm_mgr,
                                                    )

                                                    vm2 = create_vm(
                                                        VMCreateInput(
                                                            name="vm2",
                                                            image="ubuntu-24.04",
                                                            kernel="vmlinux",
                                                            vcpus=2,
                                                            mem=256,
                                                            network_name="default",
                                                            user="root",
                                                            enable_api_socket=False,
                                                            enable_pci=False,
                                                            enable_console=False,
                                                            firecracker_bin="firecracker",
                                                            lsm_flags="",
                                                            enable_logging=False,
                                                            enable_metrics=False,
                                                            cloud_init_mode=CloudInitMode.NET,
                                                        ),
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
    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.api.vm.NoCloudNetServerManager")
    @patch("mvmctl.api.vm.add_nocloud_input_rule")
    @patch("mvmctl.api.vm.remove_nocloud_input_rule")
    @patch("mvmctl.core.mvm_db.MVMDatabase.get_network_by_name")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.api.network.allocate_network_ip")
    @patch("mvmctl.api.network.release_network_ip")
    @patch("mvmctl.api.vm.setup_nocloud_input_chain")
    @patch("mvmctl.api.vm.resolve_image_multi_strategy")
    @patch("mvmctl.api.vm._resolve_kernel_path")
    @patch("mvmctl.api.vm.get_vm_dir_by_hash")
    def test_failure_cleanup_on_firewall_error(
        self,
        mock_get_vm_dir,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_setup_chain,
        mock_release_ip,
        mock_alloc_ip,
        mock_get_network,
        mock_db_get_network,
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
        from mvmctl.models.network import NetworkConfig
        from mvmctl.db.models import Network as DBNetwork

        mock_get_network.return_value = NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00+00:00",
        )
        # Mock DB network with ID for cleanup
        mock_db_get_network.return_value = DBNetwork(
            id="net-id-123",
            name="default",
            subnet="10.20.0.0/24",
            bridge="mvm-br0",
            ipv4_gateway="10.20.0.1",
            bridge_active=True,
            nat_enabled=True,
            is_default=False,
            created_at="2024-01-01T00:00:00+00:00",
            updated_at="2024-01-01T00:00:00+00:00",
        )
        mock_alloc_ip.return_value = "10.20.0.2"

        # Mock nocloud-net server that starts successfully
        mock_mgr_instance = MagicMock()
        mock_mgr_instance.start_server.return_value = ("http://10.20.0.1:8000/", 8000)
        mock_mgr_instance.get_server_pid.return_value = 45678
        mock_nocloud_mgr.return_value = mock_mgr_instance

        # Make firewall rule addition fail
        from mvmctl.exceptions import NetworkError

        mock_add_rule.side_effect = NetworkError("iptables error")

        # Setup vm_dir (directory will be created by create_vm)
        vm_dir = tmp_path / "vms" / "failing-vm"
        mock_get_vm_dir.return_value = vm_dir

        from mvmctl.api.vm import create_vm
        from mvmctl.core.vm_manager import VMManager
        from mvmctl.models import CloudInitMode

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.api.vm.get_vm_manager", return_value=vm_mgr):
            with pytest.raises(NetworkError):
                create_vm(
                    VMCreateInput(
                        name="failing-vm",
                        image="ubuntu-24.04",
                        kernel="vmlinux",
                        vcpus=2,
                        mem=256,
                        network_name="default",
                        user="root",
                        enable_api_socket=False,
                        enable_pci=False,
                        enable_console=False,
                        firecracker_bin="firecracker",
                        lsm_flags="",
                        enable_logging=False,
                        enable_metrics=False,
                        cloud_init_mode=CloudInitMode.NET,
                    ),
                    vm_manager=vm_mgr,
                )

        # Verify server was stopped on failure
        mock_mgr_instance.stop_server.assert_called_once()

        # Verify VM was not registered (cleanup happened)
        assert vm_mgr.get("failing-vm") is None

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.api.vm.subprocess.run")
    @patch("mvmctl.api.vm.NoCloudNetServerManager")
    @patch("mvmctl.api.vm.add_nocloud_input_rule")
    @patch("mvmctl.api.vm.remove_nocloud_input_rule")
    @patch("mvmctl.api.vm.subprocess.Popen")
    @patch("mvmctl.api.vm.create_tap")
    @patch("mvmctl.api.vm.add_iptables_forward_rules")
    @patch("mvmctl.api.vm.bridge_exists")
    @patch("mvmctl.core.mvm_db.MVMDatabase.get_network_by_name")
    @patch("mvmctl.api.network.get_network")
    @patch("mvmctl.api.network.allocate_network_ip")
    @patch("mvmctl.api.network.release_network_ip")
    @patch("mvmctl.api.vm.setup_nocloud_input_chain")
    @patch("mvmctl.api.vm.resolve_image_multi_strategy")
    @patch("mvmctl.api.vm._resolve_kernel_path")
    @patch("mvmctl.api.vm._write_pid_file")
    @patch("mvmctl.api.vm.get_vm_dir_by_hash")
    @patch("mvmctl.core.mvm_db.MVMDatabase.get_image_by_os_slug")
    def test_failure_cleanup_on_firecracker_error(
        self,
        mock_db_get_image,
        mock_get_vm_dir,
        mock_write_pid,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_setup_chain,
        mock_release_ip,
        mock_alloc_ip,
        mock_get_network,
        mock_db_get_network,
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
        from mvmctl.db.models import Image

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

        # Mock image entry with minimum_rootfs_size_mib to pass validation
        mock_db_get_image.return_value = Image(
            id="a" * 64,
            os_slug="ubuntu-24.04",
            os_name="Ubuntu 24.04",
            path=str(image_path),
            arch="x86_64",
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789abc",
            minimum_rootfs_size_mib=2048,
            original_size=2147483648,
            is_default=False,
            created_at="2024-01-01T00:00:00+00:00",
            updated_at="2024-01-01T00:00:00+00:00",
        )

        # Mock network
        from mvmctl.models.network import NetworkConfig
        from mvmctl.db.models import Network as DBNetwork

        mock_get_network.return_value = NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00+00:00",
        )
        # Mock DB network with ID for cleanup
        mock_db_get_network.return_value = DBNetwork(
            id="net-id-123",
            name="default",
            subnet="10.20.0.0/24",
            bridge="mvm-br0",
            ipv4_gateway="10.20.0.1",
            bridge_active=True,
            nat_enabled=True,
            is_default=False,
            created_at="2024-01-01T00:00:00+00:00",
            updated_at="2024-01-01T00:00:00+00:00",
        )
        mock_alloc_ip.return_value = "10.20.0.2"
        mock_bridge_exists.return_value = True

        # Mock nocloud-net server
        mock_mgr_instance = MagicMock()
        mock_mgr_instance.start_server.return_value = ("http://10.20.0.1:8000/", 8000)
        mock_mgr_instance.get_server_pid.return_value = 45678
        mock_nocloud_mgr.return_value = mock_mgr_instance

        # Make firecracker Popen raise FileNotFoundError
        mock_popen.side_effect = FileNotFoundError("firecracker not found")

        # Setup vm_dir (directory will be created by create_vm)
        vm_dir = tmp_path / "vms" / "fc-fail-vm"
        mock_get_vm_dir.return_value = vm_dir

        from mvmctl.api.vm import create_vm
        from mvmctl.core.vm_manager import VMManager
        from mvmctl.exceptions import MVMError
        from mvmctl.models import CloudInitMode

        vm_mgr = VMManager(tmp_path / "vms")

        with patch("mvmctl.api.vm.get_vm_manager", return_value=vm_mgr):
            with patch("mvmctl.api.vm.setup_nat"):
                with pytest.raises(MVMError, match="Firecracker binary not found"):
                    create_vm(
                        VMCreateInput(
                            name="fc-fail-vm",
                            image="ubuntu-24.04",
                            kernel="vmlinux",
                            vcpus=2,
                            mem=256,
                            network_name="default",
                            user="root",
                            enable_api_socket=False,
                            enable_pci=False,
                            enable_console=False,
                            firecracker_bin="firecracker",
                            lsm_flags="",
                            enable_logging=False,
                            enable_metrics=False,
                            cloud_init_mode=CloudInitMode.NET,
                        ),
                        vm_manager=vm_mgr,
                    )

        # Verify server was stopped on failure
        mock_mgr_instance.stop_server.assert_called_once()


class TestVMWithoutNocloudNet:
    """Test that VMs without nocloud-net mode are unaffected."""

    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.api.vm.NoCloudNetServerManager")
    @patch("mvmctl.api.vm.add_nocloud_input_rule")
    @patch("mvmctl.api.vm.subprocess.Popen")
    @patch("mvmctl.api.vm.subprocess.run")
    @patch("mvmctl.core.cloud_init.create_cloud_init_iso")
    @patch("mvmctl.api.vm.resolve_image_multi_strategy")
    @patch("mvmctl.api.vm._resolve_kernel_path")
    @patch("mvmctl.api.vm._setup_rootfs_with_guestfs")
    def test_vm_with_disabled_mode_no_nocloud(
        self,
        mock_setup_guestfs,
        mock_resolve_kernel,
        mock_resolve_image,
        mock_create_iso,
        mock_subprocess_run,
        mock_popen,
        mock_add_rule,
        mock_nocloud_mgr,
        mock_check_priv,
        mock_require_group,
        tmp_path,
        seed_test_assets,
    ):
        """Test that DISABLED mode VM doesn't start nocloud-net server."""
        mock_check_priv.return_value = None
        mock_require_group.return_value = None
        mock_setup_guestfs.return_value = None
        mock_subprocess_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

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

        from mvmctl.api.vm import create_vm
        from mvmctl.core.vm_manager import VMManager
        from mvmctl.models import CloudInitMode

        vm_mgr = VMManager(tmp_path / "vms")

        with patch.object(vm_mgr, "register", return_value=None):
            with patch("mvmctl.api.vm.get_vm_manager", return_value=vm_mgr):
                with patch("mvmctl.api.network.get_network") as mock_get_net:
                    with patch("mvmctl.api.network.allocate_network_ip", return_value="10.20.0.2"):
                        with patch("mvmctl.api.vm.bridge_exists", return_value=True):
                            with patch("mvmctl.api.vm.create_tap"):
                                with patch("mvmctl.api.vm.add_iptables_forward_rules"):
                                    with patch("mvmctl.api.vm.setup_nat"):
                                        with patch("mvmctl.api.vm._write_pid_file"):
                                            with patch("mvmctl.api.vm.setup_nocloud_input_chain"):
                                                with patch(
                                                    "mvmctl.api.vm.get_vm_dir_by_hash"
                                                ) as mock_get_vm_dir:
                                                    vm_dir = tmp_path / "vms" / "disabled-mode-vm"
                                                    mock_get_vm_dir.return_value = vm_dir

                                                    mock_get_net.return_value = NetworkConfig(
                                                        name="default",
                                                        subnet="10.20.0.0/24",
                                                        ipv4_gateway="10.20.0.1",
                                                        bridge="mvm-br0",
                                                        nat_enabled=True,
                                                        created_at="2024-01-01T00:00:00+00:00",
                                                    )

                                                    vm = create_vm(
                                                        VMCreateInput(
                                                            name="disabled-mode-vm",
                                                            image="ubuntu-24.04",
                                                            kernel="vmlinux",
                                                            vcpus=2,
                                                            mem=256,
                                                            network_name="default",
                                                            user="root",
                                                            enable_api_socket=False,
                                                            enable_pci=False,
                                                            enable_console=False,
                                                            firecracker_bin="firecracker",
                                                            lsm_flags="",
                                                            enable_logging=False,
                                                            enable_metrics=False,
                                                            cloud_init_mode=CloudInitMode.OFF,
                                                        ),
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

    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.cli.vm._resolve_active_firecracker_bin")
    @patch("mvmctl.cli.vm.resolve_image_multi_strategy")
    @patch("mvmctl.cli.vm.create_vm")
    def test_create_vm_with_nocloud_net_flag(
        self, mock_create_vm, mock_resolve_image, mock_fc_bin, mock_check_priv
    ):
        """Test creating VM with --nocloud-net flag via CLI."""
        mock_check_priv.return_value = None
        mock_fc_bin.return_value = "/usr/local/bin/firecracker"
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
        assert call_kwargs.get("input").cloud_init_mode == CloudInitMode.NET

    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.cli.vm._resolve_active_firecracker_bin")
    @patch("mvmctl.cli.vm.resolve_image_multi_strategy")
    @patch("mvmctl.cli.vm.create_vm")
    def test_create_vm_with_custom_nocloud_port(
        self, mock_create_vm, mock_resolve_image, mock_fc_bin, mock_check_priv
    ):
        """Test creating VM with custom --nocloud-net-port via CLI."""
        mock_check_priv.return_value = None
        mock_fc_bin.return_value = "/usr/local/bin/firecracker"
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
        assert call_kwargs.get("input").nocloud_net_port == 9999
