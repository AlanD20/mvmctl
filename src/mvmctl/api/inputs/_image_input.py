"""Image input models for API boundary."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from mvmctl.core._internal._db import Database
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.exceptions import ImageNotFoundError
from mvmctl.models.image import ImageItem, ImageSpec

__all__ = [
    "ImageInput",
    "ImageImportInput",
    "ImageFetchInput",
    "ImageRequest",
    "ResolvedImageInput",
]


@dataclass
class ImageImportInput:
    """Specification for importing a local image file."""

    id: str
    name: str
    source_path: Path
    output_dir: Path
    format: str  # noqa: N816
    convert_to: str = "ext4"
    disabled_detectors: list[str] = field(default_factory=list)
    force: bool = False
    partition: int | None = None


@dataclass
class ImageFetchInput:
    """Input model for image fetch and registration operations."""

    spec: ImageSpec
    output_dir: Path
    force: bool = False
    partition: int | None = None
    skip_optimization: bool = False
    disabled_detectors: list[str] = field(default_factory=list)


# =====================================================================
# COPIED FROM: api/inputs/_network_input.py — NetworkInput (lines 20-31)
# =====================================================================


@dataclass
class ImageInput:
    """Identifiers for existing image actions (ls, rm, inspect, get)."""

    id_prefix: list[str] = field(default_factory=list)
    os_slug: list[str] = field(default_factory=list)


# =====================================================================
# COPIED FROM: api/inputs/_network_input.py — ResolvedNetworkInput (lines 34-43)
# =====================================================================


@dataclass(frozen=True)
class ResolvedImageInput:
    """Resolved image identifiers."""

    items: list[ImageItem]


# =====================================================================
# COPIED FROM: api/inputs/_network_input.py — NetworkRequest (lines 46-109)
# =====================================================================


class ImageRequest:
    """Request that resolves ImageInput to ImageItem via DB."""

    _result: ResolvedImageInput | None = None

    def __init__(
        self, *, inputs: ImageInput, db: Database | None = None
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._image_resolver = ImageResolver(ImageRepository(self._db))

    @property
    def result(self) -> ResolvedImageInput | None:
        return self._result

    def resolve(self) -> ResolvedImageInput:
        """Resolve identifiers to ImageItem records from DB.

        Returns:
            ResolvedImageInput with resolved image records.

        Raises:
            ImageNotFoundError: If any identifier cannot be resolved.
        """
        identifiers = self._inputs.id_prefix + self._inputs.os_slug

        if not identifiers:
            raise ImageNotFoundError("No image identifiers provided")

        result = self._image_resolver.resolve_many(identifiers)

        if result.errors and not result.items:
            raise ImageNotFoundError(
                f"Could not resolve any images: {', '.join(result.errors)}"
            )

        self._result = ResolvedImageInput(items=result.items)

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved image inputs."""
        if self._result is None:
            raise ImageNotFoundError(
                "Failed to resolve necessary dependencies to validate"
            )

        if not self._result.items:
            raise ImageNotFoundError("No images found matching identifiers")
