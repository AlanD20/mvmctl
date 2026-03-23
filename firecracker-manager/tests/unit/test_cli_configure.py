"""Tests for cli/configure.py — guided onboarding wizard."""

from pathlib import Path
from unittest.mock import MagicMock, patch

from typer.testing import CliRunner

from fcm.cli.configure import app
from fcm.core.binary_manager import BinaryVersion
from fcm.core.key_manager import KeyInfo

runner = CliRunner()

_FAKE_KEY = KeyInfo(
    name="fcm-default",
    fingerprint="SHA256:abc",
    algorithm="ssh-ed25519",
    comment="test@host",
    added_at="2024-01-01T00:00:00",
)

_FAKE_BIN = BinaryVersion(
    version="1.12.0",
    firecracker_path=Path("/cache/bin/1.12.0/firecracker"),
    jailer_path=Path("/cache/bin/1.12.0/jailer"),
    is_active=True,
)


# ---------------------------------------------------------------------------
# Full wizard — all components already present
# ---------------------------------------------------------------------------


@patch("fcm.cli.configure.list_keys", return_value=[_FAKE_KEY])
@patch("fcm.cli.configure.get_images_dir")
@patch("fcm.cli.configure.get_kernels_dir")
@patch("fcm.cli.configure.list_local_versions", return_value=[_FAKE_BIN])
@patch("fcm.cli.configure.check_kvm_access", return_value=True)
@patch("fcm.cli.configure.get_host_state", return_value=MagicMock(init_timestamp="2024-01-01"))
@patch("fcm.cli.configure.get_cache_dir")
def test_configure_all_ready(
    mock_cache,
    mock_host_state,
    mock_kvm,
    mock_bins,
    mock_kernels_dir,
    mock_images_dir,
    mock_keys,
    tmp_path,
):
    mock_cache.return_value = tmp_path

    kdir = tmp_path / "kernels"
    kdir.mkdir()
    (kdir / "vmlinux").write_text("kernel")
    mock_kernels_dir.return_value = kdir

    idir = tmp_path / "images"
    idir.mkdir()
    (idir / "test.ext4").write_text("image")
    mock_images_dir.return_value = idir

    result = runner.invoke(app, [])
    assert result.exit_code == 0
    assert "Already configured" in result.output or "ready" in result.output


# ---------------------------------------------------------------------------
# --skip-host flag
# ---------------------------------------------------------------------------


@patch("fcm.cli.configure.list_keys", return_value=[_FAKE_KEY])
@patch("fcm.cli.configure.get_images_dir")
@patch("fcm.cli.configure.get_kernels_dir")
@patch("fcm.cli.configure.list_local_versions", return_value=[_FAKE_BIN])
@patch("fcm.cli.configure.get_cache_dir")
def test_configure_skip_host(
    mock_cache,
    mock_bins,
    mock_kernels_dir,
    mock_images_dir,
    mock_keys,
    tmp_path,
):
    mock_cache.return_value = tmp_path

    kdir = tmp_path / "kernels"
    kdir.mkdir()
    (kdir / "vmlinux").write_text("kernel")
    mock_kernels_dir.return_value = kdir

    idir = tmp_path / "images"
    idir.mkdir()
    (idir / "test.ext4").write_text("image")
    mock_images_dir.return_value = idir

    result = runner.invoke(app, ["--skip-host"])
    assert result.exit_code == 0
    assert "Skipped (--skip-host)" in result.output


# ---------------------------------------------------------------------------
# Individual step tests
# ---------------------------------------------------------------------------


@patch("fcm.cli.configure.check_kvm_access", return_value=True)
@patch("fcm.cli.configure.get_host_state", return_value=MagicMock())
@patch("fcm.cli.configure.get_cache_dir")
def test_step_host_already_done(mock_cache, mock_state, mock_kvm, tmp_path):
    from fcm.cli.configure import _step_host

    mock_cache.return_value = tmp_path
    _step_host(skip=False, non_interactive=True)
    # Should not raise


