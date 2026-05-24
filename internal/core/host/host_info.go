package host

import (
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

// HardwareFromState reconstructs HostHardware from stored state, or returns nil if not yet detected.
// Matches Python's HostHardware.from_state() exactly.
// Python: numa_nodes=state.numa_nodes or 1 → when state.numa_nodes is None or 0, returns 1.
func HardwareFromState(state *model.HostStateItem) *model.HostHardware {
	if state.CPUModel == nil {
		return nil
	}
	h := &model.HostHardware{}
	if state.Hostname != nil {
		h.Hostname = *state.Hostname
	}
	if state.CPUModel != nil {
		h.CPUModel = *state.CPUModel
	}
	if state.CPUVendor != nil {
		h.CPUVendor = *state.CPUVendor
	}
	if state.CPUCores != nil {
		h.CPUCores = *state.CPUCores
	}
	if state.CPUArchitecture != nil {
		h.CPUArchitecture = *state.CPUArchitecture
	}
	// Python: numa_nodes=state.numa_nodes or 1
	// 0 is falsy in Python, so both nil and 0 result in 1.
	if state.NumaNodes != nil && *state.NumaNodes != 0 {
		h.NumaNodes = *state.NumaNodes
	} else {
		h.NumaNodes = 1
	}
	if state.MemoryTotalMiB != nil {
		h.MemoryTotalMiB = *state.MemoryTotalMiB
	}
	if state.StorageTotalBytes != nil {
		h.StorageTotalBytes = *state.StorageTotalBytes
	}
	if state.KernelVersion != nil {
		h.KernelVersion = *state.KernelVersion
	}
	if state.OSRelease != nil {
		h.OSRelease = *state.OSRelease
	}
	if state.CPUHasVMX != nil {
		h.CPUHasVMX = *state.CPUHasVMX != 0
	}
	if state.CPUHypervisor != nil {
		h.CPUHypervisor = *state.CPUHypervisor != 0
	}
	return h
}

// LimitsFromState reconstructs HostLimits from stored state, or returns nil if not yet detected.
// Matches Python's HostLimits.from_state().
func LimitsFromState(state *model.HostStateItem) *model.HostLimits {
	if state.PIDMax == nil {
		return nil
	}
	var portRange [2]int
	if state.IPLocalPortRange != nil {
		portRange = infra.ParsePortRange(*state.IPLocalPortRange)
	} else {
		portRange = [2]int{32768, 60999}
	}
	l := &model.HostLimits{}
	if state.PIDMax != nil {
		l.PIDMax = *state.PIDMax
	}
	if state.FDMax != nil {
		l.FDMax = *state.FDMax
	}
	if state.ConntrackMax != nil {
		l.ConntrackMax = *state.ConntrackMax
	}
	if state.TAPDevicesMax != nil {
		l.TAPDevicesMax = *state.TAPDevicesMax
	}
	l.IPLocalPortRange = portRange
	if state.NestedVirtAvailable != nil {
		l.NestedVirtAvailable = *state.NestedVirtAvailable != 0
	}
	if state.EPTAvailable != nil {
		l.EPTAvailable = *state.EPTAvailable != 0
	}
	if state.HugepageCount2MB != nil {
		l.HugepageCount2MB = *state.HugepageCount2MB
	}
	if state.KSMDisabled != nil {
		l.KSMDisabled = *state.KSMDisabled != 0
	} else {
		l.KSMDisabled = true
	}
	// Python: state.cgroup_version or 1 — 0 is falsy, so both nil and 0 → 1.
	if state.CgroupVersion != nil && *state.CgroupVersion != 0 {
		l.CgroupVersion = *state.CgroupVersion
	} else {
		l.CgroupVersion = 1
	}
	if state.SwapTotalMiB != nil {
		l.SwapTotalMiB = *state.SwapTotalMiB
	}
	if state.KernelMinimumMet != nil {
		l.KernelMinimumMet = *state.KernelMinimumMet != 0
	}
	return l
}

// NewHostResources creates a HostResources with all maps initialized,
// matching Python's field(default_factory=dict) behavior.
func NewHostResources() *model.HostResources {
	return &model.HostResources{
		ModulesLoaded: make(map[string]bool),
	}
}

// NewProbeResult creates a ProbeResult with all slices initialized.
func NewProbeResult() *model.ProbeResult {
	return &model.ProbeResult{}
}

// HasCritical returns True if there are any failed critical checks.
// Matches Python's ProbeResult.has_critical property.
func HasCritical(pr *model.ProbeResult) bool {
	return len(pr.Critical) > 0
}

// EnsureModulesLoaded ensures ModulesLoaded is never nil (safe to call on any instance).
// Matches Python's default_factory=dict guarantee.
func EnsureModulesLoaded(r *model.HostResources) {
	if r.ModulesLoaded == nil {
		r.ModulesLoaded = make(map[string]bool)
	}
}

// HostInfoToDict builds the standardised info response dict from host info.
// Matches Python's HostInfo.to_dict().
func HostInfoToDict(hi *model.HostInfo) map[string]interface{} {
	detectedAt := ""
	if hi.State.DetectedAt != nil {
		detectedAt = *hi.State.DetectedAt
	}

	modulesLoaded := make(map[string]bool)
	for k, v := range hi.Resources.ModulesLoaded {
		modulesLoaded[k] = v
	}

	return map[string]interface{}{
		"detected_at": detectedAt,
		"hostname":    hi.Hardware.Hostname,
		"os": map[string]interface{}{
			"kernel":  hi.Hardware.KernelVersion,
			"release": hi.Hardware.OSRelease,
		},
		"cpu": map[string]interface{}{
			"model":        hi.Hardware.CPUModel,
			"vendor":       hi.Hardware.CPUVendor,
			"cores":        hi.Hardware.CPUCores,
			"architecture": hi.Hardware.CPUArchitecture,
			"numa_nodes":   hi.Hardware.NumaNodes,
		},
		"virtualization": map[string]interface{}{
			"cpu_has_vmx":           hi.Hardware.CPUHasVMX,
			"nested_virt_available": hi.Limits.NestedVirtAvailable,
			"ept_available":         hi.Limits.EPTAvailable,
			"hypervisor":            hi.Hardware.CPUHypervisor,
			"smt_active":            hi.Resources.SMTActive,
			"modules":               modulesLoaded,
		},
		"hugepages": map[string]interface{}{
			"count_2mb": hi.Limits.HugepageCount2MB,
			"free_2mb":  hi.Resources.HugepagesFree2MB,
		},
		"dependencies": map[string]interface{}{
			"nftables_available":      hi.Resources.NftablesAvailable,
			"iptables_available":      hi.Resources.IptablesAvailable,
			"cloud_localds_available": hi.Resources.CloudLocaldsAvailable,
			"dev_net_tun":             hi.Resources.DevNetTUNAccessible,
		},
		"system": map[string]interface{}{
			"cgroup_version":    hi.Limits.CgroupVersion,
			"ksm_disabled":      hi.Limits.KSMDisabled,
			"dev_kvm_status":    hi.Resources.DevKVMStatus,
			"user_in_kvm_group": hi.Resources.UserInKVMGroup,
		},
		"memory": map[string]interface{}{
			"total_mib":      hi.Hardware.MemoryTotalMiB,
			"available_mib":  hi.Resources.MemoryAvailableMiB,
			"swap_total_mib": hi.Limits.SwapTotalMiB,
			"swap_used_mib":  hi.Resources.SwapUsedMiB,
		},
		"storage": map[string]interface{}{
			"total_bytes": hi.Hardware.StorageTotalBytes,
			"free_bytes":  hi.Resources.StorageFreeBytes,
		},
		"kernel": map[string]interface{}{
			"version":             hi.Hardware.KernelVersion,
			"minimum_version_met": hi.Limits.KernelMinimumMet,
		},
		"limits": map[string]interface{}{
			"pid_max":          hi.Limits.PIDMax,
			"fd_max":           hi.Limits.FDMax,
			"conntrack_max":    hi.Limits.ConntrackMax,
			"tap_devices_max":  hi.Limits.TAPDevicesMax,
			"ip_local_port_range": []int{hi.Limits.IPLocalPortRange[0], hi.Limits.IPLocalPortRange[1]},
		},
		"capacity": map[string]interface{}{
			"current": map[string]interface{}{
				"pids":        hi.Resources.PIDsCurrent,
				"fds":         hi.Resources.FDCurrent,
				"conntrack":   hi.Resources.ConntrackCurrent,
				"tap_devices": hi.Resources.TAPDevicesUsed,
				"arp_entries": hi.Resources.ARPCurrent,
			},
			"recommended_max_vms": hi.Resources.RecommendedMaxVMs,
			"limiting_resource":   hi.Resources.LimitingResource,
		},
		"setup": map[string]interface{}{
			"initialized":    hi.State.Initialized,
			"initialized_at": hi.State.InitializedAt,
		},
	}
}
