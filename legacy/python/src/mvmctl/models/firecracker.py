"""Firecracker spawn configuration."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import NotRequired, TypedDict

from mvmctl.models.cloudinit import CloudInitMode


class CpuidRegisterModifier(TypedDict):
    """Modifier for a specific CPUID register within a leaf (x86_64)."""

    register: str  # eax, ebx, ecx, edx
    bitmap: str  # 32-char bitmap string: 0=clear, 1=set, x=ignore


class CpuidLeafModifier(TypedDict):
    """Modifier for a CPUID leaf and subleaf (x86_64)."""

    leaf: str  # hex, binary, or decimal string (e.g. "0x1", "0b1", "1")
    subleaf: str  # hex, binary, or decimal string
    flags: int  # KVM feature flags
    modifiers: list[CpuidRegisterModifier]


class MsrModifier(TypedDict):
    """Modifier for a model specific register (x86_64)."""

    addr: str  # MSR address (e.g. "0x10a")
    bitmap: str  # 64-char bitmap string


class ArmRegisterModifier(TypedDict):
    """Modifier for an ARM register (aarch64)."""

    addr: str  # 64-bit register address
    bitmap: str  # 128-char bitmap string


class VcpuFeatures(TypedDict):
    """vCPU feature modifier (aarch64)."""

    index: int  # Index in kvm_vcpu_init.features array
    bitmap: str  # 32-char bitmap string


class CpuConfig(TypedDict, total=False):
    """Firecracker CPU configuration — maps to PUT /cpu-config.

    All fields are optional (total=False) since a CPU config may only
    specify the subset of features being modified.
    """

    kvm_capabilities: list[str]  # KVM capability codes (e.g. ["56", "171"])
    cpuid_modifiers: list[CpuidLeafModifier]  # x86_64 only
    msr_modifiers: list[MsrModifier]  # x86_64 only
    reg_modifiers: list[ArmRegisterModifier]  # aarch64 only
    vcpu_features: list[VcpuFeatures]  # aarch64 only


class DriveConfig(TypedDict):
    drive_id: str
    path_on_host: str
    is_root_device: bool
    is_read_only: bool
    partuuid: NotRequired[str]
    cache_type: str
    io_engine: str
    rate_limiter: NotRequired[object | None]
    socket: NotRequired[str | None]


@dataclass
class FirecrackerConfig:
    """
    Explicit configuration for spawning a Firecracker VM.

    All values are resolved by the API layer before passing to the spawner.
    No defaults, no None for required fields.
    """

    # Paths
    vm_dir: Path
    rootfs_path: Path

    # Binary / kernel
    binary_path: str
    kernel_path: str

    # Machine
    vcpu_count: int
    mem_size_mib: int

    # Network
    guest_ip: str
    guest_mac: str
    tap_name: str
    network_gateway: str
    network_netmask: str

    # Image metadata
    image_fs_uuid: str | None
    image_fs_type: str

    # Boot
    boot_args: str | None
    lsm_flags: str | None

    # Feature flags
    pci_enabled: bool
    nested_virt: bool
    enable_console: bool
    enable_logging: bool
    enable_metrics: bool

    # File/path overrides (resolved from defaults.firecracker)
    log_level: str
    log_filename: str
    serial_output_filename: str
    metrics_filename: str
    api_socket_filename: str
    pid_filename: str
    config_filename: str

    # Cloud-init
    cloud_init_mode: CloudInitMode | None
    cloud_init_iso_path: Path | None
    cloud_init_nocloud_url: str | None

    # CPU config (nested virt / custom template)
    cpu_config: CpuConfig | None = None
    cpu_vendor: str | None = None
    cpu_architecture: str | None = None

    # Extra drives (volumes)
    extra_drives: list[DriveConfig] = field(default_factory=list)

    # Spawn behavior
    relay_enabled: bool = False
    relay_client_fd: int | None = None
    snapshot_mode: bool = False
