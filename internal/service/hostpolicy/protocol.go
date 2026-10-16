// Package hostpolicy observes only the fixed root-owned managed sudoers policy.
package hostpolicy

import "mvmctl/pkg/errs"

// ObserveAction names the read-only managed host policy dispatcher action.
const ObserveAction = "host-policy-observe"

// ObserveRequest accepts no caller-selected path, identity, policy, or operation.
type ObserveRequest struct{}

// ObserveResult distinguishes confirmed absence from an omitted observation.
type ObserveResult struct {
	Exists *bool `json:"exists"`
}

// Validate requires an explicit observation, including for confirmed absence.
func (result ObserveResult) Validate() error {
	if result.Exists == nil {
		return policyError("managed host policy observation is missing", nil)
	}
	return nil
}

func policyError(operation string, cause error) *errs.DomainError {
	return errs.WrapMsg(errs.CodePrivilegeSudoers, operation, cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity("/etc/sudoers.d/mvm"))
}
