"""Image management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING, Optional, cast

import typer

from mvmctl.api.image_operations import ImageOperation
from mvmctl.api.inputs._image_acquire_input import (
    ImageFetchInput,
    ImageImportInput,
)
from mvmctl.api.inputs._image_input import ImageInput
from mvmctl.constants import (
    DEFAULT_IMAGE_IMPORT_FORMAT,
    IMAGE_IMPORT_FORMAT_MAP,
)
from mvmctl.exceptions import ImageError
from mvmctl.models.image import ImageItem, ImageSpec
from mvmctl.utils.common import CommonUtils
from mvmctl.utils.console import (
    print_error,
    print_info,
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
)
from mvmctl.utils.full_hash import HashGenerator
from mvmctl.utils.progress import Spinner

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
) -> None:
    """List cached images (or available remote images with --remote)."""
    try:
        result = ImageOperation.list_(remote=remote)
        if remote:
            _list_remote_images(
                cast(list[ImageSpec], result), json_output=json_output
            )
        else:
            _list_local_images(
                cast(list[ImageItem], result), json_output=json_output
            )
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)


def _list_remote_images(images: list[ImageSpec], *, json_output: bool) -> None:
    """Render remote available images."""
    if json_output:
        data = [
            {
                "id": img.id,
                "name": img.name,
                "format": img.format,
                "size": img.size,
            }
            for img in images
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    rows: list[list[str]] = []
    for img in images:
        size_str = (
            CommonUtils.format_bytes_human_readable(img.size)
            if img.size
            else "-"
        )
        rows.append(
            [
                img.id,
                img.name,
                "-",  # compression
                img.format,
                size_str,
            ]
        )

    print_table(
        columns=["Image ID", "Name", "Compression", "Format", "Size"],
        rows=rows,
    )


def _list_local_images(images: list[ImageItem], *, json_output: bool) -> None:
    """Render locally cached images."""
    if json_output:
        data: list[dict[str, str]] = []
        for img in images:
            added = (
                CommonUtils.human_readable_datetime(img.pulled_at)
                if img.pulled_at
                else "-"
            )
            data.append(
                {
                    "id": img.id,
                    "name": img.os_name,
                    "format": img.fs_type,
                    "fs_type": img.fs_type,
                    "added": added,
                }
            )
        typer.echo(json.dumps(data, indent=2))
        return

    rows: list[list[str]] = []
    for img in images:
        display_id = CommonUtils._get_combined_marker(
            img.is_default, not img.is_present
        ) + HashGenerator.shorten(img.id)
        size = img.compressed_size or 0
        added = (
            CommonUtils.human_readable_datetime(img.pulled_at)
            if img.pulled_at
            else "-"
        )
        rows.append(
            [
                display_id,
                img.os_name,
                img.fs_type,
                CommonUtils.format_bytes_human_readable(size)
                if size > 0
                else "-",
                added,
            ]
        )

    print_table(
        columns=["ID", "OS Name", "FS Type", "Size", "Added"],
        rows=rows,
    )


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
    force: bool = typer.Option(
        False, "--force", "-f", help="Re-download even if exists"
    ),
    set_default: bool = typer.Option(
        False, "--set-default", help="Set as default image after download"
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
    disabled_detectors = (
        [n.strip() for n in disable_detector.split(",") if n.strip()]
        if disable_detector
        else []
    )

    try:
        fetch_input = ImageFetchInput(
            os_slug=image_selector,
            type=image_type or image_selector,
            version=version,
            arch=arch,
            force=force,
            skip_optimization=skip_optimization,
            disabled_detectors=disabled_detectors,
            set_default=set_default,
        )
        spinner = Spinner("Processing")

        def _phase_callback(phase: str) -> None:
            if phase == "extracting":
                spinner.start()
            elif phase in ("complete", "optimizing"):
                spinner.stop()

        result = ImageOperation.fetch(
            fetch_input, phase_callback=_phase_callback
        )
    except ImageError as e:
        spinner.stop()
        print_error(str(e))
        raise typer.Exit(code=1)

    if result is None:
        print_error(f"Failed to download image '{image_selector}'")
        raise typer.Exit(code=1)

    short_id = HashGenerator.shorten(result.result.id)
    print_success(f"Image ready: {result.result.path}")
    print_info(f"  ID: {short_id}")
    if set_default:
        print_success(f"Default image set to: {image_selector}")

    raise typer.Exit(code=0)


@image_app.command(name="set-default")
def image_set_default(
    prefix: str = typer.Argument(..., help="Image ID prefix to set as default"),
) -> None:
    """Set the default image for VM creation."""
    try:
        ImageOperation.set_default(ImageInput(id=[prefix]))
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Default image set to: {prefix}")


@image_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
def image_rm(
    prefixes: Optional[list[str]] = typer.Argument(
        None, help="Image ID prefixes to remove"
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
    effective_ids: list[str] = list(prefixes) if prefixes else []
    if not effective_ids:
        print_error("Provide at least one image ID prefix")
        raise typer.Exit(code=1)

    exit_code = 0

    for prefix in effective_ids:
        try:
            ImageOperation.remove(ImageInput(id=[prefix]), force)
            print_success(f"Removed image: {prefix}")
        except ImageError as e:
            print_error(str(e))
            exit_code = 1
            break

    raise typer.Exit(code=exit_code)


@image_app.command(name="inspect")
def image_inspect(
    prefix: str = typer.Argument(..., help="Image ID prefix to inspect"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    tree: bool = typer.Option(False, "--tree", help="Output in tree format"),
) -> None:
    """Show detailed information about an image.

    Examples:
        mvm image inspect abc123
        mvm image inspect abc123 --json
        mvm image inspect abc123 --tree
    """
    try:
        info = ImageOperation.inspect(
            ImageInput(id=[prefix]), is_json=json_output
        )
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if isinstance(info, dict):
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    if tree:
        _print_image_details_tree(info)
    else:
        _print_image_details(info)


def _print_image_details(info: ImageItem) -> None:
    os_slug = info.os_slug
    missing_marker = " (missing)" if not info.is_present else ""

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
    print_key_value(
        "Created", CommonUtils.human_readable_datetime(info.created_at)
    )
    print_key_value(
        "Updated", CommonUtils.human_readable_datetime(info.updated_at)
    )

    print_section_header("STORAGE")
    print_key_value("Filename", info.path)
    print_key_value("FS Type", info.fs_type)
    print_key_value("FS UUID", info.fs_uuid or "-")
    print_key_value(
        "File Size",
        CommonUtils.format_bytes_human_readable(info.compressed_size)
        if info.compressed_size
        else "-",
    )

    print_section_header("COMPRESSION")
    print_key_value("Format", info.compressed_format or "-")
    print_key_value(
        "Original",
        CommonUtils.format_bytes_human_readable(info.original_size)
        if info.original_size
        else "-",
    )
    print_key_value(
        "Compressed",
        CommonUtils.format_bytes_human_readable(info.compressed_size)
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
    missing_marker = " (missing)" if not info.is_present else ""

    print(f"{os_slug}{missing_marker}")

    tree_lines = [
        f"├── ID:          {info.id}",
        f"├── Name:        {info.os_name}",
        f"├── OS Slug:     {os_slug}",
        f"├── Arch:        {info.arch}",
        f"├── Default:     {'Yes' if info.is_default else 'No'}",
        f"├── Pulled:      {CommonUtils.human_readable_datetime(info.pulled_at)}",
        f"├── Created:     {CommonUtils.human_readable_datetime(info.created_at)}",
        f"├── Updated:     {CommonUtils.human_readable_datetime(info.updated_at)}",
    ]

    tree_lines.append("├── Storage")
    tree_lines.append(f"│   ├── Filename:  {info.path}")
    tree_lines.append(f"│   ├── FS Type:   {info.fs_type}")
    tree_lines.append(f"│   ├── FS UUID:   {info.fs_uuid or '-'}")
    tree_lines.append(
        f"│   └── File Size: {CommonUtils.format_bytes_human_readable(info.compressed_size) if info.compressed_size else '-'}"
    )

    tree_lines.append("├── Compression")
    tree_lines.append(f"│   ├── Format:    {info.compressed_format or '-'}")
    tree_lines.append(
        f"│   ├── Original:  {CommonUtils.format_bytes_human_readable(info.original_size) if info.original_size else '-'}"
    )
    tree_lines.append(
        f"│   ├── Compressed: {CommonUtils.format_bytes_human_readable(info.compressed_size) if info.compressed_size else '-'}"
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
    arch: Optional[str] = typer.Option(
        None,
        "--arch",
        help="Image arch: x86_64, arm64",
    ),
    root_partition: Optional[int] = typer.Option(
        None,
        "--root-partition",
        help="Root Partition: 1, 2, 3",
    ),
    format: Optional[str] = typer.Option(
        DEFAULT_IMAGE_IMPORT_FORMAT,
        "--format",
        help="Image format: qcow2, raw, tar-rootfs, or auto",
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Overwrite existing"
    ),
    set_default: bool = typer.Option(
        False, "--set-default", help="Set as default after import"
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
    """Import a local image file (qcow2, raw, tar-rootfs). The first argument is a display name."""

    if not source_path.exists():
        print_error(f"Source file not found: {source_path}")
        raise typer.Exit(code=1)

    disabled_detectors = (
        [n.strip() for n in disable_detector.split(",") if n.strip()]
        if disable_detector
        else []
    )

    if format == DEFAULT_IMAGE_IMPORT_FORMAT:
        fname = source_path.name.lower()
        format = next(
            (
                fmt
                for ext, fmt in IMAGE_IMPORT_FORMAT_MAP.items()
                if fname.endswith(ext)
            ),
            None,
        )

    if format is None:
        print_error(
            f"Cannot auto-detect format from '{source_path.name}'. "
            "Use --format qcow2|raw|tar-rootfs."
        )
        raise typer.Exit(code=1)

    spec = ImageImportInput(
        name=name,
        arch=arch,
        format=format,
        source_path=source_path,
        partition=root_partition,
        disabled_detectors=disabled_detectors,
        skip_optimization=skip_optimization,
        set_default=set_default,
        force=force,
    )

    try:
        result = ImageOperation.import_(spec)
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    short_id = HashGenerator.shorten(result.result.id)
    print_success(f"Image imported: {result.result.path}")
    print_info(f"  Name: {name}")
    print_info(f"  ID:   {short_id}")

    if set_default:
        print_success(f"Default image set to: {name}")

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
        warmed_paths = ImageOperation.warm(ImageInput(id=[image_id]))
        for path in warmed_paths:
            size_str = CommonUtils.format_bytes_human_readable(
                path.stat().st_size
            )
            print_success(f"Image warmed successfully: {image_id}")
            print_info(f"  Path: {path}")
            print_info(f"  Size: {size_str}")
        print_info("  Ready for fast VM creation!")
    except ImageError as e:
        print_error(str(e))
        raise typer.Exit(code=1)
    except Exception as e:
        print_error(f"Failed to warm image: {e}")
        raise typer.Exit(code=1)
