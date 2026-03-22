"""VM lifecycle commands."""

import typer
from pathlib import Path
from typing import Optional

from fcm.utils.console import print_error

app = typer.Typer(help="VM lifecycle management")


@app.command()
def create(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    rootfs: str = typer.Option(..., "--rootfs", help="Rootfs image ID or path"),
    kernel: Optional[str] = typer.Option(None, "--kernel", help="Kernel path"),
    cpu: int = typer.Option(2, "--cpu", help="Number of vCPUs"),
    mem: int = typer.Option(2048, "--mem", help="Memory in MiB"),
    ip: Optional[str] = typer.Option(None, "--ip", help="Guest IP address"),
    tap: Optional[str] = typer.Option(None, "--tap", help="TAP device name"),
    mac: Optional[str] = typer.Option(None, "--mac", help="Guest MAC address"),
    config: Optional[Path] = typer.Option(None, "--config", help="Firecracker JSON config path"),
    enable_socket: bool = typer.Option(False, "--enable-socket", help="Enable API socket"),
) -> None:
    """Create and start a new Firecracker VM."""
    typer.echo(f"Creating VM: {name}")
    typer.echo(f"  Rootfs: {rootfs}")
    typer.echo(f"  vCPUs: {cpu}, Memory: {mem}MiB")
    if ip:
        typer.echo(f"  IP: {ip}")


@app.command()
def delete(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    force: bool = typer.Option(
        False, "--force", "-f", help="Force kill if graceful shutdown fails"
    ),
) -> None:
    """Stop and remove a VM."""
    typer.echo(f"Deleting VM: {name}")


@app.command(name="list")
def list_vms(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    all_vms: bool = typer.Option(False, "--all", "-a", help="Show all VMs including stopped"),
) -> None:
    """List running and stopped VMs."""
    typer.echo("Listing VMs...")


@app.command()
def ssh(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    user: str = typer.Option("root", "--user", "-u", help="SSH user"),
    key: Optional[Path] = typer.Option(None, "--key", help="SSH key path"),
    cmd: Optional[str] = typer.Option(None, "--cmd", "-c", help="Command to execute"),
    assets_dir: Path = typer.Option(Path("../assets"), "--assets", help="Assets directory"),
    multi_vm_dir: Path = typer.Option(Path("../multi-vm"), "--multi-vm", help="Multi-VM directory"),
) -> None:
    """Open an SSH session into a VM."""
    from fcm.core.ssh import connect_to_vm

    exit_code = connect_to_vm(
        vm_name_or_ip=name,
        user=user,
        key_path=key,
        command=cmd,
        multi_vm_dir=multi_vm_dir,
        assets_dir=assets_dir,
        exec_mode=cmd is None,  # Exec if no command, subprocess if command
    )
    raise typer.Exit(code=exit_code)


@app.command()
def logs(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    follow: bool = typer.Option(False, "--follow", "-f", help="Follow log output"),
    lines: int = typer.Option(50, "--lines", "-n", help="Number of lines to show"),
    log_type: str = typer.Option("boot", "--type", help="Log type: boot or os"),
    multi_vm_dir: Path = typer.Option(Path("../multi-vm"), "--multi-vm", help="Multi-VM directory"),
) -> None:
    """Print VM serial console output."""
    from fcm.core.logs import show_logs

    exit_code = show_logs(
        vm_name=name,
        log_type=log_type,
        lines=lines,
        follow=follow,
        multi_vm_dir=multi_vm_dir,
    )
    raise typer.Exit(code=exit_code)


@app.command()
def cleanup(
    all_vms: bool = typer.Option(False, "--all", help="Remove all stopped VMs"),
    name: Optional[str] = typer.Option(None, "--name", help="Specific VM to clean up"),
    dry_run: bool = typer.Option(False, "--dry-run", help="Show what would be removed"),
) -> None:
    """Remove stopped VMs and stale sockets."""
    typer.echo("Cleaning up VMs...")


@app.command()
def pause(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    multi_vm_dir: Path = typer.Option(Path("../multi-vm"), "--multi-vm", help="Multi-VM directory"),
) -> None:
    """Pause a running VM.

    Requires VM to have been started with --enable-socket.
    """
    from fcm.core.firecracker import FirecrackerClient, get_vm_socket_path

    socket_path = get_vm_socket_path(name, multi_vm_dir)
    if not socket_path:
        print_error(f"Socket not found for VM '{name}'")
        print_error("VM must be running with --enable-socket")
        raise typer.Exit(code=1)

    client = FirecrackerClient(socket_path)
    try:
        if client.pause_vm():
            raise typer.Exit(code=0)
        else:
            raise typer.Exit(code=1)
    finally:
        client.close()


@app.command()
def resume(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    multi_vm_dir: Path = typer.Option(Path("../multi-vm"), "--multi-vm", help="Multi-VM directory"),
) -> None:
    """Resume a paused VM.

    Requires VM to have been started with --enable-socket.
    """
    from fcm.core.firecracker import FirecrackerClient, get_vm_socket_path

    socket_path = get_vm_socket_path(name, multi_vm_dir)
    if not socket_path:
        print_error(f"Socket not found for VM '{name}'")
        print_error("VM must be running with --enable-socket")
        raise typer.Exit(code=1)

    client = FirecrackerClient(socket_path)
    try:
        if client.resume_vm():
            raise typer.Exit(code=0)
        else:
            raise typer.Exit(code=1)
    finally:
        client.close()


@app.command()
def snapshot(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    mem_out: Path = typer.Option(..., "--mem-out", help="Memory snapshot output path"),
    state_out: Path = typer.Option(..., "--state-out", help="VM state output path"),
    multi_vm_dir: Path = typer.Option(Path("../multi-vm"), "--multi-vm", help="Multi-VM directory"),
) -> None:
    """Snapshot VM memory and disk state.

    Requires VM to have been started with --enable-socket.
    """
    from fcm.core.firecracker import FirecrackerClient, get_vm_socket_path

    socket_path = get_vm_socket_path(name, multi_vm_dir)
    if not socket_path:
        print_error(f"Socket not found for VM '{name}'")
        print_error("VM must be running with --enable-socket")
        raise typer.Exit(code=1)

    client = FirecrackerClient(socket_path)
    try:
        if client.create_snapshot(mem_out, state_out):
            raise typer.Exit(code=0)
        else:
            raise typer.Exit(code=1)
    finally:
        client.close()


@app.command()
def load(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    mem_in: Path = typer.Option(..., "--mem-in", help="Memory snapshot input path"),
    state_in: Path = typer.Option(..., "--state-in", help="VM state input path"),
    resume: bool = typer.Option(True, "--resume/--no-resume", help="Resume VM after loading"),
    multi_vm_dir: Path = typer.Option(Path("../multi-vm"), "--multi-vm", help="Multi-VM directory"),
) -> None:
    """Load VM from snapshot.

    Requires VM to have been started with --enable-socket.
    """
    from fcm.core.firecracker import FirecrackerClient, get_vm_socket_path

    socket_path = get_vm_socket_path(name, multi_vm_dir)
    if not socket_path:
        print_error(f"Socket not found for VM '{name}'")
        print_error("VM must be running with --enable-socket")
        raise typer.Exit(code=1)

    client = FirecrackerClient(socket_path)
    try:
        if client.load_snapshot(mem_in, state_in, resume):
            raise typer.Exit(code=0)
        else:
            raise typer.Exit(code=1)
    finally:
        client.close()
