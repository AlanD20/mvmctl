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
from mvmctl.cli._helpers import build_mvm_defaults
from mvmctl.constants import DEFAULT_FC_CONFIG_FILENAME
from mvmctl.exceptions import MVMError
from mvmctl.utils.console import print_error, print_info, print_success
from mvmctl.utils.error_handler import handle_mvm_error
from mvmctl.utils.fs import get_assets_dir

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
        None,
        "--config-dir",
        help="Configuration directory",
    ),
) -> None:
    """Print resolved configuration."""
    effective_config_dir = config_dir if config_dir is not None else get_assets_dir()
    try:
        config = load_config(effective_config_dir, build_mvm_defaults())
        data = dump_config(config, section)
        typer.echo(json.dumps(data, indent=2))
    except MVMError as e:
        handle_mvm_error(e)


@app.command()
def validate(
    config_dir: Path = typer.Option(
        None,
        "--config-dir",
        help="Configuration directory",
    ),
) -> None:
    """Validate all YAML config files."""
    effective_config_dir = config_dir if config_dir is not None else get_assets_dir()
    try:
        config = load_config(effective_config_dir, build_mvm_defaults())
        errors = validate_config(config)

        if errors:
            print_error("Configuration validation failed:")
            for error in errors:
                print_error(f"  - {error}")
            raise typer.Exit(code=1)
        else:
            print_success("Configuration is valid")
    except MVMError as e:
        handle_mvm_error(e)


@app.command()
def dump_vm(
    name: str = typer.Option(..., "--name", help="VM name"),
) -> None:
    """Print the Firecracker JSON config for a VM."""
    from mvmctl.api.vms import get_vm_manager
    from mvmctl.utils.fs import get_vm_dir_by_hash

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        print_error(f"VM '{name}' not found")
        raise typer.Exit(code=1)

    vm_dir = get_vm_dir_by_hash(vm.id)
    config_file = vm_dir / DEFAULT_FC_CONFIG_FILENAME

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
    except (ValueError, KeyError, OSError) as exc:
        handle_mvm_error(exc)
    print_success(f"{key} = {value}")
