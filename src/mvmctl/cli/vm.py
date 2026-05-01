"""VM lifecycle management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING, Optional

import typer

from mvmctl.api import VMCreateInput, VMInput, VMOperation
from mvmctl.models.vm import VMStatus
from mvmctl.utils._io import (
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
)
from mvmctl.utils.cli import handle_errors
from mvmctl.utils.crypto import HashGenerator

if TYPE_CHECKING:
    from mvmctl.models.vm import VMInstanceItem

vm_app = typer.Typer(
    help="VM lifecycle management",
    no_args_is_help=True,
    rich_markup_mode=None,
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
        data = [
            {
                "name": vm.name,
                "status": vm.status,
                "ipv4": vm.ipv4,
                "vcpus": vm.vcpu_count,
                "mem_mib": vm.mem_size_mib,
                "disk_mib": vm.disk_size_mib,
                "image": vm.image_id,
                "kernel": vm.kernel_id,
                "created_at": vm.created_at,
            }
            for vm in vms
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    rows = []
    for vm in vms:
        rows.append(
            [
                vm.name,
                vm.status,
                vm.ipv4 or "-",
                str(vm.vcpu_count),
                str(vm.mem_size_mib),
                str(vm.disk_size_mib),
                HashGenerator.shorten(vm.image_id),
                HashGenerator.shorten(vm.kernel_id),
                vm.created_at,
            ]
        )
    print_table(
        columns=[
            "NAME",
            "STATUS",
            "IPV4",
            "VCPUS",
            "MEM(MiB)",
            "DISK(MiB)",
            "IMAGE",
            "KERNEL",
            "CREATED",
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
        print_success("No active VMs")
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
                HashGenerator.shorten(vm.image_id),
                HashGenerator.shorten(vm.kernel_id),
                vm.created_at,
            ]
        )
    print_table(
        columns=[
            "NAME",
            "STATUS",
            "IPV4",
            "VCPUS",
            "MEM(MiB)",
            "DISK(MiB)",
            "IMAGE",
            "KERNEL",
            "CREATED",
        ],
        rows=rows,
    )


@vm_app.command(name="create")
@handle_errors
def vm_create(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    image: Optional[str] = typer.Option(
        None,
        "--image",
        help="Image name (e.g., ubuntu-24.04), short ID, or path to .ext4 file",
    ),
    kernel: Optional[str] = typer.Option(
        None,
        "--kernel",
        help="Kernel short ID or path to vmlinux file",
    ),
    image_path: Optional[Path] = typer.Option(
        None,
        "--image-path",
        help="Direct path to rootfs image file (overrides --image)",
    ),
    kernel_path: Optional[Path] = typer.Option(
        None,
        "--kernel-path",
        help="Direct path to vmlinux kernel file (overrides --kernel)",
    ),
    vcpus: Optional[int] = typer.Option(
        None,
        "--vcpus",
        "--cpus",
        help="Number of vCPUs (default: from user config)",
    ),
    mem: Optional[int] = typer.Option(
        None,
        "--mem",
        "--memory",
        help="Memory in MiB (default: from user config)",
    ),
    disk_size: Optional[str] = typer.Option(
        None,
        "--disk-size",
        "-s",
        help="Rootfs disk size in MiB/GiB (e.g., 512M=512MiB, 1G=1GiB). Default from config.",
    ),
    ip: Optional[str] = typer.Option(
        None, "--ip", help="Guest IP (auto-assigned if omitted)"
    ),
    network_name: Optional[str] = typer.Option(
        None, "--network", "--net", help="Named network to use"
    ),
    mac: Optional[str] = typer.Option(
        None, "--mac", help="Custom MAC address (auto-generated if omitted)"
    ),
    ssh_key: Optional[str] = typer.Option(
        None,
        "--ssh-key",
        help="SSH public key name (from key cache) or file path",
    ),
    user_data: Optional[Path] = typer.Option(
        None, "--user-data", help="Path to custom cloud-init user-data file"
    ),
    cloud_init_mode: Optional[str] = typer.Option(
        None,
        "--cloud-init-mode",
        help="Cloud-init mode: 'inject' (default, direct injection), 'iso' (ISO mode), 'net' (HTTP), 'off' (no cloud-init)",
    ),
    nocloud_net_port: Optional[int] = typer.Option(
        None,
        "--nocloud-net-port",
        help="Port for nocloud-net HTTP server (0 for auto-assign, default: auto-assign)",
    ),
    user: Optional[str] = typer.Option(
        None,
        "--user",
        help="Default SSH user for cloud-init (default: from user config)",
    ),
    enable_pci: Optional[bool] = typer.Option(
        None,
        "--enable-pci/--no-enable-pci",
        help="Enable PCI device support (default: from user config)",
    ),
    no_console: bool = typer.Option(
        False,
        "--no-console",
        help="Disable serial console",
    ),
    lsm_flags: Optional[str] = typer.Option(
        None,
        "--lsm-flags",
        help="Linux Security Module flags for kernel cmdline (default: from user config)",
    ),
    enable_logging: Optional[bool] = typer.Option(
        None,
        "--enable-logging/--no-enable-logging",
        help="Enable Firecracker logging (default: from user config)",
    ),
    enable_metrics: Optional[bool] = typer.Option(
        None,
        "--enable-metrics/--no-enable-metrics",
        help="Enable Firecracker metrics (default: from user config)",
    ),
    firecracker_bin: Optional[str] = typer.Option(
        None,
        "--firecracker-bin",
        envvar="MVM_FIRECRACKER_BIN",
        help="Path to firecracker binary (default: active version from mvm bin use)",
    ),
    skip_cleanup: bool = typer.Option(
        False,
        "--skip-cleanup",
        help="Skip cleanup if VM creation fails; keeps cloud-init ISO and partial resources (for debugging)",
    ),
) -> None:
    """Create and start a new Firecracker VM."""
    effective_ssh_keys = ssh_key.split(",") if ssh_key is not None else []

    VMOperation.create(
        VMCreateInput(
            name=name,
            vcpu_count=vcpus,
            mem_size_mib=mem,
            ssh_keys=effective_ssh_keys,
            user=user,
            enable_pci=enable_pci,
            enable_console=not no_console if no_console else None,
            enable_logging=enable_logging,
            enable_metrics=enable_metrics,
            firecracker_bin=firecracker_bin,
            lsm_flags=lsm_flags,
            image=image,
            kernel_id=kernel,
            image_path=image_path,
            kernel_path=kernel_path,
            disk_size=disk_size,
            requested_guest_ip=ip,
            network_name=network_name,
            requested_guest_mac=mac,
            custom_user_data=user_data,
            cloud_init_mode=cloud_init_mode,
            nocloud_net_port=nocloud_net_port,
            skip_cleanup=skip_cleanup,
        )
    )
    print_success(f"VM '{name}' created")


@vm_app.command(name="rm")
@handle_errors
def vm_rm(
    name: str = typer.Argument(..., help="VM name or ID prefix"),
    force: bool = typer.Option(False, "--force", "-f", help="Force removal"),
) -> None:
    """Remove a VM."""
    VMOperation.remove(VMInput(identifiers=[name], force=force))
    print_success(f"VM '{name}' removed")


@vm_app.command(name="start")
@handle_errors
def vm_start(
    name: str = typer.Argument(..., help="VM name or ID prefix"),
) -> None:
    """Start a stopped VM."""
    VMOperation.start(VMInput(identifiers=[name]))
    print_success(f"VM '{name}' started")


@vm_app.command(name="stop")
@handle_errors
def vm_stop(
    name: str = typer.Argument(..., help="VM name or ID prefix"),
    force: bool = typer.Option(False, "--force", "-f", help="Force stop"),
) -> None:
    """Stop a running VM."""
    VMOperation.stop(VMInput(identifiers=[name], force=force))
    print_success(f"VM '{name}' stopped")


@vm_app.command(name="reboot")
@handle_errors
def vm_reboot(
    name: str = typer.Argument(..., help="VM name or ID prefix"),
    force: bool = typer.Option(False, "--force", "-f", help="Force reboot"),
) -> None:
    """Reboot a VM."""
    VMOperation.reboot(VMInput(identifiers=[name], force=force))
    print_success(f"VM '{name}' rebooted")


@vm_app.command(name="pause")
@handle_errors
def vm_pause(
    name: str = typer.Argument(..., help="VM name or ID prefix"),
) -> None:
    """Pause a running VM."""
    VMOperation.pause(VMInput(identifiers=[name]))
    print_success(f"VM '{name}' paused")


@vm_app.command(name="resume")
@handle_errors
def vm_resume(
    name: str = typer.Argument(..., help="VM name or ID prefix"),
) -> None:
    """Resume a paused VM."""
    VMOperation.resume(VMInput(identifiers=[name]))
    print_success(f"VM '{name}' resumed")


@vm_app.command(name="snapshot")
@handle_errors
def vm_snapshot(
    name: str = typer.Argument(..., help="VM name or ID prefix"),
    mem_file: Path = typer.Argument(..., help="Memory snapshot output path"),
    state_file: Path = typer.Argument(..., help="State snapshot output path"),
) -> None:
    """Snapshot VM memory and disk state."""
    VMOperation.snapshot(VMInput(identifiers=[name]), mem_file, state_file)
    print_success(f"VM '{name}' snapshot saved")


@vm_app.command(name="load")
@handle_errors
def vm_load(
    name: str = typer.Argument(..., help="VM name or ID prefix"),
    mem_file: Path = typer.Argument(..., help="Memory snapshot input path"),
    state_file: Path = typer.Argument(..., help="State snapshot input path"),
    resume: bool = typer.Option(
        False, "--resume", help="Resume VM after loading"
    ),
) -> None:
    """Load VM from snapshot."""
    VMOperation.load_snapshot(
        VMInput(identifiers=[name]), mem_file, state_file, resume_after=resume
    )
    print_success(f"VM '{name}' snapshot loaded")


@vm_app.command(name="inspect")
@handle_errors
def vm_inspect(
    name: str = typer.Argument(..., help="VM name or ID prefix"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a VM."""
    info = VMOperation.inspect(VMInput(identifiers=[name]))

    if json_output:
        data = {
            "id": info.id,
            "name": info.name,
            "status": info.status,
            "ipv4": info.ipv4,
            "mac": info.mac,
            "vcpus": info.vcpu_count,
            "mem_mib": info.mem_size_mib,
            "disk_mib": info.disk_size_mib,
            "image_id": info.image_id,
            "kernel_id": info.kernel_id,
            "network_id": info.network_id,
            "tap_device": info.tap_device,
            "pid": info.pid,
            "created_at": info.created_at,
            "updated_at": info.updated_at,
            "cloud_init_mode": info.cloud_init_mode,
            "enable_pci": info.enable_pci,
            "enable_console": info.enable_console,
            "enable_logging": info.enable_logging,
            "enable_metrics": info.enable_metrics,
        }
        typer.echo(json.dumps(data, indent=2))
        return

    print_inspect_header(f"VM: {info.name}", info.status)

    print_section_header("BASIC INFO")
    print_key_value("Name", info.name)
    print_key_value("ID", info.id)
    print_key_value("Status", info.status)
    print_key_value("PID", str(info.pid) if info.pid else "-")
    print_key_value("Created", info.created_at)

    print_section_header("RESOURCES")
    print_key_value("vCPUs", str(info.vcpu_count))
    print_key_value("Memory", f"{info.mem_size_mib} MiB")
    print_key_value("Disk", f"{info.disk_size_mib} MiB")

    print_section_header("NETWORK")
    print_key_value("IPv4", info.ipv4 or "-")
    print_key_value("MAC", info.mac or "-")
    print_key_value("TAP", info.tap_device or "-")
    print_key_value("Network ID", HashGenerator.shorten(info.network_id))

    print_section_header("ASSETS")
    print_key_value("Image", HashGenerator.shorten(info.image_id))
    print_key_value("Kernel", HashGenerator.shorten(info.kernel_id))

    print_section_header("FEATURES")
    print_key_value("PCI", "enabled" if info.enable_pci else "disabled")
    print_key_value("Console", "enabled" if info.enable_console else "disabled")
    print_key_value("Logging", "enabled" if info.enable_logging else "disabled")
    print_key_value("Metrics", "enabled" if info.enable_metrics else "disabled")
    print_key_value("Cloud-init", info.cloud_init_mode)


@vm_app.command(name="export")
@handle_errors
def vm_export(
    identifier: str = typer.Argument(
        ..., help="VM name, ID, IP, or MAC address"
    ),
    output: Optional[Path] = typer.Argument(
        None, help="Output file path (prints to stdout if omitted)"
    ),
) -> None:
    """Export a VM's configuration to a portable JSON file.

    The exported config uses semantic references (os_slug, version, name)
    instead of internal IDs, making it portable across machines.
    """
    config = VMOperation.export(VMInput(identifiers=[identifier]))
    json_output = json.dumps(config.to_dict(), indent=2)

    if output is not None:
        output.write_text(json_output)
        print_success(f"Exported VM config to {output}")
    else:
        typer.echo(json_output)


@vm_app.command(name="import")
@handle_errors
def vm_import(
    config_path: Path = typer.Argument(..., help="Path to VM config JSON file"),
    name: Optional[str] = typer.Option(
        None, "--name", "-n", help="Override VM name from config"
    ),
) -> None:
    """Create a VM from a portable config file."""
    from mvmctl.api.inputs import VMImportInput

    VMOperation.import_(
        VMImportInput(config_path=config_path, name_override=name)
    )
    print_success(f"VM imported from {config_path}")
