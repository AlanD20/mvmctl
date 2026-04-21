"""Image operations - cross-domain orchestration for image management."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

# Import from archive during migration — will be moved to proper location
from mvmctl.api.archive.metadata import get_default_binary_entry
from mvmctl.api.inputs._image_input import (
    ImageFetchInput,
    ImageImportInput,
    ImageInput,
)
from mvmctl.core._internal._db import Database
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.exceptions import (
    ImageError,
    RootPartitionDetectionError,
    TieDetectedError,
)
from mvmctl.models.image import ImageItem, ImageSpec
from mvmctl.utils.audit import log_audit
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.full_hash import HashGenerator

logger = logging.getLogger(__name__)

__all__ = ["ImageOperation"]


@dataclass
class ImageFetchResult:
    """Result of fetch operation — the full ImageItem with generated hash as id."""

    result: ImageItem


class ImageOperation:
    """Orchestration layer for image operations.

    All methods are @staticmethod — they take Input classes as arguments,
    create Request/Resolved internally, and orchestrate across core modules.
    """

    @staticmethod
    def fetch(inputs: ImageFetchInput) -> ImageFetchResult:
        """Fetch image from remote URL, handle partition detection/retry, persist to DB.

        Args:
            inputs: ImageFetchInput containing spec, output_dir, force, partition,
                   and skip_optimization.

        Returns:
            ImageFetchResult with image metadata and full hash.

        Raises:
            ImageError: If fetch fails or partition detection fails (when no_prompt).
        """
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)
        image_service = ImageService(repo)

        # Resolve spec
        spec = image_service.get_specs_for([inputs.spec.id])[0]

        # Generate image ID
        timestamp = datetime.now(tz=timezone.utc).isoformat()
        image_id = HashGenerator.image(spec.id, spec.source, timestamp)

        # Check existing
        if not inputs.force:
            existing = ImageOperation._find_existing_image(
                spec, inputs.output_dir, repo
            )
            if existing is not None:
                logger.info("Image already exists: %s", existing.path)
                return ImageFetchResult(result=existing)

        # Get CI version for template resolution
        ci_version = ""
        try:
            default_binary = get_default_binary_entry()
            if default_binary is not None and isinstance(
                default_binary.ci_version, str
            ):
                ci_version = default_binary.ci_version
        except Exception:
            pass

        # ORCHESTRATION: download → extract → optimize
        try:
            download_path = image_service.download_image(
                spec, image_id, inputs.output_dir, inputs.force, ci_version
            )
            extracted_path = image_service.extract_downloaded_image(
                download_path,
                spec,
                image_id,
                inputs.output_dir,
                inputs.partition,
                inputs.disabled_detectors,
            )
            image_item = image_service.optimize_image(
                extracted_path,
                image_id,
                spec,
                timestamp,
                inputs.skip_optimization,
            )
            download_path.unlink(missing_ok=True)
        except (RootPartitionDetectionError, TieDetectedError):
            raise

        repo.upsert(image_item)
        return ImageFetchResult(result=image_item)

    @staticmethod
    def import_(inputs: ImageImportInput) -> ImageFetchResult:
        """Import local image file, convert, persist to DB.

        Args:
            inputs: ImageImportInput containing id, name, source_path, format,
                   convert_to, output_dir, force, and partition.

        Returns:
            ImageFetchResult with image metadata and full hash.

        Raises:
            ImageError: If import fails or partition detection fails.
        """
        from mvmctl.constants import DEFAULT_IMAGE_ARCH
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)
        image_service = ImageService(repo)

        # Derive image ID from filename: lowercase, snake_case, no extension
        import re

        filename_stem = Path(inputs.source_path).stem
        derived_id = re.sub(r"[\s\-\.]+", "_", filename_stem).lower()

        # Build synthetic spec
        spec = ImageSpec(
            id=derived_id,
            image_type="custom",
            version="",
            name=inputs.name,
            source=str(inputs.source_path),
            format=inputs.format,
            convert_to=inputs.convert_to,
            arch=DEFAULT_IMAGE_ARCH,
        )

        # Generate image ID
        timestamp = datetime.now(tz=timezone.utc).isoformat()
        image_id = HashGenerator.image(
            spec.id, str(inputs.source_path), timestamp
        )

        # Check existing
        if not inputs.force:
            existing = ImageOperation._find_existing_image(
                spec, inputs.output_dir, repo
            )
            if existing is not None:
                logger.info("Image already exists: %s", existing.path)
                return ImageFetchResult(result=existing)

        # ORCHESTRATION: extract → optimize
        try:
            extracted_path = image_service.extract_import_image(
                inputs.source_path,
                image_id,
                inputs.output_dir,
                inputs.format,
                inputs.convert_to,
                inputs.partition,
                inputs.disabled_detectors,
            )
            image_item = image_service.optimize_image(
                extracted_path,
                image_id,
                spec,
                timestamp,
                skip_optimization=False,
            )
        except (RootPartitionDetectionError, TieDetectedError):
            raise

        repo.upsert(image_item)
        return ImageFetchResult(result=image_item)

    @staticmethod
    def remove(inputs: ImageInput) -> None:
        """Remove image by ID prefix.

        Args:
            inputs: ImageInput with id_prefix identifiers.

        Raises:
            ImageError: If image not found or ambiguous prefix.
        """
        from mvmctl.api.inputs._image_input import ImageRequest
        from mvmctl.constants import SUPPORTED_IMAGE_EXTENSIONS
        from mvmctl.utils.common import CacheUtils

        images_dir = CacheUtils.get_images_dir()

        db = Database()
        repo = ImageRepository(db)

        # Resolve identifiers using ImageRequest pattern
        resolved = ImageRequest(inputs=inputs, db=db).resolve()

        for image_item in resolved.items:
            full_key = image_item.id
            filename = image_item.path
            files_to_remove: list[Path] = []

            if filename:
                candidate = images_dir / filename
                if candidate.exists():
                    files_to_remove.append(candidate)

            if not files_to_remove:
                files_to_remove = [
                    images_dir / f"{full_key}{ext}"
                    for ext in SUPPORTED_IMAGE_EXTENSIONS
                    if (images_dir / f"{full_key}{ext}").exists()
                ]

            if files_to_remove:
                for path in files_to_remove:
                    if path.is_dir():
                        import shutil

                        shutil.rmtree(path)
                    else:
                        path.unlink()

            repo.delete(full_key)

            log_audit("image.remove", f"id={full_key[:6]}")

    @staticmethod
    def list(
        inputs: ImageInput | None = None, *, remote: bool = False
    ) -> list[ImageItem] | list[ImageSpec]:
        """List images.

        Args:
            inputs: Optional ImageInput with identifiers to filter.
            remote: If True, return available remote images from YAML config.
                    If False (default), return local cached images from DB.

        Returns:
            List of ImageItem (local) or ImageSpec (remote).
        """
        from mvmctl.core._internal._db import Database
        from mvmctl.core.image._repository import ImageRepository

        db = Database()
        repo = ImageRepository(db)

        if remote:
            # Load remote images from YAML
            from mvmctl.core.image._service import ImageService

            image_service = ImageService(repo)
            return image_service.load_available_images(
                CacheUtils.get_images_dir() / "images.yaml"
            )

        # Local images from DB
        if inputs is None:
            return repo.list_all()

        # Filter by identifiers if provided
        resolver = ImageResolver(repo)
        result = resolver.resolve_many(inputs.id_prefix + inputs.os_slug)
        return result.items  # type: ignore

    @staticmethod
    def get(inputs: ImageInput) -> ImageItem:
        """Get a single image by ID prefix or OS slug.

        Args:
            inputs: ImageInput with id_prefix or os_slug identifiers.

        Returns:
            The resolved ImageItem.

        Raises:
            ImageError: If image not found or ambiguous.
        """
        from mvmctl.api.inputs._image_input import ImageRequest

        db = Database()

        # Resolve identifiers using ImageRequest pattern
        resolved = ImageRequest(inputs=inputs, db=db).resolve()

        if len(resolved.items) > 1:
            raise ImageError("Expected exactly one image identifier")

        return resolved.items[0]

    @staticmethod
    def inspect(inputs: ImageInput) -> ImageItem:
        """Inspect an image with enriched data.

        Args:
            inputs: ImageInput with id_prefix or os_slug identifiers.

        Returns:
            The resolved ImageItem.
        """
        return ImageOperation.get(inputs)

    @staticmethod
    def set_default(inputs: ImageInput) -> None:
        """Set an image as the default.

        Args:
            inputs: ImageInput with id_prefix or os_slug identifiers.
        """
        from mvmctl.api.inputs._image_input import ImageRequest

        db = Database()
        repo = ImageRepository(db)

        # Resolve identifiers using ImageRequest pattern
        resolved = ImageRequest(inputs=inputs, db=db).resolve()

        if len(resolved.items) > 1:
            raise ImageError("Expected exactly one image identifier")

        image_item = resolved.items[0]
        repo.set_default(image_item.id)

        log_audit("image.set_default", f"id={image_item.id[:6]}")

    @staticmethod
    def set_default_by_id(image_id: str) -> None:
        """Set default image by full image ID.

        Args:
            image_id: The full 64-character image ID.
        """
        db = Database()
        repo = ImageRepository(db)

        image_item = repo.get(image_id)
        if image_item is None:
            raise ImageError(f"Image not found: {image_id[:6]}")

        repo.set_default(image_item.id)

        log_audit("image.set_default_by_id", f"id={image_id[:6]}")

    @staticmethod
    def warm(image_selector: str) -> Path:
        """Pre-decompress image to ready pool for fast VM creation.

        This ensures the image is decompressed in tmpfs/RAM ahead of time,
        so VM creation can use fast copy instead of waiting for decompression.

        Args:
            image_selector: Image ID, hash prefix, or OS slug (e.g., "ubuntu-24.04", "abc123")

        Returns:
            Path to the warmed image in ready pool

        Raises:
            ImageError: If image not found or warming fails
        """
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)
        resolver = ImageResolver(repo)

        # Find the image by ID prefix or OS slug
        try:
            image_item = resolver.by_os_slug(image_selector)
        except Exception:
            image_item = resolver.by_id(image_selector)

        svc = ImageService(repo)
        warmed_paths = svc.ensure_cached([image_item])
        warmed_path = warmed_paths[0]

        logger.info("Image warmed successfully: %s", warmed_path)
        return warmed_path

    # =====================================================================
    # COPIED FROM: src/mvmctl/api/archive/image.py — resolve_image_spec (lines 186-226)
    # =====================================================================

    @staticmethod
    def resolve_image_spec(
        images: list[ImageSpec], selector: str, version: str | None
    ) -> ImageSpec:
        """Resolve ImageSpec from YAML config by selector and optional version.

        Tries exact ID match first, then falls back to image_type matching with
        optional version disambiguation.

        Args:
            images: List of ImageSpec objects loaded from images.yaml.
            selector: The image ID or image_type to resolve.
            version: Optional version string to disambiguate multiple type matches.

        Returns:
            The matching ImageSpec.

        Raises:
            ImageError: If no match or ambiguous match is found.
        """
        spec = next((img for img in images if img.id == selector), None)
        if spec is not None:
            return spec

        type_matches = [img for img in images if img.image_type == selector]
        if not type_matches:
            available = ", ".join(img.id for img in images)
            raise ImageError(
                f"Image '{selector}' not found. Available: {available}"
            )

        if version is not None:
            version_matches = [
                img for img in type_matches if img.version == version
            ]
            if len(version_matches) == 1:
                return version_matches[0]
            if len(version_matches) > 1:
                ids = ", ".join(img.id for img in version_matches)
                raise ImageError(
                    f"Multiple '{selector}' images with version '{version}' found: {ids}"
                )
            versions = ", ".join(sorted({img.version for img in type_matches}))
            raise ImageError(
                f"No '{selector}' image with version '{version}'. Available: {versions}"
            )

        if len(type_matches) == 1:
            return type_matches[0]

        versions = ", ".join(sorted({img.version for img in type_matches}))
        raise ImageError(
            f"Multiple '{selector}' images found. Provide version. Available: {versions}"
        )

    # =====================================================================
    # COPIED FROM: src/mvmctl/api/archive/image.py — validate_image_type_selector (lines 229-246)
    # =====================================================================

    @staticmethod
    def validate_image_type_selector(
        image_type: str | None, image_selector: str, images: list[ImageSpec]
    ) -> None:
        """Raise ImageError if --type and selector conflict.

        Args:
            image_type: The --type option value, if provided.
            image_selector: The image selector argument.
            images: List of ImageSpec objects for validation.

        Raises:
            ImageError: If --type conflicts with selector.
        """
        if image_type is None or image_selector == image_type:
            return
        if any(img.id == image_selector for img in images):
            raise ImageError(
                "--type cannot be used when selector is an image ID"
            )
        raise ImageError(
            "image selector and --type must match when both are provided"
        )

    @staticmethod
    def _find_existing_image(
        spec: ImageSpec | ImageImportInput,
        images_dir: Path,
        repo: ImageRepository,
    ) -> ImageItem | None:
        """Check database for an existing image for this spec.

        Args:
            spec: ImageSpec with id attribute.
            images_dir: Directory to search for image files.
            repo: ImageRepository to query for existing records.

        Returns:
            The existing ImageItem if found on disk, otherwise None.
        """
        item = repo.get_by_os_slug(spec.id)
        if item is None:
            item = repo.get(spec.id)

        if item is not None and item.path:
            candidate = images_dir / item.path
            if candidate.exists():
                return item

        return None
