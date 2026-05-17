#!/usr/bin/env python3
"""CLI - Main entry point."""

from __future__ import annotations

import importlib
import os
import signal
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

import click

from mvmctl.utils._system import run_cmd


def _setup_signal_handlers() -> None:
    """
    Install graceful signal handlers for programmatic CLI use.

    SIGTERM is sent by process managers (systemd, docker stop, etc.).
    We convert it to a clean sys.exit(143) (128 + 15).
    """

    def _handle_sigterm(signum: int, _frame: object) -> None:
        sys.exit(128 + signum)

    signal.signal(signal.SIGTERM, _handle_sigterm)


_setup_signal_handlers()

if TYPE_CHECKING:
    import importlib.metadata

    import typer
    import typer.models

# Lazy imports from constants to avoid heavy import-time work
# (package metadata resolution)


def _get_bootstrap_name() -> str:
    """Get bootstrap name lazily to avoid import-time overhead."""
    from mvmctl.constants import _BOOTSTRAP_NAME

    return _BOOTSTRAP_NAME


def _get_cli_name() -> str:
    """Get CLI name lazily to avoid import-time overhead."""
    from mvmctl.constants import CLI_NAME

    return str(CLI_NAME)


def _get_env_var(suffix: str) -> str:
    """Get env var name lazily to avoid import-time overhead."""
    from mvmctl.constants import env_var

    return env_var(suffix)


def _get_git_version_info() -> str | None:
    """
    Get git version info if running from source.

    Returns:
        - Tag name if current commit is tagged
        - Short commit hash prefixed with 'git+' if not tagged
        - None if not in a git repo or git not available

    """
    try:
        # Get the directory containing this file (src/mvmctl/)
        repo_dir = Path(__file__).parent.parent.parent
        git_dir = repo_dir / ".git"
        if not git_dir.exists():
            return None

        # Check if current commit has a tag
        result = run_cmd(
            [
                "git",
                "-C",
                str(repo_dir),
                "describe",
                "--tags",
                "--exact-match",
                "HEAD",
            ],
            check=False,
        )
        if result.returncode == 0:
            return result.stdout.strip()  # Return the tag

        # No tag, get short commit hash
        result = run_cmd(
            ["git", "-C", str(repo_dir), "rev-parse", "--short", "HEAD"],
            check=False,
        )
        if result.returncode == 0:
            return f"git+{result.stdout.strip()}"

        return None
    except Exception:
        return None


def _get_version() -> str:
    bootstrap_name = _get_bootstrap_name()
    try:
        import importlib.metadata as _meta

        version = _meta.version(bootstrap_name)
    except Exception:
        from mvmctl import __version__

        version = __version__

    # Add git info if available
    git_info = _get_git_version_info()
    if git_info:
        if git_info.startswith("git+"):
            version = f"{version}+{git_info}"
        else:
            # It's a tag, use tag as version
            version = git_info

    return version


@dataclass(frozen=True)
class _LazyCommandSpec:
    module: str
    attribute: str
    help_text: str


_COMMAND_SPECS: dict[str, _LazyCommandSpec] = {
    "vm": _LazyCommandSpec(
        "mvmctl.cli.vm", "vm_app", "VM lifecycle management"
    ),
    "console": _LazyCommandSpec(
        "mvmctl.cli.console", "console_app", "VM console access"
    ),
    "host": _LazyCommandSpec(
        "mvmctl.cli.host", "host_app", "Host configuration"
    ),
    "network": _LazyCommandSpec(
        "mvmctl.cli.network", "network_app", "Network management"
    ),
    "key": _LazyCommandSpec("mvmctl.cli.key", "key_app", "SSH key management"),
    "config": _LazyCommandSpec(
        "mvmctl.cli.config", "config_app", "Configuration commands"
    ),
    "init": _LazyCommandSpec(
        "mvmctl.cli.init", "init_app", f"Initialize {_get_cli_name()}"
    ),
    "kernel": _LazyCommandSpec(
        "mvmctl.cli.kernel", "kernel_app", "Kernel management"
    ),
    "image": _LazyCommandSpec(
        "mvmctl.cli.image", "image_app", "Image management"
    ),
    "bin": _LazyCommandSpec("mvmctl.cli.bin", "bin_app", "Binary management"),
    "cp": _LazyCommandSpec(
        "mvmctl.cli.cp", "cp_app", "Copy files between host and microVMs"
    ),
    "cache": _LazyCommandSpec(
        "mvmctl.cli.cache", "cache_app", "Cache management"
    ),
    "logs": _LazyCommandSpec(
        "mvmctl.cli.logs", "logs_app", "VM log management"
    ),
    "ssh": _LazyCommandSpec("mvmctl.cli.ssh", "ssh_app", "VM SSH access"),
    "volume": _LazyCommandSpec(
        "mvmctl.cli.volume", "volume_app", "Volume management"
    ),
}

