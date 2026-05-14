"""Image operations - cross-domain orchestration for image management."""

from __future__ import annotations

import logging
from collections.abc import Callable
from datetime import UTC, datetime
from pathlib import Path
from typing import TYPE_CHECKING, Any

from mvmctl.api.inputs._image_acquire_input import (
    ImageAcquireRequest,
    ImageImportInput,
    ImagePullInput,
)
from mvmctl.api.inputs._image_input import ImageInput
from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.config._service import SettingsService
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.exceptions import (
    ImageAcquireError,
    ImageError,
    RootPartitionDetectionError,
    TieDetectedError,
)
from mvmctl.models import ImageItem, ImageSpec, ImageVersion
from mvmctl.models.provisioner import ProvisionerType
from mvmctl.models.result import BatchResult, OperationResult, ProgressEvent
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.crypto import HashGenerator
from mvmctl.utils.operation_utils import OperationUtils

if TYPE_CHECKING:
    from mvmctl.models.result import NeedsInteraction

logger = logging.getLogger(__name__)


__all__ = ["ImageOperation"]


class ImageOperation:
    """
    Orchestration layer for image operations.

    All methods are @staticmethod — they take Input classes as arguments,
    create Request/Resolved internally, and orchestrate across core modules.
    """

    @staticmethod
    def prune(
        dry_run: bool = False,
        include_all: bool = False,
    ) -> OperationResult[list[str]]:
        """Prune unused images.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL images including default and referenced.

        Returns:
            OperationResult with item list of image IDs that were removed.
        """
        from mvmctl.core.vm._repository import VMRepository

        db = Database()
        repo = ImageRepository(db)

        # Get referenced image IDs from VMs
        vm_repo = VMRepository(db)
        vms = vm_repo.list_all()
        referenced_image_ids: set[str] = set()
        for vm in vms:
            if vm.image_id:
                referenced_image_ids.add(vm.image_id)

        default_item = repo.get_default()
        default_id = default_item.id if default_item else None

        all_images = repo.list_all()
        removed: list[str] = []

        for image in all_images:
            if not include_all:
                if image.id == default_id:
                    continue
                if image.id in referenced_image_ids:
                    continue

            if not dry_run:
                try:
                    from mvmctl.api.inputs._image_input import ImageInput

                    ImageOperation.remove(
                        ImageInput(id=[image.id]),
                        force=include_all,
                    )
                    removed.append(image.id)
                except Exception as e:
                    logger.warning("Failed to remove image %s: %s", image.id, e)
            else:
                removed.append(image.id)

        return OperationResult(
            status="success",
            code="cache.pruned",
            message=f"Pruned {len(removed)} image(s)",
            item=removed,
        )

    @staticmethod
    def pull(
        inputs: ImagePullInput,
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> OperationResult[ImageItem] | NeedsInteraction:
        """
        Pull image from remote URL, handle partition detection/retry, persist to DB.

        Args:
            inputs: ImagePullInput containing spec, output_dir, force, partition,
                   and skip_optimization.
            on_progress: Optional callback for progress events.

        Returns:
            OperationResult with image metadata on success,
            or NeedsInteraction if user interaction is required.

        """
        from mvmctl.core.binary._service import BinaryService
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)
        request = ImageAcquireRequest(inputs=inputs, db=db)
        resolved = request.resolve_pull()
        if resolved.output_dir is None:
            raise ImageError("Failed to resolve output_dir")

        # Resolve cache TTL and ci_version from settings/binary
        if resolved.no_cache:
            cache_ttl: int | None = None
        else:
            cache_ttl = int(
                SettingsService.resolve(
                    db, "defaults.image", "remote_list_cache_ttl"
                )
            )

        binary_service = BinaryService(BinaryRepository(db))
        default_firecracker = binary_service.get_default_firecracker()
        ci_version: str | None = None
        if default_firecracker and default_firecracker.ci_version:
            ci_version = default_firecracker.ci_version

        resolved_ci_version = ci_version or ""

        # Resolve spec
        spec = ImageService.get_specs_for(
            [inputs.type],
            inputs.version,
            resolved.arch,
            cache_ttl_seconds=cache_ttl,
        )[0]

        # Single query for both early-return check and cleanup
        existing_image = repo.get_by_type(spec.id)

        # Early return if image exists and not forcing re-fetch
        if not resolved.force and existing_image is not None:
            # Verify file exists on disk
            images_dir = CacheUtils.get_images_dir()
            resolved_path = images_dir / existing_image.path
            if resolved_path.exists():
                logger.info("Image already exists: %s", existing_image.path)
                if resolved.set_default:
                    repo.set_default(existing_image.id)
                return OperationResult(
                    status="skipped",
                    code="image.already_present",
                    item=existing_image,
                )

        # Generate image ID
        timestamp = datetime.now(tz=UTC).isoformat()
        image_id = HashGenerator.image(spec.id, spec.source, timestamp)
        image_service = ImageService(repo)

        # ORCHESTRATION: download → extract → optimize
        try:
            download_path = image_service.download_image(
                spec,
                image_id,
                resolved.output_dir,
                resolved.force,
                resolved_ci_version,
                progress_callback=OperationUtils.download_progress_bridge(
                    on_progress
                ),
            )
            if on_progress is not None:
                on_progress(
                    ProgressEvent(
                        phase="extract",
                        status="running",
                        message="Extracting image...",
                    )
                )
            provisioner_type = ImageOperation._resolve_image_provisioner()
            logger.info("Preparing & optimizing image...")
            extracted_path = image_service.extract_image(
                download_path,
                image_id,
                resolved.output_dir,
                spec.format,
                partition=resolved.partition,
                disabled_detectors=resolved.disabled_detectors,
                provisioner_type=provisioner_type,
            )
            if on_progress is not None:
                on_progress(
                    ProgressEvent(
                        phase="optimize",
                        status="running",
                        message="Optimizing image...",
                    )
                )
            opt_warnings: list[str] = []
            image_item = image_service.optimize_image(
                extracted_path,
                image_id,
                spec,
                timestamp,
                resolved.skip_optimization,
                provisioner_type=provisioner_type,
                warnings=opt_warnings,
            )
            if on_progress is not None:
                on_progress(
                    ProgressEvent(
                        phase="complete",
                        status="complete",
                        message="Image pull complete.",
                    )
                )

            download_path.unlink(missing_ok=True)
        except (RootPartitionDetectionError, TieDetectedError) as e:
            return OperationResult(
                status="error",
                code="image.acquire_failed",
                message=str(e),
                exception=e,
            )

        repo.upsert(image_item)
        if resolved.set_default:
            repo.set_default(image_item.id)
        elif existing_image is not None and existing_image.is_default:
            repo.set_default(image_item.id)

        # Clean up old image files if the ID changed after successful upsert.
        # Also soft-delete the old DB record so it won't be returned by
        # future queries (the file is gone but the DB still has is_present=1).
        if existing_image is not None and existing_image.id != image_item.id:
            removed = image_service.remove_many_paths([existing_image])
            repo.soft_delete(existing_image.id)
            if removed:
                logger.info(
                    "Cleaned up %d old image file(s) for %s",
                    len(removed),
                    spec.id,
                )

        msg = "Image pulled successfully"
        if opt_warnings:
            msg += f" ({'; '.join(opt_warnings)})"

        return OperationResult(
            status="success",
            code="image.acquired",
            item=image_item,
            message=msg,
        )

    @staticmethod
    def import_(
        inputs: ImageImportInput,
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> OperationResult[ImageItem]:
        """
        Import local image file, convert, persist to DB.

        Args:
            inputs: ImageImportInput containing name, source_path, format,
                   output_dir, force, and partition.
            on_progress: Optional callback for progress events.

        Returns:
            OperationResult with image metadata on success.

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

        # Build temporary spec for processing pipeline
        spec = ImageSpec(
            id=resolved.type,
            image_type=resolved.type,
            version="",
            name=resolved.name or resolved.type,
            arch=resolved.arch,
            source=str(resolved.source_path),
            format=resolved.format,
        )

        # Single query for both early-return check and cleanup
        existing_image = repo.get_by_type(resolved.type)

        # Early return if image exists and not forcing re-import
        if not resolved.force and existing_image is not None:
            images_dir = CacheUtils.get_images_dir()
            resolved_path = images_dir / existing_image.path
            if resolved_path.exists():
                logger.info("Image already exists: %s", existing_image.path)
                if resolved.set_default:
                    repo.set_default(existing_image.id)
                return OperationResult(
                    status="skipped",
                    code="image.already_present",
                    item=existing_image,
                )

        # Generate image ID
        timestamp = datetime.now(tz=UTC).isoformat()
        image_id = HashGenerator.image(
            spec.id, str(resolved.source_path), timestamp
        )
        image_service = ImageService(repo)

        # ORCHESTRATION: extract → optimize
        import_warnings: list[str] = []
        try:
            if on_progress is not None:
                on_progress(
                    ProgressEvent(
                        phase="extract",
                        status="running",
                        message="Extracting image...",
                    )
                )
            provisioner_type = ImageOperation._resolve_image_provisioner()
            extracted_path = image_service.extract_image(
                resolved.source_path,
                image_id,
                resolved.output_dir,
                resolved.format,
                partition=resolved.partition,
                disabled_detectors=resolved.disabled_detectors,
                provisioner_type=provisioner_type,
            )
            if on_progress is not None:
                on_progress(
                    ProgressEvent(
                        phase="optimize",
                        status="running",
                        message="Optimizing image...",
                    )
                )
            image_item = image_service.optimize_image(
                extracted_path,
                image_id,
                spec,
                timestamp,
                resolved.skip_optimization,
                provisioner_type=provisioner_type,
                warnings=import_warnings,
            )
            if on_progress is not None:
                on_progress(
                    ProgressEvent(
                        phase="complete",
                        status="complete",
                        message="Image import complete.",
                    )
                )
        except (RootPartitionDetectionError, TieDetectedError) as e:
            return OperationResult(
                status="error",
                code="image.import_failed",
                message=str(e),
                exception=e,
            )

        repo.upsert(image_item)
        if resolved.set_default:
            repo.set_default(image_item.id)
        elif existing_image is not None and existing_image.is_default:
            repo.set_default(image_item.id)

        # Clean up old image files if the ID changed after successful upsert.
        # Also soft-delete the old DB record so it won't be returned by
        # future queries (the file is gone but the DB still has is_present=1).
        if existing_image is not None and existing_image.id != image_item.id:
            removed = image_service.remove_many_paths([existing_image])
            repo.soft_delete(existing_image.id)
            if removed:
                logger.info(
                    "Cleaned up %d old image file(s) for %s",
                    len(removed),
                    image_item.id,
                )

        import_msg = "Image imported successfully"
        if import_warnings:
            import_msg += f" ({'; '.join(import_warnings)})"

        return OperationResult(
            status="success",
            code="image.imported",
            item=image_item,
            message=import_msg,
        )

    @staticmethod
    def remove(
        inputs: ImageInput, force: bool = False
    ) -> BatchResult[ImageItem]:
        """
        Remove image by ID prefix.

        Args:
            inputs: ImageInput with id_prefix identifiers.
            force: If True, remove even if referenced by VMs.

        Returns:
            BatchResult with per-item results.

        """
        from mvmctl.api.inputs._image_input import ImageRequest
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)
        resolved = ImageRequest(inputs=inputs, db=db).resolve()

        service = ImageService(repo)

        # Batch-enrich all resolved images with VM references
        resolver = ImageResolver(repo, include=["vm"])
        enriched = resolver.enrich(resolved.images)

        items: list[OperationResult[ImageItem]] = []

        for image in enriched:
            try:
                service.remove(image, force=force)
                items.append(
                    OperationResult(
                        status="success",
                        code="image.removed",
                        item=image,
                    )
                )
            except Exception as e:
                items.append(
                    OperationResult(
                        status="error",
                        code="image.remove_failed",
                        message=str(e),
                        item=image,
                        exception=e,
                    )
                )
        return BatchResult(items=items)

    @staticmethod
    def list_(
        inputs: ImageInput | None = None,
        *,
        remote: bool = False,
        no_cache: bool = False,
        type_filter: str | None = None,
    ) -> list[ImageItem] | list[ImageVersion]:
        """
        List images.

        Args:
            inputs: Optional ImageInput with identifiers to filter.
            remote: If True, return available remote images discovered
                    via the version resolver.
                    If False (default), return local cached images from DB.
            no_cache: If True, bypass cached version listings and fetch
                      live from upstream. Only relevant when ``remote=True``.
            type_filter: If set and ``remote=True``, only return versions
                         for this specific image type (e.g. ``"ubuntu"``).

        Returns:
            List of ImageItem (local) or ImageVersion (remote).

        """
        from mvmctl.core._shared import Database
        from mvmctl.core.image._repository import ImageRepository

        db = Database()
        repo = ImageRepository(db)

        if remote:
            # Discover remote images via version resolver
            from mvmctl.core.image._service import ImageService
            from mvmctl.core.image._version_resolver import (
                HttpDirVersionResolver,
            )

            arch = str(SettingsService.resolve(db, "defaults.image", "arch"))
            image_types_config = ImageService.load_image_types_config()

            # If type_filter is set, only pass that single type's config
            if type_filter:
                image_types_config = [
                    c
                    for c in image_types_config
                    if c.get("type") == type_filter
                ]
                if not image_types_config:
                    return []

            # Resolve ci_version from default firecracker binary or fall back
            from mvmctl.core.binary._service import BinaryService

            resolved_ci_version: str | None = None
            try:
                binary_repo = BinaryRepository(db)
                binary_service = BinaryService(binary_repo)
                default_fc = binary_service.get_default_firecracker()
                if default_fc and default_fc.ci_version:
                    resolved_ci_version = default_fc.ci_version
            except Exception:
                pass  # Fall back to resolver's default constant

            cache_ttl: int | None = (
                None
                if no_cache
                else int(
                    SettingsService.resolve(
                        db, "defaults.image", "remote_list_cache_ttl"
                    )
                )
            )

            version_map = HttpDirVersionResolver.resolve(
                image_types_config,
                arch=arch,
                cache_ttl_seconds=cache_ttl,
                ci_version=resolved_ci_version,
            )

            flattened: list[ImageVersion] = []
            for versions in version_map.values():
                flattened.extend(versions)
            return flattened

        # Local images from DB
        if inputs is None:
            from mvmctl.core.image._service import ImageService

            image_service = ImageService(repo)
            return image_service.list_local()

        # Filter by identifiers if provided
        resolver = ImageResolver(repo)
        result = resolver.resolve_many(inputs.id + inputs.type)
        return result.items

    @staticmethod
    def get(inputs: ImageInput) -> ImageItem:
        """
        Get a single image by ID prefix or type.

        Args:
            inputs: ImageInput with id_prefix or type identifiers.

        Returns:
            The resolved ImageItem.

        Raises:
            ImageError: If image not found or ambiguous.

        """
        from mvmctl.api.inputs._image_input import ImageRequest

        db = Database()

        # Resolve identifiers using ImageRequest pattern
        resolved = ImageRequest(inputs=inputs, db=db).resolve()

        if len(resolved.images) > 1:
            raise ImageError("Expected exactly one image identifier")

        return resolved.images[0]

    @staticmethod
    def _image_to_dict(img: ImageItem) -> dict[str, Any]:
        """
        Convert ImageItem to dictionary for JSON output.

        Includes every field from the model (except deleted_at).
        """
        return {
            "id": img.id,
            "type": img.type,
            "name": img.name,
            "arch": img.arch,
            "path": img.path,
            "fs_type": img.fs_type,
            "fs_uuid": img.fs_uuid,
            "compressed_size": img.compressed_size,
            "original_size": img.original_size,
            "compression_ratio": img.compression_ratio,
            "distro": img.distro,
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
            inputs: ImageInput with id_prefix or type identifiers.
            is_json: If True, return a dict suitable for JSON serialization.

        Returns:
            ImageItem or dict representation depending on is_json.

        """
        image_item = ImageOperation.get(inputs)
        if is_json:
            return ImageOperation._image_to_dict(image_item)
        return image_item

    @staticmethod
    def set_default(
        inputs: ImageInput,
    ) -> OperationResult[ImageItem]:
        """
        Set an image as the default.

        Args:
            inputs: ImageInput with id_prefix or type identifiers.

        Returns:
            OperationResult with the image that was set as default.

        """
        from mvmctl.api.inputs._image_input import ImageRequest

        db = Database()
        repo = ImageRepository(db)

        # Resolve identifiers using ImageRequest pattern
        resolved = ImageRequest(inputs=inputs, db=db).resolve()

        if len(resolved.images) > 1:
            raise ImageError("Expected exactly one image identifier")

        image_item = resolved.images[0]
        repo.set_default(image_item.id)

        AuditLog.log("image.set_default", changes={"id": image_item.id[:6]})
        return OperationResult(
            status="success",
            code="image.default_set",
            item=image_item,
        )

    @staticmethod
    def warm(
        inputs: ImageInput,
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> OperationResult[list[Path]]:
        """
        Pre-decompress images to ready pool for fast VM creation.

        This ensures images are decompressed in tmpfs/RAM ahead of time,
        so VM creation can use fast copy instead of waiting for decompression.

        Args:
            inputs: ImageInput with id_prefix or type identifiers.
            on_progress: Optional callback for progress events.

        Returns:
            OperationResult with list of paths to the warmed images.

        """
        from mvmctl.api.inputs._image_input import ImageRequest
        from mvmctl.core.image._service import ImageService

        db = Database()
        repo = ImageRepository(db)
        images = ImageRequest(inputs=inputs, db=db).resolve().images

        if on_progress is not None:
            on_progress(
                ProgressEvent(
                    phase="warm",
                    status="running",
                    message="Warming images...",
                )
            )
        svc = ImageService(repo)
        try:
            warmed_paths = svc.ensure_cached(images)
        except Exception as e:
            return OperationResult(
                status="error",
                code="image.warm_failed",
                message=str(e),
                exception=e,
            )

        if on_progress is not None:
            on_progress(
                ProgressEvent(
                    phase="warm",
                    status="complete",
                    message="Warming complete.",
                )
            )

        for path in warmed_paths:
            logger.info("Image warmed successfully: %s", path)
        return OperationResult(
            status="success",
            code="image.warmed",
            item=warmed_paths,
        )

    @staticmethod
    def _resolve_image_provisioner() -> ProvisionerType:
        """Resolve which provisioner backend to use for image optimization.

        Checks the ``guestfs_enabled`` setting. Returns LOOP_MOUNT by default.
        """
        from mvmctl.core.config._service import SettingsService

        db = Database()
        try:
            guestfs_enabled = SettingsService.resolve(
                db, "settings", "guestfs_enabled"
            )
            if guestfs_enabled:
                return ProvisionerType.GUESTFS
        except Exception:
            pass
        return ProvisionerType.LOOP_MOUNT

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
        item = repo.get_by_type(spec.id)
        if item is None:
            item = repo.get(spec.id)

        if item is not None and item.path:
            candidate = images_dir / item.path
            if candidate.exists():
                return item

        return None
