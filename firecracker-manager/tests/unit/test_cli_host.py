"""Tests for CLI host commands."""

from unittest.mock import MagicMock

from pytest_mock import MockerFixture
from typer.testing import CliRunner

from fcm.cli.host import app
from fcm.core.host import HostChange, HostState
from fcm.exceptions import HostError

runner = CliRunner()


# ---------------------------------------------------------------------------
# host init
# ---------------------------------------------------------------------------


def test_init_success_with_changes(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mock_init = mocker.patch("fcm.cli.host.init_host")
    mock_init.return_value = [
        HostChange(
            setting="net.ipv4.ip_forward",
            original_value="0",
            applied_value="1",
            mechanism="sysctl",
        ),
    ]
    mocker.patch("fcm.api.network.ensure_default_network")
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "ip_forward" in result.output
    assert "1 change" in result.output


def test_init_success_multiple_changes(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mock_init = mocker.patch("fcm.cli.host.init_host")
    mock_init.return_value = [
        HostChange(
            setting="net.ipv4.ip_forward",
            original_value="0",
            applied_value="1",
            mechanism="sysctl",
        ),
        HostChange(
            setting="sysctl_persist_file",
            original_value=None,
            applied_value="/etc/sysctl.d/fc.conf",
            mechanism="file_create",
        ),
    ]
    mocker.patch("fcm.api.network.ensure_default_network")
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "ip_forward" in result.output
    assert "sysctl_persist_file" in result.output
    assert "2 change" in result.output


def test_init_no_changes(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("fcm.cli.host.init_host", return_value=[])
    mocker.patch("fcm.api.network.ensure_default_network")
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "already configured" in result.output


def test_init_host_error(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("fcm.cli.host.init_host", side_effect=HostError("/dev/kvm is not accessible"))
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 1
    assert "not accessible" in result.output


def test_init_host_error_missing_binaries(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "fcm.cli.host.init_host",
        side_effect=HostError("Missing required binaries: iptables"),
    )
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 1
    assert "Missing required binaries" in result.output


# ---------------------------------------------------------------------------
# host ls
# ---------------------------------------------------------------------------


def test_ls_all_ok(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("fcm.cli.host.check_kvm_access", return_value=True)
    mocker.patch("fcm.cli.host.check_required_binaries", return_value=[])
    mocker.patch("fcm.cli.host.get_ip_forward_status", return_value="1")
    mocker.patch(
        "fcm.cli.host.get_host_state",
        return_value=HostState(
            init_timestamp="2025-01-01T00:00:00+00:00",
            changes=[],
        ),
    )
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "ok" in result.output
    assert "accessible" in result.output
    assert "all found" in result.output


def test_ls_failures(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("fcm.cli.host.check_kvm_access", return_value=False)
    mocker.patch("fcm.cli.host.check_required_binaries", return_value=["iptables"])
    mocker.patch("fcm.cli.host.get_ip_forward_status", return_value="0")
    mocker.patch("fcm.cli.host.get_host_state", return_value=None)
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "FAIL" in result.output
    assert "iptables" in result.output
    assert "no snapshot" in result.output


def test_ls_ip_forward_error(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("fcm.cli.host.check_kvm_access", return_value=True)
    mocker.patch("fcm.cli.host.check_required_binaries", return_value=[])
    mocker.patch("fcm.cli.host.get_ip_forward_status", side_effect=HostError("sysctl not found"))
    mocker.patch("fcm.cli.host.get_host_state", return_value=None)
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "unknown" in result.output


def test_ls_state_exists_with_timestamp(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("fcm.cli.host.check_kvm_access", return_value=True)
    mocker.patch("fcm.cli.host.check_required_binaries", return_value=[])
    mocker.patch("fcm.cli.host.get_ip_forward_status", return_value="1")
    mocker.patch(
        "fcm.cli.host.get_host_state",
        return_value=HostState(
            init_timestamp="2025-06-15T10:30:00+00:00",
            changes=[
                HostChange("net.ipv4.ip_forward", "0", "1", "sysctl"),
            ],
        ),
    )
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "saved" in result.output
    assert "2025-06-15" in result.output


def test_ls_ip_forward_off(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("fcm.cli.host.check_kvm_access", return_value=True)
    mocker.patch("fcm.cli.host.check_required_binaries", return_value=[])
    mocker.patch("fcm.cli.host.get_ip_forward_status", return_value="0")
    mocker.patch("fcm.cli.host.get_host_state", return_value=None)
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "off" in result.output
    assert "value=0" in result.output


def test_ls_multiple_missing_binaries(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("fcm.cli.host.check_kvm_access", return_value=True)
    mocker.patch("fcm.cli.host.check_required_binaries", return_value=["ip", "iptables"])
    mocker.patch("fcm.cli.host.get_ip_forward_status", return_value="1")
    mocker.patch("fcm.cli.host.get_host_state", return_value=None)
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "ip" in result.output
    assert "iptables" in result.output


def test_ls_state_error_handled(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("fcm.cli.host.check_kvm_access", return_value=True)
    mocker.patch("fcm.cli.host.check_required_binaries", return_value=[])
    mocker.patch("fcm.cli.host.get_ip_forward_status", return_value="1")
    mocker.patch("fcm.cli.host.get_host_state", side_effect=HostError("Corrupt state file"))
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "none" in result.output


# ---------------------------------------------------------------------------
# host clean
# ---------------------------------------------------------------------------


def test_clean_success(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.core.vm_manager.VMManager.list_all", return_value=[])
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mock_clean = mocker.patch("fcm.cli.host.clean_host")
    mock_clean.return_value = ["Removed network 'default' (bridge: fcm-br0)"]
    result = runner.invoke(app, ["clean", "--force"])
    assert result.exit_code == 0
    assert "cleaned successfully" in result.output
    assert "Removed network" in result.output


def test_clean_refuses_running_vms(mocker: MockerFixture):
    from fcm.models.vm import VMState

    vm = MagicMock()
    vm.name = "myvm"
    vm.status = VMState.RUNNING
    mocker.patch("fcm.core.vm_manager.VMManager.list_all", return_value=[vm])
    result = runner.invoke(app, ["clean", "--force"])
    assert result.exit_code == 1
    assert "Cannot clean" in result.output


# ---------------------------------------------------------------------------
# host reset
# ---------------------------------------------------------------------------


def test_reset_success(mocker: MockerFixture, tmp_path):
    mocker.patch("fcm.core.vm_manager.VMManager.list_all", return_value=[])
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mock_reset = mocker.patch("fcm.cli.host.reset_host")
    mock_reset.return_value = [
        "Removed network 'default' (bridge: fcm-br0)",
        "Reverted net.ipv4.ip_forward",
        "Removed sudoers file /etc/sudoers.d/fcm",
        "Removed group 'fcm'",
    ]
    result = runner.invoke(app, ["reset", "--force"])
    assert result.exit_code == 0
    assert "reset successfully" in result.output


def test_reset_refuses_running_vms(mocker: MockerFixture):
    from fcm.models.vm import VMState

    vm = MagicMock()
    vm.name = "myvm"
    vm.status = VMState.RUNNING
    mocker.patch("fcm.core.vm_manager.VMManager.list_all", return_value=[vm])
    result = runner.invoke(app, ["reset", "--force"])
    assert result.exit_code == 1
    assert "Cannot reset" in result.output


def test_init_anti_recursion_protection(mocker: MockerFixture, tmp_path, monkeypatch):
    """Test that sudo restart has anti-recursion protection."""
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "fcm.cli.host.init_host",
        side_effect=HostError("Root privileges required"),
    )
    mock_subprocess = mocker.patch("subprocess.run")

    monkeypatch.setenv("FCM_SUDO_RESTART", "1")
    result = runner.invoke(app, ["init"], input="y\n")

    assert result.exit_code == 1
    assert "Recursive sudo restart detected" in result.output
    mock_subprocess.assert_not_called()


def test_init_sudo_restart_sets_env(mocker: MockerFixture, tmp_path):
    """Test that sudo restart sets FCM_SUDO_RESTART environment variable."""
    mocker.patch("fcm.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "fcm.cli.host.init_host",
        side_effect=HostError("Root privileges required"),
    )
    mock_subprocess = mocker.patch("subprocess.run")

    result = runner.invoke(app, ["init"], input="y\n")

    assert result.exit_code == 1
    mock_subprocess.assert_called_once()
    call_args = mock_subprocess.call_args
    assert "FCM_SUDO_RESTART" in call_args.kwargs.get("env", {})
