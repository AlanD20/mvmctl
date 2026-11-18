package inputs

import (
	"encoding/hex"
	"strings"
	"time"

	"mvmctl/internal/lib/model"

	"mvmctl/pkg/errs"
)

// FloatingIPDelegationGrantInput selects stored permission and an explicit method-specific lease policy.
type FloatingIPDelegationGrantInput struct {
	Network  string `json:"network"`
	Resource string `json:"resource"`
	VM       string `json:"vm"`
	Method   string `json:"method"`
	Lease    string `json:"lease"`
}

func (input FloatingIPDelegationGrantInput) Validate() error {
	if err := (FloatingIPInput{Network: input.Network, Identifier: input.Resource}).Validate(); err != nil {
		return err
	}
	if input.VM == "" || strings.TrimSpace(input.VM) != input.VM || len(input.VM) > 64 {
		return errs.New(errs.CodeValidationFailed, "delegation requires a VM selector")
	}
	_, err := input.LeaseDuration()
	return err
}

// LeaseDuration admits no implicit duration and rejects lease arguments for untimed methods.
func (input FloatingIPDelegationGrantInput) LeaseDuration() (time.Duration, error) {
	switch input.Method {
	case "associate", "renew":
		value, err := time.ParseDuration(input.Lease)
		if err != nil || value <= 0 || value%time.Second != 0 {
			return 0, errs.New(errs.CodeValidationFailed,
				"associate and renew require --lease with a positive whole-second duration")
		}
		return value, nil
	case "inspect", "release":
		if input.Lease != "" {
			return 0, errs.New(errs.CodeValidationFailed, "inspect and release delegation forbid --lease")
		}
		return 0, nil
	default:
		return 0, errs.New(errs.CodeValidationFailed, "delegation method must be inspect, associate, renew or release")
	}
}

// FloatingIPDelegationInput identifies one immutable root grant within a selected network.
type FloatingIPDelegationInput struct {
	Network string `json:"network"`
	ID      string `json:"id"`
}

func (input FloatingIPDelegationInput) Validate() error {
	decoded, err := hex.DecodeString(input.ID)
	if err != nil || len(decoded) != 16 || strings.ToLower(input.ID) != input.ID {
		return errs.New(errs.CodeValidationFailed, "delegation requires a complete lowercase grant ID")
	}
	return nil
}

// FloatingIPDelegationEnsureInput supplies a durable creation identity, never a caller-authenticated incarnation.
type FloatingIPDelegationEnsureInput struct {
	FloatingIPDelegationGrantInput
	ID string `json:"id"`
}

func (input FloatingIPDelegationEnsureInput) Validate() error {
	if err := input.FloatingIPDelegationGrantInput.Validate(); err != nil {
		return err
	}
	if !environmentFloatingIPNetworkIDPattern.MatchString(input.Network) ||
		!environmentPolicyVMIDPattern.MatchString(
			input.Resource,
		) || !environmentPolicyVMIDPattern.MatchString(input.VM) {
		return errs.New(errs.CodeValidationFailed, "delegation ensure requires full network, resource and VM IDs")
	}
	return (FloatingIPDelegationInput{ID: input.ID}).Validate()
}

// FloatingIPDelegationRemoveOwnedInput carries an immutable root-observed receipt for comparison-only cleanup.
type FloatingIPDelegationRemoveOwnedInput struct {
	Grant model.FloatingIPDelegationItem `json:"grant"`
}

func (input FloatingIPDelegationRemoveOwnedInput) Validate() error {
	if err := (FloatingIPDelegationInput{ID: input.Grant.ID}).Validate(); err != nil {
		return err
	}
	decoded, err := hex.DecodeString(input.Grant.NetworkID)
	if err != nil || len(decoded) != 32 || strings.ToLower(input.Grant.NetworkID) != input.Grant.NetworkID {
		return errs.New(errs.CodeValidationFailed, "owned delegation requires a canonical network receipt")
	}
	return nil
}
