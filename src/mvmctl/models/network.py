"""Network data models."""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum

from mvmctl.constants import (
    MVM_FORWARD_CHAIN,
    MVM_NOCLOUD_NET_INPUT_CHAIN,
    MVM_POSTROUTING_CHAIN,
)


class IPTablesTable(str, Enum):
    FILTER = "filter"
    NAT = "nat"
    MANGLE = "mangle"
    RAW = "raw"
    SECURITY = "security"


class IPTablesChain(str, Enum):
    MVM_FORWARD = MVM_FORWARD_CHAIN
    MVM_POSTROUTING = MVM_POSTROUTING_CHAIN
    MVM_NOCLOUDNET_INPUT = MVM_NOCLOUD_NET_INPUT_CHAIN


class IPTablesRuleType(str, Enum):
    MASQUERADE = "masquerade"
    FORWARD_IN = "forward_in"
    FORWARD_OUT = "forward_out"
    NOCLOUDNET_INPUT = "nocloudnet_input"


class IPTablesProtocol(str, Enum):
    TCP = "tcp"
    UDP = "udp"
    ICMP = "icmp"
    ALL = "all"


class IPTablesTarget(str, Enum):
    ACCEPT = "ACCEPT"
    DROP = "DROP"
    REJECT = "REJECT"
    MASQUERADE = "MASQUERADE"
    LOG = "LOG"
    MARK = "MARK"


class IPTablesWildcard(str, Enum):
    ANY_CIDR = "0.0.0.0/0"
    ANY_INTERFACE = "*"


class IPTablesPort(int, Enum):
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

    # Resolved relations
    leases: list[NetworkLeaseItem] | None = None
    iptables_rules: list[IPTablesRuleItem] | None = None

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
class IPTablesRuleItem:
    """IPTables rule record — maps to iptables_rules table."""

    table_name: IPTablesTable
    chain_name: IPTablesChain
    rule_type: IPTablesRuleType
    protocol: IPTablesProtocol
    source: str
    destination: str
    in_interface: str
    out_interface: str
    target: IPTablesTarget
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
