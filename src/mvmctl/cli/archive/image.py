"""Image management commands."""

from __future__ import annotations

import json
from datetime import datetime
from pathlib import Path
from typing import Any, List, Optional

import typer

from mvmctl.api.image import (
    fetch_image_and_register,
    find_existing_image_files,
    get_image_metadata,
    import_image_and_register,
    list_images_metadata,
    load_images_config,
    remove_image,
    resolve_image_spec,
    set_default_image,
    set_default_image_by_id,
    validate_image_type_selector,
)
from mvmctl.api.metadata import find_images_by_id_prefix
from mvmctl.api.vm import get_vm_manager
from mvmctl.constants import (
    DEFAULT_IMAGE_ARCH,
    DEFAULT_IMAGE_IMPORT_FORMAT,
    IMAGE_IMPORT_FORMAT_MAP,
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.exceptions import (
    ImageError,
    RootPartitionDetectionError,
    TieDetectedError,
)
from mvmctl.models.image import ImageFetchInput, ImageImportInput
from mvmctl.utils.console import (
    format_timestamp,
    get_combined_marker,
    print_error,
    print_info,
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
    print_warning,
)
from mvmctl.utils.disk_size import format_bytes_human_readable, format_sectors_human_readable
from mvmctl.utils.fs import (
    get_assets_dir,
    get_cache_dir,
    get_file_size,
    get_images_dir,
    is_file_missing,
)
from mvmctl.utils.full_hash import generate_full_hash_image, shorten_hash
from mvmctl.utils.id_lookup import resolve_single_by_id_prefix
from mvmctl.utils.time import human_readable_time

image_app = typer.Typer(
    help="Image management",
    no_args_is_help=False,
    rich_markup_mode=None,
    add_completion=False,
)


@image_app.callback()
def image_callback(ctx: typer.Context) -> None:
    pass


def _get_vms_using_image(image_path: Path) -> list[str]:
    """Get list of VM names that reference the given image path.

    Checks both vm.config.rootfs_path (legacy) and vm.image_id (new field)
    to ensure VMs without persisted config are still protected.
    """
    vm_manager = get_vm_manager()
    vms = vm_manager.list_all()
    image_path_str = str(image_path)
    result = []
    for vm in vms:
        # Check config.rootfs_path (legacy VMs with full config)
        if vm.config and vm.config.rootfs_path == image_path:
            result.append(vm.name)
        # Check image_id field (new field for VMs without full config)
        elif vm.image_id and vm.image_id == image_path_str:
            result.append(vm.name)
    return result


def _find_image_by_os_slug(
    all_meta: dict[str, dict[str, object]], os_slug: str
) -> tuple[str, dict[str, object]] | None:
    for key, meta in all_meta.items():
        if str(meta.get("os_slug", "")) == os_slug:
            return key, meta
    return None


def _find_local_image_path(images_dir: Path, image_id: str) -> Path | None:
    for ext in SUPPORTED_IMAGE_EXTENSIONS:
        candidate = images_dir / f"{image_id}{ext}"
        if candidate.exists():
            return candidate
    return None


def _resolve_image_file(images_dir: Path, image_id: str, meta: dict[str, object]) -> Path | None:
    filename = str(meta.get("path", ""))
    if filename:
        return images_dir / filename
    return _find_local_image_path(images_dir, image_id)


def _print_image_details(info: dict[str, Any], found_path: Path | None) -> None:
    os_slug = info.get("os_slug", "-")
    missing_marker = " (missing)" if info.get("missing") else ""

    print_inspect_header(f"Image: {os_slug}{missing_marker}")

    print_section_header("BASIC INFO")
    print_key_value("ID", info.get("id", "-"))
    print_key_value("Name", info.get("name", "-"))
    print_key_value("OS Slug", os_slug)
    print_key_value("Arch", info.get("arch", "-"))
    print_key_value("Default", info.get("is_default", "-"))
    print_key_value("Pulled", format_timestamp(info.get("pulled_at")))

    print_section_header("STORAGE")
    print_key_value("Filename", info.get("filename", "-"))
    print_key_value("FS Type", info.get("fs_type", "-"))
    print_key_value("FS UUID", info.get("fs_uuid", "-"))
    print_key_value("File Size", info.get("file_size", "-"))

    print_section_header("COMPRESSION")
    print_key_value("Format", info.get("compressed_format", "-"))
    print_key_value("Original", info.get("original_size", "-"))
    print_key_value("Compressed", info.get("compressed_size", "-"))
    print_key_value("Ratio", info.get("compression_ratio", "-"))

    print_section_header("VM REQUIREMENTS")
    print_key_value("Minimum Disk", info.get("minimum_rootfs_size", "-"))


def _print_image_details_tree(info: dict[str, Any], found_path: Path | None) -> None:
    os_slug = info.get("os_slug", "-")
    missing_marker = " (missing)" if info.get("missing") else ""

    print(f"{os_slug}{missing_marker}")

    tree_lines = [
        f"├── ID:          {info.get('id', '-')}",
        f"├── Name:        {info.get('name', '-')}",
        f"├── OS Slug:     {os_slug}",
        f"├── Arch:        {info.get('arch', '-')}",
        f"├── Default:     {info.get('is_default', '-')}",
        f"├── Pulled:      {info.get('pulled_at', '-')}",
    ]

    tree_lines.append("├── Storage")
    tree_lines.append(f"│   ├── Filename:  {info.get('filename', '-')}")
    tree_lines.append(f"│   ├── FS Type:   {info.get('fs_type', '-')}")
    tree_lines.append(f"│   ├── FS UUID:   {info.get('fs_uuid', '-')}")
    tree_lines.append(f"│   └── File Size: {info.get('file_size', '-')}")

    tree_lines.append("├── Compression")
    tree_lines.append(f"│   ├── Format:    {info.get('compressed_format', '-')}")
    tree_lines.append(f"│   ├── Original:  {info.get('original_size', '-')}")
    tree_lines.append(f"│   ├── Compressed: {info.get('compressed_size', '-')}")
    tree_lines.append(f"│   └── Ratio:     {info.get('compression_ratio', '-')}")

    tree_lines.append("└── VM Requirements")
    tree_lines.append(f"    └── Minimum Disk: {info.get('minimum_rootfs_size', '-')}")

    for line in tree_lines:
        print(line)


def _prompt_for_partition_selection(
    partitions: list[dict[str, object]],
    tied_partitions: list[str] | None = None,
    disabled_detectors: list[str] | None = None,
) -> int:
    """Display partition information and prompt user to select the root partition.

    Args:
        partitions: List of partition dictionaries with 'size', 'type', 'name', 'label', 'fstype'.
        tied_partitions: Optional list of tied partition numbers (as strings) for display.
        disabled_detectors: List of detector names that were disabled during detection.

    Returns:
        The selected partition number (1-indexed).

    Raises:
        typer.Exit: If user cancels or invalid input after retries.
    """
    if not partitions:
        print_error("No partitions available to select")
        raise typer.Exit(code=1)

    print_warning("Could not automatically detect root partition")
    if tied_partitions:
        print_info(f"Tie detected between partitions: {', '.join(tied_partitions)}")
    print_info("")
    print_info("Available partitions:")

    rows: list[list[str]] = []
    for i, partition in enumerate(partitions, 1):
        size_sectors = partition.get("size", 0)
        size_str = (
            format_sectors_human_readable(int(size_sectors))
            if isinstance(size_sectors, (int, float))
            else "Unknown"
        )
        part_type = str(partition.get("type", "-"))[:20]
        label = str(partition.get("name", partition.get("label", "-")))[:15]
        fstype = str(partition.get("fstype", "-"))[:10]
        rows.append([str(i), size_str, part_type, label, fstype])

    print_table(
        columns=["#", "Size", "Type", "Label", "FS Type"],
        rows=rows,
    )

    num_partitions = len(partitions)
    while True:
        try:
            user_input = input(f"\nSelect root partition (1-{num_partitions}): ")
            selection = int(user_input.strip())
            if 1 <= selection <= num_partitions:
                return selection
            print_error(f"Invalid selection. Please enter a number between 1 and {num_partitions}")
        except ValueError:
            print_error(f"Invalid input. Please enter a number between 1 and {num_partitions}")


def _output_remote_images(images: list[Any], images_dir: Path, json_output: bool) -> None:
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

    all_meta = list_images_metadata(images_dir)
    rows: list[list[str]] = []
    for img in images:
        entry = _find_image_by_os_slug(all_meta, img.id)
        if entry:
            meta_key, meta = entry
            found_path = _resolve_image_file(images_dir, meta_key, meta)
            compression = (
                str(meta.get("compressed_format", "-"))
                if found_path and found_path.exists()
                else "-"
            )
        else:
            found_path = None
            compression = "-"
        is_missing = is_file_missing(found_path)
        display_id = get_combined_marker(False, is_missing) + img.id
        size = get_file_size(found_path)
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


def _output_local_images(images: list[Any], images_dir: Path, json_output: bool) -> None:
    all_meta = list_images_metadata(images_dir)
    yaml_specs_by_slug = {img.id: img for img in images}

    if json_output:
        result: list[dict[str, str]] = []
        for meta_id, meta in all_meta.items():
            os_slug = str(meta.get("os_slug", meta_id))
            yaml_spec = yaml_specs_by_slug.get(os_slug)
            display_name = yaml_spec.name if yaml_spec else str(meta.get("os_name", os_slug))
            fs_type = str(meta.get("fs_type", yaml_spec.convert_to if yaml_spec else "unknown"))
            result.append(
                {
                    "id": meta_id,
                    "name": display_name,
                    "format": yaml_spec.format
                    if yaml_spec
                    else str(meta.get("fs_type", "unknown")),
                    "fs_type": fs_type,
                    "added": human_readable_time(str(meta.get("pulled_at", "")))
                    if meta.get("pulled_at")
                    else "-",
                }
            )
        typer.echo(json.dumps(result, indent=2))
        return

    rows: list[list[str]] = []

    for meta_id, meta in all_meta.items():
        os_slug = str(meta.get("os_slug", meta_id))
        yaml_spec = yaml_specs_by_slug.get(os_slug)
        display_name = yaml_spec.name if yaml_spec else str(meta.get("os_name", os_slug))
        found_path = _resolve_image_file(images_dir, meta_id, meta)
        is_default = bool(meta.get("is_default", 0))
        is_missing = is_file_missing(found_path)
        added = (
            human_readable_time(str(meta.get("pulled_at", ""))) if meta.get("pulled_at") else "-"
        )
        fs_type = str(
            meta.get("fs_type", found_path.suffix.lstrip(".") if found_path else "unknown")
        )
        display_id = get_combined_marker(is_default, is_missing) + shorten_hash(meta_id, 12)
        _raw_size = meta.get("compressed_size")
        size = get_file_size(
            found_path, int(_raw_size) if isinstance(_raw_size, (int, float)) else 0
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


@image_app.command(name="ls")
def image_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
    remote: bool = typer.Option(False, "--remote", "-r", help="Show available remote images"),
    name_filter: Optional[str] = typer.Option(None, "--name", help="Filter by image name"),
) -> None:
    """List cached images (or available remote images with --remote)."""
    images_dir = images_dir if images_dir is not None else get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)

    images = load_images_config(get_assets_dir() / "images.yaml")
    if name_filter:
        images = [
            img
            for img in images
            if name_filter.lower() in img.name.lower() or name_filter.lower() in img.id.lower()
        ]

    if remote:
        _output_remote_images(images, images_dir, json_output)
    else:
        _output_local_images(images, images_dir, json_output)


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
    force: bool = typer.Option(False, "--force", "-f", help="Re-download even if exists"),
    set_default: bool = typer.Option(
        False, "--set-default", help="Set as default image after download"
    ),
    no_prompt: bool = typer.Option(
        False, "--no-prompt", help="Exit with error on detection failure"
    ),
    skip_optimization: bool = typer.Option(
        False, "--skip-optimization", help="Skip shrink and compression, keep plain ext4"
    ),
) -> None:
    """Download an image by its ID. Run 'mvm image ls -r' to list available image IDs."""
    # ── SETUP ──────────────────────────────────────────────────────────
    images_dir = out if out is not None else get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)
    images = load_images_config(get_assets_dir() / "images.yaml")

    # ── VALIDATE ───────────────────────────────────────────────────────
    try:
        validate_image_type_selector(image_type, image_selector, images)
    except ImageError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    # ── RESOLVE ──────────────────────────────────────────────────────────
    try:
        spec = resolve_image_spec(images, image_type or image_selector, version)
    except ImageError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)
    spec.arch = arch if arch is not None else DEFAULT_IMAGE_ARCH

    # ── GUARD ────────────────────────────────────────────────────────────
    if not force:
        existing = find_existing_image_files(spec, images_dir)
        if existing:
            print_warning(f"Image '{spec.id}' already exists locally:")
            for path in existing:
                print_info(f"  {path}")
            meta = get_image_metadata(spec.id)
            if meta and meta.get("pulled_at"):
                print_info(f"    Pulled: {str(meta['pulled_at'])[:19]}")
            if not typer.confirm("Re-download anyway?", default=False):
                print_info("Skipping download. Use --force to overwrite.")
                if set_default:
                    try:
                        set_default_image(spec.id)
                    except KeyError:
                        pass
                    print_success(f"Default image set to: {spec.id}")
                raise typer.Exit(code=0)

    # ── EXECUTE ─────────────────────────────────────────────────────────
    # Handle partition detection retry in CLI (user prompting stays in CLI)
    try:
        fetch_input = ImageFetchInput(
            spec=spec,
            output_dir=images_dir,
            force=True,
            skip_optimization=skip_optimization,
        )
        result = fetch_image_and_register(fetch_input)
    except (RootPartitionDetectionError, TieDetectedError) as exc:
        if no_prompt:
            print_error(str(exc))
            raise typer.Exit(code=1)
        tied = exc.tied_partitions if isinstance(exc, TieDetectedError) else None
        selected = _prompt_for_partition_selection(exc.partitions, tied_partitions=tied)
        print_info(f"Using user-selected partition: {selected}")
        fetch_input = ImageFetchInput(
            spec=spec,
            output_dir=images_dir,
            force=True,
            partition=selected,
            skip_optimization=skip_optimization,
        )
        result = fetch_image_and_register(fetch_input)

    if result is None:
        print_error(f"Failed to download image '{spec.id}'")
        raise typer.Exit(code=1)

    # ── FINALIZE ─────────────────────────────────────────────────────────
    short_id = shorten_hash(result.full_hash, 12)
    print_success(f"Image ready: {result.result.path}")
    print_info(f"  ID: {short_id}")
    if set_default:
        set_default_image(spec.id)
        print_success(f"Default image set to: {spec.id}")

    raise typer.Exit(code=0)


