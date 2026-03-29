"""SSH key management commands."""

import json
from pathlib import Path

import typer

from mvmctl.api.keys import (
    add_key,
    clear_default_keys,
    create_key,
    export_key,
    inspect_key,
    list_keys,
    remove_key,
    resolve_key_inputs,
    set_default_keys,
)
from mvmctl.cli._helpers import check_name_arg
from mvmctl.exceptions import MVMKeyError
from mvmctl.utils.console import print_error, print_info, print_success, print_table
from mvmctl.utils.time import human_readable_time
from mvmctl.utils.validation import validate_entity_name

app = typer.Typer(
    help="SSH key management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the key command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@app.command(name="ls")
def ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List all keys in the cache."""
    keys = list_keys()

    if json_output:
        from dataclasses import asdict

        result = []
        for k in keys:
            d = asdict(k)
            result.append(d)
        typer.echo(json.dumps(result, indent=2))
        return

    if not keys:
        print_info("No keys found. Add one with: mvm key add <name> <path>")
        return

    rows = []
    for k in keys:
        status = "yes" if k.has_private_key else "no"
        rows.append(
            [
                k.name,
                k.fingerprint,
                k.algorithm,
                k.comment,
                status,
                human_readable_time(k.added_at) if k.added_at else "-",
            ]
        )
    print_table(
        title="SSH Keys",
        columns=["Name", "Fingerprint", "Algorithm", "Comment", "Private Key", "Added"],
        rows=rows,
    )


@app.command(context_settings={"allow_extra_args": True, "ignore_unknown_options": True})
def add(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Name for this key"),
    public_key_path: str | None = typer.Argument(None, help="Path to public key file"),
    overwrite: bool = typer.Option(False, "--overwrite", help="Overwrite existing key"),
) -> None:
    """Import an existing public key into the cache."""
    name = check_name_arg(ctx, name)
    validate_entity_name(name, "key")
    if public_key_path is None:
        print_error("Missing argument 'PUBLIC_KEY_PATH'")
        raise typer.Exit(code=1)
    try:
        info = add_key(name, public_key_path, overwrite=overwrite)
    except MVMKeyError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Key '{info.name}' added")
    print_info(f"  Algorithm:   {info.algorithm}")
    print_info(f"  Fingerprint: {info.fingerprint}")
    if info.comment:
        print_info(f"  Comment:     {info.comment}")


@app.command(context_settings={"allow_extra_args": True, "ignore_unknown_options": True})
def create(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Name for the new keypair"),
    output: str | None = typer.Option(
        None, "--out", "-o", help="Directory for private key (default: cache dir)"
    ),
    comment: str | None = typer.Option(None, "--comment", help="Comment for the key"),
    force: bool = typer.Option(False, "--force", "-f", help="Overwrite existing key files"),
) -> None:
    """Generate a new ED25519 keypair."""
    from mvmctl.utils.fs import get_keys_dir

    name = check_name_arg(ctx, name)
    validate_entity_name(name, "key")

    output_dir = Path(output) if output else get_keys_dir()
    private_key_path = output_dir / name
    pub_key_path = output_dir / f"{name}.pub"

    if not force and (private_key_path.exists() or pub_key_path.exists()):
        existing = private_key_path if private_key_path.exists() else pub_key_path
        if not typer.confirm(f"Key file already exists: {existing}. Overwrite?"):
            raise typer.Exit(code=0)

    try:
        info, private_key_path = create_key(
            name=name,
            output_dir=output,
            comment=comment,
            overwrite=force,
        )
    except MVMKeyError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Key '{info.name}' created")
    print_info(f"  Private key: {private_key_path} (cached)")
    print_info(f"  Algorithm:   {info.algorithm}")
    print_info(f"  Fingerprint: {info.fingerprint}")


@app.command(
    name="remove",
    hidden=True,
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
def remove(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Key name"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove a key from the cache."""
    name = check_name_arg(ctx, name)
    validate_entity_name(name, "key")
    if not force:
        typer.confirm(f"Remove key '{name}' from cache?", abort=True)

    try:
        remove_key(name)
    except MVMKeyError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Key '{name}' removed from cache")


@app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
def rm(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Key name"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Alias for remove."""
    remove(ctx=ctx, name=name, force=force)


@app.command(context_settings={"allow_extra_args": True, "ignore_unknown_options": True})
def export(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Key name to export"),
    output: str | None = typer.Option(
        None, "--out", "-o", help="Destination directory (default: ~/.ssh/)"
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Overwrite existing key files"),
) -> None:
    """Export a cached keypair to ~/.ssh/ or custom directory."""
    name = check_name_arg(ctx, name)
    validate_entity_name(name, "key")

    if not force:
        dest_dir = Path(output) if output else Path.home() / ".ssh"
        dest_private = dest_dir / name
        dest_public = dest_dir / f"{name}.pub"

        if dest_private.exists() or dest_public.exists():
            existing = dest_private if dest_private.exists() else dest_public
            if not typer.confirm(f"Key file already exists: {existing}. Overwrite?"):
                raise typer.Exit(code=0)

    try:
        private_path, public_path = export_key(name, output, overwrite=True)
    except MVMKeyError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Key '{name}' exported")
    print_info(f"  Private key: {private_path}")
    print_info(f"  Public key:  {public_path}")


@app.command(context_settings={"allow_extra_args": True, "ignore_unknown_options": True})
def inspect(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Key name"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a key."""
    name = check_name_arg(ctx, name)
    validate_entity_name(name, "key")
    try:
        info = inspect_key(name)
    except MVMKeyError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    print_info(f"Key: {info['name']}")
    print_info(f"  Algorithm:   {info['algorithm']}")
    print_info(f"  Fingerprint: {info['fingerprint']}")
    print_info(f"  Comment:     {info['comment']}")
    print_info(f"  Added:       {info['added_at']}")
    print_info(f"  Public key:  {info['public_key']}")


@app.command(
    name="set-default",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
def set_default(
    ctx: typer.Context,
    keys: list[str] | None = typer.Argument(None, help="Key names, paths, or fingerprints"),
    clear: bool = typer.Option(False, "--clear", help="Clear all default keys instead of setting"),
) -> None:
    """Set one or more keys as the default for SSH connections."""
    if clear:
        try:
            clear_default_keys()
        except MVMKeyError as e:
            print_error(str(e))
            raise typer.Exit(code=1)
        print_success("Cleared all default keys")
        return

    effective_keys = list(keys) if keys else []
    if not effective_keys:
        print_error("Provide at least one key name")
        raise typer.Exit(code=1)

    try:
        resolved = resolve_key_inputs(effective_keys)
        set_default_keys(resolved)
    except MVMKeyError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Default keys set: {', '.join(resolved)}")
