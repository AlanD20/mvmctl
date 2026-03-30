"""VM SSH commands."""

from __future__ import annotations

from pathlib import Path
from typing import TYPE_CHECKING, Optional

import typer

from mvmctl.api.vms import ssh_vm
from mvmctl.exceptions import MVMError
from mvmctl.utils.error_handler import handle_mvm_error
from mvmctl.utils.validation import is_ip_address, validate_entity_name

if TYPE_CHECKING:
    from mvmctl.core.config import VMDefaultsConfig

app = typer.Typer(
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
    from mvmctl.utils.fs import get_keys_dir

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


def _get_vm_defaults() -> "VMDefaultsConfig":
    from mvmctl.api.config import load_config
    from mvmctl.utils.fs import get_assets_dir

    return load_config(get_assets_dir()).vm_defaults


@app.command()
def ssh(
    vm_id: str = typer.Argument(None, help="VM name, short ID, or IP address"),
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
        if ip is not None:
            target = ip
        elif name is not None:
            validate_entity_name(name, "VM")
            target = name
        elif vm_id is not None:
            target = vm_id
            if not is_ip_address(target):
                from mvmctl.core.vm_manager import VMManager
                from mvmctl.utils.fs import get_vms_dir

                manager = VMManager(get_vms_dir())
                matches = manager.find_by_short_id(target)
                if len(matches) == 1:
                    target = matches[0].name
                elif len(matches) > 1:
                    raise MVMError(f"Ambiguous short ID '{target}' matches {len(matches)} VMs")
                else:
                    validate_entity_name(target, "VM")
        else:
            raise MVMError("Provide either a VM identifier, --name, or --ip")

        resolved_key = _resolve_ssh_key_for_vm(key)
        effective_user = user if user is not None else _get_vm_defaults().ssh_user
        exit_code = ssh_vm(name=target, user=effective_user, key=resolved_key, cmd=cmd)
        raise typer.Exit(code=exit_code)
    except MVMError as e:
        handle_mvm_error(e)
