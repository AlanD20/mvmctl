# pyright: reportMissingImports=false
import json
from datetime import datetime
from pathlib import Path

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
        id="a" * 64,  # Full 64-char SHA256 hex string
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
    result = runner.invoke(app, ["rm", "--name", "nonexistent"])
    assert result.exit_code == 1


def test_rm_running_vm(mocker: MockerFixture):
    vm = _make_vm("delvm", VMState.RUNNING)
    mock_mgr = mocker.MagicMock()
    mock_mgr.get_by_name.return_value = [vm]
    mock_mgr.find_by_short_id.return_value = []
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
    mocker.patch("mvmctl.cli.vm.remove_vm")
    result = runner.invoke(app, ["rm", "--name", "delvm"])
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
    result = runner.invoke(app, ["prune"])
    assert result.exit_code == 0
    assert "Removed" in result.output


def test_create_vm_success(mocker: MockerFixture):
    vm = _make_vm("newvm")
    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value="/tmp/image.ext4")
    mocker.patch("mvmctl.cli.vm.create_vm", return_value=vm)
    result = runner.invoke(app, ["create", "--name", "newvm", "--image", "abc123"])
    assert result.exit_code == 0
    assert "newvm" in result.output


def test_create_vm_fail(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value="/tmp/image.ext4")
    mocker.patch("mvmctl.cli.vm.create_vm", side_effect=MVMError("Kernel not found"))
    result = runner.invoke(app, ["create", "--name", "newvm", "--image", "abc123"])
    assert result.exit_code == 1
    assert "Kernel not found" in result.output


def test_create_vm_rejects_non_matching_image_short_id(mocker: MockerFixture):
    mocker.patch(
        "mvmctl.cli.vm.resolve_image_multi_strategy",
        side_effect=MVMError("Image 'badid' was not found"),
    )
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm")
    result = runner.invoke(app, ["create", "--name", "newvm", "--image", "badid"])
    assert result.exit_code == 1
    assert "Image 'badid' was not found" in result.output
    mock_create.assert_not_called()


def test_create_vm_short_id_preserves_identifier_for_uuid_lookup(mocker: MockerFixture):
    vm = _make_vm("newvm")
    image_path = "/cache/images/ubuntu-24.04.ext4"
    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value=image_path)
    mocker.patch("mvmctl.cli.vm.resolve_kernel_multi_strategy", return_value="/tmp/vmlinux")
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm", return_value=vm)
    result = runner.invoke(
        app,
        ["create", "--name", "newvm", "--image", "1b0a", "--kernel", "def456"],
    )
    assert result.exit_code == 0
    mock_create.assert_called_once()
    assert mock_create.call_args.kwargs["image"] == "1b0a"
    assert mock_create.call_args.kwargs["kernel"] == "def456"

    vm = _make_vm("newvm")
    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value="/tmp/image.ext4")
    mocker.patch("mvmctl.cli.vm.resolve_kernel_multi_strategy", return_value="/tmp/vmlinux")
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm", return_value=vm)
    result = runner.invoke(
        app,
        ["create", "--name", "newvm", "--image", "abc123", "--kernel", "def456"],
    )
    assert result.exit_code == 0
    mock_create.assert_called_once()
    assert mock_create.call_args.kwargs["image"] == "abc123"
    assert mock_create.call_args.kwargs["kernel"] == "def456"


def test_create_output_config_uses_resolved_absolute_paths(mocker: MockerFixture, tmp_path):
    image_path = tmp_path / "rootfs.ext4"
    kernel_path = tmp_path / "vmlinux"
    output_path = tmp_path / "vm.json"

    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value=image_path)
    mocker.patch("mvmctl.cli.vm.resolve_kernel_multi_strategy", return_value=kernel_path)
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
        "mvmctl.cli.vm.resolve_image_multi_strategy",
        side_effect=MVMError("Image 'no-such-image' was not found"),
    )
    result = runner.invoke(app, ["create", "--name", "myvm", "--image", "no-such-image"])
    assert result.exit_code == 1
    assert "Image 'no-such-image' was not found" in result.output


