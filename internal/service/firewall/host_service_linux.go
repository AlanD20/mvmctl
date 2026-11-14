package firewall

import (
	"context"
	"slices"
)

// RemoveHostService revokes an exact root-stored service under its owned network lock.
// Missing intent is acknowledged only after fresh verification of the complete generation.
func (lease *MutationLease) RemoveHostService(ctx context.Context, service HostServiceIntent) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall host service lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	if err := state.ownedNetwork(service.SourceNetworkID); err != nil {
		return err
	}
	target := cloneIntent(state.intent)
	for _, existing := range target.HostServices {
		if existing.ID == service.ID && existing != service {
			return generationAuthorityError("host service revocation intent differs", nil, nil)
		}
	}
	target.HostServices = slices.DeleteFunc(target.HostServices, func(existing HostServiceIntent) bool {
		return existing.ID == service.ID
	})
	return state.applyIntent(ctx, target)
}
