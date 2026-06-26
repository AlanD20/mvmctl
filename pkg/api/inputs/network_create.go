package inputs

import (
	"context"
	"mvmctl/internal/core/network"
	libnet "mvmctl/internal/lib/network"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// NetworkCreateInput specifies network create input.
type NetworkCreateInput struct {
	Name        string   `json:"name"                   yaml:"name"`
	Subnet      string   `json:"subnet"                 yaml:"subnet"`
	IPv4Gateway *string  `json:"ipv4_gateway,omitempty" yaml:"ipv4_gateway,omitempty"`
	NATEnabled  bool     `json:"nat_enabled"            yaml:"nat_enabled"`
	NATGateways []string `json:"nat_gateways,omitempty" yaml:"nat_gateways,omitempty"`
	SetDefault  bool     `json:"default"                yaml:"default"`
}

// ResolvedNetworkCreateRequest specifies resolved network create request.
type ResolvedNetworkCreateRequest struct {
	Name        string
	Subnet      string
	IPv4Gateway string
	Bridge      string
	NATEnabled  bool
	NATGateways []string
}

// Validate checks that the network create input has required fields.
func (i *NetworkCreateInput) Validate() error {
	if i.Name == "" {
		return errs.New(errs.CodeValidationFailed, "Network name is required")
	}
	if i.Subnet == "" {
		return errs.New(errs.CodeValidationFailed, "Subnet is required")
	}
	return nil
}

// Resolve resolves and validates network creation inputs, returning
// a ResolvedNetworkCreateRequest suitable for network creation.
func (i *NetworkCreateInput) Resolve(
	ctx context.Context,
	repo network.Repository,
) (*ResolvedNetworkCreateRequest, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	// NAT defaults to true
	natEnabled := i.NATEnabled
	// Resolve or compute gateway
	var ipv4Gateway string
	if i.IPv4Gateway != nil {
		ipv4Gateway = *i.IPv4Gateway
	} else {
		gw, err := libnet.ComputeIPv4Gateway(i.Subnet)
		if err != nil {
			return nil, errs.New(errs.CodeNetworkNotFound, "Failed to compute gateway: "+err.Error())
		}
		ipv4Gateway = gw
	}
	// Compute bridge name from the input name.
	bridge := network.ComputeBridgeName(i.Name)
	// Auto-detect NAT gateways when enabled but none specified
	natGateways := i.NATGateways
	if len(natGateways) == 0 && natEnabled {
		outbound := libnet.DetectOutboundInterface(ctx)
		if outbound != "" {
			natGateways = []string{outbound}
		} else {
			natEnabled = false
		}
	}
	result := &ResolvedNetworkCreateRequest{
		Name:        i.Name,
		Subnet:      i.Subnet,
		IPv4Gateway: ipv4Gateway,
		Bridge:      bridge,
		NATEnabled:  natEnabled,
		NATGateways: natGateways,
	}
	// Validate name (no dots, lowercase only)
	if err := validators.NetworkName(result.Name); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, err.Error())
	}
	// Validate and normalize subnet
	if _, err := validators.Subnet(result.Subnet); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, err.Error())
	}
	// Validate gateway is in subnet
	if _, err := validators.IPv4Gateway(result.IPv4Gateway, result.Subnet); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, err.Error())
	}
	// Validate bridge name
	if err := validators.BridgeName(ctx, result.Bridge); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, err.Error())
	}
	// Validate NAT gateways
	if len(result.NATGateways) > 0 {
		if _, err := validators.NATGateways(ctx, result.NATGateways); err != nil {
			return nil, errs.New(errs.CodeValidationFailed, err.Error())
		}
	}
	// Check if network already exists
	existing, err := repo.GetByName(ctx, result.Name)
	if err != nil {
		return nil, errs.New(errs.CodeDatabaseError, "failed to check existing networks: "+err.Error())
	}
	if existing != nil {
		return nil, errs.AlreadyExists(errs.CodeNetworkAlreadyExists, "Network '"+result.Name+"' already exists")
	}
	// Validate no subnet overlap
	existingNetworks, err := repo.ListAll(ctx)
	if err != nil {
		return nil, errs.New(errs.CodeDatabaseError, "failed to list existing networks: "+err.Error())
	}
	subnets := make([]string, 0, len(existingNetworks))
	for _, n := range existingNetworks {
		subnets = append(subnets, n.Subnet)
	}
	if err := validators.SubnetNoOverlap(result.Subnet, subnets); err != nil {
		return nil, errs.New(errs.CodeNetworkSubnetOverlap, err.Error(), errs.WithClass(errs.ClassConflict))
	}
	return result, nil
}
