package inputs

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
)

// BinaryPullInput is the raw input for pulling a firecracker binary.
// Matches Python's BinaryPullInput dataclass exactly:
//
//	@dataclass
//	class BinaryPullInput:
//	    version: str
//	    name: str = "firecracker"
//	    git_ref: str | None = None
//	    set_default: bool = False
//	    download_override: bool = True   # NOTE: Python default is True, NOT false
type BinaryPullInput struct {
	Version          string  `json:"version"`
	Name             string  `json:"name"`
	GitRef           *string `json:"git_ref,omitempty"`
	SetDefault       bool    `json:"set_default"`
	DownloadOverride *bool   `json:"download_override,omitempty"` // *bool: nil ⟹ True (Python default)
}

// ResolvedBinaryPullInput matches Python's ResolvedBinaryPullInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedBinaryPullInput:
//	    version: str
//	    name: str
//	    git_ref: str | None
//	    set_default: bool
//	    bin_dir: Path
//	    download_override: bool
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
	db      *sql.DB
	_input  BinaryPullInput
	_result *ResolvedBinaryPullInput
}

// NewBinaryPullRequest creates a new BinaryPullRequest.
func NewBinaryPullRequest(inputs BinaryPullInput, db *sql.DB) *BinaryPullRequest {
	return &BinaryPullRequest{
		db:     db,
		_input: inputs,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
func (r *BinaryPullRequest) Result() *ResolvedBinaryPullInput {
	return r._result
}

// Resolve resolves and validates pull inputs.
// Matches Python's BinaryPullRequest.resolve() exactly.
func (r *BinaryPullRequest) Resolve(ctx context.Context) (*ResolvedBinaryPullInput, error) {
	// Default download_override to true (matches Python default: bool = True).
	// In Go, bool zero-value is false, so we use *bool and default to true when nil.
	downloadOverride := true
	if r._input.DownloadOverride != nil {
		downloadOverride = *r._input.DownloadOverride
	}

	// Normalize version (strip 'v' prefix) — Python: version = self._inputs.version.removeprefix("v")
	version := strings.TrimPrefix(r._input.Version, "v")

	// When git_ref is provided, skip semver version validation — Python:
	//   if not self._inputs.git_ref and version:
	//       if not re.match(r"^\d+\.\d+(\.\d+)?$", version):
	//           raise BinaryError(...)
	if (r._input.GitRef == nil || *r._input.GitRef == "") && version != "" {
		// Validate version format (semver-like: x.y.z)
		matched, err := regexp.MatchString(`^\d+\.\d+(\.\d+)?$`, version)
		if err != nil || !matched {
			return nil, &errs.DomainError{
				Code:    errs.CodeBinaryVersionGate,
				Op:      "binary_pull",
				Message: "Invalid version format: '" + r._input.Version + "'. Expected format: x.y.z (e.g., 1.15.0)",
				Class:   errs.ClassValidation,
			}
		}
	}

	// Default name to "firecracker" (Python default: name: str = "firecracker")
	name := r._input.Name
	if name == "" {
		name = "firecracker"
	}

	// Resolve bin_dir — Python: bin_dir = CacheUtils.get_bin_dir()
	binDir := getBinDir()

	r._result = &ResolvedBinaryPullInput{
		Version:          version,
		Name:             name,
		GitRef:           r._input.GitRef,
		SetDefault:       r._input.SetDefault,
		BinDir:           binDir,
		DownloadOverride: downloadOverride,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r._result, nil
}

func (r *BinaryPullRequest) ensureValidate() error {
	if r._result == nil {
		return &errs.DomainError{
			Code:    errs.CodeBinaryNotFound,
			Op:      "binary_pull",
			Message: "No resolved pull input to validate",
			Class:   errs.ClassValidation,
		}
	}

	// Validate binary name — only firecracker is supported for pull/build
	if strings.ToLower(r._result.Name) != "firecracker" {
		return &errs.DomainError{
			Code:    errs.CodeBinaryNotFound,
			Op:      "binary_pull",
			Message: "Unsupported binary: '" + r._result.Name + "'. Only 'firecracker' is supported for download or build.",
			Class:   errs.ClassValidation,
		}
	}

	// Skip version check for git builds — version is determined after build
	if r._result.GitRef != nil && *r._result.GitRef != "" {
		return nil
	}

	return nil
}

// getBinDir resolves the binary directory path.
func getBinDir() string {
	return infra.GetBinDir()
}
