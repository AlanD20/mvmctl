package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// VMInput specifies VM input for identifying existing VMs.
type VMInput struct {
	Identifiers []string `json:"identifiers"`
	Force       bool     `json:"force"`
}

// Validate checks that the VM input has valid identifiers.
func (i *VMInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return fmt.Errorf("at least one VM identifier is required")
	}
	for _, identifier := range i.Identifiers {
		if validators.ValidMACRegex.MatchString(identifier) {
			if err := validators.MAC(identifier); err != nil {
				return errs.New(errs.CodeVMResolveFailed, fmt.Sprintf("Invalid MAC address: %s", identifier))
			}
		} else if validators.IsIPAddress(identifier) {
			if err := validators.IPv4Address(identifier, "guest IP", true, "", ""); err != nil {
				return errs.New(errs.CodeVMResolveFailed, fmt.Sprintf("Invalid guest IP: %s", identifier))
			}
		} else {
			// Name or ID — validate as entity name
			if err := validators.EntityName(identifier, "VM", 63); err != nil {
				return errs.New(errs.CodeVMResolveFailed, fmt.Sprintf("Invalid VM identifier: %s", identifier))
			}
		}
	}
	return nil
}

// Resolve resolves all identifiers in the input to VMItem objects.
// Delegates to vm.Resolver.ResolveMany for batch resolution with
// deduplication and error collection.
func (i *VMInput) Resolve(ctx context.Context, repo vm.Repository) ([]*model.VMItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := vm.NewResolver(repo)
	result := resolver.ResolveMany(ctx, i.Identifiers)
	if result == nil || (len(result.Errors) > 0 && len(result.VMs) == 0) {
		return nil, errs.NotFound(
			errs.CodeVMNotFound,
			fmt.Sprintf("Could not resolve any VMs: %s", strings.Join(result.Errors, ", ")),
		)
	}
	if len(result.Errors) > 0 {
		return result.VMs, fmt.Errorf("partial resolve failures: %s", strings.Join(result.Errors, "; "))
	}
	return result.VMs, nil
}
