import json
import os
import shutil
import signal
import tempfile
from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, patch

from typer.testing import CliRunner

from fcm.cli.vm import app, _write_cloud_init
from fcm.core.network_manager import NetworkConfig
from fcm.exceptions import NetworkError
from fcm.models.vm import VMInstance, VMState

_FAKE_NET = NetworkConfig(
    name="default", subnet="10.20.0.0/24", gateway="10.20.0.1",
    bridge="fcm-br0", nat_enabled=True,
)

runner = CliRunner()


def _make_vm(name: str, status: VMState = VMState.RUNNING, ip: str = "10.20.0.2") -> VMInstance:
    return VMInstance(
        name=name,
        ip=ip,
        mac="02:FC:aa:bb:cc:dd",
        pid=1234,
        status=status,
        created_at=datetime(2026, 1, 1, 12, 0, 0),
    )


def test_list_vms_empty():
    with patch("fcm.cli.vm.VMManager") as mock_manager_cls:
        mock_manager_cls.return_value.list_all.return_value = []
        result = runner.invoke(app, ["list"])
    assert result.exit_code == 0
    assert "No VMs found" in result.output


def test_list_vms_json():
    vm = _make_vm("myvm")
    with patch("fcm.cli.vm.VMManager") as mock_manager_cls:
        mock_manager_cls.return_value.list_all.return_value = [vm]
        result = runner.invoke(app, ["list", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert isinstance(data, list)
    assert len(data) == 1
    assert data[0]["name"] == "myvm"


def test_list_vms_all_flag():
    running_vm = _make_vm("vm-running", VMState.RUNNING, "10.20.0.2")
    stopped_vm = _make_vm("vm-stopped", VMState.STOPPED, "10.20.0.3")
    with patch("fcm.cli.vm.VMManager") as mock_manager_cls:
        mock_manager_cls.return_value.list_all.return_value = [running_vm, stopped_vm]
        result = runner.invoke(app, ["list", "--all", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    names = {item["name"] for item in data}
    assert "vm-running" in names
    assert "vm-stopped" in names


def test_delete_vm_not_found():
    with patch("fcm.cli.vm.VMManager") as mock_manager_cls:
        mock_manager_cls.return_value.get.return_value = None
        result = runner.invoke(app, ["delete", "--name", "nonexistent", "--force"])
    assert result.exit_code == 1


def test_cleanup_nothing_to_do():
    with patch("fcm.cli.vm.VMManager") as mock_manager_cls:
        mock_manager_cls.return_value.list_all.return_value = []
        result = runner.invoke(app, ["cleanup"])
    assert result.exit_code == 0
    assert "Nothing to clean up" in result.output


def test_setup_calls_network():
    with (
        patch("fcm.cli.vm.setup_bridge") as mock_bridge,
        patch("fcm.cli.vm.setup_nat") as mock_nat,
    ):
        result = runner.invoke(app, ["setup"])
    assert result.exit_code == 0
    mock_bridge.assert_called_once()
    mock_nat.assert_called_once()


def test_create_vm_missing_kernel():
    with tempfile.TemporaryDirectory() as tmp:
        with patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}):
            result = runner.invoke(app, ["create", "--name", "test", "--image", "ubuntu-24.04"])
    assert result.exit_code == 1
    assert "Kernel not found" in result.output


def test_create_vm_missing_image():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        fake_kernel = kernels_dir / "vmlinux"
        fake_kernel.write_text("fake kernel")
        with patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}):
            result = runner.invoke(app, ["create", "--name", "test", "--image", "ubuntu-24.04"])
    assert result.exit_code == 1
    assert "Image not found" in result.output


# ---------------------------------------------------------------------------
# create command – success path
# ---------------------------------------------------------------------------


def test_create_vm_success():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

        mock_proc = MagicMock()
        mock_proc.pid = 9999

        with (
            patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}),
            patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
            patch("fcm.cli.vm.get_network", return_value=_FAKE_NET),
            patch("fcm.cli.vm.allocate_network_ip", return_value="10.20.0.5"),
            patch("fcm.cli.vm.create_tap"),
            patch("fcm.cli.vm.add_iptables_forward_rules"),
            patch("fcm.cli.vm.ConfigGenerator"),
            patch("subprocess.Popen", return_value=mock_proc),
            patch("fcm.cli.vm._inject_cloud_init"),
        ):
            mock_mgr_cls.return_value.list_all.return_value = []
            result = runner.invoke(app, ["create", "--name", "newvm", "--image", "ubuntu-24.04"])

        assert result.exit_code == 0
        assert "newvm" in result.output
        mock_mgr_cls.return_value.register.assert_called_once()


