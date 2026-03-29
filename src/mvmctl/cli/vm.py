import json
from pathlib import Path
from typing import TYPE_CHECKING, Any, List, Optional, Union

import typer

if TYPE_CHECKING:
    from mvmctl.core.config import VMDefaultsConfig

from mvmctl.api.vm_config import build_vm_config_file, load_vm_config_file, merge_cli_overrides
from mvmctl.api.vms import (
    cleanup_vms,
    create_vm,
    get_logs,
    get_vm_status_with_exit_code,
    list_vms,
    load_snapshot,
    remove_vm,
    resolve_image_short_id_path,
    resolve_kernel_short_id_path,
    snapshot_vm,
    ssh_vm,
)
from mvmctl.cli._helpers import get_state_marker, is_file_missing, is_vm_process_running
from mvmctl.constants import (
    DEFAULT_FIRECRACKER_BIN,
    DEFAULT_NETWORK_NAME,
    DEFAULT_SNAPSHOT_RESUME,
    DEFAULT_VM_LOG_FOLLOW,
    DEFAULT_VM_LOG_LINES,
    DEFAULT_VM_LOG_TYPE,
)
from mvmctl.exceptions import MVMError
from mvmctl.models import CloudInitMode, VMInstance, VMState
from mvmctl.utils.console import print_error, print_info, print_success, print_table
from mvmctl.utils.fs import get_vm_dir_by_hash as get_vm_dir  # noqa: F401
from mvmctl.utils.fs import get_vms_dir  # noqa: F401
from mvmctl.utils.time import human_readable_time
from mvmctl.utils.validation import is_ip_address, validate_entity_name

# Sentinel for auto-generation mode
USE_ISO_AUTO = "__use_iso_auto__"

