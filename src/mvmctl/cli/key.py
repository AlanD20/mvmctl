"""SSH key management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING

import typer

from mvmctl.api.inputs._key_create_input import KeyCreateInput
from mvmctl.api.inputs._key_input import KeyInput
from mvmctl.api.key_operations import KeyOperation
from mvmctl.utils.cli import CliUtils, handle_errors
from mvmctl.utils.common import CommonUtils
from mvmctl.utils.console import (
    print_error,
    print_info,
    print_key_value,
    print_success,
    print_table,
)
from mvmctl.utils.full_hash import HashGenerator

if TYPE_CHECKING:
    pass

key_app = typer.Typer(
    help="SSH key management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@key_app.callback()
def key_callback(ctx: typer.Context) -> None:  # noqa: ARG001
    pass


@key_app.command(name="ls")
@handle_errors
def key_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List all SSH keys."""
    keys = KeyOperation.list_all()

    if json_output:
        data = [
            {
                "id": k.fingerprint,
                "name": k.name,
                "algorithm": k.algorithm,
                "comment": k.comment,
                "is_default": k.is_default,
                "created_at": k.created_at,
            }
            for k in keys
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    if not keys:
        print_info(
            "No keys found. Use 'mvm key create <name>' or "
            "'mvm key add <name> <path>' to add one."
        )
        return

    rows: list[list[str]] = []
    for k in keys:
        is_default = k.is_default
        marker = CommonUtils._get_combined_marker(is_default, not k.is_present)
        rows.append(
            [
                k.fingerprint,
                f"{marker}{k.name}",
                k.algorithm,
                CommonUtils.human_readable_datetime(k.created_at),
            ]
        )

    print_table(
        columns=["Fingerprint", "Name", "Algorithm", "Added"],
        rows=rows,
    )


@key_app.command(name="add")
@handle_errors
def key_add(
    name: str = typer.Argument(..., help="Key name"),
    path: Path = typer.Argument(..., help="Path to public key file"),
    overwrite: bool = typer.Option(
        False, "--overwrite", help="Overwrite existing key"
    ),
) -> None:
    """Add an existing public key to the cache."""
    key_item = KeyOperation.add(
        name=name, pub_key_path=path, overwrite=overwrite
    )
    print_success(
        f"Key '{key_item.name}' added (ID: {HashGenerator.shorten(key_item.id)})"
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
        False, "--set-default", help="Set as default key"
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Overwrite existing key"
    ),
) -> None:
    """Generate a new SSH keypair."""
    if algorithm is None:
        print_info("Select algorithm:")
        print_info("  1. ed25519")
        print_info("  2. rsa")
        print_info("  3. ecdsa")
        choice = typer.prompt("Enter number", default="1")
        algo_map = {"1": "ed25519", "2": "rsa", "3": "ecdsa"}
        algorithm = algo_map.get(choice.strip(), "ed25519")

    inputs = KeyCreateInput(
        name=name,
        algorithm=algorithm,
        bits=bits,
        output_dir=out,
        comment=comment,
        overwrite=force,
        set_default=set_default,
    )
    key_item = KeyOperation.create(inputs)
    print_success(f"Key '{key_item.name}' created (ID: {key_item.fingerprint})")


@key_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def key_rm(
    ctx: typer.Context,
    names: list[str] = typer.Argument(None, help="Key name(s) to remove"),
) -> None:
    """Remove one or more SSH keys."""
    effective_names: list[str] = list(names) if names else []
    if not effective_names:
        print_error("Provide at least one key name to remove")
        raise typer.Exit(code=1)

    inputs = KeyInput(name=effective_names)
    KeyOperation.remove(inputs)
    print_success(f"Removed key(s): {' '.join(effective_names)}")


@key_app.command(
    name="inspect",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def key_inspect(
    ctx: typer.Context,
    name: str = typer.Argument(None, help="Key name or ID"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Inspect an SSH key."""
    name = CliUtils.check_name_arg(ctx, name)
    inputs = KeyInput(name=[name])

    if json_output:
        result = KeyOperation.inspect(inputs, is_json=True)
        typer.echo(json.dumps(result, indent=2))
        return

    key_item = KeyOperation.get(inputs)
    print_key_value("ID", HashGenerator.shorten(key_item.id))
    print_key_value("Name", key_item.name)
    print_key_value("Fingerprint", key_item.fingerprint)
    print_key_value("Algorithm", key_item.algorithm)
    print_key_value("Comment", key_item.comment)
    print_key_value("Public Key", key_item.public_key_path)
    if key_item.private_key_path:
        print_key_value("Private Key", key_item.private_key_path)
    print_key_value("Default", "yes" if key_item.is_default else "no")
    print_key_value(
        "Created", CommonUtils.human_readable_datetime(key_item.created_at)
    )


@key_app.command(name="export")
@handle_errors
def key_export(
    ctx: typer.Context,
    name: str = typer.Argument(None, help="Key name or ID"),
    out: Path = typer.Option(..., "--out", help="Destination directory"),
    force: bool = typer.Option(
        False, "--force", "-f", help="Overwrite existing files"
    ),
) -> None:
    """Export a keypair to a directory."""
    name = CliUtils.check_name_arg(ctx, name)
    inputs = KeyInput(name=[name])
    private_path, public_path = KeyOperation.export(
        inputs, destination=out, overwrite=force
    )
    print_success(f"Exported private key to {private_path}")
    print_info(f"Exported public key to {public_path}")


@key_app.command(name="set-default")
@handle_errors
def key_set_default(
    names: list[str] = typer.Argument(
        None, help="Key name(s) to set as default"
    ),
    clear: bool = typer.Option(False, "--clear", help="Clear all default keys"),
) -> None:
    """Set default SSH keys, or clear with --clear."""
    if clear:
        KeyOperation.clear_defaults()
        print_success("Cleared all default keys")
        return

    effective_names: list[str] = list(names) if names else []
    if not effective_names:
        print_error("Provide at least one key name or use --clear")
        raise typer.Exit(code=1)

    inputs = KeyInput(name=effective_names)
    KeyOperation.set_default(inputs)
    print_success(f"Default key(s) set: {', '.join(effective_names)}")


__all__ = ["key_app"]
