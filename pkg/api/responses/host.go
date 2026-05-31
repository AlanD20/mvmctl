package responses

import (
	"maps"

	"mvmctl/internal/infra/model"
)

// HostOSInfo groups OS-level host information.
type HostOSInfo struct {
	Kernel  string `json:"kernel"`
	Release string `json:"release"`
}

// HostCPUInfo groups CPU-level host information.
type HostCPUInfo struct {
	Model        string `json:"model"`
	Vendor       string `json:"vendor"`
	Cores        int    `json:"cores"`
	Architecture string `json:"architecture"`
	NumaNodes    int    `json:"numa_nodes"`
}

// HostVirtInfo groups virtualization-related host information.
type HostVirtInfo struct {
	CPUHasVMX           bool            `json:"cpu_has_vmx"`
	NestedVirtAvailable bool            `json:"nested_virt_available"`
	EPTAvailable        bool            `json:"ept_available"`
	Hypervisor          bool            `json:"hypervisor"`
	SMTActive           bool            `json:"smt_active"`
	Modules             map[string]bool `json:"modules"`
}

// HostHugepagesInfo groups hugepages-related host information.
type HostHugepagesInfo struct {
	Count2MB int `json:"count_2mb"`
	Free2MB  int `json:"free_2mb"`
}

// HostDepsInfo groups dependency availability information.
type HostDepsInfo struct {
	NftablesAvailable     bool `json:"nftables_available"`
	IptablesAvailable     bool `json:"iptables_available"`
	CloudLocaldsAvailable bool `json:"cloud_localds_available"`
	DevNetTUNAccessible   bool `json:"dev_net_tun"`
}

// HostSystemInfo groups system-level host information.
type HostSystemInfo struct {
	CgroupVersion  int    `json:"cgroup_version"`
	KSMDisabled    bool   `json:"ksm_disabled"`
	DevKVMStatus   string `json:"dev_kvm_status"`
	UserInKVMGroup bool   `json:"user_in_kvm_group"`
}

// HostMemoryInfo groups memory-related host information.
type HostMemoryInfo struct {
	TotalMiB     int `json:"total_mib"`
	AvailableMiB int `json:"available_mib"`
	SwapTotalMiB int `json:"swap_total_mib"`
	SwapUsedMiB  int `json:"swap_used_mib"`
}

// HostStorageInfo groups storage-related host information.
type HostStorageInfo struct {
	TotalBytes int `json:"total_bytes"`
	FreeBytes  int `json:"free_bytes"`
}

// HostKernelInfo groups kernel-related host information.
type HostKernelInfo struct {
	Version           string `json:"version"`
	MinimumVersionMet bool   `json:"minimum_version_met"`
}

// HostLimitsInfo groups kernel limit information.
type HostLimitsInfo struct {
	PIDMax           int   `json:"pid_max"`
	FDMax            int   `json:"fd_max"`
	ConntrackMax     int   `json:"conntrack_max"`
	TAPDevicesMax    int   `json:"tap_devices_max"`
	IPLocalPortRange []int `json:"ip_local_port_range"`
}

// HostCapacityCurrentInfo groups current resource usage for capacity projection.
type HostCapacityCurrentInfo struct {
	PIDs       int `json:"pids"`
	FDs        int `json:"fds"`
	Conntrack  int `json:"conntrack"`
	TAPDevices int `json:"tap_devices"`
	ARPEntries int `json:"arp_entries"`
}

// HostCapacityInfo groups capacity projection information.
type HostCapacityInfo struct {
	Current           HostCapacityCurrentInfo `json:"current"`
	RecommendedMaxVMs int                     `json:"recommended_max_vms"`
	LimitingResource  *string                 `json:"limiting_resource"`
}

// HostSetupInfo groups host setup state information.
type HostSetupInfo struct {
	Initialized   bool   `json:"initialized"`
	InitializedAt string `json:"initialized_at"`
}

// HostStatusCheck is the structured response for host status queries.
type HostStatusCheck struct {
	KVMOK            bool                `json:"kvm_accessible"`
	MissingBinaries  []string            `json:"missing_binaries"`
	IPForward        string              `json:"ip_forward"`
	IPForwardOK      bool                `json:"ip_forward_ok"`
	State            *HostSetupInfo      `json:"state"`
	Resources        *model.HostResources `json:"resources,omitempty"`
}

