"""Kernel management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING

import typer
from rich.console import Console

from mvmctl.api import KernelFetchInput, KernelInput, KernelOperation
from mvmctl.models.result import OperationResult, ProgressEvent
from mvmctl.utils._io import (
    print_error,
    print_info,
    print_success,
    print_table,
)
from mvmctl.utils.cli import CliUtils, handle_errors
from mvmctl.utils.common import CommonUtils
from mvmctl.utils.crypto import HashGenerator

if TYPE_CHECKING:
    from mvmctl.models import KernelItem

kernel_app = typer.Typer(
    help="Kernel management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@kernel_app.callback()
def kernel_callback(ctx: typer.Context) -> None:
    pass


@kernel_app.command(name="ls")
@handle_errors
def kernel_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List all kernels."""
    kernels: list[KernelItem] = KernelOperation.list_all()

    if json_output:
        data = [
            {
                "id": HashGenerator.shorten(k.id),
                "name": k.name,
                "version": k.version,
                "arch": k.arch,
                "type": k.type,
                "is_default": k.is_default,
                "created_at": k.created_at,
            }
            for k in kernels
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    if not kernels:
        print_info(
            "No kernels found. Use 'mvm kernel fetch --type firecracker' to download one."
        )
        return

    rows: list[list[str]] = []
    for k in kernels:
        is_default = k.is_default
        name_col = CommonUtils._get_combined_marker(
            is_default, not k.is_present
        )
        rows.append(
            [
                HashGenerator.shorten(k.id),
                f"{name_col}{k.base_name}",
                k.version,
                k.arch,
                k.type,
                CommonUtils.human_readable_datetime(k.created_at),
            ]
        )

    print_table(
        columns=["ID", "Name", "Version", "Arch", "Type", "Added"],
        rows=rows,
    )


@kernel_app.command(name="fetch")
@handle_errors
def kernel_fetch(
    kernel_type: str = typer.Option(
        ..., "--type", help="Kernel type: firecracker or official"
    ),
    version: str | None = typer.Option(
        None, "--version", help="Kernel version"
    ),
    arch: str | None = typer.Option(
        None, "--arch", help="Architecture (x86_64, arm64)"
    ),
    set_default: bool = typer.Option(
        False, "--set-default", help="Set as default after fetch"
    ),
    jobs: int | None = typer.Option(
        None, "--jobs", help="Parallel build jobs (official only)"
    ),
    keep_build_dir: bool = typer.Option(
        False, "--keep-build-dir", help="Keep build directory (official only)"
    ),
    clean_build: bool = typer.Option(
        False, "--clean-build", help="Skip cache (official only)"
    ),
    kernel_config: Path | None = typer.Option(
        None,
        "--config",
        help="Custom kernel config file to apply as a fragment",
    ),
) -> None:
    """Fetch or build a kernel."""
    inputs = KernelFetchInput(
        kernel_type=kernel_type,
        version=version,
        arch=arch,
        jobs=jobs,
        keep_build_dir=keep_build_dir,
        clean_build=clean_build,
        kernel_config=kernel_config,
        set_default=set_default,
    )
    console = Console()
    with console.status("", spinner="dots") as status:

        def _on_progress(event: ProgressEvent) -> None:
            if event.message:
                status.update(event.message)

        result = KernelOperation.fetch(inputs, on_progress=_on_progress)
    if isinstance(result, OperationResult):
        if result.is_error:
            print_error(result.message)
            raise typer.Exit(code=1)
        if result.status == "skipped":
            print_info(result.message)
            if result.item:
                print_info(f"  ID: {HashGenerator.shorten(result.item.id)}")
            raise typer.Exit(code=0)
        if result.item:
            print_success(
                f"Kernel '{result.item.name}' fetched successfully "
                f"(ID: {HashGenerator.shorten(result.item.id)})"
            )
    else:
        # Fallback for unexpected non-OperationResult returns
        print_success("Kernel fetch completed")


@kernel_app.command(
    name="set-default",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def kernel_set_default(
    ctx: typer.Context,
    kernel_id: str = typer.Argument(None, help="Kernel ID prefix or name"),
) -> None:
    """Set a kernel as the default."""
    kernel_id = CliUtils.check_name_arg(ctx, kernel_id)
    inputs = KernelInput(id=[kernel_id])
    result = KernelOperation.set_default(inputs)

    if result.is_error:
        print_error(result.message)
        raise typer.Exit(code=1)

    print_success(result.message or f"Default kernel set to {kernel_id}")


@kernel_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def kernel_rm(
    ctx: typer.Context,
    identifiers: list[str] = typer.Argument(
        None, help="Kernel ID prefixes or names to remove"
    ),
    force: bool = typer.Option(
        False, "--force", help="Remove even if referenced by VMs"
    ),
) -> None:
    """Remove one or more kernels."""
    effective_ids: list[str] = list(identifiers) if identifiers else []
    if not effective_ids:
        print_error("Provide at least one kernel ID or name")
        raise typer.Exit(code=1)

    inputs = KernelInput(id=effective_ids, force=force)
    batch_result = KernelOperation.remove(inputs)

    for item_result in batch_result.items:
        if item_result.is_ok:
            print_success(item_result.message or "Kernel removed")
        else:
            print_error(item_result.message or "Failed to remove kernel")

    if batch_result.has_any_error:
        raise typer.Exit(code=1)


__all__ = ["kernel_app"]
