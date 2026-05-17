"""VM lifecycle management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING

import typer
from rich.console import Console

from mvmctl.api import VMCreateInput as _VMCreateInput
from mvmctl.api import VMInput as _VMInput
from mvmctl.api import VMOperation as _VMOperation
from mvmctl.cli._completion import _complete_vm_names
from mvmctl.models import VMStatus
from mvmctl.models.result import NeedsInteraction, ProgressEvent

if TYPE_CHECKING:
    from mvmctl.api.inputs._vm_create_input import VMCreateInput
    from mvmctl.api.inputs._vm_input import VMInput
    from mvmctl.api.vm_operations import VMOperation
else:
    VMOperation = _VMOperation
    VMInput = _VMInput
    VMCreateInput = _VMCreateInput
from mvmctl.utils.cli import handle_errors, mvm_cli

if TYPE_CHECKING:
    from mvmctl.models import VMInstanceItem


vm_app = typer.Typer(
    help="VM lifecycle management",
    no_args_is_help=True,
    add_completion=False,
)


@vm_app.callback()
def vm_callback(ctx: typer.Context) -> None:
    pass


@vm_app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@vm_app.command(name="ls")
@handle_errors
def vm_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List all VMs."""
    vms: list[VMInstanceItem] = VMOperation.list_all()

    if json_output:
        data = VMOperation.to_json(VMOperation.list_all())
        typer.echo(json.dumps(data, indent=2))
        return

    rows = []
    for vm in vms:
        rows.append(
            [
                vm.name,
                vm.status,
                str(vm.exit_code) if vm.exit_code is not None else "-",
                vm.ipv4 or "-",
                str(vm.vcpu_count),
                str(vm.mem_size_mib),
                str(vm.disk_size_mib),
                mvm_cli.format_id(vm.image_id),
                mvm_cli.format_id(vm.kernel_id),
                mvm_cli.format_timestamp(vm.created_at),
            ]
        )
    mvm_cli.table(
        columns=[
            "Name",
            "Status",
            "Exit",
            "IPv4",
            "vCPUs",
            "Mem(MiB)",
            "Disk(MiB)",
            "Image",
            "Kernel",
            "Created",
        ],
        rows=rows,
    )


@vm_app.command(name="ps")
@handle_errors
def vm_ps() -> None:
    """List running VMs (active processes)."""
    active_vms = VMOperation.list_all(
        status=[VMStatus.STARTING, VMStatus.RUNNING]
    )

    if not active_vms:
        mvm_cli.success("No active VMs")
        return

    rows = []
    for vm in active_vms:
        rows.append(
            [
                vm.name,
                vm.status,
                vm.ipv4 or "-",
                str(vm.vcpu_count),
                str(vm.mem_size_mib),
                str(vm.disk_size_mib),
                mvm_cli.format_id(vm.image_id),
                mvm_cli.format_id(vm.kernel_id),
                mvm_cli.format_timestamp(vm.created_at),
            ]
        )
    mvm_cli.table(
        columns=[
            "Name",
            "Status",
            "IPv4",
            "vCPUs",
            "Mem(MiB)",
            "Disk(MiB)",
            "Image",
            "Kernel",
            "Created",
        ],
        rows=rows,
    )


