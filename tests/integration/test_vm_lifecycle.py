"""Integration tests for full VM lifecycle workflow.

Tests the complete VM lifecycle: create -> list -> ssh (mocked) -> snapshot -> remove
"""

import json
from datetime import datetime
from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from mvmctl.cli.vm import app as vm_app
from mvmctl.models.vm import VMInstance, VMState

runner = CliRunner()


def _make_vm(
    name: str,
    status: VMState = VMState.RUNNING,
    ip: str = "10.20.0.2",
    pid: int = 1234,
    network: str = "default",
) -> VMInstance:
    """Create a sample VMInstance for testing."""
    return VMInstance(
        name=name,
        ip=ip,
        mac="02:FC:aa:bb:cc:dd",
        pid=pid,
        status=status,
        created_at=datetime(2026, 1, 1, 12, 0, 0),
        network_name=network,
        socket_path=Path(f"/tmp/mvm/{name}.sock"),
    )


class TestVMLifecycleWorkflow:
    """Test complete VM lifecycle workflow end-to-end."""

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.cli.vm.list_vms")
    def test_create_and_list_vm(
        self, mock_list_vms, mock_create_vm, mock_resolve_image, mock_check_priv, tmp_path
    ):
        """Test creating a VM and then listing it."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("lifecycle-vm")
        mock_create_vm.return_value = vm
        mock_list_vms.return_value = [vm]

        result = runner.invoke(vm_app, ["create", "--name", "lifecycle-vm", "--image", "abc123"])
        assert result.exit_code == 0
        assert "lifecycle-vm" in result.output
        mock_create_vm.assert_called_once()

        result = runner.invoke(vm_app, ["ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) == 1
        assert data[0]["name"] == "lifecycle-vm"

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.cli.vm.ssh_vm")
    def test_create_and_ssh_vm(self, mock_ssh, mock_create_vm, mock_resolve_image, mock_check_priv):
        """Test creating a VM and then SSHing into it."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("ssh-test-vm", ip="10.20.0.5")
        mock_create_vm.return_value = vm
        mock_ssh.return_value = 0

        result = runner.invoke(vm_app, ["create", "--name", "ssh-test-vm", "--image", "abc123"])
        assert result.exit_code == 0

        result = runner.invoke(vm_app, ["ssh", "--name", "ssh-test-vm"])
        assert result.exit_code == 0
        mock_ssh.assert_called_once()

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.cli.vm.snapshot_vm")
    def test_create_snapshot_and_remove(
        self, mock_snapshot, mock_create_vm, mock_resolve_image, mock_check_priv, tmp_path
    ):
        """Test creating a VM, taking a snapshot, then removing it."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("snapshot-vm")
        mock_create_vm.return_value = vm
        mock_snapshot.return_value = None

        result = runner.invoke(vm_app, ["create", "--name", "snapshot-vm", "--image", "abc123"])
        assert result.exit_code == 0

        mem_path = tmp_path / "snapshot.mem"
        state_path = tmp_path / "snapshot.state"
        result = runner.invoke(
            vm_app,
            [
                "snapshot",
                "--name",
                "snapshot-vm",
                "--mem-out",
                str(mem_path),
                "--state-out",
                str(state_path),
            ],
        )
        assert result.exit_code == 0
        mock_snapshot.assert_called_once_with(
            name="snapshot-vm", mem_out=mem_path, state_out=state_path
        )

    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.api.vms.get_vm_manager")
    @patch("mvmctl.cli.vm.remove_vm")
    @patch("mvmctl.cli.vm.list_vms")
    def test_full_lifecycle_create_remove(
        self, mock_list, mock_remove, mock_manager, mock_create, mock_resolve_image
    ):
        """Test full lifecycle: create VM, verify it exists, then remove it."""
        mock_resolve_image.return_value = Path("/tmp/image.ext4")
        vm = _make_vm("full-lifecycle-vm")
        mock_create.return_value = vm
        mock_list.return_value = [vm]
        mock_manager.return_value.get_by_name.return_value = [vm]
        mock_manager.return_value.find_by_short_id.return_value = []

        result = runner.invoke(
            vm_app,
            ["create", "--name", "full-lifecycle-vm", "--image", "abc123"],
        )
        assert result.exit_code == 0
        assert "full-lifecycle-vm" in result.output

        result = runner.invoke(vm_app, ["ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert any(v["name"] == "full-lifecycle-vm" for v in data)

        result = runner.invoke(vm_app, ["rm", "--name", "full-lifecycle-vm", "--force"])
        assert result.exit_code == 0
        mock_remove.assert_called_once_with("full-lifecycle-vm")

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.cli.vm.get_logs")
    def test_create_and_check_logs(
        self, mock_logs, mock_create, mock_resolve_image, mock_check_priv
    ):
        """Test creating a VM and checking its logs."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("logs-vm")
        mock_create.return_value = vm
        mock_logs.return_value = ["Boot log line 1\n", "Boot log line 2\n"]

        result = runner.invoke(vm_app, ["create", "--name", "logs-vm", "--image", "abc123"])
        assert result.exit_code == 0

        result = runner.invoke(vm_app, ["logs", "--name", "logs-vm", "--type", "boot"])
        assert result.exit_code == 0
        assert "Boot log line 1" in result.output
        mock_logs.assert_called_once()

    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.cli.vm.snapshot_vm")
    @patch("mvmctl.cli.vm.load_snapshot")
    @patch("mvmctl.api.vms.get_vm_manager")
    @patch("mvmctl.cli.vm.remove_vm")
    def test_snapshot_restore_workflow(
        self,
        mock_remove,
        mock_manager,
        mock_load,
        mock_snapshot,
        mock_create,
        mock_resolve_image,
        tmp_path,
    ):
        """Test full snapshot workflow: create -> snapshot -> load -> remove."""
        mock_resolve_image.return_value = Path("/tmp/image.ext4")
        vm = _make_vm("restore-vm")
        mock_create.return_value = vm
        mock_snapshot.return_value = None
        mock_load.return_value = None
        mock_remove.return_value = None
        mock_manager.return_value.get_by_name.return_value = [vm]
        mock_manager.return_value.find_by_short_id.return_value = []

        result = runner.invoke(vm_app, ["create", "--name", "restore-vm", "--image", "abc123"])
        assert result.exit_code == 0

        mem_path = tmp_path / "vm.mem"
        state_path = tmp_path / "vm.state"
        result = runner.invoke(
            vm_app,
            [
                "snapshot",
                "--name",
                "restore-vm",
                "--mem-out",
                str(mem_path),
                "--state-out",
                str(state_path),
            ],
        )
        assert result.exit_code == 0

        result = runner.invoke(
            vm_app,
            [
                "load",
                "--name",
                "restore-vm",
                "--mem-in",
                str(mem_path),
                "--state-in",
                str(state_path),
            ],
        )
        assert result.exit_code == 0
        mock_load.assert_called_once()

        result = runner.invoke(vm_app, ["rm", "--name", "restore-vm", "--force"])
        assert result.exit_code == 0


