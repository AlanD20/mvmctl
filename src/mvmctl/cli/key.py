"""SSH key management commands."""

from __future__ import annotations

import json
from pathlib import Path

import typer

from mvmctl.cli._common import (
    ListingColumn,
    render_listing,
    resolve_listing_style,
)
from mvmctl.cli._completion import _complete_key_names
from mvmctl.utils.cli import handle_errors, mvm_cli

key_app = typer.Typer(
    help="SSH key management",
    no_args_is_help=True,
    add_completion=False,
)


@key_app.callback()
def key_callback(ctx: typer.Context) -> None:  # noqa: ARG001
    pass


_KEY_COLUMNS = [
    ListingColumn("", lambda k: mvm_cli.format_marker(k.is_default)),
    ListingColumn("ID", lambda k: mvm_cli.format_id(k.id)),
    ListingColumn(
        "Name", lambda k: mvm_cli.format_name(k.name, not k.is_present)
    ),
    ListingColumn("Algorithm", lambda k: k.algorithm),
    ListingColumn("Fingerprint", lambda k: k.fingerprint, long_only=True),
    ListingColumn("Created", lambda k: mvm_cli.format_timestamp(k.created_at)),
]


@key_app.command(name="ls")
@handle_errors
def key_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    long_output: bool = typer.Option(
        False, "--long", help="Show full listing with all columns"
    ),
) -> None:
    """List all SSH keys."""
    from mvmctl.api import KeyOperation

    keys = KeyOperation.list_all()

    if json_output:
        data = [k.to_dict() for k in keys]
        typer.echo(json.dumps(data, indent=2, default=str))
        return

    if not keys:
        mvm_cli.info(
            "No keys found. Use 'mvm key create <name>' or "
            "'mvm key add <name> <path>' to add one."
        )
        return

    style = resolve_listing_style(long_output)

    render_listing(keys, _KEY_COLUMNS, style)


@key_app.command(name="add")
@handle_errors
def key_add(
    name: str = typer.Argument(..., help="Key name"),
    path: Path = typer.Argument(..., help="Path to public key file"),
    force: bool = typer.Option(
        False, "--force", "-f", help="Overwrite existing key"
    ),
) -> None:
    """Add an existing public key to the cache."""
    from mvmctl.api import KeyOperation

    result = KeyOperation.add(name=name, pub_key_path=path, overwrite=force)
    if result.is_error:
        mvm_cli.error(result.message or f"Add failed: {name}")
        raise typer.Exit(code=1)
    assert result.item is not None
    mvm_cli.success(
        f"Added: {result.item.name} (ID: {mvm_cli.format_id(result.item.id)})"
    )


@key_app.command(name="create")
@handle_errors
def key_create(
    name: str = typer.Argument(..., help="Key name"),
    algorithm: str | None = typer.Option(
        None, "--algorithm", help="Key algorithm (ed25519, rsa, ecdsa)"
    ),
    bits: int | None = typer.Option(
        None, "--bits", help="Key size in bits (RSA only; default 4096)"
    ),
    comment: str | None = typer.Option(None, "--comment", help="Key comment"),
    out: Path | None = typer.Option(None, "--out", help="Output directory"),
    set_default: bool = typer.Option(
        False, "--default", "-d", help="Set as default key"
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Overwrite existing key"
    ),
) -> None:
    """Generate a new SSH keypair."""
    if algorithm is None:
        mvm_cli.info("Select algorithm:")
        mvm_cli.info("  1. ed25519")
        mvm_cli.info("  2. rsa")
        mvm_cli.info("  3. ecdsa")
        choice = typer.prompt("Enter number", default="1")
        algo_map = {"1": "ed25519", "2": "rsa", "3": "ecdsa"}
        algorithm = algo_map.get(choice.strip(), "ed25519")

    from mvmctl.api import KeyCreateInput, KeyOperation

    inputs = KeyCreateInput(
        name=name,
        algorithm=algorithm,
        bits=bits,
        output_dir=out,
        comment=comment,
        overwrite=force,
        set_default=set_default,
    )
    result = KeyOperation.create(inputs)
    if result.is_error:
        mvm_cli.error(result.message or f"Create failed: {name}")
        raise typer.Exit(code=1)
    assert result.item is not None
    mvm_cli.success(
        f"Created: {result.item.name} (ID: {result.item.fingerprint})"
    )


