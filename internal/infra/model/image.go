package model

// ── ImageItem ──

// ImageItem corresponds to Python's ImageItem dataclass exactly.
type ImageItem struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	Name             string   `json:"name"`
	Arch             string   `json:"arch"`
	Path             string   `json:"path"`
	FSType           string   `json:"fs_type"`
	MinRootfsSizeMiB int      `json:"minimum_rootfs_size_mib"`
	OriginalSize     int64    `json:"original_size"`
	IsDefault        bool     `json:"is_default"`
	IsPresent        bool     `json:"is_present"`
	PulledAt         string   `json:"pulled_at"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	Version          string   `json:"version"`
	Distro           *string  `json:"distro,omitempty"`
	FSUUID           *string  `json:"fs_uuid,omitempty"`
	CompressedSize   *int64   `json:"compressed_size,omitempty"`
	CompressionRatio *float64 `json:"compression_ratio,omitempty"`
	CompressedFormat *string  `json:"compressed_format,omitempty"`
	DeletedAt        *string  `json:"deleted_at,omitempty"`

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
	SHA256          *string `yaml:"sha256,omitempty"`
	SHA256URL       *string `yaml:"sha256_url,omitempty"`
	ListURLTemplate *string `yaml:"list_url_template,omitempty"`
	Size            *int64  `yaml:"size,omitempty"`
}

// ── ImageVersion ──

// ImageVersion corresponds to Python's ImageVersion dataclass.
type ImageVersion struct {
	Version     string  `json:"version"`
	Codename    *string `json:"codename,omitempty"`
	Type        string  `json:"type"`
	DownloadURL string  `json:"download_url"`
	SHA256URL   *string `json:"sha256_url,omitempty"`
	Format      string  `json:"format"`
	DisplayName string  `json:"display_name"`
	TypeName    string  `json:"type_name"`
}
