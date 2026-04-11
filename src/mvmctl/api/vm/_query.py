"""Data gathering functions for VM operations.

This module contains functions for gathering VM information and data
like inspect, export, status checking, and listing.
"""

from __future__ import annotations

import logging
import os
import re
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.api._internal._resolvers import VMResolver
from mvmctl.constants import (
    DEFAULT_FC_EXITCODE_FILENAME,
    DEFAULT_FC_LOG_FILENAME,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.vm_manager import VMManager
from mvmctl.core.vm_monitor import reconcile_vm
from mvmctl.exceptions import VMNotFoundError
from mvmctl.models import VMInstance, VMStatus
from mvmctl.utils.fs import get_vm_dir_by_hash, is_file_missing
from mvmctl.utils.process import is_process_running

if TYPE_CHECKING:
    from mvmctl.models import VMInspectInfo
    from mvmctl.models.vm_config_file import VMExportConfig

logger = logging.getLogger(__name__)

__all__ = [
    "inspect_vm",
    "export_vm_config",
    "get_vm_status_with_exit_code",
    "compute_vm_is_missing",
    "list_vms",
    "ResolveVMInstancesResult",
    "resolve_vm_target_instances",
]


@dataclass
class ResolveVMInstancesResult:
    targets: list[VMInstance]
    errors: list[str]
    exit_code: int


def resolve_vm_target_instances(
    ids: list[str],
    names: list[str],
) -> ResolveVMInstancesResult:
    """Resolve multiple VM ID prefixes and names to VMInstance objects.

    Collects all errors rather than failing on the first, then deduplicates
    targets by ID. Used by CLI commands that accept multiple VM selectors.

    Args:
        ids: List of VM ID prefixes.
        names: List of VM names.

    Returns:
        ResolveVMInstancesResult with resolved targets, error messages, and exit code.
    """
    resolver = VMResolver()
    identifiers = ids + names
    result = resolver.resolve_many(identifiers)
    return ResolveVMInstancesResult(
        targets=result.items,  # type: ignore[arg-type]
        errors=result.errors,
        exit_code=result.exit_code,
    )


def list_vms(include_stopped: bool = True, vm_manager: VMManager | None = None) -> list[VMInstance]:
    """Return all registered VMs, optionally filtering out stopped ones.

    Reconciles live VM state from process status and Firecracker API
    before returning the list.
    """
    import mvmctl.api.vm

    manager = vm_manager or mvmctl.api.vm.get_vm_manager()
    all_vms = manager.list_all()

    # Reconcile live state for VMs that might have changed
    for vm in all_vms:
        # Skip VMs with no PID — they're definitively stopped/unstarted
        if vm.pid is not None:
            new_state = reconcile_vm(vm, manager)
            vm.status = new_state

    if not include_stopped:
        terminal_states = {VMStatus.STOPPED, VMStatus.ERROR, VMStatus.CRASHED}
        return [vm for vm in all_vms if vm.status not in terminal_states]
    return all_vms


def inspect_vm(name: str) -> VMInspectInfo:
    """Get detailed VM information.

    Args:
        name: VM name or ID prefix to look up.

    Returns:
        VMInspectInfo containing comprehensive VM details.

    Raises:
        VMNotFoundError: If the VM is not found.
    """
    resolver = VMResolver()

    # Try ID prefix first
    try:
        vm = resolver.by_id(name)
        return _gather_vm_details(vm)
    except VMNotFoundError:
        pass

    # Fall back to name lookup
    try:
        vm = resolver.by_name(name)
        return _gather_vm_details(vm)
    except VMNotFoundError:
        raise VMNotFoundError(f"VM '{name}' not found")


def _resolve_asset_names(
    image_id: str | None, kernel_id: str | None
) -> tuple[str | None, str | None]:
    """Resolve friendly names for image and kernel IDs from database.

    Args:
        image_id: The image ID to resolve.
        kernel_id: The kernel ID to resolve.

    Returns:
        Tuple of (image_name, kernel_name) with friendly names or IDs as fallback.
    """
    from mvmctl.api.metadata import find_images_by_id_prefix, find_kernels_by_id_prefix
    from mvmctl.utils.fs import get_cache_dir

    image_name: str | None = None
    kernel_name: str | None = None

    if image_id:
        try:
            matches = find_images_by_id_prefix(get_cache_dir(), image_id)
            if matches:
                _, meta = matches[0]
                image_name = meta.get("os_slug") or image_id
        except Exception as exc:
            logger.debug("Failed to resolve image name for %r: %s", image_id, exc)
            image_name = image_id
    if kernel_id:
        try:
            matches = find_kernels_by_id_prefix(get_cache_dir(), kernel_id)
            if matches:
                _, meta = matches[0]
                kernel_name = meta.get("version") or kernel_id
        except Exception as exc:
            logger.debug("Failed to resolve kernel name for %r: %s", kernel_id, exc)
            kernel_name = kernel_id

    return image_name, kernel_name


def _resolve_rootfs_path(vm: VMInstance, vm_dir: Path) -> tuple[Path | None, str]:
    """Resolve rootfs path from multiple sources.

    Checks sources in priority order:
    1. vm.config.rootfs_path - if config exists and path is set
    2. VM-local rootfs{suffix} - fallback for legacy VMs
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


def _gather_vm_details(vm: VMInstance) -> VMInspectInfo:
    """Gather comprehensive VM details.

    Args:
        vm: The VM instance to gather details for.

    Returns:
        VMInspectInfo containing comprehensive VM details.
    """
    from mvmctl.models import VMInspectInfo

    vm_dir = get_vm_dir_by_hash(vm.id)

    rootfs_path, rootfs_source = _resolve_rootfs_path(vm, vm_dir)

    config_path = vm_dir / "firecracker.json"

    image_name, kernel_name = _resolve_asset_names(vm.image_id, vm.kernel_id)

    # Get network name from network_id
    db_net = MVMDatabase().get_network(vm.network_id) if vm.network_id else None
    network_name = db_net.name if db_net else None

    nocloud_net = None
    if vm.nocloud_net_port:
        nocloud_net = {
            "port": vm.nocloud_net_port,
            "server_pid": vm.nocloud_server_pid,
        }

    console = None
    if vm.console_socket_path:
        console = {
            "socket_path": str(vm.console_socket_path),
            "relay_pid": vm.console_relay_pid,
        }

    return VMInspectInfo(
        id=vm.id,
        name=vm.name,
        status=vm.status.value,
        created_at=vm.created_at.isoformat() if vm.created_at else None,
        pid=vm.pid,
        ip=vm.ipv4,
        mac=vm.mac,
        network_name=network_name,
        tap_device=vm.tap_device,
        cloud_init_mode=vm.config.cloud_init_mode.value if vm.config else "inject",
        image_id=vm.image_id,
        image_name=image_name,
        kernel_id=vm.kernel_id,
        kernel_name=kernel_name,
        paths={
            "vm_dir": str(vm_dir),
            "rootfs": str(rootfs_path) if rootfs_path else None,
            "rootfs_source": rootfs_source,
            "config": str(config_path) if config_path.exists() else None,
        },
        features={
            "api_socket": vm.api_socket_path is not None,
            "console": vm.console_socket_path is not None,
            "nocloud_net": vm.nocloud_net_port is not None,
        },
        nocloud_net=nocloud_net,
        console=console,
    )


def get_vm_status_with_exit_code(vm: VMInstance) -> tuple[str, int | None]:
    """Get VM status with exit code if process has exited.

    Args:
        vm: VM instance to check

    Returns:
        Tuple of (status_string, exit_code_or_none)
    """
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

    Args:
        vm: The VM instance to get exit code for.

    Returns:
        Exit code if found, None otherwise.
    """
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


def compute_vm_is_missing(vm: VMInstance) -> bool:
    """Check if a VM's runtime state suggests it's missing from the filesystem.

    A VM is considered "missing" if:
    - The VM directory is missing from the filesystem
    - OR the status says running but the PID is not actually running

    Args:
        vm: The VM instance to check.

    Returns:
        True if the VM appears to be missing, False otherwise.
    """
    if not vm.id:
        return False
    vm_dir = get_vm_dir_by_hash(vm.id)
    dir_missing = is_file_missing(vm_dir)
    process_running = is_process_running(vm.pid) if vm.pid else False
    return dir_missing or (vm.status == VMStatus.RUNNING and not process_running)


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

    resolver = VMResolver()

    # Try ID prefix first
    try:
        vm = resolver.by_id(name)
    except VMNotFoundError:
        # Fall back to name lookup
        try:
            vm = resolver.by_name(name)
        except VMNotFoundError:
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
            image_matches = find_images_by_id_prefix(cache_dir, vm.image_id)
            if image_matches:
                _, meta = image_matches[0]
                image_os_slug = meta.get("os_slug", "")
                image_arch = meta.get("arch", "")
        except Exception as exc:
            logger.debug("Failed to resolve image os_slug for %r: %s", vm.image_id, exc)
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
            except Exception as exc:
                logger.debug(
                    "Failed to resolve image os_slug from entries for %r: %s", vm.image_id, exc
                )
                pass

    # Resolve kernel version from metadata
    kernel_version: str | None = None
    kernel_arch: str | None = None
    kernel_type: str | None = None
    if vm.kernel_id:
        cache_dir = get_cache_dir()
        try:
            kernel_matches = find_kernels_by_id_prefix(cache_dir, vm.kernel_id)
            if kernel_matches:
                _, meta = kernel_matches[0]
                kernel_version = meta.get("version")
                kernel_arch = meta.get("arch")
                kernel_type = meta.get("type")
        except Exception as exc:
            logger.debug("Failed to resolve kernel version for %r: %s", vm.kernel_id, exc)
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
            except Exception as exc:
                logger.debug(
                    "Failed to resolve kernel version from entries for %r: %s", vm.kernel_id, exc
                )
                pass

    # Resolve binary version from metadata
    binary_version: str | None = None
    try:
        from mvmctl.core.metadata import list_binary_entries

        cache_dir = get_cache_dir()
        all_binaries = list_binary_entries(cache_dir)
        for bin_name, entries in all_binaries.items():
            for meta in entries:
                if meta.get("is_default"):
                    binary_version = meta.get("version")
                    break
            if binary_version:
                break
    except Exception as exc:
        logger.debug("Failed to resolve binary version: %s", exc)
        pass

    # Build network config - get network name from network_id
    db_net_export = MVMDatabase().get_network(vm.network_id) if vm.network_id else None
    network_name = db_net_export.name if db_net_export else None
    network_ip = vm.ipv4
    network_mac = vm.mac

    from mvmctl.models.vm_config_file import VMExportConfig

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
