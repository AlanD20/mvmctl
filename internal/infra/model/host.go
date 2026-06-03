package model

// ── HostStateItem ──

// HostStateItem matches Python's HostStateItem — singleton row (id=1).
type HostStateItem struct {
	ID                    int    `json:"id"                      db:"id"`
	Initialized           bool   `json:"initialized"             db:"initialized"`
	MvmGroupCreated       bool   `json:"mvm_group_created"       db:"mvm_group_created"`
	SudoersConfigured     bool   `json:"sudoers_configured"      db:"sudoers_configured"`
	DefaultNetworkCreated bool   `json:"default_network_created" db:"default_network_created"`
	InitializedAt         string `json:"initialized_at"          db:"initialized_at"`
	UpdatedAt             string `json:"updated_at"              db:"updated_at"`

	// Capacity detection fields
	Hostname          *string `json:"hostname,omitempty"`
	CPUModel          *string `json:"cpu_model,omitempty"`
	CPUVendor         *string `json:"cpu_vendor,omitempty"`
	CPUCores          *int    `json:"cpu_cores,omitempty"`
	CPUArchitecture   *string `json:"cpu_architecture,omitempty"`
	NumaNodes         *int    `json:"numa_nodes,omitempty"`
	MemoryTotalMiB    *int    `json:"memory_total_mib,omitempty"`
	StorageTotalBytes *int    `json:"storage_total_bytes,omitempty"`
	KernelVersion     *string `json:"kernel_version,omitempty"`
	OSRelease         *string `json:"os_release,omitempty"`
	PIDMax            *int    `json:"pid_max,omitempty"`
	FDMax             *int    `json:"fd_max,omitempty"`
	ConntrackMax      *int    `json:"conntrack_max,omitempty"`
	TAPDevicesMax     *int    `json:"tap_devices_max,omitempty"`
	IPLocalPortRange  *string `json:"ip_local_port_range,omitempty"`
	DetectedAt        *string `json:"detected_at,omitempty"`

	// Virtualization detection fields
	CPUHasVMX           *int `json:"cpu_has_vmx,omitempty"`
	CPUHypervisor       *int `json:"cpu_hypervisor,omitempty"`
	NestedVirtAvailable *int `json:"nested_virt_available,omitempty"`
	EPTAvailable        *int `json:"ept_available,omitempty"`
	HugepageCount2MB    *int `json:"hugepage_count_2mb,omitempty"`
	KSMDisabled         *int `json:"ksm_disabled,omitempty"`
	CgroupVersion       *int `json:"cgroup_version,omitempty"`
	SwapTotalMiB        *int `json:"swap_total_mib,omitempty"`
	KernelMinimumMet    *int `json:"kernel_minimum_met,omitempty"`
}

// ── HostStateChangeItem ──

// HostStateChangeItem matches Python's HostStateChangeItem.
type HostStateChangeItem struct {
	SessionID       string  `json:"session_id"`
	InitTimestamp   string  `json:"init_timestamp"`
	Setting         string  `json:"setting"`
	Mechanism       string  `json:"mechanism"`
	AppliedValue    string  `json:"applied_value"`
	Reverted        bool    `json:"reverted"`
	ChangeOrder     int     `json:"change_order"`
	CreatedAt       string  `json:"created_at"`
	ID              *int    `json:"id,omitempty"`
	OriginalValue   *string `json:"original_value,omitempty"`
	RevertedAt      *string `json:"reverted_at,omitempty"`
	RevertMechanism *string `json:"revert_mechanism,omitempty"`
}

// ── HostHardware ──

// HostHardware matches Python's HostHardware.
type HostHardware struct {
	Hostname          string `json:"hostname"`
	CPUModel          string `json:"cpu_model"`
	CPUVendor         string `json:"cpu_vendor"`
	CPUCores          int    `json:"cpu_cores"`
	CPUArchitecture   string `json:"cpu_architecture"`
	NumaNodes         int    `json:"numa_nodes"`
	MemoryTotalMiB    int    `json:"memory_total_mib"`
	StorageTotalBytes int    `json:"storage_total_bytes"`
	KernelVersion     string `json:"kernel_version"`
	OSRelease         string `json:"os_release"`
	CPUHasVMX         bool   `json:"cpu_has_vmx"`
	CPUHypervisor     bool   `json:"cpu_hypervisor"`
}

// ── HostLimits ──

// HostLimits matches Python's HostLimits.
type HostLimits struct {
	PIDMax              int    `json:"pid_max"`
	FDMax               int    `json:"fd_max"`
	ConntrackMax        int    `json:"conntrack_max"`
	TAPDevicesMax       int    `json:"tap_devices_max"`
	IPLocalPortRange    [2]int `json:"ip_local_port_range"`
	NestedVirtAvailable bool   `json:"nested_virt_available"`
	EPTAvailable        bool   `json:"ept_available"`
	HugepageCount2MB    int    `json:"hugepage_count_2mb"`
	KSMDisabled         bool   `json:"ksm_disabled"`
	CgroupVersion       int    `json:"cgroup_version"`
	SwapTotalMiB        int    `json:"swap_total_mib"`
	KernelMinimumMet    bool   `json:"kernel_minimum_met"`
}

// ── HostResources ──

// HostResources matches Python's HostResources.
type HostResources struct {
	MemoryAvailableMiB    int             `json:"memory_available_mib"`
	TAPDevicesUsed        int             `json:"tap_devices_used"`
	PIDsCurrent           int             `json:"pids_current"`
	FDCurrent             int             `json:"fd_current"`
	ConntrackCurrent      int             `json:"conntrack_current"`
	ARPCurrent            int             `json:"arp_current"`
	StorageFreeBytes      int             `json:"storage_free_bytes"`
	RecommendedMaxVMs     int             `json:"recommended_max_vms"`
	LimitingResource      *string         `json:"limiting_resource,omitempty"`
	ModulesLoaded         map[string]bool `json:"modules_loaded"`
	SwapUsedMiB           int             `json:"swap_used_mib"`
	HugepagesFree2MB      int             `json:"hugepages_free_2mb"`
	SMTActive             bool            `json:"smt_active"`
	NftablesAvailable     bool            `json:"nftables_available"`
	IptablesAvailable     bool            `json:"iptables_available"`
	CloudLocaldsAvailable bool            `json:"cloud_localds_available"`
	DevKVMStatus          string          `json:"dev_kvm_status"`
	UserInKVMGroup        bool            `json:"user_in_kvm_group"`
	DevNetTUNAccessible   bool            `json:"dev_net_tun_accessible"`
}

// ── ProbeCheck ──

// ProbeCheck matches Python's ProbeCheck.
type ProbeCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// ── ProbeResult ──

// ProbeResult matches Python's ProbeResult.
type ProbeResult struct {
	Critical []ProbeCheck `json:"critical"`
	Warnings []ProbeCheck `json:"warnings"`
	Info     []ProbeCheck `json:"info"`
}

// ── HostInfo ──

// HostInfo matches Python's HostInfo — aggregated host detection results.
type HostInfo struct {
	State     HostStateItem `json:"state"`
	Resources HostResources `json:"resources"`
	Limits    HostLimits    `json:"limits"`
	Hardware  HostHardware  `json:"hardware"`
}
