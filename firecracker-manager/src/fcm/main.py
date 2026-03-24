#!/usr/bin/env python3
"""Firecracker Manager CLI - Main entry point."""

import importlib.metadata
import logging
import os

import typer
from fcm.cli import (
    vm,
    config,
    asset,
    host,
    network,
    key,
    configure,
)  # TODO: P-M8 — lazy-load CLI modules when startup time matters
from fcm.constants import CLI_NAME, _BOOTSTRAP_NAME, env_var


def _get_version() -> str:
    """Read the version from package metadata, falling back to __version__."""
    try:
        return importlib.metadata.version(_BOOTSTRAP_NAME)
    except importlib.metadata.PackageNotFoundError:
        from fcm import __version__

        return __version__


app = typer.Typer(
    name=CLI_NAME,
    help="Firecracker Manager - Manage microVMs",
    rich_markup_mode="rich",
    pretty_exceptions_short=True,
    pretty_exceptions_show_locals=False,
)

app.add_typer(vm.app, name="vm", help="VM lifecycle management", rich_help_panel="VM Management")
app.add_typer(host.app, name="host", help="Host configuration", rich_help_panel="Host Management")
app.add_typer(network.app, name="network", help="Network management", rich_help_panel="Networking")
app.add_typer(key.app, name="key", help="SSH key management", rich_help_panel="Keys")
app.add_typer(
    config.app, name="config", help="Configuration commands", rich_help_panel="Configuration"
)
app.add_typer(
    configure.app, name="configure", help="Guided setup wizard", rich_help_panel="Configuration"
)
app.add_typer(asset.kernel_app, name="kernel", help="Kernel management", rich_help_panel="Assets")
app.add_typer(asset.image_app, name="image", help="Image management", rich_help_panel="Assets")
app.add_typer(asset.bin_app, name="bin", help="Binary management", rich_help_panel="Assets")
app.command("clear")(asset.clear_assets)


@app.callback(invoke_without_command=True)
def callback(
    ctx: typer.Context,
    verbose: bool = typer.Option(False, "--verbose", "-v", help="Enable verbose output"),
    debug: bool = typer.Option(False, "--debug", help="Enable debug mode"),
    version: bool = typer.Option(False, "--version", is_eager=True, help="Show version and exit"),
) -> None:
    """Firecracker Manager CLI."""
    if version:
        typer.echo(f"{CLI_NAME} {_get_version()}")
        raise typer.Exit()

    # If no subcommand was given, show help
    if ctx.invoked_subcommand is None:
        typer.echo(ctx.get_help())
        raise typer.Exit()

    if os.getuid() == 0:
        from fcm.utils.console import console as _console

        _console.print(
            "[yellow]Warning: running as root. Consider using the 'fcm' group instead "
            "(set up via 'sudo fcm host init').[/yellow]"
        )

    # Determine log level: --debug > --verbose > FCM_LOG_LEVEL env var > WARNING
    if debug:
        level = logging.DEBUG
        # Show locals in tracebacks when debug mode is active
        app.pretty_exceptions_show_locals = True
    elif verbose:
        level = logging.INFO
    else:
        env_level = os.environ.get(env_var("LOG_LEVEL"), "WARNING").upper()
        level = getattr(logging, env_level, logging.WARNING)

    logging.basicConfig(
        level=level,
        format="%(levelname)s: %(name)s: %(message)s",
    )

    # Silently reconcile networks (no-op if host not initialized)
    try:
        from fcm.core.network_manager import reconcile_networks

        reconcile_networks()
    except Exception:
        pass  # Never fail startup


@app.command(name="version")
def version_cmd(ctx: typer.Context) -> None:
    """Show the version and exit."""
    typer.echo(f"{CLI_NAME} {_get_version()}")
    raise typer.Exit()


@app.command(name="help")
def help_cmd(
    ctx: typer.Context,
    args: list[str] = typer.Argument(default=None),
) -> None:
    """Show help for fcm or a subcommand."""
    import click

    if not args:
        typer.echo(ctx.parent.get_help() if ctx.parent else "")
        raise typer.Exit()

    # Navigate the click group hierarchy to find the subcommand
    root = ctx.find_root()
    cmd = root.command
    for arg in args:
        if hasattr(cmd, "get_command"):
            sub = cmd.get_command(root, arg)
            if sub is None:
                typer.echo(f"Unknown command: {' '.join(args)}", err=True)
                raise typer.Exit(code=1)
            cmd = sub
        else:
            typer.echo(f"'{arg}' has no subcommands", err=True)
            raise typer.Exit(code=1)

    # Print help for the found command
    with click.Context(cmd, info_name=" ".join([root.info_name or CLI_NAME] + args)) as sub_ctx:
        typer.echo(cmd.get_help(sub_ctx))
    raise typer.Exit()


if __name__ == "__main__":
    app()
