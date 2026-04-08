"""Tests for CLI host commands."""

from unittest.mock import MagicMock

from pytest_mock import MockerFixture
from typer.testing import CliRunner

from mvmctl.cli.host import host_app as app
from mvmctl.models.host import HostStateChange, HostState
from mvmctl.exceptions import HostError

runner = CliRunner()


# ---------------------------------------------------------------------------
# host init
# ---------------------------------------------------------------------------


def test_init_success_with_changes(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mock_init = mocker.patch("mvmctl.cli.host.init_host")
    mock_init.return_value = [
        HostStateChange(
            setting="net.ipv4.ip_forward",
            original_value="0",
            applied_value="1",
            mechanism="sysctl",
        ),
    ]
    mocker.patch("mvmctl.cli.host.restore_networks", return_value=[])
    mocker.patch("mvmctl.cli.host.ensure_default_network")
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "ip_forward" in result.output
    assert "1 change" in result.output


def test_init_success_multiple_changes(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mock_init = mocker.patch("mvmctl.cli.host.init_host")
    mock_init.return_value = [
        HostStateChange(
            setting="net.ipv4.ip_forward",
            original_value="0",
            applied_value="1",
            mechanism="sysctl",
        ),
        HostStateChange(
            setting="sysctl_persist_file",
            original_value=None,
            applied_value="/etc/sysctl.d/fc.conf",
            mechanism="file_create",
        ),
    ]
    mocker.patch("mvmctl.cli.host.restore_networks", return_value=[])
    mocker.patch("mvmctl.cli.host.ensure_default_network")
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "ip_forward" in result.output
    assert "sysctl_persist_file" in result.output
    assert "2 change" in result.output


def test_init_no_changes(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.init_host", return_value=[])
    mocker.patch("mvmctl.cli.host.restore_networks", return_value=[])
    mocker.patch("mvmctl.cli.host.ensure_default_network")
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "already configured" in result.output


def test_init_warns_when_chains_already_exist(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "mvmctl.cli.host.init_host",
        return_value=[
            HostStateChange(
                setting="iptables_chains",
                original_value=None,
                applied_value="MVM chains already exist",
                mechanism="noop",
            )
        ],
    )
    mocker.patch("mvmctl.cli.host.restore_networks", return_value=[])
    mocker.patch("mvmctl.cli.host.ensure_default_network")

    result = runner.invoke(app, ["init"])

    assert result.exit_code == 0
    assert "already exist" in result.output.lower()


def test_init_host_error(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.init_host", side_effect=HostError("/dev/kvm is not accessible"))
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 1
    assert "not accessible" in result.output


def test_init_host_error_missing_binaries(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "mvmctl.cli.host.init_host",
        side_effect=HostError("Missing required binaries: iptables"),
    )
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 1
    assert "Missing required binaries" in result.output


# ---------------------------------------------------------------------------
# host ls
# ---------------------------------------------------------------------------


def test_ls_all_ok(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.check_kvm_access", return_value=True)
    mocker.patch("mvmctl.cli.host.check_required_binaries", return_value=[])
    mocker.patch("mvmctl.cli.host.get_ip_forward_status", return_value="1")
    mocker.patch(
        "mvmctl.cli.host.get_host_state",
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
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.check_kvm_access", return_value=False)
    mocker.patch("mvmctl.cli.host.check_required_binaries", return_value=["iptables"])
    mocker.patch("mvmctl.cli.host.get_ip_forward_status", return_value="0")
    mocker.patch("mvmctl.cli.host.get_host_state", return_value=None)
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "FAIL" in result.output
    assert "iptables" in result.output
    assert "no snapshot" in result.output


def test_ls_ip_forward_error(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.check_kvm_access", return_value=True)
    mocker.patch("mvmctl.cli.host.check_required_binaries", return_value=[])
    mocker.patch("mvmctl.cli.host.get_ip_forward_status", side_effect=HostError("sysctl not found"))
    mocker.patch("mvmctl.cli.host.get_host_state", return_value=None)
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "unknown" in result.output


def test_ls_state_exists_with_timestamp(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.check_kvm_access", return_value=True)
    mocker.patch("mvmctl.cli.host.check_required_binaries", return_value=[])
    mocker.patch("mvmctl.cli.host.get_ip_forward_status", return_value="1")
    mocker.patch(
        "mvmctl.cli.host.get_host_state",
        return_value=HostState(
            init_timestamp="2025-06-15T10:30:00+00:00",
            changes=[
                HostStateChange("net.ipv4.ip_forward", "0", "1", "sysctl"),
            ],
        ),
    )
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "saved" in result.output
    assert "2025-06-15" in result.output


def test_ls_ip_forward_off(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.check_kvm_access", return_value=True)
    mocker.patch("mvmctl.cli.host.check_required_binaries", return_value=[])
    mocker.patch("mvmctl.cli.host.get_ip_forward_status", return_value="0")
    mocker.patch("mvmctl.cli.host.get_host_state", return_value=None)
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "off" in result.output
    assert "value=0" in result.output


def test_ls_multiple_missing_binaries(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.check_kvm_access", return_value=True)
    mocker.patch("mvmctl.cli.host.check_required_binaries", return_value=["ip", "iptables"])
    mocker.patch("mvmctl.cli.host.get_ip_forward_status", return_value="1")
    mocker.patch("mvmctl.cli.host.get_host_state", return_value=None)
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "ip" in result.output
    assert "iptables" in result.output


def test_ls_state_error_handled(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.check_kvm_access", return_value=True)
    mocker.patch("mvmctl.cli.host.check_required_binaries", return_value=[])
    mocker.patch("mvmctl.cli.host.get_ip_forward_status", return_value="1")
    mocker.patch("mvmctl.cli.host.get_host_state", side_effect=HostError("Corrupt state file"))
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "none" in result.output


# ---------------------------------------------------------------------------
# host clean
# ---------------------------------------------------------------------------


def test_clean_success(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[])
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mock_clean = mocker.patch("mvmctl.cli.host.clean_host")
    mock_clean.return_value = ["Removed network 'default' (bridge: mvm-br0)"]
    result = runner.invoke(app, ["clean", "--force"])
    assert result.exit_code == 0
    assert "cleaned successfully" in result.output
    assert "Removed network" in result.output


def test_clean_shows_warning_lines(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[])
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "mvmctl.cli.host.clean_host",
        return_value=["Warning: MVM Networking: failed to delete chain MVM-FORWARD"],
    )

    result = runner.invoke(app, ["clean", "--force"])

    assert result.exit_code == 0
    assert "Warning" in result.output
    assert "MVM Networking" in result.output


def test_clean_refuses_running_vms(mocker: MockerFixture):
    from mvmctl.models.vm import VMStatus

    vm = MagicMock()
    vm.name = "myvm"
    vm.status = VMStatus.RUNNING
    mocker.patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[vm])
    result = runner.invoke(app, ["clean", "--force"])
    assert result.exit_code == 1
    assert "Cannot clean" in result.output


# ---------------------------------------------------------------------------
# host reset
# ---------------------------------------------------------------------------


def test_reset_success(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[])
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mock_reset = mocker.patch("mvmctl.cli.host.reset_host")
    mock_reset.return_value = [
        "Removed network 'default' (bridge: mvm-br0)",
        "Reverted net.ipv4.ip_forward",
        "Removed sudoers file /etc/sudoers.d/mvm",
        "Removed group 'mvm'",
    ]
    result = runner.invoke(app, ["reset", "--force"])
    assert result.exit_code == 0
    assert "reset successfully" in result.output


def test_reset_shows_warning_lines(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[])
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "mvmctl.cli.host.reset_host",
        return_value=[
            "Warning: skipped legacy bridge cleanup 'mvm-br0' (already clean or insufficient privileges): denied"
        ],
    )

    result = runner.invoke(app, ["reset", "--force"])

    assert result.exit_code == 0
    assert "Warning" in result.output


def test_reset_refuses_running_vms(mocker: MockerFixture):
    from mvmctl.models.vm import VMStatus

    vm = MagicMock()
    vm.name = "myvm"
    vm.status = VMStatus.RUNNING
    mocker.patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[vm])
    result = runner.invoke(app, ["reset", "--force"])
    assert result.exit_code == 1
    assert "Cannot reset" in result.output


def test_init_anti_recursion_protection(mocker: MockerFixture, tmp_path, monkeypatch):
    """Test that sudo restart has anti-recursion protection."""
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "mvmctl.cli.host.init_host",
        side_effect=HostError("Root privileges required"),
    )
    mock_subprocess = mocker.patch("subprocess.run")

    monkeypatch.setenv("MVM_SUDO_RESTART", "1")
    result = runner.invoke(app, ["init"], input="y\n")

    assert result.exit_code == 1
    assert "Recursive sudo restart detected" in result.output
    mock_subprocess.assert_not_called()


def test_init_sudo_restart_sets_env(mocker: MockerFixture, tmp_path):
    """Test that sudo restart sets MVM_SUDO_RESTART environment variable."""
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "mvmctl.cli.host.init_host",
        side_effect=HostError("Root privileges required"),
    )
    mock_subprocess = mocker.patch("subprocess.run")

    result = runner.invoke(app, ["init"], input="y\n")

    assert result.exit_code == 1
    mock_subprocess.assert_called_once()
    call_args = mock_subprocess.call_args
    assert "MVM_SUDO_RESTART" in call_args.kwargs.get("env", {})
    assert "MVM_ESCALATED" in call_args.kwargs.get("env", {})


# ---------------------------------------------------------------------------
# _format_change — untested branches
# ---------------------------------------------------------------------------


def test_format_change_iptables_save():
    from mvmctl.cli.host import _format_change

    change = HostStateChange(
        setting="iptables_rules",
        original_value=None,
        applied_value="/etc/iptables/rules.v4",
        mechanism="iptables_save",
    )
    result = _format_change(change)
    assert "iptables rules saved" in result
    assert "/etc/iptables/rules.v4" in result


def test_format_change_usermod_with_colon():
    from mvmctl.cli.host import _format_change

    change = HostStateChange(
        setting="usermod",
        original_value=None,
        applied_value="alice:mvm",
        mechanism="usermod",
    )
    result = _format_change(change)
    assert "alice" in result
    assert "mvm" in result


def test_format_change_usermod_without_colon():
    from mvmctl.cli.host import _format_change

    change = HostStateChange(
        setting="usermod",
        original_value=None,
        applied_value="alice",
        mechanism="usermod",
    )
    result = _format_change(change)
    assert "alice" in result


def test_format_change_fallback_short_value():
    from mvmctl.cli.host import _format_change

    change = HostStateChange(
        setting="some_setting",
        original_value="old",
        applied_value="new",
        mechanism="unknown_mechanism",
    )
    result = _format_change(change)
    assert "some_setting" in result
    assert "new" in result


def test_format_change_fallback_long_original_value():
    from mvmctl.cli.host import _format_change

    long_value = "x" * 100
    change = HostStateChange(
        setting="some_setting",
        original_value=long_value,
        applied_value="new",
        mechanism="unknown_mechanism",
    )
    result = _format_change(change)
    assert "…" in result


# ---------------------------------------------------------------------------
# host init — extra branches
# ---------------------------------------------------------------------------


def test_init_restore_networks_returns_results(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.init_host", return_value=[])
    mocker.patch(
        "mvmctl.cli.host.restore_networks",
        return_value=["Restored network 'default'"],
    )
    mocker.patch("mvmctl.cli.host.ensure_default_network")
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "Restored network 'default'" in result.output


def test_init_network_error_is_warned(mocker: MockerFixture, tmp_path):
    from mvmctl.exceptions import NetworkError

    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.init_host", return_value=[])
    mocker.patch("mvmctl.cli.host.restore_networks", side_effect=NetworkError("bridge failed"))
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "Network setup skipped" in result.output


def test_init_sudo_file_not_found(mocker: MockerFixture, tmp_path):
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch(
        "mvmctl.cli.host.init_host",
        side_effect=HostError("Root privileges required"),
    )
    mocker.patch("subprocess.run", side_effect=FileNotFoundError("sudo not found"))
    result = runner.invoke(app, ["init"], input="y\n")
    assert result.exit_code == 1
    assert "sudo command not found" in result.output


# ---------------------------------------------------------------------------
# host clean — MVMError
# ---------------------------------------------------------------------------


def test_clean_mvm_error(mocker: MockerFixture, tmp_path):
    from mvmctl.exceptions import NetworkError

    mocker.patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[])
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.clean_host", side_effect=NetworkError("bridge not found"))
    result = runner.invoke(app, ["clean", "--force"])
    assert result.exit_code != 0


# ---------------------------------------------------------------------------
# host reset — MVMError
# ---------------------------------------------------------------------------


def test_reset_mvm_error(mocker: MockerFixture, tmp_path):
    from mvmctl.exceptions import HostError as HE

    mocker.patch("mvmctl.core.vm_manager.VMManager.list_all", return_value=[])
    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.reset_host", side_effect=HE("revert failed"))
    result = runner.invoke(app, ["reset", "--force"])
    assert result.exit_code != 0


# ---------------------------------------------------------------------------
# host ls — JSON output
# ---------------------------------------------------------------------------


def test_ls_json_output(mocker: MockerFixture, tmp_path):
    import json

    mocker.patch("mvmctl.cli.host.get_cache_dir", return_value=tmp_path)
    mocker.patch("mvmctl.cli.host.check_kvm_access", return_value=True)
    mocker.patch("mvmctl.cli.host.check_required_binaries", return_value=[])
    mocker.patch("mvmctl.cli.host.get_ip_forward_status", return_value="1")
    mocker.patch(
        "mvmctl.cli.host.get_host_state",
        return_value=HostState(
            init_timestamp="2025-01-01T00:00:00+00:00",
            changes=[],
        ),
    )
    result = runner.invoke(app, ["ls", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert data["kvm_accessible"] is True
    assert data["ip_forward"]["ok"] is True
    assert data["state_snapshot"]["exists"] is True


# ---------------------------------------------------------------------------
# host clean-ready-pool
# ---------------------------------------------------------------------------


def test_clean_ready_pool_empty(mocker: MockerFixture, tmp_path):
    empty_dir = tmp_path / "ready_pool"
    empty_dir.mkdir()
    mocker.patch("mvmctl.cli.host.get_ready_pool_dir", return_value=empty_dir)
    result = runner.invoke(app, ["clean-ready-pool"])
    assert result.exit_code == 0
    assert "already empty" in result.output


def test_clean_ready_pool_force_removes(mocker: MockerFixture, tmp_path):
    ready_dir = tmp_path / "ready_pool"
    ready_dir.mkdir()
    (ready_dir / "image.img").write_bytes(b"x")
    mocker.patch("mvmctl.cli.host.get_ready_pool_dir", return_value=ready_dir)
    mocker.patch("mvmctl.cli.host.clean_ready_pool", return_value=1)
    result = runner.invoke(app, ["clean-ready-pool", "--force"])
    assert result.exit_code == 0
    assert "removed 1 image" in result.output


def test_clean_ready_pool_nothing_removed(mocker: MockerFixture, tmp_path):
    ready_dir = tmp_path / "ready_pool"
    ready_dir.mkdir()
    (ready_dir / "image.img").write_bytes(b"x")
    mocker.patch("mvmctl.cli.host.get_ready_pool_dir", return_value=ready_dir)
    mocker.patch("mvmctl.cli.host.clean_ready_pool", return_value=0)
    result = runner.invoke(app, ["clean-ready-pool", "--force"])
    assert result.exit_code == 0
    assert "empty" in result.output


def test_help_cmd_invocation():
    result = runner.invoke(app, ["help"])
    assert result.exit_code == 0
