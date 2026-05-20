"""CLI — ``mvm cp`` command — copy files between host and microVMs.

Uses a plain Click command (not a Typer app) because ``mvm cp`` has
variable-length positional arguments (1+ sources + 1 destination) that
don't map cleanly to Typer's subcommand model.  Click's ``nargs=-1``
handles this naturally, and its option parser correctly separates flags
like ``--user`` / ``--key`` / ``--force`` from positional paths.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import click
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


@click.command(name="cp")
@click.argument("args", nargs=-1, required=True)
@click.option("--user", "-u", default=None, help="SSH user for VM connections")
@click.option("--key", default=None, help="SSH private key path or name")
@click.option(
    "--force", "-f", is_flag=True, help="Overwrite existing destination files"
)
@handle_errors
def cp_app(
    args: tuple[str, ...],
    user: str | None,
    key: str | None,
    force: bool,
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
    args_list = list(args)

    if len(args_list) < 2:
        mvm_cli.error(
            "At least two arguments required: one or more sources and a destination"
        )
        raise click.Abort()

    *sources, dst = args_list

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
        return
    elif result.is_ok:
        mvm_cli.success(result.message)
        return
    else:
        mvm_cli.error(result.message or "Copy failed")
        raise SystemExit(1)
