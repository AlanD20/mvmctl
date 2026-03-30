"""Unit tests for libguestfs-based rootfs injection."""

from pathlib import Path
from unittest.mock import MagicMock, Mock, patch

import pytest

from mvmctl.core.rootfs_injector import (
    _detect_root_partition,
    _write_cloud_init_files,
    inject_cloud_init,
)
from mvmctl.exceptions import (
    GuestfsMountError,
    GuestfsNotAvailableError,
    GuestfsWriteError,
)
from mvmctl.utils.guestfs import check_libguestfs


class TestCheckLibguestfs:
    """Tests for check_libguestfs function."""

    def test_returns_true_when_guestfs_importable(self):
        """Test that function returns True when guestfs is available."""
        mock_guestfs = MagicMock()
        mock_guestfs.GuestFS = MagicMock
        with patch.dict("sys.modules", {"guestfs": mock_guestfs}):
            assert check_libguestfs() is True

    def test_returns_false_when_guestfs_not_importable(self):
        """Test that function returns False when guestfs is not available."""
        with patch.dict("sys.modules", {"guestfs": None}):
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

    def test_successful_injection(self, tmp_path):
        """Test successful cloud-init injection."""
        rootfs_path = tmp_path / "rootfs.img"
        rootfs_path.write_text("")
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        mock_g = Mock()
        mock_g.list_filesystems.return_value = {"/dev/sda1": "ext4"}
        mock_g.exists.return_value = True  # Has /etc/os-release

        with patch("mvmctl.core.rootfs_injector.check_libguestfs", return_value=True):
            with patch("mvmctl.utils.guestfs_helper.optimized_guestfs") as mock_ctx:
                mock_ctx.return_value.__enter__ = Mock(return_value=mock_g)
                mock_ctx.return_value.__exit__ = Mock(return_value=False)
                inject_cloud_init(str(rootfs_path), str(cloud_init_dir))

        # Verify mount was called for detection and for actual mounting
        assert mock_g.mount.call_count == 2
        # Verify umount was called (once in detection, once explicitly at end)
        assert mock_g.umount.call_count >= 1

    def test_mount_error_propagates(self, tmp_path):
        """Test that mount errors are properly propagated."""
        rootfs_path = tmp_path / "rootfs.img"
        rootfs_path.write_text("")
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        mock_g = Mock()
        mock_g.list_filesystems.return_value = {"/dev/sda1": "ext4"}
        mock_g.exists.return_value = True
        # First mount (in _detect_root_partition) succeeds, second fails
        mock_g.mount.side_effect = [None, Exception("Mount failed")]

        with patch("mvmctl.core.rootfs_injector.check_libguestfs", return_value=True):
            with patch("mvmctl.utils.guestfs_helper.optimized_guestfs") as mock_ctx:
                mock_ctx.return_value.__enter__ = Mock(return_value=mock_g)
                mock_ctx.return_value.__exit__ = Mock(return_value=False)
                with pytest.raises(GuestfsMountError, match="Failed to mount"):
                    inject_cloud_init(str(rootfs_path), str(cloud_init_dir))


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

        mock_g = Mock()
        mock_g.list_filesystems.return_value = {"/dev/sda1": fstype}
        mock_g.exists.return_value = True

        with patch("mvmctl.core.rootfs_injector.check_libguestfs", return_value=True):
            with patch("mvmctl.utils.guestfs_helper.optimized_guestfs") as mock_ctx:
                mock_ctx.return_value.__enter__ = Mock(return_value=mock_g)
                mock_ctx.return_value.__exit__ = Mock(return_value=False)
                inject_cloud_init(str(rootfs_path), str(cloud_init_dir))

        # Should complete without filesystem-specific errors
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
