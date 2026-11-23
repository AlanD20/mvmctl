package network

import (
	"mvmctl/internal/service/firewall"
	"time"
)

type floatingIPPhase string
type floatingIPOperation string

const (
	floatingIPUnassociated floatingIPPhase = "unassociated"
	floatingIPOwned        floatingIPPhase = "owned"
	floatingIPFencing      floatingIPPhase = "fencing"
	floatingIPFenced       floatingIPPhase = "fenced"
	floatingIPGranting     floatingIPPhase = "granting"
	floatingIPRenewing     floatingIPPhase = "renewing"

	floatingIPAssociate floatingIPOperation = "associate"
	floatingIPRelease   floatingIPOperation = "release"
	floatingIPRenew     floatingIPOperation = "renew"
	floatingIPRecover   floatingIPOperation = "recover"
)

type floatingIPOwnerOrigin string

const (
	floatingIPOperatorOrigin  floatingIPOwnerOrigin = "operator"
	floatingIPDelegatedOrigin floatingIPOwnerOrigin = "delegated"
)

// floatingIPOwner is an exact incarnation and explicit address lifetime policy, never proof of a live kernel key.
// Value-only fields keep complete records comparable for descriptor-backed CAS and network cleanup.
type floatingIPOwner struct {
	Lifetime         firewall.FloatingIPLifetime `json:"lifetime"`
	Origin           floatingIPOwnerOrigin       `json:"origin"`
	GrantID          string                      `json:"grant_id"`
	GrantRevision    uint64                      `json:"grant_revision"`
	BootID           string                      `json:"boot_id"`
	VMID             string                      `json:"vm_id"`
	LaunchGeneration uint64                      `json:"launch_generation"`
	ExecutionUID     uint32                      `json:"execution_uid"`
	LeaseDuration    time.Duration               `json:"lease_duration_ns"`
}

func validFloatingIPOwner(owner floatingIPOwner, operatorUID uint32) bool {
	validOrigin := owner.Origin == floatingIPOperatorOrigin && owner.GrantID == "" && owner.GrantRevision == 0 &&
		owner.Lifetime == firewall.FloatingIPPersistent && owner.LeaseDuration == 0 ||
		owner.Origin == floatingIPDelegatedOrigin && vmIDPattern.MatchString(owner.GrantID) &&
			owner.GrantRevision == floatingIPDelegationActiveRevision && owner.Lifetime == firewall.FloatingIPFinite &&
			owner.LeaseDuration > 0 && owner.LeaseDuration%time.Second == 0
	return validOrigin && bootIDPattern.MatchString(owner.BootID) && vmIDPattern.MatchString(owner.VMID) &&
		owner.LaunchGeneration > 0 && owner.ExecutionUID != 0 && owner.ExecutionUID != operatorUID
}

func sameFloatingIPIncarnation(left, right floatingIPOwner) bool {
	return left.BootID == right.BootID && left.VMID == right.VMID &&
		left.LaunchGeneration == right.LaunchGeneration && left.ExecutionUID == right.ExecutionUID
}

func validateFloatingIPOwnership(record floatingIPRecord) error {
	zero := floatingIPOwner{}
	valid := func(owner floatingIPOwner) bool { return validFloatingIPOwner(owner, record.OwnerUID) }
	if record.Owner != zero && !valid(record.Owner) ||
		record.BeforeOwner != zero && !valid(record.BeforeOwner) ||
		record.TargetOwner != zero && !valid(record.TargetOwner) {
		return networkAuthorityCorruptError("floating-IP incarnation or lifetime policy is invalid", nil)
	}
	if record.Retired && record.Phase != floatingIPUnassociated {
		return networkAuthorityCorruptError("retired floating IP retains ownership or an unfinished transition", nil)
	}
	switch record.Phase {
	case floatingIPUnassociated:
		if record.Owner == zero && record.BeforeOwner == zero && record.TargetOwner == zero && record.Operation == "" {
			return nil
		}
	case floatingIPOwned:
		if valid(record.Owner) && record.BeforeOwner == zero && record.TargetOwner == zero && record.Operation == "" {
			return nil
		}
	case floatingIPFencing, floatingIPFenced, floatingIPGranting:
		if record.Owner != zero {
			break
		}
		switch record.Operation {
		case floatingIPAssociate:
			if valid(record.TargetOwner) && (record.BeforeOwner == zero ||
				!sameFloatingIPIncarnation(record.BeforeOwner, record.TargetOwner)) {
				return nil
			}
		case floatingIPRelease:
			if record.Phase != floatingIPGranting && valid(record.BeforeOwner) && record.TargetOwner == zero {
				return nil
			}
		case floatingIPRecover:
			if record.Phase != floatingIPGranting && (valid(record.BeforeOwner) || valid(record.TargetOwner)) {
				return nil
			}
		}
	case floatingIPRenewing:
		if record.Owner == zero && record.Operation == floatingIPRenew && valid(record.BeforeOwner) &&
			sameFloatingIPIncarnation(record.BeforeOwner, record.TargetOwner) &&
			record.BeforeOwner.LeaseDuration == record.TargetOwner.LeaseDuration &&
			record.BeforeOwner.Lifetime == firewall.FloatingIPFinite && record.TargetOwner.Lifetime == firewall.FloatingIPFinite {
			return nil
		}
	}
	return networkAuthorityCorruptError("floating-IP phase and ownership intent disagree", nil)
}

