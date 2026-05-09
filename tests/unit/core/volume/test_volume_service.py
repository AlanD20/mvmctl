"""Tests for VolumeService — disk operations with mocked subprocess."""

from __future__ import annotations

import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.core.volume._service import VolumeService
from mvmctl.exceptions import VolumeCreateError
from mvmctl.models import VolumeItem, VolumeStatus


def _make_volume(
    name: str = "test-vol",
    size_bytes: int = 1073741824,
    fmt: str = "raw",
    status: str = "available",
    path: str | None = None,
) -> VolumeItem:
    vid = f"{name}-id-" + "x" * 55
    return VolumeItem(
        id=vid,
        name=name,
        size_bytes=size_bytes,
        format=fmt,
        path=path or f"/volumes/{name}.{fmt}",
        status=status,
        vm_id=None,
        created_at="2026-01-01T00:00:00+00:00",
        updated_at="2026-01-01T00:00:00+00:00",
    )


@pytest.fixture
def service(tmp_path: Path) -> VolumeService:
    """Create VolumeService with a mock repo and isolated temp dir."""
    repo = MagicMock(spec=VolumeRepository)
    return VolumeService(repo)


class TestVolumeServiceCreateDisk:
    def test_create_raw_calls_fallocate(
        self, service: VolumeService, tmp_path: Path
    ):
        """Creating a raw disk should invoke fallocate with size."""
        path = tmp_path / "test.raw"
        vol = _make_volume(path=str(path), size_bytes=1073741824)
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)

            service.create_disk(vol)

            mock_run.assert_called_once()
            args = mock_run.call_args[0][0]
            assert "fallocate" in args
            assert "-l" in args
            assert "1073741824" in args

    def test_create_raw_creates_parent_dirs(
        self, service: VolumeService, tmp_path: Path
    ):
        """Creating a disk should ensure parent directories exist."""
        path = tmp_path / "subdir" / "test.raw"
        vol = _make_volume(path=str(path), size_bytes=1073741824)
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)

            service.create_disk(vol)

        assert path.parent.exists()

    def test_create_qcow2_calls_qemu_img(
        self, service: VolumeService, tmp_path: Path
    ):
        """Creating a qcow2 disk should invoke qemu-img create."""
        path = tmp_path / "test.qcow2"
        vol = _make_volume(path=str(path), size_bytes=1073741824, fmt="qcow2")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)

            service.create_disk(vol)

            mock_run.assert_called_once()
            args = mock_run.call_args[0][0]
            assert "qemu-img" in args
            assert "create" in args
            assert "-f" in args
            assert "qcow2" in args
            assert "1073741824" in args

    def test_create_raw_raises_on_failure(
        self, service: VolumeService, tmp_path: Path
    ):
        """Disk creation failure should raise VolumeCreateError."""
        path = tmp_path / "test.raw"
        vol = _make_volume(path=str(path), size_bytes=1073741824)
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=1, stderr=b"disk full")
            mock_run.side_effect = subprocess.CalledProcessError(
                1, ["fallocate"], stderr=b"disk full"
            )

            with pytest.raises(VolumeCreateError, match="fallocate failed"):
                service.create_disk(vol)

    def test_create_unsupported_format_raises(
        self, service: VolumeService, tmp_path: Path
    ):
        """Unsupported format should raise VolumeCreateError."""
        path = tmp_path / "test.vmdk"
        vol = _make_volume(path=str(path), size_bytes=1073741824, fmt="vmdk")
        with pytest.raises(VolumeCreateError, match="Unsupported format"):
            service.create_disk(vol)

    def test_create_raw_fallocate_not_found(
        self, service: VolumeService, tmp_path: Path
    ):
        """Missing fallocate binary should raise VolumeCreateError."""
        path = tmp_path / "test.raw"
        vol = _make_volume(path=str(path), size_bytes=1073741824)
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = FileNotFoundError()

            with pytest.raises(VolumeCreateError, match="fallocate not found"):
                service.create_disk(vol)

    def test_create_qcow2_qemu_img_not_found(
        self, service: VolumeService, tmp_path: Path
    ):
        """Missing qemu-img binary should raise VolumeCreateError."""
        path = tmp_path / "test.qcow2"
        vol = _make_volume(path=str(path), size_bytes=1073741824, fmt="qcow2")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = FileNotFoundError()

            with pytest.raises(VolumeCreateError, match="qemu-img not found"):
                service.create_disk(vol)

    def test_create_disk_upserts_and_returns_volume(
        self, service: VolumeService, tmp_path: Path
    ):
        """create_disk should upsert the volume record and return it."""
        path = tmp_path / "test.raw"
        vol = _make_volume(path=str(path), size_bytes=1073741824)
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)
            result = service.create_disk(vol)
        assert result is vol
        service._repo.upsert.assert_called_once_with(vol)


