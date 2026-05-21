"""Image management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING, Any, cast

import typer
from rich.console import Console

from mvmctl.api import ConfigOperation as _ConfigOperation
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
    from mvmctl.api.config_operations import ConfigOperation
    from mvmctl.api.image_operations import ImageOperation
    from mvmctl.api.inputs._image_acquire_input import (
        ImageImportInput,
        ImagePullInput,
    )
    from mvmctl.api.inputs._image_input import ImageInput
else:
    ConfigOperation = _ConfigOperation
    ImageOperation = _ImageOperation
    ImagePullInput = _ImagePullInput
    ImageImportInput = _ImageImportInput
    ImageInput = _ImageInput
from mvmctl.cli._common import (
    ListingColumn,
    render_listing,
    resolve_listing_style,
)
from mvmctl.utils.cli import handle_errors, mvm_cli

image_app = typer.Typer(
    help="Image management",
    no_args_is_help=True,
    add_completion=False,
)


@image_app.callback()
def image_callback(ctx: typer.Context) -> None:
    pass


_IMAGE_COLUMNS = [
    ListingColumn("", lambda i: mvm_cli.format_marker(i.is_default)),
    ListingColumn("ID", lambda i: mvm_cli.format_id(i.id)),
    ListingColumn(
        "Name", lambda i: mvm_cli.format_name(i.name, not i.is_present)
    ),
    ListingColumn("Type", lambda i: i.type),
    ListingColumn("Arch", lambda i: i.arch, long_only=True),
    ListingColumn("FS Type", lambda i: i.fs_type, long_only=True),
    ListingColumn(
        "Size",
        lambda i: (
            mvm_cli.format_size(i.compressed_size) if i.compressed_size else "-"
        ),
        long_only=True,
    ),
    ListingColumn("Created", lambda i: mvm_cli.format_timestamp(i.created_at)),
]


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
    long_output: bool = typer.Option(
        False, "--long", help="Show full listing with all columns"
    ),
) -> None:
    """List cached images (or available remote images with --remote)."""
    if remote:
        with Console().status("Fetching remote images"):
            result = ImageOperation.list_all(
                remote=True,
                no_cache=no_cache,
                type_filter=type_filter,
            )
        _list_remote_images(
            cast(list[ImageVersion], result), json_output=json_output
        )
    else:
        result = ImageOperation.list_all(remote=False)
        _list_local_images(
            cast(list[ImageItem], result),
            json_output=json_output,
            long_output=long_output,
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
        mvm_cli.info("No remote images available.")
        return

    mvm_cli.table(
        columns=["Type / Version", "Description"],
        rows=rows,
    )


def _list_local_images(
    images: list[ImageItem], *, json_output: bool, long_output: bool = False
) -> None:
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

    style = resolve_listing_style(long_output)

    render_listing(images, _IMAGE_COLUMNS, style)


@image_app.command(name="pull")
@handle_errors
def image_pull(
    image_selector: str = typer.Argument(
        ...,
        help="Image ID or image type from 'mvm image ls --remote' (e.g. ubuntu:24.04 or ubuntu)",
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
        False, "--default", "-d", help="Set as default image after download"
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

    # Parse ``type:version`` syntax (e.g. ``alpine:3.21``)
    effective_type: str = image_type or image_selector
    effective_version: str | None = version
    if image_type is None and ":" in image_selector:
        parts = image_selector.split(":", maxsplit=1)
        effective_type = parts[0]
        effective_version = parts[1]

    pull_input = ImagePullInput(
        type=effective_type,
        version=effective_version,
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
        mvm_cli.info(result.message)
        raise typer.Exit(code=0)

    if result.is_error:
        mvm_cli.error(result.message or f"Download failed: {image_selector}")
        raise typer.Exit(code=1)

    assert result.item is not None
    short_id = mvm_cli.format_id(result.item.id)
    mvm_cli.success(f"Pulled: {result.item.name} (ID: {short_id})")
    if set_default:
        mvm_cli.success(f"Default image set to: {image_selector}")

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
        mvm_cli.error(result.message or f"Set default failed: {prefix}")
        raise typer.Exit(code=1)
    mvm_cli.success(f"Default image set to: {prefix}")


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
        mvm_cli.error("Provide at least one image ID prefix")
        raise typer.Exit(code=1)

    result = ImageOperation.remove(ImageInput(id=effective_ids), force)
    for r in result.items:
        item_id = mvm_cli.format_id(r.item.id) if r.item else "unknown"
        if r.is_ok:
            mvm_cli.success(f"Removed: {item_id}")
        else:
            mvm_cli.error(r.message or f"Remove failed: {item_id}")


@image_app.command(name="inspect")
@handle_errors
def image_inspect(
    prefix: str = typer.Argument(
        ...,
        help="Image ID prefix to inspect",
        autocompletion=_complete_local_image_ids,
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """
    Show detailed information about an image.

    Examples:
        mvm image inspect abc123
        mvm image inspect abc123 --json

    """
    info = ImageOperation.inspect(ImageInput(id=[prefix]))

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    name = info.get("image", {}).get("name", prefix)
    mvm_cli.print_dict_tree(info, title=f"Image: {name}")


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
        False, "--default", "-d", help="Set as default after import"
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
        mvm_cli.error(f"Source file not found: {source_path}")
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
        mvm_cli.error(
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
        mvm_cli.error(result.message or f"Import failed: {name}")
        raise typer.Exit(code=1)

    assert result.item is not None
    short_id = mvm_cli.format_id(result.item.id)
    mvm_cli.success(f"Imported: {result.item.path}")
    mvm_cli.info(f"  Name: {name}")
    mvm_cli.info(f"  ID:   {short_id}")

    if set_default:
        mvm_cli.success(f"Default image set to: {name}")

    raise typer.Exit(code=0)


@image_app.command(name="warm")
@handle_errors
def image_warm(
    image_id: str | None = typer.Argument(
        None,
        help="Image ID, hash prefix, or OS slug to warm (e.g., 'ubuntu-24.04', 'abc123')",
        autocompletion=_complete_local_image_ids,
    ),
    all: bool = typer.Option(
        False,
        "--all",
        "-a",
        help="Warm all cached images.",
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

        # Warm all cached images:
        mvm image warm --all

    """
    console = Console()
    with console.status("", spinner="dots") as status:

        def _on_progress(event: ProgressEvent) -> None:
            if event.message:
                status.update(event.message)

        if image_id is not None:
            result = ImageOperation.warm(
                ImageInput(id=[image_id]), on_progress=_on_progress
            )
        else:
            result = ImageOperation.warm(all=True, on_progress=_on_progress)
    if result.is_error:
        mvm_cli.error(result.message or "Warm failed")
        raise typer.Exit(code=1)

    for path in result.item or []:
        size_str = mvm_cli.format_size(path.stat().st_size)
        display_name = image_id or "all images"
        mvm_cli.success(f"Warmed: {display_name}")
        mvm_cli.info(f"  Path: {path}")
        mvm_cli.info(f"  Size: {size_str}")
    mvm_cli.info("  Ready for fast VM creation")
