"""Smoke tests — verify the CLI app assembles and responds to basic commands."""

from typer.testing import CliRunner

from fcm.main import app

runner = CliRunner()


def test_help_returns_zero() -> None:
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "Firecracker Manager" in result.output


def test_version_returns_zero() -> None:
    result = runner.invoke(app, ["--version"])
    assert result.exit_code == 0
    assert "fcm" in result.output


def test_unknown_command_returns_nonzero() -> None:
    result = runner.invoke(app, ["nonexistent-command"])
    assert result.exit_code != 0


def test_subcommand_help_vm() -> None:
    result = runner.invoke(app, ["vm", "--help"])
    assert result.exit_code == 0
    assert "create" in result.output


def test_subcommand_help_kernel() -> None:
    result = runner.invoke(app, ["kernel", "--help"])
    assert result.exit_code == 0
    assert "ls" in result.output


def test_subcommand_help_image() -> None:
    result = runner.invoke(app, ["image", "--help"])
    assert result.exit_code == 0
    assert "ls" in result.output


def test_subcommand_help_bin() -> None:
    result = runner.invoke(app, ["bin", "--help"])
    assert result.exit_code == 0
    assert "ls" in result.output


def test_subcommand_help_network() -> None:
    result = runner.invoke(app, ["network", "--help"])
    assert result.exit_code == 0
    assert "create" in result.output


def test_subcommand_help_key() -> None:
    result = runner.invoke(app, ["key", "--help"])
    assert result.exit_code == 0
    assert "create" in result.output


def test_subcommand_help_config() -> None:
    result = runner.invoke(app, ["config", "--help"])
    assert result.exit_code == 0
    assert "show" in result.output


def test_subcommand_help_host() -> None:
    result = runner.invoke(app, ["host", "--help"])
    assert result.exit_code == 0
    assert "init" in result.output


def test_subcommand_help_configure() -> None:
    result = runner.invoke(app, ["configure", "--help"])
    assert result.exit_code == 0
    assert "wizard" in result.output.lower() or "setup" in result.output.lower()
