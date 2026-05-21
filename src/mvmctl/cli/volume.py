"""Volume management commands."""

from __future__ import annotations

import json
from typing import TYPE_CHECKING, Any

import typer

from mvmctl.api import ConfigOperation as _ConfigOperation
from mvmctl.api import VolumeCreateInput as _VolumeCreateInput
from mvmctl.api import VolumeInput as _VolumeInput
from mvmctl.api import VolumeOperation as _VolumeOperation
from mvmctl.cli._completion import _complete_volume_names
from mvmctl.utils.cli import handle_errors, mvm_cli

if TYPE_CHECKING:
    from mvmctl.api.config_operations import ConfigOperation
    from mvmctl.api.inputs._volume_create_input import VolumeCreateInput
    from mvmctl.api.inputs._volume_input import VolumeInput
    from mvmctl.api.volume_operations import VolumeOperation
else:
    ConfigOperation = _ConfigOperation
    VolumeOperation = _VolumeOperation
    VolumeInput = _VolumeInput
    VolumeCreateInput = _VolumeCreateInput
from mvmctl.cli._common import (
    ListingColumn,
    render_listing,
    resolve_listing_style,
)

volume_app = typer.Typer(
    help="Volume management",
    no_args_is_help=True,
    add_completion=False,
)


@volume_app.callback()
def volume_callback(ctx: typer.Context) -> None:
    pass


@volume_app.command(name="create")
@handle_errors
def volume_create(
    name: str = typer.Argument(..., help="Volume name"),
    size: str = typer.Argument(..., help="Volume size (e.g., 1G, 512M)"),
    format: str | None = typer.Option(
        None, "--format", help="Disk format: raw or qcow2 (default: raw)"
    ),
    read_only: bool | None = typer.Option(
        None,
        "--read-only",
        "--readonly",
        help="Mount volume as read-only (default: writable)",
        is_flag=True,
    ),
) -> None:
    """Create a new persistent volume."""
    result = VolumeOperation.create(
        VolumeCreateInput(
            name=name, size=size, format=format, read_only=read_only
        )
    )
    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    mvm_cli.success(result.message)
    if result.item:
        mvm_cli.key_value("ID", mvm_cli.format_id(result.item.id))
        mvm_cli.key_value("Mode", "ro" if result.item.is_read_only else "rw")
        mvm_cli.key_value("Format", result.item.format)
        mvm_cli.key_value(
            "Size",
            mvm_cli.format_size(result.item.size_bytes),
        )


@volume_app.command(name="rm")
@handle_errors
def volume_rm(
    identifiers: list[str] = typer.Argument(
        ...,
        help="Volume names or ID prefixes to remove",
        autocompletion=_complete_volume_names,
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Remove even if attached to VMs"
    ),
) -> None:
    """Remove one or more volumes."""
    result = VolumeOperation.remove(
        VolumeInput(identifiers=identifiers), force=force
    )
    for r in result.items:
        item_name = r.item.name if r.item else "unknown"
        if r.is_ok:
            mvm_cli.success(f"Removed: {item_name}")
        else:
            mvm_cli.error(r.message or f"Remove failed: {item_name}")
    if result.has_any_error:
        raise typer.Exit(code=1)


_VOLUME_COLUMNS = [
    ListingColumn("ID", lambda v: mvm_cli.format_id(v.id)),
    ListingColumn("Name", lambda v: v.name),
    ListingColumn("Size", lambda v: mvm_cli.format_size(v.size_bytes)),
    ListingColumn("Status", lambda v: v.status),
    ListingColumn("Format", lambda v: v.format, long_only=True),
    ListingColumn("Attached To", lambda v: v.vm_id or "-", long_only=True),
    ListingColumn("Created", lambda v: mvm_cli.format_timestamp(v.created_at)),
]


@volume_app.command(name="ls")
@handle_errors
def volume_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    long_output: bool = typer.Option(
        False, "--long", help="Show full listing with all columns"
    ),
) -> None:
    """List all volumes."""
    volumes = VolumeOperation.list_all()

    if json_output:
        data: list[dict[str, Any]] = []
        for vol in volumes:
            data.append(
                {
                    "id": vol.id,
                    "name": vol.name,
                    "size": vol.size_bytes,
                    "size_bytes": vol.size_bytes,
                    "format": vol.format,
                    "is_read_only": vol.is_read_only,
                    "status": vol.status,
                    "vm_id": vol.vm_id,
                    "created_at": vol.created_at,
                }
            )
        typer.echo(json.dumps(data, indent=2))
        return

    style = resolve_listing_style(long_output)

    render_listing(volumes, _VOLUME_COLUMNS, style)


@volume_app.command(name="inspect")
@handle_errors
def volume_inspect(
    identifier: str = typer.Argument(
        ...,
        help="Volume name or ID prefix",
        autocompletion=_complete_volume_names,
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a volume."""
    info = VolumeOperation.inspect(VolumeInput(identifiers=[identifier]))

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    mvm_cli.print_dict_tree(info, title=f"Volume: {info['volume']['name']}")


@volume_app.command(name="resize")
@handle_errors
def volume_resize(
    identifier: str = typer.Argument(
        ...,
        help="Volume name or ID prefix",
        autocompletion=_complete_volume_names,
    ),
    size: str = typer.Argument(..., help="New size (e.g., 1G, 512M)"),
) -> None:
    """Resize a volume."""
    result = VolumeOperation.resize(
        VolumeCreateInput(name=identifier, size=size)
    )
    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    mvm_cli.success(result.message)
