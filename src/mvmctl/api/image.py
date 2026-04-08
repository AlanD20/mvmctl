"""Image API — unified orchestration for image fetch and import operations.

This module provides the API layer for image management, implementing
unified fetch and import flows with partition detection retry logic.
"""

from __future__ import annotations

import logging
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING, Any

from mvmctl.exceptions import ImageError, RootPartitionDetectionError, TieDetectedError
from mvmctl.utils.fs import get_cache_dir
from mvmctl.utils.full_hash import generate_full_hash_image

if TYPE_CHECKING:
    from mvmctl.core.image import ImageImportResult

logger = logging.getLogger(__name__)


def load_images_config(path: Path) -> list[Any]:
    """Load images configuration from a YAML file.

    Args:
        path: Path to the images.yaml file.

    Returns:
        List of image specifications from the YAML config.
    """
    from mvmctl.core.image import load_images_config as _load_images_config

    return _load_images_config(path)


__all__ = [
    "resolve_image_spec",
    "validate_image_type_selector",
    "find_existing_image_files",
    "register_fetched_image",
    "fetch_image_and_register",
    "import_image_and_register",
    "load_images_config",
]


def resolve_image_spec(images: list[Any], selector: str, version: str | None) -> Any:
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
        raise ImageError(f"Image '{selector}' not found. Available: {available}")

    if version is not None:
        version_matches = [img for img in type_matches if img.version == version]
        if len(version_matches) == 1:
            return version_matches[0]
        if len(version_matches) > 1:
            ids = ", ".join(img.id for img in version_matches)
            raise ImageError(f"Multiple '{selector}' images with version '{version}' found: {ids}")
        versions = ", ".join(sorted({img.version for img in type_matches}))
        raise ImageError(f"No '{selector}' image with version '{version}'. Available: {versions}")

    if len(type_matches) == 1:
        return type_matches[0]

    versions = ", ".join(sorted({img.version for img in type_matches}))
    raise ImageError(f"Multiple '{selector}' images found. Provide version. Available: {versions}")


