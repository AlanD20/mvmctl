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

// VMToFloatingIPTrafficPolicyInput identifies VM-source traffic policies by exact ID or unambiguous prefix.
type VMToFloatingIPTrafficPolicyInput struct {
	Identifiers []string `json:"identifiers"`
}

// Validate checks the VM-source policy identifiers.
func (i *VMToFloatingIPTrafficPolicyInput) Validate() error {
	return validatePolicyIdentifiers(i.Identifiers)
}

// Resolve binds identifiers only within the VM-source traffic-policy family.
func (i *VMToFloatingIPTrafficPolicyInput) Resolve(
	ctx context.Context,
	repo network.TrafficPolicyRepository,
) ([]*model.VMToFloatingIPTrafficAllowIntent, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	intents := make([]*model.VMToFloatingIPTrafficAllowIntent, 0, len(i.Identifiers))
	seen := make(map[string]bool, len(i.Identifiers))
	for _, identifier := range i.Identifiers {
		matches, err := repo.FindVMToFloatingIPByPrefix(ctx, identifier)
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

// VMToFloatingIPTrafficPolicyCreateInput selects an immutable reservation destination.
type VMToFloatingIPTrafficPolicyCreateInput struct {
	SourceVM              string `json:"source_vm" yaml:"source_vm"`
	DestinationFloatingIP string `json:"destination_floating_ip" yaml:"destination_floating_ip"`
	DestinationNetwork    string `json:"destination_network" yaml:"destination_network"`
	Protocol              string `json:"protocol" yaml:"protocol"`
	DestinationPort       string `json:"destination_port" yaml:"destination_port"`
}

func (i *VMToFloatingIPTrafficPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceVM) == "" {
		return errs.New(errs.CodeValidationFailed, "source vm is required")
	}
	if err := (FloatingIPInput{
		Identifier: i.DestinationFloatingIP,
		Network:    i.DestinationNetwork,
	}).Validate(); err != nil {
		return err
	}
	_, _, _, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	return err
}
func (i *VMToFloatingIPTrafficPolicyCreateInput) ResolveSourceVM(
	ctx context.Context,
	repo vm.Repository,
) (*model.VMItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return resolveOnePolicyVM(ctx, repo, i.SourceVM)
}
func (i *VMToFloatingIPTrafficPolicyCreateInput) ResolvedTraffic() (model.TrafficProtocol, int, int, error) {
	if err := i.Validate(); err != nil {
		return "", 0, 0, err
	}
	return resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
}

// NetworkToFloatingIPTrafficPolicyInput identifies network-source traffic policies by exact ID or unambiguous prefix.
type NetworkToFloatingIPTrafficPolicyInput struct {
	Identifiers []string `json:"identifiers"`
}

// Validate checks the network-source policy identifiers.
func (i *NetworkToFloatingIPTrafficPolicyInput) Validate() error {
	return validatePolicyIdentifiers(i.Identifiers)
}

// Resolve binds identifiers only within the network-source traffic-policy family.
func (i *NetworkToFloatingIPTrafficPolicyInput) Resolve(
	ctx context.Context,
	repo network.TrafficPolicyRepository,
) ([]*model.NetworkToFloatingIPTrafficAllowIntent, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	intents := make([]*model.NetworkToFloatingIPTrafficAllowIntent, 0, len(i.Identifiers))
	seen := make(map[string]bool, len(i.Identifiers))
	for _, identifier := range i.Identifiers {
		matches, err := repo.FindNetworkToFloatingIPByPrefix(ctx, identifier)
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

// NetworkToFloatingIPTrafficPolicyCreateInput selects an immutable reservation destination.
type NetworkToFloatingIPTrafficPolicyCreateInput struct {
	SourceNetwork         string `json:"source_network" yaml:"source_network"`
	DestinationFloatingIP string `json:"destination_floating_ip" yaml:"destination_floating_ip"`
	DestinationNetwork    string `json:"destination_network" yaml:"destination_network"`
	Protocol              string `json:"protocol" yaml:"protocol"`
	DestinationPort       string `json:"destination_port" yaml:"destination_port"`
}

func (i *NetworkToFloatingIPTrafficPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceNetwork) == "" {
		return errs.New(errs.CodeValidationFailed, "source network is required")
	}
	if err := (FloatingIPInput{
		Identifier: i.DestinationFloatingIP,
		Network:    i.DestinationNetwork,
	}).Validate(); err != nil {
		return err
	}
	_, _, _, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	return err
}
func (i *NetworkToFloatingIPTrafficPolicyCreateInput) ResolveSourceNetwork(
	ctx context.Context,
	repo network.Repository,
) (*model.NetworkItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return resolveOnePolicyNetwork(ctx, repo, i.SourceNetwork)
}
func (i *NetworkToFloatingIPTrafficPolicyCreateInput) ResolvedTraffic() (model.TrafficProtocol, int, int, error) {
	if err := i.Validate(); err != nil {
		return "", 0, 0, err
	}
	return resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
}