// HostInfo is the structured response for host info/capacity queries.
// Matches Python's HostInfo.to_dict() output.
type HostInfo struct {
	DetectedAt     string            `json:"detected_at"`
	Hostname       string            `json:"hostname"`
	OS             HostOSInfo        `json:"os"`
	CPU            HostCPUInfo       `json:"cpu"`
	Virtualization HostVirtInfo      `json:"virtualization"`
	Hugepages      HostHugepagesInfo `json:"hugepages"`
	Dependencies   HostDepsInfo      `json:"dependencies"`
	System         HostSystemInfo    `json:"system"`
	Memory         HostMemoryInfo    `json:"memory"`
	Storage        HostStorageInfo   `json:"storage"`
	Kernel         HostKernelInfo    `json:"kernel"`
	Limits         HostLimitsInfo    `json:"limits"`
	Capacity       HostCapacityInfo  `json:"capacity"`
	Setup          HostSetupInfo     `json:"setup"`
}

// BuildHostInfo builds a HostInfoResponse from model data.
func BuildHostInfo(hi *model.HostInfo) *HostInfo {
	detectedAt := ""
	if hi.State.DetectedAt != nil {
		detectedAt = *hi.State.DetectedAt
	}

	modules := maps.Clone(hi.Resources.ModulesLoaded)

	return &HostInfo{
		DetectedAt: detectedAt,
		Hostname:   hi.Hardware.Hostname,
		OS: HostOSInfo{
			Kernel:  hi.Hardware.KernelVersion,
			Release: hi.Hardware.OSRelease,
		},
		CPU: HostCPUInfo{
			Model:        hi.Hardware.CPUModel,
			Vendor:       hi.Hardware.CPUVendor,
			Cores:        hi.Hardware.CPUCores,
			Architecture: hi.Hardware.CPUArchitecture,
			NumaNodes:    hi.Hardware.NumaNodes,
		},
		Virtualization: HostVirtInfo{
			CPUHasVMX:           hi.Hardware.CPUHasVMX,
			NestedVirtAvailable: hi.Limits.NestedVirtAvailable,
			EPTAvailable:        hi.Limits.EPTAvailable,
			Hypervisor:          hi.Hardware.CPUHypervisor,
			SMTActive:           hi.Resources.SMTActive,
			Modules:             modules,
		},
		Hugepages: HostHugepagesInfo{
			Count2MB: hi.Limits.HugepageCount2MB,
			Free2MB:  hi.Resources.HugepagesFree2MB,
		},
		Dependencies: HostDepsInfo{
			NftablesAvailable:     hi.Resources.NftablesAvailable,
			IptablesAvailable:     hi.Resources.IptablesAvailable,
			CloudLocaldsAvailable: hi.Resources.CloudLocaldsAvailable,
			DevNetTUNAccessible:   hi.Resources.DevNetTUNAccessible,
		},
		System: HostSystemInfo{
			CgroupVersion:  hi.Limits.CgroupVersion,
			KSMDisabled:    hi.Limits.KSMDisabled,
			DevKVMStatus:   hi.Resources.DevKVMStatus,
			UserInKVMGroup: hi.Resources.UserInKVMGroup,
		},
		Memory: HostMemoryInfo{
			TotalMiB:     hi.Hardware.MemoryTotalMiB,
			AvailableMiB: hi.Resources.MemoryAvailableMiB,
			SwapTotalMiB: hi.Limits.SwapTotalMiB,
			SwapUsedMiB:  hi.Resources.SwapUsedMiB,
		},
		Storage: HostStorageInfo{
			TotalBytes: hi.Hardware.StorageTotalBytes,
			FreeBytes:  hi.Resources.StorageFreeBytes,
		},
		Kernel: HostKernelInfo{
			Version:           hi.Hardware.KernelVersion,
			MinimumVersionMet: hi.Limits.KernelMinimumMet,
		},
		Limits: HostLimitsInfo{
			PIDMax:           hi.Limits.PIDMax,
			FDMax:            hi.Limits.FDMax,
			ConntrackMax:     hi.Limits.ConntrackMax,
			TAPDevicesMax:    hi.Limits.TAPDevicesMax,
			IPLocalPortRange: []int{hi.Limits.IPLocalPortRange[0], hi.Limits.IPLocalPortRange[1]},
		},
		Capacity: HostCapacityInfo{
			Current: HostCapacityCurrentInfo{
				PIDs:       hi.Resources.PIDsCurrent,
				FDs:        hi.Resources.FDCurrent,
				Conntrack:  hi.Resources.ConntrackCurrent,
				TAPDevices: hi.Resources.TAPDevicesUsed,
				ARPEntries: hi.Resources.ARPCurrent,
			},
			RecommendedMaxVMs: hi.Resources.RecommendedMaxVMs,
			LimitingResource:  hi.Resources.LimitingResource,
		},
		Setup: HostSetupInfo{
			Initialized:   hi.State.Initialized,
			InitializedAt: hi.State.InitializedAt,
		},
	}
}
