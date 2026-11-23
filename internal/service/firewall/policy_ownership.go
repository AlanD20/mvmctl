package firewall

import (
	"mvmctl/pkg/errs"
	"regexp"
)

var policyOwnershipPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func validPolicyOwnership(claim, incarnation string) bool {
	return (incarnation == "" || policyOwnershipPattern.MatchString(incarnation)) &&
		(claim == "" || (incarnation != "" && policyOwnershipPattern.MatchString(claim)))
}

// PolicyReceipt is the durable grant instance observed under the mutation lease.
type PolicyReceipt struct {
	Incarnation string
	ClaimID     string
	WasCreated  bool
}

func policyOwnershipConflict() error {
	return errs.New(errs.CodePolicyAlreadyExists, "policy ownership changed or recorded incarnation is absent")
}