def test_create_duplicate_vm_name(mocker: MockerFixture):
    """Creating a VM whose name already exists should fail with exit code 1."""
    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value="/tmp/image.ext4")
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
        "mvmctl.cli.vm.resolve_image_multi_strategy",
        side_effect=MVMError("Image 'bogus' was not found"),
    )
    result = main_runner.invoke(main_app, ["vm", "create", "--name", "myvm", "--image", "bogus"])
    assert result.exit_code == 1
    assert "Image 'bogus' was not found" in result.output


def test_main_app_create_duplicate(mocker: MockerFixture):
    """Duplicate VM name via the top-level app should exit 1."""
    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value="/tmp/image.ext4")
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


def test_rm_by_short_id(mocker: MockerFixture):  # No --force needed
    vm = _make_vm("myvm")
    mock_mgr = mocker.MagicMock()
    mock_mgr.find_by_short_id.return_value = [vm]
    mock_mgr.get_by_name.return_value = []
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
    mocker.patch("mvmctl.cli.vm.remove_vm")
    result = runner.invoke(app, ["rm", "abc123"])
    assert result.exit_code == 0
    assert "myvm" in result.output


def test_rm_multiple_names(mocker: MockerFixture):  # No --force needed
    vm1 = _make_vm("vm1")
    vm2 = _make_vm("vm2")
    mock_mgr = mocker.MagicMock()
    mock_mgr.find_by_short_id.return_value = []
    mock_mgr.get_by_name.side_effect = lambda n: [vm1] if n == "vm1" else [vm2]
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
    mocker.patch("mvmctl.cli.vm.remove_vm")
    result = runner.invoke(app, ["rm", "--name", "vm1", "--name", "vm2"])
    assert result.exit_code == 0


def test_rm_no_targets(mocker: MockerFixture):
    mock_mgr = mocker.MagicMock()
    mock_mgr.find_by_short_id.return_value = []
    mock_mgr.get_by_name.return_value = []
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
    result = runner.invoke(app, ["rm"])
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


def test_inspect_vm_command(mocker: MockerFixture):
    """Test mvm vm inspect command displays VM info."""
    mock_inspect = mocker.patch("mvmctl.api.vms.inspect_vm")
    mock_inspect.return_value = {
        "name": "myvm",
        "id": "abc123" + "x" * 58,
        "short_id": "abc123",
        "status": "running",
        "pid": 1234,
        "ip": "10.0.0.2",
        "mac": "02:FC:00:00:00:01",
        "network_name": "default",
        "tap_device": "mvm-def-abc-123",
        "created_at": "2026-01-01T12:00:00",
        "paths": {
            "vm_dir": "/home/user/.cache/mvmctl/vms/abc123xxx",
            "rootfs": "/home/user/.cache/mvmctl/vms/abc123xxx/rootfs.ext4",
            "config": "/home/user/.cache/mvmctl/vms/abc123xxx/firecracker.json",
        },
        "features": {
            "api_socket": True,
            "console": False,
            "nocloud_net": True,
        },
    }

    result = runner.invoke(app, ["inspect", "--name", "myvm"])

    assert result.exit_code == 0
    assert "myvm" in result.output
    assert "running" in result.output
    assert "10.0.0.2" in result.output
    mock_inspect.assert_called_once_with("myvm")


def test_inspect_vm_json_output(mocker: MockerFixture):
    """Test mvm vm inspect --json outputs valid JSON."""
    mock_inspect = mocker.patch("mvmctl.api.vms.inspect_vm")
    mock_inspect.return_value = {
        "name": "myvm",
        "id": "abc123" + "x" * 58,
        "short_id": "abc123",
        "status": "running",
        "pid": 1234,
        "ip": "10.0.0.2",
        "paths": {"vm_dir": "/tmp", "rootfs": None, "config": None},
        "features": {"api_socket": True, "console": False, "nocloud_net": False},
    }

    result = runner.invoke(app, ["inspect", "--name", "myvm", "--json"])

    assert result.exit_code == 0
    data = json.loads(result.output)
    assert data["name"] == "myvm"
    assert data["status"] == "running"


def test_inspect_vm_not_found(mocker: MockerFixture):
    """Test inspect handles missing VM gracefully."""
    mock_inspect = mocker.patch("mvmctl.api.vms.inspect_vm")
    mock_inspect.side_effect = MVMError("VM not found: missing-vm")

    result = runner.invoke(app, ["inspect", "--name", "missing-vm"])

    assert result.exit_code == 1
    assert "not found" in result.output.lower()


