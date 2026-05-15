"""Kernel management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING, Any

import typer
from rich.console import Console

from mvmctl.api import KernelInput as _KernelInput
from mvmctl.api import KernelOperation as _KernelOperation
from mvmctl.api import KernelPullInput as _KernelPullInput

if TYPE_CHECKING:
    from mvmctl.api.inputs._kernel_input import KernelInput
    from mvmctl.api.inputs._kernel_pull_input import KernelPullInput
    from mvmctl.api.kernel_operations import KernelOperation
    from mvmctl.core._shared._http_dir_version_resolver import VersionInfo
else:
    KernelOperation = _KernelOperation
    KernelPullInput = _KernelPullInput
    KernelInput = _KernelInput
from mvmctl.cli._completion import _complete_kernel_ids
from mvmctl.models.result import OperationResult, ProgressEvent
from mvmctl.utils._io import (
    print_error,
    print_info,
    print_inspect_header,
    print_key_value,
    print_section_header,
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
    remote: bool = typer.Option(
        False, "--remote", "-r", help="Show available remote kernel versions"
    ),
    no_cache: bool = typer.Option(
        False,
        "--no-cache",
        help="Skip cached version listing and fetch live from upstream",
    ),
) -> None:
    """List cached kernels (or available remote kernels with --remote)."""
    if remote:
        with Console().status("Fetching remote kernel versions"):
            versions = KernelOperation.list_remote(no_cache=no_cache)
        _list_remote_kernels(versions, json_output=json_output)
    else:
        kernels: list[KernelItem] = KernelOperation.list_all()

        if json_output:
            data = [KernelOperation._kernel_to_dict(k) for k in kernels]
            typer.echo(json.dumps(data, indent=2, default=str))
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


def _list_remote_kernels(
    versions: list[VersionInfo], *, json_output: bool
) -> None:
    """Render remote available kernels grouped by type."""
    if json_output:
        data: list[dict[str, Any]] = [
            {
                "version": v.version,
                "type": v.type,
                "display_name": v.display_name,
                "download_url": v.download_url,
                "sha256_url": v.sha256_url,
                "format": v.format,
            }
            for v in versions
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    # Group by type
    groups: dict[str, list[VersionInfo]] = {}
    for v in versions:
        groups.setdefault(v.type, []).append(v)

    sorted_types = sorted(groups.keys())

    rows: list[list[str]] = []
    for type_key in sorted_types:
        version_list = groups[type_key]
        if not version_list:
            continue

        parts = type_key.split("-", maxsplit=1)
        type_display = (
            f"{parts[0].title()} {parts[1]}"
            if len(parts) > 1
            else type_key.title()
        )
        suffix = " (build required)" if type_key.startswith("official") else ""
        rows.append([type_key, f"{type_display}{suffix}"])

        for j, v in enumerate(version_list):
            is_last = j == len(version_list) - 1
            prefix = "  └─ " if is_last else "  ├─ "
            display = v.display_name or v.version
            rows.append([f"{prefix}{v.version}", display])

    if not rows:
        print_info("No remote kernels available.")
        return

    print_table(
        columns=["Type / Version", "Description"],
        rows=rows,
    )


@kernel_app.command(name="inspect")
@handle_errors
def kernel_inspect(
    prefix: str = typer.Argument(
        ...,
        help="Kernel ID prefix to inspect",
        autocompletion=_complete_kernel_ids,
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    tree: bool = typer.Option(False, "--tree", help="Output in tree format"),
) -> None:
    """Show detailed information about a kernel."""
    info = KernelOperation.inspect(
        KernelInput(id=[prefix]), is_json=json_output
    )

    if isinstance(info, dict):
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    if tree:
        _print_kernel_details_tree(info)
    else:
        _print_kernel_details(info)


def _print_kernel_details(info: KernelItem) -> None:
    """Print kernel details in human-readable format."""
    missing_marker = " (missing)" if not info.is_present else ""
    print_inspect_header(f"Kernel: {info.name}{missing_marker}")

    print_section_header("BASIC INFO")
    print_key_value("ID", info.id)
    print_key_value("Name", info.name)
    print_key_value("Base Name", info.base_name)
    print_key_value("Version", info.version)
    print_key_value("Arch", info.arch)
    print_key_value("Type", info.type)
    print_key_value("Default", "Yes" if info.is_default else "No")
    print_key_value("Present", "Yes" if info.is_present else "No")
    print_key_value("Path", info.path)
    print_key_value(
        "Created", CommonUtils.human_readable_datetime(info.created_at)
    )
    print_key_value(
        "Updated", CommonUtils.human_readable_datetime(info.updated_at)
    )


def _print_kernel_details_tree(info: KernelItem) -> None:
    """Print kernel details in tree format."""
    missing_marker = " (missing)" if not info.is_present else ""
    print(f"{info.name}{missing_marker}")

    tree_lines = [
        f"├── ID:          {info.id}",
        f"├── Name:        {info.name}",
        f"├── Base Name:   {info.base_name}",
        f"├── Version:     {info.version}",
        f"├── Arch:        {info.arch}",
        f"├── Type:        {info.type}",
        f"├── Default:     {'Yes' if info.is_default else 'No'}",
        f"├── Present:     {'Yes' if info.is_present else 'No'}",
        f"├── Path:        {info.path}",
        f"├── Created:     {CommonUtils.human_readable_datetime(info.created_at)}",
        f"└── Updated:     {CommonUtils.human_readable_datetime(info.updated_at)}",
    ]
    for line in tree_lines:
        print(line)


@kernel_app.command(name="pull")
@handle_errors
def kernel_pull(
    kernel_selector: str | None = typer.Argument(
        None,
        help="Shorthand: type:version (e.g. official:6.19.9). "
        "Use '--type' and '--version' options for explicit control.",
    ),
    kernel_type: str | None = typer.Option(
        None, "--type", help="Kernel type: firecracker or official"
    ),
    version: str | None = typer.Option(
        None, "--version", help="Kernel version"
    ),
    arch: str | None = typer.Option(
        None, "--arch", help="Architecture (x86_64, arm64)"
    ),
    set_default: bool = typer.Option(
        False, "--default", help="Set as default after fetch"
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
    """Pull or build a kernel.

    Examples:
        mvm kernel pull official:6.19.9
        mvm kernel pull official:6.19.9 --default
        mvm kernel pull --type official --version 6.19.9
        mvm kernel pull firecracker --arch arm64
    """
    # Parse ``type:version`` shorthand syntax (e.g. ``official:6.19.9``)
    effective_type = kernel_type
    effective_version = version
    if kernel_type is None and kernel_selector is not None:
        if ":" in kernel_selector:
            parts = kernel_selector.rsplit(":", maxsplit=1)
            effective_type = parts[0]
            effective_version = parts[1]
        else:
            effective_type = kernel_selector

    if effective_type is None:
        print_error(
            "Kernel type is required. "
            "Use 'mvm kernel pull --type official' or "
            "'mvm kernel pull official:6.19.9'"
        )
        raise typer.Exit(code=1)

    inputs = KernelPullInput(
        kernel_type=effective_type,
        version=effective_version,
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

        result = KernelOperation.pull(inputs, on_progress=_on_progress)
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
                f"Kernel '{result.item.name}' pulled successfully "
                f"(ID: {HashGenerator.shorten(result.item.id)})"
            )
    else:
        # Fallback for unexpected non-OperationResult returns
        print_success("Kernel pull completed")


@kernel_app.command(
    name="default",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def kernel_set_default(
    ctx: typer.Context,
    kernel_id: str = typer.Argument(
        None,
        help="Kernel ID prefix or name",
        autocompletion=_complete_kernel_ids,
    ),
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
        None,
        help="Kernel ID prefixes or names to remove",
        autocompletion=_complete_kernel_ids,
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Remove even if referenced by VMs"
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
