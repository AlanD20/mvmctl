package inputs

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"mvmctl/internal/core/execpolicy"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// NetworkToVMTrafficPolicyInput identifies network-source traffic policies by exact ID or unambiguous prefix.
type NetworkToVMTrafficPolicyInput struct {
	Identifiers []string `json:"identifiers"`
}

// Validate checks the network-source policy identifiers.
func (i *NetworkToVMTrafficPolicyInput) Validate() error {
	return validatePolicyIdentifiers(i.Identifiers)
}

// Resolve binds identifiers only within the network-source traffic-policy family.
func (i *NetworkToVMTrafficPolicyInput) Resolve(
	ctx context.Context,
	repo network.TrafficPolicyRepository,
) ([]*model.NetworkToVMTrafficAllowIntent, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	intents := make([]*model.NetworkToVMTrafficAllowIntent, 0, len(i.Identifiers))
	seen := make(map[string]bool, len(i.Identifiers))
	for _, identifier := range i.Identifiers {
		matches, err := repo.FindNetworkToVMByPrefix(ctx, identifier)
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

// VMToVMTrafficPolicyInput identifies VM-source traffic policies by exact ID or unambiguous prefix.
type VMToVMTrafficPolicyInput struct {
	Identifiers []string `json:"identifiers"`
}

// Validate checks the VM-source policy identifiers.
func (i *VMToVMTrafficPolicyInput) Validate() error { return validatePolicyIdentifiers(i.Identifiers) }

// Resolve binds identifiers only within the VM-source traffic-policy family.
func (i *VMToVMTrafficPolicyInput) Resolve(
	ctx context.Context,
	repo network.TrafficPolicyRepository,
) ([]*model.VMToVMTrafficAllowIntent, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	intents := make([]*model.VMToVMTrafficAllowIntent, 0, len(i.Identifiers))
	seen := make(map[string]bool, len(i.Identifiers))
	for _, identifier := range i.Identifiers {
		matches, err := repo.FindVMToVMByPrefix(ctx, identifier)
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

// VMToVMExecPolicyInput identifies directional execution policies by exact ID or unambiguous prefix.
type VMToVMExecPolicyInput struct {
	Identifiers []string `json:"identifiers"`
}

// Validate checks the execution-policy identifiers.
func (i *VMToVMExecPolicyInput) Validate() error { return validatePolicyIdentifiers(i.Identifiers) }

// Resolve binds identifiers only within the VM-to-VM execution-policy family.
func (i *VMToVMExecPolicyInput) Resolve(
	ctx context.Context,
	repo execpolicy.Repository,
) ([]*model.VMToVMExecAllowIntent, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	intents := make([]*model.VMToVMExecAllowIntent, 0, len(i.Identifiers))
	seen := make(map[string]bool, len(i.Identifiers))
	for _, identifier := range i.Identifiers {
		matches, err := repo.FindByPrefix(ctx, identifier)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodePolicyNotFound, "failed to resolve execution policy", err)
		}
		if len(matches) == 0 {
			return nil, errs.NotFound(errs.CodePolicyNotFound, "execution policy not found",
				errs.WithEntity(identifier))
		}
		if len(matches) > 1 {
			return nil, errs.New(errs.CodePolicyAmbiguous,
				fmt.Sprintf("execution policy identifier %q is ambiguous", identifier))
		}
		if !seen[matches[0].ID] {
			intents = append(intents, matches[0])
			seen[matches[0].ID] = true
		}
	}
	return intents, nil
}

func validatePolicyIdentifiers(identifiers []string) error {
	if len(identifiers) == 0 {
		return errs.New(errs.CodeValidationFailed, "at least one policy identifier is required")
	}
	for _, identifier := range identifiers {
		if len(identifier) < 1 || len(identifier) > 64 {
			return errs.New(errs.CodeValidationFailed,
				"policy identifiers must be between 1 and 64 lowercase hexadecimal characters")
		}
		for index := range len(identifier) {
			if (identifier[index] < '0' || identifier[index] > '9') &&
				(identifier[index] < 'a' || identifier[index] > 'f') {
				return errs.New(errs.CodeValidationFailed,
					"policy identifiers must be between 1 and 64 lowercase hexadecimal characters")
			}
		}
	}
	return nil
}

// NetworkToVMTrafficPolicyCreateInput is the public input for one managed-network-to-VM IP traffic grant.
type NetworkToVMTrafficPolicyCreateInput struct {
	SourceNetwork   string `json:"source_network"   yaml:"source_network"`
	DestinationVM   string `json:"destination_vm"   yaml:"destination_vm"`
	Protocol        string `json:"protocol"         yaml:"protocol"`
	DestinationPort string `json:"destination_port" yaml:"destination_port"`
}

// Validate checks the closed network-to-VM traffic policy shape.
func (i *NetworkToVMTrafficPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceNetwork) == "" {
		return errs.New(errs.CodeValidationFailed, "source network is required")
	}
	if strings.TrimSpace(i.DestinationVM) == "" {
		return errs.New(errs.CodeValidationFailed, "destination VM is required")
	}
	_, _, _, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	return err
}

// ResolveSourceNetwork resolves only the network-domain source fact.
func (i *NetworkToVMTrafficPolicyCreateInput) ResolveSourceNetwork(
	ctx context.Context,
	repo network.Repository,
) (*model.NetworkItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return resolveOnePolicyNetwork(ctx, repo, i.SourceNetwork)
}

// ResolveDestinationVM resolves only the VM-domain destination fact.
func (i *NetworkToVMTrafficPolicyCreateInput) ResolveDestinationVM(
	ctx context.Context,
	repo vm.Repository,
) (*model.VMItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return resolveOnePolicyVM(ctx, repo, i.DestinationVM)
}

// ResolvedTraffic returns the validated protocol and destination port bounds.
func (i *NetworkToVMTrafficPolicyCreateInput) ResolvedTraffic() (model.TrafficProtocol, int, int, error) {
	if err := i.Validate(); err != nil {
		return "", 0, 0, err
	}
	return resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
}

// VMToVMTrafficPolicyCreateInput is the public input for one exact-VM-to-VM IP traffic grant.
type VMToVMTrafficPolicyCreateInput struct {
	SourceVM        string `json:"source_vm"        yaml:"source_vm"`
	DestinationVM   string `json:"destination_vm"   yaml:"destination_vm"`
	Protocol        string `json:"protocol"         yaml:"protocol"`
	DestinationPort string `json:"destination_port" yaml:"destination_port"`
}

// Validate checks the closed VM-to-VM traffic policy shape.
func (i *VMToVMTrafficPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceVM) == "" {
		return errs.New(errs.CodeValidationFailed, "source VM is required")
	}
	if strings.TrimSpace(i.DestinationVM) == "" {
		return errs.New(errs.CodeValidationFailed, "destination VM is required")
	}
	_, _, _, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	return err
}

