"""VM state management."""

import fcntl
import hashlib
import json
import logging
from collections.abc import Generator
from contextlib import contextmanager
from datetime import datetime
from pathlib import Path
from typing import IO, Any

from mvmctl.constants import CONST_FILE_PERMS_VM_STATE
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import VMState as DBVMState
from mvmctl.models.vm import VMInstance, VMState
from mvmctl.utils.fs import get_vms_dir

logger = logging.getLogger(__name__)

_STATE_SCHEMA_VERSION = 1


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


def _vm_instance_to_db_state(vm: VMInstance) -> DBVMState:
    """Convert VMInstance to DB VMState for SQLite storage."""
    return DBVMState(
        id=vm.id,
        name=vm.name,
        status=vm.status.value,
        pid=vm.pid,
        ipv4=vm.ipv4,
        mac=vm.mac,
        network_id=vm.network_name,
        tap_device=vm.tap_device,
        image_id=vm.image_id,
        kernel_id=vm.kernel_id,
        binary_id=None,
        api_socket_path=str(vm.api_socket_path) if vm.api_socket_path else None,
        console_socket_path=str(vm.console_socket_path) if vm.console_socket_path else None,
        config_path=None,
        cloud_init_mode=vm.cloud_init_mode.value if vm.cloud_init_mode else None,
        nocloud_net_port=vm.nocloud_net_port,
        nocloud_server_pid=vm.nocloud_server_pid,
        console_relay_pid=vm.console_relay_pid,
        exit_code=vm.exit_code,
        vcpu_count=vm.config.vcpu_count if vm.config else None,
        mem_size_mib=vm.config.mem_size_mib if vm.config else None,
        disk_size_mib=None,
        rootfs_path=str(vm.config.rootfs_path) if vm.config and vm.config.rootfs_path else None,
        rootfs_suffix=vm.rootfs_suffix if vm.rootfs_suffix else None,
        created_at=vm.created_at.isoformat() if vm.created_at else None,
        updated_at=None,
    )


