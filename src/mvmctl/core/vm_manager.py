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

from mvmctl.models.vm import VMInstance, VMState
from mvmctl.utils.fs import get_vms_dir

logger = logging.getLogger(__name__)

_STATE_SCHEMA_VERSION = 1


def _is_hex_string(s: str, length: int = 64) -> bool:
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
    return hashlib.sha256(data.encode()).hexdigest()


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

            # Check if key is already a hash (64-char hex)
            if _is_hex_string(key, 64):
                # Already migrated
                new_vms[key] = vm_data
            else:
                # Old format: key is the VM name, need to migrate
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
            json.dump(state, f, indent=2, default=str)
        self.state_file.chmod(0o600)
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
            # Use vm.id as the key (full 64-char hash)
            vm_id = vm.id if vm.id else _generate_vm_id(vm.name, vm.created_at)
            state["vms"][vm_id] = {
                "id": vm_id,
                "name": vm.name,
                "pid": vm.pid,
                "socket_path": str(vm.socket_path) if vm.socket_path else None,
                "ip": vm.ip,
                "mac": vm.mac,
                "network_name": vm.network_name,
                "tap_device": vm.tap_device,
                "created_at": vm.created_at.isoformat(),
                "status": vm.status.value,
            }
            self._save_state(state)

    def update_status(self, name: str, status: VMState) -> None:
        """Update the status of a registered VM."""
        with self._locked():
            state = self._load_state()
            # Find VM by name
            for vm_id, vm_data in state["vms"].items():
                if vm_data.get("name") == name:
                    vm_data["status"] = status.value
                    self._save_state(state)
                    return
            from mvmctl.exceptions import VMNotFoundError

            raise VMNotFoundError(f"VM '{name}' not found in state")

    def get(self, name: str) -> VMInstance | None:
        """Get VM by name (searches all VMs for matching name)."""
        with self._locked(exclusive=False):
            state = self._load_state()
            for vm_id, vm_data in state["vms"].items():
                if vm_data.get("name") == name:
                    return self._vm_from_data(vm_id, vm_data)
            return None

    def get_by_short_id(self, short_id: str) -> VMInstance | None:
        """Find VM by short ID (first 6 chars of hash).

        Returns None if none or multiple VMs match.
        """
        with self._locked(exclusive=False):
            state = self._load_state()
            matches = [
                (vm_id, vm_data)
                for vm_id, vm_data in state["vms"].items()
                if vm_id.startswith(short_id)
            ]
            if len(matches) == 1:
                vm_id, vm_data = matches[0]
                return self._vm_from_data(vm_id, vm_data)
            return None

    def find_by_short_id(self, short_id: str) -> list[VMInstance]:
        """Return all VMs whose ID starts with short_id."""
        with self._locked(exclusive=False):
            state = self._load_state()
            results = []
            for vm_id, vm_data in state["vms"].items():
                if vm_id.startswith(short_id):
                    results.append(self._vm_from_data(vm_id, vm_data))
            return results

    def get_by_name(self, name: str) -> list[VMInstance]:
        """Return all VMs with the given name (may be multiple)."""
        with self._locked(exclusive=False):
            state = self._load_state()
            results = []
            for vm_id, vm_data in state["vms"].items():
                if vm_data.get("name") == name:
                    results.append(self._vm_from_data(vm_id, vm_data))
            return results

    def _vm_from_data(self, vm_id: str, vm_data: dict[str, Any]) -> VMInstance:
        """Create VMInstance from stored data."""
        return VMInstance(
            name=vm_data.get("name", ""),
            id=vm_id,
            pid=vm_data.get("pid"),
            socket_path=Path(vm_data["socket_path"]) if vm_data.get("socket_path") else None,
            ip=vm_data.get("ip"),
            mac=vm_data.get("mac"),
            network_name=vm_data.get("network_name"),
            tap_device=vm_data.get("tap_device"),
            created_at=datetime.fromisoformat(vm_data["created_at"]),
            status=VMState(vm_data["status"]),
        )

    def list_all(self) -> list[VMInstance]:
        """List all VMs."""
        with self._locked(exclusive=False):
            state = self._load_state()
            vms = []
            for vm_id, vm_data in state["vms"].items():
                vms.append(self._vm_from_data(vm_id, vm_data))
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


def get_vm_manager(run_dir: Path | None = None) -> VMManager:
    return VMManager(run_dir=run_dir)
