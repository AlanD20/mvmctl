"""VM lifecycle commands."""

import json
import os
import shutil
import signal
import subprocess
import time
from datetime import datetime
from pathlib import Path

import typer
from rich.console import Console
from rich.table import Table

from fcm.core.config_gen import ConfigGenerator
from fcm.core.firecracker import FirecrackerClient, get_vm_socket_path
from fcm.core.logs import show_logs
from fcm.core.network import (
    add_iptables_forward_rules,
    create_tap,
    delete_tap,
    generate_mac,
    remove_iptables_forward_rules,
    teardown_nat,
)
from fcm.core.network_manager import (
    DEFAULT_NETWORK_NAME,
    allocate_network_ip,
    ensure_default_network,
    get_network,
    release_network_ip,
)
from fcm.core.ssh import connect_to_vm
from fcm.core.vm_manager import VMManager
from fcm.exceptions import NetworkError
from fcm.models.vm import VMConfig, VMInstance, VMState
from fcm.utils.console import print_error, print_info, print_success
from fcm.utils.fs import get_cache_dir, get_images_dir, get_kernels_dir, get_vm_dir

app = typer.Typer(help="VM lifecycle management", no_args_is_help=True)

console = Console()


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the vm command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()

# ---------------------------------------------------------------------------
# create
# ---------------------------------------------------------------------------


