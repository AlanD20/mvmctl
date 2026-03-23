"""Kernel management commands."""

import json
import shutil
import typer
from pathlib import Path

from fcm.core.kernel import build_kernel_pipeline
from fcm.utils.console import print_error, print_success, print_table
from fcm.utils.fs import get_kernels_dir, get_cache_dir

app = typer.Typer(help="Kernel management")


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the kernel command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@app.command()
def build(
    version: str | None = typer.Option("6.1.102", "--version", help="Kernel version to build"),
    config: Path | None = typer.Option(None, "--config", help="Config fragment file"),
    jobs: int | None = typer.Option(None, "--jobs", "-j", help="Parallel build jobs"),
    out: Path = typer.Option(get_kernels_dir() / "vmlinux", "--out", help="Output path"),
    build_dir: Path = typer.Option(
        get_cache_dir() / "kernel-build", "--build-dir", help="Build directory"
    ),
) -> None:
    """Download and compile the kernel."""
    source_url = f"https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-{version}.tar.xz"

    success = build_kernel_pipeline(
        version=version or "6.1.102",
        source_url=source_url,
        output_path=out,
        build_dir=build_dir,
        jobs=jobs,
    )

    if success:
        print_success(f"Kernel built successfully: {out}")
        raise typer.Exit(code=0)
    else:
        print_error("Kernel build failed")
        raise typer.Exit(code=1)


@app.command(name="ls")
def ls_kernels(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    kernels_dir: Path = typer.Option(get_kernels_dir(), "--kernels-dir", help="Kernels directory"),
) -> None:
    """Show locally built kernels."""
    if not kernels_dir.exists():
        print_error(f"Kernels directory not found: {kernels_dir}")
        raise typer.Exit(code=1)

    kernels = []
    for path in kernels_dir.iterdir():
        if path.is_file() and path.name.startswith("vmlinux"):
            size_mb = path.stat().st_size / (1024 * 1024)
            kernels.append([path.name, f"{size_mb:.1f} MiB"])

    if json_output:
        typer.echo(json.dumps([{"name": k[0], "size": k[1]} for k in kernels], indent=2))
    else:
        print_table(
            title="Available Kernels",
            columns=["Name", "Size"],
            rows=kernels,
        )


@app.command(name="list", hidden=True)
def list_kernels(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    kernels_dir: Path = typer.Option(get_kernels_dir(), "--kernels-dir", help="Kernels directory"),
) -> None:
    """Alias for ls."""
    ls_kernels(json_output=json_output, kernels_dir=kernels_dir)


@app.command()
def clean(
    version: str | None = typer.Option(None, "--version", help="Specific version to clean"),
    build_dir: Path = typer.Option(
        get_cache_dir() / "kernel-build", "--build-dir", help="Build directory"
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Force without confirmation"),
) -> None:
    """Remove kernel build artifacts."""
    if not build_dir.exists():
        print_success("Nothing to clean")
        raise typer.Exit(code=0)

    if version:
        target_dir = build_dir / f"linux-{version}"
        if target_dir.exists():
            if not force:
                typer.confirm(f"Remove {target_dir}?", abort=True)
            shutil.rmtree(target_dir)
            print_success(f"Removed {target_dir}")
        else:
            print_error(f"Build directory for {version} not found")
            raise typer.Exit(code=1)
    else:
        if not force:
            typer.confirm(f"Remove all build artifacts in {build_dir}?", abort=True)
        shutil.rmtree(build_dir)
        print_success(f"Removed {build_dir}")
