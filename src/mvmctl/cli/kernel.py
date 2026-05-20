"""Kernel management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING, Any, cast

import typer
from rich.console import Console

from mvmctl.api import KernelImportInput as _KernelImportInput
from mvmctl.api import KernelInput as _KernelInput
from mvmctl.api import KernelOperation as _KernelOperation
from mvmctl.api import KernelPullInput as _KernelPullInput

if TYPE_CHECKING:
    from mvmctl.api.inputs._kernel_import_input import KernelImportInput
    from mvmctl.api.inputs._kernel_input import KernelInput
    from mvmctl.api.inputs._kernel_pull_input import KernelPullInput
    from mvmctl.api.kernel_operations import KernelOperation
else:
    KernelOperation = _KernelOperation
    KernelPullInput = _KernelPullInput
    KernelInput = _KernelInput
    KernelImportInput = _KernelImportInput
from mvmctl.cli._completion import _complete_kernel_ids
from mvmctl.models import KernelItem, VersionInfo
from mvmctl.models.result import OperationResult, ProgressEvent
from mvmctl.utils.cli import handle_errors, mvm_cli

kernel_app = typer.Typer(
    help="Kernel management",
    no_args_is_help=True,
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
            versions = cast(
                list[VersionInfo],
                KernelOperation.list_all(remote=True, no_cache=no_cache),
            )
        _list_remote_kernels(versions, json_output=json_output)
    else:
        kernels = cast(
            list[KernelItem],
            KernelOperation.list_all(),
        )

        if json_output:
            data = [KernelOperation._kernel_to_dict(k) for k in kernels]
            typer.echo(json.dumps(data, indent=2, default=str))
            return

        rows: list[list[str]] = []
        for k in kernels:
            rows.append(
                [
                    mvm_cli.format_marker(k.is_default),
                    mvm_cli.format_id(k.id),
                    mvm_cli.format_name(k.base_name, not k.is_present),
                    k.version,
                    k.arch,
                    k.type,
                    mvm_cli.format_timestamp(k.created_at),
                ]
            )

        mvm_cli.table(
            columns=["", "ID", "Name", "Version", "Arch", "Type", "Added"],
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
        mvm_cli.info("No remote kernels available.")
        return

    mvm_cli.table(
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
) -> None:
    """Show detailed information about a kernel."""
    info = KernelOperation.inspect(KernelInput(id=[prefix]))

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    name = info.get("kernel", {}).get("name", prefix)
    mvm_cli.print_dict_tree(info, title=f"Kernel: {name}")


@kernel_app.command(name="pull")
@handle_errors
def kernel_pull(
    kernel_selector: str | None = typer.Argument(
        None,
        help="Shorthand: type:version (e.g. official:6.19.9). "
        "Use '--type' and '--version' options for explicit control.",
        autocompletion=_complete_kernel_ids,
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
        False, "--default", "-d", help="Set as default after fetch"
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
    features: str | None = typer.Option(
        None,
        "--features",
        help="Comma-separated kernel features (kvm, nftables, tuntap)",
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
        mvm_cli.error(
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
        features=features or "",
    )
    console = Console()
    with console.status("", spinner="dots") as status:

        def _on_progress(event: ProgressEvent) -> None:
            if event.message:
                status.update(event.message)

        result = KernelOperation.pull(inputs, on_progress=_on_progress)
    if isinstance(result, OperationResult):
        if result.is_error:
            mvm_cli.error(result.message)
            raise typer.Exit(code=1)
        if result.status == "skipped":
            mvm_cli.info(result.message)
            if result.item:
                mvm_cli.info(f"  ID: {mvm_cli.format_id(result.item.id)}")
            raise typer.Exit(code=0)
        if result.item:
            mvm_cli.success(
                f"Pulled: {result.item.name} "
                f"(ID: {mvm_cli.format_id(result.item.id)})"
            )
            resolved_features = (result.metadata or {}).get("features", [])
            if resolved_features:
                mvm_cli.info(
                    "Enabled features: " + ", ".join(resolved_features)
                )
    else:
        # Fallback for unexpected non-OperationResult returns
        mvm_cli.success("Pull completed")


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
    kernel_id = mvm_cli.check_name_arg(ctx, kernel_id)
    inputs = KernelInput(id=[kernel_id])
    result = KernelOperation.set_default(inputs)

    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)

    mvm_cli.success(result.message or f"Default kernel set to: {kernel_id}")


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
        mvm_cli.error("Provide at least one kernel ID or name")
        raise typer.Exit(code=1)

    inputs = KernelInput(id=effective_ids, force=force)
    batch_result = KernelOperation.remove(inputs)

    for item_result in batch_result.items:
        if item_result.is_ok:
            mvm_cli.success(item_result.message or "Removed")
        else:
            mvm_cli.error(item_result.message or "Remove failed")

    if batch_result.has_any_error:
        raise typer.Exit(code=1)


@kernel_app.command(name="import")
@handle_errors
def kernel_import(
    name: str = typer.Argument(
        ...,
        help="User-assigned name for this kernel entry",
    ),
    path: Path = typer.Argument(
        ...,
        help="Path to vmlinux file",
        exists=True,
        readable=True,
    ),
    version: str | None = typer.Option(
        None,
        "--version",
        help="Override auto-detected kernel version",
    ),
    arch: str | None = typer.Option(
        None,
        "--arch",
        help="Kernel architecture (default: auto-detected from filename or platform)",
    ),
    set_default: bool = typer.Option(
        False, "--default", "-d", help="Set as default after import"
    ),
) -> None:
    """Register a vmlinux file as a kernel in the database.

    Examples:

        mvm kernel import my-kernel ./vmlinux-6.1-x86_64

        mvm kernel import my-kernel ./vmlinux-custom --version 6.1 --arch x86_64 --default
    """
    if not path.exists():
        mvm_cli.error(f"Source file not found: {path}")
        raise typer.Exit(code=1)

    inputs = KernelImportInput(
        name=name,
        path=path,
        version=version,
        arch=arch,
        set_default=set_default,
    )
    result = KernelOperation.import_(inputs)

    if result.is_error:
        mvm_cli.error(result.message or f"Import failed: {name}")
        raise typer.Exit(code=1)

    assert result.item is not None
    short_id = mvm_cli.format_id(result.item.id)
    mvm_cli.success(f"Imported: {result.item.name}")
    mvm_cli.info(f"  ID:   {short_id}")

    if set_default:
        mvm_cli.success(f"Default kernel set to: {name}")

    raise typer.Exit(code=0)


__all__ = ["kernel_app"]
