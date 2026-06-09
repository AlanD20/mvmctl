package inputs

import (
	"fmt"
	"os"
	"path/filepath"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// KeyCreateInput holds options for key creation.
// Matches Python's KeyCreateInput dataclass:
type KeyCreateInput struct {
	Name       string `json:"name"                 yaml:"name"`
	Algorithm  string `json:"algorithm,omitempty"  yaml:"algorithm,omitempty"`
	Bits       int    `json:"bits,omitempty"       yaml:"bits,omitempty"`
	OutputDir  string `json:"output_dir,omitempty"`
	Comment    string `json:"comment,omitempty"    yaml:"comment,omitempty"`
	Overwrite  bool   `json:"overwrite"            yaml:"force"`
	SetDefault bool   `json:"set_default"          yaml:"default"`
}

// ResolvedKeyCreateInput matches Python's ResolvedKeyCreateInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedKeyCreateInput:
//	    name: str
//	    algorithm: str
//	    bits: int | None
//	    output_dir: string
//	    comment: str
//	    overwrite: bool
//	    set_default: bool
type ResolvedKeyCreateInput struct {
	Name       string
	Algorithm  string
	Bits       *int
	OutputDir  string
	Comment    string
	Overwrite  bool
	SetDefault bool
}

// KeyCreateRequest matches Python's KeyCreateRequest.
type KeyCreateRequest struct {
	input  KeyCreateInput
	result *ResolvedKeyCreateInput
}

// NewKeyCreateRequest creates a new KeyCreateRequest.
func NewKeyCreateRequest(inputs KeyCreateInput) *KeyCreateRequest {
	return &KeyCreateRequest{
		input: inputs,
	}
}

// Resolve resolves defaults and validates.
// Matches Python's KeyCreateRequest.resolve().
func (r *KeyCreateRequest) Resolve() (*ResolvedKeyCreateInput, error) {
	// Validate key name early — before any work
	if err := validators.KeyName(r.input.Name); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, err.Error())
	}

	// Default algorithm
	algorithm := r.input.Algorithm
	if algorithm == "" {
		algorithm = "ed25519"
	}

	// Validate algorithm
	validAlgorithms := map[string]bool{"ed25519": true, "rsa": true, "ecdsa": true}
	if !validAlgorithms[algorithm] {
		return nil, errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid algorithm: '%s'. Valid choices: ed25519, rsa, ecdsa", algorithm),
		)
	}

	// Default comment (Python: f"{name}@{socket.gethostname()}")
	comment := r.input.Comment
	if comment == "" {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "unknown"
		}
		comment = fmt.Sprintf("%s@%s", r.input.Name, hostname)
	}

	// Default output_dir resolved via CacheUtils
	outputDir := r.input.OutputDir
	if outputDir == "" {
		outputDir = infra.GetKeysDir()
	}

	// File conflict validation (caller validates)
	if !r.input.Overwrite {
		if err := keyFilesExist(r.input.Name, outputDir); err != nil {
			return nil, err
		}
	}

	// Keep Bits as *int in the resolved output to match Python's
	// ResolvedKeyCreateInput.bits: int | None. Zero means "not specified".
	var bits *int
	if r.input.Bits != 0 {
		bits = &r.input.Bits
	}

	r.result = &ResolvedKeyCreateInput{
		Name:       r.input.Name,
		Algorithm:  algorithm,
		Bits:       bits,
		OutputDir:  outputDir,
		Comment:    comment,
		Overwrite:  r.input.Overwrite,
		SetDefault: r.input.SetDefault,
	}

	return r.result, nil
}

// keyFilesExist checks if key files already exist on disk.
// Matches Python's KeyCreateRequest._key_files_exist().
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
