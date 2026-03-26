"""Asset management commands — unified interface for kernels, images, and binaries."""

import json
import shutil
from pathlib import Path
from typing import List, Optional

import typer

from mvmctl.api.assets import (
    BinaryVersion,
    ImageImportSpec,
    build_kernel_pipeline,
    download_firecracker_kernel,
    fetch_binary,
    fetch_image,
    human_readable_time,
    import_image,
    list_kernels,
    list_local_versions,
    list_remote_versions,
    load_images_config,
    remove_version,
    resolve_kernel_spec,
    set_active_version,
    set_default_kernel,
)
from mvmctl.api.metadata import (
    find_images_by_short_id,
    get_image_entry,
    list_image_entries,
    remove_image_entry,
    update_image_entry,
)
from mvmctl.constants import (
    DEFAULT_FC_CI_VERSION,
    DEFAULT_FC_KERNEL_ARCH,
    DEFAULT_IMAGE_CONVERT_TO,
    DEFAULT_IMAGE_IMPORT_FORMAT,
    DEFAULT_IMAGE_IMPORT_SIZE_MIB,
    DEFAULT_KERNEL_VERSION,
    DEFAULT_REMOTE_VERSION_LIMIT,
    IMAGE_IMPORT_FORMAT_MAP,
    KERNEL_TYPE_FIRECRACKER,
    KERNEL_TYPE_OFFICIAL,
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.exceptions import AssetNotFoundError, BinaryError, ImageError, KernelError
from mvmctl.utils.console import print_error, print_info, print_success, print_table, print_warning
from mvmctl.utils.fs import get_assets_dir, get_cache_dir, get_images_dir, get_kernels_dir

kernel_app = typer.Typer(
    help="Kernel management",
    no_args_is_help=False,
    rich_markup_mode=None,
    add_completion=False,
)
image_app = typer.Typer(
    help="Image management",
    no_args_is_help=False,
    rich_markup_mode=None,
    add_completion=False,
)
bin_app = typer.Typer(
    help="Binary management",
    no_args_is_help=False,
    rich_markup_mode=None,
    add_completion=False,
)


@kernel_app.callback(invoke_without_command=True)
def kernel_callback(ctx: typer.Context) -> None:
    if ctx.invoked_subcommand is None:
        typer.echo(ctx.get_help())
        raise typer.Exit()


@image_app.callback(invoke_without_command=True)
def image_callback(ctx: typer.Context) -> None:
    if ctx.invoked_subcommand is None:
        typer.echo(ctx.get_help())
        raise typer.Exit()


@bin_app.callback(invoke_without_command=True)
def bin_callback(ctx: typer.Context) -> None:
    if ctx.invoked_subcommand is None:
        typer.echo(ctx.get_help())
        raise typer.Exit()


@kernel_app.command(name="ls")
def kernel_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    kernels_dir: Optional[Path] = typer.Option(None, "--kernels-dir", help="Kernels directory"),
    firecracker_only: bool = typer.Option(
        False, "--firecracker", help="Show only firecracker kernels"
    ),
    official_only: bool = typer.Option(
        False, "--official", help="Show only official/upstream kernels"
    ),
) -> None:
    """List cached kernels (both Firecracker CI and official upstream)."""
    kernels_dir = kernels_dir if kernels_dir is not None else get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)
    kernels = list_kernels(kernels_dir)

    if firecracker_only:
        kernels = [k for k in kernels if k.get("type") == KERNEL_TYPE_FIRECRACKER]
    elif official_only:
        kernels = [k for k in kernels if k.get("type") == KERNEL_TYPE_OFFICIAL]

    if json_output:
        typer.echo(json.dumps(kernels, indent=2))
        return

    if not kernels:
        from mvmctl.utils.console import print_info

        print_info("No kernels found. Use 'mvm kernel fetch --type firecracker' to download one.")
        return

    rows: list[list[str]] = []
    for k in kernels:
        default_marker = "✓" if k.get("is_default") == "true" else " "
        last_modified_display = human_readable_time(k.get("last_modified", "-"))
        rows.append(
            [
                default_marker,
                k.get("id", ""),
                k.get("name", "-"),
                k.get("version", ""),
                k.get("arch", "-"),
                k.get("type", ""),
                last_modified_display,
                k.get("size", "-"),
            ]
        )
    print_table(
        title="Downloaded Kernels",
        columns=["Def", "ID", "Name", "Version", "Arch", "Type", "Last Modified", "Size"],
        rows=rows,
    )


