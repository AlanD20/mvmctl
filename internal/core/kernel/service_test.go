package kernel

import (
	"testing"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/lib/model"
)

// specYAML mirrors the intermediate YAML struct used in Service.LoadSpecs
// to verify backward-compatible loading of base_config_url_template.
type specYAML struct {
	KernelType            string                         `yaml:"type"`
	Version               string                         `yaml:"version"`
	Source                string                         `yaml:"source"`
	OutputName            string                         `yaml:"output_name"`
	BuildDir              string                         `yaml:"build_dir"`
	ListURLTemplate       *string                        `yaml:"list_url_template,omitempty"`
	BaseConfigURLTemplate *string                        `yaml:"base_config_url_template,omitempty"`
	SHA256                string                         `yaml:"sha256,omitempty"`
	SHA256URL             string                         `yaml:"sha256_url,omitempty"`
	ConfigFragments       []string                       `yaml:"config_fragments"`
	ParallelJobs          *int                           `yaml:"parallel_jobs,omitempty"`
	DefaultConfigs        map[string]string              `yaml:"default_configs"`
	Resolver              *string                        `yaml:"resolver,omitempty"`
	VersionsURL           *string                        `yaml:"versions_url,omitempty"`
	FilePattern           *string                        `yaml:"file_pattern,omitempty"`
	FileSuffix            *string                        `yaml:"file_suffix,omitempty"`
	Options               map[string]any                 `yaml:"options,omitempty"`
	Features              map[string]model.KernelFeature `yaml:"features,omitempty"`
}

// TestBaseConfigURLTemplateNewField verifies that kernels.yaml with the new
// base_config_url_template field populates spec.BaseConfigURLTemplate.
func TestBaseConfigURLTemplateNewField(t *testing.T) {
	t.Parallel()

	yamlData := []byte(`
type: official
version: "6.1"
base_config_url_template: "https://example.com/{arch}.config"
config_fragments: []
default_configs: {}
`)

	var sy specYAML
	if err := yaml.Unmarshal(yamlData, &sy); err != nil {
		t.Fatalf("failed to unmarshal YAML: %v", err)
	}

	if sy.BaseConfigURLTemplate == nil {
		t.Fatal("expected BaseConfigURLTemplate to be set")
	}
	if *sy.BaseConfigURLTemplate != "https://example.com/{arch}.config" {
		t.Errorf("expected URL %q, got %q", "https://example.com/{arch}.config", *sy.BaseConfigURLTemplate)
	}
}
