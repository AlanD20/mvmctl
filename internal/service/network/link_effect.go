package network

import (
	"context"
	"errors"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type linkNamespaceRetainer interface {
	retainLinkNamespace(context.Context, NamespacePin) (*linkNamespaceLease, error)
}

type linkEffect struct {
	namespaces linkNamespaceRetainer
	route      routeNetlink
	host       func(context.Context) (*linkNamespaceLease, error)
}

func newLinkEffect(namespaces *namespaceEffect) *linkEffect {
	return &linkEffect{
		namespaces: namespaces,
		route:      newDirectRouteNetlink(),
		host: func(ctx context.Context) (*linkNamespaceLease, error) {
			return retainHostLinkNamespace(ctx, namespaces.deps, namespaces.policy)
		},
	}
}

func (effect *linkEffect) CreateLocalBridgeDown(
	ctx context.Context,
	namespace NamespacePin,
	spec localBridgeCreateSpec,
) (_ *linkIdentity, returnErr error) {
	if err := validateDerivedEffectLinkName(spec.name, "mvl"); err != nil {
		return nil, linkCreateError(spec.name, "validate local bridge identity", err, false)
	}
	lease, fd, identity, err := effect.retainVM(ctx, namespace)
	if err != nil {
		return nil, linkCreateError(spec.name, "retain local bridge namespace", err, false)
	}
	defer func() {
		returnErr = appendLinkCreateCloseError(spec.name, returnErr, lease.close(ctx))
	}()
	return effect.createSingleLink(
		ctx,
		fd,
		identity,
		spec.name,
		linkKindBridge,
		managedBridgeAlias(spec.name),
		0,
		func(mutationCtx context.Context) error {
			return effect.route.createBridgeDown(
				mutationCtx, fd, spec.name, managedBridgeAlias(spec.name),
			)
		},
	)
}

func (effect *linkEffect) CreateTAPDown(
	ctx context.Context,
	namespace NamespacePin,
	spec tapCreateSpec,
) (_ *linkIdentity, returnErr error) {
	if err := validateDerivedEffectLinkName(spec.name, "mvt"); err != nil {
		return nil, linkCreateError(spec.name, "validate TAP identity", err, false)
	}
	if spec.ownerUID == 0 {
		return nil, linkCreateError(spec.name, "validate TAP identity", errors.New(
			"TAP owner must be an authenticated non-root UID",
		), false)
	}
	lease, fd, identity, err := effect.retainVM(ctx, namespace)
	if err != nil {
		return nil, linkCreateError(spec.name, "retain TAP namespace", err, false)
	}
	defer func() {
		returnErr = appendLinkCreateCloseError(spec.name, returnErr, lease.close(ctx))
	}()
	authenticatedOwner, err := lease.owner()
	if err != nil {
		return nil, linkCreateError(spec.name, "authenticate TAP owner", err, false)
	}
	if authenticatedOwner != spec.ownerUID {
		return nil, linkCreateError(spec.name, "authenticate TAP owner", errors.New(
			"TAP owner does not match the retained namespace owner",
		), false)
	}
	alias := managedTAPAlias(spec.name, spec.ownerUID)
	return effect.createSingleLink(
		ctx,
		fd,
		identity,
		spec.name,
		linkKindTAP,
		alias,
		spec.ownerUID,
		func(mutationCtx context.Context) error {
			return effect.route.createTAPDown(mutationCtx, fd, spec.name, alias, spec.ownerUID)
		},
	)
}

func (effect *linkEffect) CreateVethPairDown(
	ctx context.Context,
	namespace NamespacePin,
	spec vethCreateSpec,
) (_ *vethPairIdentity, returnErr error) {
	if err := validateDerivedEffectLinkName(spec.hostName, "mvh"); err != nil {
		return nil, linkCreateError(spec.hostName, "validate host veth identity", err, false)
	}
	if err := validateDerivedEffectLinkName(spec.peerName, "mvn"); err != nil {
		return nil, linkCreateError(spec.peerName, "validate namespace veth identity", err, false)
	}
	vmLease, vmFD, vmIdentity, err := effect.retainVM(ctx, namespace)
	if err != nil {
		return nil, linkCreateError(spec.peerName, "retain veth peer namespace", err, false)
	}
	defer func() {
		returnErr = appendVethCreateCloseError(spec, returnErr, vmLease.close(ctx))
	}()
	hostLease, hostFD, hostIdentity, err := effect.retainHost(ctx)
	if err != nil {
		return nil, linkCreateError(spec.hostName, "retain host veth namespace", err, false)
	}
	defer func() {
		returnErr = appendVethCreateCloseError(spec, returnErr, hostLease.close(ctx))
	}()
	hostBefore, err := effect.route.inspectLink(ctx, hostFD, spec.hostName)
	if err != nil {
		return nil, linkCreateError(spec.hostName, "preflight host veth absence", err, false)
	}
	peerBefore, err := effect.route.inspectLink(ctx, vmFD, spec.peerName)
	if err != nil {
		return nil, linkCreateError(spec.peerName, "preflight namespace veth absence", err, false)
	}
	if hostBefore != nil || peerBefore != nil {
		return nil, linkCreateError(spec.hostName, "preflight veth pair absence", errors.New(
			"derived veth name already exists",
		), false)
	}
	hostAlias := managedVethAlias("veth-host", spec.hostName, spec.peerName)
	peerAlias := managedVethAlias("veth-peer", spec.peerName, spec.hostName)
	mutationErr := effect.route.createVethPairDown(
		ctx, hostFD, vmFD, spec.hostName, hostAlias, spec.peerName, peerAlias,
	)
	inspectionCtx := context.WithoutCancel(ctx)
	hostAfter, hostInspectErr := effect.route.inspectLink(inspectionCtx, hostFD, spec.hostName)
	peerAfter, peerInspectErr := effect.route.inspectLink(inspectionCtx, vmFD, spec.peerName)
	result := &vethPairIdentity{
		host: uncertainLinkIdentity(spec.hostName, hostIdentity),
		peer: uncertainLinkIdentity(spec.peerName, vmIdentity),
	}
	if hostAfter != nil {
		result.host.ifIndex = hostAfter.ifIndex
	}
	if peerAfter != nil {
		result.peer.ifIndex = peerAfter.ifIndex
	}
	if hostInspectErr != nil || peerInspectErr != nil {
		return result, linkCreateError(spec.hostName, "inspect created veth pair", errors.Join(
			mutationErr, hostInspectErr, peerInspectErr,
		), true)
	}
	if hostAfter == nil && peerAfter == nil {
		if mutationErr == nil {
			mutationErr = errors.New("acknowledged veth creation is absent")
		}
		return nil, linkCreateError(spec.hostName, "create veth pair down", mutationErr, false)
	}
	if hostAfter == nil || peerAfter == nil {
		return result, linkCreateError(spec.hostName, "inspect created veth pair", errors.Join(
			mutationErr, errors.New("created veth pair has only one surviving endpoint"),
		), false)
	}
	validationErr := errors.Join(
		validateManagedLink(*hostAfter, result.host, linkKindVeth, hostAlias, 0, 0),
		validateManagedLink(*peerAfter, result.peer, linkKindVeth, peerAlias, 0, 0),
	)
	if hostAfter.peer != peerAfter.ifIndex || peerAfter.peer != hostAfter.ifIndex {
		validationErr = errors.Join(validationErr, errors.New("veth endpoints are not reciprocal peers"))
	}
	if mutationErr != nil || validationErr != nil {
		return result, linkCreateError(spec.hostName, "create veth pair down", errors.Join(
			mutationErr, validationErr,
		), false)
	}
	return result, nil
}

func (effect *linkEffect) AttachNamespaceVethToLocalBridge(
	ctx context.Context,
	namespace NamespacePin,
	veth vethPairIdentity,
	bridge linkIdentity,
) (returnErr error) {
	if err := errors.Join(
		validateDerivedEffectLinkName(veth.host.name, "mvh"),
		validateDerivedEffectLinkName(veth.peer.name, "mvn"),
		validateDerivedEffectLinkName(bridge.name, "mvl"),
	); err != nil {
		return linkCreateError(veth.peer.name, "validate namespace veth attachment", err, false)
	}
	lease, fd, namespaceID, err := effect.retainVM(ctx, namespace)
	if err != nil {
		return linkCreateError(veth.peer.name, "retain namespace veth attachment", err, false)
	}
	defer func() {
		returnErr = appendLinkCreateCloseError(veth.peer.name, returnErr, lease.close(ctx))
	}()
	if veth.peer.namespace != namespaceID || bridge.namespace != namespaceID {
		return linkCreateError(veth.peer.name, "admit namespace veth attachment", errors.New(
			"namespace attachment identities do not match the retained namespace",
		), false)
	}
	return effect.attachLink(
		ctx,
		fd,
		veth.peer,
		linkKindVeth,
		managedVethAlias("veth-peer", veth.peer.name, veth.host.name),
		bridge,
		0,
	)
}

func (effect *linkEffect) AttachTAPToLocalBridge(
	ctx context.Context,
	namespace NamespacePin,
	tap linkIdentity,
	bridge linkIdentity,
) (returnErr error) {
	if err := errors.Join(
		validateDerivedEffectLinkName(tap.name, "mvt"),
		validateDerivedEffectLinkName(bridge.name, "mvl"),
	); err != nil {
		return linkCreateError(tap.name, "validate TAP attachment", err, false)
	}
	lease, fd, namespaceID, err := effect.retainVM(ctx, namespace)
	if err != nil {
		return linkCreateError(tap.name, "retain TAP attachment namespace", err, false)
	}
	defer func() {
		returnErr = appendLinkCreateCloseError(tap.name, returnErr, lease.close(ctx))
	}()
	if tap.namespace != namespaceID || bridge.namespace != namespaceID {
		return linkCreateError(tap.name, "admit TAP attachment", errors.New(
			"TAP attachment identities do not match the retained namespace",
		), false)
	}
	owner, err := lease.owner()
	if err != nil {
		return linkCreateError(tap.name, "authenticate TAP attachment owner", err, false)
	}
	return effect.attachLink(
		ctx, fd, tap, linkKindTAP, managedTAPAlias(tap.name, owner), bridge, owner,
	)
}

func (effect *linkEffect) AttachHostVethToManagedBridge(
	ctx context.Context,
	veth vethPairIdentity,
	bridge linkIdentity,
) (returnErr error) {
	if err := errors.Join(
		validateDerivedEffectLinkName(veth.host.name, "mvh"),
		validateDerivedEffectLinkName(veth.peer.name, "mvn"),
		validateDerivedEffectLinkName(bridge.name, "mvb"),
	); err != nil {
		return linkCreateError(veth.host.name, "validate host veth attachment", err, false)
	}
	lease, fd, namespaceID, err := effect.retainHost(ctx)
	if err != nil {
		return linkCreateError(veth.host.name, "retain host veth attachment", err, false)
	}
	defer func() {
		returnErr = appendLinkCreateCloseError(veth.host.name, returnErr, lease.close(ctx))
	}()
	if veth.host.namespace != namespaceID || bridge.namespace != namespaceID {
		return linkCreateError(veth.host.name, "admit host veth attachment", errors.New(
			"host attachment identities do not match the retained namespace",
		), false)
	}
	return effect.attachLink(
		ctx,
		fd,
		veth.host,
		linkKindVeth,
		managedVethAlias("veth-host", veth.host.name, veth.peer.name),
		bridge,
		0,
	)
}

func (effect *linkEffect) DeleteVethPair(
	ctx context.Context,
	namespace NamespacePin,
	veth vethPairIdentity,
) (returnErr error) {
	if err := errors.Join(
		validateDerivedEffectLinkName(veth.host.name, "mvh"),
		validateDerivedEffectLinkName(veth.peer.name, "mvn"),
	); err != nil {
		return linkRemoveError(veth.host.name, "validate veth cleanup identity", err)
	}
	vmLease, vmFD, vmIdentity, err := effect.retainVM(ctx, namespace)
	if err != nil {
		return linkRemoveError(veth.peer.name, "retain veth cleanup namespace", err)
	}
	defer func() {
		returnErr = appendLinkRemoveCloseError(veth.peer.name, returnErr, vmLease.close(ctx))
	}()
	hostLease, hostFD, hostIdentity, err := effect.retainHost(ctx)
	if err != nil {
		return linkRemoveError(veth.host.name, "retain host veth cleanup namespace", err)
	}
	defer func() {
		returnErr = appendLinkRemoveCloseError(veth.host.name, returnErr, hostLease.close(ctx))
	}()
	if veth.host.namespace != hostIdentity || veth.peer.namespace != vmIdentity {
		return linkRemoveError(veth.host.name, "admit veth pair cleanup", errors.New(
			"veth cleanup identity does not match retained namespaces",
		))
	}
	host, err := effect.route.inspectLink(ctx, hostFD, veth.host.name)
	if err != nil {
		return linkRemoveError(veth.host.name, "inspect host veth for cleanup", err)
	}
	peer, err := effect.route.inspectLink(ctx, vmFD, veth.peer.name)
	if err != nil {
		return linkRemoveError(veth.peer.name, "inspect namespace veth for cleanup", err)
	}
	if host == nil && peer == nil {
		return nil
	}
	if (host == nil && veth.host.ifIndex > 0) || (host != nil && veth.host.ifIndex <= 0) ||
		(peer == nil && veth.peer.ifIndex > 0) || (peer != nil && veth.peer.ifIndex <= 0) {
		return linkRemoveError(veth.host.name, "admit veth pair cleanup", errors.New(
			"veth endpoint survival does not match its rollback identity",
		))
	}
	var validationErr error
	if host != nil {
		validationErr = errors.Join(validationErr, validateManagedLink(
			*host, veth.host, linkKindVeth,
			managedVethAlias("veth-host", veth.host.name, veth.peer.name), host.master, 0,
		))
	}
	if peer != nil {
		validationErr = errors.Join(validationErr, validateManagedLink(
			*peer, veth.peer, linkKindVeth,
			managedVethAlias("veth-peer", veth.peer.name, veth.host.name), peer.master, 0,
		))
	}
	if validationErr != nil {
		return linkRemoveError(veth.host.name, "admit veth pair cleanup", validationErr)
	}
	if host != nil && peer != nil && (host.peer != peer.ifIndex || peer.peer != host.ifIndex) {
		return linkRemoveError(veth.host.name, "admit veth pair cleanup", errors.New(
			"veth cleanup endpoints are not reciprocal peers",
		))
	}
	if host != nil {
		if err := effect.validateManagedMaster(ctx, hostFD, *host, "mvb"); err != nil {
			return linkRemoveError(veth.host.name, "admit host veth master", err)
		}
	}
	if peer != nil {
		if err := effect.validateManagedMaster(ctx, vmFD, *peer, "mvl"); err != nil {
			return linkRemoveError(veth.peer.name, "admit namespace veth master", err)
		}
	}
	deleteFD := hostFD
	deleteIndex := veth.host.ifIndex
	if host == nil {
		deleteFD = vmFD
		deleteIndex = veth.peer.ifIndex
	}
	deleteErr := effect.route.deleteLink(ctx, deleteFD, deleteIndex)
	inspectionCtx := context.WithoutCancel(ctx)
	hostAfter, hostInspectErr := effect.route.inspectLink(inspectionCtx, hostFD, veth.host.name)
	peerAfter, peerInspectErr := effect.route.inspectLink(inspectionCtx, vmFD, veth.peer.name)
	if hostInspectErr != nil || peerInspectErr != nil {
		return linkRemoveError(veth.host.name, "prove veth pair absence", errors.Join(
			deleteErr, hostInspectErr, peerInspectErr,
		))
	}
	if hostAfter == nil && peerAfter == nil {
		return nil
	}
	return linkRemoveError(veth.host.name, "delete exact veth pair", errors.Join(
		deleteErr, errors.New("veth pair remains after deletion"),
	))
}

func (effect *linkEffect) DeleteTAP(
	ctx context.Context,
	namespace NamespacePin,
	tap linkIdentity,
) (returnErr error) {
	if err := validateDerivedEffectLinkName(tap.name, "mvt"); err != nil {
		return linkRemoveError(tap.name, "validate TAP cleanup identity", err)
	}
	lease, fd, namespaceID, err := effect.retainVM(ctx, namespace)
	if err != nil {
		return linkRemoveError(tap.name, "retain TAP cleanup namespace", err)
	}
	defer func() {
		returnErr = appendLinkRemoveCloseError(tap.name, returnErr, lease.close(ctx))
	}()
	if tap.namespace != namespaceID || tap.ifIndex <= 0 {
		return linkRemoveError(tap.name, "admit TAP cleanup", errors.New(
			"TAP cleanup identity does not match the retained namespace",
		))
	}
	owner, err := lease.owner()
	if err != nil {
		return linkRemoveError(tap.name, "authenticate TAP cleanup owner", err)
	}
	observation, err := effect.route.inspectLink(ctx, fd, tap.name)
	if err != nil {
		return linkRemoveError(tap.name, "inspect TAP for cleanup", err)
	}
	if observation == nil {
		return nil
	}
	if err := validateManagedLink(
		*observation, tap, linkKindTAP, managedTAPAlias(tap.name, owner), observation.master, owner,
	); err != nil {
		return linkRemoveError(tap.name, "admit TAP cleanup", err)
	}
	if err := effect.validateManagedMaster(ctx, fd, *observation, "mvl"); err != nil {
		return linkRemoveError(tap.name, "admit TAP master", err)
	}
	deleteErr := effect.route.deleteLink(ctx, fd, observation.ifIndex)
	after, inspectErr := effect.route.inspectLink(context.WithoutCancel(ctx), fd, tap.name)
	if inspectErr != nil {
		return linkRemoveError(tap.name, "prove TAP absence", errors.Join(deleteErr, inspectErr))
	}
	if after == nil {
		return nil
	}
	return linkRemoveError(tap.name, "delete exact TAP", errors.Join(
		deleteErr, errors.New("TAP remains after deletion"),
	))
}

func (effect *linkEffect) DeleteLocalBridge(
	ctx context.Context,
	namespace NamespacePin,
	bridge linkIdentity,
) (returnErr error) {
	if err := validateDerivedEffectLinkName(bridge.name, "mvl"); err != nil {
		return linkRemoveError(bridge.name, "validate local bridge cleanup identity", err)
	}
	lease, fd, namespaceID, err := effect.retainVM(ctx, namespace)
	if err != nil {
		return linkRemoveError(bridge.name, "retain local bridge cleanup namespace", err)
	}
	defer func() {
		returnErr = appendLinkRemoveCloseError(bridge.name, returnErr, lease.close(ctx))
	}()
	if bridge.namespace != namespaceID || bridge.ifIndex <= 0 {
		return linkRemoveError(bridge.name, "admit local bridge cleanup", errors.New(
			"local bridge cleanup identity does not match the retained namespace",
		))
	}
	observation, err := effect.route.inspectLink(ctx, fd, bridge.name)
	if err != nil {
		return linkRemoveError(bridge.name, "inspect local bridge for cleanup", err)
	}
	if observation == nil {
		return nil
	}
	if err := validateManagedLink(
		*observation, bridge, linkKindBridge, managedBridgeAlias(bridge.name), 0, 0,
	); err != nil {
		return linkRemoveError(bridge.name, "admit local bridge cleanup", err)
	}
	links, err := effect.route.listLinks(ctx, fd)
	if err != nil {
		return linkRemoveError(bridge.name, "inspect local bridge children", err)
	}
	for _, link := range links {
		if link.master == bridge.ifIndex {
			return linkRemoveError(bridge.name, "admit local bridge cleanup", errors.New(
				"local bridge still has child links",
			))
		}
	}
	deleteErr := effect.route.deleteLink(ctx, fd, observation.ifIndex)
	after, inspectErr := effect.route.inspectLink(context.WithoutCancel(ctx), fd, bridge.name)
	if inspectErr != nil {
		return linkRemoveError(bridge.name, "prove local bridge absence", errors.Join(deleteErr, inspectErr))
	}
	if after == nil {
		return nil
	}
	return linkRemoveError(bridge.name, "delete exact local bridge", errors.Join(
		deleteErr, errors.New("local bridge remains after deletion"),
	))
}

func (effect *linkEffect) createSingleLink(
	ctx context.Context,
	namespaceFD int,
	namespaceID namespaceIdentity,
	name linkName,
	kind string,
	alias string,
	owner OwnerUID,
	mutate func(context.Context) error,
) (*linkIdentity, error) {
	before, err := effect.route.inspectLink(ctx, namespaceFD, name)
	if err != nil {
		return nil, linkCreateError(name, "preflight derived link absence", err, false)
	}
	if before != nil {
		return nil, linkCreateError(name, "preflight derived link absence", errors.New(
			"derived link name already exists",
		), false)
	}
	mutationErr := mutate(ctx)
	after, inspectErr := effect.route.inspectLink(context.WithoutCancel(ctx), namespaceFD, name)
	result := uncertainLinkIdentity(name, namespaceID)
	if after != nil {
		result.ifIndex = after.ifIndex
	}
	if inspectErr != nil {
		return &result, linkCreateError(name, "inspect created link", errors.Join(
			mutationErr, inspectErr,
		), true)
	}
	if after == nil {
		if mutationErr == nil {
			mutationErr = errors.New("acknowledged link creation is absent")
		}
		return nil, linkCreateError(name, "create link down", mutationErr, false)
	}
	validationErr := validateManagedLink(*after, result, kind, alias, 0, owner)
	if mutationErr != nil || validationErr != nil {
		return &result, linkCreateError(name, "create link down", errors.Join(
			mutationErr, validationErr,
		), false)
	}
	return &result, nil
}

func (effect *linkEffect) attachLink(
	ctx context.Context,
	namespaceFD int,
	link linkIdentity,
	kind string,
	alias string,
	bridge linkIdentity,
	owner OwnerUID,
) error {
	linkBefore, err := effect.route.inspectLink(ctx, namespaceFD, link.name)
	if err != nil {
		return linkCreateError(link.name, "inspect attachment source", err, false)
	}
	bridgeBefore, err := effect.route.inspectLink(ctx, namespaceFD, bridge.name)
	if err != nil {
		return linkCreateError(bridge.name, "inspect attachment bridge", err, false)
	}
	if linkBefore == nil || bridgeBefore == nil {
		return linkCreateError(link.name, "admit link attachment", errors.New(
			"attachment endpoint is absent",
		), false)
	}
	if err := errors.Join(
		validateManagedLink(*linkBefore, link, kind, alias, linkBefore.master, owner),
		validateManagedLink(
			*bridgeBefore, bridge, linkKindBridge, managedBridgeAlias(bridge.name), 0, 0,
		),
	); err != nil {
		return linkCreateError(link.name, "admit link attachment", err, false)
	}
	if linkBefore.master == bridge.ifIndex {
		return nil
	}
	if linkBefore.master != 0 {
		return linkCreateError(link.name, "admit link attachment", errors.New(
			"attachment source already has a different master",
		), false)
	}
	mutationErr := effect.route.setLinkMaster(ctx, namespaceFD, link.ifIndex, bridge.ifIndex)
	after, inspectErr := effect.route.inspectLink(context.WithoutCancel(ctx), namespaceFD, link.name)
	if inspectErr != nil {
		return linkCreateError(link.name, "inspect attached link", errors.Join(
			mutationErr, inspectErr,
		), true)
	}
	if after == nil {
		return linkCreateError(link.name, "inspect attached link", errors.Join(
			mutationErr, errors.New("attachment source disappeared"),
		), false)
	}
	validationErr := validateManagedLink(*after, link, kind, alias, bridge.ifIndex, owner)
	if mutationErr != nil || validationErr != nil {
		return linkCreateError(link.name, "attach exact link to bridge", errors.Join(
			mutationErr, validationErr,
		), false)
	}
	return nil
}

func (effect *linkEffect) validateManagedMaster(
	ctx context.Context,
	namespaceFD int,
	link routeLinkObservation,
	wantPrefix string,
) error {
	if link.master == 0 {
		return nil
	}
	links, err := effect.route.listLinks(ctx, namespaceFD)
	if err != nil {
		return err
	}
	for _, candidate := range links {
		if candidate.ifIndex != link.master {
			continue
		}
		if err := validateDerivedEffectLinkName(candidate.name, wantPrefix); err != nil {
			return errors.New("link master is not a derived managed bridge")
		}
		identity := linkIdentity{name: candidate.name, ifIndex: candidate.ifIndex}
		return validateManagedLink(
			candidate, identity, linkKindBridge, managedBridgeAlias(candidate.name), 0, 0,
		)
	}
	return errors.New("link master is absent from its namespace")
}

func (effect *linkEffect) retainVM(
	ctx context.Context,
	namespace NamespacePin,
) (*linkNamespaceLease, int, namespaceIdentity, error) {
	if effect == nil || effect.namespaces == nil || effect.route == nil {
		return nil, -1, namespaceIdentity{}, errors.New("link effect dependencies are incomplete")
	}
	lease, err := effect.namespaces.retainLinkNamespace(ctx, namespace)
	if err != nil {
		return nil, -1, namespaceIdentity{}, err
	}
	fd, identity, err := lease.descriptor()
	if err != nil {
		return lease, -1, namespaceIdentity{}, errors.Join(err, lease.close(context.WithoutCancel(ctx)))
	}
	return lease, fd, identity, nil
}

func (effect *linkEffect) retainHost(
	ctx context.Context,
) (*linkNamespaceLease, int, namespaceIdentity, error) {
	if effect == nil || effect.host == nil || effect.route == nil {
		return nil, -1, namespaceIdentity{}, errors.New("link effect dependencies are incomplete")
	}
	lease, err := effect.host(ctx)
	if err != nil {
		return nil, -1, namespaceIdentity{}, err
	}
	fd, identity, err := lease.descriptor()
	if err != nil {
		return lease, -1, namespaceIdentity{}, errors.Join(err, lease.close(context.WithoutCancel(ctx)))
	}
	return lease, fd, identity, nil
}

func validateManagedLink(
	observation routeLinkObservation,
	want linkIdentity,
	kind string,
	alias string,
	master int,
	owner OwnerUID,
) error {
	switch {
	case observation.name != want.name:
		return errors.New("link name changed")
	case want.ifIndex <= 0 || observation.ifIndex != want.ifIndex:
		return errors.New("link ifindex changed")
	case observation.kind != kind:
		return errors.New("link kind changed")
	case observation.alias != alias:
		return errors.New("link ownership alias changed")
	case observation.flags&unix.IFF_UP != 0:
		return errors.New("managed link is unexpectedly up")
	case observation.master != master:
		return errors.New("link master changed")
	}
	if kind == linkKindTAP && (owner == 0 || observation.tunOwner != uint32(owner) ||
		observation.tunType != unix.IFF_TAP || observation.tunPI != 0 || observation.tunPersist != 1) {
		return errors.New("TAP type, no-PI, persistence, or authenticated owner changed")
	}
	return nil
}

func validateDerivedEffectLinkName(name linkName, prefix string) error {
	value := name.String()
	if len(prefix) != 3 || len(value) != linuxInterfaceNameMax || !strings.HasPrefix(value, prefix) {
		return errors.New("link name is not an exact derived identity")
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return errors.New("link name is not lowercase hexadecimal after its derived prefix")
		}
	}
	return nil
}

