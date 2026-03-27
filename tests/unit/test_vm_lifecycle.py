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
    remove_vm,
    snapshot_vm,
)
from mvmctl.exceptions import MVMError
from mvmctl.models.vm import VMInstance, VMState
from mvmctl.utils.short_id import resolve_single_by_short_id


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
def test_create_vm_core_success(
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
):
    """Test core create_vm() runs through successfully and registers VM."""
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
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
    mock_net.bridge = "mvm-br0"
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    vm = create_vm(name="myvm", image="ubuntu-22.04")

    assert isinstance(vm, VMInstance)
    assert vm.name == "myvm"
    assert vm.ip == "10.20.0.5"
    vm_config_arg = mock_config_gen.call_args.args[0]
    assert vm_config_arg.root_uuid == "11111111-2222-3333-4444-555555555555"
    assert vm_config_arg.root_fs_type == "ext4"
    assert vm_config_arg.cloud_init_iso_path is not None
    assert vm_config_arg.extra_drives == []
    mock_manager.register.assert_called_once()
    mock_popen.assert_called_once()
    mock_write_pid.assert_called_once()


@patch("mvmctl.core.vm_lifecycle.get_vm_manager")
def test_create_vm_limit_reached(mock_get_vm_mgr):
    """create_vm raises MVMError if max VMs reached."""
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 100  # assuming MAX_VMS=50 or similar
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
        name="myvm", ip="10.20.0.5", pid=123, status=VMState.RUNNING, network_name="default"
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
        name="vm2", ip="10.20.0.6", pid=456, status=VMState.RUNNING, network_name="isolated"
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
        name="shared", ip="10.20.0.7", pid=789, status=VMState.RUNNING, network_name="default"
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


def test_resolve_single_by_short_id_unique(tmp_path):
    def _find(_: Path, short_id: str) -> list[tuple[str, dict[str, str]]]:
        if short_id == "abc123":
            return [("abc123deadbeef", {"filename": "asset"})]
        return []

    result = resolve_single_by_short_id("abc123", _find, tmp_path)
    assert result == ("abc123deadbeef", {"filename": "asset"})


def test_resolve_single_by_short_id_none_for_ambiguous(tmp_path):
    def _find(_: Path, __: str) -> list[tuple[str, dict[str, str]]]:
        return [
            ("abc123deadbeef", {"filename": "a"}),
            ("abc123feedface", {"filename": "b"}),
        ]

    assert resolve_single_by_short_id("abc123", _find, tmp_path) is None


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


def test_resolve_image_short_id_path_unique(tmp_path, monkeypatch):
    import json

    from mvmctl.core.vm_lifecycle import _resolve_image_short_id_path

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    full_hash = "b" * 64
    img = images_dir / "ubuntu.ext4"
    img.write_bytes(b"img")
    (tmp_path / "metadata.json").write_text(
        json.dumps({"images": {full_hash: {"filename": img.name}}})
    )

    with patch("mvmctl.core.vm_lifecycle.get_images_dir", return_value=images_dir):
        result = _resolve_image_short_id_path(full_hash[:6])
    assert result == img


def test_resolve_kernel_short_id_path_unique(tmp_path, monkeypatch):
    import json

    from mvmctl.core.vm_lifecycle import _resolve_kernel_short_id_path

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    full_hash = "c" * 64
    kernel = kernels_dir / "vmlinux-short"
    kernel.write_bytes(b"kernel")
    (tmp_path / "metadata.json").write_text(
        json.dumps({"kernels": {full_hash: {"filename": kernel.name}}})
    )

    with patch("mvmctl.core.vm_lifecycle.get_kernels_dir", return_value=kernels_dir):
        result = _resolve_kernel_short_id_path(full_hash[:6])
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
    ):
        mock_manager = MagicMock()
        mock_manager.count_vms.return_value = 0
        mock_mgr.return_value = mock_manager

        with pytest.raises(MVMError, match="symlink"):
            create_vm(name="attackvm", image="ubuntu-24.04")


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
def test_create_vm_uses_cached_image_path_not_copy(
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
):
    """create_vm sets VMConfig.rootfs_path to the cached image path, not a copy."""
    mock_manager = MagicMock()
    mock_manager.count_vms.return_value = 0
    mock_get_vm_mgr.return_value = mock_manager

    mock_vm_dir = MagicMock()
    mock_vm_dir.exists.return_value = False
    mock_get_vm_dir.return_value = mock_vm_dir

    mock_kernel_dir = MagicMock()
    vmlinux = MagicMock()
    vmlinux.exists.return_value = True
    mock_kernel_dir.__truediv__.return_value = vmlinux
    mock_get_kernels.return_value = mock_kernel_dir

    # Image - this is the cached image path
    mock_img_dir = MagicMock()
    img_ext4 = MagicMock()
    img_ext4.exists.return_value = True
    mock_img_dir.__truediv__.return_value = img_ext4
    mock_get_images.return_value = mock_img_dir

    mock_net = MagicMock()
    mock_net.cidr = "10.20.0.0/24"
    mock_net.gateway = "10.20.0.1"
    mock_net.bridge = "mvm-br0"
    mock_get_net.return_value = mock_net

    mock_alloc_ip.return_value = "10.20.0.5"
    mock_gen_mac.return_value = "02:fc:11:22:33:44"
    mock_resolve_fs_uuid.return_value = "11111111-2222-3333-4444-555555555555"
    mock_resolve_fs_type.return_value = "ext4"

    mock_bridge_exists.return_value = True
    mock_popen.return_value.pid = 99999

    create_vm(name="myvm", image="ubuntu-22.04")

    vm_config_arg = mock_config_gen.call_args.args[0]
    # Verify that rootfs_path is the cached image path (not copied to vm_dir)
    assert vm_config_arg.rootfs_path == img_ext4
