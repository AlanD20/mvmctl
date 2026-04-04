"""VM log management CLI command."""

from __future__ import annotations

import typer

from mvmctl.api.vms import get_logs
from mvmctl.constants import (
    DEFAULT_VM_LOG_FOLLOW,
    DEFAULT_VM_LOG_LINES,
    DEFAULT_VM_LOG_TYPE,
)
from mvmctl.exceptions import MVMError
from mvmctl.utils.error_handler import handle_mvm_error
from mvmctl.utils.validation import validate_entity_name

app = typer.Typer(
    help="VM log management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@app.command()
def logs(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    follow: bool = typer.Option(None, "--follow", "-f", help="Follow log output"),
    lines: int = typer.Option(None, "--lines", help="Number of lines to show"),
    log_type: str = typer.Option(
        None,
        "--type",
        help="Log type: boot (serial console) or os (firecracker process log)",
    ),
) -> None:
    """View VM logs.

    Use --type boot for serial console output (what you see during boot).
    Use --type os for the Firecracker process log (hypervisor events).
    """
    try:
        validate_entity_name(name, "VM")
        effective_follow = follow if follow is not None else DEFAULT_VM_LOG_FOLLOW
        effective_lines = lines if lines is not None else DEFAULT_VM_LOG_LINES
        effective_log_type = log_type if log_type is not None else DEFAULT_VM_LOG_TYPE
        log_lines = get_logs(
            name=name, log_type=effective_log_type, lines=effective_lines, follow=effective_follow
        )
        for line in log_lines:
            print(line, end="" if line.endswith("\n") else "\n")
        raise typer.Exit(code=0)
    except MVMError as e:
        handle_mvm_error(e)
