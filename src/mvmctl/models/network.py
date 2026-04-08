from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import TYPE_CHECKING, Any, TypedDict

if TYPE_CHECKING:
    from mvmctl.db.models import Network as DBNetwork


@dataclass
class NetworkLease:
    vm_id: str
    ipv4: str


@dataclass
class NetworkConfig:
    name: str
    subnet: str
    ipv4_gateway: str
    bridge: str
    nat_enabled: bool = True
    nat_gateways: list[str] = field(default_factory=list)
    created_at: str = field(default_factory=lambda: datetime.now(tz=timezone.utc).isoformat())
    is_default: bool = False


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


@dataclass
class NetworkItem:
    id: str
    name: str
    subnet: str
    bridge: str
    ipv4_gateway: str
    bridge_active: bool = False
    nat_gateways: str | None = None
    nat_enabled: bool = False
    is_default: bool = False
    created_at: str | None = None
    updated_at: str | None = None

    @classmethod
    def from_db(cls, record: "DBNetwork") -> "NetworkItem":
        return cls(
            id=record.id,
            name=record.name,
            subnet=record.subnet,
            bridge=record.bridge,
            ipv4_gateway=record.ipv4_gateway,
            bridge_active=record.bridge_active,
            nat_gateways=record.nat_gateways,
            nat_enabled=record.nat_enabled,
            is_default=record.is_default,
            created_at=record.created_at,
            updated_at=record.updated_at,
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "network_id": self.id,
            "subnet": self.subnet,
            "bridge": self.bridge,
            "ipv4_gateway": self.ipv4_gateway,
            "bridge_active": self.bridge_active,
            "nat_gateways": self.nat_gateways.split(",") if self.nat_gateways else [],
            "nat_enabled": self.nat_enabled,
            "is_default": 1 if self.is_default else 0,
            "created_at": self.created_at,
            "updated_at": self.updated_at,
        }
