"""Comprehensive unit tests for the CLI cache module.

This module tests the `mvm cache` command group including:
- cache init: Initialize all cache resources
- cache prune: Prune various cache resources (vm, network, image, kernel, guestfs, all)

Test patterns follow the conventions from test_cli_vm.py and test_cli_asset.py.
"""

from pytest_mock import MockerFixture
from typer.testing import CliRunner

from mvmctl.cli.cache import cache_app as app

runner = CliRunner()


# -----------------------------------------------------------------------------
# Cache Init Tests
# -----------------------------------------------------------------------------


def test_cache_init_command_success(mocker: MockerFixture):
    """Test mvm cache init command success path."""
    mock_init = mocker.patch(
        "mvmctl.cli.cache.cache_api.init_all",
        return_value={
            "vms": "/home/user/.cache/mvmctl/vms",
            "images": "/home/user/.cache/mvmctl/images",
            "kernels": "/home/user/.cache/mvmctl/kernels",
            "networks": "/home/user/.cache/mvmctl/networks",
            "bin": "/home/user/.cache/mvmctl/bin",
        },
    )
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "initialized" in result.output.lower()
    assert "vms:" in result.output
    assert "images:" in result.output
    assert "kernels:" in result.output
    mock_init.assert_called_once()


def test_cache_init_command_failure(mocker: MockerFixture):
    """Test mvm cache init command failure path."""
    mock_init = mocker.patch(
        "mvmctl.cli.cache.cache_api.init_all",
        side_effect=Exception("Permission denied"),
    )
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 1
    assert "failed" in result.output.lower()
    assert "permission denied" in result.output.lower()
    mock_init.assert_called_once()


# -----------------------------------------------------------------------------
# Cache Prune Subcommand Tests
# -----------------------------------------------------------------------------


def test_cache_prune_vm_subcommand(mocker: MockerFixture):
    """Test mvm cache prune vm subcommand."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_vms",
        return_value=["vm1", "vm2"],
    )
    result = runner.invoke(app, ["prune", "vm"])
    assert result.exit_code == 0
    assert "pruned" in result.output.lower()
    assert "vm1" in result.output
    assert "vm2" in result.output
    mock_prune.assert_called_once_with(False, False, False)


def test_cache_prune_network_subcommand(mocker: MockerFixture):
    """Test mvm cache prune network subcommand."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_networks",
        return_value=["network1", "network2"],
    )
    result = runner.invoke(app, ["prune", "network"])
    assert result.exit_code == 0
    assert "pruned" in result.output.lower()
    assert "network1" in result.output
    assert "network2" in result.output
    mock_prune.assert_called_once_with(False, False)


def test_cache_prune_image_subcommand(mocker: MockerFixture):
    """Test mvm cache prune image subcommand."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_images",
        return_value=["img1abc", "img2def"],
    )
    result = runner.invoke(app, ["prune", "image"])
    assert result.exit_code == 0
    assert "pruned" in result.output.lower()
    assert "img1abc" in result.output
    assert "img2def" in result.output
    mock_prune.assert_called_once_with(False, False)


def test_cache_prune_kernel_subcommand(mocker: MockerFixture):
    """Test mvm cache prune kernel subcommand."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_kernels",
        return_value=["kern123", "kern456"],
    )
    result = runner.invoke(app, ["prune", "kernel"])
    assert result.exit_code == 0
    assert "pruned" in result.output.lower()
    assert "kern123" in result.output
    assert "kern456" in result.output
    mock_prune.assert_called_once_with(False, False)


# -----------------------------------------------------------------------------
# Cache Prune Confirmation Tests
# -----------------------------------------------------------------------------
# Cache Prune Confirmation Tests
# -----------------------------------------------------------------------------


