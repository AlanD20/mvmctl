"""Tests for CLI config commands."""

import json
from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from fcm.cli.config import app
from fcm.core.config import FCMConfig

runner = CliRunner()


def _default_config() -> FCMConfig:
    """Return a default FCMConfig for mocking."""
    return FCMConfig()


def test_show_config():
    """Test 'config show' prints JSON config."""
    with patch("fcm.cli.config.load_config", return_value=_default_config()):
        with patch(
            "fcm.cli.config.dump_config",
            return_value={"firecracker": {"binary": "/usr/local/bin/firecracker"}},
        ):
            result = runner.invoke(app, ["show"])
            assert result.exit_code == 0
            data = json.loads(result.output)
            assert "firecracker" in data


def test_show_config_with_section():
    """Test 'config show --section' filters output."""
    with patch("fcm.cli.config.load_config", return_value=_default_config()):
        with patch(
            "fcm.cli.config.dump_config",
            return_value={"vm_defaults": {"vcpu_count": 2}},
        ) as mock_dump:
            result = runner.invoke(app, ["show", "--section", "vm_defaults"])
            assert result.exit_code == 0
            mock_dump.assert_called_once_with(_default_config(), "vm_defaults")
            data = json.loads(result.output)
            assert "vm_defaults" in data


def test_show_config_error():
    """Test 'config show' exits 1 on load failure."""
    with patch("fcm.cli.config.load_config", side_effect=Exception("bad yaml")):
        result = runner.invoke(app, ["show"])
        assert result.exit_code == 1


def test_validate_config_valid():
    """Test 'config validate' exits 0 when valid."""
    with patch("fcm.cli.config.load_config", return_value=_default_config()):
        with patch("fcm.cli.config.validate_config", return_value=[]):
            result = runner.invoke(app, ["validate"])
            assert result.exit_code == 0


def test_validate_config_errors():
    """Test 'config validate' exits 1 with errors."""
    with patch("fcm.cli.config.load_config", return_value=_default_config()):
        with patch(
            "fcm.cli.config.validate_config",
            return_value=["vcpu_count must be >= 1"],
        ):
            result = runner.invoke(app, ["validate"])
            assert result.exit_code == 1


def test_dump_vm_success(tmp_path: Path):
    """Test 'config dump-vm' prints firecracker.json for a VM."""
    vm_dir = tmp_path / "test-vm"
    vm_dir.mkdir()
    config_data = {"boot-source": {"kernel_image_path": "/vmlinux"}}
    (vm_dir / "firecracker.json").write_text(json.dumps(config_data))

    with patch("fcm.cli.config.get_vm_dir", return_value=vm_dir):
        result = runner.invoke(app, ["dump-vm", "--name", "test-vm"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["boot-source"]["kernel_image_path"] == "/vmlinux"


def test_dump_vm_not_found(tmp_path: Path):
    with patch("fcm.cli.config.get_vm_dir", return_value=tmp_path / "nonexistent"):
        result = runner.invoke(app, ["dump-vm", "--name", "ghost"])
        assert result.exit_code == 1


def test_config_set(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CONFIG", str(tmp_path / "config.yaml"))
    result = runner.invoke(app, ["set", "network_interface", "wlo0"])
    assert result.exit_code == 0
    assert "wlo0" in result.output


def test_config_get_existing(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CONFIG", str(tmp_path / "config.yaml"))
    runner.invoke(app, ["set", "network_interface", "eth0"])
    result = runner.invoke(app, ["get", "network_interface"])
    assert result.exit_code == 0
    assert "eth0" in result.output


def test_config_get_missing(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CONFIG", str(tmp_path / "config.yaml"))
    result = runner.invoke(app, ["get", "nonexistent_key"])
    assert result.exit_code == 0
    assert "not set" in result.output
