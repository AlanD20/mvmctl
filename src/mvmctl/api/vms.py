"""VM lifecycle API — create, remove, list, ssh, logs."""

from __future__ import annotations

from pathlib import Path
from typing import Any

from mvmctl.api.host import check_privileges_interactive
from mvmctl.core.console import (
    check_escape_sequence,
    connect_to_relay,
    disconnect_from_relay,
    read_console_output,
    send_console_input,
)
from mvmctl.core.console import (
    get_console_state as _get_console_state,
)
from mvmctl.core.image import (
    resolve_image_id_path as _core_resolve_image_id_path,
)
from mvmctl.core.image import (
    resolve_image_path as _core_resolve_image_path,
)
from mvmctl.core.kernel import (
    resolve_kernel_id_path as _core_resolve_kernel_id_path,
)
from mvmctl.core.kernel import (
    resolve_kernel_path as _core_resolve_kernel_path,
)
from mvmctl.core.logs import show_logs
from mvmctl.core.network import teardown_nat
from mvmctl.core.network_manager import get_network
from mvmctl.core.ssh import connect_to_vm
from mvmctl.core.vm_lifecycle import (
    create_vm as _create_vm,
)
from mvmctl.core.vm_lifecycle import (
    load_snapshot as _load_snapshot,
)
from mvmctl.core.vm_lifecycle import (
    pause_vm as _pause_vm,
)
from mvmctl.core.vm_lifecycle import (
    remove_vm as _remove_vm,
)
from mvmctl.core.vm_lifecycle import (
    resume_vm as _resume_vm,
)
from mvmctl.core.vm_lifecycle import (
    snapshot_vm as _snapshot_vm,
)
from mvmctl.core.vm_manager import VMManager, get_vm_manager
from mvmctl.models import CloudInitMode, VMExportConfig, VMInstance, VMStatus
from mvmctl.services.console_relay import ConsoleRelayManager

__all__ = [
    "get_vm_status_with_exit_code",
    "list_vms",
    "get_vm",
    "vm_cache_dir",
    "create_vm",
    "remove_vm",
    "snapshot_vm",
    "load_snapshot",
    "pause_vm",
    "resume_vm",
    "stop_vm",
    "start_vm",
    "reboot_vm",
    "ssh_vm",
    "get_logs",
    "cleanup_vms",
    "get_vm_manager",
    "VMManager",
    "resolve_image_path",
    "resolve_kernel_path",
    "resolve_image_id_path",
    "resolve_kernel_id_path",
    "resolve_image_multi_strategy",
    "resolve_kernel_multi_strategy",
    "attach_console",
    "kill_console",
    "get_console_state",
    "check_escape_sequence",
    "connect_to_relay",
    "disconnect_from_relay",
    "read_console_output",
    "send_console_input",
    "export_vm_config",
]


def resolve_image_path(image: str) -> Path:
    return _core_resolve_image_path(image)


def resolve_kernel_path(kernel: str) -> Path:
    return _core_resolve_kernel_path(kernel)


def resolve_image_id_path(image: str) -> Path:
    return _core_resolve_image_id_path(image)


def resolve_kernel_id_path(kernel: str) -> Path:
    return _core_resolve_kernel_id_path(kernel)


def resolve_image_multi_strategy(value: str) -> Path:
    """Resolve image value to path using multiple strategies.

    Resolution order:
    1. Direct path (if contains '/' or ends with .ext4/.btrfs)
    2. YAML image name lookup (via os_slug)
    3. Short-ID resolution against metadata.json
    """
    from mvmctl.core.metadata import list_image_entries
    from mvmctl.utils.fs import get_cache_dir, get_images_dir

    images_dir = get_images_dir()
    cache_dir = get_cache_dir()

    # Direct path check
    if "/" in value or value.endswith((".ext4", ".btrfs")):
        path = Path(value)
        if path.exists():
            return path

    # YAML image name lookup (check os_slug in metadata)
    all_entries = list_image_entries(cache_dir)
    for full_key, meta in all_entries.items():
        os_slug = str(meta.get("os_slug", ""))
        if os_slug == value:
            path_str = str(meta.get("path", ""))
            if path_str:
                candidate = images_dir / path_str
                if candidate.exists():
                    return candidate
            # Try full_key with extensions
            for ext in (".ext4", ".btrfs"):
                candidate = images_dir / f"{full_key}{ext}"
                if candidate.exists():
                    return candidate
            # Try just the value name with extensions
            for ext in (".ext4", ".btrfs"):
                candidate = images_dir / f"{value}{ext}"
                if candidate.exists():
                    return candidate

    # ID prefix resolution
    return _core_resolve_image_id_path(value)


