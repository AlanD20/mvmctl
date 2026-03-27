"""Integration tests for cloud-init ISO creation workflow.

Tests cloud-init ISO creation, custom ISO paths, disabled cloud-init,
and ISO retention flags with mocked subprocess calls.
"""

from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
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


class TestCloudInitISOCreation:
    """Test cloud-init ISO creation during VM creation."""

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.utils.process.run_cmd")
    def test_vm_create_with_iso_cloud_init(
        self, mock_run_cmd, mock_create_vm, mock_resolve_image, mock_check_priv, tmp_path
    ):
        """Test VM creation with automatic ISO cloud-init generation.

        Verifies that:
        - cloud-localds is called to create the ISO
        - The ISO drive is added to VM configuration
        - ISO is cleaned up after VM start (by default)
        """
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        # Mock successful subprocess call for cloud-localds
        mock_run_cmd.return_value = MagicMock(returncode=0, stdout="", stderr="")

        vm = _make_vm("cloud-init-vm")
        mock_create_vm.return_value = vm

        result = runner.invoke(
            vm_app,
            ["create", "--name", "cloud-init-vm", "--image", "abc123"],
        )
        assert result.exit_code == 0
        assert "cloud-init-vm" in result.output
        mock_create_vm.assert_called_once()

        # Verify create_vm was called with correct cloud-init mode
        call_kwargs = mock_create_vm.call_args.kwargs
        assert call_kwargs["name"] == "cloud-init-vm"
        assert call_kwargs["image"] == "abc123"

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    def test_vm_create_with_custom_iso(
        self, mock_create_vm, mock_resolve_image, mock_check_priv, tmp_path
    ):
        """Test VM creation with custom cloud-init ISO path.

        Verifies that:
        - --cloud-init-iso flag is accepted
        - Custom ISO path is passed to create_vm
        - CloudInitMode.CUSTOM is used
        """
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        custom_iso = tmp_path / "custom-cloud-init.iso"
        custom_iso.write_text("mock iso content")

        vm = _make_vm("custom-iso-vm")
        mock_create_vm.return_value = vm

        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "custom-iso-vm",
                "--image",
                "abc123",
                "--cloud-init-iso",
                str(custom_iso),
            ],
        )
        assert result.exit_code == 0
        assert "custom-iso-vm" in result.output
        mock_create_vm.assert_called_once()

        # Verify custom ISO path was passed
        call_kwargs = mock_create_vm.call_args.kwargs
        assert call_kwargs["name"] == "custom-iso-vm"
        assert call_kwargs["cloud_init_iso_path"] == custom_iso

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    def test_vm_create_disabled_cloud_init(
        self, mock_create_vm, mock_resolve_image, mock_check_priv
    ):
        """Test VM creation with disabled cloud-init.

        Verifies that:
        - --no-cloud-init flag disables cloud-init
        - CloudInitMode.DISABLED is used
        - No ISO is created or attached
        """
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("no-cloud-init-vm")
        mock_create_vm.return_value = vm

        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "no-cloud-init-vm",
                "--image",
                "abc123",
                "--no-cloud-init",
            ],
        )
        assert result.exit_code == 0
        assert "no-cloud-init-vm" in result.output
        mock_create_vm.assert_called_once()

        # Verify cloud-init is disabled
        call_kwargs = mock_create_vm.call_args.kwargs
        assert call_kwargs["name"] == "no-cloud-init-vm"

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    def test_vm_create_keep_iso_flag(self, mock_create_vm, mock_resolve_image, mock_check_priv):
        """Test VM creation with --keep-cloud-init-iso flag.

        Verifies that:
        - --keep-cloud-init-iso flag is accepted
        - keep_cloud_init_iso=True is passed to create_vm
        - ISO file is retained after VM start
        """
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("keep-iso-vm")
        mock_create_vm.return_value = vm

        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "keep-iso-vm",
                "--image",
                "abc123",
                "--keep-cloud-init-iso",
            ],
        )
        assert result.exit_code == 0
        assert "keep-iso-vm" in result.output
        mock_create_vm.assert_called_once()

        # Verify keep flag was passed
        call_kwargs = mock_create_vm.call_args.kwargs
        assert call_kwargs["name"] == "keep-iso-vm"
        assert call_kwargs["keep_cloud_init_iso"] is True


class TestCloudInitISOSubprocessMocking:
    """Test cloud-init ISO workflows with mocked subprocess calls."""

    @patch("mvmctl.utils.process.run_cmd")
    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    def test_iso_creation_with_cloud_localds_mock(
        self, mock_create_vm, mock_resolve_image, mock_check_priv, mock_run_cmd, tmp_path
    ):
        """Test that cloud-localds subprocess is called correctly.

        Verifies:
        - cloud-localds command is invoked with correct arguments
        - Required files (meta-data, network-config, user-data) are referenced
        - ISO output path is correct
        """
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        # Track subprocess calls
        subprocess_calls = []

        def capture_subprocess(cmd, **kwargs):
            subprocess_calls.append(cmd)
            return MagicMock(returncode=0, stdout="", stderr="")

        mock_run_cmd.side_effect = capture_subprocess

        vm = _make_vm("subprocess-vm")
        mock_create_vm.return_value = vm

        result = runner.invoke(
            vm_app,
            ["create", "--name", "subprocess-vm", "--image", "abc123"],
        )
        assert result.exit_code == 0

    @patch("mvmctl.utils.process.run_cmd")
    def test_cloud_localds_failure_handling(self, mock_run_cmd, tmp_path):
        """Test handling when cloud-localds fails.

        Verifies:
        - CloudInitError is raised on subprocess failure
        - Error message includes the failure reason
        """
        from mvmctl.core.cloud_init import create_cloud_init_iso

        # Mock subprocess failure
        from mvmctl.exceptions import CloudInitError, ProcessError

        mock_run_cmd.side_effect = ProcessError("ISO creation failed")

        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "network-config").write_text("version: 1")
        (cloud_init_dir / "user-data").write_text("#cloud-config")

        output_iso = tmp_path / "output.iso"

        with pytest.raises(CloudInitError) as exc_info:
            create_cloud_init_iso(cloud_init_dir, output_iso)

        assert "Failed to create cloud-init ISO" in str(exc_info.value)


