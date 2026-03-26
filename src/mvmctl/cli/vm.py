import json
from pathlib import Path
from typing import TYPE_CHECKING, List, Optional

import typer

if TYPE_CHECKING:
    from mvmctl.core.config import VMDefaultsConfig

from mvmctl.api.vm_config import build_vm_config_file, load_vm_config_file, merge_cli_overrides
from mvmctl.api.vms import (
    cleanup_vms,
    create_vm,
    get_logs,
    list_vms,
    load_snapshot,
    remove_vm,
    snapshot_vm,
    ssh_vm,
)
from mvmctl.constants import (
    DEFAULT_FIRECRACKER_BIN,
    DEFAULT_NETWORK_NAME,
    DEFAULT_SNAPSHOT_RESUME,
    DEFAULT_VM_LOG_FOLLOW,
    DEFAULT_VM_LOG_LINES,
    DEFAULT_VM_LOG_TYPE,
)
from mvmctl.exceptions import MVMError
from mvmctl.models.vm import VMInstance, VMState
from mvmctl.utils.console import print_error, print_info, print_success, print_table
from mvmctl.utils.validation import is_ip_address, validate_entity_name

app = typer.Typer(
    help="VM lifecycle management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the vm command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


def _resolve_default_image() -> str | None:
    try:
        from mvmctl.api.metadata import get_default_image_entry
        from mvmctl.utils.fs import get_cache_dir

        default_entry = get_default_image_entry(get_cache_dir())
        if default_entry is None:
            return None
        image_id, meta = default_entry
        internal_id = meta.get("internal_id")
        if isinstance(internal_id, str) and internal_id:
            return internal_id
        return image_id
    except Exception:
        return None


def _resolve_default_kernel() -> str | None:
    try:
        from mvmctl.api.assets import get_default_kernel_path
        from mvmctl.utils.fs import get_kernels_dir

        path = get_default_kernel_path(get_kernels_dir())
        return str(path) if path else None
    except Exception:
        return None


def _get_vm_defaults() -> "VMDefaultsConfig":
    from mvmctl.api.config import load_config
    from mvmctl.utils.fs import get_assets_dir

    return load_config(get_assets_dir()).vm_defaults


def _resolve_active_firecracker_bin() -> str:
    try:
        from mvmctl.api.config import get_firecracker_config

        stored = get_firecracker_config().get("default_binary_path")
        if stored is not None and Path(str(stored)).exists():
            return str(stored)
        from mvmctl.api.assets import list_local_versions as _list_local_versions

        local = _list_local_versions()
        active = next((b for b in local if b.is_active), None)
        if active:
            return str(active.firecracker_path)
    except Exception:
        pass
    return DEFAULT_FIRECRACKER_BIN


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
        help="Path to vmlinux kernel (default: active default kernel or MVM_KERNEL env var)",
    ),
    vcpus: Optional[int] = typer.Option(
        None, "--vcpus", "--cpus", help="Number of vCPUs (default: from user config)"
    ),
    mem: Optional[int] = typer.Option(
        None, "--mem", "--memory", help="Memory in MiB (default: from user config)"
    ),
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
    user: Optional[str] = typer.Option(
        None, "--user", help="Default SSH user for cloud-init (default: from user config)"
    ),
    enable_api_socket: Optional[bool] = typer.Option(
        None,
        "--enable-api-socket/--no-enable-api-socket",
        help="Enable Firecracker HTTP API socket (default: from user config)",
    ),
    enable_pci: Optional[bool] = typer.Option(
        None,
        "--enable-pci/--no-enable-pci",
        help="Enable PCI device support (default: from user config)",
    ),
    firecracker_bin: Optional[str] = typer.Option(
        None,
        "--firecracker-bin",
        envvar="MVM_FIRECRACKER_BIN",
        help="Path to firecracker binary (default: active version from mvm bin use)",
    ),
    output_config: Optional[Path] = typer.Option(
        None,
        "--output-config",
        help="Output VM configuration JSON file with all resolved parameters (for debugging). VM is still created.",
    ),
    import_config: Optional[Path] = typer.Option(
        None,
        "--import-config",
        help="Import VM parameters from a JSON config file. CLI flags override file values.",
    ),
) -> None:
    """Create and start a new Firecracker VM.

    Examples:
        # Create a VM with defaults:
        mvm vm create --name myvm --image ubuntu-24.04

        # Create with custom resources and SSH key:
        mvm vm create --name myvm --image ubuntu-24.04 --vcpus 4 --mem 4096 --ssh-key mykey

        # Create with static IP:
        mvm vm create --name myvm --image ubuntu-24.04 --ip 10.20.0.10

        # Create with API socket for snapshot support:
        mvm vm create --name myvm --image ubuntu-24.04 --enable-api-socket

        # Import from config file with CLI overrides:
        mvm vm create --import-config myvm.json --name newname
    """
    if import_config is not None:
        try:
            base_config = load_vm_config_file(import_config)
        except (FileNotFoundError, ValueError) as e:
            print_error(str(e))
            raise typer.Exit(code=1)

        merged = merge_cli_overrides(
            base_config,
            name=name,
            image=image,
            kernel=kernel,
            vcpus=vcpus,
            mem=mem,
            ip=ip,
            network=network_name,
            mac=mac,
            ssh_key=ssh_key,
            user=user,
            enable_api_socket=enable_api_socket,
            enable_pci=enable_pci,
            firecracker_bin=firecracker_bin,
        )
        name = merged.name
        image = merged.image
        kernel = merged.kernel
        vcpus = merged.vcpus
        mem = merged.mem
        ip = merged.ip
        network_name = merged.network
        mac = merged.mac
        ssh_key = merged.ssh_key
        user = merged.user
        enable_api_socket = merged.enable_api_socket
        enable_pci = merged.enable_pci
        firecracker_bin = merged.firecracker_bin

    _defaults = _get_vm_defaults()
    effective_vcpus: int = vcpus if vcpus is not None else _defaults.vcpu_count
    effective_mem: int = mem if mem is not None else _defaults.mem_size_mib
    effective_user: str = user if user is not None else _defaults.ssh_user
    effective_api_socket: bool = (
        enable_api_socket if enable_api_socket is not None else _defaults.enable_api_socket
    )
    effective_pci: bool = enable_pci if enable_pci is not None else _defaults.enable_pci

    if image is None:
        image = _resolve_default_image()
        if image is None:
            print_error(
                "No --image specified and no default image set. "
                "Use 'mvm image fetch <name>' then 'mvm image set-default <name>', or pass --image."
            )
            raise typer.Exit(code=1)

    if kernel is None:
        kernel = _resolve_default_kernel()

    effective_bin = firecracker_bin or _resolve_active_firecracker_bin()

    if output_config is not None:
        from mvmctl.utils.fs import get_images_dir

        image_path: Path | None = None
        candidate = get_images_dir() / f"{image}.ext4"
        if candidate.exists():
            image_path = candidate
        else:
            test_path = Path(image)
            if test_path.exists():
                image_path = test_path

        kernel_path = Path(kernel) if kernel else None

        vm_config = build_vm_config_file(
            name=name,
            image=image,
            kernel=str(kernel_path) if kernel_path else None,
            vcpus=effective_vcpus,
            mem=effective_mem,
            ip=ip,
            network=network_name,
            mac=mac,
            ssh_key=ssh_key,
            user=effective_user,
            enable_api_socket=effective_api_socket,
            enable_pci=effective_pci,
            firecracker_bin=effective_bin,
            rootfs_path=image_path,
            gateway=None,
            subnet_mask=None,
            tap_device=None,
        )
        vm_config.to_json_file(output_config)
        print_info(f"VM config written to: {output_config}")

    try:
        vm = create_vm(
            name=name,
            image=image,
            kernel=kernel,
            vcpus=effective_vcpus,
            mem=effective_mem,
            ip=ip,
            network_name=network_name,
            mac=mac,
            ssh_key=ssh_key,
            user_data=user_data,
            user=effective_user,
            enable_api_socket=effective_api_socket,
            enable_pci=effective_pci,
            firecracker_bin=effective_bin,
        )
        from mvmctl.utils.audit import log_audit

        log_audit("vm.create", f"name={name}")
        print_success(f"VM '{name}' started (PID {vm.pid})")
        print_info(f"  SSH ready in ~30-60s: mvm vm ssh --name {name}")
        print_info(f"  Logs: mvm vm logs --name {name} --type os --follow")
    except MVMError as e:
        print_error(str(e))
        raise typer.Exit(code=1)