def _get_ci_version() -> str:
    from mvmctl.api.config import get_firecracker_config

    ci_version = get_firecracker_config().get("ci_version", "")
    if not ci_version:
        local = list_local_versions()
        active = next((b for b in local if b.is_active), None)
        if active:
            parts = active.version.split(".")
            ci_version = f"{parts[0]}.{parts[1]}" if len(parts) >= 2 else active.version
    return ci_version or DEFAULT_FC_CI_VERSION


@kernel_app.command(name="fetch")
def kernel_fetch(
    kernel_type: Optional[str] = typer.Option(
        None, "--type", help="Kernel type from kernels.yaml (e.g. firecracker, official)"
    ),
    firecracker: bool = typer.Option(
        False, "--firecracker", help="Shortcut for --type firecracker"
    ),
    version: Optional[str] = typer.Option(
        None,
        "--version",
        help="Kernel spec version from kernels.yaml (required if multiple specs share the same type)",
    ),
    arch: str = typer.Option(
        DEFAULT_FC_KERNEL_ARCH, "--arch", help="Architecture (for firecracker type)"
    ),
    out: Optional[Path] = typer.Option(None, "--out", help="Output path/name"),
    jobs: Optional[int] = typer.Option(
        None, "--jobs", "-j", help="Parallel build jobs (official only)"
    ),
    keep_build_dir: bool = typer.Option(
        False, "--keep-build-dir", help="Keep build directory after build"
    ),
    kernel_config: Optional[Path] = typer.Option(
        None, "--kernel-config", help="Path to custom kernel .config file"
    ),
    set_default: bool = typer.Option(False, "--set-default", help="Set this kernel as default"),
) -> None:
    kernels_dir = get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)

    if firecracker:
        if kernel_type is not None and kernel_type != KERNEL_TYPE_FIRECRACKER:
            print_error("--firecracker cannot be combined with a different --type value")
            raise typer.Exit(code=1)
        kernel_type = KERNEL_TYPE_FIRECRACKER

    if kernel_type is None:
        print_error("Provide --type <kernel-type> or use --firecracker")
        raise typer.Exit(code=1)

    try:
        spec = resolve_kernel_spec(kernel_type=kernel_type, version=version)
    except KernelError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1) from exc

    if spec.kernel_type == KERNEL_TYPE_FIRECRACKER:
        ci_version = _get_ci_version()
        output_name = out.name if out is not None else None
        try:
            result = download_firecracker_kernel(
                ci_version=ci_version,
                arch=arch,
                kernels_dir=kernels_dir,
                output_name=output_name,
                kernel_spec=spec,
            )
        except KernelError as exc:
            print_error(f"Kernel fetch failed: {exc}")
            raise typer.Exit(code=1) from exc
        print_success(f"Firecracker kernel ready: {result}")

    elif spec.kernel_type == KERNEL_TYPE_OFFICIAL:
        import platform

        effective_version = spec.version or DEFAULT_KERNEL_VERSION
        effective_arch = arch if arch != DEFAULT_FC_KERNEL_ARCH else platform.machine() or "x86_64"
        output_path = out if out is not None else kernels_dir / f"{spec.output_name}-{effective_version}-{effective_arch}"

        if kernel_config and not kernel_config.exists():
            print_error(f"Kernel config file not found: {kernel_config}")
            raise typer.Exit(code=1)

        source_url = spec.source
        try:
            pipeline_result = build_kernel_pipeline(
                version=effective_version,
                source_url=source_url,
                output_path=output_path,
                build_dir=None,
                jobs=jobs,
                keep_build_dir=keep_build_dir,
                user_config_path=kernel_config,
                arch=effective_arch,
                kernel_spec=spec,
            )
        except KernelError as exc:
            print_error(f"Kernel build failed: {exc}")
            raise typer.Exit(code=1) from exc

        if pipeline_result.config_result:
            for warning in pipeline_result.config_result.warnings:
                print_warning(warning)
            for info in pipeline_result.config_result.info_messages:
                print_info(info)

        if pipeline_result.build_result:
            for warning in pipeline_result.build_result.warnings:
                print_warning(warning)
            for info in pipeline_result.build_result.info_messages:
                print_info(info)

        if keep_build_dir:
            print_info(f"Build directory kept at: {pipeline_result.build_dir}")

        result = output_path
        print_success(f"Kernel built: {result}")

    else:
        print_error(f"Unsupported kernel type in spec '{spec.name}': {spec.kernel_type!r}")
        raise typer.Exit(code=1)

    if set_default:
        set_default_kernel(kernels_dir, result.name)
        print_success(f"Default kernel set to: {result.name}")

    raise typer.Exit(code=0)


