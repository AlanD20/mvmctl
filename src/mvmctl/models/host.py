"""Host data models."""

from __future__ import annotations

from dataclasses import dataclass, field

from mvmctl.utils.common import CommonUtils


@dataclass
class HostStateItem:
    """Host state record — maps to host_state table (singleton id=1)."""

    id: int
    initialized: bool
    mvm_group_created: bool
    sudoers_configured: bool
    default_network_created: bool
    initialized_at: str
    updated_at: str

    # Capacity detection fields (populated by host info --refresh or host init)
    hostname: str | None = None
    cpu_model: str | None = None
    cpu_vendor: str | None = None
    cpu_cores: int | None = None
    cpu_architecture: str | None = None
    numa_nodes: int | None = None
    memory_total_mib: int | None = None
    storage_total_bytes: int | None = None
    kernel_version: str | None = None
    os_release: str | None = None
    pid_max: int | None = None
    fd_max: int | None = None
    conntrack_max: int | None = None
    tap_devices_max: int | None = None
    ip_local_port_range: str | None = None
    detected_at: str | None = None

    # Virtualization detection fields
    cpu_has_vmx: int | None = None
    cpu_hypervisor: int | None = None
    nested_virt_available: int | None = None
    ept_available: int | None = None
    hugepage_count_2mb: int | None = None
    ksm_disabled: int | None = None
    cgroup_version: int | None = None
    swap_total_mib: int | None = None
    kernel_minimum_met: int | None = None

    def __post_init__(self) -> None:
        """Coerce bool fields loaded from SQLite."""
        CommonUtils.coerce_bool_fields(
            self,
            {
                "initialized",
                "mvm_group_created",
                "sudoers_configured",
                "default_network_created",
            },
        )


@dataclass
class HostStateChangeItem:
    """Host state change record — maps to host_state_changes table."""

    session_id: str
    init_timestamp: str
    setting: str
    mechanism: str
    applied_value: str
    reverted: bool
    change_order: int
    created_at: str

    id: int | None = None
    original_value: str | None = None
    reverted_at: str | None = None
    revert_mechanism: str | None = None

    def __post_init__(self) -> None:
        """Coerce bool fields loaded from SQLite."""
        CommonUtils.coerce_bool_fields(self, {"reverted"})


@dataclass
class HostHardware:
    """Detected host hardware capabilities."""

    hostname: str
    cpu_model: str
    cpu_vendor: str
    cpu_cores: int
    cpu_architecture: str
    numa_nodes: int
    memory_total_mib: int
    storage_total_bytes: int
    kernel_version: str
    os_release: str
    cpu_has_vmx: bool = False
    cpu_hypervisor: bool = False


@dataclass
class HostLimits:
    """Detected host kernel limits."""

    pid_max: int
    fd_max: int
    conntrack_max: int
    tap_devices_max: int
    ip_local_port_range: tuple[int, int]
    nested_virt_available: bool = False
    ept_available: bool = False
    hugepage_count_2mb: int = 0
    ksm_disabled: bool = True
    cgroup_version: int = 1
    swap_total_mib: int = 0
    kernel_minimum_met: bool = False


@dataclass
class HostResources:
    """Current host resource usage and capacity projection."""

    memory_available_mib: int
    tap_devices_used: int
    pids_current: int
    fd_current: int
    conntrack_current: int
    arp_current: int
    storage_free_bytes: int
    recommended_max_vms: int
    limiting_resource: str | None
    modules_loaded: dict[str, bool] = field(default_factory=dict)
    swap_used_mib: int = 0
    hugepages_free_2mb: int = 0
    smt_active: bool = False
    nftables_available: bool = False
    iptables_available: bool = False
    cloud_localds_available: bool = False
    dev_kvm_status: str = ""
    user_in_kvm_group: bool = False
    dev_net_tun_accessible: bool = False


@dataclass
class ProbeCheck:
    """Result of a single pre-flight probe check."""

    name: str
    passed: bool
    message: str
    details: str | None = None


@dataclass
class ProbeResult:
    """Aggregated pre-flight probe results."""

    critical: list[ProbeCheck] = field(default_factory=list)
    warnings: list[ProbeCheck] = field(default_factory=list)
    info: list[ProbeCheck] = field(default_factory=list)

    @property
    def has_critical(self) -> bool:
        """Return True if there are any failed critical checks."""
        return bool(self.critical)