def resolve_kernel_multi_strategy(value: str) -> Path:
    """Resolve kernel value to path using multiple strategies.

    Resolution order:
    1. Direct path (if contains '/')
    2. Short-ID resolution against metadata.json
    """
    from mvmctl.utils.fs import get_kernels_dir

    kernels_dir = get_kernels_dir()

    # Direct path check
    if "/" in value:
        path = Path(value)
        if path.exists():
            return path

    # Check if it's a direct filename in kernels dir
    candidate = kernels_dir / value
    if candidate.exists():
        return candidate

    # ID prefix resolution
    return _core_resolve_kernel_id_path(value)


def resolve_vm_selector(selector: str) -> str:
    """Resolve a VM selector (name or ID prefix) to a VM name.

    Tries ID-prefix lookup first, falls back to treating selector as name.
    Raises MVMError if the prefix is ambiguous (matches multiple VMs).

    Args:
        selector: VM name or ID prefix

    Returns:
        Resolved VM name

    Raises:
        MVMError: If ID prefix is ambiguous (matches multiple VMs)
    """
    from mvmctl.exceptions import MVMError

    manager = get_vm_manager()
    matches = manager.find_by_id_prefix(selector)
    if len(matches) == 1:
        return matches[0].name
    if len(matches) > 1:
        names = ", ".join(m.name for m in matches)
        raise MVMError(f"Ambiguous ID prefix '{selector}' matches {len(matches)} VMs: {names}")
    # No ID match — treat as name
    return selector


