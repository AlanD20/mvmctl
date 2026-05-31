package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"

	"github.com/jmoiron/sqlx"
)

// KernelInput is the raw input for identifying existing kernels.
type KernelInput struct {
	Identifiers []string `json:"identifiers"`
	Force       *bool    `json:"force,omitempty"`
}

// ResolvedKernelInput matches Python's ResolvedKernelInput (frozen dataclass).
type ResolvedKernelInput struct {
	Kernels []*model.KernelItem
	Force   bool
}

// KernelRequest matches Python's KernelRequest.
//
// Resolve kernel identifiers to DB records and validate.
type KernelRequest struct {
	db       *sqlx.DB
	input    KernelInput
	result   *ResolvedKernelInput
	resolver *kernel.Resolver
}

// NewKernelRequest creates a new KernelRequest.
func NewKernelRequest(inputs KernelInput, db *sqlx.DB, kernelRepo kernel.Repository) *KernelRequest {
	return &KernelRequest{
		db:       db,
		input:    inputs,
		resolver: kernel.NewResolver(kernelRepo, nil),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves kernel identifiers to KernelItem records.
// Matches Python's KernelRequest.resolve().
func (r *KernelRequest) Resolve(ctx context.Context) (*ResolvedKernelInput, error) {
	if len(r.input.Identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel",
			Message: "No kernel identifiers provided",
			Class:   errs.ClassValidation,
		}
	}

	// Validate identifier length — max 64 chars.
	for _, ident := range r.input.Identifiers {
		if len(ident) > 64 {
			return nil, &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "kernel",
				Message: fmt.Sprintf("Kernel identifier too long: '%s' exceeds maximum length of 64 characters", ident),
				Class:   errs.ClassValidation,
			}
		}
	}

	result := r.resolver.ResolveMany(ctx, r.input.Identifiers)

	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel",
			Message: "Could not resolve any kernels: " + strings.Join(result.Errors, ", "),
			Class:   errs.ClassValidation,
		}
	}

	force := false
	if r.input.Force != nil {
		force = *r.input.Force
	}

	r.result = &ResolvedKernelInput{
		Kernels: result.Items,
		Force:   force,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r.result, nil
}

func (r *KernelRequest) ensureValidate() error {
	if r.result == nil {
		return &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	if len(r.result.Kernels) == 0 {
		return &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel",
			Message: "No kernels found matching identifiers",
			Class:   errs.ClassValidation,
		}
	}

	return nil
}