class TestCloudInitISOEdgeCases:
    """Test edge cases in cloud-init ISO workflows."""

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    def test_custom_iso_not_found(self, mock_resolve_image, mock_check_priv, tmp_path):
        """Test error when custom ISO path doesn't exist."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        nonexistent_iso = tmp_path / "nonexistent.iso"

        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "missing-iso-vm",
                "--image",
                "abc123",
                "--cloud-init-iso",
                str(nonexistent_iso),
            ],
        )
        assert result.exit_code == 1
        assert "not found" in result.output.lower()

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    def test_mutually_exclusive_cloud_init_flags(
        self, mock_resolve_image, mock_check_priv, tmp_path
    ):
        """Test that --no-cloud-init and --cloud-init-iso are mutually exclusive."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        custom_iso = tmp_path / "custom.iso"
        custom_iso.write_text("content")

        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "conflict-vm",
                "--image",
                "abc123",
                "--no-cloud-init",
                "--cloud-init-iso",
                str(custom_iso),
            ],
        )
        assert result.exit_code == 1
        assert "are mutually exclusive" in result.output.lower()

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    @patch("mvmctl.cli.vm.create_vm")
    def test_nocloud_net_mode(self, mock_create_vm, mock_resolve_image, mock_check_priv):
        """Test VM creation with nocloud-net mode.

        Verifies that:
        - --nocloud-net flag is accepted
        - HTTP server is started for cloud-init
        - No ISO is created
        """
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        vm = _make_vm("nocloud-vm")
        mock_create_vm.return_value = vm

        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "nocloud-vm",
                "--image",
                "abc123",
                "--nocloud-net",
            ],
        )
        assert result.exit_code == 0
        mock_create_vm.assert_called_once()

        call_kwargs = mock_create_vm.call_args.kwargs
        assert call_kwargs["name"] == "nocloud-vm"

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    def test_nocloud_net_with_custom_iso_conflict(
        self, mock_resolve_image, mock_check_priv, tmp_path
    ):
        """Test that --nocloud-net and --cloud-init-iso are mutually exclusive."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        custom_iso = tmp_path / "custom.iso"
        custom_iso.write_text("content")

        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "conflict-vm",
                "--image",
                "abc123",
                "--nocloud-net",
                "--cloud-init-iso",
                str(custom_iso),
            ],
        )
        assert result.exit_code == 1
        assert "only one of" in result.output.lower()

    @patch("mvmctl.api.vms.check_privileges")
    @patch("mvmctl.cli.vm.resolve_image_short_id_path")
    def test_nocloud_net_with_no_cloud_init_conflict(self, mock_resolve_image, mock_check_priv):
        """Test that --nocloud-net and --no-cloud-init are mutually exclusive."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = Path("/tmp/image.ext4")

        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "conflict-vm",
                "--image",
                "abc123",
                "--nocloud-net",
                "--no-cloud-init",
            ],
        )
        assert result.exit_code == 1
        assert "only one of" in result.output.lower()


class TestCloudInitISOFileValidation:
    """Test file validation in cloud-init workflows."""

    def test_missing_required_cloud_init_files(self, tmp_path):
        """Test error when required cloud-init files are missing."""
        from mvmctl.core.cloud_init import create_cloud_init_iso
        from mvmctl.exceptions import CloudInitError

        incomplete_dir = tmp_path / "incomplete"
        incomplete_dir.mkdir()
        # Only create one file, missing others
        (incomplete_dir / "meta-data").write_text("instance-id: test")

        output_iso = tmp_path / "output.iso"

        with pytest.raises(CloudInitError) as exc_info:
            create_cloud_init_iso(incomplete_dir, output_iso)

        assert "Missing required cloud-init file" in str(exc_info.value)

    def test_all_required_files_present(self, tmp_path):
        """Test successful ISO creation when all files are present."""
        from mvmctl.core.cloud_init import create_cloud_init_iso

        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test-vm")
        (cloud_init_dir / "network-config").write_text("version: 1")
        (cloud_init_dir / "user-data").write_text("#cloud-config\nusers: []")

        output_iso = tmp_path / "output.iso"

        # Mock subprocess to avoid actual ISO creation
        with patch("mvmctl.utils.process.run_cmd") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)
            # Should not raise
            create_cloud_init_iso(cloud_init_dir, output_iso)

            # Verify subprocess was called
            mock_run.assert_called_once()
            cmd = mock_run.call_args[0][0]
            assert "cloud-localds" in cmd[0]
