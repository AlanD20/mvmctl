package inputs

import (
	"context"
	"net/netip"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// NetworkCreateInput specifies logical network intent; root selects any NAT uplink.
type NetworkCreateInput struct {
	Name        string  `json:"name" yaml:"name"`
	Subnet      string  `json:"subnet" yaml:"subnet"`
	IPv4Gateway *string `json:"ipv4_gateway,omitempty" yaml:"ipv4_gateway,omitempty"`
	NATEnabled  bool    `json:"nat_enabled" yaml:"nat_enabled"`
	SetDefault  bool    `json:"default" yaml:"default"`
}

// ResolvedNetworkCreateRequest contains canonical values and optional retryable desired state.
type ResolvedNetworkCreateRequest struct {
	Name        string
	Subnet      string
	IPv4Gateway string
	NATEnabled  bool
	Pending     *model.NetworkItem
}

// Validate requires canonical private IPv4 intent with a usable gateway.
func (input *NetworkCreateInput) Validate() error {
	if err := validators.NetworkName(input.Name); err != nil {
		return errs.Wrap(errs.CodeValidationFailed, err)
	}
	prefix, err := netip.ParsePrefix(input.Subnet)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.String() != input.Subnet || prefix.Bits() > 30 {
		return errs.New(errs.CodeValidationFailed, "network subnet must be a canonical masked IPv4 prefix no longer than /30")
	}
	private := false
	for _, block := range []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16")} {
		if block.Bits() <= prefix.Bits() && block.Contains(prefix.Addr()) {
			private = true
		}
	}
	if !private {
		return errs.New(errs.CodeValidationFailed, "network subnet must be wholly contained in RFC1918 space")
	}
	if input.IPv4Gateway != nil {
		gateway, err := netip.ParseAddr(*input.IPv4Gateway)
		last := prefix.Addr().As4()
		for bit := prefix.Bits(); bit < 32; bit++ {
			last[bit/8] |= 1 << (7 - bit%8)
		}
		if err != nil || gateway.String() != *input.IPv4Gateway || !gateway.Is4() || !prefix.Contains(gateway) ||
			gateway == prefix.Addr() || gateway == netip.AddrFrom4(last) {
			return errs.New(errs.CodeValidationFailed, "network gateway must be a canonical usable address in the subnet")
		}
	}
	return nil
}

// Resolve applies the fixed first-usable gateway default and checks desired-state conflicts.
func (input *NetworkCreateInput) Resolve(ctx context.Context, repo network.Repository) (*ResolvedNetworkCreateRequest, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	prefix, err := netip.ParsePrefix(input.Subnet)
	if err != nil {
		return nil, errs.Wrap(errs.CodeValidationFailed, err)
	}
	gateway := prefix.Addr().Next().String()
	if input.IPv4Gateway != nil {
		gateway = *input.IPv4Gateway
	}
	result := &ResolvedNetworkCreateRequest{Name: input.Name, Subnet: input.Subnet, IPv4Gateway: gateway, NATEnabled: input.NATEnabled}
	existing, err := repo.GetByName(ctx, input.Name, true)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, "resolve existing network", err)
	}
	if existing != nil {
		if existing.DeletedAt != nil || existing.IsPresent || existing.BridgeActive || existing.Subnet != input.Subnet ||
			existing.IPv4Gateway != gateway || existing.NATEnabled != input.NATEnabled {
			return nil, errs.AlreadyExists(errs.CodeNetworkAlreadyExists, input.Name)
		}
		result.Pending = existing
	}
	existingNetworks, err := repo.ListAll(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, "list existing networks", err)
	}
	for _, existing := range existingNetworks {
		if result.Pending != nil && existing.ID == result.Pending.ID {
			continue
		}
		other, err := netip.ParsePrefix(existing.Subnet)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeValidationFailed, "existing network subnet is invalid", err)
		}
		if prefix.Overlaps(other) {
			return nil, errs.New(errs.CodeNetworkSubnetOverlap, "network subnet overlaps an existing network", errs.WithClass(errs.ClassConflict))
		}
	}
	return result, nil
}
