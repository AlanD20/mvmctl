package firewall

import (
	"context"
	"errors"
	"net/netip"

	"golang.org/x/sys/unix"
)

// InvalidateFloatingIPFlows removes exact IPv4 conntrack entries only after the resource's packet
// authority is absent. The retained global mutation lock prevents association or renewal until return.
// An error, including an unknown delete outcome, forbids handover; callers must reconcile and retry.
func (lease *MutationLease) InvalidateFloatingIPFlows(
	ctx context.Context, resourceID string, expectedGeneration uint64,
) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("floating-IP flow mutation authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, kernelApplicationSocketTimeout)
	defer cancel()
	observed, err := state.network.kernel.observeGeneration(ctx)
	if err != nil {
		return err
	}
	address, err := state.floatingIPFlowAddress(resourceID, expectedGeneration, observed)
	if err != nil {
		return err
	}
	transport, err := state.network.kernel.newConntrackTransport(ctx)
	if err != nil {
		return preserveGenerationAuthorityError("open floating-IP conntrack transport", err)
	}
	primary := invalidateConntrackFlows(ctx, transport, address)
	return transport.close(context.WithoutCancel(ctx), primary)
}

// CALLER MUST HOLD: state.mu and a ready mutation lease. Authority is always re-derived from admitted
// intent and exact generation readback; neither callers nor cached topology supply a deletion address.
func (state *mutationLeaseState) floatingIPFlowAddress(
	resourceID string, expectedGeneration uint64, observed kernelObservation,
) (netip.Addr, error) {
	if err := compareKernelObservation(observed, state.compiled); err != nil {
		return netip.Addr{}, err
	}
	for _, resource := range state.intent.FloatingIPs {
		if resource.ID != resourceID {
			continue
		}
		if resource.OwnerUID != state.binding.OwnerUID ||
			resource.NetworkID != state.binding.NetworkID ||
			resource.Generation != expectedGeneration ||
			expectedGeneration == 0 {
			return netip.Addr{}, generationAuthorityError("floating-IP flow resource scope or generation differs", nil, nil)
		}
		// Reject every observed element for this address, even at remaining=0: absence alone proves expiry.
		for _, table := range observed.tables {
			for _, element := range table.secondaryElements {
				_, address, err := decodeSecondaryLeaseKey(element.key[:])
				if err != nil {
					return netip.Addr{}, err
				}
				if address == resource.IPv4 {
					return netip.Addr{}, generationAuthorityError("floating-IP packet authority is still present", nil, nil)
				}
			}
		}
		return resource.IPv4, nil
	}
	return netip.Addr{}, generationAuthorityError("floating-IP flow resource is absent from admitted intent", nil, nil)
}

func invalidateConntrackFlows(ctx context.Context, transport *kernelObservationTransport, address netip.Addr) error {
	flows, err := collectConntrackFlows(ctx, transport)
	if err != nil {
		return err
	}
	for _, flow := range flows {
		if !flow.matches(address) {
			continue
		}
		request, err := flow.deleteRequest()
		if err != nil {
			return err
		}
		if _, err := conntrackExchange(ctx, transport, request); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	// A new matching entry (including one racing the initial dump) prevents handover. Do not loop
	// indefinitely or broaden deletion; the caller can retry under the durable fenced transition.
	remaining, err := collectConntrackFlows(ctx, transport)
	if err != nil {
		return err
	}
	for _, flow := range remaining {
		if flow.matches(address) {
			return generationAuthorityError("floating-IP conntrack entries remain after invalidation", nil, nil)
		}
	}
	return nil
}
