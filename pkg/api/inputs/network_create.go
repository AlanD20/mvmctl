package inputs

import (
	"context"

	"mvmctl/internal/core/network"
	libnet "mvmctl/internal/lib/network"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"

	"github.com/jmoiron/sqlx"
)

// NetworkCreateInput matches Python's NetworkCreateInput dataclass exactly.
//
//	@dataclass
//	class NetworkCreateInput:
//	    name: str
//	    subnet: str
//	    ipv4_gateway: str | None = None
//	    nat_enabled: bool = True
//	    nat_gateways: list[str] = field(default_factory=list)
//	    set_default: bool = False
type NetworkCreateInput struct {
	Name        string   `json:"name"                   yaml:"name"`
	Subnet      string   `json:"subnet"                 yaml:"subnet"`
	IPv4Gateway *string  `json:"ipv4_gateway,omitempty" yaml:"ipv4_gateway,omitempty"`
	NATEnabled  bool     `json:"nat_enabled"            yaml:"nat_enabled"`
	NATGateways []string `json:"nat_gateways,omitempty" yaml:"nat_gateways,omitempty"`
	SetDefault  bool     `json:"set_default"            yaml:"default"`
}

// ResolvedNetworkCreateRequest matches Python's ResolvedNetworkCreateRequest (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedNetworkCreateRequest:
//	    name: str
//	    subnet: str
//	    ipv4_gateway: str
//	    bridge: str
//	    nat_enabled: bool
//	    nat_gateways: list[str]
type ResolvedNetworkCreateRequest struct {
	Name        string
	Subnet      string
	IPv4Gateway string
	Bridge      string
	NATEnabled  bool
	NATGateways []string
}

// NetworkCreateRequest matches Python's NetworkCreateRequest.
//
// Resolve and validate network creation inputs.
// Takes NetworkCreateInput and resolves DB-backed defaults,
// validates subnet overlap and bridge conflicts, and produces
// a ResolvedNetworkCreateRequest suitable for network creation.
type NetworkCreateRequest struct {
	db          *sqlx.DB
	input       NetworkCreateInput
	result      *ResolvedNetworkCreateRequest
	networkRepo network.Repository
}

// NewNetworkCreateRequest creates a new NetworkCreateRequest.
func NewNetworkCreateRequest(
	inputs NetworkCreateInput,
	db *sqlx.DB,
	networkRepo network.Repository,
) *NetworkCreateRequest {
	return &NetworkCreateRequest{
		db:          db,
		input:       inputs,
		networkRepo: networkRepo,
	}
}

// Result returns the resolved request, or nil if resolve() has not been called.

// Resolve resolves all inputs to explicit values.
// Matches Python's NetworkCreateRequest.resolve().
func (r *NetworkCreateRequest) Resolve(ctx context.Context) (*ResolvedNetworkCreateRequest, error) {
	// NAT defaults to true (Python: nat_enabled: bool = True)
	natEnabled := r.input.NATEnabled

	// Resolve or compute gateway
	var ipv4Gateway string
	if r.input.IPv4Gateway != nil {
		ipv4Gateway = *r.input.IPv4Gateway
	} else {
		gw, err := libnet.ComputeIPv4Gateway(r.input.Subnet)
		if err != nil {
			return nil, errs.New(errs.CodeNetworkNotFound, "Failed to compute gateway: "+err.Error())
		}
		ipv4Gateway = gw
	}

	// Compute bridge name — Python: NetworkUtils.compute_bridge_name(self._inputs.name)
	bridge := network.ComputeBridgeName(r.input.Name)

	// Auto-detect NAT gateways when enabled but none specified
	natGateways := r.input.NATGateways
	if len(natGateways) == 0 && natEnabled {
		outbound := libnet.DetectOutboundInterface(ctx)
		if outbound != "" {
			natGateways = []string{outbound}
		} else {
			natEnabled = false
		}
	}

	_ = ctx // context used for future DB operations if needed

	r.result = &ResolvedNetworkCreateRequest{
		Name:        r.input.Name,
		Subnet:      r.input.Subnet,
		IPv4Gateway: ipv4Gateway,
		Bridge:      bridge,
		NATEnabled:  natEnabled,
		NATGateways: natGateways,
	}

	// Validate
	if err := r.ensureValidate(ctx); err != nil {
		return nil, err
	}

	return r.result, nil
}

func (r *NetworkCreateRequest) ensureValidate(ctx context.Context) error {
	if r.result == nil {
		return errs.New(errs.CodeNetworkNotFound, "failed to resolve necessary dependencies to validate")
	}

	// Validate name (no dots, lowercase only)
	if err := validators.NetworkName(r.result.Name); err != nil {
		return errs.New(errs.CodeValidationFailed, err.Error())
	}

	// Validate and normalize subnet
	if _, err := validators.Subnet(r.result.Subnet); err != nil {
		return errs.New(errs.CodeValidationFailed, err.Error())
	}

	// Validate gateway is in subnet
	if _, err := validators.IPv4Gateway(r.result.IPv4Gateway, r.result.Subnet); err != nil {
		return errs.New(errs.CodeValidationFailed, err.Error())
	}

	// Validate bridge name
	if err := validators.BridgeName(ctx, r.result.Bridge); err != nil {
		return errs.New(errs.CodeValidationFailed, err.Error())
	}

	// Validate NAT gateways
	if len(r.result.NATGateways) > 0 {
		if _, err := validators.NATGateways(ctx, r.result.NATGateways); err != nil {
			return errs.New(errs.CodeValidationFailed, err.Error())
		}
	}

	// Check if network already exists
	existing, err := r.networkRepo.GetByName(ctx, r.result.Name)
	if err != nil {
		return errs.New(errs.CodeDatabaseError, "failed to check existing networks: "+err.Error())
	}
	if existing != nil {
		return errs.AlreadyExists(errs.CodeNetworkAlreadyExists, "Network '"+r.result.Name+"' already exists")
	}

	// Validate no subnet overlap
	existingNetworks, err := r.networkRepo.ListAll(ctx)
	if err != nil {
		return errs.New(errs.CodeDatabaseError, "failed to list existing networks: "+err.Error())
	}
	subnets := make([]string, 0, len(existingNetworks))
	for _, n := range existingNetworks {
		subnets = append(subnets, n.Subnet)
	}
	if err := validators.SubnetNoOverlap(r.result.Subnet, subnets); err != nil {
		return errs.New(errs.CodeNetworkSubnetOverlap, err.Error(), errs.WithClass(errs.ClassConflict))
	}

	return nil
}
