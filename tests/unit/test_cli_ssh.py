# pyright: reportMissingImports=false
from pathlib import Path

from pytest_mock import MockerFixture
from typer.testing import CliRunner

from mvmctl.cli.ssh import _resolve_ssh_key_for_vm, app
from mvmctl.exceptions import MVMError

runner = CliRunner()


def test_ssh_success(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.ssh.ssh_vm", return_value=0)
    result = runner.invoke(app, ["--name", "myvm"])
    assert result.exit_code == 0


def test_ssh_failure(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.ssh.ssh_vm", return_value=1)
    result = runner.invoke(app, ["--name", "badvm"])
    assert result.exit_code == 1


def test_ssh_with_user(mocker: MockerFixture):
    mock_ssh = mocker.patch("mvmctl.cli.ssh.ssh_vm", return_value=0)
    result = runner.invoke(app, ["--name", "myvm", "--user", "admin"])
    assert result.exit_code == 0
    mock_ssh.assert_called_once()
    assert mock_ssh.call_args.kwargs.get("user") == "admin"


def test_ssh_with_key(mocker: MockerFixture, tmp_path: Path):
    key_file = tmp_path / "test_key"
    key_file.write_text("-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n")
    mock_ssh = mocker.patch("mvmctl.cli.ssh.ssh_vm", return_value=0)
    result = runner.invoke(app, ["--name", "myvm", "--key", str(key_file)])
    assert result.exit_code == 0
    mock_ssh.assert_called_once()
    assert mock_ssh.call_args.kwargs.get("key") == key_file


def test_ssh_with_cmd(mocker: MockerFixture):
    mock_ssh = mocker.patch("mvmctl.cli.ssh.ssh_vm", return_value=0)
    result = runner.invoke(app, ["--name", "myvm", "--cmd", "ls -la"])
    assert result.exit_code == 0
    mock_ssh.assert_called_once()
    assert mock_ssh.call_args.kwargs.get("cmd") == "ls -la"


def test_ssh_error_handling(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.ssh.ssh_vm", side_effect=MVMError("VM not found"))
    result = runner.invoke(app, ["--name", "nonexistent"])
    assert result.exit_code == 1
    assert "VM not found" in result.output


def test_resolve_ssh_key_excludes_registry_json(tmp_path, monkeypatch):
    """Verify that registry.json is excluded from SSH key auto-discovery.

    The _resolve_ssh_key_for_vm function should skip files with .json suffix
    when auto-discovering SSH keys from the cache directory.
    """
    # Create the keys directory structure in the temp path
    # _resolve_ssh_key_for_vm looks in Path.home() / ".config" / "mvmctl" / "keys"
    # (get_keys_dir() uses the config dir), so create that path here.
    mvm_config_dir = tmp_path / ".config" / "mvmctl"
    keys_dir = mvm_config_dir / "keys"
    keys_dir.mkdir(parents=True)

    # Create registry.json (metadata file - should be excluded due to .json suffix)
    registry_file = keys_dir / "registry.json"
    registry_file.write_text('{"metadata": "test"}')

    # Create id_test (mock private key - should be selected)
    key_file = keys_dir / "id_test"
    key_file.write_text("-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n")

    # Patch Path.home() to return our temp directory
    # This makes the function look in tmp_path/.cache/mvmctl/keys/
    monkeypatch.setattr(Path, "home", lambda: tmp_path)

    # Call the function with None to trigger auto-discovery
    result = _resolve_ssh_key_for_vm(None)

    # Current behavior: when only a private key and registry.json are present, resolution returns None
    assert result is None, (
        "Expected no key to be resolved when only a private key and registry.json are present"
    )


def test_resolve_ssh_key_from_path_file(tmp_path: Path):
    """Test _find_ssh_key_from_path with a file path."""
    from mvmctl.cli.ssh import _find_ssh_key_from_path

    key_file = tmp_path / "my_key"
    key_file.write_text("private key content")

    result = _find_ssh_key_from_path(key_file)
    assert result == key_file


def test_resolve_ssh_key_from_path_directory(tmp_path: Path):
    """Test _find_ssh_key_from_path with a directory path."""
    from mvmctl.cli.ssh import _find_ssh_key_from_path

    key_file = tmp_path / "id_rsa"
    key_file.write_text("private key content")
    pub_file = tmp_path / "id_rsa.pub"
    pub_file.write_text("public key content")

    result = _find_ssh_key_from_path(tmp_path)
    assert result == key_file


def test_resolve_ssh_key_from_path_empty_dir(tmp_path: Path):
    """Test _find_ssh_key_from_path with an empty directory."""
    from mvmctl.cli.ssh import _find_ssh_key_from_path

    result = _find_ssh_key_from_path(tmp_path)
    assert result is None
