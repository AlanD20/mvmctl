"""Tests for main CLI entry point."""

from typer.testing import CliRunner

from fcm.main import app

runner = CliRunner()


def test_help():
    """Test --help exits 0 and shows app name."""
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "Firecracker Manager" in result.output


def test_vm_subcommand_registered():
    """Test 'vm --help' is reachable."""
    result = runner.invoke(app, ["vm", "--help"])
    assert result.exit_code == 0


def test_image_subcommand_registered():
    """Test 'image --help' is reachable."""
    result = runner.invoke(app, ["image", "--help"])
    assert result.exit_code == 0


def test_kernel_subcommand_registered():
    """Test 'kernel --help' is reachable."""
    result = runner.invoke(app, ["kernel", "--help"])
    assert result.exit_code == 0


def test_config_subcommand_registered():
    """Test 'config --help' is reachable."""
    result = runner.invoke(app, ["config", "--help"])
    assert result.exit_code == 0


def test_verbose_flag():
    """Test --verbose flag is accepted."""
    result = runner.invoke(app, ["--verbose", "--help"])
    assert result.exit_code == 0


def test_debug_flag():
    """Test --debug flag is accepted."""
    result = runner.invoke(app, ["--debug", "--help"])
    assert result.exit_code == 0
