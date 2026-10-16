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

// ResolveRaw resolves the complete input and rejects unsupported data-volume formats.
// Partial resolution must not allow a VM to launch with missing drives.
func (i *VolumeInput) ResolveRaw(ctx context.Context, repo volume.Repository) ([]*model.VolumeItem, error) {
	volumes, err := i.Resolve(ctx, repo)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeVolumeNotFound, "failed to resolve data volumes", err)
	}
	for _, vol := range volumes {
		if vol.Format != model.VolumeFormatRaw {
			return nil, errs.New(errs.CodeValidationFailed,
				fmt.Sprintf("volume '%s' has unsupported data-volume format %q; only raw is supported",
					vol.Name, vol.Format),
				errs.WithClass(errs.ClassValidation))
		}
	}
	return volumes, nil
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
