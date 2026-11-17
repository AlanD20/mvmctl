package inputs

import (
	"context"
	"net/netip"
	"strings"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// FloatingIPInput identifies a resource within a selected or default managed network.
type FloatingIPInput struct {
	Network    string `json:"network"`
	Identifier string `json:"identifier"`
}

// Validate requires a bounded nonempty selector for single-resource operations.
func (input FloatingIPInput) Validate() error {
	if strings.TrimSpace(input.Identifier) != input.Identifier || input.Identifier == "" || len(input.Identifier) > 63 {
		return errs.New(errs.CodeValidationFailed, "floating IP requires a name, ID, ID prefix or IPv4 address")
	}
	return nil
}

// FloatingIPCreateInput describes a logical reservation. Empty IPv4 selects automatic allocation.
type FloatingIPCreateInput struct {
	Network string `json:"network"`
	Name    string `json:"name"`
	IPv4    string `json:"ipv4"`
}

func (input FloatingIPCreateInput) Validate() error {
	if err := validators.EntityName(input.Name, "floating IP", 63); err != nil {
		return err
	}
	if input.IPv4 != "" {
		address, err := netip.ParseAddr(input.IPv4)
		if err != nil || !address.Is4() || address.String() != input.IPv4 {
			return errs.New(errs.CodeValidationFailed, "floating-IP address must be canonical IPv4")
		}
	}
	return nil
}

func (input FloatingIPCreateInput) Resolve(ctx context.Context, repo network.Repository) (*model.NetworkItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	item, err := (FloatingIPNetworkInput{Network: input.Network}).Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	if input.IPv4 != "" {
		prefix, prefixErr := netip.ParsePrefix(item.Subnet)
		address, addressErr := netip.ParseAddr(input.IPv4)
		if prefixErr != nil || addressErr != nil || !prefix.Addr().Is4() || !prefix.Contains(address) ||
			address == prefix.Masked().Addr() || !prefix.Contains(address.Next()) || input.IPv4 == item.IPv4Gateway {
			return nil, errs.New(
				errs.CodeValidationFailed,
				"floating IP must be a usable address in its network and differ from the gateway",
			)
		}
	}
	return item, nil
}

// FloatingIPNetworkInput resolves a network without trusting its cache projection as root authority.
type FloatingIPNetworkInput struct {
	Network string `json:"network"`
}

func (input FloatingIPNetworkInput) Resolve(ctx context.Context, repo network.Repository) (*model.NetworkItem, error) {
	if input.Network == "" {
		item, err := repo.GetDefault(ctx)
		if err != nil {
			return nil, err
		}
		if item == nil {
			return nil, errs.New(errs.CodeNetworkNotFound, "no default network; specify --network")
		}
		return item, nil
	}
	return network.NewResolver(repo, nil).Resolve(ctx, input.Network)
}
