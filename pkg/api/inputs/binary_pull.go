package inputs

import (
	"context"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"

	"github.com/jmoiron/sqlx"
)

// BinaryPullInput is the raw input for pulling a firecracker binary.
// Matches Python's BinaryPullInput dataclass exactly:
type BinaryPullInput struct {
	Version          string  `json:"version"`
	Name             string  `json:"name"`
	GitRef           *string `json:"git_ref,omitempty"`
	SetDefault       bool    `json:"set_default"`
	DownloadOverride bool    `json:"download_override"`
}

// ResolvedBinaryPullInput matches Python's ResolvedBinaryPullInput (frozen dataclass).
type ResolvedBinaryPullInput struct {
	Version          string
	Name             string
	GitRef           *string
	SetDefault       bool
	BinDir           string
	DownloadOverride bool
}

// BinaryPullRequest matches Python's BinaryPullRequest.
type BinaryPullRequest struct {
	db     *sqlx.DB
	input  BinaryPullInput
	result *ResolvedBinaryPullInput
}

// NewBinaryPullRequest creates a new BinaryPullRequest.
func NewBinaryPullRequest(inputs BinaryPullInput, db *sqlx.DB) *BinaryPullRequest {
	return &BinaryPullRequest{
		db:    db,
		input: inputs,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves and validates pull inputs.
// Matches Python's BinaryPullRequest.resolve() exactly.
func (r *BinaryPullRequest) Resolve(ctx context.Context) (*ResolvedBinaryPullInput, error) {
	// Normalize version (strip 'v' prefix) — Python: version = self._inputs.version.removeprefix("v")
	version := strings.TrimPrefix(r.input.Version, "v")

	// Version validation is handled by the service's ResolveVersion method,
	// which accepts latest, partial (e.g. "1.15"), and exact (e.g. "1.15.1") specs.
	// When git_ref is provided, version may be empty.

	// Default name to "firecracker"
	name := r.input.Name
	if name == "" {
		name = "firecracker"
	}

	r.result = &ResolvedBinaryPullInput{
		Version:          version,
		Name:             name,
		GitRef:           r.input.GitRef,
		SetDefault:       r.input.SetDefault,
		BinDir:           infra.GetBinDir(),
		DownloadOverride: r.input.DownloadOverride,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r.result, nil
}

func (r *BinaryPullRequest) ensureValidate() error {
	if r.result == nil {
		return errs.New(errs.CodeBinaryNotFound, "No resolved pull input to validate")
	}

	// Validate binary name — only firecracker is supported for pull/build
	if strings.ToLower(r.result.Name) != "firecracker" {
		return errs.New(
			errs.CodeBinaryNotFound,
			"Unsupported binary: '"+r.result.Name+"'. Only 'firecracker' is supported for download or build.",
		)
	}

	// Skip version check for git builds — version is determined after build
	if r.result.GitRef != nil && *r.result.GitRef != "" {
		return nil
	}

	return nil
}
