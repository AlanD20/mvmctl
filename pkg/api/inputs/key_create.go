package inputs

import (
	"fmt"
	"os"
	"path/filepath"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/validators"
)

// KeyCreateInput matches Python's KeyCreateInput dataclass.
//
//	@dataclass
//	class KeyCreateInput:
//	    name: str
//	    algorithm: str | None = None  # "ed25519", "rsa", "ecdsa"
//	    bits: int | None = None
//	    output_dir: Path | None = None
//	    comment: str | None = None
//	    overwrite: bool = False
//	    set_default: bool = False
type KeyCreateInput struct {
	Name       string  `json:"name"`
	Algorithm  *string `json:"algorithm,omitempty"`
	Bits       *int    `json:"bits,omitempty"`
	OutputDir  *string `json:"output_dir,omitempty"`
	Comment    *string `json:"comment,omitempty"`
	Overwrite  bool    `json:"overwrite"`
	SetDefault bool    `json:"set_default"`
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

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves defaults and validates.
// Matches Python's KeyCreateRequest.resolve().
func (r *KeyCreateRequest) Resolve() (*ResolvedKeyCreateInput, error) {
	// Validate key name early — before any work
	if err := validators.ValidateKeyName(r.input.Name); err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "key_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}

	// Default algorithm (Python: algorithm = self._inputs.algorithm or "ed25519")
	algorithm := "ed25519"
	if r.input.Algorithm != nil && *r.input.Algorithm != "" {
		algorithm = *r.input.Algorithm
	}

	// Validate algorithm
	validAlgorithms := map[string]bool{"ed25519": true, "rsa": true, "ecdsa": true}
	if !validAlgorithms[algorithm] {
		return nil, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "key_create",
			Message: fmt.Sprintf("Invalid algorithm: '%s'. Valid choices: ed25519, rsa, ecdsa", algorithm),
			Class:   errs.ClassValidation,
		}
	}

	// Default comment (Python: f"{name}@{socket.gethostname()}")
	comment := fmt.Sprintf("%s@%s", r.input.Name, getHostname())
	if r.input.Comment != nil && *r.input.Comment != "" {
		comment = *r.input.Comment
	}

	// Default output_dir resolved via CacheUtils
	outputDir := getKeysDir()
	if r.input.OutputDir != nil && *r.input.OutputDir != "" {
		outputDir = *r.input.OutputDir
	}

	// File conflict validation (caller validates)
	if !r.input.Overwrite {
		if err := keyFilesExist(r.input.Name, outputDir); err != nil {
			return nil, err
		}
	}

	r.result = &ResolvedKeyCreateInput{
		Name:       r.input.Name,
		Algorithm:  algorithm,
		Bits:       r.input.Bits,
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
		return &errs.DomainError{
			Code:    errs.CodeKeyAlreadyExists,
			Op:      "key_create",
			Message: fmt.Sprintf("Key file already exists: %s. Use --force to replace.", privateKeyPath),
			Class:   errs.ClassConflict,
		}
	}
	if _, err := os.Stat(pubKeyPath); err == nil {
		return &errs.DomainError{
			Code:    errs.CodeKeyAlreadyExists,
			Op:      "key_create",
			Message: fmt.Sprintf("Key file already exists: %s. Use --force to replace.", pubKeyPath),
			Class:   errs.ClassConflict,
		}
	}
	return nil
}

// getHostname returns the system hostname.
// Matches Python's socket.gethostname() which raises socket.gaierror on failure.
func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		// Python raises an exception; Go returns empty string to match the
		// spirit of an error — the fmt.Sprintf below will produce "name@"
		return ""
	}
	return hostname
}

// getKeysDir returns the default keys directory.
func getKeysDir() string {
	return infra.GetKeysDir()
}
