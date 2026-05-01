"""VM SSH commands."""

from __future__ import annotations

from pathlib import Path

import typer

from mvmctl.api import SSHInput, SSHOperation
from mvmctl.utils.cli import handle_errors

ssh_app = typer.Typer(
    help="VM SSH access",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@ssh_app.callback(invoke_without_command=True)
@handle_errors
def ssh_connect(
    ctx: typer.Context,
    identifier: str = typer.Argument(
        None, help="VM name, ID prefix, IP, or MAC address"
    ),
    user: str | None = typer.Option(
        None, "--user", "-u", help="SSH user (default: from user config)"
    ),
    key: Path | None = typer.Option(
        None, "--key", help="SSH private key file or directory of keys"
    ),
    cmd: str | None = typer.Option(
        None, "--cmd", "-c", help="Command to execute"
    ),
    ip: str | None = typer.Option(
        None, "--ip", help="IP address to connect to (skips all validation)"
    ),
    mac: str | None = typer.Option(None, "--mac", help="VM MAC address"),
    name: str | None = typer.Option(
        None, "--name", "-n", help="VM name (validates as entity name)"
    ),
) -> None:
    """
    Open an SSH session into a VM.

    Provide a VM identifier as a positional argument, or use
    --name, --ip, or --mac to specify the VM explicitly.
    """
    if ctx.invoked_subcommand is not None:
        return

    inputs = SSHInput(
        vm_id=identifier,
        user=user,
        key=key,
        cmd=cmd,
        ip=ip,
        mac=mac,
        name=name,
    )
    exit_code = SSHOperation.connect(inputs)
    raise typer.Exit(code=exit_code)
