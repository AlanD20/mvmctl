"""Binary management commands."""

from __future__ import annotations

import json
from typing import TYPE_CHECKING, Optional

import typer

from mvmctl.api.binary_operations import BinaryOperation
from mvmctl.api.inputs._binary_fetch_input import BinaryFetchInput
from mvmctl.api.inputs._binary_input import BinaryInput
from mvmctl.exceptions import AssetNotFoundError, BinaryError
from mvmctl.utils.console import (
    print_error,
    print_info,
    print_success,
    print_table,
)
from mvmctl.utils.full_hash import HashGenerator

if TYPE_CHECKING:
    pass

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
                "path": b.path,
                "is_default": b.is_default,
            }
            for b in local
        ]
        print(json.dumps(data, indent=2))
        raise typer.Exit(code=0)

    rows: list[list[str]] = []

    if remote:
        try:
            remote_versions = BinaryOperation.list_remote(limit=limit)
        except BinaryError as exc:
            print_error(str(exc))
            raise typer.Exit(code=1)

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


@bin_app.command(name="fetch")
def bin_fetch(
    version: str = typer.Argument(
        ..., help="Version to download (e.g. 1.15.0)"
    ),
    set_default: bool = typer.Option(
        False,
        "--set-default",
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

    # Check if version already exists
    already_exists = BinaryOperation.get(
        BinaryInput(names=["firecracker", "jailer"], version=normalized)
    )

    download_override = force

    if already_exists and not force:
        if not typer.confirm(
            f"Binary v{normalized} already exists. Re-download?",
            default=False,
        ):
            print_info("Aborted")
            raise typer.Exit(code=0)
        download_override = True

    try:
        inputs = BinaryFetchInput(
            version=version,
            set_as_default=set_default,
            download_override=download_override,
        )
        result = BinaryOperation.fetch(inputs)
        binaries = result.result
    except BinaryError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    for binary in binaries:
        short_id = HashGenerator.shorten(binary.id)
        print_success(
            f"Downloaded {binary.name} v{binary.version}: {binary.path}"
        )
        print_info(f"  ID: {short_id}")

    if set_default:
        print_success(f"Default binary set to v{version}")

    raise typer.Exit(code=0)


@bin_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
def bin_rm(
    identifiers: Optional[list[str]] = typer.Argument(
        None, help="Binary ID(s) to remove (6-char prefix accepted)"
    ),
    version: str = typer.Option(
        None,
        "--version",
        help="Remove both firecracker and jailer for this version",
    ),
) -> None:
    """Remove one or more binaries. Use --version to remove by version pair."""
    if version is not None:
        # Remove by version
        try:
            BinaryOperation.remove_by_version(version)
            print_success(f"Removed binaries for v{version}")
        except AssetNotFoundError as exc:
            print_error(str(exc))
            raise typer.Exit(code=1)

    effective_ids: list[str] = list(identifiers) if identifiers else []
    if not effective_ids:
        print_error("Provide at least one binary ID to remove or use --version")
        raise typer.Exit(code=1)

    exit_code = 0
    try:
        inputs = BinaryInput(id=effective_ids)
        BinaryOperation.remove(inputs)
        print_success(f"Removed binary(s): {' '.join(effective_ids)}")
    except (BinaryError, AssetNotFoundError) as exc:
        print_error(str(exc))
        exit_code = 1

    raise typer.Exit(code=exit_code)


@bin_app.command(name="default")
def bin_default(
    identifier: str = typer.Argument(
        ..., help="Binary ID to set as default (6-char prefix accepted)"
    ),
) -> None:
    """Set a binary as the active default."""
    try:
        inputs = BinaryInput(id=[identifier])
        BinaryOperation.set_default(inputs)
    except (BinaryError, AssetNotFoundError) as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    print_success(f"Default binary set to: {identifier}")
    raise typer.Exit(code=0)


__all__ = ["bin_app"]
