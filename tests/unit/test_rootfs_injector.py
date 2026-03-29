"""Unit tests for libguestfs-based rootfs injection."""

from unittest.mock import MagicMock, Mock, patch

import pytest

from mvmctl.core.rootfs_injector import (
    _detect_root_partition,
    _write_cloud_init_files,
    check_libguestfs,
    inject_cloud_init,
)
from mvmctl.exceptions import (
    GuestfsLaunchError,
    GuestfsMountError,
    GuestfsNotAvailableError,
    GuestfsWriteError,
)


class TestCheckLibguestfs:
    """Tests for check_libguestfs function."""

    def test_returns_true_when_guestfs_importable(self):
        """Test that function returns True when guestfs is available."""
        mock_guestfs = MagicMock()
        mock_guestfs.GuestFS = MagicMock
        with patch(
            "mvmctl.core.rootfs_injector.importlib.import_module",
            return_value=mock_guestfs,
        ):
            assert check_libguestfs() is True

    def test_returns_false_when_guestfs_not_importable(self):
        """Test that function returns False when guestfs is not available."""
        with patch(
            "mvmctl.core.rootfs_injector.importlib.import_module",
            side_effect=ImportError("No module named guestfs"),
        ):
            assert check_libguestfs() is False


class TestDetectRootPartition:
    """Tests for _detect_root_partition function."""

    def test_detects_partition_with_os_release(self):
        """Test auto-detection when /etc/os-release exists."""
        mock_g = Mock()
        mock_g.list_filesystems.return_value = {
            "/dev/sda1": "ext4",
            "/dev/sda2": "ext4",
        }
        mock_g.exists.side_effect = lambda path: path in {"/etc/os-release", "/etc/fstab"}

        result = _detect_root_partition(mock_g, "/path/to/rootfs.img")
        assert result == "/dev/sda1"

    def test_detects_partition_with_fstab(self):
        """Test auto-detection when /etc/fstab exists."""
        mock_g = Mock()
        mock_g.list_filesystems.return_value = {
            "/dev/sda1": "ext4",
        }
        mock_g.exists.side_effect = lambda path: path in {"/etc/os-release", "/etc/fstab"}

        result = _detect_root_partition(mock_g, "/path/to/rootfs.img")
        assert result == "/dev/sda1"

    def test_fallback_to_configured_device_when_no_clear_root(self):
        """Test fallback to DEFAULT_LIBGUESTFS_ROOT_DEVICE when no root indicators found."""
        from mvmctl.constants import DEFAULT_LIBGUESTFS_ROOT_DEVICE

        mock_g = Mock()
        mock_g.list_filesystems.return_value = {
            DEFAULT_LIBGUESTFS_ROOT_DEVICE: "ext4",
            "/dev/sda2": "swap",
        }
        mock_g.exists.return_value = False

        result = _detect_root_partition(mock_g, "/path/to/rootfs.img")
        assert result == DEFAULT_LIBGUESTFS_ROOT_DEVICE

    def test_raises_error_when_no_filesystems_found(self):
        """Test error when no filesystems detected."""
        mock_g = Mock()
        mock_g.list_filesystems.return_value = {}

        with pytest.raises(GuestfsMountError):
            _detect_root_partition(mock_g, "/path/to/rootfs.img")

    def test_handles_multiple_partitions(self):
        """Test detection with multiple partitions and mountpoints."""
        mock_g = Mock()
        mock_g.list_filesystems.return_value = {
            "/dev/sda1": "ext4",
            "/dev/sda2": "ext4",
            "/dev/sda3": "btrfs",
        }
        mock_g.exists.side_effect = lambda path: path == "/etc/os-release"

        result = _detect_root_partition(mock_g, "/path/to/rootfs.img")
        assert result == "/dev/sda1"


