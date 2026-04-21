"""Tests for core/_internal/_guestfs."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core._internal._guestfs import OptimizedGuestfs
from mvmctl.exceptions import GuestfsNotAvailableError, MVMError


class TestOptimizedGuestfs:
    """Tests for OptimizedGuestfs context manager."""

    def test_init_sets_attributes(self, tmp_path: Path) -> None:
        disk = tmp_path / "disk.raw"
        og = OptimizedGuestfs(disk, readonly=True)
        assert og.disk_path == disk
        assert og.readonly is True
        assert og._g is None

    def test_setup_environment_sets_direct_backend(
        self, tmp_path: Path
    ) -> None:
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

    def test_restore_environment_restores_existing(
        self, tmp_path: Path
    ) -> None:
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

    def test_create_handle_configures_optional_methods(
        self, tmp_path: Path
    ) -> None:
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
            with patch.object(
                OptimizedGuestfs, "_create_handle", return_value=mock_g
            ):
                og = OptimizedGuestfs(tmp_path / "disk.raw")
                result = og.__enter__()

        mock_setup.assert_called_once()
        mock_g.launch.assert_called_once()
        assert result is og

    def test_enter_launch_failure_restores_env(self, tmp_path: Path) -> None:
        with patch.object(OptimizedGuestfs, "_setup_environment"):
            with patch.object(
                OptimizedGuestfs,
                "_create_handle",
                side_effect=RuntimeError("fail"),
            ):
                with patch.object(
                    OptimizedGuestfs, "_restore_environment"
                ) as mock_restore:
                    og = OptimizedGuestfs(tmp_path / "disk.raw")
                    with pytest.raises(
                        MVMError, match="Failed to launch guestfs"
                    ):
                        og.__enter__()
                    mock_restore.assert_called_once()

    def test_exit_calls_shutdown(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        og = OptimizedGuestfs(tmp_path / "disk.raw")
        og._g = mock_g
        with patch.object(
            OptimizedGuestfs, "_restore_environment"
        ) as mock_restore:
            og.__exit__(None, None, None)
        mock_g.shutdown.assert_called_once()
        mock_restore.assert_called_once()

    def test_exit_handles_shutdown_exception(self, tmp_path: Path) -> None:
        mock_g = MagicMock()
        mock_g.shutdown.side_effect = RuntimeError("shutdown failed")
        og = OptimizedGuestfs(tmp_path / "disk.raw")
        og._g = mock_g
        with patch.object(
            OptimizedGuestfs, "_restore_environment"
        ) as mock_restore:
            og.__exit__(None, None, None)
        mock_restore.assert_called_once()

    def test_exit_handles_none_handle(self, tmp_path: Path) -> None:
        og = OptimizedGuestfs(tmp_path / "disk.raw")
        og._g = None
        with patch.object(
            OptimizedGuestfs, "_restore_environment"
        ) as mock_restore:
            og.__exit__(None, None, None)
        mock_restore.assert_called_once()


class TestFindLargestLinuxFs:
    """Tests for OptimizedGuestfs.find_largest_linux_fs method."""

    def test_returns_none_for_empty_partitions(self) -> None:
        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()
        result = og.find_largest_linux_fs([])
        assert result is None

    def test_returns_largest_ext4(self) -> None:
        sizes = {"/dev/sda1": 1000, "/dev/sda2": 2000}
        mounted: list[str] = []

        def mount(dev: str, path: str) -> None:
            mounted.clear()
            mounted.append(dev)

        def statvfs(path: str) -> dict:
            dev = mounted[0] if mounted else "/dev/sda1"
            return {"fs_blocks": sizes.get(dev, 0), "fs_bsize": 4096}

        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()
        og._g.vfs_type.return_value = "ext4"
        og._g.mount = mount
        og._g.statvfs = statvfs

        result = og.find_largest_linux_fs(["/dev/sda1", "/dev/sda2"])
        assert result == "/dev/sda2"

    def test_skips_non_linux_fs(self) -> None:
        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()
        og._g.vfs_type.return_value = "ntfs"

        result = og.find_largest_linux_fs(["/dev/sda1"])
        assert result is None

    def test_handles_exception_on_partition(self) -> None:
        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()
        og._g.vfs_type.side_effect = RuntimeError("read error")

        result = og.find_largest_linux_fs(["/dev/sda1"])
        assert result is None


class TestGetFsSize:
    """Tests for OptimizedGuestfs.get_fs_size method."""

    def test_returns_size(self) -> None:
        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()
        og._g.statvfs.return_value = {"fs_blocks": 2000, "fs_bsize": 4096}

        result = og.get_fs_size("/dev/sda1")
        assert result == 2000 * 4096
        og._g.mount.assert_called_once_with("/dev/sda1", "/")
        og._g.umount.assert_called_once_with("/dev/sda1")


class TestOptimizedGuestfsDeblob:
    """Tests for OptimizedGuestfs.deblob method."""

    def test_deblob_runs_apt_clean_on_ubuntu(self) -> None:
        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()
        og._g.cat.return_value = "ID=ubuntu\n"

        og.deblob()

        og._g.sh.assert_any_call("apt-get clean")
        og._g.sh.assert_any_call(
            "rm -rf /usr/share/doc/* /usr/share/man/* /usr/share/info/*"
        )

    def test_deblob_runs_pacman_clean_on_arch(self) -> None:
        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()
        og._g.cat.return_value = "ID=arch\n"

        og.deblob()

        og._g.sh.assert_any_call("pacman -Sc --noconfirm || true")

    def test_deblob_handles_exception_gracefully(self) -> None:
        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()
        og._g.cat.side_effect = RuntimeError("guestfs error")

        # Should not raise
        og.deblob()


class TestOptimizedGuestfsShrinkExt4:
    """Tests for OptimizedGuestfs.shrink_ext4 method."""

    def test_shrink_ext4_delegates_correctly(self) -> None:
        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()

        og.shrink_ext4("/dev/sda1")

        og._g.mount.assert_any_call("/dev/sda1", "/")
        og._g.zero_free_space.assert_called_once_with("/dev/sda1")
        og._g.e2fsck.assert_called_once_with("/dev/sda1", correct=True)
        og._g.resize2fs_size.assert_called_once_with("/dev/sda1", 0)


class TestOptimizedGuestfsShrinkBtrfs:
    """Tests for OptimizedGuestfs.shrink_btrfs method."""

    def test_shrink_btrfs_delegates_correctly(self) -> None:
        og = OptimizedGuestfs(Path("disk.raw"))
        og._g = MagicMock()

        og.shrink_btrfs("/dev/sda1")

        og._g.mount.assert_called_once_with("/dev/sda1", "/")
        og._g.sh.assert_any_call("fstrim -av / 2>/dev/null || true")
        og._g.btrfs_filesystem_sync.assert_called_once_with("/")
        og._g.btrfs_filesystem_resize.assert_called_once_with("/", 0)
        og._g.umount.assert_called_once_with("/dev/sda1")


class TestOptimizedGuestfsExtractPartition:
    """Tests for OptimizedGuestfs.extract_partition classmethod."""

    def test_classmethod_returns_none_when_guestfs_unavailable(
        self, tmp_path: Path
    ) -> None:
        with patch.object(
            OptimizedGuestfs,
            "__init__",
            side_effect=GuestfsNotAvailableError("not available"),
        ):
            result = OptimizedGuestfs.extract_partition(
                tmp_path / "disk.raw", tmp_path / "output.raw"
            )
        assert result is None

    def test_classmethod_delegates_to_instance(self, tmp_path: Path) -> None:
        output_path = tmp_path / "output.raw"
        output_path.write_bytes(b"\x00" * 4096)

        mock_og = MagicMock()
        mock_og.list_partitions.return_value = ["/dev/sda1"]
        mock_og.get_fs_size.return_value = 1000 * 4096
        mock_og.copy_device_to_file.return_value = None
        mock_og.__enter__ = MagicMock(return_value=mock_og)
        mock_og.__exit__ = MagicMock(return_value=None)

        def mock_new(cls, *args, **kwargs):  # noqa: ARG001
            return mock_og

        with patch.object(OptimizedGuestfs, "__new__", mock_new):
            result = OptimizedGuestfs.extract_partition(
                tmp_path / "disk.raw", output_path, partition=1
            )

        assert result == output_path
        mock_og.copy_device_to_file.assert_called_once_with(
            "/dev/sda1", str(output_path)
        )