@key_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def key_rm(
    ctx: typer.Context,
    names: list[str] = typer.Argument(
        None, help="Key name(s) to remove", autocompletion=_complete_key_names
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Force removal even if key is in use"
    ),
) -> None:
    """Remove one or more SSH keys."""
    effective_names: list[str] = list(names) if names else []
    if not effective_names:
        mvm_cli.error("Provide at least one key name to remove")
        raise typer.Exit(code=1)

    from mvmctl.api import KeyInput, KeyOperation

    inputs = KeyInput(name=effective_names)
    result = KeyOperation.remove(inputs, force=force)
    for r in result.items:
        item_name = r.item.name if r.item else "unknown"
        if r.is_ok:
            mvm_cli.success(f"Removed: {item_name}")
        else:
            mvm_cli.error(r.message or f"Remove failed: {item_name}")


@key_app.command(
    name="inspect",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def key_inspect(
    ctx: typer.Context,
    name: str = typer.Argument(
        None, help="Key name or ID", autocompletion=_complete_key_names
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Inspect an SSH key."""
    name = mvm_cli.check_name_arg(ctx, name)
    from mvmctl.api import KeyInput, KeyOperation

    inputs = KeyInput(name=[name])

    info = KeyOperation.inspect(inputs)

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    key_name = info.get("key", {}).get("name", name)
    mvm_cli.print_dict_tree(info, title=f"Key: {key_name}")


@key_app.command(name="export")
@handle_errors
def key_export(
    ctx: typer.Context,
    name: str = typer.Argument(
        None, help="Key name or ID", autocompletion=_complete_key_names
    ),
    out: Path = typer.Option(..., "--out", help="Destination directory"),
    force: bool = typer.Option(
        False, "--force", "-f", help="Overwrite existing files"
    ),
) -> None:
    """Export a keypair to a directory."""
    name = mvm_cli.check_name_arg(ctx, name)
    from mvmctl.api import KeyInput, KeyOperation

    inputs = KeyInput(name=[name])
    result = KeyOperation.export(inputs, destination=out, overwrite=force)
    if result.is_error:
        mvm_cli.error(result.message or f"Export failed: {name}")
        raise typer.Exit(code=1)
    assert result.item is not None
    private_path, public_path = result.item
    mvm_cli.success(f"Exported: {private_path}")
    mvm_cli.info(f"Exported public key to {public_path}")


@key_app.command(name="default")
@handle_errors
def key_set_default(
    names: list[str] = typer.Argument(
        None,
        help="Key name(s) to set as default",
        autocompletion=_complete_key_names,
    ),
    clear: bool = typer.Option(False, "--clear", help="Clear all default keys"),
) -> None:
    """Set default SSH keys, or clear with --clear."""
    from mvmctl.api import KeyInput, KeyOperation

    if clear:
        clear_result = KeyOperation.clear_defaults()
        if clear_result.is_error:
            mvm_cli.error(clear_result.message or "Clear defaults failed")
            raise typer.Exit(code=1)
        mvm_cli.success("Cleared: all default keys")
        return

    effective_names: list[str] = list(names) if names else []
    if not effective_names:
        mvm_cli.error("Provide at least one key name or use --clear")
        raise typer.Exit(code=1)

    inputs = KeyInput(name=effective_names)
    set_result = KeyOperation.set_default(inputs)
    if set_result.is_error:
        mvm_cli.error(set_result.message or "Set default failed")
        raise typer.Exit(code=1)
    mvm_cli.success(f"Default key(s) set: {', '.join(effective_names)}")


__all__ = ["key_app"]