@kernel_app.command(name="set-default")
def kernel_set_default(
    name: str = typer.Argument(..., help="Kernel file name to set as default"),
    kernels_dir: Optional[Path] = typer.Option(None, "--kernels-dir", help="Kernels directory"),
) -> None:
    """Set a kernel as the default for VM creation."""
    kernels_dir = kernels_dir if kernels_dir is not None else get_kernels_dir()
    try:
        set_default_kernel(kernels_dir, name)
    except KernelError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1) from exc
    print_success(f"Default kernel set to: {name}")


@kernel_app.command(name="rm")
def kernel_rm(
    names: Optional[List[str]] = typer.Argument(None, help="Kernel file name(s) to remove"),
    kernels_dir: Optional[Path] = typer.Option(None, "--kernels-dir", help="Kernels directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove one or more cached kernels by filename."""
    kernels_dir = kernels_dir if kernels_dir is not None else get_kernels_dir()
    effective_names: list[str] = list(names) if names else []
    if not effective_names:
        print_error("Provide at least one kernel file name to remove")
        raise typer.Exit(code=1)

    exit_code = 0
    for name in effective_names:
        path = kernels_dir / name
        if not path.exists():
            print_error(f"Kernel not found: {path}")
            exit_code = 1
            continue
        if not force:
            typer.confirm(f"Remove {path}?", abort=True)
        path.unlink()
        print_success(f"Removed {path}")

    raise typer.Exit(code=exit_code)


def _load_image_meta(images_dir: Path, image_id: str) -> dict[str, str]:
    cache_dir = get_cache_dir()
    meta = get_image_entry(cache_dir, image_id)
    return {str(k): str(v) for k, v in meta.items()}


def _save_image_meta(
    images_dir: Path, image_id: str, image_path: Path, meta: dict[str, str]
) -> None:
    from datetime import datetime, timezone

    cache_dir = get_cache_dir()
    fields: dict[str, object] = dict(meta)
    fields.setdefault("pulled_at", datetime.now(tz=timezone.utc).isoformat())
    fields.setdefault("fs_type", image_path.suffix.lstrip(".") if image_path.suffix else "unknown")
    update_image_entry(cache_dir, image_id, **fields)


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
    config_path = get_assets_dir() / "images.yaml"
    images = load_images_config(config_path)

    if name_filter:
        images = [
            img
            for img in images
            if name_filter.lower() in img.name.lower() or name_filter.lower() in img.id.lower()
        ]

    if remote:
        rows: list[list[str]] = []
        for img in images:
            found_path = next(
                (
                    images_dir / f"{img.id}{ext}"
                    for ext in SUPPORTED_IMAGE_EXTENSIONS
                    if (images_dir / f"{img.id}{ext}").exists()
                ),
                None,
            )
            downloaded = "✓" if found_path else " "
            rows.append([downloaded, img.id, img.name, img.convert_to])
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
        else:
            print_table(
                title="Available Images (Remote)",
                columns=["Downloaded", "Image ID", "Name", "FS Type"],
                rows=rows,
            )
        return

    default_img = _get_default_image()
    yaml_ids = {img.id for img in images}

    _all_meta = list_image_entries(get_cache_dir())

    def _find_meta_for_internal_id(internal_id: str) -> tuple[str, dict[str, object]] | None:
        for _k, _v in _all_meta.items():
            if str(_v.get("internal_id", "")) == internal_id:
                return _k, _v
        return None

    if json_output:
        result = []
        for img in images:
            entry = _find_meta_for_internal_id(img.id)
            if entry:
                meta_key, meta = entry
                display_id = meta_key[:6]
                result.append(
                    {
                        "id": display_id,
                        "name": img.name,
                        "format": img.format,
                        "fs_type": str(meta.get("fs_type", img.convert_to)),
                        "added": human_readable_time(str(meta.get("pulled_at", "")))
                        if meta.get("pulled_at")
                        else "-",
                    }
                )
        for meta_id, meta in _all_meta.items():
            if str(meta.get("internal_id", meta_id)) in yaml_ids:
                continue
            display_id = meta_id[:6] if len(meta_id) >= 6 else meta_id
            result.append(
                {
                    "id": display_id,
                    "name": str(meta.get("os_name", meta_id)),
                    "format": str(meta.get("fs_type", "unknown")),
                    "fs_type": str(meta.get("fs_type", "unknown")),
                    "added": human_readable_time(str(meta.get("pulled_at", "")))
                    if meta.get("pulled_at")
                    else "-",
                }
            )
        typer.echo(json.dumps(result, indent=2))
        return

    rows_local: list[list[str]] = []

    for img in images:
        found_path = next(
            (
                images_dir / f"{img.id}{ext}"
                for ext in SUPPORTED_IMAGE_EXTENSIONS
                if (images_dir / f"{img.id}{ext}").exists()
            ),
            None,
        )
        if found_path is None:
            continue
        entry = _find_meta_for_internal_id(img.id)
        if entry:
            meta_key, meta = entry
            display_id = meta_key[:6]
            added = (
                human_readable_time(str(meta.get("pulled_at", "")))
                if meta.get("pulled_at")
                else "-"
            )
            fs_type = str(meta.get("fs_type", found_path.suffix.lstrip(".")))
        else:
            display_id = "-"
            added = "-"
            fs_type = found_path.suffix.lstrip(".")
        default_marker = "✓" if img.id == default_img else " "
        rows_local.append([default_marker, display_id, img.name, fs_type, added])

    for meta_id, meta in _all_meta.items():
        if str(meta.get("internal_id", meta_id)) in yaml_ids:
            continue
        found_path = next(
            (
                images_dir / f"{meta_id}{ext}"
                for ext in SUPPORTED_IMAGE_EXTENSIONS
                if (images_dir / f"{meta_id}{ext}").exists()
            ),
            None,
        )
        if found_path is None:
            filename = str(meta.get("filename", ""))
            if filename:
                found_path = images_dir / filename
        if found_path is None or not found_path.exists():
            continue
        added = (
            human_readable_time(str(meta.get("pulled_at", ""))) if meta.get("pulled_at") else "-"
        )
        fs_type = str(meta.get("fs_type", found_path.suffix.lstrip(".")))
        os_name = str(meta.get("os_name", meta_id))
        default_marker = "✓" if meta_id == default_img else " "
        display_id = meta_id[:6] if len(meta_id) >= 6 else meta_id
        rows_local.append([default_marker, display_id, os_name, fs_type, added])

    if not rows_local:
        print_info("No images downloaded. Use 'mvm image fetch <id>' to download one.")
        return
    print_table(
        title="Downloaded Images",
        columns=["Def", "ID", "OS Name", "FS Type", "Added"],
        rows=rows_local,
    )


def _get_default_image() -> str | None:
    try:
        from mvmctl.api.config import get_defaults_config

        val = get_defaults_config().get("image")
        return str(val) if val is not None else None
    except Exception:
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
    out: Optional[Path] = typer.Option(None, "--out", help="Output directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Re-download even if exists"),
    set_default: bool = typer.Option(
        False, "--set-default", help="Set as default image after download"
    ),
) -> None:
    """Download an image by its ID. Run 'mvm image ls -r' to list available image IDs."""
    out = out if out is not None else get_images_dir()
    out.mkdir(parents=True, exist_ok=True)
    config_path = get_assets_dir() / "images.yaml"
    images = load_images_config(config_path)

    if image_type is not None and image_selector != image_type:
        if any(img.id == image_selector for img in images):
            print_error("--type cannot be used when selector is an image ID")
            raise typer.Exit(code=1)
        print_error("image selector and --type must match when both are provided")
        raise typer.Exit(code=1)

    effective_selector = image_type or image_selector

    spec = next((img for img in images if img.id == effective_selector), None)
    if spec is None:
        type_matches = [img for img in images if img.image_type == effective_selector]
        if not type_matches:
            available = ", ".join(img.id for img in images)
            print_error(f"Image '{effective_selector}' not found. Available: {available}")
            raise typer.Exit(code=1)

        if version is not None:
            version_matches = [img for img in type_matches if img.version == version]
            if len(version_matches) == 1:
                spec = version_matches[0]
            elif len(version_matches) > 1:
                ids = ", ".join(img.id for img in version_matches)
                print_error(
                    f"Multiple '{effective_selector}' images with version '{version}' found: {ids}"
                )
                raise typer.Exit(code=1)
            else:
                versions = ", ".join(sorted({img.version for img in type_matches}))
                print_error(
                    f"No '{effective_selector}' image with version '{version}'. Available: {versions}"
                )
                raise typer.Exit(code=1)
        else:
            if len(type_matches) == 1:
                spec = type_matches[0]
            else:
                versions = ", ".join(sorted({img.version for img in type_matches}))
                print_error(
                    f"Multiple '{effective_selector}' images found. Provide --version. Available: {versions}"
                )
                raise typer.Exit(code=1)

    # Check if image already exists locally
    if not force:
        existing_paths = [
            out / f"{spec.id}{ext}"
            for ext in SUPPORTED_IMAGE_EXTENSIONS
            if (out / f"{spec.id}{ext}").exists()
        ]
        if existing_paths:
            print_warning(f"Image '{spec.id}' already exists locally:")
            for path in existing_paths:
                print_info(f"  {path}")
            meta = _load_image_meta(out, spec.id)
            if meta.get("pulled_at"):
                print_info(f"    Pulled: {meta['pulled_at'][:19]}")
            if not typer.confirm("Re-download anyway?", default=False):
                print_info("Skipping download. Use --force to overwrite.")
                if set_default:
                    from mvmctl.api.config import set_defaults_value

                    set_defaults_value("image", spec.id)
                    print_success(f"Default image set to: {spec.id}")
                raise typer.Exit(code=0)
            force = True

    result = fetch_image(spec, out, force)
    if result:
        import hashlib
        import time

        try:
            file_bytes = result.read_bytes()
            file_hash = hashlib.sha256(file_bytes).hexdigest()
        except OSError:
            file_hash = hashlib.sha256(str(result).encode()).hexdigest()
        timestamp = str(time.time())
        full_id = hashlib.sha256(f"{file_hash}:{timestamp}".encode()).hexdigest()

        _save_image_meta(
            out,
            full_id,
            result,
            {
                "os_name": spec.name,
                "internal_id": spec.id,
                "fs_type": result.suffix.lstrip("."),
                "full_hash": full_id,
                "filename": result.name,
            },
        )
        print_success(f"Image ready: {result}")
        print_info(f"  ID: {full_id[:6]}")
        if set_default:
            from mvmctl.api.config import set_defaults_value

            set_defaults_value("image", spec.id)
            print_success(f"Default image set to: {spec.id}")
        raise typer.Exit(code=0)
    else:
        print_error(f"Failed to download image '{spec.id}'")
        raise typer.Exit(code=1)


@image_app.command(name="set-default")
def image_set_default(
    image_id: str = typer.Argument(..., help="Image short ID or YAML image ID to set as default"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
) -> None:
    """Set the default image for VM creation."""
    images_dir = images_dir if images_dir is not None else get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)

    found = any((images_dir / f"{image_id}{ext}").exists() for ext in SUPPORTED_IMAGE_EXTENSIONS)
    stored_id = image_id

    if not found:
        matches = find_images_by_short_id(get_cache_dir(), image_id)
        if len(matches) == 1:
            full_key, meta = matches[0]
            filename = str(meta.get("filename", ""))
            if filename and (images_dir / filename).exists():
                found = True
                stored_id = full_key
            else:
                for ext in SUPPORTED_IMAGE_EXTENSIONS:
                    if (images_dir / f"{full_key}{ext}").exists():
                        found = True
                        stored_id = full_key
                        break

    if not found:
        print_error(f"Image '{image_id}' not found in {images_dir}. Download or import it first.")
        raise typer.Exit(code=1)

    from mvmctl.api.config import set_defaults_value

    set_defaults_value("image", stored_id)
    print_success(f"✓ Default image set to: {image_id}")


@image_app.command(name="rm")
def image_rm(
    short_ids: Optional[List[str]] = typer.Argument(
        None, help="Image short IDs (6 chars) to remove"
    ),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove cached images by short ID.

    Examples:
        mvm image rm abc123
        mvm image rm abc123 def456
    """
    images_dir = images_dir if images_dir is not None else get_images_dir()
    effective_ids: list[str] = list(short_ids) if short_ids else []
    if not effective_ids:
        print_error("Provide at least one image short ID")
        raise typer.Exit(code=1)

    cache_dir = get_cache_dir()
    exit_code = 0

    for short_id in effective_ids:
        matches = find_images_by_short_id(cache_dir, short_id)
        if not matches:
            print_error(f"No image found with short ID '{short_id}'")
            exit_code = 1
            continue
        if len(matches) > 1:
            print_error(
                f"Ambiguous short ID '{short_id}' matches {len(matches)} images — use more characters"
            )
            exit_code = 1
            continue

        full_key, meta = matches[0]
        filename = str(meta.get("filename", ""))
        files_to_remove: list[Path] = []

        if filename:
            candidate = images_dir / filename
            if candidate.exists():
                files_to_remove.append(candidate)

        if not files_to_remove:
            files_to_remove = [
                images_dir / f"{full_key}{ext}"
                for ext in SUPPORTED_IMAGE_EXTENSIONS
                if (images_dir / f"{full_key}{ext}").exists()
            ]

        if not files_to_remove:
            print_error(
                f"Image file not found for ID '{short_id}' (metadata exists but file missing)"
            )
            remove_image_entry(cache_dir, full_key)
            exit_code = 1
            continue

        if not force:
            typer.confirm(
                f"Remove image '{meta.get('os_name', short_id)}' ({short_id})? [{len(files_to_remove)} file(s)]",
                abort=True,
            )

        for path in files_to_remove:
            if path.is_dir():
                shutil.rmtree(path)
            else:
                path.unlink()
            print_success(f"Removed: {path}")

        remove_image_entry(cache_dir, full_key)

    raise typer.Exit(code=exit_code)


@image_app.command(name="import")
def image_import(
    name: str = typer.Argument(..., help="Display name for the imported image"),
    source_path: Path = typer.Argument(..., help="Path to local image file"),
    format: str = typer.Option(
        DEFAULT_IMAGE_IMPORT_FORMAT,
        "--format",
        help="Image format: qcow2, raw, tar-rootfs, or auto",
    ),
    convert_to: str = typer.Option(
        DEFAULT_IMAGE_CONVERT_TO, "--convert-to", help="Target filesystem format"
    ),
    size_mib: int = typer.Option(
        DEFAULT_IMAGE_IMPORT_SIZE_MIB, "--size-mib", help="Size in MiB for tar-rootfs import"
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Overwrite existing"),
    set_default: bool = typer.Option(False, "--set-default", help="Set as default after import"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Output directory"),
) -> None:
    """Import a local image file (qcow2, raw, tar-rootfs). The first argument is a display name."""
    import hashlib
    import time

    images_dir = images_dir if images_dir is not None else get_images_dir()

    if not source_path.exists():
        print_error(f"Source file not found: {source_path}")
        raise typer.Exit(code=1)

    file_hash = hashlib.sha256(source_path.read_bytes()).hexdigest()
    timestamp = str(time.time())
    full_id_hash = hashlib.sha256(f"{file_hash}:{timestamp}".encode()).hexdigest()
    image_id = full_id_hash  # Use FULL hash as key
    short_id = full_id_hash[:6]  # Display first 6 chars

    resolved_format: str | None = format
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

    spec = ImageImportSpec(
        id=image_id,
        name=name,
        source_path=source_path,
        format=str(resolved_format),
        convert_to=convert_to,
        size_mib=size_mib,
    )

    try:
        result = import_image(spec, images_dir, force=force)
    except ImageError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    _save_image_meta(
        images_dir,
        image_id,
        result,
        {
            "os_name": name,
            "fs_type": result.suffix.lstrip("."),
            "full_hash": full_id_hash,
            "filename": result.name,
        },
    )
    print_success(f"Image imported: {result}")
    print_info(f"  Name: {name}")
    print_info(f"  ID:   {short_id}")

    if set_default:
        from mvmctl.api.config import set_defaults_value

        set_defaults_value("image", image_id)
        print_success(f"Default image set to: {image_id}")

    raise typer.Exit(code=0)


def _format_bin_row(bv: BinaryVersion) -> list[str]:
    active = "✓" if bv.is_active else " "
    return [active, bv.version, str(bv.firecracker_path)]


@bin_app.command(name="ls")
def bin_ls(
    remote: bool = typer.Option(False, "--remote", "-r", help="Also show remote versions"),
    limit: int = typer.Option(
        DEFAULT_REMOTE_VERSION_LIMIT, "--limit", help="Max remote versions to show"
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List local (and optionally remote) Firecracker versions."""
    local = list_local_versions()
    local_versions = {bv.version for bv in local}

    if json_output:
        import json

        data = [
            {
                "active": bv.is_active,
                "version": bv.version,
                "path": str(bv.firecracker_path) if bv.firecracker_path else "",
            }
            for bv in local
        ]
        print(json.dumps(data, indent=2))
        return

    if local:
        rows = [_format_bin_row(bv) for bv in local]
        print_table(title="Local Binaries", columns=["Active", "Version", "Path"], rows=rows)
    else:
        print_warning("No local binaries found")

    if remote:
        try:
            remote_versions = list_remote_versions(limit=limit)
        except BinaryError as exc:
            print_error(str(exc))
            raise typer.Exit(code=1)

        rows = []
        for ver in remote_versions:
            cached = "✓" if ver in local_versions else " "
            rows.append([cached, ver])

        print_table(title="Remote Releases", columns=["Downloaded", "Version"], rows=rows)


@bin_app.command(name="fetch")
def bin_fetch(
    version: str = typer.Argument(..., help="Version to download (e.g. 1.15.0)"),
) -> None:
    """Download a specific Firecracker version."""
    try:
        bv = fetch_binary(version)
    except BinaryError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    print_success(f"Downloaded v{bv.version}: {bv.firecracker_path}")


@bin_app.command(name="set-default")
def bin_set_default(
    version: str = typer.Argument(..., help="Version to set as active default"),
) -> None:
    """Set the active Firecracker binary version."""
    try:
        set_active_version(version)
    except AssetNotFoundError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)
    print_success(f"Active version set to {version}")


@bin_app.command(name="rm")
def bin_rm(
    versions: Optional[List[str]] = typer.Argument(None, help="Version(s) to remove"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove one or more cached Firecracker versions."""
    effective_versions: list[str] = list(versions) if versions else []
    if not effective_versions:
        print_error("Provide at least one version to remove")
        raise typer.Exit(code=1)

    exit_code = 0
    for version in effective_versions:
        if not force:
            typer.confirm(f"Remove Firecracker v{version}?", abort=True)
        try:
            remove_version(version)
            print_success(f"Removed v{version}")
        except AssetNotFoundError as exc:
            print_error(str(exc))
            exit_code = 1

    raise typer.Exit(code=exit_code)


def clear_assets(
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove all cached assets (bin, kernels, images). Does NOT touch VMs."""
    cache = get_cache_dir()
    targets = ["bin", "kernels", "images"]
    dirs_to_remove = [cache / t for t in targets if (cache / t).exists()]

    if not dirs_to_remove:
        print_warning("Nothing to clear")
        raise typer.Exit(code=0)

    if not force:
        names = ", ".join(d.name for d in dirs_to_remove)
        typer.confirm(f"Remove cached assets ({names})?", abort=True)

    for d in dirs_to_remove:
        shutil.rmtree(d)
        print_success(f"Removed {d}")
