package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// --- CpuConfig ---

// CpuConfig defines CPU template configuration for Firecracker.
type CpuConfig struct {
	KvmCapabilities []string              `json:"kvm_capabilities"`
	CpuidModifiers  []CpuidLeafModifier   `json:"cpuid_modifiers,omitempty"`
	MsrModifiers    []MsrModifier         `json:"msr_modifiers,omitempty"`
	RegModifiers    []ArmRegisterModifier `json:"reg_modifiers,omitempty"`
	VcpuFeatures    []VcpuFeatures        `json:"vcpu_features,omitempty"`
}

// Scan implements sql.Scanner for reading JSON TEXT into CpuConfig.
func (c *CpuConfig) Scan(src any) error {
	if src == nil {
		return nil
	}
	var val string
	switch v := src.(type) {
	case []byte:
		val = string(v)
	case string:
		val = v
	default:
		return fmt.Errorf("model.CpuConfig: unsupported scan type %T", src)
	}
	return json.Unmarshal([]byte(val), c)
}

// Value implements driver.Valuer for writing CpuConfig as JSON TEXT.
func (c CpuConfig) Value() (driver.Value, error) {
	return json.Marshal(c)
}

// --- SnapshotExtraConfig ---

// SnapshotExtraConfig preserves the source VM's Firecracker boot configuration
// at snapshot time. Stored as JSON TEXT in the snapshots.extra_config column.
type SnapshotExtraConfig struct {
	BootArgs      string     `json:"boot_args,omitempty"`
	LSMFlags      string     `json:"lsm_flags,omitempty"`
	PCIEnabled    bool       `json:"pci_enabled,omitempty"`
	NestedVirt    bool       `json:"nested_virt,omitempty"`
	Console       bool       `json:"console,omitempty"`
	EnableLogging bool       `json:"enable_logging,omitempty"`
	EnableMetrics bool       `json:"enable_metrics,omitempty"`
	LogLevel      string     `json:"log_level,omitempty"`
	CPUConfig     *CpuConfig `json:"cpu_config,omitempty"`
	VsockPort     int        `json:"vsock_port,omitempty"`
	VsockCID      int        `json:"vsock_cid,omitempty"`
	VsockToken    string     `json:"vsock_token,omitempty"`
}

// Scan implements sql.Scanner for reading JSON TEXT into SnapshotExtraConfig.
func (c *SnapshotExtraConfig) Scan(src any) error {
	if src == nil {
		return nil
	}
	var val string
	switch v := src.(type) {
	case []byte:
		val = string(v)
	case string:
		val = v
	default:
		return fmt.Errorf("model.SnapshotExtraConfig: unsupported scan type %T", src)
	}
	return json.Unmarshal([]byte(val), c)
}

// Value implements driver.Valuer for writing SnapshotExtraConfig as JSON TEXT.
func (c SnapshotExtraConfig) Value() (driver.Value, error) {
	return json.Marshal(c)
}

// --- CpuidRegisterModifier ---

type CpuidRegisterModifier struct {
	Register string `json:"register"`
	Bitmap   string `json:"bitmap"`
}

// --- CpuidLeafModifier ---

type CpuidLeafModifier struct {
	Leaf      string                  `json:"leaf"`
	Subleaf   string                  `json:"subleaf"`
	Flags     int                     `json:"flags"`
	Modifiers []CpuidRegisterModifier `json:"modifiers"`
}

// --- MsrModifier ---

type MsrModifier struct {
	Addr   string `json:"addr"`
	Bitmap string `json:"bitmap"`
}

// --- ArmRegisterModifier ---

type ArmRegisterModifier struct {
	Addr   string `json:"addr"`
	Bitmap string `json:"bitmap"`
}

// --- VcpuFeatures ---

type VcpuFeatures struct {
	Index  int    `json:"index"`
	Bitmap string `json:"bitmap"`
}

// --- DriveConfig ---

type DriveConfig struct {
	DriveID      string  `json:"drive_id"`
	PathOnHost   string  `json:"path_on_host"`
	IsRootDevice bool    `json:"is_root_device"`
	IsReadOnly   bool    `json:"is_read_only"`
	Partuuid     *string `json:"partuuid,omitempty"`
	CacheType    string  `json:"cache_type"`
	IOEngine     string  `json:"io_engine"`
	RateLimiter  any     `json:"rate_limiter,omitempty"` // RateLimiter is dynamically typed per Firecracker API spec — can be *TokenBucket, *RateLimiterConfig, or nil. Concrete type determined by Firecracker API response deserialization.
	Socket       *string `json:"socket,omitempty"`
}

// --- BootSourceConfig ---

type BootSourceConfig struct {
	BootArgs        string  `json:"boot_args"`
	KernelImagePath string  `json:"kernel_image_path"`
	InitrdPath      *string `json:"initrd_path,omitempty"`
}

// --- NetworkInterfaceConfig ---

type NetworkInterfaceConfig struct {
	IfaceID     string `json:"iface_id"`
	GuestMAC    string `json:"guest_mac"`
	HostDevName string `json:"host_dev_name"`
}

// --- MachineConfig ---