# ---------------------------------------------------------------------------
# State Validation X marks (Phase 4)
# ---------------------------------------------------------------------------


def test_vm_ls_shows_x_mark_for_missing_directory(mocker: MockerFixture):
    """Verify X prefix when VM directory missing."""
    vm = _make_vm("testvm", VMState.STOPPED)

    # Mock list_vms to return the VM
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[vm])

    # Mock get_vm_dir().exists() -> False
    mock_path = mocker.MagicMock()
    mock_path.exists.return_value = False
    mocker.patch("mvmctl.cli.vm.get_vm_dir", return_value=mock_path)

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "X " prefix in output
    assert "X " in result.output


def test_vm_ls_shows_x_mark_for_dead_process(mocker: MockerFixture):
    """Verify X prefix when PID not running."""
    vm = _make_vm("testvm", VMState.RUNNING)
    vm.pid = 1234  # Set a PID

    # Mock list_vms to return the VM
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[vm])

    # Mock get_vm_dir().exists() -> True (directory exists)
    mock_path = mocker.MagicMock()
    mock_path.exists.return_value = True
    mocker.patch("mvmctl.cli.vm.get_vm_dir", return_value=mock_path)

    # Mock os.kill(pid, 0) raises ProcessLookupError (process not running)
    mocker.patch("os.kill", side_effect=ProcessLookupError())

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "X " prefix in output
    assert "X " in result.output


def test_vm_ls_no_x_mark_for_running_vm(mocker: MockerFixture):
    """Verify no X prefix when VM directory exists and PID running."""
    vm = _make_vm("testvm", VMState.RUNNING)
    vm.pid = 1234  # Set a PID

    # Mock list_vms to return the VM
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[vm])

    # Mock get_vm_dir().exists() -> True
    mock_path = mocker.MagicMock()
    mock_path.exists.return_value = True
    mocker.patch("mvmctl.cli.vm.get_vm_dir", return_value=mock_path)

    # Mock os.kill(pid, 0) succeeds (process running)
    mocker.patch("os.kill", return_value=None)

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify no "X " prefix for running VM
    lines = result.output.split("\n")
    for line in lines:
        if "testvm" in line and "Name" not in line:
            assert not line.startswith("X ")


def test_vm_ls_shows_x_mark_for_missing_pid_file(mocker: MockerFixture):
    """Verify X prefix when PID file missing (can't verify process)."""
    vm = _make_vm("testvm", VMState.RUNNING)
    vm.pid = 1234

    # Mock list_vms to return the VM
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[vm])

    # Mock get_vm_dir() to return a path where pid file doesn't exist
    mock_vm_dir = mocker.MagicMock()
    mock_pid_file = mocker.MagicMock()
    mock_pid_file.exists.return_value = False
    mock_vm_dir.__truediv__ = lambda self, x: (
        mock_pid_file if x == "firecracker.pid" else mocker.MagicMock()
    )
    mock_vm_dir.exists.return_value = True
    mocker.patch("mvmctl.cli.vm.get_vm_dir", return_value=mock_vm_dir)

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "X " prefix in output
    assert "X " in result.output


# ---------------------------------------------------------------------------
# Exit code tracking tests (Phase 4)
# ---------------------------------------------------------------------------


def test_vm_ls_shows_exit_code_in_status(mocker: MockerFixture):
    """Verify vm ls displays 'exited(N)' format."""
    vm = _make_vm("exitedvm", VMState.STOPPED)
    vm.exit_code = 1

    # Mock list_vms returning VM with exit_code
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[vm])

    # Mock get_vm_status_with_exit_code returning tuple (status, exit_code)
    mocker.patch("mvmctl.cli.vm.get_vm_status_with_exit_code", return_value=("exited(1)", 1))

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify output contains "exited(1)"
    assert "exited(1)" in result.output or "exited" in result.output.lower()


