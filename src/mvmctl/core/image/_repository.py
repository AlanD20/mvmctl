"""Image database operations - Repository Pattern implementation."""

from __future__ import annotations

from pathlib import Path

from mvmctl.core._internal._db import Database
from mvmctl.db.models import Image


class ImageRepository:
    """Database operations for images."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    def get(self, image_id: str) -> Image | None:
        """Return an image by its full 64-char ID, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute("SELECT * FROM images WHERE id = ?", (image_id,)).fetchone()
        if row is None:
            return None
        return Image(**dict(row))

    def find_by_prefix(self, prefix: str) -> list[Image]:
        """Return all images whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute("SELECT * FROM images WHERE id LIKE ?", (f"{prefix}%",)).fetchall()
        return [Image(**dict(row)) for row in rows]

    def get_by_os_slug(self, os_slug: str) -> Image | None:
        """Return an image by its os_slug, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute("SELECT * FROM images WHERE os_slug = ?", (os_slug,)).fetchone()
        if row is None:
            return None
        return Image(**dict(row))

    def list_all(self) -> list[Image]:
        """Return all images."""
        with self._db.connect() as conn:
            rows = conn.execute("SELECT * FROM images ORDER BY created_at").fetchall()
        return [Image(**dict(row)) for row in rows]

    def upsert(self, image: Image) -> None:
        """Insert or replace an image record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO images (
                    id, os_slug, os_name, arch, path, fs_type, fs_uuid,
                    compressed_size, original_size, compression_ratio,
                    compressed_format, minimum_rootfs_size_mib, pulled_at, is_default, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(os_slug) DO UPDATE SET
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
                    image.created_at,
                    image.updated_at,
                ),
            )

    def delete(self, image_id: str) -> None:
        """Delete an image by ID. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM images WHERE id = ?", (image_id,))

    def set_default(self, image_id: str) -> None:
        """Set one image as default, clearing all others atomically."""
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            conn.execute("UPDATE images SET is_default = 0")
            conn.execute("UPDATE images SET is_default = 1 WHERE id = ?", (image_id,))
            conn.execute("COMMIT")

    def get_default(self) -> Image | None:
        """Return the default image entry, or None if not set."""
        with self._db.connect() as conn:
            row = conn.execute("SELECT * FROM images WHERE is_default = 1 LIMIT 1").fetchone()
        if row is None:
            return None
        return Image(**dict(row))
