import importlib
import sys

from click.testing import CliRunner

import mvmctl.main as main_module

runner = CliRunner()


def invoke_cli(args: list[str]):
    return runner.invoke(main_module.app, args)


def test_help():
    result = invoke_cli(["--help"])
    assert result.exit_code == 0
    assert "Firecracker Manager" in result.output


def test_vm_subcommand_registered():
    result = invoke_cli(["vm", "--help"])
    assert result.exit_code == 0


def test_asset_subcommands_registered():
    for subcmd in ("kernel", "image", "bin"):
        result = invoke_cli([subcmd, "--help"])
        assert result.exit_code == 0


def test_network_subcommand_registered():
    result = invoke_cli(["network", "--help"])
    assert result.exit_code == 0


def test_config_subcommand_registered():
    result = invoke_cli(["config", "--help"])
    assert result.exit_code == 0


def test_verbose_flag():
    result = invoke_cli(["--verbose", "--help"])
    assert result.exit_code == 0


def test_debug_flag():
    result = invoke_cli(["--debug", "--help"])
    assert result.exit_code == 0


def test_version_flag():
    result = invoke_cli(["--version"])
    assert result.exit_code == 0
    assert "mvm" in result.output


def test_version_command():
    result = invoke_cli(["version"])
    assert result.exit_code == 0
    assert "mvm" in result.output


def test_main_import_is_lazy(monkeypatch):
    monkeypatch.delitem(sys.modules, "mvmctl.cli.vm", raising=False)
    monkeypatch.delitem(sys.modules, "mvmctl.cli.asset", raising=False)
    monkeypatch.delitem(sys.modules, "mvmctl.cli.host", raising=False)

    importlib.reload(main_module)

    assert "mvmctl.cli.vm" not in sys.modules
    assert "mvmctl.cli.asset" not in sys.modules
    assert "mvmctl.cli.host" not in sys.modules


def test_root_help_does_not_import_cli_modules(monkeypatch):
    monkeypatch.delitem(sys.modules, "mvmctl.cli.vm", raising=False)
    monkeypatch.delitem(sys.modules, "mvmctl.cli.asset", raising=False)

    importlib.reload(main_module)
    result = invoke_cli(["--help"])

    assert result.exit_code == 0
    assert "mvmctl.cli.vm" not in sys.modules
    assert "mvmctl.cli.asset" not in sys.modules


def test_version_flag_does_not_import_cli_modules(monkeypatch):
    monkeypatch.delitem(sys.modules, "mvmctl.cli.vm", raising=False)
    monkeypatch.delitem(sys.modules, "mvmctl.cli.asset", raising=False)

    importlib.reload(main_module)
    result = invoke_cli(["--version"])

    assert result.exit_code == 0
    assert "mvmctl.cli.vm" not in sys.modules
    assert "mvmctl.cli.asset" not in sys.modules


def test_vm_help_imports_only_requested_module(monkeypatch):
    monkeypatch.delitem(sys.modules, "mvmctl.cli.vm", raising=False)
    monkeypatch.delitem(sys.modules, "mvmctl.cli.asset", raising=False)

    importlib.reload(main_module)
    result = invoke_cli(["vm", "--help"])

    assert result.exit_code == 0
    assert "mvmctl.cli.vm" in sys.modules
    assert "mvmctl.cli.asset" not in sys.modules
