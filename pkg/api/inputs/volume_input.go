package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/lib/model"
)

// VolumeInput specifies volume input.
type VolumeInput struct {
	Identifiers []string `json:"identifiers,omitempty"`
}

// Validate checks that the volume input has valid identifiers.
func (i *VolumeInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return fmt.Errorf("at least one volume identifier is required")
	}
	return nil
}

// Resolve resolves all identifiers in the input to VolumeItem objects.
// Delegates to volume.Resolver.ResolveMany for batch resolution with
// deduplication and error collection.
func (i *VolumeInput) Resolve(ctx context.Context, repo volume.Repository) ([]*model.VolumeItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := volume.NewResolver(repo)
	result := resolver.ResolveMany(ctx, i.Identifiers)
	if len(result.Errors) > 0 && len(result.Volumes) == 0 {
		return nil, fmt.Errorf("failed to resolve volumes: %s", strings.Join(result.Errors, "; "))
	}
	if len(result.Errors) > 0 {
		return result.Volumes, fmt.Errorf("partial resolve failures: %s", strings.Join(result.Errors, "; "))
	}
	return result.Volumes, nil
}
