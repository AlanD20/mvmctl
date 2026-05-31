// Package model consolidates ALL model types from across the Go codebase into
// a single shared package. No domain imports anything outside the model package.
package model

// ── Status (VM lifecycle) ──

// Status is the VM lifecycle status type, matching Python's VMStatus(StrEnum).
type Status string

const (
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusPaused   Status = "paused"
	StatusStopping Status = "stopping"
	StatusStopped  Status = "stopped"
	StatusCrashed  Status = "crashed"
	StatusError    Status = "error"
)

// ── VM ──

// VM matches Python's VMInstanceItem dataclass exactly.
type VM struct {
	ID            string `json:"id" db:"id"`
	Name          string `json:"name" db:"name"`
	Status        Status `json:"status" db:"status"`
	PID           int    `json:"pid" db:"pid"`
	IPv4          string `json:"ipv4" db:"ipv4"`
	MAC           string `json:"mac" db:"mac"`
	NetworkID     string `json:"network_id" db:"network_id"`
	TapDevice     string `json:"tap_device" db:"tap_device"`
	ImageID       string `json:"image_id" db:"image_id"`
	KernelID      string `json:"kernel_id" db:"kernel_id"`
	BinaryID      string `json:"binary_id" db:"binary_id"`
	APISocketPath string `json:"api_socket_path" db:"api_socket_path"`
	ConfigPath    string `json:"config_path" db:"config_path"`
	CloudInitMode string `json:"cloud_init_mode" db:"cloud_init_mode"`
	VCPUCount     int    `json:"vcpu_count" db:"vcpu_count"`
	MemSizeMiB    int    `json:"mem_size_mib" db:"mem_size_mib"`
	DiskSizeMiB   int    `json:"disk_size_mib" db:"disk_size_mib"`
	RootfsPath    string `json:"rootfs_path" db:"rootfs_path"`
	RootfsSuffix  string `json:"rootfs_suffix" db:"rootfs_suffix"`
	PCIEnabled    bool   `json:"pci_enabled" db:"pci_enabled"`
	NestedVirt    bool   `json:"nested_virt" db:"nested_virt"`
	EnableLogging bool   `json:"enable_logging" db:"enable_logging"`
	EnableMetrics bool   `json:"enable_metrics" db:"enable_metrics"`
	EnableConsole bool   `json:"enable_console" db:"enable_console"`
	CreatedAt     string `json:"created_at" db:"created_at"`
	UpdatedAt     string `json:"updated_at" db:"updated_at"`

	// Optional fields
	RelaySocketPath  *string `json:"relay_socket_path,omitempty" db:"relay_socket_path"`
	ProcessStartTime *int64  `json:"process_start_time,omitempty" db:"process_start_time"`
	NocloudNetPort   *int    `json:"nocloud_net_port,omitempty" db:"nocloud_net_port"`
	NocloudNetPID    *int    `json:"nocloud_net_pid,omitempty" db:"nocloud_net_pid"`
	RelayPID         *int    `json:"relay_pid,omitempty" db:"relay_pid"`
	ExitCode         *int    `json:"exit_code,omitempty" db:"exit_code"`
	LogPath          *string `json:"log_path,omitempty" db:"log_path"`
	SerialOutputPath *string `json:"serial_output_path,omitempty" db:"serial_output_path"`
	LSMFlags         *string `json:"lsm_flags,omitempty" db:"lsm_flags"`
	BootArgs         *string `json:"boot_args,omitempty" db:"boot_args"`

	// JSON-serialized in DB fields
	SSHKeys   []string   `json:"ssh_keys"`
	SSHUser   *string    `json:"ssh_user,omitempty" db:"ssh_user"`
	VolumeIDs []string   `json:"volume_ids,omitempty"`
	CPUConfig *CpuConfig `json:"cpu_config,omitempty"`

	// Resolved relations (typed as concrete model types from this package)
	Kernel  *KernelItem   `json:"kernel,omitempty"`
	Image   *ImageItem    `json:"image,omitempty"`
	Binary  *BinaryItem   `json:"binary,omitempty"`
	Network *Network      `json:"network,omitempty"`
	Volumes []*VolumeItem `json:"volumes,omitempty"`
}

// ── ConsoleInfo ──

// ConsoleInfo matches Python's ConsoleInfo dataclass.
type ConsoleInfo struct {
	SocketPath string `json:"socket_path"`
	VMName     string `json:"vm_name"`
}

// ── ConsoleState ──

// ConsoleState matches Python's ConsoleState dataclass.
type ConsoleState struct {
	Running    bool    `json:"running"`
	PID        *int    `json:"pid,omitempty"`
	SocketPath *string `json:"socket_path,omitempty"`
}

// ── VMInspectInfo ──

// VMInspectInfo matches Python's VMInspectInfo dataclass.
type VMInspectInfo struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Status        string             `json:"status"`
	CreatedAt     *string            `json:"created_at,omitempty"`
	PID           *int               `json:"pid,omitempty"`
	IP            *string            `json:"ip,omitempty"`
	MAC           *string            `json:"mac,omitempty"`
	NetworkName   *string            `json:"network_name,omitempty"`
	TapDevice     *string            `json:"tap_device,omitempty"`
	CloudInitMode string             `json:"cloud_init_mode"`
	ImageID       *string            `json:"image_id,omitempty"`
	ImageName     *string            `json:"image_name,omitempty"`
	KernelID      *string            `json:"kernel_id,omitempty"`
	KernelName    *string            `json:"kernel_name,omitempty"`
	Paths         map[string]*string `json:"paths"`
	Features      map[string]bool    `json:"features"`
	NocloudNet    map[string]any     `json:"nocloud_net,omitempty"` // Flexible: shape varies by cloud-init provider (NoCloud, OpenStack, etc.)
	Console       map[string]any     `json:"console,omitempty"`     // Flexible: console state fields differ by relay implementation
}