@app.command()
def create(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    image: str = typer.Option(
        ..., "--image", help="Image ID (from `fcm image list`) or path to .ext4 file"
    ),
    kernel: str | None = typer.Option(
        None,
        "--kernel",
        help="Path to vmlinux kernel (default: FCM_KERNEL or ~/.cache/firecracker-manager/kernels/vmlinux)",
    ),
    vcpus: int = typer.Option(1, "--vcpus", "--cpus", help="Number of vCPUs"),
    mem: int = typer.Option(512, "--mem", "--memory", help="Memory in MiB"),
    ip: str | None = typer.Option(None, "--ip", help="Guest IP (auto-assigned if omitted)"),
    network_name: str = typer.Option(
        DEFAULT_NETWORK_NAME, "--network", "--net", help="Named network to attach to"
    ),
    mac: str | None = typer.Option(
        None, "--mac", help="Custom MAC address (auto-generated if omitted)"
    ),
    ssh_key: str | None = typer.Option(
        None, "--ssh-key", help="SSH public key name (from key cache) or file path"
    ),
    user_data: Path | None = typer.Option(
        None, "--user-data", help="Path to custom cloud-init user-data file"
    ),
    user: str = typer.Option("root", "--user", help="Default SSH user for cloud-init"),
    enable_api_socket: bool = typer.Option(
        False, "--enable-api-socket", help="Enable Firecracker HTTP API socket"
    ),
    enable_pci: bool = typer.Option(False, "--enable-pci", help="Enable PCI device support"),
    firecracker_bin: str = typer.Option(
        "firecracker",
        "--firecracker-bin",
        envvar="FCM_FIRECRACKER_BIN",
        help="Path to firecracker binary",
    ),
) -> None:
    """Create and start a new Firecracker VM."""
    import re

    if mac is not None:
        mac_re = re.compile(r"^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$")
        if not mac_re.match(mac):
            print_error(f"Invalid MAC address format: {mac!r}. Expected format: XX:XX:XX:XX:XX:XX")
            raise typer.Exit(code=1)

    vm_dir = get_vm_dir(name)
    if vm_dir.exists():
        print_error(f"VM '{name}' already exists at {vm_dir}")
        raise typer.Exit(code=1)

    kernel_path: Path
    if kernel:
        kernel_path = Path(kernel)
    else:
        env_kernel = os.environ.get("FCM_KERNEL")
        if env_kernel:
            kernel_path = Path(env_kernel)
        else:
            kernel_path = get_kernels_dir() / "vmlinux"

    if not kernel_path.exists():
        print_error(f"Kernel not found: {kernel_path}")
        print_error("Build one with: fcm kernel build")
        raise typer.Exit(code=1)

    image_path: Path
    candidate = get_images_dir() / f"{image}.ext4"
    if candidate.exists():
        image_path = candidate
    else:
        image_path = Path(image)
        if not image_path.exists():
            print_error(f"Image not found: {image!r}")
            print_error(f"Checked: {candidate}")
            print_error("Download with: fcm image fetch <id>")
            raise typer.Exit(code=1)

    # Validate user-data file if provided
    if user_data is not None:
        if not user_data.exists():
            print_error(f"User-data file not found: {user_data}")
            raise typer.Exit(code=1)
        content = user_data.read_text()
        if not content.startswith("#cloud-config") and not content.startswith("Content-Type:"):
            print_info("Warning: user-data does not start with #cloud-config or MIME header")

    # -------------------------------------------------------- network & IP
    # Ensure the target network exists (auto-creates default if needed)
    try:
        net_config = get_network(network_name)
        if net_config is None:
            if network_name == DEFAULT_NETWORK_NAME:
                net_config = ensure_default_network()
            else:
                print_error(f"Network '{network_name}' not found. Create it with: fcm network create {network_name}")
                raise typer.Exit(code=1)
    except NetworkError as e:
        print_error(f"Network error: {e}")
        raise typer.Exit(code=1)

    # IP allocation
    if ip:
        # Validate IP is in the network's subnet
        import ipaddress
        try:
            ip_net = ipaddress.IPv4Network(net_config.cidr, strict=False)
            if ipaddress.IPv4Address(ip.split("/")[0]) not in ip_net:
                print_error(f"IP {ip} is outside network '{network_name}' subnet {net_config.cidr}")
                raise typer.Exit(code=1)
        except ValueError as e:
            print_error(f"Invalid IP address: {e}")
            raise typer.Exit(code=1)
        guest_ip = ip
    else:
        try:
            guest_ip = allocate_network_ip(network_name, name)
        except NetworkError as e:
            print_error(f"IP allocation failed: {e}")
            raise typer.Exit(code=1)

    # MAC address
    guest_mac = mac if mac else generate_mac()
    tap_name = f"fc-{name}-0"
    bridge = net_config.bridge

    print_info(f"Creating VM '{name}'")
    print_info(f"  Network: {network_name}")
    print_info(f"  IP:      {guest_ip}")
    print_info(f"  MAC:     {guest_mac}")
    print_info(f"  TAP:     {tap_name}")
    print_info(f"  vCPUs:   {vcpus}")
    print_info(f"  Memory:  {mem} MiB")

    # ----------------------------------------------------------------- vm dir
    vm_dir.mkdir(parents=True, exist_ok=False)

    rootfs_path = vm_dir / "rootfs.ext4"
    try:
        shutil.copy2(image_path, rootfs_path)
    except OSError as e:
        shutil.rmtree(vm_dir, ignore_errors=True)
        print_error(f"Failed to copy image: {e}")
        raise typer.Exit(code=1)

    # ------------------------------------------------------ cloud-init inject
    cloud_init_dir = vm_dir / "cloud-init"
    cloud_init_dir.mkdir(exist_ok=True)

    # Resolve SSH key
    ssh_pub_key = _resolve_ssh_key(ssh_key)

    _write_cloud_init(cloud_init_dir, name, guest_ip, user, ssh_pub_key=ssh_pub_key,
                      custom_user_data=user_data)
    _inject_cloud_init(rootfs_path, cloud_init_dir)

    # --------------------------------------------------------- firecracker cfg
    socket_path = vm_dir / "firecracker.api.socket" if enable_api_socket else None
    vm_config = VMConfig(
        name=name,
        vcpu_count=vcpus,
        mem_size_mib=mem,
        kernel_path=kernel_path,
        rootfs_path=rootfs_path,
        guest_ip=guest_ip,
        guest_mac=guest_mac,
        tap_device=tap_name,
        enable_api_socket=enable_api_socket,
        enable_pci=enable_pci,
    )
    config_file = vm_dir / "firecracker.json"
    ConfigGenerator(vm_config).write_to_file(config_file)

    # ---------------------------------------------------------------- network
    try:
        create_tap(tap_name, bridge=bridge)
        add_iptables_forward_rules(tap_name, bridge=bridge)
    except NetworkError as e:
        shutil.rmtree(vm_dir, ignore_errors=True)
        try:
            release_network_ip(network_name, name)
        except NetworkError:
            pass
        print_error(f"Network setup failed: {e}")
        raise typer.Exit(code=1)

    # ----------------------------------------------------------------- launch
    log_file = vm_dir / "firecracker.log"
    console_log_file = vm_dir / "firecracker.console.log"
    pid_file = vm_dir / "firecracker.pid"

    fc_cmd = [firecracker_bin, "--no-api", "--config-file", str(config_file)]
    if enable_api_socket and socket_path:
        # Use API socket mode instead
        fc_cmd = [
            firecracker_bin,
            "--api-sock",
            str(socket_path),
            "--config-file",
            str(config_file),
        ]

    try:
        with open(log_file, "w") as log_fp, open(console_log_file, "w") as console_fp:
            proc = subprocess.Popen(
                fc_cmd,
                stdout=console_fp,
                stderr=log_fp,
                start_new_session=True,
            )
    except FileNotFoundError:
        _cleanup_tap(tap_name)
        shutil.rmtree(vm_dir, ignore_errors=True)
        print_error(f"Firecracker binary not found: {firecracker_bin!r}")
        print_error("Install it and put it on $PATH, or set FCM_FIRECRACKER_BIN")
        raise typer.Exit(code=1)
    except OSError as e:
        _cleanup_tap(tap_name)
        shutil.rmtree(vm_dir, ignore_errors=True)
        print_error(f"Failed to start Firecracker: {e}")
        raise typer.Exit(code=1)

    pid_file.write_text(str(proc.pid))

    # --------------------------------------------------------------- register
    manager = VMManager()
    vm_instance = VMInstance(
        name=name,
        pid=proc.pid,
        socket_path=socket_path,
        ip=guest_ip,
        mac=guest_mac,
        created_at=datetime.now(),
        status=VMState.RUNNING,
    )
    manager.register(vm_instance)

    print_success(f"VM '{name}' started (PID {proc.pid})")
    print_info(f"  SSH ready in ~30-60s: fcm vm ssh --name {name}")
    print_info(f"  Logs: fcm vm logs --name {name} --type os --follow")