type MachineConfig struct {
	VCPUCount       int     `json:"vcpu_count"`
	MemSizeMiB      int     `json:"mem_size_mib"`
	SMT             bool    `json:"smt"`
	TrackDirtyPages bool    `json:"track_dirty_pages"`
	CPUTemplate     *string `json:"cpu_template,omitempty"`
}

// --- LoggerConfig ---

type LoggerConfig struct {
	LogPath       string `json:"log_path"`
	Level         string `json:"level"`
	ShowLevel     bool   `json:"show_level"`
	ShowLogOrigin bool   `json:"show_log_origin"`
}

// --- MetricsConfig ---

type MetricsConfig struct {
	MetricsPath string `json:"metrics_path"`
}

// --- FirecrackerVMConfig ---

// FirecrackerVMConfig is the JSON-serializable top-level Firecracker VM config.
// This struct is written to the --config-file JSON that Firecracker reads at boot.
// Optional sections use pointer fields with omitempty so they're omitted from JSON
// when nil.
type FirecrackerVMConfig struct {
	BootSource        BootSourceConfig         `json:"boot-source"`
	Drives            []DriveConfig            `json:"drives"`
	NetworkInterfaces []NetworkInterfaceConfig `json:"network-interfaces"`
	MachineConfig     MachineConfig            `json:"machine-config"`
	Logger            *LoggerConfig            `json:"logger,omitempty"`
	Metrics           *MetricsConfig           `json:"metrics,omitempty"`
	CPUConfig         *CpuConfig               `json:"cpu-config,omitempty"`
	Vsock             *VsockConfig             `json:"vsock,omitempty"`
}

// --- FirecrackerConfigDict ---

// FirecrackerConfigDict is a dynamic JSON map — Firecracker API has variable
// response shapes that can't be statically typed.
// Raw Firecracker JSON config; schema controlled by Firecracker API, not by us
type FirecrackerConfigDict map[string]any

// --- InstanceInfo ---

type InstanceInfo struct {
	ID         string  `json:"id"`
	State      string  `json:"state"`
	VCPUCount  int     `json:"vcpu_count"`
	MemSizeMiB int     `json:"mem_size_mib"`
	BootTime   *string `json:"boot_time,omitempty"`
}

// --- InstanceDescription ---

type InstanceDescription struct {
	ID               string            `json:"id"`
	State            string            `json:"state"`
	VCPUCount        int               `json:"vcpu_count"`
	MemSizeMiB       int               `json:"mem_size_mib"`
	Flags            []string          `json:"flags"`
	IfAddr           map[string]string `json:"if_addr"`
	UsedBlockDevices []string          `json:"used_block_devices"`
}

// --- FirecrackerConfig ---

// FirecrackerConfig holds all VM configuration for spawning Firecracker.
type FirecrackerConfig struct {
	// Paths
	VMDir      string `json:"vm_dir"`
	RootfsPath string `json:"rootfs_path"`

	// Binary / kernel
	BinaryPath string `json:"binary_path"`
	KernelPath string `json:"kernel_path"`

	// Machine
	VCPUCount  int `json:"vcpu_count"`
	MemSizeMiB int `json:"mem_size_mib"`

	// Network
	GuestIP        string `json:"guest_ip"`
	GuestMAC       string `json:"guest_mac"`
	TapName        string `json:"tap_name"`
	NetworkGateway string `json:"network_gateway"`
	NetworkNetmask string `json:"network_netmask"`

	// Image metadata
	ImageFSUUID string `json:"image_fs_uuid,omitempty"`
	ImageFSType string `json:"image_fs_type"`

	// Boot
	BootArgs string `json:"boot_args"`
	LSMFlags string `json:"lsm_flags"`

	// Feature flags
	PCIEnabled    bool `json:"pci_enabled"`
	NestedVirt    bool `json:"nested_virt"`
	EnableConsole bool `json:"enable_console"`
	EnableLogging bool `json:"enable_logging"`
	EnableMetrics bool `json:"enable_metrics"`

	// File/path overrides (full paths, no VMDir joining)
	LogLevel         string `json:"log_level"`
	LogPath          string `json:"log_path"`
	SerialOutputPath string `json:"serial_output_path"`
	MetricsPath      string `json:"metrics_path"`
	APISocketPath    string `json:"api_socket_path"`
	PIDPath          string `json:"pid_path"`
	ConfigPath       string `json:"config_path"`

	// Cloud-init
	CloudInitMode       *CloudInitMode `json:"cloud_init_mode,omitempty"`
	CloudInitISOPath    *string        `json:"cloud_init_iso_path,omitempty"`
	CloudInitNoCloudURL *string        `json:"cloud_init_nocloud_url,omitempty"`

	// CPU config
	CPUConfig       *CpuConfig `json:"cpu_config,omitempty"`
	CPUVendor       *string    `json:"cpu_vendor,omitempty"`
	CPUArchitecture *string    `json:"cpu_architecture,omitempty"`

	// Extra drives (volumes)
	ExtraDrives []DriveConfig `json:"extra_drives,omitempty"`

	// Spawn behavior
	RelayClientFD *int `json:"relay_client_fd,omitempty"`
	SnapshotMode  bool `json:"snapshot_mode"`

	// Vsock device config
	Vsock *VsockConfig `json:"vsock,omitempty"`
}
