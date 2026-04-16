"""VM state management."""

import hashlib
import logging
from datetime import datetime
from pathlib import Path

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


def _vm_instance_to_db_state(vm: VMInstance) -> DBVMInstance:
    if vm.config is None:
        raise ValueError("VM config is required for DB persistence")

    updated_at = datetime.now().isoformat()
    created_at = vm.created_at.isoformat() if vm.created_at else datetime.now().isoformat()

    return DBVMInstance(
        id=vm.id,
        name=vm.name,
        status=vm.status.value,
        pid=vm.pid,
        ipv4=vm.ipv4,
        mac=vm.mac,
        network_id=vm.network_id,
        tap_device=vm.tap_device,
        image_id=vm.image_id,
        kernel_id=vm.kernel_id,
        binary_id=vm.binary_id,
        api_socket_path=str(vm.api_socket_path) if vm.api_socket_path else None,
        relay_socket_path=str(vm.console_socket_path) if vm.console_socket_path else None,
        config_path=str(vm.config_path) if vm.config_path else "",
        cloud_init_mode=vm.config.cloud_init_mode.value,
        nocloud_net_port=vm.nocloud_net_port,
        nocloud_net_pid=vm.nocloud_server_pid,
        relay_pid=vm.console_relay_pid,
        exit_code=vm.exit_code,
        vcpu_count=vm.config.vcpu_count,
        mem_size_mib=vm.config.mem_size_mib,
        disk_size_mib=vm.disk_size_mib,
        rootfs_path=str(vm.config.rootfs_path) if vm.config.rootfs_path else "",
        rootfs_suffix=vm.rootfs_suffix,
        enable_api_socket=vm.config.enable_api_socket,
        enable_pci=vm.config.enable_pci,
        enable_logging=vm.config.enable_logging,
        enable_metrics=vm.config.enable_metrics,
        enable_console=vm.config.enable_console,
        created_at=created_at,
        updated_at=updated_at,
        lsm_flags=None,
    )


class VMManager:
    """Manages VM state persistence with hash-keyed storage."""

    def __init__(self, cache_dir: Path) -> None:
        self.cache_dir = cache_dir
        self.state_file = cache_dir / "vms" / "state.json"
        self.state_file.parent.mkdir(parents=True, exist_ok=True)

    def register(
        self,
        vm: VMInstance,
    ) -> None:
        """Register a VM in the database.

        All values are extracted from the VMInstance dataclass.
        Core does NOT provide fallback defaults.
        """
        from mvmctl.core.mvm_db import MVMDatabase

        db = MVMDatabase()
        db_state = _vm_instance_to_db_state(vm)
        db.upsert_vm(db_state)

    def deregister(self, vm_id: str) -> None:
        """Remove a VM from the database."""
        from mvmctl.core.mvm_db import MVMDatabase

        db = MVMDatabase()
        db.delete_vm(vm_id)

    def update_status(self, vm_id: str, status: VMStatus) -> None:
        """Update VM status in the database."""
        from mvmctl.core.mvm_db import MVMDatabase

        db = MVMDatabase()
        db.update_vm_status(vm_id, status.value)

    def list_all(self) -> list[VMInstance]:
        """List all VMs from the database."""
        from mvmctl.core.mvm_db import MVMDatabase

        db = MVMDatabase()
        db_vms = db.list_vms()
        return [_db_state_to_vm_instance(vm) for vm in db_vms]

    def get_by_name(self, name: str) -> list[VMInstance]:
        """Get VMs by name."""
        return [vm for vm in self.list_all() if vm.name == name]

    def find_by_id_prefix(self, prefix: str) -> list[VMInstance]:
        """Find VMs by ID prefix."""
        if len(prefix) < 3:
            return []
        return [vm for vm in self.list_all() if vm.id.startswith(prefix)]

    def get_by_full_id(self, vm_id: str) -> VMInstance | None:
        """Get a VM by its full ID.

        Args:
            vm_id: The full VM ID (16-character hex string)

        Returns:
            The VMInstance if found, None otherwise
        """
        for vm in self.list_all():
            if vm.id == vm_id:
                return vm
        return None

    def count_vms(self) -> int:
        """Count total VMs."""
        return len(self.list_all())

    def get(self, name: str) -> VMInstance | None:
        vms = self.get_by_name(name)
        return vms[0] if vms else None


def get_vm_manager() -> VMManager:
    from mvmctl.utils.fs import get_cache_dir

    return VMManager(get_cache_dir())


def _db_state_to_vm_instance(db_vm: DBVMInstance) -> VMInstance:
    """Convert DB VMState to VMInstance model."""
    from mvmctl.models.cloud_init import CloudInitMode
    from mvmctl.models.vm import VMConfig

    cloud_init_mode = CloudInitMode.INJECT
    if db_vm.cloud_init_mode:
        try:
            cloud_init_mode = CloudInitMode(db_vm.cloud_init_mode)
        except ValueError:
            pass

    config = VMConfig(
        name=db_vm.name,
        vcpu_count=db_vm.vcpu_count,
        mem_size_mib=db_vm.mem_size_mib,
        disk_size_mib=db_vm.disk_size_mib,
        lsm_flags=db_vm.lsm_flags or "",
        enable_api_socket=db_vm.enable_api_socket,
        enable_pci=db_vm.enable_pci,
        enable_logging=db_vm.enable_logging,
        enable_metrics=db_vm.enable_metrics,
        enable_console=db_vm.enable_console,
        cloud_init_mode=cloud_init_mode,
        rootfs_path=Path(db_vm.rootfs_path) if db_vm.rootfs_path else None,
    )

    return VMInstance(
        id=db_vm.id,
        name=db_vm.name,
        pid=db_vm.pid,
        ipv4=db_vm.ipv4,
        mac=db_vm.mac,
        network_id=db_vm.network_id,
        tap_device=db_vm.tap_device,
        created_at=datetime.fromisoformat(db_vm.created_at),
        updated_at=datetime.fromisoformat(db_vm.updated_at),
        status=VMStatus(db_vm.status),
        rootfs_suffix=db_vm.rootfs_suffix,
        kernel_id=db_vm.kernel_id,
        image_id=db_vm.image_id,
        binary_id=db_vm.binary_id,
        disk_size_mib=db_vm.disk_size_mib,
        config_path=Path(db_vm.config_path) if db_vm.config_path else None,
        api_socket_path=Path(db_vm.api_socket_path) if db_vm.api_socket_path else None,
        console_socket_path=Path(db_vm.relay_socket_path) if db_vm.relay_socket_path else None,
        config=config,
        nocloud_net_port=db_vm.nocloud_net_port,
        nocloud_server_pid=db_vm.nocloud_net_pid,
        console_relay_pid=db_vm.relay_pid,
        exit_code=db_vm.exit_code,
    )
