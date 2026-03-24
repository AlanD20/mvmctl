import json
from pathlib import Path
from typing import Optional

import typer
from rich.table import Table

from fcm.api.vms import (
    list_vms,
    get_vm,
    create_vm,
    remove_vm,
    snapshot_vm,
    load_snapshot,
    ssh_vm,
    get_logs,
    cleanup_vms,
)
from fcm.constants import DEFAULT_NETWORK_NAME
from fcm.exceptions import FCMError
from fcm.models.vm import VMInstance, VMState
from fcm.utils.console import console, print_error, print_info, print_success
from fcm.utils.validation import is_ip_address, validate_entity_name

app = typer.Typer(help="VM lifecycle management", no_args_is_help=True)


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the vm command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


def _resolve_default_image() -> str | None:
    try:
        from fcm.core.config_state import get_config_value

        val = get_config_value("default_image")
        return str(val) if val is not None else None
    except Exception:
        return None


def _resolve_default_kernel() -> str | None:
    try:
        from fcm.core.kernel import get_default_kernel_path
        from fcm.utils.fs import get_kernels_dir

        path = get_default_kernel_path(get_kernels_dir())
        return str(path) if path else None
    except Exception:
        return None


def _resolve_active_firecracker_bin() -> str:
    try:
        from fcm.core.config_state import get_firecracker_config

        stored = get_firecracker_config().get("active_binary_path")
        if stored is not None and Path(str(stored)).exists():
            return str(stored)
        from fcm.core.binary_manager import list_local_versions

        local = list_local_versions()
        active = next((b for b in local if b.is_active), None)
        if active:
            return str(active.firecracker_path)
    except Exception:
        pass
    return "firecracker"


@app.command()
def create(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    image: Optional[str] = typer.Option(
        None,
        "--image",
        help="Image ID or path to rootfs file with single root partition (required if no default image)",
    ),
    kernel: Optional[str] = typer.Option(
        None,
        "--kernel",
        help="Path to vmlinux kernel (default: active default kernel or FCM_KERNEL env var)",
    ),
    vcpus: int = typer.Option(2, "--vcpus", "--cpus", help="Number of vCPUs"),
    mem: int = typer.Option(2048, "--mem", "--memory", help="Memory in MiB"),
    ip: Optional[str] = typer.Option(None, "--ip", help="Guest IP (auto-assigned if omitted)"),
    network_name: str = typer.Option(
        DEFAULT_NETWORK_NAME, "--network", "--net", help="Named network to attach to"
    ),
    mac: Optional[str] = typer.Option(
        None, "--mac", help="Custom MAC address (auto-generated if omitted)"
    ),
    ssh_key: Optional[str] = typer.Option(
        None, "--ssh-key", help="SSH public key name (from key cache) or file path"
    ),
    user_data: Optional[Path] = typer.Option(
        None, "--user-data", help="Path to custom cloud-init user-data file"
    ),
    user: str = typer.Option("root", "--user", help="Default SSH user for cloud-init"),
    enable_api_socket: bool = typer.Option(
        False, "--enable-api-socket", help="Enable Firecracker HTTP API socket"
    ),
    enable_pci: bool = typer.Option(False, "--enable-pci", help="Enable PCI device support"),
    firecracker_bin: Optional[str] = typer.Option(
        None,
        "--firecracker-bin",
        envvar="FCM_FIRECRACKER_BIN",
        help="Path to firecracker binary (default: active version from fcm bin use)",
    ),
) -> None:
    """Create and start a new Firecracker VM.

    Examples:
        # Create a VM with defaults:
        fcm vm create --name myvm --image ubuntu-24.04

        # Create with custom resources and SSH key:
        fcm vm create --name myvm --image ubuntu-24.04 --vcpus 4 --mem 4096 --ssh-key mykey

        # Create with static IP:
        fcm vm create --name myvm --image ubuntu-24.04 --ip 10.20.0.10

        # Create with API socket for snapshot support:
        fcm vm create --name myvm --image ubuntu-24.04 --enable-api-socket
    """
    if image is None:
        image = _resolve_default_image()
        if image is None:
            print_error(
                "No --image specified and no default image set. "
                "Use 'fcm image fetch <name>' then 'fcm image set-default <name>', or pass --image."
            )
            raise typer.Exit(code=1)

    if kernel is None:
        kernel = _resolve_default_kernel()

    effective_bin = firecracker_bin or _resolve_active_firecracker_bin()

    try:
        vm = create_vm(
            name=name,
            image=image,
            kernel=kernel,
            vcpus=vcpus,
            mem=mem,
            ip=ip,
            network_name=network_name,
            mac=mac,
            ssh_key=ssh_key,
            user_data=user_data,
            user=user,
            enable_api_socket=enable_api_socket,
            enable_pci=enable_pci,
            firecracker_bin=effective_bin,
        )
        from fcm.utils.audit import log_audit

        log_audit("vm.create", f"name={name}")
        print_success(f"VM '{name}' started (PID {vm.pid})")
        print_info(f"  SSH ready in ~30-60s: fcm vm ssh --name {name}")
        print_info(f"  Logs: fcm vm logs --name {name} --type os --follow")
    except FCMError as e:
        print_error(str(e))
        raise typer.Exit(code=1)