def test_cache_prune_all_confirmation_yes(mocker: MockerFixture):
    """Test prune all with 'y' confirmation."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_all",
        return_value={
            "vms": ["vm1", "vm2"],
            "networks": [],
            "images": ["img1"],
            "kernels": [],
        },
    )
    result = runner.invoke(app, ["prune", "all"], input="y\n")
    assert result.exit_code == 0
    assert "pruned" in result.output.lower()
    mock_prune.assert_called_once_with(False, False, False)


def test_cache_prune_all_confirmation_no(mocker: MockerFixture):
    """Test prune all with 'n' confirmation (abort)."""
    mock_prune = mocker.patch("mvmctl.cli.cache.cache_api.prune_all")
    result = runner.invoke(app, ["prune", "all"], input="n\n")
    assert result.exit_code == 0
    assert "aborted" in result.output.lower()
    mock_prune.assert_not_called()


def test_cache_prune_all_force_flag(mocker: MockerFixture):
    """Test prune all with --force flag (skip confirmation)."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_all",
        return_value={
            "vms": ["vm1"],
            "networks": ["net1"],
            "images": ["img1"],
            "kernels": ["kern1"],
        },
    )
    result = runner.invoke(app, ["prune", "all", "--force"])
    assert result.exit_code == 0
    assert "pruned" in result.output.lower()
    mock_prune.assert_called_once_with(False, False, False)


# -----------------------------------------------------------------------------
# Cache Prune Flag Tests
# -----------------------------------------------------------------------------


def test_cache_prune_include_stopped_flag(mocker: MockerFixture):
    """Test --include-stopped flag is passed to API."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_vms",
        return_value=["vm1"],
    )
    result = runner.invoke(app, ["prune", "vm", "--include-stopped"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(True, False, False)


def test_cache_prune_include_running_flag(mocker: MockerFixture):
    """Test --include-running flag is passed to API."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_vms",
        return_value=["running-vm"],
    )
    result = runner.invoke(app, ["prune", "vm", "--include-running"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(False, True, False)


# -----------------------------------------------------------------------------
# Cache Prune Error Handling Tests
# -----------------------------------------------------------------------------


def test_cache_prune_invalid_resource():
    """Test error on invalid resource type."""
    result = runner.invoke(app, ["prune", "invalid_resource"])
    assert result.exit_code == 1
    assert "unknown resource" in result.output.lower() or "invalid" in result.output.lower()


def test_cache_prune_no_args_shows_help():
    """Test that prune with no args shows error (resource is required)."""
    result = runner.invoke(app, ["prune"])
    # The CLI shows an error when no resource is specified
    assert result.exit_code == 1
    assert "no resource specified" in result.output.lower() or "error" in result.output.lower()


# -----------------------------------------------------------------------------
# Cache Prune Output Format Tests
# -----------------------------------------------------------------------------


def test_cache_prune_output_format(mocker: MockerFixture):
    """Verify output formatting for prune operations."""
    mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_vms",
        return_value=["vm1", "vm2", "vm3"],
    )
    result = runner.invoke(app, ["prune", "vm"])
    assert result.exit_code == 0
    # Should show count and list of items
    assert "3" in result.output
    assert "vm1" in result.output
    assert "vm2" in result.output
    assert "vm3" in result.output


def test_cache_prune_dry_run_flag(mocker: MockerFixture):
    """Test --dry-run option is passed to API."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_vms",
        return_value=["vm1"],
    )
    result = runner.invoke(app, ["prune", "vm", "--dry-run"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(False, False, True)


# -----------------------------------------------------------------------------
# Additional Edge Case Tests
# -----------------------------------------------------------------------------


def test_cache_prune_vm_no_vms_to_prune(mocker: MockerFixture):
    """Test prune vm when nothing to prune."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_vms",
        return_value=[],
    )
    result = runner.invoke(app, ["prune", "vm"])
    assert result.exit_code == 0
    assert "no vms" in result.output.lower() or "nothing" in result.output.lower()
    mock_prune.assert_called_once_with(False, False, False)


def test_cache_prune_network_no_networks(mocker: MockerFixture):
    """Test prune network when nothing to prune."""
    mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_networks",
        return_value=[],
    )
    result = runner.invoke(app, ["prune", "network"])
    assert result.exit_code == 0
    assert "no networks" in result.output.lower() or "nothing" in result.output.lower()


def test_cache_prune_image_no_images(mocker: MockerFixture):
    """Test prune image when nothing to prune."""
    mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_images",
        return_value=[],
    )
    result = runner.invoke(app, ["prune", "image"])
    assert result.exit_code == 0
    assert "no images" in result.output.lower() or "nothing" in result.output.lower()


