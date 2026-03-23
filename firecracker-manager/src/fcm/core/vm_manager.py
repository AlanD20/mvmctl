"""VM state management."""

import json
from pathlib import Path
from datetime import datetime
from typing import Any, cast

from fcm.models.vm import VMInstance, VMState
from fcm.utils.fs import get_vms_dir


class VMManager:
    """Manages VM state persistence."""

    def __init__(self, run_dir: Path | None = None) -> None:
        self.run_dir = Path(run_dir) if run_dir is not None else get_vms_dir()
        self.state_file = self.run_dir / "state.json"
        self._ensure_run_dir()

    def _ensure_run_dir(self) -> None:
        """Create run directory if it doesn't exist."""
        self.run_dir.mkdir(parents=True, exist_ok=True)

    def _load_state(self) -> dict[str, Any]:
        """Load state from JSON file."""
        if not self.state_file.exists():
            return {"vms": {}}
        with open(self.state_file, "r") as f:
            return cast(dict[str, Any], json.load(f))

    def _save_state(self, state: dict[str, Any]) -> None:
        """Save state to JSON file."""
        with open(self.state_file, "w") as f:
            json.dump(state, f, indent=2, default=str)

    def register(self, vm: VMInstance) -> None:
        """Register a new VM in state."""
        state = self._load_state()
        state["vms"][vm.name] = {
            "pid": vm.pid,
            "socket_path": str(vm.socket_path) if vm.socket_path else None,
            "ip": vm.ip,
            "mac": vm.mac,
            "network_name": vm.network_name,
            "created_at": vm.created_at.isoformat(),
            "status": vm.status.value,
        }
        self._save_state(state)

    def update_status(self, name: str, status: VMState) -> None:
        """Update the status of a registered VM."""
        state = self._load_state()
        if name not in state["vms"]:
            from fcm.exceptions import VMNotFoundError

            raise VMNotFoundError(f"VM '{name}' not found in state")
        state["vms"][name]["status"] = status.value
        self._save_state(state)

    def get(self, name: str) -> VMInstance | None:
        """Get VM by name."""
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
            created_at=datetime.fromisoformat(vm_data["created_at"]),
            status=VMState(vm_data["status"]),
        )

    def list_all(self) -> list[VMInstance]:
        """List all VMs."""
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
                    created_at=datetime.fromisoformat(vm_data["created_at"]),
                    status=VMState(vm_data["status"]),
                )
            )
        return vms

    def deregister(self, name: str) -> None:
        """Remove VM from state."""
        state = self._load_state()
        if name in state["vms"]:
            del state["vms"][name]
            self._save_state(state)
