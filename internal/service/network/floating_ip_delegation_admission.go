package network

// floatingIPGuestPrincipal contains root-derived current VM facts, never a wire principal.
// Only a future coordinator retaining Jailer admission may construct and use this value;
// this comparison helper does not itself prove a socket peer or retain mutation locks.
type floatingIPGuestPrincipal struct {
	ownerUID         uint32
	networkID        string
	vmID             string
	bootID           string
	launchGeneration uint64
	executionUID     uint32
}

func (principal floatingIPGuestPrincipal) matchesResource(resource floatingIPRecord) bool {
	return principal.ownerUID == resource.OwnerUID && principal.networkID == resource.NetworkID &&
		vmIDPattern.MatchString(principal.vmID) && bootIDPattern.MatchString(principal.bootID) &&
		principal.launchGeneration > 0 &&
		principal.executionUID != 0 && principal.executionUID != principal.ownerUID && principal.executionUID != ^uint32(0)
}

// admitFloatingIPDelegation compares current root facts under the same network/global mutation locks as revocation.
// Callers must first admit the live principal and root reservation; records or directory access alone grant nothing.
func admitFloatingIPDelegation(
	principal floatingIPGuestPrincipal, resource floatingIPRecord,
	grant floatingIPDelegationRecord, method floatingIPDelegationMethod,
) error {
	if err := validateFloatingIPRecord(resource); err != nil {
		return err
	}
	if err := validateFloatingIPDelegationRecord(grant); err != nil {
		return err
	}
	if resource.Retired || !principal.matchesResource(resource) || grant.State != floatingIPDelegationActive ||
		grant.OwnerUID != principal.ownerUID || grant.NetworkID != principal.networkID || grant.ResourceID != resource.ID ||
		grant.VMID != principal.vmID || grant.BootID != principal.bootID ||
		grant.LaunchGeneration != principal.launchGeneration ||
		grant.ExecutionUID != principal.executionUID || grant.Method != method {
		return networkAuthorityCorruptError("delegation does not authorize this current guest operation", nil)
	}
	return nil
}

// floatingIPOwnerSustainedByDelegation selects only the exact grant revision that sustained ownership.
func floatingIPOwnerSustainedByDelegation(owner floatingIPOwner, grant floatingIPDelegationRecord) bool {
	// Validation proves the closed active(1)->revoking(2)->revoked(3) chain.
	// Every phase retires the same immutable active revision, rather than guessing
	// a predecessor from an arbitrary revision counter.
	if validateFloatingIPDelegationRecord(grant) != nil {
		return false
	}
	const sustainingRevision = uint64(1)
	return owner.Origin == floatingIPDelegatedOrigin && owner.GrantID == grant.ID &&
		owner.GrantRevision == sustainingRevision &&
		owner.VMID == grant.VMID && owner.BootID == grant.BootID && owner.LaunchGeneration == grant.LaunchGeneration &&
		owner.ExecutionUID == grant.ExecutionUID
}

// floatingIPRecordSustainedByDelegation includes both sides of interrupted transitions for later fencing recovery.
func floatingIPRecordSustainedByDelegation(resource floatingIPRecord, grant floatingIPDelegationRecord) bool {
	return resource.OwnerUID == grant.OwnerUID && resource.NetworkID == grant.NetworkID &&
		resource.ID == grant.ResourceID &&
		(floatingIPOwnerSustainedByDelegation(resource.Owner, grant) ||
			floatingIPOwnerSustainedByDelegation(resource.BeforeOwner, grant) ||
			floatingIPOwnerSustainedByDelegation(resource.TargetOwner, grant))
}