@app.command(name="rm")
def rm(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Stop and remove a VM."""
    try:
        vm = get_vm(name)
        if not vm:
            print_error(f"VM '{name}' not found")
            raise typer.Exit(code=1)

        if not force:
            typer.confirm(f"Remove VM '{name}' (IP: {vm.ip})?", abort=True)

        remove_vm(name)
        from fcm.utils.audit import log_audit

        log_audit("vm.remove", f"name={name}")
        print_success(f"VM '{name}' removed")
    except FCMError as e:
        print_error(str(e))
        raise typer.Exit(code=1)


@app.command(name="ls")
def ls_vms(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    all_vms: bool = typer.Option(False, "--all", "-a", help="Show all VMs including stopped"),
) -> None:
    """List running and stopped VMs."""
    vms = list_vms(include_stopped=all_vms)

    if json_output:
        data = [
            {
                "name": v.name,
                "ip": v.ip,
                "mac": v.mac,
                "status": v.status.value,
                "pid": v.pid,
                "api_socket": v.socket_path is not None,
                "network": v.network_name or "-",
                "created_at": v.created_at.isoformat(),
            }
            for v in vms
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    if not vms:
        print_info("No VMs found." + (" Use --all to include stopped VMs." if not all_vms else ""))
        return

    console.print(_build_vm_table(vms))


def _build_vm_table(vms: list[VMInstance]) -> Table:
    """Build a Rich Table displaying VM information."""
    table = Table(title="Firecracker VMs")
    table.add_column("Name", style="cyan", no_wrap=True)
    table.add_column("IP", style="green")
    table.add_column("Status", style="bold")
    table.add_column("PID")
    table.add_column("API", no_wrap=True)
    table.add_column("Created")

    status_colors = {
        VMState.RUNNING: "[green]running[/green]",
        VMState.STOPPED: "[dim]stopped[/dim]",
        VMState.ERROR: "[red]error[/red]",
    }

    for v in vms:
        status_str = status_colors.get(v.status, v.status.value)
        created = v.created_at.strftime("%Y-%m-%d %H:%M") if v.created_at else "-"
        api_str = "[green]on[/green]" if v.socket_path else "[dim]off[/dim]"
        table.add_row(
            v.name,
            v.ip or "-",
            status_str,
            str(v.pid) if v.pid else "-",
            api_str,
            created,
        )

    return table


@app.command(name="ps")
def ps_vms(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    all_vms: bool = typer.Option(False, "--all", "-a", help="Show all VMs including stopped"),
) -> None:
    """List running VMs (alias for ls)."""
    ls_vms(json_output=json_output, all_vms=all_vms)


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
            raise FCMError(f"No SSH key found at: {key}")
        return resolved
    fcm_keys_dir = Path.home() / ".cache" / "firecracker-manager" / "keys"
    if fcm_keys_dir.exists():
        for f in sorted(fcm_keys_dir.iterdir()):
            if f.is_file() and f.suffix != ".pub" and not f.name.startswith("."):
                return f
    ssh_dir = Path.home() / ".ssh"
    if ssh_dir.exists():
        for f in sorted(ssh_dir.iterdir()):
            if (
                f.is_file()
                and not f.name.endswith(".pub")
                and not f.name.startswith(".")
                and f.name not in ("known_hosts", "config", "authorized_keys")
            ):
                return f
    return None


@app.command()
def ssh(
    name: str = typer.Option(..., "--name", "-n", help="VM name or IP address"),
    user: str = typer.Option("root", "--user", "-u", help="SSH user"),
    key: Optional[Path] = typer.Option(
        None, "--key", help="SSH private key file or directory of keys"
    ),
    cmd: Optional[str] = typer.Option(None, "--cmd", "-c", help="Command to execute"),
) -> None:
    """Open an SSH session into a VM."""
    try:
        if not is_ip_address(name):
            validate_entity_name(name, "VM")
        resolved_key = _resolve_ssh_key_for_vm(key)
        exit_code = ssh_vm(name=name, user=user, key=resolved_key, cmd=cmd)
        raise typer.Exit(code=exit_code)
    except FCMError as e:
        print_error(str(e))
        raise typer.Exit(code=1)


@app.command()
def logs(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    follow: bool = typer.Option(False, "--follow", "-f", help="Follow log output"),
    lines: int = typer.Option(50, "--lines", help="Number of lines to show"),
    log_type: str = typer.Option(
        "os", "--type", help="Log type: boot (serial console) or os (firecracker process log)"
    ),
) -> None:
    """View VM logs.

    Use --type boot for serial console output (what you see during boot).
    Use --type os for the Firecracker process log (hypervisor events).
    """
    try:
        from fcm.utils.validation import validate_entity_name

        validate_entity_name(name, "VM")
        log_lines = get_logs(name=name, log_type=log_type, lines=lines, follow=follow)
        for line in log_lines:
            print(line, end="" if line.endswith("\n") else "\n")
        raise typer.Exit(code=0)
    except FCMError as e:
        print_error(str(e))
        raise typer.Exit(code=1)


def _do_prune(all_vms: bool, dry_run: bool, force: bool) -> None:
    manager = list_vms(include_stopped=True)
    targets = manager if all_vms else [v for v in manager if v.status != VMState.RUNNING]

    if not targets:
        print_info("Nothing to clean up.")
        return

    print_info(f"VMs to remove ({len(targets)}):")
    for v in targets:
        print_info(f"  {v.name} ({v.status.value}, IP: {v.ip or '-'})")

    if dry_run:
        print_info("Dry run — no changes made.")
        return

    if not force:
        typer.confirm(f"Remove {len(targets)} VM(s)?", abort=True)

    cleanup_vms(all_vms=all_vms, dry_run=False)
    for v in targets:
        print_success(f"Removed VM '{v.name}'")


@app.command()
def prune(
    all_vms: bool = typer.Option(False, "--all", help="Remove all VMs, not just stopped"),
    dry_run: bool = typer.Option(
        False, "--dry-run", help="Show what would be removed without deleting"
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove stopped VMs and stale directories."""
    _do_prune(all_vms=all_vms, dry_run=dry_run, force=force)


@app.command()
def snapshot(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    mem_out: Path = typer.Option(..., "--mem-out", help="Memory snapshot output path"),
    state_out: Path = typer.Option(..., "--state-out", help="VM state output path"),
) -> None:
    """Snapshot VM memory and disk state. Requires --enable-api-socket."""
    from fcm.utils.validation import validate_entity_name

    validate_entity_name(name, "VM")
    try:
        snapshot_vm(name=name, mem_out=mem_out, state_out=state_out)
        raise typer.Exit(code=0)
    except FCMError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)


@app.command()
def load(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    mem_in: Path = typer.Option(..., "--mem-in", help="Memory snapshot input path"),
    state_in: Path = typer.Option(..., "--state-in", help="VM state input path"),
    resume_after: bool = typer.Option(True, "--resume/--no-resume", help="Resume VM after loading"),
) -> None:
    """Load VM from snapshot. Requires --enable-api-socket."""
    from fcm.utils.validation import validate_entity_name

    validate_entity_name(name, "VM")
    try:
        load_snapshot(name=name, mem_in=mem_in, state_in=state_in, resume_after=resume_after)
        raise typer.Exit(code=0)
    except FCMError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)


@app.command(name="pause", hidden=True)
def pause(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
) -> None:
    """Pause a VM (not supported in this version)."""
    print_info("'vm pause' is not supported by Firecracker. Use 'vm snapshot' instead.")
    raise typer.Exit(code=0)


@app.command(name="resume", hidden=True)
def resume(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
) -> None:
    """Resume a paused VM (not supported in this version)."""
    print_info("'vm resume' is not supported by Firecracker. Use 'vm load' instead.")
    raise typer.Exit(code=0)
