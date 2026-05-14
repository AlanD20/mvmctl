"""Image management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING, Any, cast

import typer
from rich.console import Console

from mvmctl.api import ImageImportInput as _ImageImportInput
from mvmctl.api import ImageInput as _ImageInput
from mvmctl.api import ImageOperation as _ImageOperation
from mvmctl.api import ImagePullInput as _ImagePullInput
from mvmctl.cli._completion import (
    _complete_local_image_ids,
    _complete_remote_image_ids,
)
from mvmctl.constants import IMAGE_IMPORT_FORMAT_MAP
from mvmctl.models import ImageItem, ImageVersion
from mvmctl.models.result import (
    NeedsInteraction,
    ProgressEvent,
)

if TYPE_CHECKING:
    from mvmctl.api.image_operations import ImageOperation
    from mvmctl.api.inputs._image_acquire_input import (
        ImageImportInput,
        ImagePullInput,
    )
    from mvmctl.api.inputs._image_input import ImageInput
else:
    ImageOperation = _ImageOperation
    ImagePullInput = _ImagePullInput
    ImageImportInput = _ImageImportInput
    ImageInput = _ImageInput
from mvmctl.utils._io import (
    print_error,
    print_info,
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
)
from mvmctl.utils.cli import handle_errors
from mvmctl.utils.common import CommonUtils
from mvmctl.utils.crypto import HashGenerator

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
@handle_errors
def image_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    remote: bool = typer.Option(
        False, "--remote", "-r", help="Show available remote images"
    ),
    no_cache: bool = typer.Option(
        False,
        "--no-cache",
        help="Skip cached version listing and fetch live from upstream",
    ),
    type_filter: str | None = typer.Option(
        None, "--type", help="Filter by image type (e.g. ubuntu, alpine)"
    ),
) -> None:
    """List cached images (or available remote images with --remote)."""
    if remote:
        with Console().status("Fetching remote images"):
            result = ImageOperation.list_(
                remote=True,
                no_cache=no_cache,
                type_filter=type_filter,
            )
        _list_remote_images(
            cast(list[ImageVersion], result), json_output=json_output
        )
    else:
        result = ImageOperation.list_(remote=False)
        _list_local_images(
            cast(list[ImageItem], result), json_output=json_output
        )


def _list_remote_images(
    versions: list[ImageVersion], *, json_output: bool
) -> None:
    """Render remote available images grouped by type."""
    if json_output:
        data = [
            {
                "version": v.version,
                "codename": v.codename,
                "type": v.type,
                "display_name": v.display_name or v.version,
                "download_url": v.download_url,
                "sha256_url": v.sha256_url,
                "format": v.format,
            }
            for v in versions
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    # Group by type, preserving type order from the resolver
    groups: dict[str, list[ImageVersion]] = {}
    for v in versions:
        groups.setdefault(v.type, []).append(v)

    # Sort types alphabetically, versions descending within each type
    sorted_types = sorted(groups.keys())

    rows: list[list[str]] = []
    for type_key in sorted_types:
        version_list = groups[type_key]
        if not version_list:
            continue

        # Type-level display name from the first version's type_name
        type_display = version_list[0].type_name or type_key

        # Type header row
        rows.append([type_key, type_display])

        # Version rows with tree indent
        for j, v in enumerate(version_list):
            is_last = j == len(version_list) - 1
            prefix = "  └─ " if is_last else "  ├─ "
            display = v.display_name or v.version
            rows.append([f"{prefix}{v.version}", display])

    if not rows:
        print_info("No remote images available.")
        return

    print_table(
        columns=["Type / Version", "Description"],
        rows=rows,
    )


def _list_local_images(images: list[ImageItem], *, json_output: bool) -> None:
    """Render locally cached images."""
    if json_output:
        data: list[dict[str, Any]] = []
        for img in images:
            data.append(
                {
                    "id": img.id,
                    "name": img.name,
                    "type": img.type,
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
                img.name,
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


@image_app.command(name="pull")
@handle_errors
def image_pull(
    image_selector: str = typer.Argument(
        ...,
        help="Image ID or image type from 'mvm image ls --remote' (e.g. ubuntu-24.04 or ubuntu)",
        autocompletion=_complete_remote_image_ids,
    ),
    image_type: str | None = typer.Option(
        None,
        "--type",
        help="Image type from images.yaml (e.g. ubuntu, debian, firecracker)",
    ),
    version: str | None = typer.Option(
        None,
        "--version",
        help="Image spec version from images.yaml (required if multiple images share the same type)",
    ),
    arch: str | None = typer.Option(
        None,
        "--arch",
        help="Image architecture (e.g. x86_64, arm64)",
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Re-download even if exists"
    ),
    no_cache: bool = typer.Option(
        False,
        "--no-cache",
        help="Skip cached version listing and fetch live from upstream",
    ),
    set_default: bool = typer.Option(
        False, "--default", help="Set as default image after download"
    ),
    skip_optimization: bool = typer.Option(
        False,
        "--skip-optimization",
        help="Skip shrink and compression, keep plain ext4",
    ),
    disable_detector: str | None = typer.Option(
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

    pull_input = ImagePullInput(
        type=image_type or image_selector,
        version=version,
        arch=arch,
        force=force,
        no_cache=no_cache,
        skip_optimization=skip_optimization,
        disabled_detectors=disabled_detectors,
        set_default=set_default,
    )
    console = Console()
    with console.status("", spinner="dots") as status:

        def _on_progress(event: ProgressEvent) -> None:
            if event.message:
                status.update(event.message)

        result = ImageOperation.pull(pull_input, on_progress=_on_progress)

    if isinstance(result, NeedsInteraction):
        print_info(result.message)
        raise typer.Exit(code=0)

    if result.is_error:
        print_error(
            result.message or f"Failed to download image '{image_selector}'"
        )
        raise typer.Exit(code=1)

    assert result.item is not None
    short_id = HashGenerator.shorten(result.item.id)
    print_success(
        f"Image '{result.item.name}' pulled successfully (ID: {short_id})"
    )
    if set_default:
        print_success(f"Default image set to: {image_selector}")

    raise typer.Exit(code=0)


@image_app.command(name="default")
@handle_errors
def image_set_default(
    prefix: str = typer.Argument(
        ...,
        help="Image ID prefix to set as default",
        autocompletion=_complete_local_image_ids,
    ),
) -> None:
    """Set the default image for VM creation."""
    result = ImageOperation.set_default(ImageInput(id=[prefix]))
    if result.is_error:
        print_error(result.message or f"Failed to set default image: {prefix}")
        raise typer.Exit(code=1)
    print_success(f"Default image set to: {prefix}")


@image_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def image_rm(
    prefixes: list[str] | None = typer.Argument(
        None,
        help="Image ID prefixes to remove",
        autocompletion=_complete_local_image_ids,
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Remove even if referenced by VMs"
    ),
) -> None:
    """
    Remove cached images by ID prefix.

    Examples:
        mvm image rm abc123
        mvm image rm abc123 def456

    """
    effective_ids: list[str] = list(prefixes) if prefixes else []
    if not effective_ids:
        print_error("Provide at least one image ID prefix")
        raise typer.Exit(code=1)

    result = ImageOperation.remove(ImageInput(id=effective_ids), force)
    for r in result.items:
        item_id = HashGenerator.shorten(r.item.id) if r.item else "unknown"
        if r.is_ok:
            print_success(f"Removed image: {item_id}")
        else:
            print_error(r.message or f"Failed to remove image: {item_id}")


@image_app.command(name="inspect")
@handle_errors
def image_inspect(
    prefix: str = typer.Argument(
        ...,
        help="Image ID prefix to inspect",
        autocompletion=_complete_local_image_ids,
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    tree: bool = typer.Option(False, "--tree", help="Output in tree format"),
) -> None:
    """
    Show detailed information about an image.

    Examples:
        mvm image inspect abc123
        mvm image inspect abc123 --json
        mvm image inspect abc123 --tree

    """
    info = ImageOperation.inspect(ImageInput(id=[prefix]), is_json=json_output)

    if isinstance(info, dict):
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    if tree:
        _print_image_details_tree(info)
    else:
        _print_image_details(info)


def _print_image_details(info: ImageItem) -> None:
    type_ = info.type
    missing_marker = " (missing)" if not info.is_present else ""

    print_inspect_header(f"Image: {type_}{missing_marker}")

    print_section_header("BASIC INFO")
    print_key_value("ID", info.id)
    print_key_value("Name", info.name)
    print_key_value("Type", type_)
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
    type_ = info.type
    missing_marker = " (missing)" if not info.is_present else ""

    print(f"{type_}{missing_marker}")

    tree_lines = [
        f"├── ID:          {info.id}",
        f"├── Name:        {info.name}",
        f"├── Type:        {type_}",
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
@handle_errors
def image_import(
    name: str = typer.Argument(..., help="Display name for the imported image"),
    source_path: Path = typer.Argument(..., help="Path to local image file"),
    arch: str | None = typer.Option(
        None,
        "--arch",
        help="Image arch: x86_64, arm64",
    ),
    root_partition: int | None = typer.Option(
        None,
        "--root-partition",
        help="Root Partition: 1, 2, 3",
    ),
    format: str | None = typer.Option(
        None,
        "--format",
        help="Image format: qcow2, raw, tar-rootfs, or auto",
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Overwrite existing"
    ),
    set_default: bool = typer.Option(
        False, "--default", help="Set as default after import"
    ),
    skip_optimization: bool = typer.Option(
        False,
        "--skip-optimization",
        help="Skip shrink and compression, keep plain ext4",
    ),
    disable_detector: str | None = typer.Option(
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

    if format is None or format == "auto":
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

    console = Console()
    with console.status("", spinner="dots") as status:

        def _on_progress(event: ProgressEvent) -> None:
            if event.message:
                status.update(event.message)

        result = ImageOperation.import_(spec, on_progress=_on_progress)

    if result.is_error:
        print_error(result.message or f"Failed to import image '{name}'")
        raise typer.Exit(code=1)

    assert result.item is not None
    short_id = HashGenerator.shorten(result.item.id)
    print_success(f"Image imported: {result.item.path}")
    print_info(f"  Name: {name}")
    print_info(f"  ID:   {short_id}")

    if set_default:
        print_success(f"Default image set to: {name}")

    raise typer.Exit(code=0)


@image_app.command(name="warm")
@handle_errors
def image_warm(
    image_id: str = typer.Argument(
        ...,
        help="Image ID, hash prefix, or OS slug to warm (e.g., 'ubuntu-24.04', 'abc123')",
        autocompletion=_complete_local_image_ids,
    ),
) -> None:
    """
    Pre-decompress image to ready pool for fast VM creation.

    This command decompresses the image to tmpfs/RAM ahead of time,
    so subsequent VM creations can use fast copy instead of waiting
    for decompression.

    Examples:
        # Warm an image by OS slug:
        mvm image warm ubuntu-24.04

        # Warm by image ID prefix:
        mvm image warm abc123

    """
    console = Console()
    with console.status("", spinner="dots") as status:

        def _on_progress(event: ProgressEvent) -> None:
            if event.message:
                status.update(event.message)

        result = ImageOperation.warm(
            ImageInput(id=[image_id]), on_progress=_on_progress
        )
    if result.is_error:
        print_error(result.message or f"Failed to warm image '{image_id}'")
        raise typer.Exit(code=1)

    for path in result.item or []:
        size_str = CommonUtils.format_bytes_human_readable(path.stat().st_size)
        print_success(f"Image warmed successfully: {image_id}")
        print_info(f"  Path: {path}")
        print_info(f"  Size: {size_str}")
    print_info("  Ready for fast VM creation!")
