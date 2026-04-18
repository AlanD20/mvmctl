"""Enum constants for the mvmctl SQLite database.

Dataclass models have been migrated to domain-specific files in models/.
This file retains only the enum classes still imported by archive code.
"""

from __future__ import annotations

from enum import Enum

from mvmctl.constants import (
    MVM_FORWARD_CHAIN,
    MVM_NOCLOUD_NET_INPUT_CHAIN,
    MVM_POSTROUTING_CHAIN,
)


class IPTablesRuleType(str, Enum):
    MASQUERADE = "masquerade"
    FORWARD_IN = "forward_in"
    FORWARD_OUT = "forward_out"
    NOCLOUDNET_INPUT = "nocloudnet_input"


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