// ResolveVMs resolves only the VM-domain source and destination facts.
func (i *VMToVMTrafficPolicyCreateInput) ResolveVMs(
	ctx context.Context,
	repo vm.Repository,
) (*model.VMItem, *model.VMItem, error) {
	if err := i.Validate(); err != nil {
		return nil, nil, err
	}
	source, err := resolveOnePolicyVM(ctx, repo, i.SourceVM)
	if err != nil {
		return nil, nil, err
	}
	destination, err := resolveOnePolicyVM(ctx, repo, i.DestinationVM)
	if err != nil {
		return nil, nil, err
	}
	if source.ID == destination.ID {
		return nil, nil, errs.New(errs.CodePolicyEndpointInvalid, "source and destination VM must differ")
	}
	return source, destination, nil
}

// ResolvedTraffic returns the validated protocol and destination port bounds.
func (i *VMToVMTrafficPolicyCreateInput) ResolvedTraffic() (model.TrafficProtocol, int, int, error) {
	if err := i.Validate(); err != nil {
		return "", 0, 0, err
	}
	return resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
}

// VMToVMExecPolicyCreateInput is the public input for one directional VM-to-VM vsock exec grant.
type VMToVMExecPolicyCreateInput struct {
	SourceVM      string `json:"source_vm"      yaml:"source_vm"`
	DestinationVM string `json:"destination_vm" yaml:"destination_vm"`
	TargetUser    string `json:"target_user"    yaml:"target_user"`
}