def test_create_vm_already_exists():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        vms_dir = tmp_path / "vms" / "existvm"
        vms_dir.mkdir(parents=True)

        with patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}):
            result = runner.invoke(app, ["create", "--name", "existvm", "--image", "ubuntu-24.04"])
    assert result.exit_code == 1
    assert "already exists" in result.output


def test_create_vm_image_from_path():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        img_file = tmp_path / "custom.ext4"
        img_file.write_text("fake image")

        mock_proc = MagicMock()
        mock_proc.pid = 8888

        with (
            patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}),
            patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
            patch("fcm.cli.vm.get_network", return_value=_FAKE_NET),
            patch("fcm.cli.vm.allocate_network_ip", return_value="10.20.0.6"),
            patch("fcm.cli.vm.create_tap"),
            patch("fcm.cli.vm.add_iptables_forward_rules"),
            patch("fcm.cli.vm.ConfigGenerator"),
            patch("subprocess.Popen", return_value=mock_proc),
            patch("fcm.cli.vm._inject_cloud_init"),
        ):
            mock_mgr_cls.return_value.list_all.return_value = []
            result = runner.invoke(app, ["create", "--name", "pathvm", "--image", str(img_file)])

        assert result.exit_code == 0


def test_create_vm_popen_file_not_found():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

        with (
            patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}),
            patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
            patch("fcm.cli.vm.get_network", return_value=_FAKE_NET),
            patch("fcm.cli.vm.allocate_network_ip", return_value="10.20.0.7"),
            patch("fcm.cli.vm.create_tap"),
            patch("fcm.cli.vm.add_iptables_forward_rules"),
            patch("fcm.cli.vm.ConfigGenerator"),
            patch("subprocess.Popen", side_effect=FileNotFoundError("not found")),
            patch("fcm.cli.vm._inject_cloud_init"),
            patch("fcm.cli.vm._cleanup_tap"),
        ):
            mock_mgr_cls.return_value.list_all.return_value = []
            result = runner.invoke(app, ["create", "--name", "fnfvm", "--image", "ubuntu-24.04"])

        assert result.exit_code == 1
        assert "binary not found" in result.output


def test_create_vm_popen_os_error():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

        with (
            patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}),
            patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
            patch("fcm.cli.vm.get_network", return_value=_FAKE_NET),
            patch("fcm.cli.vm.allocate_network_ip", return_value="10.20.0.8"),
            patch("fcm.cli.vm.create_tap"),
            patch("fcm.cli.vm.add_iptables_forward_rules"),
            patch("fcm.cli.vm.ConfigGenerator"),
            patch("subprocess.Popen", side_effect=OSError("disk full")),
            patch("fcm.cli.vm._inject_cloud_init"),
            patch("fcm.cli.vm._cleanup_tap"),
        ):
            mock_mgr_cls.return_value.list_all.return_value = []
            result = runner.invoke(app, ["create", "--name", "osevm", "--image", "ubuntu-24.04"])

        assert result.exit_code == 1
        assert "Failed to start Firecracker" in result.output


def test_create_vm_network_error_on_tap():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

        with (
            patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}),
            patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
            patch("fcm.cli.vm.get_network", return_value=_FAKE_NET),
            patch("fcm.cli.vm.allocate_network_ip", return_value="10.20.0.9"),
            patch("fcm.cli.vm.create_tap", side_effect=NetworkError("tap fail")),
            patch("fcm.cli.vm.release_network_ip"),
            patch("fcm.cli.vm.ConfigGenerator"),
            patch("fcm.cli.vm._inject_cloud_init"),
        ):
            mock_mgr_cls.return_value.list_all.return_value = []
            result = runner.invoke(app, ["create", "--name", "netvm", "--image", "ubuntu-24.04"])

        assert result.exit_code == 1
        assert "Network setup failed" in result.output


# ---------------------------------------------------------------------------
# delete command – additional paths
# ---------------------------------------------------------------------------


