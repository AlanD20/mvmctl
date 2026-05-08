"""Volume management commands."""

from __future__ import annotations

import json
from typing import TYPE_CHECKING, Any

import typer

from mvmctl.api import VolumeCreateInput as _VolumeCreateInput
from mvmctl.api import VolumeInput as _VolumeInput
from mvmctl.api import VolumeOperation as _VolumeOperation
from mvmctl.utils._io import (
    print_error,
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
)
from mvmctl.utils.cli import handle_errors
from mvmctl.utils.common import CommonUtils
from mvmctl.utils.crypto import HashGenerator

if TYPE_CHECKING:
    from mvmctl.api.inputs._volume_create_input import VolumeCreateInput
    from mvmctl.api.inputs._volume_input import VolumeInput
    from mvmctl.api.volume_operations import VolumeOperation
else:
    VolumeOperation = _VolumeOperation
    VolumeInput = _VolumeInput
    VolumeCreateInput = _VolumeCreateInput

volume_app = typer.Typer(
    help="Volume management",
    no_args_is_help=True,
    rich_markup_mode=None,
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
) -> None:
    """Create a new persistent volume."""
    result = VolumeOperation.create(
        VolumeCreateInput(name=name, size=size, format=format)
    )
    if result.is_error:
        print_error(result.message)
        raise typer.Exit(code=1)
    print_success(result.message)
    if result.item:
        print_key_value("ID", HashGenerator.shorten(result.item.id))
        print_key_value("Format", result.item.format)
        print_key_value(
            "Size",
            CommonUtils.format_bytes_human_readable(result.item.size_bytes),
        )


@volume_app.command(name="rm")
@handle_errors
def volume_rm(
    names: list[str] = typer.Argument(
        ..., help="Volume names or ID prefixes to remove"
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Remove even if attached to VMs"
    ),
) -> None:
    """Remove one or more volumes."""
    result = VolumeOperation.remove(
        VolumeInput(id=[], name=list(names)), force=force
    )
    for r in result.items:
        item_name = r.item.name if r.item else "unknown"
        if r.is_ok:
            print_success(f"Removed volume: {item_name}")
        else:
            print_error(r.message or f"Failed to remove volume: {item_name}")
    if result.has_any_error:
        raise typer.Exit(code=1)


@volume_app.command(name="ls")
@handle_errors
def volume_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List all volumes."""
    volumes = VolumeOperation.list_()

    if json_output:
        data: list[dict[str, Any]] = []
        for vol in volumes:
            data.append(
                {
                    "id": vol.id,
                    "name": vol.name,
                    "size_bytes": vol.size_bytes,
                    "format": vol.format,
                    "status": vol.status,
                    "vm_id": vol.vm_id,
                    "created_at": vol.created_at,
                }
            )
        typer.echo(json.dumps(data, indent=2))
        return

    rows: list[list[str]] = []
    for vol in volumes:
        rows.append(
            [
                HashGenerator.shorten(vol.id),
                vol.name,
                vol.format,
                CommonUtils.format_bytes_human_readable(vol.size_bytes),
                vol.status,
                vol.vm_id or "-",
                CommonUtils.human_readable_datetime(vol.created_at),
            ]
        )

    print_table(
        columns=["ID", "NAME", "FORMAT", "SIZE", "STATUS", "VM", "CREATED"],
        rows=rows,
    )


@volume_app.command(name="inspect")
@handle_errors
def volume_inspect(
    name: str = typer.Argument(..., help="Volume name or ID prefix"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a volume."""
    info = VolumeOperation.inspect(VolumeInput(id=[], name=[name]))

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    print_inspect_header(f"Volume: {info['name']}")

    print_section_header("BASIC INFO")
    print_key_value("ID", info["id"])
    print_key_value("Name", info["name"])
    print_key_value("Status", info["status"])
    print_key_value("Format", info["format"])
    print_key_value(
        "Size", CommonUtils.format_bytes_human_readable(info["size_bytes"])
    )
    print_key_value(
        "Created", CommonUtils.human_readable_datetime(info["created_at"])
    )
    print_key_value(
        "Updated", CommonUtils.human_readable_datetime(info["updated_at"])
    )

    print_section_header("STORAGE")
    print_key_value("Path", info["path"])
    print_key_value("VM ID", info["vm_id"] or "-")
    if info.get("vm_name"):
        print_key_value("VM Name", info["vm_name"])

    disk_info = info.get("disk_info", {})
    if disk_info:
        print_section_header("DISK INFO")
        for key, value in disk_info.items():
            print_key_value(key, str(value))


@volume_app.command(name="resize")
@handle_errors
def volume_resize(
    name: str = typer.Argument(..., help="Volume name or ID prefix"),
    size: str = typer.Argument(..., help="New size (e.g., 1G, 512M)"),
) -> None:
    """Resize a volume."""
    result = VolumeOperation.resize(VolumeCreateInput(name=name, size=size))
    if result.is_error:
        print_error(result.message)
        raise typer.Exit(code=1)
    print_success(result.message)
