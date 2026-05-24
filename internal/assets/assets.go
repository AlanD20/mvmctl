package assets

import (
	"embed"
	"fmt"
	"io/fs"

	"gopkg.in/yaml.v3"
)

//go:embed images.yaml kernels.yaml cloud-init.template.yaml
var assetFS embed.FS

// ReadFile reads a file from the embedded assets.
func ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(assetFS, name)
}

// Stat returns a FileInfo describing the named asset file.
func Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(assetFS, name)
}

// ReadDir reads the named directory from the embedded assets.
func ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(assetFS, name)
}

// ReadYAML parses a YAML file from the embedded assets into the provided value.
func ReadYAML(name string, v any) error {
	data, err := ReadFile(name)
	if err != nil {
		return fmt.Errorf("read asset %s: %w", name, err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse asset %s: %w", name, err)
	}
	return nil
}