def test_delete_force_running_vm():
    vm = _make_vm("delvm", VMState.RUNNING)
    with (
        patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
        patch("fcm.cli.vm.get_vm_dir") as mock_vm_dir,
        patch("fcm.cli.vm.remove_iptables_forward_rules"),
        patch("fcm.cli.vm.delete_tap"),
        patch("fcm.cli.vm.teardown_nat"),
        patch("fcm.cli.vm._find_network_for_vm", return_value=[]),
        patch("os.kill") as mock_kill,
        patch("fcm.cli.vm.time.sleep"),
    ):
        mock_mgr_cls.return_value.get.return_value = vm
        fake_dir = MagicMock()
        fake_dir.exists.return_value = False
        mock_vm_dir.return_value = fake_dir
        # Simulate process dying after SIGTERM
        mock_kill.side_effect = [None, ProcessLookupError]
        result = runner.invoke(app, ["delete", "--name", "delvm", "--force"])

    assert result.exit_code == 0
    assert "removed" in result.output.lower()


def test_delete_process_already_gone():
    vm = _make_vm("gonevm", VMState.RUNNING)
    with (
        patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
        patch("fcm.cli.vm.get_vm_dir") as mock_vm_dir,
        patch("fcm.cli.vm.remove_iptables_forward_rules"),
        patch("fcm.cli.vm.delete_tap"),
        patch("fcm.cli.vm.teardown_nat"),
        patch("fcm.cli.vm._find_network_for_vm", return_value=[]),
        patch("os.kill", side_effect=ProcessLookupError),
    ):
        mock_mgr_cls.return_value.get.return_value = vm
        fake_dir = MagicMock()
        fake_dir.exists.return_value = False
        mock_vm_dir.return_value = fake_dir
        result = runner.invoke(app, ["delete", "--name", "gonevm", "--force"])

    assert result.exit_code == 0
    assert "removed" in result.output.lower()


def test_delete_permission_error_no_force():
    vm = _make_vm("permvm", VMState.RUNNING)
    with (
        patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
        patch("fcm.cli.vm.get_vm_dir") as mock_vm_dir,
        patch("fcm.cli.vm.remove_iptables_forward_rules"),
        patch("fcm.cli.vm.delete_tap"),
        patch("fcm.cli.vm.teardown_nat"),
        patch("fcm.cli.vm._find_network_for_vm", return_value=[]),
        patch("os.kill", side_effect=PermissionError("not root")),
    ):
        mock_mgr_cls.return_value.get.return_value = vm
        fake_dir = MagicMock()
        fake_dir.exists.return_value = False
        mock_vm_dir.return_value = fake_dir
        result = runner.invoke(app, ["delete", "--name", "permvm", "--force"])

    # force=True, so PermissionError is caught but continues
    assert result.exit_code == 0


def test_delete_permission_error_not_force_exits():
    vm = _make_vm("permvm2", VMState.RUNNING)
    with (
        patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
        patch("fcm.cli.vm.get_vm_dir") as mock_vm_dir,
        patch("fcm.cli.vm.remove_iptables_forward_rules"),
        patch("fcm.cli.vm.delete_tap"),
        patch("fcm.cli.vm.teardown_nat"),
        patch("fcm.cli.vm._find_network_for_vm", return_value=[]),
        patch("os.kill", side_effect=PermissionError("not root")),
    ):
        mock_mgr_cls.return_value.get.return_value = vm
        fake_dir = MagicMock()
        fake_dir.exists.return_value = False
        mock_vm_dir.return_value = fake_dir
        # Provide "y" to confirmation prompt
        result = runner.invoke(app, ["delete", "--name", "permvm2"], input="y\n")

    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# list command – table output
# ---------------------------------------------------------------------------


def test_list_table_output_with_running_vms():
    vm1 = _make_vm("vm1", VMState.RUNNING, "10.20.0.2")
    vm2 = _make_vm("vm2", VMState.RUNNING, "10.20.0.3")
    with patch("fcm.cli.vm.VMManager") as mock_mgr_cls:
        mock_mgr_cls.return_value.list_all.return_value = [vm1, vm2]
        result = runner.invoke(app, ["list"])
    assert result.exit_code == 0
    assert "vm1" in result.output
    assert "vm2" in result.output
    assert "Firecracker VMs" in result.output


