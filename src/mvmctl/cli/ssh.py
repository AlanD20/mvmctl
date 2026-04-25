"""VM SSH commands."""

from __future__ import annotations

from pathlib import Path
from typing import Optional

import typer

from mvmctl.api.inputs._ssh_input import SSHInput
from mvmctl.api.ssh_operations import SSHOperation
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
    user: Optional[str] = typer.Option(
        None, "--user", "-u", help="SSH user (default: from user config)"
    ),
    key: Optional[Path] = typer.Option(
        None, "--key", help="SSH private key file or directory of keys"
    ),
    cmd: Optional[str] = typer.Option(
        None, "--cmd", "-c", help="Command to execute"
    ),
    ip: Optional[str] = typer.Option(
        None, "--ip", help="IP address to connect to (skips all validation)"
    ),
    name: Optional[str] = typer.Option(
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
