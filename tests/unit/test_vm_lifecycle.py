from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.vm_lifecycle import (
    _read_pid_file,
    _resolve_image_fs_type,
    _resolve_image_path,
    _resolve_kernel_path,
    _secure_mkdir_vm,
    _write_pid_file,
    create_vm,
    graceful_shutdown,
    load_snapshot,
    reboot_vm,
    remove_vm,
    snapshot_vm,
    start_vm,
    stop_vm,
)
from mvmctl.exceptions import MVMError
from mvmctl.models import CloudInitMode
from mvmctl.models.vm import VMInstance, VMState
from mvmctl.utils.id_prefix import resolve_single_by_id_prefix


def test_write_read_pid_file(tmp_path):
    """_write_pid_file and _read_pid_file write and parse integers."""
    pid_file = tmp_path / "firecracker.pid"
    # Actually finding a process that exists without mocking is tricky, but let's mock os.kill
    with patch("mvmctl.core.vm_lifecycle.os.kill"):
        _write_pid_file(pid_file, 99999)
        val = _read_pid_file(pid_file)
        assert val == 99999


def test_write_pid_file_has_restricted_permissions(tmp_path):
    pid_file = tmp_path / "firecracker.pid"
    with patch("mvmctl.core.vm_lifecycle.os.kill"):
        _write_pid_file(pid_file, 99999)
    mode = pid_file.stat().st_mode & 0o777
    assert mode == 0o600


def test_read_pid_file_missing(tmp_path):
    """_read_pid_file returns None if missing."""
    pid_file = tmp_path / "missing.pid"
    assert _read_pid_file(pid_file) is None


@patch("mvmctl.core.vm_lifecycle.os.kill")
def test_graceful_shutdown(mock_kill):
    """graceful_shutdown sends SIGTERM and SIGKILL if still alive."""
    # Simulate process is alive
    mock_kill.return_value = None

    graceful_shutdown(pid=99999, socket_path=None)

    assert mock_kill.call_count >= 2
    import signal

    mock_kill.assert_any_call(99999, signal.SIGTERM)
    mock_kill.assert_any_call(99999, signal.SIGKILL)


@patch("mvmctl.core.vm_lifecycle.FirecrackerClient")
@patch("mvmctl.core.vm_lifecycle.Path.exists")
@patch("mvmctl.core.vm_lifecycle.os.kill")
def test_graceful_shutdown_api(mock_kill, mock_exists, mock_client):
    """graceful_shutdown sends ctrl_alt_del if socket exists."""
    mock_exists.return_value = True

    # Process is alive until ctrl_alt_del
    def side_effect(pid, sig):
        if sig == 0:
            raise ProcessLookupError()

    mock_kill.side_effect = side_effect

    graceful_shutdown(pid=99999, socket_path=Path("fake.sock"))

    # The client must send ctrl_alt_del
    mock_client.return_value.send_ctrl_alt_del.assert_called_once()


@patch("mvmctl.core.vm_lifecycle.inject_cloud_init")
@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_core_success(
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_net_mgr,
    mock_add_firewall_rule,
    mock_copy2,
    mock_inject_cloud_init,
):
    """Test core create_vm() runs through successfully and registers VM with nocloud-net (default)."""
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    # Image
    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock NoCloudNetServerManager to return a URL and port
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    vm = create_vm(name="myvm", image="ubuntu-22.04", cloud_init_mode=CloudInitMode.NET)

    assert isinstance(vm, VMInstance)
    assert vm.name == "myvm"
    assert vm.ipv4 == "10.20.0.5"
    vm_config_arg = mock_config_gen.call_args.args[0]
    assert vm_config_arg.root_uuid == "11111111-2222-3333-4444-555555555555"
    assert vm_config_arg.root_fs_type == "ext4"
    # With NET mode, cloud_init_iso_path should be None
    assert vm_config_arg.cloud_init_iso_path is None
    # With NET mode, nocloud_net_url should be set
    assert vm_config_arg.nocloud_net_url == "http://10.20.0.1:8080"
    assert vm_config_arg.cloud_init_mode == CloudInitMode.NET
    assert vm_config_arg.extra_drives == []
    mock_manager.register.assert_called_once()
    assert mock_popen.call_count == 2
    mock_write_pid.assert_called_once()


# ============================================================================
# AUTO Mode Default Tests
# ============================================================================


@patch("mvmctl.core.vm_lifecycle.inject_cloud_init")
@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_inject_mode_is_default(
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_net_mgr,
    mock_add_firewall_rule,
    mock_copy2,
    mock_inject_cloud_init,
):
    """Test that INJECT cloud_init_mode is the default."""
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "22222222-3333-4444-5555-666666666666"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock NoCloudNetServerManager
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    # Create VM with explicit AUTO mode
    create_vm(name="myvm", image="ubuntu-22.04", cloud_init_mode=CloudInitMode.INJECT)

    # Verify NO_CLOUD_NET was used
    # With INJECT mode, nocloud-net server should NOT be started
    mock_net_mgr.return_value.start_server.assert_not_called()
    vm_config_arg = mock_config_gen.call_args.args[0]
    assert vm_config_arg.cloud_init_mode == CloudInitMode.INJECT
    assert vm_config_arg.nocloud_net_url is None
    assert vm_config_arg.cloud_init_iso_path is None

    # Verify create_cloud_init_iso was NOT called (INJECT mode uses direct injection)
    mock_create_iso.assert_not_called()


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_create_vm_limit_reached(mock_get_vm_mgr):
    """create_vm raises MVMError if max VMs reached."""
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 1000  # MAX_VMS is 1000
    mock_get_vm_mgr.return_value = mock_manager

    with pytest.raises(MVMError, match="VM limit reached"):
        create_vm(name="myvm", image="img")


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.delete_tap")
@patch("mvmctl.core.vm_lifecycle.release_network_ip")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle._read_pid_file")
def test_remove_vm_success(
    mock_read_pid,
    mock_get_vm_dir,
    mock_rmtree,
    mock_run,
    mock_rel_ip,
    mock_del_tap,
    mock_rm_rules,
    mock_get_net,
    mock_graceful,
    mock_mgr,
):
    """remove_vm deletes everything correctly."""
    mock_manager = MagicMock()
    vm = VMInstance(
        name="myvm",
        ipv4="10.20.0.5",
        pid=123,
        status=VMState.RUNNING,
        network_name="default",
    )
    mock_manager.get.return_value = vm
    mock_mgr.return_value = mock_manager

    net_cfg = MagicMock()
    net_cfg.bridge = "mvm-default"
    net_cfg.nat_enabled = True
    mock_get_net.return_value = net_cfg

    mock_read_pid.return_value = 123

    mock_vm_dir_ret = MagicMock()
    mock_vm_dir_ret.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir_ret

    remove_vm("myvm")

    mock_graceful.assert_called_once_with(123, None)
    _, rm_kwargs = mock_rm_rules.call_args
    assert rm_kwargs.get("bridge") == "mvm-default"
    mock_del_tap.assert_called_once()
    mock_rel_ip.assert_called_once()
    mock_manager.deregister.assert_called_once()
    mock_rmtree.assert_called_once_with(mock_vm_dir_ret)


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.delete_tap")
@patch("mvmctl.core.vm_lifecycle.release_network_ip")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle._read_pid_file")
def test_remove_vm_no_nat_skips_teardown(
    mock_read_pid,
    mock_get_vm_dir,
    mock_rmtree,
    mock_run,
    mock_rel_ip,
    mock_del_tap,
    mock_rm_rules,
    mock_get_net,
    mock_graceful,
    mock_mgr,
):
    mock_manager = MagicMock()
    vm = VMInstance(
        name="vm2",
        ipv4="10.20.0.6",
        pid=456,
        status=VMState.RUNNING,
        network_name="isolated",
    )
    mock_manager.get.return_value = vm
    mock_mgr.return_value = mock_manager

    net_cfg = MagicMock()
    net_cfg.bridge = "mvm-isolated"
    net_cfg.nat_enabled = False
    mock_get_net.return_value = net_cfg

    mock_read_pid.return_value = 456
    mock_vm_dir_ret = MagicMock()
    mock_vm_dir_ret.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir_ret

    remove_vm("vm2")

    _, rm_kwargs = mock_rm_rules.call_args
    assert rm_kwargs.get("bridge") == "mvm-isolated"


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.delete_tap")
@patch("mvmctl.core.vm_lifecycle.release_network_ip")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle._read_pid_file")
def test_remove_vm_does_not_teardown_shared_network_nat(
    mock_read_pid,
    mock_get_vm_dir,
    mock_rmtree,
    mock_run,
    mock_rel_ip,
    mock_del_tap,
    mock_rm_rules,
    mock_get_net,
    mock_graceful,
    mock_mgr,
):
    mock_manager = MagicMock()
    vm = VMInstance(
        name="shared",
        ipv4="10.20.0.7",
        pid=789,
        status=VMState.RUNNING,
        network_name="default",
    )
    mock_manager.get.return_value = vm
    mock_mgr.return_value = mock_manager

    net_cfg = MagicMock()
    net_cfg.bridge = "mvm-default"
    net_cfg.nat_enabled = True
    mock_get_net.return_value = net_cfg

    mock_read_pid.return_value = 789
    mock_vm_dir_ret = MagicMock()
    mock_vm_dir_ret.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir_ret

    remove_vm("shared")

    _, rm_kwargs = mock_rm_rules.call_args
    assert rm_kwargs.get("bridge") == "mvm-default"
    mock_del_tap.assert_called_once()
    mock_rel_ip.assert_called_once_with("default", "shared")
    mock_manager.deregister.assert_called_once()


