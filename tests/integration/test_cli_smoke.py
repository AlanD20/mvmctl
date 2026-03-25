"""Smoke tests — verify the CLI app assembles and responds to basic commands."""

from click.testing import CliRunner

from mvmctl.main import app

runner = CliRunner()


def invoke_cli(args: list[str]):
    return runner.invoke(app, args)


def test_help_returns_zero() -> None:
    result = invoke_cli(["--help"])
    assert result.exit_code == 0
    assert "MicroVM Manager" in result.output


def test_version_returns_zero() -> None:
    result = invoke_cli(["--version"])
    assert result.exit_code == 0
    assert "mvm" in result.output


def test_unknown_command_returns_nonzero() -> None:
    result = invoke_cli(["nonexistent-command"])
    assert result.exit_code != 0


def test_subcommand_help_vm() -> None:
    result = invoke_cli(["vm", "--help"])
    assert result.exit_code == 0
    assert "create" in result.output


def test_subcommand_help_kernel() -> None:
    result = invoke_cli(["kernel", "--help"])
    assert result.exit_code == 0
    assert "ls" in result.output


def test_subcommand_help_image() -> None:
    result = invoke_cli(["image", "--help"])
    assert result.exit_code == 0
    assert "ls" in result.output


def test_subcommand_help_bin() -> None:
    result = invoke_cli(["bin", "--help"])
    assert result.exit_code == 0
    assert "ls" in result.output


def test_subcommand_help_network() -> None:
    result = invoke_cli(["network", "--help"])
    assert result.exit_code == 0
    assert "create" in result.output


def test_subcommand_help_key() -> None:
    result = invoke_cli(["key", "--help"])
    assert result.exit_code == 0
    assert "create" in result.output


def test_subcommand_help_config() -> None:
    result = invoke_cli(["config", "--help"])
    assert result.exit_code == 0
    assert "show" in result.output


def test_subcommand_help_host() -> None:
    result = invoke_cli(["host", "--help"])
    assert result.exit_code == 0
    assert "init" in result.output


def test_subcommand_help_configure() -> None:
    result = invoke_cli(["configure", "--help"])
    assert result.exit_code == 0
    assert (
        "onboarding" in result.output.lower()
        or "setup" in result.output.lower()
        or "wizard" in result.output.lower()
    )
