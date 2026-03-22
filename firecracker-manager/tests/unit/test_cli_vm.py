import json
import os
import tempfile
from datetime import datetime
from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from fcm.cli.vm import app
from fcm.models.vm import VMInstance, VMState

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