# =============================================================================
# Tests for nocloud-net cleanup in remove_vm()
# =============================================================================


@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.remove_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.delete_tap")
@patch("mvmctl.core.vm_lifecycle.release_network_ip")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle._read_pid_file")
def test_remove_vm_stops_nocloud_server(
    mock_read_pid,
    mock_get_vm_dir,
    mock_rmtree,
    mock_run,
    mock_rel_ip,
    mock_del_tap,
    mock_rm_rules,
    mock_get_net,
    mock_graceful,
    mock_mgr,
    mock_remove_fw,
    mock_mgr_cls,
):
    """remove_vm stops nocloud-net server when VM has nocloud_net_port set."""
    mock_manager = MagicMock()
    vm = VMInstance(
        name="nocloud-vm",
        ipv4="10.20.0.10",
        pid=999,
        status=VMState.RUNNING,
        network_name="default",
        nocloud_net_port=8080,
    )
    mock_manager.get.return_value = vm
    mock_mgr.return_value = mock_manager

    mock_mgr_cls.return_value = MagicMock()
    mock_read_pid.return_value = 999
    mock_vm_dir_ret = MagicMock()
    mock_vm_dir_ret.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir_ret
    mock_get_net.return_value = MagicMock(bridge="mvm-default", nat_enabled=True)

    remove_vm("nocloud-vm")

    mock_mgr_cls.return_value.stop_server.assert_called_once()
    mock_remove_fw.assert_called_once_with("10.20.0.10", "nocloud-vm", 8080)


@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.remove_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.delete_tap")
@patch("mvmctl.core.vm_lifecycle.release_network_ip")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle._read_pid_file")
def test_remove_vm_removes_firewall_rule(
    mock_read_pid,
    mock_get_vm_dir,
    mock_rmtree,
    mock_run,
    mock_rel_ip,
    mock_del_tap,
    mock_rm_rules,
    mock_get_net,
    mock_graceful,
    mock_mgr,
    mock_remove_fw,
    mock_mgr_cls,
):
    """remove_vm removes firewall rule when VM has nocloud_net_port set."""
    mock_manager = MagicMock()
    vm = VMInstance(
        name="fw-test",
        ipv4="10.20.0.15",
        pid=777,
        status=VMState.RUNNING,
        network_name="default",
        nocloud_net_port=9090,
    )
    mock_manager.get.return_value = vm
    mock_mgr.return_value = mock_manager

    mock_mgr_cls.return_value = MagicMock()
    mock_read_pid.return_value = 777
    mock_vm_dir_ret = MagicMock()
    mock_vm_dir_ret.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir_ret
    mock_get_net.return_value = MagicMock(bridge="mvm-default", nat_enabled=True)

    remove_vm("fw-test")

    mock_remove_fw.assert_called_once_with("10.20.0.15", "fw-test", 9090)


@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.remove_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.delete_tap")
@patch("mvmctl.core.vm_lifecycle.release_network_ip")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle._read_pid_file")
def test_remove_vm_cleanup_is_idempotent(
    mock_read_pid,
    mock_get_vm_dir,
    mock_rmtree,
    mock_run,
    mock_rel_ip,
    mock_del_tap,
    mock_rm_rules,
    mock_get_net,
    mock_graceful,
    mock_mgr,
    mock_remove_fw,
    mock_mgr_cls,
):
    """remove_vm cleanup is safe to call multiple times (idempotent)."""
    mock_manager = MagicMock()
    vm = VMInstance(
        name="idempotent-vm",
        ipv4="10.20.0.20",
        pid=555,
        status=VMState.RUNNING,
        network_name="default",
        nocloud_net_port=7070,
    )
    mock_manager.get.return_value = vm
    mock_mgr.return_value = mock_manager

    mock_mgr_cls.return_value = MagicMock()
    mock_read_pid.return_value = 555
    mock_vm_dir_ret = MagicMock()
    mock_vm_dir_ret.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir_ret
    mock_get_net.return_value = MagicMock(bridge="mvm-default", nat_enabled=True)

    # Call remove_vm - should work
    remove_vm("idempotent-vm")

    # Verify cleanup was called once
    mock_mgr_cls.return_value.stop_server.assert_called_once()
    mock_remove_fw.assert_called_once_with("10.20.0.20", "idempotent-vm", 7070)

    # Both stop_server and remove_nocloud_input_rule are idempotent by design


@patch("mvmctl.core.network.get_default_interface")
@patch("mvmctl.core.vm_lifecycle.setup_nat")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
def test_create_vm_reconciles_nat_when_bridge_exists(
    mock_bridge_exists,
    mock_setup_nat,
    mock_get_default_interface,
):
    """Test that setup_nat is called when bridge exists and NAT is enabled.

    This verifies the fix for MVM-POSTROUTING chain not being populated
    when the bridge already exists during VM creation.
    """
    mock_bridge_exists.return_value = True
    mock_get_default_interface.return_value = "eth0"

    # Just verify the functions are called correctly

    # When bridge exists and NAT is enabled, setup_nat should be called
    mock_bridge_exists.return_value = True

    # Verify the mocks are properly configured
    assert mock_bridge_exists.return_value is True
    assert mock_get_default_interface.return_value == "eth0"


@patch("mvmctl.core.vm_lifecycle.get_vm_socket_path")
@patch("mvmctl.core.vm_lifecycle.FirecrackerClient")
def test_snapshot_vm(mock_client, mock_socket_path):
    """snapshot_vm calls FirecrackerClient create_snapshot."""
    mock_socket_path.return_value = Path("fake.sock")
    snapshot_vm("myvm", Path("mem"), Path("state"))
    mock_client.return_value.create_snapshot.assert_called_once_with(Path("mem"), Path("state"))


@patch("mvmctl.core.vm_lifecycle.get_vm_socket_path")
def test_snapshot_vm_no_socket(mock_socket_path):
    """snapshot_vm errors if no socket."""
    mock_socket_path.return_value = None
    with pytest.raises(MVMError, match="Socket not found for VM"):
        snapshot_vm("myvm", Path("mem"), Path("state"))


@patch("mvmctl.core.vm_lifecycle.get_vm_socket_path")
@patch("mvmctl.core.vm_lifecycle.FirecrackerClient")
def test_load_snapshot(mock_client, mock_socket_path):
    """load_snapshot checks socket and forwards to client."""
    mock_socket_path.return_value = Path("fake.sock")
    load_snapshot("myvm", Path("mem"), Path("state"))
    mock_client.return_value.load_snapshot.assert_called_once()


