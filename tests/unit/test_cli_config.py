"""Tests for CLI config commands."""

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

from typer.testing import CliRunner

from mvmctl.cli.config import app
from mvmctl.models.config import SystemDefaultsConfig

runner = CliRunner()


def _default_config() -> SystemDefaultsConfig:
    from mvmctl.constants import (
        DEFAULT_NETWORK_NAME,
        DEFAULT_VM_BOOT_ARGS,
        DEFAULT_VM_DISK_SIZE,
        DEFAULT_VM_ENABLE_API_SOCKET,
        DEFAULT_VM_ENABLE_CONSOLE,
        DEFAULT_VM_ENABLE_LOGGING,
        DEFAULT_VM_ENABLE_METRICS,
        DEFAULT_VM_ENABLE_PCI,
        DEFAULT_VM_LSM_FLAGS,
        DEFAULT_VM_MEM_MIB,
        DEFAULT_VM_NETWORK_INTERFACE,
        DEFAULT_VM_SSH_USER,
        DEFAULT_VM_VCPU_COUNT,
    )

    return SystemDefaultsConfig(
        vcpu_count=DEFAULT_VM_VCPU_COUNT,
        mem_size_mib=DEFAULT_VM_MEM_MIB,
        ssh_user=DEFAULT_VM_SSH_USER,
        boot_args=DEFAULT_VM_BOOT_ARGS,
        disk_size=DEFAULT_VM_DISK_SIZE,
        enable_api_socket=DEFAULT_VM_ENABLE_API_SOCKET,
        enable_pci=DEFAULT_VM_ENABLE_PCI,
        lsm_flags=DEFAULT_VM_LSM_FLAGS,
        enable_logging=DEFAULT_VM_ENABLE_LOGGING,
        enable_metrics=DEFAULT_VM_ENABLE_METRICS,
        enable_console=DEFAULT_VM_ENABLE_CONSOLE,
        cloud_init_mode="inject",
        network_interface=DEFAULT_VM_NETWORK_INTERFACE,
        default_network_name=DEFAULT_NETWORK_NAME,
    )


def test_show_config():
    """Test 'config show' prints JSON config."""
    with patch("mvmctl.cli.config.load_config", return_value=_default_config()):
        with patch(
            "mvmctl.cli.config.dump_config",
            return_value={"firecracker": {"binary": "/usr/local/bin/firecracker"}},
        ):
            result = runner.invoke(app, ["show"])
            assert result.exit_code == 0
            data = json.loads(result.output)
            assert "firecracker" in data


def test_show_config_with_section():
    """Test 'config show --section' filters output."""
    with patch("mvmctl.cli.config.load_config", return_value=_default_config()):
        with patch(
            "mvmctl.cli.config.dump_config",
            return_value={"vm_defaults": {"vcpu_count": 2}},
        ) as mock_dump:
            result = runner.invoke(app, ["show", "--section", "vm_defaults"])
            assert result.exit_code == 0
            mock_dump.assert_called_once_with(_default_config(), "vm_defaults")
            data = json.loads(result.output)
            assert "vm_defaults" in data


def test_show_config_error():
    """Test 'config show' exits 1 on load failure."""
    with patch("mvmctl.cli.config.load_config", side_effect=Exception("bad yaml")):
        result = runner.invoke(app, ["show"])
        assert result.exit_code == 1


def test_validate_config_valid():
    """Test 'config validate' exits 0 when valid."""
    with patch("mvmctl.cli.config.load_config", return_value=_default_config()):
        with patch("mvmctl.cli.config.validate_config", return_value=[]):
            result = runner.invoke(app, ["validate"])
            assert result.exit_code == 0


def test_validate_config_errors():
    """Test 'config validate' exits 1 with errors."""
    with patch("mvmctl.cli.config.load_config", return_value=_default_config()):
        with patch(
            "mvmctl.cli.config.validate_config",
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

    # Mock VM manager to return a VM with the expected ID
    mock_vm = MagicMock()
    mock_vm.id = "test-vm"
    mock_manager = MagicMock()
    mock_manager.get.return_value = mock_vm

    with patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=vm_dir):
        with patch("mvmctl.api.vms.get_vm_manager", return_value=mock_manager):
            result = runner.invoke(app, ["dump-vm", "--name", "test-vm"])
            assert result.exit_code == 0
            data = json.loads(result.output)
            assert data["boot-source"]["kernel_image_path"] == "/vmlinux"


def test_dump_vm_not_found(tmp_path: Path):
    with patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=tmp_path / "nonexistent"):
        result = runner.invoke(app, ["dump-vm", "--name", "ghost"])
        assert result.exit_code == 1


def test_config_set(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("MVM_CONFIG", str(tmp_path / "config.json"))
    result = runner.invoke(app, ["set", "network_interface", "wlo0"])
    assert result.exit_code == 0
    assert "wlo0" in result.output


def test_config_get_existing(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("MVM_CONFIG", str(tmp_path / "config.json"))
    runner.invoke(app, ["set", "network_interface", "eth0"])
    result = runner.invoke(app, ["get", "network_interface"])
    assert result.exit_code == 0
    assert "eth0" in result.output


def test_config_get_missing(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("MVM_CONFIG", str(tmp_path / "config.json"))
    result = runner.invoke(app, ["get", "nonexistent_key"])
    assert result.exit_code == 0
    assert "not set" in result.output


def test_config_callback_no_subcommand():
    result = runner.invoke(app, [])
    assert result.exit_code == 0
    assert "Usage" in result.output


def test_show_config_mvm_error():
    from mvmctl.exceptions import MVMError

    with patch("mvmctl.cli.config.load_config", side_effect=MVMError("bad config")):
        result = runner.invoke(app, ["show"])
        assert result.exit_code == 1


def test_validate_config_mvm_error():
    from mvmctl.exceptions import MVMError

    with patch("mvmctl.cli.config.load_config", side_effect=MVMError("load failed")):
        result = runner.invoke(app, ["validate"])
        assert result.exit_code == 1


def test_dump_vm_config_file_missing(tmp_path: Path):
    vm_dir = tmp_path / "myvm"
    vm_dir.mkdir()

    mock_vm = MagicMock()
    mock_vm.id = "abcdef1234567890"
    mock_manager = MagicMock()
    mock_manager.get.return_value = mock_vm

    with patch("mvmctl.api.vms.get_vm_manager", return_value=mock_manager):
        with patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=vm_dir):
            result = runner.invoke(app, ["dump-vm", "--name", "myvm"])
            assert result.exit_code == 1


def test_dump_vm_invalid_json(tmp_path: Path):
    vm_dir = tmp_path / "myvm"
    vm_dir.mkdir()
    from mvmctl.constants import DEFAULT_FC_CONFIG_FILENAME

    (vm_dir / DEFAULT_FC_CONFIG_FILENAME).write_text("{ not valid json !!!")

    mock_vm = MagicMock()
    mock_vm.id = "abcdef1234567890"
    mock_manager = MagicMock()
    mock_manager.get.return_value = mock_vm

    with patch("mvmctl.api.vms.get_vm_manager", return_value=mock_manager):
        with patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=vm_dir):
            result = runner.invoke(app, ["dump-vm", "--name", "myvm"])
            assert result.exit_code == 1


def test_config_set_error(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    with patch("mvmctl.cli.config.set_config_value", side_effect=ValueError("bad key")):
        result = runner.invoke(app, ["set", "bad_key", "val"])
        assert result.exit_code == 1
