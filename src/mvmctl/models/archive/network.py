from __future__ import annotations

from dataclasses import dataclass
from typing import Any


@dataclass
class NetworkInspectInfo:
    """Complete inspection output for a network."""

    name: str
    subnet: str
    ipv4_gateway: str
    bridge: str
    nat_enabled: bool
    nat_gateways: list[str]
    created_at: str
    bridge_exists: bool
    vms: list[dict[str, Any]]  # vm_id, ipv4, status, pid