// validateFloatingIPReplacement defines the closed journal transitions. Each publication advances Revision;
// ownership intent advances Generation once, while renewal and subsequent phase writes retain it.
// fencing -> fenced acknowledges completed packet revocation AND exact flow/neighbor cleanup. Only a live
// coordinator may acknowledge granting/renewing -> owned after fresh authorization and verified timed install.
// Journal state never authorizes activation or timeout replay. Recovery preserves possibly installed targets
// until fencing completes, and its only terminal outcome is unassociated. No runtime coordinator exists here.
func validateFloatingIPReplacement(next floatingIPRecord, previous *floatingIPRecord) error {
	if err := validateFloatingIPRecord(next); err != nil {
		return err
	}
	if previous == nil {
		if next.Revision == 1 && next.Generation == 1 && !next.Retired && next.Phase == floatingIPUnassociated {
			return nil
		}
		return networkAuthorityCorruptError("new floating-IP reservation requires initial unassociated authority", nil)
	}
	before := *previous
	if err := validateFloatingIPRecord(before); err != nil {
		return err
	}
	if before.Retired || before.Revision == ^uint64(0) || next.Revision != before.Revision+1 ||
		next.Schema != before.Schema || next.OwnerUID != before.OwnerUID || next.ID != before.ID ||
		next.NetworkID != before.NetworkID || next.Name != before.Name || next.IPv4 != before.IPv4 {
		return networkAuthorityCorruptError("floating-IP replacement changes immutable authority or revision", nil)
	}
	// Compare all fields after changing only the explicitly allowed delta below.
	expected := before
	expected.Revision++
	advanceGeneration := func() bool {
		if expected.Generation == ^uint64(0) {
			return false
		}
		expected.Generation++
		return true
	}
	stable := func(phase floatingIPPhase, owner floatingIPOwner) {
		expected.Phase, expected.Operation = phase, ""
		expected.Owner, expected.BeforeOwner, expected.TargetOwner = owner, floatingIPOwner{}, floatingIPOwner{}
	}
	allowed := false
	switch before.Phase {
	case floatingIPUnassociated:
		if next.Retired {
			expected.Retired = true
			allowed = advanceGeneration()
		} else if next.Phase == floatingIPFencing && next.Operation == floatingIPAssociate {
			expected.Phase, expected.Operation, expected.TargetOwner = next.Phase, next.Operation, next.TargetOwner
			allowed = advanceGeneration()
		}
	case floatingIPOwned:
		expected.Owner, expected.BeforeOwner = floatingIPOwner{}, before.Owner
		expected.Phase, expected.Operation = next.Phase, next.Operation
		if next.Phase == floatingIPRenewing && next.Operation == floatingIPRenew {
			expected.TargetOwner = next.TargetOwner
			allowed = sameFloatingIPIncarnation(before.Owner, next.TargetOwner) &&
				before.Owner.LeaseDuration == next.TargetOwner.LeaseDuration &&
				before.Owner.Lifetime == firewall.FloatingIPFinite && next.TargetOwner.Lifetime == firewall.FloatingIPFinite
		} else if next.Phase == floatingIPFencing {
			switch next.Operation {
			case floatingIPAssociate:
				expected.TargetOwner = next.TargetOwner
				allowed = advanceGeneration()
			case floatingIPRelease, floatingIPRecover:
				allowed = advanceGeneration()
			}
		}
	case floatingIPFencing:
		if next.Phase == floatingIPFenced {
			expected.Phase = floatingIPFenced
			allowed = true
		}
	case floatingIPFenced:
		if next.Phase == floatingIPGranting && before.Operation == floatingIPAssociate {
			expected.Phase = floatingIPGranting
			allowed = true
		} else if next.Phase == floatingIPUnassociated {
			stable(floatingIPUnassociated, floatingIPOwner{})
			allowed = true
		}
	case floatingIPGranting, floatingIPRenewing:
		if next.Phase == floatingIPOwned {
			stable(floatingIPOwned, before.TargetOwner)
			allowed = true
		}
	}
	// A fresh invocation must take this named recovery path instead of resuming a timed install.
	// Preserve BOTH history fields: a granting target may already own a kernel element.
	if next.Phase == floatingIPFencing && next.Operation == floatingIPRecover &&
		before.Operation != floatingIPRecover && (before.Phase == floatingIPFencing ||
		before.Phase == floatingIPFenced || before.Phase == floatingIPGranting || before.Phase == floatingIPRenewing) {
		expected = before
		expected.Revision++
		expected.Phase, expected.Operation = floatingIPFencing, floatingIPRecover
		allowed = true
		if before.Phase == floatingIPRenewing {
			allowed = advanceGeneration()
		}
	}
	if !allowed || next != expected {
		return networkAuthorityCorruptError("floating-IP ownership transition or generation is invalid", nil)
	}
	return nil
}
