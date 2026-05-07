from __future__ import annotations

from dataclasses import dataclass


@dataclass
class VolumeItem:
    """Persistent data disk attachable to VMs."""

    id: str
    name: str
    size_bytes: int
    format: str
    path: str
    status: str
    vm_id: str | None
    created_at: str
    updated_at: str
