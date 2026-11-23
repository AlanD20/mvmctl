package firewall

import (
	"encoding/json"
	"maps"
	"slices"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/validators"
)

type ExecPolicyIntent struct {
	ID          string           `json:"id"`
	OwnerUID    uint32           `json:"owner_uid"`
	Source      VMPolicyEndpoint `json:"source"`
	Destination VMPolicyEndpoint `json:"destination"`
	TargetUser  string           `json:"target_user"`
	ClaimID     string           `json:"claim_id"`
	Incarnation string           `json:"incarnation"`
}

// VMExecPolicyID derives the immutable identity used by workflow cleanup.
func VMExecPolicyID(ownerUID uint32, source, destination VMPolicyEndpoint, targetUser string) (string, error) {
	return (ExecPolicyIntent{
		OwnerUID: ownerUID, Source: source, Destination: destination, TargetUser: targetUser,
	}).identity()
}

func (policy ExecPolicyIntent) identity() (string, error) {
	canonical, err := json.Marshal(struct {
		OwnerUID    uint32
		Source      VMPolicyEndpoint
		Destination VMPolicyEndpoint
		TargetUser  string
	}{policy.OwnerUID, policy.Source, policy.Destination, policy.TargetUser})
	if err != nil {
		return "", err
	}
	return crypto.SHA256(append([]byte("mvmctl.exec-policy.v1\x00"), canonical...)), nil
}

func (policy ExecPolicyIntent) validate(records vmLabelRecords) error {
	if policy.OwnerUID == 0 || policy.Incarnation == "" || !validPolicyOwnership(policy.ClaimID, policy.Incarnation) {
		return invalidGeneration("invalid exec policy ownership")
	}
	if !validExecPolicyTargetUser(policy.TargetUser) {
		return invalidGeneration("exec policy requires a canonical non-root target user")
	}
	if policy.Source.VMID != "" && policy.Source.VMID == policy.Destination.VMID {
		return invalidGeneration("exec policy requires distinct VMs")
	}
	for _, endpoint := range []VMPolicyEndpoint{policy.Source, policy.Destination} {
		if err := validateLabelPolicyEndpoint(endpoint, policy.OwnerUID); err != nil {
			return err
		}
		if endpoint.VMID != "" {
			record, found := records[endpoint.VMID]
			if !found || record.OwnerUID != policy.OwnerUID || record.Retired {
				return invalidGeneration("exec policy references an unowned or retired VM")
			}
		}
	}
	expected, err := policy.identity()
	if err != nil {
		return err
	}
	if policy.ID != expected {
		return invalidGeneration("exec policy identity differs from its intent")
	}
	return nil
}

func validExecPolicyTargetUser(user string) bool {
	return user != "root" && len(user) <= execPolicyIndexUserSize && validators.SSHUsername(user) == nil
}

func validateExecPolicies(policies []ExecPolicyIntent, records vmLabelRecords) error {
	seen := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		if err := policy.validate(records); err != nil {
			return err
		}
		if _, exists := seen[policy.ID]; exists {
			return invalidGeneration("exec policy identity is duplicated")
		}
		seen[policy.ID] = struct{}{}
	}
	return nil
}

func cloneExecPolicies(policies []ExecPolicyIntent) []ExecPolicyIntent {
	cloned := slices.Clone(policies)
	for index := range cloned {
		cloned[index].Source.Labels = maps.Clone(cloned[index].Source.Labels)
		cloned[index].Destination.Labels = maps.Clone(cloned[index].Destination.Labels)
	}
	return cloned
}

func (intent Intent) hasExecPolicies(ownerUID uint32) bool {
	return slices.ContainsFunc(intent.ExecPolicies, func(policy ExecPolicyIntent) bool {
		return policy.OwnerUID == ownerUID
	})
}
