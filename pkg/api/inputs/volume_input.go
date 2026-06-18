package inputs

import (
	"context"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
	"strings"

	"github.com/jmoiron/sqlx"
)

// VolumeInput specifies volume input.
type VolumeInput struct {
	Identifiers []string `json:"identifiers,omitempty"`
}

// ResolvedVolumeInput specifies resolved volume input.
type ResolvedVolumeInput struct {
	Volumes []*model.VolumeItem
}

// VolumeRequest specifies volume request.
// Request that resolves VolumeInput to VolumeItem via DB.
type VolumeRequest struct {
	db          *sqlx.DB
	input       VolumeInput
	result      *ResolvedVolumeInput
	resolver    *volume.Resolver
	partialErrs []string
}

// NewVolumeRequest creates a new VolumeRequest.
func NewVolumeRequest(inputs VolumeInput, db *sqlx.DB, volumeRepo volume.Repository) *VolumeRequest {
	return &VolumeRequest{
		db:          db,
		input:       inputs,
		resolver:    volume.NewResolver(volumeRepo),
		partialErrs: []string{},
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
// Errors returns partial-match errors from resolution (identifiers that couldn't be resolved).
// .errors property.
func (r *VolumeRequest) Errors() []string {
	return r.partialErrs
}

// Resolve resolves identifiers to VolumeItem records from DB.
func (r *VolumeRequest) Resolve(ctx context.Context) (*ResolvedVolumeInput, error) {
	identifiers := r.input.Identifiers
	if len(identifiers) == 0 {
		return nil, errs.NotFound(errs.CodeVolumeNotFound, "No volume identifiers provided")
	}
	result := r.resolver.ResolveMany(ctx, identifiers)
	if len(result.Errors) > 0 && len(result.Volumes) == 0 {
		return nil, errs.NotFound(
			errs.CodeVolumeNotFound,
			"Could not resolve any volumes: "+strings.Join(result.Errors, ", "),
		)
	}
	// Store partial-match errors so callers can surface them
	if len(result.Errors) > 0 {
		r.partialErrs = result.Errors
	}
	r.result = &ResolvedVolumeInput{
		Volumes: result.Volumes,
	}
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}
	return r.result, nil
}
func (r *VolumeRequest) ensureValidate() error {
	if r.result == nil {
		return errs.New(errs.CodeVolumeNotFound, "Failed to resolve necessary dependencies to validate")
	}
	if len(r.result.Volumes) == 0 {
		return errs.NotFound(errs.CodeVolumeNotFound, "No volumes found matching identifiers")
	}
	return nil
}
