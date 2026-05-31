package model

// ── BinaryItem ──

// BinaryItem corresponds to Python's BinaryItem dataclass.
type BinaryItem struct {
	ID          string  `json:"id" db:"id"`
	Name        string  `json:"name" db:"name"`
	Version     string  `json:"version" db:"version"`
	FullVersion string  `json:"full_version" db:"full_version"`
	CIVersion   *string `json:"ci_version,omitempty" db:"ci_version"`
	Path        string  `json:"path" db:"path"`
	IsDefault   bool    `json:"is_default" db:"is_default"`
	IsPresent   bool    `json:"is_present" db:"is_present"`
	CreatedAt   string  `json:"created_at" db:"created_at"`
	UpdatedAt   string  `json:"updated_at" db:"updated_at"`
	DeletedAt   *string `json:"deleted_at,omitempty" db:"deleted_at"`

	// Resolved relations
	VMs []*VM `json:"vms,omitempty"`
}