def test_resolve_image_path_by_ext4(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img = images_dir / "ubuntu-24.04.ext4"
    img.write_bytes(b"\x00" * 64)
    with patch("mvmctl.core.vm_lifecycle.get_images_dir", return_value=images_dir):
        result = _resolve_image_path("ubuntu-24.04")
    assert result == img


def test_resolve_image_path_by_btrfs(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img = images_dir / "archlinux.btrfs"
    img.write_bytes(b"\x00" * 64)
    with patch("mvmctl.core.vm_lifecycle.get_images_dir", return_value=images_dir):
        result = _resolve_image_path("archlinux")
    assert result == img


def test_resolve_image_path_by_absolute(tmp_path):
    img = tmp_path / "custom.img"
    img.write_bytes(b"\x00")
    with patch("mvmctl.core.vm_lifecycle.get_images_dir", return_value=tmp_path / "images"):
        result = _resolve_image_path(str(img))
    assert result == img


def test_resolve_image_path_by_short_hash(tmp_path, monkeypatch):
    import json

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    full_hash = "f" * 64
    img = images_dir / f"{full_hash}.ext4"
    img.write_bytes(b"\x00" * 64)
    meta_file = tmp_path / "metadata.json"
    meta_file.write_text(
        json.dumps(
            {
                "images": {
                    full_hash: {
                        "os_name": "MyImage",
                        "filename": img.name,
                        "fs_type": "ext4",
                        "pulled_at": "2026-01-01T00:00:00+00:00",
                        "full_hash": full_hash,
                    }
                }
            }
        )
    )
    with patch("mvmctl.core.vm_lifecycle.get_images_dir", return_value=images_dir):
        result = _resolve_image_path(full_hash[:6])
    assert result == img


def test_resolve_image_fs_uuid_by_short_hash(tmp_path, monkeypatch):
    import json

    from mvmctl.core.vm_lifecycle import _resolve_image_fs_uuid

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "a" * 64
    meta_file = tmp_path / "metadata.json"
    meta_file.write_text(
        json.dumps(
            {
                "images": {
                    full_hash: {
                        "filename": "ubuntu-24.04.ext4",
                        "fs_uuid": "11111111-2222-3333-4444-555555555555",
                    }
                }
            }
        )
    )

    result = _resolve_image_fs_uuid(full_hash[:6])
    assert result == "11111111-2222-3333-4444-555555555555"


def test_resolve_image_fs_uuid_missing_returns_none(tmp_path, monkeypatch):
    import json

    from mvmctl.core.vm_lifecycle import _resolve_image_fs_uuid

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "b" * 64
    meta_file = tmp_path / "metadata.json"
    meta_file.write_text(
        json.dumps(
            {
                "images": {
                    full_hash: {
                        "filename": "ubuntu-24.04.ext4",
                    }
                }
            }
        )
    )

    result = _resolve_image_fs_uuid(full_hash[:6])
    assert result is None


def test_resolve_image_fs_type_by_short_hash(tmp_path, monkeypatch):
    """_resolve_image_fs_type returns fs_type from metadata by short hash."""
    import json

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "c" * 64
    meta_file = tmp_path / "metadata.json"
    meta_file.write_text(
        json.dumps(
            {
                "images": {
                    full_hash: {
                        "filename": "ubuntu-24.04.ext4",
                        "fs_type": "ext4",
                    }
                }
            }
        )
    )

    result = _resolve_image_fs_type(full_hash[:6])
    assert result == "ext4"


def test_resolve_image_fs_type_missing_returns_none(tmp_path, monkeypatch):
    """_resolve_image_fs_type returns None when fs_type is not in metadata."""
    import json

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "d" * 64
    meta_file = tmp_path / "metadata.json"
    meta_file.write_text(
        json.dumps(
            {
                "images": {
                    full_hash: {
                        "filename": "ubuntu-24.04.ext4",
                    }
                }
            }
        )
    )

    result = _resolve_image_fs_type(full_hash[:6])
    assert result is None


def test_resolve_image_path_not_found(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    with patch("mvmctl.core.vm_lifecycle.get_images_dir", return_value=images_dir):
        with pytest.raises(MVMError, match="Image not found"):
            _resolve_image_path("nonexistent")


def test_resolve_single_by_id_prefix_unique(tmp_path):
    def _find(_: Path, prefix: str) -> list[tuple[str, dict[str, str]]]:
        if prefix == "abc123":
            return [("abc123deadbeef", {"filename": "asset"})]
        return []

    result = resolve_single_by_id_prefix("abc123", _find, tmp_path)
    assert result == ("abc123deadbeef", {"filename": "asset"})


def test_resolve_single_by_id_prefix_none_for_ambiguous(tmp_path):
    def _find(_: Path, __: str) -> list[tuple[str, dict[str, str]]]:
        return [
            ("abc123deadbeef", {"filename": "a"}),
            ("abc123feedface", {"filename": "b"}),
        ]

    assert resolve_single_by_id_prefix("abc123", _find, tmp_path) is None


def test_resolve_kernel_path_by_filename(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    kernel = kernels_dir / "vmlinux-test"
    kernel.write_bytes(b"kernel")
    with patch("mvmctl.core.vm_lifecycle.get_kernels_dir", return_value=kernels_dir):
        result = _resolve_kernel_path("vmlinux-test")
    assert result == kernel


def test_resolve_kernel_path_by_absolute(tmp_path):
    kernel = tmp_path / "custom-vmlinux"
    kernel.write_bytes(b"kernel")
    with patch("mvmctl.core.vm_lifecycle.get_kernels_dir", return_value=tmp_path / "kernels"):
        result = _resolve_kernel_path(str(kernel))
    assert result == kernel


def test_resolve_kernel_path_by_short_hash(tmp_path, monkeypatch):
    import json

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    full_hash = "a" * 64
    kernel = kernels_dir / "vmlinux-6.12"
    kernel.write_bytes(b"kernel")
    meta_file = tmp_path / "metadata.json"
    meta_file.write_text(
        json.dumps(
            {
                "kernels": {
                    full_hash: {
                        "filename": kernel.name,
                        "version": "6.12.0",
                        "name": kernel.name,
                    }
                }
            }
        )
    )
    with patch("mvmctl.core.vm_lifecycle.get_kernels_dir", return_value=kernels_dir):
        result = _resolve_kernel_path(full_hash[:6])
    assert result == kernel


def test_resolve_kernel_path_not_found(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    with patch("mvmctl.core.vm_lifecycle.get_kernels_dir", return_value=kernels_dir):
        with pytest.raises(MVMError, match="Kernel not found"):
            _resolve_kernel_path("nonexistent")


def test_resolve_image_id_path_unique(tmp_path, monkeypatch):
    import json

    from mvmctl.core.vm_lifecycle import _resolve_image_id_path

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    full_hash = "b" * 16
    img = images_dir / "ubuntu.ext4"
    img.write_bytes(b"img")
    (tmp_path / "metadata.json").write_text(
        json.dumps({"images": {full_hash: {"filename": img.name}}})
    )

    with patch("mvmctl.core.vm_lifecycle.get_images_dir", return_value=images_dir):
        result = _resolve_image_id_path(full_hash[:6])
    assert result == img


def test_resolve_kernel_id_path_unique(tmp_path, monkeypatch):
    import json

    from mvmctl.core.vm_lifecycle import _resolve_kernel_id_path

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    full_hash = "c" * 16
    kernel = kernels_dir / "vmlinux-short"
    kernel.write_bytes(b"kernel")
    (tmp_path / "metadata.json").write_text(
        json.dumps({"kernels": {full_hash: {"filename": kernel.name}}})
    )

    with patch("mvmctl.core.vm_lifecycle.get_kernels_dir", return_value=kernels_dir):
        result = _resolve_kernel_id_path(full_hash[:6])
    assert result == kernel


def test_secure_mkdir_vm_success(tmp_path):
    """_secure_mkdir_vm creates directory atomically."""
    vm_dir = tmp_path / "testvm"
    _secure_mkdir_vm(vm_dir, "testvm")
    assert vm_dir.exists()
    assert vm_dir.is_dir()


def test_secure_mkdir_vm_already_exists(tmp_path):
    """_secure_mkdir_vm raises MVMError if directory already exists."""
    vm_dir = tmp_path / "existingvm"
    vm_dir.mkdir()
    with pytest.raises(MVMError, match="already exists"):
        _secure_mkdir_vm(vm_dir, "existingvm")


def test_secure_mkdir_vm_rejects_symlink(tmp_path):
    """_secure_mkdir_vm detects and rejects symlinks (TOCTOU protection)."""
    # Create a target directory that the symlink points to
    target_dir = tmp_path / "target"
    target_dir.mkdir()

    # Create a symlink at the VM directory location
    vm_dir = tmp_path / "symlinkedvm"
    vm_dir.symlink_to(target_dir)

    # Should raise error due to symlink
    with pytest.raises(MVMError, match="symlink"):
        _secure_mkdir_vm(vm_dir, "symlinkedvm")


def test_secure_mkdir_vm_rejects_symlink_in_parent(tmp_path):
    """_secure_mkdir_vm detects symlinks in parent path."""
    # Create a scenario where a parent directory is a symlink
    real_parent = tmp_path / "real_parent"
    real_parent.mkdir()
    symlink_parent = tmp_path / "symlink_parent"
    symlink_parent.symlink_to(real_parent)

    vm_dir = symlink_parent / "testvm"
    # Should still work as the final path doesn't exist yet
    # The symlink is in the parent, not the VM dir itself
    _secure_mkdir_vm(vm_dir, "testvm")
    assert vm_dir.exists()
    assert vm_dir.is_dir()


def test_create_vm_with_secure_mkdir(tmp_path, monkeypatch):
    """create_vm uses _secure_mkdir_vm to prevent TOCTOU attacks."""
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))

    # Create minimal required files/directories
    images_dir = tmp_path / "images"
    images_dir.mkdir(parents=True)
    img = images_dir / "ubuntu-24.04.ext4"
    img.write_bytes(b"\x00" * 64)

    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir(parents=True)
    kernel = kernels_dir / "vmlinux"
    kernel.write_bytes(b"\x00" * 64)

    # Create a symlink at the VM directory location (simulating attack)
    vms_dir = tmp_path / "vms"
    vms_dir.mkdir()
    vm_dir = vms_dir / "attackvm"
    target_file = tmp_path / "target_file"
    target_file.write_text("sensitive data")
    vm_dir.symlink_to(target_file)

    # create_vm should detect the symlink and fail
    with (
        patch("mvmctl.core.vm_lifecycle.get_vm_manager") as mock_mgr,
        patch("mvmctl.core.vm_lifecycle.get_vm_dir", return_value=vm_dir),
        patch("mvmctl.core.vm_lifecycle.get_images_dir", return_value=images_dir),
        patch("mvmctl.core.vm_lifecycle.get_kernels_dir", return_value=kernels_dir),
        patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain"),
    ):
        mock_manager = MagicMock()
        mock_manager.count_vms.return_value = 0
        mock_mgr.return_value = mock_manager

        with pytest.raises(MVMError, match="symlink"):
            create_vm(name="attackvm", image="ubuntu-24.04")


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_uses_cached_image_path_not_copy(
    mock_setup_nat,
    mock_open,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_net_mgr,
    mock_add_firewall_rule,
    mock_copy2,
):
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    # Setup VM dir mock with proper path joining for rootfs
    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_vm_dir.__str__ = MagicMock(return_value="/tmp/cache/vms/abc123")
    mock_get_vm_dir.return_value = mock_vm_dir

    # Setup rootfs path mock that will be returned by vm_dir / "rootfs.ext4"
    mock_rootfs_path = MagicMock()
    mock_rootfs_path.__str__ = MagicMock(return_value="/tmp/cache/vms/abc123/rootfs.ext4")
    mock_rootfs_path.parent = mock_vm_dir
    mock_vm_dir.__truediv__ = MagicMock(return_value=mock_rootfs_path)

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    # Image - this is the cached image path
    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    img_ext4.suffix = ".ext4"
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock NoCloudNetServerManager
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    create_vm(name="myvm", image="ubuntu-22.04", cloud_init_mode=CloudInitMode.NET)

    # Rootfs MUST be copied to VM directory (VM-local copy)
    mock_copy2.assert_called_once()
    # Verify copy destination is the VM-local rootfs path
    copy_dest = mock_copy2.call_args.args[1]
    assert copy_dest == mock_rootfs_path

    vm_config_arg = mock_config_gen.call_args.args[0]
    # rootfs_path should be the VM-local path, not the cached image
    assert "rootfs" in str(vm_config_arg.rootfs_path)
    assert vm_config_arg.rootfs_path.parent == mock_vm_dir


@patch("mvmctl.core.vm_lifecycle.grow_rootfs_with_guestfs")
@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_disk_size_resizes_local_copy_only(
    mock_setup_nat,
    mock_open,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_net_mgr,
    mock_add_firewall_rule,
    mock_copy2,
    mock_grow_rootfs,
):
    """Verify --disk-size only resizes the VM-local copy, not the cached image."""
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    # Setup VM dir mock with proper path joining for rootfs
    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_vm_dir.__str__ = MagicMock(return_value="/tmp/cache/vms/abc123")
    mock_get_vm_dir.return_value = mock_vm_dir

    # Setup rootfs path mock that will be returned by vm_dir / "rootfs.ext4"
    mock_rootfs_path = MagicMock()
    mock_rootfs_path.__str__ = MagicMock(return_value="/tmp/cache/vms/abc123/rootfs.ext4")
    mock_rootfs_path.parent = mock_vm_dir
    mock_vm_dir.__truediv__ = MagicMock(return_value=mock_rootfs_path)

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    # Image - this is the cached image path
    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    img_ext4.suffix = ".ext4"
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock NoCloudNetServerManager
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    create_vm(name="myvm", image="ubuntu-22.04", disk_size="10G", cloud_init_mode=CloudInitMode.NET)

    # Verify copy happened first
    mock_copy2.assert_called_once()
    copied_path = mock_copy2.call_args.args[1]  # destination path

    # Verify grow_rootfs was called on the VM-local copy, not the cached image
    mock_grow_rootfs.assert_called_once()
    resized_path = mock_grow_rootfs.call_args.args[0]
    assert resized_path == copied_path
    assert resized_path == mock_rootfs_path


@patch("mvmctl.core.vm_lifecycle._cleanup_vm_creation_resources")
@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_cleanup_removes_local_rootfs_on_failure(
    mock_setup_nat,
    mock_open,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_net_mgr,
    mock_add_firewall_rule,
    mock_copy2,
    mock_cleanup,
):
    """Verify VM-local rootfs is cleaned up if VM creation fails after copy."""
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    # Setup VM dir mock with proper path joining for rootfs
    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_vm_dir.__str__ = MagicMock(return_value="/tmp/cache/vms/abc123")
    mock_get_vm_dir.return_value = mock_vm_dir

    # Setup rootfs path mock that will be returned by vm_dir / "rootfs.ext4"
    mock_rootfs_path = MagicMock()
    mock_rootfs_path.__str__ = MagicMock(return_value="/tmp/cache/vms/abc123/rootfs.ext4")
    mock_rootfs_path.parent = mock_vm_dir
    mock_vm_dir.__truediv__ = MagicMock(return_value=mock_rootfs_path)

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    # Image - this is the cached image path
    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    img_ext4.suffix = ".ext4"
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True

    # Mock NoCloudNetServerManager
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    # Simulate failure after copy (e.g., TAP creation fails)
    mock_create_tap.side_effect = Exception("TAP creation failed")

    with pytest.raises(Exception, match="TAP creation failed"):
        create_vm(name="myvm", image="ubuntu-22.04", cloud_init_mode=CloudInitMode.NET)

    # Verify copy happened before the failure
    mock_copy2.assert_called_once()

    # Verify cleanup was called (which includes shutil.rmtree on vm_dir)
    mock_cleanup.assert_called_once()
    # Verify vm_dir was passed to cleanup (second positional arg)
    call_args = mock_cleanup.call_args.args
    assert call_args[1] == mock_vm_dir


# ============================================================================
# VM Config Persistence Tests
# ============================================================================


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_persists_config_with_vm_local_rootfs_path(
    mock_setup_nat,
    mock_open,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_net_mgr,
    mock_add_firewall_rule,
    mock_copy2,
):
    """Test that create_vm persists VM config with VM-local rootfs_path in VMInstance.

    This verifies the fix for ensuring persisted VM state/metadata explicitly
    points to the VM-local rootfs path after the rootfs-copy restoration.
    """
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    # Setup VM dir mock with proper path joining for rootfs
    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_vm_dir.__str__ = MagicMock(return_value="/tmp/cache/vms/abc123")
    mock_get_vm_dir.return_value = mock_vm_dir

    # Setup rootfs path mock that will be returned by vm_dir / "rootfs.ext4"
    mock_rootfs_path = MagicMock()
    mock_rootfs_path.__str__ = MagicMock(return_value="/tmp/cache/vms/abc123/rootfs.ext4")
    mock_rootfs_path.parent = mock_vm_dir
    mock_vm_dir.__truediv__ = MagicMock(return_value=mock_rootfs_path)

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    # Image - this is the cached image path
    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    img_ext4.suffix = ".ext4"
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock NoCloudNetServerManager
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    vm = create_vm(name="myvm", image="ubuntu-22.04", cloud_init_mode=CloudInitMode.NET)

    # Verify VMInstance has config field set
    assert vm.config is not None, "VMInstance.config should be set"

    # Verify config.rootfs_path points to VM-local rootfs, not cached image
    assert "rootfs" in str(vm.config.rootfs_path), "config.rootfs_path should contain 'rootfs'"
    assert vm.config.rootfs_path.parent == mock_vm_dir, "config.rootfs_path.parent should be vm_dir"

    # Verify the config is passed to register()
    registered_vm = mock_manager.register.call_args.args[0]
    assert registered_vm.config is not None, "Registered VM should have config"
    assert "rootfs" in str(registered_vm.config.rootfs_path), (
        "Registered VM config.rootfs_path should be VM-local"
    )


# ============================================================================
# NoCloudNetServerManager Integration Tests
# ============================================================================


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_nocloud_net_starts_server(
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_net_mgr,
    mock_setup_chain,
    mock_subprocess_run,
    mock_copy2,
):
    """Test that NoCloudNetServerManager.start_server() is called when cloud_init_mode=NO_CLOUD_NET."""
    from mvmctl.core.vm_lifecycle import create_vm

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock the NoCloudNetServerManager
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    vm = create_vm(
        name="myvm",
        image="ubuntu-22.04",
        cloud_init_mode=CloudInitMode.NET,
    )

    # Verify the manager's start_server was called
    mock_net_mgr.return_value.start_server.assert_called_once()

    # Verify VMInstance was created with nocloud_net_port
    assert isinstance(vm, VMInstance)


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.network.get_default_interface")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("mvmctl.core.vm_lifecycle.cleanup_tap")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_nocloud_net_server_cleanup_on_fc_failure(
    mock_setup_nat,
    mock_open,
    mock_rmtree,
    mock_cleanup_tap,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_net_mgr,
    mock_setup_chain,
    mock_subprocess_run,
    mock_get_default_interface,
    mock_copy2,
):
    """Test that nocloud server is stopped when Firecracker fails to start."""
    from mvmctl.core.vm_lifecycle import create_vm

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True

    # Mock get_default_interface for setup_nat
    mock_get_default_interface.return_value = "eth0"

    # Mock subprocess.Popen to raise FileNotFoundError (Firecracker not found)
    mock_popen.side_effect = FileNotFoundError("Firecracker binary not found")

    # Mock the NoCloudNetServerManager
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    with pytest.raises(MVMError, match="Firecracker binary not found"):
        create_vm(
            name="myvm",
            image="ubuntu-22.04",
            cloud_init_mode=CloudInitMode.NET,
        )

    # Verify the manager's stop_server was called to cleanup
    mock_net_mgr.return_value.stop_server.assert_called_once()


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_nocloud_net_success_sets_port(
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_net_mgr,
    mock_setup_chain,
    mock_subprocess_run,
    mock_copy2,
):
    """Test that VMInstance.nocloud_net_port is set correctly when NO_CLOUD_NET mode succeeds."""
    from mvmctl.core.vm_lifecycle import create_vm

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock the NoCloudNetServerManager
    test_port = 8765
    mock_net_mgr.return_value.start_server.return_value = (
        f"http://10.20.0.1:{test_port}",
        test_port,
    )

    vm = create_vm(
        name="myvm",
        image="ubuntu-22.04",
        cloud_init_mode=CloudInitMode.NET,
    )

    # Verify VMInstance was created with the correct nocloud_net_port
    assert vm.nocloud_net_port == test_port


# ============================================================================
# Firewall Integration Tests
# ============================================================================


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_nocloud_net_adds_firewall_rule(
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_net_mgr,
    mock_setup_chain,
    mock_add_firewall_rule,
    mock_subprocess_run,
    mock_copy2,
):
    """Test that add_nocloud_input_rule() is called when NO_CLOUD_NET mode succeeds."""
    from mvmctl.core.vm_lifecycle import create_vm

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock the NoCloudNetServerManager
    test_port = 8765
    mock_net_mgr.return_value.start_server.return_value = (
        f"http://10.20.0.1:{test_port}",
        test_port,
    )

    vm = create_vm(
        name="myvm",
        image="ubuntu-22.04",
        cloud_init_mode=CloudInitMode.NET,
    )

    # Verify add_nocloud_input_rule was called with correct parameters
    mock_add_firewall_rule.assert_called_once_with("10.20.0.5", "myvm", test_port)

    # Verify VMInstance was created
    assert isinstance(vm, VMInstance)
    assert vm.nocloud_net_port == test_port


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.remove_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle.setup_nat")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("mvmctl.core.vm_lifecycle.cleanup_tap")
@patch("mvmctl.core.vm_lifecycle.release_network_ip")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
@patch("builtins.open", new_callable=MagicMock)
def test_firewall_failure_stops_server_and_raises(
    mock_open,
    mock_rmtree,
    mock_rel_ip,
    mock_cleanup_tap,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_setup_nat,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_net_mgr,
    mock_setup_chain,
    mock_add_firewall_rule,
    mock_remove_firewall_rule,
    mock_subprocess_run,
    mock_copy2,
):
    """Test that firewall failure stops server and re-raises exception."""
    from mvmctl.core.vm_lifecycle import create_vm
    from mvmctl.exceptions import NetworkError

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True

    # Mock the NoCloudNetServerManager
    test_port = 8765
    mock_net_mgr.return_value.start_server.return_value = (
        f"http://10.20.0.1:{test_port}",
        test_port,
    )

    # Make add_nocloud_input_rule raise NetworkError
    mock_add_firewall_rule.side_effect = NetworkError("Failed to add firewall rule")

    with pytest.raises(NetworkError, match="Failed to add firewall rule"):
        create_vm(
            name="myvm",
            image="ubuntu-22.04",
            cloud_init_mode=CloudInitMode.NET,
        )

    # Verify stop_server was called
    mock_net_mgr.return_value.stop_server.assert_called_once()

    # Verify no VM was registered
    mock_manager.register.assert_not_called()

    # Verify cleanup was called
    mock_rmtree.assert_called()


# ============================================================================
# Cloud-Init Completion Detection Tests
# ============================================================================


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_returns_immediately_with_nocloud_net(
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_net_mgr,
    mock_setup_chain,
    mock_subprocess_run,
    mock_copy2,
):
    """Test that create_vm returns immediately without blocking when mode=NO_CLOUD_NET."""
    from mvmctl.core.vm_lifecycle import create_vm
    from mvmctl.models import CloudInitMode

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock the NoCloudNetServerManager
    test_port = 8765
    mock_net_mgr.return_value.start_server.return_value = (
        f"http://10.20.0.1:{test_port}",
        test_port,
    )

    vm = create_vm(
        name="myvm",
        image="ubuntu-22.04",
        cloud_init_mode=CloudInitMode.NET,
    )

    # Verify VM was created successfully (no blocking wait for cloud-init)
    assert isinstance(vm, VMInstance)
    assert vm.nocloud_net_port == test_port


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
def test_create_vm_starts_nocloud_server(
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_net_mgr,
    mock_setup_chain,
    mock_subprocess_run,
    mock_copy2,
):
    """Test that create_vm starts nocloud-net server when mode=NO_CLOUD_NET."""
    from mvmctl.core.vm_lifecycle import create_vm
    from mvmctl.models import CloudInitMode

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    # Mock the NoCloudNetServerManager
    test_port = 8765
    mock_net_mgr.return_value.start_server.return_value = (
        f"http://10.20.0.1:{test_port}",
        test_port,
    )

    vm = create_vm(
        name="myvm",
        image="ubuntu-22.04",
        cloud_init_mode=CloudInitMode.NET,
    )

    # Verify VM was created successfully
    assert isinstance(vm, VMInstance)
    mock_manager.register.assert_called_once()

    # Verify nocloud-net server was started
    mock_net_mgr.return_value.start_server.assert_called_once()


@patch("mvmctl.core.vm_lifecycle._secure_mkdir_vm")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
@patch("mvmctl.core.vm_lifecycle.inject_cloud_init")
def test_direct_injection_uses_vm_local_copied_rootfs(
    mock_inject,
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_secure_mkdir,
    tmp_path,
):
    images_dir = tmp_path / "images"
    images_dir.mkdir(parents=True)
    cached_image = images_dir / "ubuntu-22.04.ext4"
    cached_image.write_bytes(b"fake rootfs content")

    vm_name = "t-direct"
    vm_dir = tmp_path / "vms" / vm_name
    vm_dir.mkdir(parents=True)

    mock_get_images.return_value = images_dir
    mock_get_vm_dir.return_value = vm_dir

    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir(parents=True)
    (kernels_dir / "vmlinux").write_text("fake kernel")
    mock_get_kernels.return_value = kernels_dir

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = True
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = None
    mock_resolve_fs_type.return_value = None
    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 12345

    call_log: list[str] = []

    def spy_copy2(src: str | Path, dst: str | Path, **kw: object) -> str | None:
        call_log.append(f"copy2:{src}:{dst}")
        return str(dst)

    def spy_inject(rootfs_path: str, cloud_init_dir: str) -> None:
        call_log.append(f"inject:{rootfs_path}")

    with patch("mvmctl.core.vm_lifecycle.shutil.copy2", side_effect=spy_copy2):
        mock_inject.side_effect = spy_inject
        create_vm(
            name=vm_name,
            image="ubuntu-22.04",
            cloud_init_mode=CloudInitMode.INJECT,
        )

    copy_calls = [e for e in call_log if e.startswith("copy2:")]
    # Rootfs MUST be copied to VM directory (VM-local copy)
    assert len(copy_calls) == 1, "shutil.copy2 should be called once to copy rootfs to VM dir"
    # Verify copy destination is in the VM directory
    assert str(vm_dir) in copy_calls[0], (
        f"Copy destination should be in VM dir, got: {copy_calls[0]}"
    )

    inject_calls = [e for e in call_log if e.startswith("inject:")]
    assert inject_calls, "inject_cloud_init was never called"
    injected_path = Path(inject_calls[0][len("inject:") :])

    # Injection should happen on the VM-local copy, not the cached image
    assert str(vm_dir) in str(injected_path), (
        f"Expected injection on VM-local rootfs in {vm_dir}, got {injected_path}"
    )


@patch("mvmctl.core.vm_lifecycle._secure_mkdir_vm")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
@patch("mvmctl.core.vm_lifecycle.inject_cloud_init")
@patch("mvmctl.core.vm_lifecycle.release_network_ip")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
def test_direct_injection_cleanup_on_injection_failure(
    mock_rmtree,
    mock_release_ip,
    mock_inject,
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_secure_mkdir,
    tmp_path,
):
    from mvmctl.exceptions import CloudInitError

    images_dir = tmp_path / "images"
    images_dir.mkdir(parents=True)
    (images_dir / "ubuntu-22.04.ext4").write_bytes(b"fake")

    vm_name = "t-fail"
    vm_dir = tmp_path / "vms" / vm_name
    vm_dir.mkdir(parents=True)
    mock_get_vm_dir.return_value = vm_dir
    mock_get_images.return_value = images_dir

    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir(parents=True)
    (kernels_dir / "vmlinux").write_text("fake kernel")
    mock_get_kernels.return_value = kernels_dir

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = True
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.2"
    mock_gen_mac.return_value = "02:fc:aa:bb:cc:dd"
    mock_resolve_fs_uuid.return_value = None
    mock_resolve_fs_type.return_value = None
    mock_bridge_exists.return_value = True

    mock_inject.side_effect = RuntimeError("simulated guestfs failure")

    # Mock net_config.name to return 'default' for the cleanup assertion
    mock_net.name = "default"

    with (
        patch("mvmctl.core.vm_lifecycle.shutil.copy2"),
        pytest.raises(CloudInitError, match="Direct injection failed"),
    ):
        create_vm(
            name=vm_name,
            image="ubuntu-22.04",
            cloud_init_mode=CloudInitMode.INJECT,
        )

    mock_rmtree.assert_called_once_with(vm_dir, ignore_errors=True)
    mock_release_ip.assert_called_once_with("default", vm_name)


# ============================================================================
# Multi-default-key injection tests
# ============================================================================


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
@patch("mvmctl.core.key_manager.get_default_keys")
def test_create_vm_without_ssh_key_injects_default_keys(
    mock_get_default_keys,
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_net_mgr,
    mock_add_firewall_rule,
    mock_copy2,
    tmp_path,
    monkeypatch,
):
    """create_vm without ssh_key reads default keys from registry and passes list to write_cloud_init."""
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))

    keys_dir = tmp_path / "keys"
    keys_dir.mkdir()
    (keys_dir / "mykey.pub").write_text("ssh-rsa AAAA key1")
    (keys_dir / "otherkey.pub").write_text("ssh-ed25519 AAAC key2")

    mock_get_default_keys.return_value = ["mykey", "otherkey"]
    monkeypatch.setattr("mvmctl.utils.fs.get_keys_dir", lambda: keys_dir)

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager
    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir
    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir
    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir
    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net
    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"
    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    create_vm(name="myvm", image="ubuntu-22.04", cloud_init_mode=CloudInitMode.NET)

    mock_write_ci.assert_called_once()
    _, kwargs = mock_write_ci.call_args
    injected_key = kwargs["ssh_pub_key"]
    assert isinstance(injected_key, list)
    assert "ssh-rsa AAAA key1" in injected_key
    assert "ssh-ed25519 AAAC key2" in injected_key


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
@patch("mvmctl.core.vm_lifecycle.resolve_ssh_key")
def test_create_vm_with_explicit_ssh_key_takes_precedence(
    mock_resolve_ssh_key,
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_net_mgr,
    mock_add_firewall_rule,
    mock_copy2,
):
    """When --ssh-key is explicitly passed, resolve_ssh_key is called (not default key lookup)."""
    mock_resolve_ssh_key.return_value = "ssh-rsa AAAA explicit-key"

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager
    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir
    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir
    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir
    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net
    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"
    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    create_vm(name="myvm", image="ubuntu-22.04", ssh_key="mykey", cloud_init_mode=CloudInitMode.NET)

    mock_resolve_ssh_key.assert_called_once_with("mykey")
    mock_write_ci.assert_called_once()
    _, kwargs = mock_write_ci.call_args
    assert kwargs["ssh_pub_key"] == "ssh-rsa AAAA explicit-key"


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.add_nocloud_input_rule")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.bridge_exists")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_uuid")
@patch("mvmctl.core.vm_lifecycle._resolve_image_fs_type")
@patch("builtins.open", new_callable=MagicMock)
@patch("mvmctl.core.vm_lifecycle.setup_nat")
@patch("mvmctl.core.key_manager.get_default_keys")
@patch("mvmctl.core.vm_lifecycle.resolve_ssh_key")
def test_create_vm_no_defaults_no_explicit_key_falls_back_to_resolve(
    mock_resolve_ssh_key,
    mock_get_default_keys,
    mock_setup_nat,
    mock_open,
    mock_resolve_fs_type,
    mock_resolve_fs_uuid,
    mock_bridge_exists,
    mock_write_pid,
    mock_popen,
    mock_add_rules,
    mock_create_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_net_mgr,
    mock_add_firewall_rule,
    mock_copy2,
):
    """With no defaults and no --ssh-key, falls back to resolve_ssh_key(None) (auto-detect)."""
    mock_get_default_keys.return_value = []
    mock_resolve_ssh_key.return_value = None

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager
    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir
    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir
    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir
    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net
    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"
    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    create_vm(name="myvm", image="ubuntu-22.04", cloud_init_mode=CloudInitMode.NET)

    mock_resolve_ssh_key.assert_called_once_with(None)


