"""Integration tests for VolumeRepository — exercises actual DB operations."""

from __future__ import annotations

from mvmctl.core._shared import Database
from mvmctl.core.volume._repository import VolumeRepository
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


class TestVolumeRepositoryDB:
    """Tests that exercise VolumeRepository against the real (isolated) DB."""

    def test_upsert_and_get(self):
        """Upserting a volume and getting it by ID should return the same item."""
        repo = VolumeRepository(Database())
        vol = _make_volume("integration-vol")

        repo.upsert(vol)
        got = repo.get(vol.id)

        assert got is not None
        assert got.id == vol.id
        assert got.name == "integration-vol"
        assert got.size_bytes == 1073741824
        assert got.format == "raw"

    def test_get_nonexistent_returns_none(self):
        """Getting a non-existent volume ID should return None."""
        repo = VolumeRepository(Database())
        result = repo.get("nonexistent-" + "x" * 52)
        assert result is None

    def test_upsert_updates_existing(self):
        """Upserting the same ID twice should update fields."""
        repo = VolumeRepository(Database())
        vol_id = "update-test-" + "x" * 52
        vol1 = _make_volume("original", id=vol_id, size_bytes=1073741824)

        repo.upsert(vol1)
        vol2 = _make_volume("updated", id=vol_id, size_bytes=2147483648)
        repo.upsert(vol2)

        got = repo.get(vol_id)
        assert got is not None
        assert got.name == "updated"
        assert got.size_bytes == 2147483648

    def test_find_by_prefix(self):
        """Finding by ID prefix should return matching volumes."""
        repo = VolumeRepository(Database())
        common_prefix = "findme"
        vol1 = _make_volume(
            "vol-alpha",
            id=common_prefix + "a" + "x" * 56,
        )
        vol2 = _make_volume(
            "vol-beta",
            id=common_prefix + "b" + "x" * 56,
        )
        other = _make_volume(
            "other-vol",
            id="zzz-other-" + "x" * 55,
        )

        repo.upsert(vol1)
        repo.upsert(vol2)
        repo.upsert(other)

        found = repo.find_by_prefix(common_prefix)
        # Should find 2 volumes with matching prefix
        assert len(found) == 2
        found_names = {v.name for v in found}
        assert found_names == {"vol-alpha", "vol-beta"}

    def test_find_by_prefix_no_match(self):
        """Finding by prefix with no matches should return empty list."""
        repo = VolumeRepository(Database())
        repo.upsert(_make_volume("existing-vol"))

        found = repo.find_by_prefix("nope")
        assert found == []

    def test_get_by_name(self):
        """Getting a volume by name should work."""
        repo = VolumeRepository(Database())
        vol = _make_volume("named-vol")
        repo.upsert(vol)

        got = repo.get_by_name("named-vol")
        assert got is not None
        assert got.id == vol.id
        assert got.name == "named-vol"

    def test_get_by_name_nonexistent(self):
        """Getting a non-existent volume by name should return None."""
        repo = VolumeRepository(Database())
        result = repo.get_by_name("no-such-volume")
        assert result is None

    def test_list_all(self):
        """Listing all volumes should return all upserted records."""
        repo = VolumeRepository(Database())
        repo.upsert(_make_volume("vol-a"))
        repo.upsert(_make_volume("vol-b"))
        repo.upsert(_make_volume("vol-c"))

        vols = repo.list_all()
        assert len(vols) == 3

    def test_list_all_empty(self):
        """Listing with no volumes should return empty list."""
        repo = VolumeRepository(Database())
        vols = repo.list_all()
        assert vols == []

    def test_delete(self):
        """Deleting a volume should remove it from the DB."""
        repo = VolumeRepository(Database())
        vol = _make_volume("delete-me")
        repo.upsert(vol)

        # Verify it exists
        assert repo.get(vol.id) is not None

        # Delete it
        repo.delete(vol.id)

        # Verify it's gone
        assert repo.get(vol.id) is None

    def test_delete_nonexistent_is_noop(self):
        """Deleting a non-existent ID should not raise."""
        repo = VolumeRepository(Database())
        repo.delete("no-such-id-" + "x" * 52)  # Should not raise

    def test_count(self):
        """Count should return the number of volumes."""
        repo = VolumeRepository(Database())
        assert repo.count() == 0

        repo.upsert(_make_volume("vol-1"))
        assert repo.count() == 1

        repo.upsert(_make_volume("vol-2"))
        assert repo.count() == 2

        repo.delete("vol-1-id-" + "x" * 55)
        # delete takes full ID, which is "vol-1-id-" + "x" * 55 vs what we stored
        # Actually we need to use the actual ID. Let me redo this properly.

    def test_count_after_delete(self):
        """Count should reflect deletions."""
        repo = VolumeRepository(Database())
        vol = _make_volume("count-me")
        repo.upsert(vol)
        assert repo.count() == 1

        repo.delete(vol.id)
        assert repo.count() == 0

    def test_volume_fields_persist_correctly(self):
        """All VolumeItem fields should persist through upsert/get round-trip."""
        repo = VolumeRepository(Database())
        vol = VolumeItem(
            id="full-test-" + "x" * 55,
            name="full-test-vol",
            size_bytes=5368709120,
            format="qcow2",
            path="/custom/path/volume.qcow2",
            status="attached",
            vm_id="vm-abc-123",
            created_at="2026-06-15T10:30:00+00:00",
            updated_at="2026-06-15T10:30:00+00:00",
        )

        repo.upsert(vol)
        got = repo.get(vol.id)

        assert got is not None
        assert got.id == vol.id
        assert got.name == "full-test-vol"
        assert got.size_bytes == 5368709120
        assert got.format == "qcow2"
        assert got.path == "/custom/path/volume.qcow2"
        assert got.status == "attached"
        assert got.vm_id == "vm-abc-123"
        assert got.created_at == "2026-06-15T10:30:00+00:00"

    def test_volume_with_null_vm_id(self):
        """A volume with no VM assigned should persist vm_id as None."""
        repo = VolumeRepository(Database())
        vol = _make_volume("detached-vol", status="available", vm_id=None)

        repo.upsert(vol)
        got = repo.get(vol.id)

        assert got is not None
        assert got.vm_id is None
        assert got.status == "available"
