import json
from pathlib import Path
from typing import TYPE_CHECKING, Any, List, Optional, Union

import typer

if TYPE_CHECKING:
    from mvmctl.core.config import VMDefaultsConfig

from mvmctl.api.network import check_ip_available
from mvmctl.api.vm_config import build_vm_config_file, load_vm_config_file, merge_cli_overrides
from mvmctl.api.vms import (
    create_vm,
    get_vm_status_with_exit_code,
    list_vms,
    load_snapshot,
    pause_vm,
    reboot_vm,
    remove_vm,
    resolve_image_multi_strategy,
    resolve_kernel_multi_strategy,
    resolve_vm_selector,
    resume_vm,
    snapshot_vm,
    start_vm,
    stop_vm,
)
from mvmctl.cli._helpers import get_state_marker, is_file_missing, is_vm_process_running
from mvmctl.constants import (
    DEFAULT_CLOUD_INIT_FINAL_MESSAGE,
    DEFAULT_CLOUD_INIT_KERNEL_CMDLINE_DS,
    DEFAULT_CLOUD_INIT_SEED_PATH,
    DEFAULT_NETWORK_NAME,
)
from mvmctl.exceptions import MVMError
from mvmctl.models import CloudInitMode, VMInstance
from mvmctl.utils.console import print_error, print_info, print_success, print_table
from mvmctl.utils.error_handler import handle_mvm_error
from mvmctl.utils.fs import get_vm_dir_by_hash as get_vm_dir  # noqa: F401
from mvmctl.utils.fs import get_vms_dir  # noqa: F401
from mvmctl.utils.time import human_readable_time

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
    from mvmctl.api.assets import get_binary_path
    from mvmctl.exceptions import AssetNotFoundError

    try:
        return get_binary_path("firecracker")
    except AssetNotFoundError as e:
        print_error(str(e))
        raise typer.Exit(1) from e


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
        help="Image name (e.g., ubuntu-24.04), short ID, or path to .ext4 file",
    ),
    kernel: Optional[str] = typer.Option(
        None,
        "--kernel",
        help="Kernel short ID or path to vmlinux file",
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
        help="Cloud-init mode: 'inject' (default, direct injection), 'iso' (ISO mode), 'net' (HTTP), 'off' (no cloud-init)",
    ),
    nocloud_net_port: Optional[int] = typer.Option(
        None,
        "--nocloud-net-port",
        help="Port for nocloud-net HTTP server (0 for auto-assign, default: auto-assign)",
    ),
    user: Optional[str] = typer.Option(
        None, "--user", help="Default SSH user for cloud-init (default: from user config)"
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
            handle_mvm_error(e)

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
    effective_api_socket: bool = True
    effective_pci: bool = enable_pci if enable_pci is not None else _defaults.enable_pci
    effective_network: str = (
        network_name if network_name is not None else _resolve_default_network()
    )
    # Check IP availability before VM creation
    if ip is not None:
        check_ip_available(effective_network, ip)

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
        resolved_image_path = resolve_image_multi_strategy(image)
        image_id_for_lookup = image if image else str(resolved_image_path)
    else:
        try:
            resolved_image_path = resolve_image_multi_strategy(image)
            image_id_for_lookup = image if image else str(resolved_image_path)
        except MVMError as e:
            handle_mvm_error(e)

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
            resolved_kernel_path = resolve_kernel_multi_strategy(kernel)
            kernel_id_for_lookup = kernel if kernel else str(resolved_kernel_path)
        except MVMError as e:
            handle_mvm_error(e)

    effective_bin = firecracker_bin or _resolve_active_firecracker_bin()

    # Determine cloud_init_mode based on flags (must be before output_config handling)
    if cloud_init_mode is not None:
        mode_lower = cloud_init_mode.lower()
        if mode_lower == "off":
            effective_cloud_init_mode = CloudInitMode.OFF
            effective_cloud_init_iso_path: Path | None = None
        elif mode_lower == "iso":
            effective_cloud_init_mode = CloudInitMode.ISO
            effective_cloud_init_iso_path = None
        elif mode_lower == "net":
            effective_cloud_init_mode = CloudInitMode.NET
            effective_cloud_init_iso_path = None
        elif mode_lower == "inject":
            effective_cloud_init_mode = CloudInitMode.INJECT
            effective_cloud_init_iso_path = None
        else:  # default to INJECT
            effective_cloud_init_mode = CloudInitMode.INJECT
            effective_cloud_init_iso_path = None
    elif no_cloud_init:
        effective_cloud_init_mode = CloudInitMode.OFF
        effective_cloud_init_iso_path = None
    elif cloud_init_iso is not None:
        effective_cloud_init_mode = CloudInitMode.ISO
        effective_cloud_init_iso_path = (
            None
            if cloud_init_iso == USE_ISO_AUTO  # type: ignore[comparison-overlap]
            else Path(cloud_init_iso)
        )
    elif nocloud_net:
        effective_cloud_init_mode = CloudInitMode.NET
        effective_cloud_init_iso_path = None
    else:
        effective_cloud_init_mode = CloudInitMode.INJECT
        effective_cloud_init_iso_path = None

    if output_config is not None:
        # Keep this at the end of config computation so we provide the latest configuration to the user.
        cloud_init_config: dict[str, Any] = {
            "mode": effective_cloud_init_mode.value,
            "seed_path": str(DEFAULT_CLOUD_INIT_SEED_PATH),
            "kernel_cmdline_ds": DEFAULT_CLOUD_INIT_KERNEL_CMDLINE_DS,
            "final_message": DEFAULT_CLOUD_INIT_FINAL_MESSAGE,
            "user_data": str(user_data) if user_data else None,
            "iso_path": str(effective_cloud_init_iso_path)
            if effective_cloud_init_iso_path
            else None,
            "enabled": not no_cloud_init,
        }
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
            ipv4_gateway=None,
            subnet_mask=None,
            tap_device=None,
            cloud_init=cloud_init_config,
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
        valid_modes = ["inject", "iso", "inject", "off", "net"]
        if mode_lower not in valid_modes:
            print_error(
                f"Invalid --cloud-init-mode '{cloud_init_mode}'. Valid modes: {', '.join(valid_modes)}"
            )
            raise typer.Exit(code=1)

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
        handle_mvm_error(e)


@app.command(name="rm")
def rm(
    ids: Optional[List[str]] = typer.Argument(None, help="VM ID prefixes to remove"),
    name: Optional[List[str]] = typer.Option(
        None, "--name", "-n", help="VM name to remove (can be specified multiple times)"
    ),
) -> None:
    """Stop and remove VMs by ID prefix or name.

    Examples:
        # Remove by ID prefix:
        mvm vm rm abc123 def456

        # Remove by name (prompts if multiple with same name):
        mvm vm rm --name runner1 --name runner2
    """
    from mvmctl.api.vms import get_vm_manager as _get_vm_manager

    manager = _get_vm_manager()
    exit_code = 0
    targets: list[VMInstance] = []
    errors: list[str] = []

    effective_ids: list[str] = list(ids) if ids else []

    # Resolve ID prefixes
    for prefix in effective_ids:
        matches = manager.find_by_id_prefix(prefix)
        if len(matches) == 0:
            errors.append(f"No VM found with ID prefix '{prefix}'")
        elif len(matches) > 1:
            errors.append(f"Multiple VMs match ID prefix '{prefix}' — use a longer prefix or name")
        else:
            targets.append(matches[0])

    # Resolve names
    effective_names = name if name is not None else []
    for n in effective_names:
        matches = manager.get_by_name(n)
        if len(matches) == 0:
            errors.append(f"No VM found with name '{n}'")
        elif len(matches) > 1:
            print_error(
                f"Multiple VMs match name '{n}'. Use ID instead of name, or remove VMs individually."
            )
            print_info("Matching VMs:")
            for v in matches:
                print_info(
                    f"  - {v.name} (ID: {v.id}, IP: {v.ipv4 or '-'}, status: {v.status.value})"
                )
            exit_code = 1
            continue
        else:
            targets.append(matches[0])

    if not targets and not errors:
        print_error("Provide at least one VM ID prefix or --name")
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
            handle_mvm_error(e)

    if removed_count == 0 and targets:
        raise typer.Exit(code=exit_code)


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
                    "id": v.id if v.id else "-",
                    "name": v.name,
                    "ip": v.ipv4,
                    "mac": v.mac,
                    "status": status_str,
                    "pid": v.pid,
                    "exit_code": exit_code,
                    "api_socket": v.api_socket_path is not None,
                    "network": v.network_name or "-",
                    "created_at": v.created_at.isoformat(),
                }
            )
        typer.echo(json.dumps(data, indent=2))
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
                v.id if v.id else "-",
                v.name,
                v.ipv4 or "-",
                status_str,
                str(v.pid) if v.pid else "-",
                "on" if v.api_socket_path else "off",
                human_readable_time(v.created_at.isoformat()) if v.created_at else "-",
            ]
        )
    print_table(
        title="",
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
        handle_mvm_error(exc)


@app.command()
def load(
    name: str = typer.Option(..., "--name", "-n", help="VM name"),
    mem_in: Path = typer.Option(..., "--mem-in", help="Memory snapshot input path"),
    state_in: Path = typer.Option(..., "--state-in", help="VM state input path"),
    resume_after: bool = typer.Option(None, "--resume/--no-resume", help="Resume VM after loading"),
) -> None:
    """Load VM from snapshot. Requires --enable-api-socket."""
    from mvmctl.utils.validation import validate_entity_name

    validate_entity_name(name, "VM")
    try:
        load_snapshot(name=name, mem_in=mem_in, state_in=state_in, resume_after=resume_after)
        raise typer.Exit(code=0)
    except MVMError as exc:
        handle_mvm_error(exc)


@app.command()
def pause(
    selector: Optional[str] = typer.Argument(None, help="VM name or ID prefix"),
    name: Optional[str] = typer.Option(None, "--name", "-n", help="VM name or ID prefix"),
) -> None:
    """Pause a running VM."""
    effective = selector or name
    if not effective:
        print_error("Error: Must provide VM name via positional argument or --name")
        raise typer.Exit(code=1)

    try:
        resolved = resolve_vm_selector(effective)
        pause_vm(name=resolved)
        print_success(f"VM '{effective}' paused")
    except MVMError as exc:
        handle_mvm_error(exc)


@app.command()
def resume(
    selector: Optional[str] = typer.Argument(None, help="VM name or ID prefix"),
    name: Optional[str] = typer.Option(None, "--name", "-n", help="VM name or ID prefix"),
) -> None:
    """Resume a paused VM."""
    effective = selector or name
    if not effective:
        print_error("Error: Must provide VM name via positional argument or --name")
        raise typer.Exit(code=1)

    try:
        resolved = resolve_vm_selector(effective)
        resume_vm(name=resolved)
        print_success(f"VM '{effective}' resumed")
    except MVMError as exc:
        handle_mvm_error(exc)


@app.command()
def stop(
    selector: Optional[str] = typer.Argument(None, help="VM name or ID prefix"),
    name: Optional[str] = typer.Option(None, "--name", "-n", help="VM name or ID prefix"),
    force: bool = typer.Option(
        False, "--force", "-f", help="Force immediate shutdown without graceful shutdown"
    ),
) -> None:
    """Stop a running VM gracefully."""
    effective = selector or name
    if not effective:
        print_error("Error: Must provide VM name via positional argument or --name")
        raise typer.Exit(code=1)

    try:
        resolved = resolve_vm_selector(effective)
        stop_vm(name=resolved, force=force)
        print_success(f"VM '{effective}' stopped")
    except MVMError as exc:
        handle_mvm_error(exc)


@app.command()
def start(
    selector: Optional[str] = typer.Argument(None, help="VM name or ID prefix"),
    name: Optional[str] = typer.Option(None, "--name", "-n", help="VM name or ID prefix"),
) -> None:
    """Start a stopped VM."""
    effective = selector or name
    if not effective:
        print_error("Error: Must provide VM name via positional argument or --name")
        raise typer.Exit(code=1)

    try:
        resolved = resolve_vm_selector(effective)
        start_vm(name=resolved)
        print_success(f"VM '{effective}' started")
    except MVMError as exc:
        handle_mvm_error(exc)


@app.command()
def reboot(
    selector: Optional[str] = typer.Argument(None, help="VM name or ID prefix"),
    name: Optional[str] = typer.Option(None, "--name", "-n", help="VM name or ID prefix"),
    force: bool = typer.Option(
        False, "--force", "-f", help="Force immediate shutdown without graceful shutdown"
    ),
) -> None:
    """Reboot a VM (graceful stop then re-launch)."""
    effective = selector or name
    if not effective:
        print_error("Error: Must provide VM name via positional argument or --name")
        raise typer.Exit(code=1)

    try:
        resolved = resolve_vm_selector(effective)
        reboot_vm(name=resolved, force=force)
        print_success(f"VM '{effective}' rebooted")
    except MVMError as exc:
        handle_mvm_error(exc)


@app.command()
def inspect(
    selector: Optional[str] = typer.Argument(None, help="VM ID prefix or name"),
    name: Optional[str] = typer.Option(None, "--name", "-n", help="VM name or ID prefix"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    tree: bool = typer.Option(False, "--tree", help="Output in tree format"),
) -> None:
    """Show detailed information about a VM.

    Examples:
        mvm vm inspect myvm
        mvm vm inspect --name myvm
        mvm vm inspect myvm --json
        mvm vm inspect myvm --tree
    """
    from mvmctl.api.vms import inspect_vm

    effective_selector = selector or name
    if not effective_selector:
        print_error("Error: Must provide VM selector via positional argument or --name")
        raise typer.Exit(code=1)

    try:
        vm_info = inspect_vm(effective_selector)
    except MVMError as e:
        handle_mvm_error(e)

    if json_output:
        typer.echo(json.dumps(vm_info, indent=2, default=str))
    elif tree:
        _print_vm_details_tree(vm_info)
    else:
        _print_vm_details(vm_info)


def _print_vm_details(info: dict[str, Any]) -> None:
    from mvmctl.api.metadata import find_images_by_id_prefix, find_kernels_by_id_prefix
    from mvmctl.utils.console import (
        format_timestamp,
        print_inspect_header,
        print_key_value,
        print_section_header,
    )
    from mvmctl.utils.fs import get_cache_dir

    name = info.get("name", "-")
    status = info.get("status", "-")

    created_str = format_timestamp(info.get("created_at"))

    disk_size_str = "-"
    rootfs_path = info.get("paths", {}).get("rootfs")
    if rootfs_path:
        try:
            rootfs_size = Path(rootfs_path).stat().st_size
            disk_size_gb = rootfs_size / (1024**3)
            disk_size_str = f"{disk_size_gb:.1f}G"
        except (OSError, ValueError):
            pass

    cloud_init_mode = info.get("cloud_init_mode", "inject").lower()
    features = info.get("features", {})

    print_inspect_header(f"VM: {name}", status)

    print_section_header("BASIC INFO")
    print_key_value("Name", name)
    print_key_value("Full ID", info.get("id", "-"))
    print_key_value("Created", created_str)
    print_key_value("PID", info.get("pid") or "-")
    print_key_value("IP", info.get("ip") or "-")
    print_key_value("MAC", info.get("mac") or "-")
    print_key_value("Network", info.get("network_name") or "-")
    print_key_value("TAP Device", info.get("tap_device") or "-")

    print_section_header("RESOURCES")
    print_key_value("Disk Size", disk_size_str)
    print_key_value("Cloud-init", cloud_init_mode)
    print_key_value("API Socket", "enabled" if features.get("api_socket") else "off")
    print_key_value("Console", "enabled" if features.get("console") else "off")

    print_section_header("ASSETS")

    image_id = info.get("image_id")
    if image_id:
        image_display = image_id
        image_name = image_id
        try:
            matches = find_images_by_id_prefix(get_cache_dir(), image_id)
            if matches:
                _, meta = matches[0]
                internal_id = meta.get("internal_id")
                if internal_id:
                    image_name = internal_id
        except Exception:
            pass
        print_key_value("Image", f"{image_name} ({image_display})")

    kernel_id = info.get("kernel_id")
    if kernel_id:
        kernel_display = kernel_id
        kernel_name = kernel_id
        try:
            matches = find_kernels_by_id_prefix(get_cache_dir(), kernel_id)
            if matches:
                _, meta = matches[0]
                version = meta.get("version")
                if version:
                    kernel_name = version
        except Exception:
            pass
        print_key_value("Kernel", f"{kernel_name} ({kernel_display})")

    print_section_header("PATHS")
    paths = info.get("paths", {})
    vm_dir = paths.get("vm_dir", "-")
    print_key_value("State Dir", vm_dir)
    if paths.get("rootfs"):
        print_key_value("Rootfs", paths["rootfs"])
    if paths.get("config"):
        print_key_value("Config", paths["config"])


def _print_vm_details_tree(info: dict[str, Any]) -> None:
    from datetime import datetime

    from mvmctl.api.metadata import find_images_by_id_prefix, find_kernels_by_id_prefix
    from mvmctl.utils.fs import get_cache_dir

    name = info.get("name", "-")
    status = info.get("status", "-")

    created_at = info.get("created_at")
    if created_at:
        try:
            dt = datetime.fromisoformat(created_at.replace("Z", "+00:00"))
            created_str = dt.strftime("%Y/%m/%d %H:%M:%S")
        except (ValueError, AttributeError):
            created_str = str(created_at)
    else:
        created_str = "-"

    disk_size_str = "-"
    rootfs_path = info.get("paths", {}).get("rootfs")
    if rootfs_path:
        try:
            rootfs_size = Path(rootfs_path).stat().st_size
            disk_size_gb = rootfs_size / (1024**3)
            disk_size_str = f"{disk_size_gb:.1f}G"
        except (OSError, ValueError):
            pass

    cloud_init_mode = info.get("cloud_init_mode", "inject").lower()
    features = info.get("features", {})

    print(f"{name} ({status})")

    tree_lines = [
        f"├── Status:     {status}",
        f"├── Created:    {created_str}",
        f"├── PID:        {info.get('pid') or '-'}",
    ]

    tree_lines.append("├── Network")
    tree_lines.append(f"│   ├── IP:     {info.get('ip') or '-'}")
    tree_lines.append(f"│   ├── MAC:    {info.get('mac') or '-'}")
    tree_lines.append(f"│   └── TAP:    {info.get('tap_device') or '-'}")

    tree_lines.append("├── Resources")
    tree_lines.append(f"│   ├── Disk:   {disk_size_str}")
    tree_lines.append(f"│   ├── Cloud:  {cloud_init_mode}")
    tree_lines.append(f"│   ├── API:    {'enabled' if features.get('api_socket') else 'off'}")
    tree_lines.append(f"│   └── Console: {'enabled' if features.get('console') else 'off'}")

    image_id = info.get("image_id")
    if image_id:
        image_display = image_id
        image_name = image_id
        try:
            matches = find_images_by_id_prefix(get_cache_dir(), image_id)
            if matches:
                _, meta = matches[0]
                internal_id = meta.get("internal_id")
                if internal_id:
                    image_name = internal_id
        except Exception:
            pass
        tree_lines.append(f"├── Image:      {image_name} ({image_display})")

    kernel_id = info.get("kernel_id")
    if kernel_id:
        kernel_display = kernel_id
        kernel_name = kernel_id
        try:
            matches = find_kernels_by_id_prefix(get_cache_dir(), kernel_id)
            if matches:
                _, meta = matches[0]
                version = meta.get("version")
                if version:
                    kernel_name = version
        except Exception:
            pass
        tree_lines.append(f"├── Kernel:     {kernel_name} ({kernel_display})")

    paths = info.get("paths", {})
    vm_dir = paths.get("vm_dir", "-")
    tree_lines.append("└── Paths")
    tree_lines.append(f"    ├── State:  {vm_dir}")
    if paths.get("rootfs"):
        tree_lines.append(f"    ├── Rootfs: {paths['rootfs']}")
    if paths.get("config"):
        tree_lines.append(f"    └── Config: {paths['config']}")

    for line in tree_lines:
        print(line)
