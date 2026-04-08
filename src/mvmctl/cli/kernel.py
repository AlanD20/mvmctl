"""Kernel management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Optional

import typer

from mvmctl.api.kernel import (
    fetch_kernel,
    list_kernels,
    register_fetched_kernel,
    remove_kernel,
    resolve_kernel_spec,
    set_default_kernel,
)
from mvmctl.constants import (
    DEFAULT_IMAGE_ARCH,
    KERNEL_TYPE_FIRECRACKER,
    KERNEL_TYPE_OFFICIAL,
)
from mvmctl.exceptions import KernelError
from mvmctl.models import KernelFetchInput
from mvmctl.utils.console import (
    get_combined_marker,
    print_error,
    print_info,
    print_success,
    print_table,
    print_warning,
)
from mvmctl.utils.disk_size import format_bytes_human_readable
from mvmctl.utils.fs import get_kernels_dir, is_file_missing
from mvmctl.utils.full_hash import shorten_hash
from mvmctl.utils.time import human_readable_time

kernel_app = typer.Typer(
    help="Kernel management.",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@kernel_app.callback()
def kernel_callback(ctx: typer.Context) -> None:
    pass


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
        print_info("No kernels found. Use 'mvm kernel fetch --type firecracker' to download one.")

    rows: list[list[str]] = []
    for k in kernels:
        is_default = k.get("is_default") == "true"
        last_modified_display = human_readable_time(k.get("last_modified", "-"))
        path_str = k.get("path", "")
        path = kernels_dir / path_str if path_str else None
        is_missing = is_file_missing(path)
        display_id = get_combined_marker(is_default, is_missing) + shorten_hash(k.get("id", ""), 12)
        size = path.stat().st_size if path and path.exists() else 0
        size_str = format_bytes_human_readable(size) if size > 0 else "-"
        rows.append(
            [
                display_id,
                k.get("name", "-"),
                k.get("version", ""),
                k.get("arch", "-"),
                k.get("type", ""),
                last_modified_display,
                size_str,
            ]
        )
    print_table(
        columns=["ID", "Name", "Version", "Arch", "Type", "Last Modified", "Size"],
        rows=rows,
    )


@kernel_app.command(name="fetch")
def kernel_fetch(
    kernel_type: Optional[str] = typer.Option(
        None, "--type", help="Kernel type from kernels.yaml (e.g. firecracker, official)"
    ),
    firecracker: bool = typer.Option(
        False, "--firecracker", help="Shortcut for --type firecracker"
    ),
    official: bool = typer.Option(False, "--official", help="Shortcut for --type official"),
    version: Optional[str] = typer.Option(
        None,
        "--version",
        help="Kernel spec version from kernels.yaml (required if multiple specs share the same type)",
    ),
    arch: Optional[str] = typer.Option(None, "--arch", help="Architecture"),
    out: Optional[Path] = typer.Option(None, "--out", help="Output path/name"),
    name: Optional[str] = typer.Option(
        None,
        "--name",
        help="Override output filename only (placed in kernels directory unless --out is used)",
    ),
    jobs: Optional[int] = typer.Option(
        None, "--jobs", "-j", help="Parallel build jobs (official only)"
    ),
    keep_build_dir: bool = typer.Option(
        False, "--keep-build-dir", help="Keep build directory after build"
    ),
    clean_build: bool = typer.Option(
        False,
        "--clean-build",
        help="Skip kernel build cache and force a clean build",
    ),
    kernel_config: Optional[Path] = typer.Option(
        None, "--kernel-config", help="Path to custom kernel .config file"
    ),
    set_default: bool = typer.Option(False, "--set-default", help="Set this kernel as default"),
) -> None:
    kernels_dir = get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)

    if name is not None and out is not None:
        print_error("--name cannot be combined with --out")
        raise typer.Exit(code=1)

    if firecracker and official:
        print_error("--firecracker cannot be combined with --official")
        raise typer.Exit(code=1)

    if firecracker:
        if kernel_type is not None and kernel_type != KERNEL_TYPE_FIRECRACKER:
            print_error("--firecracker cannot be combined with a different --type value")
            raise typer.Exit(code=1)
        resolved_type = KERNEL_TYPE_FIRECRACKER
    elif official:
        if kernel_type is not None and kernel_type != KERNEL_TYPE_OFFICIAL:
            print_error("--official cannot be combined with a different --type value")
            raise typer.Exit(code=1)
        resolved_type = KERNEL_TYPE_OFFICIAL
    elif kernel_type is None:
        print_error("Provide --type <kernel-type> or use --firecracker/--official")
        raise typer.Exit(code=1)
    else:
        resolved_type = kernel_type

    try:
        spec = resolve_kernel_spec(kernel_type=resolved_type, version=version)
    except KernelError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1) from exc

    if arch is None:
        arch = DEFAULT_IMAGE_ARCH

    try:
        fetch_input = KernelFetchInput(
            kernel_type=resolved_type,
            version=version,
            arch=arch,
            output_dir=kernels_dir,
            output_name=name,
            output_path=out,
            jobs=jobs,
            keep_build_dir=keep_build_dir,
            clean_build=clean_build,
            kernel_config=kernel_config,
        )
        result = fetch_kernel(input=fetch_input)
    except KernelError as exc:
        print_error(f"Kernel fetch failed: {exc}")
        raise typer.Exit(code=1) from exc

    for warning in result.warnings:
        print_warning(warning)
    for info in result.info_messages:
        print_success(info)

    try:
        register_fetched_kernel(result, spec, set_default=set_default)
        if set_default:
            print_success(f"Default kernel set to: {result.name}")
    except KernelError as exc:
        print_error(f"Failed to register kernel: {exc}")
        raise typer.Exit(code=1) from exc

    raise typer.Exit(code=0)


@kernel_app.command(name="set-default")
def kernel_set_default(
    prefix: str = typer.Argument(..., help="Kernel ID prefix to set as default"),
    kernels_dir: Optional[Path] = typer.Option(None, "--kernels-dir", help="Kernels directory"),
) -> None:
    """Set a kernel as the default for VM creation."""
    kernels_dir = kernels_dir if kernels_dir is not None else get_kernels_dir()

    try:
        set_default_kernel(kernels_dir, prefix)
    except KernelError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1) from exc
    print_success(f"Default kernel set to: {prefix}")


@kernel_app.command(name="rm")
def kernel_rm(
    prefixes: Optional[list[str]] = typer.Argument(None, help="Kernel ID prefixes to remove"),
    kernels_dir: Optional[Path] = typer.Option(None, "--kernels-dir", help="Kernels directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Remove even if referenced by VMs"),
) -> None:
    """Remove cached kernels by ID prefix."""
    kernels_dir = kernels_dir if kernels_dir is not None else get_kernels_dir()
    effective_ids: list[str] = list(prefixes) if prefixes else []
    if not effective_ids:
        print_error("Provide at least one kernel ID prefix")
        raise typer.Exit(code=1)

    exit_code = 0

    for prefix in effective_ids:
        try:
            remove_kernel(prefix, kernels_dir, force=force)
            print_success(f"Removed: {prefix}")
        except KernelError as exc:
            print_error(str(exc))
            exit_code = 1

    raise typer.Exit(code=exit_code)