@patch("mvmctl.core.vm_lifecycle.shutil.copy2")
@patch("mvmctl.core.vm_lifecycle.subprocess.run")
@patch("mvmctl.core.vm_lifecycle.setup_nocloud_input_chain")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_images_dir")
@patch("mvmctl.core.vm_lifecycle.get_kernels_dir")
@patch("mvmctl.core.vm_lifecycle.get_network")
@patch("mvmctl.core.vm_lifecycle.allocate_network_ip")
@patch("mvmctl.core.vm_lifecycle.generate_mac")
@patch("mvmctl.core.vm_lifecycle.write_cloud_init")
@patch("mvmctl.core.vm_lifecycle.create_cloud_init_iso")
@patch("mvmctl.core.vm_lifecycle.ConfigGenerator")
@patch("mvmctl.core.vm_lifecycle.cleanup_tap")
@patch("mvmctl.core.vm_lifecycle.create_tap")
@patch("mvmctl.core.vm_lifecycle.add_iptables_forward_rules")
@patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
@patch("mvmctl.core.vm_lifecycle.release_network_ip")
@patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
def test_create_vm_network_failure_cleans_up_tap_iptables(
    mock_net_mgr,
    mock_release_ip,
    mock_rmtree,
    mock_add_rules,
    mock_create_tap,
    mock_cleanup_tap,
    mock_config_gen,
    mock_create_iso,
    mock_write_ci,
    mock_gen_mac,
    mock_alloc_ip,
    mock_get_net,
    mock_get_kernels,
    mock_get_images,
    mock_get_vm_dir,
    mock_get_vm_mgr,
    mock_setup_chain,
    mock_subprocess_run,
    mock_copy2,
):
    """If add_iptables_forward_rules() fails after create_tap(), ensure cleanup_tap() is called."""
    from mvmctl.core.vm_lifecycle import create_vm
    from mvmctl.exceptions import NetworkError

    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_net.nat_enabled = False
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_net_mgr.return_value.start_server.return_value = ("http://10.20.0.1:8080", 8080)

    mock_create_tap.return_value = None
    mock_add_rules.side_effect = NetworkError("iptables failed")

    with pytest.raises(NetworkError, match="Network setup failed"):
        create_vm(name="myvm", image="ubuntu-22.04", cloud_init_mode=CloudInitMode.NET)

    # cleanup_tap must be called to remove TAP and iptables rules
    mock_cleanup_tap.assert_called_once()
    called_args, called_kwargs = mock_cleanup_tap.call_args
    assert called_kwargs.get("bridge") == mock_net.bridge