@app.command(name="rm")
def rm(
    ids: Optional[List[str]] = typer.Argument(None, help="VM short IDs (first 6 chars) to remove"),
    name: List[str] = typer.Option(
        [], "--name", "-n", help="VM name to remove (can be specified multiple times)"
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Stop and remove VMs by short ID or name.

    Examples:
        # Remove by short ID:
        mvm vm rm abc123 def456

        # Remove by name (prompts if multiple with same name):
        mvm vm rm --name runner1 --name runner2
    """
    from mvmctl.api.vms import get_vm_manager as _get_vm_manager

    manager = _get_vm_manager()
    targets: list[VMInstance] = []
    errors: list[str] = []

    effective_ids: list[str] = list(ids) if ids else []

    # Resolve short IDs
    for short_id in effective_ids:
        matches = manager.find_by_short_id(short_id)
        if len(matches) == 0:
            errors.append(f"No VM found with short ID '{short_id}'")
        elif len(matches) > 1:
            errors.append(f"Multiple VMs match short ID '{short_id}' — use a longer prefix or name")
        else:
            targets.append(matches[0])

    # Resolve names
    for n in name:
        matches = manager.get_by_name(n)
        if len(matches) == 0:
            errors.append(f"No VM found with name '{n}'")
        elif len(matches) > 1:
            print_info(f"Multiple VMs found with name '{n}':")
            for i, v in enumerate(matches, 1):
                print_info(
                    f"  {i}. {v.name} (ID: {v.id[:6] if v.id else '-'}, IP: {v.ip or '-'}, status: {v.status.value})"
                )
            choice = typer.prompt(f"Select VM to remove (1-{len(matches)})", type=int)
            if choice < 1 or choice > len(matches):
                print_error("Invalid selection")
                raise typer.Exit(code=1)
            targets.append(matches[choice - 1])
        else:
            targets.append(matches[0])

    if not targets and not errors:
        print_error("Provide at least one VM short ID or --name")
        raise typer.Exit(code=1)

    if errors:
        for err in errors:
            print_error(err)
        if not targets:
            raise typer.Exit(code=1)

    # Deduplicate targets by ID
    seen_ids: set[str] = set()
    unique_targets: list[VMInstance] = []
    for vm in targets:
        if vm.id not in seen_ids:
            seen_ids.add(vm.id)
            unique_targets.append(vm)
    targets = unique_targets

    if not force:
        target_names = [
            f"{vm.name} (ID: {vm.id[:6] if vm.id else '-'}, IP: {vm.ip or '-'})" for vm in targets
        ]
        typer.confirm(
            f"Remove {len(targets)} VM(s):\n  " + "\n  ".join(target_names) + "?", abort=True
        )

    removed_count = 0
    for vm in targets:
        try:
            remove_vm(vm.name)
            from mvmctl.utils.audit import log_audit

            log_audit("vm.remove", f"name={vm.name}")
            print_success(f"VM '{vm.name}' removed")
            removed_count += 1
        except MVMError as e:
            print_error(f"Failed to remove VM '{vm.name}': {e}")

    if removed_count == 0 and targets:
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
                "id": v.id[:6] if v.id else "-",
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

    rows = [
        [
            v.id[:6] if v.id else "-",
            v.name,
            v.ip or "-",
            v.status.value,
            str(v.pid) if v.pid else "-",
            "on" if v.socket_path else "off",
            v.created_at.strftime("%Y-%m-%d %H:%M") if v.created_at else "-",
        ]
        for v in vms
    ]
    print_table(
        title="Firecracker VMs",
        columns=["ID", "Name", "IP", "Status", "PID", "API", "Created"],
        rows=rows,
    )


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
            raise MVMError(f"No SSH key found at: {key}")
        return resolved
    mvm_keys_dir = Path.home() / ".cache" / "mvmctl" / "keys"
    if mvm_keys_dir.exists():
        for f in sorted(mvm_keys_dir.iterdir()):
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
    user: Optional[str] = typer.Option(
        None, "--user", "-u", help="SSH user (default: from user config)"
    ),
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
        effective_user = user if user is not None else _get_vm_defaults().ssh_user
        exit_code = ssh_vm(name=name, user=effective_user, key=resolved_key, cmd=cmd)
        raise typer.Exit(code=exit_code)
    except MVMError as e:
        print_error(str(e))
        raise typer.Exit(code=1)


@app.command()
def logs(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    follow: bool = typer.Option(DEFAULT_VM_LOG_FOLLOW, "--follow", "-f", help="Follow log output"),
    lines: int = typer.Option(DEFAULT_VM_LOG_LINES, "--lines", help="Number of lines to show"),
    log_type: str = typer.Option(
        DEFAULT_VM_LOG_TYPE,
        "--type",
        help="Log type: boot (serial console) or os (firecracker process log)",
    ),
) -> None:
    """View VM logs.

    Use --type boot for serial console output (what you see during boot).
    Use --type os for the Firecracker process log (hypervisor events).
    """
    try:
        from mvmctl.utils.validation import validate_entity_name

        validate_entity_name(name, "VM")
        log_lines = get_logs(name=name, log_type=log_type, lines=lines, follow=follow)
        for line in log_lines:
            print(line, end="" if line.endswith("\n") else "\n")
        raise typer.Exit(code=0)
    except MVMError as e:
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
    from mvmctl.utils.validation import validate_entity_name

    validate_entity_name(name, "VM")
    try:
        snapshot_vm(name=name, mem_out=mem_out, state_out=state_out)
        raise typer.Exit(code=0)
    except MVMError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)


@app.command()
def load(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    mem_in: Path = typer.Option(..., "--mem-in", help="Memory snapshot input path"),
    state_in: Path = typer.Option(..., "--state-in", help="VM state input path"),
    resume_after: bool = typer.Option(
        DEFAULT_SNAPSHOT_RESUME, "--resume/--no-resume", help="Resume VM after loading"
    ),
) -> None:
    """Load VM from snapshot. Requires --enable-api-socket."""
    from mvmctl.utils.validation import validate_entity_name

    validate_entity_name(name, "VM")
    try:
        load_snapshot(name=name, mem_in=mem_in, state_in=state_in, resume_after=resume_after)
        raise typer.Exit(code=0)
    except MVMError as exc:
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
