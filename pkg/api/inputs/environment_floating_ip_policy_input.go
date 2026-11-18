package inputs

import (
	"mvmctl/pkg/errs"
	"regexp"
)

var environmentFloatingIPNetworkIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateOwnedRemoval requires immutable endpoints so cleanup does not resolve replacement resources.
func (input VMToFloatingIPTrafficPolicyCreateInput) ValidateOwnedRemoval() error {
	if err := input.Validate(); err != nil {
		return err
	}
	if !environmentPolicyVMIDPattern.MatchString(input.SourceVM) ||
		!environmentPolicyVMIDPattern.MatchString(input.DestinationFloatingIP) {
		return errs.New(errs.CodeValidationFailed, "owned floating IP policy removal requires full VM and resource IDs")
	}
	return nil
}

// ValidateOwnedRemoval requires immutable endpoints even after a source network is removed.
func (input NetworkToFloatingIPTrafficPolicyCreateInput) ValidateOwnedRemoval() error {
	if err := input.Validate(); err != nil {
		return err
	}
	if !environmentFloatingIPNetworkIDPattern.MatchString(input.SourceNetwork) ||
		!environmentPolicyVMIDPattern.MatchString(input.DestinationFloatingIP) {
		return errs.New(
			errs.CodeValidationFailed,
			"owned floating IP policy removal requires full network and resource IDs",
		)
	}
	return nil
}
