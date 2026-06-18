package model

// --- HostStateItem ---

// HostStateItem holds singleton host initialization state (id=1).
type HostStateItem struct {
	ID                    int    `json:"id"                      db:"id"`
	Initialized           bool   `json:"initialized"             db:"initialized"`
	MvmGroupCreated       bool   `json:"mvm_group_created"       db:"mvm_group_created"`
	SudoersConfigured     bool   `json:"sudoers_configured"      db:"sudoers_configured"`
	DefaultNetworkCreated bool   `json:"default_network_created" db:"default_network_created"`
	InitializedAt         string `json:"initialized_at"          db:"initialized_at"`
	UpdatedAt             string `json:"updated_at"              db:"updated_at"`

	// Capacity detection fields
	Hostname          *string `json:"hostname,omitempty"            db:"hostname"`
	CPUModel          *string `json:"cpu_model,omitempty"           db:"cpu_model"`
	CPUVendor         *string `json:"cpu_vendor,omitempty"          db:"cpu_vendor"`
	CPUCores          *int    `json:"cpu_cores,omitempty"           db:"cpu_cores"`
	CPUArchitecture   *string `json:"cpu_architecture,omitempty"    db:"cpu_architecture"`
	NumaNodes         *int    `json:"numa_nodes,omitempty"          db:"numa_nodes"`
	MemoryTotalMiB    *int    `json:"memory_total_mib,omitempty"    db:"memory_total_mib"`
	StorageTotalBytes *int    `json:"storage_total_bytes,omitempty" db:"storage_total_bytes"`
	KernelVersion     *string `json:"kernel_version,omitempty"      db:"kernel_version"`
	OSRelease         *string `json:"os_release,omitempty"          db:"os_release"`
	PIDMax            *int    `json:"pid_max,omitempty"             db:"pid_max"`
	FDMax             *int    `json:"fd_max,omitempty"              db:"fd_max"`
	ConntrackMax      *int    `json:"conntrack_max,omitempty"       db:"conntrack_max"`
	TAPDevicesMax     *int    `json:"tap_devices_max,omitempty"     db:"tap_devices_max"`
	IPLocalPortRange  *string `json:"ip_local_port_range,omitempty" db:"ip_local_port_range"`
	DetectedAt        *string `json:"detected_at,omitempty"         db:"detected_at"`

	// Virtualization detection fields
	CPUHasVMX           *int `json:"cpu_has_vmx,omitempty"           db:"cpu_has_vmx"`
	CPUHypervisor       *int `json:"cpu_hypervisor,omitempty"        db:"cpu_hypervisor"`
	NestedVirtAvailable *int `json:"nested_virt_available,omitempty" db:"nested_virt_available"`
	EPTAvailable        *int `json:"ept_available,omitempty"         db:"ept_available"`
	HugepageCount2MB    *int `json:"hugepage_count_2mb,omitempty"    db:"hugepage_count_2mb"`
	KSMDisabled         *int `json:"ksm_disabled,omitempty"          db:"ksm_disabled"`
	CgroupVersion       *int `json:"cgroup_version,omitempty"        db:"cgroup_version"`
	SwapTotalMiB        *int `json:"swap_total_mib,omitempty"        db:"swap_total_mib"`
	KernelMinimumMet    *int `json:"kernel_minimum_met,omitempty"    db:"kernel_minimum_met"`
}

// --- HostStateChangeItem ---

// HostStateChangeItem tracks a setting change during host initialization.
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

// --- HostHardware ---

// HostHardware holds detected host hardware information.
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

// --- HostLimits ---

// HostLimits holds detected host kernel limits.
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

// --- HostResources ---

// HostResources holds detected host resource usage.
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

// --- ProbeCheck ---

// ProbeCheck represents the result of a single host probe check.
type ProbeCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// --- ProbeResult ---

// ProbeResult holds the aggregated results of host probe checks.
type ProbeResult struct {
	Critical []ProbeCheck `json:"critical"`
	Warnings []ProbeCheck `json:"warnings"`
	Info     []ProbeCheck `json:"info"`
}

// --- HostInfo ---

// HostInfo holds aggregated host detection results.
type HostInfo struct {
	State     HostStateItem `json:"state"`
	Resources HostResources `json:"resources"`
	Limits    HostLimits    `json:"limits"`
	Hardware  HostHardware  `json:"hardware"`
}
