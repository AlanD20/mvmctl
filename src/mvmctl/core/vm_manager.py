"""VM state management."""

import hashlib
import logging
import sqlite3
from datetime import datetime
from pathlib import Path

from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import VMInstance as DBVMInstance
from mvmctl.models.vm import VMInstance, VMStatus

logger = logging.getLogger(__name__)


def _is_hex_string(s: str, length: int = 16) -> bool:
    """Check if string is a hex string of given length."""
    if len(s) != length:
        return False
    try:
        int(s, 16)
        return True
    except ValueError:
        return False


def _generate_vm_id(name: str, created_at: datetime) -> str:
    """Generate a unique VM ID from name and creation time."""
    data = f"{name}:{created_at.isoformat()}"
    return hashlib.sha256(data.encode()).hexdigest()[:16]


def _vm_instance_to_db_state(vm: VMInstance, binary_id: str | None = None) -> DBVMInstance:
    """Convert VMInstance to DB VMState for SQLite storage."""
    network_id = None
    if vm.network_name:
        try:
            db = MVMDatabase()
            network = db.get_network_by_name(vm.network_name)
            if network:
                network_id = network.id
        except Exception:
            network_id = None

    # Resolve image_id: try prefix first, then os_slug fallback
    image_id = None
    if vm.image_id:
        try:
            db = MVMDatabase()
            images = db.find_images_by_prefix(vm.image_id)
            if len(images) == 1:
                image_id = images[0].id
            elif not images:
                # Try as os_slug
                image = db.get_image_by_os_slug(vm.image_id)
                if image:
                    image_id = image.id
        except (sqlite3.OperationalError, Exception):
            image_id = None

    # Resolve kernel_id: try prefix
    kernel_id = None
    if vm.kernel_id:
        try:
            db = MVMDatabase()
            kernels = db.find_kernels_by_prefix(vm.kernel_id)
            if len(kernels) == 1:
                kernel_id = kernels[0].id
        except (sqlite3.OperationalError, Exception):
            kernel_id = None

    # binary_id is passed explicitly from API layer (Resolution Layer Mandate)
    # No database query here - core receives explicit values only

    updated_at = datetime.now().isoformat()
    created_at = vm.created_at.isoformat() if vm.created_at else datetime.now().isoformat()

    return DBVMInstance(
        id=vm.id,
        name=vm.name,
        status=vm.status.value,
        pid=vm.pid,
        ipv4=vm.ipv4,
        mac=vm.mac,
        network_id=network_id,
        tap_device=vm.tap_device,
        image_id=image_id,
        kernel_id=kernel_id,
        binary_id=binary_id,
        api_socket_path=str(vm.api_socket_path) if vm.api_socket_path else None,
        console_socket_path=str(vm.console_socket_path) if vm.console_socket_path else None,
        config_path=None,
        cloud_init_mode=vm.config.cloud_init_mode.value
        if vm.config and vm.config.cloud_init_mode
        else None,
        nocloud_net_port=vm.nocloud_net_port,
        nocloud_server_pid=vm.nocloud_server_pid,
        console_relay_pid=vm.console_relay_pid,
        exit_code=vm.exit_code,
        vcpu_count=vm.config.vcpu_count if vm.config else None,
        mem_size_mib=vm.config.mem_size_mib if vm.config else None,
        disk_size_mib=None,
        rootfs_path=str(vm.config.rootfs_path) if vm.config and vm.config.rootfs_path else None,
        rootfs_suffix=vm.rootfs_suffix if vm.rootfs_suffix else None,
        created_at=created_at,
        updated_at=updated_at,
    )


