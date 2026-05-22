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

    @classmethod
    def from_state(cls, state: HostStateItem) -> HostHardware | None:
        """Reconstruct HostHardware from stored state, or None if not yet detected."""
        if state.cpu_model is None:
            return None
        return cls(
            hostname=state.hostname or "",
            cpu_model=state.cpu_model or "",
            cpu_vendor=state.cpu_vendor or "",
            cpu_cores=state.cpu_cores or 0,
            cpu_architecture=state.cpu_architecture or "",
            numa_nodes=state.numa_nodes or 1,
            memory_total_mib=state.memory_total_mib or 0,
            storage_total_bytes=state.storage_total_bytes or 0,
            kernel_version=state.kernel_version or "",
            os_release=state.os_release or "",
            cpu_has_vmx=bool(state.cpu_has_vmx)
            if state.cpu_has_vmx is not None
            else False,
            cpu_hypervisor=bool(state.cpu_hypervisor)
            if state.cpu_hypervisor is not None
            else False,
        )


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

    @classmethod
    def from_state(cls, state: HostStateItem) -> HostLimits | None:
        """Reconstruct HostLimits from stored state, or None if not yet detected."""
        if state.pid_max is None:
            return None
        port_range = (32768, 60999)
        if state.ip_local_port_range:
            try:
                parts = state.ip_local_port_range.split(",")
                if len(parts) == 2:
                    port_range = (int(parts[0]), int(parts[1]))
            except (ValueError, TypeError):
                pass
        return cls(
            pid_max=state.pid_max or 0,
            fd_max=state.fd_max or 0,
            conntrack_max=state.conntrack_max or 0,
            tap_devices_max=state.tap_devices_max
            if state.tap_devices_max is not None
            else 0,
            ip_local_port_range=port_range,
            nested_virt_available=bool(state.nested_virt_available)
            if state.nested_virt_available is not None
            else False,
            ept_available=bool(state.ept_available)
            if state.ept_available is not None
            else False,
            hugepage_count_2mb=state.hugepage_count_2mb or 0,
            ksm_disabled=bool(state.ksm_disabled)
            if state.ksm_disabled is not None
            else True,
            cgroup_version=state.cgroup_version or 1,
            swap_total_mib=state.swap_total_mib or 0,
            kernel_minimum_met=bool(state.kernel_minimum_met)
            if state.kernel_minimum_met is not None
            else False,
        )


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


@dataclass
class HostInfo:
    """Aggregated host detection results — wraps all four host models into one."""

    state: HostStateItem
    resources: HostResources
    limits: HostLimits
    hardware: HostHardware

    def to_dict(self) -> dict[str, object]:
        """Build the standardised info response dict from all four sub-models."""
        return {
            "detected_at": self.state.detected_at or "",
            "hostname": self.hardware.hostname,
            "os": {
                "kernel": self.hardware.kernel_version,
                "release": self.hardware.os_release,
            },
            "cpu": {
                "model": self.hardware.cpu_model,
                "vendor": self.hardware.cpu_vendor,
                "cores": self.hardware.cpu_cores,
                "architecture": self.hardware.cpu_architecture,
                "numa_nodes": self.hardware.numa_nodes,
            },
            "virtualization": {
                "cpu_has_vmx": self.hardware.cpu_has_vmx,
                "nested_virt_available": self.limits.nested_virt_available,
                "ept_available": self.limits.ept_available,
                "hypervisor": self.hardware.cpu_hypervisor,
                "smt_active": self.resources.smt_active,
                "modules": dict(self.resources.modules_loaded),
            },
            "hugepages": {
                "count_2mb": self.limits.hugepage_count_2mb,
                "free_2mb": self.resources.hugepages_free_2mb,
            },
            "dependencies": {
                "nftables_available": self.resources.nftables_available,
                "iptables_available": self.resources.iptables_available,
                "cloud_localds_available": self.resources.cloud_localds_available,
                "dev_net_tun": self.resources.dev_net_tun_accessible,
            },
            "system": {
                "cgroup_version": self.limits.cgroup_version,
                "ksm_disabled": self.limits.ksm_disabled,
                "dev_kvm_status": self.resources.dev_kvm_status,
                "user_in_kvm_group": self.resources.user_in_kvm_group,
            },
            "memory": {
                "total_mib": self.hardware.memory_total_mib,
                "available_mib": self.resources.memory_available_mib,
                "swap_total_mib": self.limits.swap_total_mib,
                "swap_used_mib": self.resources.swap_used_mib,
            },
            "storage": {
                "total_bytes": self.hardware.storage_total_bytes,
                "free_bytes": self.resources.storage_free_bytes,
            },
            "kernel": {
                "version": self.hardware.kernel_version,
                "minimum_version_met": self.limits.kernel_minimum_met,
            },
            "limits": {
                "pid_max": self.limits.pid_max,
                "fd_max": self.limits.fd_max,
                "conntrack_max": self.limits.conntrack_max,
                "tap_devices_max": self.limits.tap_devices_max,
                "ip_local_port_range": list(self.limits.ip_local_port_range),
            },
            "capacity": {
                "current": {
                    "pids": self.resources.pids_current,
                    "fds": self.resources.fd_current,
                    "conntrack": self.resources.conntrack_current,
                    "tap_devices": self.resources.tap_devices_used,
                    "arp_entries": self.resources.arp_current,
                },
                "recommended_max_vms": self.resources.recommended_max_vms,
                "limiting_resource": self.resources.limiting_resource,
            },
            "setup": {
                "initialized": bool(self.state.initialized),
                "initialized_at": self.state.initialized_at,
            },
        }
