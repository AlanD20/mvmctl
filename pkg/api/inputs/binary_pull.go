package inputs

import (
	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
	"strings"
)

// BinaryPullInput holds options for pulling a Firecracker binary.
type BinaryPullInput struct {
	Version          string  `json:"version"           yaml:"version"`
	Type             string  `json:"type"              yaml:"type"`
	GitRef           *string `json:"git_ref,omitempty" yaml:"git_ref,omitempty"`
	SetDefault       bool    `json:"set_default"       yaml:"default"`
	DownloadOverride bool    `json:"force"             yaml:"force"`
}

// ResolvedBinaryPullInput specifies resolved binary pull input.
type ResolvedBinaryPullInput struct {
	Version          string
	Type             string
	GitRef           *string
	SetDefault       bool
	BinDir           string
	DownloadOverride bool
}

// Validate checks that the binary pull input is valid.
func (i *BinaryPullInput) Validate() error {
	if i.Type != "" && strings.ToLower(i.Type) != "firecracker" {
		return errs.New(
			errs.CodeBinaryPullFailed,
			"Unsupported binary: '"+i.Type+"'. Only 'firecracker' is supported for download or build.",
		)
	}
	return nil
}

// Resolve resolves and validates pull inputs, returning a ResolvedBinaryPullInput.
func (i *BinaryPullInput) Resolve() (*ResolvedBinaryPullInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	// Normalize version (strip 'v' prefix).
	version := strings.TrimPrefix(i.Version, "v")
	// Version validation is handled by the service's ResolveVersion method,
	// which accepts latest, partial (e.g. "1.15"), and exact (e.g. "1.15.1") specs.
	// When git_ref is provided, version may be empty.
	// Default type to "firecracker"
	typ := i.Type
	if typ == "" {
		typ = "firecracker"
	}
	// Validate resolved type
	if strings.ToLower(typ) != "firecracker" {
		return nil, errs.New(
			errs.CodeBinaryPullFailed,
			"Unsupported binary: '"+typ+"'. Only 'firecracker' is supported for download or build.",
		)
	}
	return &ResolvedBinaryPullInput{
		Version:          version,
		Type:             typ,
		GitRef:           i.GitRef,
		SetDefault:       i.SetDefault,
		BinDir:           infra.GetBinDir(),
		DownloadOverride: i.DownloadOverride,
	}, nil
}
