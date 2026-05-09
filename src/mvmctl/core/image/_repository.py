"""Image database operations - Repository Pattern implementation."""

from __future__ import annotations

from datetime import UTC, datetime

from mvmctl.core._shared._db import Database, _graceful_read
from mvmctl.models import ImageItem


class ImageRepository:
    """Database operations for images."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    @_graceful_read(default=None)
    def get(self, image_id: str) -> ImageItem | None:
        """Return an image by its full 64-char ID, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM images WHERE id = ?", (image_id,)
            ).fetchone()
        if row is None:
            return None
        return ImageItem(**dict(row))

    @_graceful_read(factory=list)
    def find_by_prefix(self, prefix: str) -> list[ImageItem]:
        """Return all images whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM images WHERE id LIKE ? AND deleted_at IS NULL AND is_present = 1",
                (f"{prefix}%",),
            ).fetchall()
        return [ImageItem(**dict(row)) for row in rows]

    @_graceful_read(default=None)
    def get_by_os_slug(self, os_slug: str) -> ImageItem | None:
        """Return an image by its os_slug, preferring the default, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM images WHERE os_slug = ? AND deleted_at IS NULL AND is_present = 1 ORDER BY is_default DESC, created_at DESC",
                (os_slug,),
            ).fetchone()
        if row is None:
            return None
        return ImageItem(**dict(row))

    @_graceful_read(default=None)
    def get_by_name(self, name: str) -> ImageItem | None:
        """Return an image by its display name (os_name), or None if not found.

        This is particularly useful for imported images where the
        os_name is set to the import name but the os_slug is derived
        from the detected filesystem OS.
        """
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM images WHERE os_name = ? AND deleted_at IS NULL AND is_present = 1",
                (name,),
            ).fetchone()
        if row is None:
            return None
        return ImageItem(**dict(row))

    @_graceful_read(factory=list)
    def list_all(self) -> list[ImageItem]:
        """Return all images."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM images WHERE deleted_at IS NULL ORDER BY created_at"
            ).fetchall()
        return [ImageItem(**dict(row)) for row in rows]

    def upsert(self, image: ImageItem) -> None:
        """Insert or replace an image record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO images (
                    id, os_slug, os_name, arch, path, fs_type, fs_uuid,
                    compressed_size, original_size, compression_ratio,
                    compressed_format, minimum_rootfs_size_mib, pulled_at, is_default, is_present, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    os_slug = excluded.os_slug,
                    os_name = excluded.os_name,
                    arch = excluded.arch,
                    path = excluded.path,
                    fs_type = excluded.fs_type,
                    fs_uuid = excluded.fs_uuid,
                    compressed_size = excluded.compressed_size,
                    original_size = excluded.original_size,
                    compression_ratio = excluded.compression_ratio,
                    compressed_format = excluded.compressed_format,
                    minimum_rootfs_size_mib = excluded.minimum_rootfs_size_mib,
                    pulled_at = excluded.pulled_at,
                    is_default = excluded.is_default,
                    is_present = excluded.is_present,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    image.id,
                    image.os_slug,
                    image.os_name,
                    image.arch,
                    image.path,
                    image.fs_type,
                    image.fs_uuid,
                    image.compressed_size,
                    image.original_size,
                    image.compression_ratio,
                    image.compressed_format,
                    image.minimum_rootfs_size_mib,
                    image.pulled_at,
                    int(image.is_default),
                    int(image.is_present),
                    image.created_at,
                    image.updated_at,
                ),
            )

    def soft_delete(self, image_id: str) -> None:
        """Soft-delete an image by setting deleted_at and is_present=0."""
        now = datetime.now(tz=UTC).isoformat()
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE images SET deleted_at = ?, is_present = 0 WHERE id = ?",
                (now, image_id),
            )

    def delete(self, image_id: str) -> None:
        """Delete an image by ID. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM images WHERE id = ?", (image_id,))

    def set_default(self, image_id: str) -> None:
        """Set one image as default, clearing all others atomically."""
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            conn.execute(
                "UPDATE images SET is_default = 0 WHERE deleted_at IS NULL"
            )
            conn.execute(
                "UPDATE images SET is_default = 1 WHERE id = ? AND deleted_at IS NULL",
                (image_id,),
            )
            conn.execute("COMMIT")

    @_graceful_read(default=None)
    def get_default(self) -> ImageItem | None:
        """Return the default image entry, or None if not set."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM images WHERE is_default = 1 AND is_present = 1 LIMIT 1"
            ).fetchone()
        if row is None:
            return None
        return ImageItem(**dict(row))

    def update_many_is_present(
        self, image_ids: list[str], is_present: bool
    ) -> None:
        """Bulk update is_present flag for multiple images."""
        if not image_ids:
            return
        placeholders = ",".join("?" * len(image_ids))
        with self._db.connect() as conn:
            conn.execute(
                f"UPDATE images SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN ({placeholders})",
                [int(is_present)] + list(image_ids),
            )
