"""Tests for CLI host commands."""

from unittest.mock import patch

from typer.testing import CliRunner

from fcm.cli.host import app
from fcm.core.host import HostChange, HostState
from fcm.exceptions import HostError

runner = CliRunner()


# ---------------------------------------------------------------------------
# host init
# ---------------------------------------------------------------------------


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.init_host")
def test_init_success_with_changes(mock_init, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_init.return_value = [
        HostChange(
            setting="net.ipv4.ip_forward",
            original_value="0",
            applied_value="1",
            mechanism="sysctl",
        ),
    ]
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "ip_forward" in result.output
    assert "1 change" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.init_host")
def test_init_success_multiple_changes(mock_init, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
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
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "ip_forward" in result.output
    assert "sysctl_persist_file" in result.output
    assert "2 change" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.init_host")
def test_init_no_changes(mock_init, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_init.return_value = []
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 0
    assert "already configured" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.init_host")
def test_init_host_error(mock_init, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_init.side_effect = HostError("/dev/kvm is not accessible")
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 1
    assert "not accessible" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.init_host")
def test_init_host_error_missing_binaries(mock_init, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_init.side_effect = HostError("Missing required binaries: iptables")
    result = runner.invoke(app, ["init"])
    assert result.exit_code == 1
    assert "Missing required binaries" in result.output


# ---------------------------------------------------------------------------
# host ls
# ---------------------------------------------------------------------------


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.get_host_state")
@patch("fcm.cli.host.get_ip_forward_status", return_value="1")
@patch("fcm.cli.host.check_required_binaries", return_value=[])
@patch("fcm.cli.host.check_kvm_access", return_value=True)
def test_ls_all_ok(mock_kvm, mock_bins, mock_fwd, mock_state, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_state.return_value = HostState(
        init_timestamp="2025-01-01T00:00:00+00:00",
        changes=[],
    )
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "ok" in result.output
    assert "accessible" in result.output
    assert "all found" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.get_host_state")
@patch("fcm.cli.host.get_ip_forward_status", return_value="0")
@patch("fcm.cli.host.check_required_binaries", return_value=["iptables"])
@patch("fcm.cli.host.check_kvm_access", return_value=False)
def test_ls_failures(mock_kvm, mock_bins, mock_fwd, mock_state, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_state.return_value = None
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "FAIL" in result.output
    assert "iptables" in result.output
    assert "no snapshot" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.get_host_state")
@patch("fcm.cli.host.get_ip_forward_status")
@patch("fcm.cli.host.check_required_binaries", return_value=[])
@patch("fcm.cli.host.check_kvm_access", return_value=True)
def test_ls_ip_forward_error(mock_kvm, mock_bins, mock_fwd, mock_state, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_fwd.side_effect = HostError("sysctl not found")
    mock_state.return_value = None
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "unknown" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.get_host_state")
@patch("fcm.cli.host.get_ip_forward_status", return_value="1")
@patch("fcm.cli.host.check_required_binaries", return_value=[])
@patch("fcm.cli.host.check_kvm_access", return_value=True)
def test_ls_state_exists_with_timestamp(
    mock_kvm, mock_bins, mock_fwd, mock_state, mock_cache, tmp_path
):
    mock_cache.return_value = tmp_path
    mock_state.return_value = HostState(
        init_timestamp="2025-06-15T10:30:00+00:00",
        changes=[
            HostChange("net.ipv4.ip_forward", "0", "1", "sysctl"),
        ],
    )
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "saved" in result.output
    assert "2025-06-15" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.get_host_state")
@patch("fcm.cli.host.get_ip_forward_status", return_value="0")
@patch("fcm.cli.host.check_required_binaries", return_value=[])
@patch("fcm.cli.host.check_kvm_access", return_value=True)
def test_ls_ip_forward_off(mock_kvm, mock_bins, mock_fwd, mock_state, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_state.return_value = None
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "off" in result.output
    assert "value=0" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.get_host_state")
@patch("fcm.cli.host.get_ip_forward_status", return_value="1")
@patch("fcm.cli.host.check_required_binaries", return_value=["ip", "iptables"])
@patch("fcm.cli.host.check_kvm_access", return_value=True)
def test_ls_multiple_missing_binaries(
    mock_kvm, mock_bins, mock_fwd, mock_state, mock_cache, tmp_path
):
    mock_cache.return_value = tmp_path
    mock_state.return_value = None
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "ip" in result.output
    assert "iptables" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.get_host_state")
@patch("fcm.cli.host.get_ip_forward_status", return_value="1")
@patch("fcm.cli.host.check_required_binaries", return_value=[])
@patch("fcm.cli.host.check_kvm_access", return_value=True)
def test_ls_state_error_handled(mock_kvm, mock_bins, mock_fwd, mock_state, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_state.side_effect = HostError("Corrupt state file")
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "none" in result.output


# ---------------------------------------------------------------------------
# host restore
# ---------------------------------------------------------------------------


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.restore_host")
def test_restore_success(mock_restore, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_restore.return_value = [
        HostChange(
            setting="net.ipv4.ip_forward",
            original_value="1",
            applied_value="0",
            mechanism="sysctl",
        ),
    ]
    result = runner.invoke(app, ["restore"])
    assert result.exit_code == 0
    assert "Reverted" in result.output
    assert "1 change" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.restore_host")
def test_restore_success_multiple_reverts(mock_restore, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_restore.return_value = [
        HostChange(
            setting="sysctl_persist_file",
            original_value="/etc/sysctl.d/fc.conf",
            applied_value="(removed)",
            mechanism="file_remove",
        ),
        HostChange(
            setting="net.ipv4.ip_forward",
            original_value="1",
            applied_value="0",
            mechanism="sysctl",
        ),
    ]
    result = runner.invoke(app, ["restore"])
    assert result.exit_code == 0
    assert "Reverted" in result.output
    assert "2 change" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.restore_host")
def test_restore_no_state(mock_restore, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_restore.side_effect = HostError("No saved host state to restore")
    result = runner.invoke(app, ["restore"])
    assert result.exit_code == 1
    assert "No saved host state" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.restore_host")
def test_restore_nothing_to_revert(mock_restore, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_restore.return_value = []
    result = runner.invoke(app, ["restore"])
    assert result.exit_code == 0
    assert "No changes to revert" in result.output


@patch("fcm.cli.host.get_cache_dir")
@patch("fcm.cli.host.restore_host")
def test_restore_host_error_generic(mock_restore, mock_cache, tmp_path):
    mock_cache.return_value = tmp_path
    mock_restore.side_effect = HostError("Failed to revert net.ipv4.ip_forward")
    result = runner.invoke(app, ["restore"])
    assert result.exit_code == 1
    assert "Failed to revert" in result.output