def create_vm(
    name: str,
    vcpus: int,
    mem: int,
    user: str,
    enable_api_socket: bool,
    enable_pci: bool,
    enable_console: bool,
    firecracker_bin: str,
    lsm_flags: str,
    enable_logging: bool,
    enable_metrics: bool,
    image: str | None = None,
    kernel: str | None = None,
    image_path: Path | None = None,
    kernel_path: Path | None = None,
    disk_size: str | None = None,
    ip: str | None = None,
    network_name: str | None = None,
    mac: str | None = None,
    ssh_key: str | None = None,
    user_data: Path | None = None,
    cloud_init_mode: CloudInitMode = CloudInitMode.INJECT,
    cloud_init_iso_path: Path | None = None,
    keep_cloud_init_iso: bool = False,
    vm_manager: VMManager | None = None,
    nocloud_net_port: int = 0,
) -> VMInstance:
    from mvmctl.core.image import (
        resolve_image_fs_type as _resolve_image_fs_type,
    )
    from mvmctl.core.image import (
        resolve_image_fs_uuid as _resolve_image_fs_uuid,
    )
    from mvmctl.core.metadata import list_image_entries
    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.exceptions import AssetNotFoundError
    from mvmctl.utils.fs import get_cache_dir

    # Resolve DB-backed defaults when CLI passes None
    if image is None:
        db = MVMDatabase()
        default_image = db.get_default_image()
        if default_image is None:
            raise AssetNotFoundError(
                "No image specified and no default image set. "
                "Use 'mvm image fetch <name>' then 'mvm image set-default <name>', or pass --image."
            )
        image = default_image

    if network_name is None:
        db = MVMDatabase()
        default_network = db.get_default_network()
        if default_network is None:
            from mvmctl.constants import DEFAULT_NETWORK_NAME

            network_name = DEFAULT_NETWORK_NAME
        else:
            network_name = default_network.name

    check_privileges_interactive("/usr/sbin/ip", f"create VM '{name}'")

    # Resolve image path and metadata in API layer
    if image_path is not None:
        resolved_image_path = image_path
        resolved_image_fs_uuid = _resolve_image_fs_uuid(image) if image else None
        resolved_image_fs_type = _resolve_image_fs_type(image) if image else None
        resolved_image_hash: str | None = None
        if resolved_image_path.suffix == ".zst":
            cache_dir = get_cache_dir()
            all_entries = list_image_entries(cache_dir)
            for img_id, meta in all_entries.items():
                if meta.get("path") == resolved_image_path.name:
                    resolved_image_hash = img_id
                    break
            if resolved_image_hash is None:
                resolved_image_hash = resolved_image_path.stem
    else:
        resolved_image_path = resolve_image_multi_strategy(image)
        resolved_image_fs_uuid = _resolve_image_fs_uuid(image)
        resolved_image_fs_type = _resolve_image_fs_type(image)
        resolved_image_hash = None

    return _create_vm(
        name=name,
        image_path=resolved_image_path,
        kernel=kernel,
        kernel_path=kernel_path,
        vcpus=vcpus,
        mem=mem,
        disk_size=disk_size,
        ip=ip,
        network_name=network_name,
        mac=mac,
        ssh_key=ssh_key,
        user_data=user_data,
        user=user,
        enable_api_socket=enable_api_socket,
        enable_pci=enable_pci,
        enable_console=enable_console,
        firecracker_bin=firecracker_bin,
        lsm_flags=lsm_flags,
        enable_logging=enable_logging,
        enable_metrics=enable_metrics,
        cloud_init_mode=cloud_init_mode,
        cloud_init_iso_path=cloud_init_iso_path,
        keep_cloud_init_iso=keep_cloud_init_iso,
        vm_manager=vm_manager,
        nocloud_net_port=nocloud_net_port,
        image_fs_uuid=resolved_image_fs_uuid,
        image_fs_type=resolved_image_fs_type,
        image_hash=resolved_image_hash,
    )


def remove_vm(name: str, vm_manager: VMManager | None = None) -> None:
    check_privileges_interactive("/usr/sbin/ip", f"remove VM '{name}'")
    return _remove_vm(name=name, vm_manager=vm_manager)


def snapshot_vm(name: str, mem_out: Path, state_out: Path) -> None:
    return _snapshot_vm(name=name, mem_out=mem_out, state_out=state_out)


def load_snapshot(
    name: str, mem_in: Path, state_in: Path, resume_after: bool | None = None
) -> None:
    return _load_snapshot(name=name, mem_in=mem_in, state_in=state_in, resume_after=resume_after)


def pause_vm(name: str) -> None:
    """Pause a running VM."""
    return _pause_vm(name=name)


def resume_vm(name: str) -> None:
    """Resume a paused VM."""
    return _resume_vm(name=name)


def stop_vm(name: str, force: bool = False) -> None:
    """Stop a running VM gracefully."""
    from mvmctl.core.vm_lifecycle import stop_vm as _stop_vm

    return _stop_vm(name=name, force=force)


def start_vm(name: str) -> None:
    """Start a stopped VM."""
    from mvmctl.core.vm_lifecycle import start_vm as _start_vm

    return _start_vm(name=name)


def reboot_vm(name: str, force: bool = False) -> None:
    """Reboot a VM (stop then start)."""
    from mvmctl.core.vm_lifecycle import reboot_vm as _reboot_vm

    return _reboot_vm(name=name, force=force)


