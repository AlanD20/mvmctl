package jailer

import (
	"context"
	"maps"
	"os"

	"mvmctl/internal/lib/validators"
	"mvmctl/internal/service/firewall"
)

const (
	VMExecPolicyApplyAction  = "exec-policy-apply"
	VMExecPolicyRemoveAction = "exec-policy-remove"
	VMExecPolicyListAction   = "exec-policy-list"
)

type VMExecPolicyApplyRequest struct {
	CacheRoot   string                    `json:"cache_root"`
	Source      firewall.VMPolicyEndpoint `json:"source"`
	Destination firewall.VMPolicyEndpoint `json:"destination"`
	TargetUser  string                    `json:"target_user"`
	ClaimID     string                    `json:"claim_id,omitempty"`
	Incarnation string                    `json:"incarnation,omitempty"`
}

func (request VMExecPolicyApplyRequest) Validate() error {
	if _, err := parseManagedCacheLocator(request.CacheRoot); err != nil {
		return err
	}
	if request.TargetUser == "root" || len(request.TargetUser) > 32 || validators.SSHUsername(request.TargetUser) != nil {
		return instanceValidationError("execution policy requires a non-root target user")
	}
	if request.Source.VMID != "" && request.Source.VMID == request.Destination.VMID {
		return instanceValidationError("execution policy requires distinct VMs")
	}
	for _, endpoint := range []firewall.VMPolicyEndpoint{request.Source, request.Destination} {
		if endpoint.VMID == "" {
			if err := endpoint.Labels.Validate(); err != nil {
				return err
			}
		} else if endpoint.Labels != nil || !vmIDPattern.MatchString(endpoint.VMID) {
			return instanceValidationError("execution policy exact endpoints require canonical VM IDs")
		}
	}
	return nil
}

type VMExecPolicyApplyResult struct {
	Policy     firewall.ExecPolicyIntent `json:"policy"`
	WasCreated bool                      `json:"was_created"`
}

func ApplyVMExecPolicy(ctx context.Context, request VMExecPolicyApplyRequest) (VMExecPolicyApplyResult, error) {
	if err := request.Validate(); err != nil {
		return VMExecPolicyApplyResult{}, err
	}
	return exchangeTrustedRelease(ctx, NewTrustedReleaseClient(), VMExecPolicyApplyAction, request, nil, 0,
		func(result VMExecPolicyApplyResult) error {
			policy := result.Policy
			if policy.OwnerUID != uint32(os.Getuid()) || !validateExecPolicyID(policy.ID) ||
				policy.Incarnation == "" || policy.TargetUser != request.TargetUser ||
				policy.Source.VMID != request.Source.VMID || !maps.Equal(policy.Source.Labels, request.Source.Labels) ||
				policy.Destination.VMID != request.Destination.VMID ||
				!maps.Equal(policy.Destination.Labels, request.Destination.Labels) ||
				(result.WasCreated && (request.ClaimID == "" || policy.ClaimID != request.ClaimID)) {
				return instanceValidationError("execution policy receipt differs from its request")
			}
			return nil
		})
}

type VMExecPolicyRemoveRequest struct {
	PolicyID    string `json:"policy_id"`
	ClaimID     string `json:"claim_id,omitempty"`
	Incarnation string `json:"incarnation,omitempty"`
}

type VMExecPolicyRemoveResult struct {
	PolicyID string `json:"policy_id"`
}

func (request VMExecPolicyRemoveRequest) Validate() error {
	if !validateExecPolicyID(request.PolicyID) {
		return instanceValidationError("execution policy removal requires an exact policy ID")
	}
	return nil
}

func RemoveVMExecPolicy(ctx context.Context, request VMExecPolicyRemoveRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	_, err := exchangeTrustedRelease(ctx, NewTrustedReleaseClient(), VMExecPolicyRemoveAction, request, nil, 0,
		func(result VMExecPolicyRemoveResult) error {
			if result.PolicyID != request.PolicyID {
				return instanceValidationError("execution policy removal receipt differs")
			}
			return nil
		})
	return err
}

type VMExecPolicyListRequest struct{}

type VMExecPolicyListResult struct {
	Policies []firewall.ExecPolicyIntent `json:"policies"`
}

func ListVMExecPolicies(ctx context.Context) ([]firewall.ExecPolicyIntent, error) {
	result, err := exchangeTrustedRelease(
		ctx, NewTrustedReleaseClient(), VMExecPolicyListAction, VMExecPolicyListRequest{}, nil, 0,
		func(result VMExecPolicyListResult) error {
			for _, policy := range result.Policies {
				if policy.OwnerUID != uint32(os.Getuid()) || !validateExecPolicyID(policy.ID) {
					return instanceValidationError("execution policy listing differs from its owner")
				}
			}
			return nil
		},
	)
	return result.Policies, err
}

func validateExecPolicyID(id string) bool {
	return len(id) == 64 && vmIDPattern.MatchString(id[:32]) && vmIDPattern.MatchString(id[32:])
}