@image_app.command(name="set-default")
def image_set_default(
    prefix: str = typer.Argument(..., help="Image ID prefix to set as default"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
) -> None:
    """Set the default image for VM creation."""
    images_dir = images_dir if images_dir is not None else get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)

    match = resolve_single_by_id_prefix(prefix, find_images_by_id_prefix, get_cache_dir(), "image")
    if match is None:
        raise typer.Exit(code=1)

    full_key, meta = match
    filename = str(meta.get("path", ""))

    if filename and (images_dir / filename).exists():
        pass
    else:
        found = any(
            (images_dir / f"{full_key}{ext}").exists() for ext in SUPPORTED_IMAGE_EXTENSIONS
        )
        if not found:
            print_error(f"Image file not found for ID '{prefix}'")
            raise typer.Exit(code=1)

    set_default_image_by_id(full_key)
    print_success(f"Default image set to: {prefix}")


@image_app.command(name="rm")
def image_rm(
    prefixes: Optional[List[str]] = typer.Argument(None, help="Image ID prefixes to remove"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Remove even if referenced by VMs"),
) -> None:
    """Remove cached images by ID prefix.

    Examples:
        mvm image rm abc123
        mvm image rm abc123 def456
    """
    images_dir = images_dir if images_dir is not None else get_images_dir()
    effective_ids: list[str] = list(prefixes) if prefixes else []
    if not effective_ids:
        print_error("Provide at least one image ID prefix")
        raise typer.Exit(code=1)

    exit_code = 0

    for prefix in effective_ids:
        match = resolve_single_by_id_prefix(
            prefix, find_images_by_id_prefix, get_cache_dir(), "image"
        )
        if match is None:
            if not find_images_by_id_prefix(get_cache_dir(), prefix):
                print_error(f"No image found with ID prefix '{prefix}'")
            else:
                print_error(
                    f"Ambiguous ID prefix '{prefix}' matches {len(find_images_by_id_prefix(get_cache_dir(), prefix))} images — use more characters"
                )
            exit_code = 1
            continue

        full_key, meta = match
        filename = str(meta.get("path", ""))
        files_to_check: list[Path] = []

        if filename:
            candidate = images_dir / filename
            if candidate.exists():
                files_to_check.append(candidate)

        if not files_to_check:
            files_to_check = [
                images_dir / f"{full_key}{ext}"
                for ext in SUPPORTED_IMAGE_EXTENSIONS
                if (images_dir / f"{full_key}{ext}").exists()
            ]

        if not files_to_check:
            remove_image(full_key, force, images_dir)
            print_error(
                f"Image file not found for ID '{prefix}' (metadata exists but file missing)"
            )
            exit_code = 1
            continue

        # Check if image is referenced by any VMs
        for path in files_to_check:
            referencing_vms = _get_vms_using_image(path)
            if referencing_vms and not force:
                print_warning(
                    f"Image '{prefix}' is referenced by active VMs: {', '.join(referencing_vms)}"
                )
                print_info("Use --force to remove anyway (this may break those VMs)")
                exit_code = 1
                break
        else:
            files_removed, _ = remove_image(full_key, force, images_dir)
            for path in files_removed:
                print_success(f"Removed: {path}")

    raise typer.Exit(code=exit_code)


@image_app.command(name="inspect")
def image_inspect(
    prefix: str = typer.Argument(..., help="Image ID prefix to inspect"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    tree: bool = typer.Option(False, "--tree", help="Output in tree format"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
) -> None:
    """Show detailed information about an image.

    Examples:
        mvm image inspect abc123
        mvm image inspect abc123 --json
        mvm image inspect abc123 --tree
    """
    images_dir = images_dir if images_dir is not None else get_images_dir()
    cache_dir = get_cache_dir()

    match = resolve_single_by_id_prefix(prefix, find_images_by_id_prefix, cache_dir, "image")
    if match is None:
        if not find_images_by_id_prefix(cache_dir, prefix):
            print_error(f"No image found with ID prefix '{prefix}'")
        else:
            print_error(
                f"Ambiguous ID prefix '{prefix}' matches {len(find_images_by_id_prefix(cache_dir, prefix))} images — use more characters"
            )
        raise typer.Exit(code=1)

    full_id, meta = match

    filename = str(meta.get("path", ""))
    found_path = None
    if filename:
        candidate = images_dir / filename
        if candidate.exists():
            found_path = candidate
    if not found_path:
        for ext in SUPPORTED_IMAGE_EXTENSIONS:
            candidate = images_dir / f"{full_id}{ext}"
            if candidate.exists():
                found_path = candidate
                break

    is_missing = is_file_missing(found_path)

    pulled_at = meta.get("pulled_at")
    if pulled_at:
        try:
            dt = datetime.fromisoformat(str(pulled_at).replace("Z", "+00:00"))
            pulled_str = dt.strftime("%Y/%m/%d %H:%M:%S")
        except (ValueError, AttributeError):
            pulled_str = str(pulled_at)
    else:
        pulled_str = "-"

    original_size = meta.get("original_size")
    compressed_size = meta.get("compressed_size")
    compression_ratio = meta.get("compression_ratio")
    compressed_format = meta.get("compressed_format", "-")
    minimum_rootfs_size_mib = meta.get("minimum_rootfs_size_mib")

    original_size_str = format_bytes_human_readable(int(original_size)) if original_size else "-"
    compressed_size_str = (
        format_bytes_human_readable(int(compressed_size)) if compressed_size else "-"
    )
    ratio_str = f"{float(compression_ratio):.2f}x" if compression_ratio else "-"
    minimum_size_str = f"{minimum_rootfs_size_mib} MiB" if minimum_rootfs_size_mib else "-"

    file_size_str = "-"
    if found_path and found_path.exists():
        try:
            file_size = found_path.stat().st_size
            file_size_str = format_bytes_human_readable(file_size)
        except OSError:
            pass

    is_default = bool(meta.get("is_default", 0))
    created_at = meta.get("created_at")
    updated_at = meta.get("updated_at")

    info = {
        "id": full_id,
        "name": str(meta.get("os_name", "-")),
        "os_slug": str(meta.get("os_slug", "-")),
        "arch": str(meta.get("arch", "-")),
        "filename": filename or "-",
        "fs_type": str(meta.get("fs_type", "-")),
        "fs_uuid": str(meta.get("fs_uuid", "-")),
        "pulled_at": pulled_str,
        "created_at": format_timestamp(created_at) if created_at else "-",
        "updated_at": format_timestamp(updated_at) if updated_at else "-",
        "original_size": original_size_str,
        "compressed_size": compressed_size_str,
        "compression_ratio": ratio_str,
        "compressed_format": str(compressed_format),
        "file_size": file_size_str,
        "minimum_rootfs_size": minimum_size_str,
        "is_default": "Yes" if is_default else "No",
        "missing": is_missing,
    }

    if json_output:
        typer.echo(json.dumps(info, indent=2))
        return

    if tree:
        _print_image_details_tree(info, found_path)
    else:
        _print_image_details(info, found_path)


# Mapping from CLI detector names to internal detector names
CLI_TO_INTERNAL_DETECTOR = {
    "type": "type_code",
    "label": "label",
    "size": "size",
    "filesystem": "filesystem",
}


@image_app.command(name="import")
def image_import(
    name: str = typer.Argument(..., help="Display name for the imported image"),
    source_path: Path = typer.Argument(..., help="Path to local image file"),
    format: str = typer.Option(
        None,
        "--format",
        help="Image format: qcow2, raw, tar-rootfs, or auto",
    ),
    convert_to: str = typer.Option(None, "--convert-to", help="Target filesystem format"),
    force: bool = typer.Option(False, "--force", "-f", help="Overwrite existing"),
    set_default: bool = typer.Option(False, "--set-default", help="Set as default after import"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Output directory"),
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
    from datetime import datetime, timezone

    images_dir = images_dir if images_dir is not None else get_images_dir()

    if not source_path.exists():
        print_error(f"Source file not found: {source_path}")
        raise typer.Exit(code=1)

    # Parse disabled detectors
    disabled_detectors: list[str] = []
    if disable_detector:
        names = [n.strip() for n in disable_detector.split(",")]
        for detector_name in names:
            if detector_name == "all":
                disabled_detectors = list(CLI_TO_INTERNAL_DETECTOR.values())
                break
            elif detector_name in CLI_TO_INTERNAL_DETECTOR:
                disabled_detectors.append(CLI_TO_INTERNAL_DETECTOR[detector_name])
            else:
                print_error(
                    f"Unknown detector: {detector_name}. Valid: type,label,size,filesystem,all"
                )
                raise typer.Exit(code=1)

    timestamp = datetime.now(tz=timezone.utc).isoformat()
    full_id = generate_full_hash_image(source_path, name, timestamp)
    image_id = full_id

    resolved_format: str | None = format
    # Runtime resolution: if no format specified, use default and auto-detect from filename
    if resolved_format is None:
        resolved_format = DEFAULT_IMAGE_IMPORT_FORMAT
    if resolved_format == DEFAULT_IMAGE_IMPORT_FORMAT:
        fname = source_path.name.lower()
        resolved_format = next(
            (fmt for ext, fmt in IMAGE_IMPORT_FORMAT_MAP.items() if fname.endswith(ext)),
            None,
        )
    if resolved_format is None:
        print_error(
            f"Cannot auto-detect format from '{source_path.name}'. "
            "Use --format qcow2|raw|tar-rootfs."
        )
        raise typer.Exit(code=1)

    spec = ImageImportInput(
        id=image_id,
        name=name,
        source_path=source_path,
        output_dir=images_dir,
        format=str(resolved_format),
        convert_to=convert_to,
        disabled_detectors=disabled_detectors,
        force=force,
    )

    # Handle partition detection retry in CLI (user prompting stays in CLI)
    try:
        result = import_image_and_register(spec)
    except (RootPartitionDetectionError, TieDetectedError) as exc:
        if no_prompt:
            print_error(str(exc))
            raise typer.Exit(code=1)
        tied = exc.tied_partitions if isinstance(exc, TieDetectedError) else None
        selected = _prompt_for_partition_selection(
            exc.partitions,
            tied_partitions=tied,
            disabled_detectors=disabled_detectors,
        )
        print_info(f"Using user-selected partition: {selected}")
        spec.partition = selected
        result = import_image_and_register(spec)
    except ImageError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    # import_image_and_register already registers the image in DB
    short_id = shorten_hash(result.full_hash, 12)
    print_success(f"Image imported: {result.result.path}")
    print_info(f"  Name: {name}")
    print_info(f"  ID:   {short_id}")

    if set_default:
        set_default_image_by_id(image_id)
        print_success(f"Default image set to: {image_id}")

    raise typer.Exit(code=0)


@image_app.command(name="warm")
def image_warm(
    image_id: str = typer.Argument(
        ..., help="Image ID, hash prefix, or OS slug to warm (e.g., 'ubuntu-24.04', 'abc123')"
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
    from mvmctl.api.image import warm_image_for_ready_pool

    try:
        warmed_path = warm_image_for_ready_pool(image_id)
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