# ---------------------------------------------------------------------------
# remove (aliases: rm, delete)
# ---------------------------------------------------------------------------


@app.command(name="remove")
def remove(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    force: bool = typer.Option(False, "--force", "-f", help="Force kill and skip confirmation"),
) -> None:
    """Stop and remove a VM."""
    manager = VMManager()
    vm = manager.get(name)
    if not vm:
        print_error(f"VM '{name}' not found")
        raise typer.Exit(code=1)

    if not force:
        typer.confirm(f"Delete VM '{name}' (IP: {vm.ip})?", abort=True)

    vm_dir = get_vm_dir(name)
    tap_name = f"fc-{name}-0"

    # Graceful shutdown sequence
    _graceful_shutdown(vm.pid, vm.socket_path, vm.ip)

    # Teardown network
    remove_iptables_forward_rules(tap_name)
    try:
        delete_tap(tap_name)
    except NetworkError as e:
        print_error(f"Warning: failed to delete TAP {tap_name}: {e}")

    try:
        teardown_nat(force=False)
    except NetworkError as e:
        print_error(f"Warning: NAT teardown warning: {e}")

    # Release network IP lease
    for net_name in _find_network_for_vm(name):
        try:
            release_network_ip(net_name, name)
        except NetworkError:
            pass

    manager.deregister(name)

    if vm_dir.exists():
        shutil.rmtree(vm_dir)

    print_success(f"VM '{name}' removed")


# Aliases
@app.command(name="rm", hidden=True)
def rm(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    force: bool = typer.Option(False, "--force", "-f", help="Force kill and skip confirmation"),
) -> None:
    """Alias for remove."""
    remove(name=name, force=force)


@app.command(name="delete", hidden=True)
def delete(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    force: bool = typer.Option(False, "--force", "-f", help="Force kill and skip confirmation"),
) -> None:
    """Alias for remove."""
    remove(name=name, force=force)


