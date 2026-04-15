"""Dataclass models for the mvmctl SQLite database.

Each dataclass maps 1:1 to a database table. Fields match SQL schema constraints.

NOT NULL fields are required (no Optional), nullable fields use Optional.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Optional

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


@dataclass
class IPTablesRule:
    table_name: IPTablesTable
    chain_name: IPTablesChain
    rule_type: IPTablesRuleType
    target: IPTablesTarget
    network_id: str
    protocol: IPTablesProtocol
    source: str
    destination: str
    in_interface: str  # packet enters the host/network namespace
    out_interface: str  # packet exits the host/network namespace
    sport: int
    dport: int
    is_active: bool

    network_name: Optional[str] = None
    id: Optional[int] = None
    comment_tag: Optional[str] = None
    command_string: Optional[str] = None
    created_at: Optional[str] = None
    last_verified_at: Optional[str] = None


@dataclass
class Image:
    id: str
    os_slug: str
    os_name: str
    arch: str
    path: str
    fs_type: str
    fs_uuid: str
    minimum_rootfs_size_mib: int
    original_size: int
    is_default: bool
    created_at: str
    updated_at: str

    compressed_size: Optional[int] = None
    compression_ratio: Optional[float] = None
    compressed_format: Optional[str] = None
    pulled_at: Optional[str] = None


@dataclass
class Kernel:
    id: str
    name: str
    base_name: str
    version: str
    arch: str
    type: str
    path: str
    is_default: bool
    created_at: str
    updated_at: str


@dataclass
class Binary:
    id: str
    name: str
    version: str
    full_version: str
    ci_version: str
    path: str
    is_default: bool
    created_at: str
    updated_at: str


@dataclass
class Network:
    id: str
    name: str
    full_name: str
    subnet: str
    bridge: str
    ipv4_gateway: str
    bridge_active: bool
    nat_enabled: bool
    is_default: bool
    created_at: str
    updated_at: str

    nat_gateways: Optional[str] = None

    @property
    def nat_gateways_list(self) -> list[str]:
        """Return nat_gateways as a list of strings."""
        if not self.nat_gateways:
            return []
        return [gw.strip() for gw in self.nat_gateways.split(",") if gw.strip()]


@dataclass
class NetworkLease:
    network_id: str
    ipv4: str
    leased_at: str

    id: Optional[int] = None
    vm_id: Optional[str] = None
    expires_at: Optional[str] = None


@dataclass
class VMInstance:
    id: str
    name: str
    status: str
    pid: int
    ipv4: str
    mac: str
    network_id: str
    tap_device: str
    image_id: str
    kernel_id: str
    binary_id: str
    config_path: str
    vcpu_count: int
    mem_size_mib: int
    api_socket_path: str
    disk_size_mib: int
    rootfs_path: str
    rootfs_suffix: str
    created_at: str
    updated_at: str

    enable_pci: bool
    enable_logging: bool
    enable_metrics: bool
    enable_console: bool
    cloud_init_mode: str
    log_path: Optional[str] = None
    serial_output_path: Optional[str] = None
    nocloud_net_port: Optional[int] = None
    nocloud_net_pid: Optional[int] = None
    relay_socket_path: Optional[str] = None
    relay_pid: Optional[int] = None
    exit_code: Optional[int] = None
    lsm_flags: Optional[str] = None
    boot_args: Optional[str] = None


@dataclass
class HostState:
    id: int
    initialized: bool
    mvm_group_created: bool
    sudoers_configured: bool
    default_network_created: bool
    initialized_at: str
    updated_at: str


@dataclass
class HostStateChange:
    session_id: str
    init_timestamp: str
    setting: str
    mechanism: str
    applied_value: str
    change_order: int
    reverted: bool
    created_at: str

    id: Optional[int] = None
    original_value: Optional[str] = None
    reverted_at: Optional[str] = None
    revert_mechanism: Optional[str] = None


@dataclass
class SSHKey:
    """SSH key metadata stored in the database."""

    id: str
    name: str
    fingerprint: str
    algorithm: str
    comment: str
    public_key_path: str
    is_default: bool
    created_at: str
    updated_at: str

    private_key_path: Optional[str] = None