def _db_state_to_vm_instance(state: DBVMInstance) -> VMInstance:
    """Convert DB VMState to VMInstance."""
    from mvmctl.constants import (
        DEFAULT_VM_MEM_MIB,
        DEFAULT_VM_ROOTFS_FILENAME,
        DEFAULT_VM_VCPU_COUNT,
    )
    from mvmctl.models.cloud_init import CloudInitMode
    from mvmctl.models.vm import VMConfig

    # If already a VMInstance, return as-is (handles mock objects in tests)
    if isinstance(state, VMInstance):
        return state

    network_name = None
    if state.network_id:
        try:
            db = MVMDatabase()
            network = db.get_network(state.network_id)
            if network:
                network_name = network.name
        except (sqlite3.OperationalError, Exception):
            network_name = None

    config = VMConfig(
        name=state.name,
        vm_id=state.id,
        vcpu_count=state.vcpu_count if state.vcpu_count is not None else DEFAULT_VM_VCPU_COUNT,
        mem_size_mib=state.mem_size_mib if state.mem_size_mib is not None else DEFAULT_VM_MEM_MIB,
        rootfs_path=Path(state.rootfs_path)
        if state.rootfs_path
        else Path(DEFAULT_VM_ROOTFS_FILENAME),
        cloud_init_mode=CloudInitMode(state.cloud_init_mode)
        if state.cloud_init_mode
        else CloudInitMode.INJECT,
        enable_api_socket=state.enable_api_socket if state.enable_api_socket is not None else True,
        enable_pci=state.enable_pci if state.enable_pci is not None else False,
        lsm_flags=state.lsm_flags if state.lsm_flags is not None else "",
        enable_logging=state.enable_logging if state.enable_logging is not None else True,
        enable_metrics=state.enable_metrics if state.enable_metrics is not None else False,
        enable_console=state.enable_console if state.enable_console is not None else True,
    )

    vm = VMInstance(
        name=state.name,
        id=state.id,
        pid=state.pid,
        api_socket_path=Path(state.api_socket_path) if state.api_socket_path else None,
        ipv4=state.ipv4,
        mac=state.mac,
        network_name=network_name,
        tap_device=state.tap_device,
        status=VMStatus(state.status) if state.status else VMStatus.STOPPED,
        config=config,
        nocloud_net_port=state.nocloud_net_port,
        nocloud_server_pid=state.nocloud_server_pid,
        console_relay_pid=state.console_relay_pid,
        console_socket_path=Path(state.console_socket_path) if state.console_socket_path else None,
        exit_code=state.exit_code,
        rootfs_suffix=state.rootfs_suffix or ".ext4",
        image_id=state.image_id,
        kernel_id=state.kernel_id,
    )
    return vm


class VMManager:
    """Manages VM state persistence."""

    def __init__(self, run_dir: Path | None = None) -> None:
        pass

    def register(self, vm: VMInstance, binary_id: str | None = None) -> None:
        """Register a new VM in the database.

        Args:
            vm: VM instance to register
            binary_id: Optional binary ID from API layer (Resolution Layer Mandate)
        """
        if not vm.id:
            vm.id = _generate_vm_id(vm.name, vm.created_at)
        db = MVMDatabase()
        db.upsert_vm(_vm_instance_to_db_state(vm, binary_id))

    def update_status(self, name: str, status: VMStatus) -> None:
        """Update the status of a registered VM."""
        from mvmctl.exceptions import VMNotFoundError

        db = MVMDatabase()
        vm_state = db.get_vm_by_name(name)
        if vm_state is None:
            raise VMNotFoundError(f"VM '{name}' not found")
        db.update_vm_status(vm_state.id, status.value)

    def get(self, name: str) -> VMInstance | None:
        """Get VM by name (searches all VMs for matching name)."""
        db = MVMDatabase()
        vm_state = db.get_vm_by_name(name)
        if vm_state:
            return _db_state_to_vm_instance(vm_state)
        return None

    def get_by_id_prefix(self, prefix: str) -> VMInstance | None:
        """Find VM by ID prefix. Returns None if none or multiple VMs match."""
        db = MVMDatabase()
        vms = db.find_vms_by_prefix(prefix)
        if len(vms) == 1:
            return _db_state_to_vm_instance(vms[0])
        return None

    def get_by_full_id(self, full_hash: str) -> VMInstance | None:
        """Find VM by exact hash ID."""
        db = MVMDatabase()
        vm_state = db.get_vm(full_hash)
        if vm_state:
            return _db_state_to_vm_instance(vm_state)
        return None

    def find_by_id_prefix(self, prefix: str) -> list[VMInstance]:
        """Return all VMs whose ID starts with prefix."""
        db = MVMDatabase()
        vms = db.find_vms_by_prefix(prefix)
        return [_db_state_to_vm_instance(vm) for vm in vms]

    def get_by_name(self, name: str) -> list[VMInstance]:
        """Return all VMs with the given name (may be multiple)."""
        db = MVMDatabase()
        vm_state = db.get_vm_by_name(name)
        if vm_state:
            return [_db_state_to_vm_instance(vm_state)]
        return []

    def list_all(self) -> list[VMInstance]:
        """List all VMs."""
        db = MVMDatabase()
        vms = db.list_vms()
        return [_db_state_to_vm_instance(vm) for vm in vms]

    def count_vms(self) -> int:
        """Return the number of VMs."""
        db = MVMDatabase()
        return len(db.list_vms())

    def deregister(self, vm_id: str) -> None:
        """Remove VM from state by full hash ID."""
        db = MVMDatabase()
        db.delete_vm(vm_id)


def get_vm_manager() -> VMManager:
    return VMManager()