def list_vms(include_stopped: bool = True, vm_manager: VMManager | None = None) -> list[VMInstance]:
    """Return all registered VMs, optionally filtering out stopped ones.

    Reconciles live VM state from process status and Firecracker API
    before returning the list.
    """
    manager = vm_manager or get_vm_manager()
    all_vms = manager.list_all()

    # Reconcile live state for VMs that might have changed
    from mvmctl.core.vm_monitor import reconcile_vm

    for vm in all_vms:
        # Skip VMs with no PID — they're definitively stopped/unstarted
        if vm.pid is not None:
            new_state = reconcile_vm(vm, manager)
            vm.status = new_state

    if not include_stopped:
        terminal_states = {VMStatus.STOPPED, VMStatus.ERROR, VMStatus.CRASHED}
        return [vm for vm in all_vms if vm.status not in terminal_states]
    return all_vms


def get_vm(name: str, vm_manager: VMManager | None = None) -> VMInstance | None:
    """Return the VMInstance for the given name, or None if not found."""
    manager = vm_manager or get_vm_manager()
    return manager.get(name)


def vm_cache_dir(vm: VMInstance) -> Path:
    """Return the cache directory path for a VM using its hash ID."""
    from mvmctl.utils.fs import get_vm_dir_by_hash

    return get_vm_dir_by_hash(vm.id)


def ssh_vm(
    name: str,
    user: str,
    key: Path | None = None,
    cmd: str | None = None,
) -> int:
    """Open SSH session or execute command on a VM."""
    return connect_to_vm(
        vm_name_or_ip=name,
        user=user,
        key_path=key,
        command=cmd,
        exec_mode=cmd is None,
    )


def get_logs(
    name: str,
    log_type: str,
    lines: int,
    follow: bool,
) -> list[str]:
    """View VM logs. Returns log lines."""
    manager = get_vm_manager()
    vm = manager.get(name)
    # Use VM hash if found, otherwise fall back to name (for backward compatibility)
    vm_hash = vm.id if vm is not None else name
    return show_logs(
        vm_hash=vm_hash,
        log_type=log_type,
        lines=lines,
        follow=follow,
    )


