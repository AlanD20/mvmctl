"""Asset management commands — unified interface for kernels, images, and binaries."""

import json
import shutil

import typer
from pathlib import Path
from typing import Optional

from fcm.api.assets import (
    BinaryVersion,
    fetch_binary,
    fetch_image,
    import_image,
    ImageImportSpec,
    list_local_versions,
    list_remote_versions,
    load_images_config,
    remove_version,
    set_active_version,
    build_kernel_pipeline,
)
from fcm.constants import (
    KERNEL_TARBALL_URL_TEMPLATE,
    DEFAULT_KERNEL_VERSION,
    DEFAULT_FC_KERNEL_ARCH,
)
from fcm.exceptions import AssetNotFoundError, BinaryError, ImageError, KernelError
from fcm.utils.console import print_error, print_info, print_success, print_table, print_warning
from fcm.utils.fs import get_assets_dir, get_cache_dir, get_images_dir, get_kernels_dir

kernel_app = typer.Typer(help="Kernel management", no_args_is_help=False)
image_app = typer.Typer(help="Image management", no_args_is_help=False)
bin_app = typer.Typer(help="Binary management", no_args_is_help=False)


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
    kernels_dir: Path = typer.Option(get_kernels_dir(), "--kernels-dir", help="Kernels directory"),
    firecracker_only: bool = typer.Option(
        False, "--firecracker", help="Show only firecracker kernels"
    ),
    official_only: bool = typer.Option(
        False, "--official", help="Show only official/upstream kernels"
    ),
) -> None:
    """List cached kernels (both Firecracker CI and official upstream)."""
    from fcm.core.kernel import list_kernels

    kernels_dir.mkdir(parents=True, exist_ok=True)
    kernels = list_kernels(kernels_dir)

    if firecracker_only:
        kernels = [k for k in kernels if k.get("type") == "firecracker"]
    elif official_only:
        kernels = [k for k in kernels if k.get("type") == "official"]

    if json_output:
        typer.echo(json.dumps(kernels, indent=2))
        return

    if not kernels:
        from fcm.utils.console import print_info

        print_info("No kernels found. Use 'fcm kernel fetch --type firecracker' to download one.")
        return

    rows: list[list[str]] = []
    for k in kernels:
        default_marker = "✓" if k.get("is_default") else " "
        rows.append(
            [
                default_marker,
                k.get("name", ""),
                k.get("version", ""),
                k.get("type", ""),
                k.get("built_at", "-"),
                k.get("size", "-"),
            ]
        )
    print_table(
        title="Cached Kernels",
        columns=["Def", "Name", "Version", "Type", "Built At", "Size"],
        rows=rows,
    )


def _get_ci_version() -> str:
    from fcm.core.cli_state import get_cli_state

    state = get_cli_state()
    ci_version = state.get("firecracker_ci_version") or state.get("ci_version") or ""
    if not ci_version:
        from fcm.core.binary_manager import list_local_versions as _lv

        local = _lv()
        active = next((b for b in local if b.is_active), None)
        if active:
            parts = active.version.split(".")
            ci_version = f"{parts[0]}.{parts[1]}" if len(parts) >= 2 else active.version
    return ci_version or "1.12"


