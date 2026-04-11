"""VM removal classes - VMRemovalContext, VMBulkCleanupContext.

These are PURE STATE TRACKERS. They do NOT call core modules directly.
Core call sequencing stays in _registry.py (the orchestrator).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from mvmctl.core.vm_manager import VMManager
    from mvmctl.models.network import NetworkConfig
    from mvmctl.models.vm import VMInstance


@dataclass
class VMRemovalContext:
    """Manages VM removal state. Does NOT call core modules directly.

    Core call sequencing stays in _registry.py (the orchestrator).
    """

    vm: VMInstance
    vm_dir: Path
    net_config: NetworkConfig | None
    bridge: str
    manager: VMManager
    _pid: int | None = None

    @property
    def pid(self) -> int | None:
        """Get the PID of the VM process."""
        return self._pid

    @pid.setter
    def pid(self, value: int | None) -> None:
        """Set the PID of the VM process."""
        self._pid = value


@dataclass
class VMBulkCleanupContext:
    """Manages bulk VM cleanup state. Does NOT call core modules directly.

    Core call sequencing stays in _registry.py (the orchestrator).
    """

    manager: VMManager
    cache_dir: Path
    _targets: list[VMInstance] = field(default_factory=list)

    @property
    def targets(self) -> list[VMInstance]:
        """Get the list of VMs targeted for cleanup."""
        return self._targets

    def set_targets(self, vms: list[VMInstance]) -> None:
        """Set the list of VMs to clean up."""
        self._targets = vms


__all__ = ["VMRemovalContext", "VMBulkCleanupContext"]
