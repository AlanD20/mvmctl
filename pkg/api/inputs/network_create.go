package inputs

import (
	"context"
	"database/sql"

	"mvmctl/internal/core/network"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
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
	Name        string   `json:"name"`
	Subnet      string   `json:"subnet"`
	IPv4Gateway *string  `json:"ipv4_gateway,omitempty"`
	NATEnabled  bool     `json:"nat_enabled"`
	NATGateways []string `json:"nat_gateways,omitempty"`
	SetDefault  bool     `json:"set_default"`
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
	db          *sql.DB
	_input      NetworkCreateInput
	_result     *ResolvedNetworkCreateRequest
	networkRepo network.Repository
}

// NewNetworkCreateRequest creates a new NetworkCreateRequest.
func NewNetworkCreateRequest(inputs NetworkCreateInput, db *sql.DB, networkRepo network.Repository) *NetworkCreateRequest {
	return &NetworkCreateRequest{
		db:          db,
		_input:      inputs,
		networkRepo: networkRepo,
	}
}

// Result returns the resolved request, or nil if resolve() has not been called.
func (r *NetworkCreateRequest) Result() *ResolvedNetworkCreateRequest {
	return r._result
}

// Resolve resolves all inputs to explicit values.
// Matches Python's NetworkCreateRequest.resolve().
func (r *NetworkCreateRequest) Resolve(ctx context.Context) (*ResolvedNetworkCreateRequest, error) {
	// NAT defaults to true (Python: nat_enabled: bool = True)
	natEnabled := r._input.NATEnabled

	// Resolve or compute gateway
	var ipv4Gateway string
	if r._input.IPv4Gateway != nil {
		ipv4Gateway = *r._input.IPv4Gateway
	} else {
		gw, err := infra.ComputeIPv4Gateway(r._input.Subnet)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeNetworkNotFound,
				Op:      "network_create",
				Message: "Failed to compute gateway: " + err.Error(),
				Class:   errs.ClassValidation,
			}
		}
		ipv4Gateway = gw
	}

	// Compute bridge name — Python: NetworkUtils.compute_bridge_name(self._inputs.name)
	bridge := network.ComputeBridgeName(r._input.Name)

	// Auto-detect NAT gateways when enabled but none specified
	natGateways := r._input.NATGateways
	if len(natGateways) == 0 && natEnabled {
		outbound := infra.DetectOutboundInterface()
		if outbound != "" {
			natGateways = []string{outbound}
		} else {
			natEnabled = false
		}
	}

	_ = ctx // context used for future DB operations if needed

	r._result = &ResolvedNetworkCreateRequest{
		Name:        r._input.Name,
		Subnet:      r._input.Subnet,
		IPv4Gateway: ipv4Gateway,
		Bridge:      bridge,
		NATEnabled:  natEnabled,
		NATGateways: natGateways,
	}

	// Validate
	if err := r.ensureValidate(ctx); err != nil {
		return nil, err
	}

	return r._result, nil
}

func (r *NetworkCreateRequest) ensureValidate(ctx context.Context) error {
	if r._result == nil {
		return &errs.DomainError{
			Code:    errs.CodeNetworkNotFound,
			Op:      "network_create",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	validator := infra.NetworkValidator{}

	// Validate name (no dots, lowercase only)
	if err := validator.ValidateName(r._result.Name); err != nil {
		return &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "network_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}

	// Validate and normalize subnet
	if _, err := validator.ValidateSubnet(r._result.Subnet); err != nil {
		return &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "network_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}

	// Validate gateway is in subnet
	if _, err := validator.ValidateIPv4Gateway(r._result.IPv4Gateway, r._result.Subnet); err != nil {
		return &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "network_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}

	// Validate bridge name
	if err := validator.ValidateBridgeName(r._result.Bridge); err != nil {
		return &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "network_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}

	// Validate NAT gateways
	if len(r._result.NATGateways) > 0 {
		if _, err := validator.ValidateNATGateways(r._result.NATGateways); err != nil {
			return &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "network_create",
				Message: err.Error(),
				Class:   errs.ClassValidation,
			}
		}
	}

	// Check if network already exists
	existing, err := r.networkRepo.GetByName(ctx, r._result.Name)
	if err != nil {
		return &errs.DomainError{
			Code:    errs.CodeDatabaseError,
			Op:      "network_create",
			Message: "Failed to check existing networks: " + err.Error(),
			Class:   errs.ClassInternal,
		}
	}
	if existing != nil {
		return &errs.DomainError{
			Code:    errs.CodeNetworkAlreadyExists,
			Op:      "network_create",
			Message: "Network '" + r._result.Name + "' already exists",
			Class:   errs.ClassConflict,
		}
	}

	// Validate no subnet overlap
	existingNetworks, err := r.networkRepo.ListAll(ctx)
	if err != nil {
		return &errs.DomainError{
			Code:    errs.CodeDatabaseError,
			Op:      "network_create",
			Message: "Failed to list existing networks: " + err.Error(),
			Class:   errs.ClassInternal,
		}
	}
	existingNetworksGeneric := make([]interface{}, len(existingNetworks))
	for i, n := range existingNetworks {
		existingNetworksGeneric[i] = n
	}
	if err := validator.ValidateSubnetNoOverlap(r._result.Subnet, existingNetworksGeneric, r._result.Name); err != nil {
		return &errs.DomainError{
			Code:    errs.CodeNetworkSubnetOverlap,
			Op:      "network_create",
			Message: err.Error(),
			Class:   errs.ClassConflict,
		}
	}

	return nil
}
