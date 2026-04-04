"""Integration tests for console workflow."""

import signal
from pathlib import Path
from unittest.mock import MagicMock, patch

from mvmctl.constants import env_var
from mvmctl.models import VMInstance
from mvmctl.models.network import NetworkConfig


class TestConsoleWorkflow:
    @patch("mvmctl.core.vm_manager.VMManager.register")
    @patch("mvmctl.core.vm_lifecycle.ensure_default_network")
    @patch("mvmctl.core.vm_lifecycle._resolve_kernel_path")
    @patch("mvmctl.core.vm_lifecycle.shutil.copy2")
    @patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
    @patch("mvmctl.core.vm_lifecycle.os.openpty")
    @patch("mvmctl.utils.fs.secure_mkdir")
    @patch("mvmctl.core.vm_manager.get_vm_manager")
    @patch("mvmctl.utils.fs.get_vm_dir")
    @patch("mvmctl.utils.fs.get_images_dir")
    @patch("mvmctl.utils.fs.get_kernels_dir")
    @patch("mvmctl.core.network_manager.get_network")
    @patch("mvmctl.core.network_manager.allocate_network_ip")
    @patch("mvmctl.utils.network.generate_mac")
    @patch("mvmctl.utils.network.bridge_exists")
    @patch("mvmctl.core.vm_lifecycle.create_tap")
    @patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
    @patch("mvmctl.core.vm_lifecycle.setup_nat")
    @patch("mvmctl.utils.fs.write_pid_file")
    @patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
    @patch("mvmctl.core.cloud_init.write_cloud_init")
    @patch("mvmctl.core.vm_lifecycle.write_cloud_init")
    @patch("mvmctl.core.firewall.subprocess.run")
    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.vm_lifecycle.ConsoleRelayManager")
    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch("mvmctl.core.vm_lifecycle.shutil.which")
    def test_create_vm_with_console_starts_relay(
        self,
        mock_which,
        mock_relay_popen,
        mock_console_mgr,
        mock_nocloud_mgr,
        mock_add_nocloud_rule,
        mock_require_group,
        mock_subprocess_run,
        mock_write_ci2,
        mock_write_ci,
        mock_config_gen,
        mock_write_pid,
        mock_add_rules,
        mock_setup_nat,
        mock_create_tap,
        mock_bridge_exists,
        mock_gen_mac,
        mock_alloc_ip,
        mock_get_network,
        mock_get_kernels,
        mock_get_images,
        mock_get_vm_dir,
        mock_get_vm_mgr,
        mock_secure_mkdir,
        mock_openpty,
        mock_fc_popen,
        mock_copy2,
        mock_resolve_kernel,
        mock_ensure_default_net,
        mock_vm_register,
        tmp_path: Path,
    ):
        from mvmctl.core.vm_lifecycle import create_vm

        # Setup mock for subprocess.run to return success
        mock_run_result = MagicMock()
        mock_run_result.returncode = 0
        mock_subprocess_run.return_value = mock_run_result

        # Patch shutil.copy2 to avoid MagicMock path objects reaching real copy2
        mock_copy2.return_value = None
        mock_which.return_value = "/usr/bin/firecracker"
        kernel_path = tmp_path / "cache" / "kernels" / "vmlinux"
        kernel_path.parent.mkdir(parents=True, exist_ok=True)
        kernel_path.touch()
        mock_resolve_kernel.return_value = kernel_path

        mock_ensure_default_net.return_value = NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
        )

        # Debug: Ensure subprocess.Popen mock is working
        mock_fc_popen.side_effect = lambda *args, **kwargs: MagicMock(pid=1000)

        # Setup proper VM directory path (real Path, not mock)
        vm_dir = tmp_path / "testvm"
        vm_dir.mkdir(parents=True)
        mock_get_vm_dir.return_value = vm_dir

        mock_images_dir = MagicMock()
        mock_image = MagicMock()
        mock_image.exists.return_value = True
        mock_images_dir.__truediv__.return_value = mock_image
        mock_get_images.return_value = mock_images_dir

        kernels_dir = tmp_path / "cache" / "kernels"
        kernels_dir.mkdir(parents=True, exist_ok=True)
        kernel_file = kernels_dir / "vmlinux"
        kernel_file.touch()
        mock_get_kernels.return_value = kernels_dir

        mock_net = NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
        )
        mock_get_network.return_value = mock_net

        mock_alloc_ip.return_value = "10.20.0.5"
        mock_gen_mac.return_value = "02:fc:11:22:33:44"
        mock_bridge_exists.return_value = True

        mock_manager = MagicMock()
        mock_get_vm_mgr.return_value = mock_manager
        mock_manager.count_vms.return_value = 0
        mock_manager.register.return_value = None

        mock_fc_proc = MagicMock()
        mock_fc_proc.pid = 1000
        mock_fc_popen.return_value = mock_fc_proc

        mock_relay_proc = MagicMock()
        mock_relay_proc.pid = 2000
        mock_relay_popen.return_value = mock_relay_proc

        mock_openpty.return_value = (12, 13)

        # Setup mock nocloud server manager
        mock_nocloud_instance = MagicMock()
        mock_nocloud_instance.start_server.return_value = ("http://10.20.0.1:8080/", 8080)
        mock_nocloud_instance.get_server_pid.return_value = 9999
        mock_nocloud_mgr.return_value = mock_nocloud_instance

        # Setup mock console relay manager
        mock_console_instance = MagicMock()
        mock_console_instance.start_relay.return_value = (vm_dir / "console.sock", 2000)
        mock_console_mgr.return_value = mock_console_instance

        # Debug: Ensure write_cloud_init mock is working
        def mock_write_ci_impl(*args, **kwargs):
            print(f"write_cloud_init called with: {args}, {kwargs}")
            return None

        mock_write_ci.side_effect = mock_write_ci_impl
        mock_write_ci2.side_effect = mock_write_ci_impl

        # Debug: Print mock status
        print(f"mock_write_ci: {mock_write_ci}")
        print(f"mock_which: {mock_which}")
        print(f"mock_fc_popen: {mock_fc_popen}")

        # Add side_effect to track Popen calls
        def track_popen(*args, **kwargs):
            print(f"Popen called with: {args}, {kwargs}")
            return MagicMock(pid=1000)

        mock_fc_popen.side_effect = track_popen

        # Verify mock is in the correct module
        from mvmctl.core import cloud_init

        print(f"cloud_init.write_cloud_init: {cloud_init.write_cloud_init}")
        print(f"Is mock: {cloud_init.write_cloud_init is mock_write_ci}")

        from mvmctl.models import CloudInitMode

        vm = create_vm(name="testvm", image="ubuntu-22.04", cloud_init_mode=CloudInitMode.NET)

        assert isinstance(vm, VMInstance)
        assert vm.name == "testvm"
        assert vm.console_relay_pid == 2000
        assert vm.console_socket_path is not None

        # Verify Firecracker was started
        assert mock_fc_popen.call_count == 1
        # Verify console relay manager was called to start relay
        assert mock_console_instance.start_relay.call_count == 1

    @patch("mvmctl.core.vm_manager.VMManager.register")
    @patch("mvmctl.core.vm_lifecycle.ensure_default_network")
    @patch("mvmctl.core.vm_lifecycle._resolve_kernel_path")
    @patch("mvmctl.core.vm_lifecycle.shutil.copy2")
    @patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
    @patch("mvmctl.utils.fs.secure_mkdir")
    @patch("mvmctl.core.vm_manager.get_vm_manager")
    @patch("mvmctl.utils.fs.get_vm_dir")
    @patch("mvmctl.utils.fs.get_images_dir")
    @patch("mvmctl.utils.fs.get_kernels_dir")
    @patch("mvmctl.core.network_manager.get_network")
    @patch("mvmctl.core.network_manager.allocate_network_ip")
    @patch("mvmctl.utils.network.generate_mac")
    @patch("mvmctl.utils.network.bridge_exists")
    @patch("mvmctl.core.vm_lifecycle.create_tap")
    @patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
    @patch("mvmctl.core.vm_lifecycle.setup_nat")
    @patch("mvmctl.utils.fs.write_pid_file")
    @patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
    @patch("mvmctl.core.cloud_init.write_cloud_init")
    @patch("mvmctl.core.firewall.subprocess.run")
    @patch("mvmctl.utils.process.require_mvm_group_membership")
    @patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.vm_lifecycle.shutil.which")
    def test_create_vm_without_console_skips_relay(
        self,
        mock_which,
        mock_nocloud_mgr,
        mock_add_nocloud_rule,
        mock_require_group,
        mock_subprocess_run,
        mock_write_ci,
        mock_config_gen,
        mock_write_pid,
        mock_add_rules,
        mock_setup_nat,
        mock_create_tap,
        mock_bridge_exists,
        mock_gen_mac,
        mock_alloc_ip,
        mock_get_network,
        mock_get_kernels,
        mock_get_images,
        mock_get_vm_dir,
        mock_get_vm_mgr,
        mock_secure_mkdir,
        mock_popen,
        mock_copy2,
        mock_resolve_kernel,
        mock_ensure_default_net,
        mock_vm_register,
        tmp_path: Path,
    ):
        from mvmctl.core.vm_lifecycle import create_vm

        # Setup mock for subprocess.run to return success
        mock_run_result = MagicMock()
        mock_run_result.returncode = 0
        mock_subprocess_run.return_value = mock_run_result

        mock_copy2.return_value = None
        mock_which.return_value = "/usr/bin/firecracker"
        kernel_path = tmp_path / "cache" / "kernels" / "vmlinux"
        kernel_path.parent.mkdir(parents=True, exist_ok=True)
        kernel_path.touch()
        mock_resolve_kernel.return_value = kernel_path

        mock_ensure_default_net.return_value = NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
        )

        # Debug: Ensure write_cloud_init mock is working
        mock_write_ci.side_effect = lambda *args, **kwargs: None

        # Setup proper VM directory path (real Path, not mock)
        vm_dir = tmp_path / "testvm"
        vm_dir.mkdir(parents=True)
        mock_get_vm_dir.return_value = vm_dir

        mock_images_dir = MagicMock()
        mock_image = MagicMock()
        mock_image.exists.return_value = True
        mock_images_dir.__truediv__.return_value = mock_image
        mock_get_images.return_value = mock_images_dir

        kernels_dir = tmp_path / "cache" / "kernels"
        kernels_dir.mkdir(parents=True, exist_ok=True)
        kernel_file = kernels_dir / "vmlinux"
        kernel_file.touch()
        mock_get_kernels.return_value = kernels_dir

        mock_net = NetworkConfig(
            name="default",
            subnet="10.20.0.0/24",
            ipv4_gateway="10.20.0.1",
            bridge="mvm-br0",
        )
        mock_get_network.return_value = mock_net

        mock_alloc_ip.return_value = "10.20.0.5"
        mock_gen_mac.return_value = "02:fc:11:22:33:44"
        mock_bridge_exists.return_value = True

        mock_manager = MagicMock()
        mock_get_vm_mgr.return_value = mock_manager
        mock_manager.count_vms.return_value = 0
        mock_manager.register.return_value = None

        mock_proc = MagicMock()
        mock_proc.pid = 1000
        mock_popen.return_value = mock_proc

        # Setup mock nocloud server manager
        mock_nocloud_instance = MagicMock()
        mock_nocloud_instance.start_server.return_value = ("http://10.20.0.1:8080/", 8080)
        mock_nocloud_instance.get_server_pid.return_value = 9999
        mock_nocloud_mgr.return_value = mock_nocloud_instance

        from mvmctl.models import CloudInitMode

        vm = create_vm(
            name="testvm",
            image="ubuntu-22.04",
            enable_console=False,
            cloud_init_mode=CloudInitMode.NET,
        )

        assert isinstance(vm, VMInstance)
        assert vm.name == "testvm"
        assert vm.console_relay_pid is None
        assert vm.console_socket_path is None

        # Verify only Firecracker was started (no console relay)
        assert mock_popen.call_count == 1


