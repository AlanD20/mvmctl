package model

// ── VolumeStatus ──

// VolumeStatus represents volume lifecycle states.
type VolumeStatus string

const (
	VolumeStatusAvailable VolumeStatus = "available"
	VolumeStatusAttached  VolumeStatus = "attached"
)

// ── VolumeItem ──

// VolumeItem represents a persistent data disk attachable to VMs.
type VolumeItem struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	SizeBytes  int64        `json:"size_bytes"`
	Format     string       `json:"format"`
	Path       string       `json:"path"`
	Status     VolumeStatus `json:"status"`
	VMID       *string      `json:"vm_id,omitempty"`
	CreatedAt  string       `json:"created_at"`
	UpdatedAt  string       `json:"updated_at"`
	IsReadOnly bool         `json:"is_read_only"`

	// Resolved relations
	VMs []*VM `json:"vms,omitempty"`
}
