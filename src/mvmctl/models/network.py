"""Network data models."""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import TYPE_CHECKING

from mvmctl.constants import (
    MVM_FORWARD_CHAIN,
    MVM_NOCLOUD_NET_INPUT_CHAIN,
    MVM_POSTROUTING_CHAIN,
)
from mvmctl.utils.common import CommonUtils

if TYPE_CHECKING:
    from mvmctl.models.vm import VMInstanceItem


class FirewallBackendType(str, Enum):
    """Firewall backend type — iptables or nftables."""

    IPTABLES = "iptables"
    NFTABLES = "nftables"


class FirewallTable(str, Enum):
    FILTER = "filter"
    NAT = "nat"
    MANGLE = "mangle"
    RAW = "raw"
    SECURITY = "security"


class FirewallChain(str, Enum):
    MVM_FORWARD = MVM_FORWARD_CHAIN
    MVM_POSTROUTING = MVM_POSTROUTING_CHAIN
    MVM_NOCLOUDNET_INPUT = MVM_NOCLOUD_NET_INPUT_CHAIN


class FirewallRuleType(str, Enum):
    MASQUERADE = "masquerade"
    FORWARD_IN = "forward_in"
    FORWARD_OUT = "forward_out"
    NOCLOUDNET_INPUT = "nocloudnet_input"


class FirewallProtocol(str, Enum):
    TCP = "tcp"
    UDP = "udp"
    ICMP = "icmp"
    ALL = "all"


class FirewallTarget(str, Enum):
    ACCEPT = "ACCEPT"
    DROP = "DROP"
    REJECT = "REJECT"
    MASQUERADE = "MASQUERADE"
    LOG = "LOG"
    MARK = "MARK"


class FirewallWildcard(str, Enum):
    ANY_CIDR = "0.0.0.0/0"
    ANY_INTERFACE = "*"


class FirewallPort(int, Enum):
    ANY = 0


@dataclass
class NetworkItem:
    """Network record — maps to networks table."""

    id: str
    name: str
    subnet: str
    bridge: str
    ipv4_gateway: str
    bridge_active: bool
    nat_enabled: bool
    is_default: bool
    is_present: bool
    created_at: str
    updated_at: str
    deleted_at: str | None = None

    nat_gateways: str | None = None

    def __post_init__(self) -> None:
        """Coerce bool fields loaded from SQLite."""
        CommonUtils.coerce_bool_fields(
            self,
            {
                "bridge_active",
                "nat_enabled",
                "is_default",
                "is_present",
            },
        )

    # Resolved relations
    leases: list[NetworkLeaseItem] | None = None
    iptables_rules: list[FirewallRule] | None = None
    vms: list[VMInstanceItem] | None = None

    @property
    def nat_gateways_list(self) -> list[str]:
        """Return nat_gateways as a list of strings."""
        if not self.nat_gateways:
            return []
        return [gw.strip() for gw in self.nat_gateways.split(",") if gw.strip()]


@dataclass
class NetworkLeaseItem:
    """Network lease record — maps to network_leases table."""

    network_id: str
    ipv4: str
    leased_at: str

    id: int | None = None
    vm_id: str | None = None
    expires_at: str | None = None


@dataclass
class FirewallRule:
    """Firewall rule record — maps to iptables_rules or nftables_rules table."""

    table_name: FirewallTable
    chain_name: FirewallChain
    rule_type: FirewallRuleType
    protocol: FirewallProtocol
    source: str
    destination: str
    in_interface: str
    out_interface: str
    target: FirewallTarget
    sport: int
    dport: int
    network_id: str
    is_active: bool

    id: int | None = None
    network_name: str | None = None
    comment_tag: str | None = None
    command_string: str | None = None
    created_at: str | None = None
    last_verified_at: str | None = None

    def __post_init__(self) -> None:
        """Coerce bool fields loaded from SQLite."""
        CommonUtils.coerce_bool_fields(self, {"is_active"})


@dataclass
class FirewallRuleResult:
    """Result of a firewall rule operation — used by both iptables and nftables trackers."""

    success: bool
    rule: FirewallRule | None = None
    error_message: str | None = None
    command_executed: str | None = None
