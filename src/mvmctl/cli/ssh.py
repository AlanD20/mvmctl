"""VM SSH commands."""

from __future__ import annotations

from pathlib import Path
from typing import TYPE_CHECKING

import typer

from mvmctl.api import SSHInput as _SSHInput
from mvmctl.api import SSHOperation as _SSHOperation

if TYPE_CHECKING:
    from mvmctl.api.inputs._ssh_input import SSHInput
    from mvmctl.api.ssh_operations import SSHOperation
else:
    SSHOperation = _SSHOperation
    SSHInput = _SSHInput
from mvmctl.cli._completion import _complete_vm_names
from mvmctl.utils.cli import handle_errors, mvm_cli

ssh_app = typer.Typer(
    help="VM SSH access",
    no_args_is_help=True,
    add_completion=False,
    context_settings={"allow_interspersed_args": True},
)


@ssh_app.callback(invoke_without_command=True)
@handle_errors
def ssh_connect(
    ctx: typer.Context,
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC address",
        autocompletion=_complete_vm_names,
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
    timeout: int | None = typer.Option(
        None, "--timeout", "-t", help="SSH connection timeout in seconds"
    ),
) -> None:
    """
    Open an SSH session into a VM.

    Provide a VM identifier (name, ID prefix, IP, or MAC address) as the
    positional argument.
    """
    if ctx.invoked_subcommand is not None:
        return

    inputs = SSHInput(
        identifier=identifier,
        user=user,
        key=key,
        cmd=cmd,
        timeout=timeout,
    )
    result = SSHOperation.connect(inputs)
    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    raise typer.Exit(code=0)
