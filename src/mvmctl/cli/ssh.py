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


@ssh_app.command(name="ssh")
@handle_errors
def ssh_connect(
    vm_id: str = typer.Argument(None, help="VM name, ID prefix, or IP address"),
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
    name: str | None = typer.Option(
        None, "--name", "-n", help="VM name (validates as entity name)"
    ),
) -> None:
    """Open an SSH session into a VM."""
    inputs = SSHInput(
        vm_id=vm_id,
        user=user,
        key=key,
        cmd=cmd,
        ip=ip,
        name=name,
    )
    exit_code = SSHOperation.connect(inputs)
    raise typer.Exit(code=exit_code)