def test_list_without_all_filters_running_only():
    running = _make_vm("run1", VMState.RUNNING, "10.20.0.2")
    stopped = _make_vm("stop1", VMState.STOPPED, "10.20.0.3")
    with patch("fcm.cli.vm.VMManager") as mock_mgr_cls:
        mock_mgr_cls.return_value.list_all.return_value = [running, stopped]
        result = runner.invoke(app, ["list", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    names = {item["name"] for item in data}
    assert "run1" in names
    assert "stop1" not in names


# ---------------------------------------------------------------------------
# ssh command
# ---------------------------------------------------------------------------


def test_ssh_success():
    with patch("fcm.cli.vm.connect_to_vm", return_value=0):
        result = runner.invoke(app, ["ssh", "--name", "myvm"])
    assert result.exit_code == 0


def test_ssh_with_cmd():
    with patch("fcm.cli.vm.connect_to_vm", return_value=0) as mock_ssh:
        result = runner.invoke(app, ["ssh", "--name", "myvm", "--cmd", "uname -a"])
    assert result.exit_code == 0
    mock_ssh.assert_called_once_with(
        vm_name_or_ip="myvm",
        user="root",
        key_path=None,
        command="uname -a",
        exec_mode=False,
    )


def test_ssh_failure():
    with patch("fcm.cli.vm.connect_to_vm", return_value=1):
        result = runner.invoke(app, ["ssh", "--name", "badvm"])
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# logs command
# ---------------------------------------------------------------------------


def test_logs_success():
    with patch("fcm.cli.vm.show_logs", return_value=0):
        result = runner.invoke(app, ["logs", "--name", "myvm"])
    assert result.exit_code == 0


def test_logs_failure():
    with patch("fcm.cli.vm.show_logs", return_value=1):
        result = runner.invoke(app, ["logs", "--name", "badvm"])
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# cleanup command
# ---------------------------------------------------------------------------


def test_cleanup_with_stopped_vms_force():
    stopped = _make_vm("old1", VMState.STOPPED, "10.20.0.2")
    with (
        patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
        patch("fcm.cli.vm.get_vm_dir") as mock_vm_dir,
        patch("fcm.cli.vm.remove_iptables_forward_rules"),
        patch("fcm.cli.vm.delete_tap"),
        patch("fcm.cli.vm.teardown_nat"),
        patch("os.kill"),
    ):
        mock_mgr_cls.return_value.list_all.return_value = [stopped]
        fake_dir = MagicMock()
        fake_dir.exists.return_value = False
        mock_vm_dir.return_value = fake_dir
        result = runner.invoke(app, ["cleanup", "--force"])

    assert result.exit_code == 0
    assert "Removed" in result.output


def test_cleanup_all_includes_running():
    running = _make_vm("run1", VMState.RUNNING, "10.20.0.2")
    stopped = _make_vm("stop1", VMState.STOPPED, "10.20.0.3")
    with (
        patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
        patch("fcm.cli.vm.get_vm_dir") as mock_vm_dir,
        patch("fcm.cli.vm.remove_iptables_forward_rules"),
        patch("fcm.cli.vm.delete_tap"),
        patch("fcm.cli.vm.teardown_nat"),
        patch("os.kill"),
    ):
        mock_mgr_cls.return_value.list_all.return_value = [running, stopped]
        fake_dir = MagicMock()
        fake_dir.exists.return_value = False
        mock_vm_dir.return_value = fake_dir
        result = runner.invoke(app, ["cleanup", "--all", "--force"])

    assert result.exit_code == 0
    assert "run1" in result.output
    assert "stop1" in result.output


def test_cleanup_dry_run():
    stopped = _make_vm("old2", VMState.STOPPED, "10.20.0.2")
    with patch("fcm.cli.vm.VMManager") as mock_mgr_cls:
        mock_mgr_cls.return_value.list_all.return_value = [stopped]
        result = runner.invoke(app, ["cleanup", "--dry-run"])

    assert result.exit_code == 0
    assert "Dry run" in result.output
    mock_mgr_cls.return_value.deregister.assert_not_called()


def test_cleanup_abort_no_force():
    stopped = _make_vm("old3", VMState.STOPPED, "10.20.0.2")
    with patch("fcm.cli.vm.VMManager") as mock_mgr_cls:
        mock_mgr_cls.return_value.list_all.return_value = [stopped]
        result = runner.invoke(app, ["cleanup"], input="n\n")

    assert result.exit_code == 1
    mock_mgr_cls.return_value.deregister.assert_not_called()


# ---------------------------------------------------------------------------
# pause / resume / snapshot / load
# ---------------------------------------------------------------------------


def test_pause_success():
    mock_client = MagicMock()
    mock_client.pause_vm.return_value = True
    with (
        patch("fcm.cli.vm.get_vm_socket_path", return_value=Path("/tmp/fake.sock")),
        patch("fcm.cli.vm.FirecrackerClient", return_value=mock_client),
        patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
    ):
        result = runner.invoke(app, ["pause", "--name", "myvm"])
    assert result.exit_code == 0
    mock_client.pause_vm.assert_called_once()
    mock_client.close.assert_called_once()


def test_pause_no_socket():
    with patch("fcm.cli.vm.get_vm_socket_path", return_value=None):
        result = runner.invoke(app, ["pause", "--name", "myvm"])
    assert result.exit_code == 1
    assert "Socket not found" in result.output


def test_pause_failure():
    mock_client = MagicMock()
    mock_client.pause_vm.return_value = False
    with (
        patch("fcm.cli.vm.get_vm_socket_path", return_value=Path("/tmp/fake.sock")),
        patch("fcm.cli.vm.FirecrackerClient", return_value=mock_client),
    ):
        result = runner.invoke(app, ["pause", "--name", "myvm"])
    assert result.exit_code == 1


def test_resume_success():
    mock_client = MagicMock()
    mock_client.resume_vm.return_value = True
    with (
        patch("fcm.cli.vm.get_vm_socket_path", return_value=Path("/tmp/fake.sock")),
        patch("fcm.cli.vm.FirecrackerClient", return_value=mock_client),
        patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
    ):
        result = runner.invoke(app, ["resume", "--name", "myvm"])
    assert result.exit_code == 0
    mock_client.resume_vm.assert_called_once()
    mock_client.close.assert_called_once()


def test_resume_no_socket():
    with patch("fcm.cli.vm.get_vm_socket_path", return_value=None):
        result = runner.invoke(app, ["resume", "--name", "myvm"])
    assert result.exit_code == 1
    assert "Socket not found" in result.output


def test_resume_failure():
    mock_client = MagicMock()
    mock_client.resume_vm.return_value = False
    with (
        patch("fcm.cli.vm.get_vm_socket_path", return_value=Path("/tmp/fake.sock")),
        patch("fcm.cli.vm.FirecrackerClient", return_value=mock_client),
    ):
        result = runner.invoke(app, ["resume", "--name", "myvm"])
    assert result.exit_code == 1


def test_snapshot_success():
    mock_client = MagicMock()
    mock_client.create_snapshot.return_value = True
    with (
        patch("fcm.cli.vm.get_vm_socket_path", return_value=Path("/tmp/fake.sock")),
        patch("fcm.cli.vm.FirecrackerClient", return_value=mock_client),
    ):
        result = runner.invoke(
            app,
            [
                "snapshot",
                "--name",
                "myvm",
                "--mem-out",
                "/tmp/mem.snap",
                "--state-out",
                "/tmp/state.snap",
            ],
        )
    assert result.exit_code == 0
    mock_client.create_snapshot.assert_called_once()
    mock_client.close.assert_called_once()


def test_snapshot_no_socket():
    with patch("fcm.cli.vm.get_vm_socket_path", return_value=None):
        result = runner.invoke(
            app,
            [
                "snapshot",
                "--name",
                "myvm",
                "--mem-out",
                "/tmp/mem.snap",
                "--state-out",
                "/tmp/state.snap",
            ],
        )
    assert result.exit_code == 1
    assert "Socket not found" in result.output


def test_snapshot_failure():
    mock_client = MagicMock()
    mock_client.create_snapshot.return_value = False
    with (
        patch("fcm.cli.vm.get_vm_socket_path", return_value=Path("/tmp/fake.sock")),
        patch("fcm.cli.vm.FirecrackerClient", return_value=mock_client),
    ):
        result = runner.invoke(
            app,
            [
                "snapshot",
                "--name",
                "myvm",
                "--mem-out",
                "/tmp/mem.snap",
                "--state-out",
                "/tmp/state.snap",
            ],
        )
    assert result.exit_code == 1


def test_load_success():
    mock_client = MagicMock()
    mock_client.load_snapshot.return_value = True
    with (
        patch("fcm.cli.vm.get_vm_socket_path", return_value=Path("/tmp/fake.sock")),
        patch("fcm.cli.vm.FirecrackerClient", return_value=mock_client),
    ):
        result = runner.invoke(
            app,
            [
                "load",
                "--name",
                "myvm",
                "--mem-in",
                "/tmp/mem.snap",
                "--state-in",
                "/tmp/state.snap",
            ],
        )
    assert result.exit_code == 0
    mock_client.load_snapshot.assert_called_once()
    mock_client.close.assert_called_once()


def test_load_no_socket():
    with patch("fcm.cli.vm.get_vm_socket_path", return_value=None):
        result = runner.invoke(
            app,
            [
                "load",
                "--name",
                "myvm",
                "--mem-in",
                "/tmp/mem.snap",
                "--state-in",
                "/tmp/state.snap",
            ],
        )
    assert result.exit_code == 1
    assert "Socket not found" in result.output


def test_load_failure():
    mock_client = MagicMock()
    mock_client.load_snapshot.return_value = False
    with (
        patch("fcm.cli.vm.get_vm_socket_path", return_value=Path("/tmp/fake.sock")),
        patch("fcm.cli.vm.FirecrackerClient", return_value=mock_client),
    ):
        result = runner.invoke(
            app,
            [
                "load",
                "--name",
                "myvm",
                "--mem-in",
                "/tmp/mem.snap",
                "--state-in",
                "/tmp/state.snap",
            ],
        )
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# setup – NetworkError path
# ---------------------------------------------------------------------------


def test_setup_network_error():
    with (
        patch("fcm.cli.vm.setup_bridge", side_effect=NetworkError("bridge fail")),
        patch("fcm.cli.vm.setup_nat"),
    ):
        result = runner.invoke(app, ["setup"])
    assert result.exit_code == 1
    assert "Network setup failed" in result.output


# ---------------------------------------------------------------------------
# _write_cloud_init helper
# ---------------------------------------------------------------------------


def test_write_cloud_init_with_ssh_key():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        _write_cloud_init(cloud_init_dir, "testvm", "10.20.0.2", "root",
                          ssh_pub_key="ssh-ed25519 AAAA testkey")

        meta = (cloud_init_dir / "meta-data").read_text()
        assert "testvm" in meta
        net = (cloud_init_dir / "network-config").read_text()
        assert "10.20.0.2" in net
        userdata = (cloud_init_dir / "user-data").read_text()
        assert "ssh-ed25519 AAAA testkey" in userdata
        assert "ssh-authorized-keys" in userdata


def test_write_cloud_init_without_ssh_key():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        _write_cloud_init(cloud_init_dir, "testvm2", "10.20.0.3", "ubuntu")

        userdata = (cloud_init_dir / "user-data").read_text()
        assert "ssh-authorized-keys" not in userdata
        assert "#cloud-config" in userdata


def test_write_cloud_init_custom_user_data():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        custom_file = tmp_path / "custom-userdata.yaml"
        custom_file.write_text("#cloud-config\npackages:\n  - nginx\n")

        _write_cloud_init(cloud_init_dir, "testvm3", "10.20.0.4", "root",
                          custom_user_data=custom_file)

        userdata = (cloud_init_dir / "user-data").read_text()
        assert "nginx" in userdata
        assert "#cloud-config" in userdata


def test_write_cloud_init_custom_user_data_with_ssh_key_injection():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        custom_file = tmp_path / "custom-userdata.yaml"
        custom_file.write_text("#cloud-config\npackages:\n  - vim\n")

        _write_cloud_init(cloud_init_dir, "testvm4", "10.20.0.5", "root",
                          ssh_pub_key="ssh-ed25519 AAAA injected",
                          custom_user_data=custom_file)

        userdata = (cloud_init_dir / "user-data").read_text()
        assert "ssh-ed25519 AAAA injected" in userdata
        assert "ssh-authorized-keys" in userdata
        assert "vim" in userdata


def test_write_cloud_init_custom_user_data_merge_existing_ssh():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        custom_file = tmp_path / "custom-userdata.yaml"
        custom_file.write_text(
            "#cloud-config\nusers:\n  - name: admin\n"
            "    ssh_authorized_keys:\n      - ssh-rsa EXISTING\n"
        )

        _write_cloud_init(cloud_init_dir, "testvm5", "10.20.0.6", "root",
                          ssh_pub_key="ssh-ed25519 AAAA newkey",
                          custom_user_data=custom_file)

        userdata = (cloud_init_dir / "user-data").read_text()
        assert "ssh-rsa EXISTING" in userdata
        assert "ssh-ed25519 AAAA newkey" in userdata


# ---------------------------------------------------------------------------
# --mac flag
# ---------------------------------------------------------------------------


def test_create_vm_with_custom_mac():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

        mock_proc = MagicMock()
        mock_proc.pid = 7777

        with (
            patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}),
            patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
            patch("fcm.cli.vm.get_network", return_value=_FAKE_NET),
            patch("fcm.cli.vm.allocate_network_ip", return_value="10.20.0.7"),
            patch("fcm.cli.vm.create_tap"),
            patch("fcm.cli.vm.add_iptables_forward_rules"),
            patch("fcm.cli.vm.ConfigGenerator"),
            patch("subprocess.Popen", return_value=mock_proc),
            patch("fcm.cli.vm._inject_cloud_init"),
        ):
            mock_mgr_cls.return_value.list_all.return_value = []
            result = runner.invoke(app, [
                "create", "--name", "macvm",
                "--image", "ubuntu-24.04",
                "--mac", "02:AA:BB:CC:DD:EE",
            ])

        assert result.exit_code == 0
        assert "02:AA:BB:CC:DD:EE" in result.output


