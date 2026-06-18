package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
	"path/filepath"

	"github.com/jmoiron/sqlx"
)

// VolumeCreateInput specifies volume create input.
type VolumeCreateInput struct {
	Name     string  `json:"name"`
	Size     string  `json:"size"`
	Format   *string `json:"format,omitempty"`
	ReadOnly *bool   `json:"read_only,omitempty"`
}

// ResolvedVolumeCreateInput specifies resolved volume create input.
type ResolvedVolumeCreateInput struct {
	Name       string
	SizeBytes  int64
	Format     model.VolumeFormat
	Path       string
	IsReadOnly bool
}

// VolumeCreateRequest specifies volume create request.
// Resolve volume creation inputs to explicit values.
type VolumeCreateRequest struct {
	db     *sqlx.DB
	input  VolumeCreateInput
	result *ResolvedVolumeCreateInput
	repo   volume.Repository
}

// NewVolumeCreateRequest creates a new VolumeCreateRequest.
func NewVolumeCreateRequest(inputs VolumeCreateInput, db *sqlx.DB, volumeRepo volume.Repository) *VolumeCreateRequest {
	return &VolumeCreateRequest{
		db:    db,
		input: inputs,
		repo:  volumeRepo,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
// Resolve resolves creation inputs to explicit values.
func (r *VolumeCreateRequest) Resolve(ctx context.Context) (*ResolvedVolumeCreateInput, error) {
	sizeBytes, err := disk.ParseDiskSizeToBytes(r.input.Size)
	if err != nil {
		return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid volume size: %s", err.Error()))
	}
	// Default format is "raw" —
	format := model.VolumeFormatRaw
	if r.input.Format != nil {
		format = model.VolumeFormat(*r.input.Format)
	}
	if format != model.VolumeFormatRaw && format != model.VolumeFormatQCOW2 {
		return nil, errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Unsupported format: %s. Use 'raw' or 'qcow2'.", format),
		)
	}
	path := filepath.Join(infra.GetVolumesDir(), fmt.Sprintf("%s.%s", r.input.Name, string(format)))
	isReadOnly := false
	if r.input.ReadOnly != nil {
		isReadOnly = *r.input.ReadOnly
	}
	r.result = &ResolvedVolumeCreateInput{
		Name:       r.input.Name,
		SizeBytes:  sizeBytes,
		Format:     format,
		Path:       path,
		IsReadOnly: isReadOnly,
	}
	if err := r.ensureValidate(ctx); err != nil {
		return nil, err
	}
	return r.result, nil
}
func (r *VolumeCreateRequest) ensureValidate(ctx context.Context) error {
	if r.result == nil {
		return errs.New(errs.CodeVolumeNotFound, "Failed to resolve necessary dependencies to validate")
	}
	if err := validators.VolumeName(r.result.Name); err != nil {
		return errs.New(errs.CodeValidationFailed, err.Error())
	}
	// Check for existing volume with same name
	existing, err := r.repo.GetByName(ctx, r.result.Name)
	if err != nil {
		return errs.New(errs.CodeDatabaseError, "Failed to check existing volume: "+err.Error())
	}
	if existing != nil {
		return errs.AlreadyExists(
			errs.CodeVolumeAlreadyExists,
			fmt.Sprintf("Volume '%s' already exists", r.result.Name),
		)
	}
	return nil
}
