"""Asset management API --- kernels, images, Firecracker binaries.

Provides both granular operations (re-exported from core modules) and
higher-level composite helpers for common workflows.
"""

from __future__ import annotations

import logging
import shutil
from pathlib import Path
from typing import TypedDict, Literal

from fcm.core.binary_manager import (
    BinaryVersion,
    fetch_binary,
    list_local_versions,
    list_remote_versions,
    remove_version,
    set_active_version,
)
from fcm.core.image import fetch_image, load_images_config
from fcm.core.kernel import build_kernel_pipeline
from fcm.exceptions import ImageError
from fcm.utils.fs import get_assets_dir, get_images_dir, get_kernels_dir

logger = logging.getLogger(__name__)

__all__ = [
    "AssetInfo",
    "BinaryVersion",
    "fetch_binary",
    "list_local_versions",
    "list_remote_versions",
    "set_active_version",
    "remove_version",
    "fetch_image",
    "load_images_config",
    "build_kernel_pipeline",
    "setup_assets",
    "pull_kernel",
    "pull_image",
    "list_assets",
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
    version: str = "6.1.102",
    remote_tar_url: str | None = None,
    output_path: Path | None = None,
    build_dir: Path | None = None,
    jobs: int | None = None,
) -> Path:
    """Download and/or build a minimal Linux kernel for Firecracker.

    Args:
        version: The kernel version.
        remote_tar_url: Direct URL to the kernel source tarball.
        output_path: Final destination for the vmlinux binary.
        build_dir: Directory to use for compilation.
        jobs: Parallel build jobs.
    
    Returns:
        Path to the compiled kernel binary.
        
    Raises:
        KernelError: If building or fetching fails.
    """
    if remote_tar_url is None:
        remote_tar_url = f"https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-{version}.tar.xz"
    if output_path is None:
        output_path = get_kernels_dir() / "vmlinux"
    
    if build_dir is None:
        from fcm.utils.fs import get_cache_dir
        build_dir = get_cache_dir() / "kernel-build"
    
    build_kernel_pipeline(
        version=version,
        source_url=remote_tar_url,
        output_path=output_path,
        build_dir=build_dir,
        jobs=jobs,
    )
    return output_path


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
        
    return fetch_image(spec, output_dir, force=force)


def list_assets() -> list[AssetInfo]:
    """Retrieve a consolidated inventory of all local assets (binaries, kernels, images).
    
    Returns:
        List of AssetInfo specifying cache status, sizes, and types.
    """
    assets: list[AssetInfo] = []
    
    # Binaries
    for bv in list_local_versions():
        assets.append({
            "type": "binary",
            "name": bv.version,
            "active": bv.is_active,
            "size_mib": None,
            "details": str(bv.firecracker_path)
        })
        
    # Kernels
    kernels_dir = get_kernels_dir()
    if kernels_dir.exists():
        for kp in kernels_dir.iterdir():
            if kp.is_file() and kp.name.startswith("vmlinux"):
                size_mib = kp.stat().st_size / (1024 * 1024)
                assets.append({
                    "type": "kernel",
                    "name": kp.name,
                    "active": None,
                    "size_mib": size_mib,
                    "details": str(kp)
                })
                
    # Images
    images_dir = get_images_dir()
    yaml_path = get_assets_dir() / "images.yaml"
    try:
        image_specs = load_images_config(yaml_path)
        for spec in image_specs:
            ext4_path = images_dir / f"{spec.id}.ext4"
            btrfs_path = images_dir / f"{spec.id}.btrfs"
            
            exists = ext4_path.exists() or btrfs_path.exists()
            target_path = ext4_path if ext4_path.exists() else btrfs_path
            
            size_mib_out: float | None = None
            if exists:
                size_mib_out = target_path.stat().st_size / (1024 * 1024)
                
            assets.append({
                "type": "image",
                "name": spec.id,
                "active": exists,
                "size_mib": size_mib_out,
                "details": f"Format: {spec.format}"
            })
    except Exception as e:
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
        patterns = [f"{name}.ext4", f"{name}.btrfs", f"{name}.img", f"{name}.raw"]
        found = [images_dir / p for p in patterns if (images_dir / p).exists()]
        
        if not found:
            raise FileNotFoundError(f"No image files found for '{name}'")
            
        for path in found:
            if path.is_dir():
                shutil.rmtree(path)
            else:
                path.unlink()
    else:
        raise ValueError(f"Unknown asset type: {asset_type}")