app = typer.Typer(
    help="VM lifecycle management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


def _cloud_init_iso_callback(value: Union[str, bool, None]) -> Optional[Path]:
    """Normalize --cloud-init-iso option value.

    Returns:
        None: Don't use ISO mode
        USE_ISO_AUTO: Use ISO mode with auto-generation
        Path: Use custom ISO at that path
    """
    if value is None:
        return None
    if value is True or value == "":
        return USE_ISO_AUTO  # type: ignore[return-value]
    return Path(value)  # type: ignore[arg-type]


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


def _resolve_default_network() -> str:
    """Resolve the default network from metadata, falling back to 'default'."""
    from mvmctl.api.metadata import get_default_network_entry

    entry = get_default_network_entry()
    if entry is not None:
        return entry[0]  # network name
    return DEFAULT_NETWORK_NAME


@app.command()
def create(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    image: Optional[str] = typer.Option(
        None,
        "--image",
        help="Image short ID (same discovery behavior as 'mvm image rm')",
    ),
    kernel: Optional[str] = typer.Option(
        None,
        "--kernel",
        help="Kernel short ID (same discovery behavior as 'mvm kernel rm')",
    ),
    image_path: Optional[Path] = typer.Option(
        None, "--image-path", help="Direct path to rootfs image file (overrides --image)"
    ),
    kernel_path: Optional[Path] = typer.Option(
        None, "--kernel-path", help="Direct path to vmlinux kernel file (overrides --kernel)"
    ),
    vcpus: Optional[int] = typer.Option(
        None, "--vcpus", "--cpus", help="Number of vCPUs (default: from user config)"
    ),
    mem: Optional[int] = typer.Option(
        None, "--mem", "--memory", help="Memory in MiB (default: from user config)"
    ),
    disk_size: Optional[str] = typer.Option(
        None,
        "--disk-size",
        "-s",
        help="Rootfs disk size (e.g., 512M, 1G, 2.5GB). Default from config.",
    ),
    ip: Optional[str] = typer.Option(None, "--ip", help="Guest IP (auto-assigned if omitted)"),
    network_name: Optional[str] = typer.Option(
        None, "--network", "--net", help="Named network to use"
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
    cloud_init_iso: Optional[Path] = typer.Option(
        None,
        "--cloud-init-iso",
        help="Use ISO mode (optionally specify custom ISO path)",
        callback=_cloud_init_iso_callback,
    ),
    no_cloud_init: bool = typer.Option(
        False,
        "--no-cloud-init",
        help="Disable cloud-init injection entirely",
    ),
    nocloud_net: bool = typer.Option(
        False,
        "--nocloud-net",
        help="Use nocloud-net HTTP datasource (default mode; use --cloud-init-iso for ISO mode)",
    ),
    cloud_init_mode: Optional[str] = typer.Option(
        None,
        "--cloud-init-mode",
        help="Cloud-init mode: 'auto' (default, uses nocloud-net), 'iso' (ISO mode), 'nocloud-net' (HTTP), 'direct' (direct injection), 'disabled' (no cloud-init). Use --cloud-init-iso for custom ISO.",
    ),
    nocloud_net_port: Optional[int] = typer.Option(
        None,
        "--nocloud-net-port",
        help="Port for nocloud-net HTTP server (0 for auto-assign, default: auto-assign)",
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
    no_console: bool = typer.Option(
        False,
        "--no-console",
        help="Disable serial console",
    ),
    firecracker_bin: Optional[str] = typer.Option(
        None,
        "--firecracker-bin",
        envvar="MVM_FIRECRACKER_BIN",
        help="Path to firecracker binary (default: active version from mvm bin use)",
    ),
    keep_cloud_init_iso: bool = typer.Option(
        False,
        "--keep-cloud-init-iso",
        help="Keep cloud-init ISO file after VM starts (for debugging)",
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

    # Variables for path resolution
    resolved_image_path: Path | None = None
    image_id_for_lookup: str = ""
    resolved_kernel_path: Path | None = None
    kernel_id_for_lookup: str | None = None
    effective_api_socket: bool = (
        enable_api_socket if enable_api_socket is not None else _defaults.enable_api_socket
    )
    effective_pci: bool = enable_pci if enable_pci is not None else _defaults.enable_pci
    effective_network: str = (
        network_name if network_name is not None else _resolve_default_network()
    )

    # Check mutual exclusivity of --no-cloud-init and --cloud-init-iso
    if no_cloud_init and cloud_init_iso is not None:
        print_error("--no-cloud-init and --cloud-init-iso are mutually exclusive")
        raise typer.Exit(code=1)

    # Mutual exclusivity check for image options
    if image is not None and image_path is not None:
        print_error("--image and --image-path are mutually exclusive")
        raise typer.Exit(code=1)

    # Mutual exclusivity check for kernel options
    if kernel is not None and kernel_path is not None:
        print_error("--kernel and --kernel-path are mutually exclusive")
        raise typer.Exit(code=1)

    # Path validation and resolution
    if image_path is not None:
        if not image_path.exists():
            print_error(f"Image path not found: {image_path}")
            raise typer.Exit(code=1)
        if not image_path.is_file():
            print_error(f"Image path is not a file: {image_path}")
            raise typer.Exit(code=1)
        resolved_image_path = image_path
        image_id_for_lookup = image if image else str(image_path)
    elif image is None:
        image = _resolve_default_image()
        if image is None:
            print_error(
                "No --image specified and no default image set. "
                "Use 'mvm image fetch <name>' then 'mvm image set-default <name>', or pass --image."
            )
            raise typer.Exit(code=1)
        resolved_image_path = resolve_image_short_id_path(image)
        image_id_for_lookup = image if image else str(resolved_image_path)
    else:
        try:
            resolved_image_path = resolve_image_short_id_path(image)
            image_id_for_lookup = image if image else str(resolved_image_path)
        except MVMError:
            print_error(
                f"Image short ID '{image}' was not found or is ambiguous. "
                "Use the same short ID format accepted by 'mvm image rm'."
            )
            raise typer.Exit(code=1)

    # Kernel path validation and resolution
    if kernel_path is not None:
        if not kernel_path.exists():
            print_error(f"Kernel path not found: {kernel_path}")
            raise typer.Exit(code=1)
        if not kernel_path.is_file():
            print_error(f"Kernel path is not a file: {kernel_path}")
            raise typer.Exit(code=1)
        resolved_kernel_path = kernel_path
        kernel_id_for_lookup = kernel if kernel else str(kernel_path)
    elif kernel is None:
        kernel = _resolve_default_kernel()
        resolved_kernel_path = Path(kernel) if kernel is not None else None
        kernel_id_for_lookup = kernel
    else:
        try:
            resolved_kernel_path = resolve_kernel_short_id_path(kernel)
            kernel_id_for_lookup = kernel if kernel else str(resolved_kernel_path)
        except MVMError:
            print_error(
                f"Kernel short ID '{kernel}' was not found or is ambiguous. "
                "Use the same short ID format accepted by 'mvm kernel rm'."
            )
            raise typer.Exit(code=1)

    effective_bin = firecracker_bin or _resolve_active_firecracker_bin()

    if output_config is not None:
        # Keep this at the end of config computation so we provide the latest configuration to the user.
        vm_config = build_vm_config_file(
            name=name,
            image=str(resolved_image_path),
            kernel=str(resolved_kernel_path) if resolved_kernel_path else None,
            vcpus=effective_vcpus,
            mem=effective_mem,
            ip=ip,
            network=effective_network,
            mac=mac,
            ssh_key=ssh_key,
            user=effective_user,
            enable_api_socket=effective_api_socket,
            enable_pci=effective_pci,
            firecracker_bin=effective_bin,
            rootfs_path=resolved_image_path,
            gateway=None,
            subnet_mask=None,
            tap_device=None,
        )
        vm_config.to_json_file(output_config)
        print_info(f"VM config written to: {output_config}")
        return

    # Validate cloud_init_iso path if it's a custom path (not USE_ISO_AUTO)
    if cloud_init_iso is not None and cloud_init_iso != USE_ISO_AUTO:  # type: ignore[comparison-overlap]
        custom_path = Path(cloud_init_iso)
        if not custom_path.exists():
            print_error(f"Cloud-init ISO not found: {custom_path}")
            raise typer.Exit(code=1)

    # Check mutual exclusivity of cloud-init flags
    cloud_init_flags = sum(
        [
            no_cloud_init,
            cloud_init_iso is not None and cloud_init_iso != USE_ISO_AUTO,  # type: ignore[comparison-overlap]
            cloud_init_iso == USE_ISO_AUTO,  # type: ignore[comparison-overlap]
            nocloud_net,
            cloud_init_mode is not None,
        ]
    )
    if cloud_init_flags > 1:
        print_error(
            "Only one of --cloud-init-mode, --no-cloud-init, --cloud-init-iso, or --nocloud-net can be specified"
        )
        raise typer.Exit(code=1)

    # Validate --cloud-init-mode if provided
    if cloud_init_mode is not None:
        mode_lower = cloud_init_mode.lower()
        valid_modes = ["auto", "iso", "direct", "disabled", "nocloud-net"]
        if mode_lower not in valid_modes:
            print_error(
                f"Invalid --cloud-init-mode '{cloud_init_mode}'. Valid modes: {', '.join(valid_modes)}"
            )
            raise typer.Exit(code=1)

    # Determine cloud_init_mode based on flags
    if cloud_init_mode is not None:
        # New --cloud-init-mode flag takes precedence
        mode_lower = cloud_init_mode.lower()
        if mode_lower == "disabled":
            effective_cloud_init_mode = CloudInitMode.DISABLED
            effective_cloud_init_iso_path: Path | None = None
        elif mode_lower == "iso":
            effective_cloud_init_mode = CloudInitMode.ISO
            effective_cloud_init_iso_path = None  # Auto-generate ISO
        elif mode_lower == "nocloud-net":
            effective_cloud_init_mode = CloudInitMode.NO_CLOUD_NET
            effective_cloud_init_iso_path = None
        elif mode_lower == "direct":
            effective_cloud_init_mode = CloudInitMode.DIRECT_INJECTION
            effective_cloud_init_iso_path = None
        else:  # "auto"
            effective_cloud_init_mode = CloudInitMode.AUTO
            effective_cloud_init_iso_path = None
    elif no_cloud_init:
        effective_cloud_init_mode = CloudInitMode.DISABLED
        effective_cloud_init_iso_path = None
    elif cloud_init_iso is not None:
        effective_cloud_init_mode = CloudInitMode.CUSTOM
        # USE_ISO_AUTO means auto-generate (None path), otherwise use custom path
        effective_cloud_init_iso_path = (
            None
            if cloud_init_iso == USE_ISO_AUTO  # type: ignore[comparison-overlap]
            else Path(cloud_init_iso)
        )
    elif nocloud_net:
        effective_cloud_init_mode = CloudInitMode.NO_CLOUD_NET
        effective_cloud_init_iso_path = None
    else:
        effective_cloud_init_mode = CloudInitMode.AUTO
        effective_cloud_init_iso_path = None

    try:
        vm = create_vm(
            name=name,
            image=image_id_for_lookup,
            kernel=kernel_id_for_lookup,
            image_path=resolved_image_path if image_path else None,
            kernel_path=resolved_kernel_path if kernel_path else None,
            vcpus=effective_vcpus,
            mem=effective_mem,
            disk_size=disk_size,
            ip=ip,
            network_name=effective_network,
            mac=mac,
            ssh_key=ssh_key,
            user_data=user_data,
            user=effective_user,
            enable_api_socket=effective_api_socket,
            enable_pci=effective_pci,
            enable_console=not no_console,
            firecracker_bin=effective_bin,
            cloud_init_mode=effective_cloud_init_mode,
            cloud_init_iso_path=effective_cloud_init_iso_path,
            keep_cloud_init_iso=keep_cloud_init_iso,
            nocloud_net_port=nocloud_net_port if nocloud_net_port is not None else 0,
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
        data = []
        for v in vms:
            status_str, exit_code = get_vm_status_with_exit_code(v)
            data.append(
                {
                    "id": v.id[:6] if v.id else "-",
                    "name": v.name,
                    "ip": v.ip,
                    "mac": v.mac,
                    "status": status_str,
                    "pid": v.pid,
                    "exit_code": exit_code,
                    "api_socket": v.socket_path is not None,
                    "network": v.network_name or "-",
                    "created_at": v.created_at.isoformat(),
                }
            )
        typer.echo(json.dumps(data, indent=2))
        return

    if not vms:
        print_info("No VMs found." + (" Use --all to include stopped VMs." if not all_vms else ""))
        return

    rows = []
    for v in vms:
        vm_dir = get_vm_dir(v.id) if v.id else None
        dir_missing = is_file_missing(vm_dir)
        process_running = is_vm_process_running(v.pid)
        # VM is "missing" if directory missing OR (status says running but PID not running)
        is_missing = dir_missing or (v.status.value == "running" and not process_running)
        state_marker = get_state_marker(is_missing)
        status_str, _ = get_vm_status_with_exit_code(v)
        rows.append(
            [
                state_marker,
                v.id[:6] if v.id else "-",
                v.name,
                v.ip or "-",
                status_str,
                str(v.pid) if v.pid else "-",
                "on" if v.socket_path else "off",
                human_readable_time(v.created_at.isoformat()) if v.created_at else "-",
            ]
        )
    print_table(
        title="Firecracker VMs",
        columns=["State", "ID", "Name", "IP", "Status", "PID", "API", "Created"],
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


def _do_prune(all_vms: bool, dry_run: bool) -> None:
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

    cleanup_vms(all_vms=all_vms, dry_run=False)
    for v in targets:
        print_success(f"Removed VM '{v.name}'")


@app.command()
def prune(
    all_vms: bool = typer.Option(False, "--all", help="Remove all VMs, not just stopped"),
    dry_run: bool = typer.Option(
        False, "--dry-run", help="Show what would be removed without deleting"
    ),
) -> None:
    """Remove stopped VMs and stale directories."""
    _do_prune(all_vms=all_vms, dry_run=dry_run)


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


@app.command()
def inspect(
    name: str = typer.Option(..., "--name", "-n", help="VM name or short ID"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a VM.

    Examples:
        mvm vm inspect --name myvm
        mvm vm inspect --name myvm --json
    """
    from mvmctl.api.vms import inspect_vm

    try:
        vm_info = inspect_vm(name)
    except MVMError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if json_output:
        typer.echo(json.dumps(vm_info, indent=2, default=str))
    else:
        _print_vm_details(vm_info)


def _print_vm_details(info: dict[str, Any]) -> None:
    """Print formatted VM details."""
    print_table(
        title=f"VM: {info['name']}",
        columns=["Property", "Value"],
        rows=[
            ["ID", info["short_id"]],
            ["Full ID", info["id"]],
            ["Status", info["status"]],
            ["Created", info["created_at"]],
            ["PID", str(info["pid"]) if info["pid"] else "-"],
            ["IP", info["ip"] or "-"],
            ["MAC", info["mac"] or "-"],
            ["Network", info["network_name"] or "-"],
            ["TAP Device", info["tap_device"] or "-"],
            ["API Socket", "enabled" if info["features"]["api_socket"] else "disabled"],
            ["Console", "enabled" if info["features"]["console"] else "disabled"],
            ["NoCloud-net", "enabled" if info["features"]["nocloud_net"] else "disabled"],
        ],
    )

    print_info("\nPaths:")
    print_info(f"  VM Directory: {info['paths']['vm_dir']}")
    if info["paths"]["rootfs"]:
        print_info(f"  Rootfs: {info['paths']['rootfs']}")
    if info["paths"]["config"]:
        print_info(f"  Config: {info['paths']['config']}")