_STATIC_COMMAND_HELP: dict[str, str] = {
    **{name: spec.help_text for name, spec in _COMMAND_SPECS.items()},
    "version": "Show the version and exit",
    "completion": "Print shell completion script",
    "help": f"Show help for {_get_cli_name()} or a subcommand",
}

_COMMAND_ORDER = [
    "init",
    "bin",
    "cp",
    "kernel",
    "image",
    "network",
    "vm",
    "volume",
    "key",
    "ssh",
    "console",
    "logs",
    "host",
    "config",
    "cache",
    "version",
    "completion",
    "help",
]


def _version_callback(
    ctx: click.Context, _param: click.Parameter, value: bool
) -> None:
    if not value or ctx.resilient_parsing:
        return
    click.echo(f"{_get_cli_name()} {_get_version()}")
    ctx.exit()


def _warn_if_running_as_root() -> None:
    if os.getuid() != 0:
        return
    # Suppress when configure already prompted the user and escalated on their
    # behalf — they accepted, so the warning is noise.
    if os.environ.get(_get_env_var("ESCALATED")):
        return

    from mvmctl.utils._io import print_warning

    print_warning(
        f"Warning: running as root. Consider using the '{_get_cli_name()}' group instead "
        f"(set up via 'sudo {_get_cli_name()} host init')."
    )


class LazyMVMGroup(click.Group):
    _add_completion: bool = False
    registered_callback: typer.models.TyperInfo | None = None
    registered_commands: list[typer.models.CommandInfo] | None = None
    registered_groups: list[typer.models.TyperInfo] | None = None

    def list_commands(self, ctx: click.Context) -> list[str]:
        return list(_COMMAND_ORDER)

    def get_command(
        self, ctx: click.Context, cmd_name: str
    ) -> click.Command | None:
        if cmd_name == "version":
            return version_cmd
        if cmd_name == "completion":
            return completion_cmd
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

    def format_commands(
        self, ctx: click.Context, formatter: click.HelpFormatter
    ) -> None:
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
    help="MicroVM Manager - Container speed, VM Isolation",
)
@click.option(
    "--verbose", "verbose", is_flag=True, help="Enable verbose output"
)
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
    from mvmctl.utils.common import set_debug_mode

    ctx.obj = {"debug": debug}
    set_debug_mode(debug)

    if ctx.invoked_subcommand is None:
        click.echo(ctx.get_help())
        ctx.exit()

    if ctx.invoked_subcommand in {"help", "version", "init"}:
        return

    _warn_if_running_as_root()
    from mvmctl.utils._io import setup_logging

    setup_logging(verbose=verbose, debug=debug)


@click.command(name="completion", help="Print shell completion script")
@click.argument("shell", type=click.Choice(["bash", "zsh", "fish"]))
@click.pass_context
def completion_cmd(ctx: click.Context, shell: str) -> None:
    f"""Print shell completion script for {_get_cli_name()}.

    Install completion by adding the output to your shell config:

        eval "$({_get_cli_name()} completion bash)"
    """
    from click.shell_completion import BashComplete, FishComplete, ZshComplete

    root = ctx.find_root()
    complete_cls = {
        "bash": BashComplete,
        "zsh": ZshComplete,
        "fish": FishComplete,
    }[shell]
    complete = complete_cls(
        root.command, {}, _get_cli_name(), _get_env_var("COMPLETE")
    )
    click.echo(complete.source())


@click.command(name="version", help="Show the version and exit")
def version_cmd() -> None:
    version = _get_version()
    git_info = _get_git_version_info()

    click.echo(f"{_get_cli_name()} {version}")

    if git_info:
        if git_info.startswith("git+"):
            click.echo(f"  built from: {git_info[4:]}")
        else:
            click.echo(f"  tagged: {git_info}")


@click.command(
    name="help", help=f"Show help for {_get_cli_name()} or a subcommand"
)
@click.argument("args", nargs=-1)
@click.pass_context
def help_cmd(ctx: click.Context, args: tuple[str, ...]) -> None:
    if not args:
        click.echo(ctx.find_root().get_help())
        ctx.exit()

    root = ctx.find_root()
    command: click.Command = root.command
    current_ctx = root
    command_path = [root.info_name or _get_cli_name()]

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


if __name__ == "__main__":
    app()
