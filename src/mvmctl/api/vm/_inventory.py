"""VM inventory and data gathering operations.

This module contains VMInventory class and functions for querying VM state,
counting, filtering by status, and gathering VM information.
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
from mvmctl.db.models import VMInstance
from mvmctl.exceptions import VMNotFoundError
from mvmctl.models import VMStatus
from mvmctl.utils.fs import get_vm_dir_by_hash, is_file_missing
from mvmctl.utils.process import is_process_running

if TYPE_CHECKING:
    from mvmctl.models import VMInspectInfo
    from mvmctl.models.vm_config_file import VMExportConfig

logger = logging.getLogger(__name__)

__all__ = [
    "GuestfsProvisioner",
    "CloudInitProvisioner",
    "CloudInitProvisionResult",
    "VMBuilder",
    "VMInventory",
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


class VMInventory:
    """Inventory and query operations for VMs.

    Provides methods to list, count, and filter VMs by various criteria.
    """

    def __init__(self, db: MVMDatabase | None = None) -> None:
        """Initialize with optional database instance.

        Args:
            db: Optional MVMDatabase instance (creates new if None)
        """
        self._db = db if db is not None else MVMDatabase()

    def list_all(self, include_stopped: bool = True) -> list[VMInstance]:
        """Return all registered VMs.

        Args:
            include_stopped: If False, filter out stopped/error/crashed VMs

        Returns:
            List of VMInstance objects
        """
        if not include_stopped:
            terminal_states = [VMStatus.STOPPED.value, VMStatus.ERROR.value, VMStatus.CRASHED.value]
            return self._db.list_vms_excluding_statuses(terminal_states)

        return self._db.list_vms()

    def count(self) -> int:
        """Total count of all VMs.

        Returns:
            Total number of VMs in database
        """
        return len(self._db.list_vms())

    def list_by_status(self, statuses: VMStatus | list[VMStatus]) -> list[VMInstance]:
        """List VMs filtered by status(es) using direct DB query.

        This method queries the database directly with the status filter,
        making it more efficient than filtering in Python for large datasets.

        Args:
            statuses: Single status or list of statuses to filter by

        Returns:
            List of VMs with matching status(es)
        """
        if isinstance(statuses, VMStatus):
            statuses = [statuses]

        status_values = [s.value for s in statuses]
        return self._db.list_vms_by_status(status_values)

    def count_by_status(self, statuses: VMStatus | list[VMStatus]) -> int:
        """Count VMs matching given status(es).

        Args:
            statuses: Single status or list of statuses to count

        Returns:
            Count of VMs with matching status
        """
        if isinstance(statuses, VMStatus):
            statuses = [statuses]

        status_values = [s.value for s in statuses]
        vms = self._db.list_vms_by_status(status_values)
        return len(vms)

    def find_by_status(self, statuses: VMStatus | list[VMStatus]) -> list[VMInstance]:
        """Find all VMs with given status(es).

        Args:
            statuses: Single status or list of statuses to match

        Returns:
            List of VMs with matching status(es)
        """
        if isinstance(statuses, VMStatus):
            statuses = [statuses]

        status_values = [s.value for s in statuses]
        return self._db.list_vms_by_status(status_values)


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
        targets=result.items,
        errors=result.errors,
        exit_code=result.exit_code,
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
    if vm.status == VMStatus.RUNNING.value:
        return "exited", None  # Was running but process died
    return vm.status, None


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
    return dir_missing or (vm.status == VMStatus.RUNNING.value and not process_running)


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
            vcpus=vm.vcpu_count,
            mem=vm.mem_size_mib,
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
            args=vm.lsm_flags,  # Using lsm_flags as boot args fallback
            enable_console=vm.enable_console,
        ),
        firecracker=VMExportFirecrackerConfig(
            enable_api_socket=vm.enable_api_socket,
            enable_pci=vm.enable_pci,
            lsm_flags=vm.lsm_flags,
        ),
        cloud_init=VMExportCloudInitConfig(
            mode=vm.cloud_init_mode or "inject",
            user=vm.name,  # VM name doubles as default user
            keep_iso=False,  # Default value
        ),
    )