class TestVolumeServiceRemoveDisk:
    def test_remove_existing_disk_calls_unlink(
        self, service: VolumeService, tmp_path: Path
    ):
        """remove_disk should delete DB record and unlink the file."""
        path = tmp_path / "test.raw"
        path.write_text("fake disk content")
        vol = _make_volume(path=str(path))

        service.remove_disk(vol)

        assert not path.exists()
        service._repo.delete.assert_called_once_with(vol.id)

    def test_remove_nonexistent_disk_does_not_raise(
        self, service: VolumeService, tmp_path: Path
    ):
        """remove_disk should silently handle missing files."""
        path = tmp_path / "nonexistent.raw"
        vol = _make_volume(path=str(path))
        service.remove_disk(vol)  # Should not raise

    def test_remove_disk_calls_delete_and_unlink(
        self, service: VolumeService, tmp_path: Path
    ):
        """remove_disk should call delete and unlink."""
        path = tmp_path / "test.raw"
        path.write_text("content")
        vol = _make_volume(path=str(path))
        service.remove_disk(vol)
        service._repo.delete.assert_called_once_with(vol.id)
        assert not path.exists()


class TestVolumeServiceResizeDisk:
    def test_resize_raw_calls_fallocate(
        self, service: VolumeService, tmp_path: Path
    ):
        """Resizing a raw disk should call fallocate."""
        path = tmp_path / "test.raw"
        path.write_text("fake")
        vol = _make_volume(path=str(path), fmt="raw")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)

            result = service.resize_disk(vol, 2147483648)

            assert result.size_bytes == 2147483648
            mock_run.assert_called_once()
            args = mock_run.call_args[0][0]
            assert "fallocate" in args
            assert "-l" in args
            assert "2147483648" in args

    def test_resize_qcow2_calls_qemu_img(
        self, service: VolumeService, tmp_path: Path
    ):
        """Resizing a qcow2 disk should call qemu-img resize."""
        path = tmp_path / "test.qcow2"
        path.write_text("fake")
        vol = _make_volume(path=str(path), fmt="qcow2")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)

            result = service.resize_disk(vol, 2147483648)

            assert result is vol
            assert result.size_bytes == 2147483648
            mock_run.assert_called_once()
            args = mock_run.call_args[0][0]
            assert "qemu-img" in args
            assert "resize" in args
            assert "2147483648" in args

    def test_resize_nonexistent_file_raises(
        self, service: VolumeService, tmp_path: Path
    ):
        """Resizing a non-existent file should raise VolumeCreateError."""
        path = tmp_path / "nonexistent.raw"
        vol = _make_volume(path=str(path))
        with pytest.raises(VolumeCreateError, match="Disk file not found"):
            service.resize_disk(vol, 2147483648)

    def test_resize_unsupported_format_raises(
        self, service: VolumeService, tmp_path: Path
    ):
        """Unsupported resize format should raise VolumeCreateError."""
        path = tmp_path / "test.raw"
        path.write_text("fake")
        vol = _make_volume(path=str(path), fmt="vmdk")
        with pytest.raises(VolumeCreateError, match="Unsupported format"):
            service.resize_disk(vol, 2147483648)

    def test_resize_raw_failure_raises(
        self, service: VolumeService, tmp_path: Path
    ):
        """fallocate resize failure should raise VolumeCreateError."""
        path = tmp_path / "test.raw"
        path.write_text("fake")
        vol = _make_volume(path=str(path), fmt="raw")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = subprocess.CalledProcessError(
                1, ["fallocate"], stderr=b"no space"
            )

            with pytest.raises(
                VolumeCreateError, match="fallocate resize failed"
            ):
                service.resize_disk(vol, 2147483648)


