"""Binary management commands."""

from __future__ import annotations

import json
from typing import TYPE_CHECKING

import typer

from mvmctl.api import BinaryInput as _BinaryInput
from mvmctl.api import BinaryOperation as _BinaryOperation
from mvmctl.api import BinaryPullInput as _BinaryPullInput

if TYPE_CHECKING:
    from mvmctl.api.binary_operations import BinaryOperation
    from mvmctl.api.inputs._binary_input import BinaryInput
    from mvmctl.api.inputs._binary_pull_input import BinaryPullInput
else:
    BinaryOperation = _BinaryOperation
    BinaryPullInput = _BinaryPullInput
    BinaryInput = _BinaryInput
from mvmctl.cli._completion import _complete_binary_versions
from mvmctl.models.result import OperationResult
from mvmctl.utils._io import (
    print_error,
    print_info,
    print_success,
    print_table,
    print_warning,
)
from mvmctl.utils.cli import handle_errors
from mvmctl.utils.crypto import HashGenerator

if TYPE_CHECKING:
    from mvmctl.models import BinaryItem

bin_app = typer.Typer(
    help="Binary management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@bin_app.callback()
def bin_callback(ctx: typer.Context) -> None:
    pass


@bin_app.command(name="ls")
@handle_errors
def bin_ls(
    remote: bool = typer.Option(
        False, "--remote", "-r", help="Also show remote versions"
    ),
    limit: int = typer.Option(
        None, "--limit", help="Max remote versions to show"
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List local (and optionally remote) Firecracker versions."""
    local = BinaryOperation.list_local()
    local_versions = {b.version for b in local if b.name == "firecracker"}

    if json_output:
        data = [
            {
                "id": b.id,
                "name": b.name,
                "version": b.version,
                "full_version": b.full_version,
                "ci_version": b.ci_version,
                "path": b.path,
                "is_default": b.is_default,
                "is_present": b.is_present,
                "created_at": b.created_at,
                "updated_at": b.updated_at,
            }
            for b in local
        ]
        print(json.dumps(data, indent=2))
        raise typer.Exit(code=0)

    rows: list[list[str]] = []

    if remote:
        remote_versions = BinaryOperation.list_remote(limit=limit)

        for ver in remote_versions:
            cached = "✓" if ver in local_versions else " "
            rows.append([cached, ver])

        print_table(columns=["Downloaded", "Version"], rows=rows)
        raise typer.Exit(code=0)

    for b in local:
        short_id = HashGenerator.shorten(b.id)
        marker = "* " if b.is_default else "  "
        rows.append(
            [
                marker + short_id,
                b.name,
                b.version,
            ]
        )

    print_table(
        columns=["ID", "Name", "Version"],
        rows=rows,
    )
    raise typer.Exit(code=0)


@bin_app.command(name="pull")
@handle_errors
def bin_pull(
    version: str = typer.Argument(
        ..., help="Version to download (e.g. 1.15.0)"
    ),
    set_default: bool = typer.Option(
        False,
        "--default",
        "-d",
        help="Set as default after download",
    ),
    force: bool = typer.Option(
        False,
        "--force",
        "-f",
        help="Re-download even if version already exists",
    ),
) -> None:
    """Download a specific Firecracker version."""
    normalized = version.removeprefix("v")

    # Check if version already exists (may not exist yet — that's OK)
    from mvmctl.exceptions import BinaryNotFoundError

    try:
        already_exists = BinaryOperation.get(
            BinaryInput(names=["firecracker", "jailer"], version=normalized)
        )
    except BinaryNotFoundError:
        already_exists = []

    download_override = force

    if already_exists and not force:
        print_warning(f"Binary v{normalized} already exists.")
        if not typer.confirm("Re-download?", default=False):
            print_info("Aborted")
            raise typer.Exit(code=0)
        download_override = True

    inputs = BinaryPullInput(
        version=version,
        set_as_default=set_default,
        download_override=download_override,
    )
    result: OperationResult[list[BinaryItem]] = BinaryOperation.pull(inputs)  # type: ignore[assignment]

    if result.is_error:
        print_error(result.message)
        raise typer.Exit(code=1)

    if result.status == "skipped":
        print_info(result.message)
        binaries = result.item or []
        for binary in binaries:
            short_id = HashGenerator.shorten(binary.id)
            print_info(f"  {binary.name} v{binary.version}: {short_id}")
        raise typer.Exit(code=0)

    binaries = result.item or []
    for binary in binaries:
        short_id = HashGenerator.shorten(binary.id)
        print_success(
            f"Downloaded {binary.name} v{binary.version}: {binary.resolved_path}"
        )
        print_info(f"  ID: {short_id}")

    if set_default:
        print_success(f"Default binary set to v{version}")

    raise typer.Exit(code=0)


@bin_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def bin_rm(
    identifiers: list[str] | None = typer.Argument(
        None,
        help="Binary ID(s) to remove (6-char prefix accepted)",
        autocompletion=_complete_binary_versions,
    ),
    version: str = typer.Option(
        None,
        "--version",
        help="Remove both firecracker and jailer for this version",
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Remove even if referenced by VMs"
    ),
) -> None:
    """Remove one or more binaries. Use --version to remove by version pair."""
    if version is not None:
        result = BinaryOperation.remove_by_version(version, force=force)
        if result.is_error:
            print_error(result.message)
            raise typer.Exit(code=1)
        print_success(f"Removed binaries for v{version}")
        raise typer.Exit(code=0)

    effective_ids: list[str] = list(identifiers) if identifiers else []
    if not effective_ids:
        print_error("Provide at least one binary ID to remove or use --version")
        raise typer.Exit(code=1)

    inputs = BinaryInput(id=effective_ids)
    batch_result = BinaryOperation.remove(inputs, force=force)

    for item_result in batch_result.items:
        if item_result.is_ok:
            print_success(item_result.message or "Removed binary")
        else:
            print_error(item_result.message or "Failed to remove binary")

    if batch_result.has_any_error:
        raise typer.Exit(code=1)


@bin_app.command(name="default")
@handle_errors
def bin_default(
    identifier: str = typer.Argument(
        ...,
        help="Binary ID to set as default (6-char prefix accepted)",
        autocompletion=_complete_binary_versions,
    ),
) -> None:
    """Set a binary as the active default."""
    inputs = BinaryInput(id=[identifier])
    result = BinaryOperation.set_default(inputs)

    if result.is_error:
        print_error(result.message)
        raise typer.Exit(code=1)

    print_success(result.message or f"Default binary set to: {identifier}")
    raise typer.Exit(code=0)


__all__ = ["bin_app"]
