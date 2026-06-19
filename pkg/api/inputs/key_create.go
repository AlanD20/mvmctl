package inputs

import (
	"fmt"
	"mvmctl/internal/core/key"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
	"os"
	"path/filepath"
)

// KeyCreateInput holds options for key creation.
type KeyCreateInput struct {
	Name       string `json:"name"                 yaml:"name"`
	Algorithm  string `json:"algorithm,omitempty"  yaml:"algorithm,omitempty"`
	Bits       int    `json:"bits,omitempty"       yaml:"bits,omitempty"`
	OutputDir  string `json:"output_dir,omitempty"`
	Comment    string `json:"comment,omitempty"    yaml:"comment,omitempty"`
	Overwrite  bool   `json:"force"                yaml:"force"`
	SetDefault bool   `json:"default"              yaml:"default"`
}

// ResolvedKeyCreateInput specifies resolved key create input.
type ResolvedKeyCreateInput struct {
	Name       string
	Algorithm  string
	Bits       *int
	OutputDir  string
	Comment    string
	Overwrite  bool
	SetDefault bool
}

// Validate checks that the key create input is valid.
func (i *KeyCreateInput) Validate() error {
	if i.Name == "" {
		return fmt.Errorf("key name is required")
	}
	if i.Algorithm != "" {
		if !key.ValidAlgorithms[i.Algorithm] {
			return fmt.Errorf("invalid algorithm: '%s'. Valid choices: ed25519, rsa, ecdsa", i.Algorithm)
		}
	}
	return nil
}

// Resolve resolves defaults and validates.
func (i *KeyCreateInput) Resolve() (*ResolvedKeyCreateInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	// Validate key name early — before any work
	if err := validators.KeyName(i.Name); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, err.Error())
	}
	// Default algorithm
	algorithm := i.Algorithm
	if algorithm == "" {
		algorithm = "ed25519"
	}
	// Validate algorithm
	if !key.ValidAlgorithms[algorithm] {
		return nil, errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid algorithm: '%s'. Valid choices: ed25519, rsa, ecdsa", algorithm),
		)
	}
	// Default comment
	comment := i.Comment
	if comment == "" {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "unknown"
		}
		comment = fmt.Sprintf("%s@%s", i.Name, hostname)
	}
	// Default output_dir resolved via CacheUtils
	outputDir := i.OutputDir
	if outputDir == "" {
		outputDir = infra.GetKeysDir()
	}
	// File conflict validation (caller validates)
	if !i.Overwrite {
		if err := keyFilesExist(i.Name, outputDir); err != nil {
			return nil, err
		}
	}
	// Keep Bits as *int in the resolved output.
	// Zero means "not specified".
	var bits *int
	if i.Bits != 0 {
		bits = &i.Bits
	}
	return &ResolvedKeyCreateInput{
		Name:       i.Name,
		Algorithm:  algorithm,
		Bits:       bits,
		OutputDir:  outputDir,
		Comment:    comment,
		Overwrite:  i.Overwrite,
		SetDefault: i.SetDefault,
	}, nil
}

// keyFilesExist checks if key files already exist on disk.
func keyFilesExist(name, outputDir string) error {
	privateKeyPath := filepath.Join(outputDir, name)
	pubKeyPath := filepath.Join(outputDir, name+".pub")
	if _, err := os.Stat(privateKeyPath); err == nil {
		return errs.AlreadyExists(
			errs.CodeKeyAlreadyExists,
			fmt.Sprintf("Key file already exists: %s. Use --force to replace.", privateKeyPath),
		)
	}
	if _, err := os.Stat(pubKeyPath); err == nil {
		return errs.AlreadyExists(
			errs.CodeKeyAlreadyExists,
			fmt.Sprintf("Key file already exists: %s. Use --force to replace.", pubKeyPath),
		)
	}
	return nil
}