@vm_app.command(name="create")
@handle_errors
def vm_create(
    name: str = typer.Argument(..., help="VM name"),
    image: str | None = typer.Option(
        None,
        "--image",
        help="Image name, type:version (e.g. ubuntu:24.04), short ID, or path to .ext4 file",
    ),
    kernel: str | None = typer.Option(
        None,
        "--kernel",
        help="Kernel short ID or path to vmlinux file",
    ),
    vcpus: int | None = typer.Option(
        None,
        "--vcpus",
        "--cpus",
        help="Number of vCPUs (default: from user config)",
    ),
    mem: str | None = typer.Option(
        None,
        "--mem",
        "--memory",
        help="Memory in MiB or GiB (e.g. 512M, 1G, 4096). Default: from user config",
    ),
    disk_size: str | None = typer.Option(
        None,
        "--disk-size",
        "-s",
        help="Rootfs disk size in MiB/GiB (e.g., 512M=512MiB, 1G=1GiB). Default from config.",
    ),
    ip: str | None = typer.Option(
        None, "--ip", help="Guest IP (auto-assigned if omitted)"
    ),
    network_name: str | None = typer.Option(
        None, "--network", "--net", help="Named network to use"
    ),
    mac: str | None = typer.Option(
        None, "--mac", help="Custom MAC address (auto-generated if omitted)"
    ),
    ssh_key: str | None = typer.Option(
        None,
        "--ssh-key",
        help="SSH public key name (from key cache) or file path",
    ),
    user_data: Path | None = typer.Option(
        None, "--user-data", help="Path to custom cloud-init user-data file"
    ),
    cloud_init_mode: str | None = typer.Option(
        None,
        "--cloud-init-mode",
        help="Cloud-init mode: 'inject' (direct injection), 'iso' (ISO mode), 'net' (HTTP), 'off' (default, no cloud-init)",
    ),
    nocloud_net_port: int | None = typer.Option(
        None,
        "--nocloud-net-port",
        help="Port for nocloud-net HTTP server (0 for auto-assign, default: auto-assign)",
    ),
    user: str | None = typer.Option(
        None,
        "--user",
        help="Default SSH user for cloud-init (default: from user config)",
    ),
    no_pci: bool = typer.Option(
        False,
        "--no-pci",
        help="Disable PCI transport (default: enabled). Required for hotplug support.",
    ),
    nested_virt: bool | None = typer.Option(
        None,
        "--nested-virt/--no-nested-virt",
        help="Enable nested virtualization (requires PCI, adds kvm-intel/amd.nested=1 boot arg)",
    ),
    cpu_template: Path | None = typer.Option(
        None,
        "--cpu-template",
        help="Path to CPU template JSON file (merged with nested-virt config if both set)",
        exists=True,
        dir_okay=False,
    ),
    no_console: bool = typer.Option(
        False,
        "--no-console",
        help="Disable serial console",
    ),
    boot_args: str | None = typer.Option(
        None,
        "--boot-args",
        help="Kernel boot arguments (default: from constants.py)",
    ),
    lsm_flags: str | None = typer.Option(
        None,
        "--lsm-flags",
        help="Linux Security Module flags for kernel cmdline (default: from user config)",
    ),
    enable_logging: bool | None = typer.Option(
        None,
        "--enable-logging/--no-enable-logging",
        help="Enable Firecracker logging (default: from user config)",
    ),
    enable_metrics: bool | None = typer.Option(
        None,
        "--enable-metrics/--no-enable-metrics",
        help="Enable Firecracker metrics (default: from user config)",
    ),
    firecracker_bin: str | None = typer.Option(
        None,
        "--firecracker-bin",
        envvar="MVM_FIRECRACKER_BIN",
        help="Path to firecracker binary (default: active version from mvm bin default)",
    ),
    count: int | None = typer.Option(
        None,
        "--count",
        "-c",
        help="Number of VMs to create (default: 1)",
    ),
    atomic: bool = typer.Option(
        False,
        "--atomic",
        help="If any VM fails, remove all successfully-created VMs (all-or-nothing)",
    ),
    skip_cleanup: bool = typer.Option(
        False,
        "--skip-cleanup",
        help="Skip cleanup if VM creation fails; keeps cloud-init ISO and partial resources (for debugging)",
    ),
    skip_deblob: bool = typer.Option(
        False,
        "--skip-deblob",
        help="Skip debloat operations on rootfs (removes OS caches, cleans package manager caches)",
    ),
    volume: list[str] | None = typer.Option(
        None,
        "--volume",
        "-v",
        help="Attach volume(s) to the VM (can specify multiple times)",
    ),
) -> None:
    """Create and start a new Firecracker VM."""
    if skip_cleanup:
        if not typer.confirm(
            "--skip-cleanup is set: if creation fails, resources will be left behind and must be cleaned manually. Continue?",
        ):
            mvm_cli.info("Aborted")
            raise typer.Exit(code=0)

    effective_ssh_keys = ssh_key.split(",") if ssh_key is not None else []

    # --count and --volume are mutually exclusive: cannot attach same volume
    # to multiple VMs.
    effective_count = count or 1
    if effective_count > 1 and volume:
        mvm_cli.error(
            "Cannot use --count with --volume: a volume can only be "
            "attached to a single VM. Create VMs individually with --volume."
        )
        raise typer.Exit(code=1)

    console = Console()

    with console.status("", spinner="dots") as status:

        def _on_progress(event: ProgressEvent) -> None:
            if event.message:
                status.update(event.message)

        result = VMOperation.create(
            VMCreateInput(
                name=name,
                vcpu_count=vcpus,
                mem_size_mib=mem,
                ssh_keys=effective_ssh_keys,
                user=user,
                pci_enabled=not no_pci,
                nested_virt=nested_virt,
                cpu_template=cpu_template,
                enable_console=not no_console if no_console else None,
                enable_logging=enable_logging,
                enable_metrics=enable_metrics,
                firecracker_bin=firecracker_bin,
                lsm_flags=lsm_flags,
                boot_args=boot_args,
                image=image,
                kernel_id=kernel,
                disk_size=disk_size,
                requested_guest_ip=ip,
                network_name=network_name,
                requested_guest_mac=mac,
                custom_user_data=user_data,
                cloud_init_mode=cloud_init_mode,
                nocloud_net_port=nocloud_net_port,
                skip_cleanup=skip_cleanup,
                skip_deblob=skip_deblob,
                volumes=volume,
                count=count,
                atomic=atomic,
            ),
            on_progress=_on_progress,
        )
    if isinstance(result, NeedsInteraction):
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)

    if result.item is None:
        mvm_cli.error("No VMs returned")
        raise typer.Exit(code=1)

    vms = result.item
    names = [vm.name for vm in vms]
    mvm_cli.success(f"Created: {', '.join(names)}")