class TestVolumeServiceGetDiskInfo:
    def test_get_disk_info_calls_qemu_img(
        self, service: VolumeService, tmp_path: Path
    ):
        """get_disk_info should invoke qemu-img info --output=json."""
        path = tmp_path / "test.raw"
        path.write_text("fake")
        mock_json = '{"format": "raw", "virtual-size": 1073741824, "actual-size": 123456}'
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0, stdout=mock_json)

            result = service.get_disk_info(path)

            mock_run.assert_called_once()
            args = mock_run.call_args[0][0]
            assert "qemu-img" in args
            assert "info" in args
            assert "--output=json" in args
            assert result["format"] == "raw"
            assert result["virtual-size"] == 1073741824

    def test_get_disk_info_passes_text_true(
        self, service: VolumeService, tmp_path: Path
    ):
        """get_disk_info should pass text=True to subprocess.run."""
        path = tmp_path / "test.raw"
        path.write_text("fake")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(
                returncode=0, stdout='{"format": "raw"}'
            )

            service.get_disk_info(path)

            kwargs = mock_run.call_args[1]
            assert kwargs.get("text") is True

    def test_get_disk_info_nonexistent_raises(
        self, service: VolumeService, tmp_path: Path
    ):
        """get_disk_info on non-existent file should raise VolumeCreateError."""
        path = tmp_path / "nonexistent.raw"
        with pytest.raises(VolumeCreateError, match="Disk file not found"):
            service.get_disk_info(path)

    def test_get_disk_info_failure_raises(
        self, service: VolumeService, tmp_path: Path
    ):
        """qemu-img info failure should raise VolumeCreateError."""
        path = tmp_path / "test.raw"
        path.write_text("fake")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = subprocess.CalledProcessError(
                1, ["qemu-img"], stderr="file not found"
            )

            with pytest.raises(VolumeCreateError, match="qemu-img info failed"):
                service.get_disk_info(path)

    def test_get_disk_info_qemu_img_not_found(
        self, service: VolumeService, tmp_path: Path
    ):
        """Missing qemu-img binary should raise VolumeCreateError."""
        path = tmp_path / "test.raw"
        path.write_text("fake")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = FileNotFoundError()

            with pytest.raises(VolumeCreateError, match="qemu-img not found"):
                service.get_disk_info(path)


class TestVolumeServiceCreateDiskMissingBranches:
    """Additional error path tests for create_disk."""

    def test_create_qcow2_called_process_error(
        self, service: VolumeService, tmp_path: Path
    ):
        """qcow2 creation failure should raise VolumeCreateError."""
        path = tmp_path / "test.qcow2"
        vol = _make_volume(path=str(path), size_bytes=1073741824, fmt="qcow2")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = subprocess.CalledProcessError(
                1, ["qemu-img"], stderr=b"invalid size"
            )
            with pytest.raises(VolumeCreateError, match="qemu-img create failed"):
                service.create_disk(vol)


class TestVolumeServiceResizeDiskMissingBranches:
    """Additional error path tests for resize_disk."""

    def test_resize_raw_fallocate_not_found(
        self, service: VolumeService, tmp_path: Path
    ):
        """Missing fallocate should raise VolumeCreateError on raw resize."""
        path = tmp_path / "test.raw"
        path.write_text("fake")
        vol = _make_volume(path=str(path), fmt="raw")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = FileNotFoundError()
            with pytest.raises(
                VolumeCreateError, match="fallocate not found"
            ):
                service.resize_disk(vol, 2147483648)

    def test_resize_qcow2_called_process_error(
        self, service: VolumeService, tmp_path: Path
    ):
        """qemu-img resize failure should raise VolumeCreateError."""
        path = tmp_path / "test.qcow2"
        path.write_text("fake")
        vol = _make_volume(path=str(path), fmt="qcow2")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = subprocess.CalledProcessError(
                1, ["qemu-img"], stderr=b"no space"
            )
            with pytest.raises(
                VolumeCreateError, match="qemu-img resize failed"
            ):
                service.resize_disk(vol, 2147483648)

    def test_resize_qcow2_qemu_img_not_found(
        self, service: VolumeService, tmp_path: Path
    ):
        """Missing qemu-img should raise VolumeCreateError on qcow2 resize."""
        path = tmp_path / "test.qcow2"
        path.write_text("fake")
        vol = _make_volume(path=str(path), fmt="qcow2")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = FileNotFoundError()
            with pytest.raises(
                VolumeCreateError, match="qemu-img not found"
            ):
                service.resize_disk(vol, 2147483648)

    def test_resize_unsupported_format_raises_on_resize(
        self, service: VolumeService, tmp_path: Path
    ):
        """Unsupported resize format should raise VolumeCreateError."""
        path = tmp_path / "test.vmdk"
        path.write_text("fake")
        vol = _make_volume(path=str(path), fmt="vmdk")
        with pytest.raises(VolumeCreateError, match="Unsupported format"):
            service.resize_disk(vol, 2147483648)


