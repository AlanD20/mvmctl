"""VM log viewing commands."""

from __future__ import annotations

import typer

from mvmctl.api.inputs._logs_input import LogInput
from mvmctl.api.logs_operations import LogOperation
from mvmctl.utils.cli import handle_errors

logs_app = typer.Typer(
    help="VM log management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
    context_settings={"allow_interspersed_args": True},
)


@logs_app.callback(invoke_without_command=True)
@handle_errors
def logs(
    ctx: typer.Context,
    identifier: str = typer.Argument(
        ..., help="VM name, ID, IP, or MAC address"
    ),
    os_log: bool = typer.Option(
        False, "--os", help="Show Firecracker OS log instead of boot log"
    ),
    lines: int | None = typer.Option(
        None, "--lines", "-n", help="Number of log lines to show"
    ),
    follow: bool | None = typer.Option(
        None, "--follow", "-f", help="Follow log output in real-time"
    ),
) -> None:
    """
    View VM logs.

    Provide a VM identifier as a positional argument.

    By default shows the boot log (serial console output).
    Use --os to show the Firecracker process log.
    """
    if ctx.invoked_subcommand is not None:
        return

    inputs = LogInput(
        identifier=identifier,
        os_log=os_log,
        lines=lines,
        follow=follow,
    )
    for line in LogOperation.stream(inputs):
        print(line)
