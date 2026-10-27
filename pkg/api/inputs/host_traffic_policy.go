package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// NetworkToHostTrafficPolicyInput identifies network-source traffic policies by exact ID or unambiguous prefix.
type NetworkToHostTrafficPolicyInput struct {
	Identifiers []string `json:"identifiers"`
}

// Validate checks the network-source policy identifiers.
func (i *NetworkToHostTrafficPolicyInput) Validate() error {
	return validatePolicyIdentifiers(i.Identifiers)
}

// Resolve binds identifiers only within the network-source traffic-policy family.
func (i *NetworkToHostTrafficPolicyInput) Resolve(
	ctx context.Context,
	repo network.TrafficPolicyRepository,
) ([]*model.NetworkToHostTrafficAllowIntent, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	intents := make([]*model.NetworkToHostTrafficAllowIntent, 0, len(i.Identifiers))
	seen := make(map[string]bool, len(i.Identifiers))
	for _, identifier := range i.Identifiers {
		matches, err := repo.FindNetworkToHostByPrefix(ctx, identifier)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodePolicyNotFound, "failed to resolve network traffic policy", err)
		}
		if len(matches) == 0 {
			return nil, errs.NotFound(errs.CodePolicyNotFound, "network traffic policy not found",
				errs.WithEntity(identifier))
		}
		if len(matches) > 1 {
			return nil, errs.New(errs.CodePolicyAmbiguous,
				fmt.Sprintf("network traffic policy identifier %q is ambiguous", identifier))
		}
		if !seen[matches[0].ID] {
			intents = append(intents, matches[0])
			seen[matches[0].ID] = true
		}
	}
	return intents, nil
}

// VMToHostTrafficPolicyInput identifies VM-source traffic policies by exact ID or unambiguous prefix.
type VMToHostTrafficPolicyInput struct {
	Identifiers []string `json:"identifiers"`
}

// Validate checks the VM-source policy identifiers.
func (i *VMToHostTrafficPolicyInput) Validate() error {
	return validatePolicyIdentifiers(i.Identifiers)
}

// Resolve binds identifiers only within the VM-source traffic-policy family.
func (i *VMToHostTrafficPolicyInput) Resolve(
	ctx context.Context,
	repo network.TrafficPolicyRepository,
) ([]*model.VMToHostTrafficAllowIntent, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	intents := make([]*model.VMToHostTrafficAllowIntent, 0, len(i.Identifiers))
	seen := make(map[string]bool, len(i.Identifiers))
	for _, identifier := range i.Identifiers {
		matches, err := repo.FindVMToHostByPrefix(ctx, identifier)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodePolicyNotFound, "failed to resolve VM traffic policy", err)
		}
		if len(matches) == 0 {
			return nil, errs.NotFound(errs.CodePolicyNotFound, "VM traffic policy not found",
				errs.WithEntity(identifier))
		}
		if len(matches) > 1 {
			return nil, errs.New(errs.CodePolicyAmbiguous,
				fmt.Sprintf("VM traffic policy identifier %q is ambiguous", identifier))
		}
		if !seen[matches[0].ID] {
			intents = append(intents, matches[0])
			seen[matches[0].ID] = true
		}
	}
	return intents, nil
}

// VMToHostTrafficPolicyCreateInput grants explicit service ports on the source's managed gateway.
type VMToHostTrafficPolicyCreateInput struct {
	SourceVM        string `json:"source_vm" yaml:"source_vm"`
	Protocol        string `json:"protocol" yaml:"protocol"`
	DestinationPort string `json:"destination_port" yaml:"destination_port"`
}

// Validate requires an explicit source, protocol and bounded destination port selection.
func (i *VMToHostTrafficPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceVM) == "" {
		return errs.New(errs.CodeValidationFailed, "source vm is required")
	}
	_, _, _, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	return err
}

// ResolveSourceVM resolves only the source domain; root later admits ownership and gateway authority.
func (i *VMToHostTrafficPolicyCreateInput) ResolveSourceVM(
	ctx context.Context, repo vm.Repository,
) (*model.VMItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return resolveOnePolicyVM(ctx, repo, i.SourceVM)
}

// ResolvedTraffic returns explicit validated TCP/UDP destination port bounds.
func (i *VMToHostTrafficPolicyCreateInput) ResolvedTraffic() (model.TrafficProtocol, int, int, error) {
	if err := i.Validate(); err != nil {
		return "", 0, 0, err
	}
	return resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
}

// NetworkToHostTrafficPolicyCreateInput grants explicit service ports on the source's managed gateway.
type NetworkToHostTrafficPolicyCreateInput struct {
	SourceNetwork   string `json:"source_network" yaml:"source_network"`
	Protocol        string `json:"protocol" yaml:"protocol"`
	DestinationPort string `json:"destination_port" yaml:"destination_port"`
}

// Validate requires an explicit source, protocol and bounded destination port selection.
func (i *NetworkToHostTrafficPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceNetwork) == "" {
		return errs.New(errs.CodeValidationFailed, "source network is required")
	}
	_, _, _, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	return err
}

// ResolveSourceNetwork resolves only the source domain; root later admits ownership and gateway authority.
func (i *NetworkToHostTrafficPolicyCreateInput) ResolveSourceNetwork(
	ctx context.Context, repo network.Repository,
) (*model.NetworkItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return resolveOnePolicyNetwork(ctx, repo, i.SourceNetwork)
}

// ResolvedTraffic returns explicit validated TCP/UDP destination port bounds.
func (i *NetworkToHostTrafficPolicyCreateInput) ResolvedTraffic() (model.TrafficProtocol, int, int, error) {
	if err := i.Validate(); err != nil {
		return "", 0, 0, err
	}
	return resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
}
