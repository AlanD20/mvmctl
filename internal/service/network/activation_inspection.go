package network

import (
	"context"
	"errors"
)

func (lease *VMTopologyLease) verifyActivationLinks(ctx context.Context) (returnErr error) {
	effect := lease.effects.link.(*linkEffect)
	retained, err := effect.retainL3Namespaces(ctx, lease.preparation.pin)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, retained.close(context.WithoutCancel(ctx)))
	}()
	topology := lease.preparation.topology
	links, err := effect.inspectExactTopologyLinks(ctx, retained, topology, false)
	if err != nil {
		return err
	}
	progress := lease.journal.record.Progress
	if linkIsUp(links.loopback) != progress.LoopbackUp || linkIsUp(links.localBridge) != progress.BridgeUp ||
		linkIsUp(links.tap) != progress.TAPUp || linkIsUp(links.namespaceVeth) != progress.PeerUp ||
		linkIsUp(links.hostVeth) != progress.HostUp || !linkIsUp(links.managedBridge) {
		return networkAuthorityCorruptError("observed link state differs from exact activation progress", nil)
	}
	if err := effect.verifyCanonicalLoopbackAuthority(ctx, retained.vmFD, links.loopback, progress.LoopbackUp); err != nil {
		return err
	}
	for _, role := range []linkStateRole{linkStateLocalBridge, linkStateTAP, linkStateNamespaceVeth, linkStateHostVeth} {
		fd, link, err := exactLinkStateTarget(retained, links, role)
		if err != nil {
			return err
		}
		addresses, routes, err := effect.inspectIPv4Authority(ctx, fd, link.ifIndex)
		if err != nil {
			return err
		}
		if len(addresses) != 0 || len(routes) != 0 {
			return networkAuthorityCorruptError("activated fabric link acquired unexpected IPv4 authority", nil)
		}
	}
	return inspectManagedBridgeL3(ctx, effect.route, retained.hostFD, lease.network.record)
}