# ---------------------------------------------------------------------------
# --user-data flag
# ---------------------------------------------------------------------------


def test_create_vm_with_user_data():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

        ud_file = tmp_path / "my-userdata.yaml"
        ud_file.write_text("#cloud-config\npackages:\n  - htop\n")

        mock_proc = MagicMock()
        mock_proc.pid = 6666

        with (
            patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}),
            patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
            patch("fcm.cli.vm.get_network", return_value=_FAKE_NET),
            patch("fcm.cli.vm.allocate_network_ip", return_value="10.20.0.8"),
            patch("fcm.cli.vm.create_tap"),
            patch("fcm.cli.vm.add_iptables_forward_rules"),
            patch("fcm.cli.vm.ConfigGenerator"),
            patch("subprocess.Popen", return_value=mock_proc),
            patch("fcm.cli.vm._inject_cloud_init"),
        ):
            mock_mgr_cls.return_value.list_all.return_value = []
            result = runner.invoke(app, [
                "create", "--name", "udvm",
                "--image", "ubuntu-24.04",
                "--user-data", str(ud_file),
            ])

        assert result.exit_code == 0


def test_create_vm_user_data_file_not_found():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

        with (
            patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}),
            patch("fcm.cli.vm.get_network", return_value=_FAKE_NET),
            patch("fcm.cli.vm.allocate_network_ip", return_value="10.20.0.9"),
        ):
            result = runner.invoke(app, [
                "create", "--name", "badud",
                "--image", "ubuntu-24.04",
                "--user-data", "/nonexistent/path.yaml",
            ])

        assert result.exit_code == 1
        assert "not found" in result.output.lower()