def test_cache_prune_kernel_no_kernels(mocker: MockerFixture):
    """Test prune kernel when nothing to prune."""
    mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_kernels",
        return_value=[],
    )
    result = runner.invoke(app, ["prune", "kernel"])
    assert result.exit_code == 0
    assert "no kernels" in result.output.lower() or "nothing" in result.output.lower()


def test_cache_prune_all_with_flags(mocker: MockerFixture):
    """Test prune all with --include-stopped and --include-running flags."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_all",
        return_value={
            "vms": ["vm1", "vm2"],
            "networks": [],
            "images": [],
            "kernels": [],
        },
    )
    result = runner.invoke(
        app, ["prune", "all", "--include-stopped", "--include-running", "--force"]
    )
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(True, True, False)


def test_cache_prune_all_dry_run(mocker: MockerFixture):
    """Test prune all with --dry-run flag."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_all",
        return_value={
            "vms": ["vm1"],
            "networks": ["net1"],
            "images": [],
            "kernels": [],
        },
    )
    result = runner.invoke(app, ["prune", "all", "--dry-run", "--force"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(False, False, True)


def test_cache_prune_vm_failure(mocker: MockerFixture):
    """Test prune vm when API raises exception."""
    mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_vms",
        side_effect=Exception("Failed to prune"),
    )
    result = runner.invoke(app, ["prune", "vm"])
    assert result.exit_code == 1
    assert "failed" in result.output.lower()


def test_cache_prune_network_failure(mocker: MockerFixture):
    """Test prune network when API raises exception."""
    mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_networks",
        side_effect=Exception("Failed to prune networks"),
    )
    result = runner.invoke(app, ["prune", "network"])
    assert result.exit_code == 1
    assert "failed" in result.output.lower()


def test_cache_prune_all_failure(mocker: MockerFixture):
    """Test prune all when API raises exception."""
    mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_all",
        side_effect=Exception("Failed to prune all"),
    )
    result = runner.invoke(app, ["prune", "all", "--force"])
    assert result.exit_code == 1
    assert "failed" in result.output.lower()


def test_cache_prune_all_empty_result(mocker: MockerFixture):
    """Test prune all when nothing is pruned."""
    mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_all",
        return_value={
            "vms": [],
            "networks": [],
            "images": [],
            "kernels": [],
        },
    )
    result = runner.invoke(app, ["prune", "all", "--force"])
    assert result.exit_code == 0
    # Should complete without errors even if nothing was pruned


def test_cache_prune_using_all_flag(mocker: MockerFixture):
    """Test prune using --all flag instead of 'all' argument."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_all",
        return_value={
            "vms": ["vm1"],
            "networks": [],
            "images": [],
            "kernels": [],
        },
    )
    result = runner.invoke(app, ["prune", "--all", "--force"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(False, False, False)


def test_cache_prune_both_stopped_and_running_flags(mocker: MockerFixture):
    """Test prune vm with both --include-stopped and --include-running."""
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_vms",
        return_value=["vm1", "vm2", "vm3"],
    )
    result = runner.invoke(app, ["prune", "vm", "--include-stopped", "--include-running"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(True, True, False)


def test_cache_prune_vm_all_flag(mocker: MockerFixture):
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_vms",
        return_value=["vm1", "vm2"],
    )
    result = runner.invoke(app, ["prune", "vm", "--all"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(True, True, False)


def test_cache_prune_image_all_flag(mocker: MockerFixture):
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_images",
        return_value=["img1abc", "img2def"],
    )
    result = runner.invoke(app, ["prune", "image", "--all"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(False, True)


def test_cache_prune_network_all_flag(mocker: MockerFixture):
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_networks",
        return_value=["net1"],
    )
    result = runner.invoke(app, ["prune", "network", "--all"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(False, True)


def test_cache_prune_kernel_all_flag(mocker: MockerFixture):
    mock_prune = mocker.patch(
        "mvmctl.cli.cache.cache_api.prune_kernels",
        return_value=["kern1"],
    )
    result = runner.invoke(app, ["prune", "kernel", "--all"])
    assert result.exit_code == 0
    mock_prune.assert_called_once_with(False, True)
