"""Tests for CLI VM commands."""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.exceptions import MVMError
from mvmctl.main import app
from mvmctl.models import VMInstanceItem
from mvmctl.models.result import NeedsInteraction, OperationResult

runner = CliRunner()


def _make_vm(
    name: str = "test-vm",
    status: str = "running",
    vm_id: str | None = None,
    **kwargs,
) -> VMInstanceItem:
    return VMInstanceItem(
        id=vm_id or f"{name}-id-" + "x" * 55,
        name=name,
        pid=1234,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="default",
        tap_device="mvm-default-tap0",
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
        status=status,
        image_id="i" * 64,
        kernel_id="k" * 64,
        binary_id="b" * 64,
        disk_size_mib=2048,
        vcpu_count=2,
        mem_size_mib=512,
        api_socket_path="fc.socket",
        rootfs_path="rootfs.ext4",
        rootfs_suffix=".ext4",
        enable_pci=False,
        enable_logging=True,
        enable_metrics=False,
        enable_console=False,
        cloud_init_mode="off",
        config_path="vm.json",
        log_path="fc.log",
        serial_output_path="serial.log",
        **kwargs,
    )


class TestVMLs:
    """Tests for 'vm ls' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_ls_empty(self, mock_vm_op):
        mock_vm_op.list_all.return_value = []
        result = runner.invoke(app, ["vm", "ls"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.vm.VMOperation")
    def test_ls_with_vms(self, mock_vm_op):
        mock_vm_op.list_all.return_value = [
            _make_vm("vm1", "running"),
            _make_vm("vm2", "stopped"),
        ]
        result = runner.invoke(app, ["vm", "ls"])
        assert result.exit_code == 0
        assert "vm1" in result.output
        assert "vm2" in result.output

    @patch("mvmctl.cli.vm.VMOperation")
    def test_ls_json(self, mock_vm_op):
        mock_vm_op.to_json.return_value = [
            {"name": "myvm", "network_name": None}
        ]
        result = runner.invoke(app, ["vm", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) == 1
        assert data[0]["name"] == "myvm"

    def test_ls_help(self):
        result = runner.invoke(app, ["vm", "ls", "--help"])
        assert result.exit_code == 0
        assert "List all VMs" in result.output


class TestVMPs:
    """Tests for 'vm ps' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_ps_empty(self, mock_vm_op):
        mock_vm_op.list_all.return_value = []
        result = runner.invoke(app, ["vm", "ps"])
        assert result.exit_code == 0
        assert "No active VMs" in result.output

    @patch("mvmctl.cli.vm.VMOperation")
    def test_ps_with_vms(self, mock_vm_op):
        mock_vm_op.list_all.return_value = [
            _make_vm("running-vm", "running"),
        ]
        result = runner.invoke(app, ["vm", "ps"])
        assert result.exit_code == 0
        assert "running-vm" in result.output