@patch("fcm.cli.configure.check_kvm_access", return_value=False)
@patch("fcm.cli.configure.get_host_state", return_value=None)
@patch("fcm.cli.configure.init_host")
@patch("fcm.cli.configure.get_cache_dir")
def test_step_host_non_interactive(mock_cache, mock_init, mock_state, mock_kvm, tmp_path):
    mock_cache.return_value = tmp_path
    from fcm.cli.configure import _step_host

    _step_host(skip=False, non_interactive=True)
    mock_init.assert_called_once()


def test_step_host_skip():
    from fcm.cli.configure import _step_host

    # Should not call any host functions
    _step_host(skip=True, non_interactive=False)


@patch("fcm.cli.configure.list_local_versions", return_value=[_FAKE_BIN])
def test_step_binary_already_present(mock_bins):
    from fcm.cli.configure import _step_binary

    _step_binary(non_interactive=True)


@patch("fcm.cli.configure.list_local_versions", return_value=[])
@patch("fcm.cli.configure.list_remote_versions", return_value=["1.12.0"])
@patch("fcm.cli.configure.fetch_binary", return_value=_FAKE_BIN)
def test_step_binary_non_interactive_download(mock_fetch, mock_remote, mock_local):
    from fcm.cli.configure import _step_binary

    _step_binary(non_interactive=True)
    mock_fetch.assert_called_once_with("1.12.0")


@patch("fcm.cli.configure.list_keys", return_value=[_FAKE_KEY])
def test_step_ssh_key_already_present(mock_keys):
    from fcm.cli.configure import _step_ssh_key

    _step_ssh_key(non_interactive=True)


@patch("fcm.cli.configure.list_keys", return_value=[])
@patch("fcm.cli.configure.create_key", return_value=(_FAKE_KEY, Path("/home/.ssh/fcm-default")))
def test_step_ssh_key_non_interactive_create(mock_create, mock_keys):
    from fcm.cli.configure import _step_ssh_key

    _step_ssh_key(non_interactive=True)
    mock_create.assert_called_once_with("fcm-default")


@patch("fcm.cli.configure.get_kernels_dir")
def test_step_kernel_already_present(mock_kdir, tmp_path):
    kdir = tmp_path / "kernels"
    kdir.mkdir()
    (kdir / "vmlinux").write_text("kernel")
    mock_kdir.return_value = kdir

    from fcm.cli.configure import _step_kernel

    _step_kernel(non_interactive=True)


@patch("fcm.cli.configure.get_images_dir")
def test_step_image_already_present(mock_idir, tmp_path):
    idir = tmp_path / "images"
    idir.mkdir()
    (idir / "ubuntu.ext4").write_text("image")
    mock_idir.return_value = idir

    from fcm.cli.configure import _step_image

    _step_image(non_interactive=True)


# ---------------------------------------------------------------------------
# S-H5: shutil.which("fcm") replaces sys.argv[0]
# ---------------------------------------------------------------------------


@patch("fcm.cli.configure.check_kvm_access", return_value=False)
@patch("fcm.cli.configure.get_host_state", return_value=None)
@patch("fcm.cli.configure.get_cache_dir")
def test_step_host_uses_shutil_which_for_fcm(mock_cache, mock_state, mock_kvm, tmp_path):
    """_step_host should use shutil.which('fcm') to find the binary, not sys.argv[0]."""
    mock_cache.return_value = tmp_path
    from fcm.cli.configure import _step_host

    with (
        patch("typer.confirm", return_value=True),
        patch("shutil.which", return_value="/usr/local/bin/fcm") as mock_which,
        patch("subprocess.run", return_value=MagicMock(returncode=0)) as mock_run,
    ):
        _step_host(skip=False, non_interactive=False)

    mock_which.assert_called_once_with("fcm")
    mock_run.assert_called_once()
    cmd = mock_run.call_args[0][0]
    assert cmd == ["sudo", "/usr/local/bin/fcm", "host", "init"]