class TestConsoleRelayLifecycle:
    @patch("mvmctl.services.console_relay.manager.os.kill")
    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_relay_start_stop_lifecycle(self, mock_popen, mock_kill, tmp_path: Path, monkeypatch):
        from mvmctl.services.console_relay import ConsoleRelayManager

        monkeypatch.setenv(env_var("CACHE_DIR"), str(tmp_path))

        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        mgr = ConsoleRelayManager()
        vm_hash = "a" * 64  # Mock VM hash
        vm_dir = tmp_path / "vms" / vm_hash
        vm_dir.mkdir(parents=True)

        # Start relay
        socket_path, pid = mgr.start_relay("testvm", 10, vm_dir)
        assert pid == 12345
        assert mgr.is_relay_running("testvm", vm_hash) is True

        # Stop relay
        mgr.stop_relay("testvm", vm_hash)
        mock_kill.assert_any_call(12345, signal.SIGTERM)
        assert "testvm" not in mgr._relays

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_relay_kill_recovery(self, mock_kill, tmp_path: Path, monkeypatch):
        from mvmctl.services.console_relay import ConsoleRelayManager

        monkeypatch.setenv(env_var("CACHE_DIR"), str(tmp_path))

        # Create a PID file for a "stuck" relay (using hash-based path)
        vm_hash = "a" * 64  # Mock VM hash
        pid_file = tmp_path / "vms" / vm_hash / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("99999")

        call_count = [0]

        def kill_side_effect(pid, sig):
            call_count[0] += 1
            if pid == 99999 and sig == 0:
                return None
            if sig == signal.SIGTERM:
                raise ProcessLookupError()
            if sig == signal.SIGKILL:
                raise ProcessLookupError()

        mock_kill.side_effect = kill_side_effect

        mgr = ConsoleRelayManager()
        result = mgr.kill_relay("testvm", vm_hash)

        assert result is True