def test_create_vm_user_data_warns_no_cloud_config_header():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        kernels_dir = tmp_path / "kernels"
        kernels_dir.mkdir(parents=True)
        (kernels_dir / "vmlinux").write_text("fake kernel")
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

        ud_file = tmp_path / "bad-userdata.yaml"
        ud_file.write_text("packages:\n  - htop\n")

        mock_proc = MagicMock()
        mock_proc.pid = 5555

        with (
            patch.dict(os.environ, {"FCM_CACHE_DIR": tmp}),
            patch("fcm.cli.vm.VMManager") as mock_mgr_cls,
            patch("fcm.cli.vm.get_network", return_value=_FAKE_NET),
            patch("fcm.cli.vm.allocate_network_ip", return_value="10.20.0.10"),
            patch("fcm.cli.vm.create_tap"),
            patch("fcm.cli.vm.add_iptables_forward_rules"),
            patch("fcm.cli.vm.ConfigGenerator"),
            patch("subprocess.Popen", return_value=mock_proc),
            patch("fcm.cli.vm._inject_cloud_init"),
        ):
            mock_mgr_cls.return_value.list_all.return_value = []
            result = runner.invoke(app, [
                "create", "--name", "warnvm",
                "--image", "ubuntu-24.04",
                "--user-data", str(ud_file),
            ])

        assert result.exit_code == 0
        assert "warning" in result.output.lower()