@vm_app.command(name="rm")
@handle_errors
def vm_rm(
    identifiers: list[str] = typer.Argument(
        ...,
        help="VM names, ID prefixes, IPs, or MAC addresses",
        autocompletion=_complete_vm_names,
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Force removal"),
) -> None:
    """Remove one or more VMs."""
    result = VMOperation.remove(
        VMInput(identifiers=list(identifiers), force=force)
    )
    if result.has_any_error:
        for r in result.items:
            if r.is_ok:
                if r.item:
                    mvm_cli.success(f"Removed: {r.item.name}")
            else:
                item_name = r.item.name if r.item else "unknown"
                mvm_cli.error(r.message or f"Remove failed: {item_name}")
        raise typer.Exit(code=1)
    names = [r.item.name for r in result.items if r.item]
    mvm_cli.success(f"Removed: {', '.join(names)}")


@vm_app.command(name="start")
@handle_errors
def vm_start(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
) -> None:
    """Start a stopped VM."""
    result = VMOperation.start(VMInput(identifiers=[identifier]))
    if result.has_any_error:
        for r in result.items:
            if not r.is_ok:
                mvm_cli.error(r.message or f"Start failed: {identifier}")
        raise typer.Exit(code=1)
    mvm_cli.success(f"Started: {identifier}")


@vm_app.command(name="stop")
@handle_errors
def vm_stop(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Force stop"),
) -> None:
    """Stop a running VM."""
    result = VMOperation.stop(VMInput(identifiers=[identifier], force=force))
    if result.has_any_error:
        for r in result.items:
            if not r.is_ok:
                mvm_cli.error(r.message or f"Stop failed: {identifier}")
        raise typer.Exit(code=1)
    mvm_cli.success(f"Stopped: {identifier}")


@vm_app.command(name="reboot")
@handle_errors
def vm_reboot(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Force reboot"),
) -> None:
    """Reboot a VM."""
    result = VMOperation.reboot(VMInput(identifiers=[identifier], force=force))
    if result.has_any_error:
        for r in result.items:
            if not r.is_ok:
                mvm_cli.error(r.message or f"Reboot failed: {identifier}")
        raise typer.Exit(code=1)
    mvm_cli.success(f"Rebooted: {identifier}")


@vm_app.command(name="pause")
@handle_errors
def vm_pause(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
) -> None:
    """Pause a running VM."""
    result = VMOperation.pause(VMInput(identifiers=[identifier]))
    if result.has_any_error:
        for r in result.items:
            if not r.is_ok:
                mvm_cli.error(r.message or f"Pause failed: {identifier}")
        raise typer.Exit(code=1)
    mvm_cli.success(f"Paused: {identifier}")


@vm_app.command(name="resume")
@handle_errors
def vm_resume(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
) -> None:
    """Resume a paused VM."""
    result = VMOperation.resume(VMInput(identifiers=[identifier]))
    if result.has_any_error:
        for r in result.items:
            if not r.is_ok:
                mvm_cli.error(r.message or f"Resume failed: {identifier}")
        raise typer.Exit(code=1)
    mvm_cli.success(f"Resumed: {identifier}")


@vm_app.command(name="snapshot")
@handle_errors
def vm_snapshot(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
    mem_file: Path = typer.Argument(..., help="Memory snapshot output path"),
    state_file: Path = typer.Argument(..., help="State snapshot output path"),
) -> None:
    """Snapshot VM memory and disk state."""
    result = VMOperation.snapshot(
        VMInput(identifiers=[identifier]), mem_file, state_file
    )
    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    mvm_cli.success(result.message or f"Snapshot saved: {identifier}")


@vm_app.command(name="load")
@handle_errors
def vm_load(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
    mem_file: Path = typer.Argument(..., help="Memory snapshot input path"),
    state_file: Path = typer.Argument(..., help="State snapshot input path"),
    resume: bool = typer.Option(
        False, "--resume", help="Resume VM after loading"
    ),
) -> None:
    """Load VM from snapshot."""
    VMOperation.load_snapshot(
        VMInput(identifiers=[identifier]),
        mem_file,
        state_file,
        resume_after=resume,
    )
    mvm_cli.success(f"Snapshot loaded: {identifier}")


@vm_app.command(name="inspect")
@handle_errors
def vm_inspect(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a VM."""
    info = VMOperation.inspect(VMInput(identifiers=[identifier]))

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    vm_name = info.get("vm", {}).get("name", identifier)
    mvm_cli.print_dict_tree(info, title=f"VM: {vm_name}")


@vm_app.command(name="export")
@handle_errors
def vm_export(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
    output: Path | None = typer.Argument(
        None, help="Output file path (prints to stdout if omitted)"
    ),
) -> None:
    """
    Export a VM's configuration to a portable JSON file.

    The exported config uses semantic references (type, version, name)
    instead of internal IDs, making it portable across machines.
    """
    config = VMOperation.export(VMInput(identifiers=[identifier]))
    json_output = json.dumps(config.to_dict(), indent=2)

    if output is not None:
        output.write_text(json_output)
        mvm_cli.success(f"Exported: {output}")
    else:
        typer.echo(json_output)


@vm_app.command(name="import")
@handle_errors
def vm_import(
    config_path: Path = typer.Argument(..., help="Path to VM config JSON file"),
    name: str | None = typer.Option(
        None, "--name", "-n", help="Override VM name from config"
    ),
) -> None:
    """Create a VM from a portable config file."""
    from mvmctl.api.inputs import VMImportInput

    result = VMOperation.import_(
        VMImportInput(config_path=config_path, name_override=name)
    )
    if isinstance(result, NeedsInteraction):
        mvm_cli.error("Import requires privileges")
        raise typer.Exit(code=1)
    if result.status == "success":
        mvm_cli.success(result.message)
    elif result.status in ("error", "failure"):
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)


@vm_app.command(name="attach-volume")
@handle_errors
def vm_attach_volume(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC",
        autocompletion=_complete_vm_names,
    ),
    volume_name: str = typer.Argument(..., help="Volume name"),
) -> None:
    """Attach a volume to a running VM."""
    result = VMOperation.attach_volume(
        VMInput(identifiers=[identifier]), volume_name
    )
    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    mvm_cli.success(result.message)


@vm_app.command(name="detach-volume")
@handle_errors
def vm_detach_volume(
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID prefix, IP, or MAC",
        autocompletion=_complete_vm_names,
    ),
    volume_name: str = typer.Argument(..., help="Volume name"),
) -> None:
    """Detach a volume from a running VM."""
    result = VMOperation.detach_volume(
        VMInput(identifiers=[identifier]), volume_name
    )
    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    mvm_cli.success(result.message)
