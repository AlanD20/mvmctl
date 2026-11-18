package firewall

import (
	"time"

	"github.com/google/nftables"
)

func compareSecondaryLeaseElements(elements []secondaryLeaseElement, expected compiledGeneration) error {
	allowed := make(map[[secondaryLeaseKeyBytes]byte]time.Duration, len(expected.bridge.secondaryOwners))
	for _, resource := range expected.floatingIPs {
		if resource.ownerVMID == "" {
			continue
		}
		key, found := expected.bridge.secondaryOwners[resource.id]
		if !found {
			return invalidKernelObservation("compiled floating-IP owner has no exact lease key")
		}
		allowed[key] = resource.leaseDuration
	}
	seen := make(map[[secondaryLeaseKeyBytes]byte]struct{}, len(elements))
	for _, element := range elements {
		if _, duplicate := seen[element.key]; duplicate {
			return invalidKernelObservation("duplicate observed secondary lease key")
		}
		seen[element.key] = struct{}{}
		duration, found := allowed[element.key]
		if !found || element.timeout != duration || element.remaining < 0 || element.remaining > duration {
			return invalidKernelObservation("secondary lease key or timeout differs from ownership authority")
		}
	}
	// Kernel expiry can remove permission during collection. Absence is safe; it never authorizes renewal.
	return nil
}

func secondaryLeaseRevocations(before, target compiledGeneration, observed kernelObservation) []nftables.SetElement {
	if before.bridge.secondarySet == nil || target.bridge.secondarySet == nil || len(observed.tables) == 0 {
		return nil
	}
	current := make(map[string]floatingIPIntent, len(target.floatingIPs))
	for _, resource := range target.floatingIPs {
		current[resource.id] = resource
	}
	live := make(map[[secondaryLeaseKeyBytes]byte]struct{}, len(observed.tables[0].secondaryElements))
	for _, element := range observed.tables[0].secondaryElements {
		live[element.key] = struct{}{}
	}
	var revoked []nftables.SetElement
	for _, resource := range sortedCopy(before.floatingIPs, func(value floatingIPIntent) string { return value.id }) {
		if current[resource.id] == resource {
			continue
		}
		key, owned := before.bridge.secondaryOwners[resource.id]
		if _, present := live[key]; owned && present {
			revoked = append(revoked, nftables.SetElement{Key: key[:]})
		}
	}
	return revoked
}