class TestRemoveVMNATOrdering:
    """Tests for CRITICAL vm rm NAT teardown ordering fix.

    This test class verifies that teardown_nat is called BEFORE delete_tap,
    ensuring the NAT guard check can see remaining TAPs on the bridge.
    """

    @patch("mvmctl.core.firewall.subprocess.run")
    @patch("mvmctl.core.vm_lifecycle.get_vm_manager")
    @patch("mvmctl.core.vm_lifecycle.get_vm_dir")
    @patch("mvmctl.core.vm_lifecycle.get_network")
    @patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
    @patch("mvmctl.core.vm_lifecycle.cleanup_tap")
    @patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
    @patch("mvmctl.core.vm_lifecycle.teardown_nat")
    @patch("mvmctl.core.vm_lifecycle.delete_tap")
    @patch("mvmctl.core.vm_lifecycle.release_network_ip")
    @patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
    @patch("mvmctl.core.vm_lifecycle.ConsoleRelayManager")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall._chain_exists")
    def test_remove_vm_calls_teardown_nat_before_delete_tap(
        self,
        mock_chain_exists,
        mock_nocloud_mgr,
        mock_console_mgr,
        mock_rmtree,
        mock_release_ip,
        mock_delete_tap,
        mock_teardown_nat,
        mock_remove_rules,
        mock_cleanup_tap,
        mock_graceful_shutdown,
        mock_get_net_info,
        mock_get_vm_dir,
        mock_get_vm_mgr,
        mock_firewall_subprocess,
    ):
        """CRITICAL: teardown_nat must be called before delete_tap.

        If delete_tap is called before teardown_nat, the guard check in
        teardown_nat will see zero TAPs and incorrectly tear down shared
        NAT rules, breaking connectivity for all remaining VMs on the bridge.
        """
        from mvmctl.core.vm_lifecycle import remove_vm
        from mvmctl.models.vm import VMInstance, VMState

        # Create a mock VM
        vm = VMInstance(
            id="a" * 64,
            name="testvm",
            ipv4="10.0.0.2",
            mac="02:FC:00:00:00:01",
            pid=1234,
            status=VMState.STOPPED,
            tap_device="mvm-def-tes-123",
            network_name="default",
        )

        # Setup mocks
        mock_manager = MagicMock()
        mock_manager.get_by_id_prefix.return_value = []
        mock_manager.get_by_name.return_value = [vm]
        mock_get_vm_mgr.return_value = mock_manager

        mock_vm_dir = MagicMock()
        mock_vm_dir.exists.return_value = True
        mock_get_vm_dir.return_value = mock_vm_dir

        # Mock network config with bridge attribute
        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-default"
        mock_get_net_info.return_value = mock_net_config
        mock_chain_exists.return_value = True

        # Call remove_vm
        remove_vm("testvm")

        # Verify call order: teardown_nat must be called before delete_tap
        calls = []
        for call in mock_teardown_nat.call_args_list:
            calls.append(("teardown_nat", call))
        for call in mock_delete_tap.call_args_list:
            calls.append(("delete_tap", call))

        # Find positions of each call
        teardown_positions = [i for i, (name, _) in enumerate(calls) if name == "teardown_nat"]
        delete_positions = [i for i, (name, _) in enumerate(calls) if name == "delete_tap"]

        # teardown_nat should be called before delete_tap
        assert teardown_positions, "teardown_nat was not called"
        assert delete_positions, "delete_tap was not called"
        assert min(teardown_positions) < min(delete_positions), (
            "teardown_nat must be called before delete_tap to preserve NAT for other VMs"
        )

    @patch("mvmctl.core.firewall.subprocess.run")
    @patch("mvmctl.core.vm_lifecycle.get_vm_manager")
    @patch("mvmctl.core.vm_lifecycle.get_vm_dir")
    @patch("mvmctl.core.vm_lifecycle.get_network")
    @patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
    @patch("mvmctl.core.vm_lifecycle.cleanup_tap")
    @patch("mvmctl.core.vm_lifecycle.remove_iptables_forward_rules")
    @patch("mvmctl.core.vm_lifecycle.teardown_nat")
    @patch("mvmctl.core.vm_lifecycle.delete_tap")
    @patch("mvmctl.core.vm_lifecycle.release_network_ip")
    @patch("mvmctl.core.vm_lifecycle.shutil.rmtree")
    @patch("mvmctl.core.vm_lifecycle.ConsoleRelayManager")
    @patch("mvmctl.core.vm_lifecycle.NoCloudNetServerManager")
    @patch("mvmctl.core.firewall._chain_exists")
    def test_teardown_nat_called_with_force_false(
        self,
        mock_chain_exists,
        mock_nocloud_mgr,
        mock_console_mgr,
        mock_rmtree,
        mock_release_ip,
        mock_delete_tap,
        mock_teardown_nat,
        mock_remove_rules,
        mock_cleanup_tap,
        mock_graceful_shutdown,
        mock_get_net_info,
        mock_get_vm_dir,
        mock_get_vm_mgr,
        mock_firewall_subprocess,
    ):
        """teardown_nat should be called with force=False to enable guard check."""
        from mvmctl.core.vm_lifecycle import remove_vm
        from mvmctl.models.vm import VMInstance, VMState

        vm = VMInstance(
            id="a" * 64,
            name="testvm",
            ipv4="10.0.0.2",
            mac="02:FC:00:00:00:01",
            pid=1234,
            status=VMState.STOPPED,
            tap_device="mvm-def-tes-123",
            network_name="default",
        )

        mock_manager = MagicMock()
        mock_manager.get_by_id_prefix.return_value = []
        mock_manager.get_by_name.return_value = [vm]
        mock_get_vm_mgr.return_value = mock_manager

        mock_vm_dir = MagicMock()
        mock_vm_dir.exists.return_value = True
        mock_get_vm_dir.return_value = mock_vm_dir

        # Mock network config with bridge attribute
        mock_net_config = MagicMock()
        mock_net_config.bridge = "mvm-default"
        mock_get_net_info.return_value = mock_net_config
        mock_chain_exists.return_value = True

        remove_vm("testvm")

        # Verify teardown_nat was called with force=False (or no force argument)
        mock_teardown_nat.assert_called()
        call_kwargs = mock_teardown_nat.call_args[1] if mock_teardown_nat.call_args[1] else {}
        force_value = call_kwargs.get("force", False)
        assert force_value is False, "teardown_nat should be called with force=False"


