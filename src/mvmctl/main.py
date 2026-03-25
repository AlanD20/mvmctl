#!/usr/bin/env python3
"""mvm CLI - Main entry point."""

from __future__ import annotations

import importlib
import importlib.metadata
import logging
import os
from dataclasses import dataclass

import click
import typer
import typer.models

from mvmctl.constants import _BOOTSTRAP_NAME, CLI_NAME, env_var


def _get_version() -> str:
    try:
        return importlib.metadata.version(_BOOTSTRAP_NAME)
    except importlib.metadata.PackageNotFoundError:
        from mvmctl import __version__

        return __version__


@dataclass(frozen=True)
class _LazyCommandSpec:
    module: str
    attribute: str
    help_text: str


_COMMAND_SPECS: dict[str, _LazyCommandSpec] = {
    "vm": _LazyCommandSpec("mvmctl.cli.vm", "app", "VM lifecycle management"),
    "host": _LazyCommandSpec("mvmctl.cli.host", "app", "Host configuration"),
    "network": _LazyCommandSpec("mvmctl.cli.network", "app", "Network management"),
    "key": _LazyCommandSpec("mvmctl.cli.key", "app", "SSH key management"),
    "config": _LazyCommandSpec("mvmctl.cli.config", "app", "Configuration commands"),
    "configure": _LazyCommandSpec("mvmctl.cli.configure", "app", "Guided setup wizard"),
    "kernel": _LazyCommandSpec("mvmctl.cli.asset", "kernel_app", "Kernel management"),
    "image": _LazyCommandSpec("mvmctl.cli.asset", "image_app", "Image management"),
    "bin": _LazyCommandSpec("mvmctl.cli.asset", "bin_app", "Binary management"),
}

_STATIC_COMMAND_HELP: dict[str, str] = {
    **{name: spec.help_text for name, spec in _COMMAND_SPECS.items()},
    "clear": "Clear cached assets",
    "version": "Show the version and exit",
    "help": "Show help for mvm or a subcommand",
}

_COMMAND_ORDER = [
    "vm",
    "host",
    "network",
    "key",
    "config",
    "configure",
    "kernel",
    "image",
    "bin",
    "clear",
    "version",
    "help",
]


def _version_callback(ctx: click.Context, _param: click.Parameter, value: bool) -> None:
    if not value or ctx.resilient_parsing:
        return
    click.echo(f"{CLI_NAME} {_get_version()}")
    ctx.exit()


def _configure_logging(*, verbose: bool, debug: bool) -> None:
    if debug:
        level = logging.DEBUG
    elif verbose:
        level = logging.INFO
    else:
        env_level = os.environ.get(env_var("LOG_LEVEL"), "WARNING").upper()
        level = getattr(logging, env_level, logging.WARNING)

    logging.basicConfig(
        level=level,
        format="%(levelname)s: %(name)s: %(message)s",
    )


def _warn_if_running_as_root() -> None:
    if os.getuid() != 0:
        return

    from mvmctl.utils.console import console

    console.print(
        "[yellow]Warning: running as root. Consider using the 'mvm' group instead "
        "(set up via 'sudo mvm host init').[/yellow]"
    )


def _reconcile_networks() -> None:
    try:
        from mvmctl.core.network_manager import reconcile_networks

        reconcile_networks()
    except Exception:
        pass


class LazyMVMGroup(click.Group):
    _add_completion: bool = False
    registered_callback: typer.models.TyperInfo | None = None
    registered_commands: list[typer.models.CommandInfo] | None = None
    registered_groups: list[typer.models.TyperInfo] | None = None

    def list_commands(self, ctx: click.Context) -> list[str]:
        return list(_COMMAND_ORDER)

    def get_command(self, ctx: click.Context, cmd_name: str) -> click.Command | None:
        if cmd_name == "clear":
            return clear_cmd
        if cmd_name == "version":
            return version_cmd
        if cmd_name == "help":
            return help_cmd

        spec = _COMMAND_SPECS.get(cmd_name)
        if spec is None:
            return None

        module = importlib.import_module(spec.module)
        command = getattr(module, spec.attribute)
        if isinstance(command, click.Command):
            return command

        from typer.main import get_command as get_typer_command

        return get_typer_command(command)

    def format_commands(self, ctx: click.Context, formatter: click.HelpFormatter) -> None:
        rows = [
            (command_name, _STATIC_COMMAND_HELP[command_name])
            for command_name in self.list_commands(ctx)
            if command_name in _STATIC_COMMAND_HELP
        ]
        if rows:
            with formatter.section("Commands"):
                formatter.write_dl(rows)


@click.group(
    cls=LazyMVMGroup,
    invoke_without_command=True,
    help="MicroVM Manager - Manage microVMs",
)
@click.option("--verbose", "verbose", is_flag=True, help="Enable verbose output")
@click.option("--debug", is_flag=True, help="Enable debug mode")
@click.option(
    "--version",
    "show_version",
    is_flag=True,
    is_eager=True,
    expose_value=False,
    callback=_version_callback,
    help="Show version and exit",
)
@click.pass_context
def app(ctx: click.Context, verbose: bool, debug: bool) -> None:
    if ctx.invoked_subcommand is None:
        click.echo(ctx.get_help())
        ctx.exit()

    if ctx.invoked_subcommand in {"help", "version"}:
        return

    _warn_if_running_as_root()
    _configure_logging(verbose=verbose, debug=debug)
    _reconcile_networks()


@click.command(name="version", help="Show the version and exit")
def version_cmd() -> None:
    click.echo(f"{CLI_NAME} {_get_version()}")


@click.command(name="help", help="Show help for mvm or a subcommand")
@click.argument("args", nargs=-1)
@click.pass_context
def help_cmd(ctx: click.Context, args: tuple[str, ...]) -> None:
    if not args:
        click.echo(ctx.find_root().get_help())
        ctx.exit()

    root = ctx.find_root()
    command: click.Command = root.command
    current_ctx = root
    command_path = [root.info_name or CLI_NAME]

    for arg in args:
        if not isinstance(command, click.MultiCommand):
            click.echo(f"'{arg}' has no subcommands", err=True)
            ctx.exit(1)

        subcommand = command.get_command(current_ctx, arg)
        if subcommand is None:
            click.echo(f"Unknown command: {' '.join(args)}", err=True)
            ctx.exit(1)

        command = subcommand
        command_path.append(arg)
        current_ctx = click.Context(command, info_name=arg, parent=current_ctx)

    click.echo(command.get_help(current_ctx))
    ctx.exit()


@click.command(name="clear", help="Clear cached assets")
@click.option("--force", "force", is_flag=True, help="Skip confirmation")
def clear_cmd(force: bool) -> None:
    from mvmctl.cli.asset import clear_assets

    clear_assets(force=force)


if __name__ == "__main__":
    app()
