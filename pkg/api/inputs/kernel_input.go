package inputs

import (
	"context"
	"database/sql"
	"strings"

	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// KernelInput matches Python's KernelInput dataclass.
//
//	@dataclass
//	class KernelInput:
//	    id: list[str] = field(default_factory=list)
//	    name: list[str] = field(default_factory=list)
//	    force: bool | None = None
type KernelInput struct {
	ID    []string `json:"id,omitempty"`
	Name  []string `json:"name,omitempty"`
	Force *bool    `json:"force,omitempty"`
}

// ResolvedKernelInput matches Python's ResolvedKernelInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedKernelInput:
//	    kernels: list[KernelItem]
//	    force: bool
type ResolvedKernelInput struct {
	Kernels []*model.KernelItem
	Force   bool
}

// KernelRequest matches Python's KernelRequest.
//
// Resolve kernel identifiers to DB records and validate.
type KernelRequest struct {
	db       *sql.DB
	_input   KernelInput
	_result  *ResolvedKernelInput
	resolver *kernel.Resolver
}

// NewKernelRequest creates a new KernelRequest.
func NewKernelRequest(inputs KernelInput, db *sql.DB, kernelRepo kernel.Repository) *KernelRequest {
	return &KernelRequest{
		db:       db,
		_input:   inputs,
		resolver: kernel.NewResolver(kernelRepo, nil),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
func (r *KernelRequest) Result() *ResolvedKernelInput {
	return r._result
}

// Resolve resolves kernel identifiers to KernelItem records.
// Matches Python's KernelRequest.resolve().
func (r *KernelRequest) Resolve(ctx context.Context) (*ResolvedKernelInput, error) {
	identifiers := append(r._input.ID, r._input.Name...)

	if len(identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel",
			Message: "No kernel identifiers provided",
			Class:   errs.ClassValidation,
		}
	}

	result := r.resolver.ResolveMany(ctx, identifiers)

	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel",
			Message: "Could not resolve any kernels: " + strings.Join(result.Errors, ", "),
			Class:   errs.ClassValidation,
		}
	}

	force := false
	if r._input.Force != nil {
		force = *r._input.Force
	}

	r._result = &ResolvedKernelInput{
		Kernels: result.Items,
		Force:   force,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r._result, nil
}

func (r *KernelRequest) ensureValidate() error {
	if r._result == nil {
		return &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	if len(r._result.Kernels) == 0 {
		return &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel",
			Message: "No kernels found matching identifiers",
			Class:   errs.ClassValidation,
		}
	}

	return nil
}
