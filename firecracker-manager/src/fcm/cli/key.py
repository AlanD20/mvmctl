"""SSH key management commands."""

import json

import typer
from rich.console import Console
from rich.table import Table

from fcm.core.key_manager import (
    add_key,
    create_key,
    inspect_key,
    list_keys,
    remove_key,
)
from fcm.exceptions import KeyError as FCMKeyError
from fcm.utils.console import print_error, print_info, print_success

app = typer.Typer(help="SSH key management")
console = Console()


@app.command(name="ls")
def ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List all keys in the cache."""
    keys = list_keys()

    if json_output:
        from dataclasses import asdict
        typer.echo(json.dumps([asdict(k) for k in keys], indent=2))
        return

    if not keys:
        print_info("No keys found. Add one with: fcm key add <name> <path>")
        return

    table = Table(title="SSH Keys")
    table.add_column("Name", style="cyan", no_wrap=True)
    table.add_column("Algorithm", style="green")
    table.add_column("Fingerprint")
    table.add_column("Comment")
    table.add_column("Added")

    for k in keys:
        added = k.added_at[:19] if k.added_at else "-"
        table.add_row(k.name, k.algorithm, k.fingerprint, k.comment, added)

    console.print(table)


@app.command(name="list", hidden=True)
def list_cmd(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Alias for ls."""
    ls(json_output=json_output)


@app.command()
def add(
    name: str = typer.Argument(..., help="Name for this key"),
    public_key_path: str = typer.Argument(..., help="Path to public key file"),
) -> None:
    """Import an existing public key into the cache."""
    try:
        info = add_key(name, public_key_path)
    except FCMKeyError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Key '{info.name}' added")
    print_info(f"  Algorithm:   {info.algorithm}")
    print_info(f"  Fingerprint: {info.fingerprint}")
    if info.comment:
        print_info(f"  Comment:     {info.comment}")


@app.command()
def create(
    name: str = typer.Argument(..., help="Name for the new keypair"),
    output: str = typer.Option(None, "--output", help="Directory for private key (default: ~/.ssh/)"),
    comment: str = typer.Option(None, "--comment", help="Comment for the key"),
    overwrite: bool = typer.Option(False, "--overwrite", help="Overwrite existing key file"),
) -> None:
    """Generate a new ED25519 keypair."""
    try:
        info, private_key_path = create_key(
            name=name,
            output_dir=output,
            comment=comment,
            overwrite=overwrite,
        )
    except FCMKeyError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Key '{info.name}' created")
    print_info(f"  Private key: {private_key_path}")
    print_info(f"  Algorithm:   {info.algorithm}")
    print_info(f"  Fingerprint: {info.fingerprint}")


@app.command(name="remove")
def remove(
    name: str = typer.Argument(..., help="Key name"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove a key from the cache."""
    if not force:
        typer.confirm(f"Remove key '{name}' from cache?", abort=True)

    try:
        remove_key(name)
    except FCMKeyError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Key '{name}' removed from cache")


@app.command(name="rm", hidden=True)
def rm(
    name: str = typer.Argument(..., help="Key name"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Alias for remove."""
    remove(name=name, force=force)


@app.command()
def inspect(
    name: str = typer.Argument(..., help="Key name"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a key."""
    try:
        info = inspect_key(name)
    except FCMKeyError as e:
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
