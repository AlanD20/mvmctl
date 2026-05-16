"""Tests for VolumeInput, VolumeCreateInput, and their Request classes."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.api.inputs._volume_create_input import (
    ResolvedVolumeCreateInput,
    VolumeCreateInput,
    VolumeCreateRequest,
)
from mvmctl.api.inputs._volume_input import (
    ResolvedVolumeInput,
    VolumeInput,
    VolumeRequest,
)
from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.exceptions import VolumeError, VolumeNotFoundError
from mvmctl.models import VolumeItem


class TestVolumeInput:
    def test_default_empty_identifiers(self):
        """VolumeInput should default to empty identifiers list."""
        inp = VolumeInput()
        assert inp.identifiers == []

    def test_with_identifiers(self):
        """VolumeInput with identifiers provided."""
        inp = VolumeInput(identifiers=["vol-1", "my-vol"])
        assert inp.identifiers == ["vol-1", "my-vol"]

    def test_with_single_identifier(self):
        """VolumeInput with a single identifier."""
        inp = VolumeInput(identifiers=["my-vol"])
        assert inp.identifiers == ["my-vol"]

    def test_with_multiple_identifiers(self):
        """VolumeInput with multiple identifiers."""
        inp = VolumeInput(identifiers=["vol-1", "my-vol", "abc123"])
        assert inp.identifiers == ["vol-1", "my-vol", "abc123"]


class TestVolumeRequest:
    def _make_vol_item(self, name: str = "test-vol") -> VolumeItem:
        return VolumeItem(
            id=f"{name}-id-" + "x" * 55,
            name=name,
            size_bytes=1073741824,
            format="raw",
            path=f"/volumes/{name}.raw",
            status="available",
            vm_id=None,
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
        )

    def test_resolve_with_names(self):
        """VolumeRequest should resolve by name."""
        vol = self._make_vol_item("my-vol")
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = MagicMock(
            items=[vol],
            errors=[],
            exit_code=0,
        )

        with patch(
            "mvmctl.api.inputs._volume_input.VolumeResolver",
            return_value=mock_resolver,
        ):
            request = VolumeRequest(
                inputs=VolumeInput(identifiers=["my-vol"]),
                db=MagicMock(),
            )
            result = request.resolve()
            assert isinstance(result, ResolvedVolumeInput)
            assert result.volumes == [vol]

    def test_resolve_empty_identifiers_raises(self):
        """VolumeRequest with no identifiers should raise VolumeNotFoundError."""
        with pytest.raises(
            VolumeNotFoundError, match="No volume identifiers provided"
        ):
            request = VolumeRequest(
                inputs=VolumeInput(),
                db=MagicMock(),
            )
            request.resolve()

    def test_resolve_all_fail_raises(self):
        """VolumeRequest when all resolve fail should raise VolumeNotFoundError."""
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = MagicMock(
            items=[],
            errors=["vol-1: not found", "vol-2: not found"],
            exit_code=1,
        )

        with patch(
            "mvmctl.api.inputs._volume_input.VolumeResolver",
            return_value=mock_resolver,
        ):
            request = VolumeRequest(
                inputs=VolumeInput(identifiers=["vol-1", "vol-2"]),
                db=MagicMock(),
            )
            with pytest.raises(
                VolumeNotFoundError, match="Could not resolve any volumes"
            ):
                request.resolve()

    def test_ensure_validate_raises_when_no_items(self):
        """ensure_validate should raise if result has no items."""
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = MagicMock(
            items=[],
            errors=[],
            exit_code=0,
        )

        with patch(
            "mvmctl.api.inputs._volume_input.VolumeResolver",
            return_value=mock_resolver,
        ):
            request = VolumeRequest(
                inputs=VolumeInput(identifiers=["ghost"]),
                db=MagicMock(),
            )
            with pytest.raises(
                VolumeNotFoundError,
                match="No volumes found matching identifiers",
            ):
                request.resolve()

    def test_resolved_volume_input(self):
        """ResolvedVolumeInput should hold volume items."""
        vol = self._make_vol_item("test")
        resolved = ResolvedVolumeInput(volumes=[vol])
        assert resolved.volumes == [vol]
        assert len(resolved.volumes) == 1

    def test_request_default_db_creation(self, _setup_database):
        """VolumeRequest creates its own Database() if none provided."""
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = MagicMock(
            items=self._make_vol_item("v"),
            errors=[],
            exit_code=0,
        )
        with patch(
            "mvmctl.api.inputs._volume_input.VolumeResolver",
            return_value=mock_resolver,
        ):
            request = VolumeRequest(inputs=VolumeInput(identifiers=["v"]))
            assert request._db is not None
            # Should resolve with no errors
            request.resolve()

    def test_ensure_validate_with_none_result_raises(self):
        """Calling ensure_validate directly with _result=None should raise."""
        request = VolumeRequest(
            inputs=VolumeInput(identifiers=["test"]),
            db=MagicMock(),
        )
        # _result is None before resolve
        with pytest.raises(
            VolumeNotFoundError,
            match="Failed to resolve necessary dependencies",
        ):
            request.ensure_validate()


class TestVolumeCreateInput:
    def test_create_input_defaults(self):
        """VolumeCreateInput should default format to None."""
        inp = VolumeCreateInput(name="my-vol", size="1G")
        assert inp.name == "my-vol"
        assert inp.size == "1G"
        assert inp.format is None

    def test_create_input_with_format(self):
        """VolumeCreateInput with explicit format."""
        inp = VolumeCreateInput(name="my-vol", size="1G", format="qcow2")
        assert inp.format == "qcow2"


class TestVolumeCreateRequest:
    def test_resolve_raw_default(self):
        """VolumeCreateRequest should default format to 'raw'."""
        request = VolumeCreateRequest(
            inputs=VolumeCreateInput(name="my-vol", size="1G"),
            db=MagicMock(),
        )
        # Mock get_by_name to return None so ensure_validate passes
        with patch.object(VolumeRepository, "get_by_name", return_value=None):
            resolved = request.resolve()
        assert resolved.name == "my-vol"
        assert resolved.size_bytes == 1073741824
        assert resolved.format == "raw"
        assert isinstance(resolved.path, Path)
        assert resolved.path.name == "my-vol.raw"

    def test_resolve_qcow2_format(self):
        """VolumeCreateRequest should pass through qcow2 format."""
        request = VolumeCreateRequest(
            inputs=VolumeCreateInput(name="my-vol", size="10G", format="qcow2"),
            db=MagicMock(),
        )
        with patch.object(VolumeRepository, "get_by_name", return_value=None):
            resolved = request.resolve()
        assert resolved.format == "qcow2"
        assert resolved.path.name == "my-vol.qcow2"
        assert resolved.size_bytes == 10737418240

    def test_resolve_unsupported_format_raises(self):
        """VolumeCreateRequest should raise for unsupported format."""
        request = VolumeCreateRequest(
            inputs=VolumeCreateInput(name="my-vol", size="1G", format="vmdk"),
            db=MagicMock(),
        )
        with pytest.raises(VolumeError, match="Unsupported format"):
            request.resolve()

    def test_ensure_validate_before_resolve_raises(self):
        """ensure_validate before resolve should raise."""
        request = VolumeCreateRequest(
            inputs=VolumeCreateInput(name="my-vol", size="1G"),
        )
        with pytest.raises(
            VolumeError, match="Failed to resolve necessary dependencies"
        ):
            request.ensure_validate()

    def test_resolved_create_input(self):
        """ResolvedVolumeCreateInput should hold resolved values."""
        resolved = ResolvedVolumeCreateInput(
            name="my-vol",
            size_bytes=1073741824,
            format="raw",
            path=Path("/volumes/my-vol.raw"),
        )
        assert resolved.name == "my-vol"
        assert resolved.size_bytes == 1073741824
        assert resolved.format == "raw"
        assert str(resolved.path) == "/volumes/my-vol.raw"

    def test_resolve_invalid_name_raises(self):
        """VolumeCreateRequest should validate name."""
        from mvmctl.exceptions import MVMError

        request = VolumeCreateRequest(
            inputs=VolumeCreateInput(name="", size="1G"),
            db=MagicMock(),
        )
        with pytest.raises(MVMError, match="cannot be empty"):
            request.resolve()

    def test_result_property(self):
        """Result property should return None before resolve."""
        request = VolumeCreateRequest(
            inputs=VolumeCreateInput(name="my-vol", size="1G"),
        )
        assert request.result is None

    def test_result_property_after_resolve(self):
        """Result property should return resolved after resolve."""
        request = VolumeCreateRequest(
            inputs=VolumeCreateInput(name="my-vol", size="1G"),
            db=MagicMock(),
        )
        with patch.object(VolumeRepository, "get_by_name", return_value=None):
            resolved = request.resolve()
        assert request.result is resolved
