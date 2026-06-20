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
