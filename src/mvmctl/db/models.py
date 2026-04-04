"""Dataclass models for the mvmctl SQLite database.

Each dataclass maps 1:1 to a database table. Fields use Python-native types.

Notes:
    - Timestamps are stored as ISO-format strings in SQLite.
    - BOOLEAN columns (SQLite INTEGER 0/1) map to Python bool.
    - Optional fields default to None and map to nullable columns.
    - Primary keys for most tables are 64-character SHA256 hashes.
    - Exceptions: binary_defaults (name TEXT PK), network_leases (AUTOINCREMENT),
      host_state (singleton id=1), host_state_changes (AUTOINCREMENT),
      db_migrations (AUTOINCREMENT).
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional


@dataclass
class Image:
    """OS image metadata — maps to the images table."""

    id: str  # 64-char SHA256 hash (primary key)
    os_slug: str  # e.g., "alpine-3.21" (from JSON internal_id)
    path: str  # Full filesystem path (from JSON filename)
    os_name: Optional[str] = None
    fs_type: Optional[str] = None
    fs_uuid: Optional[str] = None
    compressed_size: Optional[int] = None
    original_size: Optional[int] = None
    compression_ratio: Optional[float] = None
    compressed_format: Optional[str] = None
    pulled_at: Optional[str] = None
    is_default: bool = False
    created_at: Optional[str] = None
    updated_at: Optional[str] = None


@dataclass
class Kernel:
    """Firecracker kernel metadata — maps to the kernels table."""

    id: str  # 64-char SHA256 hash (primary key)
    name: str
    version: str
    arch: str
    path: str  # Full filesystem path (from JSON filename)
    base_name: Optional[str] = None
    type: Optional[str] = None
    is_default: bool = False
    created_at: Optional[str] = None
    updated_at: Optional[str] = None  # Maps from JSON last_modified


@dataclass
class Binary:
    """Firecracker binary metadata — maps to the binaries table."""

    id: str  # 64-char SHA256 hash (primary key)
    name: str  # "firecracker" or "jailer"
    version: str  # Maps from JSON package_version
    path: str
    full_version: Optional[str] = None
    ci_version: Optional[str] = None
    is_default: bool = False
    created_at: Optional[str] = None
    updated_at: Optional[str] = None


@dataclass
class Network:
    """Named network definition — maps to the networks table."""

    id: str  # 64-char SHA256 hash (primary key)
    name: str
    subnet: str  # CIDR notation (maps from JSON cidr)
    bridge: str
    ipv4_gateway: str  # Maps from JSON gateway
    bridge_active: bool = False
    nat_gateways: Optional[str] = None  # Comma-separated interface names
    nat_enabled: bool = False
    is_default: bool = False
    created_at: Optional[str] = None
    updated_at: Optional[str] = None


@dataclass
class NetworkLease:
    """IP lease record — maps to the network_leases table.

    Primary key is INTEGER AUTOINCREMENT (id), not a hash.
    expires_at is NULL by default: leases are valid for VM's entire lifecycle.
    """

    network_id: str  # FK to networks(id)
    ipv4: str  # Maps from JSON ip
    id: Optional[int] = None  # AUTOINCREMENT (None before insert)
    vm_id: Optional[str] = None  # FK to vm_states(id), maps from JSON vm_name
    leased_at: Optional[str] = None
    expires_at: Optional[str] = None  # NULL = no expiry


@dataclass
class VMState:
    """VM runtime state — maps to the vm_states table."""

    id: str  # 64-char SHA256 hash (primary key)
    name: str
    status: str  # Validated in code, no CHECK constraint
    pid: Optional[int] = None
    ipv4: Optional[str] = None  # Maps from JSON ip
    mac: Optional[str] = None
    network_id: Optional[str] = None  # FK, maps from JSON network_name
    tap_device: Optional[str] = None
    image_id: Optional[str] = None  # FK to images(id)
    kernel_id: Optional[str] = None  # FK to kernels(id)
    binary_id: Optional[str] = None  # FK to binaries(id)
    api_socket_path: Optional[str] = None  # Maps from JSON socket_path
    console_socket_path: Optional[str] = None
    config_path: Optional[str] = None
    cloud_init_mode: Optional[str] = None  # Validated in code
    nocloud_net_port: Optional[int] = None
    nocloud_server_pid: Optional[int] = None
    console_relay_pid: Optional[int] = None
    exit_code: Optional[int] = None
    vcpu_count: Optional[int] = None
    mem_size_mib: Optional[int] = None
    disk_size_mib: Optional[int] = None
    rootfs_path: Optional[str] = None
    rootfs_suffix: Optional[str] = None
    created_at: Optional[str] = None
    updated_at: Optional[str] = None


@dataclass
class HostState:
    """Host initialization state (singleton) — maps to the host_state table.

    There is always exactly one row with id=1. The application enforces
    this invariant (no CHECK constraint in the schema).
    """

    id: int  # Always 1 (singleton)
    initialized: bool = False
    mvm_group_created: bool = False
    sudoers_configured: bool = False
    default_network_created: bool = False
    initialized_at: Optional[str] = None
    updated_at: Optional[str] = None


@dataclass
class HostStateChange:
    """Host configuration change record — maps to the host_state_changes table.

    Tracks every change made during ``mvm host init``.
    Changes are reverted in reverse order (LIFO) during ``mvm host reset``.
    Primary key is INTEGER AUTOINCREMENT.
    """

    session_id: str
    init_timestamp: str
    setting: str
    mechanism: str
    applied_value: str
    change_order: int
    id: Optional[int] = None  # AUTOINCREMENT (None before insert)
    original_value: Optional[str] = None
    reverted: bool = False
    reverted_at: Optional[str] = None
    revert_mechanism: Optional[str] = None
    created_at: Optional[str] = None
