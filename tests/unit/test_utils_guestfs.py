"""Tests for utils/guestfs.py."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.utils.guestfs import (
    OptimizedGuestfs,
    _find_largest_linux_fs,
    _get_fs_size,
    check_libguestfs,
    extract_partition_with_guestfs,
    optimized_guestfs,
)


class TestOptimizedGuestfs:
    """Tests for OptimizedGuestfs context manager."""

    def test_init_sets_attributes(self, tmp_path: Path) -> None:
        disk = tmp_path / "disk.raw"
        og = OptimizedGuestfs(disk, readonly=True)
        assert og.disk_path == disk
        assert og.readonly is True
        assert og._g is None

    def test_setup_environment_sets_direct_backend(self, tmp_path: Path) -> None:
        import os

        og = OptimizedGuestfs(tmp_path / "disk.raw")
        original_backend = os.environ.get("LIBGUESTFS_BACKEND")
        try:
            og._setup_environment()
            assert os.environ["LIBGUESTFS_BACKEND"] == "direct"
            assert os.environ["QEMU_LOCKING"] == "off"
        finally:
            if original_backend is not None:
                os.environ["LIBGUESTFS_BACKEND"] = original_backend
            elif "LIBGUESTFS_BACKEND" in os.environ:
                del os.environ["LIBGUESTFS_BACKEND"]

    def test_setup_environment_saves_original(self, tmp_path: Path) -> None:
        import os

        og = OptimizedGuestfs(tmp_path / "disk.raw")
        original = os.environ.get("LIBGUESTFS_BACKEND")
        try:
            og._setup_environment()
            assert og._orig_env["LIBGUESTFS_BACKEND"] == original
        finally:
            if original is not None:
                os.environ["LIBGUESTFS_BACKEND"] = original
            elif "LIBGUESTFS_BACKEND" in os.environ:
                del os.environ["LIBGUESTFS_BACKEND"]

    def test_restore_environment_restores_existing(self, tmp_path: Path) -> None:
        import os

        og = OptimizedGuestfs(tmp_path / "disk.raw")
        os.environ["LIBGUESTFS_BACKEND"] = "original_value"
        og._orig_env = {"LIBGUESTFS_BACKEND": "original_value"}
        os.environ["LIBGUESTFS_BACKEND"] = "direct"
        og._restore_environment()
        assert os.environ["LIBGUESTFS_BACKEND"] == "original_value"

    def test_restore_environment_removes_unset(self, tmp_path: Path) -> None:
        import os

        og = OptimizedGuestfs(tmp_path / "disk.raw")
        og._orig_env = {"TEST_VAR_XYZ": None}
        os.environ["TEST_VAR_XYZ"] = "temp"
        og._restore_environment()
        assert "TEST_VAR_XYZ" not in os.environ

    def test_create_handle_calls_add_drive_opts(self, tmp_path: Path) -> None:
        mock_guestfs_module = MagicMock()
        mock_g = MagicMock()
        mock_guestfs_module.GuestFS.return_value = mock_g

        og = OptimizedGuestfs(tmp_path / "disk.raw", readonly=True)
        with patch("importlib.import_module", return_value=mock_guestfs_module):
            result = og._create_handle()

        mock_g.add_drive_opts.assert_called_once_with(
            str(tmp_path / "disk.raw"),
            format="raw",
            readonly=True,
            cachemode="writeback",
        )
        assert result is mock_g

    def test_create_handle_configures_optional_methods(self, tmp_path: Path) -> None:
        mock_guestfs_module = MagicMock()
        mock_g = MagicMock()
        mock_guestfs_module.GuestFS.return_value = mock_g

        og = OptimizedGuestfs(tmp_path / "disk.raw")
        with patch("importlib.import_module", return_value=mock_guestfs_module):
            og._create_handle()

        mock_g.set_recovery_proc.assert_called_once_with(False)
        mock_g.set_autosync.assert_called_once_with(False)
        mock_g.set_network.assert_called_once_with(False)
        mock_g.set_smp.assert_called_once_with(1)
        mock_g.set_memsize.assert_called_once_with(256)

    def test_enter_launch_success(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        with patch.object(OptimizedGuestfs, "_setup_environment") as mock_setup:
            with patch.object(OptimizedGuestfs, "_create_handle", return_value=mock_g):
                og = OptimizedGuestfs(tmp_path / "disk.raw")
                result = og.__enter__()

        mock_setup.assert_called_once()
        mock_g.launch.assert_called_once()
        assert result is mock_g

    def test_enter_launch_failure_restores_env(self, tmp_path: Path) -> None:
        with patch.object(OptimizedGuestfs, "_setup_environment"):
            with patch.object(OptimizedGuestfs, "_create_handle", side_effect=RuntimeError("fail")):
                with patch.object(OptimizedGuestfs, "_restore_environment") as mock_restore:
                    og = OptimizedGuestfs(tmp_path / "disk.raw")
                    with pytest.raises(MVMError, match="Failed to launch guestfs"):
                        og.__enter__()
                    mock_restore.assert_called_once()

    def test_exit_calls_shutdown(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        og = OptimizedGuestfs(tmp_path / "disk.raw")
        og._g = mock_g
        with patch.object(OptimizedGuestfs, "_restore_environment") as mock_restore:
            og.__exit__(None, None, None)
        mock_g.shutdown.assert_called_once()
        mock_restore.assert_called_once()

    def test_exit_handles_shutdown_exception(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        mock_g.shutdown.side_effect = RuntimeError("shutdown failed")
        og = OptimizedGuestfs(tmp_path / "disk.raw")
        og._g = mock_g
        with patch.object(OptimizedGuestfs, "_restore_environment") as mock_restore:
            og.__exit__(None, None, None)
        mock_restore.assert_called_once()

    def test_exit_handles_none_handle(self, tmp_path: Path) -> None:
        og = OptimizedGuestfs(tmp_path / "disk.raw")
        og._g = None
        with patch.object(OptimizedGuestfs, "_restore_environment") as mock_restore:
            og.__exit__(None, None, None)
        mock_restore.assert_called_once()


class TestOptimizedGuestfsContextManager:
    """Tests for optimized_guestfs context manager function."""

    def test_yields_guestfs_handle(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        with patch.object(OptimizedGuestfs, "__enter__", return_value=mock_g):
            with patch.object(OptimizedGuestfs, "__exit__", return_value=None):
                with optimized_guestfs(tmp_path / "disk.raw") as g:
                    assert g is mock_g


class TestCheckLibguestfs:
    """Tests for check_libguestfs function."""

    def test_returns_true_when_available(self) -> None:
        mock_module = MagicMock()
        mock_module.GuestFS = MagicMock()
        with patch("importlib.import_module", return_value=mock_module):
            assert check_libguestfs() is True

    def test_returns_false_on_import_error(self) -> None:
        with patch("importlib.import_module", side_effect=ImportError("no module")):
            assert check_libguestfs() is False


class TestExtractPartitionWithGuestfs:
    """Tests for extract_partition_with_guestfs function."""

    def test_returns_none_when_guestfs_unavailable(self, tmp_path: Path) -> None:
        with patch("mvmctl.utils.guestfs.check_libguestfs", return_value=False):
            result = extract_partition_with_guestfs(tmp_path / "disk.raw", tmp_path / "output.raw")
        assert result is None

    def test_returns_none_when_no_partitions(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        mock_g.list_partitions.return_value = []
        with patch("mvmctl.utils.guestfs.check_libguestfs", return_value=True):
            with patch("mvmctl.utils.guestfs.optimized_guestfs") as mock_ctx:
                mock_ctx.return_value.__enter__ = MagicMock(return_value=mock_g)
                mock_ctx.return_value.__exit__ = MagicMock(return_value=None)
                result = extract_partition_with_guestfs(
                    tmp_path / "disk.raw", tmp_path / "output.raw"
                )
        assert result is None

    def test_returns_none_when_partition_out_of_range(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        mock_g.list_partitions.return_value = ["/dev/sda1"]
        with patch("mvmctl.utils.guestfs.check_libguestfs", return_value=True):
            with patch("mvmctl.utils.guestfs.optimized_guestfs") as mock_ctx:
                mock_ctx.return_value.__enter__ = MagicMock(return_value=mock_g)
                mock_ctx.return_value.__exit__ = MagicMock(return_value=None)
                result = extract_partition_with_guestfs(
                    tmp_path / "disk.raw", tmp_path / "output.raw", partition=5
                )
        assert result is None

    def test_returns_path_on_success(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        mock_g.list_partitions.return_value = ["/dev/sda1"]
        mock_g.statvfs.return_value = {"fs_blocks": 1000, "fs_bsize": 4096}
        output_path = tmp_path / "output.raw"
        output_path.write_bytes(b"\x00" * 4096)

        with patch("mvmctl.utils.guestfs.check_libguestfs", return_value=True):
            with patch("mvmctl.utils.guestfs.optimized_guestfs") as mock_ctx:
                mock_ctx.return_value.__enter__ = MagicMock(return_value=mock_g)
                mock_ctx.return_value.__exit__ = MagicMock(return_value=None)
                result = extract_partition_with_guestfs(
                    tmp_path / "disk.raw", output_path, partition=1
                )
        assert result == output_path
        mock_g.copy_device_to_file.assert_called_once()

    def test_auto_detect_partition(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        mock_g.list_partitions.return_value = ["/dev/sda1", "/dev/sda2"]
        mock_g.vfs_type.return_value = "ext4"
        mock_g.statvfs.return_value = {"fs_blocks": 500, "fs_bsize": 4096}
        output_path = tmp_path / "output.raw"
        output_path.write_bytes(b"\x00" * 4096)

        with patch("mvmctl.utils.guestfs.check_libguestfs", return_value=True):
            with patch("mvmctl.utils.guestfs.optimized_guestfs") as mock_ctx:
                mock_ctx.return_value.__enter__ = MagicMock(return_value=mock_g)
                mock_ctx.return_value.__exit__ = MagicMock(return_value=None)
                result = extract_partition_with_guestfs(tmp_path / "disk.raw", output_path)
        assert result == output_path

    def test_returns_none_on_exception(self, tmp_path: Path) -> None:
        with patch("mvmctl.utils.guestfs.check_libguestfs", return_value=True):
            with patch(
                "mvmctl.utils.guestfs.optimized_guestfs",
                side_effect=RuntimeError("guestfs error"),
            ):
                result = extract_partition_with_guestfs(
                    tmp_path / "disk.raw", tmp_path / "output.raw"
                )
        assert result is None


class TestFindLargestLinuxFs:
    """Tests for _find_largest_linux_fs function."""

    def test_returns_none_for_empty_partitions(self) -> None:
        mock_g = MagicMock()
        result = _find_largest_linux_fs(mock_g, [])
        assert result is None

    def test_returns_largest_ext4(self) -> None:
        mock_g = MagicMock()
        sizes = {"/dev/sda1": 1000, "/dev/sda2": 2000}
        mounted: list[str] = []

        def mount(dev: str, path: str) -> None:
            mounted.clear()
            mounted.append(dev)

        def statvfs(path: str) -> dict:
            dev = mounted[0] if mounted else "/dev/sda1"
            return {"fs_blocks": sizes.get(dev, 0), "fs_bsize": 4096}

        mock_g.vfs_type.return_value = "ext4"
        mock_g.mount = mount
        mock_g.statvfs = statvfs

        result = _find_largest_linux_fs(mock_g, ["/dev/sda1", "/dev/sda2"])
        assert result == "/dev/sda2"

    def test_skips_non_linux_fs(self) -> None:
        mock_g = MagicMock()
        mock_g.vfs_type.return_value = "ntfs"

        result = _find_largest_linux_fs(mock_g, ["/dev/sda1"])
        assert result is None

    def test_handles_exception_on_partition(self) -> None:
        mock_g = MagicMock()
        mock_g.vfs_type.side_effect = RuntimeError("read error")

        result = _find_largest_linux_fs(mock_g, ["/dev/sda1"])
        assert result is None


class TestGetFsSize:
    """Tests for _get_fs_size function."""

    def test_returns_size(self) -> None:
        mock_g = MagicMock()
        mock_g.statvfs.return_value = {"fs_blocks": 2000, "fs_bsize": 4096}

        result = _get_fs_size(mock_g, "/dev/sda1")
        assert result == 2000 * 4096
        mock_g.mount.assert_called_once_with("/dev/sda1", "/")
        mock_g.umount.assert_called_once_with("/dev/sda1")
