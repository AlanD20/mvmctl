package inputs

import "mvmctl/pkg/errs"

// EnvironmentFloatingIPOwnershipInput retains an exact reservation identity before creation.
// ResourceID is an idempotency token, not additional operator authority.
type EnvironmentFloatingIPOwnershipInput struct {
	ResourceID string `json:"resource_id" yaml:"resource_id"`
}

func (input EnvironmentFloatingIPOwnershipInput) Validate() error {
	if !environmentPolicyVMIDPattern.MatchString(input.ResourceID) {
		return errs.New(errs.CodeValidationFailed, "environment floating IP requires a full resource ID")
	}
	return nil
}
