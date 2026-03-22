"""Image management commands."""

import json
import typer
from pathlib import Path

from fcm.core.image import load_images_config, fetch_image
from fcm.utils.console import print_table, print_error, print_success
from fcm.utils.fs import get_images_dir, get_assets_dir

app = typer.Typer(help="Image management")


@app.command()
def fetch(
    id: str = typer.Argument(..., help="Image ID from images.yaml"),
    out: Path = typer.Option(get_images_dir(), "--out", help="Output directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Re-download even if exists"),
) -> None:
    """Download and convert an image."""
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


@app.command()
def fetch_all(
    force: bool = typer.Option(False, "--force", help="Re-download all images"),
    out: Path = typer.Option(get_images_dir(), "--out", help="Output directory"),
) -> None:
    """Fetch all images defined in images.yaml."""
    config_path = get_assets_dir() / "images.yaml"
    images = load_images_config(config_path)

    if not images:
        print_error("No images defined in images.yaml")
        raise typer.Exit(code=1)

    success_count = 0
    for spec in images:
        typer.echo(f"\n--- {spec.name} ---")
        result = fetch_image(spec, out, force)
        if result:
            success_count += 1

    typer.echo(f"\n{success_count}/{len(images)} images ready")


@app.command(name="list")
def list_images(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    images_dir: Path = typer.Option(get_images_dir(), "--images-dir", help="Images directory"),
) -> None:
    """Show locally available images."""
    config_path = get_assets_dir() / "images.yaml"
    images = load_images_config(config_path)

    if json_output:
        data = [{"id": img.id, "name": img.name, "format": img.format} for img in images]
        typer.echo(json.dumps(data, indent=2))
    else:
        rows = []
        for img in images:
            # Check if image exists locally
            ext4_path = images_dir / f"{img.id}.ext4"
            btrfs_path = images_dir / f"{img.id}.btrfs"
            exists = "✓" if (ext4_path.exists() or btrfs_path.exists()) else " "
            rows.append([exists, img.id, img.name, img.format])

        print_table(
            title="Available Images",
            columns=["", "ID", "Name", "Format"],
            rows=rows,
        )


@app.command()
def convert(
    src: Path = typer.Option(..., "--src", help="Source image file"),
    dst: Path = typer.Option(..., "--dst", help="Destination file"),
    format: str = typer.Option("ext4", "--format", help="Target format"),
    size: str | None = typer.Option(None, "--size", help="Target size (e.g., 2G)"),
) -> None:
    """Convert an existing image file."""
    from fcm.core.image import (
        convert_qcow2_to_raw,
        extract_partition_from_raw,
        create_ext4_from_tar,
    )

    if not src.exists():
        print_error(f"Source file not found: {src}")
        raise typer.Exit(code=1)

    success = False
    if src.suffix == ".qcow2":
        raw_path = dst.with_suffix(".raw")
        if convert_qcow2_to_raw(src, raw_path):
            result_path = extract_partition_from_raw(raw_path, dst)
            success = result_path is not None
            raw_path.unlink(missing_ok=True)
    elif src.suffix == ".tar" or str(src).endswith(".tar.gz") or str(src).endswith(".tar.xz"):
        success = create_ext4_from_tar(src, dst, size or "2G")
    elif src.suffix == ".raw" or src.suffix == ".img":
        result_path = extract_partition_from_raw(src, dst)
        success = result_path is not None
    else:
        print_error(f"Unknown source format: {src.suffix}")

    if success:
        print_success(f"Converted to {dst}")
        raise typer.Exit(code=0)
    else:
        raise typer.Exit(code=1)


@app.command()
def delete(
    id: str = typer.Option(..., "--id", help="Image ID to delete"),
    images_dir: Path = typer.Option(get_images_dir(), "--images-dir", help="Images directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Force delete without confirmation"),
) -> None:
    """Remove a local image."""
    import shutil

    # Find image files matching the ID
    patterns = [f"{id}.ext4", f"{id}.btrfs", f"{id}.img", f"{id}.raw"]
    found = []
    for pattern in patterns:
        path = images_dir / pattern
        if path.exists():
            found.append(path)

    if not found:
        print_error(f"No image files found for '{id}'")
        raise typer.Exit(code=1)

    if not force:
        typer.confirm(f"Delete {len(found)} file(s) for '{id}'?", abort=True)

    for path in found:
        shutil.rmtree(path, ignore_errors=True) if path.is_dir() else path.unlink()
        print_success(f"Deleted: {path}")

    raise typer.Exit(code=0)
