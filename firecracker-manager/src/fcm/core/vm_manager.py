"""VM state management."""

import fcntl
import json
import logging
from collections.abc import Generator
from contextlib import contextmanager
from pathlib import Path
from datetime import datetime
from typing import Any, IO

from fcm.models.vm import VMInstance, VMState
from fcm.utils.fs import get_vms_dir

logger = logging.getLogger(__name__)

_STATE_SCHEMA_VERSION = 1


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
        self._cache = state
        return state

    def _save_state(self, state: dict[str, Any]) -> None:
        """Save state to JSON file and update cache."""
        state["schema_version"] = _STATE_SCHEMA_VERSION
        with open(self.state_file, "w") as f:
            json.dump(state, f, indent=2, default=str)
        self.state_file.chmod(0o600)
        self._cache = state

    def register(self, vm: VMInstance) -> None:
        """Register a new VM in the shared ``state.json`` registry.

        The ``socket_path`` is persisted here (rather than in per-VM
        directories) so that ``list_all`` / ``get`` can return fully-
        hydrated ``VMInstance`` objects from a single read, avoiding a
        per-VM directory scan.
        """
        with self._locked():
            state = self._load_state()
            state["vms"][vm.name] = {
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
            if name not in state["vms"]:
                from fcm.exceptions import VMNotFoundError

                raise VMNotFoundError(f"VM '{name}' not found in state")
            state["vms"][name]["status"] = status.value
            self._save_state(state)

    def get(self, name: str) -> VMInstance | None:
        """Get VM by name."""
        with self._locked(exclusive=False):
            state = self._load_state()
            vm_data = state["vms"].get(name)
            if not vm_data:
                return None

            return VMInstance(
                name=name,
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
            for name, vm_data in state["vms"].items():
                vms.append(
                    VMInstance(
                        name=name,
                        pid=vm_data.get("pid"),
                        socket_path=Path(vm_data["socket_path"])
                        if vm_data.get("socket_path")
                        else None,
                        ip=vm_data.get("ip"),
                        mac=vm_data.get("mac"),
                        network_name=vm_data.get("network_name"),
                        tap_device=vm_data.get("tap_device"),
                        created_at=datetime.fromisoformat(vm_data["created_at"]),
                        status=VMState(vm_data["status"]),
                    )
                )
            return vms

    def deregister(self, name: str) -> None:
        """Remove VM from state."""
        with self._locked():
            state = self._load_state()
            if name in state["vms"]:
                del state["vms"][name]
                self._save_state(state)


def get_vm_manager(run_dir: Path | None = None) -> VMManager:
    return VMManager(run_dir=run_dir)
