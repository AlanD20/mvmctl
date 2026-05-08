"""Tests for VolumeController — volume state management."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.core.volume._controller import VolumeController
from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.exceptions import VolumeNotFoundError
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


class TestVolumeController:
    def test_get_returns_volume_when_constructed_with_item(self):
        """get() should return the VolumeItem passed at construction."""
        vol = _make_volume()
        repo = MagicMock(spec=VolumeRepository)
        controller = VolumeController(vol, repo)
        result = controller.get()
        assert result is vol
        assert result.name == "test-vol"

    def test_attach_updates_status_and_vm_id(self):
        """attach() upserts new VolumeItem with status='attached' and vm_id."""
        vol = _make_volume()
        repo = MagicMock(spec=VolumeRepository)
        controller = VolumeController(vol, repo)

        controller.attach("vm-123")

        # Should have called upsert with a new VolumeItem containing the new state
        repo.upsert.assert_called_once()
        upserted: VolumeItem = repo.upsert.call_args[0][0]
        assert upserted.status == "attached"
        assert upserted.vm_id == "vm-123"
        # The original volume's id, name, size, etc. should be preserved
        assert upserted.id == vol.id
        assert upserted.name == vol.name

        # After attach(), get() should return the updated volume
        assert controller.get().status == "attached"
        assert controller.get().vm_id == "vm-123"

    def test_detach_clears_vm_id_and_sets_available(self):
        """detach() upserts new VolumeItem with status='available' and vm_id=None."""
        vol = _make_volume(status="attached", vm_id="vm-123")
        repo = MagicMock(spec=VolumeRepository)
        controller = VolumeController(vol, repo)

        controller.detach()

        repo.upsert.assert_called_once()
        upserted: VolumeItem = repo.upsert.call_args[0][0]
        assert upserted.status == "available"
        assert upserted.vm_id is None

        assert controller.get().status == "available"
        assert controller.get().vm_id is None

    def test_attach_to_different_vm_updates_vm_id(self):
        """Attaching an already-attached volume updates to new vm_id."""
        vol = _make_volume(status="attached", vm_id="vm-old")
        repo = MagicMock(spec=VolumeRepository)
        controller = VolumeController(vol, repo)

        controller.attach("vm-new")

        repo.upsert.assert_called_once()
        upserted: VolumeItem = repo.upsert.call_args[0][0]
        assert upserted.vm_id == "vm-new"
        assert upserted.status == "attached"

    def test_detach_on_already_available_is_idempotent(self):
        """Detaching an already-available volume sets available again (idempotent)."""
        vol = _make_volume(status="available", vm_id=None)
        repo = MagicMock(spec=VolumeRepository)
        controller = VolumeController(vol, repo)

        controller.detach()

        repo.upsert.assert_called_once()
        upserted: VolumeItem = repo.upsert.call_args[0][0]
        assert upserted.status == "available"
        assert upserted.vm_id is None

    def test_constructor_resolves_string_entity(self, mocker):
        """Constructor should resolve a string via VolumeResolver."""
        vol = _make_volume()
        mock_resolver = MagicMock()
        mock_resolver.resolve.return_value = vol
        mocker.patch(
            "mvmctl.core.volume._controller.VolumeResolver",
            return_value=mock_resolver,
        )

        repo = MagicMock(spec=VolumeRepository)
        controller = VolumeController("test-vol", repo)

        assert controller.get() is vol
        mock_resolver.resolve.assert_called_once_with("test-vol")

    def test_constructor_string_raises_on_not_found(self, mocker):
        """Constructor should raise VolumeNotFoundError if string not resolved."""
        mock_resolver = MagicMock()
        mock_resolver.resolve.side_effect = VolumeNotFoundError(
            "Volume not found: 'nonexistent-vol'"
        )
        mocker.patch(
            "mvmctl.core.volume._controller.VolumeResolver",
            return_value=mock_resolver,
        )

        repo = MagicMock(spec=VolumeRepository)
        with pytest.raises(VolumeNotFoundError, match="Volume not found"):
            VolumeController("nonexistent-vol", repo)

    def test_attach_failure_propagates_repo_error(self):
        """If repo.upsert raises, attach() should propagate the exception."""
        vol = _make_volume()
        repo = MagicMock(spec=VolumeRepository)
        repo.upsert.side_effect = RuntimeError("DB error")
        controller = VolumeController(vol, repo)

        with pytest.raises(RuntimeError, match="DB error"):
            controller.attach("vm-123")

    def test_multiple_attach_detach_cycle(self):
        """Volume can cycle through attach/detach multiple times."""
        vol = _make_volume()
        repo = MagicMock(spec=VolumeRepository)
        controller = VolumeController(vol, repo)

        # Attach
        controller.attach("vm-1")
        assert controller.get().status == "attached"
        assert controller.get().vm_id == "vm-1"
        assert repo.upsert.call_count == 1

        # Detach
        controller.detach()
        assert controller.get().status == "available"
        assert controller.get().vm_id is None
        assert repo.upsert.call_count == 2

        # Re-attach to different VM
        controller.attach("vm-2")
        assert controller.get().status == "attached"
        assert controller.get().vm_id == "vm-2"
        assert repo.upsert.call_count == 3
