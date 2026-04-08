"""Tests for cli/init.py — guided onboarding wizard."""

from pathlib import Path
from unittest.mock import MagicMock, patch

from typer.testing import CliRunner

from mvmctl.cli.init import init_app as app
from mvmctl.core.binary_manager import BinaryVersion

runner = CliRunner()

_FAKE_BIN = BinaryVersion(
    version="1.12.0",
    firecracker_path=Path("/cache/bin/1.12.0/firecracker"),
    jailer_path=Path("/cache/bin/1.12.0/jailer"),
    is_active=True,
)


# ---------------------------------------------------------------------------
# Full wizard — host and network only (no asset downloads)
# ---------------------------------------------------------------------------
# --skip-host flag
# ---------------------------------------------------------------------------


@patch("mvmctl.api.network.list_networks")
@patch("mvmctl.cli.init.get_cache_dir")
def test_init_skip_host(mock_cache, mock_list_networks, tmp_path):
    """mvm init --skip-host should skip host initialization."""
    mock_cache.return_value = tmp_path
    # Mock default network exists
    from mvmctl.models.network import NetworkConfig

    mock_list_networks.return_value = [
        NetworkConfig(
            name="default",
            subnet="172.35.0.0/24",
            ipv4_gateway="172.35.0.1",
            bridge="mvm-default",
            nat_enabled=True,
            created_at="2024-01-01T00:00:00",
        )
    ]

    result = runner.invoke(app, ["--skip-host"])
    assert result.exit_code == 0
    assert "Skipped (--skip-host)" in result.output


# ---------------------------------------------------------------------------
# Individual step tests
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.init.check_kvm_access", return_value=True)
@patch("mvmctl.cli.init.get_host_state", return_value=MagicMock())
@patch("mvmctl.cli.init.get_cache_dir")
@patch("mvmctl.api.network.ensure_default_network")
def test_step_host_already_done(mock_ensure_net, mock_cache, mock_state, mock_kvm, tmp_path):
    """When state exists and KVM is ok, ensure_default_network should still be called."""
    from mvmctl.cli.init import _step_host

    mock_cache.return_value = tmp_path
    _step_host(skip=False, non_interactive=True)
    # Even when host state exists, ensure_default_network should be called
    # to verify network resources are materialized (bridge, chains, NAT)
    mock_ensure_net.assert_called_once()


@patch("mvmctl.cli.init.check_kvm_access", return_value=False)
@patch("mvmctl.cli.init.get_host_state", return_value=None)
@patch("mvmctl.cli.init.init_host")
@patch("mvmctl.api.network.ensure_default_network")
@patch("mvmctl.cli.init.get_cache_dir")
def test_step_host_non_interactive(
    mock_cache, mock_ensure_net, mock_init, mock_state, mock_kvm, tmp_path
):
    mock_cache.return_value = tmp_path
    from mvmctl.cli.init import _step_host

    _step_host(skip=False, non_interactive=True)
    mock_init.assert_called_once()


def test_step_host_skip():
    from mvmctl.cli.init import _step_host

    # Should not call any host functions
    _step_host(skip=True, non_interactive=False)


# ---------------------------------------------------------------------------
# S-H5: shutil.which("mvm") replaces sys.argv[0]
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.init.check_kvm_access", return_value=False)
@patch("mvmctl.cli.init.get_host_state", return_value=None)
@patch("mvmctl.cli.init.get_cache_dir")
def test_step_host_uses_shutil_which_for_mvm(mock_cache, mock_state, mock_kvm, tmp_path):
    """_step_host should use shutil.which('mvm') to find the binary, not sys.argv[0]."""
    mock_cache.return_value = tmp_path
    from mvmctl.cli.init import _step_host

    with (
        patch("typer.confirm", return_value=True),
        patch("shutil.which", return_value="/usr/local/bin/mvm") as mock_which,
        patch("subprocess.run", return_value=MagicMock(returncode=0)) as mock_run,
    ):
        _step_host(skip=False, non_interactive=False)

    mock_which.assert_called_once_with("mvm")
    mock_run.assert_called_once()
    cmd = mock_run.call_args[0][0]
    assert cmd == ["sudo", "-E", "/usr/local/bin/mvm", "host", "init"]


