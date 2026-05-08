from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum, auto


class VolumeStatus(StrEnum):
    """Volume lifecycle states."""

    AVAILABLE = auto()
    ATTACHED = auto()


@dataclass
class VolumeItem:
    """Persistent data disk attachable to VMs."""

    id: str
    name: str
    size_bytes: int
    format: str
    path: str
    status: VolumeStatus
    vm_id: str | None
    created_at: str
    updated_at: str

    def __post_init__(self) -> None:
        """Coerce status from string when loading from DB."""
        if isinstance(self.status, str) and not isinstance(
            self.status, VolumeStatus
        ):
            self.status = VolumeStatus(self.status)