class TestConsoleAPI:
    @patch("mvmctl.api.vms.ConsoleRelayManager")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_attach_console_returns_socket_path(self, mock_get_mgr, mock_mgr_class):
        from mvmctl.api.vms import attach_console

        mock_vm = MagicMock()
        mock_vm.name = "testvm"
        mock_manager = MagicMock()
        mock_manager.get.return_value = mock_vm
        mock_get_mgr.return_value = mock_manager

        mock_relay_mgr = MagicMock()
        mock_relay_mgr.is_relay_running.return_value = True
        mock_relay_mgr.get_socket_path.return_value = Path("/tmp/test.sock")
        mock_mgr_class.return_value = mock_relay_mgr

        result = attach_console("testvm")

        assert result["socket_path"] == "/tmp/test.sock"
        assert result["vm_name"] == "testvm"

    @patch("mvmctl.api.vms.ConsoleRelayManager")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_kill_console_terminates_relay(self, mock_get_mgr, mock_mgr_class):
        from mvmctl.api.vms import kill_console

        mock_vm = MagicMock()
        mock_vm.id = "a" * 64  # Mock VM hash
        mock_manager = MagicMock()
        mock_manager.get.return_value = mock_vm
        mock_get_mgr.return_value = mock_manager

        mock_relay_mgr = MagicMock()
        mock_relay_mgr.kill_relay.return_value = True
        mock_mgr_class.return_value = mock_relay_mgr

        result = kill_console("testvm")

        assert result is True
        mock_relay_mgr.kill_relay.assert_called_once_with("testvm", mock_vm.id)

    @patch("mvmctl.api.vms._get_console_state")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_get_console_state_returns_status(self, mock_get_mgr, mock_get_state):
        from mvmctl.api.vms import get_console_state

        mock_vm = MagicMock()
        mock_manager = MagicMock()
        mock_manager.get.return_value = mock_vm
        mock_get_mgr.return_value = mock_manager

        mock_get_state.return_value = {
            "running": True,
            "pid": 12345,
            "socket_path": "/tmp/test.sock",
        }

        result = get_console_state("testvm")

        assert result["running"] is True
        assert result["pid"] == 12345
        assert result["socket_path"] == "/tmp/test.sock"
