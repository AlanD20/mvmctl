package asset

import (
	"fmt"
	"io/fs"
	"strings"

	"mvmctl/internal/assets"
	"mvmctl/pkg/errs"
)

// AssetFile represents a bundled asset file accessible via embed.FS.
// Provides ReadText, ReadBytes, Exists, IsFile, and Name methods.
type AssetFile struct {
	path string
}

// ReadText reads the file content as text.
// ReadText reads the file content as text.
func (f *AssetFile) ReadText() (string, error) {
	data, err := assets.ReadFile(f.path)
	if err != nil {
		return "", convertError(f.path, err)
	}
	return string(data), nil
}

// ReadBytes reads the file content as bytes.
// ReadBytes reads the file content as bytes.
func (f *AssetFile) ReadBytes() ([]byte, error) {
	data, err := assets.ReadFile(f.path)
	if err != nil {
		return nil, convertError(f.path, err)
	}
	return data, nil
}

// Exists returns true if the file exists in the embedded assets.
// Exists returns true if the file exists in the embedded assets.
func (f *AssetFile) Exists() bool {
	_, err := assets.Stat(f.path)
	return err == nil
}

// IsFile returns true if the path is a file (not a directory).
// IsFile returns true if the path is a file (not a directory).
func (f *AssetFile) IsFile() bool {
	fi, err := assets.Stat(f.path)
	if err != nil {
		return false
	}
	return !fi.IsDir()
}

// Name returns the base name of the file.
// Name returns the base name of the file.
func (f *AssetFile) Name() string {
	parts := strings.Split(f.path, "/")
	return parts[len(parts)-1]
}

// Manager provides access to bundled package assets (templates, YAML configs, defaults).
// Uses the embedded assets from the internal/assets package via embed.FS.
type Manager struct {
	base string
}

// New creates a new Manager and verifies assets are accessible.
// Returns an error at first access if no embedded assets are found.
func New() *Manager {
	// Verify assets package is accessible by checking that
	// at least one known asset file can be read.
	knownAssets := []string{"images.yaml", "kernels.yaml"}
	var found bool
	for _, name := range knownAssets {
		_, err := assets.ReadFile(name)
		if err == nil {
			found = true
			break
		}
	}
	if !found {
		// We still return a Manager — the error will happen at first access.
		// Verification found nothing, but Manager is still usable — error surfaces on first access.
	}
	return &Manager{base: "."}
}

// GetFile returns an AssetFile handle for a bundled asset file.
// Supports nested paths by passing multiple path components.
//
// Examples:
//
//	file := manager.GetFile("cloud_init.template.yaml")
//	content, err := file.ReadText()
//
//	file := manager.GetFile("templates", "config.yaml")
//
// Supports nested paths by passing multiple path components.
func (m *Manager) GetFile(pathParts ...string) (*AssetFile, error) {
	if len(pathParts) == 0 {
		return nil, errs.New(errs.CodeBundledAssetError, "At least one path part is required")
	}

	return &AssetFile{path: strings.Join(pathParts, "/")}, nil
}

// ReadFile reads and returns the contents of a bundled asset file as text.
func (m *Manager) ReadFile(pathParts ...string) (string, error) {
	if len(pathParts) == 0 {
		return "", errs.New(errs.CodeBundledAssetError, "At least one path part is required")
	}

	path := strings.Join(pathParts, "/")
	data, err := assets.ReadFile(path)
	if err != nil {
		return "", convertError(path, err)
	}
	return string(data), nil
}

// ReadBytes reads and returns the contents of a bundled asset file as bytes.
func (m *Manager) ReadBytes(pathParts ...string) ([]byte, error) {
	if len(pathParts) == 0 {
		return nil, errs.New(errs.CodeBundledAssetError, "At least one path part is required")
	}

	path := strings.Join(pathParts, "/")
	data, err := assets.ReadFile(path)
	if err != nil {
		return nil, convertError(path, err)
	}
	return data, nil
}

// FileExists checks if a bundled asset file (not a directory) exists.
func (m *Manager) FileExists(pathParts ...string) bool {
	if len(pathParts) == 0 {
		return false
	}

	path := strings.Join(pathParts, "/")
	fi, err := assets.Stat(path)
	if err != nil {
		return false
	}
	return !fi.IsDir()
}

// ListFiles lists all files in the assets root directory.
func (m *Manager) ListFiles() []string {
	entries, err := assets.ReadDir(".")
	if err != nil {
		return nil
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}
	return files
}

// convertError maps embed.FS errors to the appropriate domain errors.
func convertError(path string, err error) error {
	if isFileNotFound(err) {
		return errs.New(
			errs.CodeBundledAssetNotFound,
			fmt.Sprintf("Asset file not found: '%s'", path),
			errs.WithEntity(path),
		)
	}
	return errs.New(errs.CodeBundledAssetError, fmt.Sprintf("Failed to read asset file '%s': %v", path, err))
}

// isFileNotFound checks if an error is a "file not found" error.
func isFileNotFound(err error) bool {
	if pe, ok := err.(*fs.PathError); ok {
		return pe.Err == fs.ErrNotExist
	}
	return strings.HasSuffix(err.Error(), "file does not exist")
}
