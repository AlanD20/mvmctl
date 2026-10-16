package model

import "mvmctl/internal/lib/db"

// SnapshotItem represents a managed Firecracker snapshot.
type SnapshotItem struct {
	ID           string               `json:"id"                     db:"id"`
	Name         string               `json:"name"                   db:"name"`
	SourceVMID   string               `json:"source_vm_id"           db:"source_vm_id"`
	SourceVMName string               `json:"source_vm_name"         db:"source_vm_name"`
	SnapshotDir  string               `json:"snapshot_dir"           db:"snapshot_dir"`
	MemoryFile   string               `json:"memory_file"            db:"memory_file"`
	StateFile    string               `json:"state_file"             db:"state_file"`
	RootfsFile   string               `json:"rootfs_file"            db:"rootfs_file"`
	KernelID     string               `json:"kernel_id"              db:"kernel_id"`
	NetworkID    string               `json:"network_id"             db:"network_id"`
	BinaryID     string               `json:"binary_id"              db:"binary_id"`
	VCPUCount    int                  `json:"vcpu_count"             db:"vcpu_count"`
	MemSizeMiB   int                  `json:"mem_size_mib"           db:"mem_size_mib"`
	DiskSizeMiB  int                  `json:"disk_size_mib"          db:"disk_size_mib"`
	ImageID      string               `json:"image_id"               db:"image_id"`
	SSHKeys      db.StringSlice       `json:"ssh_keys"               db:"ssh_keys"`
	SSHUser      *string              `json:"ssh_user,omitempty"     db:"ssh_user"`
	ExtraConfig  *SnapshotExtraConfig `json:"extra_config,omitempty" db:"extra_config"`
	CreatedAt    string               `json:"created_at"             db:"created_at"`
	UpdatedAt    string               `json:"updated_at"             db:"updated_at"`

	// Enriched relations (populated by enricher, not persisted)
	Image   *ImageItem   `json:"image,omitempty"`
	Kernel  *KernelItem  `json:"kernel,omitempty"`
	Network *NetworkItem `json:"network,omitempty"`
	Binary  *BinaryItem  `json:"binary,omitempty"`
}

// SnapshotCreateConfig collects all external parameters for creating a snapshot.
// Passed from the API layer to the controller.
type SnapshotCreateConfig struct {
	CacheRoot  string
	SnapshotID string
	PauseOnly  bool
}

// SnapshotRestoreConfig collects all external parameters for restoring a snapshot.
// Passed from the API layer to the controller.
type SnapshotRestoreConfig struct {
	CacheRoot  string
	SnapshotID string
	Resume     bool
}

// SnapshotCaptureResult is a display projection of the root-authenticated capture.
// It is not a capability to reopen artifacts or choose restore configuration.
type SnapshotCaptureResult struct {
	SnapshotID          string             `json:"snapshot_id"`
	SourceVMID          string             `json:"source_vm_id"`
	Mode                string             `json:"mode"`
	Paused              bool               `json:"paused"`
	VCPUCount           int                `json:"vcpu_count"`
	MemSizeMiB          int                `json:"mem_size_mib"`
	RootfsBytes         uint64             `json:"rootfs_bytes"`
	NetworkPlacement    VMNetworkPlacement `json:"network_placement"`
	NetworkID           string             `json:"network_id"`
	GuestIPv4           string             `json:"guest_ipv4"`
	GuestMAC            string             `json:"guest_mac"`
	ReleaseVersion      string             `json:"release_version"`
	ReleaseArchitecture string             `json:"release_architecture"`
	FirecrackerSHA256   string             `json:"firecracker_sha256"`
	JailerSHA256        string             `json:"jailer_sha256"`
	KernelID            string             `json:"kernel_id"`
	PCIEnabled          bool               `json:"pci_enabled"`
	NestedVirt          bool               `json:"nested_virt"`
	CloudInit           bool               `json:"cloud_init"`
	BootArgs            string             `json:"boot_args"`
	CgroupLimits        VMCgroupLimits     `json:"cgroup_limits"`
	CPUConfig           *CpuConfig         `json:"cpu_config,omitempty"`
	VsockCID            *uint32            `json:"vsock_cid,omitempty"`
	Logging             bool               `json:"logging"`
	Metrics             bool               `json:"metrics"`
	LogLevel            string             `json:"log_level"`
}

// SnapshotRestoredVolume projects a new independent disk while retaining its guest drive identity.
type SnapshotRestoredVolume struct {
	ID           string `json:"id"`
	GuestDriveID string `json:"guest_drive_id"`
	Bytes        uint64 `json:"bytes"`
	ReadOnly     bool   `json:"read_only"`
	CacheType    string `json:"cache_type"`
}

// SnapshotRestoreResult projects a completed root-owned restore transaction.
type SnapshotRestoreResult struct {
	VMID             string                   `json:"vm_id"`
	PID              int                      `json:"pid"`
	StartTimeTicks   uint64                   `json:"start_time_ticks"`
	LaunchGeneration uint64                   `json:"launch_generation"`
	Configuration    SnapshotCaptureResult    `json:"configuration"`
	Volumes          []SnapshotRestoredVolume `json:"volumes"`
}
