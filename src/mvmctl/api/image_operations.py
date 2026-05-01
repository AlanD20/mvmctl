"""Image operations - cross-domain orchestration for image management."""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from mvmctl.api.inputs._image_acquire_input import (
    ImageAcquireRequest,
    ImageFetchInput,
    ImageImportInput,
)
from mvmctl.api.inputs._image_input import ImageInput
from mvmctl.constants import DEFAULT_FIRECRACKER_CI_VERSION
from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.exceptions import (
    ImageAcquireError,
    ImageError,
    RootPartitionDetectionError,
    TieDetectedError,
)
from mvmctl.models.image import ImageItem, ImageSpec
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.crypto import HashGenerator

logger = logging.getLogger(__name__)


__all__ = ["ImageOperation"]


@dataclass
class ImageAcquireResult:
    """Result of image acquire operation — the full ImageItem with generated hash as id."""

    result: ImageItem


class ImageOperation:
    """
    Orchestration layer for image operations.

    All methods are @staticmethod — they take Input classes as arguments,
    create Request/Resolved internally, and orchestrate across core modules.
    """

    @staticmethod
    def fetch(
        inputs: ImageFetchInput,
        *,
        phase_callback: Callable[[str], None] | None = None,
    ) -> ImageAcquireResult:
        """
        Fetch image from remote URL, handle partition detection/retry, persist to DB.

        Args:
            inputs: ImageFetchInput containing spec, output_dir, force, partition,
                   and skip_optimization.

        Returns:
            ImageAcquireResult with image metadata and full hash.

        Raises:
            ImageError: If fetch fails or partition detection fails (when no_prompt).

        """
        from mvmctl.core.binary._service import BinaryService
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)
        request = ImageAcquireRequest(inputs=inputs, db=db)
        resolved = request.resolve_fetch()
        if resolved.output_dir is None:
            raise ImageError("Failed to resolve output_dir")

        # Resolve spec
        spec = ImageService.get_specs_for([inputs.os_slug], inputs.version)[0]

        # Single query for both early-return check and cleanup
        existing_image = repo.get_by_os_slug(spec.id)

        # Early return if image exists and not forcing re-fetch
        if not resolved.force and existing_image is not None:
            # Verify file exists on disk
            images_dir = CacheUtils.get_images_dir()
            resolved_path = images_dir / existing_image.path
            if resolved_path.exists():
                logger.info("Image already exists: %s", existing_image.path)
                return ImageAcquireResult(result=existing_image)

        binary_service = BinaryService(BinaryRepository(db))
        default_firecracker = binary_service.get_default_firecracker()

        # Get CI version for template resolution
        ci_version = DEFAULT_FIRECRACKER_CI_VERSION
        if default_firecracker and default_firecracker.ci_version:
            ci_version = default_firecracker.ci_version

        # Generate image ID
        timestamp = datetime.now(tz=UTC).isoformat()
        image_id = HashGenerator.image(spec.id, spec.source, timestamp)
        image_service = ImageService(repo)

        # ORCHESTRATION: download → extract → optimize
        try:
            download_path = image_service.download_image(
                spec, image_id, resolved.output_dir, resolved.force, ci_version
            )
            if phase_callback:
                phase_callback("extracting")
            extracted_path = image_service.extract_downloaded_image(
                download_path,
                spec,
                image_id,
                resolved.output_dir,
                resolved.partition,
                resolved.disabled_detectors,
            )
            if phase_callback:
                phase_callback("optimizing")
            image_item = image_service.optimize_image(
                extracted_path,
                image_id,
                spec,
                timestamp,
                resolved.skip_optimization,
            )
            if phase_callback:
                phase_callback("complete")

            download_path.unlink(missing_ok=True)
        except (RootPartitionDetectionError, TieDetectedError):
            raise

        image_item.is_default = resolved.set_default
        repo.upsert(image_item)

        # Clean up old image files if the ID changed after successful upsert
        if existing_image is not None and existing_image.id != image_item.id:
            removed = image_service.remove_many_paths([existing_image])
            if removed:
                logger.info(
                    "Cleaned up %d old image file(s) for %s",
                    len(removed),
                    spec.id,
                )

        return ImageAcquireResult(result=image_item)

    @staticmethod
    def import_(inputs: ImageImportInput) -> ImageAcquireResult:
        """
        Import local image file, convert, persist to DB.

        Args:
            inputs: ImageImportInput containing name, source_path, format,
                   output_dir, force, and partition.

        Returns:
            ImageAcquireResult with image metadata and full hash.

        Raises:
            ImageError: If import fails or partition detection fails.

        """
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)

        request = ImageAcquireRequest(inputs=inputs, db=db)
        resolved = request.resolve_import()

        if not resolved.source_path:
            raise ImageAcquireError("Failed to resolve source path")

        if not resolved.format:
            raise ImageAcquireError("Failed to resolve format")

        if not resolved.arch:
            raise ImageAcquireError("Failed to resolve format")

        # Derive image ID from filename: lowercase, snake_case, no extension
        import re

        filename_stem = Path(resolved.source_path).stem
        derived_id = re.sub(r"[\s\-\.]+", "_", filename_stem).lower()

        # Build synthetic spec
        spec = ImageSpec(
            id=derived_id,
            image_type="custom",
            version="",
            name=resolved.os_slug,
            arch=resolved.arch,
            source=str(resolved.source_path),
            format=resolved.format,
        )

        # Single query for both early-return check and cleanup
        existing_image = repo.get_by_os_slug(derived_id)

        # Early return if image exists and not forcing re-import
        if not resolved.force and existing_image is not None:
            images_dir = CacheUtils.get_images_dir()
            resolved_path = images_dir / existing_image.path
            if resolved_path.exists():
                logger.info("Image already exists: %s", existing_image.path)
                return ImageAcquireResult(result=existing_image)

        # Generate image ID
        timestamp = datetime.now(tz=UTC).isoformat()
        image_id = HashGenerator.image(
            spec.id, str(resolved.source_path), timestamp
        )
        image_service = ImageService(repo)

        # ORCHESTRATION: extract → optimize
        try:
            extracted_path = image_service.extract_import_image(
                resolved.source_path,
                image_id,
                resolved.output_dir,
                resolved.format,
                resolved.partition,
                resolved.disabled_detectors,
            )
            image_item = image_service.optimize_image(
                extracted_path,
                image_id,
                spec,
                timestamp,
                resolved.skip_optimization,
            )
        except (RootPartitionDetectionError, TieDetectedError):
            raise

        image_item.is_default = resolved.set_default
        repo.upsert(image_item)

        # Clean up old image files if the ID changed after successful upsert
        if existing_image is not None and existing_image.id != image_item.id:
            removed = image_service.remove_many_paths([existing_image])
            if removed:
                logger.info(
                    "Cleaned up %d old image file(s) for %s",
                    len(removed),
                    derived_id,
                )

        return ImageAcquireResult(result=image_item)

    @staticmethod
    def remove(inputs: ImageInput, force: bool = False) -> None:
        """
        Remove image by ID prefix.

        Args:
            inputs: ImageInput with id_prefix identifiers.
            force: If True, remove even if referenced by VMs.

        Raises:
            ImageError: If image not found or referenced by VMs.

        """
        from mvmctl.api.inputs._image_input import ImageRequest
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)

        resolved = ImageRequest(inputs=inputs, db=db).resolve()
        image_service = ImageService(repo)
        image_service.remove_many(resolved.items, force=force)

    @staticmethod
    def list_(
        inputs: ImageInput | None = None, *, remote: bool = False
    ) -> list[ImageItem] | list[ImageSpec]:
        """
        List images.

        Args:
            inputs: Optional ImageInput with identifiers to filter.
            remote: If True, return available remote images from YAML config.
                    If False (default), return local cached images from DB.

        Returns:
            List of ImageItem (local) or ImageSpec (remote).

        """
        from mvmctl.core._shared import Database
        from mvmctl.core.binary._service import BinaryService
        from mvmctl.core.image._repository import ImageRepository

        db = Database()
        repo = ImageRepository(db)

        if remote:
            # Load remote images from YAML
            from mvmctl.core.image._service import ImageService

            specs = ImageService.load_available_images()
            binary_service = BinaryService(BinaryRepository(db))
            default_firecracker = binary_service.get_default_firecracker()

            # Get CI version for template resolution
            ci_version = DEFAULT_FIRECRACKER_CI_VERSION
            if default_firecracker and default_firecracker.ci_version:
                ci_version = default_firecracker.ci_version

            ImageService.resolve_remote_sizes(specs, ci_version)
            return specs

        # Local images from DB
        if inputs is None:
            from mvmctl.core.image._service import ImageService

            image_service = ImageService(repo)
            return image_service.list_local()

        # Filter by identifiers if provided
        resolver = ImageResolver(repo)
        result = resolver.resolve_many(inputs.id + inputs.os_slug)
        return result.items

    @staticmethod
    def get(inputs: ImageInput) -> ImageItem:
        """
        Get a single image by ID prefix or OS slug.

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
    def _image_to_dict(img: ImageItem) -> dict[str, Any]:
        """
        Convert ImageItem to dictionary for JSON output.

        Includes every field from the model (except deleted_at).
        """
        return {
            "id": img.id,
            "os_slug": img.os_slug,
            "os_name": img.os_name,
            "arch": img.arch,
            "path": img.path,
            "fs_type": img.fs_type,
            "fs_uuid": img.fs_uuid,
            "compressed_size": img.compressed_size,
            "original_size": img.original_size,
            "compression_ratio": img.compression_ratio,
            "compressed_format": img.compressed_format,
            "minimum_rootfs_size_mib": img.minimum_rootfs_size_mib,
            "pulled_at": img.pulled_at,
            "is_default": img.is_default,
            "is_present": img.is_present,
            "created_at": img.created_at,
            "updated_at": img.updated_at,
        }

    @staticmethod
    def inspect(
        inputs: ImageInput, is_json: bool = False
    ) -> ImageItem | dict[str, Any]:
        """
        Inspect an image with enriched data.

        Args:
            inputs: ImageInput with id_prefix or os_slug identifiers.
            is_json: If True, return a dict suitable for JSON serialization.

        Returns:
            ImageItem or dict representation depending on is_json.

        """
        image_item = ImageOperation.get(inputs)
        if is_json:
            return ImageOperation._image_to_dict(image_item)
        return image_item

    @staticmethod
    def set_default(inputs: ImageInput) -> None:
        """
        Set an image as the default.

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

        AuditLog.log("image.set_default", changes={"id": image_item.id[:6]})

    @staticmethod
    def warm(inputs: ImageInput) -> list[Path]:
        """
        Pre-decompress images to ready pool for fast VM creation.

        This ensures images are decompressed in tmpfs/RAM ahead of time,
        so VM creation can use fast copy instead of waiting for decompression.

        Args:
            inputs: ImageInput with id_prefix or os_slug identifiers.

        Returns:
            List of paths to the warmed images in the ready pool.

        Raises:
            ImageError: If any image is not found or warming fails.

        """
        from mvmctl.api.inputs._image_input import ImageRequest
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)
        images = ImageRequest(inputs=inputs, db=db).resolve().items

        svc = ImageService(repo)
        warmed_paths = svc.ensure_cached(images)

        for path in warmed_paths:
            logger.info("Image warmed successfully: %s", path)
        return warmed_paths

    @staticmethod
    def find_existing_image(
        spec: ImageSpec,
        images_dir: Path,
        repo: ImageRepository,
    ) -> ImageItem | None:
        """
        Check database for an existing image for this spec.

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
