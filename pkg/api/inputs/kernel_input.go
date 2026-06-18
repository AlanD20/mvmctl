package inputs
import (
	"context"
	"fmt"
	"strings"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
	"github.com/jmoiron/sqlx"
)
// KernelInput is the raw input for identifying existing kernels.
type KernelInput struct {
	Identifiers []string `json:"identifiers"`
	Force       bool     `json:"force"`
}
// ResolvedKernelInput specifies resolved kernel input.
type ResolvedKernelInput struct {
	Kernels []*model.KernelItem
	Force   bool
}
// KernelRequest specifies kernel request.
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
func (r *KernelRequest) Resolve(ctx context.Context) (*ResolvedKernelInput, error) {
	if len(r.input.Identifiers) == 0 {
		return nil, errs.NotFound(errs.CodeKernelNotFound, "No kernel identifiers provided")
	}
	// Validate identifier length — max 64 chars.
	for _, ident := range r.input.Identifiers {
		if len(ident) > 64 {
			return nil, errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("Kernel identifier too long: '%s' exceeds maximum length of 64 characters", ident),
			)
		}
	}
	result := r.resolver.ResolveMany(ctx, r.input.Identifiers)
	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, errs.NotFound(
			errs.CodeKernelNotFound,
			"Could not resolve any kernels: "+strings.Join(result.Errors, ", "),
		)
	}
	r.result = &ResolvedKernelInput{
		Kernels: result.Items,
		Force:   r.input.Force,
	}
	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}
	return r.result, nil
}
func (r *KernelRequest) ensureValidate() error {
	if r.result == nil {
		return errs.New(errs.CodeKernelNotFound, "Failed to resolve necessary dependencies to validate")
	}
	if len(r.result.Kernels) == 0 {
		return errs.NotFound(errs.CodeKernelNotFound, "No kernels found matching identifiers")
	}
	return nil
}