def validate_image_type_selector(
    image_type: str | None, image_selector: str, images: list[Any]
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
        raise ImageError("--type cannot be used when selector is an image ID")
    raise ImageError("image selector and --type must match when both are provided")


def find_existing_image_files(spec: Any, images_dir: Path) -> list[Path]:
    """Check filesystem + DB for existing files for this image spec.

    Args:
        spec: ImageSpec with id attribute.
        images_dir: Directory to search for image files.

    Returns:
        List of existing Paths.
    """
    from mvmctl.constants import COMPRESSION_EXTENSION_MAP
    from mvmctl.core.metadata import list_image_entries

    compressed_extensions = list(COMPRESSION_EXTENSION_MAP.values())
    existing = [
        images_dir / f"{spec.id}{ext}"
        for ext in compressed_extensions
        if (images_dir / f"{spec.id}{ext}").exists()
    ]
    if existing:
        return existing

    cache_dir = get_cache_dir()
    all_meta = list_image_entries(cache_dir, images_dir, include_missing=False)

    # Find by os_slug match
    for meta_id, meta in all_meta.items():
        if str(meta.get("os_slug", "")) == spec.id:
            filename = str(meta.get("path", ""))
            if filename:
                candidate = images_dir / filename
                if candidate.exists():
                    return [candidate]
            # Try extensions with meta_id
            for ext in compressed_extensions:
                candidate = images_dir / f"{meta_id}{ext}"
                if candidate.exists():
                    return [candidate]

    return []


def register_fetched_image(result: Any, spec: Any) -> str:
    """Persist image to DB after successful fetch/import. Returns full image ID.

    Takes ImageImportResult (from core/image.py) and ImageSpec (from models/image.py).
    Assembles record, generates full hash, upserts via update_image_entry().

    Args:
        result: ImageImportResult with path, fs_type, fs_uuid, sizes, etc.
        spec: ImageSpec with id, name, arch attributes.

    Returns:
        Full 64-character hash ID of the registered image.
    """
    from mvmctl.core.metadata import update_image_entry

    cache_dir = get_cache_dir()
    timestamp = datetime.now(tz=timezone.utc).isoformat()

    full_id = generate_full_hash_image(result.path, spec.id, timestamp)

    fields: dict[str, object] = {
        "pulled_at": timestamp,
        "os_name": spec.name,
        "os_slug": spec.id,
        "full_hash": full_id,
        "path": result.path.name,
        "fs_type": result.fs_type
        if result.fs_type
        else (result.path.suffix.lstrip(".") if result.path.suffix else "unknown"),
        "compressed_format": "zst",
    }

    if result.fs_uuid:
        fields["fs_uuid"] = result.fs_uuid
    if result.compressed_size is not None:
        fields["compressed_size"] = result.compressed_size
    if result.original_size is not None:
        fields["original_size"] = result.original_size
    if result.compression_ratio is not None:
        fields["compression_ratio"] = result.compression_ratio
    if hasattr(spec, "arch") and spec.arch:
        fields["arch"] = spec.arch

    update_image_entry(cache_dir, full_id, **fields)
    logger.info("Registered image: %s", full_id[:6])

    return full_id


def fetch_image_and_register(
    spec: Any,
    output_dir: Path,
    force: bool = False,
    partition: int | None = None,
    skip_optimization: bool = False,
) -> Any:
    """Fetch image from remote URL, handle partition detection/retry, persist to DB.

    Flow:
    1. find_existing_image_files() → skip if exists and not force
    2. core/image.fetch_image(spec, output_dir, force, skip_optimization)
    3. register_fetched_image(result, spec)
    4. return ImageImportResult

    NOTE: partition retry is handled here when partition is provided.

    Args:
        spec: ImageSpec with source URL, format, convert_to, etc.
        output_dir: Directory to store the fetched image.
        force: Re-download even if exists.
        partition: Specific partition number (1-indexed) for retry, or None for auto.
        skip_optimization: Skip shrink and compression, keep plain ext4.

    Returns:
        ImageImportResult with path and metadata.

    Raises:
        ImageError: If fetch fails or partition detection fails (when no_prompt).
    """
    from mvmctl.api.metadata import get_default_binary_entry
    from mvmctl.core.image import fetch_image as _core_fetch_image

    # Check for existing files
    if not force:
        existing = find_existing_image_files(spec, output_dir)
        if existing:
            logger.info("Image already exists: %s", existing[0])
            # Return existing result without re-fetching
            from mvmctl.core.image import detect_filesystem_type, get_filesystem_uuid

            fs_type = detect_filesystem_type(existing[0])
            fs_uuid = get_filesystem_uuid(existing[0])
            return ImageImportResult(path=existing[0], fs_type=fs_type, fs_uuid=fs_uuid)

    # Fetch CI version from default binary for template resolution
    ci_version = ""
    try:
        default_binary = get_default_binary_entry()
        if default_binary is not None:
            _version, binary_meta = default_binary
            raw_ci_version = binary_meta.get("ci_version")
            if isinstance(raw_ci_version, str):
                ci_version = raw_ci_version
    except Exception:
        pass

    try:
        result = _core_fetch_image(
            spec=spec,
            output_dir=output_dir,
            force=force,
            partition=partition,
            skip_optimization=skip_optimization,
            ci_version=ci_version,
        )
    except (RootPartitionDetectionError, TieDetectedError) as exc:
        # Re-raise for CLI to handle prompting and retry
        raise exc

    # Register the fetched image
    register_fetched_image(result, spec)

    return result


def import_image_and_register(
    spec: Any,
    output_dir: Path,
    force: bool = False,
    partition: int | None = None,
) -> Any:
    """Import local image file, convert, persist to DB.

    Same pattern as fetch_image_and_register but for local source files.

    Args:
        spec: ImageImportSpec with source_path, format, convert_to, etc.
        output_dir: Directory to store the imported image.
        force: Overwrite existing image if present.
        partition: Specific partition number (1-indexed) for retry, or None for auto.

    Returns:
        ImageImportResult with path and metadata.

    Raises:
        ImageError: If import fails or partition detection fails.
    """
    from mvmctl.core.image import import_image as _core_import_image

    # Check for existing files
    if not force:
        existing = find_existing_image_files(spec, output_dir)
        if existing:
            logger.info("Image already exists: %s", existing[0])
            from mvmctl.core.image import detect_filesystem_type, get_filesystem_uuid

            fs_type = detect_filesystem_type(existing[0])
            fs_uuid = get_filesystem_uuid(existing[0])
            return ImageImportResult(path=existing[0], fs_type=fs_type, fs_uuid=fs_uuid)

    try:
        result = _core_import_image(
            spec=spec,
            output_dir=output_dir,
            force=force,
            partition=partition,
        )
    except (RootPartitionDetectionError, TieDetectedError) as exc:
        # Re-raise for CLI to handle prompting and retry
        raise exc

    # Register the imported image
    register_fetched_image(result, spec)

    return result