def cleanup_vms(
    all_vms: bool = False, dry_run: bool = False, vm_manager: VMManager | None = None
) -> list[VMInstance]:
    """Stop and remove stale or all VMs, tearing down their TAP devices and iptables rules."""
    check_privileges_interactive("/usr/sbin/ip", "cleanup VMs")
    import logging
    import os
    import shutil
    import signal

    from mvmctl.core.firewall import remove_nocloud_input_rule
    from mvmctl.core.network import delete_tap, remove_iptables_forward_rules
    from mvmctl.exceptions import NetworkError
    from mvmctl.services.nocloud_server import NoCloudNetServerManager
    from mvmctl.utils.fs import get_cache_dir

    log = logging.getLogger(__name__)

    manager = vm_manager or get_vm_manager()
    vms = manager.list_all()

    targets = vms if all_vms else [v for v in vms if v.status != VMStatus.RUNNING]

    if dry_run or not targets:
        return targets

    cache_dir = Path(get_cache_dir())

    for v in targets:
        vm_dir = vm_cache_dir(v) if v.id else None

        tap_name = v.tap_device
        if not tap_name:
            log.warning("VM %s has no tap_device in state, skipping TAP cleanup", v.name)

        if v.nocloud_net_port is not None and v.ipv4 is not None:
            try:
                nocloud_manager = NoCloudNetServerManager()
                nocloud_manager.stop_server(v.name, v.id)
            except (OSError, RuntimeError):
                pass

            try:
                remove_nocloud_input_rule(v.ipv4, v.name, v.nocloud_net_port)
            except NetworkError:
                pass

        if v.pid:
            try:
                os.kill(v.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass

        if tap_name:
            net_config = get_network(v.network_name or "")
            bridge = net_config.bridge if net_config else ""
            remove_iptables_forward_rules(tap_name, bridge=bridge)
            try:
                delete_tap(tap_name)
            except NetworkError:
                pass
            try:
                teardown_nat(bridge)
            except NetworkError:
                pass

        manager.deregister(v.id if v.id else v.name)

        nocloud_cache_dir = cache_dir / f"nocloud-{v.id}" if v.id else None
        if nocloud_cache_dir is not None and nocloud_cache_dir.exists():
            shutil.rmtree(nocloud_cache_dir)

        if vm_dir is not None and vm_dir.exists():
            shutil.rmtree(vm_dir)

    # Clean up any orphaned nocloud servers
    try:
        nocloud_manager = NoCloudNetServerManager()
        nocloud_manager.cleanup_orphans()
    except Exception:
        # Don't fail cleanup if orphan cleanup fails
        pass

    return targets


def attach_console(name: str) -> dict[str, Any]:
    from mvmctl.exceptions import MVMError, VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    mgr = ConsoleRelayManager()
    vm_hash = vm.id if vm.id else None
    if not mgr.is_relay_running(name, vm_hash):
        raise MVMError(f"No console relay running for VM '{name}'")

    socket_path = mgr.get_socket_path(vm_hash if vm_hash else name)
    return {"socket_path": str(socket_path), "vm_name": name}


def kill_console(name: str) -> bool:
    from mvmctl.exceptions import VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    mgr = ConsoleRelayManager()
    vm_hash = vm.id if vm.id else None
    return mgr.kill_relay(name, vm_hash)


def get_console_state(name: str) -> dict[str, Any]:
    from mvmctl.exceptions import VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_hash = vm.id if vm.id else None
    return _get_console_state(name, vm_hash)


def inspect_vm(name: str) -> dict[str, Any]:
    """Get detailed VM information."""
    from mvmctl.exceptions import MVMError, VMNotFoundError

    manager = get_vm_manager()

    # Try ID prefix first
    vm = manager.get_by_id_prefix(name)
    if vm:
        return _gather_vm_details(vm)

    # Fall back to name lookup
    matches = manager.get_by_name(name)
    if len(matches) == 1:
        return _gather_vm_details(matches[0])
    elif len(matches) > 1:
        raise MVMError(f"Multiple VMs match name '{name}' — use ID prefix")

    raise VMNotFoundError(f"VM '{name}' not found")


def _gather_vm_details(vm: VMInstance) -> dict[str, Any]:
    """Gather comprehensive VM details."""
    from mvmctl.utils.fs import get_vm_dir_by_hash

    vm_dir = get_vm_dir_by_hash(vm.id)

    rootfs_path, rootfs_source = _resolve_rootfs_path(vm, vm_dir)

    config_path = vm_dir / "firecracker.json"

    info: dict[str, Any] = {
        "id": vm.id,
        "name": vm.name,
        "status": vm.status.value,
        "created_at": vm.created_at.isoformat() if vm.created_at else None,
        "pid": vm.pid,
        "ip": vm.ipv4,
        "mac": vm.mac,
        "network_name": vm.network_name,
        "tap_device": vm.tap_device,
        "cloud_init_mode": vm.config.cloud_init_mode.value if vm.config else "inject",
        "image_id": vm.image_id,
        "kernel_id": vm.kernel_id,
        "paths": {
            "vm_dir": str(vm_dir),
            "rootfs": str(rootfs_path) if rootfs_path else None,
            "rootfs_source": rootfs_source,
            "config": str(config_path) if config_path.exists() else None,
        },
        "features": {
            "api_socket": vm.api_socket_path is not None,
            "console": vm.console_socket_path is not None,
            "nocloud_net": vm.nocloud_net_port is not None,
        },
    }

    if vm.nocloud_net_port:
        info["nocloud_net"] = {
            "port": vm.nocloud_net_port,
            "server_pid": vm.nocloud_server_pid,
        }

    if vm.console_socket_path:
        info["console"] = {
            "socket_path": str(vm.console_socket_path),
            "relay_pid": vm.console_relay_pid,
        }

    return info


def _resolve_rootfs_path(vm: VMInstance, vm_dir: Path) -> tuple[Path | None, str]:
    """Resolve rootfs path from multiple sources.

    Checks sources in priority order:
    1. vm.config.rootfs_path - if config exists and path is set
    2. VM-local rootfs{suffix} - fallback for legacy VMs

    Args:
        vm: VM instance to resolve rootfs for
        vm_dir: Path to VM directory

    Returns:
        Tuple of (resolved_path, source_name) where source_name indicates
        which source provided the path: "config", "local", or "none"
    """
    # Priority 1: Check config.rootfs_path if config exists
    if vm.config is not None and vm.config.rootfs_path is not None:
        config_path = Path(vm.config.rootfs_path)
        if config_path.exists():
            return config_path, "config"

    # Priority 2: Fallback to VM-local rootfs file
    if not vm.rootfs_suffix:
        return None, "none"
    local_path = vm_dir / f"rootfs{vm.rootfs_suffix}"
    if local_path.exists():
        return local_path, "local"

    # No rootfs found
    return None, "none"


def get_vm_status_with_exit_code(vm: VMInstance) -> tuple[str, int | None]:
    """Get VM status with exit code if process has exited.

    Args:
        vm: VM instance to check

    Returns:
        Tuple of (status_string, exit_code_or_none)
    """
    import os

    from mvmctl.models import VMStatus

    # Check if process is running
    if vm.pid is not None:
        try:
            os.kill(vm.pid, 0)
            return "running", None
        except (ProcessLookupError, OSError):
            # Process exited - try to get exit code
            pass

    # Try to get exit code from various sources
    exit_code = _get_exit_code_from_sources(vm)

    if exit_code is not None:
        return f"exited({exit_code})", exit_code

    # Check VM state from metadata
    if vm.status == VMStatus.RUNNING:
        return "exited", None  # Was running but process died
    return vm.status.value, None


def _get_exit_code_from_sources(vm: VMInstance) -> int | None:
    """Try to extract exit code from various sources.

    Sources checked in order:
    1. firecracker.exitcode file in VM directory
    2. firecracker.log for exit code patterns
    """
    import re

    from mvmctl.constants import DEFAULT_FC_EXITCODE_FILENAME, DEFAULT_FC_LOG_FILENAME
    from mvmctl.utils.fs import get_vm_dir_by_hash

    if not vm.id:
        return None

    vm_dir = get_vm_dir_by_hash(vm.id)

    # Check for explicit exit code file
    exitcode_path = vm_dir / DEFAULT_FC_EXITCODE_FILENAME
    if exitcode_path.exists():
        try:
            return int(exitcode_path.read_text().strip())
        except (ValueError, OSError):
            pass

    # Check firecracker.log for exit patterns
    log_path = vm_dir / DEFAULT_FC_LOG_FILENAME
    if log_path.exists():
        try:
            content = log_path.read_text()
            # Look for common exit code patterns
            patterns = [
                r"exit(?:ed| code)[\s:]+(\d+)",
                r"returned\s+(\d+)",
                r"exit_status[=:\s]+(\d+)",
            ]
            for pattern in patterns:
                match = re.search(pattern, content, re.IGNORECASE)
                if match:
                    return int(match.group(1))
        except OSError:
            pass

    return None


def export_vm_config(name: str) -> "VMExportConfig":
    """Export a VM's configuration as a portable VMExportConfig.

    Uses semantic references (os_slug, version, name) — NEVER internal SHA256 IDs.

    Args:
        name: VM name or ID prefix

    Returns:
        VMExportConfig with semantic references

    Raises:
        VMNotFoundError: If VM not found
    """
    from mvmctl.api.metadata import find_images_by_id_prefix, find_kernels_by_id_prefix
    from mvmctl.core.metadata import list_image_entries, list_kernel_entries
    from mvmctl.exceptions import VMNotFoundError
    from mvmctl.models.vm_config_file import (
        VMExportBinaryConfig,
        VMExportBootConfig,
        VMExportCloudInitConfig,
        VMExportComputeConfig,
        VMExportFirecrackerConfig,
        VMExportImageConfig,
        VMExportKernelConfig,
        VMExportNetworkConfig,
    )
    from mvmctl.utils.fs import get_cache_dir

    manager = get_vm_manager()

    # Try ID prefix first
    vm = manager.get_by_id_prefix(name)
    if not vm:
        # Fall back to name lookup
        matches = manager.get_by_name(name)
        if len(matches) == 1:
            vm = matches[0]
        elif len(matches) > 1:
            from mvmctl.exceptions import MVMError

            raise MVMError(f"Multiple VMs match name '{name}' — use ID prefix")
        else:
            raise VMNotFoundError(f"VM '{name}' not found")

    if vm.config is None:
        raise VMNotFoundError(f"VM '{name}' has no configuration")

    config = vm.config

    # Resolve image os_slug from metadata
    image_os_slug = ""
    image_arch = ""
    if vm.image_id:
        cache_dir = get_cache_dir()
        try:
            matches = find_images_by_id_prefix(cache_dir, vm.image_id)
            if matches:
                _, meta = matches[0]
                image_os_slug = meta.get("os_slug", "")
                image_arch = meta.get("arch", "")
        except Exception:
            pass

        # Fallback: search all entries by matching the image_id
        if not image_os_slug:
            try:
                all_entries = list_image_entries(cache_dir)
                for img_id, meta in all_entries.items():
                    if img_id == vm.image_id or img_id.startswith(vm.image_id):
                        image_os_slug = meta.get("os_slug", "")
                        image_arch = meta.get("arch", "")
                        break
            except Exception:
                pass

    # Resolve kernel version from metadata
    kernel_version: str | None = None
    kernel_arch: str | None = None
    kernel_type: str | None = None
    if vm.kernel_id:
        cache_dir = get_cache_dir()
        try:
            matches = find_kernels_by_id_prefix(cache_dir, vm.kernel_id)
            if matches:
                _, meta = matches[0]
                kernel_version = meta.get("version")
                kernel_arch = meta.get("arch")
                kernel_type = meta.get("type")
        except Exception:
            pass

        # Fallback: search all entries
        if not kernel_version:
            try:
                all_entries = list_kernel_entries(cache_dir)
                for kern_id, meta in all_entries.items():
                    if kern_id == vm.kernel_id or kern_id.startswith(vm.kernel_id):
                        kernel_version = meta.get("version")
                        kernel_arch = meta.get("arch")
                        kernel_type = meta.get("type")
                        break
            except Exception:
                pass

    # Resolve binary version from metadata
    binary_version: str | None = None
    try:
        from mvmctl.core.metadata import list_binary_entries

        cache_dir = get_cache_dir()
        all_binaries = list_binary_entries(cache_dir)
        for bin_name, meta in all_binaries.items():
            if meta.get("is_default"):
                binary_version = meta.get("version")
                break
    except Exception:
        pass

    # Build network config
    network_name = vm.network_name
    network_ip = vm.ipv4
    network_mac = vm.mac

    return VMExportConfig(
        name=vm.name,
        compute=VMExportComputeConfig(
            vcpus=config.vcpu_count,
            mem=config.mem_size_mib,
        ),
        image=VMExportImageConfig(
            os_slug=image_os_slug,
            arch=image_arch,
        ),
        kernel=VMExportKernelConfig(
            version=kernel_version,
            arch=kernel_arch,
            type=kernel_type,
        ),
        binary=VMExportBinaryConfig(
            version=binary_version,
        ),
        network=VMExportNetworkConfig(
            name=network_name,
            ip=network_ip,
            mac=network_mac,
        ),
        boot=VMExportBootConfig(
            args=config.boot_args,
            enable_console=config.enable_console,
        ),
        firecracker=VMExportFirecrackerConfig(
            enable_api_socket=config.enable_api_socket,
            enable_pci=config.enable_pci,
            lsm_flags=config.lsm_flags,
        ),
        cloud_init=VMExportCloudInitConfig(
            mode=config.cloud_init_mode.value,
            user=config.name,  # VM name doubles as default user
            keep_iso=config.keep_cloud_init_iso,
        ),
    )
