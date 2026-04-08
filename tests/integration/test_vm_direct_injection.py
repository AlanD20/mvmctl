"""Integration tests for VM creation with direct injection mode."""

from unittest.mock import MagicMock, patch

from typer.testing import CliRunner

from mvmctl.cli.vm import vm_app as vm_app
from mvmctl.models.cloud_init import CloudInitMode


class TestVMDirectInjection:
    """Integration tests for VM creation with direct injection mode."""

    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.cli.vm._resolve_active_firecracker_bin")
    @patch("mvmctl.cli.vm.resolve_image_multi_strategy")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.core.rootfs_injector.check_libguestfs")
    @patch("mvmctl.core.rootfs_injector.inject_cloud_init")
    def test_vm_create_with_direct_injection(
        self,
        mock_inject,
        mock_check_guestfs,
        mock_create_vm,
        mock_resolve_image,
        mock_fc_bin,
        mock_check_priv,
        tmp_path,
    ):
        """Test VM creation with direct injection cloud-init mode."""
        mock_check_priv.return_value = None
        mock_fc_bin.return_value = "/usr/local/bin/firecracker"
        mock_resolve_image.return_value = tmp_path / "image.ext4"
        mock_check_guestfs.return_value = True
        mock_inject.return_value = None

        # Create mock VM response
        mock_vm = MagicMock()
        mock_vm.name = "direct-test-vm"
        mock_vm.cloud_init_mode = CloudInitMode.INJECT
        mock_create_vm.return_value = mock_vm

        runner = CliRunner()
        result = runner.invoke(
            vm_app,
            [
                "create",
                "--name",
                "direct-test-vm",
                "--image",
                "abc123",
                "--cloud-init-mode",
                "inject",
            ],
        )

        assert result.exit_code == 0

        # Verify create_vm was called with correct mode
        call_kwargs = mock_create_vm.call_args[1]
        assert call_kwargs["input"].cloud_init_mode == CloudInitMode.INJECT

    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.cli.vm._resolve_active_firecracker_bin")
    @patch("mvmctl.cli.vm.resolve_image_multi_strategy")
    @patch("mvmctl.cli.vm.create_vm")
    def test_vm_create_explicit_direct_mode(
        self,
        mock_create_vm,
        mock_resolve_image,
        mock_fc_bin,
        mock_check_priv,
        tmp_path,
    ):
        """Test that --cloud-init-mode direct is properly parsed."""
        mock_check_priv.return_value = None
        mock_fc_bin.return_value = "/usr/local/bin/firecracker"
        mock_resolve_image.return_value = tmp_path / "image.ext4"

        mock_vm = MagicMock()
        mock_vm.name = "test-vm"
        mock_vm.cloud_init_mode = CloudInitMode.INJECT
        mock_create_vm.return_value = mock_vm

        runner = CliRunner()
        result = runner.invoke(
            vm_app,
            ["create", "--name", "test-vm", "--image", "img123", "--cloud-init-mode", "inject"],
        )

        assert result.exit_code == 0
        assert mock_create_vm.called
        # Verify mode was passed correctly
        args, kwargs = mock_create_vm.call_args
        assert kwargs.get("input").cloud_init_mode == CloudInitMode.INJECT

    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.cli.vm._resolve_active_firecracker_bin")
    @patch("mvmctl.cli.vm.resolve_image_multi_strategy")
    def test_vm_create_invalid_mode_rejected(
        self,
        mock_resolve_image,
        mock_fc_bin,
        mock_check_priv,
        tmp_path,
    ):
        """Test that invalid cloud-init modes are rejected."""
        mock_check_priv.return_value = None
        mock_fc_bin.return_value = "/usr/local/bin/firecracker"
        mock_resolve_image.return_value = tmp_path / "image.ext4"

        runner = CliRunner()
        result = runner.invoke(
            vm_app,
            ["create", "--name", "test-vm", "--image", "img123", "--cloud-init-mode", "invalid"],
        )

        assert result.exit_code != 0
        assert "Invalid mode" in result.output or "invalid" in result.output.lower()

    @patch("mvmctl.api.host.check_privileges_interactive")
    @patch("mvmctl.cli.vm.resolve_image_multi_strategy")
    @patch("mvmctl.cli.vm.create_vm")
    @patch("mvmctl.core.rootfs_injector.check_libguestfs")
    def test_vm_create_fails_when_libguestfs_not_available(
        self,
        mock_check_guestfs,
        mock_create_vm,
        mock_resolve_image,
        mock_check_priv,
        tmp_path,
    ):
        """Test that VM creation fails gracefully when libguestfs is not available."""
        mock_check_priv.return_value = None
        mock_resolve_image.return_value = tmp_path / "image.ext4"
        mock_check_guestfs.return_value = False

        # Mock VM creation to raise exception due to missing libguestfs
        from mvmctl.exceptions import GuestfsNotAvailableError

        mock_create_vm.side_effect = GuestfsNotAvailableError(
            "libguestfs Python bindings not available"
        )

        runner = CliRunner()
        result = runner.invoke(
            vm_app,
            ["create", "--name", "test-vm", "--image", "img123", "--cloud-init-mode", "inject"],
        )

        assert result.exit_code != 0
