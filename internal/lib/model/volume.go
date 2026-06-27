package model

// --- VolumeStatus ---

// VolumeStatus represents volume lifecycle states.
type VolumeStatus string

const (
	VolumeStatusAvailable VolumeStatus = "available"
	VolumeStatusAttached  VolumeStatus = "attached"
)

// --- VolumeFormat ---

// VolumeFormat represents disk image format types.
type VolumeFormat string

const (
	VolumeFormatRaw   VolumeFormat = "raw"
	VolumeFormatQCOW2 VolumeFormat = "qcow2"
)

// --- VolumeItem ---

// VolumeItem represents a persistent data disk attachable to VMs.
type VolumeItem struct {
	ID          string       `json:"id"              db:"id"`
	Name        string       `json:"name"            db:"name"`
	SizeBytes   int64        `json:"size_bytes"      db:"size_bytes"`
	Format      VolumeFormat `json:"format"          db:"format"`
	Path        string       `json:"path"            db:"path"`
	Status      VolumeStatus `json:"status"          db:"status"`
	VMID        *string      `json:"vm_id,omitempty" db:"vm_id"`
	CreatedAt   string       `json:"created_at"      db:"created_at"`
	UpdatedAt   string       `json:"updated_at"      db:"updated_at"`
	IsReadOnly  bool         `json:"is_read_only"    db:"is_read_only"`
	IsShareable bool         `json:"is_shareable"    db:"is_shareable"`
	CacheType   string       `json:"cache_type"      db:"cache_type"`

	// Resolved relations
	VMs []*VMItem `json:"vms,omitempty"`
}
