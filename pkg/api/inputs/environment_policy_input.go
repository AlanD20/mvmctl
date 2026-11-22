package inputs

import (
	"mvmctl/pkg/errs"
	"regexp"
)

var environmentPolicyTokenPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

var environmentPolicyVMIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidateOwnedRemoval requires immutable endpoints so absent grants remain removable after VM deletion.
func (i *VMToVMExecPolicyCreateInput) ValidateOwnedRemoval() error {
	if err := i.Validate(); err != nil {
		return err
	}
	for _, vmID := range []string{i.SourceVM, i.DestinationVM} {
		if vmID != "" && !environmentPolicyVMIDPattern.MatchString(vmID) {
			return errs.New(errs.CodeValidationFailed, "owned execution policy removal requires full VM IDs")
		}
	}
	if i.SourceVM != "" && i.SourceVM == i.DestinationVM {
		return errs.New(errs.CodeValidationFailed, "owned execution policy removal requires distinct VMs")
	}
	return nil
}

// EnvironmentPolicyOwnershipInput binds retries to a durable workflow claim and observed incarnation.
// ClaimID is an idempotency token, never additional authorization.
type EnvironmentPolicyOwnershipInput struct {
	ClaimID     string `json:"claim_id" yaml:"claim_id"`
	Incarnation string `json:"incarnation,omitempty" yaml:"incarnation,omitempty"`
}

// Validate requires canonical random ownership tokens before policy effects.
func (i EnvironmentPolicyOwnershipInput) Validate() error {
	if !environmentPolicyTokenPattern.MatchString(i.ClaimID) ||
		(i.Incarnation != "" && !environmentPolicyTokenPattern.MatchString(i.Incarnation)) {
		return errs.New(errs.CodeValidationFailed, "environment policy ownership requires canonical UUIDv4 tokens")
	}
	return nil
}
