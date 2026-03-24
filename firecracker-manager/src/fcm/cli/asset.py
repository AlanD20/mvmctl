"""Asset management commands — unified interface for kernels, images, and binaries."""

import json
import shutil

import typer
from pathlib import Path

from fcm.api.assets import (
    BinaryVersion,
    fetch_binary,
    list_local_versions,
    list_remote_versions,
    remove_version,
    set_active_version,
    fetch_image,
    load_images_config,
    build_kernel_pipeline,
)
from fcm.exceptions import AssetNotFoundError, BinaryError, KernelError
from fcm.utils.console import print_error, print_success, print_table, print_warning
from fcm.utils.fs import get_assets_dir, get_cache_dir, get_images_dir, get_kernels_dir

app = typer.Typer(help="Asset management")
kernel_app = typer.Typer(help="Kernel management")
image_app = typer.Typer(help="Image management")
bin_app = typer.Typer(help="Binary management")
app.add_typer(kernel_app, name="kernel")
app.add_typer(image_app, name="image")
app.add_typer(bin_app, name="bin")


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the asset command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@kernel_app.command(name="help", hidden=True)
def kernel_help(ctx: typer.Context) -> None:
    """Show help for the kernel subcommand."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@image_app.command(name="help", hidden=True)
def image_help(ctx: typer.Context) -> None:
    """Show help for the image subcommand."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@bin_app.command(name="help", hidden=True)
def bin_help(ctx: typer.Context) -> None:
    """Show help for the bin subcommand."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@kernel_app.command(name="ls")
def kernel_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    kernels_dir: Path = typer.Option(get_kernels_dir(), "--kernels-dir", help="Kernels directory"),
) -> None:
    """List cached kernels."""
    if not kernels_dir.exists():
        print_error(f"Kernels directory not found: {kernels_dir}")
        raise typer.Exit(code=1)

    kernels: list[list[str]] = []
    for path in kernels_dir.iterdir():
        if path.is_file() and path.name.startswith("vmlinux"):
            size_mb = path.stat().st_size / (1024 * 1024)
            kernels.append([path.name, f"{size_mb:.1f} MiB"])

    if json_output:
        typer.echo(json.dumps([{"name": k[0], "size": k[1]} for k in kernels], indent=2))
    else:
        print_table(title="Available Kernels", columns=["Name", "Size"], rows=kernels)


@kernel_app.command(name="fetch")
def kernel_fetch(
    version: str = typer.Option("6.1.102", "--version", help="Kernel version"),
    out: Path = typer.Option(get_kernels_dir() / "vmlinux", "--out", help="Output path"),
) -> None:
    """Download the official minimal kernel."""
    source_url = f"https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-{version}.tar.xz"
    try:
        build_kernel_pipeline(
            version=version,
            source_url=source_url,
            output_path=out,
            build_dir=get_cache_dir() / "kernel-build",
            jobs=None,
        )
    except KernelError as exc:
        print_error(f"Kernel build failed: {exc}")
        raise typer.Exit(code=1) from exc
    print_success(f"Kernel built: {out}")
    raise typer.Exit(code=0)


@kernel_app.command(name="build")
def kernel_build(
    version: str | None = typer.Option("6.1.102", "--version", help="Kernel version to build"),
    jobs: int | None = typer.Option(None, "--jobs", "-j", help="Parallel build jobs"),
    out: Path = typer.Option(get_kernels_dir() / "vmlinux", "--out", help="Output path"),
    build_dir: Path = typer.Option(
        get_cache_dir() / "kernel-build", "--build-dir", help="Build directory"
    ),
) -> None:
    """Build a custom upstream kernel."""
    source_url = f"https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-{version}.tar.xz"
    try:
        build_kernel_pipeline(
            version=version or "6.1.102",
            source_url=source_url,
            output_path=out,
            build_dir=build_dir,
            jobs=jobs,
        )
    except KernelError as exc:
        print_error(f"Kernel build failed: {exc}")
        raise typer.Exit(code=1) from exc
    print_success(f"Kernel built: {out}")


@kernel_app.command(name="remove")
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


@kernel_app.command(name="rm", hidden=True)
def kernel_rm(
    name: str = typer.Argument(..., help="Kernel file name to remove"),
    kernels_dir: Path = typer.Option(get_kernels_dir(), "--kernels-dir", help="Kernels directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Alias for remove."""
    kernel_remove(name=name, kernels_dir=kernels_dir, force=force)


@image_app.command(name="ls")
def image_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    images_dir: Path = typer.Option(get_images_dir(), "--images-dir", help="Images directory"),
) -> None:
    """List cached images."""
    config_path = get_assets_dir() / "images.yaml"
    images = load_images_config(config_path)

    if json_output:
        data = [{"id": img.id, "name": img.name, "format": img.format} for img in images]
        typer.echo(json.dumps(data, indent=2))
    else:
        rows: list[list[str]] = []
        for img in images:
            ext4_path = images_dir / f"{img.id}.ext4"
            btrfs_path = images_dir / f"{img.id}.btrfs"
            exists = "✓" if (ext4_path.exists() or btrfs_path.exists()) else " "
            rows.append([exists, img.id, img.name, img.format])

        print_table(title="Available Images", columns=["", "ID", "Name", "Format"], rows=rows)


@image_app.command(name="fetch")
def image_fetch(
    id: str = typer.Argument(..., help="Image ID from images.yaml"),
    out: Path = typer.Option(get_images_dir(), "--out", help="Output directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Re-download even if exists"),
) -> None:
    """Download an image."""
    config_path = get_assets_dir() / "images.yaml"
    images = load_images_config(config_path)

    spec = next((img for img in images if img.id == id), None)
    if not spec:
        print_error(f"Image '{id}' not found in images.yaml")
        raise typer.Exit(code=1)

    result = fetch_image(spec, out, force)
    if result:
        print_success(f"Image ready: {result}")
        raise typer.Exit(code=0)
    else:
        raise typer.Exit(code=1)


@image_app.command(name="remove")
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


@image_app.command(name="rm", hidden=True)
def image_rm(
    id: str = typer.Argument(..., help="Image ID to remove"),
    images_dir: Path = typer.Option(get_images_dir(), "--images-dir", help="Images directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Alias for remove."""
    image_remove(id=id, images_dir=images_dir, force=force)


def _format_bin_row(bv: BinaryVersion) -> list[str]:
    """Return a table row for a binary version entry.

    Args:
        bv: The binary version to format.

    Returns:
        A three-element list containing the active marker, version string, and path.
    """
    active = "✓" if bv.is_active else " "
    return [active, bv.version, str(bv.firecracker_path)]


@bin_app.command(name="ls")
def bin_ls(
    remote: bool = typer.Option(False, "--remote", "-r", help="Also show remote versions"),
    limit: int = typer.Option(10, "--limit", help="Max remote versions to show"),
) -> None:
    """List local (and optionally remote) Firecracker versions."""
    local = list_local_versions()
    local_versions = {bv.version for bv in local}

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


@bin_app.command(name="use")
def bin_use(
    version: str = typer.Argument(..., help="Version to activate"),
) -> None:
    """Set the active Firecracker version."""
    try:
        set_active_version(version)
    except AssetNotFoundError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    print_success(f"Active version set to {version}")


@bin_app.command(name="remove")
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


@bin_app.command(name="rm", hidden=True)
def bin_rm(
    version: str = typer.Argument(..., help="Version to remove"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Alias for remove."""
    bin_remove(version=version, force=force)


@app.command(name="clear")
def cache_clear(
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
