"""VM SSH commands."""

from __future__ import annotations

from pathlib import Path
from typing import Optional

import typer

from mvmctl.api.vms import ssh_vm
from mvmctl.cli._helpers import get_vm_defaults, resolve_ssh_target
from mvmctl.exceptions import MVMError
from mvmctl.utils.error_handler import handle_mvm_error
from mvmctl.utils.fs import get_keys_dir

ssh_app = typer.Typer(
    help="VM SSH access",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


def _find_ssh_key_from_path(key_path: Path) -> Path | None:
    if key_path.is_file():
        return key_path
    if key_path.is_dir():
        for candidate in sorted(key_path.iterdir()):
            if (
                candidate.is_file()
                and candidate.suffix != ".pub"
                and not candidate.name.startswith(".")
            ):
                return candidate
    return None


def _resolve_ssh_key_for_vm(key: Path | None) -> Path | None:
    if key is not None:
        resolved = _find_ssh_key_from_path(key)
        if resolved is None:
            raise MVMError(f"No SSH key found at: {key}")
        return resolved
    mvm_keys_dir = get_keys_dir()
    if mvm_keys_dir.exists():
        for f in sorted(mvm_keys_dir.iterdir()):
            if f.is_file() and f.suffix not in (".pub", ".json") and not f.name.startswith("."):
                return f
    ssh_dir = Path.home() / ".ssh"
    if ssh_dir.exists():
        for f in sorted(ssh_dir.iterdir()):
            if (
                f.is_file()
                and not f.name.endswith((".pub", ".json"))
                and not f.name.startswith(".")
                and f.name not in ("known_hosts", "config", "authorized_keys")
            ):
                return f
    return None


@ssh_app.command(name="ssh")
def ssh_connect(
    vm_id: str = typer.Argument(None, help="VM name, ID prefix, or IP address"),
    user: Optional[str] = typer.Option(
        None, "--user", "-u", help="SSH user (default: from user config)"
    ),
    key: Optional[Path] = typer.Option(
        None, "--key", help="SSH private key file or directory of keys"
    ),
    cmd: Optional[str] = typer.Option(None, "--cmd", "-c", help="Command to execute"),
    ip: Optional[str] = typer.Option(
        None, "--ip", help="IP address to connect to (skips all validation)"
    ),
    name: Optional[str] = typer.Option(
        None, "--name", "-n", help="VM name (validates as entity name)"
    ),
) -> None:
    """Open an SSH session into a VM."""
    try:
        target = resolve_ssh_target(vm_id, name, ip)
        resolved_key = _resolve_ssh_key_for_vm(key)
        effective_user = user if user is not None else get_vm_defaults().ssh_user
        exit_code = ssh_vm(name=target, user=effective_user, key=resolved_key, cmd=cmd)
        raise typer.Exit(code=exit_code)
    except MVMError as e:
        handle_mvm_error(e)
