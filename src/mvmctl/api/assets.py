"""Asset management API --- kernels, images, Firecracker binaries.

Provides both granular operations (re-exported from core modules) and
higher-level composite helpers for common workflows.
"""

from __future__ import annotations

import logging
import shutil
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Literal, TypedDict

import yaml

from mvmctl.constants import (
    CONST_MEBIBYTE_BYTES,
    DEFAULT_KERNEL_VERSION,
    DEFAULT_MAX_PARALLEL_DOWNLOADS,
    KERNEL_TARBALL_URL_TEMPLATE,
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.core import kernel as kernel_core
from mvmctl.core.binary_manager import (
    BinaryVersion,
    fetch_binary,
    list_local_versions,
    list_remote_versions,
    remove_version,
    set_active_version,
)
from mvmctl.core.image import fetch_image, get_filesystem_uuid, import_image, load_images_config
from mvmctl.core.kernel import (
    build_kernel_pipeline,
    download_firecracker_kernel,
    get_default_kernel_path,
    list_kernels,
    resolve_kernel_spec,
    set_default_kernel,
)
from mvmctl.exceptions import ConfigError, ImageError
from mvmctl.models.image import ImageImportSpec, ImageSpec
from mvmctl.utils.fs import get_assets_dir, get_images_dir, get_kernels_dir
from mvmctl.utils.time import human_readable_time

logger = logging.getLogger(__name__)

__all__ = [
    "AssetInfo",
    "BinaryVersion",
    "ImageSpec",
    "fetch_binary",
    "list_local_versions",
    "list_remote_versions",
    "set_active_version",
    "remove_version",
    "fetch_image",
    "import_image",
    "fetch_images_parallel",
    "load_images_config",
    "get_filesystem_uuid",
    "ImageImportSpec",
    "build_kernel_pipeline",
    "list_kernels",
    "set_default_kernel",
    "get_default_kernel_path",
    "resolve_kernel_spec",
    "download_firecracker_kernel",
    "human_readable_time",
    "setup_assets",
    "pull_kernel",
    "pull_image",
    "pull_images",
    "list_assets",
    "kernel_core",
    "remove_asset",
]


class AssetInfo(TypedDict):
    type: Literal["binary", "kernel", "image"]
    name: str
    active: bool | None
    size_mib: float | None
    details: str | None


def setup_assets(
    version: str,
    bin_dir: Path | None = None,
) -> BinaryVersion:
    """Fetch Firecracker binaries and set them as the active version.

    This is a convenience composite that combines ``fetch_binary`` and
    ``set_active_version`` into a single call, suitable for initial
    setup workflows.

    Args:
        version: Firecracker release version to fetch (e.g. ``"1.5.0"``).
        bin_dir: Override binary cache directory.  Uses the default
            cache location when *None*.

    Returns:
        The :class:`BinaryVersion` for the fetched/activated binaries.

    Raises:
        BinaryError: If the download or extraction fails.
    """
    bv = fetch_binary(version, bin_dir=bin_dir)
    set_active_version(version, bin_dir=bin_dir)
    logger.info("Firecracker %s fetched and set as active", version)
    return bv


def pull_kernel(
    version: str = DEFAULT_KERNEL_VERSION,
    remote_tar_url: str | None = None,
    output_path: Path | None = None,
    build_dir: Path | None = None,
    jobs: int | None = None,
) -> kernel_core.KernelPipelineResult:
    """Download and/or build a minimal Linux kernel for Firecracker.

    Args:
        version: The kernel version.
        remote_tar_url: Direct URL to the kernel source tarball.
        output_path: Final destination for the vmlinux binary.
        build_dir: Directory to use for compilation.
        jobs: Parallel build jobs.

    Returns:
        KernelPipelineResult with build directory and any warnings/info messages.

    Raises:
        KernelError: If building or fetching fails.
    """
    if remote_tar_url is None:
        remote_tar_url = KERNEL_TARBALL_URL_TEMPLATE.format(version=version)
    if output_path is None:
        output_path = get_kernels_dir() / "vmlinux"

    if build_dir is None:
        from mvmctl.utils.fs import get_cache_dir

        build_dir = get_cache_dir() / "kernel-build"

    return build_kernel_pipeline(
        version=version,
        source_url=remote_tar_url,
        output_path=output_path,
        build_dir=build_dir,
        jobs=jobs,
    )


def pull_image(
    image_id: str,
    force: bool = False,
    images_yaml: Path | None = None,
    output_dir: Path | None = None,
) -> Path:
    """Fetch and provision a rootfs image via its ID in images.yaml.

    Args:
        image_id: ID of the image in the YAML configuration.
        force: Redownload even if it exists locally.
        images_yaml: Override path to the images configuration.
        output_dir: Override rootfs destination directory.

    Returns:
        Path to the provisioned image file.

    Raises:
        ConfigError: If the YAML cannot be located.
        ImageError: If the image cannot be resolved or fetched.
    """
    if images_yaml is None:
        images_yaml = get_assets_dir() / "images.yaml"
    if output_dir is None:
        output_dir = get_images_dir()

    images = load_images_config(images_yaml)
    spec = next((img for img in images if img.id == image_id), None)

    if not spec:
        raise ImageError(f"Image ID '{image_id}' not found in {images_yaml}")

    result = fetch_image(spec, output_dir, force=force)
    return result.path


def fetch_images_parallel(
    specs: list[ImageSpec],
    output_dir: Path,
    force: bool = False,
    max_workers: int = DEFAULT_MAX_PARALLEL_DOWNLOADS,
) -> list[Path]:
    """Fetch multiple images concurrently using a thread pool.

    Args:
        specs: Image specifications to fetch.
        output_dir: Directory to store downloaded/converted images.
        force: Re-download even if images already exist.
        max_workers: Maximum number of concurrent download threads.

    Returns:
        List of paths to the fetched images (order matches *specs*).

    Raises:
        ImageError: If one or more fetches fail.  The message lists
            every individual failure.
    """
    results: dict[int, Path] = {}
    errors: list[str] = []

    with ThreadPoolExecutor(max_workers=max_workers) as pool:
        future_to_idx = {
            pool.submit(fetch_image, spec, output_dir, force): idx for idx, spec in enumerate(specs)
        }
        for future in as_completed(future_to_idx):
            idx = future_to_idx[future]
            try:
                import_result = future.result()
                results[idx] = import_result.path
            except Exception as exc:
                errors.append(f"{specs[idx].id}: {exc}")

    if errors:
        raise ImageError("Failed to fetch the following images:\n" + "\n".join(errors))

    return [results[i] for i in range(len(specs))]


def pull_images(
    image_ids: list[str],
    force: bool = False,
    images_yaml: Path | None = None,
    output_dir: Path | None = None,
    max_workers: int = DEFAULT_MAX_PARALLEL_DOWNLOADS,
) -> list[Path]:
    """Fetch multiple image IDs in parallel.

    Args:
        image_ids: IDs of images defined in the YAML configuration.
        force: Re-download even if images already exist.
        images_yaml: Override path to the images configuration.
        output_dir: Override rootfs destination directory.
        max_workers: Maximum number of concurrent download threads.

    Returns:
        List of paths to the fetched images (order matches *image_ids*).

    Raises:
        ImageError: If any image ID is unknown or fetching fails.
    """
    if images_yaml is None:
        images_yaml = get_assets_dir() / "images.yaml"
    if output_dir is None:
        output_dir = get_images_dir()

    all_specs = load_images_config(images_yaml)
    specs_by_id = {s.id: s for s in all_specs}

    missing = [iid for iid in image_ids if iid not in specs_by_id]
    if missing:
        raise ImageError(f"Image IDs not found in {images_yaml}: {', '.join(missing)}")

    specs = [specs_by_id[iid] for iid in image_ids]
    return fetch_images_parallel(specs, output_dir, force=force, max_workers=max_workers)


def list_assets() -> list[AssetInfo]:
    """Retrieve a consolidated inventory of all local assets (binaries, kernels, images).

    Returns:
        List of AssetInfo specifying cache status, sizes, and types.
    """
    assets: list[AssetInfo] = []

    # Binaries
    for bv in list_local_versions():
        assets.append(
            {
                "type": "binary",
                "name": bv.version,
                "active": bv.is_active,
                "size_mib": None,
                "details": str(bv.firecracker_path),
            }
        )

    # Kernels
    kernels_dir = get_kernels_dir()
    if kernels_dir.exists():
        for kp in kernels_dir.iterdir():
            if kp.is_file() and kp.name.startswith("vmlinux"):
                size_mib = kp.stat().st_size / CONST_MEBIBYTE_BYTES
                assets.append(
                    {
                        "type": "kernel",
                        "name": kp.name,
                        "active": None,
                        "size_mib": size_mib,
                        "details": str(kp),
                    }
                )

    # Images
    images_dir = get_images_dir()
    yaml_path = get_assets_dir() / "images.yaml"
    try:
        image_specs = load_images_config(yaml_path)
        for spec in image_specs:
            candidate_paths = [images_dir / f"{spec.id}{ext}" for ext in SUPPORTED_IMAGE_EXTENSIONS]
            target_path = next((p for p in candidate_paths if p.exists()), None)
            exists = target_path is not None

            size_mib_out: float | None = None
            if exists and target_path is not None:
                size_mib_out = target_path.stat().st_size / CONST_MEBIBYTE_BYTES

            assets.append(
                {
                    "type": "image",
                    "name": spec.id,
                    "active": exists,
                    "size_mib": size_mib_out,
                    "details": f"Format: {spec.format}",
                }
            )
    except (ConfigError, ImageError, yaml.YAMLError, KeyError, OSError) as e:
        logger.warning("Failed to parse images.yaml for list_assets: %s", e)

    return assets


def remove_asset(asset_type: Literal["binary", "kernel", "image"], name: str) -> None:
    """Delete a managed local asset.

    Args:
        asset_type: Distinct asset classification.
        name: Name/ID of the component (e.g. '1.5.0' for binary, 'ubuntu-22.04' for image).

    Raises:
        AssetNotFoundError: Plumbed through from binary removals.
        FileNotFoundError: For missing kernels or images.
    """
    if asset_type == "binary":
        remove_version(name)

    elif asset_type == "kernel":
        target = get_kernels_dir() / name
        if target.exists():
            target.unlink()
        else:
            raise FileNotFoundError(f"Kernel {name} not found")

    elif asset_type == "image":
        images_dir = get_images_dir()
        found = [
            images_dir / f"{name}{ext}"
            for ext in SUPPORTED_IMAGE_EXTENSIONS
            if (images_dir / f"{name}{ext}").exists()
        ]

        if not found:
            raise FileNotFoundError(f"No image files found for '{name}'")

        for path in found:
            if path.is_dir():
                shutil.rmtree(path)
            else:
                path.unlink()
    else:
        raise ValueError(f"Unknown asset type: {asset_type}")
