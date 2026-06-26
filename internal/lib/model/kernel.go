package model

// --- KernelItem ---

// KernelItem represents a cached kernel.
type KernelItem struct {
	ID        string  `json:"id"                   db:"id"`
	Name      string  `json:"name"                 db:"name"`
	BaseName  string  `json:"base_name"            db:"base_name"`
	Version   string  `json:"version"              db:"version"`
	Arch      string  `json:"arch"                 db:"arch"`
	Type      string  `json:"type"                 db:"type"`
	Path      string  `json:"path"                 db:"path"`
	IsDefault bool    `json:"is_default"           db:"is_default"`
	IsPresent bool    `json:"is_present"           db:"is_present"`
	CreatedAt string  `json:"created_at"           db:"created_at"`
	UpdatedAt string  `json:"updated_at"           db:"updated_at"`
	DeletedAt *string `json:"deleted_at,omitempty" db:"deleted_at"`

	// Resolved relations
	VMs       []*VMItem       `json:"vms,omitempty"`
	Snapshots []*SnapshotItem `json:"snapshots,omitempty"`
}

// --- KernelPullResult ---

// KernelPullResult holds the result of a kernel pull.
type KernelPullResult struct {
	Path         string
	Version      string
	Arch         string
	KernelType   string
	Warnings     []string
	InfoMessages []string
}

// --- KernelFeature ---

// KernelFeature defines an optional kernel feature with enforced config keys.
// When a feature is selected, its Enforce map is applied on top of the
// spec's DefaultConfigs, and each enforced key is verified in the final .config.
type KernelFeature struct {
	Desc    string            `yaml:"desc"`
	Enforce map[string]string `yaml:"enforce,omitempty"`
}

// --- KernelSpec ---

// KernelSpec defines a kernel in the YAML spec.
type KernelSpec struct {
	Name              string                   `yaml:"name"`
	KernelType        string                   `yaml:"kernel_type"`
	Version           string                   `yaml:"version"`
	Source            string                   `yaml:"source"`
	OutputName        string                   `yaml:"output_name"`
	BuildDir          string                   `yaml:"build_dir"`
	ListURLTemplate   *string                  `yaml:"list_url_template,omitempty"`
	ConfigURLTemplate *string                  `yaml:"config_url_template,omitempty"`
	SHA256            string                   `yaml:"sha256,omitempty"`
	SHA256URL         string                   `yaml:"sha256_url,omitempty"`
	ConfigFragments   []string                 `yaml:"config_fragments"`
	ParallelJobs      *int                     `yaml:"parallel_jobs,omitempty"`
	DefaultConfigs    map[string]string        `yaml:"default_configs"`
	Resolver          *string                  `yaml:"resolver,omitempty"`
	VersionsURL       *string                  `yaml:"versions_url,omitempty"`
	FilePattern       *string                  `yaml:"file_pattern,omitempty"`
	FileSuffix        *string                  `yaml:"file_suffix,omitempty"`
	Options           map[string]any           `yaml:"options,omitempty"` // Kernel-specific options from YAML; schema varies by kernel type
	Features          map[string]KernelFeature `yaml:"features,omitempty"`
}
