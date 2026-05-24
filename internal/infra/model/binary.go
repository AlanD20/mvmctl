package model

// ── BinaryItem ──

// BinaryItem corresponds to Python's BinaryItem dataclass.
type BinaryItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Version     string  `json:"version"`
	FullVersion string  `json:"full_version"`
	CIVersion   *string `json:"ci_version,omitempty"`
	Path        string  `json:"path"`
	IsDefault   bool    `json:"is_default"`
	IsPresent   bool    `json:"is_present"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	DeletedAt   *string `json:"deleted_at,omitempty"`

	// Resolved relations
	VMs []*VM `json:"vms,omitempty"`
}
