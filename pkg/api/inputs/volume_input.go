package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// VolumeInput specifies volume input.
type VolumeInput struct {
	Identifiers  []string `json:"identifiers,omitempty"`
	VMIdentifier string   `json:"vm_identifier,omitempty"`
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

// ResolveVM resolves the VMIdentifier to a single VMItem.
// Delegates to vm.Resolver.Resolve which tries name, IP, MAC, or ID prefix.
func (i *VolumeInput) ResolveVM(ctx context.Context, repo vm.Repository) (*model.VMItem, error) {
	if i.VMIdentifier == "" {
		return nil, fmt.Errorf("VM identifier is required")
	}
	resolver := vm.NewResolver(repo)
	vmItem, err := resolver.Resolve(ctx, i.VMIdentifier)
	if err != nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, fmt.Sprintf("VM not found: %v", err))
	}
	return vmItem, nil
}