# ---------------------------------------------------------------------------
# list
# ---------------------------------------------------------------------------


@app.command(name="list")
def list_vms(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    all_vms: bool = typer.Option(False, "--all", "-a", help="Show all VMs including stopped"),
) -> None:
    """List running and stopped VMs."""
    manager = VMManager()
    vms = manager.list_all()

    if not all_vms:
        vms = [v for v in vms if v.status == VMState.RUNNING]

    if json_output:
        data = [
            {
                "name": v.name,
                "ip": v.ip,
                "mac": v.mac,
                "status": v.status.value,
                "pid": v.pid,
                "created_at": v.created_at.isoformat(),
            }
            for v in vms
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    if not vms:
        print_info("No VMs found." + (" Use --all to include stopped VMs." if not all_vms else ""))
        return

    table = Table(title="Firecracker VMs")
    table.add_column("Name", style="cyan", no_wrap=True)
    table.add_column("IP", style="green")
    table.add_column("Status", style="bold")
    table.add_column("PID")
    table.add_column("Created")

    status_colors = {
        VMState.RUNNING: "[green]running[/green]",
        VMState.STOPPED: "[dim]stopped[/dim]",
        VMState.PAUSED: "[yellow]paused[/yellow]",
        VMState.ERROR: "[red]error[/red]",
    }

    for v in vms:
        status_str = status_colors.get(v.status, v.status.value)
        created = v.created_at.strftime("%Y-%m-%d %H:%M") if v.created_at else "-"
        table.add_row(
            v.name,
            v.ip or "-",
            status_str,
            str(v.pid) if v.pid else "-",
            created,
        )

    console.print(table)


# ---------------------------------------------------------------------------
# ssh
# ---------------------------------------------------------------------------


@app.command()
def ssh(
    name: str = typer.Option(..., "--name", "-n", help="VM name or IP address"),
    user: str = typer.Option("root", "--user", "-u", help="SSH user"),
    key: Path | None = typer.Option(None, "--key", help="SSH key path"),
    cmd: str | None = typer.Option(None, "--cmd", "-c", help="Command to execute"),
) -> None:
    """Open an SSH session into a VM."""
    exit_code = connect_to_vm(
        vm_name_or_ip=name,
        user=user,
        key_path=key,
        command=cmd,
        exec_mode=cmd is None,
    )
    raise typer.Exit(code=exit_code)


# ---------------------------------------------------------------------------
# logs
# ---------------------------------------------------------------------------


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
    exit_code = show_logs(
        vm_name=name,
        log_type=log_type,
        lines=lines,
        follow=follow,
    )
    raise typer.Exit(code=exit_code)


# ---------------------------------------------------------------------------
# cleanup
# ---------------------------------------------------------------------------


@app.command()
def cleanup(
    all_vms: bool = typer.Option(False, "--all", help="Remove all VMs, not just stopped"),
    dry_run: bool = typer.Option(
        False, "--dry-run", help="Show what would be removed without deleting"
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove stopped VMs and stale directories."""
    manager = VMManager()
    vms = manager.list_all()

    if all_vms:
        targets = vms
    else:
        targets = [v for v in vms if v.status != VMState.RUNNING]

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

    for v in targets:
        vm_dir = get_vm_dir(v.name)
        tap_name = f"fc-{v.name}-0"

        if v.pid:
            try:
                os.kill(v.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass

        remove_iptables_forward_rules(tap_name)
        try:
            delete_tap(tap_name)
        except NetworkError:
            pass

        manager.deregister(v.name)

        if vm_dir.exists():
            shutil.rmtree(vm_dir)

        print_success(f"Removed VM '{v.name}'")

    try:
        teardown_nat(force=False)
    except NetworkError:
        pass


# ---------------------------------------------------------------------------
# pause / resume / snapshot / load
# ---------------------------------------------------------------------------


@app.command()
def pause(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
) -> None:
    """Pause a running VM (not supported in this version)."""
    print_info("VM pause/resume is not supported by this version of fcm.")
    raise typer.Exit(code=0)


@app.command()
def resume(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
) -> None:
    """Resume a paused VM (not supported in this version)."""
    print_info("VM pause/resume is not supported by this version of fcm.")
    raise typer.Exit(code=0)


@app.command()
def snapshot(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    mem_out: Path = typer.Option(..., "--mem-out", help="Memory snapshot output path"),
    state_out: Path = typer.Option(..., "--state-out", help="VM state output path"),
) -> None:
    """Snapshot VM memory and disk state. Requires --enable-api-socket."""
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        print_error(f"Socket not found for VM '{name}'")
        print_error("VM must be running with --enable-api-socket")
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
    resume_after: bool = typer.Option(True, "--resume/--no-resume", help="Resume VM after loading"),
) -> None:
    """Load VM from snapshot. Requires --enable-api-socket."""
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        print_error(f"Socket not found for VM '{name}'")
        print_error("VM must be running with --enable-api-socket")
        raise typer.Exit(code=1)

    client = FirecrackerClient(socket_path)
    try:
        if client.load_snapshot(mem_in, state_in, resume_after):
            raise typer.Exit(code=0)
        else:
            raise typer.Exit(code=1)
    finally:
        client.close()


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _graceful_shutdown(pid: int | None, socket_path: Path | None, vm_ip: str | None) -> None:
    """Gracefully shut down a VM process.

    Sequence:
    1. Send Ctrl+Alt+Del via API socket (best-effort), then wait up to 5s.
    2. If still alive: SIGTERM, wait 1s.
    3. If still alive: SIGKILL.
    4. Run ssh-keygen -R <ip> to remove host key from known_hosts.
    """
    if pid is None:
        return

    def _is_alive(p: int) -> bool:
        try:
            os.kill(p, 0)
            return True
        except (ProcessLookupError, PermissionError):
            return False

    # Step 1: send Ctrl+Alt+Del via API socket
    if socket_path is not None and Path(socket_path).exists():
        try:
            client = FirecrackerClient(Path(socket_path))
            client.send_ctrl_alt_del()
            client.close()
        except Exception:
            pass  # best-effort
        # Wait up to 5 seconds for graceful exit
        for _ in range(50):
            time.sleep(0.1)
            if not _is_alive(pid):
                break

    # Step 2: SIGTERM
    if _is_alive(pid):
        try:
            os.kill(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
        time.sleep(1.0)

    # Step 3: SIGKILL
    if _is_alive(pid):
        try:
            os.kill(pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass

    # Step 4: remove VM IP from known_hosts
    if vm_ip is not None:
        subprocess.run(
            ["ssh-keygen", "-R", vm_ip],
            capture_output=True,
            check=False,
        )


def _resolve_ssh_key(ssh_key: str | None) -> str | None:
    """Resolve an SSH key from name (key cache) or file path.

    Returns the public key content string, or None.
    When ssh_key is explicitly named but not found, prints available keys and exits.
    """
    if ssh_key is None:
        # Fall back to any key in cache
        keys_dir = get_cache_dir() / "keys"
        if keys_dir.exists():
            for pub in keys_dir.glob("*.pub"):
                return pub.read_text().strip()
        return None

    # Check key cache first
    keys_dir = get_cache_dir() / "keys"
    cache_key = keys_dir / f"{ssh_key}.pub"
    if cache_key.exists():
        return cache_key.read_text().strip()

    # Treat as file path
    key_path = Path(ssh_key)
    if key_path.exists():
        return key_path.read_text().strip()

    # Key not found — list available keys and exit
    from fcm.core.key_manager import list_keys

    available = list_keys()
    if available:
        names = ", ".join(k.name for k in available)
        print_error(f"SSH key '{ssh_key}' not found in cache or filesystem.")
        print_error(f"Available keys: {names}")
    else:
        print_error(f"SSH key '{ssh_key}' not found in cache or filesystem.")
        print_error("No keys in cache. Add one with: fcm key add <name> <path>")
    raise typer.Exit(code=1)


def _write_cloud_init(
    cloud_init_dir: Path,
    vm_name: str,
    guest_ip: str,
    user: str,
    ssh_pub_key: str | None = None,
    custom_user_data: Path | None = None,
) -> None:
    """Write cloud-init seed files (meta-data, network-config, user-data)."""
    # meta-data
    meta_data = f"instance-id: {vm_name}\nlocal-hostname: {vm_name}\n"
    (cloud_init_dir / "meta-data").write_text(meta_data)

    # network-config (static IP)
    network_config = (
        "version: 1\n"
        "config:\n"
        "  - type: physical\n"
        "    name: eth0\n"
        "    subnets:\n"
        "      - type: static\n"
        f"        address: {guest_ip}/24\n"
        "        gateway: 10.20.0.1\n"
        "        dns_nameservers:\n"
        "          - 8.8.8.8\n"
        "          - 1.1.1.1\n"
    )
    (cloud_init_dir / "network-config").write_text(network_config)

    # user-data
    if custom_user_data is not None:
        # Use custom user-data, potentially merging SSH key
        content = custom_user_data.read_text()
        if ssh_pub_key and "ssh_authorized_keys" not in content:
            # Inject SSH key block
            content += (
                f"\nusers:\n"
                f"  - name: {user}\n"
                f"    ssh-authorized-keys:\n"
                f"      - {ssh_pub_key}\n"
            )
        elif ssh_pub_key and "ssh_authorized_keys" in content:
            # Append to existing ssh_authorized_keys section
            content = content.replace(
                "ssh_authorized_keys:",
                f"ssh_authorized_keys:\n      - {ssh_pub_key}",
                1,
            )
        (cloud_init_dir / "user-data").write_text(content)
    else:
        # Generate default user-data
        ssh_section = ""
        if ssh_pub_key:
            ssh_section = (
                f"  - name: {user}\n"
                "    groups: sudo\n"
                "    shell: /bin/bash\n"
                "    sudo: ALL=(ALL) NOPASSWD:ALL\n"
                "    ssh-authorized-keys:\n"
                f"      - {ssh_pub_key}\n"
            )

        ud = (
            "#cloud-config\n"
            "users:\n"
            "  - default\n"
            f"{ssh_section}"
            "package_update: false\n"
            "package_upgrade: false\n"
            "runcmd:\n"
            "  - systemctl disable --now snapd.socket 2>/dev/null || true\n"
            "final_message: 'fcm cloud-init done'\n"
        )
        (cloud_init_dir / "user-data").write_text(ud)


def _inject_cloud_init(rootfs_path: Path, cloud_init_dir: Path) -> None:
    """Loop-mount rootfs and inject cloud-init seed files.

    Requires root. Falls back gracefully if loop mount fails.
    """
    import tempfile

    seed_target = "/var/lib/cloud/seed/nocloud"
    mount_point = Path(tempfile.mkdtemp(prefix="fcm-mount-"))

    try:
        # Mount the rootfs ext4 image
        subprocess.run(
            ["mount", "-o", "loop", str(rootfs_path), str(mount_point)],
            check=True,
            capture_output=True,
        )
        try:
            target = mount_point / seed_target.lstrip("/")
            target.mkdir(parents=True, exist_ok=True)
            for f in cloud_init_dir.iterdir():
                shutil.copy2(f, target / f.name)
        finally:
            subprocess.run(
                ["umount", str(mount_point)],
                check=False,
                capture_output=True,
            )
    except subprocess.CalledProcessError as e:
        print_error(f"Warning: could not inject cloud-init (requires root): {e}")
        print_info("VM will boot without cloud-init pre-seeding")
    finally:
        try:
            mount_point.rmdir()
        except OSError:
            pass


def _cleanup_tap(tap_name: str) -> None:
    """Best-effort TAP cleanup, ignores errors."""
    try:
        remove_iptables_forward_rules(tap_name)
        delete_tap(tap_name)
    except (NetworkError, Exception):
        pass


def _find_network_for_vm(vm_name: str) -> list[str]:
    """Find which networks have a lease for this VM."""
    from fcm.core.network_manager import get_network_leases, list_networks

    result: list[str] = []
    for net in list_networks():
        leases = get_network_leases(net.name)
        if any(lease.vm_name == vm_name for lease in leases):
            result.append(net.name)
    return result