class TestVolumeServiceVolumesToDrives:
    """Tests for volumes_to_drives()."""

    def test_volumes_to_drives_empty(self):
        """volumes_to_drives with empty list should return empty list."""
        result = VolumeService.volumes_to_drives([])
        assert result == []

    def test_volumes_to_drives_available(self):
        """volumes_to_drives with available volumes should return drive configs."""
        vol = _make_volume(name="my-vol", path="/volumes/my-vol.raw")

        result = VolumeService.volumes_to_drives([vol])

        assert len(result) == 1
        assert result[0]["drive_id"] == "vol-1"
        assert result[0]["path_on_host"] == "/volumes/my-vol.raw"
        assert result[0]["is_root_device"] is False

    def test_volumes_to_drives_not_available_raises(self):
        """volumes_to_drives with non-available volume should raise."""
        vol = _make_volume(
            name="my-vol", status=VolumeStatus.ATTACHED.value
        )

        with pytest.raises(
            VolumeCreateError, match="is not available"
        ):
            VolumeService.volumes_to_drives([vol])

    def test_volumes_to_drives_multiple(self):
        """volumes_to_drives with multiple volumes should assign sequential IDs."""
        vol1 = _make_volume(name="vol-a", path="/volumes/vol-a.raw")
        vol2 = _make_volume(name="vol-b", path="/volumes/vol-b.raw")

        result = VolumeService.volumes_to_drives([vol1, vol2])

        assert len(result) == 2
        assert result[0]["drive_id"] == "vol-1"
        assert result[0]["path_on_host"] == "/volumes/vol-a.raw"
        assert result[1]["drive_id"] == "vol-2"
        assert result[1]["path_on_host"] == "/volumes/vol-b.raw"


class TestVolumeServiceSetVolumesState:
    """Tests for set_volumes_state()."""

    def test_set_volumes_state_attach_success(self, service: VolumeService):
        """set_volumes_state(ATTACHED) should attach volumes."""
        vol = MagicMock(spec=VolumeItem)
        vol.name = "my-vol"

        from mvmctl.core.volume._controller import VolumeController

        mock_controller = MagicMock(spec=VolumeController)

        with patch(
            "mvmctl.core.volume._controller.VolumeController",
            return_value=mock_controller,
        ) as mock_ctrl_cls:
            service.set_volumes_state(
                volumes=[vol],
                state=VolumeStatus.ATTACHED,
                vm_id="vm-123",
            )

            mock_ctrl_cls.assert_called_once_with(vol, service._repo)
            mock_controller.attach.assert_called_once_with("vm-123")

    def test_set_volumes_state_attach_no_vm_id_raises(self, service: VolumeService):
        """set_volumes_state(ATTACHED) without vm_id should raise ValueError."""
        with pytest.raises(ValueError, match="vm_id is required"):
            service.set_volumes_state(
                volumes=[MagicMock(spec=VolumeItem)],
                state=VolumeStatus.ATTACHED,
            )

    def test_set_volumes_state_detach_success(self, service: VolumeService):
        """set_volumes_state(AVAILABLE) should detach volumes."""
        vol = MagicMock(spec=VolumeItem)
        vol.name = "my-vol"
        vol.status = VolumeStatus.ATTACHED.value

        from mvmctl.core.volume._controller import VolumeController

        mock_controller = MagicMock(spec=VolumeController)

        with patch(
            "mvmctl.core.volume._controller.VolumeController",
            return_value=mock_controller,
        ) as mock_ctrl_cls:
            service.set_volumes_state(
                volumes=[vol],
                state=VolumeStatus.AVAILABLE,
            )

            mock_ctrl_cls.assert_called_once_with(vol, service._repo)
            mock_controller.detach.assert_called_once()

    def test_set_volumes_state_detach_skips_already_detached(
        self, service: VolumeService
    ):
        """set_volumes_state(AVAILABLE) should skip already-detached volumes."""
        vol = MagicMock(spec=VolumeItem)
        vol.name = "my-vol"
        vol.status = VolumeStatus.AVAILABLE.value

        with patch(
            "mvmctl.core.volume._controller.VolumeController"
        ) as mock_ctrl_cls:
            service.set_volumes_state(
                volumes=[vol],
                state=VolumeStatus.AVAILABLE,
            )

            mock_ctrl_cls.assert_not_called()

    def test_set_volumes_state_logs_warning_on_error(
        self, service: VolumeService, caplog
    ):
        """set_volumes_state should log a warning if operation fails."""
        import logging

        caplog.set_level(logging.WARNING)

        vol = MagicMock(spec=VolumeItem)
        vol.name = "my-vol"

        with patch(
            "mvmctl.core.volume._controller.VolumeController",
            side_effect=ValueError("operation failed"),
        ):
            service.set_volumes_state(
                volumes=[vol],
                state=VolumeStatus.ATTACHED,
                vm_id="vm-123",
            )

        assert "Failed to attach volume" in caplog.text
        assert "my-vol" in caplog.text