def _db_state_to_vm_instance(state: DBVMState) -> VMInstance:
    """Convert DB VMState to VMInstance."""
    from mvmctl.models.cloud_init import CloudInitMode

    vm = VMInstance(
        name=state.name,
        id=state.id,
        pid=state.pid,
        api_socket_path=Path(state.api_socket_path) if state.api_socket_path else None,
        ipv4=state.ipv4,
        mac=state.mac,
        network_name=state.network_id,
        tap_device=state.tap_device,
        status=VMState(state.status) if state.status else VMState.STOPPED,
        cloud_init_mode=CloudInitMode(state.cloud_init_mode)
        if state.cloud_init_mode
        else CloudInitMode.INJECT,
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
        self.run_dir = Path(run_dir) if run_dir is not None else get_vms_dir()
        self.state_file = self.run_dir / "state.json"
        self._cache: dict[str, Any] | None = None
        self._ensure_run_dir()

    def _ensure_run_dir(self) -> None:
        """Create run directory if it doesn't exist."""
        self.run_dir.mkdir(parents=True, exist_ok=True)

    @property
    def _lock_path(self) -> Path:
        """Path to the lock file alongside state.json."""
        return Path(str(self.state_file) + ".lock")

    @contextmanager
    def _locked(self, exclusive: bool = True) -> Generator[None, None, None]:
        f: IO[str] = open(self._lock_path, "a+")
        try:
            fcntl.flock(f, fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH)
            self._cache = None
            yield None
        finally:
            fcntl.flock(f, fcntl.LOCK_UN)
            f.close()

    def _load_state(self) -> dict[str, Any]:
        """Load state from JSON file, using in-memory cache when available."""
        if self._cache is not None:
            return self._cache
        if not self.state_file.exists():
            return {"vms": {}, "schema_version": _STATE_SCHEMA_VERSION}
        try:
            with open(self.state_file, "r") as f:
                loaded = json.load(f)
                state: dict[str, Any] = loaded if isinstance(loaded, dict) else {}
        except (json.JSONDecodeError, ValueError):
            logger.warning("Corrupt state file at %s — resetting to empty", self.state_file)
            return {"vms": {}, "schema_version": _STATE_SCHEMA_VERSION}
        if "vms" not in state:
            return {"vms": {}, "schema_version": _STATE_SCHEMA_VERSION}
        file_version = state.get("schema_version")
        if file_version is None:
            logger.warning(
                "State file %s has no schema_version; assuming version %d",
                self.state_file,
                _STATE_SCHEMA_VERSION,
            )
            state["schema_version"] = _STATE_SCHEMA_VERSION
        elif file_version > _STATE_SCHEMA_VERSION:
            logger.warning(
                "State file %s has schema_version %d (expected <= %d); data may be incompatible",
                self.state_file,
                file_version,
                _STATE_SCHEMA_VERSION,
            )

        # Migrate old state format (name-keyed) to new format (hash-keyed)
        state = self._migrate_state_if_needed(state)

        self._cache = state
        return state

    def _migrate_state_if_needed(self, state: dict[str, Any]) -> dict[str, Any]:
        """Migrate state from name-keyed to hash-keyed format."""
        vms = state.get("vms", {})
        if not isinstance(vms, dict):
            return state

        migrated = False
        new_vms: dict[str, Any] = {}

        for key, vm_data in vms.items():
            if not isinstance(vm_data, dict):
                continue

            # Check if key is already a hash (16-char hex)
            if _is_hex_string(key, 16):
                # Already migrated
                new_vms[key] = vm_data
            else:
                # Old format: key is the VM name or legacy hash, need to migrate
                name = key
                created_at_str = vm_data.get("created_at", datetime.now().isoformat())
                try:
                    created_at = datetime.fromisoformat(created_at_str)
                except ValueError:
                    created_at = datetime.now()

                # Generate new hash-based ID
                new_id = _generate_vm_id(name, created_at)

                # Ensure the VM data has the name field and id field
                new_vm_data = dict(vm_data)
                new_vm_data["name"] = name
                new_vm_data["id"] = new_id

                new_vms[new_id] = new_vm_data
                migrated = True
                logger.info("Migrated VM '%s' to new hash-based ID format", name)

        if migrated:
            state["vms"] = new_vms
            self._save_state(state)
            logger.info("State migration complete — all VMs now use hash-based IDs")

        return state

    def _save_state(self, state: dict[str, Any]) -> None:
        """Save state to JSON file and update cache."""
        state["schema_version"] = _STATE_SCHEMA_VERSION
        with open(self.state_file, "w") as f:
            json.dump(state, f, default=str)
        self.state_file.chmod(CONST_FILE_PERMS_VM_STATE)
        self._cache = state

    def _count_vms_from_file(self) -> int:
        if not self.state_file.exists():
            return 0
        try:
            with open(self.state_file, "r") as f:
                loaded = json.load(f)
        except (json.JSONDecodeError, OSError, ValueError):
            logger.warning("Corrupt state file at %s — resetting VM count to zero", self.state_file)
            return 0

        if not isinstance(loaded, dict):
            return 0

        vms = loaded.get("vms", {})
        if not isinstance(vms, dict):
            return 0

        return len(vms)

    def register(self, vm: VMInstance) -> None:
        """Register a new VM in the shared ``state.json`` registry.

        The ``socket_path`` is persisted here (rather than in per-VM
        directories) so that ``list_all`` / ``get`` can return fully-
        hydrated ``VMInstance`` objects from a single read, avoiding a
        per-VM directory scan.
        """
        with self._locked():
            state = self._load_state()
            vm_id = vm.id if vm.id else _generate_vm_id(vm.name, vm.created_at)
            vm_data = vm.to_dict()
            vm_data["id"] = vm_id
            state["vms"][vm_id] = vm_data
            self._save_state(state)

        # NEW: Also write to SQLite (dual-write pattern)
        try:
            db = MVMDatabase()
            db.upsert_vm(_vm_instance_to_db_state(vm))
        except Exception:
            pass  # Don't break if SQLite fails

    def update_status(self, name: str, status: VMState) -> None:
        """Update the status of a registered VM."""
        vm_id = None
        with self._locked():
            state = self._load_state()
            # Find VM by name
            for vm_id_key, vm_data in state["vms"].items():
                if vm_data.get("name") == name:
                    vm_data["status"] = status.value
                    vm_id = vm_id_key
                    self._save_state(state)
                    break
            if vm_id is None:
                from mvmctl.exceptions import VMNotFoundError

                raise VMNotFoundError(f"VM '{name}' not found in state")

        # NEW: Also update SQLite
        if vm_id:
            try:
                db = MVMDatabase()
                db.update_vm_status(vm_id, status.value)
            except Exception:
                pass

    def get(self, name: str) -> VMInstance | None:
        """Get VM by name (searches all VMs for matching name)."""
        # Try SQLite first
        try:
            db = MVMDatabase()
            db_vm = db.get_vm_by_name(name)
            if db_vm:
                return _db_state_to_vm_instance(db_vm)
        except Exception:
            pass

        # Fall back to JSON
        with self._locked(exclusive=False):
            state = self._load_state()
            for vm_id, vm_data in state["vms"].items():
                if vm_data.get("name") == name:
                    return VMInstance.from_dict(vm_data)
            return None

    def get_by_id_prefix(self, prefix: str) -> VMInstance | None:
        """Find VM by ID prefix.

        Returns None if none or multiple VMs match.
        """
        # Try SQLite first
        try:
            db = MVMDatabase()
            db_vms = db.find_vms_by_prefix(prefix)
            if len(db_vms) == 1:
                return _db_state_to_vm_instance(db_vms[0])
        except Exception:
            pass

        # Fall back to JSON
        with self._locked(exclusive=False):
            state = self._load_state()
            matches = [
                (vm_id, vm_data)
                for vm_id, vm_data in state["vms"].items()
                if vm_id.startswith(prefix)
            ]
            if len(matches) == 1:
                vm_id, vm_data = matches[0]
                return VMInstance.from_dict(vm_data)
            return None

    def get_by_full_id(self, full_hash: str) -> VMInstance | None:
        """Find VM by exact hash ID.

        Performs exact-match lookup for collision-free VM identification.
        Returns None if no VM with the exact hash exists.

        Args:
            full_id: Full 16-character hash ID of the VM

        Returns:
            VMInstance if found, None otherwise
        """
        # Try SQLite first
        try:
            db = MVMDatabase()
            db_vm = db.get_vm(full_hash)
            if db_vm:
                return _db_state_to_vm_instance(db_vm)
        except Exception:
            pass

        # Fall back to JSON
        with self._locked(exclusive=False):
            state = self._load_state()
            vm_data = state["vms"].get(full_hash)
            if vm_data is None:
                return None
            vm = VMInstance.from_dict(vm_data)
            # Defense-in-depth: verify the ID matches exactly
            if vm.id != full_hash:
                logger.warning("VM ID mismatch: looked up %s but got %s", full_hash, vm.id)
                return None
            return vm

    def find_by_id_prefix(self, prefix: str) -> list[VMInstance]:
        """Return all VMs whose ID starts with prefix."""
        # Try SQLite first
        try:
            db = MVMDatabase()
            db_vms = db.find_vms_by_prefix(prefix)
            if db_vms:
                return [_db_state_to_vm_instance(vm) for vm in db_vms]
        except Exception:
            pass

        # Fall back to JSON
        with self._locked(exclusive=False):
            state = self._load_state()
            results = []
            for vm_id, vm_data in state["vms"].items():
                if vm_id.startswith(prefix):
                    results.append(VMInstance.from_dict(vm_data))
            return results

    def get_by_name(self, name: str) -> list[VMInstance]:
        """Return all VMs with the given name (may be multiple)."""
        # Try SQLite first
        try:
            db = MVMDatabase()
            db_vm = db.get_vm_by_name(name)
            if db_vm:
                return [_db_state_to_vm_instance(db_vm)]
        except Exception:
            pass

        # Fall back to JSON
        with self._locked(exclusive=False):
            state = self._load_state()
            results = []
            for vm_id, vm_data in state["vms"].items():
                if vm_data.get("name") == name:
                    results.append(VMInstance.from_dict(vm_data))
            return results

    def list_all(self) -> list[VMInstance]:
        """List all VMs."""
        # Try SQLite first
        try:
            db = MVMDatabase()
            db_vms = db.list_vms()
            if db_vms:
                return [_db_state_to_vm_instance(vm) for vm in db_vms]
        except Exception:
            pass

        # Fall back to JSON
        with self._locked(exclusive=False):
            state = self._load_state()
            vms = []
            for vm_id, vm_data in state["vms"].items():
                vms.append(VMInstance.from_dict(vm_data))
            return vms

    def count_vms(self) -> int:
        """Return the number of VMs without loading full metadata."""
        with self._locked(exclusive=False):
            if self._cache is not None:
                vms = self._cache.get("vms", {})
                if isinstance(vms, dict):
                    return len(vms)
                return 0
            return self._count_vms_from_file()

    def deregister(self, vm_id: str) -> None:
        """Remove VM from state by full hash ID."""
        with self._locked():
            state = self._load_state()
            if vm_id in state["vms"]:
                del state["vms"][vm_id]
                self._save_state(state)

        # NEW: Also delete from SQLite
        try:
            db = MVMDatabase()
            db.delete_vm(vm_id)
        except Exception:
            pass


def get_vm_manager(run_dir: Path | None = None) -> VMManager:
    return VMManager(run_dir=run_dir)


def _get_exit_code_from_log(log_file: Path) -> int | None:
    """Parse exit code from Firecracker log file.

    Args:
        log_file: Path to the Firecracker log file

    Returns:
        Exit code if found, None otherwise
    """
    if not log_file.exists():
        return None

    import re

    content = log_file.read_text()

    # Look for various exit code patterns
    patterns = [
        r"exit code:\s*(\d+)",
        r"exited:\s*(\d+)",
        r"exit\s+(\d+)",
        r"Exit Code:\s*(\d+)",
        r"EXIT CODE:\s*(\d+)",
    ]

    for pattern in patterns:
        match = re.search(pattern, content, re.IGNORECASE)
        if match:
            return int(match.group(1))

    return None