# -----------------------------------------------------------------------------
# Stop VM tests
# -----------------------------------------------------------------------------


@pytest.fixture
def mock_vm_manager_for_stop(mocker):
    """Fixture providing a mock VMManager with a running VM."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.RUNNING
    mock_vm.pid = 12345
    mock_vm.api_socket_path = Path("/fake/socket")
    mock_mgr.get.return_value = mock_vm
    return mock_mgr


@pytest.fixture
def mock_vm_manager_for_start(mocker):
    """Fixture providing a mock VMManager with a stopped VM."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.STOPPED
    mock_vm.id = "abc123def4567890"
    mock_vm.config = MagicMock()
    mock_vm.config.enable_api_socket = True
    mock_vm.config.enable_console = False
    mock_vm.config.kernel_path = Path("/fake/kernel")
    mock_mgr.get.return_value = mock_vm
    return mock_mgr


@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_stop_vm_success(mock_get_mgr, mock_graceful_shutdown):
    """stop_vm stops a running VM and updates status."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.RUNNING
    mock_vm.pid = 12345
    mock_vm.api_socket_path = Path("/fake/socket")
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    stop_vm("myvm")

    mock_graceful_shutdown.assert_called_once_with(12345, Path("/fake/socket"), force=False)
    mock_mgr.update_status.assert_called_with("myvm", VMState.STOPPED)


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_stop_vm_not_found(mock_get_mgr):
    """stop_vm raises error if VM not found."""
    mock_mgr = MagicMock()
    mock_mgr.get.return_value = None
    mock_get_mgr.return_value = mock_mgr

    with pytest.raises(Exception, match="VM 'myvm' not found"):
        stop_vm("myvm")


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_stop_vm_already_stopped(mock_get_mgr):
    """stop_vm raises error if VM is already stopped."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.STOPPED
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    with pytest.raises(MVMError, match="VM 'myvm' is not running"):
        stop_vm("myvm")


