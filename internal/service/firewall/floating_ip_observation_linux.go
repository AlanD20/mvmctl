package firewall

import (
	"context"
	"time"
)

// FloatingIPLeaseObservation is a fresh root binding and bounded kernel lifetime, never a raw set key.
// Present may include an element with zero remaining time; only absence proves revocation.
type FloatingIPLeaseObservation struct {
	Binding   FloatingIPBinding
	Present   bool
	Remaining time.Duration
}

// ObserveFloatingIPLease verifies the complete current graph and returns one exact root resource.
// Unlike mutation admission, observation never reloads uncertain intent or writes recovery state.
func (lease *MutationLease) ObserveFloatingIPLease(
	ctx context.Context, resourceID string, generation uint64,
) (FloatingIPLeaseObservation, error) {
	if lease == nil || lease.state == nil {
		return FloatingIPLeaseObservation{}, generationAuthorityError(
			"floating-IP observation authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.uncertain {
		return FloatingIPLeaseObservation{}, generationAuthorityError("floating-IP observation requires recovery", nil, nil)
	}
	if err := state.ready(ctx); err != nil {
		return FloatingIPLeaseObservation{}, err
	}
	observed, err := state.network.kernel.observeGeneration(ctx)
	if err != nil {
		return FloatingIPLeaseObservation{}, err
	}
	return state.observeFloatingIPLease(resourceID, generation, observed)
}

func (state *mutationLeaseState) observeFloatingIPLease(
	resourceID string, generation uint64, observed kernelObservation,
) (FloatingIPLeaseObservation, error) {
	if err := compareKernelObservation(observed, state.compiled); err != nil {
		return FloatingIPLeaseObservation{}, err
	}
	for _, binding := range state.intent.FloatingIPs {
		if binding.ID != resourceID {
			continue
		}
		if binding.OwnerUID != state.binding.OwnerUID || binding.NetworkID != state.binding.NetworkID ||
			binding.Generation != generation || generation == 0 {
			return FloatingIPLeaseObservation{}, generationAuthorityError(
				"floating-IP observation scope or generation differs", nil, nil)
		}
		result := FloatingIPLeaseObservation{Binding: binding}
		if binding.OwnerVMID == "" {
			return result, nil
		}
		key, found := state.compiled.bridge.secondaryOwners[resourceID]
		if !found {
			return FloatingIPLeaseObservation{}, generationAuthorityError("floating-IP owner has no compiled endpoint", nil, nil)
		}
		for _, element := range observed.tables[0].secondaryElements {
			if element.key == key {
				result.Present = true
				result.Remaining = element.remaining
				break
			}
		}
		return result, nil
	}
	return FloatingIPLeaseObservation{}, generationAuthorityError("floating-IP observation resource is missing", nil, nil)
}