// Validate checks the closed VM-to-VM exec policy shape and rejects root execution.
func (i *VMToVMExecPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceVM) == "" {
		return errs.New(errs.CodeValidationFailed, "source VM is required")
	}
	if strings.TrimSpace(i.DestinationVM) == "" {
		return errs.New(errs.CodeValidationFailed, "destination VM is required")
	}
	if i.TargetUser == "root" {
		return errs.New(errs.CodePolicyExecUserInvalid, "VM-to-VM exec target user must not be root")
	}
	if len(i.TargetUser) > 32 {
		return errs.New(errs.CodePolicyExecUserInvalid, "VM-to-VM exec target user must be at most 32 bytes")
	}
	if err := validators.SSHUsername(i.TargetUser); err != nil {
		return errs.WrapMsg(errs.CodePolicyExecUserInvalid, "invalid VM-to-VM exec target user", err)
	}
	return nil
}

// ResolveVMs resolves only the VM-domain source and destination facts.
func (i *VMToVMExecPolicyCreateInput) ResolveVMs(
	ctx context.Context,
	repo vm.Repository,
) (*model.VMItem, *model.VMItem, error) {
	if err := i.Validate(); err != nil {
		return nil, nil, err
	}
	source, err := resolveOnePolicyVM(ctx, repo, i.SourceVM)
	if err != nil {
		return nil, nil, err
	}
	destination, err := resolveOnePolicyVM(ctx, repo, i.DestinationVM)
	if err != nil {
		return nil, nil, err
	}
	if source.ID == destination.ID {
		return nil, nil, errs.New(errs.CodePolicyEndpointInvalid, "source and destination VM must differ")
	}
	return source, destination, nil
}

func resolveCanonicalTrafficValues(protocolValue, destinationPort string) (model.TrafficProtocol, int, int, error) {
	protocol := model.TrafficProtocol(protocolValue)
	if protocol != model.TrafficProtocolTCP && protocol != model.TrafficProtocolUDP {
		return "", 0, 0, errs.New(errs.CodePolicyProtocolInvalid, "protocol must be canonical tcp or udp")
	}
	if destinationPort == "" || destinationPort != strings.TrimSpace(destinationPort) {
		return "", 0, 0, errs.New(
			errs.CodePolicyPortInvalid,
			"destination port must be a canonical port or bounded range",
		)
	}
	parts := strings.Split(destinationPort, "-")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
		return "", 0, 0, errs.New(
			errs.CodePolicyPortInvalid,
			"destination port must be a canonical port or bounded range",
		)
	}
	start, err := canonicalPolicyPort(parts[0])
	if err != nil {
		return "", 0, 0, err
	}
	end := start
	if len(parts) == 2 {
		end, err = canonicalPolicyPort(parts[1])
		if err != nil {
			return "", 0, 0, err
		}
		if end < start {
			return "", 0, 0, errs.New(
				errs.CodePolicyPortInvalid,
				"destination port range end must be greater than or equal to its start",
			)
		}
	}
	return protocol, start, end, nil
}

func canonicalPolicyPort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != value {
		return 0, errs.New(errs.CodePolicyPortInvalid, "destination port must be between 1 and 65535")
	}
	return port, nil
}

func resolveOnePolicyNetwork(
	ctx context.Context,
	repo network.Repository,
	identifier string,
) (*model.NetworkItem, error) {
	items, err := (&NetworkInput{Identifiers: []string{identifier}}).Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return items[0], nil
}

func resolveOnePolicyVM(ctx context.Context, repo vm.Repository, identifier string) (*model.VMItem, error) {
	items, err := (&VMInput{Identifiers: []string{identifier}}).Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return items[0], nil
}