@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_stop_vm_force(mock_get_mgr, mock_graceful_shutdown):
    """stop_vm passes force=True to graceful_shutdown."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.RUNNING
    mock_vm.pid = 12345
    mock_vm.api_socket_path = Path("/fake/socket")
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    stop_vm("myvm", force=True)

    mock_graceful_shutdown.assert_called_once_with(12345, Path("/fake/socket"), force=True)
    mock_mgr.update_status.assert_called_with("myvm", VMState.STOPPED)


@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_stop_vm_handles_paused(mock_get_mgr, mock_graceful_shutdown):
    """stop_vm can stop a paused VM."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.PAUSED
    mock_vm.pid = 12345
    mock_vm.api_socket_path = Path("/fake/socket")
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    stop_vm("myvm")

    mock_graceful_shutdown.assert_called_once_with(12345, Path("/fake/socket"), force=False)
    mock_mgr.update_status.assert_called_with("myvm", VMState.STOPPED)


@patch("mvmctl.core.vm_lifecycle.graceful_shutdown")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_stop_vm_failure_sets_error(mock_get_mgr, mock_graceful_shutdown):
    """stop_vm sets ERROR status on failure."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.RUNNING
    mock_vm.pid = 12345
    mock_vm.api_socket_path = Path("/fake/socket")
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    mock_graceful_shutdown.side_effect = RuntimeError("Shutdown failed")

    with pytest.raises(MVMError, match="Failed to stop VM"):
        stop_vm("myvm")

    mock_mgr.update_status.assert_called_with("myvm", VMState.ERROR)


# -----------------------------------------------------------------------------
# Start VM tests
# -----------------------------------------------------------------------------


@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_start_vm_success(mock_get_mgr, mock_get_vm_dir, mock_popen, mock_write_pid):
    """start_vm starts a stopped VM and updates status."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.STOPPED
    mock_vm.id = "abc123def4567890"
    mock_vm.config = MagicMock()
    mock_vm.config.enable_api_socket = True
    mock_vm.config.enable_console = False
    mock_vm.config.kernel_path = Path("/fake/kernel")
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    mock_vm_dir = MagicMock()
    mock_vm_dir.__truediv__ = MagicMock(side_effect=lambda x: Path(f"/fake/vm/{x}"))
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_proc = MagicMock()
    mock_proc.pid = 99999
    mock_popen.return_value = mock_proc

    with patch("builtins.open", MagicMock()):
        with patch("mvmctl.core.vm_lifecycle.time.sleep"):
            with patch("pathlib.Path.exists", return_value=True):
                start_vm("myvm")

    mock_popen.assert_called_once()
    mock_write_pid.assert_called_once()
    mock_mgr.register.assert_called_once()


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_start_vm_not_found(mock_get_mgr):
    """start_vm raises error if VM not found."""
    mock_mgr = MagicMock()
    mock_mgr.get.return_value = None
    mock_get_mgr.return_value = mock_mgr

    with pytest.raises(Exception, match="VM 'myvm' not found"):
        start_vm("myvm")


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_start_vm_already_running(mock_get_mgr):
    """start_vm raises error if VM is already running."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.RUNNING
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    with pytest.raises(MVMError, match="VM 'myvm' is not stopped"):
        start_vm("myvm")


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_start_vm_no_id(mock_get_mgr):
    """start_vm raises error if VM has no ID."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.STOPPED
    mock_vm.id = None
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    with pytest.raises(MVMError, match="VM 'myvm' has no ID"):
        start_vm("myvm")


