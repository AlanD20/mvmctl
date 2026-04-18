"""Network input models for API boundary."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone


@dataclass
class NetworkConfig:
    name: str
    subnet: str
    ipv4_gateway: str
    bridge: str
    nat_enabled: bool = True
    nat_gateways: list[str] = field(default_factory=list)
    created_at: str = field(
        default_factory=lambda: datetime.now(tz=timezone.utc).isoformat()
    )
    is_default: bool = False


@dataclass
class NetworkCreateInput:
    name: str
    subnet: str
    bridge: str
    nat_enabled: bool = True
    nat_gateways: list[str] = field(default_factory=list)
