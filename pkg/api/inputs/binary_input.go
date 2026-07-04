package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/binary"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// BinaryInput specifies binary input.
type BinaryInput struct {
	Identifiers    []string `json:"identifiers,omitempty"`
	Version        *string  `json:"version,omitempty"`
	IncludeDeleted bool     `json:"include_deleted"`
}

// Validate checks that the binary input has valid identifiers.
func (i *BinaryInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return fmt.Errorf("at least one binary identifier is required")
	}
	for _, ident := range i.Identifiers {
		if len(ident) > 64 {
			return fmt.Errorf("binary identifier too long: %q exceeds maximum length of 64 characters", ident)
		}
	}
	return nil
}

// Resolve resolves all identifiers in the input to BinaryItem objects.
// Delegates to binary.Resolver.ResolveMany for batch resolution with
// deduplication and error collection.
func (i *BinaryInput) Resolve(ctx context.Context, repo binary.Repository) ([]*model.BinaryItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := binary.NewResolver(repo)
	result := resolver.ResolveMany(ctx, i.Identifiers, i.IncludeDeleted)
	if result == nil || len(result.Items) == 0 {
		return nil, errs.NotFound(errs.CodeBinaryNotFound, "No binary identifiers provided or could be resolved")
	}
	if len(result.Errors) > 0 {
		return result.Items, fmt.Errorf("partial resolve failures: %s", result.Errors)
	}
	return result.Items, nil
}
