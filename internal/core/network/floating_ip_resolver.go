package network

import (
	"context"
	"strings"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// FloatingIPInventory supplies root-observed network resources for pure name resolution.
type FloatingIPInventory interface {
	ListFloatingIPs(context.Context, string) ([]*model.FloatingIPItem, error)
}

// FloatingIPResolver resolves names, IDs and addresses without granting network authority.
type FloatingIPResolver struct{ inventory FloatingIPInventory }

func NewFloatingIPResolver(inventory FloatingIPInventory) *FloatingIPResolver {
	return &FloatingIPResolver{inventory: inventory}
}

// Resolve prefers a full ID, then an exact name, then an unambiguous ID prefix or address.
func (resolver *FloatingIPResolver) Resolve(
	ctx context.Context,
	networkID, identifier string,
) (*model.FloatingIPItem, error) {
	items, err := resolver.inventory.ListFloatingIPs(ctx, networkID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == identifier {
			return item, nil
		}
	}
	for _, item := range items {
		if item.Name == identifier {
			return item, nil
		}
	}
	var found *model.FloatingIPItem
	for _, item := range items {
		if item.IPv4 == identifier || strings.HasPrefix(item.ID, identifier) {
			if found != nil {
				return nil, errs.New(
					errs.CodeValidationFailed,
					"floating-IP selector is ambiguous; use its full name or ID",
				)
			}
			found = item
		}
	}
	if found == nil {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, identifier)
	}
	return found, nil
}