class TestVMLifecycleEdgeCases:
    """Test edge cases in VM lifecycle workflows."""

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.api.vms.get_vm_manager")
    @patch("mvmctl.cli.vm.remove_vm")
    def test_remove_nonexistent_vm(self, mock_remove, mock_manager, mock_check_priv):
        """Test attempting to remove a VM that doesn't exist."""
        mock_check_priv.return_value = None
        mock_manager.return_value.get_by_name.return_value = []
        mock_manager.return_value.find_by_short_id.return_value = []

        result = runner.invoke(vm_app, ["rm", "--name", "missing-vm", "--force"])
        assert result.exit_code == 1
        assert "no vm found" in result.output.lower()

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    def test_create_duplicate_vm_name(self, mock_create, mock_resolve_image, mock_check_priv):
        """Test attempting to create a VM with a duplicate name."""
        from mvmctl.exceptions import MVMError

        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")
        mock_create.side_effect = MVMError("VM 'duplicate-vm' already exists")

        result = runner.invoke(vm_app, ["create", "--name", "duplicate-vm", "--image", "abc123"])
        assert result.exit_code == 1
        assert "already exists" in result.output.lower()

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.cli.vm.cleanup_vms")
    @patch("mvmctl.cli.vm.list_vms")
    def test_cleanup_workflow(
        self, mock_list, mock_cleanup, mock_create, mock_resolve_image, mock_check_priv
    ):
        """Test cleanup workflow for stopped VMs."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("cleanup-vm", status=VMState.STOPPED)
        mock_create.return_value = vm
        mock_list.return_value = [vm]
        mock_cleanup.return_value = [vm]

        result = runner.invoke(vm_app, ["create", "--name", "cleanup-vm", "--image", "abc123"])
        assert result.exit_code == 0

        result = runner.invoke(vm_app, ["prune", "--force"])
        assert result.exit_code == 0
        mock_cleanup.assert_called_once()
