package inputs

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
)

// VolumeCreateInput matches Python's VolumeCreateInput dataclass.
//
//	@dataclass
//	class VolumeCreateInput:
//	    name: str
//	    size: str
//	    format: str | None = None  # 'raw' or 'qcow2', default resolved in request
//	    read_only: bool | None = None
type VolumeCreateInput struct {
	Name     string  `json:"name"`
	Size     string  `json:"size"`
	Format   *string `json:"format,omitempty"`
	ReadOnly *bool   `json:"read_only,omitempty"`
}

// ResolvedVolumeCreateInput matches Python's ResolvedVolumeCreateInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedVolumeCreateInput:
//	    name: str
//	    size_bytes: int
//	    format: str
//	    path: Path
//	    is_read_only: bool = False
type ResolvedVolumeCreateInput struct {
	Name       string
	SizeBytes  int64
	Format     string
	Path       string
	IsReadOnly bool
}

// VolumeCreateRequest matches Python's VolumeCreateRequest.
//
// Resolve volume creation inputs to explicit values.
type VolumeCreateRequest struct {
	db      *sql.DB
	_input  VolumeCreateInput
	_result *ResolvedVolumeCreateInput
	repo    volume.Repository
}

// NewVolumeCreateRequest creates a new VolumeCreateRequest.
func NewVolumeCreateRequest(inputs VolumeCreateInput, db *sql.DB, volumeRepo volume.Repository) *VolumeCreateRequest {
	return &VolumeCreateRequest{
		db:     db,
		_input: inputs,
		repo:   volumeRepo,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
func (r *VolumeCreateRequest) Result() *ResolvedVolumeCreateInput {
	return r._result
}

// Resolve resolves creation inputs to explicit values.
// Matches Python's VolumeCreateRequest.resolve().
func (r *VolumeCreateRequest) Resolve(ctx context.Context) (*ResolvedVolumeCreateInput, error) {
	sizeBytes, err := infra.ParseDiskSizeToBytes(r._input.Size)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "volume_create",
			Message: fmt.Sprintf("Invalid volume size: %s", err.Error()),
			Class:   errs.ClassValidation,
		}
	}

	// Default format is "raw" — Python: fmt = self._inputs.format if self._inputs.format is not None else "raw"
	format := "raw"
	if r._input.Format != nil {
		format = *r._input.Format
	}

	if format != "raw" && format != "qcow2" {
		return nil, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "volume_create",
			Message: fmt.Sprintf("Unsupported format: %s. Use 'raw' or 'qcow2'.", format),
			Class:   errs.ClassValidation,
		}
	}

	path := filepath.Join(infra.GetVolumesDir(), fmt.Sprintf("%s.%s", r._input.Name, format))

	isReadOnly := false
	if r._input.ReadOnly != nil {
		isReadOnly = *r._input.ReadOnly
	}

	r._result = &ResolvedVolumeCreateInput{
		Name:       r._input.Name,
		SizeBytes:  sizeBytes,
		Format:     format,
		Path:       path,
		IsReadOnly: isReadOnly,
	}

	if err := r.ensureValidate(ctx); err != nil {
		return nil, err
	}

	return r._result, nil
}

func (r *VolumeCreateRequest) ensureValidate(ctx context.Context) error {
	if r._result == nil {
		return &errs.DomainError{
			Code:    errs.CodeVolumeNotFound,
			Op:      "volume_create",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	if err := infra.ValidateVolumeName(r._result.Name); err != nil {
		return &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "volume_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}

	// Check for existing volume with same name
	existing, err := r.repo.GetByName(ctx, r._result.Name)
	if err != nil {
		return &errs.DomainError{
			Code:    errs.CodeDatabaseError,
			Op:      "volume_create",
			Message: "Failed to check existing volume: " + err.Error(),
			Class:   errs.ClassInternal,
		}
	}
	if existing != nil {
		return &errs.DomainError{
			Code:    errs.CodeVolumeAlreadyExists,
			Op:      "volume_create",
			Message: fmt.Sprintf("Volume '%s' already exists", r._result.Name),
			Class:   errs.ClassConflict,
		}
	}

	return nil
}
