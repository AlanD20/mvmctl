from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from pytest_mock import MockerFixture

from mvmctl.api.vm import (
    cleanup_vms,
    get_logs,
    get_vm,
    inspect_vm,
    list_vms,
    pause_vm,
    reboot_vm,
    resume_vm,
    ssh_vm,
    start_vm,
    stop_vm,
    vm_cache_dir,
)
from mvmctl.exceptions import MVMError, VMNotFoundError
from mvmctl.models.vm import VMInstance, VMStatus


@patch("mvmctl.core.vm_monitor.reconcile_vm")
@patch("mvmctl.api.vm.get_vm_manager")
def test_list_vms(mock_get_manager, mock_reconcile):
    """list_vms retrieves VMs from manager."""
    # Mock reconcile_vm to return the original status without changing it
    mock_reconcile.side_effect = lambda vm, manager: vm.status

    mock_manager = MagicMock()
    vm1 = VMInstance(
        name="vm1",
        id="vm1" + "a" * 60,
        status=VMStatus.RUNNING,
        pid=1234,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-def-vm1-abc",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )
    vm2 = VMInstance(
        name="vm2",
        id="vm2" + "b" * 60,
        status=VMStatus.STOPPED,
        pid=5678,
        ipv4="10.0.0.3",
        mac="02:FC:00:00:00:02",
        network_id="default",
        tap_device="mvm-def-vm2-abc",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )
    mock_manager.list_all.return_value = [vm1, vm2]
    mock_get_manager.return_value = mock_manager

    assert len(list_vms(include_stopped=True)) == 2
    assert len(list_vms(include_stopped=False)) == 1
    assert list_vms(include_stopped=False)[0].name == "vm1"


@patch("mvmctl.api.vm.get_vm_manager")
def test_get_vm(mock_get_manager):
    """get_vm interacts with manager correctly."""
    mock_manager = MagicMock()
    mock_get_manager.return_value = mock_manager

    get_vm("vm1")
    mock_manager.get.assert_called_with("vm1")


@patch("mvmctl.utils.fs.get_vm_dir_by_hash")
def test_vm_cache_dir(mock_get_vm_dir_by_hash):
    """vm_cache_dir returns the vm path using hash-based lookup."""
    mock_get_vm_dir_by_hash.return_value = Path("/tmp/vms/abc123")
    vm = VMInstance(
        name="testvm",
        id="abc123" + "x" * 58,
        status=VMStatus.STOPPED,
        pid=0,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-def-abc-123",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )
    assert vm_cache_dir(vm) == Path("/tmp/vms/abc123")
    mock_get_vm_dir_by_hash.assert_called_once_with(vm.id)


@patch("mvmctl.api.vm.connect_to_vm")
@patch("mvmctl.api.vm.get_vm_manager")
def test_ssh_vm(mock_get_manager, mock_connect):
    """ssh_vm looks up VM and forwards to connect_to_vm with IP."""
    mock_connect.return_value = 0
    mock_manager = MagicMock()
    mock_vm = VMInstance(
        name="vm1",
        id="vm1" + "a" * 60,
        ipv4="10.0.0.5",
        status=VMStatus.RUNNING,
        pid=1234,
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-def-vm1-abc",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )
    mock_manager.get.return_value = mock_vm
    mock_get_manager.return_value = mock_manager

    res = ssh_vm("vm1", user="ubuntu", key=Path("mykey"), cmd="uptime")
    assert res == 0
    mock_connect.assert_called_with(
        ip="10.0.0.5",
        user="ubuntu",
        key_path=Path("mykey"),
        command="uptime",
        exec_mode=False,
    )


@patch("mvmctl.api.vm.show_logs")
def test_get_logs(mock_show_logs):
    """get_logs forwards to show_logs."""
    mock_show_logs.return_value = ["log1"]
    res = get_logs("vm1", log_type="console", lines=10, follow=False)
    assert res == ["log1"]
    mock_show_logs.assert_called_with(vm_hash="vm1", log_type="console", lines=10, follow=False)


@patch("shutil.rmtree")
@patch("mvmctl.api.vm.teardown_nat")
@patch("mvmctl.core.network.delete_tap")
@patch("mvmctl.core.network.remove_iptables_forward_rules")
@patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
@patch("mvmctl.core.firewall.remove_nocloud_input_rule")
@patch("os.kill")
@patch("mvmctl.api.host.check_privileges_interactive")
@patch("mvmctl.api.network.get_network")
@patch("mvmctl.api.vm.get_vm_manager")
def test_cleanup_vms(
    mock_get_manager,
    mock_get_network,
    mock_check_privs,
    mock_kill,
    mock_rm_nocloud,
    mock_nocloud_mgr,
    mock_rm_iptables,
    mock_del_tap,
    mock_teardown_nat,
    mock_rmtree,
):
    """cleanup_vms cleans stopped vms properly using persisted tap_device."""
    mock_manager = MagicMock()
    mock_nocloud_mgr.return_value.stop_server.return_value = None
    vm1 = VMInstance(
        name="vm1",
        id="vm1" + "a" * 60,  # Full 64-char hash
        status=VMStatus.STOPPED,
        pid=123,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-def-vm1-abc",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )
    vm2 = VMInstance(
        name="vm2",
        id="vm2" + "b" * 60,
        status=VMStatus.RUNNING,
        pid=456,
        ipv4="10.0.0.3",
        mac="02:FC:00:00:00:02",
        network_id="default",
        tap_device="mvm-def-vm2-xyz",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )
    mock_manager.list_all.return_value = [vm1, vm2]
    mock_get_manager.return_value = mock_manager

    mock_net_config = MagicMock()
    mock_net_config.bridge = "mvm-default"
    mock_get_network.return_value = mock_net_config

    # cleanup only stopped VMs
    with patch("mvmctl.utils.fs.get_vm_dir_by_hash") as mock_get_vm_dir:
        mock_vm_dir = MagicMock()
        mock_vm_dir.exists.return_value = True
        mock_get_vm_dir.return_value = mock_vm_dir

        res = cleanup_vms(all_vms=False)
        assert len(res) == 1
        assert res[0].name == "vm1"

        mock_check_privs.assert_called_once_with("/usr/sbin/ip", "cleanup VMs")
        mock_kill.assert_called_once_with(123, 9)
        mock_rm_iptables.assert_called_once_with("mvm-def-vm1-abc", bridge="mvm-default")
        mock_del_tap.assert_called_once_with("mvm-def-vm1-abc")
        mock_teardown_nat.assert_called_once_with("mvm-default")
        mock_manager.deregister.assert_called_once()
        mock_rmtree.assert_called_once_with(mock_vm_dir)


@patch("shutil.rmtree")
@patch("mvmctl.api.vm.teardown_nat")
@patch("mvmctl.core.network.delete_tap")
@patch("mvmctl.core.network.remove_iptables_forward_rules")
@patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
@patch("mvmctl.core.firewall.remove_nocloud_input_rule")
@patch("os.kill")
@patch("mvmctl.api.host.check_privileges_interactive")
@patch("mvmctl.api.network.get_network")
@patch("mvmctl.api.vm.get_vm_manager")
def test_cleanup_vms_removes_hash_based_dir(
    mock_get_manager,
    mock_get_network,
    mock_check_privs,
    mock_kill,
    mock_rm_nocloud,
    mock_nocloud_mgr,
    mock_rm_iptables,
    mock_del_tap,
    mock_teardown_nat,
    mock_rmtree,
):
    """cleanup_vms removes VM directories using hash-based paths."""
    mock_manager = MagicMock()
    mock_nocloud_mgr.return_value.stop_server.return_value = None
    vm_id = "abc123" + "x" * 58
    vm1 = VMInstance(
        name="vm1",
        id=vm_id,
        status=VMStatus.STOPPED,
        pid=123,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-def-vm1-abc",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )
    mock_manager.list_all.return_value = [vm1]
    mock_get_manager.return_value = mock_manager

    mock_net_config = MagicMock()
    mock_net_config.bridge = "mvm-default"
    mock_get_network.return_value = mock_net_config

    with patch("mvmctl.utils.fs.get_vm_dir_by_hash") as mock_get_vm_dir:
        mock_vm_dir = MagicMock()
        mock_vm_dir.exists.return_value = True
        mock_get_vm_dir.return_value = mock_vm_dir

        res = cleanup_vms(all_vms=False)
        assert len(res) == 1
        assert res[0].name == "vm1"

        # Verify hash-based directory lookup was used
        mock_get_vm_dir.assert_called_once_with(vm_id)
        mock_rmtree.assert_called_once_with(mock_vm_dir)


@patch("shutil.rmtree")
@patch("mvmctl.api.vm.teardown_nat")
@patch("mvmctl.core.network.delete_tap")
@patch("mvmctl.core.network.remove_iptables_forward_rules")
@patch("mvmctl.services.nocloud_server.NoCloudNetServerManager")
@patch("mvmctl.core.firewall.remove_nocloud_input_rule")
@patch("os.kill")
@patch("mvmctl.api.host.check_privileges_interactive")
@patch("mvmctl.api.network.get_network")
@patch("mvmctl.api.vm.get_vm_manager")
def test_cleanup_vms_handles_missing_vm_id(
    mock_get_manager,
    mock_get_network,
    mock_check_privs,
    mock_kill,
    mock_rm_nocloud,
    mock_nocloud_mgr,
    mock_rm_iptables,
    mock_del_tap,
    mock_teardown_nat,
    mock_rmtree,
):
    """cleanup_vms handles VMs with missing ID gracefully."""
    mock_manager = MagicMock()
    mock_nocloud_mgr.return_value.stop_server.return_value = None
    vm1 = VMInstance(
        name="vm1",
        id="",  # Empty ID simulates missing hash
        status=VMStatus.STOPPED,
        pid=123,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-def-vm1-abc",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )
    mock_manager.list_all.return_value = [vm1]
    mock_get_manager.return_value = mock_manager

    mock_net_config = MagicMock()
    mock_net_config.bridge = "mvm-default"
    mock_get_network.return_value = mock_net_config

    # Should not raise even with missing ID
    res = cleanup_vms(all_vms=False)
    assert len(res) == 1

    # Verify deregister was still called with name when id is None
    mock_manager.deregister.assert_called_once_with(vm1.name)


def test_inspect_vm_by_id_prefix(mocker: MockerFixture):
    """Test inspect_vm returns complete VM metadata by ID prefix."""
    mock_vm = VMInstance(
        name="test-vm",
        id="abc123" + "x" * 10,  # 16-char hash
        pid=1234,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        status=VMStatus.RUNNING,
        network_id="default",
        tap_device="mvm-def-abc-123",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )

    mock_resolver = mocker.MagicMock()
    mock_resolver.by_id.return_value = mock_vm
    mocker.patch("mvmctl.api.vm._query.VMResolver", return_value=mock_resolver)

    result = inspect_vm("abc123")

    assert result.name == "test-vm"
    assert result.id == mock_vm.id
    assert result.status == "running"
    assert result.pid == 1234
    assert result.ip == "10.0.0.2"


def test_inspect_vm_by_name(mocker: MockerFixture):
    """Test inspect_vm returns VM metadata by name."""
    mock_vm = VMInstance(
        name="myvm",
        id="def456" + "y" * 10,
        pid=5678,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        status=VMStatus.RUNNING,
        network_id="default",
        tap_device="mvm-def-abc-123",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )

    mock_resolver = mocker.MagicMock()
    mock_resolver.by_id.side_effect = VMNotFoundError("not found")
    mock_resolver.by_name.return_value = mock_vm
    mocker.patch("mvmctl.api.vm._query.VMResolver", return_value=mock_resolver)

    result = inspect_vm("myvm")

    assert result.name == "myvm"
    assert result.pid == 5678


def test_inspect_vm_ambiguous(mocker: MockerFixture):
    """Test inspect_vm raises error for ambiguous name."""
    mock_resolver = mocker.MagicMock()
    mock_resolver.by_id.side_effect = VMNotFoundError("not found")
    mock_resolver.by_name.side_effect = MVMError("Multiple VMs match")
    mocker.patch("mvmctl.api.vm._query.VMResolver", return_value=mock_resolver)

    with pytest.raises(MVMError, match="Multiple VMs match"):
        inspect_vm("myvm")


def test_inspect_vm_not_found(mocker: MockerFixture):
    """Test inspect_vm raises error for non-existent VM."""
    mock_mgr = mocker.MagicMock()
    mock_mgr.get_by_id_prefix.return_value = None
    mock_mgr.get_by_name.return_value = []
    mocker.patch("mvmctl.api.vm.get_vm_manager", return_value=mock_mgr)

    with pytest.raises(VMNotFoundError, match="not found"):
        inspect_vm("nonexistent")


# ---------------------------------------------------------------------------
# Rootfs path resolution tests (Issue 2 fix)
# ---------------------------------------------------------------------------


def test_resolve_rootfs_path_from_config(mocker: MockerFixture, tmp_path: Path):
    """Test _resolve_rootfs_path uses config.rootfs_path when available."""
    from mvmctl.api.vm._query import _resolve_rootfs_path

    config_path = tmp_path / "shared" / "image.ext4"
    config_path.parent.mkdir(parents=True)
    config_path.write_text("dummy")

    vm_config = MagicMock()
    vm_config.rootfs_path = config_path

    vm = VMInstance(
        name="test-vm",
        id="abc123" + "x" * 58,
        status=VMStatus.RUNNING,
        pid=1234,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-def-abc-123",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
        config=vm_config,
    )

    vm_dir = tmp_path / "vm_dir"
    vm_dir.mkdir()

    path, source = _resolve_rootfs_path(vm, vm_dir)

    assert path == config_path
    assert source == "config"


def test_resolve_rootfs_path_local_fallback(mocker: MockerFixture, tmp_path: Path):
    """Test _resolve_rootfs_path falls back to local rootfs file."""
    from mvmctl.api.vm._query import _resolve_rootfs_path

    vm = VMInstance(
        name="test-vm",
        id="abc123" + "x" * 58,
        status=VMStatus.RUNNING,
        pid=1234,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-def-abc-123",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
        config=None,
    )

    vm_dir = tmp_path / "vm_dir"
    vm_dir.mkdir()
    local_rootfs = vm_dir / "rootfs.ext4"
    local_rootfs.write_text("dummy")

    path, source = _resolve_rootfs_path(vm, vm_dir)

    assert path == local_rootfs
    assert source == "local"


def test_resolve_rootfs_path_none_when_missing(mocker: MockerFixture, tmp_path: Path):
    """Test _resolve_rootfs_path returns None when no rootfs found."""
    from mvmctl.api.vm._query import _resolve_rootfs_path

    vm = VMInstance(
        name="test-vm",
        id="abc123" + "x" * 58,
        status=VMStatus.RUNNING,
        pid=1234,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-def-abc-123",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
        config=None,
    )

    vm_dir = tmp_path / "vm_dir"
    vm_dir.mkdir()

    path, source = _resolve_rootfs_path(vm, vm_dir)

    assert path is None
    assert source == "none"


def test_inspect_vm_rootfs_source_field(mocker: MockerFixture, tmp_path: Path):
    """Test inspect_vm includes rootfs_source in output."""
    mock_vm = VMInstance(
        name="test-vm",
        id="abc123" + "x" * 58,
        pid=1234,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        status=VMStatus.RUNNING,
        network_id="default",
        tap_device="mvm-def-abc-123",
        created_at=datetime.now(),
        updated_at=datetime.now(),
        rootfs_suffix=".ext4",
        kernel_id="k" * 64,
        image_id="i" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
    )

    mock_mgr = mocker.MagicMock()
    mock_mgr.get_by_id_prefix.return_value = mock_vm
    mocker.patch("mvmctl.api.vm.get_vm_manager", return_value=mock_mgr)

    # Mock the VM directory with a local rootfs
    with patch("mvmctl.utils.fs.get_vm_dir_by_hash") as mock_get_dir:
        vm_dir = tmp_path / "vms" / mock_vm.id
        vm_dir.mkdir(parents=True)
        (vm_dir / "rootfs.ext4").write_text("dummy")
        mock_get_dir.return_value = vm_dir

        result = inspect_vm("abc123")

    assert result.paths is not None
    assert result.paths["rootfs_source"] == "local"


# -----------------------------------------------------------------------------
# Pause and Resume API tests
# -----------------------------------------------------------------------------


@patch("mvmctl.api.vm._pause_process")
def test_pause_vm_api(mock_pause):
    """pause_vm calls the process helper with a Firecracker client."""
    with patch("mvmctl.api.vm.get_vm_manager") as mock_get_manager:
        mock_manager = MagicMock()
        mock_vm = MagicMock()
        mock_vm.status = VMStatus.RUNNING
        mock_vm.api_socket_path = Path("/tmp/fc.sock")
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        with patch("mvmctl.api.vm.FirecrackerClient"):
            pause_vm("myvm")

    mock_pause.assert_called_once()


@patch("mvmctl.api.vm._resume_process")
def test_resume_vm_api(mock_resume):
    """resume_vm calls the process helper with a Firecracker client."""
    with patch("mvmctl.api.vm.get_vm_manager") as mock_get_manager:
        mock_manager = MagicMock()
        mock_vm = MagicMock()
        mock_vm.status = VMStatus.PAUSED
        mock_vm.api_socket_path = Path("/tmp/fc.sock")
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        with patch("mvmctl.api.vm.FirecrackerClient"):
            resume_vm("myvm")

    mock_resume.assert_called_once()


# -----------------------------------------------------------------------------
# Stop, Start, Reboot API tests
# -----------------------------------------------------------------------------


@patch("mvmctl.api.vm.graceful_shutdown")
def test_stop_vm_api(mock_stop):
    """stop_vm updates state and delegates shutdown."""
    with patch("mvmctl.api.vm.get_vm_manager") as mock_get_manager:
        mock_manager = MagicMock()
        mock_vm = MagicMock()
        mock_vm.status = VMStatus.RUNNING
        mock_vm.pid = 123
        mock_vm.api_socket_path = Path("/tmp/fc.sock")
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        stop_vm("myvm")

    mock_stop.assert_called_once_with(123, Path("/tmp/fc.sock"), force=False)


@patch("mvmctl.api.vm.graceful_shutdown")
def test_stop_vm_api_force(mock_stop):
    """stop_vm passes force=True to shutdown."""
    with patch("mvmctl.api.vm.get_vm_manager") as mock_get_manager:
        mock_manager = MagicMock()
        mock_vm = MagicMock()
        mock_vm.status = VMStatus.RUNNING
        mock_vm.pid = 123
        mock_vm.api_socket_path = Path("/tmp/fc.sock")
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        stop_vm("myvm", force=True)

    mock_stop.assert_called_once_with(123, Path("/tmp/fc.sock"), force=True)


@patch("mvmctl.api.vm.time.sleep")
@patch("mvmctl.api.vm._write_pid_file")
@patch("mvmctl.api.vm.subprocess.Popen")
def test_start_vm_api(mock_popen, mock_write_pid, mock_sleep):
    """start_vm queries the default binary and registers the VM."""
    mock_popen.return_value.pid = 456
    with patch("mvmctl.api.vm.get_vm_manager") as mock_get_manager:
        mock_manager = MagicMock()
        mock_vm = MagicMock()
        mock_vm.status = VMStatus.STOPPED
        mock_vm.id = "abc"
        mock_vm.config = MagicMock(enable_api_socket=False, enable_console=False, kernel_path=None)
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager
        with patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=Path("/tmp/vm")):
            with patch("pathlib.Path.exists", return_value=True):
                with patch("builtins.open", MagicMock()):
                    start_vm("myvm")

    mock_write_pid.assert_called_once()


@patch("mvmctl.api.vm.start_vm")
@patch("mvmctl.api.vm.stop_vm")
def test_reboot_vm_api(mock_stop, mock_start):
    """reboot_vm stops then starts the VM."""
    reboot_vm("myvm")
    mock_stop.assert_called_once_with("myvm", force=False)
    mock_start.assert_called_once_with("myvm")
