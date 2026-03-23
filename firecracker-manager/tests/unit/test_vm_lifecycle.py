from pathlib import Path
from unittest.mock import patch, MagicMock

import pytest

from fcm.core.vm_lifecycle import (
    graceful_shutdown,
    create_vm,
    remove_vm,
    snapshot_vm,
    load_snapshot,
    _write_pid_file,
    _read_pid_file,
)
from fcm.exceptions import FCMError
from fcm.models.vm import VMInstance


def test_write_read_pid_file(tmp_path):
    """_write_pid_file and _read_pid_file write and parse integers."""
    pid_file = tmp_path / "firecracker.pid"
    # Actually finding a process that exists without mocking is tricky, but let's mock os.kill
    with patch("fcm.core.vm_lifecycle.os.kill"):
        _write_pid_file(pid_file, 99999)
        val = _read_pid_file(pid_file)
        assert val == 99999


def test_read_pid_file_missing(tmp_path):
    """_read_pid_file returns None if missing."""
    pid_file = tmp_path / "missing.pid"
    assert _read_pid_file(pid_file) is None


@patch("fcm.core.vm_lifecycle.os.kill")
def test_graceful_shutdown(mock_kill):
    """graceful_shutdown sends SIGTERM and SIGKILL if still alive."""
    # Simulate process is alive
    mock_kill.return_value = None

    graceful_shutdown(pid=99999, socket_path=None)
    
    assert mock_kill.call_count >= 2
    import signal
    mock_kill.assert_any_call(99999, signal.SIGTERM)
    mock_kill.assert_any_call(99999, signal.SIGKILL)


@patch("fcm.core.vm_lifecycle.FirecrackerClient")
@patch("fcm.core.vm_lifecycle.Path.exists")
@patch("fcm.core.vm_lifecycle.os.kill")
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


@patch("fcm.core.vm_lifecycle.get_vm_manager")
@patch("fcm.core.vm_lifecycle.get_vm_dir")
@patch("fcm.core.vm_lifecycle.get_images_dir")
@patch("fcm.core.vm_lifecycle.get_kernels_dir")
@patch("fcm.core.vm_lifecycle.get_network")
@patch("fcm.core.vm_lifecycle.allocate_network_ip")
@patch("fcm.core.vm_lifecycle.generate_mac")
@patch("fcm.core.vm_lifecycle.shutil.copy2")
@patch("fcm.core.vm_lifecycle.write_cloud_init")
@patch("fcm.core.vm_lifecycle.inject_cloud_init")
@patch("fcm.core.vm_lifecycle.ConfigGenerator")
@patch("fcm.core.vm_lifecycle.create_tap")
@patch("fcm.core.vm_lifecycle.add_iptables_forward_rules")
@patch("fcm.core.vm_lifecycle.subprocess.Popen")
@patch("fcm.core.vm_lifecycle._write_pid_file")
@patch("fcm.core.vm_lifecycle.bridge_exists")
@patch("builtins.open", new_callable=MagicMock)
def test_create_vm_success(
    mock_open, mock_bridge_exists,
    mock_write_pid, mock_popen, mock_add_rules, mock_create_tap, mock_config_gen,
    mock_inject, mock_write_ci, mock_copy, mock_gen_mac, mock_alloc_ip, mock_get_net,
    mock_get_kernels, mock_get_images, mock_get_vm_dir, mock_get_vm_mgr
):
    """create_vm runs through successfully and registers WM."""
    mock_manager = MagicMock()
    mock_manager.list_all.return_value = []
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = False
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
    mock_net.bridge = "fcm-br0"
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    vm = create_vm(name="myvm", image="ubuntu-22.04")

    assert isinstance(vm, VMInstance)
    assert vm.name == "myvm"
    assert vm.ip == "10.20.0.5"
    mock_manager.register.assert_called_once()
    mock_popen.assert_called_once()
    mock_write_pid.assert_called_once()


@patch("fcm.core.vm_lifecycle.get_vm_manager")
def test_create_vm_limit_reached(mock_get_vm_mgr):
    """create_vm raises FCMError if max VMs reached."""
    mock_manager = MagicMock()
    mock_manager.list_all.return_value = [1] * 100  # assuming MAX_VMS=50 or similar
    mock_get_vm_mgr.return_value = mock_manager

    with pytest.raises(FCMError, match="VM limit reached"):
        create_vm(name="myvm", image="img")


@patch("fcm.core.vm_lifecycle.get_vm_manager")
@patch("fcm.core.vm_lifecycle.graceful_shutdown")
@patch("fcm.core.vm_lifecycle.remove_iptables_forward_rules")
@patch("fcm.core.vm_lifecycle.delete_tap")
@patch("fcm.core.vm_lifecycle.release_network_ip")
@patch("fcm.core.vm_lifecycle.subprocess.run")
@patch("fcm.core.vm_lifecycle.shutil.rmtree")
@patch("fcm.core.vm_lifecycle.get_vm_dir")
@patch("fcm.core.vm_lifecycle._read_pid_file")
def test_remove_vm_success(
    mock_read_pid, mock_get_vm_dir, mock_rmtree, mock_run, mock_rel_ip, mock_del_tap, mock_rm_rules, mock_graceful, mock_mgr
):
    """remove_vm deletes everything correctly."""
    mock_manager = MagicMock()
    vm = VMInstance(name="myvm", ip="10.20.0.5", pid=123, status="running", network_name="default")
    mock_manager.get.return_value = vm
    mock_mgr.return_value = mock_manager

    mock_read_pid.return_value = 123

    mock_vm_dir_ret = MagicMock()
    mock_vm_dir_ret.exists.return_value = True
    mock_get_vm_dir.return_value = mock_vm_dir_ret

    remove_vm("myvm")

    mock_graceful.assert_called_once_with(123, None)
    mock_rm_rules.assert_called_once()
    mock_del_tap.assert_called_once()
    mock_rel_ip.assert_called_once()
    mock_manager.deregister.assert_called_once_with("myvm")
    mock_rmtree.assert_called_once_with(mock_vm_dir_ret)


@patch("fcm.core.vm_lifecycle.get_vm_socket_path")
@patch("fcm.core.vm_lifecycle.FirecrackerClient")
def test_snapshot_vm(mock_client, mock_socket_path):
    """snapshot_vm calls FirecrackerClient create_snapshot."""
    mock_socket_path.return_value = Path("fake.sock")
    snapshot_vm("myvm", Path("mem"), Path("state"))
    mock_client.return_value.create_snapshot.assert_called_once_with(Path("mem"), Path("state"))


@patch("fcm.core.vm_lifecycle.get_vm_socket_path")
def test_snapshot_vm_no_socket(mock_socket_path):
    """snapshot_vm errors if no socket."""
    mock_socket_path.return_value = None
    with pytest.raises(FCMError, match="Socket not found for VM"):
        snapshot_vm("myvm", Path("mem"), Path("state"))

@patch("fcm.core.vm_lifecycle.get_vm_socket_path")
@patch("fcm.core.vm_lifecycle.FirecrackerClient")
def test_load_snapshot(mock_client, mock_socket_path):
    """load_snapshot checks socket and forwards to client."""
    mock_socket_path.return_value = Path("fake.sock")
    load_snapshot("myvm", Path("mem"), Path("state"))
    mock_client.return_value.load_snapshot.assert_called_once()