def test_vm_ls_shows_running_status(mocker: MockerFixture):
    """Verify vm ls displays 'running' for active VMs."""
    vm = _make_vm("runningvm", VMState.RUNNING)

    # Mock list_vms returning running VM
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[vm])

    # Mock get_vm_status_with_exit_code returning tuple (status, exit_code)
    mocker.patch("mvmctl.cli.vm.get_vm_status_with_exit_code", return_value=("running", None))

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify output contains "running"
    assert "running" in result.output.lower()


def test_vm_ls_json_includes_exit_code(mocker: MockerFixture):
    """Verify JSON output includes exit_code field."""
    vm = _make_vm("testvm", VMState.STOPPED)
    vm.exit_code = 1

    # Mock list_vms returning VM with exit_code
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[vm])

    # Mock get_vm_status_with_exit_code returning tuple with exit code
    mocker.patch("mvmctl.cli.vm.get_vm_status_with_exit_code", return_value=("exited(1)", 1))

    result = runner.invoke(app, ["ls", "--json"])

    assert result.exit_code == 0
    # Verify JSON contains exit_code field
    data = json.loads(result.output)
    assert len(data) == 1
    assert "exit_code" in data[0]
    assert data[0]["exit_code"] == 1


# Tests for resolve_image_multi_strategy and resolve_kernel_multi_strategy


def test_resolve_image_direct_path(tmp_path: Path, monkeypatch):
    """resolve_image_multi_strategy returns direct path when file exists."""
    from mvmctl.api.vms import resolve_image_multi_strategy

    image_file = tmp_path / "test-image.ext4"
    image_file.write_text("dummy")

    result = resolve_image_multi_strategy(str(image_file))
    assert result == image_file


def test_resolve_image_yaml_name(mocker: MockerFixture, tmp_path: Path, monkeypatch):
    """resolve_image_multi_strategy resolves YAML image name via internal_id lookup."""
    from mvmctl.api.vms import resolve_image_multi_strategy

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))

    # Create images directory with a file named after the full hash
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    image_file = images_dir / "ubuntu-24.04.ext4"
    image_file.write_text("dummy")

    # Mock metadata to return internal_id match
    mock_entries = {
        "abc123fullhash": {
            "internal_id": "ubuntu-24.04",
            "filename": "ubuntu-24.04.ext4",
        }
    }
    mocker.patch("mvmctl.core.metadata.list_image_entries", return_value=mock_entries)

    result = resolve_image_multi_strategy("ubuntu-24.04")
    assert result.name == "ubuntu-24.04.ext4"


def test_resolve_image_short_id(mocker: MockerFixture, tmp_path: Path, monkeypatch):
    """resolve_image_multi_strategy falls back to short-ID resolution."""
    from mvmctl.api.vms import resolve_image_multi_strategy

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))

    # Mock short-id resolution to return a path
    mock_path = tmp_path / "images" / "abc123.ext4"
    mocker.patch(
        "mvmctl.api.vms._core_resolve_image_short_id_path",
        return_value=mock_path,
    )

    result = resolve_image_multi_strategy("abc123")
    assert result == mock_path


def test_resolve_kernel_direct_path(tmp_path: Path, monkeypatch):
    """resolve_kernel_multi_strategy returns direct path when file exists."""
    from mvmctl.api.vms import resolve_kernel_multi_strategy

    kernel_file = tmp_path / "vmlinux"
    kernel_file.write_text("dummy kernel")

    result = resolve_kernel_multi_strategy(str(kernel_file))
    assert result == kernel_file


def test_resolve_kernel_short_id(mocker: MockerFixture, tmp_path: Path, monkeypatch):
    """resolve_kernel_multi_strategy falls back to short-ID resolution."""
    from mvmctl.api.vms import resolve_kernel_multi_strategy

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))

    # Mock short-id resolution to return a path
    mock_path = tmp_path / "kernels" / "abc123"
    mocker.patch(
        "mvmctl.api.vms._core_resolve_kernel_short_id_path",
        return_value=mock_path,
    )

    result = resolve_kernel_multi_strategy("abc123")
    assert result == mock_path