@kernel_app.command(name="fetch")
def kernel_fetch(
    kernel_type: str = typer.Option(..., "--type", help="Kernel type: firecracker or official"),
    version: Optional[str] = typer.Option(
        None,
        "--version",
        help="Kernel version (default: FIRECRACKER_CI_VERSION for firecracker, 6.19.9 for official)",
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
    """Fetch or build a kernel. --type firecracker|official is required."""
    from fcm.core.kernel import download_firecracker_kernel

    kernels_dir = get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)

    if kernel_type == "firecracker":
        ci_version = version or _get_ci_version()
        output_name = f"vmlinux-fc-{ci_version}-{arch}" if out is None else out.name
        try:
            result = download_firecracker_kernel(
                ci_version=ci_version,
                arch=arch,
                kernels_dir=kernels_dir,
                output_name=output_name,
            )
        except KernelError as exc:
            print_error(f"Kernel fetch failed: {exc}")
            raise typer.Exit(code=1) from exc
        print_success(f"Firecracker CI kernel ready: {result}")

    elif kernel_type == "official":
        effective_version = version or DEFAULT_KERNEL_VERSION
        output_name_str = f"vmlinux-{effective_version}"
        output_path = out if out is not None else kernels_dir / output_name_str

        if kernel_config and not kernel_config.exists():
            print_error(f"Kernel config file not found: {kernel_config}")
            raise typer.Exit(code=1)

        source_url = KERNEL_TARBALL_URL_TEMPLATE.format(version=effective_version)
        try:
            build_dir_path = build_kernel_pipeline(
                version=effective_version,
                source_url=source_url,
                output_path=output_path,
                build_dir=None,
                jobs=jobs,
                keep_build_dir=keep_build_dir,
                user_config_path=kernel_config,
            )
        except KernelError as exc:
            print_error(f"Kernel build failed: {exc}")
            raise typer.Exit(code=1) from exc

        if keep_build_dir:
            print_info(f"Build directory kept at: {build_dir_path}")

        result = output_path
        print_success(f"Kernel built: {result}")

    else:
        print_error(f"Unknown kernel type: {kernel_type!r}. Use 'firecracker' or 'official'.")
        raise typer.Exit(code=1)

    if set_default:
        from fcm.core.kernel import set_default_kernel as _set_default

        _set_default(kernels_dir, result.name)
        print_success(f"Default kernel set to: {result.name}")

    raise typer.Exit(code=0)


@kernel_app.command(name="set-default")
def kernel_set_default(
    name: str = typer.Argument(..., help="Kernel file name to set as default"),
    kernels_dir: Path = typer.Option(get_kernels_dir(), "--kernels-dir", help="Kernels directory"),
) -> None:
    """Set a kernel as the default for VM creation."""
    from fcm.core.kernel import set_default_kernel

    try:
        set_default_kernel(kernels_dir, name)
    except KernelError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1) from exc
    print_success(f"Default kernel set to: {name}")


