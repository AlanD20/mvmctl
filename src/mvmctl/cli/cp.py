"""CLI — ``mvm cp`` command — copy files between host and microVMs."""

from __future__ import annotations

from typing import TYPE_CHECKING

import typer
from rich.progress import (
    BarColumn,
    Progress,
    SpinnerColumn,
    TextColumn,
    TransferSpeedColumn,
)

from mvmctl.utils.cli import handle_errors, mvm_cli

if TYPE_CHECKING:
    from mvmctl.api.cp_operations import CPOperation
    from mvmctl.api.inputs._cp_input import CPInput
else:
    from mvmctl.api import CPInput, CPOperation

cp_app = typer.Typer(
    name="cp",
    help="Copy files between host and microVMs",
    no_args_is_help=True,
    add_completion=False,
)


@cp_app.callback(invoke_without_command=True)
@handle_errors
def cp(
    ctx: typer.Context,
    args: list[str] = typer.Argument(
        ...,
        help="Source path(s) and destination. "
        "Use vm_name:/path for VM paths. "
        "Multiple sources allowed for host→VM copies. "
        "The last argument is always the destination.",
    ),
    user: str | None = typer.Option(
        None,
        "--user",
        "-u",
        help="SSH user for VM connections",
    ),
    key: str | None = typer.Option(
        None,
        "--key",
        help="SSH private key path or name",
    ),
    force: bool = typer.Option(
        False,
        "--force",
        "-f",
        help="Overwrite existing destination files",
    ),
) -> None:
    """Copy files between host and microVMs using tar-over-SSH.

    Uses ``tar`` on both sides — no guest dependencies beyond POSIX-mandated tar.

    Usage:

        # Copy local files to VM
        mvm cp ./myfile.txt my-vm:/root/

        # Copy multiple local files to VM
        mvm cp file1.txt file2.txt file3.txt my-vm:/dst/
        mvm cp *.txt my-vm:/dst/         # Shell glob expands to multiple files

        # Copy file from VM to local
        mvm cp my-vm:/var/log/syslog ./syslog

        # Copy between VMs
        mvm cp vm1:/data/file.txt vm2:/data/

    Path format: use ``vm_name:/remote/path`` for VM paths,
    plain ``/local/path`` for local paths.

    The last positional argument is always the destination. Everything
    before it is a source. Multiple sources only work for host → VM.

    """
    if len(args) < 2:
        mvm_cli.error(
            "At least two arguments required: one or more sources and a destination"
        )
        raise typer.Exit(code=1)

    *sources, dst = args

    inputs = CPInput(sources=sources, dst=dst, user=user, key=key, force=force)

    progress = Progress(
        SpinnerColumn(),
        TextColumn("[progress.description]{task.description}"),
        BarColumn(),
        TransferSpeedColumn(),
        transient=True,
    )

    with progress:
        task = progress.add_task("Copying...", total=None)

        def on_progress(chunk: int) -> None:
            if progress.tasks[0].total is None:
                progress.update(task, total=chunk)
            progress.update(task, advance=chunk)

        result = CPOperation.copy(inputs, on_progress=on_progress)

    if result.is_ok and result.item:
        msg = result.item.get("message", result.message)
        mvm_cli.success(msg)
        raise typer.Exit()
    elif result.is_ok:
        mvm_cli.success(result.message)
        raise typer.Exit()
    else:
        mvm_cli.error(result.message or "Copy failed")
        raise typer.Exit(code=1)