class TestInspectCommand:
    """Tests for vm inspect command with positional selector."""

    def test_inspect_accepts_positional_selector(self, mocker: MockerFixture):
        """inspect command accepts VM name/ID as positional argument."""
        mock_inspect = mocker.patch("mvmctl.api.vms.inspect_vm")
        mock_inspect.return_value = {
            "name": "myvm",
            "id": "a" * 64,
            "short_id": "aaaaaa",
            "status": "running",
            "created_at": "2026-01-01",
            "pid": 1234,
            "ip": "10.0.0.2",
            "mac": "02:FC:00:00:00:01",
            "network_name": "default",
            "tap_device": "mvm-def-aaa-123",
            "features": {"api_socket": False, "console": True, "nocloud_net": True},
            "paths": {"vm_dir": "/tmp/vm", "rootfs": None, "config": None},
        }

        result = runner.invoke(app, ["inspect", "myvm"])
        assert result.exit_code == 0
        mock_inspect.assert_called_once_with("myvm")

    def test_inspect_accepts_name_option(self, mocker: MockerFixture):
        """inspect command still accepts --name option for backward compatibility."""
        mock_inspect = mocker.patch("mvmctl.api.vms.inspect_vm")
        mock_inspect.return_value = {
            "name": "myvm",
            "id": "a" * 64,
            "short_id": "aaaaaa",
            "status": "running",
            "created_at": "2026-01-01",
            "pid": 1234,
            "ip": "10.0.0.2",
            "mac": "02:FC:00:00:00:01",
            "network_name": "default",
            "tap_device": "mvm-def-aaa-123",
            "features": {"api_socket": False, "console": True, "nocloud_net": True},
            "paths": {"vm_dir": "/tmp/vm", "rootfs": None, "config": None},
        }

        result = runner.invoke(app, ["inspect", "--name", "myvm"])
        assert result.exit_code == 0
        mock_inspect.assert_called_once_with("myvm")

    def test_inspect_prefers_positional_over_option(self, mocker: MockerFixture):
        """inspect command prefers positional argument over --name option."""
        mock_inspect = mocker.patch("mvmctl.api.vms.inspect_vm")
        mock_inspect.return_value = {
            "name": "positional-vm",
            "id": "a" * 64,
            "short_id": "aaaaaa",
            "status": "running",
            "created_at": "2026-01-01",
            "pid": 1234,
            "ip": "10.0.0.2",
            "mac": "02:FC:00:00:00:01",
            "network_name": "default",
            "tap_device": "mvm-def-aaa-123",
            "features": {"api_socket": False, "console": True, "nocloud_net": True},
            "paths": {"vm_dir": "/tmp/vm", "rootfs": None, "config": None},
        }

        result = runner.invoke(app, ["inspect", "positional-vm", "--name", "option-vm"])
        assert result.exit_code == 0
        # Should use positional argument, not the --name option
        mock_inspect.assert_called_once_with("positional-vm")

    def test_inspect_requires_selector(self, mocker: MockerFixture):
        """inspect command requires either positional argument or --name option."""
        mock_inspect = mocker.patch("mvmctl.api.vms.inspect_vm")

        result = runner.invoke(app, ["inspect"])
        assert result.exit_code == 1
        assert "Must provide VM selector" in result.output or "Error" in result.output
        mock_inspect.assert_not_called()

    def test_inspect_json_output(self, mocker: MockerFixture):
        """inspect command supports --json output."""
        mock_inspect = mocker.patch("mvmctl.api.vms.inspect_vm")
        mock_inspect.return_value = {
            "name": "myvm",
            "id": "a" * 64,
            "short_id": "aaaaaa",
            "status": "running",
            "created_at": "2026-01-01",
            "pid": 1234,
            "ip": "10.0.0.2",
            "mac": "02:FC:00:00:00:01",
            "network_name": "default",
            "tap_device": "mvm-def-aaa-123",
            "features": {"api_socket": False, "console": True, "nocloud_net": True},
            "paths": {"vm_dir": "/tmp/vm", "rootfs": None, "config": None},
        }

        result = runner.invoke(app, ["inspect", "myvm", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["name"] == "myvm"


def test_create_output_config_with_cloud_init_mode(mocker: MockerFixture, tmp_path):
    """Test --output-config passes cloud_init mode when specified."""
    image_path = tmp_path / "rootfs.ext4"
    kernel_path = tmp_path / "vmlinux"
    output_path = tmp_path / "vm.json"

    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value=image_path)
    mocker.patch("mvmctl.cli.vm.resolve_kernel_multi_strategy", return_value=kernel_path)
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm")
    mock_build = mocker.patch("mvmctl.cli.vm.build_vm_config_file")
    mock_config = mocker.MagicMock()
    mock_config.cloud_init = {"mode": "nocloud-net", "enabled": True}
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
            "--cloud-init-mode",
            "nocloud-net",
            "--output-config",
            str(output_path),
        ],
    )

    assert result.exit_code == 0
    assert mock_build.call_args.kwargs["cloud_init"] is not None
    assert mock_build.call_args.kwargs["cloud_init"]["mode"] == "nocloud-net"
    mock_create.assert_not_called()
    mock_config.to_json_file.assert_called_once_with(output_path)


