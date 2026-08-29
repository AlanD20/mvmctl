package inputs

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// PolicyCreateInput is the raw input for a routed service-access policy.
type PolicyCreateInput struct {
	SourceNetwork   string `json:"source_network"`
	DestinationVM   string `json:"destination_vm"`
	Protocol        string `json:"protocol"`
	DestinationPort string `json:"destination_port"`
}

// Validate checks the typed policy shape and destination port bounds.
func (i *PolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceNetwork) == "" {
		return errs.New(errs.CodeValidationFailed, "source network is required")
	}
	if strings.TrimSpace(i.DestinationVM) == "" {
		return errs.New(errs.CodeValidationFailed, "destination VM is required")
	}
	protocol := model.ServiceAccessPolicyProtocol(strings.ToLower(i.Protocol))
	if protocol != model.ServiceAccessPolicyProtocolTCP && protocol != model.ServiceAccessPolicyProtocolUDP {
		return errs.New(errs.CodePolicyProtocolInvalid, "protocol must be tcp or udp")
	}
	_, _, err := parsePolicyPort(i.DestinationPort)
	return err
}

// ResolveSourceNetwork resolves the policy source network.
func (i *PolicyCreateInput) ResolveSourceNetwork(
	ctx context.Context,
	repo network.Repository,
) (*model.NetworkItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	networks, err := (&NetworkInput{Identifiers: []string{i.SourceNetwork}}).Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return networks[0], nil
}

// ResolveDestinationVM resolves the exact destination VM.
func (i *PolicyCreateInput) ResolveDestinationVM(
	ctx context.Context,
	repo vm.Repository,
) (*model.VMItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	vms, err := (&VMInput{Identifiers: []string{i.DestinationVM}}).Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return vms[0], nil
}

// ResolvedPort returns the validated typed protocol and bounded destination port range.
func (i *PolicyCreateInput) ResolvedPort() (model.ServiceAccessPolicyProtocol, int, int, error) {
	if err := i.Validate(); err != nil {
		return "", 0, 0, err
	}
	start, end, err := parsePolicyPort(i.DestinationPort)
	return model.ServiceAccessPolicyProtocol(strings.ToLower(i.Protocol)), start, end, err
}

func parsePolicyPort(value string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) == 0 || len(parts) > 2 || parts[0] == "" {
		return 0, 0, errs.New(errs.CodePolicyPortInvalid, "destination port must be a port or bounded range")
	}
	start, err := strconv.Atoi(parts[0])
	if err != nil || start < 1 || start > 65535 {
		return 0, 0, errs.New(errs.CodePolicyPortInvalid, "destination port must be between 1 and 65535")
	}
	end := start
	if len(parts) == 2 {
		end, err = strconv.Atoi(parts[1])
		if err != nil || end < start || end > 65535 {
			return 0, 0, errs.New(
				errs.CodePolicyPortInvalid,
				"destination port range end must be between its start and 65535",
			)
		}
	}
	return start, end, nil
}

// PolicyInput identifies existing policies by exact ID or ID prefix.
type PolicyInput struct {
	Identifiers []string `json:"identifiers"`
}

// Validate checks that at least one policy identifier was supplied.
func (i *PolicyInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return errs.New(errs.CodeValidationFailed, "at least one policy identifier is required")
	}
	for _, identifier := range i.Identifiers {
		if strings.TrimSpace(identifier) == "" {
			return errs.New(errs.CodeValidationFailed, "policy identifiers must not be empty")
		}
	}
	return nil
}

// Resolve resolves policy IDs and rejects missing or ambiguous prefixes.
func (i *PolicyInput) Resolve(
	ctx context.Context,
	repo network.ServiceAccessPolicyRepository,
) ([]*model.ServiceAccessPolicy, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	policies := make([]*model.ServiceAccessPolicy, 0, len(i.Identifiers))
	seen := make(map[string]bool)
	for _, identifier := range i.Identifiers {
		matches, err := repo.FindByPrefix(ctx, identifier)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodePolicyNotFound, "failed to resolve service-access policy", err)
		}
		if len(matches) == 0 {
			return nil, errs.NotFound(errs.CodePolicyNotFound, "service-access policy not found",
				errs.WithEntity(identifier))
		}
		if len(matches) > 1 {
			return nil, errs.New(errs.CodePolicyAmbiguous,
				fmt.Sprintf("service-access policy identifier %q is ambiguous", identifier))
		}
		if !seen[matches[0].ID] {
			policies = append(policies, matches[0])
			seen[matches[0].ID] = true
		}
	}
	return policies, nil
}