func uncertainLinkIdentity(name linkName, namespace namespaceIdentity) linkIdentity {
	return linkIdentity{name: name, ifIndex: -1, namespace: namespace}
}

func linkCreateError(name linkName, stage string, cause error, uncertain bool) *errs.DomainError {
	if cause == nil {
		cause = errors.New("failed to " + stage)
	}
	details := map[string]any{"mutation_outcome_unknown": uncertain}
	return errs.WrapMsg(
		errs.CodeNetworkCreateFailed,
		"failed to "+stage,
		cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity(name.String()),
		errs.WithDetails(details),
	)
}

func linkRemoveError(name linkName, stage string, cause error) *errs.DomainError {
	if cause == nil {
		cause = errors.New("failed to " + stage)
	}
	return errs.WrapMsg(
		errs.CodeNetworkRemoveFailed,
		"failed to "+stage,
		cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity(name.String()),
	)
}

func appendLinkCreateCloseError(name linkName, primary, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	return linkCreateError(name, "close retained link namespace", errors.Join(primary, closeErr), true)
}

func appendVethCreateCloseError(spec vethCreateSpec, primary, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	return linkCreateError(spec.hostName, "close retained veth namespace", errors.Join(primary, closeErr), true)
}

func appendLinkRemoveCloseError(name linkName, primary, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	return linkRemoveError(name, "close retained link namespace", errors.Join(primary, closeErr))
}
