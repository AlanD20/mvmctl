"""Image resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.core._shared import RelationEnricher, RelationSpec
from mvmctl.core.image._repository import ImageRepository
from mvmctl.exceptions import ImageNotFoundError
from mvmctl.models import ImageItem

__all__ = [
    "ImageResolver",
    "ImageResolveResult",
]


@dataclass
class ImageResolveResult:
    items: list[ImageItem]
    errors: list[str]
    exit_code: int


class ImageResolver:
    """Resolver for image resources."""

    RELATIONS: dict[str, RelationSpec] = {
        "vm": RelationSpec(
            fk_field="id",
            resolver="vm",
            method="by_image_id",
            relation_name="vms",
            is_reverse=True,
            batch_method="by_image_id_batch",
        ),
    }

    def __init__(
        self,
        repo: ImageRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else ImageRepository()
        self._include = include

    def _enrich(self, images: list[ImageItem]) -> list[ImageItem]:
        """Enrich images with relations if include is set."""
        if self._include and images:
            RelationEnricher().enrich(images, self._include, self.RELATIONS)
        return images

    def by_id(self, image_id: str) -> ImageItem:
        """Resolve by full ID."""
        matches = self._repo.find_by_prefix(image_id)
        if len(matches) == 0:
            raise ImageNotFoundError(f"Image not found: {image_id!r}")
        if len(matches) > 1:
            raise ImageNotFoundError(f"Image ID is ambiguous: {image_id!r}")
        return self._enrich(matches)[0]

    def by_os_slug(self, os_slug: str) -> ImageItem:
        """Resolve by OS slug."""
        db_image = self._repo.get_by_os_slug(os_slug)
        if db_image is None:
            raise ImageNotFoundError(f"Image not found: {os_slug!r}")
        return self._enrich([db_image])[0]

    def get_default(self) -> ImageItem | None:
        """Resolve the default image, or None if not set."""
        image = self._repo.get_default()
        if image is None:
            return None
        return self._enrich([image])[0]

    def resolve(self, value: str) -> ImageItem:
        """Resolve image by os_slug or ID prefix."""
        try:
            image = self.by_os_slug(value)
        except ImageNotFoundError:
            image = self.by_id(value)
        return image

    def resolve_many(self, identifiers: list[str]) -> ImageResolveResult:
        """Resolve multiple image identifiers by os_slug or id."""
        # Deduplicate identifiers while preserving order
        seen_inputs: set[str] = set()
        unique_ids: list[str] = []
        for ident in identifiers:
            if ident not in seen_inputs:
                seen_inputs.add(ident)
                unique_ids.append(ident)

        items: list[ImageItem] = []
        errors: list[str] = []
        resolved_ids: set[str] = set()

        for identifier in unique_ids:
            try:
                item = self.resolve(identifier)
                if item.id not in resolved_ids:
                    resolved_ids.add(item.id)
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        items = self._enrich(items)

        exit_code = 1 if errors and not items else 0
        return ImageResolveResult(
            items=items, errors=errors, exit_code=exit_code
        )


from mvmctl.core._shared import register  # noqa: E402

register("image", lambda: ImageResolver)
