"""Dataclass models for the mvmctl SQLite database.

Each dataclass maps 1:1 to a database table. Fields match SQL schema constraints.

NOT NULL fields are required (no Optional), nullable fields use Optional.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Optional


class IPTablesRuleType(str, Enum):
    MASQUERADE = "masquerade"
    FORWARD_IN = "forward_in"
    FORWARD_OUT = "forward_out"
    NOCLOUD_INPUT = "nocloud_input"


class IPTablesProtocol(str, Enum):
    TCP = "tcp"
    UDP = "udp"
    ICMP = "icmp"
    ALL = "all"


class IPTablesWildcard(str, Enum):
    ANY_CIDR = "0.0.0.0/0"
    ANY_INTERFACE = "*"


class IPTablesPort(int, Enum):
    ANY = 0


@dataclass
class IPTablesRule:
    table_name: str
    chain_name: str
    rule_type: IPTablesRuleType
    target: str
    network_id: str
    protocol: IPTablesProtocol
    source: str
    destination: str
    in_interface: str
    out_interface: str
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
    subnet: str
    bridge: str
    ipv4_gateway: str
    bridge_active: bool
    nat_enabled: bool
    is_default: bool
    created_at: str
    updated_at: str

    nat_gateways: Optional[str] = None


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
    cloud_init_mode: str
    vcpu_count: int
    mem_size_mib: int
    disk_size_mib: int
    rootfs_path: str
    rootfs_suffix: str
    created_at: str
    updated_at: str

    enable_api_socket: bool
    enable_pci: bool
    enable_logging: bool
    enable_metrics: bool
    enable_console: bool
    api_socket_path: Optional[str] = None
    console_socket_path: Optional[str] = None
    nocloud_net_port: Optional[int] = None
    nocloud_server_pid: Optional[int] = None
    console_relay_pid: Optional[int] = None
    exit_code: Optional[int] = None
    lsm_flags: Optional[str] = None


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