class TestWriteCloudInitFiles:
    """Tests for _write_cloud_init_files function."""

    def test_writes_required_files(self, tmp_path):
        """Test writing meta-data and user-data."""
        mock_g = Mock()
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        _write_cloud_init_files(mock_g, str(cloud_init_dir))

        from mvmctl.constants import DEFAULT_LIBGUESTFS_SEED_DIR

        mock_g.mkdir_p.assert_called_once_with(DEFAULT_LIBGUESTFS_SEED_DIR)
        assert mock_g.write.call_count == 2

    def test_writes_optional_network_config(self, tmp_path):
        """Test writing optional network-config file."""
        mock_g = Mock()
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")
        (cloud_init_dir / "network-config").write_text("version: 2\n")

        _write_cloud_init_files(mock_g, str(cloud_init_dir))

        assert mock_g.write.call_count == 3

    def test_raises_error_when_meta_data_missing(self, tmp_path):
        """Test error when required meta-data file is missing."""
        mock_g = Mock()
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        with pytest.raises(GuestfsWriteError, match="meta-data"):
            _write_cloud_init_files(mock_g, str(cloud_init_dir))

    def test_raises_error_when_user_data_missing(self, tmp_path):
        """Test error when required user-data file is missing."""
        mock_g = Mock()
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        (cloud_init_dir / "meta-data").write_text("instance-id: test")

        with pytest.raises(GuestfsWriteError, match="user-data"):
            _write_cloud_init_files(mock_g, str(cloud_init_dir))


