"""Image resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.core._internal._db import Database
from mvmctl.exceptions import ImageNotFoundError
from mvmctl.db.models import Image

__all__ = [
    "ImageResolver",
    "ImageResolveResult",
]


@dataclass
class ImageResolveResult:
    items: list[Image]
    errors: list[str]
    exit_code: int


class ImageResolver:
    """Resolver for image resources."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db if db is not None else Database()

    def by_id(self, image_id: str) -> Image:
        """Resolve by full ID."""
        matches = self._db.find_images_by_prefix(image_id)
        if len(matches) == 0:
            raise ImageNotFoundError(f"Image not found: {image_id!r}")
        if len(matches) > 1:
            raise ImageNotFoundError(f"Image ID is ambiguous: {image_id!r}")
        return matches[0]

    def by_os_slug(self, os_slug: str) -> Image:
        """Resolve by OS slug."""
        db_image = self._db.get_image_by_os_slug(os_slug)
        if db_image is None:
            raise ImageNotFoundError(f"Image not found: {os_slug!r}")
        return db_image

    def resolve(self, value: str) -> Image:
        """Resolve image by os_slug or ID prefix."""
        try:
            return self.by_os_slug(value)
        except ImageNotFoundError:
            pass
        return self.by_id(value)

    def resolve_many(self, identifiers: list[str]) -> ImageResolveResult:
        """Resolve multiple image identifiers by os_slug or id."""
        items: list[Image] = []
        errors: list[str] = []

        for identifier in identifiers:
            try:
                item = self.resolve(identifier)
                if item not in items:
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        exit_code = 1 if errors and not items else 0
        return ImageResolveResult(items=items, errors=errors, exit_code=exit_code)
