"""SSH key management commands."""

import json
from pathlib import Path

import typer

from mvmctl.api.keys import (
    add_key,
    clear_default_keys,
    create_key,
    export_key,
    get_default_keys,
    inspect_key,
    list_keys,
    remove_key,
    resolve_key_inputs,
    set_default_keys,
)
from mvmctl.cli._helpers import check_name_arg
from mvmctl.exceptions import MVMKeyError
from mvmctl.utils.console import (
    get_combined_marker,
    print_error,
    print_info,
    print_success,
    print_table,
)
from mvmctl.utils.fs import get_keys_config_dir, is_file_missing
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
    default_keys = get_default_keys()

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

    rows = []
    keys_dir = get_keys_config_dir()
    for k in keys:
        status = "yes" if k.has_private_key else "no"
        private_key_path = keys_dir / k.name
        is_missing = is_file_missing(private_key_path)
        display_name = get_combined_marker(k.name in default_keys, is_missing) + k.name
        rows.append(
            [
                display_name,
                k.fingerprint,
                k.algorithm,
                k.comment,
                status,
                human_readable_time(k.added_at) if k.added_at else "-",
            ]
        )
    print_table(
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
    import os

    name = check_name_arg(ctx, name)
    validate_entity_name(name, "key")
    if public_key_path is None:
        print_error("Missing argument 'PUBLIC_KEY_PATH'")
        print_info("Usage: mvm key add <name> <path-to-public-key>")
        print_info("Example: mvm key add mykey ~/.ssh/id_rsa.pub")
        raise typer.Exit(code=1)

    path_obj = Path(public_key_path)

    if not path_obj.exists():
        print_error(f"File not found: {public_key_path}")
        print_info("Check the path and ensure the file exists:")
        print_info(f"  ls -la {public_key_path}")
        print_info("Common locations for SSH keys:")
        print_info("  ~/.ssh/id_rsa.pub")
        print_info("  ~/.ssh/id_ed25519.pub")
        raise typer.Exit(code=1)

    if not public_key_path.endswith(".pub"):
        pub_path = Path(public_key_path + ".pub")
        if pub_path.exists():
            print_error(f"File does not appear to be a public key: {public_key_path}")
            print_info("Public keys typically end in .pub")
            print_info(f"Did you mean: {pub_path}")
            print_info(f"Try: mvm key add {name} {pub_path}")
        else:
            print_error(f"File does not appear to be a public key: {public_key_path}")
            print_info("Public keys typically end in .pub")
            print_info("Example: ~/.ssh/id_rsa.pub")
            print_info("")
            print_info("If this is a private key, the public key may be at:")
            print_info(f"  {public_key_path}.pub")
        raise typer.Exit(code=1)

    if not os.access(path_obj, os.R_OK):
        print_error(f"Cannot read file: {public_key_path}")
        print_info("Check file permissions:")
        print_info(f"  ls -l {public_key_path}")
        print_info("")
        print_info("To fix permissions, run:")
        print_info(f"  chmod 644 {public_key_path}")
        raise typer.Exit(code=1)

    try:
        info = add_key(name, public_key_path, overwrite=overwrite)
    except MVMKeyError as e:
        error_msg = str(e)
        print_error(f"Failed to add key: {error_msg}")
        print_info("")
        print_info("Common issues:")
        if "already exists" in error_msg.lower():
            print_info("  - Key name already in cache: Use --overwrite to replace")
            print_info(f"    mvm key add {name} {public_key_path} --overwrite")
        elif "private key" in error_msg.lower():
            print_info("  - You provided a private key instead of a public key")
            print_info("  - Public keys end in .pub and contain 'ssh-rsa', 'ssh-ed25519', etc.")
        elif "not found" in error_msg.lower():
            print_info("  - File doesn't exist: Check the path")
        else:
            print_info("  - File doesn't exist: Check the path")
            print_info("  - Permission denied: Check file permissions")
            print_info("  - Not a valid public key: Ensure the file contains a valid SSH key")
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
    name = check_name_arg(ctx, name)
    validate_entity_name(name, "key")

    output_dir = Path(output) if output else get_keys_config_dir()
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
) -> None:
    """Remove a key from the cache."""
    name = check_name_arg(ctx, name)
    validate_entity_name(name, "key")

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
) -> None:
    """Alias for remove."""
    remove(ctx=ctx, name=name)


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

    from datetime import datetime

    added_formatted = datetime.fromisoformat(info["added_at"]).strftime("%Y/%m/%d %H:%M:%S")
    print_info(f"Key: {info['name']}")
    print_info(f"  Algorithm:   {info['algorithm']}")
    print_info(f"  Fingerprint: {info['fingerprint']}")
    print_info(f"  Comment:     {info['comment']}")
    print_info(f"  Added:       {added_formatted}")
    print_info(f"  Public key:  {info['public_key']}")
    if info.get("private_key_path"):
        print_info(f"  Private key path: {info['private_key_path']}")
    if info.get("public_key_path"):
        print_info(f"  Public key path:  {info['public_key_path']}")


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
