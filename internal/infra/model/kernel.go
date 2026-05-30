package model

// ── KernelItem ──

// KernelItem corresponds to Python's KernelItem dataclass.
type KernelItem struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	BaseName  string  `json:"base_name"`
	Version   string  `json:"version"`
	Arch      string  `json:"arch"`
	Type      string  `json:"type"`
	Path      string  `json:"path"`
	IsDefault bool    `json:"is_default"`
	IsPresent bool    `json:"is_present"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DeletedAt *string `json:"deleted_at,omitempty"`

	// Resolved relations
	VMs []*VM `json:"vms,omitempty"`
}

// ── KernelPullResult ──

// KernelPullResult corresponds to Python's KernelPullResult.
type KernelPullResult struct {
	Path         string
	Version      string
	Arch         string
	KernelType   string
	Warnings     []string
	InfoMessages []string
}

// ── KernelFeature ──

// KernelFeature corresponds to Python's KernelFeature.
type KernelFeature struct {
	Desc     string   `yaml:"desc"`
	Configs  []string `yaml:"configs"`
	Requires []string `yaml:"requires"`
}

// ── KernelSpec ──

// KernelSpec corresponds to Python's KernelSpec.
type KernelSpec struct {
	Name              string                   `yaml:"name"`
	KernelType        string                   `yaml:"kernel_type"`
	Version           string                   `yaml:"version"`
	Source            string                   `yaml:"source"`
	OutputName        string                   `yaml:"output_name"`
	BuildDir          string                   `yaml:"build_dir"`
	ListURLTemplate   *string                  `yaml:"list_url_template,omitempty"`
	ConfigURLTemplate *string                  `yaml:"config_url_template,omitempty"`
	SHA256            *string                  `yaml:"sha256,omitempty"`
	SHA256URL         *string                  `yaml:"sha256_url,omitempty"`
	ConfigFragments   []string                 `yaml:"config_fragments"`
	ParallelJobs      *int                     `yaml:"parallel_jobs,omitempty"`
	EnabledConfigs    []string                 `yaml:"enabled_configs"`
	DisabledConfigs   []string                 `yaml:"disabled_configs"`
	SetValConfigs     [][2]string              `yaml:"set_val_configs,omitempty"`
	RequiredSettings  []string                 `yaml:"required_settings"`
	Resolver          *string                  `yaml:"resolver,omitempty"`
	VersionsURL       *string                  `yaml:"versions_url,omitempty"`
	FilePattern       *string                  `yaml:"file_pattern,omitempty"`
	FileSuffix        *string                  `yaml:"file_suffix,omitempty"`
	Options           map[string]any           `yaml:"options,omitempty"` // Kernel-specific options from YAML; schema varies by kernel type
	Features          map[string]KernelFeature `yaml:"features,omitempty"`
}
