import json
from datetime import datetime

from click.testing import CliRunner as ClickCliRunner
from pytest_mock import MockerFixture
from typer.testing import CliRunner

from mvmctl.cli.vm import app
from mvmctl.exceptions import MVMError
from mvmctl.main import app as main_app
from mvmctl.models.vm import VMInstance, VMState

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
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[])
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "No VMs found" in result.output


def test_list_vms_json(mocker: MockerFixture):
    vm = _make_vm("myvm")
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[vm])
    result = runner.invoke(app, ["ls", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert len(data) == 1
    assert data[0]["name"] == "myvm"


def test_list_vms_all_flag(mocker: MockerFixture):
    running_vm = _make_vm("vm-running", VMState.RUNNING, "10.20.0.2")
    stopped_vm = _make_vm("vm-stopped", VMState.STOPPED, "10.20.0.3")
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[running_vm, stopped_vm])
    result = runner.invoke(app, ["ls", "--all", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    names = {item["name"] for item in data}
    assert "vm-running" in names
    assert "vm-stopped" in names


def test_rm_vm_not_found(mocker: MockerFixture):
    mock_mgr = mocker.MagicMock()
    mock_mgr.get_by_name.return_value = []
    mock_mgr.find_by_short_id.return_value = []
    mocker.patch("mvmctl.core.vm_manager.get_vm_manager", return_value=mock_mgr)
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
    result = runner.invoke(app, ["rm", "--name", "nonexistent", "--force"])
    assert result.exit_code == 1


def test_rm_force_running_vm(mocker: MockerFixture):
    vm = _make_vm("delvm", VMState.RUNNING)
    mock_mgr = mocker.MagicMock()
    mock_mgr.get_by_name.return_value = [vm]
    mock_mgr.find_by_short_id.return_value = []
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
    mocker.patch("mvmctl.cli.vm.remove_vm")
    result = runner.invoke(app, ["rm", "--name", "delvm", "--force"])
    assert result.exit_code == 0
    assert "removed" in result.output.lower()


def test_prune_nothing_to_do(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[])
    result = runner.invoke(app, ["prune"])
    assert result.exit_code == 0
    assert "Nothing to clean up" in result.output


def test_prune_with_vms(mocker: MockerFixture):
    stopped_vm = _make_vm("vm-stopped", VMState.STOPPED, "10.20.0.3")
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[stopped_vm])
    mocker.patch("mvmctl.cli.vm.cleanup_vms")
    result = runner.invoke(app, ["prune", "--force"])
    assert result.exit_code == 0
    assert "Removed" in result.output


def test_create_vm_success(mocker: MockerFixture):
    vm = _make_vm("newvm")
    mocker.patch("mvmctl.cli.vm.resolve_image_short_id_path", return_value="/tmp/image.ext4")
    mocker.patch("mvmctl.cli.vm.create_vm", return_value=vm)
    result = runner.invoke(app, ["create", "--name", "newvm", "--image", "abc123"])
    assert result.exit_code == 0
    assert "newvm" in result.output


def test_create_vm_fail(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.resolve_image_short_id_path", return_value="/tmp/image.ext4")
    mocker.patch("mvmctl.cli.vm.create_vm", side_effect=MVMError("Kernel not found"))
    result = runner.invoke(app, ["create", "--name", "newvm", "--image", "abc123"])
    assert result.exit_code == 1
    assert "Kernel not found" in result.output


def test_create_vm_rejects_non_matching_image_short_id(mocker: MockerFixture):
    mocker.patch(
        "mvmctl.cli.vm.resolve_image_short_id_path",
        side_effect=MVMError("Image short ID not found or ambiguous: 'badid'"),
    )
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm")
    result = runner.invoke(app, ["create", "--name", "newvm", "--image", "badid"])
    assert result.exit_code == 1
    assert "Image short ID 'badid' was not found or is ambiguous" in result.output
    mock_create.assert_not_called()


def test_create_vm_resolves_kernel_short_id(mocker: MockerFixture):
    vm = _make_vm("newvm")
    mocker.patch("mvmctl.cli.vm.resolve_image_short_id_path", return_value="/tmp/image.ext4")
    mocker.patch("mvmctl.cli.vm.resolve_kernel_short_id_path", return_value="/tmp/vmlinux")
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm", return_value=vm)
    result = runner.invoke(
        app,
        ["create", "--name", "newvm", "--image", "abc123", "--kernel", "def456"],
    )
    assert result.exit_code == 0
    mock_create.assert_called_once()
    assert mock_create.call_args.kwargs["image"] == "/tmp/image.ext4"
    assert mock_create.call_args.kwargs["kernel"] == "/tmp/vmlinux"


def test_create_output_config_uses_resolved_absolute_paths(mocker: MockerFixture, tmp_path):
    image_path = tmp_path / "rootfs.ext4"
    kernel_path = tmp_path / "vmlinux"
    output_path = tmp_path / "vm.json"

    mocker.patch("mvmctl.cli.vm.resolve_image_short_id_path", return_value=image_path)
    mocker.patch("mvmctl.cli.vm.resolve_kernel_short_id_path", return_value=kernel_path)
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm")
    mock_build = mocker.patch("mvmctl.cli.vm.build_vm_config_file")
    mock_config = mocker.MagicMock()
    mock_build.return_value = mock_config

    result = runner.invoke(
        app,
        [
            "create",
            "--name",
            "newvm",
            "--image",
            "abc123",
            "--kernel",
            "def456",
            "--output-config",
            str(output_path),
        ],
    )

    assert result.exit_code == 0
    assert mock_build.call_args.kwargs["image"] == str(image_path)
    assert mock_build.call_args.kwargs["kernel"] == str(kernel_path)
    assert mock_build.call_args.kwargs["rootfs_path"] == image_path
    assert mock_build.call_args.kwargs["tap_device"] is None
    assert mock_build.call_args.kwargs["gateway"] is None
    mock_create.assert_not_called()
    mock_config.to_json_file.assert_called_once_with(output_path)


def test_ssh_success(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.ssh_vm", return_value=0)
    result = runner.invoke(app, ["ssh", "--name", "myvm"])
    assert result.exit_code == 0


def test_ssh_failure(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.ssh_vm", return_value=1)
    result = runner.invoke(app, ["ssh", "--name", "badvm"])
    assert result.exit_code == 1


def test_logs_success(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.get_logs", return_value=["line 1\n", "line 2\n"])
    result = runner.invoke(app, ["logs", "--name", "myvm"])
    assert result.exit_code == 0


def test_logs_failure(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.get_logs", side_effect=MVMError("Log error"))
    result = runner.invoke(app, ["logs", "--name", "badvm"])
    assert result.exit_code == 1


def test_snapshot_success(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.snapshot_vm")
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
    mocker.patch("mvmctl.cli.vm.snapshot_vm", side_effect=MVMError("Failed to create snapshot"))
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
    mocker.patch("mvmctl.cli.vm.load_snapshot")
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
    mocker.patch("mvmctl.cli.vm.load_snapshot", side_effect=MVMError("Failed to load snapshot"))
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
# T-H3: vm create error-path tests
# ---------------------------------------------------------------------------


def test_create_missing_name_flag():
    """Omitting --name should fail (required option)."""
    result = runner.invoke(app, ["create", "--image", "ubuntu-24.04"])
    assert result.exit_code != 0


def test_create_missing_image_flag():
    """Omitting --image should fail (required option)."""
    result = runner.invoke(app, ["create", "--name", "myvm"])
    assert result.exit_code != 0


def test_create_invalid_image_not_found(mocker: MockerFixture):
    """Image that doesn't exist should result in exit code 1."""
    mocker.patch(
        "mvmctl.cli.vm.resolve_image_short_id_path",
        side_effect=MVMError("Image short ID not found or ambiguous: 'no-such-image'"),
    )
    mocker.patch(
        "mvmctl.cli.vm.create_vm",
        side_effect=MVMError("Image not found: 'no-such-image'"),
    )
    result = runner.invoke(app, ["create", "--name", "myvm", "--image", "no-such-image"])
    assert result.exit_code == 1
    assert "Image short ID 'no-such-image' was not found or is ambiguous" in result.output


def test_create_duplicate_vm_name(mocker: MockerFixture):
    """Creating a VM whose name already exists should fail with exit code 1."""
    mocker.patch("mvmctl.cli.vm.resolve_image_short_id_path", return_value="/tmp/image.ext4")
    mocker.patch(
        "mvmctl.cli.vm.create_vm",
        side_effect=MVMError("VM 'myvm' already exists"),
    )
    result = runner.invoke(app, ["create", "--name", "myvm", "--image", "abc123"])
    assert result.exit_code == 1
    assert "already exists" in result.output


# ---------------------------------------------------------------------------
# T-H3 (via main app): vm create error-path tests through the top-level CLI
# ---------------------------------------------------------------------------

main_runner = ClickCliRunner()


def test_main_app_create_missing_name():
    """Missing --name via the top-level app should fail."""
    result = main_runner.invoke(main_app, ["vm", "create", "--image", "ubuntu-24.04"])
    assert result.exit_code != 0


def test_main_app_create_missing_image():
    """Missing --image via the top-level app should fail."""
    result = main_runner.invoke(main_app, ["vm", "create", "--name", "myvm"])
    assert result.exit_code != 0


def test_main_app_create_invalid_image(mocker: MockerFixture):
    """Invalid image via the top-level app should exit 1."""
    mocker.patch(
        "mvmctl.cli.vm.resolve_image_short_id_path",
        side_effect=MVMError("Image short ID not found or ambiguous: 'bogus'"),
    )
    mocker.patch(
        "mvmctl.cli.vm.create_vm",
        side_effect=MVMError("Image not found: 'bogus'"),
    )
    result = main_runner.invoke(main_app, ["vm", "create", "--name", "myvm", "--image", "bogus"])
    assert result.exit_code == 1
    assert "Image short ID 'bogus' was not found or is ambiguous" in result.output


def test_main_app_create_duplicate(mocker: MockerFixture):
    """Duplicate VM name via the top-level app should exit 1."""
    mocker.patch("mvmctl.cli.vm.resolve_image_short_id_path", return_value="/tmp/image.ext4")
    mocker.patch(
        "mvmctl.cli.vm.create_vm",
        side_effect=MVMError("VM 'myvm' already exists at /some/path"),
    )
    result = main_runner.invoke(main_app, ["vm", "create", "--name", "myvm", "--image", "abc123"])
    assert result.exit_code == 1
    assert "already exists" in result.output


# ---------------------------------------------------------------------------
# T-H8: vm snapshot / vm load — error-path tests
# ---------------------------------------------------------------------------


def test_snapshot_vm_not_found(mocker: MockerFixture):
    """Snapshot on a non-existent VM should exit 1."""
    mocker.patch(
        "mvmctl.cli.vm.snapshot_vm",
        side_effect=MVMError(
            "Socket not found for VM 'ghost'. Must be running with --enable-api-socket"
        ),
    )
    result = runner.invoke(
        app,
        [
            "snapshot",
            "--name",
            "ghost",
            "--mem-out",
            "/tmp/mem.snap",
            "--state-out",
            "/tmp/state.snap",
        ],
    )
    assert result.exit_code == 1
    assert "Socket not found" in result.output


def test_snapshot_no_api_socket(mocker: MockerFixture):
    """Snapshot without API socket enabled should exit 1."""
    mocker.patch(
        "mvmctl.cli.vm.snapshot_vm",
        side_effect=MVMError(
            "Socket not found for VM 'myvm'. Must be running with --enable-api-socket"
        ),
    )
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
    assert "enable-api-socket" in result.output


def test_snapshot_missing_required_flags():
    """Snapshot without --mem-out or --state-out should fail."""
    result = runner.invoke(app, ["snapshot", "--name", "myvm"])
    assert result.exit_code != 0


def test_load_vm_not_found(mocker: MockerFixture):
    """Load snapshot on a non-existent VM should exit 1."""
    mocker.patch(
        "mvmctl.cli.vm.load_snapshot",
        side_effect=MVMError(
            "Socket not found for VM 'ghost'. Must be running with --enable-api-socket"
        ),
    )
    result = runner.invoke(
        app,
        [
            "load",
            "--name",
            "ghost",
            "--mem-in",
            "/tmp/mem.snap",
            "--state-in",
            "/tmp/state.snap",
        ],
    )
    assert result.exit_code == 1
    assert "Socket not found" in result.output


def test_load_no_api_socket(mocker: MockerFixture):
    """Load without API socket enabled should exit 1."""
    mocker.patch(
        "mvmctl.cli.vm.load_snapshot",
        side_effect=MVMError(
            "Socket not found for VM 'myvm'. Must be running with --enable-api-socket"
        ),
    )
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
    assert "enable-api-socket" in result.output


def test_load_missing_snapshot_files(mocker: MockerFixture):
    """Load with non-existent snapshot files should exit 1."""
    mocker.patch(
        "mvmctl.cli.vm.load_snapshot",
        side_effect=MVMError("Snapshot file not found: /nonexistent/mem.snap"),
    )
    result = runner.invoke(
        app,
        [
            "load",
            "--name",
            "myvm",
            "--mem-in",
            "/nonexistent/mem.snap",
            "--state-in",
            "/nonexistent/state.snap",
        ],
    )
    assert result.exit_code == 1
    assert "not found" in result.output


def test_load_missing_required_flags():
    """Load without --mem-in or --state-in should fail."""
    result = runner.invoke(app, ["load", "--name", "myvm"])
    assert result.exit_code != 0


def test_load_no_resume_flag(mocker: MockerFixture):
    """Load with --no-resume flag should invoke load_snapshot with resume_after=False."""
    mock_load = mocker.patch("mvmctl.cli.vm.load_snapshot")
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
            "--no-resume",
        ],
    )
    assert result.exit_code == 0
    mock_load.assert_called_once()
    call_kwargs = mock_load.call_args
    assert call_kwargs.kwargs.get("resume_after") is False or (
        len(call_kwargs.args) >= 4 and call_kwargs.args[3] is False
    )


def test_ps_alias(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[_make_vm("myvm")])
    result = runner.invoke(app, ["ps"])
    assert result.exit_code == 0
    assert "myvm" in result.output


def test_ps_all_flag(mocker: MockerFixture):
    mocker.patch(
        "mvmctl.cli.vm.list_vms",
        return_value=[
            _make_vm("running", VMState.RUNNING),
            _make_vm("stopped", VMState.STOPPED),
        ],
    )
    result = runner.invoke(app, ["ps", "--all"])
    assert result.exit_code == 0
    assert "running" in result.output
    assert "stopped" in result.output


def test_rm_by_short_id(mocker: MockerFixture):
    vm = _make_vm("myvm")
    mock_mgr = mocker.MagicMock()
    mock_mgr.find_by_short_id.return_value = [vm]
    mock_mgr.get_by_name.return_value = []
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
    mocker.patch("mvmctl.cli.vm.remove_vm")
    result = runner.invoke(app, ["rm", "abc123", "--force"])
    assert result.exit_code == 0
    assert "myvm" in result.output


def test_rm_multiple_names(mocker: MockerFixture):
    vm1 = _make_vm("vm1")
    vm2 = _make_vm("vm2")
    mock_mgr = mocker.MagicMock()
    mock_mgr.find_by_short_id.return_value = []
    mock_mgr.get_by_name.side_effect = lambda n: [vm1] if n == "vm1" else [vm2]
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
    mocker.patch("mvmctl.cli.vm.remove_vm")
    result = runner.invoke(app, ["rm", "--name", "vm1", "--name", "vm2", "--force"])
    assert result.exit_code == 0


def test_rm_no_targets(mocker: MockerFixture):
    mock_mgr = mocker.MagicMock()
    mock_mgr.find_by_short_id.return_value = []
    mock_mgr.get_by_name.return_value = []
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
    result = runner.invoke(app, ["rm", "--force"])
    assert result.exit_code == 1


def test_prune_no_stopped(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[_make_vm("myvm")])
    result = runner.invoke(app, ["prune"])
    assert result.exit_code == 0
    assert "Nothing" in result.output


def test_prune_dry_run(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[_make_vm("stopped", VMState.STOPPED)])
    result = runner.invoke(app, ["prune", "--dry-run"])
    assert result.exit_code == 0
    assert "Dry run" in result.output