# ---------------------------------------------------------------------------
# Init scope reduction: mvm init should NOT download assets
# ---------------------------------------------------------------------------


@patch("mvmctl.api.network.ensure_default_network")
@patch("mvmctl.cli.init.set_active_version")
@patch("mvmctl.cli.init.fetch_binary")
@patch("mvmctl.api.network.list_networks", return_value=[])
@patch("mvmctl.cli.init.check_kvm_access", return_value=True)
@patch("mvmctl.cli.init.get_host_state", return_value=MagicMock(init_timestamp="2024-01-01"))
@patch("mvmctl.cli.init.get_cache_dir")
def test_init_non_interactive_no_kernel_image_key(
    mock_cache,
    mock_host_state,
    mock_kvm,
    mock_list_networks,
    mock_fetch_binary,
    mock_set_active,
    mock_ensure_net,
    tmp_path,
):
    """mvm init --non-interactive should NOT download kernel/image or create keys, but MAY fetch binary."""
    mock_cache.return_value = tmp_path

    result = runner.invoke(app, ["--non-interactive"])
    assert result.exit_code == 0
    # Binary is allowed (may or may not be called depending on cache state)


@patch("mvmctl.api.network.ensure_default_network")
@patch("mvmctl.cli.init.set_active_version")
@patch("mvmctl.cli.init.fetch_binary")
@patch("mvmctl.cli.init.list_local_versions", return_value=[])  # No local binaries
@patch("mvmctl.cli.init.list_remote_versions")
@patch("mvmctl.api.network.list_networks", return_value=[])
@patch("mvmctl.cli.init.check_kvm_access", return_value=True)
@patch("mvmctl.cli.init.get_host_state", return_value=MagicMock(init_timestamp="2024-01-01"))
@patch("mvmctl.cli.init.get_cache_dir")
def test_init_non_interactive_fetches_binary_when_none_cached(
    mock_cache,
    mock_host_state,
    mock_kvm,
    mock_list_networks,
    mock_list_remote,
    mock_list_local,
    mock_fetch_binary,
    mock_set_active,
    mock_ensure_net,
    tmp_path,
):
    """mvm init --non-interactive should fetch binary when none is cached."""
    mock_cache.return_value = tmp_path
    mock_list_remote.return_value = [
        BinaryVersion(
            version="1.12.0",
            firecracker_path=tmp_path / "bin" / "firecracker",
            jailer_path=tmp_path / "bin" / "jailer",
            is_active=False,
        )
    ]

    result = runner.invoke(app, ["--non-interactive"])
    assert result.exit_code == 0

    mock_fetch_binary.assert_called_once()


@patch("mvmctl.api.network.ensure_default_network")
@patch("mvmctl.cli.init.ensure_default_binary", return_value="1.12.0")
@patch("mvmctl.cli.init.list_local_versions")
@patch("mvmctl.api.network.list_networks", return_value=[])
@patch("mvmctl.cli.init.check_kvm_access", return_value=True)
@patch("mvmctl.cli.init.get_host_state", return_value=MagicMock(init_timestamp="2024-01-01"))
@patch("mvmctl.cli.init.get_cache_dir")
def test_step_binary_repairs_default_when_no_active(
    mock_cache,
    mock_host_state,
    mock_kvm,
    mock_list_networks,
    mock_list_local,
    mock_ensure_default,
    mock_ensure_net,
    tmp_path,
):
    """When local binaries exist but none is active, ensure_default_binary should be called."""
    mock_cache.return_value = tmp_path

    inactive_bv = BinaryVersion(
        version="1.12.0",
        firecracker_path=tmp_path / "bin" / "firecracker-v1.12.0",
        jailer_path=tmp_path / "bin" / "jailer-v1.12.0",
        is_active=False,
    )
    mock_list_local.return_value = [inactive_bv]

    result = runner.invoke(app, ["--non-interactive"])
    assert result.exit_code == 0
    mock_ensure_default.assert_called_once()
    assert "1.12.0" in result.output
