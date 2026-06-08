package model

// ── ImageItem ──

// ImageItem corresponds to Python's ImageItem dataclass exactly.
type ImageItem struct {
	ID               string   `json:"id"                          db:"id"`
	Type             string   `json:"type"                        db:"type"`
	Name             string   `json:"name"                        db:"name"`
	Arch             string   `json:"arch"                        db:"arch"`
	Path             string   `json:"path"                        db:"path"`
	FSType           string   `json:"fs_type"                     db:"fs_type"`
	MinRootfsSizeMiB int      `json:"minimum_rootfs_size_mib"     db:"minimum_rootfs_size_mib"`
	OriginalSize     int64    `json:"original_size"               db:"original_size"`
	IsDefault        bool     `json:"is_default"                  db:"is_default"`
	IsPresent        bool     `json:"is_present"                  db:"is_present"`
	PulledAt         string   `json:"pulled_at"                   db:"pulled_at"`
	CreatedAt        string   `json:"created_at"                  db:"created_at"`
	UpdatedAt        string   `json:"updated_at"                  db:"updated_at"`
	Version          string   `json:"version"                     db:"version"`
	Distro           string   `json:"distro,omitempty"            db:"distro"`
	FSUUID           string   `json:"fs_uuid,omitempty"           db:"fs_uuid"`
	CompressedSize   *int64   `json:"compressed_size,omitempty"   db:"compressed_size"`
	CompressionRatio *float64 `json:"compression_ratio,omitempty" db:"compression_ratio"`
	CompressedFormat *string  `json:"compressed_format,omitempty" db:"compressed_format"`
	DeletedAt        *string  `json:"deleted_at,omitempty"        db:"deleted_at"`

	// Resolved relations
	VMs []*VM `json:"vms,omitempty"`
}

// ── ImageSpec ──

// ImageSpec corresponds to Python's ImageSpec dataclass.
type ImageSpec struct {
	Type            string  `yaml:"type"`
	Version         string  `yaml:"version"`
	Name            string  `yaml:"name"`
	Source          string  `yaml:"source"`
	Format          string  `yaml:"format"`
	Arch            string  `yaml:"arch"`
	SHA256          string  `yaml:"sha256,omitempty"`
	SHA256URL       string  `yaml:"sha256_url,omitempty"`
	ListURLTemplate *string `yaml:"list_url_template,omitempty"`
	Size            *int64  `yaml:"size,omitempty"`
}
