package inputs

import (
	"context"
	"strings"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"

	"github.com/jmoiron/sqlx"
)

// VolumeInput matches Python's VolumeInput dataclass.
//
//	@dataclass
//	class VolumeInput:
//	    identifiers: list[str] = field(default_factory=list)
type VolumeInput struct {
	Identifiers []string `json:"identifiers,omitempty"`
}

// ResolvedVolumeInput matches Python's ResolvedVolumeInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedVolumeInput:
//	    volumes: list[VolumeItem]
type ResolvedVolumeInput struct {
	Volumes []*model.VolumeItem
}

// VolumeRequest matches Python's VolumeRequest.
//
// Request that resolves VolumeInput to VolumeItem via DB.
type VolumeRequest struct {
	db       *sqlx.DB
	input    VolumeInput
	result   *ResolvedVolumeInput
	resolver *volume.Resolver
	_errors  []string
}

// NewVolumeRequest creates a new VolumeRequest.
func NewVolumeRequest(inputs VolumeInput, db *sqlx.DB, volumeRepo volume.Repository) *VolumeRequest {
	return &VolumeRequest{
		db:       db,
		input:    inputs,
		resolver: volume.NewResolver(volumeRepo),
		_errors:  []string{},
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Errors returns partial-match errors from resolution (identifiers that couldn't be resolved).
// Matches Python's VolumeRequest.errors property.
func (r *VolumeRequest) Errors() []string {
	return r._errors
}

// Resolve resolves identifiers to VolumeItem records from DB.
// Matches Python's VolumeRequest.resolve().
func (r *VolumeRequest) Resolve(ctx context.Context) (*ResolvedVolumeInput, error) {
	identifiers := r.input.Identifiers

	if len(identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeVolumeNotFound,
			Op:      "volume",
			Message: "No volume identifiers provided",
			Class:   errs.ClassValidation,
		}
	}

	result := r.resolver.ResolveMany(ctx, identifiers)

	if len(result.Errors) > 0 && len(result.Volumes) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeVolumeNotFound,
			Op:      "volume",
			Message: "Could not resolve any volumes: " + strings.Join(result.Errors, ", "),
			Class:   errs.ClassValidation,
		}
	}

	// Store partial-match errors so callers can surface them
	if len(result.Errors) > 0 {
		r._errors = result.Errors
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
		return &errs.DomainError{
			Code:    errs.CodeVolumeNotFound,
			Op:      "volume",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	if len(r.result.Volumes) == 0 {
		return &errs.DomainError{
			Code:    errs.CodeVolumeNotFound,
			Op:      "volume",
			Message: "No volumes found matching identifiers",
			Class:   errs.ClassValidation,
		}
	}

	return nil
}
