"""Image management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING, Any, Optional

import typer

from mvmctl.api.image_operations import ImageOperation
from mvmctl.api.inputs._image_input import (
    ImageFetchInput,
    ImageImportInput,
    ImageInput,
)
from mvmctl.constants import (
    DEFAULT_IMAGE_ARCH,
    DEFAULT_IMAGE_IMPORT_FORMAT,
    IMAGE_IMPORT_FORMAT_MAP,
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.exceptions import ImageError
from mvmctl.models.image import ImageItem, ImageSpec
from mvmctl.utils.common import CacheUtils, CommonUtils
from mvmctl.utils.console import (
    print_error,
    print_info,
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
)
from mvmctl.utils.disk_size import (
    format_bytes_human_readable,
)
from mvmctl.utils.fs import (
    get_file_size,
    is_file_missing,
)
from mvmctl.utils.full_hash import shorten_hash

if TYPE_CHECKING:
    pass

image_app = typer.Typer(
    help="Image management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@image_app.callback()
def image_callback(ctx: typer.Context) -> None:
    pass


@image_app.command(name="ls")
def image_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    remote: bool = typer.Option(
        False, "--remote", "-r", help="Show available remote images"
    ),
    name_filter: Optional[str] = typer.Option(
        None, "--name", help="Filter by image name"
    ),
) -> None:
    """List cached images (or available remote images with --remote)."""
    images_dir = CacheUtils.get_images_dir()

    images_dir.mkdir(parents=True, exist_ok=True)

    try:
        images = ImageOperation.load_available_images(
            images_dir / "images.yaml"
        )
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if name_filter:
        images = [
            img
            for img in images
            if name_filter.lower() in img.name.lower()
            or name_filter.lower() in img.id.lower()
        ]

    if remote:
        _output_remote_images(images, images_dir, json_output)
    else:
        _output_local_images(images, images_dir, json_output)


def _output_remote_images(
    images: list[Any], images_dir: Path, json_output: bool
) -> None:
    if json_output:
        typer.echo(
            json.dumps(
                [
                    {
                        "id": img.id,
                        "name": img.name,
                        "format": img.format,
                        "convert_to": img.convert_to,
                    }
                    for img in images
                ],
                indent=2,
            )
        )
        return

    rows: list[list[str]] = []
    for img in images:
        compression = "-"
        size = 0
        display_id = img.id
        rows.append(
            [
                display_id,
                img.name,
                compression,
                img.convert_to,
                format_bytes_human_readable(size) if size > 0 else "-",
            ]
        )

    print_table(
        columns=["Image ID", "Name", "Compression", "FS Type", "Size"],
        rows=rows,
    )


def _output_local_images(
    images: list[Any], images_dir: Path, json_output: bool
) -> None:
    if json_output:
        result: list[dict[str, str]] = []
        for meta_id, meta in _get_images_metadata().items():
            os_slug = str(meta.get("os_slug", meta_id))
            yaml_spec = next((img for img in images if img.id == os_slug), None)
            display_name = (
                yaml_spec.name
                if yaml_spec
                else str(meta.get("os_name", os_slug))
            )
            fs_type = str(
                meta.get(
                    "fs_type", yaml_spec.convert_to if yaml_spec else "unknown"
                )
            )
            result.append(
                {
                    "id": meta_id,
                    "name": display_name,
                    "format": fs_type,
                    "fs_type": fs_type,
                    "added": CommonUtils.human_readable_datetime(
                        str(meta.get("pulled_at", ""))
                        if meta.get("pulled_at")
                        else None
                    )
                    if meta.get("pulled_at")
                    else "-",
                }
            )
        typer.echo(json.dumps(result, indent=2))
        return

    rows: list[list[str]] = []
    all_meta = _get_images_metadata()

    for meta_id, meta in all_meta.items():
        os_slug = str(meta.get("os_slug", meta_id))
        yaml_spec = next((img for img in images if img.id == os_slug), None)
        display_name = (
            yaml_spec.name if yaml_spec else str(meta.get("os_name", os_slug))
        )
        found_path = _resolve_image_file(images_dir, meta_id, meta)
        is_default = bool(meta.get("is_default", 0))
        is_missing = is_file_missing(found_path)
        added = (
            CommonUtils.human_readable_datetime(str(meta.get("pulled_at", "")))
            if meta.get("pulled_at")
            else "-"
        )
        fs_type = str(
            meta.get(
                "fs_type",
                found_path.suffix.lstrip(".") if found_path else "unknown",
            )
        )
        display_id = _get_combined_marker(
            is_default, is_missing
        ) + shorten_hash(meta_id, 12)
        _raw_size = meta.get("compressed_size")
        size = get_file_size(
            found_path,
            int(_raw_size) if isinstance(_raw_size, (int, float)) else 0,
        )
        rows.append(
            [
                display_id,
                display_name,
                fs_type,
                format_bytes_human_readable(size) if size > 0 else "-",
                added,
            ]
        )

    print_table(
        columns=["ID", "OS Name", "FS Type", "Size", "Added"],
        rows=rows,
    )


def _get_combined_marker(is_default: bool, is_missing: bool) -> str:
    """Get combined default and existence marker."""
    if is_default and is_missing:
        return "*X "
    elif is_missing:
        return " X "
    elif is_default:
        return "*  "
    else:
        return "   "


def _get_images_metadata() -> dict[str, dict[str, Any]]:
    """Get all image metadata from the database."""
    try:
        items = ImageOperation.list()
        result: dict[str, dict[str, Any]] = {}
        for item in items:
            result[item.id] = {
                "os_slug": item.os_slug,
                "os_name": item.os_name,
                "path": item.path,
                "fs_type": item.fs_type,
                "is_default": item.is_default,
                "pulled_at": item.pulled_at,
                "compressed_size": item.compressed_size,
            }
        return result
    except Exception:
        return {}


def _resolve_image_file(
    images_dir: Path, image_id: str, meta: dict[str, Any]
) -> Path | None:
    filename = str(meta.get("path", ""))
    if filename:
        candidate = images_dir / filename
        if candidate.exists():
            return candidate
    for ext in SUPPORTED_IMAGE_EXTENSIONS:
        candidate = images_dir / f"{image_id}{ext}"
        if candidate.exists():
            return candidate
    return None


@image_app.command(name="fetch")
def image_fetch(
    image_selector: str = typer.Argument(
        ...,
        help="Image ID or image type from 'mvm image ls --remote' (e.g. ubuntu-24.04 or ubuntu)",
    ),
    image_type: Optional[str] = typer.Option(
        None,
        "--type",
        help="Image type from images.yaml (e.g. ubuntu, debian, firecracker)",
    ),
    version: Optional[str] = typer.Option(
        None,
        "--version",
        help="Image spec version from images.yaml (required if multiple images share the same type)",
    ),
    arch: Optional[str] = typer.Option(
        None,
        "--arch",
        help="Image architecture (e.g. x86_64, arm64)",
    ),
    out: Optional[Path] = typer.Option(None, "--out", help="Output directory"),
    force: bool = typer.Option(
        False, "--force", "-f", help="Re-download even if exists"
    ),
    set_default: bool = typer.Option(
        False, "--set-default", help="Set as default image after download"
    ),
    no_prompt: bool = typer.Option(
        False, "--no-prompt", help="Exit with error on detection failure"
    ),
    skip_optimization: bool = typer.Option(
        False,
        "--skip-optimization",
        help="Skip shrink and compression, keep plain ext4",
    ),
    disable_detector: Optional[str] = typer.Option(
        None,
        "--disable-detector",
        help="Comma-separated detectors to disable: type,label,size,filesystem,all",
    ),
) -> None:
    """Download an image by its ID. Run 'mvm image ls -r' to list available image IDs."""
    images_dir = out if out is not None else CacheUtils.get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)

    # Parse disabled detectors
    disabled_detectors: list[str] = []
    if disable_detector:
        names = [n.strip() for n in disable_detector.split(",")]
        CLI_TO_INTERNAL_DETECTOR = {
            "type": "type_code",
            "label": "label",
            "size": "size",
            "filesystem": "filesystem",
        }
        for detector_name in names:
            if detector_name == "all":
                disabled_detectors = list(CLI_TO_INTERNAL_DETECTOR.values())
                break
            elif detector_name in CLI_TO_INTERNAL_DETECTOR:
                disabled_detectors.append(
                    CLI_TO_INTERNAL_DETECTOR[detector_name]
                )
            else:
                print_error(
                    f"Unknown detector: {detector_name}. Valid: type,label,size,filesystem,all"
                )
                raise typer.Exit(code=1)

    try:
        fetch_input = ImageFetchInput(
            spec=ImageSpec(
                id=image_selector,
                image_type=image_type or image_selector,
                version=version or "",
                name=image_selector,
                source="",
                format="",
                convert_to="",
                arch=arch if arch is not None else DEFAULT_IMAGE_ARCH,
            ),
            output_dir=images_dir,
            force=force,
            skip_optimization=skip_optimization,
            disabled_detectors=disabled_detectors,
        )
        result = ImageOperation.fetch(fetch_input)
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if result is None:
        print_error(f"Failed to download image '{image_selector}'")
        raise typer.Exit(code=1)

    short_id = shorten_hash(result.result.id, 12)
    print_success(f"Image ready: {result.result.path}")
    print_info(f"  ID: {short_id}")
    if set_default:
        try:
            ImageOperation.set_default(ImageInput(id_prefix=[result.result.id]))
            print_success(f"Default image set to: {image_selector}")
        except ImageError:
            pass

    raise typer.Exit(code=0)


@image_app.command(name="set-default")
def image_set_default(
    prefix: str = typer.Argument(..., help="Image ID prefix to set as default"),
    images_dir: Optional[Path] = typer.Option(
        None, "--images-dir", help="Images directory"
    ),
) -> None:
    """Set the default image for VM creation."""
    images_dir = (
        images_dir if images_dir is not None else CacheUtils.get_images_dir()
    )
    images_dir.mkdir(parents=True, exist_ok=True)

    try:
        ImageOperation.set_default(ImageInput(id_prefix=[prefix]))
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Default image set to: {prefix}")


@image_app.command(name="rm")
def image_rm(
    prefixes: Optional[list[str]] = typer.Argument(
        None, help="Image ID prefixes to remove"
    ),
    images_dir: Optional[Path] = typer.Option(
        None, "--images-dir", help="Images directory"
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Remove even if referenced by VMs"
    ),
) -> None:
    """Remove cached images by ID prefix.

    Examples:
        mvm image rm abc123
        mvm image rm abc123 def456
    """
    images_dir = (
        images_dir if images_dir is not None else CacheUtils.get_images_dir()
    )
    effective_ids: list[str] = list(prefixes) if prefixes else []
    if not effective_ids:
        print_error("Provide at least one image ID prefix")
        raise typer.Exit(code=1)

    exit_code = 0

    for prefix in effective_ids:
        try:
            ImageOperation.remove(ImageInput(id_prefix=[prefix]))
            print_success(f"Removed image: {prefix}")
        except ImageError as e:
            print_error(str(e))
            exit_code = 1
            continue

    raise typer.Exit(code=exit_code)


@image_app.command(name="inspect")
def image_inspect(
    prefix: str = typer.Argument(..., help="Image ID prefix to inspect"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    tree: bool = typer.Option(False, "--tree", help="Output in tree format"),
    images_dir: Optional[Path] = typer.Option(
        None, "--images-dir", help="Images directory"
    ),
) -> None:
    """Show detailed information about an image.

    Examples:
        mvm image inspect abc123
        mvm image inspect abc123 --json
        mvm image inspect abc123 --tree
    """
    images_dir = (
        images_dir if images_dir is not None else CacheUtils.get_images_dir()
    )

    try:
        info = ImageOperation.inspect(ImageInput(id_prefix=[prefix]))
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if json_output:
        typer.echo(json.dumps(_image_to_dict(info), indent=2, default=str))
        return

    if tree:
        _print_image_details_tree(info)
    else:
        _print_image_details(info)


def _image_to_dict(img: ImageItem) -> dict[str, Any]:
    """Convert ImageItem to dictionary for JSON output."""
    return {
        "id": img.id,
        "name": img.os_name,
        "os_slug": img.os_slug,
        "arch": img.arch,
        "path": img.path,
        "fs_type": img.fs_type,
        "fs_uuid": img.fs_uuid or "-",
        "is_default": "Yes" if img.is_default else "No",
        "pulled_at": img.pulled_at,
        "created_at": img.created_at,
        "updated_at": img.updated_at,
        "original_size": format_bytes_human_readable(img.original_size)
        if img.original_size
        else "-",
        "compressed_size": format_bytes_human_readable(img.compressed_size)
        if img.compressed_size
        else "-",
        "compression_ratio": f"{img.compression_ratio:.2f}x"
        if img.compression_ratio
        else "-",
        "compressed_format": img.compressed_format or "-",
        "minimum_rootfs_size": f"{img.minimum_rootfs_size_mib} MiB"
        if img.minimum_rootfs_size_mib
        else "-",
    }


def _print_image_details(info: ImageItem) -> None:
    os_slug = info.os_slug
    missing = is_file_missing(Path(info.path)) if info.path else False
    missing_marker = " (missing)" if missing else ""

    print_inspect_header(f"Image: {os_slug}{missing_marker}")

    print_section_header("BASIC INFO")
    print_key_value("ID", info.id)
    print_key_value("Name", info.os_name)
    print_key_value("OS Slug", os_slug)
    print_key_value("Arch", info.arch)
    print_key_value("Default", "Yes" if info.is_default else "No")
    print_key_value(
        "Pulled", CommonUtils.human_readable_datetime(info.pulled_at)
    )

    print_section_header("STORAGE")
    print_key_value("Filename", info.path)
    print_key_value("FS Type", info.fs_type)
    print_key_value("FS UUID", info.fs_uuid or "-")
    print_key_value(
        "File Size",
        format_bytes_human_readable(info.compressed_size)
        if info.compressed_size
        else "-",
    )

    print_section_header("COMPRESSION")
    print_key_value("Format", info.compressed_format or "-")
    print_key_value(
        "Original",
        format_bytes_human_readable(info.original_size)
        if info.original_size
        else "-",
    )
    print_key_value(
        "Compressed",
        format_bytes_human_readable(info.compressed_size)
        if info.compressed_size
        else "-",
    )
    print_key_value(
        "Ratio",
        f"{info.compression_ratio:.2f}x" if info.compression_ratio else "-",
    )

    print_section_header("VM REQUIREMENTS")
    print_key_value(
        "Minimum Disk",
        f"{info.minimum_rootfs_size_mib} MiB"
        if info.minimum_rootfs_size_mib
        else "-",
    )


def _print_image_details_tree(info: ImageItem) -> None:
    os_slug = info.os_slug
    missing = is_file_missing(Path(info.path)) if info.path else False
    missing_marker = " (missing)" if missing else ""

    print(f"{os_slug}{missing_marker}")

    tree_lines = [
        f"├── ID:          {info.id}",
        f"├── Name:        {info.os_name}",
        f"├── OS Slug:     {os_slug}",
        f"├── Arch:        {info.arch}",
        f"├── Default:     {'Yes' if info.is_default else 'No'}",
        f"├── Pulled:      {CommonUtils.human_readable_datetime(info.pulled_at)}",
    ]

    tree_lines.append("├── Storage")
    tree_lines.append(f"│   ├── Filename:  {info.path}")
    tree_lines.append(f"│   ├── FS Type:   {info.fs_type}")
    tree_lines.append(f"│   ├── FS UUID:   {info.fs_uuid or '-'}")
    tree_lines.append(
        f"│   └── File Size: {format_bytes_human_readable(info.compressed_size) if info.compressed_size else '-'}"
    )

    tree_lines.append("├── Compression")
    tree_lines.append(f"│   ├── Format:    {info.compressed_format or '-'}")
    tree_lines.append(
        f"│   ├── Original:  {format_bytes_human_readable(info.original_size) if info.original_size else '-'}"
    )
    tree_lines.append(
        f"│   ├── Compressed: {format_bytes_human_readable(info.compressed_size) if info.compressed_size else '-'}"
    )
    tree_lines.append(
        f"│   └── Ratio:     {f'{info.compression_ratio:.2f}x' if info.compression_ratio else '-'}"
    )

    tree_lines.append("└── VM Requirements")
    tree_lines.append(
        f"    └── Minimum Disk: {info.minimum_rootfs_size_mib} MiB"
        if info.minimum_rootfs_size_mib
        else "    └── Minimum Disk: -"
    )

    for line in tree_lines:
        print(line)


@image_app.command(name="import")
def image_import(
    name: str = typer.Argument(..., help="Display name for the imported image"),
    source_path: Path = typer.Argument(..., help="Path to local image file"),
    format: Optional[str] = typer.Option(
        None,
        "--format",
        help="Image format: qcow2, raw, tar-rootfs, or auto",
    ),
    convert_to: Optional[str] = typer.Option(
        None, "--convert-to", help="Target filesystem format"
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Overwrite existing"
    ),
    set_default: bool = typer.Option(
        False, "--set-default", help="Set as default after import"
    ),
    images_dir: Optional[Path] = typer.Option(
        None, "--images-dir", help="Output directory"
    ),
    disable_detector: Optional[str] = typer.Option(
        None,
        "--disable-detector",
        help="Comma-separated detectors to disable: type,label,size,filesystem,all",
    ),
    no_prompt: bool = typer.Option(
        False, "--no-prompt", help="Exit with error on detection failure"
    ),
) -> None:
    """Import a local image file (qcow2, raw, tar-rootfs). The first argument is a display name."""
    images_dir = (
        images_dir if images_dir is not None else CacheUtils.get_images_dir()
    )

    if not source_path.exists():
        print_error(f"Source file not found: {source_path}")
        raise typer.Exit(code=1)

    # Parse disabled detectors
    disabled_detectors: list[str] = []
    if disable_detector:
        names = [n.strip() for n in disable_detector.split(",")]
        CLI_TO_INTERNAL_DETECTOR = {
            "type": "type_code",
            "label": "label",
            "size": "size",
            "filesystem": "filesystem",
        }
        for detector_name in names:
            if detector_name == "all":
                disabled_detectors = list(CLI_TO_INTERNAL_DETECTOR.values())
                break
            elif detector_name in CLI_TO_INTERNAL_DETECTOR:
                disabled_detectors.append(
                    CLI_TO_INTERNAL_DETECTOR[detector_name]
                )
            else:
                print_error(
                    f"Unknown detector: {detector_name}. Valid: type,label,size,filesystem,all"
                )
                raise typer.Exit(code=1)

    resolved_format: str | None = format
    if resolved_format is None:
        resolved_format = DEFAULT_IMAGE_IMPORT_FORMAT
    if resolved_format == DEFAULT_IMAGE_IMPORT_FORMAT:
        fname = source_path.name.lower()
        resolved_format = next(
            (
                fmt
                for ext, fmt in IMAGE_IMPORT_FORMAT_MAP.items()
                if fname.endswith(ext)
            ),
            None,
        )
    if resolved_format is None:
        print_error(
            f"Cannot auto-detect format from '{source_path.name}'. "
            "Use --format qcow2|raw|tar-rootfs."
        )
        raise typer.Exit(code=1)

    spec = ImageImportInput(
        id="",  # Will be generated by API
        name=name,
        source_path=source_path,
        output_dir=images_dir,
        format=str(resolved_format),
        convert_to=convert_to or "ext4",
        disabled_detectors=disabled_detectors,
        force=force,
    )

    try:
        result = ImageOperation.import_(spec)
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    short_id = shorten_hash(result.result.id, 12)
    print_success(f"Image imported: {result.result.path}")
    print_info(f"  Name: {name}")
    print_info(f"  ID:   {short_id}")

    if set_default:
        try:
            ImageOperation.set_default(ImageInput(id_prefix=[result.result.id]))
            print_success(f"Default image set to: {name}")
        except ImageError:
            pass

    raise typer.Exit(code=0)


@image_app.command(name="warm")
def image_warm(
    image_id: str = typer.Argument(
        ...,
        help="Image ID, hash prefix, or OS slug to warm (e.g., 'ubuntu-24.04', 'abc123')",
    ),
) -> None:
    """Pre-decompress image to ready pool for fast VM creation.

    This command decompresses the image to tmpfs/RAM ahead of time,
    so subsequent VM creations can use fast copy instead of waiting
    for decompression.

    Examples:
        # Warm an image by OS slug:
        mvm image warm ubuntu-24.04

        # Warm by image ID prefix:
        mvm image warm abc123
    """
    try:
        warmed_path = ImageOperation.warm(image_id)
        size_str = format_bytes_human_readable(warmed_path.stat().st_size)
        print_success(f"Image warmed successfully: {image_id}")
        print_info(f"  Path: {warmed_path}")
        print_info(f"  Size: {size_str}")
        print_info("  Ready for fast VM creation!")
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)
    except Exception as e:
        print_error(f"Failed to warm image: {e}")
        raise typer.Exit(code=1)