class TestVMCreate:
    """Tests for 'vm create' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_create_success(self, mock_vm_op):
        mock_vm_op.create.return_value = OperationResult(
            status="success", code="vm.created", message="VM created"
        )
        result = runner.invoke(
            app,
            [
                "vm",
                "create",
                "--name",
                "newvm",
                "--image",
                "ubuntu-24.04",
            ],
        )
        assert result.exit_code == 0
        assert "created" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_create_missing_name(self, mock_vm_op):
        """Missing --name should fail."""
        result = runner.invoke(app, ["vm", "create", "--image", "ubuntu-24.04"])
        assert result.exit_code != 0

    @patch("mvmctl.cli.vm.VMOperation")
    def test_create_api_error(self, mock_vm_op):
        mock_vm_op.create.side_effect = MVMError("VM already exists")
        result = runner.invoke(
            app,
            [
                "vm",
                "create",
                "--name",
                "existing",
                "--image",
                "ubuntu-24.04",
            ],
        )
        assert result.exit_code == 1
        assert "already exists" in result.output.lower()

    def test_create_help(self):
        result = runner.invoke(app, ["vm", "create", "--help"])
        assert result.exit_code == 0
        assert "Create and start" in result.output


class TestVMRemove:
    """Tests for 'vm rm' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_rm_success(self, mock_vm_op):
        mock_vm_op.remove.return_value = OperationResult(
            status="success", code="vm.removed", message="VM removed"
        )
        result = runner.invoke(app, ["vm", "rm", "myvm"])
        assert result.exit_code == 0
        assert "removed" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_rm_multiple(self, mock_vm_op):
        mock_vm_op.remove.return_value = OperationResult(
            status="success", code="vm.removed", message="VM removed"
        )
        result = runner.invoke(app, ["vm", "rm", "vm1", "vm2"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.vm.VMOperation")
    def test_rm_with_name_flag(self, mock_vm_op):
        mock_vm_op.remove.return_value = OperationResult(
            status="success", code="vm.removed", message="VM removed"
        )
        result = runner.invoke(app, ["vm", "rm", "myvm"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.vm.VMOperation")
    def test_rm_api_error(self, mock_vm_op):
        mock_vm_op.remove.side_effect = MVMError("VM not found")
        result = runner.invoke(app, ["vm", "rm", "nonexistent"])
        assert result.exit_code == 1
        assert "not found" in result.output.lower()


class TestVMStart:
    """Tests for 'vm start' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_start_success(self, mock_vm_op):
        mock_vm_op.start.return_value = OperationResult(
            status="success", code="vm.started", message="VM started"
        )
        result = runner.invoke(app, ["vm", "start", "myvm"])
        assert result.exit_code == 0
        assert "started" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_start_with_name_flag(self, mock_vm_op):
        mock_vm_op.start.return_value = OperationResult(
            status="success", code="vm.started", message="VM started"
        )
        result = runner.invoke(app, ["vm", "start", "myvm"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.vm.VMOperation")
    def test_start_api_error(self, mock_vm_op):
        mock_vm_op.start.side_effect = MVMError("VM not found")
        result = runner.invoke(app, ["vm", "start", "nonexistent"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.vm.VMOperation")
    def test_start_missing_identifier(self, mock_vm_op):
        result = runner.invoke(app, ["vm", "start"])
        assert result.exit_code != 0


class TestVMStop:
    """Tests for 'vm stop' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_stop_success(self, mock_vm_op):
        mock_vm_op.stop.return_value = OperationResult(
            status="success", code="vm.stopped", message="VM stopped"
        )
        result = runner.invoke(app, ["vm", "stop", "myvm"])
        assert result.exit_code == 0
        assert "stopped" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_stop_with_force(self, mock_vm_op):
        mock_vm_op.stop.return_value = OperationResult(
            status="success", code="vm.stopped", message="VM stopped"
        )
        result = runner.invoke(app, ["vm", "stop", "myvm", "--force"])
        assert result.exit_code == 0
        call_kwargs = mock_vm_op.stop.call_args
        assert call_kwargs[0][0].force is True

    @patch("mvmctl.cli.vm.VMOperation")
    def test_stop_api_error(self, mock_vm_op):
        mock_vm_op.stop.side_effect = MVMError("VM not running")
        result = runner.invoke(app, ["vm", "stop", "myvm"])
        assert result.exit_code == 1


class TestVMReboot:
    """Tests for 'vm reboot' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_reboot_success(self, mock_vm_op):
        mock_vm_op.reboot.return_value = OperationResult(
            status="success", code="vm.rebooted", message="VM rebooted"
        )
        result = runner.invoke(app, ["vm", "reboot", "myvm"])
        assert result.exit_code == 0
        assert "rebooted" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_reboot_with_force(self, mock_vm_op):
        mock_vm_op.reboot.return_value = OperationResult(
            status="success", code="vm.rebooted", message="VM rebooted"
        )
        result = runner.invoke(app, ["vm", "reboot", "myvm", "--force"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.vm.VMOperation")
    def test_reboot_api_error(self, mock_vm_op):
        mock_vm_op.reboot.side_effect = MVMError("VM not responsive")
        result = runner.invoke(app, ["vm", "reboot", "myvm"])
        assert result.exit_code == 1


class TestVMPause:
    """Tests for 'vm pause' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_pause_success(self, mock_vm_op):
        mock_vm_op.pause.return_value = OperationResult(
            status="success", code="vm.paused", message="VM paused"
        )
        result = runner.invoke(app, ["vm", "pause", "myvm"])
        assert result.exit_code == 0
        assert "paused" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_pause_api_error(self, mock_vm_op):
        mock_vm_op.pause.side_effect = MVMError("VM not running")
        result = runner.invoke(app, ["vm", "pause", "myvm"])
        assert result.exit_code == 1


class TestVMResume:
    """Tests for 'vm resume' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_resume_success(self, mock_vm_op):
        mock_vm_op.resume.return_value = OperationResult(
            status="success", code="vm.resumed", message="VM resumed"
        )
        result = runner.invoke(app, ["vm", "resume", "myvm"])
        assert result.exit_code == 0
        assert "resumed" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_resume_api_error(self, mock_vm_op):
        mock_vm_op.resume.side_effect = MVMError("VM not paused")
        result = runner.invoke(app, ["vm", "resume", "myvm"])
        assert result.exit_code == 1


class TestVMSnapshot:
    """Tests for 'vm snapshot' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_snapshot_success(self, mock_vm_op, tmp_path):
        mock_vm_op.snapshot.return_value = OperationResult(
            status="success",
            code="vm.snapshot_created",
            message="Snapshot created",
        )
        mem_file = tmp_path / "mem.snap"
        state_file = tmp_path / "state.snap"
        result = runner.invoke(
            app,
            [
                "vm",
                "snapshot",
                "myvm",
                str(mem_file),
                str(state_file),
            ],
        )
        assert result.exit_code == 0
        assert "snapshot created" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_snapshot_api_error(self, mock_vm_op, tmp_path):
        mock_vm_op.snapshot.side_effect = MVMError("VM not running")
        mem_file = tmp_path / "mem.snap"
        state_file = tmp_path / "state.snap"
        result = runner.invoke(
            app,
            [
                "vm",
                "snapshot",
                "myvm",
                str(mem_file),
                str(state_file),
            ],
        )
        assert result.exit_code == 1

    @patch("mvmctl.cli.vm.VMOperation")
    def test_snapshot_missing_args(self, mock_vm_op):
        result = runner.invoke(app, ["vm", "snapshot", "myvm"])
        assert result.exit_code != 0


class TestVMLoad:
    """Tests for 'vm load' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_load_success(self, mock_vm_op, tmp_path):
        mock_vm_op.load_snapshot.return_value = OperationResult(
            status="success",
            code="vm.snapshot_loaded",
            message="Snapshot loaded",
        )
        mem_file = tmp_path / "mem.snap"
        state_file = tmp_path / "state.snap"
        result = runner.invoke(
            app,
            [
                "vm",
                "load",
                "myvm",
                str(mem_file),
                str(state_file),
            ],
        )
        assert result.exit_code == 0
        assert "snapshot loaded" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_load_with_resume(self, mock_vm_op, tmp_path):
        mock_vm_op.load_snapshot.return_value = OperationResult(
            status="success",
            code="vm.snapshot_loaded",
            message="Snapshot loaded",
        )
        mem_file = tmp_path / "mem.snap"
        state_file = tmp_path / "state.snap"
        result = runner.invoke(
            app,
            [
                "vm",
                "load",
                "myvm",
                str(mem_file),
                str(state_file),
                "--resume",
            ],
        )
        assert result.exit_code == 0
        call_input = mock_vm_op.load_snapshot.call_args
        assert call_input[1]["resume_after"] is True

    @patch("mvmctl.cli.vm.VMOperation")
    def test_load_api_error(self, mock_vm_op, tmp_path):
        mock_vm_op.load_snapshot.side_effect = MVMError(
            "Snapshot file not found"
        )
        mem_file = tmp_path / "mem.snap"
        state_file = tmp_path / "state.snap"
        result = runner.invoke(
            app,
            [
                "vm",
                "load",
                "myvm",
                str(mem_file),
                str(state_file),
            ],
        )
        assert result.exit_code == 1


class TestVMInspect:
    """Tests for 'vm inspect' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_inspect_success(self, mock_vm_op):
        mock_vm_op.inspect.return_value = {
            "name": "myvm",
            "id": "abc123xxx",
            "status": "running",
            "created_at": "2026-01-01T12:00:00",
            "pid": 1234,
            "exit_code": None,
            "vcpus": 2,
            "mem_mib": 512,
            "disk_mib": 2048,
            "ipv4": "10.0.0.2",
            "mac": "02:FC:00:00:00:01",
            "tap_device": "mvm-default-tap0",
            "network_name": "default",
            "network_id": "net-default",
            "image_id": "i" * 64,
            "image_name": "Ubuntu 24.04",
            "kernel_id": "k" * 64,
            "kernel_version": "6.1.0",
            "binary_id": "b" * 64,
            "binary_name": "firecracker",
            "vm_dir": "/tmp/vm",
            "rootfs_path": "/tmp/vm/rootfs.ext4",
            "config_path": "/tmp/vm/vm.json",
            "log_path": "/tmp/vm/fc.log",
            "serial_output_path": "/tmp/vm/serial.log",
            "relay_running": False,
            "relay_pid": None,
            "relay_socket_path": None,
            "enable_pci": False,
            "enable_console": False,
            "enable_logging": True,
            "enable_metrics": False,
            "cloud_init_mode": "off",
        }
        result = runner.invoke(app, ["vm", "inspect", "myvm"])
        assert result.exit_code == 0
        assert "myvm" in result.output
        assert "running" in result.output

    @patch("mvmctl.cli.vm.VMOperation")
    def test_inspect_json(self, mock_vm_op):
        mock_vm_op.inspect.return_value = {
            "name": "myvm",
            "status": "running",
        }
        result = runner.invoke(app, ["vm", "inspect", "myvm", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["name"] == "myvm"

    @patch("mvmctl.cli.vm.VMOperation")
    def test_inspect_not_found(self, mock_vm_op):
        mock_vm_op.inspect.side_effect = MVMError("VM not found")
        result = runner.invoke(app, ["vm", "inspect", "nonexistent"])
        assert result.exit_code == 1
        assert "not found" in result.output.lower()


class TestVMExport:
    """Tests for 'vm export' command."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_export_success(self, mock_vm_op, tmp_path):
        mock_config = MagicMock()
        mock_config.to_dict.return_value = {
            "schema_version": "1.0",
            "name": "myvm",
        }
        mock_vm_op.export.return_value = mock_config
        output = tmp_path / "myvm.json"
        result = runner.invoke(app, ["vm", "export", "myvm", str(output)])
        assert result.exit_code == 0
        assert "Exported" in result.output
        assert output.exists()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_export_not_found(self, mock_vm_op):
        mock_vm_op.export.side_effect = MVMError("VM not found")
        result = runner.invoke(app, ["vm", "export", "nonexistent"])
        assert result.exit_code == 1


class TestVMImport:
    """Tests for 'vm import' command."""

    def test_import_success(self, mocker, tmp_path):
        from mvmctl.models.result import OperationResult

        mocker.patch(
            "mvmctl.cli.vm.VMOperation.import_",
            return_value=OperationResult(
                status="success", code="ok", message="imported"
            ),
        )
        config = tmp_path / "config.json"
        config.write_text('{"name": "myvm"}')
        result = runner.invoke(app, ["vm", "import", str(config)])
        assert result.exit_code == 0
        assert "imported" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_import_with_name_override(self, mock_vm_op, tmp_path):
        from mvmctl.models.result import OperationResult

        mock_vm_op.import_.return_value = OperationResult(
            status="success", code="ok", message="imported"
        )
        config = tmp_path / "config.json"
        config.write_text('{"name": "original"}')
        result = runner.invoke(
            app,
            [
                "vm",
                "import",
                str(config),
                "--name",
                "override",
            ],
        )
        assert result.exit_code == 0
        call_input = mock_vm_op.import_.call_args[0][0]
        assert call_input.name_override == "override"


class TestVMHelp:
    """Tests for vm command group help."""

    def test_vm_help(self):
        result = runner.invoke(app, ["vm", "--help"])
        assert result.exit_code == 0
        assert "VM lifecycle management" in result.output

    def test_vm_help_command(self):
        result = runner.invoke(app, ["vm", "help"])
        assert result.exit_code == 0


class TestVMLsEdgeCases:
    """Tests for 'vm ls' edge cases."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_ls_json_empty(self, mock_vm_op):
        mock_vm_op.to_json.return_value = []
        result = runner.invoke(app, ["vm", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data == []

    @patch("mvmctl.cli.vm.VMOperation")
    def test_ls_with_exit_code_set(self, mock_vm_op):
        mock_vm_op.list_all.return_value = [
            _make_vm("exited-vm", "stopped", exit_code=0),
        ]
        result = runner.invoke(app, ["vm", "ls"])
        assert result.exit_code == 0
        assert "0" in result.output

    @patch("mvmctl.cli.vm.VMOperation")
    def test_ls_json_includes_all_fields(self, mock_vm_op):
        mock_vm_op.to_json.return_value = [
            {
                "id": "full-vm-id-xxxxxxxxx",
                "name": "full-vm",
                "status": "running",
                "pid": 1234,
                "exit_code": None,
                "ipv4": "10.0.0.2",
                "mac": "02:FC:00:00:00:01",
                "network_id": "default",
                "network_name": None,
                "tap_device": "mvm-default-tap0",
            }
        ]
        result = runner.invoke(app, ["vm", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data[0]["name"] == "full-vm"
        assert data[0]["ipv4"] == "10.0.0.2"
        assert data[0]["exit_code"] is None


class TestVMCreateEdgeCases:
    """Tests for 'vm create' edge cases."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_create_with_skip_cleanup(self, mock_vm_op):
        mock_vm_op.create.return_value = OperationResult(
            status="success", code="vm.created", message="VM created"
        )
        result = runner.invoke(
            app,
            [
                "vm",
                "create",
                "--name",
                "testvm",
                "--image",
                "ubuntu-24.04",
                "--skip-cleanup",
            ],
            input="y\n",
        )
        assert result.exit_code == 0

    @patch("mvmctl.cli.vm.VMOperation")
    def test_create_with_ssh_keys(self, mock_vm_op):
        mock_vm_op.create.return_value = OperationResult(
            status="success", code="vm.created", message="VM created"
        )
        result = runner.invoke(
            app,
            [
                "vm",
                "create",
                "--name",
                "testvm",
                "--image",
                "ubuntu-24.04",
                "--ssh-key",
                "mykey,pubkey",
            ],
        )
        assert result.exit_code == 0

    @patch("mvmctl.cli.vm.VMOperation")
    def test_create_with_all_options(self, mock_vm_op):
        mock_vm_op.create.return_value = OperationResult(
            status="success", code="vm.created", message="VM created"
        )
        result = runner.invoke(
            app,
            [
                "vm",
                "create",
                "--name",
                "fullvm",
                "--image",
                "ubuntu-24.04",
                "--kernel",
                "krn1",
                "--vcpus",
                "4",
                "--mem",
                "1024",
                "--disk-size",
                "10G",
                "--ip",
                "10.0.0.42",
                "--network",
                "mynet",
                "--mac",
                "02:FC:00:00:00:02",
                "--user",
                "admin",
                "--no-enable-pci",
                "--enable-logging",
                "--no-enable-metrics",
                "--no-console",
            ],
        )
        assert result.exit_code == 0


class TestVMStopEdgeCases:
    """Tests for 'vm stop' edge cases."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_stop_with_ip_flag(self, mock_vm_op):
        mock_vm_op.stop.return_value = OperationResult(
            status="success", code="vm.stopped", message="VM stopped"
        )
        result = runner.invoke(
            app,
            ["vm", "stop", "10.0.0.5"],
        )
        assert result.exit_code == 0

    @patch("mvmctl.cli.vm.VMOperation")
    def test_stop_with_mac_flag(self, mock_vm_op):
        mock_vm_op.stop.return_value = OperationResult(
            status="success", code="vm.stopped", message="VM stopped"
        )
        result = runner.invoke(
            app,
            ["vm", "stop", "02:FC:00:00:00:01"],
        )
        assert result.exit_code == 0


class TestVMRebootEdgeCases:
    """Tests for 'vm reboot' edge cases."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_reboot_with_ip_flag(self, mock_vm_op):
        mock_vm_op.reboot.return_value = OperationResult(
            status="success", code="vm.rebooted", message="VM rebooted"
        )
        result = runner.invoke(
            app,
            ["vm", "reboot", "10.0.0.5"],
        )
        assert result.exit_code == 0


class TestVMPauseEdgeCases:
    """Tests for 'vm pause' edge cases."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_pause_with_ip_flag(self, mock_vm_op):
        mock_vm_op.pause.return_value = OperationResult(
            status="success", code="vm.paused", message="VM paused"
        )
        result = runner.invoke(
            app,
            ["vm", "pause", "10.0.0.5"],
        )
        assert result.exit_code == 0


class TestVMResumeEdgeCases:
    """Tests for 'vm resume' edge cases."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_resume_with_ip_flag(self, mock_vm_op):
        mock_vm_op.resume.return_value = OperationResult(
            status="success", code="vm.resumed", message="VM resumed"
        )
        result = runner.invoke(
            app,
            ["vm", "resume", "10.0.0.5"],
        )
        assert result.exit_code == 0


class TestVMSnapshotEdgeCases:
    """Tests for 'vm snapshot' edge cases."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_snapshot_with_ip_flag(self, mock_vm_op, tmp_path):
        mock_vm_op.snapshot.return_value = OperationResult(
            status="success",
            code="vm.snapshot_created",
            message="Snapshot created",
        )
        mem_file = tmp_path / "mem.snap"
        state_file = tmp_path / "state.snap"
        result = runner.invoke(
            app,
            [
                "vm",
                "snapshot",
                "10.0.0.5",
                str(mem_file),
                str(state_file),
            ],
        )
        assert result.exit_code == 0


class TestVMLoadEdgeCases:
    """Tests for 'vm load' edge cases."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_load_with_ip_flag(self, mock_vm_op, tmp_path):
        mock_vm_op.load_snapshot.return_value = OperationResult(
            status="success",
            code="vm.snapshot_loaded",
            message="Snapshot loaded",
        )
        mem_file = tmp_path / "mem.snap"
        state_file = tmp_path / "state.snap"
        result = runner.invoke(
            app,
            [
                "vm",
                "load",
                "10.0.0.5",
                str(mem_file),
                str(state_file),
            ],
        )
        assert result.exit_code == 0


class TestVMInspectTree:
    """Tests for 'vm inspect --tree' format."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_inspect_tree(self, mock_vm_op):
        mock_vm_op.inspect.return_value = {
            "vm": {
                "name": "myvm",
                "id": "abc-id",
                "status": "running",
                "pid": 1234,
                "exit_code": None,
            },
            "resources": {"vcpus": 2, "mem": 512, "disk": 2048},
            "networking": {
                "ipv4": "10.0.0.2",
                "mac": "02:FC:00:00:00:01",
                "network_name": "default",
                "tap_device": "mvm-default-tap0",
            },
            "assets": {
                "image_name": "Ubuntu 24.04",
                "kernel_version": "6.1.0",
                "binary_name": "firecracker",
            },
            "filesystem": {
                "vm_dir": "/tmp/vm",
                "rootfs_path": "/tmp/vm/rootfs.ext4",
                "config_path": "/tmp/vm/vm.json",
                "log_path": "/tmp/vm/fc.log",
                "serial_output_path": "/tmp/vm/serial.log",
            },
            "console": {
                "relay_running": False,
                "relay_pid": None,
                "relay_socket_path": None,
            },
        }
        result = runner.invoke(app, ["vm", "inspect", "myvm", "--tree"])
        assert result.exit_code == 0
        assert "Resources" in result.output
        assert "Networking" in result.output

    @patch("mvmctl.cli.vm.VMOperation")
    def test_inspect_tree_with_nulls(self, mock_vm_op):
        mock_vm_op.inspect.return_value = {
            "vm": {
                "name": "nullvm",
                "id": "null-id",
                "status": "stopped",
                "pid": None,
                "exit_code": 0,
            },
            "resources": {"vcpus": 1, "mem": 256, "disk": 1024},
            "networking": {
                "ipv4": None,
                "mac": None,
                "network_name": None,
                "tap_device": None,
            },
            "assets": {
                "image_name": None,
                "kernel_version": None,
                "binary_name": None,
            },
            "filesystem": {
                "vm_dir": "/tmp/vm",
                "rootfs_path": "/tmp/vm/rootfs.ext4",
                "config_path": None,
                "log_path": None,
                "serial_output_path": None,
            },
            "console": {
                "relay_running": False,
                "relay_pid": None,
                "relay_socket_path": None,
            },
        }
        result = runner.invoke(app, ["vm", "inspect", "nullvm", "--tree"])
        assert result.exit_code == 0
        assert "nullvm" in result.output


class TestVMExportEdgeCases:
    """Tests for 'vm export' edge cases."""

    @patch("mvmctl.cli.vm.VMOperation")
    def test_export_stdout(self, mock_vm_op):
        mock_config = MagicMock()
        mock_config.to_dict.return_value = {
            "name": "myvm",
            "schema_version": "1.0",
        }
        mock_vm_op.export.return_value = mock_config
        result = runner.invoke(app, ["vm", "export", "myvm"])
        assert result.exit_code == 0
        assert "myvm" in result.output

    @patch("mvmctl.cli.vm.VMOperation")
    def test_export_api_error(self, mock_vm_op):
        mock_vm_op.export.side_effect = MVMError("Export failed")
        result = runner.invoke(app, ["vm", "export", "myvm"])
        assert result.exit_code == 1


class TestVMImportEdgeCases:
    """Tests for 'vm import' edge cases."""

    def test_import_needs_interaction(self, mocker, tmp_path):
        mocker.patch(
            "mvmctl.cli.vm.VMOperation.import_",
            return_value=NeedsInteraction(
                code="privilege.required",
                message="Need privileges",
                input_type="sudo",
            ),
        )
        config = tmp_path / "config.json"
        config.write_text('{"name": "myvm"}')
        result = runner.invoke(app, ["vm", "import", str(config)])
        assert result.exit_code == 1
        assert "privileges required" in result.output.lower()

    @patch("mvmctl.cli.vm.VMOperation")
    def test_import_error_status(self, mock_vm_op, tmp_path):
        mock_vm_op.import_.return_value = OperationResult(
            status="error", code="vm.import.error", message="Import failed"
        )
        config = tmp_path / "config.json"
        config.write_text('{"name": "myvm"}')
        result = runner.invoke(app, ["vm", "import", str(config)])
        assert result.exit_code == 1
        assert "Import failed" in result.output

    @patch("mvmctl.cli.vm.VMOperation")
    def test_import_failure_status(self, mock_vm_op, tmp_path):
        mock_vm_op.import_.return_value = OperationResult(
            status="failure", code="vm.import.failure", message="Import failed"
        )
        config = tmp_path / "config.json"
        config.write_text('{"name": "myvm"}')
        result = runner.invoke(app, ["vm", "import", str(config)])
        assert result.exit_code == 1