class TestInjectCloudInit:
    """Integration-style tests for inject_cloud_init function."""

    def test_raises_file_not_found_for_missing_rootfs(self, tmp_path):
        """Test error when rootfs image does not exist."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        with pytest.raises(FileNotFoundError, match="rootfs"):
            inject_cloud_init("/nonexistent/rootfs.img", str(cloud_init_dir))

    def test_raises_file_not_found_for_missing_cloud_init_dir(self, tmp_path):
        """Test error when cloud-init directory does not exist."""
        rootfs_path = tmp_path / "rootfs.img"
        rootfs_path.write_text("")  # Create dummy file

        with pytest.raises(FileNotFoundError, match="cloud-init"):
            inject_cloud_init(str(rootfs_path), "/nonexistent/cloud-init")

    def test_raises_guestfs_not_available_when_import_fails(self, tmp_path):
        """Test error when libguestfs is not available."""
        rootfs_path = tmp_path / "rootfs.img"
        rootfs_path.write_text("")
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        with patch("mvmctl.core.rootfs_injector.check_libguestfs", return_value=False):
            with pytest.raises(GuestfsNotAvailableError):
                inject_cloud_init(str(rootfs_path), str(cloud_init_dir))

    def test_raises_launch_error_when_guestfs_fails_to_launch(self, tmp_path):
        """Test error when guestfs appliance fails to launch."""
        rootfs_path = tmp_path / "rootfs.img"
        rootfs_path.write_text("")
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        mock_guestfs = Mock()
        mock_g = Mock()
        mock_guestfs.GuestFS.return_value = mock_g
        mock_g.launch.side_effect = Exception("Launch failed")

        with patch.dict("sys.modules", {"guestfs": mock_guestfs}):
            with patch("mvmctl.core.rootfs_injector.check_libguestfs", return_value=True):
                with pytest.raises(GuestfsLaunchError):
                    inject_cloud_init(str(rootfs_path), str(cloud_init_dir))

    def test_successful_injection(self, tmp_path):
        """Test successful cloud-init injection."""
        rootfs_path = tmp_path / "rootfs.img"
        rootfs_path.write_text("")
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        mock_guestfs = Mock()
        mock_g = Mock()
        mock_guestfs.GuestFS.return_value = mock_g
        mock_g.list_filesystems.return_value = {"/dev/sda1": "ext4"}
        mock_g.exists.return_value = True  # Has /etc/os-release

        with patch.dict("sys.modules", {"guestfs": mock_guestfs}):
            with patch("mvmctl.core.rootfs_injector.check_libguestfs", return_value=True):
                inject_cloud_init(str(rootfs_path), str(cloud_init_dir))

        mock_g.add_drive.assert_called_once_with(str(rootfs_path), readonly=False)
        mock_g.launch.assert_called_once()
        # mount is called twice: once in _detect_root_partition and once in inject_cloud_init
        assert mock_g.mount.call_count == 2
        mock_g.shutdown.assert_called_once()
        mock_g.close.assert_called_once()

    def test_cleanup_on_write_failure(self, tmp_path):
        """Test that guestfs is properly cleaned up on write failure."""
        rootfs_path = tmp_path / "rootfs.img"
        rootfs_path.write_text("")
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        # Missing required files to trigger write failure

        mock_guestfs = Mock()
        mock_g = Mock()
        mock_guestfs.GuestFS.return_value = mock_g
        mock_g.list_filesystems.return_value = {"/dev/sda1": "ext4"}
        mock_g.exists.return_value = True

        with patch.dict("sys.modules", {"guestfs": mock_guestfs}):
            with patch("mvmctl.core.rootfs_injector.check_libguestfs", return_value=True):
                with pytest.raises(GuestfsWriteError):
                    inject_cloud_init(str(rootfs_path), str(cloud_init_dir))

        mock_g.shutdown.assert_called_once()
        mock_g.close.assert_called_once()


class TestFilesystemTypes:
    """Tests for different rootfs filesystem types."""

    @pytest.mark.parametrize("fstype", ["ext4", "btrfs", "xfs"])
    def test_handles_various_filesystems(self, fstype, tmp_path):
        """Test injection works with ext4, btrfs, and xfs."""
        rootfs_path = tmp_path / "rootfs.img"
        rootfs_path.write_text("")
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        mock_guestfs = Mock()
        mock_g = Mock()
        mock_guestfs.GuestFS.return_value = mock_g
        mock_g.list_filesystems.return_value = {"/dev/sda1": fstype}
        mock_g.exists.return_value = True

        with patch.dict("sys.modules", {"guestfs": mock_guestfs}):
            with patch("mvmctl.core.rootfs_injector.check_libguestfs", return_value=True):
                inject_cloud_init(str(rootfs_path), str(cloud_init_dir))

        # Should complete without filesystem-specific errors
        # mount is called twice: once in _detect_root_partition and once in inject_cloud_init
        assert mock_g.mount.call_count == 2


class TestConstantsUsage:
    def test_detect_root_partition_uses_fallback_device(self):
        from mvmctl.constants import DEFAULT_LIBGUESTFS_ROOT_DEVICE

        mock_g = Mock()
        mock_g.list_filesystems.return_value = {
            DEFAULT_LIBGUESTFS_ROOT_DEVICE: "ext4",
        }
        mock_g.exists.return_value = False

        result = _detect_root_partition(mock_g, "/path/to/rootfs.img")
        assert result == DEFAULT_LIBGUESTFS_ROOT_DEVICE

    def test_write_cloud_init_uses_seed_dir_constant(self, tmp_path):
        from mvmctl.constants import DEFAULT_LIBGUESTFS_SEED_DIR

        mock_g = Mock()
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        _write_cloud_init_files(mock_g, str(cloud_init_dir))

        mock_g.mkdir_p.assert_called_once_with(DEFAULT_LIBGUESTFS_SEED_DIR)

    def test_launch_uses_timeout_constant(self, tmp_path):
        from mvmctl.constants import DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT

        rootfs_path = tmp_path / "rootfs.img"
        rootfs_path.write_text("")
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        mock_guestfs = Mock()
        mock_g = Mock()
        mock_guestfs.GuestFS.return_value = mock_g
        mock_g.list_filesystems.return_value = {"/dev/sda1": "ext4"}
        mock_g.exists.return_value = True

        with patch.dict("sys.modules", {"guestfs": mock_guestfs}):
            with patch("mvmctl.core.rootfs_injector.check_libguestfs", return_value=True):
                inject_cloud_init(str(rootfs_path), str(cloud_init_dir))

        mock_g.set_timeout.assert_called_once_with(DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT)
