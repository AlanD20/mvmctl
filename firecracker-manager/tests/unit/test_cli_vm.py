import json
from datetime import datetime

from pytest_mock import MockerFixture
from typer.testing import CliRunner

from fcm.cli.vm import app
from fcm.exceptions import FCMError
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


def test_list_vms_empty(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.list_vms", return_value=[])
    result = runner.invoke(app, ["list"])
    assert result.exit_code == 0
    assert "No VMs found" in result.output


def test_list_vms_json(mocker: MockerFixture):
    vm = _make_vm("myvm")
    mocker.patch("fcm.cli.vm.list_vms", return_value=[vm])
    result = runner.invoke(app, ["list", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert len(data) == 1
    assert data[0]["name"] == "myvm"


def test_list_vms_all_flag(mocker: MockerFixture):
    running_vm = _make_vm("vm-running", VMState.RUNNING, "10.20.0.2")
    stopped_vm = _make_vm("vm-stopped", VMState.STOPPED, "10.20.0.3")
    mocker.patch("fcm.cli.vm.list_vms", return_value=[running_vm, stopped_vm])
    result = runner.invoke(app, ["list", "--all", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    names = {item["name"] for item in data}
    assert "vm-running" in names
    assert "vm-stopped" in names


def test_delete_vm_not_found(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.get_vm", return_value=None)
    result = runner.invoke(app, ["delete", "--name", "nonexistent", "--force"])
    assert result.exit_code == 1


def test_delete_force_running_vm(mocker: MockerFixture):
    vm = _make_vm("delvm", VMState.RUNNING)
    mocker.patch("fcm.cli.vm.get_vm", return_value=vm)
    mocker.patch("fcm.cli.vm.remove_vm")
    result = runner.invoke(app, ["delete", "--name", "delvm", "--force"])
    assert result.exit_code == 0
    assert "removed" in result.output.lower()


def test_cleanup_nothing_to_do(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.list_vms", return_value=[])
    result = runner.invoke(app, ["cleanup"])
    assert result.exit_code == 0
    assert "Nothing to clean up" in result.output


def test_cleanup_with_vms(mocker: MockerFixture):
    stopped_vm = _make_vm("vm-stopped", VMState.STOPPED, "10.20.0.3")
    mocker.patch("fcm.cli.vm.list_vms", return_value=[stopped_vm])
    mocker.patch("fcm.cli.vm.cleanup_vms")
    result = runner.invoke(app, ["cleanup", "--force"])
    assert result.exit_code == 0
    assert "Removed" in result.output


def test_create_vm_success(mocker: MockerFixture):
    vm = _make_vm("newvm")
    mocker.patch("fcm.cli.vm.create_vm", return_value=vm)
    result = runner.invoke(app, ["create", "--name", "newvm", "--image", "ubuntu-24.04"])
    assert result.exit_code == 0
    assert "newvm" in result.output


def test_create_vm_fail(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.create_vm", side_effect=FCMError("Kernel not found"))
    result = runner.invoke(app, ["create", "--name", "newvm", "--image", "ubuntu-24.04"])
    assert result.exit_code == 1
    assert "Kernel not found" in result.output


def test_ssh_success(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.ssh_vm", return_value=0)
    result = runner.invoke(app, ["ssh", "--name", "myvm"])
    assert result.exit_code == 0


def test_ssh_failure(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.ssh_vm", return_value=1)
    result = runner.invoke(app, ["ssh", "--name", "badvm"])
    assert result.exit_code == 1


def test_logs_success(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.get_logs", return_value=["line 1\n", "line 2\n"])
    result = runner.invoke(app, ["logs", "--name", "myvm"])
    assert result.exit_code == 0


def test_logs_failure(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.get_logs", side_effect=FCMError("Log error"))
    result = runner.invoke(app, ["logs", "--name", "badvm"])
    assert result.exit_code == 1


def test_pause_not_supported():
    result = runner.invoke(app, ["pause", "--name", "myvm"])
    assert result.exit_code == 0
    assert "not supported" in result.output.lower()


def test_resume_not_supported():
    result = runner.invoke(app, ["resume", "--name", "myvm"])
    assert result.exit_code == 0
    assert "not supported" in result.output.lower()


def test_snapshot_success(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.snapshot_vm")
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


def test_snapshot_failure(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.snapshot_vm", side_effect=FCMError("Failed to create snapshot"))
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


def test_load_success(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.load_snapshot")
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


def test_load_failure(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.load_snapshot", side_effect=FCMError("Failed to load snapshot"))
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
