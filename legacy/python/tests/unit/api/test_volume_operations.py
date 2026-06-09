"""Tests for VolumeOperation — volume orchestration."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.api.inputs._volume_create_input import (
    VolumeCreateInput,
)
from mvmctl.api.inputs._volume_input import VolumeInput
from mvmctl.api.volume_operations import VolumeOperation
from mvmctl.exceptions import VolumeError, VolumeNotFoundError
from mvmctl.models import VolumeItem


def _make_volume(
    name: str = "test-vol",
    status: str = "available",
    vm_id: str | None = None,
    **kwargs,
) -> VolumeItem:
    # Allow kwargs to override computed defaults
    volume_id = kwargs.pop("id", f"{name}-id-" + "x" * 55)
    return VolumeItem(
        id=volume_id,
        name=name,
        size_bytes=kwargs.pop("size_bytes", 1073741824),
        format=kwargs.pop("format", "raw"),
        path=kwargs.pop("path", f"/volumes/{name}.raw"),
        status=status,
        vm_id=vm_id,
        created_at=kwargs.pop("created_at", "2026-01-01T00:00:00+00:00"),
        updated_at=kwargs.pop("updated_at", "2026-01-01T00:00:00+00:00"),
        **kwargs,
    )


class TestVolumeOperationCreate:
    def test_create_calls_service_and_persists(self, mocker):
        """Create should delegate disk creation to VolumeService, persist to DB."""
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None  # No existing volume
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        mock_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeService",
            return_value=mock_svc,
        )
        # Mock VolumeCreateRequest to return a resolved input
        mock_resolved = MagicMock()
        mock_resolved.name = "my-vol"
        mock_resolved.size_bytes = 1073741824
        mock_resolved.format = "raw"
        mock_resolved.path = Path("/volumes/my-vol.raw")
        mock_resolved.is_read_only = False
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")
        mocker.patch(
            "mvmctl.api.volume_operations.HashGenerator.volume",
            return_value="vol-hash-" + "x" * 55,
        )

        result = VolumeOperation.create(
            VolumeCreateInput(name="my-vol", size="1G")
        )

        assert result.status == "success"
        assert result.code == "volume.created"
        assert result.item is not None
        assert result.item.name == "my-vol"
        # Default is_read_only should be False
        assert result.item.is_read_only is False
        # Service should have been called to create the disk
        mock_svc.create_disk.assert_called_once()

    def test_create_with_read_only_true(self, mocker):
        """Create with read_only=True should produce is_read_only=True on VolumeItem."""
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        mock_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeService",
            return_value=mock_svc,
        )
        mock_resolved = MagicMock()
        mock_resolved.name = "my-vol"
        mock_resolved.size_bytes = 1073741824
        mock_resolved.format = "raw"
        mock_resolved.path = Path("/volumes/my-vol.raw")
        mock_resolved.is_read_only = True
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")
        mocker.patch(
            "mvmctl.api.volume_operations.HashGenerator.volume",
            return_value="vol-hash-" + "x" * 55,
        )

        result = VolumeOperation.create(
            VolumeCreateInput(name="my-vol", size="1G", read_only=True)
        )

        assert result.status == "success"
        assert result.item.is_read_only is True
        mock_svc.create_disk.assert_called_once()
        # Verify the VolumeItem passed to create_disk has is_read_only=True
        created_vol: VolumeItem = mock_svc.create_disk.call_args[0][0]
        assert created_vol.is_read_only is True

    def test_create_with_qcow2_format(self, mocker):
        """Create with format=qcow2 should pass format to service."""
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        mock_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeService",
            return_value=mock_svc,
        )
        mock_resolved = MagicMock()
        mock_resolved.name = "my-vol"
        mock_resolved.size_bytes = 10737418240
        mock_resolved.format = "qcow2"
        mock_resolved.path = Path("/volumes/my-vol.qcow2")
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")
        mocker.patch(
            "mvmctl.api.volume_operations.HashGenerator.volume",
            return_value="vol-hash-" + "x" * 55,
        )

        result = VolumeOperation.create(
            VolumeCreateInput(name="my-vol", size="10G", format="qcow2")
        )

        assert result.status == "success"
        mock_svc.create_disk.assert_called_once()
        # The format should be passed through via VolumeItem
        created_vol: VolumeItem = mock_svc.create_disk.call_args[0][0]
        assert created_vol.format == "qcow2"

    def test_create_existing_name_returns_error(self, mocker):
        """Create with an existing name should return error."""
        mock_request = MagicMock()
        mock_request.resolve.side_effect = VolumeError(
            "Volume 'my-vol' already exists"
        )
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")

        result = VolumeOperation.create(
            VolumeCreateInput(name="my-vol", size="1G")
        )

        assert result.status == "error"
        assert "already exists" in result.message

    def test_create_failure_bubbles_from_service(self, mocker):
        """If VolumeService.create_disk fails, error should propagate."""
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        mock_svc = MagicMock()
        mock_svc.create_disk.side_effect = VolumeError("disk full")
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeService",
            return_value=mock_svc,
        )
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")

        with pytest.raises(VolumeError, match="disk full"):
            VolumeOperation.create(VolumeCreateInput(name="my-vol", size="1G"))


class TestVolumeOperationRemove:
    def test_remove_calls_service_and_deletes_from_db(self, mocker):
        """Remove should delete disk file and DB record."""
        vol = _make_volume()
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        mock_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeService",
            return_value=mock_svc,
        )
        # Mock VolumeRequest to return our volume
        mock_resolved = MagicMock()
        mock_resolved.volumes = [vol]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")

        result = VolumeOperation.remove(VolumeInput(identifiers=["test-vol"]))

        assert result.items[0].is_ok
        assert result.items[0].status == "success"
        assert result.items[0].code == "volume.removed"
        mock_svc.remove.assert_called_once()

    def test_remove_attached_without_force_returns_error(self, mocker):
        """Removing an attached volume without --force should return error."""
        vol = _make_volume(status="attached", vm_id="vm-123")
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        mock_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeService",
            return_value=mock_svc,
        )
        mock_resolved = MagicMock()
        mock_resolved.volumes = [vol]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")

        result = VolumeOperation.remove(
            VolumeInput(identifiers=["test-vol"]), force=False
        )

        assert result.items[0].is_error
        assert "attached" in result.items[0].message
        assert mock_svc.remove.call_count == 0


class TestVolumeOperationList:
    def test_list_returns_all_volumes(self, mocker):
        """List should return all volumes from repository."""
        vols = [_make_volume("vol-1"), _make_volume("vol-2")]
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = vols
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        # list_() creates Database() and VolumeRepository internally
        mocker.patch("mvmctl.api.volume_operations.Database")

        result = VolumeOperation.list_all()

        assert len(result) == 2
        assert result[0].name == "vol-1"
        assert result[1].name == "vol-2"

    def test_list_empty_returns_empty_list(self, mocker):
        """List with no volumes should return empty list."""
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")

        result = VolumeOperation.list_all()
        assert result == []


class TestVolumeOperationResize:
    def test_resize_increases_size(self, mocker):
        """Resize should update volume size in DB and disk."""
        vol = _make_volume(size_bytes=1073741824)
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        mock_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeService",
            return_value=mock_svc,
        )
        # Mock VolumeRequest to return the volume
        mock_vol_resolved = MagicMock()
        mock_vol_resolved.volumes = [vol]
        mock_vol_request = MagicMock()
        mock_vol_request.resolve.return_value = mock_vol_resolved
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRequest",
            return_value=mock_vol_request,
        )
        # Mock VolumeCreateRequest for size parsing
        mock_resolved = MagicMock()
        mock_resolved.size_bytes = 2147483648
        mock_resolved.format = "raw"
        mock_resolved.path = Path("/volumes/test-vol.raw")
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeCreateRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")
        mocker.patch("mvmctl.api.volume_operations.HashGenerator")

        result = VolumeOperation.resize(
            VolumeCreateInput(name="test-vol", size="2G")
        )

        assert result.status == "success"
        assert result.code == "volume.resized"
        # Verify the disk was resized with the volume and new size
        mock_svc.resize_disk.assert_called_once()
        _resized_vol, new_size = mock_svc.resize_disk.call_args[0]
        assert new_size == 2147483648

    def test_resize_nonexistent_volume_raises(self, mocker):
        """Resizing a non-existent volume should raise VolumeNotFoundError."""
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRepository",
            return_value=mock_repo,
        )
        # Mock VolumeRequest to raise VolumeNotFoundError
        mock_vol_request = MagicMock()
        mock_vol_request.resolve.side_effect = VolumeNotFoundError(
            "Volume not found: 'ghost-vol'"
        )
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRequest",
            return_value=mock_vol_request,
        )
        mocker.patch("mvmctl.api.volume_operations.Database")

        with pytest.raises(VolumeNotFoundError, match="Volume not found"):
            VolumeOperation.resize(
                VolumeCreateInput(name="ghost-vol", size="2G")
            )


class TestVolumeOperationGet:
    def test_get_returns_single_volume(self, mocker):
        """get() should return a single volume by name."""
        vol = _make_volume("test-vol")
        mock_resolved = MagicMock()
        mock_resolved.volumes = [vol]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRequest",
            return_value=mock_request,
        )

        result = VolumeOperation.get(VolumeInput(identifiers=["test-vol"]))

        assert result is vol
        assert result.name == "test-vol"

    def test_get_multiple_items_raises(self, mocker):
        """get() should raise VolumeNotFoundError if multiple volumes match."""
        vol1 = _make_volume("vol-1")
        vol2 = _make_volume("vol-2")
        mock_resolved = MagicMock()
        mock_resolved.volumes = [vol1, vol2]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeRequest",
            return_value=mock_request,
        )

        with pytest.raises(VolumeNotFoundError, match="Expected exactly one"):
            VolumeOperation.get(VolumeInput(identifiers=["ambiguous"]))


class TestVolumeOperationInspect:
    def test_inspect_returns_volume_with_disk_info(self, mocker):
        """inspect() should return volume metadata plus disk info."""
        vol = _make_volume("test-vol")
        # Mock get() to return the volume
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeOperation.get",
            return_value=vol,
        )
        mock_svc = MagicMock()
        mock_svc.get_disk_info.return_value = {
            "format": "raw",
            "virtual-size": 1073741824,
        }
        mocker.patch(
            "mvmctl.api.volume_operations.VolumeService",
            return_value=mock_svc,
        )
        mocker.patch("mvmctl.api.volume_operations.VolumeRepository")
        mocker.patch("mvmctl.api.volume_operations.Database")

        result = VolumeOperation.inspect(VolumeInput(identifiers=["test-vol"]))

        assert isinstance(result, dict)
        assert result["volume"]["name"] == "test-vol"
        assert result["volume"]["id"] == vol.id
        assert result["disk_info"]["format"] == "raw"
        assert result["disk_info"]["virtual-size"] == 1073741824
        mock_svc.get_disk_info.assert_called_once()
