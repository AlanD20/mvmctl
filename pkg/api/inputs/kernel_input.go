package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
	"strings"
)

// KernelInput is the raw input for identifying existing kernels.
type KernelInput struct {
	Identifiers    []string `json:"identifiers"`
	Force          bool     `json:"force"`
	IncludeDeleted bool     `json:"include_deleted"`
}

// Validate checks that the kernel input has valid identifiers.
func (i *KernelInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return fmt.Errorf("at least one kernel identifier is required")
	}
	for _, ident := range i.Identifiers {
		if len(ident) > 64 {
			return fmt.Errorf("kernel identifier too long: %q exceeds maximum length of 64 characters", ident)
		}
	}
	return nil
}

// Resolve resolves all identifiers in the input to KernelItem objects.
// Delegates to kernel.Resolver.ResolveMany for batch resolution with
// deduplication and error collection.
func (i *KernelInput) Resolve(ctx context.Context, repo kernel.Repository) ([]*model.KernelItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := kernel.NewResolver(repo, nil)
	result := resolver.ResolveMany(ctx, i.Identifiers, i.IncludeDeleted)
	if len(result.Items) == 0 {
		return nil, errs.NotFound(errs.CodeKernelNotFound, "No kernels found matching identifiers")
	}
	if len(result.Errors) > 0 {
		return result.Items, fmt.Errorf("partial resolve failures: %s", strings.Join(result.Errors, "; "))
	}
	return result.Items, nil
}