@kernel_app.command(name="remove", hidden=True)
def kernel_remove(
    name: str = typer.Argument(..., help="Kernel file name to remove"),
    kernels_dir: Path = typer.Option(get_kernels_dir(), "--kernels-dir", help="Kernels directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove a cached kernel."""
    path = kernels_dir / name
    if not path.exists():
        print_error(f"Kernel not found: {path}")
        raise typer.Exit(code=1)

    if not force:
        typer.confirm(f"Remove {path}?", abort=True)

    path.unlink()
    print_success(f"Removed {path}")


@kernel_app.command(name="rm")
def kernel_rm(
    name: str = typer.Argument(..., help="Kernel file name to remove"),
    kernels_dir: Path = typer.Option(get_kernels_dir(), "--kernels-dir", help="Kernels directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Alias for remove."""
    kernel_remove(name=name, kernels_dir=kernels_dir, force=force)


def _load_image_meta(images_dir: Path, image_id: str) -> dict[str, str]:
    for ext in (".ext4", ".btrfs", ".img", ".raw"):
        meta_path = images_dir / f"{image_id}{ext}.json"
        if meta_path.exists():
            try:
                raw: object = json.loads(meta_path.read_text())
                if isinstance(raw, dict):
                    return {str(k): str(v) for k, v in raw.items()}
            except (json.JSONDecodeError, OSError):
                pass
    meta_path = images_dir / f"{image_id}.json"
    if meta_path.exists():
        try:
            raw = json.loads(meta_path.read_text())
            if isinstance(raw, dict):
                return {str(k): str(v) for k, v in raw.items()}
        except (json.JSONDecodeError, OSError):
            pass
    return {}


def _save_image_meta(
    images_dir: Path, image_id: str, image_path: Path, meta: dict[str, str]
) -> None:
    from datetime import datetime, timezone

    meta.setdefault("pulled_at", datetime.now(tz=timezone.utc).isoformat())
    meta.setdefault("fs_type", image_path.suffix.lstrip(".") if image_path.suffix else "unknown")
    meta_path = images_dir / f"{image_path.name}.json"
    meta_path.write_text(json.dumps(meta, indent=2))


@image_app.command(name="ls")
def image_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    images_dir: Path = typer.Option(get_images_dir(), "--images-dir", help="Images directory"),
    remote: bool = typer.Option(False, "--remote", "-r", help="Show available remote images"),
    name_filter: Optional[str] = typer.Option(None, "--name", help="Filter by image name"),
) -> None:
    """List cached images (or available remote images with --remote)."""
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
                    for ext in (".ext4", ".btrfs", ".img", ".raw")
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
                columns=["Downloaded", "ID", "Name", "FS Type"],
                rows=rows,
            )
        return

    if json_output:
        result = []
        for img in images:
            meta = _load_image_meta(images_dir, img.id)
            result.append(
                {
                    "id": img.id,
                    "name": img.name,
                    "format": img.format,
                    "fs_type": meta.get("fs_type", img.convert_to),
                    "pulled_at": meta.get("pulled_at", "-"),
                }
            )
        typer.echo(json.dumps(result, indent=2))
        return

    default_img = _get_default_image()
    rows_local: list[list[str]] = []
    for img in images:
        found_path = next(
            (
                images_dir / f"{img.id}{ext}"
                for ext in (".ext4", ".btrfs", ".img", ".raw")
                if (images_dir / f"{img.id}{ext}").exists()
            ),
            None,
        )
        if found_path is None:
            continue
        meta = _load_image_meta(images_dir, img.id)
        pulled_at = meta.get("pulled_at", "-")[:19] if meta.get("pulled_at") else "-"
        fs_type = meta.get("fs_type", found_path.suffix.lstrip("."))
        default_marker = "✓" if img.id == default_img else " "
        rows_local.append([default_marker, img.id, img.name, fs_type, pulled_at])

    if not rows_local:
        print_info("No images downloaded. Use 'fcm image fetch <id>' to download one.")
        return
    print_table(
        title="Downloaded Images",
        columns=["Def", "ID", "OS Name", "FS Type", "Pulled At"],
        rows=rows_local,
    )


def _get_default_image() -> str | None:
    try:
        from fcm.core.cli_state import get_cli_state_value

        val = get_cli_state_value("default_image")
        return str(val) if val is not None else None
    except Exception:
        return None


@image_app.command(name="fetch")
def image_fetch(
    image_name: str = typer.Argument(
        ..., help="Image name/ID (e.g. ubuntu) or full ID (e.g. ubuntu-24.04)"
    ),
    codename: Optional[str] = typer.Argument(
        None, help="Optional version codename (e.g. noble, jammy)"
    ),
    out: Path = typer.Option(get_images_dir(), "--out", help="Output directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Re-download even if exists"),
    set_default: bool = typer.Option(
        False, "--set-default", help="Set as default image after download"
    ),
) -> None:
    """Download an image. Pass name (ubuntu) or full ID (ubuntu-24.04), optionally with codename."""
    out.mkdir(parents=True, exist_ok=True)
    config_path = get_assets_dir() / "images.yaml"
    images = load_images_config(config_path)

    full_id = f"{image_name}-{codename}" if codename else image_name
    spec = next((img for img in images if img.id == full_id), None)
    if spec is None:
        spec = next(
            (img for img in images if img.id.startswith(image_name + "-") or img.id == image_name),
            None,
        )
    if spec is None:
        available = ", ".join(img.id for img in images)
        print_error(f"Image '{full_id}' not found. Available: {available}")
        raise typer.Exit(code=1)

    result = fetch_image(spec, out, force)
    if result:
        _save_image_meta(
            out, spec.id, result, {"os_name": spec.name, "fs_type": result.suffix.lstrip(".")}
        )
        print_success(f"Image ready: {result}")
        if set_default:
            from fcm.core.cli_state import set_cli_state_value

            set_cli_state_value("default_image", spec.id)
            print_success(f"Default image set to: {spec.id}")
        raise typer.Exit(code=0)
    else:
        print_error(f"Failed to download image '{spec.id}'")
        raise typer.Exit(code=1)


@image_app.command(name="set-default")
def image_set_default(
    image_id: str = typer.Argument(..., help="Image ID to set as default"),
    images_dir: Path = typer.Option(get_images_dir(), "--images-dir", help="Images directory"),
) -> None:
    """Set the default image for VM creation."""
    images_dir.mkdir(parents=True, exist_ok=True)
    found = any(
        (images_dir / f"{image_id}{ext}").exists() for ext in (".ext4", ".btrfs", ".img", ".raw")
    )
    if not found:
        print_error(f"Image '{image_id}' not found in {images_dir}. Download it first.")
        raise typer.Exit(code=1)
    from fcm.core.cli_state import set_cli_state_value

    set_cli_state_value("default_image", image_id)
    print_success(f"✓ Default image set to: {image_id}")


@image_app.command(name="remove", hidden=True)
def image_remove(
    id: str = typer.Argument(..., help="Image ID to remove"),
    images_dir: Path = typer.Option(get_images_dir(), "--images-dir", help="Images directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove a cached image."""
    patterns = [f"{id}.ext4", f"{id}.btrfs", f"{id}.img", f"{id}.raw"]
    found = [images_dir / p for p in patterns if (images_dir / p).exists()]

    if not found:
        print_error(f"No image files found for '{id}'")
        raise typer.Exit(code=1)

    if not force:
        typer.confirm(f"Remove {len(found)} file(s) for '{id}'?", abort=True)

    for path in found:
        if path.is_dir():
            shutil.rmtree(path)
        else:
            path.unlink()
        print_success(f"Removed: {path}")

    raise typer.Exit(code=0)


@image_app.command(name="rm")
def image_rm(
    id: str = typer.Argument(..., help="Image ID to remove"),
    images_dir: Path = typer.Option(get_images_dir(), "--images-dir", help="Images directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Alias for remove."""
    image_remove(id=id, images_dir=images_dir, force=force)


_FORMAT_EXT_MAP: dict[str, str] = {
    ".qcow2": "qcow2",
    ".raw": "raw",
    ".img": "raw",
    ".tar": "tar-rootfs",
    ".tar.gz": "tar-rootfs",
    ".tar.xz": "tar-rootfs",
    ".tgz": "tar-rootfs",
}


@image_app.command(name="import")
def image_import(
    image_id: str = typer.Argument(..., help="Unique ID for the imported image"),
    source_path: Path = typer.Argument(..., help="Path to local image file"),
    format: str = typer.Option(
        "auto", "--format", help="Image format: qcow2, raw, tar-rootfs, or auto"
    ),
    convert_to: str = typer.Option("ext4", "--convert-to", help="Target filesystem format"),
    size_mib: int = typer.Option(2048, "--size-mib", help="Size in MiB for tar-rootfs import"),
    force: bool = typer.Option(False, "--force", "-f", help="Overwrite existing"),
    set_default: bool = typer.Option(False, "--set-default", help="Set as default after import"),
    images_dir: Path = typer.Option(get_images_dir(), "--images-dir", help="Output directory"),
) -> None:
    """Import a local image file (qcow2, raw, tar-rootfs)."""
    resolved_format: str | None = format
    if resolved_format == "auto":
        name = source_path.name.lower()
        resolved_format = next(
            (fmt for ext, fmt in _FORMAT_EXT_MAP.items() if name.endswith(ext)),
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
        name=image_id,
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
        {"os_name": image_id, "fs_type": result.suffix.lstrip(".")},
    )
    print_success(f"Image imported: {result}")

    if set_default:
        from fcm.core.cli_state import set_cli_state_value

        set_cli_state_value("default_image", image_id)
        print_success(f"Default image set to: {image_id}")

    raise typer.Exit(code=0)


def _format_bin_row(bv: BinaryVersion) -> list[str]:
    active = "✓" if bv.is_active else " "
    return [active, bv.version, str(bv.firecracker_path)]


@bin_app.command(name="ls")
def bin_ls(
    remote: bool = typer.Option(False, "--remote", "-r", help="Also show remote versions"),
    limit: int = typer.Option(5, "--limit", help="Max remote versions to show"),
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

        print_table(title="Remote Releases", columns=["Cached", "Version"], rows=rows)


@bin_app.command(name="fetch")
def bin_fetch(
    version: str = typer.Argument(..., help="Version to download (e.g. 1.12.0)"),
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


@bin_app.command(name="use", hidden=True)
def bin_use(
    version: str = typer.Argument(..., help="Version to activate"),
) -> None:
    """Set the active Firecracker version (deprecated: use set-default)."""
    bin_set_default(version)


@bin_app.command(name="remove", hidden=True)
def bin_remove(
    version: str = typer.Argument(..., help="Version to remove"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove a cached Firecracker version."""
    if not force:
        typer.confirm(f"Remove Firecracker v{version}?", abort=True)

    try:
        remove_version(version)
    except AssetNotFoundError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    print_success(f"Removed v{version}")


@bin_app.command(name="rm")
def bin_rm(
    version: str = typer.Argument(..., help="Version to remove"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Alias for remove."""
    bin_remove(version=version, force=force)


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
