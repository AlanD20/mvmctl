import json
from pathlib import Path

import typer

from mvmctl.api.config import (
    dump_config,
    get_config_value,
    load_config,
    set_config_value,
    validate_config,
)
from mvmctl.exceptions import MVMError
from mvmctl.utils.console import print_error, print_info, print_success
from mvmctl.utils.fs import get_assets_dir, get_vm_dir

app = typer.Typer(
    help="Configuration commands",
    rich_markup_mode=None,
    add_completion=False,
)


@app.callback(invoke_without_command=True)
def config_callback(ctx: typer.Context) -> None:
    if ctx.invoked_subcommand is None:
        typer.echo(ctx.get_help())
        raise typer.Exit()


@app.command()
def show(
    section: str | None = typer.Option(None, "--section", help="Config section to show"),
    config_dir: Path = typer.Option(
        get_assets_dir(),
        "--config-dir",
        help="Configuration directory",
    ),
) -> None:
    """Print resolved configuration."""
    try:
        config = load_config(config_dir)
        data = dump_config(config, section)
        typer.echo(json.dumps(data, indent=2))
    except MVMError as e:
        print_error(f"Failed to load config: {e}")
        raise typer.Exit(code=1)


@app.command()
def validate(
    config_dir: Path = typer.Option(
        get_assets_dir(),
        "--config-dir",
        help="Configuration directory",
    ),
) -> None:
    """Validate all YAML config files."""
    try:
        config = load_config(config_dir)
        errors = validate_config(config)

        if errors:
            print_error("Configuration validation failed:")
            for error in errors:
                print_error(f"  - {error}")
            raise typer.Exit(code=1)
        else:
            print_success("Configuration is valid")
    except MVMError as e:
        print_error(f"Validation error: {e}")
        raise typer.Exit(code=1)


@app.command()
def dump_vm(
    name: str = typer.Option(..., "--name", help="VM name"),
) -> None:
    """Print the Firecracker JSON config for a VM."""
    vm_dir = get_vm_dir(name)
    config_file = vm_dir / "firecracker.json"

    if not config_file.exists():
        print_error(f"VM '{name}' not found or no config file")
        raise typer.Exit(code=1)

    try:
        with open(config_file, "r") as f:
            data = json.load(f)
            typer.echo(json.dumps(data, indent=2))
    except json.JSONDecodeError as e:
        print_error(f"Invalid JSON in config file: {e}")
        raise typer.Exit(code=1)


@app.command(name="get")
def config_get(
    key: str = typer.Argument(..., help="Config key (dot-notation, e.g. network_interface)"),
) -> None:
    """Get a configuration value."""
    value = get_config_value(key)
    if value is None:
        print_info(f"{key} = (not set)")
    else:
        typer.echo(f"{key} = {value}")


@app.command(name="set")
def config_set(
    key: str = typer.Argument(..., help="Config key (dot-notation, e.g. network_interface)"),
    value: str = typer.Argument(..., help="Value to set"),
) -> None:
    """Set a configuration value."""
    try:
        set_config_value(key, value)
    except Exception as exc:
        print_error(f"Failed to set config: {exc}")
        raise typer.Exit(code=1)
    print_success(f"{key} = {value}")