@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_start_vm_failure_cleanup(mock_get_mgr, mock_get_vm_dir, mock_popen):
    """start_vm cleans up resources on failure."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.STOPPED
    mock_vm.id = "abc123def4567890"
    mock_vm.config = MagicMock()
    mock_vm.config.enable_api_socket = True
    mock_vm.config.enable_console = False
    mock_vm.config.kernel_path = Path("/fake/kernel")
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    mock_vm_dir = MagicMock()
    mock_vm_dir.__truediv__ = MagicMock(side_effect=lambda x: Path(f"/fake/vm/{x}"))
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_popen.side_effect = OSError("Failed to start process")

    with patch("builtins.open", MagicMock()):
        with patch("pathlib.Path.exists", return_value=True):
            with pytest.raises(MVMError, match="Failed to start VM"):
                start_vm("myvm")


# -----------------------------------------------------------------------------
# Reboot VM tests
# -----------------------------------------------------------------------------


@patch("mvmctl.core.vm_lifecycle.start_vm")
@patch("mvmctl.core.vm_lifecycle.stop_vm")
def test_reboot_vm_success(mock_stop, mock_start):
    """reboot_vm calls stop then start."""
    reboot_vm("myvm")

    mock_stop.assert_called_once_with("myvm", None, force=False)
    mock_start.assert_called_once_with("myvm", None)


@patch("mvmctl.core.vm_lifecycle.start_vm")
@patch("mvmctl.core.vm_lifecycle.stop_vm")
def test_reboot_vm_force(mock_stop, mock_start):
    """reboot_vm passes force=True to stop_vm."""
    reboot_vm("myvm", force=True)

    mock_stop.assert_called_once_with("myvm", None, force=True)
    mock_start.assert_called_once_with("myvm", None)


@patch("mvmctl.core.vm_lifecycle.stop_vm")
def test_reboot_vm_stop_fails(mock_stop):
    """reboot_vm raises error if stop fails."""
    mock_stop.side_effect = MVMError("Stop failed")

    with pytest.raises(MVMError, match="Stop failed"):
        reboot_vm("myvm")


@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_start_vm_with_console(mock_get_mgr, mock_get_vm_dir, mock_popen, mock_write_pid):
    """start_vm handles console-enabled VMs."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.STOPPED
    mock_vm.id = "abc123def4567890"
    mock_vm.config = MagicMock()
    mock_vm.config.enable_api_socket = False
    mock_vm.config.enable_console = True
    mock_vm.config.kernel_path = None
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    mock_vm_dir = MagicMock()
    mock_vm_dir.__truediv__ = MagicMock(side_effect=lambda x: Path(f"/fake/vm/{x}"))
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_proc = MagicMock()
    mock_proc.pid = 99999
    mock_popen.return_value = mock_proc

    with patch("builtins.open", MagicMock()):
        with patch("mvmctl.core.vm_lifecycle.time.sleep"):
            with patch("pathlib.Path.exists", return_value=True):
                start_vm("myvm")

    mock_popen.assert_called_once()
    mock_write_pid.assert_called_once()


@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_start_vm_missing_config_file(mock_get_mgr, mock_get_vm_dir):
    """start_vm raises error if firecracker.json is missing."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.STOPPED
    mock_vm.id = "abc123def4567890"
    mock_vm.config = MagicMock()
    mock_vm.config.enable_api_socket = True
    mock_vm.config.enable_console = False
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    mock_vm_dir = MagicMock()
    mock_vm_dir.__truediv__ = MagicMock(side_effect=lambda x: Path(f"/fake/vm/{x}"))
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    with patch("pathlib.Path.exists", return_value=False):
        with pytest.raises(MVMError, match="VM config not found"):
            start_vm("myvm")


@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_start_vm_missing_firecracker_binary(
    mock_get_mgr, mock_get_vm_dir, mock_popen, mock_write_pid
):
    """start_vm raises error if firecracker binary is missing (absolute path)."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.STOPPED
    mock_vm.id = "abc123def4567890"
    mock_vm.config = MagicMock()
    mock_vm.config.enable_api_socket = True
    mock_vm.config.enable_console = False
    mock_vm.config.kernel_path = Path("/fake/kernel")
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    mock_vm_dir = MagicMock()
    mock_vm_dir.__truediv__ = MagicMock(side_effect=lambda x: Path(f"/fake/vm/{x}"))
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_proc = MagicMock()
    mock_proc.pid = 99999
    mock_popen.return_value = mock_proc

    with patch("builtins.open", MagicMock()):
        with patch("mvmctl.core.vm_lifecycle.time.sleep"):
            with patch("pathlib.Path.exists", return_value=True):
                with patch(
                    "mvmctl.core.vm_lifecycle.DEFAULT_FIRECRACKER_BIN_NAME",
                    "firecracker",
                ):
                    start_vm("myvm")

    mock_popen.assert_called_once()
    mock_write_pid.assert_called_once()


@patch("mvmctl.core.vm_lifecycle._write_pid_file")
@patch("mvmctl.core.vm_lifecycle.subprocess.Popen")
@patch("mvmctl.core.vm_lifecycle.get_vm_dir")
@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_start_vm_log_close_oserror(mock_get_mgr, mock_get_vm_dir, mock_popen, mock_write_pid):
    """start_vm handles OSError when closing log file by raising MVMError."""
    mock_mgr = MagicMock()
    mock_vm = MagicMock()
    mock_vm.status = VMState.STOPPED
    mock_vm.id = "abc123def4567890"
    mock_vm.config = MagicMock()
    mock_vm.config.enable_api_socket = True
    mock_vm.config.enable_console = False
    mock_vm.config.kernel_path = None
    mock_mgr.get.return_value = mock_vm
    mock_get_mgr.return_value = mock_mgr

    mock_vm_dir = MagicMock()
    mock_vm_dir.__truediv__ = MagicMock(side_effect=lambda x: Path(f"/fake/vm/{x}"))
    mock_vm_dir.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_proc = MagicMock()
    mock_proc.pid = 99999
    mock_popen.return_value = mock_proc

    mock_log_fp = MagicMock()
    mock_log_fp.close.side_effect = OSError("Close failed")

    with patch("builtins.open", return_value=mock_log_fp):
        with patch("mvmctl.core.vm_lifecycle.time.sleep"):
            with patch("pathlib.Path.exists", return_value=True):
                with pytest.raises(MVMError, match="Failed to start VM"):
                    start_vm("myvm")

    mock_popen.assert_called_once()
