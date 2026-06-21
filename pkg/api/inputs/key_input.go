package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/key"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// KeyInput is the raw input for identifying existing SSH keys.
// struct behavior — identifiers are resolved
// by name or ID in a single pass (lumping both).
type KeyInput struct {
	Identifiers []string `json:"identifiers,omitempty"`
}

// Validate checks that the key input has valid identifiers.
func (i *KeyInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return fmt.Errorf("at least one key identifier is required")
	}
	return nil
}

// Resolve resolves all identifiers in the input to SSHKeyItem objects.
// Delegates to key.Resolver.ResolveMany for batch resolution with
// deduplication and error collection.
func (i *KeyInput) Resolve(ctx context.Context, repo key.Repository) ([]*model.SSHKeyItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := key.NewResolver(repo)
	result, err := resolver.ResolveMany(ctx, i.Identifiers)
	if err != nil {
		return nil, err
	}
	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, errs.NotFound(errs.CodeKeyNotFound,
			fmt.Sprintf("failed to resolve keys: %s", strings.Join(result.Errors, "; ")))
	}
	if len(result.Errors) > 0 {
		return result.Items, fmt.Errorf("partial resolve failures: %s", strings.Join(result.Errors, "; "))
	}
	return result.Items, nil
}
