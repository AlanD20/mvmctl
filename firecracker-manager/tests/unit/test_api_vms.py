from pathlib import Path
from unittest.mock import patch, MagicMock


from fcm.api.vms import (
    list_vms,
    get_vm,
    deregister_vm,
    vm_cache_dir,
    ssh_vm,
    get_logs,
    cleanup_vms,
)
from fcm.models.vm import VMInstance, VMState


@patch("fcm.api.vms.get_vm_manager")
def test_list_vms(mock_get_manager):
    """list_vms retrieves VMs from manager."""
    mock_manager = MagicMock()
    vm1 = VMInstance(name="vm1", status=VMState.RUNNING)
    vm2 = VMInstance(name="vm2", status=VMState.STOPPED)
    mock_manager.list_all.return_value = [vm1, vm2]
    mock_get_manager.return_value = mock_manager

    assert len(list_vms(include_stopped=True)) == 2
    assert len(list_vms(include_stopped=False)) == 1
    assert list_vms(include_stopped=False)[0].name == "vm1"


@patch("fcm.api.vms.get_vm_manager")
def test_get_vm_and_deregister(mock_get_manager):
    """get_vm and deregister_vm interact with manager correctly."""
    mock_manager = MagicMock()
    mock_get_manager.return_value = mock_manager

    get_vm("vm1")
    mock_manager.get.assert_called_with("vm1")

    deregister_vm("vm1")
    mock_manager.deregister.assert_called_with("vm1")


@patch("fcm.utils.fs.get_vms_dir")
def test_vm_cache_dir(mock_get_vms_dir):
    """vm_cache_dir returns the vm path."""
    mock_get_vms_dir.return_value = Path("/tmp/vms")
    assert vm_cache_dir("testvm") == Path("/tmp/vms/testvm")


@patch("fcm.api.vms.connect_to_vm")
def test_ssh_vm(mock_connect):
    """ssh_vm forwards to connect_to_vm."""
    mock_connect.return_value = 0
    res = ssh_vm("vm1", user="ubuntu", key=Path("mykey"), cmd="uptime")
    assert res == 0
    mock_connect.assert_called_with(
        vm_name_or_ip="vm1",
        user="ubuntu",
        key_path=Path("mykey"),
        command="uptime",
        exec_mode=False,
    )


@patch("fcm.api.vms.show_logs")
def test_get_logs(mock_show_logs):
    """get_logs forwards to show_logs."""
    mock_show_logs.return_value = ["log1"]
    res = get_logs("vm1", log_type="console", lines=10)
    assert res == ["log1"]
    mock_show_logs.assert_called_with(
        vm_name="vm1", log_type="console", lines=10, follow=False
    )


@patch("shutil.rmtree")
@patch("fcm.core.network.delete_tap")
@patch("fcm.core.network.remove_iptables_forward_rules")
@patch("os.kill")
@patch("fcm.api.vms.get_vm_manager")
def test_cleanup_vms(mock_get_manager, mock_kill, mock_rm_iptables, mock_del_tap, mock_rmtree):
    """cleanup_vms cleans stopped vms properly."""
    mock_manager = MagicMock()
    vm1 = VMInstance(name="vm1", status=VMState.STOPPED, pid=123)
    vm2 = VMInstance(name="vm2", status=VMState.RUNNING, pid=456)
    mock_manager.list_all.return_value = [vm1, vm2]
    mock_get_manager.return_value = mock_manager

    # cleanup only stopped VMs
    with patch("fcm.api.vms.vm_cache_dir") as mock_cache_dir:
        mock_dir = MagicMock()
        mock_dir.exists.return_value = True
        mock_cache_dir.return_value = mock_dir

        res = cleanup_vms(all_vms=False)
        assert len(res) == 1
        assert res[0].name == "vm1"

        mock_kill.assert_called_once_with(123, 9)
        mock_rm_iptables.assert_called_with("fc-vm1-0")
        mock_del_tap.assert_called_with("fc-vm1-0")
        mock_manager.deregister.assert_called_with("vm1")
        mock_rmtree.assert_called_once()