def test_create_output_config_with_no_cloud_init(mocker: MockerFixture, tmp_path):
    """Test --output-config with --no-cloud-init flag."""
    image_path = tmp_path / "rootfs.ext4"
    kernel_path = tmp_path / "vmlinux"
    output_path = tmp_path / "vm.json"

    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value=image_path)
    mocker.patch("mvmctl.cli.vm.resolve_kernel_multi_strategy", return_value=kernel_path)
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm")
    mock_build = mocker.patch("mvmctl.cli.vm.build_vm_config_file")
    mock_config = mocker.MagicMock()
    mock_config.cloud_init = {"mode": "disabled", "enabled": False}
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
            "--no-cloud-init",
            "--output-config",
            str(output_path),
        ],
    )

    assert result.exit_code == 0
    assert mock_build.call_args.kwargs["cloud_init"] is not None
    assert mock_build.call_args.kwargs["cloud_init"]["enabled"] is False
    assert mock_build.call_args.kwargs["cloud_init"]["mode"] == "disabled"
    mock_create.assert_not_called()


def test_create_output_config_with_user_data(mocker: MockerFixture, tmp_path):
    """Test --output-config with --user-data option."""
    image_path = tmp_path / "rootfs.ext4"
    kernel_path = tmp_path / "vmlinux"
    output_path = tmp_path / "vm.json"
    user_data_path = tmp_path / "user-data.yaml"
    user_data_path.write_text("#cloud-config\n")

    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value=image_path)
    mocker.patch("mvmctl.cli.vm.resolve_kernel_multi_strategy", return_value=kernel_path)
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm")
    mock_build = mocker.patch("mvmctl.cli.vm.build_vm_config_file")
    mock_config = mocker.MagicMock()
    mock_config.cloud_init = {"mode": "auto", "enabled": True, "user_data": str(user_data_path)}
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
            "--user-data",
            str(user_data_path),
            "--output-config",
            str(output_path),
        ],
    )

    assert result.exit_code == 0
    assert mock_build.call_args.kwargs["cloud_init"] is not None
    assert mock_build.call_args.kwargs["cloud_init"]["user_data"] == str(user_data_path)
    mock_create.assert_not_called()


def test_create_output_config_with_nocloud_net_flag(mocker: MockerFixture, tmp_path):
    """Test --output-config with --nocloud-net flag."""
    image_path = tmp_path / "rootfs.ext4"
    kernel_path = tmp_path / "vmlinux"
    output_path = tmp_path / "vm.json"

    mocker.patch("mvmctl.cli.vm.resolve_image_multi_strategy", return_value=image_path)
    mocker.patch("mvmctl.cli.vm.resolve_kernel_multi_strategy", return_value=kernel_path)
    mock_create = mocker.patch("mvmctl.cli.vm.create_vm")
    mock_build = mocker.patch("mvmctl.cli.vm.build_vm_config_file")
    mock_config = mocker.MagicMock()
    mock_config.cloud_init = {"mode": "nocloud-net", "enabled": True}
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
            "--nocloud-net",
            "--output-config",
            str(output_path),
        ],
    )

    assert result.exit_code == 0
    assert mock_build.call_args.kwargs["cloud_init"] is not None
    assert mock_build.call_args.kwargs["cloud_init"]["mode"] == "nocloud-net"
    assert mock_build.call_args.kwargs["cloud_init"]["enabled"] is True
    mock_create.assert_not_called()
